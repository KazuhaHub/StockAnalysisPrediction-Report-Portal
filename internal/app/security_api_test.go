package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The login-protection settings as the admin page sees them.
//
// The shape of this endpoint is the part that is easy to get wrong in a way nobody notices: every
// block is optional, so a page that renders one card must not clear the other two by sending only
// what changed. And a rejected field must not leave its neighbours stored — the form would show a
// mixture of the old policy and the new one, with no way to tell which half took.

func readSecurity(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiAdminSecurity(rec, httptest.NewRequest(http.MethodGet, "/api/admin/security", nil), "boss")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/security → %d (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func block(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	m, ok := doc[name].(map[string]any)
	if !ok {
		t.Fatalf("the response has no %q block: %v", name, doc)
	}
	return m
}

// The three blocks report what the portal is actually doing, on a portal that has never saved them.
func TestTheSecurityPageShowsTheShippedPolicy(t *testing.T) {
	s := limitsServer(t)
	doc := readSecurity(t, s)

	twofa := block(t, doc, "twofa")
	if twofa["totp_enroll"] != true || twofa["passkey_enroll"] != true {
		t.Errorf("a portal that has never saved this must show enrolment allowed: %v", twofa)
	}
	if enabled := block(t, doc, "recovery")["enabled"]; enabled != true {
		t.Errorf("recovery = %v, want true (it was always available)", enabled)
	}
	lock := block(t, doc, "lockout")
	if lock["enabled"] != false {
		t.Errorf("lockout = %v, want false (the throttle's window is what shipped)", lock["enabled"])
	}
	if lock["scope"] != lockoutScopeIPAccount {
		t.Errorf("lockout scope = %v, want %q", lock["scope"], lockoutScopeIPAccount)
	}
	if lock["duration_min"] != float64(defLockoutDurationMin) {
		t.Errorf("lockout duration = %v, want %d", lock["duration_min"], defLockoutDurationMin)
	}
}

func TestTheSecurityPolicyRoundTrips(t *testing.T) {
	s := limitsServer(t)
	body := `{"twofa":{"totp_enroll":false,"passkey_enroll":true},
		"recovery":{"enabled":false},
		"lockout":{"enabled":true,"duration_min":90,"scope":"account"}}`
	if code := saveSecurity(t, s, body); code != http.StatusOK {
		t.Fatalf("save → %d", code)
	}
	doc := readSecurity(t, s)
	if got := block(t, doc, "twofa")["totp_enroll"]; got != false {
		t.Errorf("totp_enroll = %v, want false", got)
	}
	if got := block(t, doc, "recovery")["enabled"]; got != false {
		t.Errorf("recovery = %v, want false", got)
	}
	lock := block(t, doc, "lockout")
	if lock["enabled"] != true || lock["duration_min"] != float64(90) || lock["scope"] != "account" {
		t.Errorf("lockout = %v, want enabled/90/account", lock)
	}
	// The save is a policy change and has to be in the audit log, like every other one here.
	if rows := auditRows(t, s, AuditPolicyChange); len(rows) != 1 {
		t.Errorf("audit rows for %s = %d, want 1", AuditPolicyChange, len(rows))
	}
}

// The page sends the cards it renders. A block it omits has to survive untouched — otherwise opening
// the page and saving the 2FA card would silently switch password recovery off.
func TestAnOmittedBlockIsLeftAlone(t *testing.T) {
	s := limitsServer(t)
	all := `{"twofa":{"totp_enroll":false,"passkey_enroll":false},
		"recovery":{"enabled":false},
		"lockout":{"enabled":true,"duration_min":90,"scope":"account"}}`
	if code := saveSecurity(t, s, all); code != http.StatusOK {
		t.Fatalf("first save → %d", code)
	}
	if code := saveSecurity(t, s, `{"twofa":{"totp_enroll":true}}`); code != http.StatusOK {
		t.Fatalf("second save → %d", code)
	}
	doc := readSecurity(t, s)
	twofa := block(t, doc, "twofa")
	if twofa["totp_enroll"] != true {
		t.Error("the field that was sent did not change")
	}
	if twofa["passkey_enroll"] != false {
		t.Error("the omitted field in the same block was cleared")
	}
	if block(t, doc, "recovery")["enabled"] != false {
		t.Error("an omitted block was cleared")
	}
	if block(t, doc, "lockout")["scope"] != "account" {
		t.Error("an omitted block's field was cleared")
	}
}

// Out of range is refused rather than clamped, and the refusal takes the WHOLE request with it: a bad
// duration must not leave the scope beside it saved.
func TestARefusedFieldStoresNothingFromItsRequest(t *testing.T) {
	s := limitsServer(t)
	for _, bad := range []string{
		`{"lockout":{"enabled":true,"duration_min":0,"scope":"account"}}`,
		`{"lockout":{"enabled":true,"duration_min":999999,"scope":"account"}}`,
	} {
		if code := saveSecurity(t, s, bad); code != http.StatusBadRequest {
			t.Fatalf("%s → %d, want 400", bad, code)
		}
	}
	doc := readSecurity(t, s)
	lock := block(t, doc, "lockout")
	if lock["enabled"] != false || lock["scope"] != lockoutScopeIPAccount {
		t.Errorf("a refused save stored part of its block: %v", lock)
	}
}

func TestAnUnknownLockoutScopeIsRefused(t *testing.T) {
	s := limitsServer(t)
	if code := saveSecurity(t, s, `{"lockout":{"scope":"everyone"}}`); code != http.StatusBadRequest {
		t.Fatalf("an unknown scope → %d, want 400", code)
	}
	if got := block(t, readSecurity(t, s), "lockout")["scope"]; got != lockoutScopeIPAccount {
		t.Errorf("scope = %v, want the shipped default left in place", got)
	}
}

// ---------- the per-OU overrides ----------

// The overrides travel with the OU, because that is where an admin edits them: the settings page
// carries the portal-wide switches, and the OU page carries what each OU decides for its members.
func TestTheGroupAPICarriesTheEnrolmentOverrides(t *testing.T) {
	s := ouAPIServer(t)
	if err := s.st.UpsertUser(User{Username: "member", PasswordHash: mustHash("correct-horse-battery"), Role: "user"}); err != nil {
		t.Fatal(err)
	}
	id := int64(postGroup(t, s, `{"name":"客户A"}`)["id"].(float64))
	if err := s.st.SetPrimaryGroup("member", id); err != nil {
		t.Fatal(err)
	}

	row := func() map[string]any {
		for _, g := range userGroupsJSON(s.st.ListUserGroups()) {
			if g["id"] == id {
				return g
			}
		}
		t.Fatalf("group %d is missing from the list", id)
		return nil
	}

	if rec := putGroup(s, id, `{"name":"客户A","totp_enroll":false}`); rec.Code != http.StatusOK {
		t.Fatalf("save → %d %s", rec.Code, rec.Body.String())
	}
	if got := row()["totp_enroll"]; got != false {
		t.Errorf("the list reports totp_enroll = %v, want false", got)
	}
	if got := row()["passkey_enroll"]; got != nil {
		t.Errorf("an override that was not sent must read as inherited, got %v", got)
	}
	if s.st.SecuritySettings("member").TOTPAllowed {
		t.Error("the OU's own member may still enrol TOTP")
	}
	if !s.st.SecuritySettings("member").PasskeyAllowed {
		t.Error("passkeys were not touched and must stay allowed")
	}

	// null means inherit again — the way back that an InheritField needs.
	if rec := putGroup(s, id, `{"name":"客户A","totp_enroll":null}`); rec.Code != http.StatusOK {
		t.Fatalf("clearing → %d %s", rec.Code, rec.Body.String())
	}
	if got := row()["totp_enroll"]; got != nil {
		t.Errorf("after clearing, totp_enroll = %v, want nil (inherited)", got)
	}
	if !s.st.SecuritySettings("member").TOTPAllowed {
		t.Error("clearing the override must restore the inherited answer")
	}
}
