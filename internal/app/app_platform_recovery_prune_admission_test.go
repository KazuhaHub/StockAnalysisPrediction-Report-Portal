package app

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
)

// platformInsertDeleteCheckpointRow records a delete_checkpoint effect that names a recovery set, in the state given,
// on an operation. The receipt is the shape the driver writes before the runner answers.
func platformInsertDeleteCheckpointRow(t *testing.T, s *Server, operation, set, state string, number int) string {
	t.Helper()
	id := platformDerivedUUID("test-delete-checkpoint", operation+"/"+set+"/"+state+"/"+strconv.Itoa(number))
	receipt := `{"recovery_set_id":"` + set + `","checkpoint_id":"` + platformFixtureID(42) + `","source":"retention","attempt":0}`
	mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT id,'remove_data',?,?,'delete_checkpoint',?,NULL,controller_epoch,CASE WHEN fence<1 THEN 1 ELSE fence END,0,?,?,created_ms,updated_ms FROM app_platform_operations WHERE id=?`,
		number, id, platformDigest([]byte(id)), state, receipt, operation)
	return id
}

// Test 17, the bundle writer. Fails if a set with a decided deletion still demands its archive, or if a refused
// deletion (reverted) stops the bundle requiring an archive the runner still holds.
func TestBundleDoesNotRequireTheArchiveOfASetWhoseDeletionIsDecided(t *testing.T) {
	for _, state := range []string{"prepared", "unknown", "applied"} {
		t.Run(state, func(t *testing.T) {
			b := platformBundleFixture(t)
			keep := b.retain(t, 1, "verified", b.release, []byte("the archive that stays"))
			gone := b.retain(t, 2, "verified", b.release, []byte("the archive being deleted"))
			platformInsertDeleteCheckpointRow(t, b.s, b.operation, gone, state, 2)
			delete(b.archives, gone)
			dir := t.TempDir()
			manifest, err := b.s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{Archives: b.archives})
			if err != nil {
				t.Fatalf("a bundle was refused for lack of an archive the runner is deleting: %v", err)
			}
			if len(manifest.Checkpoints) != 1 || manifest.Checkpoints[0].RecoverySetID != keep {
				t.Fatalf("the manifest lists %+v, want only the set that stays", manifest.Checkpoints)
			}
			// An archive handed in for the deleted set is refused as for any set that is not retained.
			b.archives[gone] = filepath.Join(b.hostDir, platformFixtureID(7202)+".tar")
			if _, err := b.s.writePlatformRecoveryBundle(t.TempDir(), platformRecoveryBundleInput{Archives: b.archives}); err == nil {
				t.Fatal("an archive was accepted for a set that is not retained")
			}
		})
	}
	t.Run("a refused deletion keeps the set retained", func(t *testing.T) {
		b := platformBundleFixture(t)
		b.retain(t, 1, "verified", b.release, []byte("the archive that stays"))
		refused := b.retain(t, 2, "verified", b.release, []byte("the archive the runner refused to delete"))
		platformInsertDeleteCheckpointRow(t, b.s, b.operation, refused, "reverted", 2)
		delete(b.archives, refused)
		if _, err := b.s.writePlatformRecoveryBundle(t.TempDir(), platformRecoveryBundleInput{Archives: b.archives}); err == nil {
			t.Fatal("a bundle was written without the archive of a set the runner still holds")
		}
	})
}

// Test 18. Fails if a deletion that commits between the bundle's before-read and after-read lets the bundle ship: the
// dump was taken with the set retained, so the bundle would promise an archive that is gone.
func TestBundleWrittenWhileADeletionCommitsRefuses(t *testing.T) {
	b := platformBundleFixture(t)
	b.retain(t, 1, "verified", b.release, []byte("the archive that stays"))
	victim := b.retain(t, 2, "verified", b.release, []byte("the archive deleted during the write"))
	platformTestBundleBeforeRecheck = func() {
		platformInsertDeleteCheckpointRow(t, b.s, b.operation, victim, "prepared", 2)
	}
	defer func() { platformTestBundleBeforeRecheck = nil }()
	dir := t.TempDir()
	_, err := b.s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{Archives: b.archives})
	if !errors.Is(err, errPlatformBundle) {
		t.Fatalf("a bundle was written across a deletion decision: %v", err)
	}
	if left := platformBundleFiles(t, dir); len(left) != 0 {
		t.Fatalf("the refused bundle left files behind: %v", left)
	}
}

// Test 17, restore admission. The review is the first admission, the stage's intent the second: each must refuse a
// set with a decided deletion with its existing typed refusal, and admit one whose deletion was refused.
func TestRestoreAdmissionRefusesASetWhoseDeletionIsDecided(t *testing.T) {
	for _, state := range []string{"prepared", "unknown", "applied"} {
		t.Run("review "+state, func(t *testing.T) {
			eachPlatformDriver(t, func(t *testing.T, pg bool) {
				s, actor, update, set := platformManagedRestoreReviewFixture(t, pg)
				platformInsertDeleteCheckpointRow(t, s, update.id, set, state, 2)
				if _, err := s.createPlatformManagedRestoreReview(context.Background(), actor, update.installationID, set); !errors.Is(err, errAppPlatformPlanChanged) {
					t.Fatalf("a restore review was admitted for a set whose checkpoint is being deleted: %v", err)
				}
			})
		})
		t.Run("intent "+state, func(t *testing.T) {
			eachPlatformDriver(t, func(t *testing.T, pg bool) {
				s, lease, _ := platformVerifiedCheckpointUpdateFixture(t, pg)
				set := scalar[string](t, s.st, `SELECT id FROM app_platform_recovery_sets WHERE state='verified'`)
				platformInsertDeleteCheckpointRow(t, s, lease.id, set, state, 2)
				if _, err := s.preparePlatformRestore(context.Background(), lease); !errors.Is(err, errAppPlatformStageEvidence) {
					t.Fatalf("a restore was prepared from a set whose checkpoint is being deleted: %v", err)
				}
				if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data'`, lease.id) != 0 ||
					scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, set) != "verified" {
					t.Fatal("a refused restore left rows behind")
				}
			})
		})
	}
	t.Run("a refused deletion does not block a restore", func(t *testing.T) {
		s, actor, update, set := platformManagedRestoreReviewFixture(t, false)
		platformInsertDeleteCheckpointRow(t, s, update.id, set, "reverted", 2)
		if _, err := s.createPlatformManagedRestoreReview(context.Background(), actor, update.installationID, set); err != nil {
			t.Fatalf("a refused deletion blocked the restore review: %v", err)
		}
	})
}

