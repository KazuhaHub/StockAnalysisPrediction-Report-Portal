# D2 extension proposal: application plugin data storage

Status: owner-approved, 2026-10-30.

The owner requires one dedicated data table per application plugin. Plugin
payloads must not share a catch-all business table, and uninstall must clear the
plugin's data.

## Decision

The Portal database owns two fixed metadata tables:

- `app_platform_object_storage` is the singleton global byte/object counter; and
- `app_platform_plugin_tables` records one physical data table per `app_id`,
  its fixed data format and its byte/object counters.

Each plugin receives a separate fixed-schema table named
`app_platform_plugin_<24 lowercase hex characters>`. The name is derived from
the verified application ID. It is never supplied by an application and cannot
contain a path, SQL fragment or user-controlled identifier.

The table is created in the same transaction as the plugin's first write. A
fresh installation can list an empty collection without creating storage.

Plugin payload rows are not stored in `reports`, `users`, installation tables or
another plugin's table. The two metadata tables contain no user payload.

## Plugin table

Every plugin table has the same reviewed columns:

- opaque object ID;
- exact installation ID;
- current account identity and account generation;
- collection, label and media type;
- bounded content bytes, SHA-256 digest and byte length;
- optimistic revision and create-request idempotency key; and
- creation/update timestamps.

Unique and list indexes are scoped by installation and exact account instance.
A recreated username, a different installation or another plugin cannot read or
claim prior rows.

The initial v1 limits remain:

- 1 MiB per object;
- 32 MiB and 512 objects per account per plugin;
- 256 MiB and 4,096 objects per plugin; and
- 2 GiB and 65,536 objects globally.

The API stays opaque and supports only list, get, create, replace and delete.
It accepts no SQL, table name, filesystem path, owner field or database handle.

## Uninstall

Uninstall is revocation-first and transactionally clears data:

1. app access, launch authority and capability grants are revoked;
2. rows belonging to the installation are removed;
3. plugin and global byte/object counters are decremented;
4. the plugin table is dropped when no live installation uses it; and
5. its registry row is removed.

The revocation, data deletion, counter update, table drop and registry removal
commit together. A failed transaction restores all of them. Reinstall creates a
new empty table and cannot adopt the previous installation's rows.

Account deletion removes only that account's rows from each plugin table and
recomputes the affected counters.

## Backup and restore

Logical backups discover registered plugin tables dynamically. Restore:

- validates plugin table names and their exact fixed columns before import;
- recreates declared plugin tables inside the restore transaction;
- restores payload rows and registry counters together; and
- refuses unregistered tables, name/registry mismatches, stale installation
  rows and counter drift before commit.

A dump that predates migration `0017` restores with an empty plugin store and
the singleton counter seeded. Managed runner/XFS data remains a separate
storage boundary.

## Approval boundary

This decision covers the two metadata tables, dynamically created plugin data
tables with the fixed schema above, the stated limits, account deletion,
transactional uninstall cleanup, and backup/restore discovery.

It does not approve arbitrary schemas, plugin-supplied DDL, SQL passthrough,
public sharing, filesystem paths, collaboration, larger attachments, managed
backend data migration or production rollout.
