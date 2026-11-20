package appplatform

import "testing"

func TestSetDataQuotaIsOneEffectOnAManagedInstallation(t *testing.T) {
	stages := operationStages(ActionSetDataQuota, true)
	want := []Stage{StageQueued, StageValidate, StagePrepareData, StageComplete}
	if len(stages) != len(want) {
		t.Fatalf("stages %v", stages)
	}
	for i := range want {
		if stages[i] != want[i] {
			t.Fatalf("stages %v", stages)
		}
	}
	if len(operationStages(ActionSetDataQuota, false)) != 0 {
		t.Fatal("a frontend installation has no data quota to raise")
	}
	op, err := NewOperation(ActionSetDataQuota, true, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []Stage{StageValidate, StagePrepareData, StageComplete} {
		if op, err = op.Advance(op.Snapshot().Revision, next); err != nil {
			t.Fatalf("advance to %s: %v", next, err)
		}
	}
	if _, err := OperationFromSnapshot(op.Snapshot()); err != nil {
		t.Fatal(err)
	}
}

func TestImportDataHasTheImportStageBetweenSnapshotAndStart(t *testing.T) {
	want := []Stage{StageQueued, StageValidate, StageQuiesce, StageSnapshot, StageImport, StageStart, StageProbe, StageActivate, StageComplete}
	stages := operationStages(ActionImportData, true)
	if len(stages) != len(want) {
		t.Fatalf("stages %v", stages)
	}
	for i := range want {
		if stages[i] != want[i] {
			t.Fatalf("stages %v", stages)
		}
	}
	if len(operationStages(ActionImportData, false)) != 0 {
		t.Fatal("a frontend installation has no namespace to import into")
	}
	op, _ := NewOperation(ActionImportData, true, 4)
	for _, next := range want[1:] {
		var err error
		if op, err = op.Advance(op.Snapshot().Revision, next); err != nil {
			t.Fatalf("advance to %s: %v", next, err)
		}
	}
	// An import cannot skip its import stage.
	skip, _ := NewOperation(ActionImportData, true, 4)
	for _, next := range []Stage{StageValidate, StageQuiesce, StageSnapshot} {
		skip, _ = skip.Advance(skip.Snapshot().Revision, next)
	}
	if _, err := skip.Advance(skip.Snapshot().Revision, StageStart); err == nil {
		t.Fatal("an import went from its checkpoint to its start")
	}
	// A failed import that ended at its start is a state an operation restores; the start can be proven never to have run.
	failed, _ := NewOperation(ActionImportData, true, 4)
	for _, next := range want[1:6] {
		failed, _ = failed.Advance(failed.Snapshot().Revision, next)
	}
	failed, err := failed.Fail(failed.Snapshot().Revision, EffectsUnknown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OperationFromSnapshot(failed.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if _, err := failed.SettleStartNeverRan(failed.Snapshot().Revision); err != nil {
		t.Fatalf("an import whose start never ran cannot be settled: %v", err)
	}
}
