# D2 extension proposal: static application object storage

Status: owner-approved D2 direction, 2026-10-30. The owner explicitly requires
uninstall to clear the application's object data; reinstall starts empty. This
supersedes the earlier retained-by-default lifecycle for this object store.

## Approved decision

Approve three new Portal database tables:

- `app_platform_object_storage`, a singleton global admission/counter row;
- `app_platform_object_namespaces`, one lifecycle/accounting row per application
  installation lineage; and
- `app_platform_storage_objects`, typed metadata and bounded bytes for each
  current-viewer object.

The v1 object payload is stored in the Portal database, not on the managed
runner data root. This deliberately lets a static application persist objects
without a runner, backend, Docker, filesystem path or manual pairing.

Object bytes use the Portal database's existing backup and at-rest operating
boundary. This proposal does not add per-object encryption or a second vault key
hierarchy. Applications must not place platform credentials or secrets in object
content; approved credential use remains H06.

The existing `app_platform_data_namespaces` remains the managed runner/XFS
namespace for application backends and writable code. It is not reused for this
Portal-managed object store. Mixing XFS byte accounting with database payloads
would make both quota and restore evidence ambiguous.

## Scope

The first approved object API is intentionally small:

- opaque platform-owned object IDs;
- current-account-only ownership;
- bounded opaque bytes and safe display metadata;
- list, read, create, full replace and delete;
- optimistic revision checks;
- create-request idempotency;
- per-user, per-namespace and global byte/object limits; and
- revocation-first, resumable deletion on uninstall with no data adoption.

There is no arbitrary path, file descriptor, SQL, table name, query language,
directory traversal, symlink, executable upload, public share, cross-user
collaboration or app-supplied owner field.

This API serves the Notes reference application. A future large-file or managed
backend store can use a separately approved runner-backed extension. It does not
implicitly inherit this D2 approval.

## Tables

Logical type names follow the existing platform schema declarations. `ID` is a
lowercase UUID, `SHA` is 64 lowercase hexadecimal characters, `R` is a positive
64-bit revision, `N` is a nonnegative 64-bit integer, `MS` is Unix milliseconds
in UTC, `T(n)` is bounded text and `BLOB?` is nullable binary data.

### `app_platform_object_storage`

Singleton and global admission/counter record.

| Column | Type | Null/default | Rule |
| --- | --- | --- | --- |
| `id` | `N` | not null | primary key, check `id=1` |
| `max_bytes` | `N` | not null, 2147483648 | 2 GiB initial global maximum |
| `max_objects` | `N` | not null, 65536 | 65,536 active objects globally |
| `used_bytes` | `N` | not null, 0 | sum of active object bytes |
| `used_objects` | `N` | not null, 0 | count of active objects |
| `revision` | `R` | not null, 1 | increments on every quota/count change |
| `updated_ms` | `MS` | not null | last counter change |

Checks require `used_bytes<=max_bytes`, `used_objects<=max_objects`, and either
both counters zero or both positive. The migration seeds exactly one row. A
missing, duplicate or inconsistent row refuses object admission and startup
product verification.

All object mutations lock this row. This intentionally serializes short metadata
transactions for the initial small-object store. No network, file or application
code runs while the row is locked.

### `app_platform_object_namespaces`

One durable object namespace per application installation lineage.

| Column | Type | Null/default | Rule |
| --- | --- | --- | --- |
| `id` | `ID` | not null | primary key |
| `origin_installation_id` | `ID` | not null | installation that first created it |
| `owner_installation_id` | `ID?` | nullable | current active installation |
| `state` | `T(24)` | not null | `active`, `deleting`, `recovery_required`, `deleted` |
| `data_format` | `N` | not null, 1 | application object schema version |
| `quota_bytes` | `N` | not null, 268435456 | 256 MiB namespace maximum |
| `observed_bytes` | `N` | not null, 0 | active object byte sum |
| `object_count` | `N` | not null, 0 | active object count |
| `revision` | `R` | not null, 1 | namespace lifecycle/accounting revision |
| `deleted_ms` | `MS?` | nullable | set only after content cleanup |
| `created_ms` | `MS` | not null | creation instant |
| `updated_ms` | `MS` | not null | last lifecycle/accounting change |

Constraints:

- `origin_installation_id` references `app_platform_installations(id)` with
  `RESTRICT`;
- `owner_installation_id` references the same table with `SET NULL`;
- unique `origin_installation_id` prevents two independent histories for one
  installation lineage;
- partial unique `owner_installation_id` where non-null permits at most one
  active owner for a namespace;
- `deleted` requires `deleted_ms`, zero bytes and zero objects;
- `observed_bytes<=quota_bytes`; and
- `updated_ms>=created_ms`.

