package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func seedExportReport(t *testing.T, s *Server, r Rep) int64 {
	t.Helper()
	id, _, err := s.st.UpsertReport(r)
	if err != nil {
		t.Fatalf("UpsertReport: %v", err)
	}
	return id
}

func TestAdminReportExportListFiltersGeneratorVersion(t *testing.T) {
	s := newV1Server(t)
	seedExportReport(t, s, Rep{Symbol: "600001", Name: "Alpha", Date: "2026-10-18", RType: "Investment decision", Title: "600001 Investment decision V3.17", Source: "1-6-4 V3.17", MD: "# one"})
	seedExportReport(t, s, Rep{Symbol: "600002", Name: "Beta", Date: "2026-10-19", RType: "Investment decision", Title: "600002 Investment decision V3.16", Source: "1-6-4 V3.16.2", MD: "# two"})
	seedExportReport(t, s, Rep{Symbol: "600003", Name: "Gamma", Date: "2026-10-20", RType: "Industry", Title: "600003 Industry V3.17", Source: "worker V3.17", MD: "# three"})

	req := httptest.NewRequest("GET", "/api/admin/report-exports?rtype=Investment+decision&generator_version=V3.17&date_from=2026-10-18&page=1&page_size=20", nil)
	w := httptest.NewRecorder()
	s.apiAdminReportExports(w, req, "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Reports  []adminExportReport `json:"reports"`
		Total    int                 `json:"total"`
		Types    []string            `json:"types"`
		Versions []string            `json:"generator_versions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 1 || len(out.Reports) != 1 || out.Reports[0].Symbol != "600001" {
		t.Fatalf("unexpected filtered result: %+v", out)
	}
	if out.Reports[0].GeneratorVersion != "V3.17" {
		t.Fatalf("generator version=%q", out.Reports[0].GeneratorVersion)
	}
	if !containsString(out.Types, "Investment decision") || !containsString(out.Versions, "V3.16.2") || !containsString(out.Versions, "V3.17") {
		t.Fatalf("filter options missing: types=%v versions=%v", out.Types, out.Versions)
	}
}

func TestAdminReportExportZipSelectedMarkdown(t *testing.T) {
	s := newV1Server(t)
	id1 := seedExportReport(t, s, Rep{Symbol: "600001", Name: "Alpha", Date: "2026-10-18", RType: "Investment decision", Title: "600001 Investment decision V3.17", Source: "1-6-4 V3.17", MD: "# one"})
	id2 := seedExportReport(t, s, Rep{Symbol: "600002", Name: "Beta", Date: "2026-10-19", RType: "Investment decision", Title: "600002 Investment decision V3.17", Source: "1-6-4 V3.17", MD: "# two"})
	seedExportReport(t, s, Rep{Symbol: "600003", Name: "Gamma", Date: "2026-10-20", RType: "Industry", Title: "600003 Industry V3.17", Source: "worker V3.17", MD: "# excluded"})

	body := strings.NewReader(`{"ids":[` + jsonNumber(id2) + `,` + jsonNumber(id1) + `]}`)
	req := httptest.NewRequest("POST", "/api/admin/report-exports/download", body)
	w := httptest.NewRecorder()
	s.apiAdminReportExportDownload(w, req, "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("Content-Type=%q", got)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 2 {
		t.Fatalf("entries=%d, want 2", len(zr.File))
	}
	contents := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		contents[f.Name] = string(b)
	}
	if !mapContainsValue(contents, "# one") || !mapContainsValue(contents, "# two") || mapContainsValue(contents, "# excluded") {
		t.Fatalf("unexpected ZIP contents: %v", contents)
	}
}

func jsonNumber(n int64) string { return strconv.FormatInt(n, 10) }

func mapContainsValue(m map[string]string, want string) bool {
	for _, v := range m {
		if v == want {
			return true
		}
	}
	return false
}

func TestAdminReportExportZipByFiltersAndRejectsMixedSelection(t *testing.T) {
	s := newV1Server(t)
	id := seedExportReport(t, s, Rep{Symbol: "600001", Name: "Alpha", Date: "2026-10-18", RType: "Investment decision", Title: "600001 Investment decision V3.17", Source: "1-6-4 V3.17", MD: "# keep"})
	seedExportReport(t, s, Rep{Symbol: "600002", Name: "Beta", Date: "2026-10-19", RType: "Investment decision", Title: "600002 Investment decision V3.16", Source: "1-6-4 V3.16.2", MD: "# older"})

	req := httptest.NewRequest("POST", "/api/admin/report-exports/download", strings.NewReader(`{"filters":{"rtype":"Investment decision","generator_version":"v3.17"}}`))
	w := httptest.NewRecorder()
	s.apiAdminReportExportDownload(w, req, "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 || !strings.HasSuffix(zr.File[0].Name, "_"+jsonNumber(id)+".md") {
		t.Fatalf("unexpected entries: %d", len(zr.File))
	}

	req = httptest.NewRequest("POST", "/api/admin/report-exports/download", strings.NewReader(`{"ids":[`+jsonNumber(id)+`],"filters":{}}`))
	w = httptest.NewRecorder()
	s.apiAdminReportExportDownload(w, req, "admin")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("mixed selection status=%d, want 400", w.Code)
	}
}

func TestAdminReportExportAuditsOneEntryPerDownload(t *testing.T) {
	s := newV1Server(t)
	id1 := seedExportReport(t, s, Rep{Symbol: "600001", Name: "Alpha", Date: "2026-10-18", RType: "Investment decision", Title: "600001 Investment decision V3.17", Source: "1-6-4 V3.17", MD: "# one"})
	id2 := seedExportReport(t, s, Rep{Symbol: "600002", Name: "Beta", Date: "2026-10-19", RType: "Investment decision", Title: "600002 Investment decision V3.17", Source: "1-6-4 V3.17", MD: "# two"})

	req := httptest.NewRequest("POST", "/api/admin/report-exports/download", strings.NewReader(`{"ids":[`+jsonNumber(id1)+`,`+jsonNumber(id2)+`]}`))
	w := httptest.NewRecorder()
	s.apiAdminReportExportDownload(w, req, "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if _, reads := s.st.ListAudit(AuditFilter{Action: AuditReportRead}); reads != 0 {
		t.Fatalf("bulk export wrote %d report.read rows, want 0", reads)
	}
	rows, total := s.st.ListAudit(AuditFilter{Action: AuditExportDownload})
	if total != 1 {
		t.Fatalf("export.download rows=%d, want 1", total)
	}
	var detail struct {
		Source string  `json:"source"`
		Format string  `json:"format"`
		Count  int     `json:"count"`
		IDs    []int64 `json:"ids"`
	}
	if err := json.Unmarshal([]byte(rows[0].Detail), &detail); err != nil {
		t.Fatal(err)
	}
	if rows[0].Actor != "admin" || detail.Source != "reports" || detail.Format != "md_zip" || detail.Count != 2 || len(detail.IDs) != 2 {
		t.Fatalf("unexpected audit row: %+v detail=%+v", rows[0], detail)
	}
}
