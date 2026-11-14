package appplatform

import (
	"errors"
	"testing"
)

func TestOperationRequiresOrderedStagesAndCurrentRevision(t *testing.T) {
	op, err := NewOperation(ActionUpdate, true, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Advance(0, StageValidate); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision admitted: %v", err)
	}
	if _, err := op.Advance(1, StageActivate); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("skipped migration/readiness: %v", err)
	}
	for _, stage := range []Stage{StageValidate, StageDownload, StageStage, StageQuiesce, StageSnapshot, StageMigrate, StageStart, StageProbe, StageActivate, StageComplete} {
		prior := op.Snapshot()
		op, err = op.Advance(prior.Revision, stage)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if op.Snapshot().Revision != prior.Revision+1 || op.Snapshot().Generation != 4 {
			t.Fatal("revision or generation lost")
		}
		if _, err := op.Advance(op.Snapshot().Revision, stage); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("duplicate stage repeated: %v", err)
		}
	}
	if _, err := op.Fail(op.Snapshot().Revision, EffectsUnknown); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("completed operation changed: %v", err)
	}
}

func TestStaticInstallationNeverNeedsRuntimeStages(t *testing.T) {
	op, err := NewOperation(ActionInstall, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []Stage{StageValidate, StageDownload, StageStage, StagePrepareData, StageActivate, StageComplete} {
		op, err = op.Advance(op.Snapshot().Revision, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	if op.Snapshot().Stage != StageComplete {
		t.Fatal("static installation did not complete")
	}
}

func TestRestoreFencesAndSnapshotsBeforeReplacingData(t *testing.T) {
	op, err := NewOperation(ActionRestore, true, 12)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []Stage{StageValidate, StageRevoke, StageQuiesce, StageSnapshot, StageRestoreData, StageStart, StageProbe, StageActivate, StageComplete} {
		if stage != StageComplete && stage != StageActivate {
			if _, err := op.Advance(op.Snapshot().Revision, StageActivate); !errors.Is(err, ErrInvalidTransition) {
				t.Fatal("restore activated before complete recovery/readiness evidence")
			}
		}
		op, err = op.Advance(op.Snapshot().Revision, stage)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
}

func TestDisableRevokesBeforeStoppingAndEnableChecksReadiness(t *testing.T) {
	op, _ := NewOperation(ActionDisable, true, 3)
	if _, err := op.Advance(1, StageStop); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("disable stopped before authority revocation")
	}
	var err error
	for _, stage := range []Stage{StageValidate, StageRevoke, StageStop, StageComplete} {
		op, err = op.Advance(op.Snapshot().Revision, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	op, _ = NewOperation(ActionEnable, true, 4)
	for _, stage := range []Stage{StageValidate, StageSnapshot, StageStart, StageProbe, StageActivate, StageComplete} {
		op, err = op.Advance(op.Snapshot().Revision, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestUninstallRevokesAndRemovesDataBeforeCompletion(t *testing.T) {
	op, err := NewOperation(ActionUninstall, true, 9)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []Stage{StageValidate, StageRevoke, StageStop, StageRemoveCode, StageRemoveData, StageComplete} {
		if _, err := op.Advance(op.Snapshot().Revision, StageRemoveData); !errors.Is(err, ErrInvalidTransition) {
			if stage != StageRemoveData {
				t.Fatal("uninstall admitted data deletion early")
			}
		}
		op, err = op.Advance(op.Snapshot().Revision, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestUncertainFailureRequiresSeparateRecovery(t *testing.T) {
	for _, effects := range []EffectOutcome{EffectsNone, EffectsReverted, EffectsUnknown} {
		op, err := NewOperation(ActionInstall, true, 1)
		if err != nil {
			t.Fatal(err)
		}
		op, err = op.Fail(op.Snapshot().Revision, effects)
		if err != nil {
			t.Fatal(err)
		}
		want := StageFailed
		if effects == EffectsUnknown {
			want = StageRecoveryRequired
		}
		if op.Snapshot().Stage != want {
			t.Fatalf("%s: got %s", effects, op.Snapshot().Stage)
		}
		if _, err := op.Advance(op.Snapshot().Revision, StageValidate); !errors.Is(err, ErrInvalidTransition) {
			t.Fatal("terminal failure silently restarted")
		}
	}
}

func TestInvalidOperationInputsDoNotCreateWork(t *testing.T) {
	if _, err := NewOperation(Action("exec-shell"), true, 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("unknown action admitted")
	}
	if _, err := NewOperation(ActionInstall, true, 0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("missing generation admitted")
	}
	var zero Operation
	if _, err := zero.Advance(0, StageComplete); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("zero operation completed")
	}
	op, _ := NewOperation(ActionInstall, true, 1)
	if _, err := op.Fail(1, EffectOutcome("assume-success")); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("unknown effect outcome admitted")
	}
}

// A recovery action whose whole effect is one reviewed database transition is recorded as an operation
// that was admitted already complete; its snapshot must be one the model itself accepts.
func TestCompletedOperationIsAModelState(t *testing.T) {
	for _, action := range []Action{ActionDisable, ActionEnable} {
		snapshot, err := CompletedOperation(action, false, 5)
		if err != nil || snapshot.Stage != StageComplete || snapshot.Generation != 5 {
			t.Fatalf("%s: %+v %v", action, snapshot, err)
		}
		if _, err := OperationFromSnapshot(snapshot); err != nil {
			t.Fatalf("%s: the completed snapshot is not a model state: %v", action, err)
		}
	}
	if _, err := CompletedOperation(Action("invented"), false, 5); err == nil {
		t.Fatal("an unknown action produced a completed operation")
	}
	if _, err := CompletedOperation(ActionDisable, false, 0); err == nil {
		t.Fatal("generation zero produced a completed operation")
	}
}

// An operation that ended recovery_required is settled to failed once every effect it left is known to be
// reverted or never applied. Only that one transition leaves recovery_required, and the result must be
// a state the model itself restores from a snapshot, whichever stage the failure came from.
func TestSettleMovesRecoveryRequiredToFailed(t *testing.T) {
	for _, action := range []Action{ActionRestoreImport, ActionRestore} {
		managed := true
		stages := operationStages(action, managed)
		for index := 1; index < len(stages)-1; index++ {
			op, err := NewOperation(action, managed, 3)
			if err != nil {
				t.Fatal(err)
			}
			for step := 1; step <= index; step++ {
				if op, err = op.Advance(op.Snapshot().Revision, stages[step]); err != nil {
					t.Fatal(err)
				}
			}
			unsettled, err := op.Fail(op.Snapshot().Revision, EffectsUnknown)
			if err != nil {
				t.Fatal(err)
			}
			settled, err := unsettled.Settle(unsettled.Snapshot().Revision)
			if err != nil || settled.Snapshot().Stage != StageFailed || settled.Snapshot().Revision != unsettled.Snapshot().Revision+1 {
				t.Fatalf("%s from %s: %+v %v", action, stages[index], settled.Snapshot(), err)
			}
			if _, err := OperationFromSnapshot(settled.Snapshot()); err != nil {
				t.Fatalf("%s from %s: the settled snapshot is not a model state: %v", action, stages[index], err)
			}
			if _, err := unsettled.Settle(unsettled.Snapshot().Revision + 1); !errors.Is(err, ErrRevisionConflict) {
				t.Fatal("a stale settle was accepted")
			}
			if _, err := settled.Settle(settled.Snapshot().Revision); !errors.Is(err, ErrInvalidTransition) {
				t.Fatal("a failed operation was settled again")
			}
		}
	}
	live, _ := NewOperation(ActionRestoreImport, true, 3)
	if _, err := live.Settle(live.Snapshot().Revision); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("an operation still in flight was settled")
	}
}

// An operation that ended recovery_required completes, one revision later, when evidence shows its last
// effect was applied after all. It must be a state the model restores, so an operation that failed
// before it could have done any work (queued) is refused.
func TestRecoverCompletesARecoveryRequiredOperation(t *testing.T) {
	stages := operationStages(ActionRestoreImport, true)
	for index, want := range map[int]bool{0: false, 1: true, 2: true} {
		op, _ := NewOperation(ActionRestoreImport, true, 3)
		var err error
		for step := 1; step <= index; step++ {
			if op, err = op.Advance(op.Snapshot().Revision, stages[step]); err != nil {
				t.Fatal(err)
			}
		}
		unsettled, err := op.Fail(op.Snapshot().Revision, EffectsUnknown)
		if err != nil {
			t.Fatal(err)
		}
		done, err := unsettled.Recover(unsettled.Snapshot().Revision)
		if (err == nil) != want {
			t.Fatalf("recover after a failure at %s: %v", stages[index], err)
		}
		if err != nil {
			continue
		}
		if done.Snapshot().Stage != StageComplete || done.Snapshot().Revision != unsettled.Snapshot().Revision+1 {
			t.Fatalf("recovered: %+v", done.Snapshot())
		}
		if _, err := OperationFromSnapshot(done.Snapshot()); err != nil {
			t.Fatalf("the recovered snapshot is not a model state: %v", err)
		}
		if _, err := unsettled.Recover(unsettled.Snapshot().Revision + 1); !errors.Is(err, ErrRevisionConflict) {
			t.Fatal("a stale recover was accepted")
		}
		if _, err := done.Recover(done.Snapshot().Revision); !errors.Is(err, ErrInvalidTransition) {
			t.Fatal("a completed operation was recovered again")
		}
	}
	failed, _ := NewOperation(ActionRestoreImport, true, 3)
	failed, _ = failed.Fail(failed.Snapshot().Revision, EffectsNone)
	if _, err := failed.Recover(failed.Snapshot().Revision); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("a clean failure was recovered")
	}
}

// An operation that ended recovery_required because the reply to one stage's effect was lost goes on from
// the next stage when evidence shows the effect was applied. The state it returns to is the one Advance
// would have given had the reply arrived, so the model restores it and it runs like any other.
func TestReopenContinuesAfterTheFailedStage(t *testing.T) {
	op, err := NewOperation(ActionRestore, true, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []Stage{StageValidate, StageRevoke, StageQuiesce, StageSnapshot, StageRestoreData, StageStart} {
		if op, err = op.Advance(op.Snapshot().Revision, stage); err != nil {
			t.Fatal(err)
		}
	}
	want, err := op.Advance(op.Snapshot().Revision, StageProbe)
	if err != nil {
		t.Fatal(err)
	}
	unsettled, err := op.Fail(op.Snapshot().Revision, EffectsUnknown)
	if err != nil {
		t.Fatal(err)
	}
	got, err := unsettled.Reopen(unsettled.Snapshot().Revision)
	if err != nil || got.Snapshot() != want.Snapshot() {
		t.Fatalf("reopen: %+v want %+v (%v)", got.Snapshot(), want.Snapshot(), err)
	}
	if _, err := OperationFromSnapshot(got.Snapshot()); err != nil {
		t.Fatalf("the reopened snapshot is not a model state: %v", err)
	}
	if _, err := got.Advance(got.Snapshot().Revision, StageActivate); err != nil {
		t.Fatalf("a reopened operation cannot go on: %v", err)
	}
	if _, err := unsettled.Reopen(unsettled.Snapshot().Revision + 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("a stale reopen was accepted")
	}
	if _, err := got.Reopen(got.Snapshot().Revision); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("an operation in flight was reopened")
	}
	failed, _ := op.Fail(op.Snapshot().Revision, EffectsNone)
	if _, err := failed.Reopen(failed.Snapshot().Revision); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("a clean failure was reopened")
	}
	// The last stage before complete is finished by Recover, not reopened into complete.
	last, _ := NewOperation(ActionRestoreImport, true, 3)
	for _, stage := range []Stage{StageValidate, StageRestoreData} {
		last, _ = last.Advance(last.Snapshot().Revision, stage)
	}
	lastUnsettled, _ := last.Fail(last.Snapshot().Revision, EffectsUnknown)
	if _, err := lastUnsettled.Reopen(lastUnsettled.Snapshot().Revision); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("an operation was reopened into complete")
	}
}

// The states that exist only because an operation was recovered or settled out of recovery_required (a
// failure one revision later, a completion one revision later) belong to the restore actions whose
// recovery paths write them. An ordinary action's loader refuses them, and so do the transitions.
func TestRecoveryStatesAreRestoredOnlyForTheActionsThatWriteThem(t *testing.T) {
	recovery := map[Action]bool{ActionRestoreImport: true, ActionRestore: true}
	for _, action := range []Action{ActionInstall, ActionUpdate, ActionDisable, ActionEnable, ActionRepair, ActionUninstall, ActionDeleteData, ActionRestore, ActionRestoreImport} {
		for _, managed := range []bool{false, true} {
			stages := operationStages(action, managed)
			if len(stages) == 0 {
				continue
			}
			beyond := uint64(len(stages)) + 1
			for _, stage := range []Stage{StageFailed, StageComplete} {
				_, err := OperationFromSnapshot(OperationSnapshot{Action: action, Managed: managed, Generation: 3, Revision: beyond, Stage: stage})
				if (err == nil) != recovery[action] {
					t.Fatalf("%s managed=%v: %s one revision past the last stage restored=%v, want %v", action, managed, stage, err == nil, recovery[action])
				}
			}
			// The model's own transitions cannot reach those states for an ordinary action.
			op, err := NewOperation(action, managed, 3)
			if err != nil {
				t.Fatal(err)
			}
			if op, err = op.Advance(op.Snapshot().Revision, stages[1]); err != nil {
				t.Fatal(err)
			}
			unsettled, err := op.Fail(op.Snapshot().Revision, EffectsUnknown)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := unsettled.Settle(unsettled.Snapshot().Revision); (err == nil) != recovery[action] {
				t.Fatalf("%s managed=%v: settle accepted=%v, want %v", action, managed, err == nil, recovery[action])
			}
		}
	}
}

// The removal of a never-started installation closes its failed install by one explicit transition. It is
// the install action only, it needs recovery_required, and it produces a state the model restores from a snapshot.
// Settle itself stays limited to the restore actions.
func TestSettleNeverStartedClosesOnlyAFailedInstall(t *testing.T) {
	op, err := NewOperation(ActionInstall, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	stages := operationStages(ActionInstall, true)
	for _, stage := range stages[1:] {
		if stage == StageStart {
			break
		}
		if op, err = op.Advance(op.Snapshot().Revision, stage); err != nil {
			t.Fatal(err)
		}
	}
	if op, err = op.Advance(op.Snapshot().Revision, StageStart); err != nil {
		t.Fatal(err)
	}
	unsettled, err := op.Fail(op.Snapshot().Revision, EffectsUnknown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unsettled.Settle(unsettled.Snapshot().Revision); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("Settle accepted an install")
	}
	closed, err := unsettled.SettleNeverStarted(unsettled.Snapshot().Revision)
	if err != nil || closed.Snapshot().Stage != StageFailed || closed.Snapshot().Revision != unsettled.Snapshot().Revision+1 {
		t.Fatalf("install not closed: %+v %v", closed.Snapshot(), err)
	}
	if _, err := OperationFromSnapshot(closed.Snapshot()); err != nil {
		t.Fatalf("closed snapshot is not a model state: %v", err)
	}
	if _, err := unsettled.SettleNeverStarted(unsettled.Snapshot().Revision + 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("stale close accepted")
	}
	if _, err := closed.SettleNeverStarted(closed.Snapshot().Revision); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("closed install closed twice")
	}
	if _, err := op.SettleNeverStarted(op.Snapshot().Revision); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("a running install was closed")
	}
	for _, action := range []Action{ActionUpdate, ActionEnable, ActionRepair, ActionUninstall, ActionDisable, ActionDeleteData, ActionRestore, ActionRestoreImport} {
		other, err := NewOperation(action, true, 3)
		if err != nil {
			t.Fatal(err)
		}
		if other, err = other.Advance(other.Snapshot().Revision, operationStages(action, true)[1]); err != nil {
			t.Fatal(err)
		}
		failed, err := other.Fail(other.Snapshot().Revision, EffectsUnknown)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := failed.SettleNeverStarted(failed.Snapshot().Revision); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("%s was closed as a never-started install", action)
		}
	}
}
