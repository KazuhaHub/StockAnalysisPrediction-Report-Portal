# Observe and recover an uncertain initial writer

This development slice follows [94](94-writer-execution-and-receipts.md). It
recovers a completed physical permission transition whose atomic journal receipt
was lost. It never repairs a partial transition or starts an application. It uses
the approved existing journal tables; no schema, migration or extra store changes.

## The failure being recovered

`ExecuteDataWriter` may change the namespace to root:65532 `02770`, then fail to
commit its receipt. That leaves the writer command `unknown` and the volume at
revision 2 even though native permissions are writable. Repeating the mutation
would be unsafe. A new process also cannot recreate the old one-shot claim.

Recovery therefore observes current physical state under fresh authority and
records what it actually observed. It does not pretend to know the unrecorded
original execution time or original executing control-key revision.

## Trusted call sequence

1. Retain the original effect, logical payload, exact preparation receipt hash,
   future resource and claim marker. The journal already owns these records.
2. Wait until the original claim's signed expiry. Issue the same logical
   `DataWriterCommand` under the current controller key with a fresh signed data
   reference. Its issued time must be strictly greater than the old expiry;
   re-signing the old dates is insufficient. The normal command limit remains
   30 seconds. This is observation authorization for an already unknown effect;
   it cannot reclaim or reexecute the original writer operation.
3. Call `Store.ReconcileDataWriter(ctx, raw, signature, descriptor, native)`.
   Supply the exact reviewed release descriptor and the platform-owned native
   runtime. This is a trusted internal API, not a new browser or public RPC route.
4. The Store acquires its execution gate for the complete preflight/observation/
   settlement sequence. Close waits before releasing the journal owner lock.
   The preflight independently verifies current controller identity, key/revision,
   epoch, exact admitted command/fence/floor, original claim/source receipt,
   resource revision 2, source ownership, timestamps and absence of conflicting
   installation history. Source observation must precede the original claim.
5. Bound execution to the new signed deadline and invoke only
   `CandidateRuntime.ObserveDataWriter`. It never calls `EnableDataWriter`,
   chmod/chown, create/start/stop/remove, or a directory repair routine.
6. The concrete observer checks the immutable image/platform and fixed policy,
   root identity and protected path chain, exact root:65532 `02770` permissions,
   namespace device/inode, project inheritance, hard byte/inode limits and bounded
   usage. It rejects local launch pins and all running/stopped Docker references,
   including direct/parent/child/symlink sources. Recheck inventory, held/fresh
   directory pins and physical allocation before returning evidence.
7. Only this concrete observer produces opaque
   `DataWriterRecoveryObservation`. It is a different type from execution proof;
   neither JSON nor a public snapshot can manufacture it. Bind the original
   effect, claim ID, logical/source receipt hashes and complete candidate tuple.
8. Reopen the exact unknown transaction after IO. Recheck current authority,
   unchanged source and claim, current metadata and clock/deadline. Match opaque
   evidence against a newly derived request. Native observation cannot predate
   preflight or the recovery envelope, or occur after settlement.
9. Atomically set volume revision 2->3 and writer unknown->applied, preserving
   original data ownership/fences/generation. Store a recovery receipt. Never
   update only one record or reset unknown on failure.

## Two receipt formats

Original execution receipts remain byte-compatible with
`portal.runner.data-writer-receipt/1-dev.1`. They contain no recovery fields and
require observation/settlement within the original claim window. Their binding
purpose and original encoding remain unchanged.

Recovered receipts use
`portal.runner.reconciled-data-writer-receipt/1-dev.1` and a separate binding
purpose. They retain the original claim verbatim and add:

| Field | Meaning |
| --- | --- |
| `recovery_envelope_sha256` | Hash of the exact newly verified signed writer command bytes |
| `recovery_issued_ms` | Issued time of the fresh current-authority reference |
| `recovery_expires_ms` | Deadline of that fresh reference |
| `control_revision` | Revision authorizing this later observation, not an invented original execution revision |
| Native `observed_ms` | Time of the later concrete observation |
| `settled_ms` | Atomic journal completion time |

The recovery issued time must follow the original expiry. Observation and
settlement must fall within the new window; observation must precede settlement.
The separate digest binds recovery dates/envelope, original claim and native
identity. It does not replace current signature verification or opaque proof.

`ReadDataWriterReceipt` recognizes both strict canonical formats. A current-key
renewal can read either format without rewriting the original stored evidence.
The evidence revision must be between source preparation revision and current
reference revision. An original receipt with recovery fields, recovered receipt
with an old observation window or changed binding is refused.

## Refusal and retry behavior

- Sealed, missing or partially changed permissions: refuse; leave native state
  and unknown intent unchanged. Do not finish chmod/chown or create a directory.
- Wrong root/inode/project/limits, active/stopped mount references, unknown
  inventory/path evidence, changed authority/source/fences or expired new
  envelope: refuse. No guessed resource takeover or deletion.
- Recovery transaction failure: leave volume revision 2 and writer unknown.
  Retrying a fresh **read-only recovery** can be safe; reexecuting permission
  mutation or recreating the original claim is still prohibited.
- Applied receipt already present: use the historical receipt reader instead of
  invoking recovery again. This API does not advance an active writer lease.

The same single-owned-runtime/journal and protected-host assumptions as 94 apply.
Point-in-time inventory is not a global lock against trusted host root/Docker
administrators exposing unapproved bind aliases.

## Verification and evidence scope

Local SQLite/crypto tests require an expired old claim and fresh current signed
reference, reject changed source/marker/ack/fences/authority/history, refuse
missing or decoded native proof and preserve unknown metadata. Parser tests
separate old and new time windows; synthetic historical reader tests verify
current-key renewal preserves the original recovery observation. These fixtures
are not native permission evidence.

Disposable Linux quota acceptance extends the existing actual writer trial:

- Observe sealed and test-only partial permissions; refuse both without changing
  inode permissions/group/ctime. An actual stopped container referencing writable
  data also excludes observation. Only after owned removal may observation pass.
- After actual permission success and an injected receipt commit failure, restart
  the journal/runtime, wait for the original expiry and issue fresh signed
  authority. Recovery must preserve the original marker/native inode.
- Inject a second SQLite failure during recovery settlement. Require both records
  to stay unchanged, then retry read-only recovery and require atomic success.
  Namespace mode/group/inode/ctime must be unchanged across both attempts,
  demonstrating that recovery did not repeat permission mutations.
- Read the recovered receipt under current authority; reject another execution
  claim or recovery replay. Existing filtered quota, viewer and owned-resource
  cleanup gates remain required.

The parent requires native markers before emitting
`native_writer_recovery_partial_permission_refused`,
`native_data_writer_recovery_after_expiry_verified`,
`native_writer_recovery_without_permission_mutation` and
`native_writer_recovery_metadata_failure_retry_verified`.
Local full runner race passed (journal 116.228s, transport 1.689s; other packages
cache hits). Focused writer race passed in 20.822s, expanded recovery race in
8.551s and platform regression in 11.433s. Vet/default Linux staticcheck,
Linux amd64/arm64 compilation and all 14 Python probe tests passed. Native
results will be recorded after the functional run.
Actual execution remains restricted to the disposable supported Linux profile;
no production, credentials, Status, server beta, dependency or toolchain changes.

## Next dependency

Implement a distinct signed writable candidate/start and history contract that
consumes successful execution/recovery evidence. Do not relax the old sealed
candidate reader to accept writable state. Physical writer stop/revocation,
managed activation and normal workers, broker capabilities, SDK/shared shell,
reference apps and complete P00–P14 lifecycle acceptance remain incomplete.
