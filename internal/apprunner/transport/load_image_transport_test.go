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

func loadImageTransportID(n int) string { return migrateTransportBase + strconv.Itoa(60+n) }

func loadImageTransportFixture(t *testing.T) (control.Authority, control.LoadImageCommand, []byte, []byte, []byte, journal.LoadImageReceipt) {
	t.Helper()
	a, checkpoint, _, _, descriptor, _ := checkpointTransportFixture(t)
	now := time.Now().UnixMilli()
	command := control.LoadImageCommand{CommandHeader: control.CommandHeader{
		Format: control.LoadImageFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch,
		ControlRevision: strconv.FormatInt(a.ControlRevision, 10), InstallationID: checkpoint.InstallationID,
		OperationID: loadImageTransportID(1), TargetGeneration: checkpoint.TargetGeneration, Stage: "stage",
		EffectNumber: "1", EffectID: loadImageTransportID(2), Kind: "load-image", InstallationFence: checkpoint.InstallationFence,
		IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
	}, Payload: control.LoadImagePayload{
		ReleaseSHA256: checkpoint.Payload.ReleaseSHA256, Platform: "linux/amd64", ManifestDigest: strings.Repeat("1", 64),
		ConfigDigest: strings.Repeat("2", 64), Layers: strings.Repeat("3", 64) + ":4096",
	}}
	var err error
	command.PayloadSHA256, err = control.LoadImageLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, signature, err := control.SignLoadImage(command, migrateTransportKey(), a, now)
	if err != nil {
		t.Fatal(err)
	}
	stamp := strconv.FormatInt(now, 10)
	p := command.Payload
	receipt := journal.LoadImageReceipt{
		Format: "portal.runner.load-image-receipt/1-dev.1",
		Claim: journal.LoadImageClaim{
			Format: "portal.runner.load-image-claim/1-dev.1", ClaimID: loadImageTransportID(3), EffectID: command.EffectID,
			ReleaseSHA256: p.ReleaseSHA256, Platform: p.Platform, ManifestDigest: p.ManifestDigest, ConfigDigest: p.ConfigDigest,
			Layers: p.Layers, AdmittedFence: command.InstallationFence, ClaimedMS: stamp,
		},
		DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch, InstallationID: command.InstallationID,
		OperationID: command.OperationID, TargetGeneration: command.TargetGeneration, Stage: command.Stage, EffectID: command.EffectID,
		PayloadSHA256: command.PayloadSHA256, ImageID: "sha256:" + strings.Repeat("2", 64), Store: "classic",
		State: "loaded", ObservedMS: stamp, SettledMS: stamp,
	}
	return a, command, raw, signature, descriptor, receipt
}

func TestLoadImageTransportActionsAndSignaturePurposes(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := loadImageTransportFixture(t)
	now := time.Now().UnixMilli()
	for _, action := range []WritableLifecycleAction{LoadImageExecute, LoadImageRead} {
		if !action.valid() {
			t.Fatal("load-image action is not valid", action)
		}
		ref, err := verifyWritableLifecycle(action, raw, lifecycleSignature(t, action, a, raw, sig), descriptor, a, now)
		if err != nil || ref.loadImage.EffectID != command.EffectID || ref.release != command.Payload.ReleaseSHA256 {
			t.Fatal("load-image reference", action, err)
		}
		body, _ := json.Marshal(receipt)
		if !validWritableLifecycleReply(action, body, ref, time.Now().Add(-time.Second), time.Now()) {
			t.Fatal("exact load-image reply refused", action)
		}
	}
	if !LoadImageExecute.mutating() || LoadImageRead.mutating() {
		t.Fatal("only the execute action may mutate")
	}
	read := lifecycleSignature(t, LoadImageRead, a, raw, sig)
	if _, err := verifyWritableLifecycle(LoadImageExecute, raw, read, descriptor, a, now); err == nil {
		t.Fatal("a read signature was accepted as an execute")
	}
	if _, err := verifyWritableLifecycle(LoadImageRead, raw, sig, descriptor, a, now); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
	expired, _ := strconv.ParseInt(command.ExpiresMS, 10, 64)
	if _, err := verifyWritableLifecycle(LoadImageExecute, raw, sig, descriptor, a, expired+1); err == nil {
		t.Fatal("expired load dispatch accepted")
	}
	if _, err := verifyWritableLifecycle(LoadImageRead, raw, read, descriptor, a, expired+1); err != nil {
		t.Fatal("a read after the signed window was refused", err)
	}
	if _, err := verifyWritableLifecycle(LoadImageExecute, raw, sig, append(append([]byte(nil), descriptor...), ' '), a, now); err == nil {
		t.Fatal("a load verified against another release descriptor")
	}
	// A migrate command is not a load.
	_, _, mraw, msig, mdescriptor, _ := migrateTransportFixture(t)
	if _, err := verifyWritableLifecycle(LoadImageExecute, mraw, msig, mdescriptor, a, now); err == nil {
		t.Fatal("a migrate command was accepted as a load")
	}
}

