package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The mandate: an account that MUST have a second factor, and the wall it meets until it does.
//
// Two properties decide everything here, and both exist because the alternative fails quietly:
//
//   - the mandate is STICKY down the OU tree, like the tenant restriction. A parent that requires
//     two-factor for its division cannot be un-required by a child, or the policy is advisory.
//   - the gate FAILS CLOSED. It decides whether business data is readable, so a policy that cannot be
//     read is a refusal the client can retry, never a pass — and a mandate nobody can satisfy holds
//     the account rather than quietly switching itself off.

// mandateServer is one OU that requires two-factor, a member in it, and a member outside it.
type mandateEnv struct {
	s       *Server
	ouID    int64
	already string // has a factor, but the mandate still applies to them
	must    string // mandated and without one
	free    string // not mandated
}

func mandateServer(t *testing.T) mandateEnv {
	t.Helper()
	env := policyServer(t)
	st := env.s.st
	if err := st.SetGroupRequire2FA(env.childID, true); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"must-enrol", "already-has", "outside"} {
		if err := st.UpsertUser(User{Username: u, PasswordHash: mustHash("correct-horse-battery"), Role: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetPrimaryGroup("must-enrol", env.childID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPrimaryGroup("already-has", env.childID); err != nil {
		t.Fatal(err)
	}
	st.SetTOTPSecret("already-has", "sealed")
	if err := st.EnableTOTP("already-has", `["h1"]`); err != nil {
		t.Fatal(err)
	}
	return mandateEnv{s: env.s, ouID: env.childID, already: "already-has", must: "must-enrol", free: "outside"}
}

// sessionFor is a request carrying a session cookie for this server's signing key.
func sessionFor(t *testing.T, s *Server, user string) *http.Request {
	t.Helper()
	return sessionForPath(t, s, user, "GET /api/home", "/api/home")
}

func sessionForPath(t *testing.T, s *Server, user, pattern, path string) *http.Request {
	t.Helper()
	u := s.st.GetUser(user)
	if u == nil {
		t.Fatalf("no such account %q", user)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	s.setSessionCookie(rec, r, *u)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	req.Pattern = pattern
	return req
}

// ---------- the policy ----------

func TestTheMandateIsStickyDownTheTree(t *testing.T) {
	env := mandateServer(t)
	st := env.s.st
	// A child of the mandated OU sets it OFF: the parent still governs, as `restricted` does.
	child, err := st.CreateUserGroup("sub", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupParent(child, env.ouID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupRequire2FA(child, false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPrimaryGroup(env.must, child); err != nil {
		t.Fatal(err)
	}
	if !securityOf(t, st, env.must).Require2FA {
		t.Error("a child cleared a mandate its parent set: the policy is advisory if it can be undone downstairs")
	}
}

func TestTheMandateReachesOnlyItsOwnMembers(t *testing.T) {
	env := mandateServer(t)
	inside := securityOf(t, env.s.st, env.must)
	outside := securityOf(t, env.s.st, env.free)
	if !inside.Require2FA {
		t.Error("the mandated OU's member is not required to enrol")
	}
	if outside.Require2FA {
		t.Error("an account outside the OU must not be required")
	}
}

// The global switch is for staff, and it is the portal's floor rather than an OU's.
func TestTheStaffMandateCoversAdministrators(t *testing.T) {
	env := mandateServer(t)
	if err := env.s.st.UpsertUser(User{Username: "ops", PasswordHash: mustHash("correct-horse-battery"), Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if must, _ := env.s.mustEnroll("ops"); must {
		t.Error("an administrator is not required while the staff mandate is off")
	}
	env.s.st.SetSetting(setRequire2FAForStaff, "1")
	if must, _ := env.s.mustEnroll("ops"); !must {
		t.Error("the staff mandate did not reach an administrator")
	}
	if must, _ := env.s.mustEnroll(env.free); must {
		t.Error("the staff mandate reached an ordinary reader")
	}
}

func TestAFactorSatisfiesTheMandate(t *testing.T) {
	env := mandateServer(t)
	if must, _ := env.s.mustEnroll(env.already); must {
		t.Error("an account with a factor is required to enrol one")
	}
	if must, _ := env.s.mustEnroll(env.must); !must {
		t.Error("an account with no factor in a mandated OU is not required to enrol")
	}
}

// A federated account's factors belong to its IdP, so it can never satisfy a local mandate — and
// holding it at a wall it cannot climb is a lockout dressed as a policy.
func TestFederatedAccountsAreExempt(t *testing.T) {
	env := mandateServer(t)
	if err := env.s.st.UpsertUser(User{Username: "fed", PasswordHash: "", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := env.s.st.SetPrimaryGroup("fed", env.ouID); err != nil {
		t.Fatal(err)
	}
	env.s.st.SetUserSource("fed", "jit", "acme")
	if must, _ := env.s.mustEnroll("fed"); must {
		t.Error("a federated account must not be held at a local enrolment wall")
	}
}

// There is no "and nobody can enrol anyway" escape: an unsatisfiable mandate keeps holding the
// account, because the alternative is a policy that switches itself off and says nothing.
func TestAnUnsatisfiableMandateStillHolds(t *testing.T) {
	env := mandateServer(t)
	env.s.st.SetSetting(setTOTPEnroll, "0")
	env.s.st.SetSetting(setPasskeyEnroll, "0")
	if must, _ := env.s.mustEnroll(env.must); !must {
		t.Error("both methods off must not silently disable the mandate")
	}
	// The account can still reach the page that says so, and the operator has the CLI.
	if !env.s.enrolmentReachable("GET /api/me") {
		t.Error("the account page must stay reachable while the mandate holds")
	}
}

// ---------- the gate ----------

// The wall: a mandated account with no factor reaches the enrolment routes and nothing else.
func TestTheGateHoldsAMandatedAccountAtTheWall(t *testing.T) {
	env := mandateServer(t)
	for _, pattern := range []string{"GET /api/home", "GET /api/stock/{symbol}", "GET /api/admin/users", "GET /api/announcements"} {
		rec := httptest.NewRecorder()
		req := sessionForPath(t, env.s, env.must, pattern, "/api/home")
		env.s.gateEnrolment(rec, req, env.must)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s → %d, want 403", pattern, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "enrolment_required") {
			t.Errorf("%s refused with %q, want the code the SPA branches on", pattern, body)
		}
	}
}

// The routes an account needs to GET OUT of the wall, and the ones signing out needs. Everything
// here is exact: a wildcard would be a hole nobody could see.
func TestTheGateLetsTheEnrolmentRoutesAndSignOutThrough(t *testing.T) {
	env := mandateServer(t)
	for _, pattern := range []string{
		"GET /api/me", "POST /api/me/password",
		"POST /api/me/2fa/setup", "POST /api/me/2fa/enable", "POST /api/me/2fa/disable",
		"GET /api/me/passkeys", "POST /api/me/passkeys/register/begin",
		"POST /api/me/passkeys/register/finish", "DELETE /api/me/passkeys/{id}",
		"GET /api/account/stepup/policy", "POST /api/logout",
	} {
		if !env.s.enrolmentReachable(pattern) {
			t.Errorf("%s is not reachable while the mandate holds, and an account that cannot enrol cannot leave", pattern)
		}
	}
}

// An account NOT under the mandate is unaffected: this is a policy, not a maintenance mode.
func TestTheGateDoesNotTouchUnmandatedAccounts(t *testing.T) {
	env := mandateServer(t)
	rec := httptest.NewRecorder()
	env.s.gateEnrolment(rec, sessionFor(t, env.s, env.free), env.free)
	if rec.Code != http.StatusOK {
		t.Errorf("an ordinary reader was gated → %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	env.s.gateEnrolment(rec, sessionFor(t, env.s, env.already), env.already)
	if rec.Code != http.StatusOK {
		t.Errorf("an account with a factor was gated → %d", rec.Code)
	}
}

// A policy that cannot be read refuses the request rather than answering "you are fine": the gate
// decides whether business data is readable, so its failure mode is the safe direction.
func TestTheGateFailsClosedWhenThePolicyCannotBeRead(t *testing.T) {
	env := mandateServer(t)
	// A closed store is the honest way to make the read fail. The request is built first: it needs
	// the account, and the account needs the store.
	req := sessionFor(t, env.s, env.must)
	env.s.st.Close()
	rec := httptest.NewRecorder()
	env.s.gateEnrolment(rec, req, env.must)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("an unreadable policy → %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "enrolment_policy_unavailable") {
		t.Errorf("refused with %q, want the retryable code", body)
	}
}

// /api/me is what the SPA uses to learn it is gated, so it has to answer even while gated — and it
// has to say so.
func TestMeSaysWhetherTheAccountMustEnrol(t *testing.T) {
	env := mandateServer(t)
	read := func(user string) map[string]any {
		var out map[string]any
		b, _ := json.Marshal(env.s.meJSON(user))
		json.Unmarshal(b, &out)
		return out
	}
	if got := read(env.must)["must_enroll_2fa"]; got != true {
		t.Errorf("a mandated account reads must_enroll_2fa=%v", got)
	}
	if got := read(env.free)["must_enroll_2fa"]; got != false {
		t.Errorf("an ordinary reader reads must_enroll_2fa=%v", got)
	}
}

// ---------- the save-time guard ----------

// The check is on the EFFECTIVE result, not on the field being changed: turning both methods off is
// harmless until something requires a factor, and enabling a mandate is harmless until no method is
// left. Refusing the pair is what keeps an operator from locking their own people out with two
// individually reasonable saves.
func TestAPolicyThatNobodyCouldSatisfyIsRefused(t *testing.T) {
	env := mandateServer(t) // one OU already requires a factor
	// ...and both methods are on, which is the state everything above ran in.
	if code := saveSecurity(t, env.s, `{"twofa":{"totp_enroll":false,"passkey_enroll":false}}`); code != http.StatusBadRequest {
		t.Fatalf("turning both methods off under a mandate → %d, want 400", code)
	}
	doc := readSecurity(t, env.s)
	if got := block(t, doc, "twofa")["totp_enroll"]; got != true {
		t.Error("the refused save stored part of its block")
	}

	// The other direction: no method enabled, then a mandate proposed.
	if code := saveSecurity(t, env.s, `{"twofa":{"totp_enroll":false,"passkey_enroll":false}}`); code != http.StatusBadRequest {
		t.Fatal("the guard should have refused the methods already")
	}
	// With the OU's mandate cleared, turning both off is fine...
	stuck := securityOf(t, env.s.st, env.must)
	if stuck.TOTPAllowed != true {
		t.Fatal("the test's own setup is not what it looks like")
	}
	if err := env.s.st.SetGroupRequire2FA(env.ouID, false); err != nil {
		t.Fatal(err)
	}
	if code := saveSecurity(t, env.s, `{"twofa":{"totp_enroll":false,"passkey_enroll":false}}`); code != http.StatusOK {
		t.Fatalf("with no mandate anywhere, turning the methods off must be allowed → %d", code)
	}
	// ...and now the mandate cannot be turned back on.
	if code := saveSecurity(t, env.s, `{"twofa":{"require_staff":true}}`); code != http.StatusBadRequest {
		t.Fatalf("a mandate with no method to satisfy it → %d, want 400", code)
	}
}

// The OU page is the other place a mandate is turned on, and it has to refuse the same impossible
// state — including when the OU is MOVED under a parent that mandates one.
func TestTheOUSaveRefusesAMandateWithNoMethodEnabled(t *testing.T) {
	env := mandateServer(t)
	if err := env.s.st.SetGroupRequire2FA(env.ouID, false); err != nil {
		t.Fatal(err)
	}
	env.s.st.SetSetting(setTOTPEnroll, "0")
	env.s.st.SetSetting(setPasskeyEnroll, "0")
	rec := putGroup(env.s, env.ouID, `{"name":"安禅内部","require_2fa":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("requiring a factor with no method enabled → %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if got := block(t, readSecurity(t, env.s), "twofa")["require_staff"]; got != false {
		t.Error("the refused OU save changed the portal's own mandate")
	}
}

// The number, not just the flag: turning a mandate on asks people to do something, and how many is
// the difference between a policy and a surprise.
func TestTheOUSaveReportsWhoWouldBeAskedToEnrol(t *testing.T) {
	env := mandateServer(t)
	for _, u := range []string{"a", "b"} {
		if err := env.s.st.UpsertUser(User{Username: u, PasswordHash: mustHash("correct-horse-battery"), Role: "user"}); err != nil {
			t.Fatal(err)
		}
		if err := env.s.st.SetPrimaryGroup(u, env.ouID); err != nil {
			t.Fatal(err)
		}
	}
	rec := putGroup(env.s, env.ouID, `{"name":"安禅内部","require_2fa":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save → %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Pending int `json:"pending_enrolment"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	// The OU holds must-enrol, child-user (from the fixture), a and b — all four without a factor.
	// already-has has one, and the accounts outside the OU are not counted at all.
	if out.Pending != 4 {
		t.Errorf("pending_enrolment = %d, want 4", out.Pending)
	}
}

// ---------- the rescue ----------

// The test the user asked for by name: the staff mandate is on, an administrator is held at the wall,
// and the CLI is what lets them back in.
//
// It is a test about the RESCUE PATH rather than about the CLI's wording, because the failure it
// guards is a policy that can be switched on from the product and off only by editing SQL. An
// operator with shell access has the database anyway; what they do not have is the web UI they are
// locked out of.
func TestTheSecurityCLILetsAnOperatorBackIn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "portal.db")
	cfgPath := writeTestConfig(t, dbPath)
	st, err := OpenStore("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUser(User{Username: "boss", PasswordHash: mustHash("correct-horse-battery"), Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	st.SetSetting(setRequire2FAForStaff, "1")
	s := &Server{st: st}

	// The administrator is held: this is the state the CLI has to be able to end.
	if must, err := s.mustEnroll("boss"); err != nil || !must {
		t.Fatalf("an un-enrolled administrator under the staff mandate must be held (must=%v err=%v)", must, err)
	}
	st.Close()

	out, err := SecurityCommand(cfgPath, []string{"show"})
	if err != nil {
		t.Fatalf("security show: %v", err)
	}
	if !strings.Contains(out, "staff                on") {
		t.Errorf("show does not report the mandate:\n%s", out)
	}

	if _, err := SecurityCommand(cfgPath, []string{"clear-mandate"}); err != nil {
		t.Fatalf("clear-mandate: %v", err)
	}
	after, err := OpenStore("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	s2 := &Server{st: after}
	if must, err := s2.mustEnroll("boss"); err != nil || must {
		t.Fatalf("after the rescue the administrator is still held (must=%v err=%v)", must, err)
	}
}

// `set` goes through the same validation as the settings page, so an operator cannot use the CLI to
// build the state the product refuses.
func TestTheSecurityCLIRefusesWhatThePageRefuses(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "portal.db")
	cfgPath := writeTestConfig(t, dbPath)
	if _, err := SecurityCommand(cfgPath, []string{"set", setTOTPEnroll, "false"}); err != nil {
		t.Fatalf("turning a method off with no mandate in force must be allowed: %v", err)
	}
	if _, err := SecurityCommand(cfgPath, []string{"set", setPasskeyEnroll, "false"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SecurityCommand(cfgPath, []string{"set", setRequire2FAForStaff, "true"}); err == nil {
		t.Fatal("the CLI stored a mandate with no method enabled, which the page refuses")
	}
	if _, err := SecurityCommand(cfgPath, []string{"set", "nonsense", "true"}); err == nil {
		t.Error("an unknown key must be refused rather than ignored")
	}
}
