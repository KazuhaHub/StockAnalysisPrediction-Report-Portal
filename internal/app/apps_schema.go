package app

// migration0014 adds the two tables behind app ordering and per-OU app access (ADR 0048). The base
// schema stays frozen: both are new tables, nothing is back-filled, and an older dump simply has no
// rows in them, so the step is olderDumpOK. With no rows every app is open and ordered as before, so
// applying the step changes nobody's access. Every statement is IF NOT EXISTS, which makes a re-run
// after a crash between the DDL and the ledger row a no-op.
//
//	app_order(app_key PK, position)       one shared ordering for built-in and installed apps
//	app_ou_access(app_key, group_id, enabled)   1 open, 0 closed; no row = inherit from the parent OU
func migration0014() migration {
	return migration{
		id: "0014",
		columns: []colRef{
			{table: "app_order", name: "app_key"}, {table: "app_order", name: "position"},
			{table: "app_ou_access", name: "app_key"}, {table: "app_ou_access", name: "group_id"},
			{table: "app_ou_access", name: "enabled"},
		},
		tables:  []string{"app_order", "app_ou_access"},
		restore: restoreRule{olderDumpOK: true},
		up: func(e migExec) error {
			for _, q := range []string{
				`CREATE TABLE IF NOT EXISTS app_order(
					app_key TEXT PRIMARY KEY,
					position INTEGER NOT NULL)`,
				`CREATE TABLE IF NOT EXISTS app_ou_access(
					app_key TEXT NOT NULL,
					group_id INTEGER NOT NULL,
					enabled INTEGER NOT NULL,
					PRIMARY KEY(app_key, group_id))`,
			} {
				if _, err := e.exec(q); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
