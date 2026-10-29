package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

// The forecasts export source (ADR 0045 addendum): one row per accepted prediction set of the
// forecast ledger (ADR 0029), with the entry price and the probability-weighted terminal value read
// from its claims, and the latest quote read from the shared batch quote path.

const (
	// exportBelowEntryMaxSymbols caps the distinct symbols a below-entry screen may price. The screen
	// needs a price for every candidate before it can count, so the candidate set must be bounded; a
	// request above the cap is refused (400) rather than truncated, because a truncated screen would
	// silently miss stocks.
	exportBelowEntryMaxSymbols = 2000

	// exportQuoteChunk is how many symbols go into one upstream batch. It is the cap the home feed's
	// own batch endpoint applies, so both callers present the vendor with the same request shape.
	exportQuoteChunk = quoteBatchMax
)

// exportQuotesOn reports whether this portal may send stock codes to a quote vendor for an export.
//
// The switch is the operator's home-feed quote switch (setQuoteHomeCards, read by quoteHomeCards).
// It is the only switch that governs OUTBOUND DISCLOSURE of codes to a vendor: its own comment says
// that turning prices on sends the codes on a reader's screen to a third party, and that is exactly
// what a price-bearing export does with its candidate symbols. The other quote settings (source
// order, TTLs) tune how quotes are fetched, not whether codes may leave. A stock page fetches a quote
// only for the one code the reader opened; an export sends a whole screen's worth, so it follows the
// stricter, disclosure-oriented switch.
func (s *Server) exportQuotesOn() bool { return s.quoteHomeCards() }

// exportView returns the source as this portal can serve it right now: with quotes off, the fields
// and columns that depend on a latest price are absent (not empty).
func (s *Server) exportView(src *exportSource) *exportSource {
	gated := false
	for _, f := range src.Fields {
		gated = gated || f.quote
	}
	for _, c := range src.Columns {
		gated = gated || c.quote
	}
	if !gated || s.exportQuotesOn() {
		return src
	}
	v := *src
	v.Fields, v.Columns = nil, nil
	for _, f := range src.Fields {
		if !f.quote {
			v.Fields = append(v.Fields, f)
		}
	}
	for _, c := range src.Columns {
		if !c.quote {
			v.Columns = append(v.Columns, c)
		}
	}
	return &v
}

// exportPrice is one symbol's latest price: fen, and the vendor's own as-of stamp.
type exportPrice struct {
	fen  int64
	asOf string
}

// exportLatest is a compiled latest_per_symbol condition: want is the outcome (newest or not) it accepts.
type exportLatest struct{ want bool }

// exportNewest is the newest candidate of one symbol.
type exportNewest struct {
	madeAt string
	id     int64
}

// isLatest reports whether the row is the newest candidate of its symbol.
func (p *exportPlan) isLatest(raw []string) bool {
	n, ok := p.newest[raw[p.src.symIdx]]
	return ok && n.madeAt == raw[p.src.madeIdx] && n.id == atoi64(raw[p.src.idIdx])
}

