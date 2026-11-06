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
// the frozen action list on the two checked tables. Step 0015 must keep producing this shape; step
// 0021 widens it afterwards, for fresh and upgraded databases alike.
func platformLegacySchemaTables() []platformTable {
	tables := platformSchemaTables()
	for i, t := range tables {
		if !slices.Contains(platformActionCheckedTables, t.name) {
			continue
		}
		checks := slices.Clone(t.checks)
		for j, check := range checks {
			if check == platformActions() {
				checks[j] = platformLegacyActions()
			}
		}
		tables[i].checks = checks
	}
	return tables
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
		{"uq_ap_import_grant_runtime", "import_grants", "recovery_set_id,runtime_id", "", true},
		{"uq_ap_import_grant_issued", "import_grants", "recovery_set_id", "state='issued'", true},
		{"idx_ap_import_grant_state", "import_grants", "runtime_id,state", "", false},
	}
}

// platformActionCheckName is the constraint name of the action list on one of the checked tables. The
// name follows from the declaration, so it is derived rather than repeated.
func platformActionCheckName(table string) (string, error) {
	for _, t := range platformSchemaTables() {
		if t.name != table {
			continue
		}
		for i, check := range t.checkExpressions("postgres") {
			if check == platformActions() {
				return platformPrefix + t.name + "_ck_" + strconv.Itoa(i+1), nil
			}
		}
	}
	return "", fmt.Errorf("table %s has no action check", table)
}

// setPlatformActionChecks replaces the action list `from` with `to` on the checked tables, in place.
//
// PostgreSQL drops and re-adds the named constraint. SQLite cannot alter a CHECK, so the stored table
// text is rewritten under writable_schema: the new list is a strict superset, so every stored row
// already satisfies it and nothing on disk changes. The schema cookie is bumped so other connections
// reparse, and the rewritten tables are checked before the transaction may commit. The call is
// idempotent: a table that already carries `to` is left as it is, and one that carries neither is an
// error, never a guess.
func setPlatformActionChecks(e migExec, from, to string) error {
	if e.s.driver == "postgres" {
		for _, table := range platformActionCheckedTables {
			name, err := platformActionCheckName(table)
			if err != nil {
				return err
			}
			full := platformPrefix + table
			if _, err := e.exec("ALTER TABLE " + full + " DROP CONSTRAINT IF EXISTS " + name); err != nil {
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
	for _, table := range platformActionCheckedTables {
		full := platformPrefix + table
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
			if err := e.queryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=? AND instr(sql,?)>0`, full, newText).Scan(&present); err != nil {
				return err
			}
			if present != 1 {
				return fmt.Errorf("application control table %s carries an unexpected action check", full)
			}
		}
	}
	if _, err := e.exec(`PRAGMA writable_schema=OFF`); err != nil {
		return err
	}
	if _, err := e.exec(`PRAGMA schema_version=` + strconv.FormatInt(cookie+1, 10)); err != nil {
		return err
	}
	for _, table := range platformActionCheckedTables {
		var result string
		if err := e.queryRow(`PRAGMA integrity_check(` + platformPrefix + table + `)`).Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return fmt.Errorf("integrity check of %s failed after the action check rewrite", platformPrefix+table)
		}
	}
	for _, table := range platformActionCheckedTables {
		rows, err := e.query(`PRAGMA foreign_key_check(` + platformPrefix + table + `)`)
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
			return fmt.Errorf("foreign key check of %s failed after the action check rewrite", platformPrefix+table)
		}
	}
	return nil
}

func verifyPlatformImportGrantSchema(e migExec) error {
	var tables []platformTable
	for _, t := range platformSchemaTables() {
		if slices.Contains(platformActionCheckedTables, t.name) {
			tables = append(tables, t)
		}
	}
	tables = append(tables, platformImportGrantSchemaTables()...)
	var indexes []platformIndex
	for _, i := range platformSchemaIndexes() {
		if slices.Contains(platformActionCheckedTables, i.table) {
			indexes = append(indexes, i)
		}
	}
	indexes = append(indexes, platformImportGrantSchemaIndexes()...)
	return verifyPlatformSchemaProducts(e, tables, indexes)
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
		return setPlatformActionChecks(e, platformLegacyActions(), platformActions())
	}
	return m
}
