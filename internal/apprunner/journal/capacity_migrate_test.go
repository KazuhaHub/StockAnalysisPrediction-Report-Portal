//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"testing"
)

func TestMigrationJobHoldsDeclaredResourcesThenReleasesWhenAbsent(t *testing.T) {
	m := migrateDataJournalFixture(t)
	if err := m.f.store.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.dispatch(t); err != nil {
		t.Fatal(err)
	}
	var kind, state string
	var memory, cpu int64
	if err := m.f.store.db.QueryRow(`SELECT kind,state,memory_mib,cpu_millis FROM runner_reservations WHERE resource_id=?`, m.command.Payload.ResourceID).Scan(&kind, &state, &memory, &cpu); err != nil {
		t.Fatalf("no reservation: %v", err)
	}
	// The job finished and its container is proven absent in the settling transaction, so it is released.
	if kind != "migration" || state != "released" || memory == 0 || cpu == 0 {
		t.Fatalf("reservation = %s %s %d %d", kind, state, memory, cpu)
	}
}

func TestMigrationJobRefusedRunsNothingAndReplays(t *testing.T) {
	m := migrateDataJournalFixture(t)
	budgets := roomyBudgets()
	budgets.MemoryMiB = 1
	if err := m.f.store.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	_, err := m.dispatch(t)
	var refusal *CapacityRefusalError
	if !errors.As(err, &refusal) || refusal.Receipt.Dimension != CapacityMemory || refusal.Receipt.Remaining != "1" {
		t.Fatalf("err = %v", err)
	}
	if m.native.runs != 0 {
		t.Fatal("a refused migration ran a job")
	}
	db := m.f.store.db
	if scalar(t, db, `SELECT COUNT(*) FROM runner_resources WHERE id='`+m.command.Payload.ResourceID+`'`) != 0 || scalar(t, db, `SELECT COUNT(*) FROM runner_reservations`) != 0 {
		t.Fatal("a refused migration left a row")
	}
	if scalar(t, db, `SELECT COUNT(*) FROM runner_commands WHERE kind='migrate-data' AND state='reverted'`) != 1 {
		t.Fatal("refused migration not reverted")
	}
	_, err = m.dispatch(t)
	var again *CapacityRefusalError
	if !errors.As(err, &again) || again.Receipt != refusal.Receipt {
		t.Fatalf("replay = %v", err)
	}
	got, err := m.f.store.ReadMigrateDataOrStatus(context.Background(), m.raw, migrateRead(t, m.raw))
	if receipt, ok := got.(CapacityRefusal); err != nil || !ok || receipt != refusal.Receipt {
		t.Fatalf("read-back = %#v %v", got, err)
	}
	if m.native.runs != 0 {
		t.Fatal("a replay ran a job")
	}
}

func TestMigrationJobDoesNotCountAgainstTheBackendLimit(t *testing.T) {
	m := migrateDataJournalFixture(t)
	budgets := roomyBudgets()
	budgets.RunningBackends = 1
	if err := m.f.store.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	if _, err := m.f.store.db.Exec(`INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms) VALUES(?,?,?,'backend',16,25,0,0,'held',1,1)`, fixtureID(8201), fixtureID(8202), fixtureID(8203)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.dispatch(t); err != nil {
		t.Fatalf("migration refused by the backend limit: %v", err)
	}
}
