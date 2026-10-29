package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Reader report export (ADR 0046): the ADR 0045 engine behind session-authenticated endpoints, with
// the reader's read scope applied everywhere and three per-OU limits enforced on submit.

var (
	errExportDisabled   = errors.New("export_disabled")
	errExportCapReached = errors.New("export cap reached")
)

const (
	exportMaxReportsBound = 5000
	exportDailyQuotaBound = 100000
)

// exportPolicy is a user's resolved reader export policy. Administrators are not governed by it: they
// are reported as allowed and unlimited, and use the admin console.
type exportPolicy struct {
	Allowed    bool
	MaxReports int
	DailyQuota int
	Unlimited  bool
}

// readerExportPolicy resolves the policy for a user from one OU snapshot.
func (s *Store) readerExportPolicy(user string) (exportPolicy, error) {
	sub, snap, err := s.loadOUPolicySubject(user)
	if err != nil {
		return exportPolicy{}, err
	}
	if !sub.Active {
		return exportPolicy{}, nil
	}
	if can(sub.Role, PermManage) {
		return exportPolicy{Allowed: true, Unlimited: true}, nil
	}
	p, err := snap.resolve(sub.EffectiveOU)
	if err != nil {
		return exportPolicy{}, err
	}
	return exportPolicy{Allowed: p.ExportAllowed.Effective, MaxReports: p.ExportMax.Effective,
		DailyQuota: p.ExportQuota.Effective}, nil
}

// exportDayBounds is the current panel-timezone civil day as [from, to) unix milliseconds.
func (s *Server) exportDayBounds() (from, to int64) {
	now := s.exports.clock().In(s.panelLocation())
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return start.UnixMilli(), start.AddDate(0, 0, 1).UnixMilli()
}

// exportUsage is what a user's jobs created in [from, to) are charged: the estimate while a job is
// queued or running, and what it actually wrote once it has ended, whatever way it ended.
func (s *Store) exportUsage(ex execer, user string, from, to int64) (int64, error) {
	var n sql.NullInt64
	err := ex.QueryRow(s.bind(`SELECT SUM(CASE WHEN status IN ('queued','running') THEN rows_estimate ELSE rows_done END)
		FROM export_jobs WHERE requested_by=? AND created_at>=? AND created_at<?`), user, from, to).Scan(&n)
	return n.Int64, err
}

// exportRefusal answers a reader-export refusal. The error text is the stable code itself (the
// contract in ADR 0046) and code repeats it under the key the SPA translates through.
func exportRefusal(w http.ResponseWriter, status int, code string, extra map[string]any) {
	body := map[string]any{"error": code, "code": code}
	for k, v := range extra {
		body[k] = v
	}
	writeJSONStatus(w, status, body)
}

func (s *Server) exportDisabled(w http.ResponseWriter) {
	exportRefusal(w, http.StatusForbidden, "export_disabled", nil)
}

// readerGate resolves the caller's policy and answers 403 export_disabled when it does not allow
// export. ok is false once an answer has been written.
func (s *Server) readerGate(w http.ResponseWriter, user string) (exportPolicy, bool) {
	pol, err := s.st.readerExportPolicy(user)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return pol, false
	}
	if !pol.Allowed {
		s.exportDisabled(w)
		return pol, false
	}
	return pol, true
}

// ---------- GET /api/exports/policy ----------

func (s *Server) apiReaderExportPolicy(w http.ResponseWriter, _ *http.Request, user string) {
	pol, err := s.st.readerExportPolicy(user)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	var used int64
	if pol.Allowed && !pol.Unlimited {
		from, to := s.exportDayBounds()
		if used, err = s.st.exportUsage(s.st.db, user, from, to); err != nil {
			jsonError(w, http.StatusInternalServerError, "database error")
			return
		}
	}
	remaining := int64(pol.DailyQuota) - used
	if remaining < 0 {
		remaining = 0
	}
	writeJSON(w, map[string]any{
		"allowed": pol.Allowed, "max_reports": pol.MaxReports, "daily_quota": pol.DailyQuota,
		"used_today": used, "remaining": remaining, "unlimited": pol.Unlimited,
	})
}

// ---------- sources, preview ----------

func (s *Server) apiReaderExportSources(w http.ResponseWriter, r *http.Request, user string) {
	if s.isAdmin(user) {
		s.apiExportSources(w, r, user)
		return
	}
	if _, ok := s.readerGate(w, user); !ok {
		return
	}
	writeJSON(w, map[string]any{"sources": s.exportSourceDocs(r, s.viewerScope(user), true)})
}

