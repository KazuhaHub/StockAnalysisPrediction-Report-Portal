package app

import (
	"slices"
	"testing"
)

// The migration list is ordered by when steps reached main, not by id. A backup's header declares the
// steps its database had as a prefix of this list (checkDumpMigrations), so a step that lands later must
// be appended at the end even when its id is smaller: ids are labels, not positions, and a branch still
// in flight may hold some of them (reservedMigrationIDs). Inserting a late step before one that has
// shipped would make every backup written by that release unrestorable.
//
// When a branch adds a step, append its id here as well. A rebase that resolves the migrations() line
// by sorting the ids fails this test.
var migrationListOrder = []string{"0001", "0002", "0003", "0004", "0005", "0006", "0010", "0009", "0007", "0008", "0011", "0012", "0013", "0014", "0015", "0016", "0017", "0018", "0019", "0020", "0021"}

// reservedMigrationIDs are ids held by a long-lived branch that has not reached main. main must not
// define them: the branch merges main in regularly and the ledger is keyed by id, so a second step with
// the same id would collide on that merge and on every database that applied either one. A new
// long-lived branch that adds steps registers its ids here on main first.
//
// The owning branch deletes its entries when its steps exist there: in the commit that adds a step, or,
// for steps it already has, in the first commit that merges this table in. Once the branch lands the
// entries are gone. Reserving an id does not reserve a position: if main appends a step first, the
// branch's steps are appended after it, so databases and backups written by a pre-merge build of the
// branch are disposable.
var reservedMigrationIDs = map[string]string{}

func TestMigrationListKeepsItsMergeOrder(t *testing.T) {
	st := newTestStore(t)
	var ids []string
	for _, m := range st.migrations() {
		ids = append(ids, m.id)
	}
	if len(ids) < len(migrationListOrder) || !slices.Equal(ids[:len(migrationListOrder)], migrationListOrder) {
		t.Fatalf("migrations() = %v, want it to start with %v: a later step is appended, never inserted", ids, migrationListOrder)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("migration id %s appears twice in %v", id, ids)
		}
		seen[id] = true
	}
}

func reservedMigrationCollisions(ids []string, reserved map[string]string) []string {
	var hits []string
	for _, id := range ids {
		if owner, ok := reserved[id]; ok {
			hits = append(hits, id+" (reserved by "+owner+")")
		}
	}
	return hits
}

func TestMigrationListAvoidsReservedIDs(t *testing.T) {
	if hits := reservedMigrationCollisions([]string{"0001", "0099"}, map[string]string{"0099": "fixture"}); len(hits) != 1 {
		t.Fatalf("a list containing a reserved id must report exactly one collision, got %v", hits)
	}
	st := newTestStore(t)
	var ids []string
	for _, m := range st.migrations() {
		ids = append(ids, m.id)
	}
	if hits := reservedMigrationCollisions(ids, reservedMigrationIDs); len(hits) > 0 {
		t.Fatalf("migrations() defines reserved ids %v: pick an id outside the reservations, or delete the entry if this branch owns the step", hits)
	}
}
