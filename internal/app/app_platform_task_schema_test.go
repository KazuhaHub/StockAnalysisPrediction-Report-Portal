package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestAppPlatformTaskSchemaProducts(t *testing.T) {
	st := newTestStore(t)
	step := migration0019()
	if got, want := strings.Join(step.tables, ","), "app_platform_tasks,app_platform_task_attempts"; got != want {
		t.Fatalf("tables = %s, want %s", got, want)
	}
	if len(step.indices) != 7 {
		t.Fatalf("indexes = %d, want 7", len(step.indices))
	}
	if !step.restore.olderDumpOK || step.restore.upgradeOnRestore {
		t.Fatal("a table-only task migration must accept an older dump and create empty tables")
	}
	if !recorded(st, "0019") {
		t.Fatal("task migration not recorded")
	}
	if err := st.verifyMigrationProducts(st.poolExec(), st.migrations()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAppPlatformTaskSchemaProducts(t *testing.T) {
	st := platformTestStore(t, true)
	if err := migration0019().verify(st.poolExec()); err != nil {
		t.Fatal(err)
	}
}

func TestAppPlatformTaskSnapshotsDoNotReferencePublisherOrReleaseRows(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			st := platformTestStore(t, postgres)
			e := st.poolExec()
			platformSeedPublisher(t, e, "task-publisher")
			installation := platformFixtureID(1200)
			if err := platformSeedInstallation(t, e, installation, "task-publisher", "task-app"); err != nil {
				t.Fatal(err)
			}
			task := appPlatformTaskFixture(installation)
			insertAppPlatformTask(t, e, task)
			if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_tasks WHERE publisher_id='removed-publisher' AND release_sha256=?`, strings.Repeat("f", 64)) != 1 {
				t.Fatal("task did not retain immutable publisher/release snapshots")
			}
		})
	}
}

func TestAppPlatformTaskOlderDumpRestoresWithEmptyTables(t *testing.T) {
	src := newTestStore(t)
	tables := append(migration0019().tables, migration0020().tables...)
	tables = append(tables, migration0021().tables...)
	dump := withoutSections(t, dumpOf(t, src), tables, []string{"0019", "0020", "0021"})
	if bytes.Contains(dump, []byte(`"0019"`)) || bytes.Contains(dump, []byte(`"0020"`)) || bytes.Contains(dump, []byte("app_platform_tasks")) {
		t.Fatal("fixture still describes 0019 or 0020")
	}
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatal(err)
	}
	if !recorded(dst, "0019") {
		t.Fatal("older dump did not acquire the task migration")
	}
	if !recorded(dst, "0020") {
		t.Fatal("older dump did not acquire the event subscription migration")
	}
	if err := dst.verifyMigrationProducts(dst.poolExec(), dst.migrations()); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, dst, `SELECT COUNT(*) FROM app_platform_tasks`)+scalar[int](t, dst, `SELECT COUNT(*) FROM app_platform_task_attempts`) != 0 {
		t.Fatal("older dump restored task rows")
	}
}

func TestAppPlatformTaskBackupRoundTrip(t *testing.T) {
	src := newTestStore(t)
	e := src.poolExec()
	platformSeedPublisher(t, e, "task-publisher")
	installation := platformFixtureID(1201)
	if err := platformSeedInstallation(t, e, installation, "task-publisher", "task-app"); err != nil {
		t.Fatal(err)
	}
	task := appPlatformTaskFixture(installation)
	insertAppPlatformTask(t, e, task)
	attempt := appPlatformTaskAttemptFixture(task.ID)
	insertAppPlatformTaskAttempt(t, e, attempt)

	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dumpOf(t, src)), true); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, dst, `SELECT handler FROM app_platform_tasks WHERE id=?`, task.ID); got != task.Handler {
		t.Fatalf("handler = %q", got)
	}
	if got := scalar[string](t, dst, `SELECT execution_sha256 FROM app_platform_task_attempts WHERE task_id=? AND attempt=1`, task.ID); got != attempt.ExecutionSHA {
		t.Fatalf("attempt digest = %q", got)
	}
}

func TestAppPlatformTaskRestoreRequiresReviewAndStopsInFlightAttempts(t *testing.T) {
	src := newTestStore(t)
	e := src.poolExec()
	platformSeedPublisher(t, e, "task-publisher")
	installation := platformFixtureID(1202)
	if err := platformSeedInstallation(t, e, installation, "task-publisher", "task-app"); err != nil {
		t.Fatal(err)
	}
	task := appPlatformTaskFixture(installation)
	insertAppPlatformTask(t, e, task)
	attempt := appPlatformTaskAttemptFixture(task.ID)
	insertAppPlatformTaskAttempt(t, e, attempt)
	if _, err := e.exec(`UPDATE app_platform_tasks
		SET state='running',attempts=1,fence=1,revision=3,worker_id=?,lease_id=?,lease_until_ms=40002
		WHERE id=?`, platformFixtureID(1212), platformFixtureID(1213), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.exec(`UPDATE app_platform_task_attempts SET state='running' WHERE task_id=?`, task.ID); err != nil {
		t.Fatal(err)
	}

	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dumpOf(t, src)), true); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, dst, `SELECT state FROM app_platform_tasks WHERE id=?`, task.ID); got != "review_required" {
		t.Fatalf("restored task state = %q", got)
	}
	if scalar[int](t, dst, `SELECT COUNT(*) FROM app_platform_tasks WHERE id=? AND worker_id IS NULL AND lease_id IS NULL AND lease_until_ms IS NULL`, task.ID) != 1 {
		t.Fatal("restored task retained an executable lease")
	}
	if got := scalar[string](t, dst, `SELECT state FROM app_platform_task_attempts WHERE task_id=? AND attempt=1`, task.ID); got != "uncertain" {
		t.Fatalf("restored attempt state = %q", got)
	}
	if scalar[int](t, dst, `SELECT COUNT(*) FROM app_platform_task_attempts WHERE task_id=? AND finished_ms IS NOT NULL AND error_code='restored_attempt'`, task.ID) != 1 {
		t.Fatal("restored attempt was not closed as uncertain")
	}
}

type appPlatformTaskRow struct {
	ID, Installation, App, Publisher, Occurrence, Handler string
}

func appPlatformTaskFixture(installation string) appPlatformTaskRow {
	return appPlatformTaskRow{
		ID:           platformFixtureID(1210),
		Installation: installation,
		App:          "task-app",
		Publisher:    "removed-publisher",
		Occurrence:   "occurrence-1",
		Handler:      "app.backend.task",
	}
}

func insertAppPlatformTask(t *testing.T, e migExec, task appPlatformTaskRow) {
	t.Helper()
	_, err := e.exec(`INSERT INTO app_platform_tasks(
		id,installation_id,app_id,publisher_id,occurrence_id,handler,payload,payload_sha256,state,
		generation,policy_revision,release_sha256,controller_epoch,attempts,max_attempts,fence,revision,
		worker_id,lease_id,lease_until_ms,next_attempt_ms,expires_ms,error_code,result_json,completed_ms,
		created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,'waiting',1,1,?,?,0,3,1,1,NULL,NULL,NULL,1,100000,NULL,NULL,NULL,1,1)`,
		task.ID, task.Installation, task.App, task.Publisher, task.Occurrence, task.Handler,
		[]byte(`{"scheduleId":"daily"}`), strings.Repeat("a", 64), strings.Repeat("f", 64),
		platformFixtureID(1211))
	if err != nil {
		t.Fatal(err)
	}
}

type appPlatformTaskAttemptRow struct {
	TaskID, ExecutionSHA string
}

func appPlatformTaskAttemptFixture(taskID string) appPlatformTaskAttemptRow {
	return appPlatformTaskAttemptRow{TaskID: taskID, ExecutionSHA: strings.Repeat("b", 64)}
}

func insertAppPlatformTaskAttempt(t *testing.T, e migExec, attempt appPlatformTaskAttemptRow) {
	t.Helper()
	_, err := e.exec(`INSERT INTO app_platform_task_attempts(
		task_id,attempt,state,worker_id,lease_id,fence,execution_sha256,started_ms,lease_until_ms,
		finished_ms,error_code,result_sha256,evidence_json,created_ms,updated_ms)
		VALUES(?,1,'leased',?,?,1,?,2,40002,NULL,NULL,NULL,NULL,2,2)`,
		attempt.TaskID, platformFixtureID(1212), platformFixtureID(1213), attempt.ExecutionSHA)
	if err != nil {
		t.Fatal(err)
	}
}
