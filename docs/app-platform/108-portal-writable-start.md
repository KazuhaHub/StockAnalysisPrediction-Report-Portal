# Portal writable backend start and historical completion

This follows [107](107-portal-writable-install-writer.md). Portal can settle the
writer, but cannot yet start its reviewed writable backend. This change joins the
exact applied writer source to a separately signed start effect and consumes
opaque running evidence from the existing root lifecycle endpoint. It advances
only to `probe`. Readiness, registration, boot/viewer access, activation and the
automatic managed worker still need implementation.

## Source and durable intent

The pre-start writer reader retains its original exact two-effect/no-start
contract. A private extended mode is used only inside the new writable start
loader. That loader independently requires the exact sole start effect before
returning a result. It accepts initial install, generation 1, at `start`, or the
same already settled start at `probe` for historical source validation.

The source must remain the separately confirmed writable review and configured
runtime, original preparation receipt, applied writer receipt, active namespace
revision 3 and one candidate activation identity. There are no access grants,
readiness bytes or observed activation time. Current administrator/review,
publisher/key/release, lease/fence/epoch and encrypted runtime trust checks remain
mandatory. All original source timestamps, identities and receipt bytes stay
pinned across native IO. Database/host time ordering uses the existing one-second
bound; physical observations may not be in the host clock future.

`preparePlatformWritableStart` reuses the future container resource ID already
allocated by writer preparation. It creates only one `start`, effect 1,
`start-candidate` row; it does not allocate another candidate or namespace. The
complete source now has exactly three effects: preparation, writer and start.
A partial/unrelated/fourth effect, unknown Portal effect, changed source, candidate
readiness/activation or foreign resource refuses rather than being repaired.

The new `WritableCandidateCommand` binds the current nested writer/preparation
references, exact canonical original writer receipt SHA, stable start effect ID
and closed writable sandbox profile. Signing uses the current encrypted control
key, the existing 30-second maximum and lease expiry minus one-second bound.
Logical IDs/digest stay stable as short-lived envelopes renew. Seed/key copies
are cleared. Repeated preparation produces one intent audit and never native
success. Readonly source/command consumers cannot adopt this format.

## Dispatch and running evidence

Three separately selected paths use the authenticated lifecycle endpoint:

- `dispatchPlatformWritableStart`: first prepared native attempt / applied runner
  replay. Unknown runner work cannot create/start again.
- `completePlatformWritableStart`: strict historical read after lost response or
  Portal transaction rollback.
- `reconcilePlatformWritableStart`: the existing observation-only runner recovery
  API, still requiring fresh authority after original unknown claim expiry.

The Portal caller persists/revalidates intent and freshly checks the exact managed
root target before IO. Only the concrete runtime/journal can dispatch. Endpoint
UID/GID, peer credentials, current nested signatures, descriptor and protocol
framing checks remain in the transport from 106. No browser JSON, callback,
stored native ID or parsed receipt supplies physical provenance.

A successful Portal commit requires an opaque start/read/reconcile response with
age at most three seconds, exact configured root/controller identities, current
authority/key, request command SHA and signed effect/source identity. Independently
parse the separate canonical writable running receipt and check:

1. Start effect/payload, stable resource ID, admitted fence, original data/writer
   receipt hashes and closed writable sandbox profile.
2. Exact stable ownership digest using deployment/epoch/installation/operation/
   generation/fences/release/source identities; not just a syntactically valid SHA.
3. Allowed original/recovery control revisions and claim/settlement time windows.
4. Preflight image equal to the applied writer image; exact native platform,
   seccomp policy, root filesystem/inode, project/device/namespace inode and quota
   equal to the reviewed original physical source.

This duplicates relevant source/binding checks at the host boundary; the runner's
concrete native preflight/postflight remains independently mandatory. A running
receipt is historical process evidence, not current readiness or active access.
Nested receipt copies are rebuilt on intent clone to avoid preflight pointer
aliases.

## Atomic Portal commit

After IO, lock/re-read current lease, publisher/review/runtime authority and the
entire exact source. Require unchanged writer/candidate/start identities,
creation/update timestamps, receipt state and logical command. Reverify the
current signed reference. CAS the prepared start effect to applied using exact
state, digest, runtime, epoch, fence and timestamps; commit the original receipt.
Advance operation `start` → `probe` with its exact state/row revision and lease.
Write one audit record, re-read the resulting applied source, and repeat current
authority/lease/freshness checks before commit. Any failure rolls everything back.
The candidate retains empty readiness and no observed activation time. No grant,
notification, registration key delivery or activation is written.

## Verification

