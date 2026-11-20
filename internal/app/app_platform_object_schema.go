package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	platformObjectGlobalMaxBytes   int64 = 2 << 30
	platformObjectGlobalMaxObjects int64 = 65536
	platformObjectNamespaceBytes   int64 = 256 << 20
	platformObjectNamespaceObjects int64 = 4096
	platformPluginMaxBytes         int64 = 256 << 20
	platformPluginMaxObjects       int64 = 4096
)

var platformPluginDataTablePattern = regexp.MustCompile(`^app_platform_plugin_[0-9a-f]{24}$`)

func platformLegacyObjectSchemaTables() []platformTable {
	ref := func(columns, target, targetColumns, deletion string) platformReference {
		return platformReference{columns, platformPrefix + target, targetColumns, deletion}
	}
	return []platformTable{
		{
			name:    "object_storage",
			fields:  "id:N max_bytes:N max_objects:N used_bytes:N used_objects:N revision:R updated_ms:MS",
			primary: "id",
			checks: []string{
				"id=1",
				"used_bytes<=max_bytes",
				"used_objects<=max_objects",
				"(used_objects>0) OR (used_bytes=0)",
			},
		},
		{
			name:    "object_namespaces",
			fields:  "id:ID origin_installation_id:ID owner_installation_id:ID? state:T24 data_format:N quota_bytes:R observed_bytes:N object_count:N revision:R deleted_ms:MS? created_ms:MS updated_ms:MS",
			primary: "id",
			references: []platformReference{
				ref("origin_installation_id", "installations", "id", "NO ACTION"),
				ref("owner_installation_id", "installations", "id", "SET NULL"),
			},
			checks: []string{
				platformEnum("state", "active", "deleting", "recovery_required", "deleted"),
				"(state<>'active') OR (owner_installation_id IS NOT NULL)",
				"(state<>'deleted') OR ((owner_installation_id IS NULL) AND (deleted_ms IS NOT NULL) AND (observed_bytes=0) AND (object_count=0))",
				"observed_bytes<=quota_bytes",
				"(object_count>0) OR (observed_bytes=0)",
				"updated_ms>=created_ms",
			},
		},
		{
			name:    "storage_objects",
			fields:  "id:ID namespace_id:ID owner_username:T320 owner_account_created:T64 owner_account_generation:N collection:T32 label:T256 media_type:T128 content:B1048576 content_sha256:SHA size_bytes:N revision:R idempotency_key:T128 created_ms:MS updated_ms:MS",
			primary: "id",
			references: []platformReference{
				ref("namespace_id", "object_namespaces", "id", "NO ACTION"),
				{"owner_username", "users", "username", "NO ACTION"},
			},
			checks: []string{
				"size_bytes<=1048576",
				"BYTE_LENGTH(content)=size_bytes",
				"updated_ms>=created_ms",
			},
		},
	}
}

func platformLegacyObjectSchemaIndexes() []platformIndex {
	return []platformIndex{
		{name: "uq_ap_object_origin", table: "object_namespaces", columns: "origin_installation_id", unique: true},
		{name: "uq_ap_object_owner", table: "object_namespaces", columns: "owner_installation_id", predicate: "owner_installation_id IS NOT NULL", unique: true},
		{name: "idx_ap_object_namespace_state", table: "object_namespaces", columns: "state,updated_ms"},
		{name: "uq_ap_object_idempotency", table: "storage_objects", columns: "namespace_id,owner_username,owner_account_created,owner_account_generation,idempotency_key", unique: true},
		{name: "idx_ap_object_owner_list", table: "storage_objects", columns: "namespace_id,owner_username,owner_account_created,owner_account_generation,updated_ms,id"},
		{name: "idx_ap_object_cleanup", table: "storage_objects", columns: "namespace_id,updated_ms"},
	}
}

func platformPluginDataTableName(appID string) string {
	sum := sha256.Sum256([]byte("portal-plugin-data/v1:" + appID))
	return platformPrefix + "plugin_" + hex.EncodeToString(sum[:12])
}

