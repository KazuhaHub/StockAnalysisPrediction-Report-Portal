# Signed data dispatch and bounded static installation

This implements the explicit execution request and a bounded driver connecting
existing static installation stages. It replaces the acceptance fixture's manual
root-side data execution with a real non-root Portal request. It does not start a
background daemon, enroll a controller, expose an HTTP installer or provision any
production OS identity. The full application platform remains in progress.

## Responsibilities and entry points

| Owner | Entry point | Responsibility |
| --- | --- | --- |
| Runtime infrastructure | `journal.Store.DispatchPrepareData` | Choose the allowed next data action from durable command state |
| Runtime infrastructure | `transport.ListenDataController` | Explicitly expose data execution on an existing provisioned root socket profile |
| Non-root Portal | `transport.PrepareControllerData` | Submit one signed execution request and authenticate its matching receipt |
| Portal repository | `dispatchPlatformInstallData` | Persist/revalidate intent, request data work, atomically commit observed stage progress |
| Portal worker | `runPlatformStaticInstall` | Drive one already-claimed initial static installation through its remaining stages |

The runtime receives a concrete open native root, its independently provisioned
filesystem UUID/inode pin and the already-open shared journal. None comes from an
application command, browser parameter, package or user-controlled filesystem
path. Provisioning and lifecycle ownership remain with the deployment operator:
the caller must keep the root and journal open until the server has stopped its
sessions. There is no second journal, per-app database or storage migration.

## Dispatch according to durable state

The orchestrator first rejects invalid/missing root configuration, canceled
context, closed store and malformed envelope bounds. It clones the signed input
and requires the actual native root identity to equal the configured pin before
command admission. The existing admission method verifies current durable
controller trust, signature, logical identity, expiry and fencing. An admitted
fence different from the current fence requires reconciliation elsewhere.

| Admitted state | Permitted action | Refused shortcut |
| --- | --- | --- |
| prepared | Obtain/validate the stable reservation, acquire a first durable claim, execute through the concrete native provider | Treating admission or reservation as physical completion |
| unknown | Existing read-only reconciliation using fresh signed authority, exact root/history and original effect | Calling Prepare again, repairing partial data or reconstructing a live claim |
| applied | Read and validate the unchanged historical receipt and its exact root pin | Recreating the namespace or refreshing its physical observation |
| anything else | Refuse | Guessing a replacement operation |

Resuming a prepared reservation is safe only because `ClaimPrepareData` changes
the durable state to unknown before returning its process-local one-shot handle.
Concurrent first calls cannot claim the same prepared command twice. One may
finish while others refuse uncertain state or observe the applied receipt. A
successful repeat returns the same physical identity and receipt. It does not
prove a second execution occurred or grant a writer.

An unknown claim whose root was never bound, whose native data is absent or whose
preparation is partial remains unknown. The orchestrator does not repair it.
Reconciliation uses the existing execution gate so it cannot inspect concurrently
with a live native attempt. A fresh signature must be later than the original
claim; original expired authority is never resurrected. Completed recovery keeps
the original claim and native identity in the canonical recovered receipt.

## Explicit transport operation

The existing admission and read-only receipt protocols keep their meanings.
`ListenController` and the private development listener do not enable execution.
`ListenDataController` requires root, a concrete journal/root/pin, and the existing
non-root controller UID/dedicated GID profile. It checks native identity before
creating the socket. It creates no parent directory, user, group or trust record.
Every connection still passes kernel peer UID authentication before frame reads.

The execution operation uses the same bounded receipt framing with distinct
protocol identifiers:

```text
request: PRCE0001 + 32-byte random nonce + 4-byte big-endian command length
         + exact command bytes + 64-byte signature + EOF
reply:   PRCF0001 + SHA-256(entire request) + 4-byte receipt length
         + canonical receipt JSON + EOF
```

Commands are at most 65,536 bytes; receipts at most 16,384 bytes. The existing
three-second session deadline and eight-session bound apply. Truncated,
oversized, trailing, cross-command, nonce-mismatched, future-observed and
wrong-protocol replies refuse. An admission acknowledgement or ordinary receipt
reply cannot masquerade as an execution reply. Detailed errors, signing keys and
live execution handles do not cross the socket.

The controller pins the root socket's path/inode, ownership/group/modes and kernel
server UID through the existing connection helper. It verifies the current signed
envelope before and after the round trip and requires the receipt to match the
exact logical command/ownership. Only this authenticated client can mint the
opaque `DataReceiptObservation`. Its receipt remains historical preparation
evidence; it is not an application-ready flag.

