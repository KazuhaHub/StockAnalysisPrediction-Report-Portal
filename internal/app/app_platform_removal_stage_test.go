package app

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformRemovalStageFixture(t *testing.T, postgres, managed bool, action appplatform.Action) (*Server, platformActorSession, platformOperationLease) {
	t.Helper()
	s, session := platformRemovalFixture(t, postgres, managed)
	plan, err := s.createPlatformRemovalReview(session, platformFixtureID(1), action)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformRemovalReview(session, plan.ID, plan.SHA256, platformFixtureID(20))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(30))
	if err != nil {
		t.Fatal(err)
	}
	return s, session, lease
}

func testPlatformRemovalControlStages(t *testing.T, postgres bool) {
	for _, managed := range []bool{false, true} {
		for _, action := range []appplatform.Action{appplatform.ActionDisable, appplatform.ActionUninstall} {
			t.Run(fmt.Sprintf("%s/managed=%t", action, managed), func(t *testing.T) {
				s, _, lease := platformRemovalStageFixture(t, postgres, managed, action)
				// A consumed decision is durable cleanup intent, not a fresh plan.
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=0`)
				original := lease
				for _, next := range []appplatform.Stage{appplatform.StageValidate, appplatform.StageRevoke} {
					old := lease
					var err error
					lease, err = s.st.advancePlatformRemovalControl(old)
					if err != nil {
						t.Fatal(err)
					}
					if lease.snapshot.Stage != next || lease.snapshot.Revision != old.snapshot.Revision+1 || lease.rowRevision != old.rowRevision+1 ||
						lease.owner != old.owner || lease.fence != old.fence || lease.leaseUntilMS != old.leaseUntilMS {
						t.Fatalf("incorrect control step: %+v", lease)
					}
					if _, err := s.st.advancePlatformRemovalControl(old); !errors.Is(err, errAppPlatformLeaseConflict) {
						t.Fatalf("stale stage token: %v", err)
					}
				}
				before := dumpOf(t, s.st)
				if _, err := s.st.advancePlatformRemovalControl(lease); !errors.Is(err, errAppPlatformStageEvidence) {
					t.Fatalf("completed revocation without evidence: %v", err)
				}
				if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
					t.Fatal("unsupported stage mutated state")
				}
				if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE completed_ms IS NOT NULL`) != 0 || scalar[int](t, s.st, `SELECT observed_bytes FROM app_platform_data_namespaces`) != 123 ||
					scalar[string](t, s.st, `SELECT owner_installation_id FROM app_platform_data_namespaces`) != original.installationID {
					t.Fatal("control step completed work or released data")
				}
				if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.operation_stage' AND target_id=?`, lease.id) != 2 {
					t.Fatal("missing stage audit")
				}
				if _, err := s.st.heartbeatPlatformOperation(lease); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestAppPlatformRemovalControlStages(t *testing.T) { testPlatformRemovalControlStages(t, false) }
func TestPostgresAppPlatformRemovalControlStages(t *testing.T) {
	testPlatformRemovalControlStages(t, true)
}

func testPlatformRemovalControlActorGone(t *testing.T, postgres bool) {
	s, session, lease := platformRemovalStageFixture(t, postgres, true, appplatform.ActionUninstall)
	if err := s.st.DeleteUser(session.username); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0,revision=revision+1`)
	mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET revoked_ms=1`)
	for range 2 {
		var err error
		lease, err = s.st.advancePlatformRemovalControl(lease)
		if err != nil {
			t.Fatal(err)
		}
	}
	if lease.snapshot.Stage != appplatform.StageRevoke || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("cleanup recreated actor authority")
	}
}

func TestAppPlatformRemovalControlActorGone(t *testing.T) {
	testPlatformRemovalControlActorGone(t, false)
}
func TestPostgresAppPlatformRemovalControlActorGone(t *testing.T) {
	testPlatformRemovalControlActorGone(t, true)
}

func testPlatformRemovalStageGuards(t *testing.T, postgres bool) {
	for _, tc := range []struct {
		name, query string
		args        []any
		want        error
	}{
		{"plan_bytes", `UPDATE app_platform_review_plans SET plan_json='{}'`, nil, errAppPlatformStageEvidence},
		{"plan_not_consumed", `UPDATE app_platform_review_plans SET consumed_ms=NULL`, nil, errAppPlatformStageEvidence},
		{"plan_scope", `UPDATE app_platform_review_plans SET actor_scope=?`, []any{strings.Repeat("f", 64)}, errAppPlatformStageEvidence},
		{"request_binding", `UPDATE app_platform_operations SET request_sha256=?`, []any{strings.Repeat("f", 64)}, errAppPlatformStageEvidence},
		{"active_route", `UPDATE app_platform_activations SET state='active'`, nil, errAppPlatformStageEvidence},
		{"source_generation", `UPDATE app_platform_installations SET generation=1`, nil, errAppPlatformStageEvidence},
		{"desired_enabled", `UPDATE app_platform_installations SET desired_state='enabled'`, nil, errAppPlatformStageEvidence},
		{"different_app", `UPDATE app_platform_installations SET app_id='different-app'`, nil, errAppPlatformStageEvidence},
		{"unfenced_data", `UPDATE app_platform_data_namespaces SET writer_fence=0`, nil, errAppPlatformStageEvidence},
		{"changed_data_format", `UPDATE app_platform_data_namespaces SET data_format=2`, nil, errAppPlatformStageEvidence},
		{"expired_lease", `UPDATE app_platform_operations SET lease_until_ms=0`, nil, errAppPlatformLeaseConflict},
		{"row_exhausted", `UPDATE app_platform_operations SET row_revision=?`, []any{int64(math.MaxInt64)}, errAppPlatformLeaseConflict},
		{"paused", `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`, nil, errAppPlatformControlPaused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, lease := platformRemovalStageFixture(t, postgres, false, appplatform.ActionDisable)
			mustExec(t, s.st, tc.query, tc.args...)
			if tc.name == "row_exhausted" {
				lease.rowRevision = math.MaxInt64
			}
			before := dumpOf(t, s.st)
			if _, err := s.st.advancePlatformRemovalControl(lease); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused transition mutated database")
			}
		})
	}
}

