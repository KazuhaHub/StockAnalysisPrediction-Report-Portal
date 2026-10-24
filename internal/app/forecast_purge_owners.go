package app

import (
	"database/sql"
	"fmt"
	"strings"
)

// Purge ownership is evidence, not a reconstruction from today's report grants. The table is step
// 0009: ordinary startup creates it empty on an existing database and verifies its exact shape on
// every start.

// lockPredictionOwnership must be the first database operation in both the retention and OU-delete
// transactions, before inspecting or reassigning sets. Holding it only around the evidence writes
// is too late: a concurrent delete could already have transferred the sets and missed new evidence.
// Postgres uses a transaction-scoped lock; SQLite reserves its single writer before any snapshot.
func lockPredictionOwnership(e migExec) error {
	if _, ok := e.ex.(*sql.Tx); !ok {
		return fmt.Errorf("prediction ownership requires a transaction")
	}
	if e.s.driver == "postgres" {
		_, err := e.exec(`SELECT pg_advisory_xact_lock(1380994896, 2)`)
		return err
	}
	_, err := e.exec(`UPDATE meta SET v=v WHERE k='schema_version'`)
	return err
}

const predictionPurgeOwnersDDL = `CREATE TABLE IF NOT EXISTS prediction_purge_owners (
		rtype TEXT NOT NULL,
		cohort TEXT NOT NULL,
		attributed_ou BIGINT NOT NULL CHECK (attributed_ou > 0),
		PRIMARY KEY (rtype, cohort, attributed_ou)
	)`

// createPredictionPurgeOwners is step 0009's DDL, run by the migration runner on a fresh database
// and on an upgraded one alike. It creates the table empty: historic markers are never backfilled.
func createPredictionPurgeOwners(e migExec) error {
	_, err := e.exec(predictionPurgeOwnersDDL)
	return err
}

