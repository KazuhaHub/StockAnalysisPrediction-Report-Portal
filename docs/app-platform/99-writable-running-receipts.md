# Bind writable preflight and settle actual running evidence

This slice connects the one-shot claims in [97](97-writable-candidate-reservation-and-claims.md)
to the concrete native adapter in [98](98-writable-native-driver.md). It persists
actual preflight before create/start and atomically settles opaque process
results. It does not enable public dispatch, readiness, registration or activation.

## Trusted execution order

1. Admit, reserve and claim the exact completed-writer-bound command using 96/97.
2. Call `ExecuteWritableCandidate(ctx, claim, native, descriptor)` with the exact
   reviewed descriptor and configured concrete runtime. No app callback, native
   ID, parsed receipt or browser boolean can supply process success.
3. Enter `RunWritableCandidateOnce` under its owner execution gate and original
   signed deadline. Reread current authority, source, pending resource and claim.
   Parse/verify descriptor digest, backend, declared platform and data schema.
4. Derive the native request from current verified facts, not a caller-edited plan
   copy. Bind exact effect/claim, ownership, source receipt hashes, candidate/data
   identity and configured writable profile. Provision only owned bounded IPC,
   then obtain the concrete driver's live opaque writable launch/pins.
5. `bindWritableCandidatePreflight` accepts only that live typed native launch.
   Require exact current binding/identity/allocation, original writer image ID,
   platform and current/source seccomp identity. Persist its path-free preflight
   and bound time in the original unknown command before any Docker create/start.
6. Reopen current signed transaction immediately before native IO. Recheck the
   durable preflight against both the process-local bound proof and live native
   handle. Actual driver start also repeats quota/pin/reference/policy checks.
7. Capture the concrete opaque running observation, close native pins and finish
   the guarded callback. Postflight rechecks current authority/source/resource
   and original immutable claim fields. Only the private typed binder may extend
   the marker; ordinary callback success still cannot create applied state.
8. Reacquire the owner gate for atomic settlement. Verify current signature,
   original deadline, exact bound marker and concrete opaque process result again.
   In one transaction, advance pending resource to actual native ID/running/revision
   2 and the command unknown->applied with its typed running receipt.
9. Reverify authority/time before commit and before returning. Errors after possible
   effect remain uncertain; no automatic retry, adoption, repair or deletion.

No SQL transaction spans native IO. Close cannot release journal ownership while
callback or settlement is executing. No schema, column, enum, migration or
additional persistent store is introduced.

## Durable preflight extension

The existing claim format gains optional `preflight` and `preflight_bound_ms`.
Unbound marker bytes remain compatible because both fields are omitted.

Preflight format `portal.runner.writable-candidate-preflight/1-dev.1` contains
strict native image/platform/full-policy/mount hashes, root filesystem UUID/inode,
project/device/namespace inode, hard limits and seccomp hash. Numeric physical
values are canonical decimal strings. It is metadata, not an executable handle.

Binding must fall after original claim and before its expiry. While unknown,
command update time equals claim time when unbound and bound time when bound.
The pending resource remains revision 1 without native observation. A plain
snapshot, decoded marker or callback success cannot bind native preflight.

The postflight guard allows precisely this internally verified extension; all
immutable claim fields, original source evidence, pending resource and original
intent creation time remain unchanged. No renewed key/reference can resurrect
an unknown executable claim. Native handles/copies retain their existing
one-shot and owner/refusal rules.

## Actual running receipt and atomic pair

`WritableCandidateRunningReceipt` format is
`portal.runner.writable-candidate-running-receipt/1-dev.1`. It records the original
complete claim/preflight, actual native container ID, native observation time
and settlement time. Observation must follow binding; settlement must follow
observation and remain inside the original signed claim window.

Only `driver.WritableCandidateRunningObservation` may enter settlement. Its
identity, binding, allocation, image/profile/seccomp and preflight hashes must
match current verified facts and the bound marker. Neither its public snapshot
nor parsed JSON can substitute for that opaque type.

The transaction changes both records together:

| Record | Before | After |
| --- | --- | --- |
| Container resource | Exact writable pending name, unknown, revision 1, NULL observation | Actual native ID, running, revision 2, native observation time |
| Start command | Unknown with exact bound preflight marker | Applied with canonical typed running receipt |

Original resource identity/ownership/generation/fences/creation time, preparation
and writer receipt, volume revision 3 and all original source timestamps stay
unchanged. If the command update fails after resource update, rollback must undo
both. A physical container may already exist while both records remain unknown/
pending; this is a required uncertain state, not a reason to repeat create/start.