// Test 17, import-grant issuance and restore-from-import. A decided deletion refuses both with the existing typed
// refusal, and no grant is written.
func TestImportAdmissionRefusesASetWhoseDeletionIsDecided(t *testing.T) {
	for _, state := range []string{"prepared", "unknown", "applied"} {
		t.Run(state, func(t *testing.T) {
			eachPlatformDriver(t, func(t *testing.T, pg bool) {
				f := platformRestoreImportFixture(t, pg)
				review, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet)
				if err != nil {
					t.Fatal("the import review before any deletion:", err)
				}
				platformInsertDeleteCheckpointRow(t, f.s, f.checkpointOperation, f.recoverySet, state, 2)
				if _, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet); err == nil {
					t.Fatal("an import review was admitted for a set whose checkpoint is being deleted")
				}
				if _, err := f.s.consumePlatformRestoreImportReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5200)); !errors.Is(err, errAppPlatformPlanChanged) {
					t.Fatalf("a grant was issued for a set whose checkpoint is being deleted: %v", err)
				}
				if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_import_grants`) != 0 {
					t.Fatal("a refused import left a grant behind")
				}
			})
		})
	}
}

func TestPlatformPruneSelectionExcludesASetWithADecidedDeletionFromRetention(t *testing.T) {
	p := newPruneFixture(t, false)
	a, b, c := p.set(t, "verified", 100), p.set(t, "verified", 200), p.set(t, "verified", 300)
	// With the deletion of the oldest decided it is no longer retained, so the other two are the latest two.
	platformInsertDeleteCheckpointRow(t, p.s, p.hostOp, a, "unknown", 2)
	p.s.st.SetSetting(platformRetentionRecoverySetsKey, "2")
	beyond, err := platformRecoverySetsBeyondRetention(p.s.st.poolExec(), 2)
	if err != nil || len(beyond) != 0 {
		t.Fatalf("beyond = %v, %v (sets %s %s %s)", beyond, err, a, b, c)
	}
	// A refused deletion is retained again.
	mustExec(t, p.s.st, `UPDATE app_platform_operation_effects SET state='reverted' WHERE kind='delete_checkpoint'`)
	if beyond, err = platformRecoverySetsBeyondRetention(p.s.st.poolExec(), 2); err != nil || len(beyond) != 1 || beyond[0] != a {
		t.Fatalf("beyond after the refusal = %v, %v", beyond, err)
	}
}

// Test 22, the retention pin. Fails if the operation holding a completed deletion is swept: its effect would go with
// it, and the next sweep of sets of deleted namespaces would send the same deletion again.
func TestRetentionKeepsTheOperationHoldingACompletedCheckpointDeletion(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		old := retNow - 100*retDay
		removed := f.removedInstallation()
		namespace := f.namespace(removed, "deleted")
		cut, _ := f.operation(removed, "update", "complete", old, 2)
		set := f.recoverySet(removed, namespace, cut, "deleted", 1)
		receiptOf := func(set string) string {
			return `{"recovery_set_id":"` + set + `","outcome":"removed"}`
		}
		hosted := func(state, named string) string {
			op, _ := f.operation(removed, "disable", "complete", old, 3)
			f.effectAt(op, "remove_data", 2, "delete_checkpoint", state, receiptOf(named))
			return op
		}
		applied := hosted("applied", set)
		// A set that no longer exists cannot be selected again, so nothing keeps its deletion.
		orphan := hosted("applied", platformFixtureID(31415))
		// A refused deletion is kept while its set is not deleted (the attempt count), and forgotten after.
		// The live set belongs to another installation: a retained set whose receipt cannot be read pins its own.
		other := f.removedInstallation()
		otherNamespace := f.namespace(other, "deleted")
		otherCut, _ := f.operation(other, "update", "complete", old, 2)
		live := f.recoverySet(other, otherNamespace, otherCut, "verified", 2)
		refusedLive := hosted("reverted", live)
		refusedDeleted := hosted("reverted", set)
		f.sweep(retNow)
		for name, want := range map[string]struct {
			op   string
			kept bool
		}{
			"an applied deletion of an existing set":                  {applied, true},
			"an applied deletion of a set that is gone":               {orphan, false},
			"a refused deletion of a set that is not deleted":         {refusedLive, true},
			"a refused deletion of a set that was deleted afterwards": {refusedDeleted, false},
		} {
			if got := f.exists("operations", want.op); got != want.kept {
				t.Errorf("%s: operation kept=%v, want %v", name, got, want.kept)
			}
		}
	})
}

// A delete_checkpoint effect row whose receipt names no set cannot be attributed, so the operation is kept.
func TestRetentionKeepsTheOperationOfADeletionItCannotAttribute(t *testing.T) {
	f := newRetFixture(t, false)
	removed := f.removedInstallation()
	f.namespace(removed, "deleted")
	op, _ := f.operation(removed, "disable", "complete", retNow-100*retDay, 3)
	f.effectAt(op, "remove_data", 2, "delete_checkpoint", "applied", "not json")
	f.sweep(retNow)
	if !f.exists("operations", op) {
		t.Fatal("the operation of an effect that names no set was swept")
	}
}

// Test 24. The admission transaction takes the locks operation admission takes, in their order: a held trust lock and
// a held OU mutation lock each make the decision wait, and the decision is made once they are released.
func TestPlatformPruneSerializesWithAdmissionOnPostgres(t *testing.T) {
	p := newPruneFixture(t, true)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	hold := func(statement string) func() {
		tx, err := p.s.st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := tx.Commit(); err != nil {
				t.Error(err)
			}
		}
	}
	run := func() {
		if _, err := p.s.prunePlatformRecoverySets(context.Background(), p.endpoint); err != nil {
			t.Error(err)
		}
	}
	t.Run("the OU mutation lock", func(t *testing.T) {
		release := hold(`SELECT pg_advisory_xact_lock(780716)`)
		retBlocked(t, p.s.st, run, release)
		if p.state(t, victim) != "deleted" {
			t.Fatal("the set was not deleted once the lock was released")
		}
	})
	t.Run("the trust lock operation admission takes", func(t *testing.T) {
		again := p.set(t, "verified", 50)
		release := hold(`LOCK TABLE app_platform_publishers,app_platform_publisher_keys IN SHARE ROW EXCLUSIVE MODE`)
		retBlocked(t, p.s.st, run, release)
		if p.state(t, again) != "deleted" {
			t.Fatal("the set was not deleted once the lock was released")
		}
	})
}

// Test 24, the transaction helper itself. The decision (prepareDeletion) and the settlement both run in pruneTx, so a
// held OU mutation lock or trust lock makes either wait, and neither runs before the locks are taken. This needs no
// runtime, so it runs where the full fixture above cannot.
func TestPlatformPruneTransactionTakesTheAdmissionLocksOnPostgres(t *testing.T) {
	f := newRetFixture(t, true)
	hold := func(statement string) func() {
		tx, err := f.st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := tx.Commit(); err != nil {
				t.Error(err)
			}
		}
	}
	for name, statement := range map[string]string{
		"the OU mutation lock":                     `SELECT pg_advisory_xact_lock(780716)`,
		"the trust lock operation admission takes": `LOCK TABLE app_platform_publishers,app_platform_publisher_keys IN SHARE ROW EXCLUSIVE MODE`,
	} {
		t.Run(name, func(t *testing.T) {
			ran := false
			retBlocked(t, f.st, func() {
				if err := f.s.pruneTx(context.Background(), nil, true, func(migExec, int64) error { ran = true; return nil }); err != nil {
					t.Error(err)
				}
			}, hold(statement))
			if !ran {
				t.Fatal("the transaction did not run once the lock was released")
			}
		})
	}
}

// The SQL of the pruning step and its predicates, on both drivers and with no runtime: the decided-deletion condition
// (a LIKE over the receipt with a concatenated pattern), the retained-set predicate built from it, the leak selection,
// the effect history and the effect-number allocation. The driver tests above need a full runtime fixture.
func TestPlatformPruneQueriesOnBothDrivers(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		removed := f.removedInstallation()
		namespace := f.namespace(removed, "deleted")
		cut, _ := f.operation(removed, "update", "complete", retNow-1*retDay, 2)
		decided := f.recoverySet(removed, namespace, cut, "verified", 1)
		plain := f.recoverySet(removed, namespace, cut, "verified", 2)
		leaked := f.recoverySet(removed, namespace, cut, "deleted", 3)
		host, _ := f.operation(removed, "disable", "complete", retNow-1*retDay, 3)
		named := func(set string) string {
			return `{"recovery_set_id":"` + set + `","checkpoint_id":"` + platformFixtureID(42) + `","source":"retention","attempt":0}`
		}
		deletion := f.effectAt(host, "remove_data", 2, "delete_checkpoint", "prepared", named(decided))
		e := f.st.poolExec()
		if yes, err := platformRecoverySetDeletionDecided(e, decided); err != nil || !yes {
			t.Fatalf("a set with a prepared deletion is not decided: %v %v", yes, err)
		}
		if yes, err := platformRecoverySetDeletionDecided(e, plain); err != nil || yes {
			t.Fatalf("a set with no deletion is decided: %v %v", yes, err)
		}
		retained, err := loadPlatformRetainedSets(e)
		if err != nil || len(retained) != 1 || retained[0].id != plain {
			t.Fatalf("retained = %+v, %v", retained, err)
		}
		mustExec(t, f.st, `UPDATE app_platform_operation_effects SET state='reverted' WHERE effect_id=?`, deletion)
		if retained, err = loadPlatformRetainedSets(e); err != nil || len(retained) != 2 {
			t.Fatalf("a refused deletion changed the retained sets: %+v, %v", retained, err)
		}
		leak, err := platformDeletedNamespaceSets(e)
		if err != nil || len(leak) != 1 || leak[0].id != leaked {
			t.Fatalf("the leak selection = %+v, %v", leak, err)
		}
		history, open, err := platformPruneHistory(e)
		if err != nil || len(open) != 0 || len(history[decided]) != 1 || history[decided][0].effectID != deletion || history[decided][0].intent.Source != "retention" {
			t.Fatalf("history = %+v open = %+v, %v", history, open, err)
		}
		number, free, err := platformPruneNumber(e, host)
		if err != nil || !free || number != 3 {
			t.Fatalf("the next effect number = %d %v %v, want 3", number, free, err)
		}
		if beyond, err := platformBeyondRetention(e, 1); err != nil || len(beyond) != 1 || beyond[0].id != decided {
			t.Fatalf("beyond = %+v, %v", beyond, err)
		}
	})
}
