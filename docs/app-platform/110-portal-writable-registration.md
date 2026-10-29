# Portal writable registration intent, delivery and result recovery

This follows [109](109-portal-writable-readiness.md). A writable process that is
ready still needs its private backend registration file. This continuation
prepares that exact identity, delivers it through the authenticated root runner,
and settles the actual result in Portal. It does not yet verify independent host
key possession/boot, open a viewer, activate a generation or drive the complete
managed installation worker. The product remains incomplete until those paths
and the other P00–P14 requirements pass.

## Exact existing source and one registration effect

Initial writer, start and readiness readers retain their existing two/three-effect
contracts. A private registration-specific source path permits exactly three
original effects plus the sole registration effect, and validates all four before
returning. It is restricted to the separately reviewed writable initial install,
generation 1, pending installation, candidate activation and the current operation
at `activate`. There must still be one original namespace and no access grants.

The continuation reconstructs the exact data receipt, applied writer, original
applied start receipt and canonical stored readiness. It pins the same resource,
namespace, release, physical source facts, writer fence, runtime/control authority,
review, publisher, OU authority, candidate timestamps and current lease. It does
not adapt writable evidence into a readonly candidate contract.

The first preparation requires a native readiness timestamp within the preceding
three seconds and no later than the host clock. The commit transaction repeats
this freshness check before accepting the new intent. Once an intent exists,
readiness is historical source binding: retrying delivery or reading its original
result must not require another container/start or silently replace the identity.
Current source, runtime, lease and publisher authority are still mandatory.

Registration uses Portal's existing encrypted installation identity seed and the
existing per-generation backend registration derivation. A shared private helper
now accepts a verified descriptor and exact release digest, rather than requiring
a readonly candidate structure. It verifies that descriptor bytes hash to the
release. Original readonly callers keep their original validated source wrapper.
The returned private handle is closed on every failure and by successful callers;
exported document bytes are cleared after digesting or bounded transport delivery.

The Portal effect uses existing `stage='activate', effect_number=1, kind='activate'`
columns, without a schema addition. It has one stable UUID and logical digest;
its signed writable registration command binds:

- the renewable signed original writable start reference;
- the SHA of the exact canonical original running receipt;
- the SHA of the private registration document;
- the same current controller authority and lease-bounded nested expiry.

Only digests enter the signed command, control effect and audit detail. Neither
the signing key nor private document is stored there. The existing encrypted
identity seed remains the source of deterministic registration material. The
runner's separate writable registration index is the fourth effect in its
existing command history; no new wire action or journal layout is introduced.

## Deliver, read and commit

`preparePlatformWritableRegistration` locks/revalidates current OU and publisher
trust, operation lease, runtime and review. It creates the sole prepared effect
and intent audit atomically. Re-read the complete source inside the transaction,
compare snapshots, recheck authority and signature, then commit. An audit failure
must roll back the intent. Existing prepared/applied records must have exact
state, timestamps, fences, digest and canonical receipt; partial or unknown Portal
history is not silently repaired.

The Linux endpoint uses the existing closed `WritableRegistrationDeliver` or
`WritableRegistrationRead` action over the protected root/non-root-controller
transport. Private document bytes are exported only for delivery and cleared
when it returns. Non-Linux refuses. Before IO, the Portal caller checks the exact
configured managed root endpoint. The runner independently checks signed source,
current journal/descriptor authority, exact owned process and protected file
rules. This continuation adds no URL, Docker socket access or callback for apps.

The returned opaque lifecycle observation must have the exact action, endpoint,
root peer, controller UID/GID, authority/key and request SHA. It must be received
within three seconds using the local monotonic receipt time. The canonical receipt
must match effect, command digest, original running receipt, native process,
document digest, source/current control revision and valid native/file timestamps.
Signed bytes must exactly encode the prepared command and its nested start wire
reference. Parsed JSON or a stored historical receipt cannot manufacture this
opaque proof.

