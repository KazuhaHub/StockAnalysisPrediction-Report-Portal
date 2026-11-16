package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A submission refused for the installation's task quota is the common answer of a flooding backend.
// After one full verification the Portal remembers that credential for a moment and answers the
// repeats from one live statement that re-reads the installation's authority and its active task count,
// so the quota stays exactly the database's and the cache can only ever refuse, never admit.

type taskRefusalFixture struct {
	s            *Server
	spy          *spyDriver
	installation string
	identity     []byte
	now          int64
}

func newTaskRefusalFixture(t *testing.T, atQuota bool) *taskRefusalFixture {
	t.Helper()
	st, spy := newSpyTestStore(t)
	s, installation := platformServiceIdentityFixtureWithStore(t, st)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	f := &taskRefusalFixture{s: s, spy: spy, installation: installation, now: now}
	if atQuota {
		current := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, installation)
		for i := 0; i < appplatform.MaxInstallationActiveTasks-current; i++ {
			if _, created, err := s.admitPlatformTask(context.Background(), installation, f.task(fmt.Sprintf("fill:%d", i))); err != nil || !created {
				t.Fatalf("fill %d: %t %v", i, created, err)
			}
		}
	}
	f.identity, _, err = s.issuePlatformServiceIdentity(context.Background(), installation,
		platformFixtureID(6501), []string{"tasks.schedule"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *taskRefusalFixture) task(occurrence string) platformTaskAdmission {
	return platformTaskAdmission{OccurrenceID: occurrence, Handler: appplatform.TaskHandlerBackend,
		Payload: []byte(`{"v":1}`), RunAtMS: f.now + int64(time.Hour/time.Millisecond), ExpiresMS: f.now + int64(2*time.Hour/time.Millisecond)}
}

func (f *taskRefusalFixture) submit(t *testing.T, occurrence string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"occurrenceId":%q,"handler":"app.backend.task","payload":{"v":1}}`, occurrence)
	w := platformTaskHTTPWithIdentity(t, f.s, f.installation, body, f.identity)
	return w.Code, w.Body.String()
}

// statements runs f with tracing on and returns the statements issued.
func (f *taskRefusalFixture) statements(f2 func()) []string {
	f.spy.mu.Lock()
	f.spy.tracing, f.spy.trace = true, nil
	f.spy.mu.Unlock()
	f2()
	f.spy.mu.Lock()
	defer f.spy.mu.Unlock()
	f.spy.tracing = false
	var out []string
	for _, line := range f.spy.trace {
		if !strings.HasPrefix(line, "--") {
			out = append(out, line)
		}
	}
	return out
}

func TestPlatformTaskQuotaRefusalIsAnsweredFromOneStatementAfterTheFirst(t *testing.T) {
	f := newTaskRefusalFixture(t, true)
	if code, body := f.submit(t, "flood:0"); code != 429 || !strings.Contains(body, "task_capacity") {
		t.Fatalf("first refusal: %d %s", code, body)
	}
	var code int
	var body string
	stmts := f.statements(func() { code, body = f.submit(t, "flood:1") })
	if code != 429 || !strings.Contains(body, "task_capacity") {
		t.Fatalf("repeat refusal: %d %s", code, body)
	}
	if len(stmts) > 3 {
		t.Fatalf("a remembered refusal issued %d statements: %v", len(stmts), stmts)
	}
	for _, stmt := range stmts {
		if strings.Contains(strings.ToUpper(stmt), "BEGIN") || strings.Contains(stmt, "INSERT") || strings.Contains(stmt, "UPDATE") {
			t.Fatalf("a remembered refusal opened a transaction or wrote: %v", stmts)
		}
	}
	if got := scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=?`, f.installation); got != appplatform.MaxInstallationActiveTasks {
		t.Fatalf("refusals changed the task count: %d", got)
	}
}

// The quota is the database's: a slot freed by completion, cancellation or expiry is used by the very
// next submission, remembered refusal or not, and the quota is never exceeded afterwards.
func TestPlatformTaskRememberedRefusalNeverAdmitsBeyondQuotaAndUsesFreedSlotsAtOnce(t *testing.T) {
	for _, terminal := range []string{"cancelled", "succeeded", "failed"} {
		t.Run(terminal, func(t *testing.T) {
			f := newTaskRefusalFixture(t, true)
			f.submit(t, "flood:0")
			if code, _ := f.submit(t, "flood:1"); code != 429 {
				t.Fatalf("not refused: %d", code)
			}
			finishOneTask(t, f.s.st, f.installation, terminal)
			if code, body := f.submit(t, "after-free"); code != 200 {
				t.Fatalf("the freed slot was not used: %d %s", code, body)
			}
			if code, _ := f.submit(t, "after-free-2"); code != 429 {
				t.Fatalf("admitted beyond the quota: %d", code)
			}
			if got := scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, f.installation); got != appplatform.MaxInstallationActiveTasks {
				t.Fatalf("active tasks = %d", got)
			}
		})
	}
}

