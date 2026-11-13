//go:build linux || darwin

package journal

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newV2Journal creates an empty journal exactly as the v2 binary wrote it.
func newV2Journal(t *testing.T) (string, *sql.DB) {
	t.Helper()
	directory := privateDirectory(t)
	if err := os.WriteFile(filepath.Join(directory, journalName), nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(journalDriver, journalDSN(directory, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL fixture profile: %s %v", mode, err)
	}
	if err := initializeVersion(context.Background(), db, 2); err != nil {
		t.Fatal(err)
	}
	return directory, db
}

func insertV2Resource(t *testing.T, db *sql.DB, n int, kind, native, desired, observed string) {
	t.Helper()
	namespace := any(fixtureID(900 + n))
	if kind == "volume" {
		namespace = fixtureID(n)
	}
	if _, err := db.Exec(`INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
        VALUES(?,?,?,?,?,?,1,?,1,1,?,?,?,1,NULL,1,1)`,
		fixtureID(n), fixtureID(500+n), namespace, kind, native, strings.Repeat("a", 64), fixtureID(2), desired, observed, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
}

func insertV2Load(t *testing.T, db *sql.DB, n int, installation int, state, receipt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
        VALUES(?,?,?,'stage',1,'load-image',?,1,?,1,?,?,NULL,1,1)`,
		fixtureID(n), fixtureID(600+n), fixtureID(installation), strings.Repeat("c", 64), fixtureID(2), state, receipt); err != nil {
		t.Fatal(err)
	}
}

type reservationRow struct {
	kind, installation, state string
	memory, cpu, disk, image  int64
}

func reservationsByResource(t *testing.T, db *sql.DB) map[string]reservationRow {
	t.Helper()
	rows, err := db.Query(`SELECT resource_id,kind,installation_id,state,memory_mib,cpu_millis,disk_bytes,image_bytes FROM runner_reservations`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]reservationRow{}
	for rows.Next() {
		var resource string
		var r reservationRow
		if err := rows.Scan(&resource, &r.kind, &r.installation, &r.state, &r.memory, &r.cpu, &r.disk, &r.image); err != nil {
			t.Fatal(err)
		}
		out[resource] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFreshJournalIsV3WithReservations(t *testing.T) {
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if scalar(t, store.db, `PRAGMA user_version`) != 3 {
		t.Fatal("fresh journal is not v3")
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='runner_reservations'`) != 1 {
		t.Fatal("fresh v3 journal has no reservation table")
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM runner_reservations`) != 0 {
		t.Fatal("fresh journal holds reservations")
	}
}

func TestV2SchemaProductsAreFrozen(t *testing.T) {
	for _, product := range schemaProducts(2) {
		if strings.Contains(product.name, "reservation") {
			t.Fatalf("v2 declares %s", product.name)
		}
	}
	found := false
	for _, product := range schemaProducts(3) {
		found = found || product.name == "runner_reservations"
	}
	if !found {
		t.Fatal("v3 does not declare runner_reservations")
	}
}

func TestNewerThanV3Refused(t *testing.T) {
	directory := privateDirectory(t)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version=4`); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if reopened, err := Open(directory); !errors.Is(err, ErrNewerVersion) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("v4 journal opened: %v", err)
	}
}

func TestOpenUpgradesV2ToV3AndBacksFillAtTheMaxima(t *testing.T) {
	directory, db := newV2Journal(t)
	if err := insertDeployment(db); err != nil {
		t.Fatal(err)
	}
	insertV2Resource(t, db, 10, "container", "candidate-pending/v1/x", "present", "running")
	insertV2Resource(t, db, 11, "container", "writable-candidate-pending/v1/x", "present", "stopped") // stopped, still present
	insertV2Resource(t, db, 12, "container", "candidate-pending/v1/y", "absent", "absent")            // proven absent
	insertV2Resource(t, db, 13, "container", "migration-job/"+fixtureID(13), "present", "unknown")
	insertV2Resource(t, db, 14, "container", "update-start-pending/v1/z", "absent", "unknown") // not proven absent
	insertV2Resource(t, db, 20, "volume", "xfs-project/v1/1", "present", "present")
	insertV2Resource(t, db, 21, "volume", "xfs-project/v1/2", "absent", "absent")
	insertV2Resource(t, db, 30, "socket", "sock", "present", "present")
	digestX, digestY, digestZ := strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)
	insertV2Load(t, db, 40, 71, "applied", `{"claim":{"config_digest":"`+digestX+`"}}`)
	insertV2Load(t, db, 41, 72, "applied", `{"claim":{"config_digest":"`+digestX+`"}}`)
	insertV2Load(t, db, 42, 73, "unknown", `{"config_digest":"`+digestY+`"}`)
	insertV2Load(t, db, 43, 74, "reverted", `{"claim":{"config_digest":"`+digestZ+`"}}`)
	if scalar(t, db, `PRAGMA user_version`) != 2 {
		t.Fatal("fixture is not v2")
	}
	db.Close()

	store, err := Open(directory)
	if err != nil {
		t.Fatalf("v2 journal did not upgrade: %v", err)
	}
	defer store.Close()
	if scalar(t, store.db, `PRAGMA user_version`) != 3 {
		t.Fatal("not upgraded to v3")
	}
	got := reservationsByResource(t, store.db)
	want := map[string]reservationRow{
		fixtureID(10):      {"backend", fixtureID(510), "held", 4096, 4000, 0, 0},
		fixtureID(11):      {"backend", fixtureID(511), "held", 4096, 4000, 0, 0},
		fixtureID(13):      {"migration", fixtureID(513), "held", 4096, 4000, 0, 0},
		fixtureID(14):      {"backend", fixtureID(514), "held", 4096, 4000, 0, 0},
		fixtureID(20):      {"namespace", fixtureID(520), "held", 0, 0, 768 << 20, 0},
		"image/" + digestX: {"image", fixtureID(71), "held", 0, 0, 0, 4 << 30},
		"image/" + digestY: {"image", fixtureID(73), "held", 0, 0, 0, 4 << 30},
	}
	if len(got) != len(want) {
		t.Fatalf("backfill rows = %+v", got)
	}
	for resource, row := range want {
		if got[resource] != row {
			t.Fatalf("%s: got %+v want %+v", resource, got[resource], row)
		}
	}
	if _, err := inspect(context.Background(), store.db); err != nil {
		t.Fatalf("upgraded journal fails inspection: %v", err)
	}
}