Indexes:

- partial unique `owner_installation_id` where non-null;
- `(state,updated_ms)` for deletion/recovery reconciliation.

### `app_platform_storage_objects`

| Column | Type | Null/default | Rule |
| --- | --- | --- | --- |
| `id` | `ID` | not null | opaque platform-generated primary key |
| `namespace_id` | `ID` | not null | references object namespace `RESTRICT` |
| `owner_username` | `T(320)` | not null | current account name, not caller-supplied |
| `owner_account_created` | `T(64)` | not null | prevents deleted/recreated username inheritance |
| `owner_account_generation` | `N` | not null | current account generation binding |
| `collection` | `T(32)` | not null | lower-case app partition such as `note` |
| `label` | `T(256)` | not null | plain display label, never HTML |
| `media_type` | `T(128)` | not null | canonical bounded media type |
| `content` | `BLOB` | not null | exact bounded bytes |
| `content_sha256` | `SHA` | not null | digest of exact content |
| `size_bytes` | `N` | not null | 0..1,048,576 |
| `revision` | `R` | not null, 1 | optimistic replace/delete version |
| `idempotency_key` | `T(128)` | not null | create request identity |
| `created_ms` | `MS` | not null | creation instant |
| `updated_ms` | `MS` | not null | last mutation instant |

Constraints:

- namespace reference uses `RESTRICT`;
- every row requires content, digest, `size_bytes` and its create request ID;
- owner fields are nonempty and account generation is positive;
- revision is positive and `updated_ms>=created_ms`; and
- application verification additionally requires `length(content)=size_bytes`
  and a matching SHA-256 before returning bytes.

Indexes:

- unique create idempotency
  `(namespace_id,owner_username,owner_account_created,owner_account_generation,idempotency_key)`;
- owner list
  `(namespace_id,owner_username,owner_account_created,owner_account_generation,updated_ms,id)`;
- namespace cleanup `(namespace_id,updated_ms)`.

Labels are not unique. The platform object ID is the address. The API never
derives a path or table name from `collection`, label or media type.

## Limits

Initial defaults, all enforced server-side:

| Limit | Value |
| --- | --- |
| Exact object bytes | 0..1,048,576 |
| Active objects per account per namespace | 512 |
| Active bytes per account per namespace | 33,554,432 |
| Active objects per namespace | 4,096 |
| Active bytes per namespace | 268,435,456 |
| Active objects globally | 65,536 |
| Active bytes globally | 2,147,483,648 |
| List page size | 1..50, default 20 |
| Collection | 1..32 lower-case ASCII letters/digits/underscore |
| Label | at most 256 UTF-8 bytes, no NUL/control line break |
| Media type | at most 128 printable ASCII bytes |
| Bridge request/response envelope | at most 8 MiB |

Per-account limits prevent one user from consuming an entire installation
namespace. Global and namespace counters change in the same transaction as the
object. Exceeding a limit returns `storage_full`; querying another owner's
objects returns the same unavailable result as a missing object.

There are no temporary upload files. The exact payload arrives in one bounded
request, is verified and committed directly in the database transaction. A
database-full, cancellation or lost response leaves no partial active object.

## API and concurrency

The parent-only endpoint is:

```text
POST /api/app-platform/installations/{installation}/objects
```

Every request contains the current launch `binding`. The exact action union is:

```text
{action:"list", collection, cursor?, limit?}
{action:"get", id}
{action:"create", requestId, collection, label, mediaType, contentBase64}
{action:"update", id, expectedRevision, label, mediaType, contentBase64}
{action:"delete", id, expectedRevision}
```

Unknown fields, trailing JSON, mixed actions, malformed base64, oversized bytes
and noncanonical counters are refused. The endpoint fixes the installation and
derives namespace, account identity and ownership server-side.

Admission repeats the launch binding, current `storage.files` grant, current
session/account generation, namespace owner and application access before and
after the transaction. A missing capability, revoked app access, deleting/
recovery-required namespace, changed account generation or changed launch
binding fails closed.

The mutation transaction locks the singleton counter row and namespace row before
the object row. SQLite's single writer provides the same ordering. The create
path rechecks both quota sums, inserts the object and advances all counters in
one commit.

Create idempotency is scoped to the exact namespace/account instance. Repeating
the same request ID and exact payload returns the existing object. Reusing the
request ID with different bytes or metadata returns `conflict`.

Update and delete require the exact current revision. A stale revision returns
`conflict` and never overwrites newer content. A lost success response is
`uncertain`; a retry normally observes a stale revision and must list/get before
deciding what to do. The SDK never retries automatically.

