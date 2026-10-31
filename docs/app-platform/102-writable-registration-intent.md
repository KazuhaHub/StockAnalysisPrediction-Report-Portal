# Bind writable backend registration intent to exact running evidence

This follows [101](101-writable-readiness.md). The old registration command
verifies a sealed/readonly candidate reference; a writable start must not bypass
that verifier or pretend to be its old command. This slice adds a separate signed
writable registration request and a current prepared-intent view. Protected file
delivery, uncertain file observation, handshake and activation are still separate
unfinished dependencies. No private registration material is persisted here.

## What this step does

A trusted controller can request registration only for an already applied,
independently validated writable process receipt. The request binds the exact
receipt bytes and intended private registration document by SHA256. The runner
stores one prepared intent, returns public metadata, and permits safe replay of
that same prepared identity. It creates no execution handle or registration file.

Admission is historical-source validation, not fresh native health. A stopped
process whose exact historical running receipt remains valid may still have an
intent; future delivery must independently verify current readiness and native
policy before touching the protected file. An unknown start has no completed
source and cannot admit registration, even if a process is physically running.

## Source and execution map

1. `control/writable_backend_registration.go` defines
   `WritableBackendRegistrationCommand`. Fields are format, effect ID, logical
   payload hash, current nested writable reference/signature, exact running
   receipt hash and private document hash. There is no URL, path, environment,
   permission option, key or private document field.
2. Parse a bounded UTF-8 object (at most the existing 65,536-byte command bound),
   rejecting duplicate/unknown fields, malformed values and trailing objects.
   Verify every nested preparation, writer and writable-start reference against
   the same current authority/window, then the new outer signature purpose
   `portal.app/runner-writable-backend-registration/v1`.
3. Logical effect digest uses its own framed purpose and exact start effect ID,
   logical start payload hash, running receipt hash and document hash. Reissuing
   current transport windows/key/revision does not change that logical identity.
   Reject effect IDs aliasing preparation/writer/start/resource/namespace IDs.
   The independently computed fixture vector is
   `310f8af108a4c51907b1d30a319069ca8d6d75be95c4be2d5d9cbc40bee41aa8`.
4. Getters clone nested bytes/signatures. A verified getter cannot mutate another
   verified reference. Old registration, start, writer and preparation verifiers
   remain separate; a signature from another purpose cannot authorize this step.
5. `ValidateWritableBackendRegistrationDocument` checks private bytes separately
   in memory. Reuse the existing document validator after the new verifier's
   format guard: exact document SHA, descriptor/backend/release/app ID, deployment,
   epoch, installation, generation 1 and initial policy revision 1. Return the
   existing closeable registration object; the caller closes/clears private bytes.
   A parsed document proves no physical delivery or handshake.
6. `journal/writable_backend_registration_intent.go` projects this verified command
   into the existing command ledger at stage `activate`, slot 3, kind `activate`.
   Preparation is `prepare_data/1`, writer is `prepare_data/2`, start is `start/1`;
   the new slot does not collide with their effects. The projection is an index,
   not a signed legacy command or executable permit. No schema changes are made.
7. Shared admission validates the new typed registration source before the
   existing logical replay, installation exclusion and durable floor checks. It
   may target the already owned container only after full independent source
   validation; generic start admission still rejects occupied resource identity.
8. The shared transaction-local `loadWritableCandidateFacts` preserves every
   previous start/reservation/running validation. Normal callers pass no
   registration scope and retain the original strict three-command history.
   Registration passes only a separately verified registration wrapper whose
   complete current nested index must match the exact source reference.
9. The registration history scope allows exactly its own absent/new or matching
   prepared row, then excludes only that exact effect from the conflict count.
   Existing row must match every logical index field, admitted fence, prepared
   state, empty receipt, null acknowledgement, creation/update equality and
   bounded times. Unknown/applied registration states are deliberately refused
   until their dedicated physical claim/receipt protocols exist.
10. Independently authenticate and validate applied start state, current floor,
    canonical normal/recovered receipt, exact writer/data source history and
    volume revision 3, exact running native resource/revision 2 and ownership/
    fences/timestamps. Require SHA256 of actual stored running receipt bytes to
    equal the signed request. Existing registration creation must not precede
    source settlement. Any other command/resource history still refuses.
11. Common admission stores only the usual index/hash and `{}` receipt. Same
    logical prepared intent replays; changed running/document hash, another
    registration effect, slot collision, current source drift or stale authority
    cannot silently reuse or replace it. Nothing is delivered or acknowledged.
