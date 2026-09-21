package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The lockout: an optional, longer-lived refusal on top of the failure window the throttle has always
// had.
//
// Two things must not be confused, because one of them is a brake and the other is a policy:
//
//   - the IP ceiling (unchanged, always on) is what prices bcrypt before the password is checked. It
//     blocks a caller by ADDRESS, so a correct password from that address waits for the window to roll
//     over — which is the accepted cost of not handing an attacker unbounded bcrypt.
//   - the lockout is the operator's choice of a longer refusal, bound to a key they pick. At scope
//     `account` it refuses even a CORRECT password for its duration, which is the one setting here
//     that can lock an owner out of their own account. That is why it is off by default and why it is
//     tested as a deliberate break rather than as an accident.

// loginFrom drives the password leg from a given address.
func loginFrom(t *testing.T, s *Server, ip, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{"username":"`+user+`","password":"`+pass+`"}`))
	r.RemoteAddr = ip + ":5555"
	rec := httptest.NewRecorder()
	s.apiLogin(rec, r)
	return rec
}

func lockoutServer(t *testing.T) *Server {
	t.Helper()
	s := limitsServer(t)
	if err := s.st.UpsertUser(User{Username: "bob", PasswordHash: mustHash("correct-horse-battery"), Role: "user"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func failTwice(t *testing.T, s *Server, ip string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		loginFrom(t, s, ip, "bob", "wrong")
	}
}

// ---------- off by default ----------

// An unconfigured portal must behave exactly as it did: the window refuses wrong guesses, and no
// setting anywhere refuses a right one.
func TestTheLockoutIsOffUntilItIsConfigured(t *testing.T) {
	s := lockoutServer(t)
	if s.lockoutEnabled() {
		t.Fatal("the lockout ships off: an upgrade must not start refusing logins")
	}
	s.st.SetSetting(setLoginFailMax, "2")
	failTwice(t, s, "10.0.0.1")

	// The account key must never refuse a correct password. (The IP brake does block this ADDRESS for
	// the rest of the window — that is the brake doing its job, and it is why this logs in from
	// elsewhere.)
	if rec := loginFrom(t, s, "10.0.0.9", "bob", "correct-horse-battery"); rec.Code != http.StatusOK {
		t.Fatalf("a correct password must still succeed → %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------- scope: account ----------

// The documented break: with the lock bound to the ACCOUNT, a correct password stops working for the
// duration. It is opt-in, and this test exists so that nobody discovers it by accident.
func TestALockedAccountRefusesEvenACorrectPassword(t *testing.T) {
	s := lockoutServer(t)
	s.st.SetSetting(setLoginFailMax, "2")
	s.st.SetSetting(setLockoutEnabled, "1")
	s.st.SetSetting(setLockoutScope, lockoutScopeAccount)
	s.st.SetSetting(setLockoutDurationMin, "30")

	failTwice(t, s, "10.0.0.1")

	// From a different address entirely: the account itself is locked.
	rec := loginFrom(t, s, "10.0.0.9", "bob", "correct-horse-battery")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a locked account must refuse a correct password → %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------- scope: ip_account (the default) ----------

// The default scope binds to the address AND the account: an attacker cannot lock a victim they are
// not sitting behind, and a shared address is not punished for someone else's attempts on another
// account.
func TestAnIPAccountLockDoesNotFollowTheAccountElsewhere(t *testing.T) {
	s := lockoutServer(t)
	s.st.SetSetting(setLoginFailMax, "2")
	s.st.SetSetting(setLockoutEnabled, "1")
	s.st.SetSetting(setLockoutScope, lockoutScopeIPAccount)

	failTwice(t, s, "10.0.0.1")

	if rec := loginFrom(t, s, "10.0.0.1", "bob", "correct-horse-battery"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the locked pair must be refused → %d", rec.Code)
	}
	if rec := loginFrom(t, s, "10.0.0.2", "bob", "correct-horse-battery"); rec.Code != http.StatusOK {
		t.Errorf("the same account from another address must still work → %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------- the lock's lifetime ----------

// A lock is a refusal for its OWN duration, which is the whole point of having one: the window it was
// taken in may roll over first.
func TestTheLockOutlivesTheWindowItWasTakenIn(t *testing.T) {
	s := lockoutServer(t)
	s.st.SetSetting(setLoginFailMax, "1")
	s.st.SetSetting(setLoginFailWindowMin, "1")
	s.st.SetSetting(setLockoutEnabled, "1")
	s.st.SetSetting(setLockoutDurationMin, "5")
	s.loginThr.lockout = s.loginLockout

	now := time.Now()
	s.loginThr.record("u:bob", now)
	if !s.loginThr.blocked("u:bob", now) {
		t.Fatal("the ceiling of one did not block")
	}
	// The window has lapsed; the lock has not.
	if !s.loginThr.blocked("u:bob", now.Add(2*time.Minute)) {
		t.Error("the lock did not outlive the window it was taken in")
	}
	if s.loginThr.blocked("u:bob", now.Add(6*time.Minute)) {
		t.Error("the lock outlived its own duration")
	}
	// The captcha's counter is still the WINDOW's, not the lock's: it reports "recently suspicious",
	// which a refusal that outlives the window is not. fails() reads the real clock, so the window is
	// lapsed explicitly here rather than by waiting.
	s.loginThr.mu.Lock()
	s.loginThr.recs["u:bob"].resetAt = time.Now().Add(-time.Second)
	s.loginThr.mu.Unlock()
	if got := s.loginThr.fails("u:bob"); got != 0 {
		t.Errorf("fails() after the window lapsed = %d, want 0 (the lock is not a suspicion count)", got)
	}
	if !s.loginThr.blocked("u:bob", time.Now()) {
		t.Error("the lock must stand while the window it was taken in has lapsed")
	}
}

// The pruning that keeps the map bounded must never take a lock with it: a lock is a decision the
// operator asked for, and dropping it under a flood is exactly when an attacker would want it gone.
func TestAFloodOfKeysDoesNotDropALock(t *testing.T) {
	s := lockoutServer(t)
	s.st.SetSetting(setLoginFailMax, "1")
	s.st.SetSetting(setLoginFailWindowMin, "60")
	s.st.SetSetting(setLockoutEnabled, "1")
	s.st.SetSetting(setLockoutDurationMin, "60")
	s.loginThr.lockout = s.loginLockout

	now := time.Now()
	s.loginThr.record("u:locked", now)
	if !s.loginThr.blocked("u:locked", now) {
		t.Fatal("the key under test is not locked")
	}
	// A flood of distinct sources: enough live keys to defeat the opportunistic prune, which is the
	// path that used to drop the whole map.
	for i := 0; i < 6000; i++ {
		s.loginThr.record("ip:10."+string(rune('0'+i%10))+".0."+string(rune('0'+i%10)), now)
	}
	if !s.loginThr.blocked("u:locked", now) {
		t.Error("a flood dropped a lock: locked keys must survive the bound")
	}
}

// ---------- scope: ip ----------

// The IP scope names the key the brake already uses, so its effect is to hold that refusal for longer
// than the window — the brake itself is never replaced by the lockout.
func TestTheIPScopeExtendsTheAddressRefusal(t *testing.T) {
	s := lockoutServer(t)
	s.st.SetSetting(setLoginFailMax, "1")
	s.st.SetSetting(setLoginFailWindowMin, "1")
	s.st.SetSetting(setLockoutEnabled, "1")
	s.st.SetSetting(setLockoutDurationMin, "5")
	s.st.SetSetting(setLockoutScope, lockoutScopeIP)
	s.loginThr.lockout = s.loginLockout

	ipKey := "ip:10.0.0.1"
	now := time.Now()
	s.loginThr.record(ipKey, now)
	if !s.loginThr.blocked(ipKey, now.Add(2*time.Minute)) {
		t.Error("the address refusal did not survive the window it was taken in")
	}
	if s.loginThr.blocked(ipKey, now.Add(6*time.Minute)) {
		t.Error("the address refusal outlived the configured lock duration")
	}
}
