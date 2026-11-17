# D2 extension decision: credential custody and application bindings

Status: Option A approved by the owner on 2026-10-29 Pacific for the exact
products and rules below. Implementation and verification remain required. This decision
covers development and isolated tests only, not production privileges, real
credentials, Status adoption or SDK freeze. Initial control-table approval did
not include these independent entities.

## 1. Concrete decision requested

Option A (recommended): add two tables in the existing Portal SQLite/PostgreSQL
database for platform-owned encrypted credentials and application-specific use
bindings. Reuse the existing DEK/AES purpose-sealing and audit/backup infrastructure.
Add no vault server, runner database/table, per-app database or filesystem store.

A credential is custody of one opaque value; a binding is permission to use a
specific credential for a narrow operation in one installation. They have separate
identities/revisions because revoking one app's use must not destroy a credential
used by another installation, and rotating the credential must invalidate all old
request authorizations. Neither record grants app-use audience or report access.

Alternatives:

| Option | Tradeoff |
| --- | --- |
| A: Portal encrypted credential plus explicit app binding | Reuses key custody and current authority; requires two lifecycle-managed products |
| B: Bind to an existing Dify/SMTP/Webhook secret row | Avoids some custody duplication but couples unrelated products, implies cross-product disclosure and cannot cover arbitrary new app credentials safely |
| C: Runner-owned vault or per-app secret file | Duplicates custody/restore authority and creates deployment/manual-pairing work; not selected |

Do not implement A by storing arbitrary rows in `meta`, capability policy JSON or
a new untracked file. Existing capability JSON may contain typed restrictions and
binding IDs only; it cannot contain plaintext, ciphertext or mutable entities.

## 2. Scope and explicit limits

The first implementation permits broker-side use with two registered injection
shapes: HTTP Authorization Bearer and HTTP X-API-Key. No app can select arbitrary
header names, URL/query interpolation, TLS roots, proxy, DNS or destination policy.
Credential bytes are never sent to the browser/app for ordinary `secrets.use`.
Normal foreground uses still require the user's current app/resource authority.

The storage can represent a separately reviewed `raw-read` binding, but that
operation must remain unavailable until the exceptional `secrets.read` consent,
warning, redaction and audit path are implemented and tested. This proposal is
not permission to enable raw disclosure, background delegation, arbitrary network
or provider billing access. Jobs/events/notification schemas remain separate D2
extensions. This is one required P07 storage extension, not full P00–P14 delivery.

Provisional limits: 1..16,384 plaintext bytes per credential, 32,768 bytes sealed
text, 256 credential rows per deployment and 32 binding rows per installation.
Limits include revoked records until explicitly purged. Refuse creation at capacity;
do not silently evict credentials, bindings or uncertain operations. Administrator
metadata lists are paginated, at most 100 rows per page, and expose no secret
prefix/suffix, length, hash or ciphertext.

## 3. Physical conventions

Use the initial control specification's ID/T/SHA/R/MS/F conventions, explicit
created/updated UTC milliseconds, nonnegative counters with overflow refusal,
canonical typed JSON and driver-portable SQL. No generated SQL identity,
PostgreSQL-only types or SQLite rowid assumptions. New tables are append-only
migration products after the current migration tail at implementation time;
keep v0.4.72 and all released migrations unchanged. Do not reserve a migration
number before inspecting that tail.

All references use deferred constraints compatible with atomic backup restore.
Shape verification covers types, nullability, primary/foreign keys, actions,
deferrability, checks and indexes on both drivers. Never disable foreign keys
for restore. All SQL writes, audit and relevant revision updates are atomic.

### 3.1 app_platform_credentials

| Column | Type | Rule |
| --- | --- | --- |
| id | ID | Primary key, random nonzero UUID; never reused |
| label | T(160) | Administrator description; no secret embedded |
| state | T(24) | active, revoked, review_required |
| sealed_value | T(32768)? | Existing purpose-sealed ciphertext; NULL after revocation |
| value_revision | R | Increment on replacement or revocation; never reset |
| revision | R | Every administrative mutation |
| created_ms | MS | Explicit creation instant |
| updated_ms | MS | Last mutation instant |
| revoked_ms | MS? | Present exactly when revoked |

Index `(state, updated_ms, id)`. State checks require revoked to have NULL sealed
value and a revoked instant; active/review_required require nonempty sealed value
and no revoked instant. Physical length/enum/positive/null checks must be verified,
with stricter runtime validation before any decrypt. Empty/unknown envelope or
missing key fails closed, never becomes a plaintext fallback.

Seal using the existing DEK and AES helpers, with AAD purpose `app-platform`
and identity `credential:<id>:<value_revision>`. Preserve the repository's key
custody/backup rules; this is not a new master-key hierarchy. Changing ciphertext
between IDs or value revisions must fail authentication. Metadata update alone
does not reseal the same value. Replacement seals a new value at revision+1 in
the same transaction that invalidates dependent bindings. Plaintext is bounded
before allocation/sealing, kept out of generic errors and cleared after use where
Go ownership permits; do not claim complete erasure of immutable strings/copies.