func platformPluginDataTableDefinition(table string) (platformTable, []platformIndex) {
	suffix := strings.TrimPrefix(table, platformPrefix)
	return platformTable{
		name:    suffix,
		fields:  "id:ID installation_id:ID owner_username:T320 owner_account_created:T64 owner_account_generation:N collection:T32 label:T256 media_type:T128 content:B1048576 content_sha256:SHA size_bytes:N revision:R idempotency_key:T128 created_ms:MS updated_ms:MS",
		primary: "id",
		references: []platformReference{
			{"installation_id", platformPrefix + "installations", "id", "NO ACTION"},
			{"owner_username", "users", "username", "NO ACTION"},
		},
		checks: []string{
			"size_bytes<=1048576",
			"BYTE_LENGTH(content)=size_bytes",
			"updated_ms>=created_ms",
		},
	}, []platformIndex{
		{name: table + "_uq_idem", table: suffix, columns: "installation_id,owner_username,owner_account_created,owner_account_generation,idempotency_key", unique: true},
		{name: table + "_idx_owner_list", table: suffix, columns: "installation_id,owner_username,owner_account_created,owner_account_generation,updated_ms,id"},
		{name: table + "_idx_cleanup", table: suffix, columns: "installation_id,updated_ms"},
	}
}

func platformObjectSchemaTables() []platformTable {
	return []platformTable{
		{
			name:    "object_storage",
			fields:  "id:N max_bytes:N max_objects:N used_bytes:N used_objects:N revision:R updated_ms:MS",
			primary: "id",
			checks: []string{
				"id=1",
				"used_bytes<=max_bytes",
				"used_objects<=max_objects",
				"(used_objects>0) OR (used_bytes=0)",
			},
		},
		{
			name:    "plugin_tables",
			fields:  "id:ID app_id:T64 table_name:T64 data_format:N used_bytes:N used_objects:N revision:R created_ms:MS updated_ms:MS",
			primary: "id",
			checks: []string{
				"data_format=1",
				"used_bytes<=268435456",
				"used_objects<=4096",
				"(used_objects>0) OR (used_bytes=0)",
				"updated_ms>=created_ms",
			},
		},
	}
}

func platformObjectSchemaIndexes() []platformIndex {
	return []platformIndex{
		{name: "uq_ap_plugin_table_app", table: "plugin_tables", columns: "app_id", unique: true},
		{name: "uq_ap_plugin_table_name", table: "plugin_tables", columns: "table_name", unique: true},
	}
}

func createPlatformPluginDataTable(e migExec, table string) error {
	if !platformPluginDataTablePattern.MatchString(table) {
		return fmt.Errorf("invalid plugin data table name")
	}
	definition, indexes := platformPluginDataTableDefinition(table)
	if _, err := e.exec(definition.ddlNamed(e.s.driver, table)); err != nil {
		return err
	}
	for _, index := range indexes {
		if _, err := e.exec(index.ddl()); err != nil {
			return err
		}
	}
	return nil
}

func dropPlatformPluginDataTable(e migExec, table string) error {
	if !platformPluginDataTablePattern.MatchString(table) {
		return fmt.Errorf("invalid plugin data table name")
	}
	_, err := e.exec(`DROP TABLE IF EXISTS ` + table)
	return err
}

func verifyPlatformPluginDataTable(e migExec, table string) error {
	if !platformPluginDataTablePattern.MatchString(table) {
		return fmt.Errorf("invalid plugin data table name")
	}
	definition, indexes := platformPluginDataTableDefinition(table)
	return verifyPlatformSchemaProducts(e, []platformTable{definition}, indexes)
}

