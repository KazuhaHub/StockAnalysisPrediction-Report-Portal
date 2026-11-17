package app

import (
	"errors"
	"strings"
	"testing"
)

// platformSimulateRestore applies the same pause a real restore leaves behind: runtime bindings
// retired (disabled, no key, no pin), a rotated controller epoch, every installation awaiting review.
func platformSimulateRestore(t *testing.T, s *Server) {
	t.Helper()
	tx, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := applyPlatformRestore(migExec{s: s.st, ex: tx}, platformRestorePrior{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func testReplacementRuntimeAfterRestore(t *testing.T, pg bool) {
	s, actor, _, _ := platformManagedRestoreReviewFixture(t, pg)
	oldID := scalar[string](t, s.st, `SELECT id FROM app_platform_runtime_bindings`)
	platformSimulateRestore(t, s)
	epoch := s.st.GetSetting("app_platform_controller_epoch", "")
	if scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings WHERE id=?`, oldID) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings WHERE sealed_control_key IS NOT NULL`) != 0 {
		t.Fatal("restore did not retire the old binding")
	}
	deployment := platformFixtureID(5100)
	reg, err := s.preparePlatformReplacementRuntime(actor, deployment)
	if err != nil {
		t.Fatal(err)
	}
	if reg.id == oldID || reg.deploymentID != deployment || reg.controllerEpoch != epoch || reg.revision != 1 || len(reg.publicKey) != 32 {
		t.Fatalf("replacement registration: %+v", reg)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings`) != 2 ||
		scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings WHERE id=?`, oldID) != 0 ||
		scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings WHERE id=?`, reg.id) != 0 {
		t.Fatal("replacement preparation enabled a binding or disturbed the retired one")
	}
	detail := scalar[string](t, s.st, `SELECT detail FROM audit_log WHERE action='app_platform.runtime_replacement_prepared' AND target_id=?`, reg.id)
	if !strings.Contains(detail, oldID) || !strings.Contains(detail, deployment) {
		t.Fatalf("replacement audit does not name what it replaces: %s", detail)
	}
	again, err := s.preparePlatformReplacementRuntime(actor, deployment)
	if err != nil || again.id != reg.id || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_replacement_prepared' AND target_id=?`, reg.id) != 1 {
		t.Fatal("replacement preparation is not idempotent", err)
	}
	if _, err := s.preparePlatformReplacementRuntime(actor, platformFixtureID(5101)); !errors.Is(err, errAppPlatformRuntimeConflict) {
		t.Fatalf("a second pending replacement with another deployment was admitted: %v", err)
	}
	// Ordinary enrollment stays refused while paused and never adopts a replacement by itself.
	if _, err := s.preparePlatformRuntimeBinding(actor, deployment); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("ordinary enrollment ran during recovery: %v", err)
	}
	confirmed, err := s.confirmPlatformObservedReplacementRuntime(actor, reg, runtimeObservationFixture(reg))
	if err != nil || confirmed.revision != 2 || scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings WHERE id=?`, reg.id) != 1 {
		t.Fatalf("replacement was not confirmed: %v", err)
	}
	if _, err := s.confirmPlatformObservedRuntime(actor, reg, runtimeObservationFixture(reg)); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("ordinary confirmation ran during recovery: %v", err)
	}
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(s.st.poolExec(), epoch, dek)
	if err != nil || runtime.id != reg.id || runtime.deploymentID != deployment {
		t.Fatalf("data runtime did not select the replacement: %v", err)
	}
	clear(seed)
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_confirmed' AND target_id=?`, reg.id) != 1 {
		t.Fatal("confirmation left no audit row")
	}
}

func TestReplacementRuntimeAfterRestoreSQLite(t *testing.T) {
	testReplacementRuntimeAfterRestore(t, false)
}
func TestReplacementRuntimeAfterRestorePostgres(t *testing.T) {
	testReplacementRuntimeAfterRestore(t, true)
}

