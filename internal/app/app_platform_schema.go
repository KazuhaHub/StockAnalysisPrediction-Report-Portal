package app

import (
	"fmt"
	"strconv"
	"strings"
)

// The owner-approved initial control schema is separate from the frozen baseline.
// Entries are closed source declarations, never manifest-supplied SQL.
type platformColumn struct {
	name, kind string
	nullable   bool
}
type platformReference struct{ columns, target, targetColumns, deletion string }
type platformTable struct {
	name, fields, primary string
	references            []platformReference
	checks                []string
}
type platformIndex struct {
	name, table, columns, predicate string
	unique                          bool
}

const platformPrefix = "app_platform_"

func platformSchemaTables() []platformTable {
	ref := func(columns, target, targetColumns string) platformReference {
		return platformReference{columns, platformPrefix + target, targetColumns, "NO ACTION"}
	}
	return []platformTable{
		{name: "publishers", fields: "id:T64 namespaces_json:J8192 enabled:F revision:R created_ms:MS updated_ms:MS", primary: "id"},
		{name: "publisher_keys", fields: "publisher_id:T64 key_id:T128 algorithm:T32 public_key:B4096 not_before_ms:MS not_after_ms:MS? revoked_ms:MS? revision:R created_ms:MS updated_ms:MS", primary: "publisher_id,key_id", references: []platformReference{ref("publisher_id", "publishers", "id")}, checks: []string{"(not_after_ms IS NULL) OR (not_after_ms>not_before_ms)"}},
		{name: "frontend_artifacts", fields: "sha256:SHA archive_bytes:N content:B16777216 created_ms:MS", primary: "sha256", checks: []string{"archive_bytes>0", "archive_bytes=BYTE_LENGTH(content)"}},
		{name: "releases", fields: "sha256:SHA app_id:T64 publisher_id:T64 version:T128 descriptor:B131072 signature:B4096 verification_key_id:T128 trust_revision:R verified_ms:MS frontend_sha256:SHA managed:F created_ms:MS", primary: "sha256", references: []platformReference{ref("publisher_id,verification_key_id", "publisher_keys", "publisher_id,key_id"), ref("frontend_sha256", "frontend_artifacts", "sha256")}},
		{name: "runtime_bindings", fields: "id:ID deployment_id:ID controller_epoch:ID control_revision:R enabled:F verification_key_fingerprint:SHA? sealed_control_key:T16384? revision:R created_ms:MS updated_ms:MS", primary: "id", checks: []string{"(enabled=0) OR ((verification_key_fingerprint IS NOT NULL) AND (sealed_control_key IS NOT NULL))"}},
		{name: "installations", fields: "id:ID app_id:T64 publisher_id:T64 runtime_id:ID? desired_state:T16 phase:T32 generation:R revision:R policy_revision:R control_fence:N controller_epoch:ID sealed_identity_seed:T16384 created_ms:MS updated_ms:MS", primary: "id", references: []platformReference{ref("publisher_id", "publishers", "id"), ref("runtime_id", "runtime_bindings", "id")}, checks: []string{platformEnum("desired_state", "enabled", "disabled", "removed"), platformEnum("phase", "pending", "setup_required", "ready", "disabled", "recovery_required", "quarantined", "removed", "review_required")}},
		{name: "data_namespaces", fields: "id:ID origin_installation_id:ID owner_installation_id:ID? state:T24 data_format:N quota_bytes:R observed_bytes:N writer_fence:N revision:R retained_ms:MS? deleted_ms:MS? created_ms:MS updated_ms:MS", primary: "id", references: []platformReference{ref("origin_installation_id", "installations", "id"), {"owner_installation_id", platformPrefix + "installations", "id", "SET NULL"}}, checks: []string{platformEnum("state", "active", "retained", "deleting", "deleted", "review_required"), "(state<>'active') OR (owner_installation_id IS NOT NULL)"}},
		{name: "activations", fields: "id:ID installation_id:ID release_sha256:SHA generation:R policy_revision:R data_namespace_id:ID? data_format:N state:T16 readiness_evidence:J8192 observed_ms:MS? created_ms:MS updated_ms:MS", primary: "id", references: []platformReference{ref("installation_id", "installations", "id"), ref("release_sha256", "releases", "sha256"), ref("data_namespace_id", "data_namespaces", "id")}, checks: []string{platformEnum("state", "candidate", "active", "retired")}},
		{name: "review_plans", fields: "id:ID installation_id:ID? app_id:T64 publisher_id:T64 action:T24 source_release_sha256:SHA? target_release_sha256:SHA? installation_revision:N policy_revision:N trust_revision:R controller_epoch:ID actor_scope:SHA username:T320? account_created:T64 account_generation:N authority_fingerprint:SHA plan_sha256:SHA plan_json:J65536 expires_ms:MS consumed_ms:MS? created_ms:MS updated_ms:MS", primary: "id", references: []platformReference{ref("installation_id", "installations", "id"), ref("publisher_id", "publishers", "id"), ref("source_release_sha256", "releases", "sha256"), ref("target_release_sha256", "releases", "sha256"), {"username", "users", "username", "SET NULL"}}, checks: []string{platformActions()}},
		{name: "operations", fields: "id:ID installation_id:ID review_plan_id:ID actor_scope:SHA idempotency_sha256:SHA request_sha256:SHA action:T24 managed:F source_generation:N target_generation:R stage:T32 state_revision:R row_revision:R lease_owner:ID? lease_until_ms:MS? fence:N controller_epoch:ID error_code:T96? completed_ms:MS? created_ms:MS updated_ms:MS", primary: "id", references: []platformReference{ref("installation_id", "installations", "id"), ref("review_plan_id", "review_plans", "id")}, checks: []string{platformActions(), platformEnum("stage", "queued", "validate", "download", "stage", "prepare_data", "quiesce", "snapshot", "migrate", "start", "probe", "activate", "revoke", "stop", "remove_code", "remove_data", "restore_data", "complete", "failed", "recovery_required"), "((lease_owner IS NULL) AND (lease_until_ms IS NULL)) OR ((lease_owner IS NOT NULL) AND (lease_until_ms IS NOT NULL))", "(lease_owner IS NULL) OR (fence>=1)", "(completed_ms IS NULL) OR (lease_owner IS NULL)", "(((stage='complete') OR (stage='failed') OR (stage='recovery_required')) AND (completed_ms IS NOT NULL)) OR (((stage<>'complete') AND (stage<>'failed') AND (stage<>'recovery_required')) AND (completed_ms IS NULL))"}},
		{name: "operation_effects", fields: "operation_id:ID stage:T32 effect_number:R effect_id:ID kind:T32 payload_sha256:SHA runtime_id:ID? controller_epoch:ID admitted_fence:R observed_fence:N state:T16 receipt_json:J16384 created_ms:MS updated_ms:MS", primary: "operation_id,stage,effect_number", references: []platformReference{ref("operation_id", "operations", "id"), ref("runtime_id", "runtime_bindings", "id")}, checks: []string{platformEnum("state", "prepared", "applied", "reverted", "unknown"), "effect_number<=32"}},
		{name: "access_grants", fields: "id:ID installation_id:ID principal_type:T8 username:T320? account_created:T64? account_generation:N? group_id:N? group_created:T64? revision:R created_ms:MS updated_ms:MS", primary: "id", references: []platformReference{ref("installation_id", "installations", "id"), {"username", "users", "username", "CASCADE"}, {"group_id", "user_groups", "id", "CASCADE"}}, checks: []string{"((principal_type='user') AND (username IS NOT NULL) AND (account_created IS NOT NULL) AND (account_generation IS NOT NULL) AND (group_id IS NULL) AND (group_created IS NULL)) OR ((principal_type='ou') AND (username IS NULL) AND (account_created IS NULL) AND (account_generation IS NULL) AND (group_id IS NOT NULL) AND (group_created IS NOT NULL))"}},
		{name: "capability_grants", fields: "installation_id:ID capability:T96 granted:F policy_json:J16384 policy_revision:R review_plan_id:ID created_ms:MS updated_ms:MS", primary: "installation_id,capability", references: []platformReference{ref("installation_id", "installations", "id"), ref("review_plan_id", "review_plans", "id")}},
		{name: "recovery_sets", fields: "id:ID installation_id:ID data_namespace_id:ID operation_id:ID release_sha256:SHA data_format:R checkpoint_id:ID manifest_sha256:SHA? bytes:N state:T24 controller_epoch:ID writer_fence:R verified_ms:MS? deleted_ms:MS? created_ms:MS updated_ms:MS", primary: "id", references: []platformReference{ref("installation_id", "installations", "id"), ref("data_namespace_id", "data_namespaces", "id"), ref("operation_id", "operations", "id"), ref("release_sha256", "releases", "sha256")}, checks: []string{platformEnum("state", "preparing", "verified", "restoring", "deleted", "review_required"), "(state<>'verified') OR ((manifest_sha256 IS NOT NULL) AND (verified_ms IS NOT NULL))"}},
	}
}

