package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func admitTaskFixture(t *testing.T, s *Server, installation, occurrence, handler string, payload []byte) platformTaskAdmission {
	t.Helper()
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	in := platformTaskAdmission{
		OccurrenceID: occurrence, Handler: handler, Payload: payload,
		ExpiresMS: now + int64(time.Hour/time.Millisecond),
	}
	id, created, err := s.admitPlatformTask(context.Background(), installation, in)
	if err != nil || !created || !platformValidUUID(id) {
		t.Fatalf("admit task: id=%q created=%v err=%v", id, created, err)
	}
	return in
}

func TestPlatformTaskWorkerExecutesDigestAfterBrowserIndependentClaim(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			s, installation := platformServiceIdentityFixtureWithDatabase(t, postgres)
			admitTaskFixture(t, s, installation, "digest:2026-11-03",
				appplatform.TaskHandlerBackend, []byte(`{"scheduleId":"daily","count":2}`))
			s.platformTaskBackend = func(_ context.Context, task platformTaskLease, _ string) appplatform.TaskExecutionResult {
				result, err := platformTaskDigestResult(task)
				if err != nil {
					return appplatform.TaskExecutionResult{Outcome: appplatform.TaskResultFailed, ErrorCode: "result_unavailable"}
				}
				return appplatform.TaskExecutionResult{
					Outcome: appplatform.TaskResultSucceeded, ResultJSON: result,
					EvidenceSHA: platformDigest(result),
				}
			}
			scheduler, err := newPlatformTaskScheduler(s, platformFixtureID(6001), 50*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if worked, err := scheduler.tick(context.Background()); err != nil || !worked {
				t.Fatalf("worker tick: worked=%v err=%v", worked, err)
			}
			if got := scalar[string](t, s.st, `SELECT state FROM app_platform_tasks`); got != "succeeded" {
				t.Fatalf("task state = %q", got)
			}
			if got := scalar[string](t, s.st, `SELECT state FROM app_platform_task_attempts`); got != "succeeded" {
				t.Fatalf("attempt state = %q", got)
			}
			var raw string
			if err := s.st.queryRow(`SELECT result_json FROM app_platform_tasks`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var result struct {
				OccurrenceID string `json:"occurrenceId"`
				Digest       string `json:"digest"`
			}
			if json.Unmarshal([]byte(raw), &result) != nil || result.OccurrenceID != "digest:2026-11-03" || !platformValidSHA(result.Digest) {
				t.Fatalf("result = %s", raw)
			}
		})
	}
}

func TestPlatformTaskConditionalClaimHasOneWinner(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			s, installation := platformServiceIdentityFixtureWithDatabase(t, postgres)
			admitTaskFixture(t, s, installation, "claim-once",
				appplatform.TaskHandlerBackend, []byte(`{"fixture":true}`))
			workers := []string{platformFixtureID(6011), platformFixtureID(6012)}
			results := make([]platformTaskLease, len(workers))
			errs := make([]error, len(workers))
			var wg sync.WaitGroup
			for i := range workers {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					results[i], errs[i] = s.claimNextPlatformTask(context.Background(), workers[i])
				}(i)
			}
			wg.Wait()
			claimed := 0
			for i := range results {
				if errs[i] != nil {
					t.Fatalf("worker %d claim: %v", i, errs[i])
				}
				if results[i].ID != "" {
					claimed++
				}
			}
			if claimed != 1 {
				t.Fatalf("claim winners = %d", claimed)
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_task_attempts`) != 1 ||
				scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE attempts=1 AND fence=2 AND revision=2 AND state='leased'`) != 1 {
				t.Fatal("claim did not atomically update task and attempt")
			}
		})
	}
}

func TestPlatformTaskAuthorityRevocationWithholdsResult(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	admitTaskFixture(t, s, installation, "revoked-before-result",
		appplatform.TaskHandlerBackend, []byte(`{"fixture":true}`))
	claimed, err := s.claimNextPlatformTask(context.Background(), platformFixtureID(6021))
	if err != nil {
		t.Fatal(err)
	}
	started, identity, err := s.startPlatformTask(context.Background(), claimed)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='background.run'`, installation)
	if err := s.completePlatformTask(context.Background(), started, identity, appplatform.TaskExecutionResult{
		Outcome: appplatform.TaskResultSucceeded, ResultJSON: []byte(`{"private":"must-not-escape"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, s.st, `SELECT state||':'||error_code FROM app_platform_tasks`); got != "uncertain:authority_revoked" {
		t.Fatalf("task = %q", got)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE result_json IS NOT NULL`) != 0 ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_task_attempts`) != "uncertain" {
		t.Fatal("revoked task released a result or left its attempt open")
	}
}