func (s *Server) apiReaderExportPreview(w http.ResponseWriter, r *http.Request, user string) {
	if s.isAdmin(user) {
		s.apiExportPreview(w, r, user)
		return
	}
	if _, ok := s.readerGate(w, user); !ok {
		return
	}
	s.serveExportPreview(w, r, s.viewerScope(user), true)
}

// ---------- POST /api/exports ----------

func (s *Server) apiReaderExportCreate(w http.ResponseWriter, r *http.Request, user string) {
	if s.isAdmin(user) {
		s.apiExportCreate(w, r, user)
		return
	}
	pol, ok := s.readerGate(w, user)
	if !ok {
		return
	}
	var in exportCreateRequest
	if !decodeExportRequest(w, r, &in) {
		return
	}
	if in.Source != "reports" {
		jsonError(w, http.StatusBadRequest, "readers can export the reports source only")
		return
	}
	if in.Format == "" {
		in.Format = "md_zip"
	}
	if in.Format != "md_zip" {
		jsonError(w, http.StatusBadRequest, "readers can export Markdown archives only")
		return
	}
	src := s.exportView(exportSourceByKey("reports"))
	tz, ok := resolveExportTZ(in.TZ)
	if !ok {
		jsonError(w, http.StatusBadRequest, "tz must be panel or utc")
		return
	}
	opts, msg := resolveExportOptions(in.Options, in.Format)
	if msg != "" {
		jsonError(w, http.StatusBadRequest, msg)
		return
	}
	// No column choice: a reader archive always carries the front-matter fields.
	cols, err := resolveExportColumns(src, nil, in.Format)
	if err != nil {
		exportFail(w, err)
		return
	}
	plan, err := s.newExportPlan(src, in.Filter)
	if err != nil {
		exportFail(w, err)
		return
	}
	// The id bound is read before the count and applied to it, so what is counted, charged and later
	// exported are the same set however many reports arrive in between.
	var maxID int64
	if err := s.st.queryRow(`SELECT COALESCE(MAX(id),0) FROM reports`).Scan(&maxID); err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	plan.withReaderLimits(s.viewerScope(user), maxID)
	count, err := plan.count(r.Context())
	if err != nil {
		exportFail(w, err)
		return
	}
	if count == 0 {
		exportRefusal(w, http.StatusBadRequest, "export_empty", nil)
		return
	}
	if count > int64(pol.MaxReports) {
		exportRefusal(w, http.StatusUnprocessableEntity, "export_too_large", map[string]any{"limit": pol.MaxReports, "count": count})
		return
	}
	name, file := s.exportFileName(src.Key, in.Format, in.Name)
	params := exportParams{Name: name, Filter: normalizeExportFilter(in.Filter), Columns: cols, TZ: tz,
		Options: opts, Reader: true, Cap: count, MaxID: maxID}
	raw, err := json.Marshal(params)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not encode the export")
		return
	}
	j := &exportJob{
		ID: newExportID(), Source: src.Key, Format: in.Format, Params: string(raw), RequestedBy: user,
		Status: exportQueued, RowsEstimate: count, FileName: file, CreatedAt: s.exports.clock().UnixMilli(),
	}
	used, err := s.submitReaderExport(j, count, int64(pol.DailyQuota))
	switch {
	case errors.Is(err, errExportQuota):
		exportRefusal(w, http.StatusTooManyRequests, "export_quota", map[string]any{"limit": pol.DailyQuota, "used": used, "count": count})
		return
	case err != nil:
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	s.recordChange(r, user, AuditExportCreate, "export", j.ID, map[string]any{
		"source": src.Key, "format": j.Format, "filter": params.Filter, "columns": cols, "estimate": count,
		"scope": "reader",
	})
	select {
	case s.exports.wakeCh() <- struct{}{}:
	default:
	}
	writeJSONStatus(w, http.StatusCreated, j.view())
}

var errExportQuota = errors.New("export quota exceeded")

