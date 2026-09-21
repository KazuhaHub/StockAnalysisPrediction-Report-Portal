package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/pquerna/otp/totp"
)

// The enrolment policy: which accounts may add a second factor, resolved down the OU tree.
//
// The switch says ALLOW TO ENROL, and nothing else. Turning it off does not disable an authenticator
// that is already registered, does not stop it signing in, and does not remove it — the same reading
// the sibling project's settings page states in its own hint. That reading is what decides the
// behaviour of everything below: an account that already enrolled stays able to authenticate, which
// is why nobody is locked out by an administrator withdrawing enrolment from their OU.

// policyServer is a store, a server, a child OU of the Default, and one account in each.
type policyEnv struct {
	s         *Server
	defaultID int64
	childID   int64
}

func policyServer(t *testing.T) policyEnv {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		loginThr: newLoginThrottle()}
	def := st.EnsureDefaultGroup()
	child, err := st.CreateUserGroup("安禅内部", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupParent(child, def); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"root-user", "child-user"} {
		if err := st.UpsertUser(User{Username: u, PasswordHash: mustHash("correct-horse-battery"), Role: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetPrimaryGroup("child-user", child); err != nil {
		t.Fatal(err)
	}
	return policyEnv{s: s, defaultID: def, childID: child}
}

func boolPtr(v bool) *bool { return &v }

// ---------- resolution ----------

// With nothing configured, every account may enrol — which is what the portal did before this
// existed, and what an upgrade must not change.
func TestEnrolmentDefaultsToAllowed(t *testing.T) {
	env := policyServer(t)
	for _, u := range []string{"root-user", "child-user", "nobody-at-all"} {
		got := env.s.st.SecuritySettings(u)
		if !got.TOTPAllowed || !got.PasskeyAllowed {
			t.Errorf("%s defaults to %+v, want both allowed", u, got)
		}
	}
}

// An OU that withdraws enrolment does so for its members, and for nobody else.
func TestAnOUWithdrawsEnrolmentFromItsMembersOnly(t *testing.T) {
	env := policyServer(t)
	if err := env.s.st.SetGroupEnrolment(env.childID, boolPtr(false), nil); err != nil {
		t.Fatal(err)
	}
	if got := env.s.st.SecuritySettings("child-user"); got.TOTPAllowed {
		t.Error("the child OU withdrew TOTP enrolment; its member still has it")
	}
	if got := env.s.st.SecuritySettings("child-user"); !got.PasskeyAllowed {
		t.Error("passkeys were not withdrawn and must stay allowed (NULL = inherit)")
	}
	if got := env.s.st.SecuritySettings("root-user"); !got.TOTPAllowed {
		t.Error("an account outside that OU must be unaffected")
	}
}

// Deeper wins: a child may allow what its parent withdrew, because the setting is a policy about its
// own members rather than a prohibition on the subtree.
func TestADeeperOUSettingWins(t *testing.T) {
	env := policyServer(t)
	// Default withdraws TOTP; the child re-allows it.
	if err := env.s.st.SetGroupEnrolment(env.defaultID, boolPtr(false), nil); err != nil {
		t.Fatal(err)
	}
	if err := env.s.st.SetGroupEnrolment(env.childID, boolPtr(true), nil); err != nil {
		t.Fatal(err)
	}
	if got := env.s.st.SecuritySettings("root-user"); got.TOTPAllowed {
		t.Error("the Default OU withdrew TOTP enrolment; an account directly in it must be refused")
	}
	if got := env.s.st.SecuritySettings("child-user"); !got.TOTPAllowed {
		t.Error("the child OU re-allowed it and must win over its parent")
	}
}

// An OU with no setting of its own takes its parent's, all the way up to the Default.
func TestAnOUWithNoSettingInheritsItsParent(t *testing.T) {
	env := policyServer(t)
	if err := env.s.st.SetGroupEnrolment(env.defaultID, nil, boolPtr(false)); err != nil {
		t.Fatal(err)
	}
	if got := env.s.st.SecuritySettings("child-user"); got.PasskeyAllowed {
		t.Error("the child sets nothing itself, so it inherits the Default's withdrawal")
	}
}

// The global switch is the floor: no OU can re-enable enrolment the portal as a whole has withdrawn.
// The two layers are visible here — Store.SecuritySettings answers for the OU, and the server ANDs the
// portal's switch onto it — because a test that read only one of them would pass with the other left
// out of the path entirely.
func TestTheGlobalSwitchOverridesEveryOU(t *testing.T) {
	env := policyServer(t)
	env.s.st.SetSetting(setTOTPEnroll, "0")
	if err := env.s.st.SetGroupEnrolment(env.childID, boolPtr(true), nil); err != nil {
		t.Fatal(err)
	}
	if !env.s.st.SecuritySettings("child-user").TOTPAllowed {
		t.Fatal("the OU is set to allow, so the OU layer must say allowed")
	}
	if env.s.totpEnrolAllowed("child-user") {
		t.Error("an OU cannot allow what the portal has switched off")
	}
	// And with the switch back on, the same OU's answer stands.
	env.s.st.SetSetting(setTOTPEnroll, "1")
	if !env.s.totpEnrolAllowed("child-user") {
		t.Error("with the switch on, the OU's own setting decides")
	}
}

// ---------- the enrolment endpoints ----------

// A withdrawn enrolment is refused at the endpoint, before step-up and before anything is written —
// the refusal the UI cannot talk its way past.
func TestTOTPEnrolmentIsRefusedWhereItIsWithdrawn(t *testing.T) {
	env := policyServer(t)
	// Enrol first, while it is allowed, so the second half of this test has a real credential to
	// remove rather than a staged one.
	secret, _ := enrol(t, env.s, "child-user")
	if err := env.s.st.SetGroupEnrolment(env.childID, boolPtr(false), nil); err != nil {
		t.Fatal(err)
	}
	rec := postAs(t, env.s.apiTOTPSetup, "child-user", `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("setup → %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	// ...and the same account may still withdraw the one it has: turning enrolment off is a policy
	// about ADDING a factor, so it must never trap anyone inside one.
	code, err := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if rec := postAs(t, env.s.apiTOTPDisable, "child-user", `{"code":"`+code+`"}`); rec.Code != http.StatusOK {
		t.Errorf("a withdrawn enrolment must still be removable: disable → %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestPasskeyEnrolmentIsRefusedWhereItIsWithdrawn(t *testing.T) {
	env := policyServer(t)
	if err := env.s.st.SetGroupEnrolment(env.childID, nil, boolPtr(false)); err != nil {
		t.Fatal(err)
	}
	rec := postAs(t, env.s.apiPasskeyRegisterBegin, "child-user", `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("register begin → %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// The switch is about ADDING a factor. An account that already has one keeps signing in with it,
// which is the whole reason withdrawing enrolment is not a lockout.
func TestAnEnrolledFactorStillSignsInWhenEnrolmentIsWithdrawn(t *testing.T) {
	env := policyServer(t)
	s := env.s
	// Enrol TOTP while it is allowed, then withdraw it for this OU.
	if err := s.st.UpsertUser(User{Username: "alice", PasswordHash: mustHash("correct-horse-battery"), Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetPrimaryGroup("alice", env.childID); err != nil {
		t.Fatal(err)
	}
	secret, _ := enrol(t, s, "alice")
	if err := s.st.SetGroupEnrolment(env.childID, boolPtr(false), nil); err != nil {
		t.Fatal(err)
	}
	// The password leg demands the second factor...
	rec := httptest.NewRecorder()
	s.apiLogin(rec, httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{"username":"alice","password":"correct-horse-battery"}`)))
	var first struct {
		Required bool   `json:"totp_required"`
		Token    string `json:"token"`
	}
	json.Unmarshal(rec.Body.Bytes(), &first)
	if !first.Required || first.Token == "" {
		t.Fatalf("an existing factor must still be demanded, got %s", rec.Body.String())
	}
	// ...and it is accepted, with enrolment off.
	code, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	rec2 := httptest.NewRecorder()
	s.apiLoginTOTP(rec2, httptest.NewRequest(http.MethodPost, "/api/login/2fa",
		strings.NewReader(`{"token":"`+first.Token+`","code":"`+code+`"}`)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("an enrolled factor must still sign in with enrolment withdrawn → %d (%s)", rec2.Code, rec2.Body.String())
	}
}

// /api/me carries the resolved policy so the account page can hide what would only fail on submit.
func TestMeReportsTheResolvedEnrolmentPolicy(t *testing.T) {
	env := policyServer(t)
	if err := env.s.st.SetGroupEnrolment(env.childID, boolPtr(false), nil); err != nil {
		t.Fatal(err)
	}
	read := func(user string) map[string]any {
		var out map[string]any
		b, _ := json.Marshal(env.s.meJSON(user))
		json.Unmarshal(b, &out)
		return out
	}
	got := read("child-user")
	if got["totp_allowed"] != false || got["passkey_allowed"] != true {
		t.Errorf("a member of the OU reads %v, want totp_allowed=false passkey_allowed=true", got)
	}
	if other := read("root-user"); other["totp_allowed"] != true {
		t.Errorf("an account outside the OU reads %v", other)
	}
}
