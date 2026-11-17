//go:build linux || darwin

package journal

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestJournalRowsEnforceBoundsAndState(t *testing.T) {
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := insertDeployment(store.db); err != nil {
		t.Fatal(err)
	}
	if err := insertCommand(store.db); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
        VALUES(?,?,NULL,'artifact','fixture-native-id',NULL,0,?,0,0,'present','unknown',?,1,NULL,1,1)`, fixtureID(6), fixtureID(5), fixtureID(2), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO runner_checkpoints(id,data_namespace_id,installation_id,operation_id,release_sha256,data_format,writer_fence,manifest_sha256,bytes,state,revision,verified_ms,created_ms,updated_ms)
        VALUES(?,?,?,?,?,1,1,NULL,0,'preparing',1,NULL,1,1)`, fixtureID(7), fixtureID(8), fixtureID(5), fixtureID(4), strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name, sql string
		args      []any
	}{
		{"singleton", `UPDATE runner_deployment SET singleton=2`, nil},
		{"revision", `UPDATE runner_deployment SET revision=0`, nil},
		{"public_key_type", `UPDATE runner_deployment SET verification_key='plaintext'`, nil},
		{"generation", `UPDATE runner_commands SET target_generation=0`, nil},
		{"fractional", `UPDATE runner_commands SET admitted_fence=1.5`, nil},
		{"effect_count", `UPDATE runner_commands SET effect_number=33`, nil},
		{"arbitrary_command", `UPDATE runner_commands SET kind='exec'`, nil},
		{"unknown_stage", `UPDATE runner_commands SET stage='shell'`, nil},
		{"unknown_state", `UPDATE runner_commands SET state='success'`, nil},
		{"premature_ack", `UPDATE runner_commands SET ack_attempted_ms=1`, nil},
		{"non_object_receipt", `UPDATE runner_commands SET receipt_json='[]'`, nil},
		{"oversized_receipt", `UPDATE runner_commands SET receipt_json=?`, []any{`{"value":"` + strings.Repeat("x", 16384) + `"}`}},
		{"malformed_uuid", `UPDATE runner_commands SET operation_id='INVALID'`, nil},
		{"uppercase_digest", `UPDATE runner_commands SET payload_sha256=?`, []any{strings.Repeat("A", 64)}},
		{"resource_revision", `UPDATE runner_resources SET revision=NULL`, nil},
		{"native_id_limit", `UPDATE runner_resources SET native_id=?`, []any{strings.Repeat("x", 513)}},
		{"resource_type", `UPDATE runner_resources SET kind='host'`, nil},
		{"unverified_checkpoint", `UPDATE runner_checkpoints SET state='verified'`, nil},
		{"checkpoint_format", `UPDATE runner_checkpoints SET data_format=0`, nil},
		{"checkpoint_bytes", `UPDATE runner_checkpoints SET bytes=-1`, nil},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if _, err := store.db.Exec(fixture.sql, fixture.args...); err == nil {
				t.Fatal("invalid journal mutation accepted")
			}
		})
	}
	if _, err := store.db.Exec(`UPDATE runner_commands SET state='unknown',ack_attempted_ms=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE runner_checkpoints SET state='verified',manifest_sha256=?,verified_ms=1`, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
}

func TestJournalIntegrityCheckRefusesBypassedConstraint(t *testing.T) {
	for _, fixture := range []struct{ name, mutation string }{
		{"revision", `UPDATE runner_deployment SET revision=0`},
		{"command_state", `UPDATE runner_commands SET state='unexpected'`},
		{"premature_ack", `UPDATE runner_commands SET ack_attempted_ms=1`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			directory := privateDirectory(t)
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			if err := insertDeployment(store.db); err != nil {
				t.Fatal(err)
			}
			if err := insertCommand(store.db); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(fixture.mutation); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			// Refuse in the read-only preflight, not after enabling a writer.
			probe, err := sql.Open(journalDriver, journalDSN(directory, "ro"))
			if err != nil {
				t.Fatal(err)
			}
			_, inspectErr := inspect(context.Background(), probe)
			probe.Close()
			if !errors.Is(inspectErr, ErrCorrupt) {
				t.Fatalf("read-only preflight accepted invalid rows: %v", inspectErr)
			}
			if accepted, err := Open(directory); !errors.Is(err, ErrCorrupt) {
				if accepted != nil {
					accepted.Close()
				}
				t.Fatalf("persisted violation accepted: %v", err)
			}
		})
	}
}
