package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func platformSetPause(t *testing.T, st *Store, paused string) {
	t.Helper()
	mustExec(t, st, `UPDATE meta SET v=? WHERE k='app_platform_restore_paused'`, paused)
}

// While the restore pause is set, every mutation keeps its refusal except the recovery admissions.
func testPausedPortalRefusesNonRecoveryMutation(t *testing.T, pg bool) {
	s, session := runtimeRegistrationFixture(t, pg)
	platformSetPause(t, s.st, "1")
	before := dumpOf(t, s.st)
	if _, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400)); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("ordinary runtime enrollment ran while paused: %v", err)
	}
	if _, err := s.revokePlatformRuntimeControl(session, platformFixtureID(5), 1); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("runtime revocation ran while paused: %v", err)
	}
	if err := s.withPlatformTrustTransaction(session, func(migExec, platformActorFacts, platformActorControl) error { return nil }); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("trust transaction ran while paused: %v", err)
	}
	// The recovery transaction is admitted only while paused, and never the other way round.
	ran := false
	err := s.withPlatformRecoveryTransaction(session, func(_ migExec, _ platformActorFacts, control platformActorControl) error {
		ran = control.recovery
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("recovery transaction refused while paused: %v ran=%v", err, ran)
	}
	if string(stripHeader(before)) != string(stripHeader(dumpOf(t, s.st))) {
		t.Fatal("refused mutations changed state")
	}
	platformSetPause(t, s.st, "0")
	if err := s.withPlatformRecoveryTransaction(session, func(migExec, platformActorFacts, platformActorControl) error { return nil }); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("recovery transaction ran on a Portal that is not paused: %v", err)
	}
}

func TestPausedPortalRefusesNonRecoveryMutationSQLite(t *testing.T) {
	testPausedPortalRefusesNonRecoveryMutation(t, false)
}
func TestPausedPortalRefusesNonRecoveryMutationPostgres(t *testing.T) {
	testPausedPortalRefusesNonRecoveryMutation(t, true)
}

func platformSeedQueuedOperation(t *testing.T, st *Store, n int, action string) string {
	t.Helper()
	hash := strings.Repeat("a", 64)
	id := platformFixtureID(3000 + n)
	plan := platformFixtureID(3100 + n)
	platformTestInsert(t, st.poolExec(), "review_plans", map[string]any{
		"id": plan, "installation_id": platformFixtureID(1), "app_id": "fixture-app", "publisher_id": "fixture-publisher", "action": action,
		"source_release_sha256": nil, "target_release_sha256": nil, "installation_revision": 1, "policy_revision": 1, "trust_revision": 1,
		"controller_epoch": platformFixtureID(99), "actor_scope": hash, "username": nil, "account_created": "", "account_generation": 0,
		"authority_fingerprint": hash, "plan_sha256": hash, "plan_json": "{}", "expires_ms": 10000, "consumed_ms": 1, "created_ms": 1, "updated_ms": 1,
	})
	platformTestInsert(t, st.poolExec(), "operations", map[string]any{
		"id": id, "installation_id": platformFixtureID(1), "review_plan_id": plan, "actor_scope": hash, "idempotency_sha256": fmt.Sprintf("%064d", n),
		"request_sha256": hash, "action": action, "managed": 0, "source_generation": 1, "target_generation": 2, "stage": "queued",
		"state_revision": 1, "row_revision": 1, "lease_owner": nil, "lease_until_ms": nil, "fence": 0, "controller_epoch": platformFixtureID(99),
		"error_code": nil, "completed_ms": nil, "created_ms": 1, "updated_ms": 1,
	})
	return id
}

