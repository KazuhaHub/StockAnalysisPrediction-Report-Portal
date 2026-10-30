# Candidate resource reservation and one-shot execution claims

This connects authenticated candidate intent to durable resource reservation and
a process-owned, bounded synchronous invocation gate. It is a P04/P05 prerequisite,
not a production container dispatcher or a completed managed installation. It
changes no approved table, column, migration or persistent side-store layout.

## Source map

| File | Responsibility |
| --- | --- |
| internal/apprunner/journal/candidate_reservation.go | Exact candidate/data facts, durable resource identity and reservation |
| internal/apprunner/journal/candidate_claim.go | Unknown-before-invocation marker and process-local one-shot handle |
| internal/apprunner/journal/candidate_claim_test.go | Data tuples, replay/rollback, concurrency, restart/owner, cancellation and close tests |
| internal/apprunner/journal/candidate_commands.go | Shared private candidate index projection |
| internal/apprunner/journal/data_reservation.go | Volume-specific namespace lookup with container coexistence |

The [candidate signature contract](67-candidate-command-admission.md) and
[closed container adapter](66-candidate-container-policy.md) remain distinct.
Neither applications nor JSON can supply an infrastructure callback. No service
route or native Docker callback is wired by this work.

## Sequence

1. `AdmitCandidateCommand` records a verified start-candidate intent, as before.
2. `ReserveCandidate` rechecks explicit current controller trust, exact admitted
   command, immutable logical digest, original fence/current durable floors,
   unacknowledged prepared state and absence of another pending installation
   command. All checks occur inside the journal transaction.
3. Match exactly one applied prepare-data command in the same operation,
   installation, generation and controller epoch. Parse its canonical validated
   receipt and reconstruct its original logical data command to verify payload,
   namespace, release, data format, writer fence and ownership. Match the unchanged
   observed volume revision/time/project and deployment-wide physical data-root
   history. A caller-supplied JSON receipt is never accepted as this evidence.
4. Reserve the signed resource UUID as a container row with an explicitly pending
   native ID (`candidate-pending/v1/<resource UUID>`), unknown observation and no
   observed timestamp. This placeholder is not a Docker native ID and must never
   be passed to Docker. Its ownership SHA-256 binds candidate identity, original
   fence, complete logical payload and exact durable data receipt bytes.
5. `ClaimCandidate` requires the unchanged reservation and evidence, then changes
   prepared to unknown with a typed claim marker before issuing a live handle.
   The marker binds a random claim UUID, effect/resource, ownership, data receipt,
   original fence and deadline. Unknown work cannot receive another first claim.
6. `RunCandidateOnce` verifies the owner, cancellation, deadline, reservation,
   current signature/authority/floors, exact marker and data history again. It
   commits the validation transaction before invoking trusted synchronous work,
   and checks expiry/closing again at that boundary. No SQL write lock spans
   external work. The shared process execution gate remains held until return.
7. Copies share one atomic consumption bit. At most one callback may run. Provider
   failure, panic, expiry or lost response never changes unknown into applied.
   There is no implicit retry, native receipt, activation or writer grant.

A replayed reservation is durable intent only. An existing foreign, changed,
observed or differently fenced resource cannot be adopted. The global resource
primary key and native-ID uniqueness reserve identity atomically. Any failed
transaction leaves no partial container reservation. Final signature/time checks
precede commits. Fresh reservation itself performs no filesystem or Docker work.

## Owner and time boundaries

The claim contains a private Store pointer, exact signed bytes and shared atomic
consumption state. JSON serialization/deserialization is refused. Another Store
or a reopened journal cannot use it, even with the same persisted data. A restart
retains unknown intent and the pending resource but cannot recreate a first
execution handle; future candidate reconciliation must inspect native state
under renewed signed authority rather than run the effect again.

A process-local monotonic deadline is derived from the envelope's wall-clock
expiry when claiming. This prevents a wall-clock rollback from extending the
live handle. The callback context is bounded by that deadline and any shorter
caller context. A canceled caller waiting for the shared execution gate does not
consume the unused claim. Once entered, a late cancellation or deadline error
still leaves a possibly-effectful invocation unknown. Callbacks must honor their
context and finish all native work before returning; context expiry cannot undo
a Docker effect or forcibly stop arbitrary Go callback code.

Store.Close marks closing before waiting on the same gate used by data/candidate
execution. It does not release the journal owner lock or close the database while
a synchronous candidate callback is still running. A callback must never close
the same Store or recursively acquire the gate. Old handles remain unusable after
close/reopen.

Current authority is checked at admission and immediately before invocation. The
gate is not continuous controller revocation or a substitute for native writer/
root/socket fences during an in-flight operation. The concrete native integration
must pin physical identities and enforce the signed bounds at its effect and
receipt boundaries. This change does not claim those physical guarantees.

## Data namespace coexistence

Container and volume resources legitimately share one application's namespace.
The old data lookup selected all rows by namespace, so adding a container row
would incorrectly look like a conflicting second volume. The query now selects
namespace/installation matches only for kind=volume, while still checking a row
whose resource ID equals the namespace UUID regardless of kind. This preserves
resource-ID collision refusal and existing volume identity checks without
mistaking the owned container for the volume.

Tests create both resource kinds and confirm that the data evidence reader still
selects the exact volume and the original public data receipt reader works once
the separate pending-command barrier is removed. The barrier itself remains.

## Verification and follow-on work

Portable tests use explicitly synthetic native observations to test journal
semantics. They prove no Docker or hard quota behavior. Cases include missing/
changed data tuple or receipt, failed transaction rollback, reservation replay,
foreign owner/fence/controller, copied/concurrent handles, canceled and expired
contexts, restart inability to reclaim, close waiting, and durable unknown state.

The predecessor 836c1ae5090e626367aa47b1f041dd76113df8f0 passed main CI
40408711973, real daemon/container workflow 40408711894 and hard quota workflow
40408711972. That evidence belongs to its revision and does not prove candidate
native execution through these new claims.

Next, bind configured physical data/socket roots and quota evidence to the claim,
wire exact create/start/readiness/stop, persist a typed native receipt and provide
candidate-specific reconciliation. No production dispatcher, automatic backend
registration, broker, active-writer transition, application readiness or complete
lifecycle/release acceptance is delivered by this gate. Production trust/host
configuration, real credentials, Status migration, SDK freeze and beta server
settings remain untouched.

## Local validation record

Full runner/CLI race regression passed before the additional concurrency/restart/
data-coexistence cases (journal 66.113 seconds). Final focused candidate claim and
reservation race tests passed (4.888 seconds). Full local Go regression passed
(application package 65.049 seconds, journal 15.116 seconds), along with vet,
staticcheck and Linux amd64/arm64 journal compilation. PostgreSQL cases were not
run locally because no DSN was supplied. These checks are not native execution
or production acceptance evidence.
The full journal race suite was then rerun on the final source, including all
added cases, and passed in 73.029 seconds.
