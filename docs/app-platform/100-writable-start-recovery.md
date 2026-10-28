# Observe an uncertain writable start without repeating it

This extends [99](99-writable-running-receipts.md). It addresses the interval in
which Docker has started the exact application process but the runner has not
committed its running resource/receipt. It does not activate the app, expose an
RPC route or resume a lost executable claim.

## Required behavior

When startup loses its result, the command remains `unknown`, with its original
bound native preflight and a pending revision-1 container record. The process may
already exist. Repeating create/start could duplicate work or write data twice.
Recovery therefore obtains new signed permission to **observe**, independently
verifies the original process, and commits its observation. It never starts a
missing/stopped container or changes permissions, policy, mounts or IPC storage.

No new schema, database, daemon service or production configuration is added.
The initial generation/writer fence remains 1; later update and revocation
protocols must not reuse this path for writer takeover.

## Implementation order and source map

1. `journal/writable_candidate_recovery.go` owns the trusted recovery entry
   `ReconcileWritableCandidate`. It clones bounded inputs, acquires the Store
   execution gate, checks closing state and holds that gate through preparation,
   native observation and atomic settlement. Closing the owner waits for it.
2. Its private preparation opens `beginWritableCandidateView` in the recovery-only
   unknown view. Normal reservation/claim/execute callers retain the strict live
   revision/expiry match. The recovery view permits original historical claim
   identity only with non-null verified preflight. It still authenticates the
   current signature, deployment/epoch, logical effect, original source receipts,
   acknowledgement state, current fences and exact pending resource/history.
3. Require the new signed envelope's issuance to be later than original claim
   expiry, and no later than now. An unexpired claim, stale envelope, unbound
   marker, guessed process ID or new logical effect cannot authorize observation.
   The old claim window is never extended or rewritten.
4. Reparse the exact descriptor/digest/platform/schema. Derive the request and
   expected native snapshot from verified facts and original bound evidence;
   callers cannot supply a replacement image, native ID, policy or mount hash.
5. `driver/writable_candidate_recovery_linux.go` implements
   `ObserveWritableCandidate` with a five-second native limit inside the current
   signed deadline. Require supported rootful Linux/architecture, current daemon
   compatibility and the same protected quota root. Refuse a live process-local
   launch handle for the namespace; recovery cannot race/resume it.
6. Inspect the immutable image and rebuild the same closed writable plan. The
   shared `pinCandidatePreflight` helper pins existing directories, validates
   protected ancestors, exact root/data inodes, writable data permissions, actual
   project quota and bounded trusted HTTP tmpfs. It computes the same path-free
   mount metadata hash as initial execution. Require exact equality with original
   persisted native preflight. This helper provisions nothing.
7. Enumerate all running **and stopped** Docker containers with a bounded decoder.
   Require exactly one distinct native ID referring to any of the app's private
   data/HTTP/broker mounts. Duplicate IDs, multiple references (including parent,
   child and symlink aliases), missing/unresolvable sources and malformed inventory
   refuse observation. A single match is only a candidate for full inspection.
8. Inspect that candidate against the complete closed plan: exact name/native
   ID/image, full ownership/claim/source labels, user, network, quota/limits,
   syscall policy, readonly root, environment and exact mount identities/options.
   Require running and not paused/restarting/dead. Repeat inventory, exact native
   inspection, current data and held/fresh directory validation before returning.
   Missing, created, stopped, substituted or ambiguous processes remain unknown.
9. Return `WritableCandidateRecoveryObservation`, an opaque type distinct from
   live-start evidence. JSON serialization/decoding is refused. A public snapshot
   can be compared/read but cannot be passed as native settlement authority.
10. Under the same Store gate, reopen the current transaction and compare exact
    original marker/source/resource/timestamps and signed recovery authority.
    Recheck the opaque snapshot against original expected preflight and request;
    the observation time must fall after this recovery preparation and before
    settlement. Atomically advance pending resource to actual native ID/running/
    revision 2 and command unknown to applied. Commit only under current authority.

No SQL transaction spans Docker/filesystem IO. The native path only uses daemon
inspection endpoints and filesystem reads/pins; it never invokes provisioning,
chmod, container create/start/stop/remove or executable launch registration.