List uses a stable `(updated_ms,id)` cursor, returns only
the current account instance and bounded summaries without content. Get returns
one object and content only to that same instance. Delete removes the object row
entirely, so neither reads nor later storage maintenance can recover its bytes
or metadata.

## Lifecycle

### Install and uninstall

A frontend install declaring required `storage.files` becomes launchable without
runner data. Its active object namespace and `data_format=1` binding are created
on the first object create, in the same transaction as that object. List/get on
a fresh installation therefore return an empty/missing result without
manufacturing storage. Other frontend capabilities do not create a namespace.
Optional `storage.files` remains denied unless explicitly selected by the
install review.

Uninstall first revokes app access, launch authority, object API authority and
all capability grants. It then moves the namespace to `deleting`; no object can
be read or written in that state. Object rows are deleted in bounded batches and
the global/namespace counters are reduced in the same transaction as each batch.
After the last batch the namespace records `deleted` with zero objects and zero
bytes.

Deletion is idempotent and transactional. A crash, database error or cancellation
leaves `deleting` or `recovery_required` with access still denied. Recovery
continues only the owned namespace's cleanup and never follows an app-supplied
name, path or table. A reinstall always gets a fresh namespace and empty data;
there is no retained-data adoption path for this store.

### Account and OU changes

Ownership is the exact current account instance, not username alone. A recreated
username has a different `owner_account_created` and generation and cannot list,
read or claim old objects. Moving the same account between OUs does not silently
transfer or share personal Notes.

Deleting an account deletes that account's active objects and advances namespace/
global counters in the same Portal transaction as the account sweep. The object
content is not retained as an orphan. Audit records contain object counts and
safe IDs, never note text.

Administrator database access remains an operational capability, not an
application API. `storage.files` does not provide content-level administrator
search or export.

## Migration and backup

The migration is one append-only ADR 0035 step. It creates the three tables, their
checks, indices and singleton row, declares every product and leaves the frozen
baseline untouched. A database without the step upgrades transactionally. A
database or dump claiming the step without all products is refused.

The tables are included automatically in logical backup enumeration. The payload
is part of the same Portal backup as namespace metadata, counters, installations
and grants. There is no second runner file set to coordinate for this store.

An older dump without this migration is allowed to restore with an empty object
store because the step backfills no existing rows. A new dump must contain all
three tables and columns. Restore validates row shape, namespace/counter sums,
content/digest/size rules and the presence of exactly one global row before
commit. Individual reads revalidate content length and SHA-256 and fail closed if
the database was corrupted after restore.

Restore never turns a `deleting`, `recovery_required` or `deleted` namespace into
`active`. Restored deletion state resumes revocation-first cleanup against only
the recorded namespace. Managed runner recovery remains governed by its existing
checkpoint/journal rules. Object data has no automatic background upload,
delivery or migration after restore.

## SDK

Apps SDK `useAppObjects()` will expose stable `available`, `ready`, `list`, `get`,
`create`, `update` and `delete` methods. It converts bounded `Uint8Array` content
to the shared base64 envelope, returns opaque summaries/details and maps stable
errors. It never exposes SQL, paths, storage roots, owner identity or database
handles.

Notes uses one `note` collection and stores application JSON inside its bounded
objects. The shared platform does not parse Note titles or body except for safe
label/media metadata supplied through the API. Version conflicts preserve the
user's draft and present a refresh/overwrite decision; they never silently merge.

## Verification required after approval

- SQLite and PostgreSQL migration, constraints, product verification and
  old/new/corrupt backup restore;
- concurrent create/update/delete, quota races, global/namespace/account limits,
  duplicate request IDs and stale revisions;
- cross-user, cross-app, deleted/recreated account, OU move, admin and old
  generation direct-call denial;
- zero-byte, exact 1 MiB, oversized, malformed base64, NUL/control metadata,
  invalid cursor and query-count leakage;
- cancellation/lost response classification and no automatic retry;
- database-full rollback, interrupted deletion and restore resume;
- static install/uninstall/reinstall, including failed/resumed deletion and an
  empty reinstall;
- real browser Notes create/edit/list/delete, refresh persistence and two-user
  isolation with no runner installed; and
- SDK source pin, real bridge path, language/theme updates and retired closures.

## Approval boundary

Approval covers these three tables, their checks/indices, the stated limits,
bounded Portal-database payload storage, account deletion of owned objects,
revocation-first uninstall cleanup, no data adoption and backup/restore rules.

It does not approve public file sharing, arbitrary filesystem paths, runner file
I/O, collaboration, attachments larger than 1 MiB, a global object-search API,
managed backend migration or production rollout. Those require separate designs.
