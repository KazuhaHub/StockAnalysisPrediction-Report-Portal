package app

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The login-protection policy: which accounts may enrol a second factor, whether password recovery
// by email is offered, and how a run of failed logins is locked out.
//
// Two shapes of setting live here and they are deliberately different:
//
//   - the GLOBAL switches are scalars in `meta`, read through the same helpers as every other
//     setting (`settingBool`, `settingInt`), and they are the floor — an OU cannot re-enable what the
//     portal has switched off;
//   - the PER-OU enrolment rules are nullable columns on `user_groups` (migration 0001), resolved
//     down the OU tree exactly like the run-governance columns: NULL = inherit from the OU above,
//     deeper wins. That is ADR 0022's model, reused rather than paralleled.
//
// The enrolment switch means ALLOW TO ENROL and nothing else. It does not disable a factor that is
// already registered, does not stop one signing in, and does not remove it. That reading is what
// makes withdrawing enrolment a policy rather than a lockout — an account that already has a second
// factor keeps working — and it is the reading the settings page states in its own hint.

const (
	setTOTPEnroll         = "twofa_totp_enroll"
	setPasskeyEnroll      = "twofa_passkey_enroll"
	setPasswordRecovery   = "password_recovery_enabled"
	setLockoutEnabled     = "lockout_enabled"
	setLockoutDurationMin = "lockout_duration_min"
	setLockoutScope       = "lockout_scope"
	// The staff mandate: every administrator and operator must have a second factor. It is the
	// portal's own floor, alongside the per-OU one on user_groups.
	setRequire2FAForStaff = "require_2fa_for_staff"
)

// The lockout's shipped values. Off is the default because the throttle it extends has always been
// a window counter, and a lock is a new, longer-reaching behaviour: an upgrade must not start
// refusing logins it used to accept.
const (
	defLockoutDurationMin = 30
	maxLockoutDurationMin = 7 * 24 * 60 // a week: long enough for any policy, short enough to be a typo guard
)

// Lockout scopes: which key a lock binds to.
const (
	lockoutScopeIP        = "ip"
	lockoutScopeIPAccount = "ip_account"
	lockoutScopeAccount   = "account"
)

// SecuritySettings is one account's resolved enrolment policy.
type SecuritySettings struct {
	TOTPAllowed    bool
	PasskeyAllowed bool
	// Require2FA is the mandate: this account MUST have a second factor.
	Require2FA bool
}

// SecuritySettings resolves the account's OU chain. The baseline is permissive — an unconfigured
// portal, and an account in no OU at all, may enrol everything, which is what it could do before any
// of this existed.
//
// The error is the whole reason this returns one. The two method switches ride in front of a UI (a
// failed read there is best read as "allowed", which is the shipped behaviour and locks nobody out),
// while the mandate rides in front of a GATE (a failed read there has to refuse). One read, and each
// caller decides which way its own failure falls.
func (s *Store) SecuritySettings(username string) (SecuritySettings, error) {
	out := SecuritySettings{TOTPAllowed: true, PasskeyAllowed: true}
	chain, err := s.groupChainErr(username)
	if err != nil {
		return SecuritySettings{}, err
	}
	for _, gid := range chain {
		raw, err := s.rawGroupSecurity(gid)
		if err != nil {
			return SecuritySettings{}, err
		}
		if raw.totp != nil {
			out.TOTPAllowed = *raw.totp
		}
		if raw.passkey != nil {
			out.PasskeyAllowed = *raw.passkey
		}
		// STICKY, not deeper-wins: a parent OU that requires a second factor of its division cannot
		// be un-required by a child, or the mandate is advice. That is the same argument ADR 0022
		// makes for `restricted`.
		if raw.require != nil && *raw.require {
			out.Require2FA = true
		}
	}
	return out, nil
}

