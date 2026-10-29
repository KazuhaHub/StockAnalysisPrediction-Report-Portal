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
Only public references/receipts use the fixture file; no private key/document is
written there. Required markers/report flags are:

- `PORTAL_WRITABLE_START_RECONCILED` /
  `native_portal_writable_start_reconciled`.
- `PORTAL_WRITABLE_START_RETAINED_IDENTITY` /
  `native_portal_writable_start_retained_identity`.
- `PORTAL_WRITABLE_START_NATIVE_WITNESS` /
  `native_portal_writable_start_native_witness`.

This proves Portal rollback with an already applied runner result only after
fresh native execution. Positive expired-unknown runner recovery through Portal
is not claimed by this fixture. Native Linux results are pending.

## Remaining dependency

Extend the separate writable source through current readiness, protected
registration and host MAC/boot verification, eventually four exact effects.
Then wire current viewer access/activation and the automatic managed install
worker. Physical stop/writer revocation/update/retained-data/restore lifecycle,
broker, SDK/shared React/Ant shell, neutral reference apps/marketplace and full
P00–P14 acceptance remain incomplete. No schema/dependencies/toolchain,
production/credentials/Status adoption, public SDK freeze or beta/version
configuration changes are included.
