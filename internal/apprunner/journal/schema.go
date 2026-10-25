package journal

import (
	"fmt"
	"strconv"
	"strings"
)

const schemaVersion = 1

type schemaProduct struct{ kind, name, sql, rowCheck string }

// Closed physical declarations from the approved four-table journal scope.
// Neither manifests nor control messages can supply SQL declarations.
func schemaProducts() []schemaProduct {
	tables := []struct {
		name, fields, keys string
		checks             []string
	}{
		{"runner_deployment", "singleton:N deployment_id:ID controller_epoch:ID control_revision:R verification_algorithm:T32 verification_key:B4096 key_fingerprint:SHA revision:R created_ms:MS updated_ms:MS",
			"PRIMARY KEY(singleton),UNIQUE(deployment_id)", []string{"singleton=1"}},
		{"runner_commands", "effect_id:ID operation_id:ID installation_id:ID stage:T32 effect_number:R kind:T32 payload_sha256:SHA target_generation:R controller_epoch:ID admitted_fence:R state:T16 receipt_json:J16384 ack_attempted_ms:MS? created_ms:MS updated_ms:MS",
			"PRIMARY KEY(effect_id),UNIQUE(operation_id,stage,effect_number)", []string{"effect_number<=32",
				enumCondition("state", "prepared", "applied", "reverted", "unknown"),
				enumCondition("stage", "validate", "download", "stage", "prepare_data", "quiesce", "snapshot", "migrate", "start", "probe", "activate", "revoke", "stop", "remove_code", "remove_data", "restore_data"),
				enumCondition("kind", "stage", "verify", "prepare-data", "start-candidate", "probe", "activate", "stop", "snapshot", "restore", "remove-code", "delete-data"),
				"ack_attempted_ms IS NULL OR state<>'prepared'"}},
		{"runner_resources", "id:ID installation_id:ID data_namespace_id:ID? kind:T24 native_id:T512 release_sha256:SHA? generation:N controller_epoch:ID control_fence:N writer_fence:N desired_state:T16 observed_state:T24 ownership_sha256:SHA revision:R observed_ms:MS? created_ms:MS updated_ms:MS",
			"PRIMARY KEY(id),UNIQUE(kind,native_id)", []string{enumCondition("kind", "container", "volume", "socket", "artifact"),
				enumCondition("desired_state", "present", "running", "stopped", "absent"),
				enumCondition("observed_state", "unknown", "present", "running", "stopped", "absent", "failed", "quarantined")}},
		{"runner_checkpoints", "id:ID data_namespace_id:ID installation_id:ID operation_id:ID release_sha256:SHA data_format:R writer_fence:R manifest_sha256:SHA? bytes:N state:T24 revision:R verified_ms:MS? created_ms:MS updated_ms:MS",
			"PRIMARY KEY(id)", []string{enumCondition("state", "preparing", "verified", "restoring", "deleted", "review_required"),
				"state<>'verified' OR (manifest_sha256 IS NOT NULL AND verified_ms IS NOT NULL)"}},
	}
	products := make([]schemaProduct, 0, 8)
	for _, table := range tables {
		columns := []string{}
		predicates := []string{}
		for _, field := range strings.Fields(table.fields) {
			name, kind, _ := strings.Cut(field, ":")
			definition, condition := columnSQL(name, kind)
			columns = append(columns, definition)
			predicates = append(predicates, "("+condition+")")
		}
		columns = append(columns, table.keys)
		for _, condition := range table.checks {
			columns = append(columns, "CHECK("+condition+")")
			predicates = append(predicates, "("+condition+")")
		}
		products = append(products, schemaProduct{"table", table.name, "CREATE TABLE " + table.name + "(" + strings.Join(columns, ",") + ")", strings.Join(predicates, " AND ")})
	}
	for _, index := range []struct{ name, table, columns string }{
		{"idx_runner_commands_installation", "runner_commands", "installation_id"},
		{"idx_runner_commands_state", "runner_commands", "state,updated_ms"},
		{"idx_runner_resources_installation", "runner_resources", "installation_id,kind"},
		{"idx_runner_checkpoints_namespace", "runner_checkpoints", "data_namespace_id,state,created_ms"},
	} {
		products = append(products, schemaProduct{"index", index.name, "CREATE INDEX " + index.name + " ON " + index.table + "(" + index.columns + ")", ""})
	}
	return products
}

func enumCondition(column string, values ...string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = "'" + value + "'"
	}
	return column + " IN (" + strings.Join(quoted, ",") + ")"
}

func columnSQL(name, kind string) (string, string) {
	nullable := strings.HasSuffix(kind, "?")
	kind = strings.TrimSuffix(kind, "?")
	typ, check := "TEXT", ""
	switch kind {
	case "N", "R", "MS":
		typ = "INTEGER"
		minimum := 0
		if kind == "R" {
			minimum = 1
		}
		check = fmt.Sprintf("typeof(%s)='integer' AND %s>=%d", name, name, minimum)
	case "ID":
		check = fmt.Sprintf("typeof(%[1]s)='text' AND length(%[1]s)=36 AND length(CAST(%[1]s AS BLOB))=36 AND substr(%[1]s,9,1)='-' AND substr(%[1]s,14,1)='-' AND substr(%[1]s,19,1)='-' AND substr(%[1]s,24,1)='-' AND length(replace(%[1]s,'-',''))=32 AND replace(%[1]s,'-','') NOT GLOB '*[^0-9a-f]*'", name)
	case "SHA":
		check = fmt.Sprintf("typeof(%[1]s)='text' AND length(%[1]s)=64 AND length(CAST(%[1]s AS BLOB))=64 AND %[1]s NOT GLOB '*[^0-9a-f]*'", name)
	default:
		if len(kind) < 2 {
			panic("invalid closed journal column kind")
		}
		limit, err := strconv.Atoi(kind[1:])
		if err != nil || limit <= 0 {
			panic("invalid closed journal byte bound")
		}
		switch kind[0] {
		case 'T', 'J':
			check = fmt.Sprintf("typeof(%[1]s)='text' AND length(CAST(%[1]s AS BLOB)) BETWEEN 1 AND %[2]d", name, limit)
			if kind[0] == 'J' {
				check += " AND json_valid(" + name + ") AND json_type(" + name + ")='object'"
			}
		case 'B':
			typ = "BLOB"
			check = fmt.Sprintf("typeof(%[1]s)='blob' AND length(%[1]s) BETWEEN 1 AND %[2]d", name, limit)
		default:
			panic("unknown closed journal column kind")
		}
	}
	required := " NOT NULL"
	if nullable {
		required = ""
		check = name + " IS NULL OR (" + check + ")"
	}
	return name + " " + typ + required + " CHECK(" + check + ")", check
}
