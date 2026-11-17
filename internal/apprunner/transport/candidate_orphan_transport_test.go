package transport

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func orphanFor(id journal.CapacityIdentity) journal.CandidateOrphanReceipt {
	return journal.CandidateOrphanReceipt{
		Format: "portal.runner.candidate-orphan-receipt/1-dev.1", DeploymentID: id.DeploymentID, ControllerEpoch: id.ControllerEpoch,
		InstallationID: id.InstallationID, OperationID: id.OperationID, TargetGeneration: id.TargetGeneration, Stage: id.Stage,
		EffectNumber: id.EffectNumber, EffectID: id.EffectID, Kind: id.Kind, PayloadSHA256: id.PayloadSHA256,
		ResourceID: "11111111-1111-4111-8111-111111110001", Outcome: journal.CandidateOrphanOutcome,
		SettledMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
}

func TestCandidateOrphanActionsAreTheWritableStartAndItsReadBacks(t *testing.T) {
	want := map[WritableLifecycleAction]bool{WritableCandidateStart: true, WritableCandidateRead: true, WritableCandidateReconcile: true}
	for action := WritableLifecycleAction(0); action < 255; action++ {
		if action.valid() && candidateOrphanAction(action) != want[action] {
			t.Fatalf("action %d: candidateOrphanAction = %v", action, candidateOrphanAction(action))
		}
	}
}

// The runner answers a start it proved never ran with the orphan receipt, and the sender believes it only when it is
// bound to the exact signed command.
func TestWritableLifecycleAcceptsOnlyABoundCandidateOrphanReceipt(t *testing.T) {
	id := journal.CapacityIdentity{
		DeploymentID: "11111111-1111-4111-8111-111111110010", ControllerEpoch: "11111111-1111-4111-8111-111111110011",
		InstallationID: "11111111-1111-4111-8111-111111110012", OperationID: "11111111-1111-4111-8111-111111110013",
		TargetGeneration: "2", Stage: "start", EffectNumber: "1", EffectID: "11111111-1111-4111-8111-111111110014",
		Kind: "start-candidate", PayloadSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	ref := writableLifecycleReference{capacity: &id}
	receipt := orphanFor(id)
	body, _ := json.Marshal(receipt)
	started, received := time.Now().Add(-time.Second), time.Now().Add(time.Second)
	for _, action := range []WritableLifecycleAction{WritableCandidateStart, WritableCandidateRead, WritableCandidateReconcile} {
		if !validWritableLifecycleReply(action, body, ref, started, received) {
			t.Fatalf("a bound orphan receipt was refused for action %d", action)
		}
	}
	// It answers no other action.
	for _, action := range []WritableLifecycleAction{WritableCandidateProbe, UpdateStartExecute, LoadImageExecute} {
		if validWritableLifecycleReply(action, body, ref, started, received) {
			t.Fatalf("an orphan receipt answered action %d", action)
		}
	}
	for name, mutate := range map[string]func(*journal.CandidateOrphanReceipt){
		"effect": func(r *journal.CandidateOrphanReceipt) { r.EffectID = "11111111-1111-4111-8111-111111119990" },
		"payload": func(r *journal.CandidateOrphanReceipt) {
			r.PayloadSHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"future": func(r *journal.CandidateOrphanReceipt) {
			r.SettledMS = strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			raw, _ := json.Marshal(changed)
			if validWritableLifecycleReply(WritableCandidateStart, raw, ref, started, received) {
				t.Fatal("an orphan receipt that does not answer this command was accepted")
			}
		})
	}
	if validWritableLifecycleReply(WritableCandidateStart, body, writableLifecycleReference{}, started, received) {
		t.Fatal("an orphan receipt was accepted with no command identity")
	}
}
