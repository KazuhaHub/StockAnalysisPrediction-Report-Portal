package transport

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func refusalFor(id journal.CapacityIdentity, dimension string) journal.CapacityRefusal {
	return journal.CapacityRefusal{
		Format: "portal.runner.capacity-refusal/1-dev.1", DeploymentID: id.DeploymentID, ControllerEpoch: id.ControllerEpoch,
		InstallationID: id.InstallationID, OperationID: id.OperationID, TargetGeneration: id.TargetGeneration, Stage: id.Stage,
		EffectNumber: id.EffectNumber, EffectID: id.EffectID, Kind: id.Kind, PayloadSHA256: id.PayloadSHA256,
		Outcome: "capacity_exhausted", Dimension: dimension, Requested: "256", Remaining: "100",
		SettledMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
}

func TestCapacityRefusalActionsAreTheStartBearingOnes(t *testing.T) {
	want := map[WritableLifecycleAction]bool{
		WritableCandidateStart: true, WritableCandidateRead: true, UpdateStartExecute: true, UpdateStartReceipt: true,
		MigrateDataExecute: true, MigrateDataRead: true, LoadImageExecute: true, LoadImageRead: true,
		ImportCheckpointExecute: true, ImportCheckpointRead: true,
	}
	for action := WritableLifecycleAction(0); action < 255; action++ {
		if action.valid() && capacityRefusalAction(action) != want[action] {
			t.Fatalf("action %d: capacityRefusalAction = %v", action, capacityRefusalAction(action))
		}
	}
}

func TestWritableLifecycleAcceptsOnlyABoundCapacityRefusal(t *testing.T) {
	a, command, raw, sig, descriptor, _ := updateStartTransportFixture(t)
	now := time.Now().UnixMilli()
	ref, err := verifyWritableLifecycle(UpdateStartExecute, raw, sig, descriptor, a, now)
	if err != nil || ref.capacity == nil {
		t.Fatalf("no capacity identity on an update start: %v", err)
	}
	if ref.capacity.EffectID != command.EffectID {
		t.Fatal("capacity identity is not the command's")
	}
	refusal := refusalFor(*ref.capacity, "memory")
	body, _ := json.Marshal(refusal)
	started, received := time.Now().Add(-time.Second), time.Now()
	for _, action := range []WritableLifecycleAction{UpdateStartExecute} {
		r, err := verifyWritableLifecycle(action, raw, sig, descriptor, a, now)
		if err != nil {
			t.Fatal(err)
		}
		if !validWritableLifecycleReply(action, body, r, started, received) {
			t.Fatalf("a bound refusal was refused for action %d", action)
		}
	}
	// It is not an answer to the reservation, the probe or a registration.
	for _, action := range []WritableLifecycleAction{UpdateStartReserve, UpdateStartProbe} {
		r, err := verifyWritableLifecycle(action, raw, sig, descriptor, a, now)
		if err != nil {
			t.Fatal(err)
		}
		if validWritableLifecycleReply(action, body, r, started, received) {
			t.Fatalf("a refusal answered action %d", action)
		}
	}
	for name, mutate := range map[string]func(*journal.CapacityRefusal){
		"effect": func(r *journal.CapacityRefusal) { r.EffectID = "11111111-1111-4111-8111-111111119990" },
		"payload": func(r *journal.CapacityRefusal) {
			r.PayloadSHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"operation": func(r *journal.CapacityRefusal) { r.OperationID = "11111111-1111-4111-8111-111111119991" },
		"future": func(r *journal.CapacityRefusal) {
			r.SettledMS = strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
		},
		"dimension": func(r *journal.CapacityRefusal) { r.Dimension = "swap" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := refusal
			mutate(&changed)
			b, _ := json.Marshal(changed)
			if validWritableLifecycleReply(UpdateStartExecute, b, ref, started, received) {
				t.Fatal("a refusal that does not bind the command was accepted")
			}
		})
	}
}
