package app

import (
	"strings"
	"testing"
)

// The catalog-backed probes are an optimisation of the per-name ones, so the contract is that they give
// the same answer for every name the shape checks ask about, and for the names they must say no to: a
// missing table, a missing column of a present table, a column of a missing table, a different case.
func TestSchemaCatalogAnswersLikeTheProbes(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		st := platformTestStore(t, pg)
		plain := st.poolExec()
		cached, err := plain.withCatalog()
		if err != nil {
			t.Fatal(err)
		}
		if cached.cat == nil {
			t.Fatal("withCatalog returned an executor without a catalog")
		}

		var tables, indexes []string
		var cols []colRef
		for _, stmt := range st.baseSchemaStmts() {
			if isIndexDDL(stmt) {
				if name, ok := parseIndexName(stmt); ok {
					indexes = append(indexes, name)
				}
				continue
			}
			table, defs, ok := parseCreateTable(stmt)
			if !ok {
				continue
			}
			tables = append(tables, table)
			for _, c := range defs {
				cols = append(cols, colRef{table, c.name})
			}
		}
		for _, m := range st.migrations() {
			tables = append(tables, m.tables...)
			indexes = append(indexes, m.indices...)
			cols = append(cols, m.columns...)
			for _, r := range m.renames {
				cols = append(cols, colRef{r.table, r.from}, colRef{r.table, r.to})
			}
		}
		if len(tables) == 0 || len(indexes) == 0 || len(cols) == 0 {
			t.Fatalf("nothing declared: %d tables, %d indexes, %d columns", len(tables), len(indexes), len(cols))
		}
		tables = append(tables, "no_such_table", strings.ToUpper(tables[0]))
		indexes = append(indexes, "no_such_index", strings.ToUpper(indexes[0]))
		cols = append(cols, colRef{cols[0].table, "no_such_column"}, colRef{"no_such_table", cols[0].name},
			colRef{cols[0].table, strings.ToUpper(cols[0].name)}, colRef{strings.ToUpper(cols[0].table), cols[0].name})

		present := 0
		for _, name := range tables {
			want := plain.tableExists(name)
			if got := cached.tableExists(name); got != want {
				t.Errorf("tableExists(%q) = %v from the catalog, %v from the probe", name, got, want)
			}
			if want {
				present++
			}
		}
		for _, name := range indexes {
			if got, want := cached.indexExists(name), plain.indexExists(name); got != want {
				t.Errorf("indexExists(%q) = %v from the catalog, %v from the probe", name, got, want)
			}
		}
		for _, c := range cols {
			if got, want := cached.columnExists(c.table, c.name), plain.columnExists(c.table, c.name); got != want {
				t.Errorf("columnExists(%q, %q) = %v from the catalog, %v from the probe", c.table, c.name, got, want)
			}
		}
		if present == 0 {
			t.Fatal("no declared table exists: the comparison proved nothing")
		}
	})
}

// A step's products are verified inside its own transaction, so a catalog read through that transaction
// must see the step's uncommitted DDL, and a catalog read before the DDL must not be what answers after it.
func TestSchemaCatalogReadsThroughTheTransaction(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		st := platformTestStore(t, pg)
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck // the table must not outlive the test
		e := migExec{s: st, ex: tx}
		before, err := e.withCatalog()
		if err != nil {
			t.Fatal(err)
		}
		if before.tableExists("catalog_probe_tx") {
			t.Fatal("the table exists before it was created")
		}
		if _, err := e.exec(`CREATE TABLE catalog_probe_tx (id INTEGER PRIMARY KEY, label TEXT)`); err != nil {
			t.Fatal(err)
		}
		if _, err := e.exec(`CREATE INDEX idx_catalog_probe_tx_label ON catalog_probe_tx(label)`); err != nil {
			t.Fatal(err)
		}
		after, err := e.withCatalog()
		if err != nil {
			t.Fatal(err)
		}
		if !after.tableExists("catalog_probe_tx") || !after.columnExists("catalog_probe_tx", "label") ||
			!after.indexExists("idx_catalog_probe_tx_label") {
			t.Fatal("a catalog read through the transaction does not see its uncommitted DDL")
		}
		if after.columnExists("catalog_probe_tx", "missing") {
			t.Fatal("a column that was never created is reported present")
		}
	})
}
