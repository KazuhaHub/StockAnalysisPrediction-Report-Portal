package app

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The export source registry (ADR 0045). One exportSource per exportable log: its filter fields, its
// columns and how its rows are read. The sources endpoint, the count/preview endpoint and the worker
// all read this table, so adding a source is one entry here.

type exportField struct {
	Key       string
	Type      string // enum | text | date | datetime | number
	Operators []string

	col     string    // SQL expression the condition applies to
	enc     exportEnc // instant encoding for datetime fields
	compile func(c *exportCompiler, f *exportField, op string, vals []string) (exportLeaf, error)
	options func(s *Server) []string
	tree    bool // carries the kind -> subtypes tree (reports kind / rtype)
	quote   bool // needs the operator's quote switch on; absent from the source when it is off
}

type exportColumn struct {
	Key     string
	Default bool
	quote   bool // needs the operator's quote switch on; absent from the source when it is off
}

type exportSource struct {
	Key       string
	Formats   []string // first is the default
	Fields    []exportField
	Columns   []exportColumn
	TimeField string

	// Query shape. Every selected expression is rendered to text by the database (COALESCEd), and the
	// keyset cursor is (k1, id) descending: k1 may be empty, in which case id alone orders the rows.
	from     string
	base     string // restriction applied to every query of this source
	sel      []string
	bodyExpr string // selected last when a report body is wanted
	k1       string
	k1Int    bool
	id       string
	idInt    bool

	// build turns one raw row into column values. It must not touch the database: on SQLite the pool
	// is a single connection, and the streamer may still hold it.
	build func(ec *exportCtx, raw []string) exportRow
	// symIdx and entryIdx locate the symbol and the entry price (fen text) in a raw row (forecasts).
	symIdx, entryIdx int
	madeIdx, idIdx   int    // made_at and set id in a raw row (forecasts)
	symExpr          string // SQL expression of the symbol; set on sources that carry prices
	// gvValue extracts the derived generator version from a raw row (reports only).
	gvValue func(raw []string) string
}

func (src *exportSource) field(key string) *exportField {
	for i := range src.Fields {
		if src.Fields[i].Key == key {
			return &src.Fields[i]
		}
	}
	return nil
}

func (src *exportSource) column(key string) *exportColumn {
	for i := range src.Columns {
		if src.Columns[i].Key == key {
			return &src.Columns[i]
		}
	}
	return nil
}

func (src *exportSource) defaultColumns() []string {
	var out []string
	for _, c := range src.Columns {
		if c.Default {
			out = append(out, c.Key)
		}
	}
	return out
}

func (src *exportSource) hasFormat(f string) bool { return containsString(src.Formats, f) }

type exportRow map[string]any

func exportSourceByKey(key string) *exportSource {
	for _, s := range exportSourceList {
		if s.Key == key {
			return s
		}
	}
	return nil
}

var exportSourceList = []*exportSource{reportsSource(), auditSource(), signinsSource(), runsSource(), notificationsSource(), cleanupSource(), forecastsSource()}

func col(key string, def bool) exportColumn { return exportColumn{Key: key, Default: def} }

// ncol is col for a numeric column; the type lives in build, which returns int64 for it.
func ncol(key string, def bool) exportColumn { return col(key, def) }

func textField(key, col string, ops []string) exportField {
	return exportField{Key: key, Type: "text", Operators: ops, col: col, compile: compileDefault}
}

func enumField(key, col string, options func(*Server) []string) exportField {
	return exportField{Key: key, Type: "enum", Operators: opsEnum, col: col, options: options, compile: compileDefault}
}

func staticOptions(v ...string) func(*Server) []string { return func(*Server) []string { return v } }

func timeField(key, col string, enc exportEnc) exportField {
	return exportField{Key: key, Type: "datetime", Operators: opsTime, col: col, enc: enc, compile: compileDefault}
}

