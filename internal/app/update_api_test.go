package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

// The update prompt's administrator policy is one enum on the settings page, not two independent
// switches: "may the user defer this" and "must the page be refreshed" are the same decision read
// two ways, and a dismissible flag plus a required flag can describe a state nobody chose.
func TestUpdatePromptPolicyIsOneValidatedEnum(t *testing.T) {
	s := tenancyServer(t)

	if got := s.updatePromptPolicy(); got != "dismissible" {
		t.Errorf("default policy = %q; an existing database with no stored value must not surprise the reader", got)
	}
	for _, p := range []string{"dismissible", "persistent", "required", "automatic"} {
		if code := settingsRoundTrip(t, s, `{"updatePromptPolicy":"`+p+`"}`); code != http.StatusOK {
			t.Fatalf("saving policy %q → %d", p, code)
		}
		if got := s.updatePromptPolicy(); got != p {
			t.Errorf("after saving %q the policy reads %q", p, got)
		}
	}
	// An unknown value is refused rather than coerced: silently writing the default would tell an
	// admin their choice was kept when it was not.
	if code := settingsRoundTrip(t, s, `{"updatePromptPolicy":"always"}`); code != http.StatusBadRequest {
		t.Errorf("saving an unknown policy → %d, want 400", code)
	}
	if got := s.updatePromptPolicy(); got != "automatic" {
		t.Errorf("a refused save changed the stored policy to %q", got)
	}
	// A payload that does not mention the policy leaves it alone (per-field merge).
	if code := settingsRoundTrip(t, s, `{"siteTitle":"x"}`); code != http.StatusOK {
		t.Fatalf("an unrelated save → %d", code)
	}
	if got := s.updatePromptPolicy(); got != "automatic" {
		t.Errorf("an unrelated save rewrote the policy to %q", got)
	}
}

// An unreadable stored value degrades to the default instead of forcing a refresh on every reader:
// the failure mode of a corrupted meta row must be the least disruptive one.
func TestUpdatePromptPolicyFallsBackOnAnUnknownStoredValue(t *testing.T) {
	s := tenancyServer(t)
	if err := s.st.SetSetting("update_prompt_policy", "whatever"); err != nil {
		t.Fatal(err)
	}
	if got := s.updatePromptPolicy(); got != "dismissible" {
		t.Errorf("a corrupt stored policy reads %q, want the default", got)
	}
}

// Already-open pages learn the effective policy from the same poll that tells them a deploy landed,
// so an admin's escalation reaches a tab nobody reloaded.
func TestVersionPayloadCarriesTheUpdatePolicy(t *testing.T) {
	s := tenancyServer(t)
	rec := httptest.NewRecorder()
	s.handleVersion(rec, httptest.NewRequest("GET", "/api/version", nil), "alice")

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["version"] == "" || out["commit"] == "" || out["buildDate"] == "" {
		t.Fatalf("version payload = %v; the existing identity fields must stay", out)
	}
	if out["updatePromptPolicy"] != "dismissible" {
		t.Errorf("updatePromptPolicy = %v, want the stored/default policy", out["updatePromptPolicy"])
	}
}

func TestAutomaticUpdateUsesRequiredAsTheLegacyFallback(t *testing.T) {
	s := tenancyServer(t)
	if err := s.st.SetSetting(updatePromptPolicySetting, policyAutomatic); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleVersion(rec, httptest.NewRequest("GET", "/api/version", nil), "alice")

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["updatePromptPolicy"] != policyPersistent || out["automaticUpdate"] != true {
		t.Fatalf("automatic version payload = %v, want persistent compatibility plus automatic flag", out)
	}
}

func TestReleaseNotesRequireASession(t *testing.T) {
	srv := &Server{}
	for _, path := range []string{"/api/release-notes", "/api/release-history"} {
		rec := httptest.NewRecorder()
		handler := srv.handleReleaseNotes
		if path == "/api/release-history" {
			handler = srv.handleReleaseHistory
		}
		srv.requireUserJSON(handler)(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous GET %s = %d, want 401", path, rec.Code)
		}
	}
}

