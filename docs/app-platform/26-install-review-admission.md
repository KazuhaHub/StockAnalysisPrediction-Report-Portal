# Initial installation review and durable intent

Status: P03/P05 development integration. This implements the first install review
and confirmation repository, not a complete installer, public endpoint or running
application. It uses only the approved control tables and existing purpose-sealed
keyring. No new physical storage, production installation, SDK freeze or server
channel/configuration change is included.

## End-to-end boundary

The host can now perform these internal steps:

1. Explicitly enroll development publisher trust through the private trust repository.
2. Stage a signed descriptor and its verified frontend ZIP using
   `stagePlatformRelease` from the previous storage integration.
3. Call `createPlatformInstallReview` with the staged descriptor digest and exact
   current publisher revision. Receive an immutable review ID, canonical JSON,
   SHA-256 and a 15-minute database-clock expiry.
4. After the administrator confirms that exact review, call
   `consumePlatformInstallReview` with the review ID/digest and a fresh canonical
   UUID idempotency key.
5. Commit a new installation identity and one `queued` install operation. A worker
   can claim its existing operation lease. No stage is completed by this commit.

There is no download-from-market endpoint, management route, progress UI,
frontend activation, data preparation executor or backend launch in this slice.
Those remain required work, including the full managed application path.

## Actual availability policy

`platformInstallRequirements` evaluates protocol compatibility before admitting
review or confirmation. The existing bounded evaluator from PR #190 is integrated
with its pinned `Masterminds/semver/v3` dependency and tests. It is not replaced
with lexical string comparison or Portal's product version.

The internal candidate API version is `2.0.0-dev.1`. This identifies development
semantics only; it does not advertise a stable platform release or change Portal's
CalVer/`beta` channel. Semver prerelease matching is explicit: test descriptors use
`>=2.0.0-0 <3.0.0`. A normal stable range that excludes this prerelease refuses.

No enforced runtime or capability broker is delivered yet. Therefore:

- The host supplies no runner compatibility observation. Managed descriptors
  refuse with unavailable requirements; a caller cannot pass `ready=true` or a
  fabricated runtime version to bypass this repository.
- Every required capability currently refuses review/admission. Static execution
  alone does not prove that storage, reports, networking, notifications or another
  provider works. A later provider registry must add actual enforced support.
- All optional capabilities are explicitly declined and stored with `granted=0`.
  The review lists those declined capabilities in sorted order. It never silently
  grants optional features because the descriptor asks for them.
- Static descriptors without required capabilities can record installation intent.
  This is an incremental path toward the complete platform, not a replacement for
  required capability/managed support or permission to publish incomplete apps.

The requirement error is fixed safe text. Localized management reasons and the
complete capability review UI remain host presentation work.

## Review binding

The canonical `portal.install-review/1-dev.1` projection binds:

- Exact app/publisher identity and staged descriptor SHA-256.
- Current publisher revision, key ID and key revision.
- Controller epoch and the current session/account/OU authority fingerprint.
- The internal platform API candidate and explicit declined capabilities.
- Initial data quota of 256 MiB for the future owned namespace preparation stage.

The review has no existing installation or source release. Its initial
installation/policy revisions are zero. The stored target points to verified
immutable release bytes. A future retained-data adoption review is a different
explicit choice; this path does not inherit an older installation's grants or data.

The review callback uses the same locked trust/release transaction as staging and
rechecks current signature validity before committing the review. Review creation
creates no installation, data namespace, permission grant or operation.

Confirmation locks and reloads the review, then reconstructs the full projection
from current authority, current trust, exact stored release and actual available
providers. Reject changed digests/JSON/authority/epoch/revisions/requirements,
expired or consumed reviews, changed release metadata and another actor's proof.
Do not deserialize the stored plan into an execution request and trust its fields.

## Old installation exclusion

Check every historical installation for the app ID, not only the partial index
that excludes `desired_state='removed'`. Refuse while any predecessor has:

- A desired state other than removed, or a phase other than removed.
- An unfinished operation or an operation in `recovery_required`.
- A prepared/unknown external effect associated with any operation.

