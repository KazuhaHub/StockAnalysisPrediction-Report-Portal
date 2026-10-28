# Independently authorized backend request relay

This development phase adds bounded native application requests after verified
registration/boot. A separate controller signature authorizes each exact request;
the application independently verifies its viewer assertion MAC, boot and nonce.
This is infrastructure transport, not the browser-facing current-viewer gate.
Managed launch remains denied; no HTTP API route or user/provider/writer grant is
enabled. Current session/OU/grant authorization and response revalidation remain
mandatory before connecting the public request API.

## Separate control authorization

`control.BackendRequestCommand` uses format
`portal.runner.backend-request/1-dev.1` and signature purpose
`portal.app/runner-backend-request/v1`. It binds a nonzero request UUID, the current
signed registration reference, expected boot ID, exact method/path, body SHA-256
and backend assertion SHA-256. The raw body and assertion are separate bounded
inputs. Registration/start signatures alone cannot authorize forwarding.

The nested registration/candidate signature chain must pass current independent
controller key, epoch, revision and short-lived envelope checks. Duplicate/unknown
fields, malformed UTF-8, invalid IDs/digests, unsupported methods and reserved
handshake routes are refused. Getters clone signed reference buffers.

Data validation checks actual body/assertion digests and canonical shared-protocol
claim structure. Boot/method/path/body digest in the assertion must equal the
separately signed request. This is structural consistency only: the relay does
not authenticate the viewer MAC or invent user/OU facts. The app's independent
shared-protocol verifier must reject invalid MACs, old boot IDs and repeated
assertion nonces before executing its route. The control signing key and backend
assertion key have separate purposes and key material.

## Paths, payloads and responses

Methods follow the pinned shared protocol: GET/POST/PUT/DELETE. Paths must be a
canonical ASCII request URI below `/v1/`; non-ASCII text and query spaces must be
percent encoded. Both URL parsers must preserve the same request URI. Fragments,
control characters, ambiguous encodings/traversal and all decoded aliases of
`/v1/handshake` are refused. Query strings remain bound exactly by both signatures.

A request body is at most 65536 bytes, empty or valid JSON; GET bodies are refused.
Assertions are at most 32768 bytes. The backend HTTP request supplies only fixed
JSON content/accept headers and the exact `X-Portal-Assertion`; it never accepts
ambient cookies, authorization headers, arbitrary URLs, destinations or host paths.

Response headers are bounded; proxies, redirects, compression and keepalive are
disabled. Only JSON bodies of 1–1048576 bytes are returned. Status is 2xx or the
existing typed rejection set 400/403/404/409/422/429. Upstream cookies, redirect
targets, content-disposition and other headers never enter the result type.
Unsafe status/type/body/size and timeout errors are refused.

## Native and journal checks

`CandidateRuntime.ForwardCandidateRequest` accepts a concrete prepared launch and
public applied-delivery snapshot. The same shared native boundary used by boot
checks the exact container policy, pins, quota/data evidence and protected file
binding/hash/device/inode before and after HTTP IO. It uses the fixed private
socket with strict owner/mode/inode/kernel-peer checks. IO is bounded to 15 seconds.

The launch remains pinned throughout IO, but the runtime-wide mutex is released
so unrelated applications are not serialized behind a slow request. Runtime Close
waits on the same launch lock before releasing its pins. No callback or JSON body
can mint an opaque native observation.

`Store.ForwardBackendRequest` verifies the independent control signature/data and
requires the exact already-applied registration under current journal authority,
source candidate/data history and floors. It rebuilds/matches the original native
preflight, rechecks authority immediately before forwarding, then rereads the
same registration and signature validity after IO. The signed envelope expiry
also bounds the attempt. It creates no durable effect row or access grant.

## Protected transport and uncertainty

The provisional root/non-root request/reply magic is `PRRQ0001`/`PRRS0001`.
Requests carry a fresh nonce, bounded signed control command, descriptor, raw body
and assertion. Replies bind the complete request frame digest and canonical public
metadata: request ID/hash, original delivery receipt digest, native/boot ID,
bounded JSON response and observation time. Encoded results are capped at 1500000
bytes, including base64 expansion of the body. Current command/boot/request hash
and fresh observation time are checked again by the client. Mutable getter buffers
are cloned; opaque transport observations refuse JSON import/export.

Ordinary read-only/data-only listeners cannot forward. Only the configured
controller UID and protected root Unix endpoint may use this infrastructure API.
Payload/frame buffers are cleared and no body/assertion enters journal rows or
logs. No browser route, background-service identity or app-controlled principal
is added.

There is exactly one HTTP attempt and no automatic retry. A lost connection,
cancellation, expiry, malformed reply or failed post-IO check can follow an app
side effect. Such failures must remain outcome-uncertain to callers; they do not
prove rollback or authorize a fresh write attempt. App-level idempotency remains
necessary for deliberate business retries. Exact assertion replay is independently
refused by the backend's bounded nonce store; process restart must use a fresh
boot ID. This relay is not a persistent business-result cache.

## Tests and actual acceptance

Pure contract/control tests cover canonical routes, reserved encoded aliases,
control signature-domain separation, malformed commands, source expiry, cloned
references, data tampering and assertion/control divergence. Same-UID Linux HTTP
and framing tests cover boundaries, redirects, compression, status/type/body,
missing ambient authority, private provenance, response cloning and fresh nonce/
request/boot/time binding. Mock transport responses cannot prove an actual app.

The disposable backend adds a shared-protocol verifier with per-process nonce
storage and a small `/v1/echo` contract fixture. Its authenticated viewer comes
only from verified claims; a body containing `role: admin` cannot change a user
assertion. A successful POST increments an in-memory execution counter once.

The native test uses an explicit disposable controller authorization and synthetic
opaque viewer for transport acceptance. It does not fabricate current Portal user
access or enable a pending app. After actual material consumption/boot verification,
it requires one real signed POST, exact viewer/body separation, rejection of exact
replay, altered body, fabricated viewer MAC and old boot, then a fresh GET proving
the rejected writes did not increment the counter. Result getters must be isolated
and refuse serialization. The script requires
`MANAGED_BACKEND_REQUEST_RELAY_VERIFIED` before reporting
`native_backend_request_relay_verified: true`.

Local runner/CLI race passed (journal 85.091 s). Final contract/control race
passed (1.749/1.876 s), and Portal SQLite regressions passed (28.615 s). Linux
compilation, vet/staticcheck, actionlint and 14 Python tests passed. The native
workflow now directly runs control/appplatform race contracts and triggers on
control edits, in addition to transport/driver and physical acceptance. Native
workflow evidence is pending; synthetic replies do not prove physical execution.

## Remaining scope

Implement the current viewer/session/OU/grant request gate and stable per-install
pseudonyms, then connect it to this relay with live boot and response revalidation.
Backend route authorization must come from host policy, never app-supplied viewer
facts. Default-deny provider/broker and writer authority, managed activation and
normal scheduling remain gated. Bounded writable socket storage, image fetching,
restart/takeover and full cleanup/update/rollback/uninstall/reference apps,
cross-repository SDK publication and release approvals remain. No schema,
production/beta configuration, real credentials, dependency/toolchain version or
public SDK freeze changes are included.
