# Publisher trust and staged release storage

Status: internal P02/P03 integration under the approved initial D2 tables and
D1/D3/D4 development candidate. No new migration, table, column or side-store is
introduced. There is no HTTP endpoint, automatic publisher enrollment, production
trust change, installed-app migration or server channel/configuration change.

The pure [publisher verifier](24-publisher-signature-verification.md) now has a
transactional storage consumer. It is still not the complete installer/runtime.

## Source and entry points

| Source | Responsibility |
| --- | --- |
| `internal/app/app_platform_trust.go` | Current trusted publisher/key snapshot, serialized administrative trust changes, revision/audit/invalidation commits |
| `internal/app/app_platform_release.go` | Immutable descriptor/frontend staging and same-transaction current-trust rechecks |
| `internal/appplatform/publisher.go` | Bounded typed trust validation and strict stored namespace JSON parsing |
| `internal/app/app_platform_trust_test.go` | Both-driver trust, concurrency, failure, audit, independent SQLite store and reopen checks |
| `internal/app/app_platform_release_test.go` | Signature/archive/metadata/revision refusal and actual PostgreSQL lock-wait expiry rollback |

All entry points are private to the host package. A future management handler
must construct `platformActorSession` from its authenticated session, enforce
its HTTP/CSRF/request-size gates and map internal errors to safe responses.
Never deserialize that session proof from an app or request body.

## Trust changes

`changePlatformPublisherTrust` accepts one closed internal change kind:

| Kind | Input | Result |
| --- | --- | --- |
| `register` | New publisher ID, explicit enabled state, namespace list and first key; expected publisher revision zero | Publisher/key revision 1, one transaction |
| `policy` | Existing publisher ID, exact current revision, enabled state and replacement namespace list | Publisher revision advances; key bytes and revocation remain untouched |
| `add_key` | Existing publisher ID/current revision, a previously unused key ID | New immutable key at revision 1; publisher revision advances |
| `revoke_key` | Publisher ID/current revision, key ID/current key revision | Record database-clock revocation, advance both revisions; cannot clear it |

New keys must specify `Ed25519`, canonical 32-byte public-key encoding, revision
`"1"`, no revocation and valid explicit time bounds. Rotation adds a key; it does
not overwrite the bytes or time bounds of an existing ID. There is no delete,
replace-key, edit-validity or unrevoke action. Extraneous action fields, stale
revisions, overflow, invalid/overlapping namespaces and unsupported algorithms
refuse the whole transaction.

An already revoked key with the exact current publisher/key revisions returns its
existing revision without adding another audit event or changing the timestamp.
An old retry carrying stale revisions conflicts and must reload. The repository
never lowers a revocation floor. Disabling a publisher retains its namespace
ownership; another publisher cannot claim it.

Every effective change expires that publisher's unconsumed review plans and
inserts `app_platform.publisher_trust_changed` in the same transaction. Already
consumed reviews remain historical records. Audit failure rolls back the key,
publisher revision and review invalidation together. Logs contain bounded action
and identity/revision metadata, not descriptor content or credential material.

## Snapshot and lock boundary

The repository reuses current session/account/OU lifecycle authorization and
control-epoch/recovery-pause checks. Acquire locks in this order:

1. Existing principal/OU mutation guard and current administrator/session checks.
2. Publisher/key trust transaction guard.
3. Release/frontend evidence rows when validating a staged release.
4. Future reviewed installation/resource locks in an explicitly consistent order.
5. Final current authority/time verification before returning for commit.

