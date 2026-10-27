# Native candidate execution and durable receipts

This continues P06/P07 after the concrete native runtime in document 71. It is
an infrastructure implementation, not a production dispatcher or app activation.
No new table, column, side store or public SDK contract is introduced.

## Execution order

1. Admit the separately signed candidate command, reserve its container identity,
   and obtain the existing one-shot claim. The journal must already contain the
   exact applied prepare-data receipt for the same operation and generation.
2. `Store.ExecuteCandidate` holds the shared native execution gate through return.
   It derives every native identity and quota/root pin from that signed command
   and its unchanged data receipt. The supplied descriptor bytes are cloned;
   the concrete driver independently requires their exact signed release digest.
3. `CandidateRuntime.PrepareCandidate` performs read-only native inspections and
   returns an opaque handle holding mount pins. Its path-free snapshot must match
   the journal identities, platform, concrete root, namespace inode/device,
   project ID and quotas exactly.
4. Commit a versioned preflight inside the existing unknown command marker:
   immutable image ID, policy and mount hashes, root and namespace identity,
   project/limits and binding time. Native integer values use decimal strings.
   An existing preflight cannot be overwritten. There is no SQL transaction open
   during Docker/filesystem IO.
5. Recheck the signed authority, exact original admitted fence, pending-work
   barrier, resource reservation, applied data history and committed preflight.
   Invoke the concrete driver's one-shot create/start with the live native pins.
   The command deadline bounds native work. Expired/changed authority also refuses
   settlement; no app is activated during this candidate step.
6. Accept only the driver's opaque running observation. A JSON object, native ID,
   snapshot or parsed receipt cannot enter the settlement path as native success.
7. In one journal transaction, revalidate the same authority/data/preflight,
   replace the pending native-ID sentinel with the exact native container ID,
   record `running`, native observation time and resource revision 2, and change
   the command to applied with a typed receipt. Both updates roll back together.
   A resource stays desired-present: candidate process presence grants no writer.
8. Release native pins after the attempt. Runtime/provider ownership remains
   unchanged; no automatic container cleanup or restart follows an uncertain
   effect. Consumed claims cannot execute again, including after failed SQL.

## Metadata and refusal rules

`portal.runner.candidate-preflight/1-dev.1` and
`portal.runner.candidate-running-receipt/1-dev.1` are provisional internal formats.
The running receipt includes the original claim and bound preflight, control
revision, deployment/epoch, installation/operation/generation, data namespace and
format, release/payload digests, writer fence, native ID and observation/settlement
times. Time ordering requires claim <= preflight <= native observation <=
settlement < signed expiry. Inode/device values retain full uint64 precision.

`ParseCandidateRunningReceipt` checks canonical structure only: it rejects unknown
fields, numeric wire substitutions, trailing bytes, missing preflight, malformed
IDs/digests and invalid time ranges. Parsing never changes execution state,
authenticates a process, acknowledges a command or authorizes recovery.

Preflight refusal, native error, cancellation, authority/fence/data change,
commit failure and lost acknowledgement must not invent completion. After a
possible native effect, uncertainty remains for a future separately authorized
candidate reconciliation path. The prepare-data recovery path is not reused to
adopt containers. Concurrent copies still share one claim consumption flag.

## Verification and reproduction

Local checks:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 -race -count=1 ./internal/apprunner/...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 -race -count=1 ./cmd/app-runner
python3 -B -m unittest discover -s scripts -p project_quota_probe_test.py
```

The focused journal tests use synthetic snapshots only for persistence/refusal.
They cover changed identities/data/platform/image/mount evidence, authority and
fences, acknowledgements, failed preflight SQL, one binding only, opaque-result
forgery refusal, canonical receipt parsing and uint64 precision. They do not
claim real native execution success.

The existing opt-in disposable Linux quota workflow additionally runs
`TestNativeJournalCandidateExecution` after its journal remount/recovery and Portal
acceptance phases. It reuses the same shared journal/root and monotonic project
allocator, prepares two real data namespaces, and uses the public digest-pinned
inert pause fixture with no model requests or credentials. One candidate must
persist its actual native ID and running receipt atomically. A second candidate
must run physically while an injected SQL failure leaves its resource and command
unknown; a retry must refuse. Test-only independent Docker inspection checks the
full owner tuple before cleanup by exact native ID, with no force/volume removal.
The test selector must actually report PASS before the script emits:

- `native_candidate_journal_running_receipt: true`
- `native_candidate_journal_atomic_failure_unknown: true`

Native receipt acceptance is pending until the current source passes this
workflow. Prior document 71 native-runtime evidence cannot substitute for it.

## Remaining integration

A durable candidate receipt is historical process evidence, not application
readiness, writer access, broker admission, controller delivery/acknowledgement or
Portal install completion. Candidate receipt transport, current-authority read and
replay, unknown-effect reconciliation, readiness, active writer transition and
managed lifecycle remain. The generic automatic install dispatcher has not been
connected to this API. Execution readiness remains false. Production, beta
configuration, credentials, Status migration and SDK freeze remain outside this
change and require their existing gates.
