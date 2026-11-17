# Background static installation scheduler

This connects durable initial-static-install admission to an explicitly owned
background service. The service discovers work, claims it transactionally, drives
installation and stops uncertain work with retained recovery evidence. It is a
development infrastructure entry point, not a production startup hook, HTTP
installer, recovery approval UI or the full application platform.

The current development installer uses the native-backed data profile. RUN-01
(static installation without a configured runner) remains a required separate
provider path. Do not turn this service's runtime preflight into a permanent
prerequisite for every static application in the public installer.

## Admission and service ownership

The existing review/commit repositories remain the authority boundary. A caller
must stage a verified package, obtain the server-produced confirmation and consume
that review under current administrator/session/CSRF authority. Consumption writes
a queued operation; it never calls the worker from the browser request.

The trusted service owner then:

1. Uses an already provisioned root data endpoint and confirmed shared runtime.
   It does not read an endpoint, UID, worker identity or key from an application.
2. Generates one worker UUID for this service instance. A new instance must not
   reuse an old in-memory lease as authority after restart.
3. Calls `newPlatformInstallScheduler(server, endpoint, worker, interval)`.
   The constructor validates the worker UUID, absolute canonical path shape,
   Unix-socket length bound, non-root UID/GID and interval (50 ms through 30 s).
   Construction starts no goroutine and creates no filesystem or database record.
4. Owns `serve(ctx)` and waits for it to finish before closing the Portal store.
   The Linux process UID must equal the configured non-root controller UID. An
   unsupported host or identity refuses before claiming work. One instance cannot
   run its service loop twice.
5. Cancels the service context on shutdown. Current context-bound SQL and local
   transport attempts finish/cancel before the loop returns. It does not clear a
   live uncertain lease merely to make shutdown look clean.

Existing Portal startup does not invoke this service, and no production config,
OS identity, runtime enrollment or deployment is changed here. The disposable
Linux fixture invokes the real service explicitly. Startup/configuration wiring
remains a later integration step requiring the existing rollout boundaries.

## Select and claim one eligible installation

`claimNextPlatformStaticInstall(ctx, worker)` uses a context-bound transaction.
It locks principal/OU mutation, publisher trust, then the shared runtime before
installation/operation rows. The existing DEK is obtained before the transaction;
any temporary runtime seed copy is cleared immediately after preflight.

The query reads at most 32 candidate IDs ordered by creation time and ID. It
selects only initial unmanaged install operations under the current controller
epoch, enabled pending generation-1 installations with no backend binding, and
supported nonterminal stages. A lease must be absent or expired. Query results
are hints: actual installation/operation rows, database clock and eligibility are
rechecked under locks. A concurrent claimant's live lease is skipped.

For a candidate with no persisted external-work resources, the selector checks
the complete original consumed review, current signed package/artifact, sponsor,
audience identities, publisher/key trust, declined capabilities and control state.
The confirmed shared runtime must currently be available before granting a new
execution lease. It then uses the existing lease CAS to increment the installation
fence/revision and bind the exact worker/operation/state/row revisions. A claim
audit and late authority/lease checks commit together. Audit, cancellation,
expiry or authority failure cannot leave a partial claim.

The selector does not start native work while holding this transaction. Static
installations keep runtime_id NULL. Managed applications, later generations and
other lifecycle actions remain outside this worker's selection contract.

## Expiry and review decisions

The selector checks persisted namespaces, external effects, activations, app-use
grants and granted capabilities for this installation. The result controls what
an expired owner can safely leave to another worker:

| Situation | Decision |
| --- | --- |
| Absent/expired lease; no resources; valid authority | Claim a new fence and resume the existing artifact stage |
| Still-live lease | Skip; never steal it |
| Expired lease with any persisted external-work resource | Stop with recovery_required; retain all evidence and data |
| Expired activate stage even if its records are missing | Treat the missing preparation history as unknown; require recovery |
| Known current authority refusal before any external work | Fail the operation and mark installation review_required |
| SQL, storage or unexpected infrastructure error | Roll back and report worker unavailable; do not infer application failure |
| Global restore pause or uninitialized control | Refuse without changing the operation |

Persisted prepare-data intent means execution may have happened even if the
Portal receipt is still empty. The worker cannot reclaim it under a higher fence
and replay creation. An effect-free artifact operation can resume because the
production data path commits its durable namespace/effect pair before returning
any signed execution command.

A known runtime-disabled state does not prevent denial-only recovery marking for
an already expired uncertain operation. It does prevent fresh execution claims.
Other unexpected runtime/storage failures remain errors, not guessed evidence.