This prevents an uninstall intent/tombstone from admitting a new writer while old
cleanup or uncertain work is unresolved. The check runs again under database
serialization at confirmation. A later fully resolved tombstone permits a new
installation identity; no former identity seed or permission is revived.

`phase='removed'` is only valid after the future removal executor proves physical
cleanup. This admission repository does not manufacture that proof. Its tests
seed lifecycle records to verify exclusion; they do not simulate a real stop.

## Confirmation writes and rollback

One database transaction:

1. Rechecks current administrator/session/control state and locks trust/review.
2. Resolves the global actor-lifecycle/idempotency namespace. An identical retry
   returns the already committed operation; changed review input conflicts.
3. Verifies the signed release and reconstructs the exact review projection.
4. Allocates fresh random installation/operation UUIDs and a random 32-byte
   installation identity seed. Seal that seed using the existing DEK with the
   `app-platform` purpose and installation ID as associated identity.
5. Inserts the installation as `enabled/pending`, generation/revision/policy 1,
   control fence 0, and no runtime binding. Desired enabled is an intent, not a
   serving/ready state.
6. Consumes the exact review, inserts one generation-1 static install operation
   at `queued`, and records every optional capability as declined.
7. Inserts `app_platform.operation_admitted`, then checks current actor/control,
   key validity and review expiry again after all database lock waits.
8. Commits only if every check succeeds. Otherwise rollback removes installation,
   sealed identity, consumed marker, grants, operation and audit together.

The DEK is loaded after an initial current administrator check and before opening
the transaction, avoiding nested keyring queries on SQLite's single connection.
The authoritative actor/session check still occurs again inside the transaction.

No activation, access grant or data namespace is inserted. Audience/OU review,
namespace preparation, authenticated worker execution and activation policy remain
separate required lifecycle work. A backend worker must never treat a queued
installation record as permission to expose routes or a raw credential.

Existing bounded transaction retry applies only after rollback of database
contention. The callbacks contain no external download, container or notification
effect. Idempotent retry after successful admission reports the historical committed
operation even when its review has since expired; it does not restart execution.

## Verification

Meaningful success-path/availability tests were observed failing before this
implementation. SQLite and disposable PostgreSQL tests cover:

- Review creates no installation; confirmation creates exactly one pending
  installation and queued operation, with a recoverable purpose-sealed identity.
- No activation, access authority, prepared data or granted capability is fabricated.
- Idempotent retry, consumed review conflicts and six simultaneous confirmations.
- Expired reviews, rotated trust, administrator demotion, changed JSON/digest and
  unfinished removed predecessors refuse without database writes.
- Removed predecessors with unfinished/recovery-required operations remain blocked;
  a resolved tombstone permits a fresh identity with a new idempotency key.
- Injected audit failure rolls back the complete install intent on both drivers.
- A PostgreSQL audit table lock actually blocks confirmation, as observed through
  `pg_stat_activity`. Release the lock after review expiry and require full rollback,
  including the consumed marker and declined capability rows.
- Required unavailable capabilities, incompatible protocol ranges and managed
  descriptors without a real runtime refuse installation requirements.

From the Portal root with a dedicated disposable PostgreSQL database:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
TEST_POSTGRES_DSN='postgres://portal@127.0.0.1:59409/reports_test?sslmode=disable' \
go test -p 2 -race ./internal/app ./internal/appplatform \
  -run '^(TestPlatformInstall.*|TestPostgresPlatformInstallReviewExpiryAfterAuditWait|Test.*Compatibility.*)$'
```

A separate Go build overlay that bypassed only the final review-expiry branch
made the real PostgreSQL audit-wait test fail by accepting the expired review;
the working-tree implementation retains that check. The preceding release-expiry
test now compares audit counts before/after its own operation so historical fixture
audits in a reusable integration database cannot produce a false rollback failure.

Full source CI, runtime enforcement, capability providers, worker stage commits,
installer UX and real market installation remain separate acceptance requirements.
