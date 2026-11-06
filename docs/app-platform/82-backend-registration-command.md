# Backend registration command and journal admission

This development phase connects registration material to an authenticated,
durable delivery intent for an already started managed candidate. It does not
write a bootstrap file, deliver a key, complete a backend handshake or activate
an application. Production activation and SDK freeze remain separately gated.

## Command contract

`control.BackendRegistrationCommand` uses format
`portal.runner.backend-registration-command/1-dev.1` and a separate Ed25519
signature domain, `portal.app/runner-backend-registration/v1`. Its fields are
format, a new effect UUID, logical payload SHA-256, original candidate command
bytes and signature, and the canonical registration document SHA-256. The
private document and its signing key are absent from the command and journal.
No app-controlled path, endpoint or executable option is accepted.

The independently verified nested candidate reference must meet the current
controller authority, epoch, revision and short-lived validity requirements.
The outer signature must also verify. Logical identity binds deployment, epoch,
installation, operation, generation, container resource, original candidate
effect/payload, and document digest. Renewal timestamps and transport fences do
not change the logical digest; journal ownership and durable floors still apply.
A new delivery effect cannot reuse the original candidate effect UUID.

Parsing rejects oversize input, invalid UTF-8, duplicate or unknown fields,
trailing values, incorrect signatures and cross-command signature domains.
Callers receive cloned command byte slices.

## Private document validation

`ValidateBackendRegistrationDocument` accepts only an opaque verified command,
a matching descriptor and a bounded canonical registration document. Before any
future file write, it checks the signed document digest, descriptor release hash,
backend declaration, and exact app/deployment/epoch/installation/generation/
policy/release tuple. The current experimental initial-install path accepts
only generation 1 and policy revision 1. Unsupported future cases fail closed.
The returned live registration handle must be closed; temporary exported document
bytes and signer copies must be cleared by their owners.

This check proves consistency and authorization of bytes. It does not prove
current native health, delivery, boot identity, viewer authority or app access.

## Transactional admission

`Store.AdmitBackendRegistrationCommand` uses the existing signed-command
transaction, enrollment, authority, floor and replay checks. It records a
prepared `activate` kind/stage command at effect slot 1, using existing approved
journal fields. No table or migration is added.

Unlike start admission, registration intentionally targets an existing container.
The narrow exception requires the original candidate command to be applied with
the same logical identity and fence, a valid matching running receipt with no
acknowledgment attempt, and the exact resource/native ID. The resource must be
present/running at revision 2 with matching control and writer fences. Missing,
changed, stopped, acknowledged or uncertain source records are refused. Normal
candidate-start resource reuse remains prohibited.

An exact same-effect replay returns its existing admission. Changing its document
or claiming another effect in the same slot conflicts. The SQL row retains only
public digests and status; admission receives no private document. Native state
can change after this transaction, so a future dispatcher must independently
recheck the actual container before delivery.

## Verification

Control tests cover signature separation, independently signed wrong-tuple
registration documents, stale authority, malformed input, tampering and safe
copying. Journal tests cover applied source history, exact replay and conflicts,
resource/fence/acknowledgment changes, and absence of extra resource rows. Their
seeded source rows are synthetic and are not native execution evidence.

The Linux acceptance fixture additionally exports only a public registration
digest after the non-root Portal prepares material under actual backend
readiness. The root test controller signs sample authorization against that
candidate's public command and admits/replays the intent in the actual runner
journal. It requires `BACKEND_REGISTRATION_INTENT_ADMITTED` before the script
reports `native_backend_registration_intent: true`. This tests admission against
an actual started candidate, not Portal RPC delivery or private key transfer.

Local focused control/journal race and the full runner/CLI race suite passed
(journal 79.356 s). Linux amd64/arm64 Portal compilation, Linux vet/staticcheck
and 14 Python tests passed. Functional source
`c011d25e58d0ab28ec745f3375a3d79cdc66189f` passed native quota workflow
[40619597258](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40619597258),
job 122590125633. `native_backend_registration_intent`, actual managed backend
readiness and owned cleanup are true; exact registry cleanup passed. Linux
transport/driver race passed in 4.482/1.481 s. These results prove intent
admission against the actual ready candidate, not secret transfer or handshake.

General CI 40619597274 passed workflow lint, web checks and all web test shards,
Docker smoke, Go build/vet/staticcheck and market validation. Its Go job
122590126069 failed the existing x/net v0.60.0 vulnerability gate (GO-2026-6866
and GO-2026-6860, reported fixed in v0.62.0), so its subsequent non-race Test
step was skipped. App race shards were not all terminal at capture; general CI
is not all green.

## Remaining implementation

Next implement the signed durable one-shot native delivery effect, protected
bootstrap file handling, actual boot handshake and authenticated forwarding.
Unknown outcomes must be reconciled against exact existing native evidence;
blind overwrite or duplicate delivery is not permitted. Current viewer/OU checks,
broker/provider denial, writer authorization, fresh readiness and activation
remain necessary. Bounded socket storage, image fetching, scheduling/startup,
cleanup/reconciliation and full lifecycle/reference apps are still outstanding.
No production/beta configuration, real credentials, dependency versions or public
SDK freeze change in this phase.
