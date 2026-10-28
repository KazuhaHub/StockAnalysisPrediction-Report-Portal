# Initial writer intent, durable claim and one-shot guard

## What this step supplies

[92](92-data-write-access.md) established the native permission transition and
actual filtered app/XFS trial. This step adds the separately authenticated intent
and durable one-shot attempt boundary needed before that transition can be used
by a managed installer. It does not wire an automatic writer, writable container,
broker capability or managed activation.

The implementation uses the approved shared journal, without schema, baseline,
user-version, enum, database-count or side-store changes:

| Record | Representation |
| --- | --- |
| Prepared data source | Existing applied `prepare_data/1`, kind `prepare-data` |
| Initial writer intent | Existing `runner_commands`, `prepare_data/2`, kind `activate` |
| Uncertain writer claim | Same row, state `unknown`, versioned claim in `receipt_json` |
| Namespace allocation | Existing volume resource, still revision 2 |
| Writer execution receipt | Not supplied by this step; callback return is insufficient |

Here `activate` names preparation of write access, not activation of an app route.
Backend registration remains a different signed domain and uses `activate/1`.
The stage/slot, independent logical digest and signature purpose keep the two
intents separate. Ordinary prepare-data, candidate and registration parsers must
refuse the writer wire format.

## Signed contract

`control.DataWriterCommand` has format
`portal.runner.data-writer-command/1-dev.1` and a dedicated Ed25519 signature
purpose, `portal.app/runner-data-writer/v1`.

It includes only an effect UUID, logical SHA-256, separately signed renewable
prepare-data reference, exact data-receipt SHA-256, future container resource UUID,
platform and fixed writer sandbox profile. No path, ownership/mode, Docker option,
URL, executable or secret can be supplied. The profile is
`linux-docker-rootful-projectquota-writer/v1-dev`; this is a development contract,
not a frozen public SDK or an enabled runtime path.

The first contract accepts source generation 1, source effect slot 1 and writer
fence 1. Namespace and effect/resource identities must be valid nonzero UUIDs;
namespace/resource and source/writer effect aliases are refused. Supported
platforms remain Linux amd64/arm64. Later takeover/update/revocation requires an
explicit protocol; increasing a counter is not permission to skip that work.

The logical digest binds deployment, epoch, installation, operation, generation,
source effect/logical digest, exact prepared-data receipt, future resource,
platform and sandbox profile. Renewable envelope timing/key revision and control
fence do not change this identity; durable source/floor checks still reject an
unapproved fence takeover. The independently reproduced fixed vector in the
control tests is
`9317625db31c65f21081e4287512d0c66bf82c6ffc78e8306986c859b95bd308`.

`VerifyDataWriter` verifies both the nested source under the **current** durable
authority and the independently signed outer writer command. Strict bounded JSON
rejects aliases, duplicate/unknown fields, trailing content and oversized input.
Verified getters clone byte slices. No retired-key fallback is introduced.

## Admission transaction

`Store.AdmitDataWriterCommand(ctx, raw, signature)` acquires the existing journal
write intent, loads current controller trust and verifies the complete command.
Before inserting or replaying it, require:

1. The exact source command is applied under its original fence, with a canonical
   receipt, no acknowledgement attempt and the exact signed receipt hash.
2. The receipt matches the source's immutable ownership tuple. A currently signed
   renewed reference may use a newer key revision; validate original ownership
   with the revision recorded in the source receipt, without rewriting it. A
   reference revision older than the source receipt is refused.
3. Exactly matching present volume/resource history at revision 2, observed and
   updated timestamps equal to the receipt's observation, valid project identity,
   and the existing physical-root history binding.
4. No other command history for that initial installation except the source and
   this writer effect. No resource using the proposed future container UUID and
   no other namespace-associated resource, including a stopped container.
5. Existing generation/control/writer floors, foreign-namespace checks and
   durable effect/slot uniqueness all pass. Final signature/expiry checks pass
   before commit.

