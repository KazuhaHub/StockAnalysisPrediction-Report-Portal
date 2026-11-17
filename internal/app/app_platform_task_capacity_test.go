package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type platformIsolationLatencies struct {
	queueMax  time.Duration
	queueP95  time.Duration
	authMax   time.Duration
	healthMax time.Duration
}

func latencyPercentile(values []time.Duration, percentile int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := (len(values)*percentile + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(values) {
		index = len(values)
	}
	return values[index-1]
}

func measurePlatformTaskIsolation(
	s *Server, installation string, iterations int,
) (platformIsolationLatencies, time.Duration, error) {
	var result platformIsolationLatencies
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		return result, 0, err
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	queueLatencies := make([]time.Duration, 0, iterations)
	var authLatencies []time.Duration
	var healthLatencies []time.Duration
	var floodDuration time.Duration
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		began := time.Now()
		for i := 0; i < iterations; i++ {
			_, created, err := s.admitPlatformTask(
				context.Background(), installation, platformTaskAdmission{
					OccurrenceID: fmt.Sprintf("pressure:%d", i),
					Handler:      appplatform.TaskHandlerBackend,
					Payload:      []byte(fmt.Sprintf(`{"pressure":%d}`, i)),
					RunAtMS:      now + int64(time.Hour/time.Millisecond),
					ExpiresMS:    now + int64(2*time.Hour/time.Millisecond),
				})
			if created || !errors.Is(err, errAppPlatformTaskQuota) {
				errs <- fmt.Errorf("pressure admission %d: created=%t err=%v", i, created, err)
				return
			}
		}
		floodDuration = time.Since(began)
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			began := time.Now()
			_ = s.queuedItems()
			queueLatencies = append(queueLatencies, time.Since(began))
			began = time.Now()
			if s.st.GetUser("platform-review-actor") == nil {
				errs <- fmt.Errorf("authentication account disappeared under pressure")
				return
			}
			authLatencies = append(authLatencies, time.Since(began))
			began = time.Now()
			recorder := httptest.NewRecorder()
			s.handleHealthz(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if recorder.Code != http.StatusOK {
				errs <- fmt.Errorf("health under pressure: %d %s", recorder.Code, recorder.Body.String())
				return
			}
			healthLatencies = append(healthLatencies, time.Since(began))
		}
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		return result, floodDuration, err
	}
	result.queueMax = queueLatencies[len(queueLatencies)-1]
	for _, value := range queueLatencies {
		if value > result.queueMax {
			result.queueMax = value
		}
	}
	result.queueP95 = latencyPercentile(queueLatencies, 95)
	for _, value := range authLatencies {
		if value > result.authMax {
			result.authMax = value
		}
	}
	for _, value := range healthLatencies {
		if value > result.healthMax {
			result.healthMax = value
		}
	}
	return result, floodDuration, nil
}

func testPlatformTaskCapacityIsolation(t *testing.T, postgres bool) {
	s, installation := platformServiceIdentityFixtureWithDatabase(t, postgres)
	target := seedTarget(t, s.st)
	jobID, err := s.st.CreateBatchJob(
		target, 1, 0, "platform-review-actor",
		[]map[string]string{{"symbol": "600000"}}, "50")
	if err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	subscriptionID, created, err := s.createPlatformEventSubscription(
		context.Background(), installation, appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report",
			ResourceID: "7301", MaxInflight: 1,
		})
	if err != nil || !created {
		t.Fatalf("event subscription: %s %t %v", subscriptionID, created, err)
	}
	for i := 0; i < 2; i++ {
		event := NotificationEvent{
			ID:        fmt.Sprintf("10000000-0000-4000-8000-%012d", 7300+i),
			SourceKey: fmt.Sprintf("capacity:%d", i),
			Type:      appplatform.EventReportIngested, Payload: `{"id":1}`,
			ResourceType: "report", ResourceID: "7301",
			CreatedAt: now,
		}
		if err := s.st.CaptureNotification(event, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := scalar[string](t, s.st, `SELECT state FROM app_platform_event_subscriptions WHERE id=?`, subscriptionID); got != "review_required" {
		t.Fatalf("event saturation state = %q", got)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events WHERE source_key LIKE 'capacity:%'`) != 2 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE handler=?`, appplatform.TaskHandlerEvent) != 1 {
		t.Fatal("event saturation lost a source event or admitted duplicate delivery")
	}
	current := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, installation)
	for i := 0; i < appplatform.MaxInstallationActiveTasks-current; i++ {
		if _, created, err := s.admitPlatformTask(
			context.Background(), installation, platformTaskAdmission{
				OccurrenceID: fmt.Sprintf("fill:%d", i),
				Handler:      appplatform.TaskHandlerBackend,
				Payload:      []byte(fmt.Sprintf(`{"fill":%d}`, i)),
				RunAtMS:      now + int64(time.Hour/time.Millisecond),
				ExpiresMS:    now + int64(2*time.Hour/time.Millisecond),
			}); err != nil || !created {
			t.Fatalf("fill admission %d: created=%t err=%v", i, created, err)
		}
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=?`, installation); got != appplatform.MaxInstallationActiveTasks {
		t.Fatalf("installation active tasks = %d", got)
	}
	// Warm the process-local health cache, then constrain analysis-queue and health
	// reads while repeated task admissions are refused at the installation ceiling.
	if code, _ := healthGET(t, s); code != http.StatusOK {
		t.Fatal("health warmup failed")
	}
	metrics, flood, err := measurePlatformTaskIsolation(s, installation, 64)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.queueMax > 5*time.Second || metrics.queueP95 > time.Second ||
		metrics.authMax > time.Second || metrics.healthMax > 500*time.Millisecond {
		t.Fatalf("task quota pressure starved portal paths: %+v", metrics)
	}
	jobs := s.queuedItems()
	if len(jobs) != 1 || jobs[0].ID != jobID {
		t.Fatalf("analysis queue changed under task pressure: %+v", jobs)
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks`); got != appplatform.MaxInstallationActiveTasks {
		t.Fatalf("quota refusals inserted tasks: %d", got)
	}
	t.Logf("task isolation postgres=%t: flood=%s queue_max=%s queue_p95=%s auth_max=%s health_max=%s",
		postgres, flood, metrics.queueMax, metrics.queueP95, metrics.authMax, metrics.healthMax)
}

func TestPlatformTaskCapacityIsolationSQLite(t *testing.T) {
	testPlatformTaskCapacityIsolation(t, false)
}

func TestPlatformTaskCapacityIsolationPostgres(t *testing.T) {
	testPlatformTaskCapacityIsolation(t, true)
}
