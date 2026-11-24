package transport

import "testing"

// A checkpoint walks and then re-reads the whole archive to verify it, work that routinely outlives the short
// execution bound. Cut there it would be cancelled as uncertain on every attempt and rewritten each time, so it is a bulk
// data action on both sides of the connection. Its read-back only reads the journal and stays short.
func TestCheckpointExecuteIsBoundedAsBulkDataOnBothSides(t *testing.T) {
	if !bulkDataAction(CheckpointCreate) {
		t.Fatal("a checkpoint is not a bulk data action")
	}
	if writableLifecycleTimeout(CheckpointCreate) != restoreExecutionTimeout || writableLifecycleServerTimeout(CheckpointCreate) != restoreExecutionTimeout {
		t.Fatalf("client %v server %v", writableLifecycleTimeout(CheckpointCreate), writableLifecycleServerTimeout(CheckpointCreate))
	}
	if bulkDataAction(CheckpointRead) || writableLifecycleTimeout(CheckpointRead) > candidateExecutionTimeout || writableLifecycleServerTimeout(CheckpointRead) != candidateExecutionTimeout {
		t.Fatal("the read-back of a checkpoint is bulk work")
	}
}
