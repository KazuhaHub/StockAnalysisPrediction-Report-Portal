# Candidate receipt reads and replay dispatch

This continues P04/P05 after document 72. A caller can now retrieve the original
applied candidate receipt under a current signed request without recreating its
container. It does not introduce a production endpoint, candidate reconciliation,
writer admission, application readiness or activation.

## Source map

| Source | Responsibility |
| --- | --- |
| `journal/candidate_receipt.go` | Logical receipt binding and current-authority historical read |
| `journal/candidate_dispatch.go` | First-attempt versus applied-replay dispatch; unknown refusal |
| `journal/candidate_reservation.go` | Shared signed transaction preflight with an optional exact receipt |
| `journal/candidate_receipt_test.go` | Synthetic historical-row validation/refusal and restart tests |
| `journal/candidate_dispatch_test.go` | Invalid input/runtime/data and pending-operation refusal |
| `journal/candidate_native_linux_test.go` | Actual native first dispatch, journal restart, signed renewal/replay and unknown refusal |

Paths in the table are relative to `internal/apprunner/`.

## Historical receipt read

`ReadCandidateRunningReceipt(ctx, command, signature)` performs these checks in
one journal transaction before returning a copy of the canonical stored receipt:

1. Verify the complete candidate command against current explicit deployment,
   controller key, epoch and revision; require the envelope to be valid now.
2. Require the existing logical command to be applied, unacknowledged, at the
   exact original admitted fence and current resource floor, with no other
   prepared/unknown operation for this installation.
3. Revalidate the applied prepare-data receipt and volume resource for the same
   operation/generation, including retained root history. Require its digest to
   equal the one bound into the original candidate claim.
4. Require every command/receipt identity, release, data format, generation,
   writer fence and platform to match. Recompute the original ownership digest.
5. Reconstruct the preflight data evidence from that data receipt, preserving
   full native integer precision; compare root, project, device, namespace inode
   and quotas with the stored candidate preflight.
6. Require the exact native ID, ownership, release, namespace, epoch, generation,
   fences, desired-present/observed-running state and revision 2 in the resource
   row. Native observation/settlement timestamps must match the receipt exactly;
   resource creation must precede the bound preflight.
7. Recheck context, time, signature authority and owner closing around commit.
   Return the original receipt, never a rewritten observation timestamp.

A newly signed envelope may renew issuance/expiry while retrieving old historical
success. It cannot change the original control revision, ownership fence, release
or logical identity. The receipt's own claim/preflight/native/settlement times
must still satisfy its original expiry; the current envelope must independently
be valid at read time. `MatchesCommand` only compares metadata and is never
signature, OS peer, native process or freshness authentication.

This read performs no native inspection. An externally stopped process or changed
filesystem is not detected by historical reads alone. A later current native
observation/readiness stage must verify those facts before activation; receipt
age is not a running health guarantee.

## Dispatch behavior

`DispatchCandidate` is a trusted infrastructure API. It clones inputs and requires
an exact managed descriptor/release digest before candidate signature admission.
The descriptor determines native policy; callers cannot supply Docker options,
paths, a native ID or a success callback.

| Durable state | Behavior |
| --- | --- |
| No existing command / prepared | Admit the signed logical intent, require a configured concrete native runtime, reserve against applied data, acquire one claim, then run the concrete execution/receipt path |
| Applied | Revalidate and return the historical receipt; no native runtime is needed and no native call is performed |
| Unknown or any other state | Refuse; do not create, start, reclaim, adopt or repair |

Missing native configuration may leave signed intent prepared, but never creates
or claims a resource. Malformed/static/mismatched descriptor bytes, cancelled
input and invalid signatures do not admit work. A stale original fence also
refuses after admission. Concurrent first requests remain governed by the existing
one-shot claim: an in-flight request may return a reconciliation error; it must
not create a second native effect. Applied replay is available only while all
current signed-authority, data/history and pending-work checks still pass.

Unknown-effect inspection/reconciliation remains a separate missing operation.
This API deliberately makes that absence visible instead of treating uncertainty
as permission to restart a container. A future reconciler must bind exact native
ownership/policy/mount evidence, not names or caller-provided IDs.

## Verification

Synthetic fixtures directly seed historical journal rows only for read/refusal
checks. They do not mint opaque native observations or prove real execution.
Tests cover reopen and renewed envelopes, original observation preservation,
changed resource/authority/data state, timestamps, native ID, owner/revision,
acknowledgement, higher fences, expired/cancelled/bad signatures, altered
preflight evidence, invalid dispatch input and a new pending-operation barrier.

The disposable quota workflow's real native candidate fixture additionally:

1. Uses `DispatchCandidate` for a first actual digest-pinned inert process launch.
2. Closes and reopens the shared journal, renews the signed envelope, and reads
   the unchanged actual receipt/native ID.
3. Dispatches the applied request with no native runtime and requires byte-equal
   receipt metadata and the same independently inspected native ID.
4. After an actual native start and injected SQL failure, dispatches the unknown
   command and requires refusal with exactly the same single native ID.
5. Inspects full fixture ownership and cleans exact native IDs as before.

The probe emits the following only after that exact native test reports PASS:

- `native_candidate_receipt_restart_renewal_replay: true`
- `native_candidate_unknown_dispatch_no_restart: true`

Functional source `3845330aaf787949683f73520dd122286e71ca2f` passed quota
workflow [40458125760](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40458125760).
The completed enforcement log reports both new restart/unknown-dispatch keys,
the existing candidate running/atomic-failure keys and owned-resource cleanup as
true. `execution_ready` remains false. General CI and daemon checks are separate.

The local full runner/CLI race suite passed (journal 77.673 s), focused candidate
race checks passed (15.778 s), all 13 Python probe tests passed, and Linux vet,
repository-pinned staticcheck plus amd64/arm64 test compilation passed. No browser
or native screen automation is used.

## Remaining work

Candidate receipt OS-authenticated transport, Portal delivery and stage settlement,
unknown-effect reconciliation, readiness, data writer transition, broker admission,
automatic managed backend registration, complete lifecycle and reference app
acceptance remain. Execution readiness stays false. No storage layout, production
configuration, beta setting, real credential, Status deployment or SDK freeze is
changed by this phase.
