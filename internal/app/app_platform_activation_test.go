package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformActivationFixture(t *testing.T, pg bool, audience bool) (*Server, platformActorSession, platformOperationLease) {
	t.Helper()
	s, session, release := platformInstallFixture(t, pg)
	var targets []platformAudienceTarget
	if audience {
		targets = []platformAudienceTarget{{kind: "user", username: session.username}}
	}
	review, err := s.createPlatformInstallAudienceReview(session, release.sha256, 1, targets)
	if err != nil {
		t.Fatal(err)
	}
	return platformFinishActivationFixture(t, s, session, review)
}
func platformFinishActivationFixture(t *testing.T, s *Server, session platformActorSession, review platformRemovalReview) (*Server, platformActorSession, platformOperationLease) {
	t.Helper()
	admitted, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(910))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.st.claimPlatformOperation(admitted.OperationID, platformFixtureID(911))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmPlatformObservedRuntime(session, runtime, runtimeObservationFixture(runtime)); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []appplatform.Stage{appplatform.StageValidate, appplatform.StageDownload, appplatform.StageStage, appplatform.StagePrepareData} {
		lease, err = s.st.advancePlatformInstallStaging(lease)
		if err != nil || lease.snapshot.Stage != stage {
			t.Fatal(err)
		}
	}
	intent, err := s.preparePlatformInstallData(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformRuntimeEndpoint{path: "/run/portal-apps/runner.sock", controllerUID: 41001, controllerGID: 41001}
	lease, err = s.commitPlatformObservedData(context.Background(), lease, intent, endpoint, platformDataObservationFixture(t, intent, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	return s, session, lease
}
func testPlatformStaticActivation(t *testing.T, pg bool) {
	s, session, lease := platformActivationFixture(t, pg, true)
	if _, err := s.inspectPlatformLaunchAccess(session, lease.installationID); err == nil {
		t.Fatal("pending installation opened")
	}
	result, err := s.activatePlatformStaticInstall(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if result.snapshot.Stage != appplatform.StageComplete || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations`) != "ready" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE completed_ms IS NOT NULL AND lease_owner IS NULL AND lease_until_ms IS NULL`) != 1 {
		t.Fatal("activation did not finish operation")
	}
	if _, err := s.inspectPlatformLaunchAccess(session, lease.installationID); err != nil {
		t.Fatal("explicitly reviewed installer audience cannot open", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 1 {
		t.Fatal("audience not applied exactly")
	}
	if _, err := s.activatePlatformStaticInstall(context.Background(), lease); err == nil {
		t.Fatal("activation replayed")
	}
}
func TestPlatformStaticActivationSQLite(t *testing.T)   { testPlatformStaticActivation(t, false) }
func TestPlatformStaticActivationPostgres(t *testing.T) { testPlatformStaticActivation(t, true) }
func TestPlatformStaticActivationNoImplicitAdminSQLite(t *testing.T) {
	s, session, lease := platformActivationFixture(t, false, false)
	if _, err := s.activatePlatformStaticInstall(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("empty audience silently gained grants")
	}
	if _, err := s.inspectPlatformLaunchAccess(session, lease.installationID); err == nil {
		t.Fatal("admin bypassed missing audience")
	}
}

func platformActivationReader(t *testing.T, s *Server, username string) platformActorSession {
	t.Helper()
	if err := s.st.UpsertUser(User{Username: username, PasswordHash: "fixture", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.st.DeleteUser(username); err != nil {
			t.Error(err)
		}
	})
	user := s.st.GetUser(username)
	generation, err := notificationAccountGeneration(s.st.poolExec(), username)
	if err != nil {
		t.Fatal(err)
	}
	session := platformActorSession{username: username, created: user.CreatedAt, sessionID: platformDigest([]byte(t.Name() + username)), sessionRevision: user.SessionRev, generation: generation, expires: time.Unix(time.Now().Add(time.Hour).Unix(), 0)}
	platformSeedActorSession(t, s.st, session)
	return session
}
func testPlatformStaticActivationOU(t *testing.T, pg bool) {
	s, actor, release := platformInstallFixture(t, pg)
	reader := platformActivationReader(t, s, "activation-ou-viewer")
	parent, err := s.st.CreateUserGroup("Activation parent", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.st.CreateUserGroup("Activation child", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.st.SetPrimaryGroups([]string{reader.username}, 0)
		if _, _, err := s.st.DeleteUserGroup(child); err != nil {
			t.Error(err)
		}
		if _, _, err := s.st.DeleteUserGroup(parent); err != nil {
			t.Error(err)
		}
	})
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, parent, child)
	if _, err := s.st.SetPrimaryGroups([]string{reader.username}, child); err != nil {
		t.Fatal(err)
	}
	review, err := s.createPlatformInstallAudienceReview(actor, release.sha256, 1, []platformAudienceTarget{{kind: "ou", ouID: parent}})
	if err != nil {
		t.Fatal(err)
	}
	s, _, lease := platformFinishActivationFixture(t, s, actor, review)
	if _, err := s.activatePlatformStaticInstall(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := s.inspectPlatformLaunchAccess(reader, lease.installationID); err != nil {
		t.Fatal("reviewed ancestor audience denied ordinary user", err)
	}
	if _, err := s.inspectPlatformLaunchAccess(actor, lease.installationID); err == nil {
		t.Fatal("installer bypassed selected OU audience")
	}
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=NULL WHERE id=?`, child)
	if _, err := s.inspectPlatformLaunchAccess(reader, lease.installationID); err == nil {
		t.Fatal("policy fallback retained ancestor grant")
	}
}
func TestPlatformStaticActivationOUSQLite(t *testing.T)   { testPlatformStaticActivationOU(t, false) }
func TestPlatformStaticActivationOUPostgres(t *testing.T) { testPlatformStaticActivationOU(t, true) }
func testPlatformStaticActivationRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"namespace", "receipt", "effect", "backend", "desired", "policy", "lease", "sponsor", "runtime", "key", "artifact", "pause", "recipient"} {
		t.Run(kind, func(t *testing.T) {
			s, actor, lease := platformActivationFixture(t, pg, true)
			switch kind {
			case "namespace":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET writer_fence=2`)
			case "receipt":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}'`)
			case "effect":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='unknown'`)
			case "backend":
				mustExec(t, s.st, `UPDATE app_platform_installations SET runtime_id=(SELECT id FROM app_platform_runtime_bindings)`)
			case "desired":
				mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='disabled'`)
			case "policy":
				mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=2`)
			case "lease":
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			case "sponsor":
				mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "key":
				mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET revoked_ms=1`)
			case "artifact":
				archive := scalar[[]byte](t, s.st, `SELECT content FROM app_platform_frontend_artifacts`)
				archive[0] ^= 1
				mustExec(t, s.st, `UPDATE app_platform_frontend_artifacts SET content=?`, archive)
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "recipient":
				mustExec(t, s.st, `UPDATE users SET created_at=created_at||'-changed' WHERE username=?`, actor.username)
			}
			before := dumpOf(t, s.st)
			if _, err := s.activatePlatformStaticInstall(context.Background(), lease); err == nil {
				t.Fatal("invalid static candidate activated", kind)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("activation refusal changed state", kind)
			}
		})
	}
}
func TestPlatformStaticActivationRefusalsSQLite(t *testing.T) {
	testPlatformStaticActivationRefusals(t, false)
}
func TestPlatformStaticActivationRefusalsPostgres(t *testing.T) {
	testPlatformStaticActivationRefusals(t, true)
}
func testPlatformStaticActivationRollback(t *testing.T, pg bool) {
	for _, table := range []string{"app_platform_access_grants", "app_platform_installations", "app_platform_operations"} {
		t.Run(table, func(t *testing.T) {
			s, _, lease := platformActivationFixture(t, pg, true)
			operation := "UPDATE"
			condition := ""
			if table == "app_platform_access_grants" {
				operation = "INSERT"
			}
			if table == "app_platform_installations" {
				condition = " WHEN (NEW.phase='ready')"
			}
			if table == "app_platform_operations" {
				condition = " WHEN (NEW.stage='complete')"
			}
			if pg {
				mustExec(t, s.st, `CREATE FUNCTION fixture_activation_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture activation failure';END $$`)
				mustExec(t, s.st, `CREATE TRIGGER fixture_activation_failure BEFORE `+operation+` ON `+table+` FOR EACH ROW`+condition+` EXECUTE FUNCTION fixture_activation_failure()`)
				t.Cleanup(func() { mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_activation_failure() CASCADE`) })
			} else {
				mustExec(t, s.st, `CREATE TRIGGER fixture_activation_failure BEFORE `+operation+` ON `+table+condition+` BEGIN SELECT RAISE(ABORT,'fixture activation failure');END`)
			}
			if _, err := s.activatePlatformStaticInstall(context.Background(), lease); err == nil {
				t.Fatal("partial activation succeeded")
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations`) != "pending" || scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "activate" {
				t.Fatal("failed activation partly committed")
			}
			if pg {
				mustExec(t, s.st, `DROP TRIGGER fixture_activation_failure ON `+table)
			} else {
				mustExec(t, s.st, `DROP TRIGGER fixture_activation_failure`)
			}
			if _, err := s.activatePlatformStaticInstall(context.Background(), lease); err != nil {
				t.Fatal("activation retry after rollback", err)
			}
		})
	}
}
func TestPlatformStaticActivationRollbackSQLite(t *testing.T) {
	testPlatformStaticActivationRollback(t, false)
}
func TestPlatformStaticActivationRollbackPostgres(t *testing.T) {
	testPlatformStaticActivationRollback(t, true)
}

// An authentic publisher signature does not imply that the host can safely
// compile the package into the supported frontend document profile.
func TestPlatformStaticActivationUnsupportedFrontendSQLite(t *testing.T) {
	s, actor, _ := platformInstallFixture(t, false)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write([]byte(`<html><script type="module">export const value=1;</script></html>`)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := platformSignedReleaseFixture(t, "1.0.0-beta.2")
	var manifest appplatform.Manifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	manifest.Capabilities.Required = []string{}
	manifest.Frontend.SHA256 = platformDigest(archive.Bytes())
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(actor, raw, platformSignTestDescriptor(t, raw), archive.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.createPlatformInstallAudienceReview(actor, release.sha256, 1, []platformAudienceTarget{{kind: "user", username: actor.username}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, lease := platformFinishActivationFixture(t, s, actor, review)
	before := dumpOf(t, s.st)
	if _, err := s.activatePlatformStaticInstall(context.Background(), lease); err == nil {
		t.Fatal("unsupported signed frontend activated")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("frontend refusal changed state")
	}
}

func TestPlatformStaticActivationLateAuditPostgres(t *testing.T) {
	for _, kind := range []string{"expired_lease", "canceled_context"} {
		t.Run(kind, func(t *testing.T) {
			s, _, lease := platformActivationFixture(t, true, true)
			delay := "0.5"
			if kind == "expired_lease" {
				now, err := platformDatabaseNow(s.st.poolExec())
				if err != nil {
					t.Fatal(err)
				}
				lease.leaseUntilMS = now + 1000
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=?`, lease.leaseUntilMS)
				delay = "1.2"
			}
			mustExec(t, s.st, `CREATE FUNCTION fixture_slow_activation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(`+delay+`);RETURN NEW;END $$`)
			mustExec(t, s.st, `CREATE TRIGGER fixture_slow_activation_audit BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.install_activated') EXECUTE FUNCTION fixture_slow_activation_audit()`)
			t.Cleanup(func() {
				mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_slow_activation_audit ON audit_log`)
				mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_slow_activation_audit()`)
			})
			ctx := context.Background()
			cancel := func() {}
			if kind == "canceled_context" {
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
			}
			_, err := s.activatePlatformStaticInstall(ctx, lease)
			cancel()
			if err == nil {
				t.Fatal("late activation accepted expired/canceled lease")
			}
			if kind == "canceled_context" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("context ignored", err)
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations`) != "pending" || scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "activate" {
				t.Fatal("late failure partly committed")
			}
		})
	}
}
