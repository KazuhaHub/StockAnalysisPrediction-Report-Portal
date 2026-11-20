package app

import (
	"fmt"
	"slices"
	"strconv"
)

// Step 0021: restore onto a fresh host (doc 168, step 8).
//
// It adds the single-use import grant that authorises one checkpoint import, and widens the action
// list of review_plans and operations by one value, restore_import. The widening is a strict superset
// of the list 0015 created, so no row changes and no table is rebuilt.

// platformActionCheckedTables are the tables whose action column carries platformActions.
var platformActionCheckedTables = []string{"review_plans", "operations"}

// platformLegacySchemaTables is the control schema as step 0015 created it: the current tables with
// the frozen action and stage lists and without quota_inodes. Step 0015 must keep producing this shape;
// steps 0021 and 0024 widen it afterwards, for fresh and upgraded databases alike.
func platformLegacySchemaTables() []platformTable {
	return platformSchemaTablesAt(platformLegacyActions(), platformLegacyStages(), false)
}

// platformSchemaTables0021 is the shape step 0021 leaves: the 0021 action list, still the legacy stages.
func platformSchemaTables0021() []platformTable {
	return platformSchemaTablesAt(platformActions0021(), platformLegacyStages(), false)
}

func platformImportGrantSchemaTables() []platformTable {
	ref := func(columns, target, targetColumns string) platformReference {
		return platformReference{columns, platformPrefix + target, targetColumns, "NO ACTION"}
	}
	return []platformTable{{
		name:    "import_grants",
		fields:  "id:ID recovery_set_id:ID runtime_id:ID controller_epoch:ID state:T16 consumed_ms:MS? created_ms:MS updated_ms:MS",
		primary: "id",
		references: []platformReference{
			ref("recovery_set_id", "recovery_sets", "id"),
			ref("runtime_id", "runtime_bindings", "id"),
		},
		checks: []string{
			platformEnum("state", "issued", "consumed", "revoked"),
			"((state='consumed') AND (consumed_ms IS NOT NULL)) OR ((state<>'consumed') AND (consumed_ms IS NULL))",
		},
	}}
}

func platformImportGrantSchemaIndexes() []platformIndex {
	return []platformIndex{
		// A revoked grant is history: it must not stop the same recovery set being imported again
		// onto the same runtime.
		{"uq_ap_import_grant_runtime", "import_grants", "recovery_set_id,runtime_id", "state<>'revoked'", true},
		{"uq_ap_import_grant_issued", "import_grants", "recovery_set_id", "state='issued'", true},
		{"idx_ap_import_grant_state", "import_grants", "runtime_id,state", "", false},
	}
}

// platformCheckName is the PostgreSQL constraint name of the check on table that renders as expr. The
// name follows from the declaration (the position of the expression among the table's checks), so it
// is derived rather than repeated.
func platformCheckName(tables []platformTable, table, expr string) (string, error) {
	for _, t := range tables {
		if t.name != table {
			continue
		}
		for i, check := range t.checkExpressions("postgres") {
			if check == expr {
				return platformPrefix + t.name + "_ck_" + strconv.Itoa(i+1), nil
			}
		}
	}
	return "", fmt.Errorf("table %s has no check %q", table, expr)
}

// platformActionCheckName is the constraint name of the action list on one of the checked tables.
func platformActionCheckName(table string) (string, error) {
	return platformCheckName(platformSchemaTables(), table, platformActions())
}

// platformCheckTarget is one table whose stored CHECK list a step widens: the short table name and the
// PostgreSQL constraint that carries the list.
type platformCheckTarget struct{ table, constraint string }

// setPlatformActionChecks replaces the action list `from` with `to` on the checked tables, in place.
//
// PostgreSQL drops and re-adds the named constraint, and refuses a table that does not have it. SQLite cannot alter a CHECK, so the stored table
// text is rewritten under writable_schema: the new list is a strict superset, so every stored row
// already satisfies it and nothing on disk changes. The schema cookie is bumped so other connections
// reparse, and the rewritten tables are checked before the transaction may commit. The call is
// idempotent: a table that already carries `to` is left as it is, and one that carries neither is an
// error, never a guess.
func setPlatformActionChecks(e migExec, from, to string, later ...string) error {
	var targets []platformCheckTarget
	for _, table := range platformActionCheckedTables {
		name, err := platformActionCheckName(table)
		if err != nil {
			return err
		}
		targets = append(targets, platformCheckTarget{table, name})
	}
	return rewritePlatformChecks(e, targets, from, to, later...)
}