12. `ReadWritableBackendRegistrationIntent` reopens current write-intent trust and
    signatures, performs the same full source/history/own-row checks and current
    expiry/closing checks before and after transaction completion. It returns
    public effect/payload/source/document hashes and exact container/installation/
    operation/namespace/release/generation/fence identity. No private bytes or
    native executable authority are returned.

All authority/source/index validation happens in the same journal transaction;
there is no native IO or SQL transaction held across filesystem/network work.
The existing readonly registration document validator now calls the same private
pure document helper after its own original format guard. Its signature domain
and source semantics remain unchanged.

## Why older readers stay strict

After registration is admitted, plain pre-registration start/readiness readers
see a fourth command and refuse. They cannot simply ignore arbitrary later work.
The new current registration view authenticates its own prepared row before
allowing exactly that history extension. Future delivery/readiness/handshake
consumers must likewise independently validate their own exact lifecycle scope.
Do not relax all old readers or pass a caller-chosen effect ID to a SQL filter.

## Verification

Protocol tests cover the independent logical vector, exact receipt/document
binding, separate signature domains, aliases, malformed/duplicate/unknown fields,
nested signature failure, cloned getters and logical renewal. Private synthetic
material validation checks exact descriptor/document identity and rejects altered
bytes or a zero verified request. No production credential is used.

SQLite/crypto tests cover admission/replay, exact metadata, unchanged resource
revisions, no secret/delivery record, owner reopen and fresh current reference.
Current-key rotation must preserve original source and reject the old key.
Drift cases include authority, source/writer/start acknowledgements, native ID,
volume/container revision, own payload/slot/state/ack/times, chronology, extra
history and wrong receipt hash. Unknown starts must refuse before any new row is
inserted. A synthetic historical fixture tests metadata only, never native proof.

Disposable native acceptance extends both actual original and reconciled process
cases after their existing readiness checks. Build/validate synthetic registration
material in disposable memory, admit/replay intent bound to actual running
receipt bytes, read exact native/source/document metadata, reject another receipt
hash and require the new row to remain prepared with `{}`/null acknowledgement.
The normal process has been deliberately stopped by the prior readiness refusal
case; this proves intent does not incorrectly claim fresh health. No registration
file or native effect is produced by this intent test. The parent requires
`native_writable_registration_intent_actual_sources_verified`, alongside all
existing actual writer/start/recovery/readiness and owned cleanup checks.

Focused registration/legacy registration journal race passed in 12.795s and the
expanded current-key registration test run in 8.991s. Platform regression passed
in 8.253s. Linux amd64/arm64 journal builds, vet, Linux staticcheck and all 14
Python probe tests passed. Full local runner race passed (control 1.827s,
journal 140.336s, transport 1.368s; data/driver cached).

Functional source `c88ea3d0f675491014bec06e09d032524137ab43` passed quota
workflow 40771960021/job 123111204192. Actual normal/reconciled source intent,
in-memory document binding, replay and changed receipt refusal passed; the new
registration intent flag and existing writable writer/start/recovery/readiness/
owned cleanup flags are true. Linux control/appplatform/transport/driver race
passed in 1.963/1.611/5.409/3.083s.

Daemon workflow 40771959992/job 123111205032 passed full Linux journal race in
205.284s, followed by actual native policy/IPC, distinct identities and Portal
root endpoint acceptance. These verify the new intent source scope without
enabling protected writable registration delivery or production dispatch.

General workflow 40771960020 passed all app race shards/aggregate, web test
shards, web lint/typecheck/build, workflow lint and Docker image/smoke checks.
Go job 123111206787 passed build/vet/staticcheck, then failed the existing
`golang.org/x/net@v0.60.0` vulnerability gate (GO-2026-6866/GO-2026-6860), so its
subsequent ordinary test did not run. This change does not modify dependencies.

These tests do
not prove protected file delivery, app consumption, handshake, full daemon crash/
relaunch, marketplace installation, active viewer access or completed installation.

## Next dependencies

Add durable one-shot writable registration claim and protected document delivery,
observation-only uncertain file recovery and exact current receipt read. Then
connect handshake/viewer delivery, managed activation/worker and signed controller
transport. Physical fencing/lifecycle, broker, SDK/shared shell/reference apps,
market install and full P00–P14 acceptance remain incomplete. Production, real
credentials, Status adoption, public SDK freeze and server beta/version settings
remain outside this development change.
