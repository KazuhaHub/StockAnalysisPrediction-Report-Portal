package journal

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func deleteDataCommandFixture(t *testing.T) control.DeleteDataCommand {
	t.Helper()
	command := control.DeleteDataCommand{
		Format:           control.DeleteDataFormat,
		DeploymentID:     fixtureID(920),
		ControllerEpoch:  fixtureID(921),
		ControlRevision:  "1",
		InstallationID:   fixtureID(922),
		OperationID:      fixtureID(923),
		TargetGeneration: "2",
		Stage:            "remove_data", EffectNumber: "1",
		EffectID: fixtureID(924), Kind: "delete-data",
		InstallationFence: "2",
		Payload: control.DeleteDataPayload{
			NamespaceID:   fixtureID(925),
			ReleaseSHA256: strings.Repeat("a", 64),
			ProjectID:     "4204", DataFormat: "1",
			QuotaBytes: "8388608", QuotaInodes: "128",
			ExpectedDevice: "7", ExpectedNamespaceInode: "11",
			WriterFence: "2",
		},
	}
	digest, err := control.DeleteDataLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	return command
}

func deleteDataReceiptFixture(
	command control.DeleteDataCommand,
) DeleteDataReceipt {
	return DeleteDataReceipt{
		Format:                 deleteDataReceiptFormat,
		DeploymentID:           command.DeploymentID,
		ControllerEpoch:        command.ControllerEpoch,
		InstallationID:         command.InstallationID,
		OperationID:            command.OperationID,
		TargetGeneration:       command.TargetGeneration,
		Stage:                  command.Stage,
		EffectID:               command.EffectID,
		PayloadSHA256:          command.PayloadSHA256,
		NamespaceID:            command.Payload.NamespaceID,
		ReleaseSHA256:          command.Payload.ReleaseSHA256,
		DataReceiptSHA256:      strings.Repeat("b", 64),
		OwnershipSHA256:        strings.Repeat("c", 64),
		ProjectID:              command.Payload.ProjectID,
		DataFormat:             command.Payload.DataFormat,
		QuotaBytes:             command.Payload.QuotaBytes,
		QuotaInodes:            command.Payload.QuotaInodes,
		ExpectedDevice:         command.Payload.ExpectedDevice,
		ExpectedNamespaceInode: command.Payload.ExpectedNamespaceInode,
		WriterFence:            command.Payload.WriterFence,
		Entries:                "4",
		UsedBytes:              "8192",
		UsedInodes:             "5",
		NamespaceDevice:        command.Payload.ExpectedDevice,
		NamespaceInode:         command.Payload.ExpectedNamespaceInode,
		ObservedMS:             "100",
		SettledMS:              "101",
	}
}

func TestDeleteDataReceiptRoundTripAndBindings(t *testing.T) {
	command := deleteDataCommandFixture(t)
	receipt := deleteDataReceiptFixture(command)
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseDeleteDataReceipt(raw)
	if err != nil || parsed != receipt ||
		!deleteDataReceiptMatchesCommand(parsed, command) {
		t.Fatal("delete-data receipt round trip", parsed, err)
	}
	for name, mutate := range map[string]func(*DeleteDataReceipt){
		"deployment": func(v *DeleteDataReceipt) {
			v.DeploymentID = fixtureID(930)
		},
		"operation": func(v *DeleteDataReceipt) {
			v.OperationID = fixtureID(931)
		},
		"effect": func(v *DeleteDataReceipt) {
			v.EffectID = fixtureID(932)
		},
		"namespace": func(v *DeleteDataReceipt) {
			v.NamespaceID = fixtureID(933)
		},
		"release": func(v *DeleteDataReceipt) {
			v.ReleaseSHA256 = strings.Repeat("d", 64)
		},
		"project": func(v *DeleteDataReceipt) {
			v.ProjectID = "4205"
		},
		"quota": func(v *DeleteDataReceipt) {
			v.QuotaBytes = strconv.Itoa(9 << 20)
		},
		"writer": func(v *DeleteDataReceipt) {
			v.WriterFence = "3"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			if changed == receipt {
				t.Fatal("receipt mutation was not applied")
			}
			if deleteDataReceiptMatchesCommand(changed, command) {
				t.Fatal("changed delete-data receipt matched command")
			}
		})
	}
	invalid := receipt
	invalid.AlreadyAbsent = true
	encoded, _ := json.Marshal(invalid)
	if _, err := ParseDeleteDataReceipt(encoded); err == nil {
		t.Fatal("absent receipt kept namespace inode identity")
	}
	invalid = receipt
	invalid.SettledMS = "99"
	encoded, _ = json.Marshal(invalid)
	if _, err := ParseDeleteDataReceipt(encoded); err == nil {
		t.Fatal("receipt time moved backwards")
	}
}
