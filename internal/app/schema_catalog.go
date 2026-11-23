package app

import "strings"

// schemaCatalog is one read of the database's tables, columns and indexes, so that a check which asks
// about hundreds of names asks the database three times instead of once per name. The start-up shape
// checks (checkBaseShape, verifyMigrationProducts) ask about every declared table, column and index;
// on PostgreSQL that was about 1,800 round trips per OpenStore.
//
// It answers exactly what the per-name probes in migrate_steps.go answer, from the same catalog views
// and the same filters, and the equivalence is tested. It is a snapshot: an executor holding one must
// not be used across DDL, which is why the checks take one for their read-only loops only.
type schemaCatalog struct {
	tables  map[string]bool
	indexes map[string]bool
	// columns is keyed by columnKey. SQLite resolves PRAGMA table_info's table name without regard to
	// case, which the probe relies on, so its keys fold the table name; PostgreSQL compares exactly.
	columns map[string]bool
	fold    bool
}

func (c *schemaCatalog) columnKey(table, col string) string {
	if c.fold {
		table = strings.ToLower(table)
	}
	return table + "\x00" + col
}

// withCatalog returns a copy of e whose tableExists, indexExists and columnExists answer from one read
// of the catalog, made through e itself (inside e's transaction when it has one).
func (e migExec) withCatalog() (migExec, error) {
	cat := &schemaCatalog{tables: map[string]bool{}, indexes: map[string]bool{}, columns: map[string]bool{}}
	tablesQ := `SELECT name FROM sqlite_master WHERE type='table'`
	indexesQ := `SELECT name FROM sqlite_master WHERE type='index'`
	columnsQ := `SELECT m.name, p.name FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type='table'`
	if e.s.driver == "postgres" {
		tablesQ = `SELECT table_name FROM information_schema.tables WHERE table_schema='public'`
		indexesQ = `SELECT indexname FROM pg_indexes WHERE schemaname='public'`
		columnsQ = `SELECT table_name, column_name FROM information_schema.columns WHERE table_schema='public'`
	} else {
		cat.fold = true
	}
	for _, set := range []struct {
		q   string
		dst map[string]bool
	}{{tablesQ, cat.tables}, {indexesQ, cat.indexes}} {
		if err := e.scanNames(set.q, func(v ...string) { set.dst[v[0]] = true }, 1); err != nil {
			return e, err
		}
	}
	if err := e.scanNames(columnsQ, func(v ...string) { cat.columns[cat.columnKey(v[0], v[1])] = true }, 2); err != nil {
		return e, err
	}
	e.cat = cat
	return e, nil
}

func (e migExec) scanNames(q string, add func(...string), n int) error {
	rows, err := e.query(q)
	if err != nil {
		return err
	}
	defer rows.Close()
	vals := make([]string, n)
	ptrs := make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		add(vals...)
	}
	return rows.Err()
}
