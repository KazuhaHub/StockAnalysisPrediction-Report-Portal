package app

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformInstallStageFixture(t *testing.T, pg bool) (*Server, platformActorSession, platformOperationLease) {
	t.Helper()
	s, session, release := platformInstallFixture(t, pg)
	plan, err := s.createPlatformNativeInstallReview(session, release.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformInstallReview(session, plan.ID, plan.SHA256, platformFixtureID(270))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(271))
	if err != nil {
		t.Fatal(err)
	}
	return s, session, lease
}
func testPlatformInstallStaging(t *testing.T, pg bool) {
	s, _, lease := platformInstallStageFixture(t, pg)
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=0`)
	for _, next := range []appplatform.Stage{appplatform.StageValidate, appplatform.StageDownload, appplatform.StageStage, appplatform.StagePrepareData} {
		old := lease
		var err error
		lease, err = s.st.advancePlatformInstallStaging(old)
		if err != nil {
			t.Fatal(err)
		}
		if lease.snapshot.Stage != next || lease.snapshot.Revision != old.snapshot.Revision+1 || lease.rowRevision != old.rowRevision+1 || lease.fence != old.fence || lease.owner != old.owner || lease.leaseUntilMS != old.leaseUntilMS {
			t.Fatal("stage lost fencing/lease identity")
		}
		if _, err := s.st.advancePlatformInstallStaging(old); !errors.Is(err, errAppPlatformLeaseConflict) {
			t.Fatalf("stale lease advanced: %v", err)
		}
	}
	before := dumpOf(t, s.st)
	if _, err := s.st.advancePlatformInstallStaging(lease); !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatalf("prepared data without provider: %v", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("unsupported stage mutated state")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects`) != 0 {
		t.Fatal("metadata validation fabricated physical proof")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.operation_stage' AND target_id=?`, lease.id) != 4 {
		t.Fatal("missing stage audit")
	}
	if _, err := s.st.heartbeatPlatformOperation(lease); err != nil {
		t.Fatal(err)
	}
}
func testPlatformInstallStagingRefusal(t *testing.T, pg bool) {
	for _, name := range []string{"trust", "archive", "review", "request", "policy", "capability", "sponsor", "inactive_sponsor", "expired_sponsor", "deleted_sponsor", "revoked_key", "expired_lease", "paused"} {
		t.Run(name, func(t *testing.T) {
			s, session, lease := platformInstallStageFixture(t, pg)
			switch name {
			case "trust":
				key := platformTrustFixtureKey(9, "next")
				if _, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "add_key", publisherID: "fixture-publisher", expectedRevision: 1, key: &key}); err != nil {
					t.Fatal(err)
				}
			case "archive":
				var raw []byte
				if err := s.st.queryRow(`SELECT content FROM app_platform_frontend_artifacts`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.st, `UPDATE app_platform_frontend_artifacts SET content=?`, bytes.Repeat([]byte{1}, len(raw)))
			case "review":
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json='{}'`)
			case "request":
				mustExec(t, s.st, `UPDATE app_platform_operations SET request_sha256=?`, platformDigest([]byte("other")))
			case "policy":
				mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=policy_revision+1`)
			case "capability":
				mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1`)
			case "sponsor":
				mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, session.username)
			case "inactive_sponsor":
				mustExec(t, s.st, `UPDATE users SET active=0 WHERE username=?`, session.username)
			case "expired_sponsor":
				mustExec(t, s.st, `UPDATE users SET expires_at='2000-01-01' WHERE username=?`, session.username)
			case "deleted_sponsor":
				if err := s.st.DeleteUser(session.username); err != nil {
					t.Fatal(err)
				}
			case "revoked_key":
				mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET revoked_ms=1 WHERE key_id='first-key'`)
			case "expired_lease":
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			case "paused":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			}
			before := dumpOf(t, s.st)
			if _, err := s.st.advancePlatformInstallStaging(lease); err == nil {
				t.Fatal("invalid stage evidence accepted")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refusal mutated stage")
			}
		})
	}
}
func TestPlatformInstallStagingSQLite(t *testing.T) {
	testPlatformInstallStaging(t, false)
	testPlatformInstallStagingRefusal(t, false)
}
func TestPlatformInstallStagingPostgres(t *testing.T) {
	testPlatformInstallStaging(t, true)
	testPlatformInstallStagingRefusal(t, true)
}

func TestPostgresPlatformInstallStageExpiryAfterAuditWait(t *testing.T) {
	for _, kind := range []string{"lease", "key"} {
		t.Run(kind, func(t *testing.T) {
			s, _, lease := platformInstallStageFixture(t, true)
			now, err := platformDatabaseNow(s.st.poolExec())
			if err != nil {
				t.Fatal(err)
			}
			expiry := now + 750
			if kind == "lease" {
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=? WHERE id=?`, expiry, lease.id)
				lease.leaseUntilMS = expiry
			} else {
				mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET not_after_ms=? WHERE key_id='first-key'`, expiry)
			}
			holder, err := s.st.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback()
			if _, err := holder.Exec(`LOCK TABLE audit_log IN SHARE MODE`); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { _, err := s.st.advancePlatformInstallStaging(lease); result <- err }()
			blocked := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				var n int
				if err := s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%INSERT INTO audit_log%'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					blocked = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !blocked {
				holder.Rollback()
				t.Fatalf("worker did not reach held audit lock: %v", <-result)
			}
			for {
				now, err = platformDatabaseNow(s.st.poolExec())
				if err != nil {
					t.Fatal(err)
				}
				if now >= expiry {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := holder.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-result; err == nil {
				t.Fatal("expired evidence committed a stage")
			}
			if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "queued" || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.operation_stage' AND target_id=?`, lease.id) != 0 {
				t.Fatal("expiry left stage or audit committed")
			}
		})
	}
}
func testPlatformInstallStageAuditRollback(t *testing.T, pg bool) {
	s, _, lease := platformInstallStageFixture(t, pg)
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_install_stage_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='app_platform.operation_stage' THEN RAISE EXCEPTION 'fixture stage failure'; END IF; RETURN NEW; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_install_stage_audit_failure BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fixture_install_stage_audit_failure()`)
		t.Cleanup(func() {
			if _, err := s.st.exec(`DROP TRIGGER IF EXISTS fixture_install_stage_audit_failure ON audit_log`); err != nil {
				t.Error(err)
			}
			if _, err := s.st.exec(`DROP FUNCTION IF EXISTS fixture_install_stage_audit_failure()`); err != nil {
				t.Error(err)
			}
		})
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_install_stage_audit_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.operation_stage' BEGIN SELECT RAISE(ABORT,'fixture stage failure');END`)
	}
	before := dumpOf(t, s.st)
	if _, err := s.st.advancePlatformInstallStaging(lease); err == nil {
		t.Fatal("stage audit failure ignored")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("audit failure advanced stage or revision")
	}
}
func TestPlatformInstallStageAuditSQLite(t *testing.T) {
	testPlatformInstallStageAuditRollback(t, false)
}
func TestPlatformInstallStageAuditPostgres(t *testing.T) {
	testPlatformInstallStageAuditRollback(t, true)
}
