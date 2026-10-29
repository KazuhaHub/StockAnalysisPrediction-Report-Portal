package app

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// exportBatchRows is the keyset batch size (ADR 0045): small enough that no read holds the SQLite
// connection for long, large enough that a million-row log is a thousand queries.
const exportBatchRows = 1000

// exportBatchBytes caps one batch's buffered text. Report bodies can be large, so a batch ends early
// once it has read this much; the next batch resumes from the last row.
const exportBatchBytes = 4 << 20

// exportPlan is a source plus a compiled filter: everything needed to count, preview or stream.
type exportPlan struct {
	s     *Server
	src   *exportSource
	where string // always parenthesised
	args  []any

	// A tree holding a derived (generator version) condition cannot be answered by SQL alone. The
	// WHERE is then a superset; flags carries the exact value of every other leaf per row, and post
	// evaluates the whole tree in Go.
	flagSel  []string
	flagArgs []any
	post     func(raw []string, flagBase int) bool

	// Latest prices (forecasts source). prices is filled by prepare (a below-entry condition needs
	// every candidate priced before it can count) or per batch by prefetch (price columns only).
	latestMode  bool // the tree holds a latest_per_symbol condition
	candWhere   string
	candArgs    []any
	newest      map[string]exportNewest
	belowMode   bool // the tree holds a below_entry condition: prices cover the whole candidate set
	needPrices  bool // a latest-price column was requested
	prepared    bool
	prices      map[string]exportPrice
	priceLookup int // upstream chunks requested so far (diagnostic)
}

// newExportPlan validates and compiles a filter against a source.
func (s *Server) newExportPlan(src *exportSource, filter exportNode) (*exportPlan, error) {
	src = s.exportView(src)
	filter = normalizeExportFilter(filter)
	if err := validateExportFilter(src, filter); err != nil {
		return nil, err
	}
	whereSQL, args, c, tree, err := s.compileExportFilter(src, filter, false)
	if err != nil {
		return nil, err
	}
	p := &exportPlan{s: s, src: src, where: whereSQL, args: args}
	if !c.neutral {
		for _, l := range c.leaves {
			if l.latest != nil {
				// The candidate set: the same tree with the derived conditions taken as true.
				candSQL, candArgs, _, _, err := s.compileExportFilter(src, filter, true)
				if err != nil {
					return nil, err
				}
				p.candWhere, p.candArgs = candSQL, candArgs
				break
			}
		}
	}
	hasGV := false
	for _, l := range c.leaves {
		if l.gv != nil || l.below != nil || l.latest != nil {
			hasGV = true
		}
		if l.below != nil {
			p.belowMode = true
		}
		if l.latest != nil {
			p.latestMode = true
		}
	}
	if hasGV {
		flagAt := map[int]int{}
		for i, l := range c.leaves {
			if l.gv == nil && l.below == nil && l.latest == nil {
				flagAt[i] = len(p.flagSel)
				p.flagSel = append(p.flagSel, "CASE WHEN ("+l.sql+") THEN 1 ELSE 0 END")
				p.flagArgs = append(p.flagArgs, l.args...)
			}
		}
		leaves := c.leaves
		p.post = func(raw []string, flagBase int) bool {
			gv := ""
			return tree.eval(func(i int) bool {
				if g := leaves[i].gv; g != nil {
					if gv == "" {
						gv = src.gvValue(raw)
					}
					return g.match(gv)
				}
				if b := leaves[i].below; b != nil {
					return p.belowMatch(raw) == b.want
				}
				if l := leaves[i].latest; l != nil {
					return p.isLatest(raw) == l.want
				}
				return raw[flagBase+flagAt[i]] == "1"
			})
		}
	}
	return p, nil
}

func (p *exportPlan) fullWhere() string {
	w := p.where
	if p.src.base != "" {
		w = "(" + p.src.base + ") AND " + w
	}
	return w
}

type exportCursor struct {
	started bool
	k1, id  string
}

// selectList returns the SELECT expressions and where the optional pieces land in a raw row.
func (p *exportPlan) selectList(withBody bool) (exprs []string, flagBase int) {
	exprs = append(exprs, p.src.sel...)
	if withBody && p.src.bodyExpr != "" {
		exprs = append(exprs, p.src.bodyExpr)
	}
	flagBase = len(exprs)
	exprs = append(exprs, p.flagSel...)
	if p.src.k1 != "" {
		exprs = append(exprs, p.src.k1)
	}
	exprs = append(exprs, p.src.id)
	return exprs, flagBase
}

// batchQuery builds one keyset batch (or, with offset >= 0 and no cursor, one preview page).
func (p *exportPlan) batchQuery(cur exportCursor, withBody bool, limit, offset int) (string, []any, int) {
	exprs, flagBase := p.selectList(withBody)
	args := append([]any(nil), p.flagArgs...)
	args = append(args, p.args...)
	var b strings.Builder
	b.WriteString("SELECT " + strings.Join(exprs, ",") + " FROM " + p.src.from + " WHERE " + p.fullWhere())
	if cur.started {
		src := p.src
		idv := exportKeyArg(cur.id, src.idInt)
		if src.k1 != "" {
			k := exportKeyArg(cur.k1, src.k1Int)
			b.WriteString(" AND (" + src.k1 + " < ? OR (" + src.k1 + " = ? AND " + src.id + " < ?))")
			args = append(args, k, k, idv)
		} else {
			b.WriteString(" AND " + src.id + " < ?")
			args = append(args, idv)
		}
	}
	if p.src.k1 != "" {
		b.WriteString(" ORDER BY " + p.src.k1 + " DESC, " + p.src.id + " DESC")
	} else {
		b.WriteString(" ORDER BY " + p.src.id + " DESC")
	}
	b.WriteString(" LIMIT " + strconv.Itoa(limit))
	if offset > 0 {
		b.WriteString(" OFFSET " + strconv.Itoa(offset))
	}
	return b.String(), args, flagBase
}

