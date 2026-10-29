package app

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Bulk report export is read-only and deliberately separate from the retention console: it lets an
// administrator hand report bodies to an offline tool or an archive. It changes nothing and adds
// no persistence of its own.

const (
	adminExportDefaultPageSize = 50
	adminExportMaxPageSize     = 200
	adminExportMaxReports      = 20000
	adminExportRequestBytes    = 1 << 20
)

type adminReportExportFilter struct {
	Q                string `json:"q"`
	RType            string `json:"rtype"`
	GeneratorVersion string `json:"generator_version"`
	DateFrom         string `json:"date_from"`
	DateTo           string `json:"date_to"`
}

type adminExportReport struct {
	ID               int64  `json:"id"`
	Title            string `json:"title"`
	Symbol           string `json:"symbol"`
	Name             string `json:"name"`
	RType            string `json:"rtype"`
	Date             string `json:"date"`
	GeneratorVersion string `json:"generator_version"`
	SentAt           string `json:"sent_at"`
}

func normalizeExportFilter(f adminReportExportFilter) adminReportExportFilter {
	f.Q = strings.TrimSpace(f.Q)
	f.RType = strings.TrimSpace(f.RType)
	f.GeneratorVersion = strings.TrimSpace(f.GeneratorVersion)
	f.DateFrom = strings.TrimSpace(f.DateFrom)
	f.DateTo = strings.TrimSpace(f.DateTo)
	return f
}

func exportFilterFromQuery(r *http.Request) adminReportExportFilter {
	q := r.URL.Query()
	return normalizeExportFilter(adminReportExportFilter{
		Q: q.Get("q"), RType: q.Get("rtype"), GeneratorVersion: q.Get("generator_version"),
		DateFrom: q.Get("date_from"), DateTo: q.Get("date_to"),
	})
}

// adminExportReports resolves the database-supported filters first, then the producer-version
// suffix. Generator provenance is derived from source/title for historical compatibility and is not
// a column, so it cannot be truthfully filtered in SQL.
func (s *Server) adminExportReports(f adminReportExportFilter) ([]Rep, error) {
	f = normalizeExportFilter(f)
	reps, err := s.st.SearchNew(Filters{Q: f.Q, RType: f.RType, DateFrom: f.DateFrom, DateTo: f.DateTo}, nil)
	if err != nil {
		return nil, err
	}
	if f.GeneratorVersion == "" {
		return reps, nil
	}
	out := make([]Rep, 0, len(reps))
	for _, rep := range reps {
		if strings.EqualFold(generatorVersion(rep), f.GeneratorVersion) {
			out = append(out, rep)
		}
	}
	return out, nil
}

func (s *Server) apiAdminReportExports(w http.ResponseWriter, r *http.Request, _ string) {
	all, err := s.st.SearchNew(Filters{}, nil)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	typeSet, versionSet := map[string]bool{}, map[string]bool{}
	for _, rep := range all {
		if rep.RType != "" {
			typeSet[rep.RType] = true
		}
		if v := generatorVersion(rep); v != "" {
			versionSet[v] = true
		}
	}

	filtered, err := s.adminExportReports(exportFilterFromQuery(r))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	page := queryPositiveInt(r, "page", 1)
	pageSize := queryPositiveInt(r, "page_size", adminExportDefaultPageSize)
	if pageSize > adminExportMaxPageSize {
		pageSize = adminExportMaxPageSize
	}
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	rows := make([]adminExportReport, 0, end-start)
	for i := start; i < end; i++ {
		rep := filtered[i]
		rows = append(rows, adminExportReport{ID: rep.ID, Title: s.repDisplayTitle(&rep), Symbol: rep.Symbol,
			Name: rep.Name, RType: rep.RType, Date: rep.Date, GeneratorVersion: generatorVersion(rep), SentAt: rep.Time})
	}
	writeJSON(w, map[string]any{
		"reports": rows, "total": len(filtered), "page": page, "page_size": pageSize,
		"types": keysOf(typeSet), "generator_versions": keysOf(versionSet),
	})
}

