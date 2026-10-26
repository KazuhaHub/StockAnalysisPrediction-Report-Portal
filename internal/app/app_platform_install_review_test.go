package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformInstallFixture(t *testing.T, pg bool) (*Server, platformActorSession, platformStagedRelease) {
	t.Helper()
	s, session := platformTrustTestServer(t, pg)
	platformRegisterTestPublisher(t, s, session)
	raw, _, archive := platformSignedReleaseFixture(t, "1.0.0-beta.1")
	var manifest appplatform.Manifest
	if json.Unmarshal(raw, &manifest) != nil {
		t.Fatal("bad fixture")
	}
	manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	manifest.Capabilities.Required = []string{}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := s.stagePlatformRelease(session, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	return s, session, staged
}
func testPlatformInstallReview(t *testing.T, pg bool) {
	s, session, staged := platformInstallFixture(t, pg)
	review, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != 0 {
		t.Fatal("review installed app")
	}
	commit, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(200))
	if err != nil {
		t.Fatal(err)
	}
	if commit.Replayed || commit.Stage != appplatform.StageQueued || commit.Generation != 1 {
		t.Fatal("invalid initial install state")
	}
	if scalar[string](t, s.st, `SELECT phase FROM app_platform_installations`) != "pending" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations`) != 0 {
		t.Fatal("admission pretended activation")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces`) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("admission bypassed data preparation or audience review")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE granted=1`) != 0 {
		t.Fatal("unimplemented capability granted")
	}
	sealed := scalar[string](t, s.st, `SELECT sealed_identity_seed FROM app_platform_installations`)
	seed, err := s.openSecret("app-platform", commit.InstallationID, sealed)
	if err != nil || len(seed) != 32 {
		t.Fatalf("installation identity not purpose sealed: %v", err)
	}
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=0`)
	replay, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(200))
	if err != nil || !replay.Replayed || replay.OperationID != commit.OperationID {
		t.Fatalf("idempotent replay: %v", err)
	}
	if _, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(201)); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatalf("consumed plan reused: %v", err)
	}
	if _, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatalf("existing install accepted: %v", err)
	}
	lease, err := s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(202))
	if err != nil {
		t.Fatal(err)
	}
	if lease.snapshot.Action != appplatform.ActionInstall || lease.snapshot.Managed {
		t.Fatal("worker got wrong install mode")
	}
}
func testPlatformInstallStaleReview(t *testing.T, pg bool) {
	for _, name := range []string{"expiry", "trust", "demotion", "json", "wrong_digest", "old_unfinished"} {
		t.Run(name, func(t *testing.T) {
			s, session, staged := platformInstallFixture(t, pg)
			review, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "expiry":
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=0`)
			case "trust":
				key := platformTrustFixtureKey(9, "rotated")
				if _, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "add_key", publisherID: "fixture-publisher", expectedRevision: 1, key: &key}); err != nil {
					t.Fatal(err)
				}
			case "demotion":
				mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, session.username)
			case "json":
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json='{}'`)
			case "wrong_digest":
				review.SHA256 = platformDigest([]byte("other"))
			case "old_unfinished":
				if err := platformSeedInstallation(t, s.st.poolExec(), platformFixtureID(210), "fixture-publisher", "notes-sample"); err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='removed',phase='pending'`)
			}
			before := dumpOf(t, s.st)
			if _, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(211)); err == nil {
				t.Fatal("stale install review accepted")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refusal wrote state")
			}
		})
	}
}
func testPlatformConcurrentInstallCommit(t *testing.T, pg bool) {
	s, session, staged := platformInstallFixture(t, pg)
	review, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	commits := make(chan platformRemovalCommit, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			result, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(220))
			commits <- result
			errs <- err
		})
	}
	wg.Wait()
	close(commits)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	newCount := 0
	for c := range commits {
		if !c.Replayed {
			newCount++
		}
		if id != "" && id != c.OperationID {
			t.Fatal("duplicate operation")
		}
		id = c.OperationID
	}
	if newCount != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != 1 {
		t.Fatal("duplicate install")
	}
}
func TestPlatformInstallReviewSQLite(t *testing.T) {
	testPlatformInstallReview(t, false)
	testPlatformInstallStaleReview(t, false)
	testPlatformConcurrentInstallCommit(t, false)
}
func TestPlatformInstallReviewPostgres(t *testing.T) {
	testPlatformInstallReview(t, true)
	testPlatformInstallStaleReview(t, true)
	testPlatformConcurrentInstallCommit(t, true)
}
func TestPlatformInstallRequirements(t *testing.T) {
	raw, _, _ := platformSignedReleaseFixture(t, "1.0.0-beta.1")
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	m := parsed.Manifest
	m.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	if compatible, err := appplatform.EvaluateCompatibility(m, appplatform.CompatibilityTarget{PlatformAPI: platformDevelopmentAPI}); err != nil || !compatible.Compatible {
		t.Fatalf("required-capability fixture must be otherwise compatible: %v", err)
	}
	if err := platformInstallRequirements(m); err == nil {
		t.Fatal("unimplemented required storage accepted")
	}
	m.Capabilities.Required = []string{}
	if err := platformInstallRequirements(m); err != nil {
		t.Fatal(err)
	}
	m.Compatibility.PlatformAPI = ">=3.0.0"
	if err := platformInstallRequirements(m); err == nil {
		t.Fatal("incompatible host accepted")
	}
	managedRaw, err := os.ReadFile("../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	managed, err := appplatform.ParseDescriptor(managedRaw)
	if err != nil {
		t.Fatal(err)
	}
	m = managed.Manifest
	m.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	m.Capabilities.Required = []string{}
	m.Capabilities.Optional = []string{}
	if result, err := appplatform.EvaluateCompatibility(m, appplatform.CompatibilityTarget{PlatformAPI: platformDevelopmentAPI}); err != nil || len(result.Issues) != 1 || result.Issues[0] != appplatform.RunnerRequired {
		t.Fatalf("managed fixture did not isolate missing runner: %v", err)
	}
	if err := platformInstallRequirements(m); err == nil {
		t.Fatal("managed runtime fabricated")
	}
}

func testPlatformInstallAtomicRollback(t *testing.T, pg bool) {
	s, session, staged := platformInstallFixture(t, pg)
	review, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_install_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='app_platform.operation_admitted' THEN RAISE EXCEPTION 'fixture install failure'; END IF; RETURN NEW; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_install_audit_failure BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fixture_install_audit_failure()`)
		t.Cleanup(func() {
			if _, err := s.st.exec(`DROP TRIGGER IF EXISTS fixture_install_audit_failure ON audit_log`); err != nil {
				t.Error(err)
			}
			if _, err := s.st.exec(`DROP FUNCTION IF EXISTS fixture_install_audit_failure()`); err != nil {
				t.Error(err)
			}
		})
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_install_audit_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.operation_admitted' BEGIN SELECT RAISE(ABORT,'fixture install failure');END`)
	}
	before := dumpOf(t, s.st)
	if _, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(230)); err == nil {
		t.Fatal("audit failure accepted")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("failed install left identity, operation, grants or consumed review")
	}
}
func TestPlatformInstallAtomicRollbackSQLite(t *testing.T) {
	testPlatformInstallAtomicRollback(t, false)
}
func TestPlatformInstallAtomicRollbackPostgres(t *testing.T) {
	testPlatformInstallAtomicRollback(t, true)
}

