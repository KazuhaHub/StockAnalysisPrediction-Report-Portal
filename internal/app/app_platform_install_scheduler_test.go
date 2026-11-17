package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func platformSchedulerFixture(t *testing.T, s *Server) *platformInstallScheduler {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "install-worker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	endpoint := platformRuntimeEndpoint{path: filepath.Join(dir, "absent.sock"), controllerUID: 41001, controllerGID: 41001}
	w, err := newPlatformInstallScheduler(s, endpoint, platformFixtureID(930), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func testPlatformInstallSchedulerRetries(t *testing.T, pg bool) {
	s, _, id := platformInstallQueueFixture(t, pg)
	w := platformSchedulerFixture(t, s)
	for attempt := 0; attempt < platformInstallAttemptLimit; attempt++ {
		err := w.tick(context.Background())
		if attempt < platformInstallAttemptLimit-1 && err == nil {
			t.Fatal("missing endpoint succeeded")
		}
		if attempt < platformInstallAttemptLimit-1 && w.currentStatus().code != "retry_pending" {
			t.Fatal("worker did not retain retry state")
		}
	}
	if w.active.id != "" || w.currentStatus().code != "review_required" || w.currentStatus().operationID != id {
		t.Fatal("retry ceiling did not settle review state", w.currentStatus())
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces WHERE state='review_required' AND writer_fence=0`) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE state='prepared' AND receipt_json='{}'`) != 1 {
		t.Fatal("retries replaced or cleaned uncertain data")
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "recovery_required" || scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations`) != "install_execution_review_required" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE lease_owner IS NULL AND lease_until_ms IS NULL AND completed_ms IS NOT NULL`) != 1 {
		t.Fatal("review did not revoke worker ownership")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations`) != 0 {
		t.Fatal("failed worker exposed application")
	}
	before := dumpOf(t, s.st)
	if err := w.tick(context.Background()); err != nil || w.currentStatus().code != "idle" {
		t.Fatal("reviewed job re-entered queue", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("idle worker changed state")
	}
}
func TestPlatformInstallSchedulerRetriesSQLite(t *testing.T) {
	testPlatformInstallSchedulerRetries(t, false)
}
func TestPlatformInstallSchedulerRetriesPostgres(t *testing.T) {
	testPlatformInstallSchedulerRetries(t, true)
}
func TestPlatformInstallSchedulerCanceledSQLite(t *testing.T) {
	s, _, _ := platformInstallQueueFixture(t, false)
	w := platformSchedulerFixture(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := dumpOf(t, s.st)
	if err := w.tick(ctx); err == nil {
		t.Fatal("canceled tick processed work")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("canceled tick changed database")
	}
}

func TestPlatformInstallHeartbeatLateRefusalPostgres(t *testing.T) {
	for _, kind := range []string{"expiry", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			s, _, id := platformInstallQueueFixture(t, true)
			selected, err := s.claimNextPlatformStaticInstall(context.Background(), platformFixtureID(934))
			if err != nil || selected.lease.id != id {
				t.Fatal(err)
			}
			lease := selected.lease
			delay := "0.5"
			if kind == "expiry" {
				now, err := platformDatabaseNow(s.st.poolExec())
				if err != nil {
					t.Fatal(err)
				}
				lease.leaseUntilMS = now + 1000
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=?`, lease.leaseUntilMS)
				delay = "1.2"
			}
			mustExec(t, s.st, `CREATE FUNCTION fixture_slow_install_heartbeat() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(`+delay+`);RETURN NEW;END $$`)
			mustExec(t, s.st, `CREATE TRIGGER fixture_slow_install_heartbeat BEFORE UPDATE ON app_platform_operations FOR EACH ROW WHEN (NEW.row_revision>OLD.row_revision) EXECUTE FUNCTION fixture_slow_install_heartbeat()`)
			t.Cleanup(func() {
				mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_slow_install_heartbeat ON app_platform_operations`)
				mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_slow_install_heartbeat()`)
			})
			ctx := context.Background()
			cancel := func() {}
			if kind == "cancellation" {
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
			}
			before := dumpOf(t, s.st)
			_, err = s.st.updatePlatformOperationLeaseContext(ctx, lease.id, lease.owner, &lease)
			cancel()
			if err == nil {
				t.Fatal("late heartbeat extended expired/canceled authority")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("late heartbeat partly committed")
			}
		})
	}
}

func TestPlatformInstallSchedulerServiceGuardsSQLite(t *testing.T) {
	s, _, _ := platformInstallQueueFixture(t, false)
	w := platformSchedulerFixture(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := dumpOf(t, s.st)
	err := w.serve(ctx)
	if err != nil && !errors.Is(err, errAppPlatformRuntimeConflict) {
		t.Fatal("unexpected service refusal", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("unsupported/canceled service claimed work")
	}
	if w.started.Load() {
		if err := w.serve(ctx); !errors.Is(err, errAppPlatformRuntimeConflict) {
			t.Fatal("service instance started twice", err)
		}
	}
}
