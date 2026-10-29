package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// The export task API (ADR 0045). Every route is administrator-only and registered through s.admin.

const (
	exportPreviewDefaultSize = 50
	exportPreviewMaxSize     = 200
	exportListDefault        = 50
	exportListMax            = 200
	exportRequestBytes       = 1 << 20
	exportCancelWait         = 5 * time.Second
)

func decodeExportRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, exportRequestBytes)).Decode(v); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}

// filterStatus maps a compile/validation failure to its HTTP status: a client mistake is a 400, and
// anything else (a database failure) a 500.
func exportFail(w http.ResponseWriter, err error) {
	var fe *exportFilterError
	if errors.As(err, &fe) {
		jsonError(w, http.StatusBadRequest, fe.msg)
		return
	}
	jsonError(w, http.StatusInternalServerError, "database error")
}

// resolveExportColumns validates a requested column list against the source and falls back to the
// defaults. A report archive defaults to the front-matter fields rather than the CSV default set.
func resolveExportColumns(src *exportSource, requested []string, format string) ([]string, error) {
	if len(requested) == 0 {
		if src.Key == "reports" && format == "md_zip" {
			return append([]string(nil), frontMatterColumns...), nil
		}
		return src.defaultColumns(), nil
	}
	seen := map[string]bool{}
	for _, k := range requested {
		if src.column(k) == nil {
			return nil, filterErr("source %q has no column %q", src.Key, k)
		}
		if seen[k] {
			return nil, filterErr("column %q is listed twice", k)
		}
		seen[k] = true
	}
	return requested, nil
}

// ---------- GET /api/admin/exports/sources ----------

type exportFieldJSON struct {
	Key       string           `json:"key"`
	Type      string           `json:"type"`
	Operators []string         `json:"operators"`
	Options   []string         `json:"options,omitempty"`
	Tree      []exportKindTree `json:"tree,omitempty"`
}

func (s *Server) apiExportSources(w http.ResponseWriter, r *http.Request, _ string) {
	var tree []exportKindTree // built once: kind and rtype both carry it
	out := make([]map[string]any, 0, len(exportSourceList))
	for _, listed := range exportSourceList {
		src := s.exportView(listed)
		fields := make([]exportFieldJSON, 0, len(src.Fields))
		for _, f := range src.Fields {
			fj := exportFieldJSON{Key: f.Key, Type: f.Type, Operators: f.Operators}
			switch {
			case f.tree:
				if tree == nil {
					tree = s.reportKindTree()
				}
				fj.Tree = tree
				for _, k := range tree {
					if f.Key == "kind" {
						fj.Options = append(fj.Options, k.Kind)
					} else {
						fj.Options = append(fj.Options, k.Subtypes...)
					}
				}
			case f.options != nil:
				fj.Options = f.options(s)
			}
			fields = append(fields, fj)
		}
		cols := make([]map[string]any, 0, len(src.Columns))
		for _, c := range src.Columns {
			cols = append(cols, map[string]any{"key": c.Key, "default": c.Default})
		}
		var total int64
		if plan, err := s.newExportPlan(src, exportNode{}); err == nil {
			total, _ = plan.count(r.Context())
		}
		out = append(out, map[string]any{
			"key": src.Key, "formats": src.Formats, "fields": fields, "columns": cols,
			"time_field": src.TimeField, "total": total,
		})
	}
	writeJSON(w, map[string]any{"sources": out})
}

// ---------- POST /api/admin/exports/preview ----------