// submitReaderExport checks the daily quota and inserts the job as one step serialised per
// requester, so two submits that each fit but together do not cannot both pass. On Postgres the
// requester's user row is locked for the transaction; SQLite has a single connection, so the
// transaction itself is the lock. Only the transaction is used inside it. On both drivers the read
// also proves the account still exists: a requester deleted after authentication inserts nothing,
// so no row outlives the account to be inherited by the next holder of the name.
func (s *Server) submitReaderExport(j *exportJob, count, quota int64) (used int64, err error) {
	from, to := s.exportDayBounds()
	tx, err := s.st.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	lock := `SELECT username FROM users WHERE username=?`
	if s.st.driver == "postgres" {
		lock += ` FOR UPDATE`
	}
	var u string
	if err := tx.QueryRow(s.st.bind(lock), j.RequestedBy).Scan(&u); err != nil {
		return 0, err
	}
	used, err = s.st.exportUsage(tx, j.RequestedBy, from, to)
	if err != nil {
		return 0, err
	}
	if used+count > quota {
		return used, errExportQuota
	}
	if _, err := s.st.insertExportJobOn(tx, j); err != nil {
		return 0, err
	}
	return used, tx.Commit()
}

// ---------- own tasks ----------

// ownsExport answers 404 for a task that is not the caller's, the same answer as for an id that does
// not exist, so ids do not leak existence. Only a job submitted through the reader endpoints is ever
// a reader's: a non-reader row under the same name (an administrator's, left by a deletion racing
// its submit) stays out of reach. An administrator is answered from the admin rules.
func (s *Server) ownsExport(w http.ResponseWriter, r *http.Request, user string) bool {
	if s.isAdmin(user) {
		return true
	}
	if j := s.st.getExportJob(r.PathValue("id")); j == nil || j.RequestedBy != user || !j.isReader() {
		jsonError(w, http.StatusNotFound, "export not found")
		return false
	}
	return true
}

func (s *Server) apiReaderExportList(w http.ResponseWriter, r *http.Request, user string) {
	owner := user
	if s.isAdmin(user) {
		owner = ""
	}
	s.serveExportList(w, r, owner)
}

func (s *Server) apiReaderExportGet(w http.ResponseWriter, r *http.Request, user string) {
	if s.ownsExport(w, r, user) {
		s.apiExportGet(w, r, user)
	}
}

func (s *Server) apiReaderExportCancel(w http.ResponseWriter, r *http.Request, user string) {
	if s.ownsExport(w, r, user) {
		s.apiExportCancel(w, r, user)
	}
}

func (s *Server) apiReaderExportFile(w http.ResponseWriter, r *http.Request, user string) {
	if !s.ownsExport(w, r, user) {
		return
	}
	if s.isAdmin(user) {
		s.apiExportFile(w, r, user)
		return
	}
	// A file produced before export was switched off is not served after it.
	if _, ok := s.readerGate(w, user); !ok {
		return
	}
	s.serveExportFile(w, r, user, "reader")
}

// ---------- admin policy console ----------

type exportPolicyEntry struct {
	ID         int64                `json:"id"`
	Name       string               `json:"name"`
	ParentID   int64                `json:"parent_id"`
	IsDefault  bool                 `json:"is_default"`
	Allowed    ouPolicyResult[bool] `json:"allowed"`
	MaxReports ouPolicyResult[int]  `json:"max_reports"`
	DailyQuota ouPolicyResult[int]  `json:"daily_quota"`
}

