package app

// Step 0023: machine read tokens (doc 183, approved by the owner on 2026-11-20).
//
// One additive table. A token is bound to one installation, a subset of its reviewed routes, a rate
// limit, an optional source list and a required expiry. Only the SHA-256 of the secret part is stored.
// The 16-active-per-installation, route subset and 365-day maximum rules depend on other rows or on
// policy and are enforced in code, not in CHECKs.

func platformMachineTokenTables() []platformTable {
	return []platformTable{{
		name:    "machine_tokens",
		fields:  "id:ID installation_id:ID handle:T16 label:T64 secret_sha256:SHA routes_json:J2048 per_minute:R source_cidrs_json:J1024 state:T16 policy_revision:R created_by:T320 revision:R expires_ms:MS created_ms:MS updated_ms:MS revoked_ms:MS? last_used_ms:MS?",
		primary: "id",
		references: []platformReference{
			{"installation_id", platformPrefix + "installations", "id", "CASCADE"},
		},
		checks: []string{
			platformEnum("state", "active", "revoked"),
			"((state='revoked') AND (revoked_ms IS NOT NULL)) OR ((state<>'revoked') AND (revoked_ms IS NULL))",
			"per_minute<=600",
			"expires_ms>created_ms",
			"updated_ms>=created_ms",
		},
	}}
}

func platformMachineTokenIndexes() []platformIndex {
	return []platformIndex{
		{"uq_ap_machine_token_handle", "machine_tokens", "handle", "", true},
		{"uq_ap_machine_token_secret", "machine_tokens", "secret_sha256", "", true},
		{"idx_ap_machine_token_installation", "machine_tokens", "installation_id,state,id", "", false},
		{"idx_ap_machine_token_expiry", "machine_tokens", "state,expires_ms", "", false},
	}
}

func verifyPlatformMachineTokenSchema(e migExec) error {
	return verifyPlatformSchemaProducts(e, platformMachineTokenTables(), platformMachineTokenIndexes())
}

// migration0023 adds the table and nothing else. A dump from before it has no such table: restoring it
// leaves the table empty, so an older dump is allowed.
func migration0023() migration {
	tables, indexes := platformMachineTokenTables(), platformMachineTokenIndexes()
	m := migration{id: "0023", restore: restoreRule{olderDumpOK: true}, verify: verifyPlatformMachineTokenSchema}
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