func queryPositiveInt(r *http.Request, key string, fallback int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

type adminReportExportDownload struct {
	IDs     []int64                  `json:"ids"`
	Filters *adminReportExportFilter `json:"filters"`
}

func (s *Server) apiAdminReportExportDownload(w http.ResponseWriter, r *http.Request, user string) {
	var in adminReportExportDownload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, adminExportRequestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(in.IDs) > 0 && in.Filters != nil {
		jsonError(w, http.StatusBadRequest, "choose report ids or filters, not both")
		return
	}
	var meta []Rep
	var err error
	if len(in.IDs) > 0 {
		if len(in.IDs) > adminExportMaxReports {
			jsonError(w, http.StatusBadRequest, "too many reports")
			return
		}
		seen := map[int64]bool{}
		for _, id := range in.IDs {
			if id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			rep, getErr := s.st.GetNew(id, nil)
			if getErr != nil {
				err = getErr
				break
			}
			if rep == nil {
				jsonError(w, http.StatusNotFound, "report not found")
				return
			}
			meta = append(meta, *rep)
		}
	} else if in.Filters != nil {
		meta, err = s.adminExportReports(*in.Filters)
	} else {
		jsonError(w, http.StatusBadRequest, "report selection required")
		return
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	if len(meta) == 0 {
		jsonError(w, http.StatusNotFound, "no reports match")
		return
	}
	if len(meta) > adminExportMaxReports {
		jsonError(w, http.StatusBadRequest, "too many reports")
		return
	}

	// Filter-based selections carry metadata only; fetch every body before sending headers so a
	// database error cannot turn an HTTP error into a corrupt half-ZIP.
	reps := make([]Rep, 0, len(meta))
	for _, item := range meta {
		if item.MD != "" || item.HTML != "" {
			reps = append(reps, item)
			continue
		}
		full, getErr := s.st.GetNew(item.ID, nil)
		if getErr != nil {
			jsonError(w, http.StatusInternalServerError, "database error")
			return
		}
		if full != nil {
			reps = append(reps, *full)
		}
	}
	if len(reps) == 0 {
		jsonError(w, http.StatusNotFound, "no reports match")
		return
	}
	sort.SliceStable(reps, func(i, j int) bool {
		if reps[i].Date != reps[j].Date {
			return reps[i].Date > reps[j].Date
		}
		if reps[i].Symbol != reps[j].Symbol {
			return reps[i].Symbol < reps[j].Symbol
		}
		return reps[i].ID < reps[j].ID
	})

	fn := fmt.Sprintf("reports_markdown_%s_%d.zip", reps[len(reps)-1].Date, len(reps))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.QueryEscape(fn))
	zw := zip.NewWriter(w)
	for _, rep := range reps {
		base := strings.Join(nonEmptyStrings(rep.Date, rep.Symbol, s.repDisplayTitle(&rep), generatorVersion(rep), strconv.FormatInt(rep.ID, 10)), "_")
		name := truncateUTF8Bytes(sanitizeFilename(base), 240-len(".md")) + ".md"
		fw, createErr := zw.Create(name)
		if createErr != nil {
			log.Printf("report export: create %d: %v", rep.ID, createErr)
			continue
		}
		if _, writeErr := fw.Write([]byte(rep.MD)); writeErr != nil {
			log.Printf("report export: write %d: %v", rep.ID, writeErr)
		}
	}
	if err := zw.Close(); err != nil {
		log.Printf("report export: close zip: %v", err)
	}
	s.recordChange(r, user, AuditExportDownload, "export", "", exportAuditDetail(reps, in.Filters))
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func truncateUTF8Bytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// exportAuditIDCap bounds the id list kept on the audit row; the count is always exact.
const exportAuditIDCap = 500

func exportAuditDetail(reps []Rep, filters *adminReportExportFilter) map[string]any {
	ids := make([]int64, 0, min(len(reps), exportAuditIDCap))
	for i := 0; i < len(reps) && i < exportAuditIDCap; i++ {
		ids = append(ids, reps[i].ID)
	}
	detail := map[string]any{"source": "reports", "format": "md_zip", "count": len(reps), "ids": ids}
	if len(reps) > exportAuditIDCap {
		detail["ids_truncated"] = true
	}
	if filters != nil {
		detail["filters"] = normalizeExportFilter(*filters)
	}
	return detail
}
