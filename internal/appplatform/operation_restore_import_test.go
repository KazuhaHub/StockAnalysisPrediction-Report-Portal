package appplatform

import "testing"

// A restore import imports one checkpoint onto the replacement runtime: nothing is stopped, started
// or activated, so it has exactly the validate and restore-data stages between queue and completion.
func TestRestoreImportOperationStages(t *testing.T) {
	op, err := NewOperation(ActionRestoreImport, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []Stage{StageValidate, StageRestoreData, StageComplete} {
		if op, err = op.Advance(op.Snapshot().Revision, next); err != nil {
			t.Fatalf("advance to %s: %v", next, err)
		}
	}
	skip, _ := NewOperation(ActionRestoreImport, true, 2)
	if _, err := skip.Advance(1, StageRestoreData); err == nil {
		t.Fatal("validate was skipped")
	}
	started, _ := NewOperation(ActionRestoreImport, true, 2)
	started, _ = started.Advance(1, StageValidate)
	if _, err := started.Advance(2, StageStart); err == nil {
		t.Fatal("a restore import reached start")
	}
	failing, _ := NewOperation(ActionRestoreImport, true, 2)
	failing, _ = failing.Advance(1, StageValidate)
	failing, _ = failing.Advance(2, StageRestoreData)
	if unknown, err := failing.Fail(3, EffectsUnknown); err != nil || unknown.Snapshot().Stage != StageRecoveryRequired {
		t.Fatal("an unknown import effect must require recovery", err)
	}
}