A deadline, cancellation, missing reply or process loss can follow durable or
physical work. Keep the exact effect/namespace identity. Use current authority to
read/reconcile it; do not create another effect merely because a request failed.
The runtime leaves uncertain work unknown, and Portal does not mark the operation
complete based on transport admission alone.

## Portal data request and bounded driver

`dispatchPlatformInstallData` first calls `preparePlatformInstallData` with the
current exact lease. That transaction rechecks the original reviewed package,
audience, sponsor, publisher, runtime, epoch, pause state and lease before creating
or reusing its stable intent and signing a bounded envelope. It then makes the
explicit execution request and passes the opaque response through the existing
full observation transaction. This is the same atomic namespace/effect/stage
commit used by read-only receipt completion. An unavailable endpoint leaves one
prepared intent; retries cannot allocate extra namespaces or grant access.

`runPlatformStaticInstall` accepts only an already-claimed initial unmanaged
install. It has a maximum of six transitions and dispatches only these cases:

```text
queued -> validate -> download -> stage -> prepare_data -> activate -> complete
          artifact transactions          signed data       activation transaction
```

After each committed step it replaces its private lease token with the returned
row/state revisions. It checks cancellation between steps. Artifact transactions
now also use `BeginTx(ctx)` and context-bound SQL, allowing cancellation while a
stage is blocked in an audit write. The compatibility repository entry point
continues to use a background context for existing callers.

On failure the driver returns the last confirmed lease token and the error. That
is progress information, not proof that a lost commit acknowledgement left the
database unchanged. Callers must revalidate durable state before taking over.
On success it returns the completed activation and clears the lease from its
result. The driver never silently renews/reclaims a lease, enrolls a runtime,
starts a polling loop or skips a failed stage. Unsupported/managed/non-install
operations refuse rather than borrowing this static path.

## Verification and continuation

Repository tests cover invalid native configuration before admission, unavailable
endpoint retries retaining one intent, worker progress/token replacement,
activation completion, cancellation before work and cancellation during a real
PostgreSQL audit delay. Existing staging/data/activation checks remain required.
The transport malformed-reply corpus now runs against both read and execution
protocols, including swapped-protocol refusal. Read-only listeners must reject an
execution request without treating it as generic admission.

The disposable native journal fixture additionally exercises dispatch recovery of
complete unknown data, unchanged applied replay, partial/absent refusal and
concurrent first requests sharing one receipt/resource. The Portal fixture now
uses `ListenDataController`; its non-root child dispatches execution, retries an
applied request and reads the same receipt. The root parent no longer performs
manual admission, reservation, claim or execution. Portal then commits data,
proves that this alone did not activate/grant access, and finishes activation via
the bounded driver before reading the protected document. A second independently
signed application starts at queued and runs the entire bounded driver without
manual intermediate advancement, then verifies completion/lease clearance and
protected document access.

The workflow report adds `native_dispatch_recovers_without_reexecution` and
`native_portal_signed_data_dispatch`, plus
`native_portal_static_driver_queued_to_complete`, only after these fixtures pass. A local
cross-compile cannot establish native success; record the actual successful run
and downloaded report separately. `execution_ready` must remain false until the
remaining runtime/platform release gates pass.

Remaining work includes explicit daemon startup/configuration, automatic operation
selection and lease coordination, managed application lifecycle, runtime-free
static data support, HTTP/review/market installation UX, SDK/shell acceptance and
release gates. No production enrollment, credentials, Status migration, public SDK
freeze or server beta setting is changed by this implementation.


## Observed source evidence

Source `6e13be8466430603a515006d13dea5a8d6914756` passed disposable native
run [40320510850](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40320510850).
The downloaded report confirms `native_dispatch_recovers_without_reexecution`,
`native_portal_signed_data_dispatch`, the preceding activation/launch and
separate-data-stage assertions, partial/absent refusal and owned cleanup. The
workflow also passed the full Linux transport race suite, including malformed and
swapped execution replies. `execution_ready` remains false.

Final local full Go tests with disposable PostgreSQL passed (application package
212.343 seconds). Focused SQLite/PostgreSQL worker/staging/dispatch race tests,
including in-flight context cancellation, passed. Vet, staticcheck, Linux
amd64/arm64 cross-compilation, 12 acceptance-script checks and workflow lint
passed. Main and daemon source CI have independent status.


A follow-up native fixture adds the full queued-to-complete second application.
Its additional report flag is not established by run 40320510850; it requires a
successful run of the follow-up source.
