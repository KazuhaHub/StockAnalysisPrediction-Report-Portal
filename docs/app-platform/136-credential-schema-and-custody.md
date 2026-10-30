# Credential storage, custody primitives and restore floors

Status: implementation slice under the owner's Option A decision in document
131. Development and isolated tests only. This does not complete P07 or the
application platform. No real credential, production service, Status adoption,
public SDK, installation permission or server beta setting is changed.

## Products and upgrade

The inspected migration tail before this candidate was 0014. New migration 0016 declares exactly two
Portal products, `app_platform_credentials` and `app_platform_secret_bindings`,
with four indexes. The existing baseline and migration 0015's 14 tables/24 indexes
remain unchanged. The original catalog verifier now accepts explicit product
lists; both migrations still independently verify their own exact declarations.

Both drivers enforce bounded column types, positive revisions, known state and
operation enums, custody ciphertext/revocation consistency and timestamp order.
Binding installation deletion cascades, credential and consumed-review references
use deferred NO ACTION, and installation/name uniqueness includes revoked rows.
This structural migration does not validate a binding's grant or policy JSON.
There is no binding creation/activation/use API in this slice.

The existing migration-driven backup enumeration includes both products and all
columns. Pre-migration backups can load empty new products through the existing
compatibility mechanism. A backup claiming the new migration cannot omit either
product. The offline upgrade tests now rewind and expect this new tail, retaining
their read-only inspection and actual offline application assertions.

## Custody boundary

Private administration repositories support create, metadata pagination and
replacement/revocation of an unbound active credential. Every call uses the
existing current administrator/session/account-generation checks, principal
mutation guard, platform identity/restore guard and authority refresh. Creation
capacity counts every state, including revoked identities, up to 256 records.
List pages contain at most 100 metadata records, ordered by immutable ID with an
exclusive after-ID cursor; values and ciphertext are not selected.

Input values are 1..16384 bytes, bounded before copying. The owned mutable copy
is cleared after the transaction. Encryption uses the existing DEK/AES helpers,
with AAD `app-platform` and `credential:<id>:<value_revision>`. Opening validates
UUID, positive revision, bounded known envelope, authenticated identity and
plaintext length; key loss, unknown/plaintext envelopes and authentication
failures return the same fixed error with no partial value. This is a private
custody primitive, not an authorization proof or callable app read operation.

Create seals revision 1. Replacement increments both metadata and value revisions
and reseals. Revocation increments both, erases ciphertext and permanently
revokes that identity. Stale, exhausted or already revoked identities refuse.
All changes and non-secret audit details commit together. Fresh authority is
checked after resource locking and again after audit. A failure returns no result
metadata and rolls back the transaction.

There is deliberately no HTTP, bridge, SDK or automatic startup caller. Mutation
objects refuse generic JSON serialization/deserialization. Metadata excludes value
length, digest, preview, ciphertext and secret-dependent fields. Labels are
administrator descriptions, not a destination for credentials, and are not copied
into audit details. Immutable input strings/copies cannot be promised erased.

**Bound replacements/revocations currently refuse without writes.** The affected-
installation preview, current review/consume and atomic dependent-binding
transition must be implemented before permitting them. This temporary restriction
prevents an incomplete repository from bypassing the approved cross-installation
review and lock order. It is not the final rotation UX or final feature behavior.
Raw read and credential header injection remain unavailable.

## Restore and retained revocations

Restore's existing offline transaction locks the two new products together with
the original control tables. Before clearing rows it captures locally revoked
credential and binding identities. Reconciliation happens before authority is
resumed or a new epoch can commit:

1. Restore known credential tombstones, taking the higher observed value and
   metadata revision and preserving the earlier known revocation instant. Never
   restore ciphertext over a revoked identity.
2. Keep known binding IDs revoked. If the backup omitted that binding, reinsert
   its tombstone only with its original references; do not fabricate a review.
3. Refuse the complete restore if missing parents, a conflicting installation/name
   or capacity would prevent preserving a floor. Deferred FK failure at commit
   also rolls back everything. An older backup is not permission to forget a
   known revocation or revoke some other installation's shared credential.
4. Check capacity including retained tombstones. Refuse authority-revision
   exhaustion before arithmetic. Mark every nonrevoked credential/binding
   review_required and increment its metadata revision.
5. Preserve a nonrevoked credential's value revision and ciphertext together:
   value revision is authenticated AAD. The metadata/binding revision changes and
   new global controller epoch invalidate old authorization; changing value
   revision without resealing would destroy decryptability.
6. Include standalone credentials when determining whether restored platform
   state needs the existing controller-epoch rotation and restore pause.

A restore does no decrypt/probe/network I/O or request replay. Existing disabled
installations, revoked values and binding floors stay denied. A missing key cannot
turn ciphertext into a usable plaintext credential. An orphaned new custody
record also counts as control history and prevents fresh-control initialization.

## Verification and remaining implementation

Behavioral tests were first run without the implementation: missing schema and
custody APIs failed to compile; restore cases demonstrated active records,
resurrected revocations and accepted overflow before the restore integration.

Driver-paired tests cover migration products/ledger, forbidden state/null/counter/
operation/reference shapes, index weakening, deferred credential protection,
installation CASCADE retaining custody, dry-run nonmutation, restored pause,
revocation floors, absent binding reinsertion, missing-parent rollback, exhaustion,
older backups and false schema claims. Custody tests cover identity/revision AAD,
wrong/missing keys, unknown/truncated envelopes, list non-disclosure, permanent
revocation, stale edits, input and retained-capacity limits, pagination, current
admin demotion, audit rollback and refusal of unreviewed bound mutations.

The complete local `internal/app` suite passes (157.205s), as do focused
SQLite race tests, repeated concurrent mutation tests and vet; PostgreSQL tests require the
isolated CI database. CI runs the entire driver-paired credential group with
race detection before staticcheck/vulnerability/full-suite gates. Exact revision
CI evidence is still required before counting PostgreSQL verification as passed.
The original full local suite exposed two migration-tail expectations; these were
updated to explicitly include 0016, with an unchanged read-only/offline contract;
the full local suite then passed.

Next work remains: strict canonical binding policies; consumed exact grant review;
affected-installation rotation/revoke preview and transaction; binding-only and
uninstall invalidation; live current-user use checks; private credential-use
exchange; bounded fixed-header validation and response projection/echo prevention;
concurrency on both drivers; generic React/Ant administration and neutral-app
acceptance. None of these is enabled by the existence of ciphertext or an active
row alone. Later business/job/event schemas and production acceptance retain
their independent decisions.