PostgreSQL takes `SHARE ROW EXCLUSIVE` table locks on publishers and publisher
keys. This serializes cooperating trust/release transactions and excludes DML
phantoms or key changes while a snapshot is in use. Selected release/frontend
rows are held `FOR SHARE`. See the PostgreSQL documentation on
[table and row lock conflicts](https://www.postgresql.org/docs/current/explicit-locking.html).
SQLite obtains write intent before reading, including across independent Store
instances. No process-local mutex is used as a substitute for the database guard.

Publisher rows are read in ID order with a 129-row refusal sentinel; keys use a
257-row sentinel. Apply the verifier's 128-publisher/256-key and namespace limits
before accepting the snapshot. Stored namespace JSON is limited to 8 KiB and
parsed strictly: reject duplicate/escaped duplicate or unknown fields, aliases,
invalid kinds, loose prefixes and trailing JSON. An invalid unrelated trust row
also refuses the snapshot; do not quietly skip it and broaden another grant.

Database-only transaction callbacks retry recognized contention up to five times
with bounded 10/20/40/80/160 ms backoff. Each retry reloads current authority/trust
and starts a new transaction after rollback. Never put downloads, physical
container effects, notifications or other external work in these callbacks.

## Staging exact bytes

`stagePlatformRelease` takes exact descriptor bytes, detached signature bytes and
frontend ZIP bytes. It copies/bounds the inputs, strictly parses the descriptor
and fully inspects the declared frontend digest/entry/archive limits. It then:

1. Enters the authenticated trust transaction and loads the current snapshot.
2. Verifies the exact signature, publisher namespace, key validity/revocation and
   current publisher/key revision using database time.
3. Refuses a different descriptor digest for an existing publisher/app/version.
   A published version cannot change even by adding descriptor whitespace.
4. Stores the frontend bytes and release row atomically with
   `app_platform.release_staged`. A reused frontend digest must identify equal
   stored bytes/length. No partially staged archive remains after failure.
5. On replay, revalidates existing descriptor/signature/frontend/metadata and
   current trust, rather than treating a cached release row as authorization.
6. Rechecks current administrator/control authority and signature validity after
   all bounded archive work and lock waits, before allowing the transaction to
   return for commit. A key that expired during an audit-lock wait rolls back
   the complete staging write.

Exact successful replay reports `reused=true` and adds no duplicate release or
staging audit. The original release's `trust_revision`/verification time remain
historical immutable metadata. Current verification returns the current revision;
it never treats the historical stored revision as present authority.

Staging a managed descriptor checks its declared immutable image reference only.
It does not fetch or verify the OCI image or runtime architecture. Neither static
nor managed staging creates an installation, generation, capability grant,
operation, process or serving route.

## Integrating an install review or commit

Use `withPlatformStagedRelease(session, digest, expectedTrustRevision, callback)`
for host admission work that must share the validation transaction. Do not call
`recheckPlatformStagedRelease`, retain its returned value, then write intent in a
later transaction. That is a time-of-check/time-of-use bug.

The callback receives the same transaction, authenticated actor/control facts
and verified release identity. It may perform database-only reviewed-intent work.
Any callback error or failed final authority/expiry check rolls back everything.
The final check runs after callback lock waits and writes; an expired key cannot
leave a committed admission intent. Physical execution must be dispatched later
through the durable operation/effect pipeline.

The wrapper refuses a changed expected publisher revision, disabled publisher,
lost namespace, expired/revoked key, changed stored descriptor/signature/version,
frontend digest/bytes mismatch or changed managed/static metadata. PostgreSQL row
locks hold inspected evidence until the transaction ends. A callback must not
mutate trust, release or frontend evidence directly; those writes belong only in
their named repositories. It must still validate its own operation-specific
review, idempotency, runtime, capacity, permission and data requirements.

This wrapper is a storage integration point, not an install endpoint or proof
that all admission requirements pass. Emergency quarantine of running code after revocation and full install review/consume repositories,
compatibility evaluation, artifact/image distribution, grants and physical
lifecycle execution remain unfinished.

## Verification

Both the namespace parser and repository success paths were observed failing
before implementation. SQLite/PostgreSQL tests cover registration, immutable key
rotation, monotonic revocation, current revision conflicts, disabled namespace
ownership, concurrent conflicting registrations, unconsumed review expiry,
malformed persisted policy, administrator demotion/recovery pause and overflow.
Audit failure injection proves atomic rollback for trust changes and archive plus
release staging. Independent file-backed SQLite stores contend on one namespace,
observe each other's revocation, and retain it after close/reopen.

Stored-release tests revalidate signatures, exact versions/frontend bytes,
namespace/disabled/expiry state and metadata before entering admission callbacks.
The PostgreSQL expiry test actually observes a worker waiting on the held audit
table through `pg_stat_activity`, advances past the key's validity end, then releases
the lock. Both staging and a database-only admission callback must roll back.
The real signed-release restore tests load an older backup after revocation,
then verify that the original revocation survives, the publisher remains disabled
and control is paused while the immutable release bytes remain available.
No production service, publisher private key, container or application route is
used by these tests.

Run the focused integration suite against a dedicated disposable PostgreSQL DB:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
TEST_POSTGRES_DSN='postgres://portal@127.0.0.1:59409/reports_test?sslmode=disable' \
go test -p 2 -race ./internal/app ./internal/appplatform \
  -run '^(TestPlatform.*(Trust|Release)|TestPostgresPlatformReleaseExpiryAfterAuditWait|TestStoredPublisherNamespaces|TestSharedPublisherSignatureVectors)'
```

`platformActorTestServer` now delegates its fixture setup to a reusable
Store-specific helper; existing actor tests retain the same setup and cleanup.
The runtime/trust decision and initial schema approval remain the governing scope.

Development evidence: the focused SQLite/PostgreSQL race suite passes on the
current source with disposable PostgreSQL 18.6. The independent file-backed
SQLite contention/reopen test passed six repeated race runs after adding bounded
rollback/retry. A Go build overlay that bypassed only the final release guard
made both real audit-lock expiry cases fail (`stage` and `admission` accepted an
expired key); the unchanged working-tree implementation passes both. The overlay
changes no repository source. Full source CI remains a separate PR requirement.
