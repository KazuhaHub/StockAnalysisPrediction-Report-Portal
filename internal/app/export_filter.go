package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The export filter model (ADR 0045): an expression tree of AND / OR groups and conditions that the
// server compiles to parameterized SQL from a per-source whitelist. One tree drives the count, the
// preview and the export, so what is previewed is what is exported.

const (
	exportMaxConditions = 20
	exportMaxDepth      = 3 // the root group is depth 1
	exportMaxValues     = 200
	exportMaxValueLen   = 256
)

// Operators, as they appear on the wire.
const (
	opIs       = "is"
	opIsNot    = "is_not"
	opIn       = "in"
	opNotIn    = "not_in"
	opContains = "contains"
	opBefore   = "before"
	opAfter    = "after"
	opBetween  = "between"
)

var (
	opsText   = []string{opIs, opIsNot, opIn, opNotIn, opContains}
	opsEnum   = []string{opIs, opIsNot, opIn, opNotIn}
	opsDate   = []string{opIs, opIsNot, opIn, opNotIn, opBefore, opAfter, opBetween}
	opsTime   = []string{opBefore, opAfter, opBetween}
	opsNumber = []string{opIs, opIsNot, opBefore, opAfter, opBetween}
)

// exportNode is a group (Op set) or a condition (Field set). It decodes straight from the wire
// shape; validation decides which of the two it is.
type exportNode struct {
	Op       string       `json:"op,omitempty"`
	Items    []exportNode `json:"items,omitempty"`
	Field    string       `json:"field,omitempty"`
	Operator string       `json:"operator,omitempty"`
	Values   []string     `json:"values,omitempty"`
}

func (n exportNode) isGroup() bool { return n.Op != "" }

// MarshalJSON always writes the full wire shape: a group carries items and a condition carries
// values, even when empty, so the client never has to guess between absent and empty.
func (n exportNode) MarshalJSON() ([]byte, error) {
	if n.isGroup() {
		items := n.Items
		if items == nil {
			items = []exportNode{}
		}
		return json.Marshal(struct {
			Op    string       `json:"op"`
			Items []exportNode `json:"items"`
		}{n.Op, items})
	}
	values := n.Values
	if values == nil {
		values = []string{}
	}
	return json.Marshal(struct {
		Field    string   `json:"field"`
		Operator string   `json:"operator"`
		Values   []string `json:"values"`
	}{n.Field, n.Operator, values})
}

// exportFilterError is a client mistake in a filter: always a 400, raised before any SQL exists.
type exportFilterError struct{ msg string }

func (e *exportFilterError) Error() string { return e.msg }

func filterErr(format string, args ...any) error {
	return &exportFilterError{msg: fmt.Sprintf(format, args...)}
}

// normalizeExportFilter turns an absent filter into the empty root group, which matches everything.
func normalizeExportFilter(n exportNode) exportNode {
	if n.Op == "" && n.Field == "" && len(n.Items) == 0 {
		return exportNode{Op: "and", Items: []exportNode{}}
	}
	return n
}

