# Managed backend boot handshake

This development phase proves that an actually running candidate can consume its
protected registration document and answer the existing shared protocol challenge.
It adds read-only native/Unix exchange and host verification. It does not activate
an app, authorize a viewer/provider/writer, create a connection row or schedule a
managed installation automatically.

## Shared protocol and bounded message shape

The protocol remains the existing pinned extensionprotocol HMAC handshake: a host
creates a fresh authenticated challenge; the app verifies it and answers with its
per-process boot ID and exact challenge digest; the host independently verifies
the answer. The key remains scoped to deployment/epoch/installation/generation/
policy/release. An application-held backend key never becomes runner or broker
authority. Boot ID is the application's authenticated process identity, not
hardware attestation.

`appplatform` adds bounded strict single-field challenge/answer JSON helpers.
Each token is at most 4096 ASCII base64url-token characters with one separating
dot; request/response bodies are at most 8192 bytes. Missing/null/wrong-type,
case aliases, duplicate/unknown fields and trailing values are refused. Lexical
validation alone does not authenticate a token: only the shared signer verifies
its MAC, exact challenge, identity tuple and lifetime.

## Native exchange and delivered-file continuity

The driver accepts an opaque prepared candidate plus the public applied-delivery
snapshot. It independently checks the actual running container policy, pinned
mounts and concrete quota/data evidence before and after HTTP IO. It reads only
fixed `broker/registration.json` through the pinned parent descriptor, requiring
the exact claim xattr, mode 0400, UID/GID 65532, one regular-file link, bounded
size, stable metadata, stored device/inode and document SHA-256. Private read
buffers are cleared.

The existing delivery-file observer shares this protected reader and retains its
exact byte comparison. Reading does not chmod, repair, replace or publish files.
Missing/replaced files or changed binding/hash/ownership are refused.

The only HTTP operation is POST `/v1/handshake` over the fixed private Unix socket.
Socket owner/mode/inode and kernel peer credentials use the existing strict
readiness dialer. There is no proxy, redirect, compression, keepalive, arbitrary
URL/path, cookie, authorization header, viewer body or key transfer. Headers,
body and duration are bounded; current native/file evidence is checked again
after the answer. The opaque native observation is a physical exchange report,
not proof of a valid application MAC by itself.

## Runner and controller channel

`Store.ExchangeBackendHandshake` requires an already applied exact registration
effect under current enrolled controller authority, original candidate/data
history and durable fences. It rebuilds the native preflight and checks the
original snapshot before exchange, then rereads the same applied delivery after
IO. It creates no new effect, receipt, resource or grant.

The provisional protected request/reply magic is `PRHQ0001`/`PRHS0001`. Requests
carry a random nonce, bounded signed registration command, digest-bound descriptor
and bounded challenge. Replies carry the entire request digest and canonical
bounded evidence: logical payload, delivery receipt digest, native ID, challenge
digest, answer and fresh observation time. No private registration document is
retransmitted. Ordinary read-only/data-only listeners refuse this operation.

The transport requires fresh observation time between request start and reply
receipt, exact nonce/request digest and matching command/challenge. It rechecks
signed validity on return. Its opaque result retains root endpoint/controller
provenance and cloned public authority. Parsed JSON and private-profile mock
frames cannot mint root provenance. The answer still needs host-side MAC checks.

## Portal verification

`probePlatformBackendBoot` first prepares the current durable delivery and requires
its applied state. It confirms the exact runtime root, derives a live per-instance
signer, creates a fresh challenge and invokes the protected channel. It requires
fresh root provenance, exact endpoint/UID/GID/current controller, command digest,
applied receipt digest, original native ID and challenge digest, then verifies
the application's MAC and boot ID with the existing shared protocol.

After native IO, Portal rechecks current lease, pause, OU/admin/publisher/runtime,
sealed material and original source history by preparing the same applied intent
again. The receipt/logical/document/runtime bindings must remain identical; the
proof must still be at most three seconds old. A second MAC verification precedes
returning a private non-serializable boot value tied to that exact lease/runtime/
delivery/document/native ID and response time. Key copies and live handles are
cleared/closed. No readiness timestamp is refreshed or activation row changed.

Future activation/request consumers must revalidate current authority and the
short freshness window. A previously valid boot value is not a durable grant or
proof of later health. Actual health remains an independent check.

## Tests and acceptance

Pure message tests cover strict parsing/bounds. Journal tests refuse handshake
before applied delivery and malformed evidence. Same-UID Linux HTTP tests cover
fixed method/path/body and missing ambient authority, duplicate/unknown/trailing/
oversize answers, redirects, compression, type/status, socket mode/peer and timeout
refusal. Linux framing tests cover nonce, malformed replies, stale/future time,
command/challenge mismatch, opaque provenance and absence of private document
retransmission. These mocks are framing tests, not MAC/native proof.

The disposable backend now generates one random boot ID per process, reads the
actual mode-0400 registration file as UID 65532 and uses the pinned shared signer
to answer. The test fixture uses the internal document parser; public app SDK
bootstrap/schema publication remains a later cross-repository deliverable.

After the existing root/Portal delivery recovery tests, Portal must obtain two
fresh actual handshakes with the same process boot/native ID, with no database/
activation/grant changes. An actual answer cannot verify a different fresh
challenge; the actual backend must refuse a valid-shaped wrong-key challenge.
The live boot value must refuse JSON export. The script requires
`MANAGED_BACKEND_BOOT_VERIFIED` before emitting
`native_portal_backend_boot_verified: true`.

Local appplatform/runner/CLI race passed (journal 88.439 s); SQLite platform
regressions passed (20.251 s), independent PostgreSQL regressions passed
(16.922 s). Linux compilation, vet/staticcheck, actionlint and 14 Python tests
passed. A synthetic fixture timestamp now uses the database clock to avoid a
SQLite millisecond-rounding boundary; 30 repeated affected cases passed. Native
workflow
[40632718683](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40632718683),
job 122632590592, passed functional source
`107d944e7296d1bc4336ead25b6f1f4d671fbb3a`: actual Portal backend boot,
durable delivery and owned cleanup keys are true; exact registry cleanup passed.
Linux transport/driver race passed in 4.551/1.500 s. This proves actual protected
file consumption, challenge/answer verification and refusal cases against the
running backend fixture, not managed activation, health or viewer/provider access.

General CI 40632718681 passed web checks/test shards, workflow lint and Docker
smoke. Go job 122632591536 passed build/vet/staticcheck and market validation,
then failed the existing x/net v0.60.0 vulnerability gate (GO-2026-6866 and
GO-2026-6860, reported fixed in v0.62.0), skipping the following non-race Test
step. App race jobs were not all terminal at capture; general CI is not all green.

## Remaining full platform work

Connect current viewer/OU-bound request forwarding, default-deny broker/provider
and writer authorization, managed activation and normal scheduling with fresh
health/boot checks. Bounded writable socket storage, image fetching, startup/
restart/takeover reconciliation, cleanup, update/rollback/uninstall/reference
apps, cross-repository SDK publication and release approvals remain. No schema,
real credentials, production deployment, beta configuration, dependency/toolchain
version or public SDK freeze changes are included.
