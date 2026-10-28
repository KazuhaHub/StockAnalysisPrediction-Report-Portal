# Writable candidate intent and completed-writer binding

This development slice follows [95](95-writer-observation-recovery.md). It defines
and durably admits the separately authenticated start intent that a writable
candidate execution must consume. It does not yet reserve a container, issue a
live execution handle, perform Docker IO or enable production installation.

## Why a separate command

The existing `CandidateCommand` and sealed data reader describe read-only
candidates. Accepting writable data through that protocol would silently change
its signed meaning and evidence requirements. Keep that verifier/reader strict.

The new `WritableCandidateCommand` binds an exact completed writer receipt and
future container identity. It carries no filesystem path, UID/GID/mode, command,
environment, URL, port, Docker flag or secret. Native execution must independently
verify the reviewed descriptor and concrete sandbox before creating a process.

## Wire and logical contract

| Field | Meaning |
| --- | --- |
| `format` | `portal.runner.writable-candidate-command/1-dev.1` |
| `effect_id` | Nonzero new start effect UUID; cannot alias source preparation, writer effect, namespace or future container resource |
| `payload_sha256` | Stable logical digest described below |
| `writer_raw` / `writer_signature` | Current separately signed `DataWriterCommand` reference, including its current signed preparation reference |
| `writer_receipt_sha256` | SHA-256 of the exact canonical completed writer receipt stored in the journal |
| `sandbox_profile` | Fixed `linux-docker-rootful-projectquota-writable/v1-dev` development candidate profile |

The outer Ed25519 signature purpose is
`portal.app/runner-writable-candidate/v1`. The existing bounded strict-object
parser rules apply: no duplicate/unknown keys, trailing JSON, invalid UTF-8 or
oversized command. Verify all three signature layers under current durable
controller trust; a valid outer signature cannot replace an invalid nested one.

Logical digest inputs are the versioned effect format, writer effect UUID,
writer logical payload hash, exact writer receipt hash and fixed writable
profile. The writer's logical identity already binds deployment/epoch,
installation/operation, initial generation, preparation identity/receipt,
namespace, future container UUID and platform. Current key/revision, transport
issuance/expiry and renewal fence stay outside stable logical identity; journal
source ownership and floor checks independently prohibit takeover.

The digest uses a separate `portal.app/runner-writable-candidate-effect/v1`
framing purpose. The test vector, independently reproduced with Python JSON,
big-endian framing and SHA-256, is
`e99174d74155ae5fd6f2fbeefbf6afc3c3e9b863a34999b07f35abd080b8fffe`.

`VerifiedWritableCandidateCommand.Index()` projects current verified identity
into existing indexing fields: stage `start`, effect slot 1, kind
`start-candidate`, and the writer's exact future container. This projection is
not a signed legacy command. `VerifyCandidate`, data preparation, writer mutation
and backend registration verifiers reject the new envelope. Copied getters do
not expose mutable buffers inside verified values.

## Admission sequence

1. Obtain an applied execution or recovered writer receipt. Keep its exact
   canonical bytes; do not rebuild a semantically similar object with new dates.
2. Renew the preparation and writer references under current controller trust,
   preserving stable logical identities. Hash the stored writer receipt and sign
   the new writable candidate envelope. Use the writer's already bound resource;
   do not allocate a different container identity in this request.
3. Call `Store.AdmitWritableCandidateCommand(ctx, raw, signature)`. The common
   write-intent transaction loads current enrollment and verifies the new
   signature domain before projecting any durable identity.
4. Require the original writer command to be applied, unacknowledged and exactly
   indexed, with its original admitted fence. Parse the canonical receipt and
   require its exact hash to match the signed outer reference. Check original
   writer creation/claim/settlement timestamps against stored command times.
   Writer creation cannot precede preparation completion.
5. Recheck original preparation receipt and volume using the preparation's
   original control revision/ownership. The volume must remain present at
   revision 3 with observation/settlement timestamps matching the writer receipt.
   Verify root history, project/native identity, current policy, exact release,
   source receipt binding and current logical writer reference. Both original
   execution and separately formatted recovery receipts are supported.
