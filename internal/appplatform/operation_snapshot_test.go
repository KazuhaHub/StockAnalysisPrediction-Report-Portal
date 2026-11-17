package appplatform

import (
	"errors"
	"testing"
)

func TestOperationSnapshotHydration(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, action := range []Action{ActionInstall, ActionUpdate, ActionDisable, ActionEnable, ActionRepair, ActionUninstall, ActionDeleteData, ActionRestore} {
			op, err := NewOperation(action, managed, 7)
			if err != nil {
				t.Fatal(err)
			}
			for index, stage := range operationStages(action, managed) {
				if index != 0 {
					op, err = op.Advance(op.Snapshot().Revision, stage)
					if err != nil {
						t.Fatal(err)
					}
				}
				got, err := OperationFromSnapshot(op.Snapshot())
				if err != nil || got.Snapshot() != op.Snapshot() {
					t.Fatalf("hydrate %s/%t/%s: %+v %v", action, managed, stage, got.Snapshot(), err)
				}
				if stage != StageComplete {
					for _, outcome := range []EffectOutcome{EffectsNone, EffectsReverted, EffectsUnknown} {
						failed, err := op.Fail(op.Snapshot().Revision, outcome)
						if err != nil {
							t.Fatal(err)
						}
						got, err := OperationFromSnapshot(failed.Snapshot())
						if err != nil || got.Snapshot() != failed.Snapshot() {
							t.Fatalf("hydrate failure: %v", err)
						}
					}
				}
			}
		}
	}
}

func TestOperationSnapshotRefusesImpossibleState(t *testing.T) {
	for _, snapshot := range []OperationSnapshot{
		{Action: ActionDisable, Generation: 1, Revision: 0, Stage: StageQueued},
		{Action: ActionDisable, Generation: 0, Revision: 1, Stage: StageQueued},
		{Action: "future", Generation: 1, Revision: 1, Stage: StageQueued},
		{Action: ActionDisable, Generation: 1, Revision: 2, Stage: StageQueued},
		{Action: ActionDisable, Generation: 1, Revision: 1, Stage: StageComplete},
		{Action: ActionDisable, Generation: 1, Revision: 4, Stage: StageStart},
		{Action: ActionDisable, Generation: 1, Revision: 1, Stage: StageFailed},
		{Action: ActionDisable, Generation: 1, Revision: 99, Stage: StageRecoveryRequired},
		{Action: ActionDisable, Generation: 1, Revision: 1, Stage: "future"},
	} {
		if _, err := OperationFromSnapshot(snapshot); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("accepted impossible snapshot %+v: %v", snapshot, err)
		}
	}
}