// A ledger row and column names alone cannot prove non-null, exact integer ownership keys.
func verifyPredictionPurgeOwners(e migExec) error {
	query := `SELECT name, UPPER(type), "notnull", pk FROM pragma_table_info('prediction_purge_owners')`
	if e.s.driver == "postgres" {
		query = `SELECT c.column_name, UPPER(c.data_type), CASE WHEN c.is_nullable='NO' THEN 1 ELSE 0 END,
			COALESCE((SELECT k.ordinal_position FROM information_schema.table_constraints t
			JOIN information_schema.key_column_usage k ON k.constraint_name=t.constraint_name
			AND k.table_schema=t.table_schema AND k.table_name=t.table_name
			WHERE t.table_schema='public' AND t.table_name=c.table_name
			AND t.constraint_type='PRIMARY KEY' AND k.column_name=c.column_name),0)
			FROM information_schema.columns c WHERE c.table_schema='public' AND c.table_name='prediction_purge_owners'`
	}
	rows, err := e.query(query)
	if err != nil {
		return err
	}
	want := map[string]string{"rtype": "TEXT:1:1", "cohort": "TEXT:1:2", "attributed_ou": "BIGINT:1:3"}
	keys := 0
	for rows.Next() {
		var name, typ string
		var required, key int
		if err := rows.Scan(&name, &typ, &required, &key); err != nil {
			rows.Close()
			return err
		}
		if key > 0 {
			keys++
		}
		if expected, ok := want[name]; ok {
			if fmt.Sprintf("%s:%d:%d", typ, required, key) != expected {
				rows.Close()
				return fmt.Errorf("prediction_purge_owners has incompatible type/nullability/key for %s", name)
			}
			delete(want, name)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(want) != 0 || keys != 3 {
		return fmt.Errorf("prediction_purge_owners does not have its exact three-column primary key")
	}
	query = `SELECT sql FROM sqlite_master WHERE type='table' AND name='prediction_purge_owners'`
	if e.s.driver == "postgres" {
		query = `SELECT COALESCE(string_agg(pg_get_constraintdef(oid),' '),'') FROM pg_constraint
			WHERE conrelid='public.prediction_purge_owners'::regclass AND contype='c' AND convalidated`
	}
	var checks string
	if err := e.queryRow(query).Scan(&checks); err != nil {
		return err
	}
	canonical := func(text string) string {
		clean := strings.NewReplacer(" ", "", "\n", "", "\t", "", "\r", "").Replace(strings.ToLower(text))
		return strings.ReplaceAll(clean, "ifnotexists", "")
	}
	clean := canonical(checks)
	// SQLite exposes the table declaration rather than parsed check constraints. Require our
	// canonical declaration, so a comment or a quoted literal cannot impersonate an active CHECK.
	if e.s.driver != "postgres" && clean != canonical(predictionPurgeOwnersDDL) {
		return fmt.Errorf("prediction_purge_owners has a noncanonical SQLite constraint declaration")
	}
	if !strings.Contains(clean, "check(attributed_ou>0)") && !strings.Contains(clean, "check((attributed_ou>0))") {
		return fmt.Errorf("prediction_purge_owners is missing its positive-owner constraint")
	}
	return nil
}

// capturePredictionPurgeOwners runs before deleting sets, on the caller's purge transaction.
// NULL/dangling attribution proves nothing; historical markers are never backfilled by guessing.
func capturePredictionPurgeOwners(e migExec, rtype, cohort string) error {
	if err := lockPredictionOwnership(e); err != nil {
		return err
	}
	_, err := e.exec(`INSERT INTO prediction_purge_owners(rtype, cohort, attributed_ou)
		SELECT DISTINCT ps.rtype, ps.cohort, ps.attributed_ou FROM prediction_sets ps
		JOIN user_groups ug ON ug.id=ps.attributed_ou
		WHERE ps.rtype=? AND ps.cohort=? AND ps.attributed_ou > 0
		ON CONFLICT(rtype, cohort, attributed_ou) DO NOTHING`, rtype, cohort)
	return err
}

// reattributePredictionPurgeOwners shares the OU-deletion transaction with set reassignment.
// Inserting before deleting deduplicates evidence already held by the destination. A missing
// destination drops unprovable evidence rather than retaining an id that could be reused.
func reattributePredictionPurgeOwners(e migExec, from, to int64) error {
	if err := lockPredictionOwnership(e); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	if to > 0 {
		if _, err := e.exec(`INSERT INTO prediction_purge_owners(rtype, cohort, attributed_ou)
			SELECT rtype, cohort, ? FROM prediction_purge_owners WHERE attributed_ou=?
			ON CONFLICT(rtype, cohort, attributed_ou) DO NOTHING`, to, from); err != nil {
			return err
		}
	}
	_, err := e.exec(`DELETE FROM prediction_purge_owners WHERE attributed_ou=?`, from)
	return err
}

// loadPredictionPurgeMarkersForPopulation distinguishes nil (all) from an empty own subtree.
// EXISTS prevents a month shared by multiple descendants from multiplying its visible marker.
func (s *Store) loadPredictionPurgeMarkersForPopulation(pop forecastPopulation) (map[[2]string]string, error) {
	if pop.OUs == nil {
		return s.loadPredictionPurgeMarkers()
	}
	out := map[[2]string]string{}
	if len(pop.OUs) == 0 {
		return out, nil
	}
	args := make([]any, len(pop.OUs))
	for i, id := range pop.OUs {
		args[i] = id
	}
	rows, err := s.query(`SELECT p.rtype, p.cohort, p.purged_at FROM prediction_purges p
		WHERE EXISTS (SELECT 1 FROM prediction_purge_owners o
		WHERE o.rtype=p.rtype AND o.cohort=p.cohort AND o.attributed_ou IN (`+placeholders(len(args))+`))`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rtype, cohort, at string
		if err := rows.Scan(&rtype, &cohort, &at); err != nil {
			return nil, err
		}
		out[[2]string{rtype, cohort}] = at
	}
	return out, rows.Err()
}
