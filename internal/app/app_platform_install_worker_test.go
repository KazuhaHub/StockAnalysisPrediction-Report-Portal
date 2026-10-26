package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func testPlatformStaticInstallRunProgress(t *testing.T, pg bool) {
	s, actor, lease := platformInstallStageFixture(t, pg)
	runtime, err := s.preparePlatformRuntimeBinding(actor, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.confirmPlatformObservedRuntime(actor, runtime, runtimeObservationFixture(runtime)); err != nil {
		t.Fatal(err)
	}
	endpoint := platformRuntimeEndpoint{path: filepath.Join(t.TempDir(), "absent.sock"), controllerUID: 41001, controllerGID: 41001}
	result, err := s.runPlatformStaticInstall(context.Background(), lease, endpoint)
	if err == nil || result.lease.snapshot.Stage != appplatform.StagePrepareData || result.lease.rowRevision <= lease.rowRevision {
		t.Fatal("worker did not retain exact last committed progress", result, err)
	}
	again, err := s.runPlatformStaticInstall(context.Background(), result.lease, endpoint)
	if err == nil || again.lease != result.lease {
		t.Fatal("uncertain endpoint replaced progress")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE state='prepared'`) != 1 || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations`) != "pending" {
		t.Fatal("worker fabricated completed effect")
	}
}

func TestPlatformStaticInstallRunProgressSQLite(t *testing.T) {
	testPlatformStaticInstallRunProgress(t, false)
}
func TestPlatformStaticInstallRunProgressPostgres(t *testing.T) {
	testPlatformStaticInstallRunProgress(t, true)
}
func testPlatformStaticInstallRunActivation(t *testing.T, pg bool) {
	s, actor, lease := platformActivationFixture(t, pg, true)
	result, err := s.runPlatformStaticInstall(context.Background(), lease, platformRuntimeEndpoint{})
	if err != nil || result.activation.snapshot.Stage != appplatform.StageComplete || result.lease.id != "" {
		t.Fatal("worker did not finish activation and discard lease", err)
	}
	if _, err := s.inspectPlatformLaunchAccess(actor, lease.installationID); err != nil {
		t.Fatal("worker result not launchable", err)
	}
}
func TestPlatformStaticInstallRunActivationSQLite(t *testing.T) {
	testPlatformStaticInstallRunActivation(t, false)
}
func TestPlatformStaticInstallRunActivationPostgres(t *testing.T) {
	testPlatformStaticInstallRunActivation(t, true)
}
func TestPlatformStaticInstallRunCanceledBeforeWorkSQLite(t *testing.T) {
	s, _, lease := platformInstallStageFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := dumpOf(t, s.st)
	if _, err := s.runPlatformStaticInstall(ctx, lease, platformRuntimeEndpoint{}); err == nil {
		t.Fatal("canceled worker advanced")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("canceled worker changed state")
	}
}

func TestPlatformStaticInstallRunCancellationDuringStagePostgres(t *testing.T) {
	s, _, lease := platformInstallStageFixture(t, true)
	mustExec(t, s.st, `CREATE FUNCTION fixture_slow_install_run() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.5);RETURN NEW;END $$`)
	mustExec(t, s.st, `CREATE TRIGGER fixture_slow_install_run BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.operation_stage') EXECUTE FUNCTION fixture_slow_install_run()`)
	t.Cleanup(func() {
		mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_slow_install_run ON audit_log`)
		mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_slow_install_run()`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err := s.runPlatformStaticInstall(ctx, lease, platformRuntimeEndpoint{})
	if !errors.Is(err, context.DeadlineExceeded) || result.lease != lease {
		t.Fatal("in-flight stage ignored cancellation", err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "queued" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.operation_stage' AND target_id='`+lease.id+`'`) != 0 {
		t.Fatal("canceled stage partly committed")
	}
}
