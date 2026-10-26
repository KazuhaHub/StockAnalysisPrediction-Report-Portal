# Reconcile uncertain completed data preparation

This adds an inspection-only recovery path for a prepared directory whose final
receipt was not committed. It does not resume creation, repair a half-created
namespace, reset an execution claim or complete a Portal operation. Full managed
installation and the remaining recovery states are still in progress.

## How the runtime calls it

1. Keep the existing unknown command, original bound claim and initial resource.
   Do not delete/reinsert them or change unknown to prepared.
2. Obtain a newly signed prepare-data envelope for the exact same effect/payload,
   generation, controller epoch, original fence and writer fence. Its issue time
   must be strictly later than the original claim timestamp, and it must be
   currently valid under the durable enrolled controller (maximum 30-second TTL).
3. Call `Store.ReconcilePrepareData(ctx, raw, signature, concreteRoot, expectedPin)`.
   No old live handle is accepted, reconstructed or required after restart.
4. The method enters the same cancelable execution gate used by native creation
   and Close. It reads the unknown marker under current signature/fence/resource
   checks in a short transaction. An unbound, malformed or conflicting marker,
   pending sibling work, changed ack/resource or unsupported history refuses.
5. Require the original root UUID/inode, claim/effect/resource/ownership/fence and
   deployment-wide retained root history to agree. Original resource ownership
   is re-derived, so this initial path also refuses controller revision/epoch or
   fence takeover rather than silently adopting old data under a new owner.
6. Commit the read preflight and inspect the concrete native namespace under a
   context bounded by the new envelope's expiry. No SQL transaction spans native
   inspection. The native call checks the pinned root, sealed ownership/mode,
   project ID, quota enforcement and inherited limits. It never calls Prepare.
7. Recheck the same current signed command, exact original marker and initial
   resource in another transaction. Native limits must match the signed intent.
8. Atomically mark the command applied and resource present/revision 2, with a
   typed recovered receipt. Return success only after that commit succeeds.

Renewing a command and calling ordinary admission still grants no execution
permission. Recovery is a separate trusted infrastructure method; no app route,
capability, socket dispatch or startup loop is added. A valid signature alone
never proves physical success. A newer controller/fence requires the future
explicit takeover/recovery protocol, not relaxed checks in this method.

## Historical receipt and fresh authority

Recovered receipts use `portal.runner.reconciled-data-receipt/1-dev.1` with the
same bounded native observation fields as ordinary receipts. Three additional
canonical string fields identify the new observation authority:

| Field | Meaning |
| --- | --- |
| `recovery_envelope_sha256` | SHA-256 of the exact currently verified renewed envelope bytes |
| `recovery_issued_ms` | New signed issue time, strictly after the original claim |
| `recovery_expires_ms` | New signed expiry, at most 30 seconds after issue |

The original claim ID, original expiry, original ownership/fence and physical
root remain unchanged. `observed_ms` lies within the renewed envelope, so a
completed directory can be recognized after the original envelope expired.
Normal receipts still require observation within the original claim's expiry and
must contain none of the recovery fields. Both formats preserve the immutable
root binding when retained history is checked on subsequent installations.

The receipt is an authenticated-at-creation local journal observation, not a new
runner signing-key format or a controller acknowledgement. Future transport must
still authenticate the exact local peer/request and verify the controller-side
operation before committing a Portal stage. Neither format grants a writer or
asserts current readiness. Numeric fields remain strings; no table, column,
migration, journal version, side database or storage product changes.

## Failure handling

| Situation | Behavior |
| --- | --- |
| Original envelope replayed or expired | Refuse; obtain fresh signed observation authority |
| Original marker has no physical root binding | Refuse; do not infer whether old execution happened |
| Different root, namespace/project, limits, owner or permissions | Refuse and preserve unknown |
| Namespace absent | Return native absence; do not create or reset the attempt |
| Namespace partially prepared | Preserve it and unknown state; no quota/project repair |
| Native inspection or current authority fails | No applied receipt or resource observation |
| Final DB transaction fails | Both command/resource changes roll back; a fresh read-only recovery may retry |
| Commit acknowledgement lost | Durable result may be applied; inspect history instead of recreating data |
| Two concurrent recoveries | Shared gate plus current-state CAS allows only one settlement |
| Close, restart, consumed old claim | Old execution authority never returns; fresh recovery uses durable intent |

This handles the completed-but-unrecorded case. Proving safe absence, repairing
partial creation, controller takeover, quarantining conflicts and staged rollback
remain separate implementation tasks. Do not turn errors into deletion/replay.

## Verification

Tests first failed for missing recovery/preflight APIs. Unit coverage checks old
execution-envelope refusal, fresh exact intent, real expiry of the original
window, changed controller/fence/writer/ack/marker/payload, atomic rollback and
retry, late authority changes and recovered receipt/root retention after reopen.
Synthetic observations in transaction tests do not prove native behavior.

The disposable XFS workflow extends the existing real native journal fixture. It
reopens after remount, waits until the original signed envelope actually expires,
then concurrently requests recovery with a fresh signature. Exactly one request
must commit; original claim and physical directory inode/project/limits/usage
must remain identical. It creates explicit interruption fixtures after binding
and after mkdir, proving missing and incomplete directories are not created or
repaired. After changing only the owned loop image UUID, recovery with its old pin
must return a root-identity refusal. All earlier quota/cleanup cases remain.
The journal native test timeout allows the deliberate original-expiry wait;
privileged execution remains limited to the owned disposable GitHub worker.

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 ./...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -race -p 2 ./internal/apprunner/journal
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go vet ./internal/apprunner/journal
python3 -B -m unittest discover -s scripts -p project_quota_probe_test.py
```

Inspect this PR's exact source CI and downloaded native report before claiming
these integration cases pass. Cross-compilation is not native arm64 evidence.
Production privileges, real credentials, Status migration, server beta settings,
controller receipt delivery and Portal stage completion remain outside this slice.