func platformSchemaIndexes() []platformIndex {
	return []platformIndex{
		{"idx_ap_keys_revocation", "publisher_keys", "publisher_id,revoked_ms", "", false},
		{"uq_ap_release_identity", "releases", "publisher_id,app_id,version", "", true},
		{"idx_ap_release_product", "releases", "app_id,publisher_id", "", false},
		{"uq_ap_runtime_deployment", "runtime_bindings", "deployment_id", "", true},
		{"uq_ap_installation_live", "installations", "app_id", "desired_state<>'removed'", true},
		{"idx_ap_installation_phase", "installations", "phase,updated_ms", "", false},
		{"uq_ap_namespace_owner", "data_namespaces", "owner_installation_id", "owner_installation_id IS NOT NULL", true},
		{"idx_ap_namespace_state", "data_namespaces", "state,updated_ms", "", false},
		{"idx_ap_namespace_origin", "data_namespaces", "origin_installation_id", "", false},
		{"uq_ap_activation_generation", "activations", "installation_id,generation", "", true},
		{"uq_ap_activation_active", "activations", "installation_id", "state='active'", true},
		{"idx_ap_plan_expiry", "review_plans", "expires_ms,consumed_ms", "", false},
		{"uq_ap_operation_review", "operations", "review_plan_id", "", true},
		{"uq_ap_operation_idempotency", "operations", "actor_scope,idempotency_sha256", "", true},
		{"uq_ap_operation_active", "operations", "installation_id", "completed_ms IS NULL", true},
		{"idx_ap_operation_claim", "operations", "completed_ms,lease_until_ms,updated_ms", "", false},
		{"uq_ap_effect_identity", "operation_effects", "effect_id", "", true},
		{"idx_ap_effect_state", "operation_effects", "state,updated_ms", "", false},
		{"uq_ap_access_user", "access_grants", "installation_id,username,account_created,account_generation", "principal_type='user'", true},
		{"uq_ap_access_ou", "access_grants", "installation_id,group_id,group_created", "principal_type='ou'", true},
		{"idx_ap_access_username", "access_grants", "username", "", false},
		{"idx_ap_access_group", "access_grants", "group_id", "", false},
		{"uq_ap_recovery_checkpoint", "recovery_sets", "checkpoint_id", "", true},
		{"idx_ap_recovery_installation", "recovery_sets", "installation_id,state,created_ms", "", false},
	}
}

