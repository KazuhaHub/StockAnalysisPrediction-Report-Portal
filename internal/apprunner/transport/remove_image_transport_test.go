package transport

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func removeImageTransportFixture(t *testing.T) (control.Authority, control.RemoveImageCommand, []byte, []byte, []byte, journal.RemoveImageReceipt) {
	t.Helper()
	a, load, _, _, descriptor, _ := loadImageTransportFixture(t)
	now := time.Now().UnixMilli()
	command := control.RemoveImageCommand{CommandHeader: load.CommandHeader, Payload: control.RemoveImagePayload{ReleaseSHA256: load.Payload.ReleaseSHA256, Platform: load.Payload.Platform, ManifestDigest: load.Payload.ManifestDigest, ConfigDigest: load.Payload.ConfigDigest, Layers: load.Payload.Layers, KnownLoads: loadImageTransportID(20)}}
	command.Format, command.Kind, command.EffectNumber, command.EffectID = control.RemoveImageFormat, control.RemoveImageKind, "2", loadImageTransportID(12)
	var err error
	command.PayloadSHA256, err = control.RemoveImageLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, signature, err := control.SignRemoveImage(command, migrateTransportKey(), a, now)
	if err != nil {
		t.Fatal(err)
	}
	stamp := strconv.FormatInt(now, 10)
	p := command.Payload
	receipt := journal.RemoveImageReceipt{
		Format: "portal.runner.remove-image-receipt/1-dev.1",
		Claim: journal.RemoveImageClaim{
			Format: "portal.runner.remove-image-claim/1-dev.1", ClaimID: loadImageTransportID(13), EffectID: command.EffectID,
			ReleaseSHA256: p.ReleaseSHA256, Platform: p.Platform, ManifestDigest: p.ManifestDigest, ConfigDigest: p.ConfigDigest,
			Layers: p.Layers, AdmittedFence: command.InstallationFence, ClaimedMS: stamp,
		},
		DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch, InstallationID: command.InstallationID,
		OperationID: command.OperationID, TargetGeneration: command.TargetGeneration, Stage: command.Stage, EffectID: command.EffectID,
		PayloadSHA256: command.PayloadSHA256, ImageID: "sha256:" + strings.Repeat("2", 64), Store: "classic",
		State: "removed", ObservedMS: stamp, SettledMS: stamp,
	}
	return a, command, raw, signature, descriptor, receipt
}

func TestRemoveImageTransportActionsAndSignaturePurposes(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := removeImageTransportFixture(t)
	now := time.Now().UnixMilli()
	for _, action := range []WritableLifecycleAction{RemoveImageExecute, RemoveImageRead} {
		if !action.valid() {
			t.Fatal("remove-image action is not valid", action)
		}
		ref, err := verifyWritableLifecycle(action, raw, lifecycleSignature(t, action, a, raw, sig), descriptor, a, now)
		if err != nil || ref.removeImage.EffectID != command.EffectID || ref.release != command.Payload.ReleaseSHA256 {
			t.Fatal("remove-image reference", action, err)
		}
		body, _ := json.Marshal(receipt)
		if !validWritableLifecycleReply(action, body, ref, time.Now().Add(-time.Second), time.Now()) {
			t.Fatal("exact remove-image reply refused", action)
		}
	}
	if !RemoveImageExecute.mutating() || RemoveImageRead.mutating() {
		t.Fatal("only the execute action may mutate")
	}
	read := lifecycleSignature(t, RemoveImageRead, a, raw, sig)
	if _, err := verifyWritableLifecycle(RemoveImageExecute, raw, read, descriptor, a, now); err == nil {
		t.Fatal("a read signature was accepted as an execute")
	}
	if _, err := verifyWritableLifecycle(RemoveImageRead, raw, sig, descriptor, a, now); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
	// A signed load is never a removal, and the reverse.
	_, _, lraw, lsig, ldescriptor, _ := loadImageTransportFixture(t)
	if _, err := verifyWritableLifecycle(RemoveImageExecute, lraw, lsig, ldescriptor, a, now); err == nil {
		t.Fatal("a load command was accepted as a removal")
	}
	if _, err := verifyWritableLifecycle(LoadImageExecute, raw, sig, descriptor, a, now); err == nil {
		t.Fatal("a removal command was accepted as a load")
	}
	expired, _ := strconv.ParseInt(command.ExpiresMS, 10, 64)
	if _, err := verifyWritableLifecycle(RemoveImageExecute, raw, sig, descriptor, a, expired+1); err == nil {
		t.Fatal("expired removal dispatch accepted")
	}
	if _, err := verifyWritableLifecycle(RemoveImageRead, raw, read, descriptor, a, expired+1); err != nil {
		t.Fatal("a read after the signed window was refused", err)
	}
}

