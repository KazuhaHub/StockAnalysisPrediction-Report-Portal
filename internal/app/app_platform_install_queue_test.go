package app

import (
	"bytes"
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformInstallQueueFixture(t *testing.T, pg bool) (*Server, platformActorSession, string) {
	t.Helper()
	s, actor, release := platformInstallFixture(t, pg)
	runtime, err := s.preparePlatformRuntimeBinding(actor, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.confirmPlatformObservedRuntime(actor, runtime, runtimeObservationFixture(runtime)); err != nil {
		t.Fatal(err)
	}
	review, err := s.createPlatformInstallAudienceReview(actor, release.sha256, 1, []platformAudienceTarget{{kind: "user", username: actor.username}})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := s.consumePlatformInstallReview(actor, review.ID, review.SHA256, platformFixtureID(920))
	if err != nil {
		t.Fatal(err)
	}
	return s, actor, admitted.OperationID
}
func testPlatformInstallQueueClaim(t *testing.T, pg bool) {
	s, _, id := platformInstallQueueFixture(t, pg)
	first, err := s.claimNextPlatformStaticInstall(context.Background(), platformFixtureID(921))
	if err != nil || first.lease.id != id || first.lease.snapshot.Stage != appplatform.StageQueued || first.lease.fence != 1 {
		t.Fatal("queue did not claim exact initial install", first, err)
	}
	before := dumpOf(t, s.st)
	second, err := s.claimNextPlatformStaticInstall(context.Background(), platformFixtureID(922))
	if err != nil || second.lease.id != "" || second.reviewed {
		t.Fatal("live queue lease stolen", second, err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("idle claim changed state")
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
	resumed, err := s.claimNextPlatformStaticInstall(context.Background(), platformFixtureID(922))
	if err != nil || resumed.lease.id != id || resumed.lease.fence != 2 {
		t.Fatal("side-effect-free expired install did not resume", err)
	}
}
func TestPlatformInstallQueueClaimSQLite(t *testing.T)   { testPlatformInstallQueueClaim(t, false) }
func TestPlatformInstallQueueClaimPostgres(t *testing.T) { testPlatformInstallQueueClaim(t, true) }
func testPlatformInstallQueueRecovery(t *testing.T, pg bool) {
	s, _, lease := platformDataIntentFixture(t, pg)
	intent, err := s.preparePlatformInstallData(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
	selected, err := s.claimNextPlatformStaticInstall(context.Background(), platformFixtureID(923))
	if err != nil || !selected.reviewed || selected.lease.id != "" {
		t.Fatal("expired intent was reclaimed for execution", selected, err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "recovery_required" || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations`) != "recovery_required" {
		t.Fatal("uncertain install not made explicit")
	}
	if scalar[int64](t, s.st, `SELECT control_fence FROM app_platform_installations`) != lease.fence+1 || scalar[string](t, s.st, `SELECT effect_id FROM app_platform_operation_effects`) != intent.effectID || scalar[string](t, s.st, `SELECT state FROM app_platform_data_namespaces`) != "review_required" {
		t.Fatal("review replaced native intent")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE lease_owner IS NULL AND lease_until_ms IS NULL AND completed_ms IS NOT NULL`) != 1 {
		t.Fatal("review did not settle worker authority")
	}
}
func TestPlatformInstallQueueRecoverySQLite(t *testing.T) { testPlatformInstallQueueRecovery(t, false) }
func TestPlatformInstallQueueRecoveryPostgres(t *testing.T) {
	testPlatformInstallQueueRecovery(t, true)
}

func testPlatformInstallQueueRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"pause", "runtime", "sponsor", "canceled", "audit"} {
		t.Run(kind, func(t *testing.T) {
			s, actor, _ := platformInstallQueueFixture(t, pg)
			ctx := context.Background()
			switch kind {
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "sponsor":
				mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "audit":
				if pg {
					mustExec(t, s.st, `CREATE FUNCTION fixture_queue_claim_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture claim failure';END $$`)
					mustExec(t, s.st, `CREATE TRIGGER fixture_queue_claim_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.install_claimed') EXECUTE FUNCTION fixture_queue_claim_failure()`)
					t.Cleanup(func() {
						mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_queue_claim_failure ON audit_log`)
						mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_queue_claim_failure()`)
					})
				} else {
					mustExec(t, s.st, `CREATE TRIGGER fixture_queue_claim_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.install_claimed' BEGIN SELECT RAISE(ABORT,'fixture claim failure');END`)
				}
			}
			before := dumpOf(t, s.st)
			selected, err := s.claimNextPlatformStaticInstall(ctx, platformFixtureID(931))
			if kind == "sponsor" {
				if err != nil || !selected.reviewed || scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "failed" || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations`) != "review_required" {
					t.Fatal("invalid sponsor executed or blocked queue silently", err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid queue claim succeeded", kind)
				}
				if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
					t.Fatal("refused claim partly committed", kind)
				}
			}
		})
	}
}
func TestPlatformInstallQueueRefusalsSQLite(t *testing.T) { testPlatformInstallQueueRefusals(t, false) }
func TestPlatformInstallQueueRefusalsPostgres(t *testing.T) {
	testPlatformInstallQueueRefusals(t, true)
}

func testPlatformInstallQueueConcurrent(t *testing.T, pg bool) {
	s, _, id := platformInstallQueueFixture(t, pg)
	type outcome struct {
		selection platformInstallSelection
		err       error
	}
	done := make(chan outcome, 2)
	for _, worker := range []string{platformFixtureID(932), platformFixtureID(933)} {
		go func() {
			selected, err := s.claimNextPlatformStaticInstall(context.Background(), worker)
			done <- outcome{selected, err}
		}()
	}
	claimed := 0
	for range 2 {
		out := <-done
		if out.err != nil {
			t.Fatal(out.err)
		}
		if out.selection.lease.id != "" {
			claimed++
			if out.selection.lease.id != id {
				t.Fatal("wrong operation selected")
			}
		}
	}
	if claimed != 1 || scalar[int64](t, s.st, `SELECT control_fence FROM app_platform_installations`) != 1 {
		t.Fatal("competing queue workers both claimed or advanced fence", claimed)
	}
}
func TestPlatformInstallQueueConcurrentSQLite(t *testing.T) {
	testPlatformInstallQueueConcurrent(t, false)
}
func TestPlatformInstallQueueConcurrentPostgres(t *testing.T) {
	testPlatformInstallQueueConcurrent(t, true)
}

func testPlatformInstallQueueRecoveryBoundaries(t *testing.T, pg bool) {
	for _, kind := range []string{"runtime_offline", "audit_failure"} {
		t.Run(kind, func(t *testing.T) {
			s, _, lease := platformDataIntentFixture(t, pg)
			if _, err := s.preparePlatformInstallData(context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			if kind == "runtime_offline" {
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			} else if pg {
				mustExec(t, s.st, `CREATE FUNCTION fixture_queue_review_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture review failure';END $$`)
				mustExec(t, s.st, `CREATE TRIGGER fixture_queue_review_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.install_review_required') EXECUTE FUNCTION fixture_queue_review_failure()`)
				t.Cleanup(func() {
					mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_queue_review_failure ON audit_log`)
					mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_queue_review_failure()`)
				})
			} else {
				mustExec(t, s.st, `CREATE TRIGGER fixture_queue_review_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.install_review_required' BEGIN SELECT RAISE(ABORT,'fixture review failure');END`)
			}
			before := dumpOf(t, s.st)
			selected, err := s.claimNextPlatformStaticInstall(context.Background(), platformFixtureID(935))
			if kind == "runtime_offline" {
				if err != nil || !selected.reviewed {
					t.Fatal("offline runtime prevented denial-only recovery marking", err)
				}
			} else {
				if err == nil {
					t.Fatal("review committed without audit")
				}
				if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
					t.Fatal("failed review partly committed")
				}
				if pg {
					mustExec(t, s.st, `DROP TRIGGER fixture_queue_review_failure ON audit_log`)
				} else {
					mustExec(t, s.st, `DROP TRIGGER fixture_queue_review_failure`)
				}
				if selected, err = s.claimNextPlatformStaticInstall(context.Background(), platformFixtureID(935)); err != nil || !selected.reviewed {
					t.Fatal("review retry after rollback", err)
				}
			}
		})
	}
}
func TestPlatformInstallQueueRecoveryBoundariesSQLite(t *testing.T) {
	testPlatformInstallQueueRecoveryBoundaries(t, false)
}
func TestPlatformInstallQueueRecoveryBoundariesPostgres(t *testing.T) {
	testPlatformInstallQueueRecoveryBoundaries(t, true)
}
