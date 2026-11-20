package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// A rotation retires the old token by shortening its expiry. Restoring an older backup must not
// bring the longer expiry back.
func testMachineTokenRestoreKeepsShortenedExpiry(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	platformSeedActionFixtures(t, st)
	inst := platformFixtureID(1)
	far := time.Now().Add(48 * time.Hour).UnixMilli()
	if err := machineTokenInsert(st, platformFixtureID(91), inst, "aaaaaaaaaaaa", strings.Repeat("a", 64), "active", nil, far); err != nil {
		t.Fatal(err)
	}
	backup := dumpOf(t, st)
	retired := time.Now().UnixMilli() - 1000
	mustExec(t, st, `UPDATE app_platform_machine_tokens SET expires_ms=?,revision=2,updated_ms=? WHERE handle='aaaaaaaaaaaa'`, retired, retired)
	if _, err := st.restoreFrom(bytes.NewReader(backup), true); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int64](t, st, `SELECT expires_ms FROM app_platform_machine_tokens WHERE handle='aaaaaaaaaaaa'`); got != retired {
		t.Fatalf("the restore lengthened a rotated-away token: expires_ms %d, want %d", got, retired)
	}
	// A restored expiry that is already shorter than the live one is kept.
	mustExec(t, st, `UPDATE app_platform_machine_tokens SET expires_ms=? WHERE handle='aaaaaaaaaaaa'`, far)
	soon := far - 1000
	mustExec(t, st, `UPDATE app_platform_machine_tokens SET expires_ms=? WHERE handle='aaaaaaaaaaaa'`, soon)
	backup = dumpOf(t, st)
	mustExec(t, st, `UPDATE app_platform_machine_tokens SET expires_ms=? WHERE handle='aaaaaaaaaaaa'`, far)
	if _, err := st.restoreFrom(bytes.NewReader(backup), true); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int64](t, st, `SELECT expires_ms FROM app_platform_machine_tokens WHERE handle='aaaaaaaaaaaa'`); got != soon {
		t.Fatalf("expires_ms %d, want the backup's %d", got, soon)
	}
}

func TestMachineTokenRestoreKeepsShortenedExpirySQLite(t *testing.T) {
	testMachineTokenRestoreKeepsShortenedExpiry(t, false)
}
func TestMachineTokenRestoreKeepsShortenedExpiryPostgres(t *testing.T) {
	testMachineTokenRestoreKeepsShortenedExpiry(t, true)
}