// qField is the free-text "contains" search over several columns, like the console's search box.
func qField(cols ...string) exportField {
	return exportField{Key: "q", Type: "text", Operators: []string{opContains}, compile: func(c *exportCompiler, f *exportField, op string, vals []string) (exportLeaf, error) {
		parts := make([]string, len(cols))
		args := make([]any, len(cols))
		for i, cl := range cols {
			parts[i] = "COALESCE(" + cl + ",'') " + c.like() + " ? ESCAPE '\\'"
			args[i] = "%" + escapeLike(vals[0]) + "%"
		}
		return exportLeaf{sql: strings.Join(parts, " OR "), args: args}, nil
	}}
}

// ---------- rendering helpers ----------

// exportCtx carries what row building needs: the rendering zone, the geo lookup and the requested
// columns (so unrequested derived columns cost nothing).
type exportCtx struct {
	s       *Server
	loc     *time.Location // panel zone, or UTC
	want    map[string]bool
	ouNames map[string]string
	// priceOf answers a symbol's latest price from the plan driving the export (forecasts only).
	priceOf func(symbol string) (exportPrice, bool)
}

func (ec *exportCtx) wants(key string) bool { return ec.want == nil || ec.want[key] }

// instant renders a stored instant in the export zone: RFC 3339 with an offset (or Z for UTC).
// A value that is not a recognised stamp is returned as it is rather than guessed at.
func (ec *exportCtx) instant(raw string, enc exportEnc) any {
	if raw == "" {
		return nil
	}
	var t time.Time
	switch enc {
	case encMillis:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return raw
		}
		t = time.UnixMilli(n)
	case encLocal:
		parsed, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.Local)
		if err != nil {
			return raw
		}
		t = parsed
	default:
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			t = parsed
		} else if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.Local); err == nil {
			t = parsed // legacy audit stamp: the host's wall clock
		} else {
			return raw
		}
	}
	return t.In(ec.loc).Format(time.RFC3339)
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func (ec *exportCtx) geo(row exportRow, ip string) {
	if !ec.wants("country") && !ec.wants("region") && !ec.wants("city") {
		return
	}
	loc := ec.s.geo.Lookup(ip)
	row["country"], row["region"], row["city"] = loc.Country, loc.Region, loc.City
}

// ---------- reports ----------

type exportKindTree struct {
	Kind     string   `json:"kind"`
	Subtypes []string `json:"subtypes"`
}

// reportKindTree is kind -> subtypes, from the type registry plus whatever the reports table holds.
// Subtypes follow the registry order; kinds follow their first subtype. A subtype the registry does
// not know yet still appears, under the kind its reports carry.
func (s *Server) reportKindTree() []exportKindTree {
	type sub struct {
		name string
		ord  int
	}
	byKind := map[string]map[string]int{}
	add := func(kind, name string, ord int) {
		if kind == "" || name == "" {
			return
		}
		if byKind[kind] == nil {
			byKind[kind] = map[string]int{}
		}
		if cur, ok := byKind[kind][name]; !ok || ord < cur {
			byKind[kind][name] = ord
		}
	}
	for _, t := range s.st.TypeConfigs() {
		add(t.Kind, t.Name, t.Ord)
	}
	if rows, err := s.st.query(`SELECT DISTINCT COALESCE(kind,''),COALESCE(rtype,'') FROM reports`); err == nil {
		for rows.Next() {
			var kind, rt string
			if rows.Scan(&kind, &rt) == nil {
				add(kind, rt, 1<<30)
			}
		}
		rows.Close()
	}
	type kindEntry struct {
		kind string
		subs []sub
		min  int
	}
	var kinds []kindEntry
	for kind, subs := range byKind {
		ke := kindEntry{kind: kind, min: 1 << 30}
		for name, ord := range subs {
			ke.subs = append(ke.subs, sub{name, ord})
			if ord < ke.min {
				ke.min = ord
			}
		}
		sort.Slice(ke.subs, func(i, j int) bool {
			if ke.subs[i].ord != ke.subs[j].ord {
				return ke.subs[i].ord < ke.subs[j].ord
			}
			return ke.subs[i].name < ke.subs[j].name
		})
		kinds = append(kinds, ke)
	}
	sort.Slice(kinds, func(i, j int) bool {
		if kinds[i].min != kinds[j].min {
			return kinds[i].min < kinds[j].min
		}
		return kinds[i].kind < kinds[j].kind
	})
	out := make([]exportKindTree, 0, len(kinds))
	for _, k := range kinds {
		t := exportKindTree{Kind: k.kind, Subtypes: []string{}}
		for _, sb := range k.subs {
			t.Subtypes = append(t.Subtypes, sb.name)
		}
		out = append(out, t)
	}
	return out
}