## Receipt windows and historical reading

The existing receipt gains optional recovery fields. The normal format and bytes
remain unchanged when these fields are absent. A recovered result uses
`portal.runner.reconciled-writable-candidate-running-receipt/1-dev.1` and records:

- The complete original claim and its bound preflight, byte-for-byte unchanged.
- The actual freshly observed native ID, observation time and settlement time.
- SHA256 of the current signed recovery envelope, its control revision and its
  original issuance/expiry window.

Original claim must satisfy `claim <= preflight bind < original expiry`, with
its original window at most 30 seconds. Recovery must satisfy
`original expiry < new issuance <= observation <= settlement < new expiry`, with
its new window at most 30 seconds. Recovery revision cannot precede original
claim revision or exceed current reader authority. Normal-format receipts reject
all recovery fields and all settlements outside the original window.

`ReadWritableCandidateRunningReceipt` validates both formats under current signed
authority, with independently checked source/history/resource identity, native
ID, revision and timestamps. It may retrieve committed history after lost
acknowledgement. It proves historical process observation, not present health,
readiness, a registered backend, writer revocation or activation.

## Failure rules

| Situation | Required result |
| --- | --- |
| Original preflight never committed | Refuse recovery; do not create IPC/process or guess policy |
| Original signed execution window remains live | Refuse recovery; never compete with a live owner |
| Original process absent/stopped or exact policy differs | Refuse observation, retain unknown/pending |
| Another container references private mounts | Refuse observation even if expected process also exists |
| Current authority/source/resource changes before or after IO | Refuse settlement; do not repair or retry start |
| Resource update succeeds but receipt update fails | Roll back both updates; retain unknown/pending |
| Read-only recovery commit failed | A later fresh observation may retry; no physical execution is repeated |
| Pair committed but reply lost | Current historical reader returns the original recorded result |

Inventory checks are bounded point-in-time observations under trusted admitted
lifecycle exclusion. They are not a global lock against a host administrator
changing Docker or exposing new private-path aliases during inspection.

## Verification

SQLite/crypto tests validate expired bound claim selection, original marker
preservation, refusal to resurrect execution, missing opaque proof, current
trust/source/fence/ack/resource/policy/image/time drift, strict normal/recovered
receipt windows and historical reads. Synthetic rows prove metadata validation
only; they never manufacture successful native authority.

Linux inventory tests use a GET-only fake daemon and actual owned temporary paths
to check unique selection, stopped-container inclusion, duplicate IDs, additional
data/HTTP references, parent/symlink aliases, missing sources and invalid IDs.
The complete native inspection policy remains covered by the existing driver
policy/refusal tests and disposable actual-container acceptance.

The actual XFS/Docker journal fixture extends its post-start commit-failure case:
close/reopen both Store and native Runtime objects without stopping the process,
wait for original expiry, sign a fresh envelope, inject recovery metadata failure
and verify atomic rollback, then retry observation. Require the same native ID,
creation/start timestamps and restart count before and after both observations,
unchanged original claim/preflight, valid recovered historical read, and refusal
to reclaim/reexecute/reconcile applied state. Parent probe requires all markers:
`native_writable_start_recovery_after_expiry_verified`,
`native_writable_recovery_without_process_restart`,
`native_writable_recovery_metadata_failure_retry_verified`.

Expanded writable journal race passed in 26.846s, writable driver race in 1.649s
and platform regression in 16.524s. Linux amd64/arm64 driver/journal test builds,
vet, Linux staticcheck and all 14 Python probe tests passed. An existing capacity
test reused a short signed envelope after a full 65,535-row ledger scan; its
independent next request now refreshes test authority. Production expiry checks
are unchanged. The full runner race rerun and actual native acceptance are pending.

Owner-object reopen in this fixture
is not a whole daemon kill/relaunch, host reboot or full app installation test.

## Next dependencies

Writable readiness, registration/handshake/current-viewer delivery and managed
activation/worker integration still need to consume these separate source facts.
Physical stop/fencing, broker capability enforcement, SDK/shared shell, neutral
reference apps, updates/uninstall/restore and complete P00–P14 release acceptance
remain incomplete. Production deployment, real credentials, Status migration,
public SDK freeze and beta/version configuration remain outside this change.
