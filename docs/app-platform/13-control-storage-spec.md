# Initial control-plane and runner storage specification

Status: initial physical design approved by the owner on 2026-10-24 Pacific,
covering Option A and the scope below (reviewed source `419cd28`). Implementation
and validation remain separate; approval does not assert that migration, runner
journal or deployment is complete.

This refines [the logical storage proposal](05-storage-proposal.md) for the first
P03 persistence slice. It covers install/update/disable/uninstall/restore control
state, policy review, artifact identity and runtime recovery. The full platform
still includes business storage, secret bindings, schedules, event delivery and
notifications; their later physical products need a separate extension proposal.
This slice is not a replacement for those work packages or a complete platform.

## 1. Decision in ordinary terms

Recommended Option A:

- Portal keeps application installation records and permissions in its existing
  database, supporting SQLite and PostgreSQL. Add 14 named control tables.
- Store the bounded original frontend ZIP in that database. Keep the exact bytes
  needed to verify the signed artifact again after backup/restore; do not rely on
  an unbound derived file list. This needs no new static-app filesystem deployment.
- The runner has one small local SQLite journal for the whole runtime, with four
  tables. It records actual process/data effects; it is not another database per app.
- Managed apps have isolated data directories/volumes. A separate database server
  per app is not required. Apps choose business files/data formats later within
  approved storage capabilities.
- Uninstall retains data by default; deletion is separate. Reinstall uses a new
  installation identity and fresh grants, with an explicit retained-data adoption.
- Restore leaves execution and delivery paused for review. It never trusts a
  copied database as proof that an old running process stopped.

Option B keeps only Portal metadata without a runner journal. It is simpler on
paper but cannot independently resolve an acknowledged versus uncertain runtime
effect after outages/restore. Option A is recommended for the recovery guarantees
already required by the plan.

Approval of this design permits its named schema products and transaction tests.
It does not approve privileged runtime deployment, public exposure, real provider
credentials, a release, or production migration. D1/D3/D4/D6 remain independent.

## 2. Physical conventions

All columns listed below are non-null with no implicit default unless marked `?`.
The suffix `?` means SQL NULL. Application creation supplies explicit values.

| Type notation | SQLite | PostgreSQL | Meaning |
| --- | --- | --- | --- |
| `ID` | TEXT | TEXT | Random UUID, canonical lowercase, 36 characters |
| `SHA` | TEXT | TEXT | Lowercase SHA-256 hex, 64 characters |
| `T(n)` | TEXT | TEXT | UTF-8 text bounded to n bytes by repository validation |
| `N` | INTEGER | BIGINT | Nonnegative signed 64-bit integer |
| `R` | INTEGER | BIGINT | Positive revision/generation, 1..MaxInt64 |
| `MS` | INTEGER | BIGINT | UTC Unix milliseconds, nonnegative |
| `F` | INTEGER | INTEGER | Exactly 0 or 1, checked by SQL |
| `J(n)` | TEXT | TEXT | Strict, bounded, typed JSON; no duplicate keys or unknown executable fields |
| `B(n)` | BLOB | BYTEA | Bytes bounded by the stated limit |
| `C` | repeated columns | repeated columns | `created_ms MS, updated_ms MS` |

`C` is documentation shorthand, not a new database type. DDL must spell out its
columns. Use checks for enums, booleans, nonnegative/positive counters and byte
lengths where portable. Repository validation also checks UUID/hex/text/JSON and
cross-record facts. Text byte limits use byte length, not character count.

Go's pure operation model uses uint64. The repository refuses values above
MaxInt64 before binding to SQL; it never casts/wraps them into negative values.
Counter exhaustion refuses a mutation without changing state.

Use foreign keys and the indexes listed below on both drivers. The current Portal
source has no declared foreign keys and does not enable SQLite foreign-key checks.
This proposal therefore explicitly includes connection initialization that enables
and verifies enforcement on every SQLite connection, including replacements. It
must not assume the single-connection pool enables that setting automatically. No JSONB, PostgreSQL-only arrays,
SQLite rowid assumptions or database-generated identity sequences are needed.
All proposed table names start with `app_platform_`.

