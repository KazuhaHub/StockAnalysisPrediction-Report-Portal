package appplatform

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// The web console lists an operation's stages from a copy of operationStages. This file is that copy, written by this
// test and read by the console's own test, so a stage added or reordered here fails CI until the console follows.
// Regenerate with: UPDATE_OPERATION_STAGES=1 go test ./internal/appplatform -run TestOperationStagesMatchTheWebContract
const operationStagesContract = "../../web/src/lib/operation-stages.generated.json"

func TestOperationStagesMatchTheWebContract(t *testing.T) {
	actions := []Action{ActionInstall, ActionUpdate, ActionDisable, ActionEnable, ActionRepair, ActionUninstall, ActionDeleteData, ActionRestore, ActionSetDataQuota, ActionImportData, ActionRestoreImport}
	type lists struct {
		Managed   []Stage `json:"managed"`
		Unmanaged []Stage `json:"unmanaged"`
	}
	contract := map[Action]lists{}
	for _, action := range actions {
		managed, unmanaged := operationStages(action, true), operationStages(action, false)
		if managed == nil {
			managed = []Stage{}
		}
		if unmanaged == nil {
			unmanaged = []Stage{}
		}
		contract[action] = lists{Managed: managed, Unmanaged: unmanaged}
	}
	want, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("UPDATE_OPERATION_STAGES") == "1" {
		if err := os.WriteFile(operationStagesContract, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	have, err := os.ReadFile(operationStagesContract)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(have, want) {
		t.Fatal("web/src/lib/operation-stages.generated.json is out of date; regenerate it (see the comment above) and update web/src/pages/manage/operationSteps.ts")
	}
}
