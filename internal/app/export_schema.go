package app

import "fmt"

// exportJobColumns is the declared shape of export_jobs (ADR 0045). Instants are unix milliseconds,
// like the notification tables, so they compare as numbers on both drivers.
var exportJobColumns = []struct{ name, typ string }{
	{"id", "TEXT"}, {"source", "TEXT"}, {"format", "TEXT"}, {"params", "TEXT"}, {"requested_by", "TEXT"},
	{"status", "TEXT"}, {"rows_estimate", "BIGINT"}, {"rows_done", "BIGINT"}, {"bytes", "BIGINT"},
	{"file_name", "TEXT"}, {"error", "TEXT"}, {"created_at", "BIGINT"}, {"started_at", "BIGINT"},
	{"finished_at", "BIGINT"}, {"expires_at", "BIGINT"},
}

// migration0012 adds the data-export task table (ADR 0045). The base schema stays frozen: this is a
// new table, nothing is back-filled, and an older dump simply has no jobs, so the step is
// olderDumpOK. Every statement is IF NOT EXISTS, which makes a re-run after a crash between the DDL
// and the ledger row a no-op.
func migration0012() migration {
	cols := make([]colRef, 0, len(exportJobColumns))
	for _, c := range exportJobColumns {
		cols = append(cols, colRef{table: "export_jobs", name: c.name})
	}
	return migration{
		id:      "0012",
		columns: cols,
		tables:  []string{"export_jobs"},
		indices: []string{"idx_export_jobs_created", "idx_export_jobs_status"},
		restore: restoreRule{olderDumpOK: true},
		up: func(e migExec) error {
			for _, q := range []string{
				`CREATE TABLE IF NOT EXISTS export_jobs(
					id TEXT PRIMARY KEY, source TEXT NOT NULL, format TEXT NOT NULL, params TEXT NOT NULL,
					requested_by TEXT NOT NULL, status TEXT NOT NULL,
					rows_estimate BIGINT NOT NULL DEFAULT 0, rows_done BIGINT NOT NULL DEFAULT 0,
					bytes BIGINT NOT NULL DEFAULT 0, file_name TEXT NOT NULL DEFAULT '',
					error TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL,
					started_at BIGINT NOT NULL DEFAULT 0, finished_at BIGINT NOT NULL DEFAULT 0,
					expires_at BIGINT NOT NULL DEFAULT 0)`,
				`CREATE INDEX IF NOT EXISTS idx_export_jobs_created ON export_jobs(created_at DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_export_jobs_status ON export_jobs(status, expires_at)`,
			} {
				if _, err := e.exec(q); err != nil {
					return err
				}
			}
			return nil
		},
		verify: verifyExportJobsShape,
	}
}

// verifyExportJobsShape checks the column types and the identity key, which the declared column
// names alone cannot express: a table with the right names and a TEXT created_at would sort jobs
// lexically and compare expiry as strings.
func verifyExportJobsShape(e migExec) error {
	query := `SELECT name,UPPER(type),pk FROM pragma_table_info('export_jobs')`
	if e.s.driver == "postgres" {
		query = `SELECT c.column_name,UPPER(c.data_type),
			COALESCE((SELECT k.ordinal_position FROM information_schema.table_constraints t
			JOIN information_schema.key_column_usage k ON k.constraint_name=t.constraint_name
			AND k.table_schema=t.table_schema AND k.table_name=t.table_name
			WHERE t.table_schema='public' AND t.table_name=c.table_name
			AND t.constraint_type='PRIMARY KEY' AND k.column_name=c.column_name),0)
			FROM information_schema.columns c WHERE c.table_schema='public' AND c.table_name='export_jobs'`
	}
	rows, err := e.query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := map[string]string{}
	for _, c := range exportJobColumns {
		want[c.name] = c.typ
	}
	for rows.Next() {
		var name, typ string
		var key int
		if err := rows.Scan(&name, &typ, &key); err != nil {
			return err
		}
		expected, declared := want[name]
		if !declared {
			continue // a superset is tolerated, like the base shape check
		}
		if typ != expected {
			return fmt.Errorf("export_jobs.%s is %s, want %s", name, typ, expected)
		}
		if (key != 0) != (name == "id") {
			return fmt.Errorf("export_jobs has an incompatible identity key (column %s)", name)
		}
		delete(want, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for name := range want {
		return fmt.Errorf("export_jobs.%s is missing", name)
	}
	return nil
}