References described below as RESTRICT mean no orphan may survive a transaction:
implement them as `ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED`, not immediate
RESTRICT actions. Use deferrable constraints for the CASCADE/SET NULL references
as well; their deletion actions still run when the parent is deleted. This keeps
the existing single-transaction, alphabetical backup deletion/insertion viable.
Do not disable foreign keys for restore. Validate all references at commit and
exercise full/dry restore on both drivers. The distinction follows
[SQLite's deferred/action rules](https://www.sqlite.org/foreignkeys.html) and
[PostgreSQL's constraint rules](https://www.postgresql.org/docs/current/sql-createtable.html).
A startup shape verifier must check keys/actions/deferrability, index definitions,
column types/nullability and checks through the existing migration verify hook.

## 3. Publisher and immutable code records

### 3.1 `app_platform_publishers`

Columns: `id T(64)` primary key; `namespaces_json J(8192)`; `enabled F`;
`revision R`; `C`.

Namespaces are a bounded typed list of exact IDs and explicit prefixes. Empty
means no execution namespace. Trust writes reject overlaps that would permit
publisher takeover. Market URL changes do not create trusted publishers.
Index: primary key only. Referenced publishers are disabled/tombstoned rather
than physically deleted while releases still refer to them.

### 3.2 `app_platform_publisher_keys`

Columns: `publisher_id T(64)`; `key_id T(128)`; `algorithm T(32)`;
`public_key B(4096)`; `not_before_ms MS`; `not_after_ms MS?`;
`revoked_ms MS?`; `revision R`; `C`.

Primary key: `(publisher_id, key_id)`. Foreign key publisher -> publishers,
RESTRICT deletion. Index `(publisher_id, revoked_ms)`. Non-null expiry must be
after not-before. This stores public verification material only. The accepted
algorithm/signature wire format is selected at D3/D4; an arbitrary stored
algorithm string never enables verification. Revocation increments publisher
revision in the same transaction. Referenced keys are never overwritten with
new key bytes under the same identity.

### 3.3 `app_platform_frontend_artifacts`

Columns: `sha256 SHA` primary key; `archive_bytes N`;
`content B(16777216)`; `created_ms MS`.

Check `archive_bytes > 0` and exact content byte length. Admission rehashes bytes
and runs the bounded frontend inspector before insertion. A digest conflict
compares exact bytes and refuses disagreement; it is not an UPDATE of code.
This table holds original frontend ZIPs only, not OCI image layers or business data.

### 3.4 `app_platform_releases`

Columns: `sha256 SHA` primary key; `app_id T(64)`; `publisher_id T(64)`;
`version T(128)`; `descriptor B(131072)`; `signature B(4096)`;
`verification_key_id T(128)`; `trust_revision R`; `verified_ms MS`;
`frontend_sha256 SHA`; `managed F`; `created_ms MS`.

Unique `(publisher_id, app_id, version)`. Foreign keys `(publisher_id,
verification_key_id)` -> publisher_keys and frontend_sha256 -> frontend_artifacts,
all RESTRICT. Index `(app_id, publisher_id)`. Descriptor digest, frontend binding,
image digest and declarations are revalidated, not inferred from these columns.
Immutable fields cannot be replaced under a release identity. Runtime image
indexes/layers remain in the runner's bounded OCI cache; the immutable descriptor
binds their root digest, and OCI verification binds the selected platform image.

An inspection-only or unsigned candidate does not enter this admitted-release
set. It may be inspected in memory but cannot be activated. Cached trust never
substitutes for current activation/revocation policy.

## 4. Runtime and installation identity

### 4.1 `app_platform_runtime_bindings`

Columns: `id ID` primary key; `deployment_id ID`; `controller_epoch ID`;
`control_revision R`; `enabled F`; `verification_key_fingerprint SHA?`;
`sealed_control_key T(16384)?`; `revision R`; `C`.

Unique deployment_id. The first deployment supports one local Unix-socket runner.
Its socket path is operator infrastructure bootstrap configuration, not an app
field or an arbitrary browser-provided destination. Control signing material is
purpose-sealed by the existing keyring. This does not introduce a new vault/master
key hierarchy. Actual enrollment/key algorithm remains gated by D1/D4.

A binding without valid enrollment material cannot be enabled. Static installations
do not need a runtime binding. Restore clears sealed control credentials, disables
bindings and requires deliberate reenrollment against actual runner state.

### 4.2 `app_platform_installations`

Columns: `id ID` primary key; `app_id T(64)`; `publisher_id T(64)`;
`runtime_id ID?`; `desired_state T(16)`; `phase T(32)`;
`generation R`; `revision R`; `policy_revision R`; `control_fence N`;
`controller_epoch ID`; `sealed_identity_seed T(16384)`; `C`.

Desired state enum: enabled, disabled, removed. Phase enum: pending, setup_required,
ready, disabled, recovery_required, quarantined, removed, review_required.
Foreign keys publisher -> publishers and runtime_id -> runtime_bindings, RESTRICT.
Partial unique index on app_id WHERE desired_state <> 'removed'. This provides
one live installation of a product; removed tombstones may coexist for retained data.
Index `(phase, updated_ms)` for bounded reconciliation.

The seed is independent of rotating control keys/generations and is never exposed
to apps. Update preserves it. Reinstall creates a new ID/seed. Deleted/recreated
users or OUs cannot recover old grants through an installation name.

Generation changes fence execution authority. Revision tracks administrator-visible
state. Policy revision tracks grants. Control fence increases across worker claims
and operations within this installation; it never resets at each operation.

### 4.3 `app_platform_data_namespaces`

Columns: `id ID` primary key; `origin_installation_id ID`;
`owner_installation_id ID?`; `state T(24)`; `data_format N`;
`quota_bytes R`; `observed_bytes N`; `writer_fence N`; `revision R`;
`retained_ms MS?`; `deleted_ms MS?`; `C`.

States: active, retained, deleting, deleted, review_required. Foreign key origin ->
installations RESTRICT; owner -> installations SET NULL. Partial unique index on
non-null owner_installation_id; index `(state, updated_ms)`. The first contract
provides one root namespace per installation. Paths derive from IDs under a
runner-owned root; no absolute app-supplied path is stored here.

Data format 0 means not prepared; a ready app requires its declared positive
format. Observed bytes are diagnostic, never quota-enforcement evidence. Default
quota 256 MiB; admission reserves snapshot capacity before a data-changing update.
Writer fence is namespace-owned and increases across adoption, even with a new
installation ID. Logical fences do not stop direct filesystem writes: the runner
must prove an old writer stopped before mounting the namespace into a new writer.

### 4.4 `app_platform_activations`

Columns: `id ID` primary key; `installation_id ID`; `release_sha256 SHA`;
`generation R`; `policy_revision R`; `data_namespace_id ID?`;
`data_format N`; `state T(16)`; `readiness_evidence J(8192)`;
`observed_ms MS?`; `C`.

States: candidate, active, retired. Foreign keys installation -> installations,
release -> releases, data namespace -> data_namespaces, RESTRICT. Unique
`(installation_id, generation)`. Partial unique installation_id WHERE state='active'.
No circular installation/activation foreign key is needed: readers select the
active row and require matching current installation generation and policy. Audience-only edits
update the active policy revision with the audience transaction, without changing
code/data. Effective capability changes use reviewed generation activation rather
than editing an immutable code tuple or inheriting authority silently.
Readiness evidence is bounded safe contract/digest evidence, never raw app stderr.

## 5. Review, idempotency and external effects

### 5.1 `app_platform_review_plans`

Columns: `id ID` primary key; `installation_id ID?`; `app_id T(64)`;
`publisher_id T(64)`; `action T(24)`; `source_release_sha256 SHA?`;
`target_release_sha256 SHA?`; `installation_revision N`;
`policy_revision N`; `trust_revision R`; `controller_epoch ID`;
`actor_scope SHA`; `username T(320)?`; `account_created T(64)`;
`account_generation N`; `authority_fingerprint SHA`; `plan_sha256 SHA`;
`plan_json J(65536)`; `expires_ms MS`; `consumed_ms MS?`; `C`.

Foreign keys installation/releases/publisher RESTRICT, username -> users SET NULL.
Index `(expires_ms, consumed_ms)`. Actions are the existing pure Action enum.
New install uses null installation and revision 0; existing actions require a
matching installation/revision. Target/source nullability is validated per action.

Plan JSON is a strict typed review projection, not a storage container for arbitrary
subsystems. It binds release, grants, data effect and required runtime evidence.
No raw secrets, destination credentials or user-directory snapshot is stored.
Expiry defaults to 15 minutes. Current administrator/session/account/OU authority,
trust policy and plan digest are rechecked at consumption.

Actor scope is an opaque HMAC of the current account lifecycle identity, using a
purpose-sealed platform actor seed in existing meta storage. It is not a session
ID or username alone. The seed is backed up under existing keyring custody. User
labels may be cleared while the opaque scope still prevents idempotency collisions.

### 5.2 `app_platform_operations`

Columns: `id ID` primary key; `installation_id ID`; `review_plan_id ID`;
`actor_scope SHA`; `idempotency_sha256 SHA`; `request_sha256 SHA`;
`action T(24)`; `managed F`; `source_generation N`; `target_generation R`; `stage T(32)`;
`state_revision R`; `row_revision R`; `lease_owner ID?`;
`lease_until_ms MS?`; `fence N`; `controller_epoch ID`;
`error_code T(96)?`; `completed_ms MS?`; `C`.

Foreign keys installation and plan RESTRICT. Unique review_plan_id and unique
`(actor_scope, idempotency_sha256)`. Partial unique installation_id WHERE
completed_ms IS NULL. Index `(completed_ms, lease_until_ms, updated_ms)`.

Action/stage use the existing pure model. State revision belongs to that model;
row revision also changes for lease bookkeeping. The two must not be conflated.
Fresh install uses source generation 0 and target 1; ordinary existing-installation
actions reserve current generation + 1. Download/stage may preserve the old active
generation; revocation/quiescence installs the target generation before new authority.
Stage-specific CAS checks admit only that source/target transition, not any generation.
Lease owner/until are both present or both absent. An admitted unclaimed operation
starts queued, state/row revision 1, fence 0 and no lease.

Complete/failed/recovery_required operations have completed_ms and release their
active-operation slot. A recovery-required installation still refuses ordinary
mutations: only a separately reviewed typed recovery can proceed. Terminal state
is not a request to retry its effects under another ID.

### 5.3 `app_platform_operation_effects`

Columns: `operation_id ID`; `stage T(32)`; `effect_number R`;
`effect_id ID`; `kind T(32)`; `payload_sha256 SHA`;
`runtime_id ID?`; `controller_epoch ID`; `admitted_fence R`;
`observed_fence N`; `state T(16)`; `receipt_json J(16384)`; `C`.

Primary key `(operation_id, stage, effect_number)`, unique effect_id. Foreign keys
operation/runtime RESTRICT. States: prepared, applied, reverted, unknown. Index
`(state, updated_ms)`. Kind is a registered typed effect, never a shell command.
Limit 32 effects per operation stage in repository validation.

Persist intent before external dispatch. A retry reuses effect identity and logical
payload digest; a lease/fence change does not create a fresh migration effect.
An old receipt may be inspected by a new fenced worker but cannot directly commit
current state. The current worker must classify evidence and actual resources;
unknown outcomes remain recovery-required. Keep safe observation history through
the existing audit log rather than logging credentials or raw provider responses.

## 6. Grants and recovery records

### 6.1 `app_platform_access_grants`

Columns: `id ID` primary key; `installation_id ID`; `principal_type T(8)`;
`username T(320)?`; `account_created T(64)?`; `account_generation N?`;
`group_id N?`; `group_created T(64)?`; `revision R`; `C`.

Foreign keys installation RESTRICT, username -> users CASCADE, group_id ->
user_groups CASCADE. Check exactly the user fields or the OU fields are present,
according to principal_type user/ou. User generation follows the existing account
lifecycle generation used by extension identity; it is not a new notification grant.

Partial unique `(installation_id, username, account_created, account_generation)`
for user rows; partial unique `(installation_id, group_id, group_created)` for OU
rows. Index username and group_id for deletion impact discovery.

These are app-use audience grants only. Sensitive business visibility, delegated
service reads and anonymous publication are not implied. Later resource-grant
products must be reviewed separately. Management still requires current administrator
role; no OU row can mint administrator authority.

### 6.2 `app_platform_capability_grants`

Columns: `installation_id ID`; `capability T(96)`; `granted F`;
`policy_json J(16384)`; `policy_revision R`; `review_plan_id ID`; `C`.

Primary key `(installation_id, capability)`. Foreign keys installation and review
RESTRICT. Capabilities use the registered versioned contract. Optional grants
default off; new required permissions require fresh review.

Policy JSON holds typed bounds such as quotas and approved network origins. It
cannot hide user/OU identities, plaintext secrets or independently mutable entities.
Delegated business-resource and secret bindings belong to the later P08 schema.
All current dispatches still require viewer/service authorization, not just this row.

### 6.3 `app_platform_recovery_sets`

Columns: `id ID` primary key; `installation_id ID`; `data_namespace_id ID`;
`operation_id ID`; `release_sha256 SHA`; `data_format R`;
`checkpoint_id ID`; `manifest_sha256 SHA?`; `bytes N`;
`state T(24)`; `controller_epoch ID`; `writer_fence R`;
`verified_ms MS?`; `deleted_ms MS?`; `C`.

Foreign keys installation/namespace/operation/release RESTRICT. Unique checkpoint_id.
Index `(installation_id, state, created_ms)`. States: preparing, verified,
restoring, deleted, review_required. A verified set requires digest and verified_ms.
Checkpoint IDs resolve through runner ownership records, never arbitrary host paths.

A verified snapshot binds compatible code, data format, writer fence and key-recovery
references. Require at least one verified pre-update recovery point before destructive
migration. Failure to stop writers or reserve disk means no snapshot success.

### 6.4 Stable explicit index names

Primary-key indexes are checked structurally rather than by engine-generated name.
The following explicit indexes are migration products; predicates are part of
verification, not just index-name existence.

| Name | Columns / predicate |
| --- | --- |
| `idx_ap_keys_revocation` | publisher_keys(publisher_id, revoked_ms) |
| `uq_ap_release_identity` | UNIQUE releases(publisher_id, app_id, version) |
| `idx_ap_release_product` | releases(app_id, publisher_id) |
| `uq_ap_runtime_deployment` | UNIQUE runtime_bindings(deployment_id) |
| `uq_ap_installation_live` | UNIQUE installations(app_id) WHERE desired_state <> 'removed' |
| `idx_ap_installation_phase` | installations(phase, updated_ms) |
| `uq_ap_namespace_owner` | UNIQUE data_namespaces(owner_installation_id) WHERE owner_installation_id IS NOT NULL |
| `idx_ap_namespace_state` | data_namespaces(state, updated_ms) |
| `idx_ap_namespace_origin` | data_namespaces(origin_installation_id) |
| `uq_ap_activation_generation` | UNIQUE activations(installation_id, generation) |
| `uq_ap_activation_active` | UNIQUE activations(installation_id) WHERE state='active' |
| `idx_ap_plan_expiry` | review_plans(expires_ms, consumed_ms) |
| `uq_ap_operation_review` | UNIQUE operations(review_plan_id) |
| `uq_ap_operation_idempotency` | UNIQUE operations(actor_scope, idempotency_sha256) |
| `uq_ap_operation_active` | UNIQUE operations(installation_id) WHERE completed_ms IS NULL |
| `idx_ap_operation_claim` | operations(completed_ms, lease_until_ms, updated_ms) |
| `uq_ap_effect_identity` | UNIQUE operation_effects(effect_id) |
| `idx_ap_effect_state` | operation_effects(state, updated_ms) |
| `uq_ap_access_user` | UNIQUE access_grants(installation_id, username, account_created, account_generation) WHERE principal_type='user' |
| `uq_ap_access_ou` | UNIQUE access_grants(installation_id, group_id, group_created) WHERE principal_type='ou' |
| `idx_ap_access_username` | access_grants(username) |
| `idx_ap_access_group` | access_grants(group_id) |
| `uq_ap_recovery_checkpoint` | UNIQUE recovery_sets(checkpoint_id) |
| `idx_ap_recovery_installation` | recovery_sets(installation_id, state, created_ms) |

Table names in this matrix omit the common `app_platform_` prefix for readability.
Unique rules already stated above use these indexes, not duplicate additional
indexes. Add no further physical index without recording the schema decision.

## 7. Runner SQLite journal: four tables, one runtime

The journal is local infrastructure, not app-controlled storage. Files/directories
are owner-only; the database is never mounted in app containers. Use a single-writer
connection, foreign keys, WAL and durable synchronous writes. Initialization is one
transaction; PRAGMA user_version tracks its schema version. Unknown nonempty,
newer or damaged shapes are refused before workload activity/repair DDL.

All IDs, counters and byte limits follow the conventions above. Keys are public
verification material; private control signing keys remain sealed in Portal.

| Table | Columns and key rules |
| --- | --- |
| `runner_deployment` | `singleton N` PK checked equal to 1; `deployment_id ID` UNIQUE; `controller_epoch ID`; `control_revision R`; `verification_algorithm T(32)`; `verification_key B(4096)`; `key_fingerprint SHA`; `revision R`; `C` |
| `runner_commands` | `effect_id ID` PK; `operation_id ID`; `installation_id ID`; `stage T(32)`; `effect_number R`; `kind T(32)`; `payload_sha256 SHA`; `target_generation R`; `controller_epoch ID`; `admitted_fence R`; `state T(16)` prepared/applied/reverted/unknown; `receipt_json J(16384)`; `ack_attempted_ms MS?`; `C`; UNIQUE(operation_id,stage,effect_number); index installation_id and (state,updated_ms) |
| `runner_resources` | `id ID` PK; `installation_id ID`; `data_namespace_id ID?`; `kind T(24)` container/volume/socket/artifact; `native_id T(512)`; `release_sha256 SHA?`; `generation N`; `controller_epoch ID`; `control_fence N`; `writer_fence N`; `desired_state T(16)`; `observed_state T(24)`; `ownership_sha256 SHA`; `revision R`; `observed_ms MS?`; `C`; UNIQUE(kind,native_id); index(installation_id,kind) |
| `runner_checkpoints` | `id ID` PK; `data_namespace_id ID`; `installation_id ID`; `operation_id ID`; `release_sha256 SHA`; `data_format R`; `writer_fence R`; `manifest_sha256 SHA?`; `bytes N`; `state T(24)`; `revision R`; `verified_ms MS?`; `C`; index(data_namespace_id,state,created_ms) |

Host UUID references are not cross-database foreign keys. Validate them through
authenticated control admission. Local references to resources/checkpoints are
validated within runner transactions. State enums match the typed runner contract;
unknown actions/statuses fail closed.

Record received intent before runtime effects, then durable observed result, then
acknowledgement attempt. ack_attempted_ms does not prove network delivery. Repeated
commands with the same ID/digest return recorded evidence; changed payload conflicts.
Old controller epoch/fence cannot execute work. A new epoch is explicitly enrolled
by the operator, not accepted merely because a signed random UUID is unfamiliar.

Native runtime IDs/ownership labels must both agree before adoption or deletion.
An unknown container name is not proof of ownership. A live old writer prevents
new activation even if Portal's database says removed or restored.

## 8. Transactions and concurrency

1. **Lock order:** reuse the existing principal-lifecycle advisory lock where grant
   or account/OU validity is involved; then runtime binding, then installation IDs
   in sorted order, then plan/operation/effect rows. Never acquire them in reverse.
   SQLite serializes writes; PostgreSQL uses explicit row locks and CAS. No network
   request, ZIP inspection, Docker action or file snapshot holds a DB transaction.
2. **Plan consumption:** look up an existing idempotency identity first; same input
   returns its operation even if the plan is now consumed. Changed input conflicts.
   Otherwise recheck current authority/trust/revision/expiry, consume the plan and
   insert installation/operation in one transaction. A uniqueness conflict is read
   and classified; it is not treated as successful duplicate execution.
3. **Claims:** sample database UTC milliseconds, conditionally claim queued/expired
   work, increment installation control_fence and copy it into the lease, bump row
   revision. Claim must affect exactly one eligible row. Default lease 30 seconds,
   heartbeat 10 seconds. Expired owners must reclaim with a new fence, not renew
   an expired lease. Use database time rather than independently skewed host clocks.
4. **Stage transition:** require current epoch, generation, fence, lease owner,
   unexpired lease and expected state/row revisions. Validate the pure transition
   and persist it conditionally. A zero-row result is a conflict, not success.
5. **Activation:** retire the previous activation and select the ready candidate,
   effective grants and matching namespace in one transaction. Their generation,
   descriptor, policy revision and data format must agree. Never join a frontend
   from one candidate with a backend from another.
6. **Disable/remove:** persist denial/new generation before shutdown/cleanup. A lost
   runtime response leaves truthful stop/cleanup pending, without broker authority.
   Retained data remains separately owned; deletion requires a newly reviewed action.
7. **Account/OU deletion:** discover affected installation IDs while holding the
   existing lifecycle lock, then preserve the existing user-first deletion rule.
   Cascade audience rows, clear optional actor labels and invalidate affected plans/
   policy caches in the same transaction. Never transfer app grants to a replacement
   OU or recreated username. Extend the principal-sweep tests and explicit hooks.
8. **Actor disappears during an operation:** cleanup/revocation can continue for
   safety. New grant activation requires current authorized review. Do not replay
   business or external effects under another user's identity.

## 9. Migration and restore contract

Add a new ordered migration only after acceptance of this specification. Select
its next free ID against current main at implementation time. No baseline changes,
released-step edits, or runtime acceptance-contract rewrites. All 14 tables,
columns and indexes are declared products, with full constraint verification; create/verify within the
existing migration transaction. Empty DB initialization and existing accepted DB
upgrade must produce the same shape on both drivers.

New protected meta values: purpose-sealed actor identity seed and controller epoch/
restore-pause marker. They contain no app catalogue or business settings. A missing
key or inconsistent seed refuses authority rather than regenerating identities for
existing installations. Existing keyring recovery remains required.

Backup includes original ZIP artifacts, release/trust metadata and all control rows
through migration product enumeration. Managed backup additionally coordinates a
runner-journal checkpoint plus every selected app data checkpoint. A Portal-only
dump remains identifiable as incomplete for managed recovery.

Before destructive restore, verify baseline, migration products, key custody,
artifact digests, checkpoint manifest and data-format/code consistency. An older
backup without these products restores with no v2 installations. A backup claiming
the migration but missing its products is refused. Never import rows into an older
binary by silently discarding unknown columns.

During restore preserve stronger locally known publisher/key revocations; a
copied older trust row cannot clear a live revocation. Disable restored publisher
trust until independent current trust review. Key-byte disagreement under the same
publisher/key identity is a refusal, not automatic replacement.

After restore, rotate controller epoch and installation generations; clear runtime
enrollment credentials; mark restored installations/data/recovery sets review-required;
retire active routing; classify unfinished operations as recovery-required; clear
leases. Do not resume background work, notifications or anonymous publication.
An existing runner with a newer journal rejects stale authority until explicit
operator reenrollment and reconciliation. Stop actual writers before data restore.

Rollback after the migration uses a tested compatible backup and matching binary,
not an old binary opened against unknown new products. Server deployment remains
on its existing beta reference; this specification performs no release or deployment.

## 10. Retention, quotas and implementation evidence

- Review plans: 15-minute validity; expired unreferenced plans removable after 24 hours.
- Completed operations/evidence: 90 days; never collect unresolved effects or records
  pinned by a recovery set. Idempotency retention is explicitly 90 days, not forever.
- Referenced releases, keys and artifacts stay pinned by live/retained/recovery state.
- Uninstalled data stays until explicit deletion; show observed size and policy.
- Remove minimal installation tombstones only after all retained data, recovery and
  required evidence references are gone. No cascade may erase retained business data.
- Runner logs: 10 MiB per installation, rotated, seven-day target. No secrets/raw
  upstream responses. Public artifact cache quota and snapshot reservations are
  enforced before mutation; eviction never removes pinned recovery code.

P03 acceptance must include SQLite and PostgreSQL migration/restore fixtures,
parallel claims, expired-owner fencing, duplicate commit/conflicting payload,
restore against a newer live runner, account/OU deletion and recreated identities,
corrupt artifact/journal refusal and missing-key recovery. The pure model/helper
or green parser tests alone do not satisfy these persistence cases.

## 11. Approval scope and later products

Approved decision: Option A and these initial 14 Portal/four runner tables,
including the named protected meta values, lifecycle cleanup, retention and paused
restore behavior, for implementation in isolated development/staging fixtures.

P08/P09 business records, secret bindings, schedules, jobs, event subscriptions and
outboxes remain in the full delivery scope. Their exact schema is intentionally
not guessed here; propose and review those products before adding them. This
approval is an incremental D2 decision, not an assertion that P03 or the platform
is complete, and not permission for privileged production runtime enrollment.
