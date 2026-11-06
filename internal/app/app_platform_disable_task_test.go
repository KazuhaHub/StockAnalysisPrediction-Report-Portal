package app

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func testPlatformDisableInvalidatesActiveTask(
	t *testing.T, postgres bool,
) {
	s, admin := platformRemovalFixture(t, postgres, false)
	installation := platformFixtureID(1)
	appID := scalar[string](t, s.st, `SELECT app_id
		FROM app_platform_installations WHERE id=?`, installation)
	publisherID := scalar[string](t, s.st, `SELECT publisher_id
		FROM app_platform_installations WHERE id=?`, installation)
	task := appPlatformTaskRow{
		ID:           platformFixtureID(6081),
		Installation: installation,
		App:          appID,
		Publisher:    publisherID,
		Occurrence:   "disable-active-handler",
		Handler:      appplatform.TaskHandlerBackend,
	}
	insertAppPlatformTask(t, s.st.poolExec(), task)
	insertAppPlatformTaskAttempt(
		t, s.st.poolExec(), appPlatformTaskAttemptFixture(task.ID))
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_tasks
		SET state='running',attempts=1,worker_id=?,lease_id=?,
		    lease_until_ms=?,revision=2,updated_ms=?
		WHERE id=?`,
		platformFixtureID(6082), platformFixtureID(6083),
		now+60000, now, task.ID)

	plan, err := s.createPlatformRemovalReview(
		admin, installation, appplatform.ActionDisable)
	if err != nil {
		t.Fatal("disable review", err)
	}
	if _, err := s.consumePlatformRemovalReview(
		admin, plan.ID, plan.SHA256, platformFixtureID(6084)); err != nil {
		t.Fatal("disable admission", err)
	}
	if got := scalar[string](t, s.st, `SELECT state||':'||error_code
		FROM app_platform_tasks WHERE id=?`, task.ID); got !=
		"uncertain:installation_disabled" {
		t.Fatalf("disable active task = %q", got)
	}
	if got := scalar[string](t, s.st, `SELECT state||':'||error_code
		FROM app_platform_task_attempts WHERE task_id=?`, task.ID); got !=
		"uncertain:installation_disabled" {
		t.Fatalf("disable active attempt = %q", got)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks
		WHERE id=? AND worker_id IS NULL AND lease_id IS NULL
		AND lease_until_ms IS NULL AND result_json IS NULL`, task.ID) != 1 {
		t.Fatal("disabled active task retained a live lease or result")
	}
	claimed, err := s.claimNextPlatformTask(
		context.Background(), platformFixtureID(6085))
	if err != nil || claimed.ID != "" {
		t.Fatalf("disabled installation offered a new claim: %+v %v",
			claimed, err)
	}
	if got := scalar[string](t, s.st, `SELECT desired_state||':'||phase
		FROM app_platform_installations WHERE id=?`,
		installation); got != "disabled:disabled" {
		t.Fatalf("disabled installation = %q", got)
	}
}

func TestPlatformDisableInvalidatesActiveTaskSQLite(t *testing.T) {
	testPlatformDisableInvalidatesActiveTask(t, false)
}

func TestPlatformDisableInvalidatesActiveTaskPostgres(t *testing.T) {
	testPlatformDisableInvalidatesActiveTask(t, true)
}
