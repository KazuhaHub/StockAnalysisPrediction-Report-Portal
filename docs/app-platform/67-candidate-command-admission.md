# Signed candidate intent and journal admission

This adds the authenticated start-candidate intent required between the
[closed container adapter](66-candidate-container-policy.md) and future managed
execution claims. It does not connect a production service endpoint or grant a
container execution/write lease. No database schema or physical storage layout
changes; the approved journal already defines stage=start and kind=start-candidate.

## Source map

| File | Responsibility |
| --- | --- |
| internal/apprunner/control/candidate.go | Strict candidate intent, separate signature/effect framing and time/authority checks |
| internal/apprunner/control/candidate_test.go | Stable framing vector, immutable input binding and signed invalid/wire cases |
| internal/apprunner/control/candidate_profile_test.go | Signed sandbox profile agrees with the runtime adapter |
| internal/apprunner/journal/candidate_commands.go | Candidate verification projection and existing resource identity checks |
| internal/apprunner/journal/commands.go | Shared atomic admission, durable floors/replay and final verification |
| internal/apprunner/journal/candidate_commands_test.go | Persisted intent/reopen, takeover, changed inputs, resource conflicts and cancellation |

Existing prepare-data command bytes, signature/effect purposes and the 48-case
shared corpus remain unchanged. Both intent types use a private common journal
index only after their own complete signature verification. That projection is
not a new signed message and is never dispatched as a prepare-data request.

## Experimental wire contract

Format: `portal.runner.candidate-command/1-dev.1`.
Stage: `start`. Kind: `start-candidate`.

The envelope contains deployment/controller epoch and revision, installation,
operation, target generation, effect slot/ID, installation fence, logical payload
SHA-256 and issued/expiry milliseconds. Its payload contains only:

- resource UUID and data namespace UUID;
- exact reviewed release SHA-256;
- declared native platform, linux/amd64 or linux/arm64;
- positive data format and writer fence;
- the closed linux-docker-rootful-projectquota/v1-dev sandbox profile.

There is no host path, mount option, raw Docker request, private key, service
credential, arbitrary executable, image tag or network URL. The exact release
bytes bind backend image/resources; those bytes and their signed publisher trust
must still be independently checked at dispatch. The payload cannot substitute
another image or reinterpret an unsigned resource limit.

UUIDs are canonical lowercase and nonzero. All counters/times are canonical
nonnegative signed-int64-range decimal strings, positive where required. Effect
slots are 1..32 and data formats 1..1000000. JSON is UTF-8, at most 64 KiB, with
bounded depth/field count, duplicate/alias/unknown/trailing-value refusal. Arrays
are not valid candidate command fields. Missing/null required values refuse.

## Cryptographic identities

The Ed25519 signature input is the existing length-prefixed framing construction
with the distinct purpose `portal.app/runner-candidate-command/v1`. The exact raw
message bytes are signed. A valid prepare-data-domain signature cannot authorize
a candidate, and the prepare-data verifier refuses candidate wire messages.

The stable logical SHA-256 uses purpose
`portal.app/runner-candidate-effect/v1` and typed
`portal.runner.candidate-effect/1-dev.1` bytes. It binds deployment, epoch,
operation, installation, generation, stage, effect slot, kind and the entire
candidate payload. Transport control revision, installation fence and timing are
outside that stable identity so a renewed authenticated envelope can reconcile
one durable effect; they remain checked during signature verification/admission.

The fixed logical framing test vector is
`d3afb103abf8796bf6b800fe9ac587ca185b36c15a65747022712cf67174ba0b`.
Independent hash framing was used to check that value. Tests also mutate every
immutable field and require a changed digest, rather than relying solely on a
sign/verify round trip. This is development evidence, not a public SDK freeze or
completed cross-language publication corpus.

Verification uses an already trusted controller public key/fingerprint, deployment,
epoch and exact current control revision. The command lifetime is at most 30
seconds; future-issued, expired or inconsistent time windows refuse. Signing also
verifies the completed message against that authority before returning bytes.
Neither signing nor successful verification proves a current journal lease,
native quota, container ownership or active data-write authority.

## Durable admission

`Store.AdmitCandidateCommand` uses the same transaction/floor/replay mechanism as
prepare-data, with a candidate-specific verifier. It reads explicit controller
trust after acquiring journal write intent, verifies the full candidate, and
projects only index/floor fields plus a private resource ID. It never initializes
trust or accepts a caller-supplied verification key.

Admission checks current installation control/generation floors, namespace writer
floor, foreign namespace ownership and controller epoch. For an existing resource
ID it additionally checks container kind, installation, epoch, namespace, release
and generation. A new effect cannot reuse an existing resource ID, including an
owned historical row. An exact prior effect may be read as a replay when those
identities and durable floors agree. Unreconciled higher-fence takeover refuses;
the original admitted fence is immutable.

One installation cannot admit a second pending prepared/unknown command merely
because it has a different kind or stage. Effect ID, operation/stage/slot and full
logical digest conflicts refuse. Final signature/time verification runs again
before transaction commit. Cancellation or a failed transaction does not record
an admitted intent.

The returned state is historical prepared/applied/unknown journal state; it is
not a physical container receipt or a dispatch permit. Fresh admission creates
only a runner_commands intent, never a runner_resources/container row. It does not
reserve an absent resource ID globally across installations: the next execution
claim must reserve and fence that resource atomically before any native effect.
No replay executes a container merely because admission succeeded.

## Acceptance and remaining work

Tests cover fresh intent and reopen replay, domain separation, signature/authority
and lifetime refusal, changed resource/release/namespace identity, pending command
conflicts, durable resource floors, existing-resource reuse/rebinding, and canceled
contexts. Existing prepare-data corpus and admission regressions continue to pass.

The container predecessor c1ac06701b4ccf60dfd5ed0c33612d7d72a7fa48 passed the full
disposable daemon workflow 40407582383, including real stopped-container creation,
policy inspection and ownership-checked cleanup. It did not start an application.
This candidate protocol is separate from that native adapter acceptance.

Next, add durable candidate execution claims/resource reservations, root/socket/
quota pins and receipts, then integrate create/start/readiness/stop and automatic
backend registration. Recovery of prepared/unknown candidate intent must use the
complete signed command and fresh native observations; it cannot borrow the
prepare-data recovery path. Broker and active-writer lifecycle remain open.
Production enrollment, real credentials, Status migration, server beta settings,
SDK freeze and D5/D6 acceptance remain outside this change.

## Local verification record

The full runner/control/data/driver/journal/transport plus CLI race suite passed
(journal 64.403 seconds). Final focused candidate, existing command and durable
floor tests passed (control 1.444 seconds, journal 5.187 seconds); the added
cross-package profile guard then passed the full control race suite (1.360
seconds). Full local Go regression passed (application package 66.808 seconds),
as did vet, staticcheck and Linux amd64 journal test compilation. PostgreSQL and
native release gates remain distinct; no production or SDK acceptance is inferred
from these local checks.