Review transitions use the pure operation model's Fail decision. They atomically
increment the installation control fence/revision, set review_required or
recovery_required, advance operation state/row revisions, persist a fixed error
code, clear the lease, set completion time and write an audit. The old command,
namespace, effect receipt and sealed installation identity are retained. No
native repair, cleanup, acknowledgement synthesis or new access grant occurs.
Late control/clock checks still apply; a live-owner review additionally requires
its exact lease to remain live through the final write.

Fixed internal error codes are `install_lease_recovery_required`,
`install_authority_review_required` and `install_execution_review_required`.
They contain no raw database, endpoint, provider or secret diagnostics. These are
implementation codes, not a frozen public SDK/API contract.

## Serial ownership and bounded attempts

The scheduler owns one active lease at a time. Its gate serializes concurrent
internal ticks and respects cancellation while waiting. A tick has a 20-second
context limit. If it has no active lease, it selects work; otherwise it keeps the
last confirmed lease returned by the bounded installer.

Before an attempt it renews only at this serial boundary. A heartbeat cannot race
the driver's revision-changing stage transactions. The common lease repository
now supports context-bound SQL. It rechecks control and clock after writes and
uses the original deadline when renewing: an expired old lease cannot be revived
by computing a later deadline before a blocked write.

The existing bounded driver then executes the remaining stages. Success clears
the in-memory lease and leaves the durable operation complete with no lease.
Failure retains the exact last confirmed token for another attempt under the
same fence. Uncertain requests continue using the same effect/namespace, allowing
only the existing applied read or fresh-authority read-only reconciliation.

After three failed attempts the service makes the retained review transition;
it does not start a fourth native attempt. A failed review transaction is retried
as review rather than execution. If its token has expired or another owner has
changed it, the service drops the cached token and lets durable selection decide
what is eligible next. It never finalizes using an out-of-date ownership token.
Parent shutdown cancellation stops work without counting it as an application
failure or releasing uncertain ownership early.

Internal status snapshots contain only a fixed code, operation ID, stage and
failed-attempt count. Codes include stopped, idle, running, retry_pending,
worker_unavailable, review_required and complete. Review status does not guess the
terminal stage; the durable operation distinguishes failed from recovery_required.
No signing key, raw error, receipt payload or reusable lease token is published.
These snapshots are not registered HTTP endpoints or an existing management UI.

## Evidence and continuation

SQLite and PostgreSQL tests cover competing claimants, live lease refusal,
effect-free expired resumption, uncertain expiry with retained data, authority
refusal, pause/offline/cancel guards, claim/review audit rollback, bounded endpoint
failure retries, no accidental app access, service guards and late heartbeat
expiry/cancellation during real PostgreSQL writes. These tests reuse the approved
14 control tables and existing shared journal without schema changes.

The disposable Linux fixture's second signed application now submits only its
reviewed queued intent and starts the actual background service. Test code no
longer claims that operation or invokes the driver directly. It waits for durable
completion, cancels and joins the worker, verifies cleared leases, and opens the
protected document. The parent requires the background-service completion marker.
The workflow sets `native_portal_background_install_scheduler` only after this
fixture succeeds. Native success must be recorded from the actual run/report;
local cross-compilation is not execution evidence.

This does not complete fenced data-effect takeover or an approval-based recovery
workflow. It deliberately retains uncertain state for that work. Startup wiring,
HTTP plan/commit/progress surfaces, market UX, managed lifecycle, runtime-free
static data, SDK/shell acceptance and production release gates remain required.
No production credentials, Status migration, public SDK freeze or server beta
configuration changes are part of this implementation.


## Observed source evidence

Source `616003ab97bf77e9e98e2599aa041c12454501c2` passed disposable native
run [40326048071](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40326048071).
Its downloaded report confirms `native_portal_background_install_scheduler`,
queued-to-complete driver execution, signed dispatch, read-only recovery,
activation/launch and owned cleanup. The background application actually reached
protected document access without a manual claim/driver call for its operation.
`execution_ready` remains false because managed runtime/full-platform gates are
still outstanding.

Local full Go tests with disposable PostgreSQL passed (application package
160.415 seconds). Focused SQLite/PostgreSQL queue/scheduler/lease race checks,
including late renewal and rollback/offline cases, passed. Staticcheck, vet,
Linux amd64/arm64 cross-compilation, 12 Python acceptance checks and workflow lint
passed. The same source also passed main CI
[40326048047](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40326048047),
including full Go/PostgreSQL tests, configured race shards, web checks, image
smoke and workflow lint.
