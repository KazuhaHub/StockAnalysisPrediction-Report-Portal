package control

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func updateStartRemovalFixture(t *testing.T) (UpdateStartRemovalCommand, []byte, []byte, Authority, int64) {
	t.Helper()
	start, key, authority, now := updateStartFixture(t)
	startRaw, startSignature, err := SignUpdateStart(
		start, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	command := UpdateStartRemovalCommand{
		Format:      UpdateStartRemovalFormat,
		EffectID:    "c1000000-0000-4000-8000-000000000000",
		OperationID: "c2000000-0000-4000-8000-000000000000",
		Stage:       "stop", InstallationFence: start.InstallationFence,
		StartRaw: startRaw, StartSignature: startSignature,
		RunningReceiptSHA256: strings.Repeat("a", 64),
	}
	command.PayloadSHA256, err = UpdateStartRemovalLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, signature, err := SignUpdateStartRemoval(
		command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return command, raw, signature, authority, now
}

func TestUpdateStartRemovalRoundTripAndBinding(t *testing.T) {
	command, raw, signature, authority, now :=
		updateStartRemovalFixture(t)
	verified, err := VerifyUpdateStartRemoval(
		raw, signature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Start().Command().EffectID !=
		commandStartEffectForTest(t, command.StartRaw) ||
		verified.Command().OperationID ==
			verified.Start().Command().OperationID {
		t.Fatal("removal lost source or outer operation binding")
	}
	for name, mutate := range map[string]func(*UpdateStartRemovalCommand){
		"stage": func(c *UpdateStartRemovalCommand) {
			c.Stage = "remove_code"
		},
		"operation": func(c *UpdateStartRemovalCommand) {
			c.OperationID = "c3000000-0000-4000-8000-000000000000"
		},
		"fence": func(c *UpdateStartRemovalCommand) {
			c.InstallationFence = "3"
		},
		"running": func(c *UpdateStartRemovalCommand) {
			c.RunningReceiptSHA256 = strings.Repeat("b", 64)
		},
		"start": func(c *UpdateStartRemovalCommand) {
			c.StartRaw[0] ^= 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			changed.StartRaw = bytes.Clone(command.StartRaw)
			changed.StartSignature = bytes.Clone(command.StartSignature)
			mutate(&changed)
			changedRaw, _ := json.Marshal(changed)
			if _, err := VerifyUpdateStartRemoval(
				changedRaw, signature, authority, now); err == nil {
				t.Fatal("changed removal command accepted")
			}
		})
	}
}

func commandStartEffectForTest(t *testing.T, raw []byte) string {
	t.Helper()
	command, err := parseUpdateStart(raw)
	if err != nil {
		t.Fatal(err)
	}
	return command.EffectID
}
