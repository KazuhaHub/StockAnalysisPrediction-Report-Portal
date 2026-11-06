# Writable candidate reservation and one-shot claims

This development slice follows [96](96-writable-candidate-intent.md). It connects
completed-writer-bound intent to an exact pending container identity and one
process-local invocation. It does not create/start a Docker container, implement
native writable preflight, mint a running receipt or activate an application.

## State and entry points

Use these trusted internal calls in order; none is an application/browser API:

1. `AdmitWritableCandidateCommand`: verify and persist the original no-resource
   prepared intent, as described in 96.
2. `ReserveWritableCandidate`: under current signed authority, create exactly one
   pending resource for the future container already named in the writer receipt.
   A concurrent/repeated call returns that same validated reservation.
3. `ReadWritableCandidateReservation`: read a prepared intent with that exact
   pending resource. Use this specific later-stage reader; the earlier
   no-resource intent reader and admission scope intentionally remain strict.
4. `ClaimWritableCandidate`: atomically change the start command from prepared
   `{}` to unknown with a typed claim marker before returning a live handle.
5. `RunWritableCandidateOnce`: serialize one trusted synchronous callback under
   current durable authority and the original signed deadline. Recheck source,
   resource and current authority after IO. Callback success alone leaves the
   command unknown and resource pending; it is not process success/readiness.

After a claim exists, do not call the prepared reservation reader, recreate a
claim, reset unknown to prepared or retry a possible effect. Later native
execution/reconciliation must add concrete preflight and opaque process proof.
The current callback is infrastructure code only, not code supplied by apps.

## Pending resource ownership

The record uses the approved `runner_resources` table, kind `container`, with
these exact initial facts:

| Field | Required value |
| --- | --- |
| ID | The writer's already bound future container UUID |
| Installation / namespace / epoch / release / generation | Exact verified source tuple |
| Control and writer fences | Original admitted fence and verified writer fence |
| Native ID | `writable-candidate-pending/v1/<resource UUID>` |
| Desired / observed state | `present` / `unknown` |
| Revision / observed timestamp | 1 / NULL |
| Creation / update times | Equal; not before start intent creation |
| Ownership hash | Separate versioned ownership described below |

The writable pending prefix is distinct from the legacy read-only prefix. The
resource lookup also catches the same pending native name under a different
resource UUID; neither alias is adopted. No host path, native Docker ID or ready
bit can be provided by the request. The reservation remains metadata only.

Ownership purpose `portal.runner.writable-candidate-ownership/1-dev.1` binds
exact deployment/epoch, start effect, resource, installation/operation,
namespace/release/generation, original control fence, writer fence, logical
payload and both exact preparation/writer receipt hashes. Current key revision
and transport dates are deliberately excluded from this stable ownership.
Original evidence revisions remain bound inside the immutable receipt bytes.

Therefore a currently authorized key/revision can continue an unclaimed prepared
reservation without rewriting ownership. A higher-fence request cannot take it
over. A later claimed handle still contains its original signed bytes and claim
control revision; key/revision changes invalidate that live invocation.

No new table, column, enum, migration, auxiliary database or persistent side
store is introduced. Existing volume revision 3 and original receipts remain
unchanged. No data permission is changed by reservation or claiming.

## Exact extended source/history checks

Each prepared/unknown transaction reacquires journal write intent and verifies
all nested signatures against current durable controller trust. Require exact
stored effect/slot/logical identity, unacknowledged command, original fence and
current generation/epoch/resource floors. Check command creation/update order,
source writer completion time and signed clock/deadline bounds.

The shared source core still verifies the original preparation, completed writer
receipt, current policy, original ownership, quota/native identity and volume
revision/timestamps. The earlier initial-intent reader retains its exact
three-command/no-container history rule.

The new reservation/claim scope permits that same three-command history plus
**only** its independently validated pending container resource. Validate every
resource field and ownership hash before excluding its ID from the conflict
scan. Other namespace resources, guessed/running/stopped containers, foreign
owners, wrong prefixes, changed release/fences/timestamps/revision or extra
installation commands refuse admission. This is not an arbitrary history bypass.

Prepared records require the original `{}` receipt and equal command timestamps.
Unknown records require the exact canonical live marker, an existing validated
pending resource, claim time not before resource creation and command update time
equal to claim time. No unknown record issues another executable handle.

## Claim and execution boundary

The marker format is `portal.runner.writable-candidate-claim/1-dev.1`. It retains
a random nonzero claim UUID, exact effect/resource/logical payload, stable
ownership and both receipt hashes, current claim control revision, original
admitted fence, fixed writable profile and original claim/expiry times.

