//go:build linux || darwin

package journal

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// versionJournal creates an empty journal as the binary of schema version v wrote it, closed.
func versionJournal(t *testing.T, version int) string {
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
	if err := initializeVersion(context.Background(), db, version); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestJournalIsVersionFourWithTheBudgetTable(t *testing.T) {
	if schemaVersion != 5 {
		t.Fatalf("schema version %d", schemaVersion)
	}
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if scalar(t, store.db, `SELECT COUNT(*) FROM sqlite_schema WHERE name='runner_proxy_budget'`) != 1 {
		t.Fatal("a fresh journal has no budget table")
	}
}

// A v3 journal upgrades by adding the table: its rows stay and it is then exactly a v5 journal.
func TestV3JournalUpgradesToTheBudgetTable(t *testing.T) {
	directory := versionJournal(t, 3)
	db, err := sql.Open(journalDriver, journalDSN(directory, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	if err := insertDeployment(db); err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := Open(directory)
	if err != nil {
		t.Fatalf("a v3 journal was not upgraded: %v", err)
	}
	defer store.Close()
	if scalar(t, store.db, `PRAGMA user_version`) != 5 || scalar(t, store.db, `SELECT COUNT(*) FROM runner_deployment`) != 1 {
		t.Fatal("upgrade lost the journal's rows or did not set the version")
	}
	if _, err := inspect(context.Background(), store.db); err != nil {
		t.Fatalf("upgraded journal fails inspection: %v", err)
	}
	if err := store.AddProxyBudget(context.Background(), fixtureID(5), 20000, 10); err != nil {
		t.Fatal(err)
	}
}

// Every older version reaches v4 in one open, and a journal from a newer binary is still refused.
func TestOlderJournalsUpgradeToVersionFour(t *testing.T) {
	store, err := Open(v1Journal(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if scalar(t, store.db, `PRAGMA user_version`) != 5 || scalar(t, store.db, `SELECT COUNT(*) FROM sqlite_schema WHERE name='runner_proxy_budget'`) != 1 {
		t.Fatal("v1 journal did not reach v5")
	}
}

func TestProxyBudgetCountsAndSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	directory := privateDirectory(t)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	a, b := fixtureID(5), fixtureID(6)
	for _, add := range []struct {
		id    string
		day   int64
		bytes int64
	}{{a, 20000, 100}, {a, 20000, 50}, {a, 20001, 7}, {b, 20000, 9}} {
		if err := store.AddProxyBudget(ctx, add.id, add.day, add.bytes); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := store.ProxyBudgetUsed(ctx, a, 20000); err != nil || got != 150 {
		t.Fatalf("used %d %v", got, err)
	}
	if got, _ := store.ProxyBudgetUsed(ctx, a, 20002); got != 0 {
		t.Fatalf("an untouched day has %d", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.ProxyBudgetUsed(ctx, a, 20000); err != nil || got != 150 {
		t.Fatalf("after a restart the budget is %d %v", got, err)
	}
	if got, _ := reopened.ProxyBudgetUsed(ctx, b, 20000); got != 9 {
		t.Fatalf("another installation's budget is %d", got)
	}
}

func TestProxyBudgetRefusesNonsenseAndPrunesOldDays(t *testing.T) {
	ctx := context.Background()
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := fixtureID(5)
	for _, bad := range []struct {
		id         string
		day, bytes int64
	}{{"not-an-id", 1, 1}, {a, -1, 1}, {a, 1, -1}} {
		if err := store.AddProxyBudget(ctx, bad.id, bad.day, bad.bytes); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	for day := int64(20000); day < 20010; day++ {
		if err := store.AddProxyBudget(ctx, a, day, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PruneProxyBudget(ctx, 20008); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, store.db, `SELECT COUNT(*) FROM runner_proxy_budget`); n != 2 {
		t.Fatalf("%d rows after pruning, want the two newest days", n)
	}
	if got, _ := store.ProxyBudgetUsed(ctx, a, 20007); got != 0 {
		t.Fatal("a pruned day still counts")
	}
	if got, _ := store.ProxyBudgetUsed(ctx, a, 20009); got != 1 {
		t.Fatal("a kept day was pruned")
	}
}
