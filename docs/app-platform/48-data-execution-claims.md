# One-shot data execution claims

This adds the durable claim and process-local invocation boundary between data
reservation and a future native executor. It does not wire the XFS provider,
commit a physical receipt, reconcile an old uncertain attempt, grant a writer or
activate an installation. The complete application platform remains in progress.

## State and authority sequence

| Step | Durable command state | Available authority |
| --- | --- | --- |
| Admit signed command | prepared | Intent only |
| Reserve namespace/project | prepared | Same intent plus stable target metadata |
| Claim existing exact reservation | unknown | One opaque live handle in this process |
| Invoke trusted synchronous callback | unknown | Handle is consumed once |
| Callback returns/errors/panics | unknown | No new dispatch; physical settlement still required |
| Process closes/restarts | unknown | Old handle cannot run; reconcile actual resources |

`unknown` is persisted **before** a live handle is returned. A crash between claim
and invocation therefore cannot look like fresh unexecuted work to a new process.
Unknown is conservative: it may mean nothing ran, work partially ran, or work
finished without a verified receipt. Do not guess which outcome occurred.

## ClaimPrepareData

`Store.ClaimPrepareData(ctx, rawCommand, signature)` is internal infrastructure
code. It shares the reservation repository's context-bound transaction and exact
checks for current enrolled controller, signature/timing, admitted logical effect,
original fence/floors, pending work and initial resource identity. An existing
reservation is mandatory; this method never allocates a namespace/project.

It requires the exact command still to be prepared with receipt `{}` and no ack
marker. It validates the unchanged unknown resource row, then atomically changes
only that command to unknown and records a typed intermediate claim marker in
its existing `receipt_json` field. The bounded marker has these fields:

| Field | Meaning |
| --- | --- |
| `format` | `portal.runner.prepare-data-claim/1-dev.1` |
| `claim_id` | Random canonical UUID for this one attempt |
| `effect_id`, `resource_id` | Exact original effect and reserved namespace resource |
| `ownership_sha256` | Existing signed-tuple/native-project ownership binding |
| `admitted_fence` | Immutable original fence, decimal string |
| `claimed_ms`, `expires_ms` | Claim clock and original envelope expiry, decimal strings |

The marker is not a physical receipt. It contains no filesystem observation,
readiness flag, completed stage or secret. It is metadata of the existing command,
not a new independently mutable entity. No schema, table, column, migration,
journal version, side store or runtime signing key is added.

After updating, the method repeats context/clock/signature checks and commits.
Closing state or any failure prevents a handle from being returned. A lost commit
acknowledgement may leave durable unknown state; the caller must reconcile rather
than infer absence. A failed transaction that remains prepared can be claimed
again because no handle/native work was issued by that failed transaction.

## Opaque process-local handle

`DataClaim` holds private shared state owned by this exact Store object. Copying
its public Go value shares one atomic consumption flag. Zero values are invalid.
JSON encoding and decoding explicitly refuse; an unmarshal attempt clears its
receiver rather than minting or retaining authority from serialized input.

The database marker/UUID is not a bearer token and cannot reconstruct the private
handle after restart. A new Store rejects a handle from the old Store, and a
closed Store rejects further invocations. A signed renewal cannot re-claim the
now-unknown command. Native recovery needs a separate typed reconciliation path.

The handle retains the original envelope expiry. Claim only when work is ready
for dispatch; do not pre-claim a large queue. There is no automatic renewal or
unknown-to-prepared reset when a handle times out or is lost.

## RunPrepareDataOnce

`Store.RunPrepareDataOnce(ctx, claim, callback)` accepts only trusted synchronous
infrastructure code. The callback is not loaded from app code/JSON and is not an
app capability API. No production/native callback is wired in this slice.

The initial runner uses one process-local execution gate for all data attempts,
which also serializes each installation. Waiting for this gate is cancelable.
After acquiring it the method refuses closing/consumed handles, re-reads current
controller/command/resource authority under the journal write guard, and compares
the exact persisted claim marker. It repeats signature/time/context checks after
all waits and immediately before consumption/invocation.

The transaction commits before external work; no SQL lock spans the callback.
The shared handle is atomically consumed before invocation, and the execution
gate stays held until that synchronous callback settles. Callback error or panic
does not reset the flag or turn the command back into prepared.

`DataAttempt.Invoked` reports only that the callback was entered. Even a nil
callback error leaves the command unknown and resource observation untouched.
It cannot complete a Portal stage or serve as a native receipt. A nil callback,
invalid handle, expired authority or failed recheck never invokes anything.
Cancellation while queued does not consume an otherwise live handle; retry still
requires its original unexpired authority and current durable checks.

A callback must not close this Store, recursively enter its execution gate, spawn
unsettled native work and return early, or hand its arguments to an untrusted
caller. Its future implementation must bind the configured physical root and
perform synchronous native operations. Receipt settlement may use its own short
SQL transaction after observation; it must not open a second execution attempt.

## Close, crash and recovery

Close marks the Store closing and waits for its active synchronous callback before
closing the database/releasing journal ownership. Queued/new invocation paths
then refuse. This preserves the process ownership boundary while native work is
still in flight. A kernel call that does not settle can delay orderly shutdown;
context checks do not claim to kill synchronous kernel work.

The SQL claim/unknown marker survives close/reopen. No startup hook, socket method,
worker loop or CLI command dispatches it automatically. Recovery must inspect the
original reserved native target and ownership, establish that an old writer cannot
continue, and settle actual evidence under current fences. An old receipt or
`state=applied` row alone will still be historical metadata, not current readiness.

This is the crash **barrier**, not the recovery executor. Complete recovery,
physical filesystem/root binding, typed observation/receipt commits and activation
remain required before shipping the platform.

## Verification

- A positive claim/invocation test first failed against a refusing stub.
- Tests prove unknown is durable before a handle is returned, one success among
  eight concurrent claimers, one invocation among eight copied handles, and no
  reconstruction from JSON, a closed owner or a reopened Store.
- Changed resource/project/fence/receipt/command/controller identity refuses
  before callback entry. Missing reservation and injected SQL update failure
  preserve prepared state so an actual rollback can retry.
- A real journal connection wait expires the signed envelope before invocation;
  callback count remains zero and durable unknown is preserved.
- Cancellation/nil callbacks do not consume unused live handles. Another
  installation can admit/reserve/claim while a callback blocks, proving no SQL
  transaction spans that callback. Its queued invocation can time out independently.
- Close is observed waiting while a second Store cannot open the same
  journal. After the callback settles, close/reopen succeeds with unknown state.
- Callback errors and panics consume the handle and preserve uncertainty.
- A temporary build overlay removed only the shared consume operation. The
  copied-handle test then failed by invoking the callback again. Normal source
  retains the atomic operation; the overlay never edits repository source.

These tests use synthetic synchronous callbacks. They do not create physical
namespaces through the new claim API or prove native effect completion. The XFS
primitive has separate predecessor acceptance evidence. Combining it with the
live claim, physical root pin and receipt settlement remains necessary.

Predecessor reservation source `e4a08844805529a80048acdc99d87a416e990366`
passed main CI 40289609568 and runner probe 40289609569. Local runner race checks passed (journal 69.171 seconds); final claim race checks
passed in 4.274 seconds after explicit serialization refusal was added. Full local
Go regression/build passed (app package 65.460 seconds; optional PostgreSQL cases
were not enabled because no Portal DB code/schema changed). Go vet, staticcheck
and Linux amd64 test-binary cross-compilation passed. Each new head needs its
own exact-source CI. No production deployment, real credentials, Status migration,
public SDK freeze or server beta configuration change is performed.
