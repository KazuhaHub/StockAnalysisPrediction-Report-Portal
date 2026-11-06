//go:build linux || darwin

package journal

import (
	"context"
	"testing"
	"time"
)

func TestLifecycleUnknownCommandsSurviveRunnerRestart(t *testing.T) {
	type lifecycleStage struct {
		stage string
		kind  string
	}
	stages := []lifecycleStage{
		{"validate", "stage"},
		{"download", "verify"},
		{"stage", "stage"},
		{"prepare_data", "prepare-data"},
		{"quiesce", "stop"},
		{"snapshot", "snapshot"},
		{"migrate", "remove-code"},
		{"start", "update-start"},
		{"probe", "probe"},
		{"activate", "activate"},
		{"revoke", "stop"},
		{"stop", "stop"},
		{"remove_code", "remove-code"},
		{"remove_data", "delete-data"},
		{"restore_data", "restore"},
	}
	directory := privateDirectory(t)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnrollController(
		context.Background(), enrollmentFixture(30)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	operation := fixtureID(9100)
	for index, lifecycle := range stages {
		effect := fixtureID(9200 + index)
		kind := lifecycle.kind
		if _, err := store.db.Exec(`INSERT INTO runner_commands(
			effect_id,operation_id,installation_id,stage,effect_number,
			kind,payload_sha256,target_generation,controller_epoch,
			admitted_fence,state,receipt_json,ack_attempted_ms,
			created_ms,updated_ms)
			VALUES(?,?,?,?,1,?,?,2,?,3,'unknown','{}',?,?,?)`,
			effect, operation, fixtureID(9300), lifecycle.stage, kind,
			candidateHash([]byte(lifecycle.stage)), fixtureID(9400),
			now, now, now); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var count, unknown, applied int
	if err := reopened.db.QueryRow(`SELECT
		COUNT(*),
		COALESCE(SUM(state='unknown'),0),
		COALESCE(SUM(state='applied'),0)
		FROM runner_commands`).Scan(&count, &unknown, &applied); err != nil {
		t.Fatal(err)
	}
	if count != len(stages) || unknown != len(stages) || applied != 0 {
		t.Fatalf("restart states: count=%d unknown=%d applied=%d",
			count, unknown, applied)
	}
	for index, lifecycle := range stages {
		var effect, kind, state, receipt string
		var fence int
		if err := reopened.db.QueryRow(`SELECT effect_id,kind,state,
			receipt_json,admitted_fence FROM runner_commands
			WHERE operation_id=? AND stage=? AND effect_number=1`,
			operation, lifecycle.stage).Scan(
			&effect, &kind, &state, &receipt, &fence); err != nil {
			t.Fatal(err)
		}
		if effect != fixtureID(9200+index) ||
			kind != lifecycle.kind ||
			state != "unknown" ||
			receipt != "{}" ||
			fence != 3 {
			t.Fatalf("%s changed across restart: %s %s %s %s %d",
				lifecycle.stage, effect, kind, state, receipt, fence)
		}
	}
	_, duplicateErr := reopened.db.Exec(`INSERT INTO runner_commands(
		effect_id,operation_id,installation_id,stage,effect_number,
		kind,payload_sha256,target_generation,controller_epoch,
		admitted_fence,state,receipt_json,ack_attempted_ms,
		created_ms,updated_ms)
		VALUES(?,?,?,?,1,?,?,2,?,3,'unknown','{}',?,?,?)`,
		fixtureID(9500), operation, fixtureID(9300), stages[0].stage,
		stages[0].kind, candidateHash([]byte(stages[0].stage)),
		fixtureID(9400), now, now, now)
	if duplicateErr == nil {
		t.Fatalf("reopen admitted a duplicate effect identity: %v",
			duplicateErr)
	}
}