6. Permit only preparation, writer and this exact start intent in the
   installation's command history. Require no container under the future resource
   and no additional namespace-associated resource. This initial intent contract
   does not pre-authorize any later reservation/history shape.
7. Apply existing global resource/slot uniqueness, current generation/epoch,
   namespace ownership and installation/writer-fence floors. Persist only one
   `prepared` start command with `{}` receipt. Reverification precedes commit.
   A valid repeated request returns the same command; a different start UUID,
   changed receipt/release/resource or higher-fence takeover is refused.

No table, column, enum, migration, secondary database or persistent side store is
added. The approved runner command/resource layout remains unchanged.

## Current prepared-intent reader

`ReadWritableCandidateIntent` independently verifies current signed authority,
exact stored prepared command, empty receipt, no acknowledgement, original
command timestamps and current floors. Start creation cannot precede writer
completion. It repeats the complete source/history
validation under a write-intent transaction and rechecks cancellation/time/
signature around commit.

The returned view is path-free metadata: current intent/source/resource IDs,
writer receipt hash, release, platform and counters. It contains no readiness or
execution flag. Unknown/applied start rows are not reported as prepared; there is
no claim reconstruction or physical effect through this reader.

A current key/revision may read the same prepared intent while retaining the
original source preparation and writer evidence. A renewal changing the original
fence cannot take over a writer. Once this start intent exists, the older strict
pre-start writer reader refuses the additional history; callers must use the
specific later-stage contract. The old sealed candidate entry points remain
unchanged and cannot execute this new envelope.

Shared original preparation/volume checks were extracted into a private core.
Each consumer still independently enforces its exact allowed history: existing
writer APIs retain their original two-effect/no-container scope, while this new
intent explicitly permits its own third command only. The shared core is not a
public history bypass or execution API.

## Verification

Control tests cover the independent fixed digest vector, signature separation,
all nested signature layers, copied getters, identity aliases, writer receipt
binding, current-reference renewal, invalid profiles/fields and strict wire
parsing. SQLite tests cover idempotent prepared intent and owner reopen,
source/volume/time/ack/fence/key/history changes, wrong receipt hashes, duplicate
start effects and refusal through legacy claim paths. Synthetic historical rows
exercise metadata/crypto only; they do not prove native execution.

The existing disposable native writer fixture now invokes this contract twice:
once after actual permission execution/receipt settlement and once after actual
observation-only recovery. Each case first refuses a signed wrong receipt hash,
then admits/replays/reads the correct intent. It checks exact future identity,
unchanged native writer receipt bytes, no container reservation and continued
legacy claim/pre-start-reader refusal. The parent requires
`NATIVE_WRITABLE_CANDIDATE_INTENT_ACTUAL_SOURCES_VERIFIED` before emitting
`native_writable_candidate_intent_actual_sources_verified=true`.

Local full control/data/driver/journal/transport race passed (control 2.307s,
journal 116.225s, transport 1.601s; other packages cache hits). Focused writer/
writable-contract race passed in 1.602/24.140s and platform regression in 10.688s.
Vet/default Linux staticcheck, Linux amd64/arm64 compilation and all 14 Python
probe tests passed. Additional chronology-drift race passed in 5.326s with
Linux staticcheck clean. Native evidence will be recorded after the functional run. Passing
this case proves actual-source intent admission, not a writable process start,
readiness, backend pairing, active writer lease or complete application install.

## Required next implementation

Add the separately gated reservation/one-shot claim and concrete writable
candidate driver. Define the exact extended command/resource history required
for preflight, start, uncertain recovery and later receipt reads. Bind native
image, mount/profile, data identity and the writer receipt before Docker IO.
Recheck current source/fences immediately before effect and before settlement;
never retry an unknown create/start or manufacture readiness from durable intent.

Then connect actual readiness/registration and managed activation/normal worker
flows. Physical stop/fencing, broker, SDK/shared shell/reference apps,
updates/uninstall/restore and complete P00–P14 acceptance remain outstanding.
No production, real credentials, Status adoption, server beta configuration,
dependency/toolchain change or public SDK freeze is included.