func (s *Server) reportKindOptions() []string {
	tree := s.reportKindTree()
	out := make([]string, 0, len(tree))
	for _, k := range tree {
		out = append(out, k.Kind)
	}
	return out
}

func (s *Server) reportRTypeOptions() []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range s.reportKindTree() {
		for _, t := range k.Subtypes {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// reportGeneratorVersions lists the producer versions present. The version is parsed from
// source/title, so this reads those two columns only (no bodies) and de-duplicates in Go.
func (s *Server) reportGeneratorVersions() []string {
	set := map[string]bool{}
	rows, err := s.st.query(`SELECT COALESCE(source,''),COALESCE(title,'') FROM reports`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var r Rep
		if rows.Scan(&r.Source, &r.Title) == nil {
			if v := generatorVersion(r); v != "" {
				set[v] = true
			}
		}
	}
	out := keysOf(set)
	sort.Strings(out)
	return out
}

func compileGeneratorVersion(c *exportCompiler, f *exportField, op string, vals []string) (exportLeaf, error) {
	gv := &exportGV{values: vals, negate: op == opIsNot || op == opNotIn}
	if gv.negate {
		// A negation cannot be pre-filtered by a substring: the exact answer is the complement of the
		// Go check, so every row is a candidate.
		return exportLeaf{sql: "1=1", gv: gv}, nil
	}
	parts := make([]string, 0, len(vals))
	var args []any
	for _, v := range vals {
		p := "%" + escapeLike(v) + "%"
		parts = append(parts, "(COALESCE(r.source,'') "+c.like()+" ? ESCAPE '\\' OR COALESCE(r.title,'') "+c.like()+" ? ESCAPE '\\')")
		args = append(args, p, p)
	}
	return exportLeaf{sql: strings.Join(parts, " OR "), args: args, gv: gv}, nil
}

func reportsSource() *exportSource {
	return &exportSource{
		Key:     "reports",
		Formats: []string{"md_zip", "csv", "jsonl"},
		Fields: []exportField{
			{Key: "kind", Type: "enum", Operators: opsEnum, col: "r.kind", compile: compileDefault, tree: true,
				options: func(s *Server) []string { return s.reportKindOptions() }},
			{Key: "rtype", Type: "enum", Operators: opsEnum, col: "r.rtype", compile: compileDefault, tree: true,
				options: func(s *Server) []string { return s.reportRTypeOptions() }},
			{Key: "generator_version", Type: "enum", Operators: opsEnum, compile: compileGeneratorVersion,
				options: func(s *Server) []string { return s.reportGeneratorVersions() }},
			{Key: "date", Type: "date", Operators: opsDate, col: "r.rdate", compile: compileDefault},
			textField("symbol", "r.symbol", opsText),
			qField("r.title", "r.symbol", "r.name"),
		},
		Columns: []exportColumn{
			ncol("id", true), col("symbol", true), col("name", true), col("date", true), col("kind", true),
			col("rtype", true), col("title", true), col("generator_version", true), col("version", true),
			col("sent_at", true), col("source", false), col("run_id", false),
		},
		TimeField: "date",
		from:      "reports r",
		sel: []string{"r.id", "COALESCE(r.symbol,'')", "COALESCE(r.name,'')", "COALESCE(r.rdate,'')",
			"COALESCE(r.kind,'')", "COALESCE(r.rtype,'')", "COALESCE(r.title,'')", "COALESCE(r.version,'')",
			"COALESCE(r.source,'')", "COALESCE(r.sent_at,'')", "COALESCE(r.run_id,'')"},
		bodyExpr: "COALESCE(r.body_md,'')",
		id:       "r.id", idInt: true,
		build: func(ec *exportCtx, raw []string) exportRow {
			rep := reportFromRaw(raw)
			return exportRow{
				"id": atoi64(raw[0]), "symbol": raw[1], "name": raw[2], "date": raw[3], "kind": raw[4],
				"rtype": raw[5], "title": raw[6], "version": raw[7], "source": raw[8],
				"sent_at": ec.instant(raw[9], encRFC3339), "run_id": raw[10],
				"generator_version": generatorVersion(rep),
			}
		},
		gvValue: func(raw []string) string { return generatorVersion(reportFromRaw(raw)) },
	}
}

// reportFromRaw reads the reports select list back into a Rep (body excluded).
func reportFromRaw(raw []string) Rep {
	return Rep{ID: atoi64(raw[0]), Symbol: raw[1], Name: raw[2], Date: raw[3], Kind: raw[4], RType: raw[5],
		Title: raw[6], Version: raw[7], Source: raw[8], Time: raw[9], RunID: raw[10]}
}

// ---------- audit and sign-ins ----------

func auditActionOptions(*Server) []string {
	out := append([]string(nil), auditVocabulary...)
	sort.Strings(out)
	return out
}

func authActionOptions(*Server) []string {
	var out []string
	for _, a := range auditVocabulary {
		if strings.HasPrefix(a, "auth.") {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

var auditSelect = []string{"a.id", "COALESCE(a.at,'')", "COALESCE(a.actor,'')", "COALESCE(a.actor_ou,0)",
	"COALESCE(a.action,'')", "COALESCE(a.target_type,'')", "COALESCE(a.target_id,'')",
	"COALESCE(a.detail,'')", "COALESCE(a.ip,'')"}

func auditSource() *exportSource {
	return &exportSource{
		Key:     "audit",
		Formats: []string{"csv", "jsonl"},
		Fields: []exportField{
			timeField("at", "a.at", encRFC3339),
			textField("actor", "a.actor", opsText),
			enumField("action", "a.action", auditActionOptions),
			textField("target_type", "a.target_type", opsText),
			textField("target_id", "a.target_id", opsText),
			textField("ip", "a.ip", opsText),
			qField("a.detail", "a.target_id", "a.actor"),
		},
		Columns: []exportColumn{
			col("at", true), col("actor", true), col("action", true), col("target_type", true),
			col("target_id", true), col("ip", true), col("detail", true), col("actor_ou", false),
			col("country", false), col("region", false), col("city", false), ncol("id", false),
		},
		TimeField: "at",
		from:      "audit_log a", sel: auditSelect,
		k1: "a.at", id: "a.id", idInt: true,
		build: func(ec *exportCtx, raw []string) exportRow {
			row := exportRow{
				"id": atoi64(raw[0]), "at": ec.instant(raw[1], encRFC3339), "actor": raw[2],
				"action": raw[4], "target_type": raw[5], "target_id": raw[6], "detail": raw[7], "ip": raw[8],
			}
			if ou := raw[3]; ou != "0" && ou != "" {
				if name, ok := ec.ouNames[ou]; ok {
					row["actor_ou"] = name
				} else {
					row["actor_ou"] = ou
				}
			} else {
				row["actor_ou"] = ""
			}
			ec.geo(row, raw[8])
			return row
		},
	}
}

func signinsSource() *exportSource {
	// action >= 'auth.' AND action < 'auth/' is the index-friendly spelling of a prefix match
	// ('/' is the character after '.'), and it behaves the same on both drivers.
	return &exportSource{
		Key:     "signins",
		Formats: []string{"csv", "jsonl"},
		Fields: []exportField{
			timeField("at", "a.at", encRFC3339),
			textField("actor", "a.actor", opsText),
			enumField("action", "a.action", authActionOptions),
			textField("target_id", "a.target_id", opsText),
			textField("ip", "a.ip", opsText),
			qField("a.detail", "a.target_id", "a.actor"),
		},
		Columns: []exportColumn{
			col("at", true), col("actor", true), col("action", true), col("target_id", true), col("ip", true),
			col("method", true), col("browser", true), col("os", true), col("device_type", false),
			col("country", false), col("region", false), col("city", false), ncol("id", false),
		},
		TimeField: "at",
		from:      "audit_log a",
		base:      "a.action >= 'auth.' AND a.action < 'auth/'",
		sel:       auditSelect,
		k1:        "a.at", id: "a.id", idInt: true,
		build: func(ec *exportCtx, raw []string) exportRow {
			row := exportRow{
				"id": atoi64(raw[0]), "at": ec.instant(raw[1], encRFC3339), "actor": raw[2],
				"action": raw[4], "target_id": raw[6], "ip": raw[8],
			}
			var d struct {
				Method string     `json:"method"`
				Client ClientInfo `json:"client"`
			}
			if raw[7] != "" && json.Unmarshal([]byte(raw[7]), &d) == nil {
				row["method"] = d.Method
				row["browser"] = strings.TrimSpace(d.Client.Browser + " " + d.Client.BrowserVersion)
				row["os"] = strings.TrimSpace(d.Client.OS + " " + d.Client.OSVersion)
				row["device_type"] = d.Client.DeviceType
			} else {
				row["method"], row["browser"], row["os"], row["device_type"] = "", "", "", ""
			}
			ec.geo(row, raw[8])
			return row
		},
	}
}

// ---------- runs ----------

func runsSource() *exportSource {
	return &exportSource{
		Key:     "runs",
		Formats: []string{"csv", "jsonl"},
		Fields: []exportField{
			timeField("created_at", "b.created_at", encLocal),
			enumField("status", "b.status", staticOptions("queued", "running", "cancelling", "finished", "cancelled", "expired")),
			textField("created_by", "b.created_by", opsText),
			textField("target", "t.name", opsText),
			enumField("priority", "b.priority", staticOptions("normal", "urgent", "idle")),
		},
		Columns: []exportColumn{
			ncol("id", true), col("created_at", true), col("started_at", true), col("finished_at", true),
			col("status", true), col("created_by", true), col("target", true), col("priority", true),
			ncol("total", true), ncol("succeeded", true), ncol("partial", true), ncol("failed", true),
			col("run_at", false), ncol("concurrency", false), ncol("max_retries", false),
		},
		TimeField: "created_at",
		from:      "batch_jobs b LEFT JOIN batch_targets t ON t.id=b.target_id",
		sel: []string{"b.id", "COALESCE(b.created_at,'')", "COALESCE(b.started_at,'')", "COALESCE(b.finished_at,'')",
			"COALESCE(b.status,'')", "COALESCE(b.created_by,'')", "COALESCE(t.name,'')", "COALESCE(b.priority,'')",
			"COALESCE(b.total,0)", "COALESCE(b.succeeded,0)", "COALESCE(b.partial,0)", "COALESCE(b.failed,0)",
			"COALESCE(b.run_at,'')", "COALESCE(b.concurrency,0)", "COALESCE(b.max_retries,0)"},
		id: "b.id", idInt: true,
		build: func(ec *exportCtx, raw []string) exportRow {
			return exportRow{
				"id": atoi64(raw[0]), "created_at": ec.instant(raw[1], encLocal), "started_at": ec.instant(raw[2], encLocal),
				"finished_at": ec.instant(raw[3], encLocal), "status": raw[4], "created_by": raw[5], "target": raw[6],
				"priority": raw[7], "total": atoi64(raw[8]), "succeeded": atoi64(raw[9]), "partial": atoi64(raw[10]),
				"failed": atoi64(raw[11]), "run_at": ec.instant(raw[12], encLocal), "concurrency": atoi64(raw[13]),
				"max_retries": atoi64(raw[14]),
			}
		},
	}
}

// ---------- notification deliveries ----------

func notificationsSource() *exportSource {
	return &exportSource{
		Key:     "notifications",
		Formats: []string{"csv", "jsonl"},
		Fields: []exportField{
			timeField("created_at", "d.created_at", encMillis),
			enumField("state", "d.state", staticOptions("pending", "leased", "retry_wait", "accepted", "failed", "uncertain", "canceled", "expired")),
			textField("channel", "c.label", opsText),
			textField("route", "r.label", opsText),
			textField("username", "d.username", opsText),
			textField("event_type", "e.event_type", opsText),
			{Key: "status_code", Type: "number", Operators: opsNumber, col: "d.status_code", compile: compileDefault},
		},
		Columns: []exportColumn{
			col("created_at", true), col("state", true), col("channel", true), col("route", true),
			col("username", true), col("event_type", true), ncol("attempts", true), ncol("status_code", true),
			col("last_error", true), col("id", false),
		},
		TimeField: "created_at",
		from: "notification_deliveries d LEFT JOIN notification_channels c ON c.id=d.channel_id " +
			"LEFT JOIN notification_routes r ON r.id=d.route_id LEFT JOIN notification_events e ON e.id=d.event_id",
		sel: []string{"d.id", "d.created_at", "COALESCE(d.state,'')", "COALESCE(c.label,'')", "COALESCE(r.label,'')",
			"COALESCE(d.username,'')", "COALESCE(e.event_type,'')", "COALESCE(d.attempts,0)", "COALESCE(d.status_code,0)",
			"COALESCE(d.last_error,'')"},
		k1: "d.created_at", k1Int: true, id: "d.id",
		build: func(ec *exportCtx, raw []string) exportRow {
			return exportRow{
				"id": raw[0], "created_at": ec.instant(raw[1], encMillis), "state": raw[2], "channel": raw[3],
				"route": raw[4], "username": raw[5], "event_type": raw[6], "attempts": atoi64(raw[7]),
				"status_code": atoi64(raw[8]), "last_error": raw[9],
			}
		},
	}
}

// ---------- cleanup runs ----------

func compileCleanupOK(c *exportCompiler, f *exportField, op string, vals []string) (exportLeaf, error) {
	nums := make([]any, len(vals))
	for i, v := range vals {
		switch v {
		case "true":
			nums[i] = 1
		case "false":
			nums[i] = 0
		default:
			return exportLeaf{}, filterErr("%q takes true or false", f.Key)
		}
	}
	switch op {
	case opIs:
		return exportLeaf{sql: "COALESCE(c.ok,0) = ?", args: nums}, nil
	case opIsNot:
		return exportLeaf{sql: "COALESCE(c.ok,0) <> ?", args: nums}, nil
	case opNotIn:
		return exportLeaf{sql: "COALESCE(c.ok,0) NOT IN (" + placeholders(len(nums)) + ")", args: nums}, nil
	}
	return exportLeaf{sql: "COALESCE(c.ok,0) IN (" + placeholders(len(nums)) + ")", args: nums}, nil
}

func cleanupSource() *exportSource {
	counts := []string{"batch_deleted", "tokens_deleted", "reports_deleted", "audit_deleted", "revisions_deleted",
		"predictions_deleted", "bytes_reclaimed", "duration_ms"}
	cols := []exportColumn{col("ran_at", true), col("trigger", true), col("ok", true), col("error", true)}
	sel := []string{"c.id", "COALESCE(c.ran_at,'')", "COALESCE(c.trigger,'')", "COALESCE(c.ok,0)", "COALESCE(c.error,'')"}
	for _, k := range counts {
		cols = append(cols, ncol(k, true))
		sel = append(sel, "COALESCE(c."+k+",0)")
	}
	cols = append(cols, ncol("id", false))
	return &exportSource{
		Key:     "cleanup",
		Formats: []string{"csv", "jsonl"},
		Fields: []exportField{
			timeField("ran_at", "c.ran_at", encLocal),
			enumField("trigger", "c.trigger", staticOptions("schedule", "manual")),
			{Key: "ok", Type: "enum", Operators: opsEnum, compile: compileCleanupOK, options: staticOptions("true", "false")},
		},
		Columns:   cols,
		TimeField: "ran_at",
		from:      "cleanup_runs c",
		sel:       sel,
		id:        "c.id", idInt: true,
		build: func(ec *exportCtx, raw []string) exportRow {
			row := exportRow{"id": atoi64(raw[0]), "ran_at": ec.instant(raw[1], encLocal), "trigger": raw[2],
				"ok": strconv.FormatBool(raw[3] != "0"), "error": raw[4]}
			for i, k := range counts {
				row[k] = atoi64(raw[5+i])
			}
			return row
		},
	}
}