func platformPluginDataTables(e migExec) ([]string, error) {
	q := `SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'app_platform_plugin_%' ORDER BY name`
	if e.s.driver == "postgres" {
		q = `SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name LIKE 'app_platform_plugin_%' ORDER BY table_name`
	}
	rows, err := e.query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		if table == platformPrefix+"plugin_tables" {
			continue
		}
		if !platformPluginDataTablePattern.MatchString(table) {
			return nil, fmt.Errorf("unregistered plugin-shaped table %q", table)
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

func verifyPlatformObjectStorage(e migExec) error {
	var maxBytes, maxObjects, usedBytes, usedObjects int64
	if err := e.queryRow(`SELECT max_bytes,max_objects,used_bytes,used_objects FROM app_platform_object_storage WHERE id=1`).Scan(&maxBytes, &maxObjects, &usedBytes, &usedObjects); err != nil {
		return err
	}
	var pluginBytes, pluginObjects int64
	if err := e.queryRow(`SELECT COALESCE(SUM(used_bytes),0),COALESCE(SUM(used_objects),0) FROM app_platform_plugin_tables`).Scan(&pluginBytes, &pluginObjects); err != nil {
		return err
	}
	if maxBytes != platformObjectGlobalMaxBytes || maxObjects != platformObjectGlobalMaxObjects || usedBytes != pluginBytes || usedObjects != pluginObjects {
		return fmt.Errorf("object storage counters do not match plugin data tables: global=%d/%d plugins=%d/%d", usedBytes, usedObjects, pluginBytes, pluginObjects)
	}

	rows, err := e.query(`SELECT id,app_id,table_name,data_format,used_bytes,used_objects,revision FROM app_platform_plugin_tables ORDER BY app_id`)
	if err != nil {
		return err
	}
	type registryRow struct {
		id, appID, table            string
		format, bytes, objects, rev int64
	}
	var registry []registryRow
	for rows.Next() {
		var row registryRow
		if err := rows.Scan(&row.id, &row.appID, &row.table, &row.format, &row.bytes, &row.objects, &row.rev); err != nil {
			rows.Close()
			return err
		}
		registry = append(registry, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var registered []string
	for _, row := range registry {
		if !platformValidUUID(row.id) || !platformDirectoryAppID.MatchString(row.appID) || row.table != platformPluginDataTableName(row.appID) || row.format != 1 || row.bytes < 0 || row.objects < 0 || row.rev < 1 {
			return fmt.Errorf("plugin data registry row is invalid")
		}
		registered = append(registered, row.table)
		if err := verifyPlatformPluginDataTable(e, row.table); err != nil {
			return fmt.Errorf("plugin %s: %w", row.appID, err)
		}
		var actualBytes, actualObjects int64
		if err := e.queryRow(`SELECT COALESCE(SUM(size_bytes),0),COUNT(*) FROM `+row.table).Scan(&actualBytes, &actualObjects); err != nil {
			return err
		}
		if actualBytes != row.bytes || actualObjects != row.objects {
			return fmt.Errorf("plugin %s counters do not match its data table", row.appID)
		}
		var mismatched int
		if err := e.queryRow(`SELECT COUNT(*) FROM `+row.table+` d LEFT JOIN app_platform_installations i ON i.id=d.installation_id WHERE i.id IS NULL OR i.app_id<>? OR i.desired_state='removed'`, row.appID).Scan(&mismatched); err != nil {
			return err
		}
		if mismatched != 0 {
			return fmt.Errorf("plugin %s data belongs to a stale installation", row.appID)
		}
	}
	actualTables, err := platformPluginDataTables(e)
	if err != nil {
		return err
	}
	sort.Strings(registered)
	if strings.Join(actualTables, "\x00") != strings.Join(registered, "\x00") {
		return fmt.Errorf("plugin data tables do not match their registry")
	}
	var legacyObjects int
	if e.tableExists(platformPrefix + "storage_objects") {
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_storage_objects`).Scan(&legacyObjects); err != nil {
			return err
		}
	}
	if legacyObjects != 0 {
		return fmt.Errorf("legacy object rows have not been migrated")
	}
	return nil
}

func verifyPlatformLegacyObjectStorage(e migExec) error {
	var maxBytes, maxObjects, usedBytes, usedObjects int64
	if err := e.queryRow(`SELECT max_bytes,max_objects,used_bytes,used_objects FROM app_platform_object_storage WHERE id=1`).Scan(&maxBytes, &maxObjects, &usedBytes, &usedObjects); err != nil {
		return err
	}
	if maxBytes != platformObjectGlobalMaxBytes || maxObjects != platformObjectGlobalMaxObjects {
		return fmt.Errorf("legacy object storage limits changed")
	}
	// After 0018 the shared counters describe plugin tables, not legacy rows.
	if e.tableExists(platformPrefix + "plugin_tables") {
		return nil
	}
	var actualBytes, actualObjects int64
	if err := e.queryRow(`SELECT COALESCE(SUM(size_bytes),0),COUNT(*) FROM app_platform_storage_objects`).Scan(&actualBytes, &actualObjects); err != nil {
		return err
	}
	if usedBytes != actualBytes || usedObjects != actualObjects {
		return fmt.Errorf("legacy object storage counters do not match active objects")
	}
	var mismatched int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_object_namespaces n WHERE n.observed_bytes<>(SELECT COALESCE(SUM(o.size_bytes),0) FROM app_platform_storage_objects o WHERE o.namespace_id=n.id) OR n.object_count<>(SELECT COUNT(*) FROM app_platform_storage_objects o WHERE o.namespace_id=n.id) OR EXISTS(SELECT 1 FROM app_platform_storage_objects o WHERE o.namespace_id=n.id AND n.state<>'active')`).Scan(&mismatched); err != nil {
		return err
	}
	if mismatched != 0 {
		return fmt.Errorf("legacy object namespace counters do not match active objects")
	}
	return nil
}

func migration0017() migration {
	tables, indexes := platformLegacyObjectSchemaTables(), platformLegacyObjectSchemaIndexes()
	m := migration{
		id:          "0017",
		restore:     restoreRule{olderDumpOK: true},
		restoreSeed: ensurePlatformObjectStorageSeed,
		verify:      verifyPlatformLegacyObjectStorage,
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
		return ensurePlatformObjectStorageSeed(e)
	}
	return m
}

func migration0018() migration {
	tables, indexes := platformObjectSchemaTables(), platformObjectSchemaIndexes()
	var added []platformTable
	for _, table := range tables {
		if table.name != "plugin_tables" {
			continue
		}
		added = append(added, table)
	}
	m := migration{
		id:      "0018",
		restore: restoreRule{olderDumpOK: true, upgradeOnRestore: true},
		verify:  verifyPlatformObjectStorage,
	}
	for _, table := range added {
		m.tables = append(m.tables, platformPrefix+table.name)
		for _, column := range table.columns() {
			m.columns = append(m.columns, colRef{platformPrefix + table.name, column.name})
		}
	}
	for _, index := range indexes {
		m.indices = append(m.indices, index.name)
	}
	m.up = func(e migExec) error {
		for _, table := range added {
			if _, err := e.exec(table.ddl(e.s.driver)); err != nil {
				return err
			}
		}
		for _, index := range indexes {
			if _, err := e.exec(index.ddl()); err != nil {
				return err
			}
		}
		if err := ensurePlatformObjectStorageSeed(e); err != nil {
			return err
		}
		return migratePlatformObjectNamespacesToPluginTables(e)
	}
	return m
}

func migratePlatformObjectNamespacesToPluginTables(e migExec) error {
	if !e.tableExists(platformPrefix+"storage_objects") || !e.tableExists(platformPrefix+"object_namespaces") {
		return verifyPlatformObjectStorage(e)
	}
	rows, err := e.query(`SELECT DISTINCT i.app_id FROM app_platform_storage_objects o JOIN app_platform_object_namespaces n ON n.id=o.namespace_id JOIN app_platform_installations i ON i.id=n.origin_installation_id WHERE n.state='active' AND i.desired_state<>'removed' ORDER BY i.app_id`)
	if err != nil {
		return err
	}
	var appIDs []string
	for rows.Next() {
		var appID string
		if err := rows.Scan(&appID); err != nil {
			rows.Close()
			return err
		}
		appIDs = append(appIDs, appID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, appID := range appIDs {
		plugin, err := ensurePlatformPluginTable(e, appID)
		if err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO `+plugin.TableName+`(id,installation_id,owner_username,owner_account_created,owner_account_generation,collection,label,media_type,content,content_sha256,size_bytes,revision,idempotency_key,created_ms,updated_ms)
			SELECT o.id,n.origin_installation_id,o.owner_username,o.owner_account_created,o.owner_account_generation,o.collection,o.label,o.media_type,o.content,o.content_sha256,o.size_bytes,o.revision,o.idempotency_key,o.created_ms,o.updated_ms
			FROM app_platform_storage_objects o JOIN app_platform_object_namespaces n ON n.id=o.namespace_id JOIN app_platform_installations i ON i.id=n.origin_installation_id
			WHERE i.app_id=? AND n.state='active' AND i.desired_state<>'removed' AND NOT EXISTS(SELECT 1 FROM `+plugin.TableName+` p WHERE p.id=o.id)`, appID); err != nil {
			return err
		}
		now, err := platformDatabaseNow(e)
		if err != nil {
			return err
		}
		if err := refreshPlatformPluginUsage(e, plugin, now); err != nil {
			return err
		}
	}
	if _, err := e.exec(`DELETE FROM app_platform_storage_objects`); err != nil {
		return err
	}
	if _, err := e.exec(`DELETE FROM app_platform_object_namespaces`); err != nil {
		return err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return err
	}
	if err := refreshPlatformObjectStorage(e, now); err != nil {
		return err
	}
	return verifyPlatformObjectStorage(e)
}

func ensurePlatformObjectStorageSeed(e migExec) error {
	_, err := e.exec(`INSERT INTO app_platform_object_storage(id,max_bytes,max_objects,used_bytes,used_objects,revision,updated_ms)
		SELECT 1,?,?,0,0,1,? WHERE NOT EXISTS(SELECT 1 FROM app_platform_object_storage WHERE id=1)`,
		platformObjectGlobalMaxBytes, platformObjectGlobalMaxObjects, time.Now().UnixMilli())
	return err
}