func TestPlatformTaskRetryIsBoundedAndNotImmediate(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	admitTaskFixture(t, s, installation, "retry-once",
		appplatform.TaskHandlerBackend, []byte(`{"fixture":true}`))
	s.platformTaskBackend = func(context.Context, platformTaskLease, string) appplatform.TaskExecutionResult {
		return appplatform.TaskExecutionResult{Outcome: appplatform.TaskResultRetry, ErrorCode: "provider_unavailable"}
	}
	scheduler, err := newPlatformTaskScheduler(s, platformFixtureID(6031), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := scheduler.tick(context.Background()); err != nil || !worked {
		t.Fatalf("first tick: worked=%v err=%v", worked, err)
	}
	if got := scalar[string](t, s.st, `SELECT state||':'||error_code FROM app_platform_tasks`); got != "waiting:provider_unavailable" {
		t.Fatalf("task = %q", got)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE next_attempt_ms>updated_ms`) != 1 ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_task_attempts`) != "failed" {
		t.Fatal("retry was not scheduled with a closed failed attempt")
	}
	if worked, err := scheduler.tick(context.Background()); err != nil || worked {
		t.Fatalf("retry ran before its due time: worked=%v err=%v", worked, err)
	}
}

func TestPlatformTaskUncertaintyNeverReleasesResultOrRetries(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	admitTaskFixture(t, s, installation, "lost-confirmation",
		appplatform.TaskHandlerBackend, []byte(`{"fixture":true}`))
	s.platformTaskBackend = func(context.Context, platformTaskLease, string) appplatform.TaskExecutionResult {
		return appplatform.TaskExecutionResult{
			Outcome: appplatform.TaskResultUncertain, ErrorCode: "effect_unknown",
			ResultJSON: []byte(`{"private":"detail"}`),
		}
	}
	scheduler, err := newPlatformTaskScheduler(s, platformFixtureID(6041), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := scheduler.tick(context.Background()); err != nil || !worked {
		t.Fatalf("tick: worked=%v err=%v", worked, err)
	}
	if got := scalar[string](t, s.st, `SELECT state||':'||error_code FROM app_platform_tasks`); got != "uncertain:effect_unknown" {
		t.Fatalf("task = %q", got)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE result_json IS NOT NULL OR completed_ms IS NOT NULL`) != 0 {
		t.Fatal("uncertain task released a result or presented itself as complete")
	}
	if worked, err := scheduler.tick(context.Background()); err != nil || worked {
		t.Fatalf("uncertain task was retried: worked=%v err=%v", worked, err)
	}
}

func TestPlatformTaskAdmissionIsIdempotentAndImmutable(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	in := platformTaskAdmission{
		OccurrenceID: "same-occurrence", Handler: appplatform.TaskHandlerBackend,
		Payload: []byte(`{"b":2,"a":1}`), ExpiresMS: now + 60000,
	}
	id, created, err := s.admitPlatformTask(context.Background(), installation, in)
	if err != nil || !created {
		t.Fatal(err)
	}
	in.Payload = []byte(`{"a":1,"b":2}`)
	again, created, err := s.admitPlatformTask(context.Background(), installation, in)
	if err != nil || created || again != id {
		t.Fatalf("canonical replay: id=%q created=%v err=%v", again, created, err)
	}
	in.Payload = []byte(`{"a":2,"b":2}`)
	if _, _, err := s.admitPlatformTask(context.Background(), installation, in); !errors.Is(err, errAppPlatformTaskConflict) {
		t.Fatalf("changed occurrence bytes: %v", err)
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks`); got != 1 {
		t.Fatalf("task rows = %d", got)
	}
	if strings.Contains(scalar[string](t, s.st, `SELECT payload FROM app_platform_tasks`), " ") {
		t.Fatal("payload was not stored canonically")
	}
}

func TestPlatformTaskLifecycleInvalidationSeparatesUnexecutedAndInFlight(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	admitTaskFixture(t, s, installation, "lifecycle-running",
		appplatform.TaskHandlerBackend, []byte(`{"fixture":"running"}`))
	admitTaskFixture(t, s, installation, "lifecycle-waiting",
		appplatform.TaskHandlerBackend, []byte(`{"fixture":"waiting"}`))
	claimed, err := s.claimNextPlatformTask(context.Background(), platformFixtureID(6051))
	if err != nil || claimed.ID == "" {
		t.Fatal("claim", err)
	}
	if _, _, err := s.startPlatformTask(context.Background(), claimed); err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	if err := invalidatePlatformTasksForInstallation(
		s.st.poolExec(), installation, "installation_uninstalled", now); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE state='uncertain' AND error_code='installation_uninstalled'`) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE state='cancelled' AND completed_ms IS NOT NULL`) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_task_attempts WHERE state='uncertain' AND finished_ms IS NOT NULL`) != 1 {
		t.Fatal("uninstall did not invalidate executable and in-flight task state distinctly")
	}
}

func TestPlatformBackendTaskFailsClosedWithoutRunnerEndpoint(t *testing.T) {
	s, _ := platformServiceIdentityFixture(t)
	result := s.runPlatformBackendTask(
		context.Background(),
		platformTaskLease{Generation: 1, PolicyRevision: 1},
		"identity")
	if result.Outcome != appplatform.TaskResultFailed ||
		result.ErrorCode != "backend_transport_unavailable" {
		t.Fatalf("backend task result = %+v", result)
	}
}

func TestPlatformBackendTaskRechecksServiceIdentityBeforeNativeDispatch(t *testing.T) {
	s, _ := platformServiceIdentityFixture(t)
	endpoint := platformRuntimeEndpoint{
		path: "/tmp/not-reached.sock", controllerUID: 1000, controllerGID: 1001,
	}
	s.platformBackendEndpoint.Store(&endpoint)
	result := s.runPlatformBackendTask(
		context.Background(),
		platformTaskLease{Generation: 1, PolicyRevision: 1},
		"not-an-identity")
	if result.Outcome != appplatform.TaskResultUncertain ||
		result.ErrorCode != "task_authority_revoked" {
		t.Fatalf("revoked backend task result = %+v", result)
	}
}
