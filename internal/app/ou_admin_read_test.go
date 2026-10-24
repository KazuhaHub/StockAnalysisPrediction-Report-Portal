package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnifiedOUAdminPolicyResultsMatchRuntime(t *testing.T) {
	s, org, team, _, _ := ouFixture(t)
	if _, err := s.st.exec(`UPDATE user_groups SET max_queued=9,totp_enroll=0 WHERE id=?`, org); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.apiAdminGroups(w, httptest.NewRequest("GET", "/api/admin/groups", nil), "admin")
	var response struct {
		Groups []struct {
			ID       int64              `json:"id"`
			Policies ouResolvedPolicies `json:"policy_results"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, g := range response.Groups {
		if g.ID == team {
			if g.Policies.MaxQueued.Effective != 9 || g.Policies.MaxQueued.SourceOU != org || g.Policies.TOTP.Effective || !g.Policies.MaxQueued.Available {
				t.Fatalf("admin result differs from runtime: %+v", g.Policies)
			}
			return
		}
	}
	t.Fatal("child policy result missing")
}

func TestUnifiedOUAdminPreviewDoesNotWrite(t *testing.T) {
	s, org, team, _, _ := ouFixture(t)
	if _, err := s.st.exec(`UPDATE user_groups SET max_queued=9 WHERE id=?`, org); err != nil {
		t.Fatal(err)
	}
	before := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/groups/1/preview", strings.NewReader(fmt.Sprintf(`{"name":"preview-only","parent_id":%d,"max_queued":null}`, org)))
	r.SetPathValue("id", itoa64(team))
	s.apiGroupPolicyPreview(w, r, "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Policies ouResolvedPolicies `json:"policy_results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Policies.MaxQueued.Effective != 9 || out.Policies.MaxQueued.SourceOU != org {
		t.Fatalf("preview policy: %+v", out.Policies.MaxQueued)
	}
	if got := scalar[string](t, s.st, `SELECT name FROM user_groups WHERE id=?`, team); got == "preview-only" {
		t.Fatal("preview persisted the draft")
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log`); got != before {
		t.Fatal("preview emitted a change audit")
	}
}
