package app

func platformEventSubscriptionSchemaTables() []platformTable {
	ref := func(columns, target, targetColumns string) platformReference {
		return platformReference{columns, platformPrefix + target, targetColumns, "NO ACTION"}
	}
	return []platformTable{{
		name:    "event_subscriptions",
		fields:  "id:ID installation_id:ID app_id:T64 publisher_id:T64 event_type:T96 resource_type:T64 resource_id:T256 state:T24 generation:R policy_revision:R release_sha256:SHA controller_epoch:ID max_inflight:N revision:R created_ms:MS updated_ms:MS",
		primary: "id",
		references: []platformReference{
			ref("installation_id", "installations", "id"),
		},
		checks: []string{
			"((event_type='report.ingested') AND (resource_type='report')) OR ((event_type='batch.job.finished') AND (resource_type='job'))",
			platformEnum("state", "active", "paused", "revoked", "review_required"),
			"max_inflight>=1",
			"max_inflight<=16",
			"updated_ms>=created_ms",
		},
	}}
}

func platformEventSubscriptionSchemaIndexes() []platformIndex {
	return []platformIndex{
		{"uq_ap_event_subscription", "event_subscriptions", "installation_id,event_type,resource_type,resource_id", "", true},
		{"idx_ap_event_subscription_resource", "event_subscriptions", "event_type,resource_type,resource_id,state,installation_id", "", false},
		{"idx_ap_event_subscription_installation", "event_subscriptions", "installation_id,state,updated_ms", "", false},
		{"idx_ap_event_subscription_revision", "event_subscriptions", "state,revision", "", false},
	}
}

func verifyPlatformEventSubscriptionSchema(e migExec) error {
	return verifyPlatformSchemaProducts(
		e, platformEventSubscriptionSchemaTables(),
		platformEventSubscriptionSchemaIndexes())
}

func migration0020() migration {
	tables, indexes := platformEventSubscriptionSchemaTables(), platformEventSubscriptionSchemaIndexes()
	m := migration{
		id:      "0020",
		restore: restoreRule{olderDumpOK: true},
		verify:  verifyPlatformEventSubscriptionSchema,
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