The live handle additionally holds its Store owner, copied signed bytes, a
monotonic deadline and a shared atomic consumed flag. JSON marshal fails; decode
clears/refuses the handle. Copies share the one-shot flag. A different Store or
owner reopen cannot use an old handle or reconstruct one from marker JSON.

`RunWritableCandidateOnce` holds the shared Store execution gate across preflight,
callback and postflight; no SQL transaction spans native IO. It verifies exact
unknown state and resource/source before consuming the handle and calling a
trusted callback with copied command, verified index projection, reservation,
original preparation and writer evidence. The callback must honor cancellation,
stop all IO before returning, and must not close/reenter the Store or mutate/
settle its claim/resource rows.

After invocation, reread durable current authority, source, marker and resource.
Any changed source/resource/time/acknowledgement/floor/authority, cancellation,
expiry or callback failure returns `ErrWritableCandidateUncertain` with
`Invoked=true`. No retry, fake running receipt, cleanup or state reset follows.
Even a successful callback leaves unknown command, pending native ID, resource
revision 1 and NULL native observation time. Native writable execution must later
bind real preflight before Docker effects and settle separate opaque evidence.

Close marks the Store closing and waits for the synchronous invocation before
closing the database and releasing journal ownership. A new owner cannot open
the same journal while the old callback remains live. Closing during invocation
prevents a successful completion claim and preserves unknown metadata.

## Tests and native evidence scope

Real SQLite/crypto tests cover first concurrent reservation and claim, resource
idempotence, copied-handle exactly-once invocation, prepared owner reopen and
unknown owner/JSON/cross-Store refusal. Current-key renewal preserves original
ownership and source evidence while higher fences fail. Tests inject authority,
ack/marker/ownership/native prefix/resource time/source time/writer fence/extra
resource changes both before and during IO; preflight refuses invocation and
postflight preserves uncertainty. Actual signed expiry, cancellation and Close
waiting tests exercise the live boundary.

Synthetic reader fixtures test metadata/crypto only; they do not construct native
writable process proof. The disposable native writer fixture additionally runs
reservation/claim/one-shot checks after both actual execution and recovered writer
receipts. Its callback only reinspects the actual XFS writable allocation. The
test requires unchanged physical allocation/permissions, unknown start command,
pending revision-1 resource, no native observation and refusal of repeated claim/
invocation. The parent requires
`NATIVE_WRITABLE_CANDIDATE_CLAIM_ACTUAL_SOURCES_VERIFIED` before emitting
`native_writable_candidate_claim_actual_sources_verified=true`.

Local full runner race passed (journal 127.626s, transport 1.527s; other packages
cache hits). Expanded writable-contract race passed in 17.748s and platform
regression in 9.778s. Linux amd64/arm64 compilation, vet/default Linux staticcheck
and all 14 Python probe tests passed. Functional source `62eb67278c259b39359ea6867b6d0765bc044890` passed quota workflow
40694331237/job 122836964588. The new actual-source claim flag is true after both
native writer receipt kinds. The read-only callback retains unchanged XFS
allocation and unknown/pending journal facts; repeated claims/invocations refuse.
Existing writable-intent/writer/recovery/filtered-quota/current-viewer/owned-cleanup
evidence remains true; fixture registry cleanup passed. Linux control/appplatform/
transport/driver race passed in 1.448/1.396/4.854/2.461s. Actual execution evidence
is amd64; arm64 is compilation only.

Daemon workflow 40694331553/job 122836965353 passed the full runner race suite
(journal 179.838s), native closed-container/IPC checks and Portal confirmation
through the root endpoint. General workflow 40694331560 passed all three app
race shards and aggregate, web checks/tests, workflow lint and Docker smoke.
Go job 122836966930 passed staticcheck but retains the existing x/net v0.60.0
vulnerability gate failure (GO-2026-6866/6860); its following non-race Test step
was skipped. This is not an all-green release. Reopening Store
objects is not a daemon kill/relaunch test. Actual writable process startup and
full process lifecycle acceptance remain required. No production, credentials,
Status migration, server beta, dependency or toolchain changes are included.

## Next implementation

Implement the concrete writable container plan, protected writable data/mount
pins and preflight binding under this claim before create/start. Record a typed
opaque native running receipt atomically, then add current historical reads and
observation-only recovery of uncertain starts. Keep the old sealed candidate
protocol and reader independent. Readiness/registration, managed activation,
normal workers, physical stop/fencing, broker, SDK/shared shell/reference apps,
update/uninstall/restore and full P00–P14 acceptance remain outstanding.