func TestReleaseNotesResponseRules(t *testing.T) {
	const note = "## Fixed\n\n- the thing\n"
	url := version.ReleaseNotesBaseURL + "/releases/tag/v2026.38.1"
	releases := []publishedRelease{{Tag: "v2026.38.1", Markdown: note, Maturity: "beta"}}

	got := releaseNotesResp("", "v2026.38.1", releases, false)
	if got["tag"] != "v2026.38.1" || got["available"] != true || got["markdown"] != note || got["url"] != url || got["maturity"] != "beta" {
		t.Errorf("served payload = %v", got)
	}
	got = releaseNotesResp("v2026.38", "v2026.38.1", releases, true)
	if got["available"] != false || got["markdown"] != "" {
		t.Errorf("a mismatched tag served a body: %v", got)
	}
	if got["url"] != version.ReleaseNotesBaseURL+"/releases/tag/v2026.38" {
		t.Errorf("a mismatched tag lost its release link: %v", got)
	}
	if got["tag"] != "v2026.38" {
		t.Errorf("the response must name the tag it is about: %v", got)
	}
	if got["stale"] != true {
		t.Errorf("a cached GitHub response lost its stale marker: %v", got)
	}
	// A diagnostic build has no note and no release page: no link is invented.
	got = releaseNotesResp("", "dev", releases, false)
	if got["available"] != false || got["url"] != "" || got["maturity"] != "dev" {
		t.Errorf("a diagnostic build fabricated notes or a link: %v", got)
	}
	got = releaseNotesResp("", "v2026.38.2", releases, false)
	if got["available"] != false || got["markdown"] != "" {
		t.Errorf("an unpublished tag reported success: %v", got)
	}
	if got["url"] != version.ReleaseNotesBaseURL+"/releases/tag/v2026.38.2" {
		t.Errorf("an unpublished tag dropped its canonical release link: %v", got)
	}
	got = releaseNotesResp("", "v2026.38.2", []publishedRelease{{Tag: "v2026.38.2", Maturity: "release"}}, false)
	if got["available"] != false || got["markdown"] != "" || got["maturity"] != "release" {
		t.Errorf("an empty published body should retain maturity without claiming notes exist: %v", got)
	}
}

func TestReleaseHistoryUsesCurrentGitHubMaturity(t *testing.T) {
	releases := []publishedRelease{
		{Tag: "v2026.38.4", Title: "Current", Markdown: "current", Maturity: "beta"},
		{Tag: "v2026.38.3", Title: "Candidate", Markdown: "candidate", Maturity: "beta"},
		{Tag: "v2026.38", Title: "Baseline", Markdown: "baseline", Maturity: "release"},
	}
	items := releaseHistoryResp("v2026.38.4", releases)
	if len(items) != 3 || items[0]["tag"] != "v2026.38.4" || items[2]["tag"] != "v2026.38" {
		t.Fatalf("beta release history = %v", items)
	}
	// Promoting the same GitHub Release changes the view without rebuilding the tag.
	releases[0].Maturity = "release"
	items = releaseHistoryResp("v2026.38.4", releases)
	if len(items) != 2 || items[0]["tag"] != "v2026.38.4" || items[1]["tag"] != "v2026.38" {
		t.Fatalf("stable release history = %v", items)
	}
}

// The note is matched by the tag VERBATIM, which is what keeps a neighbouring number from being
// served as this one's. The one-digit boundary is where a prefix test would betray it: v2026.9 is a
// string prefix of nothing that follows, v2026.10 is week ten, and the two must never be conflated.
func TestReleaseNotesAreMatchedByExactTag(t *testing.T) {
	const note = "## week ten\n"
	releases := []publishedRelease{{Tag: "v2026.10", Markdown: note, Maturity: "release"}}
	got := releaseNotesResp("v2026.9", "v2026.10", releases, false)
	if got["available"] != false || got["markdown"] != "" {
		t.Errorf("asking for v2026.9 served v2026.10's note: %v", got)
	}
	if got["tag"] != "v2026.9" {
		t.Errorf("the refusal must name the tag it is about: %v", got)
	}

	// And the revision position behaves the same way.
	got = releaseNotesResp("v2026.9.9", "v2026.9.10", releases, false)
	if got["available"] != false || got["markdown"] != "" {
		t.Errorf("asking for v2026.9.9 served v2026.9.10's note: %v", got)
	}

	// The exact match still works, so the rule above is a boundary and not a blanket refusal.
	got = releaseNotesResp("v2026.10", "v2026.10", releases, false)
	if got["available"] != true || got["markdown"] != note {
		t.Errorf("the deployed build's own tag must be served: %v", got)
	}
}

func TestHandleReleaseNotesReadsGitHub(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []githubRelease{{Tag: "v2026.38.1", Body: "# live", Prerelease: true}})
	}))
	defer upstream.Close()
	s := tenancyServer(t)
	s.releases = releaseCatalog{client: upstream.Client(), apiURL: upstream.URL}
	rec := httptest.NewRecorder()
	s.handleReleaseNotes(rec, httptest.NewRequest(http.MethodGet, "/api/release-notes?tag=v2026.38.1", nil), "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/release-notes → %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["available"] != true || out["markdown"] != "# live" || out["maturity"] != "beta" {
		t.Errorf("GitHub release payload = %v", out)
	}
}

// The settings payload is read by the management page, so the policy must round-trip through the
// same endpoint that writes it.
func TestAdminSettingsExposesTheUpdatePolicy(t *testing.T) {
	s := tenancyServer(t)
	if err := s.st.SetSetting("update_prompt_policy", "required"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.apiAdminSettings(rec, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil), "admin")
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["updatePromptPolicy"] != "required" {
		t.Errorf("updatePromptPolicy = %v, want required", out["updatePromptPolicy"])
	}
	if !strings.Contains(rec.Body.String(), "siteTitle") {
		t.Error("the admin settings payload must still merge the public brand fields")
	}
}
