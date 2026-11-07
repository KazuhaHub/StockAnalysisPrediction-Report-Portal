package transport

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// restoreStartTransportFixture is the update-start fixture re-signed as a restore-start.
func restoreStartTransportFixture(t *testing.T) (control.Authority, control.UpdateStartCommand, []byte, []byte, []byte) {
	t.Helper()
	a, command, _, _, descriptor, _ := updateStartTransportFixture(t)
	command.Kind = "restore-start"
	command.OperationID = "11111111-1111-4111-8111-111111119a70"
	var err error
	if command.PayloadSHA256, err = control.UpdateStartLogicalDigest(command); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, signature, err := control.SignUpdateStart(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, now)
	if err != nil {
		t.Fatal(err)
	}
	return a, command, raw, signature, descriptor
}

func updateStartAbsenceFor(command control.UpdateStartCommand) journal.UpdateStartAbsence {
	return journal.UpdateStartAbsence{
		Format: "portal.runner.update-start-absence/1-dev.1", DeploymentID: command.DeploymentID,
		ControllerEpoch: command.ControllerEpoch, InstallationID: command.InstallationID,
		OperationID: command.OperationID, TargetGeneration: command.TargetGeneration, Stage: command.Stage,
		EffectNumber: command.EffectNumber, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
		State: "absent", ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
}

func TestWritableLifecycleRestoreStartAbsenceReply(t *testing.T) {
	a, command, raw, sig, descriptor := restoreStartTransportFixture(t)
	now := time.Now().UnixMilli()
	started, received := time.Now().Add(-time.Second), time.Now().Add(time.Second)
	read, err := verifyWritableLifecycle(UpdateStartReceipt, raw, lifecycleSignature(t, UpdateStartReceipt, a, raw, sig), descriptor, a, now)
	if err != nil {
		t.Fatal(err)
	}
	execute, err := verifyWritableLifecycle(UpdateStartExecute, raw, sig, descriptor, a, now)
	if err != nil {
		t.Fatal(err)
	}
	absence := updateStartAbsenceFor(command)
	body, _ := json.Marshal(absence)
	if !validWritableLifecycleReply(UpdateStartReceipt, body, read, started, received) || !startAbsenceReply(UpdateStartReceipt, body) {
		t.Fatal("exact absence reply refused for a receipt read")
	}
	if validWritableLifecycleReply(UpdateStartExecute, body, execute, started, received) || startAbsenceReply(UpdateStartExecute, body) {
		t.Fatal("an execute may never be answered with an absence")
	}
	if startAbsenceReply(UpdateStartReserve, body) || startAbsenceReply(UpdateStartRead, body) {
		t.Fatal("only the receipt read may be answered with an absence")
	}
	for name, mutate := range map[string]func(*journal.UpdateStartAbsence){
		"effect":       func(v *journal.UpdateStartAbsence) { v.EffectID = "11111111-1111-4111-8111-111111119a64" },
		"operation":    func(v *journal.UpdateStartAbsence) { v.OperationID = "11111111-1111-4111-8111-111111119a65" },
		"installation": func(v *journal.UpdateStartAbsence) { v.InstallationID = "11111111-1111-4111-8111-111111119a66" },
		"number":       func(v *journal.UpdateStartAbsence) { v.EffectNumber = "2" },
		"stage":        func(v *journal.UpdateStartAbsence) { v.Stage = "probe" },
		"payload":      func(v *journal.UpdateStartAbsence) { v.PayloadSHA256 = strings.Repeat("f", 64) },
		"generation":   func(v *journal.UpdateStartAbsence) { v.TargetGeneration = "9" },
		"state":        func(v *journal.UpdateStartAbsence) { v.State = "running" },
		"future": func(v *journal.UpdateStartAbsence) {
			v.ObservedMS = strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
		},
		"before issue": func(v *journal.UpdateStartAbsence) { v.ObservedMS = "1" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := absence
			mutate(&changed)
			encoded, _ := json.Marshal(changed)
			if validWritableLifecycleReply(UpdateStartReceipt, encoded, read, started, received) {
				t.Fatal("changed absence reply accepted")
			}
		})
	}
	// An update-start is answered with an absence too, bound to its own effect; other start kinds are not.
	_, updateCommand, updateRaw, updateSig, updateDescriptor, _ := updateStartTransportFixture(t)
	updateRead, err := verifyWritableLifecycle(UpdateStartReceipt, updateRaw, lifecycleSignature(t, UpdateStartReceipt, a, updateRaw, updateSig), updateDescriptor, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	updateBody, _ := json.Marshal(updateStartAbsenceFor(updateCommand))
	if !validWritableLifecycleReply(UpdateStartReceipt, updateBody, updateRead, started, received) {
		t.Fatal("an update-start receipt read was not answered with an absence")
	}
	if validWritableLifecycleReply(UpdateStartReceipt, body, updateRead, started, received) {
		t.Fatal("another start's absence answered an update-start read")
	}
}
