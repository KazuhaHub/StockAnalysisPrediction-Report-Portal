# Initial control-storage implementation evidence

Status: P03 in progress. The owner approved the initial Option A physical scope
on 2026-10-24 Pacific. This record covers the first Portal persistence slice;
it does not mark P03, the runner or the application platform complete.

## Implemented surface

Migration 0015 appends the approved 14 control tables and 24 explicit indexes.
Every column is a declared migration product. The accepted v0.4.72 baseline and
all earlier migration definitions remain unchanged. Older backups without these
products are allowed by the migration restore rule; a backup claiming the step
must actually contain its products.

`internal/app/app_platform_schema.go` owns the closed declarations. Application
manifests cannot supply SQL. Column types, nullable fields, integer/boolean bounds,
byte limits, principal alternatives, terminal-operation/lease coherence, foreign
keys and partial uniqueness rules are enforced in the database. Cryptographic and
authority admission remain additional checks, not consequences of a row existing.

The Portal uses its own SQLite driver registration with an initialization hook.
It enables and verifies foreign-key enforcement for every new connection, including
replacement connections, without modifying the globally registered default SQLite
driver used by unrelated tools. PostgreSQL uses its existing driver and pool.

`app_platform_verify.go` verifies current catalog definitions read-only. SQLite
checks full table/index declarations while preserving literal bytes. PostgreSQL
checks column types/nullability/defaults, primary/foreign/check constraints,
deferrability/validation, index fields/uniqueness/readiness and partial predicates.
Boolean normalization preserves grouping and literal contents; it does not remove
parentheses indiscriminately. Same-named but weakened products are refused.

## Restore behavior

A forced restore captures locally known disabled-publisher and key-revocation
facts within its transaction, before clearing rows. PostgreSQL locks the control
tables; SQLite acquires write intent. Restore remains an offline operator procedure.
It is not an online replacement endpoint.

The restore preserves stronger known revocations and refuses different key bytes
under the same revoked publisher/key identity. Restored publisher trust is disabled.
Original artifact bytes are rehashed; release declarations, stored metadata and
frontend archives are revalidated before commit using the pure package inspector.
No application code is extracted or executed during this validation.

Restored authority is fenced:

- New controller epoch and incremented installation generations/revisions.
- Runtime bindings disabled; restored private control credentials/fingerprints cleared.
- Active/candidate routing retired; installations marked review-required unless removed.
- Unfinished operations marked recovery-required, with leases cleared.
- Prepared external effects marked unknown rather than replayable.
- Data namespaces and recovery sets require review; writer fences advance.
- Old review plans expire; a protected restore-pause marker is recorded.
- Existing notification restore pausing remains independent and enabled.

No container or filesystem operation occurs in this database hook. Actual old
writers must be stopped and reconciled by the later runner recovery procedure
before any new writer is admitted. A database update cannot prove process shutdown.

The read-only dry run continues to check backup format, declared products and
readability without writing data. It is not a substitute for the required isolated
full restore: relational constraints, artifact inspection, current revocation
reconciliation and managed checkpoint/key compatibility require stronger evidence.

## Account and OU lifecycle

Existing account/OU deletion transactions collect affected app installations before
foreign-key CASCADE/SET NULL actions remove the source references. They preserve
the existing principal lock and user-first deletion rule. Affected installations
are locked in sorted order on PostgreSQL.

The same transaction removes audience grants, clears optional deleted-actor labels,
advances audience policy revisions, refreshes active audience-policy metadata and
expires affected plans. Actor plans without an installation are also invalidated.
A recreated username does not regain the previous app-use rows. OU grants are not
transferred to a replacement OU merely because report attribution moves there.

These hooks cover the initial app-use/control tables. P08/P09 must extend deletion
handling for delegated business/secret/job/event records when those products exist.
They do not inherit authorization automatically from this implementation.

## Verification and environment

Tests were added and observed failing before migration/restore/deletion changes.
Additional tests exposed literal-erasure in catalog normalization; that behavior
was corrected and covered before the schema checkpoint.

A fresh, isolated Homebrew PostgreSQL 18.6 cluster was started on loopback with
synthetic data. It is not the Portal production database or the Status database.
The same applicable fixtures run on SQLite and PostgreSQL, including:

- All 14 tables, column products, 24 indexes and the migration ledger.
- SQLite enforcement after connection replacement.
- Duplicate live app identity and retained removed tombstones.
- Deferred insertion order and rejected orphan commit, followed by a usable connection.
- Invalid counter/phase/null state; catalog drift under unchanged index/check names.
- Read-only dry restore and fenced full restore of pending control state.
- Stronger live key revocation and refused key-identity replacement, with rollback.
- Corrupt artifact restore refusal, with the previous target left intact.
- Intact release restore and rejected descriptor/metadata/ZIP-entry mismatch,
  with rollback preserving the target on both databases. Test signature bytes
  are synthetic: restored trust stays disabled, and signature admission remains
  separate from these integrity checks.
- Account deletion, recreated username, OU deletion and policy invalidation,
  including actor plans created before an installation exists.
- Existing backup, principal cleanup and migration-inspection integration fixtures.

Targeted integration command:

```sh
TEST_POSTGRES_DSN='<disposable-loopback-database>' \
  GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test ./internal/app -run '^(TestAppPlatform|TestPostgresAppPlatform|TestBackup|TestRestore|TestPostgres.*Backup|TestPostgres.*Restore|TestEveryPrincipal|TestDeletingAn|TestDeletingA|TestStep0006|TestSchemaUpgrade|TestExistingDatabaseAtStep0006)' -count=1
```

The targeted integration set passes. The full Go suite with the disposable
PostgreSQL target also passes, as do full vet/build and targeted race checks.
These checks were repeated after the final catalog-normalization hardening;
the additional actor-without-installation regression passed in the race run.
A raw interrupted-initialization fixture now explicitly mirrors connection setup;
a separate negative test confirms that disabled enforcement is refused. Exact-source
CI remains a separate required review gate. Later source changes must rerun their
affected checks; a prior green commit does not certify an updated candidate.

## Still required

- Durable operation admission, idempotent request lookup, claims/heartbeat/CAS and
  effect reconciliation repositories. Constraints alone do not execute those protocols.
- The approved four-table runner journal and real process/data ownership recovery.
- Actual combined app-data backup, key-recovery rehearsal and newer-runner restore drills.
- Publisher signature/trust admission and immutable release publication.
- Platform business storage, secret bindings, schedules/events/outboxes and their
  later physical proposals, plus source-owned notification integration.
- Production host shell/SDK integration and static/managed reference app lifecycle.

There are no new v2 HTTP admission routes or executable runtime integrations in
this slice. Legacy app parsing/bridge behavior and server beta configuration stay
outside it. Do not expose the prototype as a production managed installer to
claim these remaining work packages are complete.