Credentials are deployment-owned. Creator/updater attribution belongs in the
existing audit log, not a new account-owned grant or independent principal field.
Account deletion therefore does not transfer credential ownership to a recreated
username. Only a current authorized administrator can manage credential metadata,
replace/revoke it or approve bindings; read-side app code cannot enumerate it.

### 3.2 app_platform_secret_bindings

| Column | Type | Rule |
| --- | --- | --- |
| id | ID | Primary key; never reused |
| installation_id | ID | Existing installation; deferred ON DELETE CASCADE |
| name | T(64) | Stable lowercase app binding slug |
| credential_id | ID | Credentials; deferred ON DELETE NO ACTION |
| credential_value_revision | R | Exact current value revision accepted for use |
| operation | T(24) | http_bearer, http_api_key, raw_read |
| state | T(24) | active, revoked, review_required |
| policy_json | J(8192) | Strict typed resource restriction described below |
| policy_revision | R | Exact installation capability policy revision |
| review_plan_id | ID | Existing consumed review; deferred ON DELETE NO ACTION |
| revision | R | Increment on change/revoke/rotation/recovery |
| created_ms | MS | Explicit creation instant |
| updated_ms | MS | Last mutation instant |
| revoked_ms | MS? | Present exactly when revoked |

Unique `(installation_id, name)` for all states. Indexes
`(credential_id, state, id)` and `(installation_id, state, id)`. Enum/null/positive
checks apply. A slug is never a credential identity; lookup always binds the
current installation ID. Uninstall/reinstall uses a new ID and cannot adopt an
old binding by matching app ID/name.

Policy JSON is a versioned discriminated object, not arbitrary configuration:

- HTTP operations contain exactly format, operation and canonical
  `network_policy_sha256`. The digest must reference that installation's exact
  currently reviewed `network.http` policy, restricting origin/method/path/limits.
  http_bearer uses fixed Authorization with `Bearer `; http_api_key uses fixed
  X-API-Key. A binding cannot introduce a destination or broaden network policy.
- raw_read contains exactly format and operation. It is non-callable until the
  exceptional route exists; it cannot be smuggled through an HTTP operation.

No plaintext/ciphertext/header values, account/OU snapshots, arbitrary URLs,
independent subscription/entity records or expression/template language is
allowed in this JSON. Validate complete canonical bytes and hash/binding before
any operation. Format/version selection and fixtures precede implementation;
this proposal does not invent an externally published wire contract.

## 4. Review and grant transaction

A binding is not active just because a row has `state=active`. Admission must
prove all of the following within the current authority transaction:

1. Installation is enabled/current/not restore-paused; signed release/trust and
   active generation are current; actor/session/app-use OU authority is valid.
2. Signed required/optional capability declarations and the exact consumed plan
   cover `secrets.use` (or exceptional `secrets.read`) and the named binding.
3. Current capability row, complete canonical policy and binding's
   policy_revision/review_plan_id match that consumed plan and active policy.
4. Credential is active and exact current value revision matches the binding.
5. Operation and network digest match the selected current network grant. Raw
   disclosure requires its distinct capability and explicit warning/approval.

Fresh binding/retarget/change requires new review containing exact credential ID,
non-secret label, operation, network-policy digest and expected revisions. No
credential value, digest or preview appears in the plan. If any reviewed revision
changes before commit, refuse and regenerate the review. Current administrator
permissions and approval freshness must be rechecked at consumption.

Use lock order consistent with existing principal -> trust -> installation
transactions, then credentials sorted by ID, then binding rows sorted by ID.
Credential-wide operations that affect multiple installations must lock those
installations in sorted ID order before credential/binding updates, recheck the
queried affected set after acquiring locks and retry boundedly on concurrent
changes. Do not invert this order to rotate a shared credential. PostgreSQL row
locks and SQLite serialized writes need matching concurrent tests.

## 5. Rotation, revocation and deletion

**Rotation:** a current administrator selects an existing credential replacement.
Preview shows all affected installation/binding IDs and metadata, expected
revisions and which operations will be paused. In one transaction, increment value
and credential revisions, reseal the replacement, mark all affected active bindings
review_required and increment their revisions. Do not automatically resume them
or copy an old grant to a new value. Each affected installation's reapproval
binds the new revision through a fresh exact review. In-flight old checks refuse.

**Credential revocation:** increment revisions, NULL ciphertext, mark credential
revoked and dependent bindings revoked with instants/revisions in the same
transaction and audit. A value cannot be reactivated under the same ID. A new
credential is a new ID. Binding-only revocation leaves other installations'
credential uses intact, but increments installation policy/binding revision and
invalidates old request authorizations atomically.

**Disable/quarantine/uninstall:** admission stops at existing installation state
before decrypt. Uninstall additionally revokes all bindings; retained business
data carries no active credential rights. Reinstall requires new bindings and
fresh approval. Merely keeping a credential row does not imply use permission.

**Purge:** separate explicit current-admin action, refused while any binding
references the credential. Revoked binding rows can be removed after installation
removal or an explicit safe metadata cleanup; do not delete a review referenced
elsewhere. Keep existing nonidentifying audit evidence under current retention.
Deletion never retries/clears an uncertain outgoing effect or promises to undo it.

