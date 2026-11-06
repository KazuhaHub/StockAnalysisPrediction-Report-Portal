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