// loadNewest finds the newest candidate per symbol with one grouped query, folded in Go; memory is
// bounded by the number of distinct symbols.
func (p *exportPlan) loadNewest(ctx context.Context) error {
	atomic.AddInt64(&p.s.exports.queries, 1)
	rows, err := p.s.st.db.QueryContext(ctx, p.s.st.bind("SELECT ps.symbol, ps.made_at, MAX(ps.id) FROM "+p.src.from+
		" WHERE "+p.src.base+" AND "+p.candWhere+" GROUP BY ps.symbol, ps.made_at"), p.candArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	p.newest = map[string]exportNewest{}
	for rows.Next() {
		var sym, at string
		var id int64
		if err := rows.Scan(&sym, &at, &id); err != nil {
			return err
		}
		if cur, ok := p.newest[sym]; !ok || at > cur.madeAt || at == cur.madeAt && id > cur.id {
			p.newest[sym] = exportNewest{madeAt: at, id: id}
		}
	}
	return rows.Err()
}

// exportBelow is a compiled below_entry condition: want is the outcome (below or not) it accepts.
type exportBelow struct{ want bool }

// belowMatch reports whether the row's set is below its entry price: it has an entry price, the
// symbol has a price, and the price is strictly lower. A set without an entry price, or a symbol the
// vendor could not price, is never below.
func (p *exportPlan) belowMatch(raw []string) bool {
	entry := atoi64(raw[p.src.entryIdx])
	if entry <= 0 {
		return false
	}
	pr, ok := p.prices[raw[p.src.symIdx]]
	return ok && pr.fen < entry
}

func (p *exportPlan) priceOf(symbol string) (exportPrice, bool) {
	pr, ok := p.prices[symbol]
	return pr, ok
}

// withColumns tells the plan which columns the caller renders, so latest prices are only fetched
// when a price column is wanted.
func (p *exportPlan) withColumns(cols []string) {
	for _, k := range cols {
		if k == "latest_price" || k == "latest_at" || k == "vs_entry_pct" {
			p.needPrices = true
		}
	}
}

func (p *exportPlan) maxCandidates() int {
	if n := p.s.exports.maxCandidates; n > 0 {
		return n
	}
	return exportBelowEntryMaxSymbols
}

// prepare runs once per plan, before the first read. A below-entry condition needs a price for every
// candidate before anything can be counted, so the candidate symbols (those matching the compiled
// WHERE, a superset of the answer) are collected, capped, and priced up front.
func (p *exportPlan) prepare(ctx context.Context) error {
	if p.prepared || !p.belowMode && !p.latestMode {
		return nil
	}
	if p.latestMode {
		if err := p.loadNewest(ctx); err != nil {
			return err
		}
	}
	if !p.belowMode {
		p.prepared = true
		return nil
	}
	limit := p.maxCandidates()
	atomic.AddInt64(&p.s.exports.queries, 1)
	rows, err := p.s.st.db.QueryContext(ctx, p.s.st.bind("SELECT DISTINCT "+p.src.symExpr+" FROM "+p.src.from+
		" WHERE "+p.fullWhere()+" LIMIT "+strconv.Itoa(limit+1)), p.args...)
	if err != nil {
		return err
	}
	var syms []string
	for rows.Next() {
		var sym string
		if err := rows.Scan(&sym); err != nil {
			rows.Close()
			return err
		}
		syms = append(syms, sym)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(syms) > limit {
		return filterErr("below_entry needs a latest price for every candidate: more than %d symbols match the other filters; narrow them (a date range, a report type, or latest_per_symbol)", limit)
	}
	p.prices = make(map[string]exportPrice, len(syms))
	if err := p.fetchPrices(ctx, syms); err != nil {
		return err
	}
	p.prepared = true
	return nil
}

// prefetch prices the symbols of one batch of rows when a price column is wanted. Outside the
// below-entry mode the previous batch's prices are dropped first, so memory stays constant however
// long the export runs (the quote cache still serves a repeated symbol without an upstream call).
func (p *exportPlan) prefetch(ctx context.Context, rows [][]string) error {
	if !p.needPrices || !p.s.exportQuotesOn() || p.src.symExpr == "" {
		return nil
	}
	if p.belowMode {
		return nil // prepare already priced the whole candidate set
	}
	seen := map[string]bool{}
	var syms []string
	for _, raw := range rows {
		if sym := raw[p.src.symIdx]; sym != "" && !seen[sym] {
			seen[sym] = true
			syms = append(syms, sym)
		}
	}
	p.prices = make(map[string]exportPrice, len(syms))
	return p.fetchPrices(ctx, syms)
}

// fetchPrices asks the shared batch quote path for the symbols, one upstream batch per chunk of
// exportQuoteChunk symbols and never one per row. A symbol that does not resolve or that the vendor
// does not answer simply has no entry, and is therefore blank and never "below".
func (p *exportPlan) fetchPrices(ctx context.Context, syms []string) error {
	if !p.s.exportQuotesOn() {
		return nil
	}
	cfg := p.s.quoteConfigLoad()
	for start := 0; start < len(syms); start += exportQuoteChunk {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+exportQuoteChunk, len(syms))
		targets := make([]quoteTarget, 0, end-start)
		byVendor := map[string][]string{}
		for _, sym := range syms[start:end] {
			t, err := quoteResolve(sym)
			if err != nil {
				continue
			}
			if _, dup := byVendor[t.Symbol]; !dup {
				targets = append(targets, t)
			}
			byVendor[t.Symbol] = append(byVendor[t.Symbol], sym)
		}
		if len(targets) == 0 {
			continue
		}
		p.priceLookup++
		for vendor, entry := range p.s.quotes.fetchBatchUnder(ctx, cfg, targets) {
			if entry.resp == nil || entry.resp.HistoryOnly || entry.resp.Snapshot.Last <= 0 {
				continue
			}
			for _, sym := range byVendor[vendor] {
				p.prices[sym] = exportPrice{fen: entry.resp.Snapshot.Last, asOf: entry.resp.Snapshot.AsOf}
			}
		}
	}
	return nil
}

// ---------- the source ----------

const (
	forecastEntrySQL  = `(SELECT CAST(c.price_fen AS TEXT) FROM prediction_claims c WHERE c.set_id=ps.id AND c.kind='entry_price' LIMIT 1)`
	forecastTermSQL   = `(SELECT CAST(c.price_fen AS TEXT) FROM prediction_claims c WHERE c.set_id=ps.id AND c.kind='terminal_value' LIMIT 1)`
	forecastStanceSQL = `(SELECT c.stance FROM prediction_claims c WHERE c.set_id=ps.id AND c.kind='stance' LIMIT 1)`
	// forecastHasEntrySQL is the below_entry prefilter: a set without an entry price can never be below it.
	forecastHasEntrySQL = `EXISTS (SELECT 1 FROM prediction_claims c WHERE c.set_id=ps.id AND c.kind='entry_price' AND c.price_fen > 0)`
)

func qcol(key string, def bool) exportColumn {
	return exportColumn{Key: key, Default: def, quote: true}
}

func parseYesNo(f *exportField, op string, vals []string) (yes, no bool, err error) {
	set := map[bool]bool{}
	for _, v := range vals {
		switch v {
		case "yes":
			set[true] = true
		case "no":
			set[false] = true
		default:
			return false, false, filterErr("%q takes yes or no", f.Key)
		}
	}
	switch op {
	case opIs, opIn:
		return set[true], set[false], nil
	default: // is_not, not_in: the complement
		return !set[true], !set[false], nil
	}
}

// compileLatestPerSymbol keeps, per symbol, the newest set (made_at, then id) AMONG the sets that
// match the rest of the filter. It is exact in OR and nested groups: the plan computes the candidate
// set with this condition (and below_entry) taken as true, then the tree is evaluated per row with
// this leaf equal to "the row is its symbol's newest candidate".
func compileLatestPerSymbol(c *exportCompiler, f *exportField, op string, vals []string) (exportLeaf, error) {
	yes, no, err := parseYesNo(f, op, vals)
	if err != nil {
		return exportLeaf{}, err
	}
	if c.neutral || yes && no {
		return exportLeaf{sql: "1=1"}, nil
	}
	return exportLeaf{sql: "1=1", latest: &exportLatest{want: yes}}, nil
}

func compileBelowEntry(c *exportCompiler, f *exportField, op string, vals []string) (exportLeaf, error) {
	yes, no, err := parseYesNo(f, op, vals)
	if err != nil {
		return exportLeaf{}, err
	}
	if c.neutral {
		return exportLeaf{sql: "1=1"}, nil
	}
	switch {
	case yes && no:
		return exportLeaf{sql: "1=1"}, nil
	case yes:
		return exportLeaf{sql: forecastHasEntrySQL, below: &exportBelow{want: true}}, nil
	default:
		return exportLeaf{sql: "1=1", below: &exportBelow{want: false}}, nil
	}
}

func forecastRTypeOptions(s *Server) []string {
	var out []string
	rows, err := s.st.query(`SELECT DISTINCT rtype FROM prediction_sets WHERE state=? AND rtype<>'' ORDER BY rtype`, predStateAccepted)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// yuan renders integer fen as yuan with two decimals. It is a json.Number so the wire value is a
// JSON number that keeps its trailing zero ("12.50") and the CSV cell is the same text.
func yuan(fen int64) json.Number {
	sign := ""
	if fen < 0 {
		sign, fen = "-", -fen
	}
	return json.Number(fmt.Sprintf("%s%d.%02d", sign, fen/100, fen%100))
}

func yuanText(raw string) any {
	if raw == "" {
		return nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil
	}
	return yuan(n)
}

// pctChange is (latest-entry)/entry*100 to two decimals, rounded half away from zero in integers.
func pctChange(latest, entry int64) json.Number {
	num := (latest - entry) * 10000
	q := num / entry
	if r := num % entry; r != 0 && 2*abs64(r) >= abs64(entry) {
		if num < 0 {
			q--
		} else {
			q++
		}
	}
	return yuan(q)
}

func forecastsSource() *exportSource {
	return &exportSource{
		Key:     "forecasts",
		Formats: []string{"csv", "jsonl"},
		Fields: []exportField{
			{Key: "report_date", Type: "date", Operators: opsDate, col: "ps.report_date", compile: compileDefault},
			textField("symbol", "ps.symbol", opsText),
			{Key: "rtype", Type: "enum", Operators: opsEnum, col: "ps.rtype", compile: compileDefault, options: forecastRTypeOptions},
			textField("version", "ps.version", opsText),
			{Key: "stance", Type: "enum", Operators: opsEnum, col: forecastStanceSQL, compile: compileDefault,
				options: staticOptions(predStances...)},
			{Key: "latest_per_symbol", Type: "enum", Operators: opsEnum, compile: compileLatestPerSymbol, options: staticOptions("yes", "no")},
			{Key: "below_entry", Type: "enum", Operators: opsEnum, compile: compileBelowEntry, options: staticOptions("yes", "no"), quote: true},
		},
		Columns: []exportColumn{
			ncol("set_id", true), ncol("report_id", true), col("symbol", true), col("name", true), col("report_date", true),
			col("made_at", true), col("rtype", true), col("version", true), col("stance", true),
			ncol("entry_price", true), ncol("terminal_value", true),
			qcol("latest_price", true), qcol("latest_at", true), qcol("vs_entry_pct", true),
		},
		TimeField: "report_date",
		from:      "prediction_sets ps LEFT JOIN reports r ON r.id=ps.report_id LEFT JOIN stocks k ON k.code=ps.symbol",
		base:      "ps.state = '" + predStateAccepted + "'",
		sel: []string{"ps.id", "COALESCE(CAST(ps.report_id AS TEXT),'')", "COALESCE(ps.symbol,'')",
			"COALESCE(NULLIF(r.name,''),k.name,'')", "COALESCE(ps.report_date,'')", "COALESCE(ps.made_at,'')",
			"COALESCE(ps.rtype,'')", "COALESCE(ps.version,'')", "COALESCE(" + forecastStanceSQL + ",'')",
			"COALESCE(" + forecastEntrySQL + ",'')", "COALESCE(" + forecastTermSQL + ",'')"},
		symIdx: 2, entryIdx: 9, madeIdx: 5, idIdx: 0, symExpr: "ps.symbol",
		k1: "ps.made_at", id: "ps.id", idInt: true,
		build: func(ec *exportCtx, raw []string) exportRow {
			row := exportRow{
				"set_id": atoi64(raw[0]), "symbol": raw[2], "name": raw[3], "report_date": raw[4],
				"made_at": ec.instant(raw[5], encRFC3339), "rtype": raw[6], "version": raw[7], "stance": raw[8],
				"entry_price": yuanText(raw[9]), "terminal_value": yuanText(raw[10]),
			}
			if raw[1] != "" {
				row["report_id"] = atoi64(raw[1])
			}
			if ec.priceOf == nil {
				return row
			}
			if pr, ok := ec.priceOf(raw[2]); ok {
				row["latest_price"] = yuan(pr.fen)
				if t, err := time.Parse(time.RFC3339, pr.asOf); err == nil {
					row["latest_at"] = t.In(ec.loc).Format(time.RFC3339)
				} else if pr.asOf != "" {
					row["latest_at"] = pr.asOf
				}
				if entry := atoi64(raw[9]); entry > 0 {
					row["vs_entry_pct"] = pctChange(pr.fen, entry)
				}
			}
			return row
		},
	}
}
