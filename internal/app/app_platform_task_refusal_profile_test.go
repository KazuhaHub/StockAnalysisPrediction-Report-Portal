package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The refused-submission profile: what one 429 task_capacity costs a Portal whose installation is
// already at its quota, split into the identity verification and the admission transaction, and the
// admission transaction split into its lock and its counts. It logs; run it with -v.

func platformTaskAtQuotaFixture(t *testing.T, postgres bool) (*Server, string, []byte) {
	t.Helper()
	s, installation := platformServiceIdentityFixtureWithDatabase(t, postgres)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	current := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, installation)
	for i := 0; i < appplatform.MaxInstallationActiveTasks-current; i++ {
		if _, _, err := s.admitPlatformTask(context.Background(), installation, platformTaskAdmission{
			OccurrenceID: fmt.Sprintf("fill:%d", i), Handler: appplatform.TaskHandlerBackend,
			Payload: []byte(fmt.Sprintf(`{"fill":%d}`, i)), RunAtMS: now + int64(time.Hour/time.Millisecond),
			ExpiresMS: now + int64(2*time.Hour/time.Millisecond),
		}); err != nil {
			t.Fatal(err)
		}
	}
	identity, _, err := s.issuePlatformServiceIdentity(context.Background(), installation,
		platformFixtureID(6401), []string{"tasks.schedule"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return s, installation, identity
}

func platformTaskMean(n int, f func()) time.Duration {
	started := time.Now()
	for i := 0; i < n; i++ {
		f()
	}
	return time.Since(started) / time.Duration(n)
}

func testPlatformTaskRefusalProfile(t *testing.T, postgres bool) {
	s, installation, identity := platformTaskAtQuotaFixture(t, postgres)
	const n = 300
	now, _ := platformDatabaseNow(s.st.poolExec())
	seq := 0
	admit := func() {
		seq++
		_, _, err := s.admitPlatformTask(context.Background(), installation, platformTaskAdmission{
			OccurrenceID: fmt.Sprintf("refused:%d", seq), Handler: appplatform.TaskHandlerBackend,
			Payload: []byte(`{"x":1}`), RunAtMS: now + int64(time.Hour/time.Millisecond),
			ExpiresMS: now + int64(2*time.Hour/time.Millisecond),
		})
		if err != errAppPlatformTaskQuota {
			t.Fatalf("admission was not refused for quota: %v", err)
		}
	}
	verify := platformTaskMean(n, func() {
		if _, err := s.verifyPlatformServiceIdentity(context.Background(), identity, "tasks.schedule"); err != nil {
			t.Fatal(err)
		}
	})
	admission := platformTaskMean(n, admit)
	lock := platformTaskMean(n, func() {
		tx, _ := s.st.db.BeginTx(context.Background(), nil)
		_ = lockPlatformTaskAdmission(migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: context.Background()}})
		tx.Rollback()
	})
	count := func(query string, args ...any) time.Duration {
		return platformTaskMean(n, func() {
			var c int64
			if err := s.st.poolExec().queryRow(query, args...).Scan(&c); err != nil {
				t.Fatal(err)
			}
		})
	}
	global := count(`SELECT COUNT(*) FROM app_platform_tasks WHERE state NOT IN ('succeeded','failed','cancelled')`)
	app := count(`SELECT COUNT(*) FROM app_platform_tasks WHERE app_id=? AND state NOT IN ('succeeded','failed','cancelled')`, scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, installation))
	inst := count(`SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, installation)
	handler := func(reset bool) time.Duration {
		return platformTaskMean(n, func() {
			if reset {
				s.platformTaskRefusals.reset()
			}
			seq++
			w := platformTaskHTTPWithIdentity(t, s, installation,
				fmt.Sprintf(`{"occurrenceId":"http:%d","handler":"app.backend.task","payload":{"x":1}}`, seq), identity)
			if w.Code != 429 {
				t.Fatalf("handler answered %d", w.Code)
			}
		})
	}
	full := handler(true)
	remembered := handler(false)
	t.Logf("handler refusal postgres=%t: full path (verify + admission)=%s remembered refusal=%s", postgres, full, remembered)
	t.Logf("task refusal profile postgres=%t over %d refusals at quota (256 active): verify_identity=%s admission_tx_total=%s (begin+lock+rollback=%s) count_global=%s count_application=%s count_installation=%s",
		postgres, n, verify, admission, lock, global, app, inst)
}

func TestPlatformTaskRefusalProfileSQLite(t *testing.T)   { testPlatformTaskRefusalProfile(t, false) }
func TestPlatformTaskRefusalProfilePostgres(t *testing.T) { testPlatformTaskRefusalProfile(t, true) }
