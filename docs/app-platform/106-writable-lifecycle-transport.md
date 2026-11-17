# Writable lifecycle controller transport

This follows [105](105-writable-handshake-transport.md). Concrete writable
writer/start/recovery/readiness/delivery APIs existed, but a non-root Portal
controller could only request the writable handshake. This change exposes a
closed lifecycle RPC on the explicitly configured candidate/managed endpoint.
It is infrastructure plumbing; Portal installation/boot/viewer/activation is
still not wired to this new path. No production endpoint starts automatically.

## Contract

Request magic is `PWLQ0001`, reply magic `PWLR0001`. A request contains:

1. Magic (8 bytes), fresh cryptographic random bytes (32), command byte length
   (4-byte big endian), and one action byte.
2. Exact bounded signed command bytes followed by the 64-byte signature.
3. Existing descriptor frame: 4-byte length and exact descriptor bytes.
4. Private document length (4 bytes) and bytes, then EOF. Only registration
   delivery permits/requires a nonempty document, bounded to 4096 bytes.

Actions are deliberately separate:

| Value | Action | Concrete operation |
| --- | --- | --- |
| 1 | Writer execute | First prepared permission attempt, or applied receipt replay |
| 2 | Writer read | Current historical writer receipt only |
| 3 | Writer reconcile | Fresh separately authorized observation of expired unknown writer |
| 4 | Candidate start | First prepared native start, or applied receipt replay |
| 5 | Candidate read | Current historical writable running receipt only |
| 6 | Candidate reconcile | Fresh separately authorized observation of expired unknown start |
| 7 | Candidate probe | Current writable readiness observation |
| 8 | Registration deliver | Protected delivery / separately bounded file reconciliation / applied replay |
| 9 | Registration read | Current historical protected-file delivery receipt only |

The action byte is included in the complete request SHA used for reply binding.
Each action independently verifies the correct signed command domain and all
nested current authority before native dispatch. A writer command cannot become
a start command by changing the action. Read/reconcile never becomes first
execution. Unknown writer/start results never silently reenter create/start or
permission mutation. The existing preparation transport supplies the first data
receipt; writable handshake remains the separate protocol from 105.

Reply: magic (8), SHA256 of the complete exact request (32), body length (4),
canonical typed evidence (at most 16384 bytes), EOF. No private document or key is
returned. No detailed native errors are serialized on refusal. Invalid/truncated,
trailing, over-limit or uncorrelated replies are rejected. Correlation prevents
another session's otherwise identical response from being reused.

## Authority and provenance

`RunControllerWritableLifecycle` requires the actual configured non-root UID,
root-owned endpoint profile, protected ancestors, exact peer credentials and
unchanged socket identity. Before connecting, bound/clone public and private
input, verify current nested signatures and descriptor release, and validate the
complete private document when delivering it. Clear private byte copies and the
request frame after use. No app URL, filesystem path or Docker options are
accepted. Ordinary read-only/data-only endpoints cannot dispatch these actions.

The server authenticates kernel UID before reading any header. Only its concrete
candidate runtime/journal provider can execute lifecycle work. It verifies the
current stored controller authority and descriptor before reading private bytes.
The entire server session is bounded to 30 seconds from connection; client read
and probe sessions have a three-second limit, client effect/reconciliation
sessions have a 30-second limit. Signed command deadlines remain independent and
can expire earlier. Cancellation closes the connection; lost responses may
follow effects and do not authorize replacing an identity.

Client and server parse the separate action's canonical receipt, compare signed
effect/payload/source/resource/fence bindings, reject future settlement, and
require probe timestamps inside the current observation interval. Reverify the
current signed reference on client receipt. Historical applied replays preserve
the original receipt and physical observation timestamps. They are not fresh
health claims.

`WritableLifecycleObservation` keeps canonical bytes, exact action, cloned current
authority, endpoint provenance, command SHA and local receipt time privately. It
refuses JSON encoding/decoding. Typed accessors refuse other action families and
parse new receipt copies, avoiding nested pointer aliases. Private mock socket
proofs retain `RootController: false`. Host consumers must still independently
check current Portal operation/runtime/source state, freshness and expected
native/receipt identities. Registration evidence is not a handshake, and none of
these observations activate an app or grant viewer/broker access.

## First-attempt dispatch

Journal `DispatchDataWriter` and `DispatchWritableCandidate` check complete
current signatures and exact managed descriptor before admission, then route only
prepared work to existing claim/native execution. Strict completed-history readers
run before first-admission checks because those deliberately reject later resource
revisions. An exact still-prepared candidate reservation is independently read
and claimed, allowing an interruption between reservation and claim to resume
without relaxing initial admission. Candidate start reserves its
exact existing identity before claiming. Applied work uses the strict historical
reader; unknown/other states refuse. Existing claims remain once-only and
owner-bound. Existing concrete execution controls exclusion/preflight, native
policies and atomic settlement; no callbacks or parsed receipts can provide a
success result. Reconciliation remains a separately selected concrete API with
fresh issuance after original expiry and no native mutation.

## Verification

Portable tests cover all nine independently signed action references, foreign
command domains, bad signatures/descriptors, invalid actions, opaque JSON refusal,
typed accessor separation and authority-key copy isolation. Journal tests reject
invalid input before writer admission and preserve an unknown writer claim when
initial dispatch is repeated. Linux mock sockets cover read-only endpoint refusal,
framing/digest/size/truncation/trailing/cancellation, canonical parser refusal and
foreign registration bindings. These cases are protocol evidence only.