// rawGroupSecurity reads one OU's overrides; nil means it sets nothing and inherits.
func (s *Store) rawGroupSecurity(id int64) (raw struct{ totp, passkey, require *bool }, err error) {
	var t, p, r sql.NullInt64
	if err := s.queryRow(`SELECT totp_enroll, passkey_enroll, require_2fa FROM user_groups WHERE id=?`, id).
		Scan(&t, &p, &r); err != nil {
		return raw, err
	}
	ptr := func(v sql.NullInt64) *bool {
		if !v.Valid {
			return nil
		}
		b := v.Int64 != 0
		return &b
	}
	raw.totp, raw.passkey, raw.require = ptr(t), ptr(p), ptr(r)
	return raw, nil
}

// SetGroupEnrolment writes an OU's overrides; a nil pointer clears it back to inherit. The column is
// what the whole tree reads, so clearing has to write NULL rather than 0 — 0 would withdraw enrolment
// from every member of the OU instead of deferring to its parent.
func (s *Store) SetGroupEnrolment(id int64, totp, passkey, require *bool) error {
	val := func(p *bool) any {
		if p == nil {
			return nil
		}
		if *p {
			return 1
		}
		return 0
	}
	_, err := s.exec(`UPDATE user_groups SET totp_enroll=?, passkey_enroll=?, require_2fa=? WHERE id=?`,
		val(totp), val(passkey), val(require), id)
	return err
}

// GroupRequires2FA reports whether a mandate applies to an OU, counting its ancestors: the flag is
// sticky down the tree, so an OU under a mandated parent is mandated too.
func (s *Store) GroupRequires2FA(id int64) bool {
	for _, gid := range s.groupAncestry(id, s.DefaultGroupID()) {
		raw, err := s.rawGroupSecurity(gid)
		if err != nil {
			continue
		}
		if raw.require != nil && *raw.require {
			return true
		}
	}
	return false
}

// SetGroupRequire2FA sets one OU's mandate on its own.
func (s *Store) SetGroupRequire2FA(id int64, on bool) error {
	_, err := s.exec(`UPDATE user_groups SET require_2fa=? WHERE id=?`, boolSetting(on), id)
	return err
}

// ---------- the global switches ----------

// settingBool reads a meta scalar as a boolean, with the shipped value as the fallback for anything
// unreadable — absent, blank, or written by a build whose vocabulary differed.
func (s *Server) switchOn(key string, shipped bool) bool {
	if s.st == nil {
		return shipped
	}
	return settingBool(s.st.GetSetting(key, ""), shipped)
}

// totpEnrolAllowed / passkeyEnrolAllowed are the whole answer for one account: the portal's switch AND
// the account's OU, in that order.
// enrolmentSettings is the resolved policy, with a failed read answered as "allowed". That is the
// direction the shipped behaviour lies in, and it cannot lock anybody out — unlike the mandate below,
// which reads the same thing and refuses when it cannot.
func (s *Server) enrolmentSettings(username string) SecuritySettings {
	if s.st == nil {
		return SecuritySettings{TOTPAllowed: true, PasskeyAllowed: true}
	}
	sts, err := s.st.SecuritySettings(username)
	if err != nil {
		return SecuritySettings{TOTPAllowed: true, PasskeyAllowed: true}
	}
	return sts
}

func (s *Server) totpEnrolAllowed(username string) bool {
	if !s.switchOn(setTOTPEnroll, true) {
		return false
	}
	return s.enrolmentSettings(username).TOTPAllowed
}

func (s *Server) passkeyEnrolAllowed(username string) bool {
	if !s.switchOn(setPasskeyEnroll, true) {
		return false
	}
	return s.enrolmentSettings(username).PasskeyAllowed
}

// passwordRecoveryEnabled is whether the portal offers the emailed reset link at all. It is a plain
// switch: the reset path's other conditions (an active local account with an address, and a mail
// service) are unchanged, and the response stays constant either way so the switch cannot become an
// account-existence oracle.
func (s *Server) passwordRecoveryEnabled() bool { return s.switchOn(setPasswordRecovery, true) }

