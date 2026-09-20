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
	for _, p := range []string{"dismissible", "persistent", "required"} {
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
	if got := s.updatePromptPolicy(); got != "required" {
		t.Errorf("a refused save changed the stored policy to %q", got)
	}
	// A payload that does not mention the policy leaves it alone (per-field merge).
	if code := settingsRoundTrip(t, s, `{"siteTitle":"x"}`); code != http.StatusOK {
		t.Fatalf("an unrelated save → %d", code)
	}
	if got := s.updatePromptPolicy(); got != "required" {
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

func TestReleaseNotesRequireASession(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	srv.requireUserJSON(srv.handleReleaseNotes)(rec, httptest.NewRequest(http.MethodGet, "/api/release-notes", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/release-notes = %d, want 401", rec.Code)
	}
}

// The body is built from a packaged note plus the server's own tag, so the rules — which tag may be
// served, and when a GitHub link exists — are exercised here without a release build.
func TestReleaseNotesResponseRules(t *testing.T) {
	const note = "## Fixed\n\n- the thing\n"
	url := version.ReleaseNotesBaseURL + "/releases/tag/v2026.38.1"

	// The deployed build answers with its own note.
	got := releaseNotesResp("", "v2026.38.1", note, true)
	if got["tag"] != "v2026.38.1" || got["available"] != true || got["markdown"] != note || got["url"] != url {
		t.Errorf("served payload = %v", got)
	}
	// A page still running the previous build asks for ITS version. The server has not packaged
	// that note, so it says so and offers the exact release link instead of the new body under the
	// old number.
	got = releaseNotesResp("v2026.38", "v2026.38.1", note, true)
	if got["available"] != false || got["markdown"] != "" {
		t.Errorf("a mismatched tag served a body: %v", got)
	}
	if got["url"] != version.ReleaseNotesBaseURL+"/releases/tag/v2026.38" {
		t.Errorf("a mismatched tag lost its release link: %v", got)
	}
	if got["tag"] != "v2026.38" {
		t.Errorf("the response must name the tag it is about: %v", got)
	}
	// A diagnostic build has no note and no release page: no link is invented.
	got = releaseNotesResp("", "dev", "", false)
	if got["available"] != false || got["url"] != "" {
		t.Errorf("a diagnostic build fabricated notes or a link: %v", got)
	}
	// A release tag whose note step did not run is reported as unavailable, never as a link-only
	// success with an empty body.
	got = releaseNotesResp("", "v2026.38.1", "", false)
	if got["available"] != false || got["markdown"] != "" {
		t.Errorf("a missing note reported success: %v", got)
	}
	if got["url"] != url {
		t.Errorf("a missing note dropped the usable release link: %v", got)
	}
}

// The note is matched by the tag VERBATIM, which is what keeps a neighbouring number from being
// served as this one's. The one-digit boundary is where a prefix test would betray it: v2026.9 is a
// string prefix of nothing that follows, v2026.10 is week ten, and the two must never be conflated.
func TestReleaseNotesAreMatchedByExactTag(t *testing.T) {
	const note = "## week ten\n"
	got := releaseNotesResp("v2026.9", "v2026.10", note, true)
	if got["available"] != false || got["markdown"] != "" {
		t.Errorf("asking for v2026.9 served v2026.10's note: %v", got)
	}
	if got["tag"] != "v2026.9" {
		t.Errorf("the refusal must name the tag it is about: %v", got)
	}

	// And the revision position behaves the same way.
	got = releaseNotesResp("v2026.9.9", "v2026.9.10", note, true)
	if got["available"] != false || got["markdown"] != "" {
		t.Errorf("asking for v2026.9.9 served v2026.9.10's note: %v", got)
	}

	// The exact match still works, so the rule above is a boundary and not a blanket refusal.
	got = releaseNotesResp("v2026.10", "v2026.10", note, true)
	if got["available"] != true || got["markdown"] != note {
		t.Errorf("the deployed build's own tag must be served: %v", got)
	}
}

func TestHandleReleaseNotesServesTheUnavailableState(t *testing.T) {
	s := tenancyServer(t)
	rec := httptest.NewRecorder()
	s.handleReleaseNotes(rec, httptest.NewRequest(http.MethodGet, "/api/release-notes", nil), "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/release-notes → %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// The test binary is built without -ldflags, so there is no packaged note: the honest answer.
	if out["available"] != false {
		t.Errorf("available = %v in a diagnostic build; want false", out["available"])
	}
	if out["tag"] != version.Version {
		t.Errorf("tag = %v, want this build's own %q", out["tag"], version.Version)
	}
	if _, ok := out["markdown"]; !ok {
		t.Error("the payload must carry the markdown field even when empty, so a client need not branch on its absence")
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