## Current historical reader

`ReadWritableCandidateRunningReceipt` independently authenticates a current signed
reference and the exact applied command, original source, current floors and
strict stage-specific history. It validates the complete receipt against the
actual recorded resource, original ownership and matching timestamps/revision.
Only that independently validated owned running resource extends namespace
history. Pending/reservation readers remain strict and reject applied state.

Current key/revision/transport renewal can retrieve the original historical
receipt without rewriting its original claim revision, times or native identity.
The historical claim revision must fall between writer evidence revision and
current signed authority. Old claim expiry is not replaced by the new reference's
expiry. Changed source/acknowledgement/fence/resource/policy/image/native/times
refuse the read. This reader performs no native IO and proves no current health,
active writer lease, registration or application activation.

## Failure and recovery boundary

- Failure before durable preflight commit must not create a process. Owned IPC
  provisioning may already exist; no resource is guessed or repaired.
- Failure after possible create/start returns uncertainty and no successful
  running receipt. Preserve exact bound intent and pending metadata.
- Lost acknowledgement after a committed pair can be resolved by the current
  historical reader, including after owner reopen and reference renewal.
- Physical start with no committed pair remains unknown. Observation-only native
  start reconciliation is still a separate unfinished protocol. Do not reset the
  command, reconstruct a claim, retry start or infer readiness from Docker labels.

## Verification

Local real SQLite/crypto tests retain one-shot/deadline/drift/Close checks and add
strict process parser/history tests, current-key renewal with original claim
preservation, resource/native/revision/time/ack/source/policy/image drift refusal,
and rejection of zero native launch/process evidence. Synthetic applied rows test
historical metadata validation only, never native authority or successful effect.

The disposable native workflow adds three cases against the same actual XFS root
and shared journal:

1. Prepare and grant actual data, admit/reserve/claim, bind real native preflight,
   create/start the actual typed writable process and settle. Independently inspect
   exact native name, ID, image and complete claim/owner labels; verify atomic
   resource/receipt state, historical read and owner reopen/current renewal.
2. Inject a command-update failure during settlement after a real process starts.
   Require the exact injected failure, real owned native identity and durable
   preflight, but unknown command and pending revision-1 resource without a
   successful read/returned receipt. Refuse claim/start replay.
3. Inject preflight-update failure. Require the exact injected failure, unbound
   unknown marker, unchanged pending resource and no native container. This proves
   commit precedes the first create/start effect.

All created processes and private IPC are cleaned only after independent exact
owned-fixture validation; no public lifecycle/deletion route is added. Parent
requires the native markers before emitting
`native_writable_journal_process_settlement_verified`,
`native_writable_process_commit_failure_preserves_unknown` and
`native_writable_preflight_commit_precedes_process`.

Local full runner race passed (journal 123.129s, transport 1.589s; other runner
packages cached), expanded writable race passed in 21.627s and platform regression
passed in 8.321s. Linux amd64/arm64 journal test builds, vet, Linux staticcheck
and all 14 Python probe tests passed.

Functional source `deaa01f19666e769dfba983d48bd956b5410d9d3` passed quota workflow
40761567921/job 123074574861 and daemon workflow 40761567968/job 123074574054.
The quota report requires and records all three new writable process flags as
true, alongside original source claims, concrete driver execution, filtered
writer/quota, current viewer and owned-resource cleanup evidence. Linux
control/appplatform/transport/driver race passed in 1.693/1.570/5.304/2.978s.
The daemon's full runner journal race passed in 184.912s, followed by actual
container policy/IPC, distinct identity and root endpoint acceptance.

General workflow 40761567937 passed all three app race shards, all web test
shards, web lint/typecheck/build, Docker image/smoke and workflow lint checks.
Go job 123074574375 passed build/vet/staticcheck, then failed the existing
`golang.org/x/net@v0.60.0` vulnerability gate (GO-2026-6866 and GO-2026-6860);
its subsequent ordinary test step did not execute. This dependency remains
unchanged; the separate runner/app race results above do not make that gate pass.

Reopening Store objects is not full daemon crash/relaunch acceptance. Production, real
credentials, Status adoption, beta configuration, dependencies/toolchain and
public SDK remain unchanged.

## Next dependency

Implement observation-only reconciliation for unknown native starts, then actual
writable readiness, automatic registration/handshake and managed activation/worker
integration. Physical stop/fencing, broker, SDK/shared shell/reference apps,
updates/uninstall/restore and full P00–P14 release acceptance remain incomplete.
