package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The console must be able to reach the uninstall: the release listing marks the failed install's own release as
// uninstallable with its installation id, and the uninstall review carries that release so the console accepts it.
func TestStartedFailedInstallIsOfferedForUninstallInTheCatalogue(t *testing.T) {
	t.Parallel()
	f := newStartedFailedInstall(t, false)
	release := scalar[string](t, f.s.st, `SELECT target_release_sha256 FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, f.install)
	mux := http.NewServeMux()
	f.s.wireRoutes(mux)
	cookie := f.s.managedCookie(*f.s.st.GetUser(f.actor.username), f.actor.sessionID, f.actor.expires.Unix())
	list := func() platformStagedSummary {
		got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/releases", cookie, "")
		var page struct {
			Items []platformStagedSummary `json:"items"`
		}
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &page) != nil {
			t.Fatal("release catalogue", got.Code, got.Body.String())
		}
		for _, item := range page.Items {
			if item.Release == release {
				return item
			}
		}
		t.Fatal("the failed install's release is missing from the catalogue")
		return platformStagedSummary{}
	}
	item := list()
	if item.Installable || item.Reason != "already_installed" || !item.Uninstallable || item.InstallationID != f.installation || item.CurrentReleaseSHA256 != release {
		t.Fatalf("the failed install is not offered for uninstall: %+v", item)
	}
	body := `{"action":"uninstall","installation_id":"` + f.installation + `"}`
	got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/plans", cookie, body)
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"release_sha256":"`+release+`"`) {
		t.Fatalf("the uninstall review does not carry the installed release: %d %s", got.Code, got.Body.String())
	}
	// Once the evidence fails, the listing stops offering it.
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET state='unknown' WHERE kind='start-candidate'`)
	if item := list(); item.Uninstallable || item.InstallationID != "" {
		t.Fatalf("an uninstall that would be refused is offered: %+v", item)
	}
}
