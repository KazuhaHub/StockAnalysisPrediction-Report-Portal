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
	for _, stage := range []Stage{StageValidate, StageStart, StageProbe, StageActivate, StageComplete} {
		op, err = op.Advance(op.Snapshot().Revision, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestUninstallRevokesBeforeStoppingAndKeepsData(t *testing.T) {
	op, err := NewOperation(ActionUninstall, true, 9)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []Stage{StageValidate, StageRevoke, StageStop, StageRemoveCode, StageComplete} {
		if _, err := op.Advance(op.Snapshot().Revision, StageRemoveData); !errors.Is(err, ErrInvalidTransition) {
			t.Fatal("uninstall admitted data deletion")
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