type exportPreviewRequest struct {
	Source   string     `json:"source"`
	Filter   exportNode `json:"filter"`
	Columns  []string   `json:"columns"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

func (s *Server) apiExportPreview(w http.ResponseWriter, r *http.Request, _ string) {
	var in exportPreviewRequest
	if !decodeExportRequest(w, r, &in) {
		return
	}
	src := exportSourceByKey(in.Source)
	if src == nil {
		jsonError(w, http.StatusBadRequest, "unknown export source")
		return
	}
	src = s.exportView(src)
	cols, err := resolveExportColumns(src, in.Columns, "")
	if err != nil {
		exportFail(w, err)
		return
	}
	plan, err := s.newExportPlan(src, in.Filter)
	if err != nil {
		exportFail(w, err)
		return
	}
	plan.withColumns(cols)
	size := in.PageSize
	if size <= 0 {
		size = exportPreviewDefaultSize
	}
	if size > exportPreviewMaxSize {
		size = exportPreviewMaxSize
	}
	page := in.Page
	if page < 1 {
		page = 1
	}
	total, err := plan.count(r.Context())
	if err != nil {
		exportFail(w, err)
		return
	}
	raws, err := plan.page(r.Context(), (page-1)*size, size)
	if err != nil {
		exportFail(w, err)
		return
	}
	ec := s.newExportCtx("utc", cols)
	ec.priceOf = plan.priceOf
	rows := make([]map[string]any, 0, len(raws))
	for _, raw := range raws {
		full := src.build(ec, raw)
		row := make(map[string]any, len(cols))
		for _, k := range cols {
			row[k] = full[k]
		}
		rows = append(rows, row)
	}
	writeJSON(w, map[string]any{"total": total, "rows": rows})
}

// ---------- POST /api/admin/exports ----------

type exportCreateRequest struct {
	Source  string         `json:"source"`
	Format  string         `json:"format"`
	Filter  exportNode     `json:"filter"`
	Columns []string       `json:"columns"`
	Name    string         `json:"name"`
	TZ      string         `json:"tz"`
	Options *exportOptions `json:"options"`
}

func (s *Server) apiExportCreate(w http.ResponseWriter, r *http.Request, user string) {
	var in exportCreateRequest
	if !decodeExportRequest(w, r, &in) {
		return
	}
	src := exportSourceByKey(in.Source)
	if src == nil {
		jsonError(w, http.StatusBadRequest, "unknown export source")
		return
	}
	src = s.exportView(src)
	if in.Format == "" {
		in.Format = src.Formats[0]
	}
	if !src.hasFormat(in.Format) {
		jsonError(w, http.StatusBadRequest, "format not available for this source")
		return
	}
	tz := in.TZ
	switch tz {
	case "":
		tz = "panel"
	case "panel", "utc":
	default:
		jsonError(w, http.StatusBadRequest, "tz must be panel or utc")
		return
	}
	cols, err := resolveExportColumns(src, in.Columns, in.Format)
	if err != nil {
		exportFail(w, err)
		return
	}
	var opts exportOptions
	if in.Options != nil && in.Format == "md_zip" {
		opts = *in.Options
		switch opts.Layout {
		case "":
			opts.Layout = "flat"
		case "flat", "date", "symbol":
		default:
			jsonError(w, http.StatusBadRequest, "layout must be flat, date or symbol")
			return
		}
	}
	plan, err := s.newExportPlan(src, in.Filter)
	if err != nil {
		exportFail(w, err)
		return
	}
	estimate, err := plan.count(r.Context())
	if err != nil {
		exportFail(w, err)
		return
	}
	name, file := s.exportFileName(src.Key, in.Format, in.Name)
	params := exportParams{Name: name, Filter: normalizeExportFilter(in.Filter), Columns: cols, TZ: tz, Options: opts}
	raw, err := json.Marshal(params)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not encode the export")
		return
	}
	j := &exportJob{
		ID: newExportID(), Source: src.Key, Format: in.Format, Params: string(raw), RequestedBy: user,
		Status: exportQueued, RowsEstimate: estimate, FileName: file, CreatedAt: s.exports.clock().UnixMilli(),
	}
	if err := s.st.insertExportJob(j); err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	// One row per submission, whatever the size of the export: reading the exported rows is not a
	// per-row event.
	s.recordChange(r, user, AuditExportCreate, "export", j.ID, map[string]any{
		"source": src.Key, "format": j.Format, "filter": params.Filter, "columns": cols, "estimate": estimate,
	})
	select {
	case s.exports.wakeCh() <- struct{}{}:
	default:
	}
	writeJSONStatus(w, http.StatusCreated, j.view())
}

// ---------- GET /api/admin/exports, /{id}, cancel ----------

func (s *Server) apiExportList(w http.ResponseWriter, r *http.Request, _ string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = exportListDefault
	}
	if limit > exportListMax {
		limit = exportListMax
	}
	active := r.URL.Query().Get("active") == "1"
	jobs, err := s.st.listExportJobs(limit, active)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	views := make([]exportJobJSON, 0, len(jobs))
	for _, j := range jobs {
		views = append(views, j.view())
	}
	writeJSON(w, map[string]any{"jobs": views})
}

func (s *Server) apiExportGet(w http.ResponseWriter, r *http.Request, _ string) {
	j := s.st.getExportJob(r.PathValue("id"))
	if j == nil {
		jsonError(w, http.StatusNotFound, "export not found")
		return
	}
	writeJSON(w, j.view())
}

func (s *Server) apiExportCancel(w http.ResponseWriter, r *http.Request, _ string) {
	id := r.PathValue("id")
	j := s.st.getExportJob(id)
	if j == nil {
		jsonError(w, http.StatusNotFound, "export not found")
		return
	}
	switch j.Status {
	case exportQueued:
		// The conditional UPDATE is the cancel: a worker that claimed the job first wins, and the
		// re-read below then sees it running.
		s.st.exec(`UPDATE export_jobs SET status='canceled',finished_at=? WHERE id=? AND status='queued'`,
			s.exports.clock().UnixMilli(), id)
	case exportRunning:
		if s.exports.cancelRunning(id) {
			// The worker stops at the next batch boundary, removes its partial file and records the
			// state; wait for that briefly so the answer is the final one.
			deadline := time.Now().Add(exportCancelWait)
			for time.Now().Before(deadline) {
				if cur := s.st.getExportJob(id); cur == nil || cur.Status != exportRunning {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		} else {
			// Marked running but no live worker owns it (a row restored from a backup): nothing is
			// writing, so closing it out is safe.
			s.st.exec(`UPDATE export_jobs SET status='canceled',finished_at=? WHERE id=? AND status='running'`,
				s.exports.clock().UnixMilli(), id)
		}
	default:
		jsonError(w, http.StatusConflict, "export already finished")
		return
	}
	cur := s.st.getExportJob(id)
	if cur == nil {
		jsonError(w, http.StatusNotFound, "export not found")
		return
	}
	writeJSON(w, cur.view())
}

// ---------- GET /api/admin/exports/{id}/file ----------

var exportContentTypes = map[string]string{
	"csv":    "text/csv; charset=utf-8",
	"jsonl":  "application/x-ndjson",
	"md_zip": "application/zip",
}

func (s *Server) apiExportFile(w http.ResponseWriter, r *http.Request, user string) {
	j := s.st.getExportJob(r.PathValue("id"))
	if j == nil {
		jsonError(w, http.StatusNotFound, "export not found")
		return
	}
	switch j.Status {
	case exportExpired:
		jsonError(w, http.StatusGone, "export expired")
		return
	case exportDone:
	default:
		jsonError(w, http.StatusConflict, "export is "+j.Status+": there is no file to download")
		return
	}
	path := s.exportPath(j)
	// Expiry is enforced here as well as by the hourly sweep, so a file never outlives its window
	// by up to an hour.
	if j.ExpiresAt > 0 && s.exports.clock().UnixMilli() >= j.ExpiresAt {
		_ = os.Remove(path)
		s.st.exec(`UPDATE export_jobs SET status='expired' WHERE id=? AND status='done'`, j.ID)
		jsonError(w, http.StatusGone, "export expired")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.st.exec(`UPDATE export_jobs SET status='expired' WHERE id=? AND status='done'`, j.ID)
			jsonError(w, http.StatusGone, "the export file is no longer available")
			return
		}
		jsonError(w, http.StatusInternalServerError, "could not open the export file")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not open the export file")
		return
	}
	// A resumed download re-requests ranges of one file: only the first request is a download event.
	if rng := r.Header.Get("Range"); rng == "" || strings.HasPrefix(rng, "bytes=0-") {
		s.recordChange(r, user, AuditExportDownload, "export", j.ID, map[string]any{
			"source": j.Source, "format": j.Format, "rows": j.RowsDone, "bytes": info.Size(),
		})
	}
	w.Header().Set("Content-Type", exportContentTypes[j.Format])
	w.Header().Set("Content-Disposition", exportDisposition(j.FileName))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, j.FileName, info.ModTime(), f)
}

// exportDisposition is an attachment header with an ASCII fallback and the exact UTF-8 name (RFC 5987).
func exportDisposition(name string) string {
	var fallback strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' {
			fallback.WriteByte('_')
		} else {
			fallback.WriteRune(r)
		}
	}
	return `attachment; filename="` + fallback.String() + `"; filename*=UTF-8''` + url.PathEscape(name)
}