func exportPolicyEntries(snap ouPolicySnapshot) []exportPolicyEntry {
	out := make([]exportPolicyEntry, 0, len(snap.graph.nodes))
	for id, node := range snap.graph.nodes {
		e := exportPolicyEntry{ID: id, Name: node.name, ParentID: node.parent, IsDefault: node.isDefault}
		if p, err := snap.resolve(id); err == nil {
			e.Allowed, e.MaxReports, e.DailyQuota = p.ExportAllowed, p.ExportMax, p.ExportQuota
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b exportPolicyEntry) int {
		if a.IsDefault != b.IsDefault {
			if a.IsDefault {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func (s *Server) apiAdminExportPolicy(w http.ResponseWriter, _ *http.Request, _ string) {
	snap, err := s.st.loadOUPolicySnapshotFrom(s.st.db)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, map[string]any{"groups": exportPolicyEntries(snap)})
}

type exportPolicyInput struct {
	Allowed    json.RawMessage `json:"allowed"`
	MaxReports json.RawMessage `json:"max_reports"`
	DailyQuota json.RawMessage `json:"daily_quota"`
}

// exportPolicyChange is one column to write: a nil value is NULL (inherit).
type exportPolicyChange struct {
	field, column string
	value         any
}

func isJSONNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// parseExportPolicyInput turns the tri-state body into column writes. Absent fields are unchanged,
// null is inherit (refused for the Default OU, the root of inheritance), a value is an override.
func parseExportPolicyInput(in exportPolicyInput, isDefault bool) ([]exportPolicyChange, string) {
	var out []exportPolicyChange
	add := func(field, column string, raw json.RawMessage, lo, hi int, boolean bool) string {
		if len(raw) == 0 {
			return ""
		}
		if isJSONNull(raw) {
			if isDefault {
				return field + " cannot inherit on the Default OU"
			}
			out = append(out, exportPolicyChange{field, column, nil})
			return ""
		}
		if boolean {
			var b bool
			if json.Unmarshal(raw, &b) != nil {
				return field + " must be true, false or null"
			}
			out = append(out, exportPolicyChange{field, column, boolInt(b)})
			return ""
		}
		var n int
		if json.Unmarshal(raw, &n) != nil || n < lo || n > hi {
			return field + " is out of range"
		}
		out = append(out, exportPolicyChange{field, column, n})
		return ""
	}
	for _, step := range []struct {
		field, column string
		raw           json.RawMessage
		lo, hi        int
		boolean       bool
	}{
		{"allowed", "export_allowed", in.Allowed, 0, 0, true},
		{"max_reports", "export_max_reports", in.MaxReports, 1, exportMaxReportsBound, false},
		{"daily_quota", "export_daily_quota", in.DailyQuota, 1, exportDailyQuotaBound, false},
	} {
		if msg := add(step.field, step.column, step.raw, step.lo, step.hi, step.boolean); msg != "" {
			return nil, msg
		}
	}
	return out, ""
}

func (s *Server) apiAdminExportPolicySave(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "ou")
	var in exportPolicyInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	var audits []map[string]any
	var after ouPolicySnapshot
	notFound, invalid := false, ""
	err := s.st.withOUMutation(func(e migExec) error {
		audits, notFound, invalid = nil, false, ""
		snap, err := s.st.loadOUPolicySnapshotFrom(e.ex)
		if err != nil {
			return err
		}
		node, ok := snap.graph.nodes[id]
		if !ok {
			notFound = true
			return nil
		}
		changes, msg := parseExportPolicyInput(in, node.isDefault)
		if msg != "" {
			invalid = msg
			return nil
		}
		before := snap.values[id]
		for _, c := range changes {
			from := exportPolicyFrom(before, c.field)
			if equalPolicyValue(from, c.value) {
				continue
			}
			if _, err := e.exec(`UPDATE user_groups SET `+c.column+`=? WHERE id=?`, c.value, id); err != nil {
				return err
			}
			audits = append(audits, map[string]any{"field": c.field, "from": auditPolicyValue(c.field, from),
				"to": auditPolicyValue(c.field, c.value)})
		}
		after, err = s.st.loadOUPolicySnapshotFrom(e.ex)
		return err
	})
	switch {
	case err != nil:
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	case notFound:
		jsonError(w, http.StatusNotFound, "group not found")
		return
	case invalid != "":
		jsonError(w, http.StatusBadRequest, invalid)
		return
	}
	for _, detail := range audits {
		s.recordChange(r, user, AuditGroupChange, "group", itoa64(id), detail)
	}
	for _, e := range exportPolicyEntries(after) {
		if e.ID == id {
			writeJSON(w, e)
			return
		}
	}
}

// exportPolicyFrom reads an OU's own value of one policy field as it is stored (nil = inherit), in the
// same shape the writer binds, so before and after compare and audit alike.
func exportPolicyFrom(v ouPolicyValues, field string) any {
	switch field {
	case "allowed":
		if v.exportAllowed != nil {
			return boolInt(*v.exportAllowed)
		}
	case "max_reports":
		if v.exportMax != nil {
			return *v.exportMax
		}
	case "daily_quota":
		if v.exportDailyQuota != nil {
			return *v.exportDailyQuota
		}
	}
	return nil
}

func equalPolicyValue(a, b any) bool { return a == b }

// auditPolicyValue reports the switch as a boolean in the audit row, though it is stored as 0 or 1.
func auditPolicyValue(field string, v any) any {
	if n, ok := v.(int); ok && field == "allowed" {
		return n != 0
	}
	return v
}
