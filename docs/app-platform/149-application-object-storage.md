# Static application object storage

Status: H05 development implementation, 2026-10-30. The owner approved the
physical D2 direction and required uninstall to clear application data.

## Storage model

Migration `0017` adds three Portal tables:

- `app_platform_object_storage`, the singleton global byte/object counter;
- `app_platform_object_namespaces`, one lifecycle/accounting row per installation
  lineage; and
- `app_platform_storage_objects`, bounded object bytes and metadata for all
  applications, isolated by namespace and exact account instance.

Plugin data does not live in `reports`, `users` or any existing business table.
The shared object table is intentional: every row carries an opaque namespace
rooted in one installation, and all reads/writes include that namespace and the
current account creation/generation. Dynamic per-plugin tables or databases are
not used.

The namespace is created lazily by the first successful object create. A fresh
static application can launch and return an empty list without manufacturing
data or requiring a runner.

## Limits and transactions

- one object: 0..1 MiB;
- one account in one namespace: 32 MiB / 512 objects;
- one namespace: 256 MiB / 4,096 objects;
- all namespaces: 2 GiB / 65,536 objects; and
- list page: 1..50 entries.

All mutation paths lock the singleton and namespace before an object row. SQLite
serializes the transaction; PostgreSQL uses row locks. Quota checks, content,
digest, counters and revisions commit together. A database-full or failed
transaction leaves no partial object.

Create request IDs are unique within the exact namespace/account instance.
Repeating the same request and bytes returns the same object; changed bytes or
metadata under that request ID returns conflict. Update and delete require the
current revision.

## Authority

The app route is:

```text
POST /api/app-platform/installations/{installation}/objects
```

It accepts only `list`, `get`, `create`, `update` and `delete` actions using the
current launch binding. Portal checks the current session/account, application
access, installation generation, policy revision, release, namespace owner and
`storage.files` grant inside the same transaction that performs the operation.

Object owner identity is username plus account creation and generation.
Recreating a username does not inherit old objects. A caller cannot provide a
namespace, owner, path, table name, SQL, URL or arbitrary database handle.

## Uninstall and account deletion

Uninstall admission revokes launch/application authority and clears the
installation's object rows, namespace counters and global counters in the same
transaction. It records the namespace as `deleted` with no owner and zero
objects/bytes. Reinstall creates a new empty namespace.

Deleting an account removes only that account instance's objects and recomputes
the affected namespace counters before the account transaction commits. There
is no automatic object adoption or retention after uninstall.

## Backup and restore

The three tables are declared migration products and enter logical backups
automatically. New dumps must contain their rows and columns. An older dump that
predicts `0017` restores an empty store and the migration seed hook creates the
singleton row before product verification.

Restore verifies row/column shape, active content length, digest fields,
namespace/global counter sums and the singleton before commit. Reads still
recompute the content digest and fail closed if storage was corrupted after
restore.

## SDK and Notes

`AppFrameV2` installs `createAppObjectsTransport(binding)` only for a current
`storage.files` grant. The bridge uses an independent request ID/map with a
two-request ceiling, cancellation and timeout. Apps SDK `useAppObjects()` exposes
stable `available`, `ready`, `list`, `get`, `create`, `update` and `delete`.

The static `apps/notes-sample` uses only this hook. It stores bounded JSON note
content, handles conflict/storage/uncertain errors, keeps the editor draft on a
failed save, and does not use localStorage, the parent DOM or a backend.

## Verification

Local focused tests cover:

- SQLite/PostgreSQL schema and counter products;
- create idempotency, list/get/update/delete and stale revisions;
- per-user, per-namespace and global quota admission;
- cross-account and old-binding isolation;
- account deletion, uninstall cleanup and empty reinstall state;
- new and pre-`0017` backup restore;
- Portal bridge, HTTP transport, frame wiring and the real React hook; and
- the static Notes create/list/open/save flow.

The Linux browser fixture also requires `SDK_NATIVE_BROWSER_OBJECTS`: it writes a
real object through AppRoot/bridge/Portal, reloads the app, reads it back, and
then confirms the old frame receives `unavailable` after application access is
revoked. Exact CI evidence is recorded in the handoff progress table.

The paired Apps candidate is
`78531bfe8c943ce0edc45e3b810163d5b62da357`; it contains the generated bridge,
`useAppObjects`, documentation and the static Notes reference application.
