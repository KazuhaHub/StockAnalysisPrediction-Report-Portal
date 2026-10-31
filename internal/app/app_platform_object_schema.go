package app

import (
	"fmt"
	"time"
)

const (
	platformObjectGlobalMaxBytes   int64 = 2 << 30
	platformObjectGlobalMaxObjects int64 = 65536
	platformObjectNamespaceBytes   int64 = 256 << 20
	platformObjectNamespaceObjects int64 = 4096
)

func platformObjectSchemaTables() []platformTable {
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

func platformObjectSchemaIndexes() []platformIndex {
	return []platformIndex{
		{name: "uq_ap_object_origin", table: "object_namespaces", columns: "origin_installation_id", unique: true},
		{name: "uq_ap_object_owner", table: "object_namespaces", columns: "owner_installation_id", predicate: "owner_installation_id IS NOT NULL", unique: true},
		{name: "idx_ap_object_namespace_state", table: "object_namespaces", columns: "state,updated_ms"},
		{name: "uq_ap_object_idempotency", table: "storage_objects", columns: "namespace_id,owner_username,owner_account_created,owner_account_generation,idempotency_key", unique: true},
		{name: "idx_ap_object_owner_list", table: "storage_objects", columns: "namespace_id,owner_username,owner_account_created,owner_account_generation,updated_ms,id"},
		{name: "idx_ap_object_cleanup", table: "storage_objects", columns: "namespace_id,updated_ms"},
	}
}

func verifyPlatformObjectStorage(e migExec) error {
	var maxBytes, maxObjects, usedBytes, usedObjects int64
	if err := e.queryRow(`SELECT max_bytes,max_objects,used_bytes,used_objects FROM app_platform_object_storage WHERE id=1`).Scan(&maxBytes, &maxObjects, &usedBytes, &usedObjects); err != nil {
		return err
	}
	var actualBytes, actualObjects int64
	if err := e.queryRow(`SELECT COALESCE(SUM(size_bytes),0),COUNT(*) FROM app_platform_storage_objects`).Scan(&actualBytes, &actualObjects); err != nil {
		return err
	}
	if maxBytes != platformObjectGlobalMaxBytes || maxObjects != platformObjectGlobalMaxObjects || usedBytes != actualBytes || usedObjects != actualObjects {
		return fmt.Errorf("object storage counters do not match active objects")
	}
	var mismatched int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_object_namespaces n WHERE n.observed_bytes<>(SELECT COALESCE(SUM(o.size_bytes),0) FROM app_platform_storage_objects o WHERE o.namespace_id=n.id) OR n.object_count<>(SELECT COUNT(*) FROM app_platform_storage_objects o WHERE o.namespace_id=n.id) OR EXISTS(SELECT 1 FROM app_platform_storage_objects o WHERE o.namespace_id=n.id AND n.state<>'active')`).Scan(&mismatched); err != nil {
		return err
	}
	if mismatched != 0 {
		return fmt.Errorf("object namespace counters do not match active objects")
	}
	return nil
}

func migration0017() migration {
	tables, indexes := platformObjectSchemaTables(), platformObjectSchemaIndexes()
	m := migration{
		id:          "0017",
		restore:     restoreRule{olderDumpOK: true},
		restoreSeed: ensurePlatformObjectStorageSeed,
		verify:      verifyPlatformObjectStorage,
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

func ensurePlatformObjectStorageSeed(e migExec) error {
	_, err := e.exec(`INSERT INTO app_platform_object_storage(id,max_bytes,max_objects,used_bytes,used_objects,revision,updated_ms)
		SELECT 1,?,?,0,0,1,? WHERE NOT EXISTS(SELECT 1 FROM app_platform_object_storage WHERE id=1)`,
		platformObjectGlobalMaxBytes, platformObjectGlobalMaxObjects, time.Now().UnixMilli())
	return err
}
