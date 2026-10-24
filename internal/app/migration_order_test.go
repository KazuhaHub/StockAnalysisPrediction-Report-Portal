package app

import (
	"slices"
	"testing"
)

// The migration list is ordered by when steps reached main, not by id. A backup's header declares the
// steps its database had as a prefix of this list (checkDumpMigrations), so a step that lands later must
// be appended at the end even when its id is smaller: ids such as 0007-0009 are reserved by branches
// still in flight and are labels, not positions. Inserting a late step before one that has shipped would
// make every backup written by that release unrestorable.
//
// When a branch adds a step, append its id here as well. A rebase that resolves the migrations() line
// by sorting the ids fails this test.
var migrationListOrder = []string{"0001", "0002", "0003", "0004", "0005", "0006", "0010", "0009"}

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