func exportKeyArg(v string, isInt bool) any {
	if isInt {
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return v
}

// count answers the filter's row count. Without a derived condition it is one COUNT; with one the
// exact answer needs the per-row check, so the candidates are scanned (metadata only).
func (p *exportPlan) count(ctx context.Context) (int64, error) {
	if p.post == nil {
		var n int64
		q := "SELECT COUNT(*) FROM " + p.src.from + " WHERE " + p.fullWhere()
		err := p.s.st.db.QueryRowContext(ctx, p.s.st.bind(q), p.args...).Scan(&n)
		return n, err
	}
	var n int64
	err := p.scan(ctx, false, exportBatchRows, func([]string) error { n++; return nil }, nil)
	return n, err
}

// page returns one preview page, newest first.
func (p *exportPlan) page(ctx context.Context, offset, limit int) ([][]string, error) {
	if p.post == nil {
		q, args, _ := p.batchQuery(exportCursor{}, false, limit, offset)
		rows, _, err := p.readBatch(ctx, q, args, 0, limit)
		if err != nil {
			return nil, err
		}
		return rows, p.prefetch(ctx, rows)
	}
	var out [][]string
	skipped := 0
	err := p.scan(ctx, false, exportBatchRows, func(raw []string) error {
		if skipped < offset {
			skipped++
			return nil
		}
		if len(out) < limit {
			out = append(out, raw)
		}
		return nil
	}, nil)
	return out, err
}

// readBatch runs one query and buffers its rows as text. It stops early once maxBytes of text have
// been read (0 = no cap), reporting whether it did, so the caller can continue from the last row.
func (p *exportPlan) readBatch(ctx context.Context, q string, args []any, maxBytes, limit int) ([][]string, bool, error) {
	atomic.AddInt64(&p.s.exports.queries, 1)
	rows, err := p.s.st.db.QueryContext(ctx, p.s.st.bind(q), args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	n, err := rows.Columns()
	if err != nil {
		return nil, false, err
	}
	var out [][]string
	size := 0
	cut := false
	for rows.Next() {
		vals := make([]sql.NullString, len(n))
		dest := make([]any, len(n))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, false, err
		}
		raw := make([]string, len(n))
		for i, v := range vals {
			raw[i] = v.String
			size += len(v.String)
		}
		out = append(out, raw)
		if maxBytes > 0 && size >= maxBytes && len(out) < limit {
			cut = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return out, cut, nil
}

// scan walks the whole filtered set in keyset batches. visit receives each matching raw row;
// batchDone (optional) runs after every batch with the rows that batch yielded, and a non-nil error
// from either stops the walk. No transaction spans batches and the connection is free between them.
func (p *exportPlan) scan(ctx context.Context, withBody bool, batch int, visit func(raw []string) error, batchDone func(rows int) error) error {
	if p.s.exports.batchSize > 0 {
		batch = p.s.exports.batchSize
	}
	if err := p.prepare(ctx); err != nil {
		return err
	}
	exprs, flagBase := p.selectList(withBody)
	k1Idx, idIdx := -1, len(exprs)-1
	if p.src.k1 != "" {
		k1Idx = len(exprs) - 2
	}
	var cur exportCursor
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q, args, _ := p.batchQuery(cur, withBody, batch, 0)
		rows, cut, err := p.readBatch(ctx, q, args, exportBatchBytes, batch)
		if err != nil {
			return err
		}
		if err := p.prefetch(ctx, rows); err != nil {
			return err
		}
		matched := 0
		for _, raw := range rows {
			if p.post != nil && !p.post(raw, flagBase) {
				continue
			}
			matched++
			if err := visit(raw); err != nil {
				return err
			}
		}
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			cur = exportCursor{started: true, id: last[idIdx]}
			if k1Idx >= 0 {
				cur.k1 = last[k1Idx]
			}
		}
		if batchDone != nil {
			if err := batchDone(matched); err != nil {
				return err
			}
		}
		if len(rows) < batch && !cut {
			return nil
		}
	}
}

// exportRuntime is the part of Server the export worker owns.
type exportRuntime struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	wake    chan struct{}
	once    sync.Once

	queries   int64 // batch queries issued (atomic); also a test probe for the bounded-query guarantee
	batchSize int   // test seam: overrides exportBatchRows
	// maxCandidates overrides exportBelowEntryMaxSymbols (test seam).
	maxCandidates int

	// afterBatch, when set, runs after each batch of a running job has been flushed (test seam).
	afterBatch func(id string, batch int)
	// now is the clock (test seam).
	now func() time.Time
}

func (r *exportRuntime) wakeCh() chan struct{} {
	r.once.Do(func() { r.wake = make(chan struct{}, 1) })
	return r.wake
}

func (r *exportRuntime) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// buildCtx prepares row building for a request. tz is "panel" or "utc"; a nil want means every column.
func (s *Server) newExportCtx(tz string, want []string) *exportCtx {
	ec := &exportCtx{s: s, loc: time.UTC, ouNames: map[string]string{}}
	if tz != "utc" {
		ec.loc = s.panelLocation()
	}
	if want != nil {
		ec.want = map[string]bool{}
		for _, k := range want {
			ec.want[k] = true
		}
	}
	for _, g := range s.st.ListUserGroups() {
		ec.ouNames[strconv.FormatInt(g.ID, 10)] = g.Name
	}
	return ec
}

func fmtCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		return fmt.Sprint(x)
	}
}
