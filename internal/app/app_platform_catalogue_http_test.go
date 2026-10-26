package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func testPlatformStagedCatalogueHTTP(t *testing.T, pg bool) {
	s, mux, actor, release, cookie := platformManagementFixture(t, pg)
	got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/releases", cookie, "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), release) || !strings.Contains(got.Body.String(), `"installable":true`) {
		t.Fatal("verified release missing", got.Code, got.Body.String())
	}
	for _, kind := range []string{"user", "ou"} {
		got = sessionTestRequest(mux, "GET", "/api/admin/app-platform/subjects?kind="+kind, cookie, "")
		if got.Code != 200 || (kind == "user" && !strings.Contains(got.Body.String(), actor.username)) {
			t.Fatal("audience choices unavailable", kind, got.Code, got.Body.String())
		}
	}
	for _, path := range []string{"/api/admin/app-platform/releases?debug=1", "/api/admin/app-platform/releases?after=bad", "/api/admin/app-platform/subjects?kind=all", "/api/admin/app-platform/subjects?kind=user&kind=ou", "/api/admin/app-platform/subjects?kind=ou&after=01"} {
		if got := sessionTestRequest(mux, "GET", path, cookie, ""); got.Code != 400 {
			t.Fatal("invalid list request accepted", path, got.Code)
		}
	}
	mustExec(t, s.st, `UPDATE app_platform_releases SET signature=? WHERE sha256=?`, []byte("invalid"), release)
	got = sessionTestRequest(mux, "GET", "/api/admin/app-platform/releases", cookie, "")
	if got.Code != 200 || strings.Contains(got.Body.String(), release) {
		t.Fatal("tampered release advertised", got.Code, got.Body.String())
	}
	if got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/releases", "", ""); got.Code != 401 {
		t.Fatal("anonymous catalogue", got.Code)
	}
}

func TestPlatformStagedCatalogueHTTPSQLite(t *testing.T)   { testPlatformStagedCatalogueHTTP(t, false) }
func TestPlatformStagedCatalogueHTTPPostgres(t *testing.T) { testPlatformStagedCatalogueHTTP(t, true) }

func TestPlatformSubjectDirectoryPaginationSQLite(t *testing.T) {
	s, mux, _, _, cookie := platformManagementFixture(t, false)
	for i := 0; i < 35; i++ {
		if err := s.st.UpsertUser(User{Username: fmt.Sprintf("subject-%02d", i), DisplayName: "Subject", Role: "user", PasswordHash: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, s.st, `UPDATE users SET active=0 WHERE username='subject-34'`)
	seen := map[string]bool{}
	after := ""
	for page := 0; page < 3; page++ {
		path := "/api/admin/app-platform/subjects?kind=user"
		if after != "" {
			path += "&after=" + after
		}
		got := sessionTestRequest(mux, "GET", path, cookie, "")
		var response struct {
			Items []platformSubjectSummary `json:"items"`
			Next  string                   `json:"next_cursor"`
		}
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &response) != nil {
			t.Fatal("subject page unavailable", got.Code)
		}
		if len(response.Items) > 32 || strings.Contains(got.Body.String(), "PasswordHash") || strings.Contains(got.Body.String(), "subject-34") {
			t.Fatal("subject directory exceeded its disclosure/page bounds")
		}
		for _, item := range response.Items {
			if seen[item.ID] {
				t.Fatal("duplicate subject across cursor pages")
			}
			seen[item.ID] = true
		}
		after = response.Next
		if after == "" {
			break
		}
	}
	if len(seen) != 35 || after != "" {
		t.Fatalf("subject paging lost eligible accounts: %d", len(seen))
	}
}