`commitPlatformWritableRegistration` clones public source snapshots before IO
results are consumed. In one transaction it locks/reloads current authority and
all four effects, compares the exact source, reverifies the signed command, and
CAS-updates the sole prepared registration effect to applied. CAS predicates pin
identity, runtime/epoch, digest, fences and original timestamps. Increment only
the operation row revision, retaining stage/state revision, lease, owner and
fence. Record one delivery audit. Reconstruct the resulting complete source and
require exactly the expected receipt/timestamp update, then recheck current
trust, lease, signature and observation freshness before commit.

An already applied identical receipt returns the unchanged operation and emits
no second delivery audit. A different result refuses. Delivery does not modify
the original start/writer receipts, readiness, native resource, namespace revision
or data. It does not create an active connection, boot observation or viewer grant.

If native delivery succeeds but the Portal transaction fails, the prepared Portal
effect remains. `completePlatformWritableRegistration` obtains a new authenticated
read of the original runner result and commits it under current authority. It
never substitutes a new process or key. Positive expired-unknown delivery
reconciliation through the composed Portal path is still a separate acceptance
requirement; this phase proves recovery from Portal commit failure after actual
successful native delivery.

## Verification and limits

Explicit SQLite/PostgreSQL tests cover deterministic identity/digest renewal,
private document/key exclusion from control records, exactly four effects and no
grants, unchanged storage after zero-proof refusal, canonical applied historical
receipt loading, old-reader refusal and current source failures. Refusals include
missing or stale initial readiness, changed original start, unusable encrypted
identity seed, runtime/publisher disablement, restore pause, expired lease,
registration effect alias/digest/state/receipt/fence/time changes, extra history
and forced intent-audit rollback. A deterministic test checks inclusive three-second
freshness, the following millisecond, future timestamps and missing observation.
Prepared intent reuse is also tested after actual observation expiry. Synthetic
SQL receipts are historical metadata only and never opaque native proof.

The existing disposable non-root Portal/root runner fixture now continues from
actual writable readiness into registration:

1. Prepare the exact original source and refuse completion before native delivery.
2. Force Portal delivery-audit failure after real native file delivery and require
   the Portal effect and operation revision remain prepared/unchanged.
3. Read the actual original root result, remove the failure trigger, complete its
   Portal transaction and retain the exact file receipt, source and native ID.
4. Replay delivery and require unchanged receipt, revision and one delivery audit.
5. Refuse the stale pre-settlement lease. Obtain a new actual read, disable the
   current runtime binding and refuse commit with an unchanged storage dump.
6. Require four original effects, candidate/pending state and no access grants.
   Existing root witness still independently checks the exact running process,
   original start time, physical full-quota result and owned resource cleanup.

The acceptance script requires and reports:

| Marker | Report flag |
| --- | --- |
| `PORTAL_WRITABLE_REGISTRATION_COMMITTED` | `native_portal_writable_registration_committed` |
| `PORTAL_WRITABLE_REGISTRATION_RETAINED_IDENTITY` | `native_portal_writable_registration_retained_identity` |
| `PORTAL_WRITABLE_REGISTRATION_STALE_SOURCE_REFUSED` | `native_portal_writable_registration_stale_source_refused` |

The quota workflow now includes writable/writer Portal file paths for both PR and
main events, so later source-only changes cannot omit that native composition.
Fresh Linux acceptance is pending; earlier runner-only registration acceptance is
not a substitute for these Portal cases.

## Next requirements

Connect the existing writable host handshake to independent key/MAC/current boot
validation, current viewer relay and managed activation, then the actual install
worker/scheduler. Complete positive expired-unknown recovery through Portal,
physical stop/fencing/revocation, update/migration/rollback/retained uninstall and
restore/checkpoint lifecycle, broker/secret/task capabilities, SDK/shared React/
Ant shell and neutral static/managed reference apps from the real market. D5
visual/SDK and D6 production gates remain outstanding. No production, real
credentials, Status adoption, public SDK freeze, dependency/toolchain or beta
configuration changes are included here.
