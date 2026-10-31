//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

type managedRemovalFixture struct {
	store   *Store
	command control.ManagedRemovalCommand
	raw     []byte
	sig     []byte
	source  CandidateRunningReceipt
}

func newManagedRemovalFixture(t *testing.T) managedRemovalFixture {
	t.Helper()
	s, _, _, source := appliedCandidateReceiptFixture(t)
	var sourceRaw, dataRaw, sourceOperation, sourceFence string
	if err := s.db.QueryRow(`SELECT operation_id,admitted_fence,receipt_json FROM runner_commands WHERE effect_id=? AND kind='start-candidate' AND state='applied'`, source.Claim.EffectID).Scan(&sourceOperation, &sourceFence, &sourceRaw); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE operation_id=? AND stage='prepare_data' AND effect_number=1 AND state='applied'`, sourceOperation).Scan(&dataRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDataPreparationReceipt([]byte(dataRaw)); err != nil {
		t.Fatal(err)
	}
	sourceDigest := sha256.Sum256([]byte(sourceRaw))
	now := time.Now().UnixMilli()
	command := control.ManagedRemovalCommand{
		Format:            control.ManagedRemovalFormat,
		DeploymentID:      source.DeploymentID,
		ControllerEpoch:   source.ControllerEpoch,
		ControlRevision:   source.ControlRevision,
		InstallationID:    source.InstallationID,
		OperationID:       fixtureID(850),
		TargetGeneration:  "2",
		Stage:             "stop",
		EffectNumber:      "1",
		EffectID:          fixtureID(851),
		Kind:              "stop",
		InstallationFence: "2",
		IssuedMS:          strconv.FormatInt(now, 10),
		ExpiresMS:         strconv.FormatInt(now+25000, 10),
		Payload: control.ManagedRemovalPayload{
			ResourceID:          source.Claim.ResourceID,
			DataNamespaceID:     source.DataNamespaceID,
			ReleaseSHA256:       source.ReleaseSHA256,
			Platform:            source.Claim.Preflight.Platform,
			SourceOperationID:   sourceOperation,
			SourceEffectID:      source.Claim.EffectID,
			SourceReceiptSHA256: hex.EncodeToString(sourceDigest[:]),
		},
	}
	digest, err := control.ManagedRemovalLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	enrollment := enrollmentFixture(30)
	authority := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	raw, sig, err := control.SignManagedRemoval(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return managedRemovalFixture{store: s, command: command, raw: raw, sig: sig, source: source}
}

func resignManagedRemoval(t *testing.T, f managedRemovalFixture, mutate func(*control.ManagedRemovalCommand)) ([]byte, []byte) {
	t.Helper()
	c := f.command
	mutate(&c)
	digest, err := control.ManagedRemovalLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	c.PayloadSHA256 = digest
	now := time.Now().UnixMilli()
	c.IssuedMS = strconv.FormatInt(now, 10)
	c.ExpiresMS = strconv.FormatInt(now+25000, 10)
	enrollment := enrollmentFixture(30)
	authority := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	raw, sig, err := control.SignManagedRemoval(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

func TestManagedRemovalPersistsOneExactStopIntent(t *testing.T) {
	f := newManagedRemovalFixture(t)
	source, replay, err := f.store.beginManagedRemoval(context.Background(), f.raw, f.sig)
	if err != nil || replay || source.source.NativeID != f.source.NativeID {
		t.Fatal(source, replay, err)
	}
	var count int
	if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM runner_commands WHERE effect_id=? AND stage='stop' AND kind='stop' AND state='unknown' AND receipt_json='{}'`, f.command.EffectID).Scan(&count); err != nil || count != 1 {
		t.Fatal("removal intent not durably unknown")
	}
	var resourceState string
	if err := f.store.db.QueryRow(`SELECT revision||':'||observed_state FROM runner_resources WHERE kind='container'`).Scan(&resourceState); err != nil || resourceState != "2:running" {
		t.Fatal("intent fabricated physical completion")
	}
	if _, replay, err := f.store.beginManagedRemoval(context.Background(), f.raw, f.sig); !errors.Is(err, ErrFenceReconciliation) || replay {
		t.Fatal("uncertain stop was reclaimed", replay, err)
	}
}

