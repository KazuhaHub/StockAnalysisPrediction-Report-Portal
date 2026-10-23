package app

import (
	"database/sql"
	"fmt"
)

// Purge ownership is evidence, not a reconstruction from today's report grants. These primitives
// are intentionally not registered with startup migrations: existing databases require an explicit
// operator upgrade before the read/cleanup/delete paths can adopt this contract.

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

// createPredictionPurgeOwners is shared by explicit upgrade and fresh-database initialization.
// It must never be called as an implicit repair of an existing database at server startup.
func createPredictionPurgeOwners(e migExec) error {
	_, err := e.exec(`CREATE TABLE IF NOT EXISTS prediction_purge_owners (
		rtype TEXT NOT NULL,
		cohort TEXT NOT NULL,
		attributed_ou BIGINT NOT NULL CHECK (attributed_ou > 0),
		PRIMARY KEY (rtype, cohort, attributed_ou)
	)`)
	return err
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