// A restore import is the only operation a paused Portal may lease, and it may not be leased unpaused.
func testPausedLeaseOnlyRestoreImport(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	platformSeedActionFixtures(t, st)
	mustExec(t, st, `UPDATE app_platform_installations SET phase='review_required',desired_state='disabled'`)
	for k, v := range map[string]string{"app_platform_controller_epoch": platformFixtureID(99), "app_platform_restore_paused": "1"} {
		if err := st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		st.exec(`DELETE FROM meta WHERE k IN ('app_platform_controller_epoch','app_platform_restore_paused')`)
	})
	worker := platformFixtureID(3999)
	importID := platformSeedQueuedOperation(t, st, 1, "restore_import")

	if lease, err := st.claimPlatformOperation(importID, worker); err != nil || lease.snapshot.Action != "restore_import" {
		t.Fatalf("paused Portal refused a restore import lease: %v", err)
	}
	mustExec(t, st, `UPDATE app_platform_operations SET completed_ms=2,stage='complete',state_revision=4,lease_owner=NULL,lease_until_ms=NULL WHERE id=?`, importID)
	disableID := platformSeedQueuedOperation(t, st, 2, "disable")
	mustExec(t, st, `UPDATE app_platform_installations SET phase='ready',desired_state='enabled'`)
	if _, err := st.claimPlatformOperation(disableID, worker); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("paused Portal leased a non-recovery operation: %v", err)
	}
	// Unpaused, the roles reverse: the restore import has no standing outside recovery.
	platformSetPause(t, st, "0")
	mustExec(t, st, `UPDATE app_platform_operations SET completed_ms=2,stage='complete',state_revision=4 WHERE id=?`, disableID)
	mustExec(t, st, `UPDATE app_platform_operations SET completed_ms=NULL,stage='queued',state_revision=1 WHERE id=?`, importID)
	mustExec(t, st, `UPDATE app_platform_installations SET phase='review_required',desired_state='disabled'`)
	if _, err := st.claimPlatformOperation(importID, worker); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("an unpaused Portal leased a restore import: %v", err)
	}
}

func TestPausedLeaseOnlyRestoreImportSQLite(t *testing.T) { testPausedLeaseOnlyRestoreImport(t, false) }
func TestPausedLeaseOnlyRestoreImportPostgres(t *testing.T) {
	testPausedLeaseOnlyRestoreImport(t, true)
}

func testEndRecoveryRequiresSettledInstallations(t *testing.T, pg bool) {
	s, session := runtimeRegistrationFixture(t, pg)
	st := s.st
	platformSeedActionFixtures(t, st)
	platformSetPause(t, st, "1")
	mustExec(t, st, `UPDATE app_platform_installations SET phase='review_required',desired_state='disabled'`)
	refused := func(why string) {
		t.Helper()
		if err := s.endPlatformRecovery(session); !errors.Is(err, errAppPlatformRecoveryUnsettled) {
			t.Fatalf("%s: recovery ended (%v)", why, err)
		}
		if scalar[string](t, st, `SELECT v FROM meta WHERE k='app_platform_restore_paused'`) != "1" {
			t.Fatalf("%s: the pause was cleared", why)
		}
	}
	refused("an installation is still awaiting review")
	mustExec(t, st, `UPDATE app_platform_installations SET phase='disabled'`)
	if err := platformSeedInstallation(t, st.poolExec(), platformFixtureID(7), "fixture-publisher", "second-app"); err != nil {
		t.Fatal(err)
	}
	refused("a second installation is unsettled")
	mustExec(t, st, `UPDATE app_platform_installations SET desired_state='removed',phase='removed' WHERE id=?`, platformFixtureID(7))
	op := platformSeedQueuedOperation(t, st, 1, "restore_import")
	refused("a restore import is in flight")
	mustExec(t, st, `UPDATE app_platform_operations SET stage='failed',state_revision=2,completed_ms=2 WHERE id=?`, op)
	mustExec(t, st, `UPDATE app_platform_installations SET phase='ready',desired_state='enabled' WHERE id=?`, platformFixtureID(1))
	if err := s.endPlatformRecovery(session); err != nil {
		t.Fatalf("a settled Portal could not end recovery: %v", err)
	}
	if scalar[string](t, st, `SELECT v FROM meta WHERE k='app_platform_restore_paused'`) != "0" {
		t.Fatal("recovery ended without clearing the pause")
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.recovery_ended'`) != 1 {
		t.Fatal("ending recovery left no audit row")
	}
	if err := s.endPlatformRecovery(session); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("ending recovery twice: %v", err)
	}
}

func TestEndRecoveryRequiresSettledInstallationsSQLite(t *testing.T) {
	testEndRecoveryRequiresSettledInstallations(t, false)
}
func TestEndRecoveryRequiresSettledInstallationsPostgres(t *testing.T) {
	testEndRecoveryRequiresSettledInstallations(t, true)
}