This is a deliberately initial pre-start contract. Its journal checks cannot
prove that an unrecorded Docker process or old file descriptor is absent. The
physical adapter must independently verify that boundary. It is also not a
historical read API for an already running writer generation.

An exact replay reports stored intent state; it never becomes an execution token
or completion receipt. A new effect at the same slot, a rebound resource/receipt,
changed source history or a higher-fence reference is refused.

## Claim and execution guard

`Store.ClaimDataWriter` requires that exact prepared row with receipt `{}`. It
repeats current trust/source/floor/acknowledgement checks, generates a fresh claim
UUID and commits `prepared -> unknown` **before** returning a live handle. The
marker binds effect/payload, data receipt, future resource, original admitted
fence, claim time and signed expiry. Commit/cancellation ambiguity never proves
that no claim was written.

`DataWriterClaim` is a process-local handle owned by one Store. Copies share one
atomic consumption state. JSON marshal fails; unmarshal clears the handle and
fails. Unknown work cannot be reclaimed or reconstructed after restart. Renewing
an envelope may observe stored unknown intent, but does not create another
executable attempt.

`RunDataWriterOnce(ctx, claim, callback)` holds the existing owner-wide synchronous
execution gate. It rechecks the exact live marker/current source and authority,
then commits its read transaction before invoking anything outside SQL. It
derives the callback deadline from the signed expiry and consumes the handle
once. The trusted callback receives copied public command/source metadata. It
must verify actual native writer exclusion before any physical mutation, must
not close/reenter the Store or settle its row, and must not return while work is
still live. An app or JSON request can never supply this callback.

After invocation, repeat the durable authority/source/marker checks and reject
clock regression. An error or cancellation after invocation returns
`DataWriterAttempt{Invoked:true}`: a physical effect remains possible and must
not be retried automatically. Even callback success leaves the row unknown and
the volume at revision 2. A future native adapter must separately settle opaque
OS evidence under fresh current authority. Close waits for the synchronous
invocation to finish before releasing journal ownership.

## Tests and evidence boundary

Control tests cover the fixed logical vector, purpose/domain separation,
receipt/resource binding, copied getters, signed invalid fields, renewal,
generation/writer-fence restrictions and strict wire parsing.

Real SQLite tests use explicitly synthetic native-source rows to test admission,
slot uniqueness, altered state/ack/fence/owner/source refusal, recorded-container
conflicts, concurrent handle copies, cross-Store rejection, restart, current-key
renewal without original-ownership rewrite, pre/during-invocation changes,
cancellation, actual signed deadline expiry and Close waiting. These rows are
not native permission evidence.

The disposable quota workflow also exercises this contract using the actual XFS
receipt from its existing successful concurrent data preparation. After the
existing recovery/count assertions, it admits/claims the writer and runs a
read-only callback that independently inspects the still-sealed namespace.
Repeated claims/invocations are refused; successful callback return must leave
unknown intent and unchanged physical allocation/permissions. The parent requires
`NATIVE_DATA_WRITER_INTENT_VERIFIED` before emitting
`native_writer_intent_and_claim_verified=true`. No physical writer permission is
granted by this acceptance case.

Local control/journal/transport race passed in 1.880/88.397/1.318s. Expanded focused
writer race passed in 10.621s (control cache hit). Linux amd64/arm64 journal
compilation, vet/default Linux staticcheck and all 14 Python probe tests passed.
Fresh native workflow evidence remains pending.

## Next implementation dependency

Add the native adapter that rejects every actual old namespace/container
reference, checks exact root/allocation/image/policy pins, invokes the permission
primitive once and settles independent opaque evidence. Then implement current
writer receipt reads/reconciliation, the writable candidate profile and physical
fence/revocation lifecycle. Broker authority, automatic managed activation,
normal worker scheduling, SDK/reference apps and complete P00–P14 acceptance
remain outstanding. No production, credentials, Status adoption, dependency/
toolchain or server beta change is included.
