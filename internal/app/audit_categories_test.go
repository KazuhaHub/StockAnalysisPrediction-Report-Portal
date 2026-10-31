package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Every action this build writes sits in exactly one category: in none, the category filter could
// never reach it; in two, picking either category would show it and the counts would not add up.
func TestEveryActionBelongsToExactlyOneCategory(t *testing.T) {
	seen := map[string]string{}
	for _, c := range auditCategories {
		if c.Key == "" || c.Key == auditCategoryOther {
			t.Fatalf("category key %q is reserved or empty", c.Key)
		}
		if len(c.Actions) == 0 {
			t.Errorf("category %q is empty", c.Key)
		}
		for _, a := range c.Actions {
			if prev, ok := seen[a]; ok {
				t.Errorf("%q is in both %q and %q", a, prev, c.Key)
			}
			seen[a] = c.Key
		}
	}
	for _, a := range auditVocabulary {
		if seen[a] == "" {
			t.Errorf("%q is in the vocabulary but in no category", a)
		}
	}
	if len(seen) != len(auditVocabulary) {
		t.Errorf("categories hold %d actions, vocabulary %d", len(seen), len(auditVocabulary))
	}
}

func TestAuditAPIGroupsActionsByCategory(t *testing.T) {
	s := auditServer(t)
	s.st.UpsertUser(User{Username: "admin", PasswordHash: mustHash("admin-password-here"), Role: "admin"})
	s.st.WriteAudit(AuditEntry{Action: "legacy.retired", TargetType: "x", TargetID: "1"})

	rec := httptest.NewRecorder()
	s.apiAdminAudit(rec, httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil), "admin")
	var out struct {
		Categories []auditCategory `json:"categories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Categories) != len(auditCategories)+1 {
		t.Fatalf("got %d categories, want the %d declared plus other", len(out.Categories), len(auditCategories))
	}
	// Declared order, actions in their declared (reading) order rather than sorted by key.
	if got := out.Categories[0]; got.Key != "auth" || got.Actions[0] != AuditLogin || got.Actions[1] != AuditLoginFailed {
		t.Errorf("first category = %+v, want auth starting with sign-in, then failed sign-in", got)
	}
	last := out.Categories[len(out.Categories)-1]
	if last.Key != auditCategoryOther || len(last.Actions) != 1 || last.Actions[0] != "legacy.retired" {
		t.Errorf("other = %+v, want only the action present in the data that no category declares", last)
	}
}

func TestAuditAPIOmitsOtherWhenEveryActionIsKnown(t *testing.T) {
	s := auditServer(t)
	s.st.UpsertUser(User{Username: "admin", PasswordHash: mustHash("admin-password-here"), Role: "admin"})
	s.recordAuth(nil, AuditReportRead, "kazuha", "1", nil)

	rec := httptest.NewRecorder()
	s.apiAdminAudit(rec, httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil), "admin")
	var out struct {
		Categories []auditCategory `json:"categories"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	for _, c := range out.Categories {
		if c.Key == auditCategoryOther {
			t.Fatalf("other offered with %v although nothing unknown was recorded", c.Actions)
		}
	}
}

func TestAuditCategoryFilter(t *testing.T) {
	s := auditServer(t)
	for _, a := range []string{AuditLogin, AuditLoginFailed, AuditReportRead, AuditExportCreate, "legacy.retired", ""} {
		s.st.WriteAudit(AuditEntry{Action: a, TargetType: "x", TargetID: "1"})
	}
	actions := func(f AuditFilter) map[string]bool {
		rows, total := s.st.ListAudit(f)
		if total != len(rows) {
			t.Fatalf("total %d but %d rows for %+v", total, len(rows), f)
		}
		got := map[string]bool{}
		for _, r := range rows {
			got[r.Action] = true
		}
		return got
	}

	if got := actions(AuditFilter{Category: "auth"}); len(got) != 2 || !got[AuditLogin] || !got[AuditLoginFailed] {
		t.Errorf("auth = %v, want the two sign-in rows", got)
	}
	if got := actions(AuditFilter{Category: auditCategoryOther}); len(got) != 2 || !got["legacy.retired"] || !got[""] {
		t.Errorf("other = %v, want the undeclared action and the row with none", got)
	}
	// An event outside the chosen category matches nothing rather than silently widening.
	if got := actions(AuditFilter{Category: "auth", Action: AuditReportRead}); len(got) != 0 {
		t.Errorf("auth + report.read = %v, want nothing", got)
	}
	// A category this build does not know is an empty result, not the whole log.
	if got := actions(AuditFilter{Category: "nope"}); len(got) != 0 {
		t.Errorf("unknown category = %v, want nothing", got)
	}

	rec := httptest.NewRecorder()
	s.st.UpsertUser(User{Username: "admin", PasswordHash: mustHash("admin-password-here"), Role: "admin"})
	s.apiAdminAudit(rec, httptest.NewRequest(http.MethodGet, "/api/admin/audit?category=exports", nil), "admin")
	var out struct {
		Total int `json:"total"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Total != 1 {
		t.Errorf("?category=exports total = %d, want 1", out.Total)
	}
}
