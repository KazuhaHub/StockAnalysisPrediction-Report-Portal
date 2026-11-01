package control

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func deleteDataFixture(t *testing.T) (DeleteDataCommand, []byte, Authority, int64) {
	t.Helper()
	_, key, authority, now := updateStartFixture(t)
	command := DeleteDataCommand{
		Format:           DeleteDataFormat,
		DeploymentID:     authority.DeploymentID,
		ControllerEpoch:  authority.ControllerEpoch,
		ControlRevision:  strconv.FormatInt(authority.ControlRevision, 10),
		InstallationID:   "d0000000-0000-4000-8000-000000000001",
		OperationID:      "d0000000-0000-4000-8000-000000000002",
		TargetGeneration: "3",
		Stage:            "remove_data", EffectNumber: "1",
		EffectID: "d0000000-0000-4000-8000-000000000003",
		Kind:     "remove-data", InstallationFence: "2",
		IssuedMS:  strconv.FormatInt(now, 10),
		ExpiresMS: strconv.FormatInt(now+25000, 10),
		Payload: DeleteDataPayload{
			NamespaceID:   "d0000000-0000-4000-8000-000000000004",
			ReleaseSHA256: strings.Repeat("a", 64),
			ProjectID:     "41001", DataFormat: "1",
			QuotaBytes: "268435456", QuotaInodes: "65536",
			ExpectedDevice:         "66310",
			ExpectedNamespaceInode: "987654321",
			WriterFence:            "4",
		},
	}
	var err error
	command.PayloadSHA256, err = DeleteDataLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	return command, key, authority, now
}

func TestDeleteDataRoundTrip(t *testing.T) {
	command, key, authority, now := deleteDataFixture(t)
	raw, signature, err := SignDeleteData(
		command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyDeleteData(
		raw, signature, authority, now)
	if err != nil || verified.Command().Payload.NamespaceID !=
		command.Payload.NamespaceID {
		t.Fatal("delete-data round trip", err)
	}
}

func TestDeleteDataRefusesChangedBindings(t *testing.T) {
	command, key, authority, now := deleteDataFixture(t)
	raw, signature, err := SignDeleteData(
		command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*DeleteDataCommand){
		"namespace": func(c *DeleteDataCommand) {
			c.Payload.NamespaceID =
				"d0000000-0000-4000-8000-000000000005"
		},
		"project": func(c *DeleteDataCommand) {
			c.Payload.ProjectID = "41002"
		},
		"device": func(c *DeleteDataCommand) {
			c.Payload.ExpectedDevice = "66311"
		},
		"inode": func(c *DeleteDataCommand) {
			c.Payload.ExpectedNamespaceInode = "987654322"
		},
		"quota": func(c *DeleteDataCommand) {
			c.Payload.QuotaBytes = "268435968"
		},
		"writer": func(c *DeleteDataCommand) {
			c.Payload.WriterFence = "5"
		},
		"generation": func(c *DeleteDataCommand) {
			c.TargetGeneration = "4"
		},
		"fence": func(c *DeleteDataCommand) {
			c.InstallationFence = "3"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			changedRaw, _ := json.Marshal(changed)
			if _, err := VerifyDeleteData(
				changedRaw, signature, authority, now); err == nil {
				t.Fatal("changed delete-data command accepted")
			}
		})
	}
	if _, err := VerifyDeleteData(
		raw, signature, authority, now+35000); err == nil {
		t.Fatal("expired delete-data command accepted")
	}
	changedRaw := bytes.Clone(raw)
	changedRaw[0] ^= 1
	if _, err := VerifyDeleteData(
		changedRaw, signature, authority, now); err == nil {
		t.Fatal("changed delete-data bytes accepted")
	}
}
