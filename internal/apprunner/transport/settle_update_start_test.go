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

func TestSettleUpdateStartActionsAndReceiptBinding(t *testing.T) {
	a, start, startRaw, startSig, descriptor, _ := updateStartTransportFixture(t)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	now := time.Now().UnixMilli()
	startRead, err := control.SignUpdateStartRead(startRaw, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	_ = startSig
	command := control.SettleUpdateStartCommand{
		Format: control.SettleUpdateStartFormat, Mode: control.SettleModeSettle, EffectID: migrateTransportID(60), OperationID: migrateTransportID(61), InstallationFence: start.InstallationFence,
		IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10), StartRaw: startRaw, StartReadSignature: startRead,
	}
	if command.PayloadSHA256, err = control.SettleUpdateStartLogicalDigest(command); err != nil {
		t.Fatal(err)
	}
	raw, sig, err := control.SignSettleUpdateStart(command, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	readSig, err := control.SignSettleUpdateStartRead(raw, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if !SettleUpdateStartExecute.valid() || !SettleUpdateStartRead.valid() || !SettleUpdateStartExecute.mutating() || SettleUpdateStartRead.mutating() {
		t.Fatal("only the execute action may mutate")
	}
	execute, err := verifyWritableLifecycle(SettleUpdateStartExecute, raw, sig, descriptor, a, now)
	if err != nil || execute.settleStart.EffectID != command.EffectID {
		t.Fatalf("execute reference: %v", err)
	}
	read, err := verifyWritableLifecycle(SettleUpdateStartRead, raw, readSig, descriptor, a, now)
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}
	if _, err := verifyWritableLifecycle(SettleUpdateStartExecute, raw, readSig, descriptor, a, now); err == nil {
		t.Fatal("a read signature was accepted as an execute")
	}
	if _, err := verifyWritableLifecycle(SettleUpdateStartRead, raw, sig, descriptor, a, now); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
	receipt := journal.SettleUpdateStartReceipt{
		Format: "portal.runner.settle-update-start-receipt/1-dev.1", DeploymentID: start.DeploymentID, ControllerEpoch: start.ControllerEpoch,
		InstallationID: start.InstallationID, OperationID: command.OperationID, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
		StartOperationID: start.OperationID, StartEffectID: start.EffectID, ResourceID: start.Payload.ResourceID, NamespaceID: start.Payload.NamespaceID,
		Outcome: "applied", ContainerState: "running", NativeID: strings.Repeat("ab", 32),
		ObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	body, _ := json.Marshal(receipt)
	started, received := time.Now().Add(-time.Second), time.Now().Add(time.Second)
	if !validWritableLifecycleReply(SettleUpdateStartExecute, body, execute, started, received) || !validWritableLifecycleReply(SettleUpdateStartRead, body, read, started, received) {
		t.Fatal("an exact receipt was refused")
	}
	for name, mutate := range map[string]func(*journal.SettleUpdateStartReceipt){
		"effect":    func(r *journal.SettleUpdateStartReceipt) { r.EffectID = migrateTransportID(62) },
		"payload":   func(r *journal.SettleUpdateStartReceipt) { r.PayloadSHA256 = strings.Repeat("f", 64) },
		"start":     func(r *journal.SettleUpdateStartReceipt) { r.StartEffectID = migrateTransportID(63) },
		"resource":  func(r *journal.SettleUpdateStartReceipt) { r.ResourceID = migrateTransportID(64) },
		"namespace": func(r *journal.SettleUpdateStartReceipt) { r.NamespaceID = migrateTransportID(65) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			encoded, _ := json.Marshal(changed)
			if validWritableLifecycleReply(SettleUpdateStartExecute, encoded, execute, started, received) {
				t.Fatal("a receipt for another settlement was accepted")
			}
		})
	}
}

// The lifecycle verification of a settlement admits an enable or restore start as it does an update start, and the
// receipt of such a settlement binds the same way.
func TestSettleUpdateStartActionsAcceptEnableAndRestoreStarts(t *testing.T) {
	for _, kind := range []string{"update-start", "enable-start", "restore-start"} {
		t.Run(kind, func(t *testing.T) {
			a, start, _, _, descriptor, _ := updateStartTransportFixture(t)
			key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
			now := time.Now().UnixMilli()
			start.Kind = kind
			if kind == "restore-start" {
				start.OperationID = migrateTransportID(70)
			}
			var err error
			if start.PayloadSHA256, err = control.UpdateStartLogicalDigest(start); err != nil {
				t.Fatal(err)
			}
			startRaw, _, err := control.SignUpdateStart(start, key, a, now)
			if err != nil {
				t.Fatal(err)
			}
			startRead, err := control.SignUpdateStartRead(startRaw, key, a, now)
			if err != nil {
				t.Fatal(err)
			}
			command := control.SettleUpdateStartCommand{
				Format: control.SettleUpdateStartFormat, Mode: control.SettleModeSettle, EffectID: migrateTransportID(60), OperationID: migrateTransportID(61),
				InstallationFence: start.InstallationFence, IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
				StartRaw: startRaw, StartReadSignature: startRead,
			}
			if command.PayloadSHA256, err = control.SettleUpdateStartLogicalDigest(command); err != nil {
				t.Fatal(err)
			}
			raw, sig, err := control.SignSettleUpdateStart(command, key, a, now)
			if err != nil {
				t.Fatal(err)
			}
			readSig, err := control.SignSettleUpdateStartRead(raw, key, a, now)
			if err != nil {
				t.Fatal(err)
			}
			execute, err := verifyWritableLifecycle(SettleUpdateStartExecute, raw, sig, descriptor, a, now)
			if err != nil || execute.updateStart.Kind != kind {
				t.Fatalf("execute: %v", err)
			}
			read, err := verifyWritableLifecycle(SettleUpdateStartRead, raw, readSig, descriptor, a, now)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			receipt := journal.SettleUpdateStartReceipt{
				Format: "portal.runner.settle-update-start-receipt/1-dev.1", DeploymentID: start.DeploymentID, ControllerEpoch: start.ControllerEpoch,
				InstallationID: start.InstallationID, OperationID: command.OperationID, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
				StartOperationID: start.OperationID, StartEffectID: start.EffectID, ResourceID: start.Payload.ResourceID, NamespaceID: start.Payload.NamespaceID,
				Outcome: "never_ran", NativeID: control.ReservedStartNativeID(start.Payload.ResourceID),
				ObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
			}
			body, _ := json.Marshal(receipt)
			started, received := time.Now().Add(-time.Second), time.Now().Add(time.Second)
			if !validWritableLifecycleReply(SettleUpdateStartExecute, body, execute, started, received) || !validWritableLifecycleReply(SettleUpdateStartRead, body, read, started, received) {
				t.Fatal("an exact receipt was refused")
			}
		})
	}
}

// A settlement over a start of any other kind never reaches the lifecycle.
func TestSettleUpdateStartActionsRefuseOtherKinds(t *testing.T) {
	a, start, startRaw, _, descriptor, _ := updateStartTransportFixture(t)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	now := time.Now().UnixMilli()
	startRead, err := control.SignUpdateStartRead(startRaw, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(startRaw, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"stop", "stage", "load-image"} {
		parsed["kind"] = kind
		other, _ := json.Marshal(parsed)
		command := control.SettleUpdateStartCommand{
			Format: control.SettleUpdateStartFormat, Mode: control.SettleModeSettle, EffectID: migrateTransportID(60), OperationID: migrateTransportID(61),
			InstallationFence: start.InstallationFence, IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
			StartRaw: other, StartReadSignature: startRead, PayloadSHA256: strings.Repeat("a", 64),
		}
		raw, _ := json.Marshal(command)
		sig := ed25519.Sign(key, raw)
		if _, err := verifyWritableLifecycle(SettleUpdateStartExecute, raw, sig, descriptor, a, now); err == nil {
			t.Fatalf("a settlement over a %q start was verified", kind)
		}
	}
}