// requireTOTPEnrolment / requirePasskeyEnrolment write the refusal and report whether the caller may
// go ahead. They are checked BEFORE step-up: a policy refusal is not something a password can change,
// and asking for proof of identity first would be a confusing way to say "not for your account".
func (s *Server) requireTOTPEnrolment(w http.ResponseWriter, username string) bool {
	if s.totpEnrolAllowed(username) {
		return true
	}
	jsonErrorCode(w, http.StatusForbidden, "enrolment_disabled", "authenticator-app enrolment is turned off for this account")
	return false
}

func (s *Server) requirePasskeyEnrolment(w http.ResponseWriter, username string) bool {
	if s.passkeyEnrolAllowed(username) {
		return true
	}
	jsonErrorCode(w, http.StatusForbidden, "enrolment_disabled", "passkey registration is turned off for this account")
	return false
}

// ---------- the mandate, and the wall it puts up ----------

// required2FA is whether a second factor is demanded of this account: its OU's mandate, or the
// portal's for the roles that administer it.
//
// The error is not decoration: the gate below refuses when this cannot be answered, and the only way
// for it to know is for the read to say so.
func (s *Server) required2FA(username string) (bool, error) {
	if s.st == nil {
		return false, nil
	}
	sts, err := s.st.SecuritySettings(username)
	if err != nil {
		return false, err
	}
	if sts.Require2FA {
		return true, nil
	}
	if !s.switchOn(setRequire2FAForStaff, false) || !s.hasPerm(username, PermManage) {
		return false, nil
	}
	return true, nil
}

// mustEnroll reports whether this account is required to have a second factor and does not have one.
//
// There is deliberately NO "and it could enrol one" clause. A mandate that switches itself off when
// no method is enabled is a policy that fails open and says nothing; the account stays held, the
// account page says why, and the operator has the CLI (the rescue path in docs/releases). The one
// exemption is a FEDERATED account: its factors belong to its IdP, so a local wall is one it can
// never climb.
func (s *Server) mustEnroll(username string) (bool, error) {
	required, err := s.required2FA(username)
	if err != nil {
		return false, err
	}
	if !required {
		return false, nil
	}
	u := s.st.GetUser(username)
	if u == nil || u.IsFederated() {
		return false, nil
	}
	if u.TOTPEnabled || len(s.st.PasskeyList(username)) > 0 {
		return false, nil
	}
	return true, nil
}

// enrolmentPaths are the routes an account may still reach while the wall is up: what it needs to
// enrol a factor, what it needs to prove itself in order to, and what it needs to sign out.
//
// Exact method + pattern, never a prefix. A wildcard here would be a hole nobody could see, and the
// routes_test.go behavioural test walks the table rather than this list — so a route added later is
// either gated or has to be added HERE, deliberately.
var enrolmentPaths = map[string]bool{
	"GET /api/me":                           true, // how the SPA learns it is at the wall
	"POST /api/me/password":                 true, // step-up proof for the enrolment routes
	"GET /api/account/stepup/policy":        true,
	"POST /api/me/2fa/setup":                true,
	"POST /api/me/2fa/enable":               true,
	"POST /api/me/2fa/disable":              true, // removing a factor is the account's business too
	"GET /api/me/passkeys":                  true,
	"POST /api/me/passkeys/register/begin":  true,
	"POST /api/me/passkeys/register/finish": true,
	"DELETE /api/me/passkeys/{id}":          true,
	"POST /api/logout":                      true, // an account that cannot enrol must be able to leave
}

