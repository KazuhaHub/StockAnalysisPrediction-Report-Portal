package app

import (
	"bytes"
	"errors"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"strings"
	"testing"
)

// These synthetic bindings prove only denial/cleanup. The removal fixture's
// synthetic package never proves an executable credential grant.
func platformSeedRemovalSecret(t *testing.T, s *Server, actor platformActorSession) {
	t.Helper()
	seed, err := s.createPlatformRemovalReview(actor, platformFixtureID(1), appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	platformTestInsert(t, s.st.poolExec(), "credentials", map[string]any{"id": platformFixtureID(2001), "label": "Fixture custody", "state": "active", "sealed_value": "fixture-envelope", "value_revision": 1, "revision": 1, "created_ms": 1, "updated_ms": 1, "revoked_ms": nil})
	for i, state := range []string{"active", "review_required", "revoked"} {
		var revoked any
		if state == "revoked" {
			revoked = int64(1)
		}
		platformTestInsert(t, s.st.poolExec(), "secret_bindings", map[string]any{"id": platformFixtureID(2010 + i), "installation_id": platformFixtureID(1), "name": []string{"first", "second", "third"}[i], "credential_id": platformFixtureID(2001), "credential_value_revision": 1, "operation": "http_bearer", "state": state, "policy_json": "{}", "policy_revision": 1, "review_plan_id": seed.ID, "revision": 1, "created_ms": 1, "updated_ms": 1, "revoked_ms": revoked})
	}
	if err := platformSeedInstallation(t, s.st.poolExec(), platformFixtureID(2090), "fixture-publisher", "neighbor-sample"); err != nil {
		t.Fatal(err)
	}
	platformTestInsert(t, s.st.poolExec(), "secret_bindings", map[string]any{"id": platformFixtureID(2091), "installation_id": platformFixtureID(2090), "name": "neighbor", "credential_id": platformFixtureID(2001), "credential_value_revision": 1, "operation": "http_bearer", "state": "active", "policy_json": "{}", "policy_revision": 1, "review_plan_id": seed.ID, "revision": 1, "created_ms": 1, "updated_ms": 1, "revoked_ms": nil})

}
func testPlatformSecretUninstall(t *testing.T, pg bool) {
	for _, action := range []appplatform.Action{appplatform.ActionDisable, appplatform.ActionUninstall} {
		t.Run(string(action), func(t *testing.T) {
			s, actor := platformRemovalFixture(t, pg, false)
			platformSeedRemovalSecret(t, s, actor)
			plan, err := s.createPlatformRemovalReview(actor, platformFixtureID(1), action)
			if err != nil {
				t.Fatal(err)
			}
			key := platformFixtureID(2080)
			commit, err := s.consumePlatformRemovalReview(actor, plan.ID, plan.SHA256, key)
			if err != nil {
				t.Fatal(err)
			}

			if action == appplatform.ActionUninstall && !strings.Contains(scalar[string](t, s.st, `SELECT detail FROM audit_log WHERE action='app_platform.operation_admitted' AND target_id=?`, commit.OperationID), `"revoked_secret_bindings":2`) {
				t.Fatal("binding denial missing from atomic operation audit")
			}
			expected := 1
			if action == appplatform.ActionUninstall {
				expected = 3
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_secret_bindings WHERE state='revoked'`) != expected {
				t.Fatal("incorrect binding lifecycle denial")
			}
			if action == appplatform.ActionUninstall && scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_secret_bindings WHERE id<>? AND revision=2 AND revoked_ms IS NOT NULL`, platformFixtureID(2012)) != 2 {
				t.Fatal("nonrevoked binding revision not invalidated")
			}
			if scalar[int](t, s.st, `SELECT revision FROM app_platform_secret_bindings WHERE id=?`, platformFixtureID(2012)) != 1 {
				t.Fatal("existing tombstone changed")
			}
			if scalar[string](t, s.st, `SELECT state FROM app_platform_credentials`) != "active" || scalar[string](t, s.st, `SELECT sealed_value FROM app_platform_credentials`) != "fixture-envelope" || scalar[int](t, s.st, `SELECT observed_bytes FROM app_platform_data_namespaces`) != 123 {
				t.Fatal("uninstall damaged shared custody or retained data")
			}

			if scalar[string](t, s.st, `SELECT state FROM app_platform_secret_bindings WHERE id=?`, platformFixtureID(2091)) != "active" || scalar[int](t, s.st, `SELECT revision FROM app_platform_secret_bindings WHERE id=?`, platformFixtureID(2091)) != 1 {
				t.Fatal("neighbor installation binding changed")
			}
			before := dumpOf(t, s.st)
			again, err := s.consumePlatformRemovalReview(actor, plan.ID, plan.SHA256, key)
			if err != nil || !again.Replayed || again.OperationID != commit.OperationID {
				t.Fatal("idempotent replay", err)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("replay changed binding revocation")
			}
		})
	}
}
func TestPlatformSecretUninstallSQLite(t *testing.T)   { testPlatformSecretUninstall(t, false) }
func TestPlatformSecretUninstallPostgres(t *testing.T) { testPlatformSecretUninstall(t, true) }

func testPlatformSecretUninstallStaleReview(t *testing.T, pg bool) {
	s, actor := platformRemovalFixture(t, pg, false)
	platformSeedRemovalSecret(t, s, actor)
	plan, err := s.createPlatformRemovalReview(actor, platformFixtureID(1), appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_secret_bindings SET revision=revision+1 WHERE state='active'`)
	before := dumpOf(t, s.st)
	if _, err := s.consumePlatformRemovalReview(actor, plan.ID, plan.SHA256, platformFixtureID(2081)); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatal("binding change did not stale review", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("stale review wrote")
	}
}
func TestPlatformSecretUninstallStaleReviewSQLite(t *testing.T) {
	testPlatformSecretUninstallStaleReview(t, false)
}
func TestPlatformSecretUninstallStaleReviewPostgres(t *testing.T) {
	testPlatformSecretUninstallStaleReview(t, true)
}

func testPlatformSecretUninstallRollback(t *testing.T, pg bool) {
	for _, reason := range []string{"overflow", "operation_failure"} {
		t.Run(reason, func(t *testing.T) {
			s, actor := platformRemovalFixture(t, pg, false)
			platformSeedRemovalSecret(t, s, actor)
			if reason == "overflow" {
				mustExec(t, s.st, `UPDATE app_platform_secret_bindings SET revision=9223372036854775807 WHERE state='active' AND installation_id=?`, platformFixtureID(1))
			}
			plan, err := s.createPlatformRemovalReview(actor, platformFixtureID(1), appplatform.ActionUninstall)
			if err != nil {
				t.Fatal(err)
			}
			if reason == "operation_failure" {
				if pg {
					mustExec(t, s.st, `CREATE FUNCTION fixture_secret_uninstall_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture uninstall failure'; END $$`)
					mustExec(t, s.st, `CREATE TRIGGER fixture_secret_uninstall_failure BEFORE INSERT ON app_platform_operations FOR EACH ROW EXECUTE FUNCTION fixture_secret_uninstall_failure()`)
					t.Cleanup(func() {
						mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_secret_uninstall_failure ON app_platform_operations`)
						mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_secret_uninstall_failure()`)
					})
				} else {
					mustExec(t, s.st, `CREATE TRIGGER fixture_secret_uninstall_failure BEFORE INSERT ON app_platform_operations BEGIN SELECT RAISE(ABORT,'fixture uninstall failure');END`)
				}
			}
			before := dumpOf(t, s.st)
			if _, err := s.consumePlatformRemovalReview(actor, plan.ID, plan.SHA256, platformFixtureID(2082)); err == nil {
				t.Fatal("failed uninstall committed")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("failed uninstall left partial authority or binding changes")
			}
		})
	}
}
func TestPlatformSecretUninstallRollbackSQLite(t *testing.T) {
	testPlatformSecretUninstallRollback(t, false)
}
func TestPlatformSecretUninstallRollbackPostgres(t *testing.T) {
	testPlatformSecretUninstallRollback(t, true)
}
