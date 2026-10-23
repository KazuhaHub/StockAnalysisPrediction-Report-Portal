package app

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPredictionPurgeOwnerStore(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { testPredictionPurgeOwnerStore(t, newTestStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		testPredictionPurgeOwnerStore(t, pgStore(t, "prediction_claims", "prediction_marks", "prediction_sets", "prediction_purges"))
	})
}

func TestPostgresPredictionPurgeOwnershipSerialization(t *testing.T) {
	st := pgStore(t)
	if err := createPredictionPurgeOwners(st.poolExec()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := createPredictionPurgeOwners(st.poolExec()); err != nil {
			t.Error(err)
		}
		if _, err := st.exec(`DELETE FROM prediction_purge_owners`); err != nil {
			t.Error(err)
		}
	})
	child := seedOU(t, st, 0, scopeOwn)
	parent := seedOU(t, st, 0, scopeOwn)
	first, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback()
	if err := lockPredictionOwnership(migExec{s: st, ex: first}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Exec(st.bind(`INSERT INTO prediction_purge_owners(rtype,cohort,attributed_ou) VALUES(?,?,?)`), predTestRtype, "1901-01", child); err != nil {
		t.Fatal(err)
	}
	// The concurrent delete must wait before its first read, not miss an uncommitted snapshot.
	second, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Rollback()
	if _, err := second.Exec(`SET LOCAL lock_timeout='100ms'`); err != nil {
		t.Fatal(err)
	}
	err = lockPredictionOwnership(migExec{s: st, ex: second})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("concurrent ownership transaction did not wait for the lock: %v", err)
	}
	if err := second.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	third, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer third.Rollback()
	if err := lockPredictionOwnership(migExec{s: st, ex: third}); err != nil {
		t.Fatal(err)
	}
	if err := reattributePredictionPurgeOwners(migExec{s: st, ex: third}, child, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := third.Exec(st.bind(`DELETE FROM user_groups WHERE id=?`), child); err != nil {
		t.Fatal(err)
	}
	if err := third.Commit(); err != nil {
		t.Fatal(err)
	}
	var owner int64
	if err := st.queryRow(`SELECT attributed_ou FROM prediction_purge_owners WHERE cohort='1901-01'`).Scan(&owner); err != nil || owner != parent {
		t.Fatalf("ownership transfer after serialization = %d, %v; want %d", owner, err, parent)
	}
}

func testPredictionPurgeOwnerStore(t *testing.T, st *Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := createPredictionPurgeOwners(st.poolExec()); err != nil {
			t.Error(err)
			return
		}
		if _, err := st.exec(`DELETE FROM prediction_purge_owners`); err != nil {
			t.Error(err)
		}
	})
	// Exercise the idempotent explicit creation primitive against a freshly initialized test store.
	if err := createPredictionPurgeOwners(st.poolExec()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := createPredictionPurgeOwners(st.poolExec()); err != nil {
			t.Error(err)
		}
		if _, err := st.exec(`DELETE FROM prediction_purge_owners`); err != nil {
			t.Error(err)
		}
	})
	if _, err := st.exec(`DELETE FROM prediction_purge_owners`); err != nil {
		t.Fatal(err)
	}
	if err := lockPredictionOwnership(st.poolExec()); err == nil {
		t.Fatal("ownership lock accepted a pooled, nontransactional handle")
	}
	if err := createPredictionPurgeOwners(st.poolExec()); err != nil {
		t.Fatalf("idempotent create: %v", err)
	}
	for _, owner := range []any{nil, int64(0), int64(-1)} {
		if _, err := st.exec(`INSERT INTO prediction_purge_owners(rtype,cohort,attributed_ou) VALUES(?,?,?)`, predTestRtype, "1900-01", owner); err == nil {
			t.Fatalf("invalid ownership %v accepted", owner)
		}
	}
	a := seedOU(t, st, 0, scopeOwn)
	b := seedOU(t, st, a, nil)
	other := seedOU(t, st, 0, scopeOwn)
	for i, owner := range []any{a, b, b, other, nil} {
		id := seedPredictionSetForKey(t, st, "2020-01", sql.NullInt64{}, "purge-owner-"+itoa64(int64(i)))
		if _, err := st.exec(`UPDATE prediction_sets SET attributed_ou=?, rtype=? WHERE id=?`, owner, predTestRtype, id); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := capturePredictionPurgeOwners(migExec{s: st, ex: tx}, predTestRtype, "2020-01"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	// Repeating the snapshot is idempotent even when the source contains duplicate owners.
	if err := capturePredictionPurgeOwners(migExec{s: st, ex: tx}, predTestRtype, "2020-01"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_purge_owners`); n != 3 {
		t.Fatalf("distinct non-null owners = %d, want 3", n)
	}
	for _, month := range []string{"2020-01", "2019-12"} {
		if _, err := st.exec(`INSERT INTO prediction_purges(rtype,cohort,purged_at,sets_removed) VALUES(?,?,?,1)`, predTestRtype, month, "2026-10-22T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	check := func(pop forecastPopulation, want int) {
		t.Helper()
		got, err := st.loadPredictionPurgeMarkersForPopulation(pop)
		if err != nil || len(got) != want {
			t.Fatalf("population %+v: markers %v, err %v; want %d", pop, got, err, want)
		}
		if pop.OUs != nil && want == 1 && got[[2]string{predTestRtype, "2020-01"}] != "2026-10-22T00:00:00Z" {
			t.Fatalf("own response substituted a legacy/unrelated marker: %v", got)
		}
	}
	check(forecastPopulation{}, 2)
	check(forecastPopulation{OUs: []int64{}}, 0)
	check(forecastPopulation{OUs: []int64{a, b}}, 1)
	check(forecastPopulation{OUs: []int64{b}}, 1)
	check(forecastPopulation{OUs: []int64{other}}, 1)
	check(forecastPopulation{OUs: []int64{other + 999}}, 0)
	// Moving a child into an already represented parent must deduplicate, not fail or widen history.
	tx, err = st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := reattributePredictionPurgeOwners(migExec{s: st, ex: tx}, b, a); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	check(forecastPopulation{OUs: []int64{b}}, 0)
	check(forecastPopulation{OUs: []int64{a}}, 1)
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_purge_owners`); n != 2 {
		t.Fatalf("deduplicated owners = %d, want 2", n)
	}
	// The caller's rollback must restore both transfer and removal.
	tx, err = st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := reattributePredictionPurgeOwners(migExec{s: st, ex: tx}, a, other); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check(forecastPopulation{OUs: []int64{a}}, 1)
	// A missing fallback OU removes the old id rather than authorizing its future replacement.
	tx, err = st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := reattributePredictionPurgeOwners(migExec{s: st, ex: tx}, a, 0); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	check(forecastPopulation{OUs: []int64{a}}, 0)
	// An unprovable population fails closed if its evidence storage is unavailable.
	if _, err := st.exec(`DROP TABLE prediction_purge_owners`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.loadPredictionPurgeMarkersForPopulation(forecastPopulation{OUs: []int64{a}}); err == nil {
		t.Fatal("missing ownership evidence did not fail closed")
	}
	check(forecastPopulation{}, 2)
}