**Account/OU changes:** foreground use checks live membership/session on every
checkpoint; a deleted/recreated principal cannot gain old authority. Secret
custody has no account grant to transfer. Future background use needs a separately
approved explicit service grant, never creator/admin impersonation.

## 6. Use boundary and leakage prevention

Decrypt only after current authorization, directly inside the trusted bounded
broker operation. Validate the value for the fixed injection format: printable
nonempty header data with no CR/LF/control bytes. Invalid format refuses without
exposing the bytes. Keep it out of environment variables, image layers, process
arguments, URLs, frontend storage, bridge messages, diagnostics and logs.

The initial runner-side network executor does not yet inject credentials. Custody
approval does not authorize sending decrypted material to the runner through
existing generic framing. Implementation must first document and test a private
peer-authenticated use exchange and current revision checks; it may retain the
secret-consuming operation in Portal if that boundary is preferable. Decide that
transport in code review before enabling a binding; do not improvise it in a UI.

Any target can echo a credential in response bytes. Header redaction alone cannot
make arbitrary upstream bodies safe. Credential-use adapters must define response
projection/redaction before returning data, and tests must prove they do not
return sent secrets, upstream cookies, auth headers, redirect locations or raw
errors. Logs/audit record IDs, operation, revisions and safe outcome only. Raw
read, if implemented later, is deliberate disclosure and has a separate policy.

## 7. Backup, restore, key loss and older backups

Extend existing coordinated backup/restore, rather than exporting secret files.
Encrypted records are included under existing key custody; ordinary application
exports and diagnostic bundles omit ciphertext and values. A backup remains
sensitive even though values are sealed.

Restore atomically rotates the platform controller epoch and pauses existing
execution as today. In the same authority-invalidating transition, mark restored
credentials and nonrevoked bindings review_required; increment revisions with
overflow refusal and invalidate old value/binding authorization. Require current
admin review/key availability and current release/grant/principal checks before
resumption. Never auto-probe, auto-deliver or replay requests to prove a restored
credential works. A copied DB is not evidence that an old process stopped.

Apply the existing restore revocation-floor principle: a backup cannot resurrect
a locally known revoked credential/binding under the same ID. Preserve current
revoked/tombstoned identities when reconciling an older backup; conflicts remain
review-required and values are never copied back over a revoked tombstone.
Cross-host restore has no local floor proof and stays paused for explicit review.

Missing/wrong DEK or AAD authentication failure refuses use with a fixed safe
error. No plaintext fallback or resetting a credential to the ciphertext string.
Old backups from before these products restore through the existing compatibility
path with empty new tables; backups claiming the new schema must contain both
products and pass shape/FK/reference validation. Newer incompatible backups refuse.
Implement dry-run/full restore on both drivers, rollback on any failure, and
verify failed restore never lowers existing authority/revocation floors.

## 8. Migration and implementation order after approval

1. Inspect current migration tail; append one migration with both tables/checks/
   indexes and portable verification. Add explicit deletion order/FK coverage to
   full/dry backup/restore tests. Do not modify the released baseline.
2. Write failing repository tests for capacity, enums, shape mismatch, foreign
   keys, revision overflow, missing keys, AAD substitution and rollback first.
3. Implement bounded credential create/list/replace/revoke under existing current
   administrator authority and audit. Metadata responses contain no value facts.
4. Implement binding review/consume/current-use resolution, strict operation
   policies, shared rotation/revoke transactions and uninstall invalidation.
5. Implement restore pause/revocation floors/key-loss handling before any callable
   operation. Add SQLite/PostgreSQL concurrent use-vs-revoke/rotation tests.
6. Prove the private use exchange, fixed header validation and response projection
   in isolated tests with disposable credential values, including intentional
   upstream echo/leak attempts and uncertain-no-retry behavior.
7. Add generic administration/permission UX using shared React/Ant/locale/OU
   conventions, three languages and keyboard/accessibility tests. Never add an
   app-specific manual runner URL, container, master key or pairing flow.
8. Connect neutral-app acceptance through reviewed installation, rotation,
   revocation, uninstall/reinstall and restored-paused cases. Keep real providers,
   production deployment, Status migration and beta settings untouched.

Required proof is actual transaction/source/runtime behavior, not schema existence
or a mocked callback returning an Authority. This document requests storage and
isolated development approval only; D5/D6 and later jobs/events schema decisions
remain independent.

## 9. Owner decision record

Approved by the owner on 2026-10-29 Pacific in response to PR #339, covering the proposal text present at
head `3627d68b71e4d49b59b2e59ceaabc028d3f59d6c`. The response accepts Option A and
the stated rotation/reapproval, uninstall revocation, restored pause/revocation
floor and existing-key-custody rules. It authorizes the two named schema products
and their development/isolated implementation/tests. It does not authorize real
credentials, production rollout, raw-read enablement, Status adoption, SDK freeze
or later jobs/events/business-storage products. Do not request this same storage
decision again; separately scoped release and later storage decisions remain.
This approval is not evidence that tables, migration, binding operations or
recovery behavior have already been implemented.