func TestAppPlatformRemovalStageGuards(t *testing.T)         { testPlatformRemovalStageGuards(t, false) }
func TestPostgresAppPlatformRemovalStageGuards(t *testing.T) { testPlatformRemovalStageGuards(t, true) }

func testPlatformRemovalStageConcurrent(t *testing.T, postgres bool) {
	s, _, lease := platformRemovalStageFixture(t, postgres, true, appplatform.ActionUninstall)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, err := s.st.advancePlatformRemovalControl(lease); errs <- err })
	}
	wg.Wait()
	close(errs)
	winners := 0
	for err := range errs {
		if err == nil {
			winners++
		} else if !errors.Is(err, errAppPlatformLeaseConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.operation_stage' AND target_id=?`, lease.id) != 1 {
		t.Fatalf("stage committed %d times", winners)
	}
}

func TestAppPlatformRemovalStageConcurrent(t *testing.T) {
	testPlatformRemovalStageConcurrent(t, false)
}
func TestPostgresAppPlatformRemovalStageConcurrent(t *testing.T) {
	testPlatformRemovalStageConcurrent(t, true)
}

func testPlatformRemovalStageAuditRollback(t *testing.T, postgres bool) {
	s, _, lease := platformRemovalStageFixture(t, postgres, true, appplatform.ActionDisable)
	if postgres {
		mustExec(t, s.st, `CREATE FUNCTION fixture_stage_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='app_platform.operation_stage' THEN RAISE EXCEPTION 'fixture stage audit failure'; END IF; RETURN NEW; END $$`)
		t.Cleanup(func() {
			if _, err := s.st.exec(`DROP TRIGGER IF EXISTS fixture_stage_audit_failure ON audit_log`); err != nil {
				t.Error(err)
			}
			if _, err := s.st.exec(`DROP FUNCTION IF EXISTS fixture_stage_audit_failure()`); err != nil {
				t.Error(err)
			}
		})
		mustExec(t, s.st, `CREATE TRIGGER fixture_stage_audit_failure BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fixture_stage_audit_failure()`)
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_stage_audit_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.operation_stage' BEGIN SELECT RAISE(ABORT,'fixture stage audit failure');END`)
	}
	before := dumpOf(t, s.st)
	if _, err := s.st.advancePlatformRemovalControl(lease); err == nil {
		t.Fatal("stage succeeded without its audit")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("failed audit left stage advanced")
	}
}

func TestAppPlatformRemovalStageAuditRollback(t *testing.T) {
	testPlatformRemovalStageAuditRollback(t, false)
}
func TestPostgresAppPlatformRemovalStageAuditRollback(t *testing.T) {
	testPlatformRemovalStageAuditRollback(t, true)
}

func testPlatformRemovalControlNoData(t *testing.T, postgres bool) {
	s, session := platformRemovalFixture(t, postgres, false)
	mustExec(t, s.st, `DELETE FROM app_platform_activations`)
	mustExec(t, s.st, `DELETE FROM app_platform_data_namespaces`)
	plan, err := s.createPlatformRemovalReview(session, platformFixtureID(1), appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformRemovalReview(session, plan.ID, plan.SHA256, platformFixtureID(20))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(30))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		lease, err = s.st.advancePlatformRemovalControl(lease)
		if err != nil {
			t.Fatal(err)
		}
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces`) != 0 {
		t.Fatal("cleanup invented application data")
	}
}

func TestAppPlatformRemovalControlNoData(t *testing.T) { testPlatformRemovalControlNoData(t, false) }
func TestPostgresAppPlatformRemovalControlNoData(t *testing.T) {
	testPlatformRemovalControlNoData(t, true)
}

func TestPostgresAppPlatformRemovalStagePauseWhileWaiting(t *testing.T) {
	s, _, lease := platformRemovalStageFixture(t, true, true, appplatform.ActionDisable)
	holder, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	var id string
	if err := holder.QueryRow(`SELECT id FROM app_platform_review_plans WHERE installation_id=$1 FOR UPDATE`, lease.installationID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.st.advancePlatformRemovalControl(lease); done <- err }()
	blocked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()
            AND wait_event_type='Lock' AND query LIKE '%FROM app_platform_review_plans WHERE id=%FOR UPDATE%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			blocked = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !blocked {
		holder.Rollback()
		<-done
		t.Fatal("stage did not reach held review lock")
	}
	mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("pause while waiting ignored: %v", err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "queued" {
		t.Fatal("paused controller advanced stage")
	}
}