func TestRemoveImageTransportReplyBinding(t *testing.T) {
	a, _, raw, sig, descriptor, receipt := removeImageTransportFixture(t)
	ref, err := verifyWritableLifecycle(RemoveImageExecute, raw, sig, descriptor, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	started, received := time.Now().Add(-time.Second), time.Now()
	for name, mutate := range map[string]func(*journal.RemoveImageReceipt){
		"effect":       func(v *journal.RemoveImageReceipt) { v.EffectID = loadImageTransportID(40) },
		"claim effect": func(v *journal.RemoveImageReceipt) { v.Claim.EffectID = loadImageTransportID(41) },
		"payload":      func(v *journal.RemoveImageReceipt) { v.PayloadSHA256 = strings.Repeat("b", 64) },
		"config":       func(v *journal.RemoveImageReceipt) { v.Claim.ConfigDigest = strings.Repeat("c", 64) },
		"platform":     func(v *journal.RemoveImageReceipt) { v.Claim.Platform = "linux/arm64" },
		"fence":        func(v *journal.RemoveImageReceipt) { v.Claim.AdmittedFence = "9" },
		"installation": func(v *journal.RemoveImageReceipt) { v.InstallationID = loadImageTransportID(44) },
		"stage":        func(v *journal.RemoveImageReceipt) { v.Stage = "migrate" },
		"state":        func(v *journal.RemoveImageReceipt) { v.State = "loaded" },
		"future": func(v *journal.RemoveImageReceipt) {
			v.SettledMS = strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := receipt
			mutate(&v)
			body, _ := json.Marshal(v)
			if validWritableLifecycleReply(RemoveImageExecute, body, ref, started, received) {
				t.Fatal("a reply that does not answer this command was accepted")
			}
		})
	}
	// A refusal because a container uses the image is also a receipt, with its typed outcome.
	refused := receipt
	refused.State, refused.Outcome = "in_use", "image_in_use"
	body, _ := json.Marshal(refused)
	if !validWritableLifecycleReply(RemoveImageExecute, body, ref, started, received) {
		t.Fatal("an in-use refusal receipt was refused")
	}
	// A load receipt never answers a removal.
	_, _, _, _, _, loadReceipt := loadImageTransportFixture(t)
	body, _ = json.Marshal(loadReceipt)
	if validWritableLifecycleReply(RemoveImageExecute, body, ref, started, received) {
		t.Fatal("a load receipt answered a removal")
	}
}

func TestRemoveImageTransportStatusReplies(t *testing.T) {
	a, command, raw, sig, descriptor, _ := removeImageTransportFixture(t)
	now := time.Now()
	read := lifecycleSignature(t, RemoveImageRead, a, raw, sig)
	ref, err := verifyWritableLifecycle(RemoveImageRead, raw, read, descriptor, a, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	status := func(state string, observed time.Time) []byte {
		raw, _ := json.Marshal(journal.RemoveImageStatus{
			Format: "portal.runner.remove-image-status/1-dev.1", DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
			InstallationID: command.InstallationID, OperationID: command.OperationID, TargetGeneration: command.TargetGeneration, Stage: command.Stage,
			EffectNumber: command.EffectNumber, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256, State: state,
			ObservedMS: strconv.FormatInt(observed.UnixMilli(), 10),
		})
		return raw
	}
	started, received := now.Add(-time.Second), now.Add(time.Second)
	for _, tc := range []struct {
		name  string
		body  []byte
		valid bool
	}{
		{"pending", status("pending", now), true},
		{"orphaned", status("orphaned", now), true},
		{"absent", status("absent", now), true},
		{"unknown state", status("blobs_missing", now), false},
		{"observed before the command was issued", status("pending", now.Add(-time.Hour)), false},
		{"observed in the future", status("pending", now.Add(time.Hour)), false},
	} {
		if got := validWritableLifecycleReply(RemoveImageRead, tc.body, ref, started, received); got != tc.valid {
			t.Fatalf("%s: valid=%v want %v", tc.name, got, tc.valid)
		}
	}
	if !removeImageAbsenceReply(RemoveImageRead, status("absent", now)) || removeImageAbsenceReply(RemoveImageExecute, status("absent", now)) ||
		removeImageAbsenceReply(RemoveImageRead, status("pending", now)) || removeImageAbsenceReply(RemoveImageRead, status("orphaned", now)) {
		t.Fatal("absence classification is wrong")
	}
}

func TestRemoveImageObservationAccessors(t *testing.T) {
	_, _, _, _, _, receipt := removeImageTransportFixture(t)
	body, _ := json.Marshal(receipt)
	o := WritableLifecycleObservation{action: RemoveImageExecute, body: string(body)}
	if got, err := o.RemoveImageReceipt(); err != nil || got != receipt {
		t.Fatalf("receipt accessor: %v", err)
	}
	if _, err := o.RemoveImageStatus(); err == nil {
		t.Fatal("a receipt parsed as a status")
	}
	foreign := WritableLifecycleObservation{action: LoadImageExecute, body: string(body)}
	if _, err := foreign.RemoveImageReceipt(); err == nil {
		t.Fatal("a load observation yielded a removal receipt")
	}
}
