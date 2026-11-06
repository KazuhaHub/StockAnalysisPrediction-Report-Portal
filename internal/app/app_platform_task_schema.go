package app

func platformTaskSchemaTables() []platformTable {
	ref := func(columns, target, targetColumns string) platformReference {
		return platformReference{columns, platformPrefix + target, targetColumns, "NO ACTION"}
	}
	return []platformTable{
		{
			name:    "tasks",
			fields:  "id:ID installation_id:ID app_id:T64 publisher_id:T64 occurrence_id:T128 handler:T64 payload:B8192 payload_sha256:SHA state:T24 generation:R policy_revision:R release_sha256:SHA controller_epoch:ID attempts:N max_attempts:N fence:R revision:R worker_id:ID? lease_id:ID? lease_until_ms:MS? next_attempt_ms:MS expires_ms:MS error_code:T96? result_json:J8192? completed_ms:MS? created_ms:MS updated_ms:MS",
			primary: "id",
			references: []platformReference{
				ref("installation_id", "installations", "id"),
			},
			checks: []string{
				platformEnum("state", "waiting", "leased", "running", "succeeded", "failed", "cancelled", "uncertain", "review_required"),
				"max_attempts>=1",
				"max_attempts<=5",
				"attempts<=max_attempts",
				"(((state='leased') OR (state='running')) AND worker_id IS NOT NULL AND lease_id IS NOT NULL AND lease_until_ms IS NOT NULL) OR (((state<>'leased') AND (state<>'running')) AND worker_id IS NULL AND lease_id IS NULL AND lease_until_ms IS NULL)",
				"((((state='succeeded') OR (state='failed')) OR (state='cancelled')) AND completed_ms IS NOT NULL) OR ((((state<>'succeeded') AND (state<>'failed')) AND (state<>'cancelled')) AND completed_ms IS NULL)",
				"((state<>'leased') AND (state<>'running')) OR (lease_until_ms>=(updated_ms+30000) AND lease_until_ms<=(updated_ms+600000))",
				"expires_ms>=created_ms",
				"next_attempt_ms<=expires_ms",
				"completed_ms IS NULL OR completed_ms>=created_ms",
				"updated_ms>=created_ms",
			},
		},
		{
			name:       "task_attempts",
			fields:     "task_id:ID attempt:R state:T24 worker_id:ID lease_id:ID fence:R execution_sha256:SHA started_ms:MS lease_until_ms:MS finished_ms:MS? error_code:T96? result_sha256:SHA? evidence_json:J8192? created_ms:MS updated_ms:MS",
			primary:    "task_id,attempt",
			references: []platformReference{ref("task_id", "tasks", "id")},
			checks: []string{
				platformEnum("state", "leased", "running", "succeeded", "failed", "cancelled", "uncertain", "review_required"),
				"attempt>=1",
				"attempt<=5",
				"lease_until_ms>=(started_ms+30000)",
				"lease_until_ms<=(started_ms+600000)",
				"(((state='leased') OR (state='running')) AND finished_ms IS NULL) OR (((state<>'leased') AND (state<>'running')) AND finished_ms IS NOT NULL)",
				"finished_ms IS NULL OR finished_ms>=started_ms",
				"finished_ms IS NULL OR finished_ms<=updated_ms",
				"updated_ms>=created_ms",
			},
		},
	}
}

func platformTaskSchemaIndexes() []platformIndex {
	return []platformIndex{
		{"uq_ap_task_occurrence", "tasks", "installation_id,occurrence_id", "", true},
		{"idx_ap_task_due", "tasks", "state,next_attempt_ms,expires_ms", "", false},
		{"idx_ap_task_installation", "tasks", "installation_id,state,next_attempt_ms", "", false},
		{"idx_ap_task_lease", "tasks", "state,lease_until_ms,updated_ms", "", false},
		{"idx_ap_task_expiry", "tasks", "expires_ms,state", "", false},
		{"idx_ap_task_app", "tasks", "app_id,state", "", false},
		{"idx_ap_task_attempt_state", "task_attempts", "state,updated_ms", "", false},
	}
}

func verifyPlatformTaskSchema(e migExec) error {
	return verifyPlatformSchemaProducts(
		e, platformTaskSchemaTables(), platformTaskSchemaIndexes())
}

func migration0019() migration {
	tables, indexes := platformTaskSchemaTables(), platformTaskSchemaIndexes()
	m := migration{
		id:      "0019",
		restore: restoreRule{olderDumpOK: true},
		verify:  verifyPlatformTaskSchema,
	}
	for _, table := range tables {
		m.tables = append(m.tables, platformPrefix+table.name)
		for _, column := range table.columns() {
			m.columns = append(m.columns,
				colRef{platformPrefix + table.name, column.name})
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
		return nil
	}
	return m
}
