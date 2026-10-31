# Application plugin data storage

Status: H05 development implementation, 2026-10-30.

## Storage layout

Migration `0017` remains the immutable legacy namespace/object product.
Migration `0018` adds:

- `app_platform_plugin_tables`, one registry/counter row per application plugin;
- one fixed-schema `app_platform_plugin_<24 hex>` data table per plugin.

It reuses the singleton `app_platform_object_storage` counter introduced by
`0017`, migrates any active legacy rows into the deterministic plugin tables,
retires the legacy rows, and recomputes both plugin and global counters in the
same transaction. Existing databases recorded at `0017` therefore upgrade
without being treated as a fresh schema.

Plugin payloads are never mixed in one shared business table. Every plugin
table has the same reviewed columns and indexes and remains isolated by exact
installation and account instance. The registry contains no payload bytes.

The table name is derived from the verified `app_id`; callers cannot name or
create a table. A row is registered and its table created on the first write.
List/get on a fresh installation returns empty/not-found without creating
storage.

## Mutation boundary

The parent-only endpoint remains:

```text
POST /api/app-platform/installations/{installation}/objects
```

It accepts only list, get, create, update and delete requests carrying the
current launch binding. The Portal checks session/account generation,
application access, installation generation, policy revision, release identity,
the plugin table registry and the current `storage.files` grant.

Create, update and delete lock the global counter and plugin registry before the
row. Quota admission, content, digest, revision, plugin counters and global
counters commit together. A stale revision, malformed request, changed binding
or failed quota check leaves no partial row.

Limits remain 1 MiB per object, 32 MiB/512 objects per account per plugin,
256 MiB/4,096 objects per plugin and 2 GiB/65,536 objects globally.

## Uninstall and account deletion

Uninstall revokes application access, launch authority and capability grants
before serving any cleanup read. In one transaction it:

- removes the installation's plugin rows;
- recomputes global counters;
- drops the dedicated plugin table when no live installation remains; and
- removes the plugin registry row.

The table cannot survive as an orphan containing old payload. Reinstall starts
with a new empty table. If another live installation of the same plugin exists,
only the uninstalled installation's rows are removed.

Account deletion removes only that account's rows from every registered plugin
table and recomputes plugin and global counters before the user transaction
commits.

## Backup and restore

Backups discover plugin tables through `app_platform_plugin_tables` and include
their sections in the dump header. Restore validates the strict table-name
pattern and exact fixed columns, drops unneeded current plugin tables, recreates
the tables declared by the dump, and inserts their rows in the main restore
transaction.

After import, product verification requires:

- one singleton global counter;
- a deterministic table name for every registry row;
- the exact table schema and indexes;
- matching row and byte sums at plugin and global levels;
- no unregistered plugin table; and
- installation/app identity agreement for every payload row.

A dump from before `0017` restores with no plugin tables and the singleton
counter seeded. A dump recorded at `0017` carries legacy rows; restore applies
`0018` and moves those rows into the plugin tables before product verification.

## SDK

`AppFrameV2` exposes the object bridge only when the current launch advertises
`storage.files`. Apps SDK `useAppObjects()` provides bounded `list`, `get`,
`create`, `update` and `delete` operations without exposing SQL, paths, owner
identity or database handles.

The static Notes sample uses only this hook. It stores bounded note content,
handles conflict/uncertain errors, keeps drafts on failure and uses no
`localStorage`, parent DOM or backend.

## Verification

Focused tests cover:

- two plugins receiving distinct data tables;
- create idempotency, list/get/update/delete and revision conflict;
- private account and old-binding isolation;
- per-account, per-plugin and global quota admission;
- account deletion and transactional uninstall cleanup;
- empty table/store after uninstall and fresh reinstall behavior;
- dynamic plugin table discovery in backup and restore;
- pre-`0017` restore with an empty seeded store;
- `0017` to `0018` object migration with legacy-table retirement; and
- SQLite and PostgreSQL schema/product verification.

Full PostgreSQL/native browser acceptance and complete CI remain H05 evidence
gates.
