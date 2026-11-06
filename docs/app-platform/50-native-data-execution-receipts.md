# Native data execution and durable receipts

This connects the existing signed admission, stable resource reservation and
one-shot execution claim to the concrete XFS primitive. It is trusted runtime
infrastructure for isolated development, with no worker/startup/socket dispatch
or production activation. The full phone-app installation experience remains in
progress.

## Call sequence

1. Admit the current signed prepare-data envelope.
2. Reserve its exact namespace/project resource in the shared journal.
3. Claim it once. The command becomes unknown before a live handle is returned.
4. Call `Store.ExecutePrepareData(ctx, claim, root, expectedRootIdentity)` with
   a concrete `*data.Root` and an independently established infrastructure pin.
5. The execution gate rechecks current command/resource authority and consumes
   the shared process-local handle. The callback's context is bounded by the
   original envelope expiry, including waits for root/native/journal access.
   Binding refuses a wall clock earlier than the original claim timestamp.
6. Observe the concrete native root and compare it with the expected pin.
7. In a short transaction, recheck authority/resource/history and persist the
   physical UUID/inode in the existing claim marker. Commit before any mkdir or
   quota mutation. Only then update the private live marker.
8. Call native Prepare and fresh Inspect with the mandatory expected root pin.
   Compare namespace, project, device, inode, limits and inheritance. No SQL
   transaction spans filesystem work.
9. In another short transaction, verify current signed authority, exact bound
   claim and unchanged initial resource again; atomically write the typed
   physical receipt and mark the resource present/revision 2 with observed time.
   The command becomes applied only in this same commit.
10. Return the receipt only after commit succeeds. Keep controller ack, writer
    fence and generation unchanged. Portal stage completion is a later step.

Applications cannot supply native observations, callbacks or success flags to
this API. No public route accepts a receipt. The private settlement helper is
called only from the concrete execution path; synthetic observations appear only
in unit tests for transaction/refusal behavior. Actual execution is separately
exercised by the disposable native workflow.

## Root binding without a new storage product

The existing command claim marker adds optional `root_filesystem_uuid` and
`root_inode` decimal-string fields. An unbound claim has neither field. Both are
required once native work can start. This is metadata of that execution attempt,
not a new table, column, journal version, config file or per-app database.

Before binding and settlement, scan up to the approved 65,536 retained data
attempts. Every previous bound uncertain claim or applied physical receipt,
including old epochs and absent resource history, must identify the same root.
A caller cannot switch a deployment to a different directory by supplying a new
matching pin. Conflicting, malformed, duplicate-key, noncanonical, unknown-format
or incomplete records require reconciliation instead of being skipped. Empty
receipts are acceptable only for prepared commands that have never been claimed.
The initial implementation conservatively refuses unsupported reverted formats.

Retain these command tombstones with the resource/project history. A future
compaction/deletion implementation must preserve the same immutable storage
binding before deleting any evidence. Do not clear history to make a new root
appear acceptable. Disaster recovery/replacement needs an explicit reviewed
reconciliation flow and is not implemented here.

## Receipt shape and meaning

Format is `portal.runner.prepare-data-receipt/1-dev.1`, bounded by the existing
16 KiB receipt column. It contains the original claim ID, effect/resource/owner
binding, original fence, claim/expiry timestamps and physical root, plus:

- Deployment, controller epoch/revision and installation identity.
- Signed payload/release digest, data format, generation and writer fence.
- Reserved project ID, observed namespace inode and device.
- Observed hard byte/inode limits and usage; inheritance must be present.
- Observation time sampled after native work and within the original envelope.

All numbers are canonical decimal strings. Limits must match the signed intent;
usage is diagnostic and is not a readiness grant. Device is an observation, not
a stable mount identity. The receipt is a historical local observation, not a
signature from a new runner key, an acknowledgement, current health, writer
access, mounted application volume or successful Portal stage transition.

No controller receipt transport exists in this slice. Existing socket admission
must continue to report admission only, never reinterpret command applied state
or a caller's JSON as a fully authenticated controller receipt.

## Failure and restart behavior

| Boundary | Result |
| --- | --- |
| Invalid root or wrong expected pin | No native mutation; claimed work remains unknown |
| Conflicting prior root | No native mutation or root rebind |
| Binding transaction failure | No native mutation; live marker not advanced |
| Binding commit acknowledgement lost | No creation is authorized; reconcile durable unknown state |
| Native operation fails/partially runs or expires | Bound unknown claim, no physical receipt |
| Native success then settlement fails | Actual directory may exist; resource observation stays unknown and no receipt is returned |
| Settlement commit acknowledgement lost | May be durably applied; do not infer failure means absence |
| Callback error, copied handle, close/reopen | Never reset/reconstruct one-shot authority or blindly replay |
| Successful receipt then remount/restart | Historical receipt remains, but fresh native inspection and current authority are still needed |

The execution gate excludes another data attempt until the synchronous callback
settles. Existing Close waits for it before releasing journal ownership. Native
roots stay UID 0/mode 0700. This does not start application code, grant a UID,
mount a writer, reconcile unknown effects or reactivate restored data.

## Verification and reproduction

Tests first failed for the missing executor/receipt/binding API. Unit tests cover
missing concrete native providers, durable binding/reopen, alternate root refusal,
canonical history/corruption, atomic settlement, trigger-injected rollback on
both command/resource updates, changed authority/ack/resource/receipt, canceled
SQL connection waits, and consumed-handle refusal. Existing claim concurrency,
expiry, close and reservation capacity tests continue to apply.

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 ./...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -race -p 2 ./internal/apprunner/journal
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go vet ./internal/apprunner/journal ./internal/apprunner/data
python3 -B -m unittest discover -s scripts -p project_quota_probe_test.py
```

The extended project-quota workflow builds a second Go native test binary for
journal execution. Its single private control journal lives outside the owned
app-data filesystem. The expected root UUID/inode comes independently from
blkid/stat. It performs real signed admission/reservation/claim/native execution,
checks a returned receipt equals the durable record, refuses another physical
root, successfully prepares another installation on the original root, then
injects a final receipt write failure after real native preparation. It confirms
physical data exists while the command/resource remain unknown and the consumed
handle cannot replay. After remount it checks physical identity/limits and
historical receipts; unknown and applied effects cannot be re-claimed. Finally,
after changing only the owned image UUID, a fresh signed claim with the old pin
must refuse before creating a namespace. The previous native quota workloads and
owned cleanup checks remain. Source `eb3f52d64b291def1de82b68c853bae2dbf1f145`
passed [native run 40298739199](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40298739199).
The downloaded report confirms every new native_journal field plus existing quota
checks and owned cleanup. A subsequent pre-mutation clock guard has targeted race
coverage and requires its own source CI. A negative overlay removing root-history
comparison fails at the root-rebind assertion, rather than a compile error. No native arm64 or production acceptance
is implied by cross-compilation or disposable amd64 evidence.

## Next required integration

Unknown-effect reconciliation needs new signed recovery authority, exact retained
binding/ownership observations and a settlement path that never blindly replays
creation. Controller receipt authentication/delivery, Portal stage CAS, writer
admission/exclusion, managed lifecycle, market install/progress, backups and
release gates still remain. Existing storage baselines, real credentials, Status,
server beta configuration and production runtime privileges are unchanged.