func TestOpenUpgradesV1StraightToV3(t *testing.T) {
	directory := v1Journal(t)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if scalar(t, store.db, `PRAGMA user_version`) != 3 || scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands`) != 3 {
		t.Fatal("v1 journal not carried to v3")
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM runner_reservations`) != 0 {
		t.Fatal("a v1 journal with no live resource backfilled a reservation")
	}
}

func TestReservationTableRejectsInconsistentRows(t *testing.T) {
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	insert := `INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,?,1,1)`
	if _, err := store.db.Exec(insert, fixtureID(1), fixtureID(2), fixtureID(3), "backend", 16, 25, 0, 0, "held"); err != nil {
		t.Fatalf("valid reservation refused: %v", err)
	}
	for name, args := range map[string][]any{
		"duplicate_resource": {fixtureID(4), fixtureID(2), fixtureID(3), "backend", 16, 25, 0, 0, "held"},
		"unknown_kind":       {fixtureID(5), fixtureID(6), fixtureID(3), "volume", 0, 0, 1, 0, "held"},
		"unknown_state":      {fixtureID(5), fixtureID(6), fixtureID(3), "backend", 16, 25, 0, 0, "pending"},
		"backend_with_disk":  {fixtureID(5), fixtureID(6), fixtureID(3), "backend", 16, 25, 1, 0, "held"},
		"image_with_memory":  {fixtureID(5), "image/" + strings.Repeat("a", 64), fixtureID(3), "image", 16, 0, 0, 1, "held"},
		"negative":           {fixtureID(5), fixtureID(6), fixtureID(3), "backend", -1, 25, 0, 0, "held"},
		"namespace_with_cpu": {fixtureID(5), fixtureID(6), fixtureID(3), "namespace", 0, 5, 1, 0, "held"},
	} {
		if _, err := store.db.Exec(insert, args...); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