SQL tests use synthetic historical writer metadata only. They cover stable
future resource/start IDs and writer receipt SHA, renewable signed deadline,
exact three-effect history, original two-effect writer reader refusal, readonly
separation, zero-proof refusal and unchanged dumps. Refusal cases include changed
writer/result/namespace/runtime/publisher, restore pause, stale lease/fence,
readiness injection, unknown/digest/time drift, partial/extra history. Both SQLite
and PostgreSQL variants are explicitly included in the focused workflow step
before the existing vulnerability scan. They do not depend on the race subset's
name filter. No local PostgreSQL is started.

The disposable Linux acceptance extends the non-root Portal fixture after actual
writer completion. It prepares signed start intent, verifies stable writer ID/
receipt, then forces a Portal audit failure after the root runner has actually
started the container and settled its receipt. Portal remains at prepared start.
It reads the original runner receipt, removes the trigger and commits that exact
result, retaining resource/effect/digest/receipt and advancing only to `probe`.
The stale original lease must refuse further dispatch. Source remains pending,
with exactly three effects, one candidate, empty readiness and no grants.

A separate root witness authenticates the public historical intent at issuance,
checks its signed writer receipt hash, unique native ID, image and every owned
identity/start/claim/source label, running state, zero restart count and actual
start time no later than the original native observation. Cleanup authenticates
ownership again before stopping/removing the exact fixture and releasing its IPC.
The backend fixture observes and strictly accepts only its two fixed actual byte
boundaries: native low-level 8MiB/128 inode and reviewed Portal 256MiB/65536 inode.
It tests the corresponding inode denial independently. The root witness waits
for the fixed proof, verifies the 256MiB/65536 inode denial results and reinspects
the exact running process after the completed probe. Intermediate/unenforced
byte limits refuse; no environment or application request selects a quota. This
keeps Portal's real limits while retaining the original small native checks.
Only public references/receipts use the fixture file; no private key/document is
written there. Required markers/report flags are:

- `PORTAL_WRITABLE_START_RECONCILED` /
  `native_portal_writable_start_reconciled`.
- `PORTAL_WRITABLE_START_RETAINED_IDENTITY` /
  `native_portal_writable_start_retained_identity`.
- `PORTAL_WRITABLE_START_NATIVE_WITNESS` /
  `native_portal_writable_start_native_witness`.
- `PORTAL_WRITABLE_START_FULL_QUOTA_VERIFIED` /
  `native_portal_writable_start_full_quota_verified`.

This proves Portal rollback with an already applied runner result only after
fresh native execution. Positive expired-unknown runner recovery through Portal
is not claimed by this fixture.

Local focused writer/start race passed in 15.893s; standalone start SQL race
passed in 8.381s. Broader Portal platform/managed race passed in 138.433s. Linux
amd64/arm64 Portal test builds, Linux vet/staticcheck and Python acceptance tests
(14) passed. Functional source is
`797e1ace7b9e841dd8f408fb370fde1ef7d8c4a9` (after the fixed Portal
quota fixture and completed-probe witness updates).

Final source passed disposable quota workflow
[40816295673](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40816295673),
job 123258102372. Checkout identifies the exact functional source merged into
parent `a54cc364f185689cf900bd4d46d6771906395113`. All four new Portal writable
start reconciliation, retained identity, native witness and full quota flags
are true. The actual non-root Portal/root runner path recovered after Portal
rollback, retained the original receipt/resource/native identity and advanced
only to `probe`. Root verified the completed 256MiB/65536 inode denial proof and
unchanged running/start-time facts. Both fixed fixture quota classifier tests
and prior native checks passed. `owned_resources_cleaned` is true and owned
registry cleanup passed.

General workflow 40816295709/job 123258136783 actually executed the explicit
SQLite/PostgreSQL writer/start contracts (25.957s combined), including start
intent and all refusal variants. Build/vet/staticcheck also passed; later
vulnerability scanning retained existing x/net v0.60.0 GO-2026-6866/6860 findings
(fixed version reported v0.62.0), skipping ordinary full tests. General workflow
[40816295709](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40816295709)
passed all three app race shards and aggregate, all web tests/build checks,
workflow lint and Docker font/smoke checks. Race shard 1/job 123258136904 ran the
full runner journal race in 223.364s and its app subset in 112.071s. The explicit
SQLite/PostgreSQL step, rather than the race name filter, covers the new start
SQL cases. The general workflow is not green because of the retained dependency
findings. The independent daemon workflow was not triggered by this Portal/
fixture-only diff; no fresh standalone daemon result is claimed.

## Remaining dependency

Extend the separate writable source through current readiness, protected
registration and host MAC/boot verification, eventually four exact effects.
Then wire current viewer access/activation and the automatic managed install
worker. Physical stop/writer revocation/update/retained-data/restore lifecycle,
broker, SDK/shared React/Ant shell, neutral reference apps/marketplace and full
P00–P14 acceptance remain incomplete. No schema/dependencies/toolchain,
production/credentials/Status adoption, public SDK freeze or beta/version
configuration changes are included.
