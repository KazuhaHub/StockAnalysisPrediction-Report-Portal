package app

import (
	"database/sql"
	"fmt"
	"math"
)

type restoreIdentityFloor struct {
	sequence string
	next     int64
	maximum  int64
}

// PostgreSQL sequence changes do not roll back. Capture both live-row and
// sequence/cache high-water marks before clearing any table, so a failed restore
// cannot rewind allocation into rows that the transaction subsequently restores.
// Restore remains an offline operation, not a concurrent-write protocol.
func (s *Store) captureIdentityFloors(tx *sql.Tx, identity map[string]string) (map[string]restoreIdentityFloor, error) {
	floors := make(map[string]restoreIdentityFloor, len(identity))
	for table, column := range identity {
		relation := quoteIdent("public") + "." + quoteIdent(table)
		var schema, name string
		if err := tx.QueryRow(`SELECT n.nspname,c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE c.oid=pg_get_serial_sequence($1,$2)::regclass`, relation, column).Scan(&schema, &name); err != nil {
			return nil, err
		}
		sequence := quoteIdent(schema) + "." + quoteIdent(name)
		var high, live int64
		var called bool
		if err := tx.QueryRow(`SELECT last_value,is_called FROM `+sequence).Scan(&high, &called); err != nil {
			return nil, err
		}
		if err := tx.QueryRow(`SELECT COALESCE(MAX(` + quoteIdent(column) + `),0) FROM ` + relation).Scan(&live); err != nil {
			return nil, err
		}
		var increment, maximum int64
		var cycle bool
		if err := tx.QueryRow(`SELECT seqincrement,seqmax,seqcycle FROM pg_sequence WHERE seqrelid=$1::regclass`, sequence).Scan(&increment, &maximum, &cycle); err != nil {
			return nil, err
		}
		if increment != 1 || cycle {
			return nil, fmt.Errorf("identity %s.%s has an unsupported allocation policy", table, column)
		}
		if live == math.MaxInt64 || (called && high == math.MaxInt64) {
			return nil, fmt.Errorf("identity %s.%s is exhausted", table, column)
		}
		next := high
		if called {
			next++
		}
		if next < live+1 {
			next = live + 1
		}
		if next > maximum {
			return nil, fmt.Errorf("identity %s.%s is exhausted", table, column)
		}
		floors[table] = restoreIdentityFloor{sequence: sequence, next: next, maximum: maximum}
	}
	return floors, nil
}

func (s *Store) advanceRestoreIdentities(tx *sql.Tx, identity map[string]string, floors map[string]restoreIdentityFloor) error {
	for table, column := range identity {
		floor, ok := floors[table]
		if !ok {
			return fmt.Errorf("missing identity floor for %s", table)
		}
		var restored int64
		relation := quoteIdent("public") + "." + quoteIdent(table)
		if err := tx.QueryRow(`SELECT COALESCE(MAX(` + quoteIdent(column) + `),0) FROM ` + relation).Scan(&restored); err != nil {
			return err
		}
		if restored == math.MaxInt64 {
			return fmt.Errorf("restored identity %s.%s is exhausted", table, column)
		}
		next := max(restored+1, floor.next)
		if next > floor.maximum {
			return fmt.Errorf("restored identity %s.%s is exhausted", table, column)
		}
		if _, err := tx.Exec(`SELECT setval($1::regclass,$2,false)`, floor.sequence, next); err != nil {
			return fmt.Errorf("advance %s.%s sequence: %w", table, column, err)
		}
	}
	return nil
}
