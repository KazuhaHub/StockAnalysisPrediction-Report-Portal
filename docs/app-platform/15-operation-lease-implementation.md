# Durable operation lease implementation

Status: P03 in progress. This extends the approved initial control storage;
it introduces no additional tables, columns or indexes. The storage foundation
is in [PR #193](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/193),
whose source `d34bca2844f2b8537e79e4018eee1a7d4e7378f6` passed its CI checks.

## Scope and entry points

`internal/app/app_platform_lease.go` supplies two internal repository methods:

- `claimPlatformOperation(operationID, workerID)` acquires queued or expired work.
- `heartbeatPlatformOperation(previousLease)` renews an unexpired matching lease.

Both return a complete internal lease token, including operation/installation IDs,
controller epoch, worker ID, installation fence, row revision, expiry and pure
operation snapshot. Worker IDs must be lowercase UUIDs. A future worker process
must allocate a fresh ID for each process lifetime; it must not reuse a global
hardcoded worker ID after restart.

There is no HTTP endpoint, scheduler, runner dispatch or public SDK method here.
The rows being leased must eventually come from the separately validated admission
transaction. A lease is permission to inspect/reconcile work, not evidence that an
external effect committed and not permission to activate application authority.

## Claim transaction

1. Reject malformed operation/worker identities before opening the transaction.
2. SQLite obtains write intent before reads. PostgreSQL resolves the installation
   and locks its row before locking the operation row. No runtime binding is
   modified or locked, and no network/runtime call occurs inside the transaction.
3. Hydrate and validate the pure operation state. Refuse completed operations,
   mismatched installation/operation epochs, impossible source/target generations,
   removed installations and paused/recovery/quarantine phases.
4. Require an explicitly initialized controller epoch and restore-pause value `0`.
   Missing markers fail closed. This method does not generate an epoch or enroll
   a runtime to make an unknown state appear usable.
5. Sample UTC milliseconds from the database after acquiring locks. Host process
   clocks are not lease inputs. SQLite uses its database date function;
   PostgreSQL uses `clock_timestamp()`, not transaction-start time.
6. Refuse a currently unexpired lease. An expired claim increments the installation
   control fence, rather than resetting it for a new operation. Advance installation
   revision and operation row revision; copy the new fence into the lease.
7. Conditionally update exactly one eligible operation and commit. Zero affected
   rows are a conflict. Exhausted signed 64-bit counters refuse without wrapping.

Fresh installation generation is source `0`, target `1`. Existing operations
reserve source + 1. The installation may currently be at the reserved source or
target generation; unrelated generations refuse. Stage-specific generation changes
and activation checks still belong to the future transition repository.

Lease duration is 30 seconds, matching the accepted specification. The future
worker loop must schedule heartbeats at 10-second intervals. A lost claim response
does not justify dispatching an effect blindly; inspect durable state or wait for
expiry and reclaim. Claims do not automatically rerun any recorded effects.

## Heartbeat transaction

Heartbeat follows the same lock, state and controller checks. It requires the
entire previous token to match current durable state, including state/row revisions,
worker, epoch, generation and fence. The current installation fence must agree.
An expired owner must reclaim with a new fence; it cannot renew its expired token.

A successful heartbeat extends expiry from database time and increments only the
operation row revision. It does not change the operation's stage, state revision,
target generation or fence. Callers must retain the newly returned token. Reusing
the token from before that heartbeat is a stale-revision conflict.

All errors roll back the transaction. Storage errors remain storage errors; they
are not represented as a successful claim. No in-memory mutex is relied upon to
serialize separate PostgreSQL connections or preserve ownership across restart.

## Hydration of the pure model

`appplatform.OperationFromSnapshot` accepts only states reachable in the existing
ordered model. Normal stages require their exact state revision for that action
and static/managed variant. Failed/recovery-required stages require a possible
post-failure revision. Unknown actions/stages, zero generation/revision and impossible
stage/revision pairs refuse.

Hydration validates structural reachability. It cannot prove historical effects,
publisher trust, user authority, database lease ownership or runner observations.
SQL lease revisions and heartbeats never become pure model state revisions.

## Verification

Tests were written and observed failing before these entry points were implemented.
The targeted race suite covers both SQLite and disposable loopback PostgreSQL:

- Eight concurrent claims admit exactly one owner and advance the fence once.
- A live lease cannot be stolen, and a stale revision cannot renew it.
- Expiry requires reclaim; the new owner has a higher installation fence.
- Old/displaced workers cannot renew after another claim.
- Missing controller markers, changed epochs and restore pause refuse.
- Recovery/removed state, unrelated generation and impossible state revision refuse.
- Terminal operations and exhausted counters refuse without mutating the database.
- A file-backed SQLite store closes and reopens without losing the live lease,
  counter or fence; the pre-restart token cannot renew after displacement.
- Every reachable pure state for all eight actions and both static/managed modes
  round-trips through hydration; impossible snapshots refuse.

Expiry tests deliberately modify synthetic persisted expiry values. They do not
change the system clock or wait 30 seconds. Fixtures restore controller markers
after each PostgreSQL test so other integration fixtures do not inherit them.

Run the repository's declared toolchain with bounded local workers:

```sh
TEST_POSTGRES_DSN='<disposable-loopback-database>' \
  GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -race ./internal/appplatform ./internal/app \
  -run 'TestOperationSnapshot|TestAppPlatformLease|TestPostgresAppPlatformLease' -count=1
```

Full Go tests with the disposable PostgreSQL target, full vet/build and the
targeted race checks passed locally. The added file-backed restart regression
also passed with the race detector. Exact-source CI remains the separate review
gate; these results do not certify subsequent source changes.

## Remaining P03 work

- Typed review-plan construction/consumption and idempotent operation admission.
- Conditional stage transitions with current authority, epoch/generation/lease/
  revision checks and independently validated effect evidence.
- Persisted effect intent, idempotent dispatch and uncertain-result reconciliation.
- Atomic activation, denial before stop/remove, and separately reviewed recovery
  and retained-data deletion. These are not bypassed by leasing a row.
- The approved runner journal, process/resource ownership and coordinated recovery.

The complete application platform, application installer and production runner
remain unfinished. No production setting or existing beta deployment changes here.