func TestManagedRemovalRejectsChangedSourceAndPendingWork(t *testing.T) {
	for _, name := range []string{"source state", "source hash", "resource", "pending", "target", "authority"} {
		t.Run(name, func(t *testing.T) {
			f := newManagedRemovalFixture(t)
			raw, sig := f.raw, f.sig
			switch name {
			case "source state":
				if _, err := f.store.db.Exec(`UPDATE runner_commands SET state='unknown' WHERE effect_id=? AND kind='start-candidate'`, f.source.Claim.EffectID); err != nil {
					t.Fatal(err)
				}
			case "source hash":
				raw, sig = resignManagedRemoval(t, f, func(c *control.ManagedRemovalCommand) {
					c.Payload.SourceReceiptSHA256 = strings.Repeat("e", 64)
				})
			case "resource":
				if _, err := f.store.db.Exec(`UPDATE runner_resources SET native_id=? WHERE kind='container'`, strings.Repeat("e", 64)); err != nil {
					t.Fatal(err)
				}
			case "pending":
				if _, err := f.store.db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms) VALUES(?,?,?,'validate',1,'verify',?,1,?,1,'prepared','{}',NULL,1,1)`, fixtureID(852), fixtureID(853), f.source.InstallationID, strings.Repeat("f", 64), f.source.ControllerEpoch); err != nil {
					t.Fatal(err)
				}
			case "target":
				raw, sig = resignManagedRemoval(t, f, func(c *control.ManagedRemovalCommand) { c.TargetGeneration = "1" })
			case "authority":
				if _, err := f.store.db.Exec(`UPDATE runner_deployment SET control_revision=control_revision+1`); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := f.store.beginManagedRemoval(context.Background(), raw, sig); err == nil {
				t.Fatalf("changed %s admitted", name)
			}
			var count int
			if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM runner_commands WHERE effect_id=?`, f.command.EffectID).Scan(&count); err != nil || count != 0 {
				t.Fatal("refusal wrote removal intent")
			}
		})
	}
}

func TestManagedRemovalReceiptReadMatchesAppliedIntent(t *testing.T) {
	f := newManagedRemovalFixture(t)
	source, replay, err := f.store.beginManagedRemoval(context.Background(), f.raw, f.sig)
	if err != nil || replay {
		t.Fatal(source, replay, err)
	}
	now := time.Now().UnixMilli()
	receipt := ManagedRemovalReceipt{
		Format:              managedRemovalReceiptFormat,
		DeploymentID:        f.command.DeploymentID,
		ControllerEpoch:     f.command.ControllerEpoch,
		InstallationID:      f.command.InstallationID,
		OperationID:         f.command.OperationID,
		SourceOperationID:   f.command.Payload.SourceOperationID,
		SourceEffectID:      f.command.Payload.SourceEffectID,
		TargetGeneration:    f.command.TargetGeneration,
		Stage:               f.command.Stage,
		EffectID:            f.command.EffectID,
		PayloadSHA256:       f.command.PayloadSHA256,
		SourceReceiptSHA256: f.command.Payload.SourceReceiptSHA256,
		ResourceID:          f.command.Payload.ResourceID,
		DataNamespaceID:     f.command.Payload.DataNamespaceID,
		ReleaseSHA256:       f.command.Payload.ReleaseSHA256,
		NativeID:            f.source.NativeID,
		State:               "stopped",
		ObservedMS:          strconv.FormatInt(now, 10),
		SettledMS:           strconv.FormatInt(now, 10),
	}
	raw, _ := json.Marshal(receipt)
	if _, err := f.store.db.Exec(`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=?`, string(raw), now, f.command.EffectID); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.ReadManagedRemovalReceipt(context.Background(), f.raw, f.sig)
	if err != nil || got != receipt {
		t.Fatal(got, err)
	}
}

func TestManagedRemovalReceiptReadRejectsChangedBinding(t *testing.T) {
	for name, mutate := range map[string]func(*ManagedRemovalReceipt){
		"operation":        func(v *ManagedRemovalReceipt) { v.OperationID = fixtureID(854) },
		"source operation": func(v *ManagedRemovalReceipt) { v.SourceOperationID = fixtureID(855) },
		"source effect":    func(v *ManagedRemovalReceipt) { v.SourceEffectID = fixtureID(856) },
		"resource":         func(v *ManagedRemovalReceipt) { v.ResourceID = fixtureID(857) },
		"namespace":        func(v *ManagedRemovalReceipt) { v.DataNamespaceID = fixtureID(858) },
		"release":          func(v *ManagedRemovalReceipt) { v.ReleaseSHA256 = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newManagedRemovalFixture(t)
			if _, _, err := f.store.beginManagedRemoval(context.Background(), f.raw, f.sig); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UnixMilli()
			receipt := ManagedRemovalReceipt{
				Format: managedRemovalReceiptFormat, DeploymentID: f.command.DeploymentID,
				ControllerEpoch: f.command.ControllerEpoch, InstallationID: f.command.InstallationID,
				OperationID: f.command.OperationID, SourceOperationID: f.command.Payload.SourceOperationID,
				SourceEffectID: f.command.Payload.SourceEffectID, TargetGeneration: f.command.TargetGeneration,
				Stage: f.command.Stage, EffectID: f.command.EffectID, PayloadSHA256: f.command.PayloadSHA256,
				SourceReceiptSHA256: f.command.Payload.SourceReceiptSHA256, ResourceID: f.command.Payload.ResourceID,
				DataNamespaceID: f.command.Payload.DataNamespaceID, ReleaseSHA256: f.command.Payload.ReleaseSHA256,
				NativeID: f.source.NativeID, State: "stopped",
				ObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
			}
			mutate(&receipt)
			raw, _ := json.Marshal(receipt)
			if _, err := f.store.db.Exec(`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=?`, string(raw), now, f.command.EffectID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.ReadManagedRemovalReceipt(context.Background(), f.raw, f.sig); err == nil {
				t.Fatal("changed receipt binding was read as applied intent")
			}
		})
	}
}