// membersWithoutFactor counts the accounts in an OU's subtree that would meet the wall: they are
// subject to the mandate and have no second factor. The number is what an admin needs before they
// turn a mandate on — "eleven people will be asked to enrol at their next sign-in" is a decision,
// and "the mandate is on" is not.
func (s *Store) membersWithoutFactor(groupID int64) (int, error) {
	ids, err := s.subtreeIDs(groupID)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	var n int
	err = s.queryRow(`SELECT COUNT(*) FROM users u
		WHERE u.group_id IN (`+placeholders+`)
		  AND COALESCE(u.totp_enabled,0)=0
		  AND COALESCE(u.source,'local')='local'
		  AND NOT EXISTS (SELECT 1 FROM webauthn_credentials w WHERE w.username=u.username)`, args...).Scan(&n)
	return n, err
}

// subtreeIDs is an OU and every OU under it.
func (s *Store) subtreeIDs(root int64) ([]int64, error) {
	if root == 0 {
		return nil, nil
	}
	rows, err := s.query(`SELECT id, parent_id FROM user_groups`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	children := map[int64][]int64{}
	for rows.Next() {
		var id int64
		var parent sql.NullInt64
		if err := rows.Scan(&id, &parent); err != nil {
			return nil, err
		}
		children[parent.Int64] = append(children[parent.Int64], id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out, stack := []int64{}, []int64{root}
	seen := map[int64]bool{}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		stack = append(stack, children[id]...)
	}
	return out, nil
}

// enrolmentReachable reports whether a pattern is on that list.
func (s *Server) enrolmentReachable(pattern string) bool { return enrolmentPaths[pattern] }

// gateEnrolment is the wall itself, called by the session helpers after they have resolved who is
// asking. It answers false once it has written the refusal.
//
// Fail-closed, and that is the whole design: this decides whether business data is readable, so an
// unreadable policy is a refusal with a code the client can retry on, never a pass. The one place it
// fails open is a route on the list above, which is a decision rather than an accident.
func (s *Server) gateEnrolment(w http.ResponseWriter, r *http.Request, username string) bool {
	if username == "" || s.enrolmentReachable(r.Pattern) {
		return true
	}
	must, err := s.mustEnroll(username)
	if err != nil {
		jsonErrorCode(w, http.StatusServiceUnavailable, "enrolment_policy_unavailable",
			"the sign-in policy could not be read, so this request was refused rather than allowed")
		return false
	}
	if !must {
		return true
	}
	jsonErrorCode(w, http.StatusForbidden, "enrolment_required",
		"this account must set up a second factor before it can be used")
	return false
}

// gateEnrolmentPage is the same wall for the server-rendered report pages: a browser is sent to the
// account page rather than shown a status code, and a policy that cannot be read answers 503 rather
// than the page it was asked for.
func (s *Server) gateEnrolmentPage(w http.ResponseWriter, r *http.Request, username string) bool {
	if username == "" || s.enrolmentReachable(r.Pattern) {
		return true
	}
	must, err := s.mustEnroll(username)
	if err != nil {
		http.Error(w, "the sign-in policy could not be read, so this page was not served", http.StatusServiceUnavailable)
		return false
	}
	if !must {
		return true
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
	return false
}

// ---------- the lockout ----------

func (s *Server) lockoutEnabled() bool { return s.switchOn(setLockoutEnabled, false) }

// lockoutDuration is how long a key stays locked once it reaches the failure ceiling.
func (s *Server) lockoutDuration() time.Duration {
	return time.Duration(s.settingInt(setLockoutDurationMin, defLockoutDurationMin, 1, maxLockoutDurationMin)) * time.Minute
}

// lockoutScope is which key a lock binds to, falling back to the shipped default for anything
// unrecognised — a value written by a build with a wider vocabulary must not become "no scope".
func (s *Server) lockoutScope() string {
	if s.st == nil {
		return lockoutScopeIPAccount
	}
	switch s.st.GetSetting(setLockoutScope, "") {
	case lockoutScopeIP, lockoutScopeAccount:
		return s.st.GetSetting(setLockoutScope, "")
	case lockoutScopeIPAccount:
		return lockoutScopeIPAccount
	default:
		return lockoutScopeIPAccount
	}
}

// loginLockout is the lockout policy as the throttle reads it: whether to lock, and for how long.
// Handed over as a function rather than a pair of values, for the same reason the ceiling is — a
// change on the settings page applies to the next attempt, not at the next restart.
func (s *Server) loginLockout() (bool, time.Duration) {
	if !s.lockoutEnabled() {
		return false, 0
	}
	return true, s.lockoutDuration()
}

// lockoutKey names the key a lock binds to under the configured scope. The scope decides WHICH key;
// it never decides whether the IP brake above runs, which is not a setting.
//
//	ip         the address alone — the same key the brake uses, so the effect is to hold that refusal
//	           past the window it was taken in
//	ip_account the address and the account together: an attacker elsewhere cannot lock a victim, and a
//	           shared address is not punished for someone else's attempts
//	account    the account alone. This is the one that refuses a CORRECT password for the duration.
func (s *Server) lockoutKey(ip, username string) string {
	switch s.lockoutScope() {
	case lockoutScopeIP:
		return "ip:" + ip
	case lockoutScopeAccount:
		return "u:" + strings.ToLower(username)
	default:
		return "ipu:" + ip + "|" + strings.ToLower(username)
	}
}

// validLockoutScope reports whether a submitted scope is one this build knows.
func validLockoutScope(v string) bool {
	return v == lockoutScopeIP || v == lockoutScopeIPAccount || v == lockoutScopeAccount
}

// ---------- the settings page ----------

// securityPolicyInput is the policy half of POST /api/admin/security.
//
// Every field is a pointer, so a body that omits one leaves the stored value alone. That is the same
// convention `limits` and `login` use on this endpoint, and it is what lets the page save the blocks
// it renders without clearing the ones it does not.
type securityPolicyInput struct {
	TwoFA *struct {
		TOTPEnroll    *bool `json:"totp_enroll"`
		PasskeyEnroll *bool `json:"passkey_enroll"`
		// The staff mandate. It sits with the methods because the two are one decision: a mandate
		// with no method enabled is a wall nobody can climb.
		RequireStaff *bool `json:"require_staff"`
	} `json:"twofa"`
	Recovery *struct {
		Enabled *bool `json:"enabled"`
	} `json:"recovery"`
	Lockout *struct {
		Enabled     *bool   `json:"enabled"`
		DurationMin *int    `json:"duration_min"`
		Scope       *string `json:"scope"`
	} `json:"lockout"`
}

// proposedPolicy is the state a save would leave behind, so the check below can be made on the
// RESULT rather than on the one field that changed. Turning both methods off is harmless until some
// OU requires a factor; enabling a mandate is harmless until no method is left. Neither is wrong on
// its own, and together they are a locked door with the key inside — the same shape as the
// sso_only-with-registration guard next door.
type proposedPolicy struct {
	totp, passkey, staff *bool
	ouID                 int64
	ouRequire            *bool
}

func or(p *bool, fallback bool) bool {
	if p == nil {
		return fallback
	}
	return *p
}

// unsatisfiableMandates names what a proposed policy would strand: every OU (and the staff mandate)
// that would require a second factor while no method is enabled at all. Empty means the state is
// satisfiable. An error is a policy that could not be read, which the caller refuses the save for.
//
// It deliberately asks the question on the EFFECTIVE state, which is also what makes an OU MOVE safe
// to check with the same call: a move changes which ancestors apply, and the OU that sets the flag is
// what this names — whether the flag is already stored or is the one being saved right now.
func (s *Server) unsatisfiableMandates(p proposedPolicy) ([]string, error) {
	methodsOn := or(p.totp, s.switchOn(setTOTPEnroll, true)) || or(p.passkey, s.switchOn(setPasskeyEnroll, true))
	if methodsOn {
		return nil, nil
	}
	var stuck []string
	if or(p.staff, s.switchOn(setRequire2FAForStaff, false)) {
		stuck = append(stuck, "staff")
	}
	for _, g := range s.st.ListUserGroups() {
		require := g.Require2FA
		if g.ID == p.ouID && p.ouRequire != nil {
			require = *p.ouRequire
		}
		if require {
			stuck = append(stuck, g.Name)
		}
	}
	return stuck, nil
}

// applySecurityPolicy validates and stores the policy blocks, returning false once it has answered
// 400. Modelled on applyLimits: validated as a whole before anything is stored, and out of range is
// REFUSED rather than clamped — an operator who typed a duration the portal will not honour asked for
// something it will not do, and quietly storing 30 minutes instead would leave them believing a policy
// they never wrote.
func (s *Server) applySecurityPolicy(w http.ResponseWriter, in *securityPolicyInput) bool {
	if in.Lockout != nil {
		if in.Lockout.DurationMin != nil {
			if d := *in.Lockout.DurationMin; d < 1 || d > maxLockoutDurationMin {
				jsonErrorCode(w, http.StatusBadRequest, "bad_limit", "无效的安全限制取值：lockout_duration_min")
				return false
			}
		}
		if in.Lockout.Scope != nil && !validLockoutScope(strings.TrimSpace(*in.Lockout.Scope)) {
			jsonErrorCode(w, http.StatusBadRequest, "bad_lockout_scope",
				"lockout scope must be ip | ip_account | account")
			return false
		}
	}
	// A mandate with no method enabled is refused rather than stored: nobody could satisfy it, so it
	// is not a strict policy but a lockout — and one that would take effect at the next sign-in.
	if in.TwoFA != nil {
		var staff *bool
		if in.TwoFA != nil {
			staff = in.TwoFA.RequireStaff
		}
		var totp, passkey *bool
		if in.TwoFA != nil {
			totp, passkey = in.TwoFA.TOTPEnroll, in.TwoFA.PasskeyEnroll
		}
		stuck, err := s.unsatisfiableMandates(proposedPolicy{totp: totp, passkey: passkey, staff: staff})
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "the security policy could not be read")
			return false
		}
		if len(stuck) > 0 {
			jsonErrorCode(w, http.StatusBadRequest, "unsatisfiable_mandate",
				"turning both second-factor methods off would leave the mandate unsatisfiable for: "+
					strings.Join(stuck, ", ")+" — enable a method, or turn the mandate off first")
			return false
		}
	}
	// Nothing above this line has been stored: one bad field must not leave the rest of the block
	// saved with the form showing a mixture of old and new.
	if in.TwoFA != nil {
		if in.TwoFA.TOTPEnroll != nil {
			s.st.SetSetting(setTOTPEnroll, boolSetting(*in.TwoFA.TOTPEnroll))
		}
		if in.TwoFA.PasskeyEnroll != nil {
			s.st.SetSetting(setPasskeyEnroll, boolSetting(*in.TwoFA.PasskeyEnroll))
		}
		if in.TwoFA.RequireStaff != nil {
			s.st.SetSetting(setRequire2FAForStaff, boolSetting(*in.TwoFA.RequireStaff))
		}
	}
	if in.Recovery != nil && in.Recovery.Enabled != nil {
		s.st.SetSetting(setPasswordRecovery, boolSetting(*in.Recovery.Enabled))
	}
	if in.Lockout != nil {
		if in.Lockout.Enabled != nil {
			s.st.SetSetting(setLockoutEnabled, boolSetting(*in.Lockout.Enabled))
		}
		if in.Lockout.DurationMin != nil {
			s.st.SetSetting(setLockoutDurationMin, strconv.Itoa(*in.Lockout.DurationMin))
		}
		if in.Lockout.Scope != nil {
			s.st.SetSetting(setLockoutScope, strings.TrimSpace(*in.Lockout.Scope))
		}
	}
	return true
}