func platformEnum(column string, values ...string) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = "(" + column + "='" + value + "')"
	}
	return strings.Join(parts, " OR ")
}
func platformActions() string {
	return platformEnum("action", "install", "update", "disable", "enable", "repair", "uninstall", "delete_data", "restore")
}

func (t platformTable) columns() []platformColumn {
	var result []platformColumn
	for _, field := range strings.Fields(t.fields) {
		name, kind, _ := strings.Cut(field, ":")
		result = append(result, platformColumn{name, strings.TrimSuffix(kind, "?"), strings.HasSuffix(kind, "?")})
	}
	return result
}
func (c platformColumn) sqlType(driver string) string {
	switch c.kind {
	case "F":
		return "INTEGER"
	case "N", "R", "MS":
		if driver == "postgres" {
			return "BIGINT"
		}
		return "INTEGER"
	}
	if strings.HasPrefix(c.kind, "B") {
		if driver == "postgres" {
			return "BYTEA"
		}
		return "BLOB"
	}
	return "TEXT"
}
func platformByteLength(value, driver string) string {
	if driver == "postgres" {
		return "octet_length(" + value + ")"
	}
	return "length(CAST(" + value + " AS BLOB))"
}
func (t platformTable) checkExpressions(driver string) []string {
	var checks []string
	for _, c := range t.columns() {
		if c.kind == "N" || c.kind == "R" || c.kind == "MS" || c.kind == "F" {
			min := "0"
			if c.kind == "R" {
				min = "1"
			}
			checks = append(checks, c.name+">="+min)
			if c.kind == "F" {
				checks = append(checks, c.name+"<=1")
			}
			if driver != "postgres" {
				check := "typeof(" + c.name + ")='integer'"
				if c.nullable {
					check = "(" + c.name + " IS NULL) OR (" + check + ")"
				}
				checks = append(checks, check)
			}
			continue
		}
		size := 0
		exact := false
		switch c.kind {
		case "ID":
			size = 36
			exact = true
		case "SHA":
			size = 64
			exact = true
		default:
			size, _ = strconv.Atoi(c.kind[1:])
		}
		op := "<="
		if exact {
			op = "="
		}
		checks = append(checks, platformByteLength(c.name, driver)+op+strconv.Itoa(size))
		if driver != "postgres" {
			typ := "text"
			if strings.HasPrefix(c.kind, "B") {
				typ = "blob"
			}
			check := "typeof(" + c.name + ")='" + typ + "'"
			if c.nullable {
				check = "(" + c.name + " IS NULL) OR (" + check + ")"
			}
			checks = append(checks, check)
		}
	}
	for _, check := range t.checks {
		checks = append(checks, strings.ReplaceAll(check, "BYTE_LENGTH(content)", platformByteLength("content", driver)))
	}
	return checks
}
func (t platformTable) ddl(driver string) string {
	name := platformPrefix + t.name
	var parts []string
	for _, c := range t.columns() {
		part := c.name + " " + c.sqlType(driver)
		if !c.nullable {
			part += " NOT NULL"
		}
		parts = append(parts, part)
	}
	parts = append(parts, "CONSTRAINT "+name+"_pk PRIMARY KEY ("+t.primary+")")
	for i, r := range t.references {
		parts = append(parts, fmt.Sprintf("CONSTRAINT %s_fk_%d FOREIGN KEY (%s) REFERENCES %s(%s) ON DELETE %s DEFERRABLE INITIALLY DEFERRED", name, i+1, r.columns, r.target, r.targetColumns, r.deletion))
	}
	for i, check := range t.checkExpressions(driver) {
		parts = append(parts, fmt.Sprintf("CONSTRAINT %s_ck_%d CHECK (%s)", name, i+1, check))
	}
	return "CREATE TABLE IF NOT EXISTS " + name + " (" + strings.Join(parts, ",") + ")"
}
func (i platformIndex) ddl() string {
	unique := ""
	if i.unique {
		unique = "UNIQUE "
	}
	q := "CREATE " + unique + "INDEX IF NOT EXISTS " + i.name + " ON " + platformPrefix + i.table + " (" + i.columns + ")"
	if i.predicate != "" {
		q += " WHERE " + i.predicate
	}
	return q
}

func migration0015() migration {
	tables, indexes := platformSchemaTables(), platformSchemaIndexes()
	m := migration{id: "0015", restore: restoreRule{olderDumpOK: true}, verify: verifyAppPlatformSchema}
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