// validateExportFilter enforces the structural limits and the source whitelist. It touches no
// database: an unknown field, operator or arity is refused here.
func validateExportFilter(src *exportSource, root exportNode) error {
	if !root.isGroup() {
		return filterErr("the filter root must be an and/or group")
	}
	conds := 0
	var walk func(n exportNode, depth int) error
	walk = func(n exportNode, depth int) error {
		if n.isGroup() {
			if n.Op != "and" && n.Op != "or" {
				return filterErr("unknown group operator %q", n.Op)
			}
			if n.Field != "" || n.Operator != "" || len(n.Values) > 0 {
				return filterErr("a group cannot carry a field, operator or values")
			}
			if depth > exportMaxDepth {
				return filterErr("filters nest at most %d levels", exportMaxDepth)
			}
			for _, c := range n.Items {
				if err := walk(c, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		if n.Field == "" {
			return filterErr("each filter item must be a group or a condition")
		}
		if len(n.Items) > 0 {
			return filterErr("a condition cannot carry items")
		}
		conds++
		if conds > exportMaxConditions {
			return filterErr("filters hold at most %d conditions", exportMaxConditions)
		}
		f := src.field(n.Field)
		if f == nil {
			return filterErr("source %q has no filter field %q", src.Key, n.Field)
		}
		if !containsString(f.Operators, n.Operator) {
			return filterErr("field %q does not allow operator %q", n.Field, n.Operator)
		}
		return checkArity(f, n)
	}
	return walk(root, 1)
}

func checkArity(f *exportField, n exportNode) error {
	want := func(k int) error {
		if len(n.Values) != k {
			return filterErr("operator %q on %q takes %d value(s)", n.Operator, n.Field, k)
		}
		return nil
	}
	switch n.Operator {
	case opIs, opIsNot, opContains, opBefore, opAfter:
		if err := want(1); err != nil {
			return err
		}
	case opBetween:
		if err := want(2); err != nil {
			return err
		}
	case opIn, opNotIn:
		if len(n.Values) == 0 || len(n.Values) > exportMaxValues {
			return filterErr("operator %q on %q takes 1 to %d values", n.Operator, n.Field, exportMaxValues)
		}
	}
	for _, v := range n.Values {
		if len(v) > exportMaxValueLen {
			return filterErr("a value for %q is too long", n.Field)
		}
		switch f.Type {
		case "date":
			if _, err := time.Parse("2006-01-02", v); err != nil {
				return filterErr("%q takes YYYY-MM-DD dates", n.Field)
			}
		case "datetime":
			if _, _, err := parseExportInstant(v, time.UTC); err != nil {
				return filterErr("%q takes RFC 3339 instants or YYYY-MM-DD dates", n.Field)
			}
		case "number":
			if _, err := strconv.ParseInt(v, 10, 64); err != nil {
				return filterErr("%q takes whole numbers", n.Field)
			}
		}
	}
	return nil
}

// parseExportInstant reads a datetime filter value. dayOnly reports a bare civil date, which means
// the whole day in the panel time zone rather than its first instant.
func parseExportInstant(v string, loc *time.Location) (t time.Time, dayOnly bool, err error) {
	if t, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		return t, true, nil
	}
	t, err = time.Parse(time.RFC3339, v)
	return t, false, err
}

// instant encodings: how a datetime column stores its value.
type exportEnc int

const (
	encText    exportEnc = iota
	encRFC3339           // UTC RFC 3339 text (audit_log.at)
	encLocal             // server-local "2006-01-02 15:04:05" text (batch_jobs, cleanup_runs)
	encMillis            // unix milliseconds (notification tables)
)

func (e exportEnc) format(t time.Time) any {
	switch e {
	case encRFC3339:
		return t.UTC().Format(time.RFC3339)
	case encLocal:
		return t.In(time.Local).Format("2006-01-02 15:04:05")
	default:
		return t.UnixMilli()
	}
}

// leaf is one compiled condition. gv leaves are evaluated in Go (see the reports source).
type exportLeaf struct {
	sql  string
	args []any
	gv   *exportGV // non-nil: derived generator version, checked exactly in Go
	// below is a below-entry condition (forecasts): the SQL is a prefilter and the exact test needs
	// the latest price, so it is evaluated in Go like gv.
	below *exportBelow
	// latest is a latest-per-symbol condition (forecasts), evaluated in Go against the newest
	// candidate of each symbol.
	latest *exportLatest
}

// exportGV is a generator-version condition. The SQL side is only a prefilter; the exact test is
// generatorVersion(), because the version is parsed from source/title and is not a column.
type exportGV struct {
	values []string
	negate bool
}

func (g *exportGV) match(v string) bool {
	hit := false
	for _, want := range g.values {
		if strings.EqualFold(v, want) {
			hit = true
			break
		}
	}
	return hit != g.negate
}

// exportEval mirrors the filter tree with leaf indices, so a tree holding a derived condition can be
// evaluated exactly per row: the other leaves arrive as SQL-computed flags.
type exportEval struct {
	and  bool
	kids []*exportEval
	leaf int // index into the compiler's leaves when kids is nil
}

func (e *exportEval) eval(flag func(leaf int) bool) bool {
	if e.kids == nil {
		return flag(e.leaf)
	}
	for _, k := range e.kids {
		r := k.eval(flag)
		if e.and && !r {
			return false
		}
		if !e.and && r {
			return true
		}
	}
	return e.and
}

type exportCompiler struct {
	s      *Server
	src    *exportSource
	loc    *time.Location
	leaves []exportLeaf
	// neutral compiles the derived conditions (latest per symbol, below entry) as true: the result
	// is the candidate set the latest-per-symbol rule picks from.
	neutral bool
}

// compileExportFilter returns the WHERE fragment (always parenthesised, "1=1" for an empty tree),
// its arguments in textual order, the leaves and the evaluation tree. A derived generator-version
// leaf contributes an over-approximating prefilter, so the fragment is a superset of the exact
// answer whenever a tree contains one.
func (s *Server) compileExportFilter(src *exportSource, root exportNode, neutral bool) (sqlText string, args []any, c *exportCompiler, tree *exportEval, err error) {
	c = &exportCompiler{s: s, src: src, loc: s.panelLocation(), neutral: neutral}
	var walk func(n exportNode) (string, []any, *exportEval, error)
	walk = func(n exportNode) (string, []any, *exportEval, error) {
		if n.isGroup() {
			if len(n.Items) == 0 {
				return "1=1", nil, &exportEval{and: true, kids: []*exportEval{}}, nil
			}
			join := " AND "
			if n.Op == "or" {
				join = " OR "
			}
			parts := make([]string, 0, len(n.Items))
			var all []any
			ev := &exportEval{and: n.Op == "and", kids: []*exportEval{}}
			for _, item := range n.Items {
				q, a, k, err := walk(item)
				if err != nil {
					return "", nil, nil, err
				}
				parts = append(parts, q)
				all = append(all, a...)
				ev.kids = append(ev.kids, k)
			}
			return "(" + strings.Join(parts, join) + ")", all, ev, nil
		}
		f := c.src.field(n.Field)
		leaf, err := f.compile(c, f, n.Operator, n.Values)
		if err != nil {
			return "", nil, nil, err
		}
		c.leaves = append(c.leaves, leaf)
		return "(" + leaf.sql + ")", leaf.args, &exportEval{leaf: len(c.leaves) - 1}, nil
	}
	sqlText, args, tree, err = walk(root)
	return
}

func (c *exportCompiler) like() string { return c.s.st.likeOp() }

// escapeLike makes user text literal inside a LIKE pattern (ESCAPE '\').
func escapeLike(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(v)
}

// compileDefault is the compiler for every field that is just a column: text, enum, date, datetime
// and number, with the instant encodings above.
func compileDefault(c *exportCompiler, f *exportField, op string, vals []string) (exportLeaf, error) {
	col := f.col
	switch f.Type {
	case "datetime":
		return compileInstant(c, f, op, vals)
	case "date":
		return compileDate(col, op, vals), nil
	case "number":
		return compileNumber(col, op, vals)
	}
	text := "COALESCE(" + col + ",'')"
	switch op {
	case opIs:
		return exportLeaf{sql: text + " = ?", args: []any{vals[0]}}, nil
	case opIsNot:
		return exportLeaf{sql: text + " <> ?", args: []any{vals[0]}}, nil
	case opIn, opNotIn:
		not := ""
		if op == opNotIn {
			not = "NOT "
		}
		return exportLeaf{sql: text + " " + not + "IN (" + placeholders(len(vals)) + ")", args: anyStrings(vals)}, nil
	case opContains:
		return exportLeaf{sql: text + " " + c.like() + " ? ESCAPE '\\'", args: []any{"%" + escapeLike(vals[0]) + "%"}}, nil
	}
	return exportLeaf{}, filterErr("operator %q is not supported on %q", op, f.Key)
}

func anyStrings(v []string) []any {
	out := make([]any, len(v))
	for i := range v {
		out[i] = v[i]
	}
	return out
}

// compileDate handles civil-date text columns (reports.rdate). ISO dates order lexically.
func compileDate(col, op string, vals []string) exportLeaf {
	text := "COALESCE(" + col + ",'')"
	switch op {
	case opIs:
		return exportLeaf{sql: text + " = ?", args: []any{vals[0]}}
	case opIsNot:
		return exportLeaf{sql: text + " <> ?", args: []any{vals[0]}}
	case opIn, opNotIn:
		not := ""
		if op == opNotIn {
			not = "NOT "
		}
		return exportLeaf{sql: text + " " + not + "IN (" + placeholders(len(vals)) + ")", args: anyStrings(vals)}
	case opBefore:
		return exportLeaf{sql: text + " < ? AND " + text + " <> ''", args: []any{vals[0]}}
	case opAfter:
		return exportLeaf{sql: text + " > ?", args: []any{vals[0]}}
	default: // between, inclusive
		return exportLeaf{sql: text + " >= ? AND " + text + " <= ?", args: []any{vals[0], vals[1]}}
	}
}

func compileNumber(col, op string, vals []string) (exportLeaf, error) {
	nums := make([]any, len(vals))
	for i, v := range vals {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return exportLeaf{}, filterErr("%q is not a whole number", v)
		}
		nums[i] = n
	}
	c := "COALESCE(" + col + ",0)"
	switch op {
	case opIs:
		return exportLeaf{sql: c + " = ?", args: nums}, nil
	case opIsNot:
		return exportLeaf{sql: c + " <> ?", args: nums}, nil
	case opBefore:
		return exportLeaf{sql: c + " < ?", args: nums}, nil
	case opAfter:
		return exportLeaf{sql: c + " > ?", args: nums}, nil
	default:
		return exportLeaf{sql: c + " >= ? AND " + c + " <= ?", args: nums}, nil
	}
}

// compileInstant compares a datetime column against filter values. A bare date means the whole civil
// day in the panel time zone, converted to UTC instants first, so "after 2026-10-01" starts at the
// next panel-tz midnight and "between a and b" is inclusive of both days.
func compileInstant(c *exportCompiler, f *exportField, op string, vals []string) (exportLeaf, error) {
	col := f.col
	one := func(v string) (lo, hiExcl time.Time, day bool, err error) {
		t, day, err := parseExportInstant(v, c.loc)
		if err != nil {
			return t, t, false, filterErr("%q takes RFC 3339 instants or YYYY-MM-DD dates", f.Key)
		}
		if day {
			return t, t.AddDate(0, 0, 1), true, nil
		}
		return t, t, false, nil
	}
	enc := f.enc
	switch op {
	case opAfter:
		lo, hi, day, err := one(vals[0])
		if err != nil {
			return exportLeaf{}, err
		}
		if day {
			return exportLeaf{sql: col + " >= ?", args: []any{enc.format(hi)}}, nil
		}
		return exportLeaf{sql: col + " > ?", args: []any{enc.format(lo)}}, nil
	case opBefore:
		lo, _, _, err := one(vals[0])
		if err != nil {
			return exportLeaf{}, err
		}
		return exportLeaf{sql: col + " < ?", args: []any{enc.format(lo)}}, nil
	case opBetween:
		a, _, _, err := one(vals[0])
		if err != nil {
			return exportLeaf{}, err
		}
		b, bHi, bDay, err := one(vals[1])
		if err != nil {
			return exportLeaf{}, err
		}
		if bDay {
			return exportLeaf{sql: col + " >= ? AND " + col + " < ?", args: []any{enc.format(a), enc.format(bHi)}}, nil
		}
		return exportLeaf{sql: col + " >= ? AND " + col + " <= ?", args: []any{enc.format(a), enc.format(b)}}, nil
	}
	return exportLeaf{}, filterErr("operator %q is not supported on %q", op, f.Key)
}
