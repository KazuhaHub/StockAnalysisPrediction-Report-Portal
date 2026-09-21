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
}

// SecuritySettings resolves the account's OU chain. The baseline is permissive — an unconfigured
// portal, and an account in no OU at all, may enrol everything, which is what it could do before any
// of this existed.
func (s *Store) SecuritySettings(username string) SecuritySettings {
	out := SecuritySettings{TOTPAllowed: true, PasskeyAllowed: true}
	for _, gid := range s.groupChain(username) {
		totp, passkey := s.rawGroupEnrolment(gid)
		if totp != nil {
			out.TOTPAllowed = *totp
		}
		if passkey != nil {
			out.PasskeyAllowed = *passkey
		}
	}
	return out
}

// rawGroupEnrolment reads one OU's overrides; nil means it sets nothing and inherits.
func (s *Store) rawGroupEnrolment(id int64) (totp, passkey *bool) {
	var (
		t sql.NullInt64
		p sql.NullInt64
	)
	if err := s.queryRow(`SELECT totp_enroll, passkey_enroll FROM user_groups WHERE id=?`, id).Scan(&t, &p); err != nil {
		return nil, nil
	}
	ptr := func(v sql.NullInt64) *bool {
		if !v.Valid {
			return nil
		}
		b := v.Int64 != 0
		return &b
	}
	return ptr(t), ptr(p)
}

// SetGroupEnrolment writes an OU's overrides; a nil pointer clears it back to inherit. The column is
// what the whole tree reads, so clearing has to write NULL rather than 0 — 0 would withdraw enrolment
// from every member of the OU instead of deferring to its parent.
func (s *Store) SetGroupEnrolment(id int64, totp, passkey *bool) error {
	val := func(p *bool) any {
		if p == nil {
			return nil
		}
		if *p {
			return 1
		}
		return 0
	}
	_, err := s.exec(`UPDATE user_groups SET totp_enroll=?, passkey_enroll=? WHERE id=?`,
		val(totp), val(passkey), id)
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
func (s *Server) totpEnrolAllowed(username string) bool {
	if !s.switchOn(setTOTPEnroll, true) {
		return false
	}
	if s.st == nil {
		return true
	}
	return s.st.SecuritySettings(username).TOTPAllowed
}

func (s *Server) passkeyEnrolAllowed(username string) bool {
	if !s.switchOn(setPasskeyEnroll, true) {
		return false
	}
	if s.st == nil {
		return true
	}
	return s.st.SecuritySettings(username).PasskeyAllowed
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
	// Nothing above this line has been stored: one bad field must not leave the rest of the block
	// saved with the form showing a mixture of old and new.
	if in.TwoFA != nil {
		if in.TwoFA.TOTPEnroll != nil {
			s.st.SetSetting(setTOTPEnroll, boolSetting(*in.TwoFA.TOTPEnroll))
		}
		if in.TwoFA.PasskeyEnroll != nil {
			s.st.SetSetting(setPasskeyEnroll, boolSetting(*in.TwoFA.PasskeyEnroll))
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