// An occurrence that already exists replays as 200 even at the quota, and a changed body under it is
// still a 409: the remembered refusal does not shadow either.
func TestPlatformTaskRememberedRefusalKeepsOccurrenceReplayAndConflict(t *testing.T) {
	f := newTaskRefusalFixture(t, true)
	f.submit(t, "flood:0")
	w := platformTaskHTTPWithIdentity(t, f.s, f.installation,
		`{"occurrenceId":"fill:3","handler":"app.backend.task","payload":{"v":1}}`, f.identity)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"created":false`) {
		t.Fatalf("replay at quota: %d %s", w.Code, w.Body.String())
	}
	w = platformTaskHTTPWithIdentity(t, f.s, f.installation,
		`{"occurrenceId":"fill:3","handler":"app.backend.task","payload":{"fill":999}}`, f.identity)
	if w.Code != 409 {
		t.Fatalf("changed occurrence at quota: %d %s", w.Code, w.Body.String())
	}
}

// Authority is re-read on every remembered refusal, so a change that retires the credential is seen at
// once: disabling or removing the installation, a new policy revision, a paused restore.
func TestPlatformTaskRememberedRefusalFollowsAuthorityChangesAtOnce(t *testing.T) {
	cases := map[string]string{
		"disabled":         `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`,
		"phase left ready": `UPDATE app_platform_installations SET phase='quarantined' WHERE id=?`,
		"policy revision":  `UPDATE app_platform_installations SET policy_revision=policy_revision+1 WHERE id=?`,
		"generation":       `UPDATE app_platform_installations SET generation=generation+1 WHERE id=?`,
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTaskRefusalFixture(t, true)
			f.submit(t, "flood:0")
			if code, _ := f.submit(t, "flood:1"); code != 429 {
				t.Fatalf("not remembered: %d", code)
			}
			mustExec(t, f.s.st, change, f.installation)
			if code, body := f.submit(t, "flood:2"); code == 429 || code == 200 {
				t.Fatalf("a retired credential still got %d %s", code, body)
			}
		})
	}
	t.Run("restore paused", func(t *testing.T) {
		f := newTaskRefusalFixture(t, true)
		f.submit(t, "flood:0")
		mustExec(t, f.s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
		if code, _ := f.submit(t, "flood:1"); code == 429 || code == 200 {
			t.Fatalf("a paused platform still got %d", code)
		}
	})
}

// The memory is process-local and never persisted: a restarted Portal verifies in full again, and a
// different credential, installation or resource never reuses another's entry.
func TestPlatformTaskRememberedRefusalIsPerCredentialAndLostOnRestart(t *testing.T) {
	f := newTaskRefusalFixture(t, true)
	f.submit(t, "flood:0")
	// A restart is a zero-value cache: nothing is persisted or rebuilt.
	f.s.platformTaskRefusals.reset()
	restarted := f.s
	full := f.statements(func() {
		w := platformTaskHTTPWithIdentity(t, restarted, f.installation,
			`{"occurrenceId":"flood:9","handler":"app.backend.task","payload":{"v":1}}`, f.identity)
		if w.Code != 429 {
			t.Fatalf("restarted refusal: %d %s", w.Code, w.Body.String())
		}
	})
	if len(full) < 8 {
		t.Fatalf("a restarted Portal answered from %d statements; it must verify in full: %v", len(full), full)
	}
	other, _, err := f.s.issuePlatformServiceIdentity(context.Background(), f.installation,
		platformFixtureID(6502), []string{"tasks.schedule"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cold := f.statements(func() {
		w := platformTaskHTTPWithIdentity(t, f.s, f.installation,
			`{"occurrenceId":"flood:10","handler":"app.backend.task","payload":{"v":1}}`, other)
		if w.Code != 429 {
			t.Fatalf("second credential: %d", w.Code)
		}
	})
	if len(cold) < 8 {
		t.Fatalf("an unseen credential was answered from the memory: %v", cold)
	}
}

func TestPlatformTaskRefusalMemoryExpiresAfterItsTimeLimitAndIsBounded(t *testing.T) {
	var c platformTaskRefusalCache
	now := time.Unix(1_000, 0)
	c.now = func() time.Time { return now }
	grant := appplatform.ServiceIdentityGrant{InstallationID: "i", Generation: "1", PolicyRevision: "1", ControllerEpoch: "e",
		ExpiresMS: fmt.Sprint(now.Add(time.Hour).UnixMilli())}
	key := platformTaskRefusalKey([]byte("credential"))
	c.remember(key, grant)
	if e, ok := c.lookup(key, "i"); !ok || e.expiresMS != now.Add(time.Hour).UnixMilli() {
		t.Fatalf("entry missing or without the credential's expiry: %+v %t", e, ok)
	}
	now = now.Add(platformTaskRefusalTTL + time.Millisecond)
	if _, ok := c.lookup(key, "i"); ok {
		t.Fatal("entry outlived its own time limit")
	}
	if _, ok := c.lookup(platformTaskRefusalKey([]byte("other")), "i"); ok {
		t.Fatal("a different credential found an entry")
	}
	c.remember(key, grant)
	if _, ok := c.lookup(key, "another-installation"); ok {
		t.Fatal("an entry served another installation")
	}
	bad := grant
	bad.ExpiresMS = "not-a-number"
	c.remember(platformTaskRefusalKey([]byte("bad")), bad)
	if _, ok := c.lookup(platformTaskRefusalKey([]byte("bad")), "i"); ok {
		t.Fatal("an entry was kept for an unreadable expiry")
	}
	for i := 0; i < platformTaskRefusalMax*3; i++ {
		c.remember(platformTaskRefusalKey([]byte(fmt.Sprint("c", i))), grant)
	}
	if n := c.size(); n > platformTaskRefusalMax {
		t.Fatalf("cache grew to %d", n)
	}
}

// Many backends submitting while tasks complete and are cancelled never push the installation past its
// quota; run under -race.
func TestPlatformTaskRememberedRefusalNeverExceedsQuotaUnderConcurrentCompletion(t *testing.T) {
	st := newTestStore(t)
	s, installation := platformServiceIdentityFixtureWithStore(t, st)
	f := &taskRefusalFixture{s: s, installation: installation}
	f.now, _ = platformDatabaseNow(s.st.poolExec())
	for i := 0; i < appplatform.MaxInstallationActiveTasks; i++ {
		if _, _, err := s.admitPlatformTask(context.Background(), installation, f.task(fmt.Sprintf("fill:%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	f.identity, _, err = s.issuePlatformServiceIdentity(context.Background(), installation, platformFixtureID(6503), []string{"tasks.schedule"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	var admitted atomic.Int64
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				code, _ := f.submit(t, fmt.Sprintf("race:%d:%d", w, i))
				if code == http.StatusOK {
					admitted.Add(1)
				}
			}
		}(w)
	}
	for i := 0; i < 40; i++ {
		finishOneTask(t, s.st, installation, "cancelled")
		if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, installation); got > appplatform.MaxInstallationActiveTasks {
			t.Errorf("active tasks %d exceeded the quota", got)
		}
		time.Sleep(2 * time.Millisecond)
	}
	stop.Store(true)
	wg.Wait()
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, installation); got > appplatform.MaxInstallationActiveTasks {
		t.Fatalf("active tasks %d exceeded the quota", got)
	}
	if admitted.Load() == 0 || admitted.Load() > 40 {
		t.Fatalf("admitted %d tasks for 40 freed slots", admitted.Load())
	}
}

// finishOneTask moves one waiting task of the installation to a terminal state the way the worker and
// the administrator do, freeing a slot of the quota.
func finishOneTask(t *testing.T, st *Store, installation, state string) {
	t.Helper()
	set := map[string]string{
		"cancelled": `state='cancelled',error_code='admin_cancelled',completed_ms=created_ms`,
		"succeeded": `state='succeeded',completed_ms=created_ms,result_json='{"ok":true}'`,
		"failed":    `state='failed',error_code='fixture_failed',completed_ms=created_ms`,
	}[state]
	mustExec(t, st, `UPDATE app_platform_tasks SET `+set+` WHERE id=(SELECT id FROM app_platform_tasks WHERE installation_id=? AND state='waiting' LIMIT 1)`, installation)
}
