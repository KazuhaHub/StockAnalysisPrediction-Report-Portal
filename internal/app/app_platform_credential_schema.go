package app

// Credential custody extends the control schema through a separate migration;
// the original control products and their verification remain unchanged.
func platformCredentialTables() []platformTable {
	return platformCredentialTablesWithOperations(platformSecretBindingOperations())
}

// platformLegacyCredentialTables is the credential schema as step 0016 created it: the current tables
// with the frozen three-value operation list. Step 0016 must keep producing this shape; step 0022
// widens it afterwards, for fresh and upgraded databases alike.
func platformLegacyCredentialTables() []platformTable {
	return platformCredentialTablesWithOperations(platformLegacySecretBindingOperations())
}

func platformCredentialTablesWithOperations(operations string) []platformTable {
	return []platformTable{
		{name: "credentials", fields: "id:ID label:T160 state:T24 sealed_value:T32768? value_revision:R revision:R created_ms:MS updated_ms:MS revoked_ms:MS?", primary: "id", checks: []string{
			platformEnum("state", "active", "revoked", "review_required"),
			"((state='revoked') AND (sealed_value IS NULL) AND (revoked_ms IS NOT NULL)) OR ((state<>'revoked') AND (sealed_value IS NOT NULL) AND (sealed_value<>'') AND (revoked_ms IS NULL))",
			"updated_ms>=created_ms",
		}},
		{name: "secret_bindings", fields: "id:ID installation_id:ID name:T64 credential_id:ID credential_value_revision:R operation:T24 state:T24 policy_json:J8192 policy_revision:R review_plan_id:ID revision:R created_ms:MS updated_ms:MS revoked_ms:MS?", primary: "id", references: []platformReference{
			{"installation_id", platformPrefix + "installations", "id", "CASCADE"},
			{"credential_id", platformPrefix + "credentials", "id", "NO ACTION"},
			{"review_plan_id", platformPrefix + "review_plans", "id", "NO ACTION"},
		}, checks: []string{
			operations, platformEnum("state", "active", "revoked", "review_required"),
			"((state='revoked') AND (revoked_ms IS NOT NULL)) OR ((state<>'revoked') AND (revoked_ms IS NULL))", "updated_ms>=created_ms",
		}},
	}
}
func platformCredentialIndexes() []platformIndex {
	return []platformIndex{
		{"idx_ap_credential_state", "credentials", "state,updated_ms,id", "", false},
		{"uq_ap_secret_binding_name", "secret_bindings", "installation_id,name", "", true},
		{"idx_ap_secret_binding_credential", "secret_bindings", "credential_id,state,id", "", false},
		{"idx_ap_secret_binding_installation", "secret_bindings", "installation_id,state,id", "", false},
	}
}
func migration0016() migration {
	tables, indexes := platformLegacyCredentialTables(), platformCredentialIndexes()
	// A database that has not applied 0022 still carries the frozen operation list, and verification
	// runs before that step: 0016 accepts either list.
	verify := func(e migExec) error {
		return verifyPlatformSchemaProductsWithAlternates(e, platformCredentialTables(), indexes, tables)
	}
	m := migration{id: "0016", restore: restoreRule{olderDumpOK: true}, verify: verify}
	for _, t := range tables {
		m.tables = append(m.tables, platformPrefix+t.name)
		for _, c := range t.columns() {
			m.columns = append(m.columns, colRef{platformPrefix + t.name, c.name})
		}
	}
	for _, i := range indexes {
		m.indices = append(m.indices, i.name)
	}
	m.up = func(e migExec) error {
		for _, t := range tables {
			if _, err := e.exec(t.ddl(e.s.driver)); err != nil {
				return err
			}
		}
		for _, i := range indexes {
			if _, err := e.exec(i.ddl()); err != nil {
				return err
			}
		}
		return nil
	}
	return m
}