// rewritePlatformChecks is setPlatformActionChecks for any list of targets. later are lists a newer step
// widened `to` into: a table that already carries one of them is left as it is, because a database
// whose ledger was cleared (a baseline fixture) still has the newest shape.
func rewritePlatformChecks(e migExec, targets []platformCheckTarget, from, to string, later ...string) error {
	if e.s.driver == "postgres" {
		for _, target := range targets {
			name := target.constraint
			full := platformPrefix + target.table
			// The constraint must exist. Dropping it only if present would add a fresh one over a table
			// that is not the expected shape and hide that.
			var present int
			if err := e.queryRow(`SELECT COUNT(*) FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid
				WHERE t.relname=? AND c.conname=? AND c.contype='c' AND pg_table_is_visible(t.oid)`, full, name).Scan(&present); err != nil {
				return err
			}
			if present != 1 {
				return fmt.Errorf("application control table %s lacks its check %s", full, name)
			}
			if _, err := e.exec("ALTER TABLE " + full + " DROP CONSTRAINT " + name); err != nil {
				return err
			}
			if _, err := e.exec("ALTER TABLE " + full + " ADD CONSTRAINT " + name + " CHECK (" + to + ")"); err != nil {
				return err
			}
		}
		return nil
	}
	var cookie int64
	if err := e.queryRow(`PRAGMA schema_version`).Scan(&cookie); err != nil {
		return err
	}
	if _, err := e.exec(`PRAGMA writable_schema=ON`); err != nil {
		return err
	}
	// The flag belongs to the connection, not the transaction, so it is cleared even on failure.
	defer e.exec(`PRAGMA writable_schema=OFF`)
	for _, target := range targets {
		full := platformPrefix + target.table
		oldText, newText := "CHECK ("+from+")", "CHECK ("+to+")"
		result, err := e.exec(`UPDATE sqlite_master SET sql=replace(sql,?,?) WHERE type='table' AND name=? AND instr(sql,?)>0`,
			oldText, newText, full, oldText)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			var present int
			for _, accepted := range append([]string{to}, later...) {
				var n int
				if err := e.queryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=? AND instr(sql,?)>0`, full, "CHECK ("+accepted+")").Scan(&n); err != nil {
					return err
				}
				present += n
			}
			if present != 1 {
				return fmt.Errorf("application control table %s carries an unexpected check", full)
			}
		}
	}
	if _, err := e.exec(`PRAGMA writable_schema=OFF`); err != nil {
		return err
	}
	if _, err := e.exec(`PRAGMA schema_version=` + strconv.FormatInt(cookie+1, 10)); err != nil {
		return err
	}
	for _, target := range targets {
		var result string
		if err := e.queryRow(`PRAGMA integrity_check(` + platformPrefix + target.table + `)`).Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return fmt.Errorf("integrity check of %s failed after the check rewrite", platformPrefix+target.table)
		}
	}
	for _, target := range targets {
		rows, err := e.query(`PRAGMA foreign_key_check(` + platformPrefix + target.table + `)`)
		if err != nil {
			return err
		}
		violated := rows.Next()
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if violated {
			return fmt.Errorf("foreign key check of %s failed after the check rewrite", platformPrefix+target.table)
		}
	}
	return nil
}

func verifyPlatformImportGrantSchema(e migExec) error {
	only := func(tables []platformTable) []platformTable {
		var result []platformTable
		for _, t := range tables {
			if slices.Contains(platformActionCheckedTables, t.name) {
				result = append(result, t)
			}
		}
		return result
	}
	// Step 0024 widens the two checked tables again, so a later start sees either shape.
	tables := append(only(platformSchemaTables()), platformImportGrantSchemaTables()...)
	var indexes []platformIndex
	for _, i := range platformSchemaIndexes() {
		if slices.Contains(platformActionCheckedTables, i.table) {
			indexes = append(indexes, i)
		}
	}
	indexes = append(indexes, platformImportGrantSchemaIndexes()...)
	return verifyPlatformSchemaProductsWithAlternates(e, tables, indexes, only(platformSchemaTables0021()))
}

func migration0021() migration {
	tables, indexes := platformImportGrantSchemaTables(), platformImportGrantSchemaIndexes()
	m := migration{
		id:      "0021",
		restore: restoreRule{olderDumpOK: true},
		verify:  verifyPlatformImportGrantSchema,
	}
	for _, table := range tables {
		m.tables = append(m.tables, platformPrefix+table.name)
		for _, column := range table.columns() {
			m.columns = append(m.columns, colRef{platformPrefix + table.name, column.name})
		}
	}
	for _, index := range indexes {
		m.indices = append(m.indices, index.name)
	}
	m.up = func(e migExec) error {
		for _, table := range tables {
			if _, err := e.exec(table.ddl(e.s.driver)); err != nil {
				return err
			}
		}
		for _, index := range indexes {
			if _, err := e.exec(index.ddl()); err != nil {
				return err
			}
		}
		return setPlatformActionChecks(e, platformLegacyActions(), platformActions0021(), platformActions())
	}
	return m
}