// Each mutation breaks the binding between a reply and the exact signed command; the reply is refused.
func TestLoadImageTransportReplyBinding(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := loadImageTransportFixture(t)
	ref, err := verifyWritableLifecycle(LoadImageExecute, raw, sig, descriptor, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	started, received := time.Now().Add(-time.Second), time.Now()
	for name, mutate := range map[string]func(*journal.LoadImageReceipt){
		"effect":       func(v *journal.LoadImageReceipt) { v.EffectID = loadImageTransportID(40) },
		"claim effect": func(v *journal.LoadImageReceipt) { v.Claim.EffectID = loadImageTransportID(41) },
		"payload":      func(v *journal.LoadImageReceipt) { v.PayloadSHA256 = strings.Repeat("b", 64) },
		"release":      func(v *journal.LoadImageReceipt) { v.Claim.ReleaseSHA256 = strings.Repeat("c", 64) },
		"platform":     func(v *journal.LoadImageReceipt) { v.Claim.Platform = "linux/arm64" },
		"manifest":     func(v *journal.LoadImageReceipt) { v.Claim.ManifestDigest = strings.Repeat("9", 64) },
		"layers":       func(v *journal.LoadImageReceipt) { v.Claim.Layers = strings.Repeat("3", 64) + ":4097" },
		"fence":        func(v *journal.LoadImageReceipt) { v.Claim.AdmittedFence = "9" },
		"installation": func(v *journal.LoadImageReceipt) { v.InstallationID = loadImageTransportID(44) },
		"generation":   func(v *journal.LoadImageReceipt) { v.TargetGeneration = "9" },
		"stage":        func(v *journal.LoadImageReceipt) { v.Stage = "migrate" },
		"state":        func(v *journal.LoadImageReceipt) { v.State = "restored" },
		"future": func(v *journal.LoadImageReceipt) {
			v.SettledMS = strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := receipt
			mutate(&v)
			body, _ := json.Marshal(v)
			if validWritableLifecycleReply(LoadImageExecute, body, ref, started, received) {
				t.Fatal("a reply that does not answer this command was accepted")
			}
		})
	}
	// A rejected load is also a receipt, with its typed outcome.
	rejected := receipt
	rejected.State, rejected.Outcome = "rejected", "retag_detected"
	body, _ := json.Marshal(rejected)
	if !validWritableLifecycleReply(LoadImageExecute, body, ref, started, received) {
		t.Fatal("a rejection receipt was refused")
	}
	_ = command
}

func TestLoadImageTransportStatusReplies(t *testing.T) {
	a, command, raw, sig, descriptor, _ := loadImageTransportFixture(t)
	now := time.Now()
	read := lifecycleSignature(t, LoadImageRead, a, raw, sig)
	ref, err := verifyWritableLifecycle(LoadImageRead, raw, read, descriptor, a, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	status := func(state string, missing []string, observed time.Time) []byte {
		v := journal.LoadImageStatus{
			Format: "portal.runner.load-image-status/1-dev.1", DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
			InstallationID: command.InstallationID, OperationID: command.OperationID, TargetGeneration: command.TargetGeneration, Stage: command.Stage,
			EffectNumber: command.EffectNumber, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256, State: state, Missing: missing,
			ObservedMS: strconv.FormatInt(observed.UnixMilli(), 10),
		}
		raw, _ := json.Marshal(v)
		return raw
	}
	started, received := now.Add(-time.Second), now.Add(time.Second)
	for _, tc := range []struct {
		name  string
		body  []byte
		valid bool
	}{
		{"pending", status("pending", nil, now), true},
		{"orphaned", status("orphaned", nil, now), true},
		{"absent", status("absent", nil, now), true},
		{"blobs missing", status("blobs_missing", []string{strings.Repeat("4", 64)}, now), true},
		{"observed before the command was issued", status("pending", nil, now.Add(-time.Hour)), false},
		{"observed in the future", status("pending", nil, now.Add(time.Hour)), false},
	} {
		if got := validWritableLifecycleReply(LoadImageRead, tc.body, ref, started, received); got != tc.valid {
			t.Fatalf("%s: valid=%v want %v", tc.name, got, tc.valid)
		}
	}
	// Only a read-back's absence is definite; an execute that found no row would have claimed one.
	if !loadImageAbsenceReply(LoadImageRead, status("absent", nil, now)) || loadImageAbsenceReply(LoadImageExecute, status("absent", nil, now)) ||
		loadImageAbsenceReply(LoadImageRead, status("pending", nil, now)) || loadImageAbsenceReply(LoadImageRead, status("blobs_missing", []string{strings.Repeat("4", 64)}, now)) {
		t.Fatal("absence classification is wrong")
	}
	// A status that names another effect is refused.
	other := journal.LoadImageStatus{}
	_ = json.Unmarshal(status("pending", nil, now), &other)
	other.EffectID = loadImageTransportID(55)
	body, _ := json.Marshal(other)
	if validWritableLifecycleReply(LoadImageRead, body, ref, started, received) {
		t.Fatal("a status for another effect was accepted")
	}
}