`TestNativeWritableLifecycleTransport` uses disposable hosted root Linux and the
owned XFS quota root/shared journal. Root prepares a unique fixture allocation;
a copied child test binary runs with the configured non-root controller UID/GID.
All writer execution, start, readiness and protected delivery happen through the
new root listener, including historical reads and identical applied replays.
Applied writer/start reconciliation must refuse. Synthetic private document
bytes travel to the child over stdin, never environment or log. The fixture then
uses the independent writable handshake channel and host MAC/boot verification.
Cleanup inspects exact native ownership before stopping/removing the fixture.

Required new report flags:
`native_writable_lifecycle_cross_uid_verified` and
`native_writable_lifecycle_idempotent_replays_verified`. The script must see both
new test markers; passing a previous fixture alone is insufficient. Positive
expired-unknown recovery over this new channel is not claimed by this fixture;
existing native journal tests separately prove those observation-only APIs.
Local focused race passed (journal 1.994s, transport 1.442s); full local runner
race passed after the replay correction (journal 164.939s; other packages cached;
initial transport run 1.689s and runner command 1.268s). Portal platform regression passed in 8.103s. Linux amd64/arm64
transport test builds, Linux vet/staticcheck and Python acceptance tests (14)
passed. The child receipt marker uses raw test output so the parent can parse
it independently of testing log prefixes. The completed-writer replay regression
passed in 6.283s after routing historical reads before first-admission checks.

Functional source `2f304d36818d18a9b3a8e3934697e3c3185a72a8` passed disposable
quota workflow [40790818016](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40790818016),
job 123175301435. Checkout identifies that exact source merged into parent
`15b5ba863cb622f578c33726dd6f13f5bc0012fb`. Both new lifecycle evidence flags
are true. Actual cross-UID writer/start/readiness/delivery, strict historical
reads and identical applied replays passed, with unchanged process start/restart
metadata and independent host MAC/boot verification. Existing writable/native
checks, `owned_resources_cleaned` and owned registry cleanup passed.

Daemon workflow [40790818046](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40790818046),
job 123175301720, passed on the same functional source: full Linux runner race
(control 1.903s, data 1.012s, driver 3.034s, journal 226.732s, transport 7.457s,
runner command 1.036s), closed native policy, distinct process identities and
Portal confirmation through the root endpoint.

General workflow [40790817988](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40790817988)
passed all three app race shards and aggregate, web tests/build, workflow lint
and Docker font/smoke checks. Go job 123175301782 passed build/vet/staticcheck,
then failed existing `golang.org/x/net@v0.60.0` vulnerabilities GO-2026-6866 and
GO-2026-6860 (reported fixed in v0.62.0), skipping its ordinary test step. The
general workflow remains failed; no dependency/toolchain upgrade is included.

## Portal integration sequence

The next implementation must retain the existing strict readonly source checks.
`internal/app/app_platform_candidate_intent.go` currently requires namespace
revision 2, two source effects (three with registration), and readonly running/
readiness receipt formats. Merely accepting the new RPC proof in those functions
would erase lifecycle distinctions. Introduce a separately verified writable
source path instead:

1. Reuse current operation lease, review authority, publisher trust, runtime
   enrollment and exact descriptor verification. Persist one stable future
   candidate resource identity before constructing its writer command. Do not
   generate another resource/effect after an uncertain response.
2. Bind preparation effect 1 to writer effect 2, then writable start and
   registration: four exact effects. Validate the corresponding volume revision
   3 / writer receipt, source hashes and candidate identity independently of the
   old sealed revision-2 path. Existing control tables suffice; this transport
   does not authorize a schema change.
3. For each RPC, validate opaque action, root endpoint/UID/GID, current authority,
   exact signed command SHA and bounded proof age. Compare typed receipt against
   the exact expected source/native/resource identities. Re-read current Portal
   lease/runtime/review/publisher state before committing the result with an exact
   revision/fence CAS. A historical receipt must never substitute for current
   readiness or restart fencing.
4. Derive the writable registration document using the existing encrypted key
   system and exact installation/generation/release identity. Store only the
   public digest/receipt. Keep document bytes out of rows, logs and environment.
   Match protected delivery before the independent challenge/MAC/boot exchange.
5. Introduce a separate writable boot consumer and viewer/request path; only
   then integrate activation and the automatic install worker. Test unknown
   outcomes, reserved-but-unclaimed restart, revoked authority, stale control
   revision, epoch restoration and lost responses through the real controller
   endpoint, with actual physical state inspected before and after retry.

A green transport fixture is a prerequisite for these steps, not evidence that
Portal has implemented them.

## Remaining work

Wire writable operation/runtime/source state into Portal and boot/viewer checks,
then automatic activation/install worker. Test lost-response/restart recovery
through the full Portal/controller path. Physical writer revocation/stop/update,
retained data lifecycle, restore fencing, capability broker, SDK/shared React/Ant
shell, reference apps/market installation and full P00–P14 acceptance remain.
No schema, dependencies/toolchain, production configuration, real credentials,
Status adoption, public SDK freeze or server beta/version changes are included.