// An active token that has already expired authorises nothing, so losing it is not reported.
func TestMachineTokenRestoreDoesNotCountExpiredTokensAsLost(t *testing.T) {
	st := newTestStore(t)
	platformSeedActionFixtures(t, st)
	inst := platformFixtureID(1)
	backup := dumpOf(t, st)
	live := time.Now().Add(time.Hour).UnixMilli()
	if err := machineTokenInsert(st, platformFixtureID(92), inst, "aaaaaaaaaaaa", strings.Repeat("a", 64), "active", nil, live); err != nil {
		t.Fatal(err)
	}
	if err := machineTokenInsert(st, platformFixtureID(93), inst, "bbbbbbbbbbbb", strings.Repeat("b", 64), "active", nil, 90_000); err != nil {
		t.Fatal(err)
	}
	rep, err := st.restoreFrom(bytes.NewReader(backup), true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.MachineTokensLost != 1 {
		t.Fatalf("lost %d, want 1 (the expired token is not counted)", rep.MachineTokensLost)
	}
}

// A runner that does not answer says nothing about its support: the failure is not remembered.
func TestMachineRunnerSupportDoesNotCacheATransportFailure(t *testing.T) {
	e := platformMachineFixture(t, false)
	endpoint := platformRuntimeEndpoint{path: "/run/fixture/control.sock", controllerUID: 1000, controllerGID: 1000}
	var asked int
	fail := true
	e.s.platformMachineSupportQuery = func(context.Context, platformRuntimeEndpoint) (transport.MachineSupport, error) {
		asked++
		if fail {
			return transport.MachineSupport{}, errors.New("timeout")
		}
		return transport.NewMachineSupport(), nil
	}
	if e.s.machineRunnerSupport(context.Background(), endpoint, time.Now()) {
		t.Fatal("a failed query reported support")
	}
	fail = false
	if !e.s.machineRunnerSupport(context.Background(), endpoint, time.Now()) || asked != 2 {
		t.Fatal("the failure was remembered", asked)
	}
	// A definite "unsupported" is remembered.
	e.s.platformMachine.mu.Lock()
	e.s.platformMachine.support.until = time.Now().Add(-time.Second)
	e.s.platformMachine.mu.Unlock()
	e.s.platformMachineSupportQuery = func(context.Context, platformRuntimeEndpoint) (transport.MachineSupport, error) {
		asked++
		return transport.MachineSupport{}, transport.ErrMachineSupportUnsupported
	}
	for range 2 {
		if e.s.machineRunnerSupport(context.Background(), endpoint, time.Now()) {
			t.Fatal("unsupported reported as supported")
		}
	}
	if asked != 3 {
		t.Fatal("a definite answer was not remembered", asked)
	}
}

// Authenticated traffic is governed by the token's own limit: the per-source ceiling counts only
// requests that did not authenticate.
func TestMachineSourceCeilingCountsOnlyFailedAttempts(t *testing.T) {
	e := platformMachineFixture(t, false)
	if w := e.enableWith(t, []string{"bars"}, 50, 16); w.Code != 200 {
		t.Fatal(w.Code)
	}
	made := e.create(t, "Busy", []string{"bars"}, 600, []string{}, 90)
	for i := 0; i < platformMachineSourcePerMinute+20; i++ {
		if w := e.data(made.Token, "nope", "", "10.70.0.1:1"); w.Code != 404 {
			t.Fatal("an authenticated request was capped by the source ceiling", i, w.Code)
		}
	}
	e.s.platformMachine.mu.Lock()
	st := e.s.platformMachine.stats[""]
	e.s.platformMachine.mu.Unlock()
	if st != nil && st.refusals["request_unauthenticated"] != 0 {
		t.Fatal("authenticated requests were counted as unauthenticated", st.refusals)
	}
	// Failed attempts fill the ceiling, and then even a good token from that address waits.
	for range platformMachineSourcePerMinute {
		e.get("/machine/apps/"+e.id+"/data/bars", "10.70.0.2:1", nil)
	}
	if w := e.data(made.Token, "bars", "", "10.70.0.2:1"); w.Code != 429 {
		t.Fatal("source ceiling after failures", w.Code)
	}
	if w := e.data(made.Token, "bars", "", "10.70.0.3:1"); w.Code != 200 {
		t.Fatal("another address", w.Code)
	}
}

// The expiry sweep moves on: tokens that were audited, or retired by a rotation, are not read again
// and do not crowd out newer expiries.
func TestMachineExpirySweepReachesEveryExpiredToken(t *testing.T) {
	st := newTestStore(t)
	platformSeedActionFixtures(t, st)
	s := &Server{st: st}
	inst := platformFixtureID(1)
	now := time.Now()
	const n = 105
	for i := 0; i < n; i++ {
		handle := "a" + strings.Repeat("b", 3) + fmt.Sprintf("%08s", strings.NewReplacer("0", "c", "1", "d", "2", "e", "3", "f", "4", "g", "5", "h", "6", "i", "7", "j", "8", "k", "9", "l").Replace(fmt.Sprint(i)))
		if err := machineTokenInsert(st, platformFixtureID(100+i), inst, handle, fmt.Sprintf("%064d", i), "active", nil, now.UnixMilli()-int64(n-i)*1000); err != nil {
			t.Fatal(err)
		}
	}
	// One of them was retired by a rotation, which has its own audit row.
	rotated := scalar[string](t, st, `SELECT handle FROM app_platform_machine_tokens ORDER BY expires_ms LIMIT 1`)
	mustExec(t, st, `INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'admin',0,?,'app_platform_installation',?,?,'')`,
		now.UTC().Format(time.RFC3339), platformMachineTokenRotatedAudit, inst, `{"handle":"`+rotated+`","successor":"zzzzzzzzzzzz"}`)
	for range 3 {
		s.sweepPlatformMachineExpiry(now)
	}
	if got := scalar[int](t, st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, platformMachineTokenExpiredAudit); got != n-1 {
		t.Fatalf("%d expiry audits, want %d (every token but the rotated one)", got, n-1)
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM audit_log WHERE action=? AND detail LIKE ?`, platformMachineTokenExpiredAudit, "%"+rotated+"%") != 0 {
		t.Fatal("a rotated-away token was audited as expired")
	}
}
