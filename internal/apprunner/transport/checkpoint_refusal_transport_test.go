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

func checkpointRefusalCommand(now int64) control.CheckpointCommand {
	return control.CheckpointCommand{
		Format: control.CheckpointFormat, DeploymentID: loadImageTransportID(21), ControllerEpoch: loadImageTransportID(22),
		ControlRevision: "1", InstallationID: loadImageTransportID(23), OperationID: loadImageTransportID(24),
		TargetGeneration: "2", Stage: "snapshot", EffectNumber: "1", EffectID: loadImageTransportID(25), Kind: "snapshot",
		InstallationFence: "2", PayloadSHA256: strings.Repeat("5", 64),
		IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
	}
}

func checkpointRefusalBody(t *testing.T, command control.CheckpointCommand, code string, observed int64) []byte {
	t.Helper()
	failed := &journal.DataEffectFailedError{Header: journal.CheckpointCommandHeader(command), Code: code}
	body, err := json.Marshal(failed.Status(observed))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// The runner replies a refused checkpoint with a typed failed status for exactly that command; the sender accepts it
// as the answer to the execute and to the read-back, and nothing else that is a status.
func TestCheckpointRefusalReplyIsAcceptedOnlyForTheExactCommand(t *testing.T) {
	now := time.Now().UnixMilli()
	command := checkpointRefusalCommand(now)
	ref := writableLifecycleReference{checkpoint: command}
	started, received := time.Now().Add(-time.Second), time.Now().Add(time.Second)
	for _, action := range []WritableLifecycleAction{CheckpointCreate, CheckpointRead} {
		body := checkpointRefusalBody(t, command, "checkpoint_unsafe", now+1)
		if !validWritableLifecycleReply(action, body, ref, started, received) {
			t.Fatal("an exact refusal was refused", action)
		}
		other := command
		other.EffectID = loadImageTransportID(26)
		if validWritableLifecycleReply(action, checkpointRefusalBody(t, other, "checkpoint_unsafe", now+1), ref, started, received) {
			t.Fatal("a refusal of another effect was accepted", action)
		}
		if validWritableLifecycleReply(action, checkpointRefusalBody(t, command, "checkpoint_unsafe", now-1000), ref, started, received) {
			t.Fatal("a refusal observed before the command was issued was accepted", action)
		}
		if validWritableLifecycleReply(action, checkpointRefusalBody(t, command, "checkpoint_unsafe", received.UnixMilli()+5000), ref, started, received) {
			t.Fatal("a refusal from the future was accepted", action)
		}
		var status journal.MigrateDataStatus
		if err := json.Unmarshal(body, &status); err != nil {
			t.Fatal(err)
		}
		status.State, status.Code = "pending", ""
		pending, _ := json.Marshal(status)
		if validWritableLifecycleReply(action, pending, ref, started, received) {
			t.Fatal("a pending status answered a checkpoint", action)
		}
	}
	observation := WritableLifecycleObservation{action: CheckpointCreate, body: string(checkpointRefusalBody(t, command, "checkpoint_too_large", now+1))}
	status, err := observation.DataEffectStatus()
	if err != nil || status.State != "failed" || status.Code != "checkpoint_too_large" {
		t.Fatal("the observation does not expose the refusal", status, err)
	}
}
