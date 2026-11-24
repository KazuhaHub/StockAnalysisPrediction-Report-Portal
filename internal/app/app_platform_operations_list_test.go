package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type platformOperationListReply struct {
	Items []platformOperationItem `json:"items"`
}

func platformOperationListRead(t *testing.T, mux *http.ServeMux, cookie, query string, want int) platformOperationListReply {
	t.Helper()
	got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/operations"+query, cookie, "")
	if got.Code != want {
		t.Fatalf("GET operations%s = %d, want %d: %s", query, got.Code, want, got.Body.String())
	}
	var reply platformOperationListReply
	if want == http.StatusOK {
		if err := json.Unmarshal(got.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
	}
	return reply
}

// The history lists an installation's operations newest first, with the versions of the plan's two releases, and
// the latest-per-installation form answers with the newest one only.
func TestPlatformOperationListHTTP(t *testing.T) {
	s, actor, installation, target := platformUpdateFixture(t, []int{1})
	source := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	sourceVersion := scalar[string](t, s.st, `SELECT version FROM app_platform_releases WHERE sha256=?`, source)
	targetVersion := scalar[string](t, s.st, `SELECT version FROM app_platform_releases WHERE sha256=?`, target.sha256)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())

	body := fmt.Sprintf(`{"action":"update","release_sha256":%q,"trust_revision":"1","installation_id":%q}`, target.sha256, installation)
	plan := sessionTestRequest(mux, "POST", "/api/admin/app-platform/plans", cookie, body)
	var review struct {
		ID  string `json:"plan_id"`
		SHA string `json:"plan_sha256"`
	}
	if err := json.Unmarshal(plan.Body.Bytes(), &review); err != nil || plan.Code != 200 {
		t.Fatal("update review", plan.Code, plan.Body.String(), err)
	}
	commit := sessionTestRequest(mux, "POST", "/api/admin/app-platform/operations", cookie,
		fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`, review.ID, review.SHA, platformFixtureID(6101)))
	if commit.Code != 202 {
		t.Fatal("update commit", commit.Code, commit.Body.String())
	}
	var admitted struct {
		ID string `json:"operation_id"`
	}
	if err := json.Unmarshal(commit.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	platformStartFrontendService(t, s)
	platformAwaitFrontendService(t, s, "complete")

	history := platformOperationListRead(t, mux, cookie, "?installation="+installation, 200)
	if len(history.Items) != 2 {
		t.Fatalf("history has %d operations, want the install and the update: %+v", len(history.Items), history.Items)
	}
	update, install := history.Items[0], history.Items[1]
	if update.OperationID != admitted.ID || update.Action != "update" || update.Stage != "complete" || !update.Terminal || update.ErrorCode != "" ||
		update.InstallationID != installation || update.FromVersion != sourceVersion || update.ToVersion != targetVersion || update.CompletedMS == "" || update.CreatedMS == "" || update.Revision == "" {
		t.Fatalf("update row: %+v", update)
	}
	if install.Action != "install" || install.FromVersion != "" || install.ToVersion != sourceVersion || !install.Terminal {
		t.Fatalf("install row: %+v", install)
	}

	one := platformOperationListRead(t, mux, cookie, "?installation="+installation+"&limit=1", 200)
	if len(one.Items) != 1 || one.Items[0].OperationID != admitted.ID {
		t.Fatalf("limit=1: %+v", one.Items)
	}
	latest := platformOperationListRead(t, mux, cookie, "?latest=1", 200)
	if len(latest.Items) != 1 || latest.Items[0].OperationID != admitted.ID {
		t.Fatalf("latest per installation: %+v", latest.Items)
	}

	// A failed operation carries only the public error code, never the stored one.
	mustExec(t, s.st, `UPDATE app_platform_operations SET error_code='raw_internal_detail' WHERE id=?`, admitted.ID)
	failed := platformOperationListRead(t, mux, cookie, "?installation="+installation+"&limit=1", 200)
	if failed.Items[0].ErrorCode != "app_platform_operation_failed" {
		t.Fatalf("error code leaked or lost: %q", failed.Items[0].ErrorCode)
	}
	raw := sessionTestRequest(mux, "GET", "/api/admin/app-platform/operations?installation="+installation, cookie, "")
	if strings.Contains(raw.Body.String(), "raw_internal_detail") {
		t.Fatal("stored error detail reached the response")
	}
}

func TestPlatformOperationListRefusesBadRequests(t *testing.T) {
	s, actor, installation, _ := platformUpdateFixture(t, []int{1})
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	for _, query := range []string{
		"",                                       // neither selection
		"?latest=1&installation=" + installation, // both selections
		"?latest=2",
		"?installation=not-a-uuid",
		"?installation=" + installation + "&limit=0",
		"?installation=" + installation + "&limit=51",
		"?installation=" + installation + "&limit=07",
		"?installation=" + installation + "&limit=1&limit=2",
		"?installation=" + installation + "&order=asc",
	} {
		platformOperationListRead(t, mux, cookie, query, 400)
	}
	platformOperationListRead(t, mux, cookie, "?installation="+platformFixtureID(6199), 404)
	if got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/operations?latest=1", "", ""); got.Code == 200 {
		t.Fatal("history readable without a session", got.Body.String())
	}
}