func TestPostgresPlatformInstallReviewExpiryAfterAuditWait(t *testing.T) {
	s, session, staged := platformInstallFixture(t, true)
	review, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	expiry := now + 500
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=? WHERE id=?`, expiry, review.ID)
	holder, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.Exec(`LOCK TABLE audit_log IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(240))
		result <- err
	}()
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
		t.Fatalf("consumer did not reach actual audit lock: %v", <-result)
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
	if err := <-result; !errors.Is(err, errAppPlatformPlanExpired) {
		t.Fatalf("expired install committed after lock wait: %v", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants`) != 0 {
		t.Fatal("expired install left partial authority")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE consumed_ms IS NOT NULL`) != 0 {
		t.Fatal("expired review consumed")
	}
}

func testPlatformInstallRemovedOperationFence(t *testing.T, pg bool) {
	s, session, staged := platformInstallFixture(t, pg)
	review, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(250))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='removed',phase='removed' WHERE id=?`, commit.InstallationID)
	if _, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatalf("tombstone hid unfinished operation: %v", err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='recovery_required',state_revision=2,completed_ms=1 WHERE id=?`, commit.OperationID)
	if _, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatalf("tombstone hid unresolved recovery: %v", err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='failed' WHERE id=?`, commit.OperationID)
	next, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformInstallReview(session, next.ID, next.SHA256, platformFixtureID(250)); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatalf("global idempotency accepted changed review: %v", err)
	}
	reinstall, err := s.consumePlatformInstallReview(session, next.ID, next.SHA256, platformFixtureID(251))
	if err != nil {
		t.Fatal(err)
	}
	if reinstall.InstallationID == commit.InstallationID {
		t.Fatal("reinstall revived old authority")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != 2 {
		t.Fatal("reinstall lost tombstone")
	}
}
func TestPlatformInstallRemovedFenceSQLite(t *testing.T) {
	testPlatformInstallRemovedOperationFence(t, false)
}
func TestPlatformInstallRemovedFencePostgres(t *testing.T) {
	testPlatformInstallRemovedOperationFence(t, true)
}