func testReplacementRefusedWhenOldBindingLive(t *testing.T, pg bool) {
	for _, kind := range []string{"enabled", "pending", "none"} {
		t.Run(kind, func(t *testing.T) {
			s, session := runtimeRegistrationFixture(t, pg)
			if kind != "none" {
				first, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
				if err != nil {
					t.Fatal(err)
				}
				if kind == "enabled" {
					if _, err := s.confirmPlatformObservedRuntime(session, first, runtimeObservationFixture(first)); err != nil {
						t.Fatal(err)
					}
				}
			}
			platformSetPause(t, s.st, "1")
			before := dumpOf(t, s.st)
			if _, err := s.preparePlatformReplacementRuntime(session, platformFixtureID(5100)); !errors.Is(err, errAppPlatformRuntimeConflict) {
				t.Fatalf("replacement admitted beside a %s binding: %v", kind, err)
			}
			if string(stripHeader(before)) != string(stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused replacement changed state")
			}
		})
	}
}

func TestReplacementRefusedWhenOldBindingLiveSQLite(t *testing.T) {
	testReplacementRefusedWhenOldBindingLive(t, false)
}
func TestReplacementRefusedWhenOldBindingLivePostgres(t *testing.T) {
	testReplacementRefusedWhenOldBindingLive(t, true)
}

func testDataRuntimeIgnoresRetiredBinding(t *testing.T, pg bool) {
	s, session := runtimeRegistrationFixture(t, pg)
	reg, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmPlatformObservedRuntime(session, reg, runtimeObservationFixture(reg)); err != nil {
		t.Fatal(err)
	}
	epoch := s.st.GetSetting("app_platform_controller_epoch", "")
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	retire := func(id string, n int) {
		t.Helper()
		platformTestInsert(t, s.st.poolExec(), "runtime_bindings", map[string]any{
			"id": id, "deployment_id": platformFixtureID(6000 + n), "controller_epoch": epoch, "control_revision": 2, "enabled": 0,
			"verification_key_fingerprint": nil, "sealed_control_key": nil, "revision": 2, "created_ms": 1, "updated_ms": 1,
		})
	}
	// One retired row sorts before the live binding and one after, so no ordering or limit can hide it.
	retire("00000000-0000-4000-8000-000000000001", 1)
	retire("ffffffff-ffff-4fff-8fff-ffffffffffff", 2)
	runtime, seed, err := s.platformDataRuntime(s.st.poolExec(), epoch, dek)
	if err != nil || runtime.id != reg.id {
		t.Fatalf("retired rows hid the live binding: %v", err)
	}
	clear(seed)
	// A second live binding is still an unresolved identity, not a choice.
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=1,revision=2,verification_key_fingerprint=?,sealed_control_key='x' WHERE id=?`,
		strings.Repeat("b", 64), "ffffffff-ffff-4fff-8fff-ffffffffffff")
	if _, _, err := s.platformDataRuntime(s.st.poolExec(), epoch, dek); !errors.Is(err, errAppPlatformRuntimeConflict) {
		t.Fatalf("two live bindings resolved: %v", err)
	}
	// Retired rows alone are no runtime.
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0,sealed_control_key=NULL,verification_key_fingerprint=NULL WHERE id IN (?,?)`, reg.id, "ffffffff-ffff-4fff-8fff-ffffffffffff")
	if _, _, err := s.platformDataRuntime(s.st.poolExec(), epoch, dek); !errors.Is(err, errAppPlatformRuntimeConflict) {
		t.Fatalf("only retired bindings resolved: %v", err)
	}
}

func TestDataRuntimeIgnoresRetiredBindingSQLite(t *testing.T) {
	testDataRuntimeIgnoresRetiredBinding(t, false)
}
func TestDataRuntimeIgnoresRetiredBindingPostgres(t *testing.T) {
	testDataRuntimeIgnoresRetiredBinding(t, true)
}
