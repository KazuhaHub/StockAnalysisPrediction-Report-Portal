# Writable backend request protocol and physical relay

This follows [110](110-portal-writable-registration.md). Registration and boot
verification do not themselves forward application requests. This change adds
the distinct writable request authorization, physical exchange, journal checks
and root/controller transport needed by the future Portal viewer/activation path.
It does not expose a browser route, accept browser-supplied identity or mark an
installation active. Current Portal session/OU/audience checks and final writable
activation/worker integration remain necessary before users can open these apps.

## Closed command, separate from readonly and lifecycle mutation

`control.WritableBackendRequestCommand` uses
`portal.runner.writable-backend-request/1-dev.1`, signed in the separate
`portal.app/runner-writable-backend-request/v1` domain. Its nested registration
must verify as the writable registration family, which in turn binds the exact
writer/start/source receipts. Readonly request/registration signatures cannot
substitute for this domain. The command includes one random request UUID, the
renewable signed registration, verified backend boot ID, HTTP method/path, body
SHA and assertion SHA. It has no persistent effect number and admits no new
runner command, container, file, permission or acknowledgement.

Strict parsing rejects duplicate/unknown/trailing JSON, invalid UUID/digest/boot,
expired or altered nested source, invalid signature and forbidden routes. Verified
command getters copy nested buffers. A shared private structural validator keeps
the existing readonly request rules unchanged without manufacturing a readonly
verified value for writable callers. It checks canonical assertion claims against
the method/path/body/boot and exact assertion digest. This is structural checking,
not MAC/viewer authentication: the app must independently verify the backend MAC,
identity, boot, nonce and viewer claims before executing its own route.

Existing request limits apply unchanged:

| Item | Limit / rule |
| --- | --- |
| Signed command | 65536 bytes, including nested references |
| Request body | 65536 bytes; nonempty body must be JSON and not GET |
| Backend assertion | 32768 bytes, separately transported and digest-bound |
| Application response body | 1048576 bytes, JSON only |
| Encoded result frame | 1500000 bytes |
| Route | Existing method policy under `/v1`, excluding reserved handshake and ambiguous/encoded path escapes |
| Root/controller request | One total 20-second deadline, bounded further by signed source expiry |
| Native HTTP exchange | At most 15 seconds within its caller deadline |
| Forwarded headers | Fixed JSON accept/content type and the backend assertion only |
| Returned response | Allowed status and JSON body; no cookies, redirects or upstream headers |

The app response is business data. It cannot supply Portal viewer authority or
mint a capability grant. The privileged controller must still derive assertions
from current Portal authorization; an app cannot request arbitrary control signing.

## Physical exchange and exact protected file

`CandidateRuntime.ForwardWritableCandidateRequest` accepts the exact original
writable launch request and registration snapshot. It produces a separate opaque
`WritableBackendRequestObservation`, never a readonly observation or parsed health
boolean. Zero/JSON-imported observations refuse; getters copy the response body.

The native boundary holds the existing writable process/source and mount pins
through the exchange. It verifies daemon/image/policy, physical quota/data profile,
exact owned running container, private socket directory and native identity. Read
the protected registration file before HTTP and require its original device,
inode, owner/mode/xattr and content digest. Forward exactly once to the fixed
UID/GID-bound Unix socket. Verify the same registration file and the complete
process/mount/policy again after HTTP. No backend URL or destination header comes
from an app. Non-Linux refuses rather than substituting another backend.

The existing bounded HTTP helper is shared with readonly execution. It opens a
fresh non-reused Unix connection, disables redirects/compression/keepalive, checks
peer identity, response media type, allowed status and bounded JSON. No automatic
retry is added. If HTTP or any postflight fails, clear the buffered response and
return an uncertain outcome: an application write may already have happened.

## Journal before and after IO

The existing handshake-only-named preflight helper is renamed
`prepareWritableBackendExchange` because both operations require the same exact
current applied registration/process/source. The underlying validations remain
unchanged. Normal and recovered canonical registration receipts are supported.

`Store.ForwardWritableBackendRequest` holds the owner execution exclusion through
source validation, native IO and postflight. Verify the distinct signed request,
structural assertion/body bindings, current controller authority and current
applied registration before native dispatch. Bound context lifetime to the signed
nested expiry. No callback or untrusted native success result is accepted.

After IO, reopen the exact current registration source. Require unchanged original
writer/start/file receipt, resource identity, command/ack state and timestamps;
reverify current signatures and data. Require the opaque physical observation to
match that registration and exact method/path/body/assertion, with observation
time within journal pre/postflight. Only then return canonical
`WritableBackendRequestResult`, bound to request UUID/SHA, original delivery receipt
SHA, native ID, boot ID, response and observed time. Recheck deadline/closing state
before return. This writes no request result or effect row. Failed/expired/current
source checks cannot cause a native restart, private-file repair or retry.

A failure after dispatch cannot roll an app's business state back. The caller
must report an uncertain outcome and use app-defined recovery/idempotency where
needed; it must not turn a transport failure into an automatic second POST.

## Root/controller frame

The protected listener adds `PWRQ0001` / `PWRS0001` frames alongside the original
readonly request family. Existing kernel peer authentication runs before reading
any frame magic. Only an explicitly configured concrete candidate-execution
provider can handle the operation; a read-only/shared metadata provider cannot.
The request includes a random nonce, bounded command/signature/descriptor, bounded
body/assertion and EOF. The server verifies signed authorization before allocating
private request data. A whole-frame digest binds the correlated reply.

`RequestWritableControllerBackend` additionally receives the host's expected
canonical writable registration receipt. Validate its exact effect/source/document
binding before connecting; verify reply native ID and delivery receipt SHA against
that expected value, not merely against a syntactically valid SHA. Require exact
request UUID/SHA/boot, bounded canonical result, observation inside the local
request/receive interval, EOF, deadline and current signature at receipt.

The separate `WritableBackendRequestObservation` holds local monotonic receive
time, authenticated endpoint/controller facts, request SHA and a copied result.
It rejects JSON import/export. Simulated private-endpoint frame tests never claim
to supply a root/native observation; actual cross-UID acceptance runs separately.

## Verification

Local focused race tests exercise both readonly and writable command domains,
malformed signed requests, assertion/control divergence, getter copies, result
canonicality, zero native proof, current authority/source/ack/resource/file/timestamp
changes and unchanged journal on refusal. The journal snapshot test helper is moved
unchanged into a Linux/Darwin fixture file so metadata-only refusals can compare
storage locally without a privileged runtime. Native snapshot import/export tests
also run locally. Linux-only framing tests cover nonce/magic/size/EOF, stale/future
observations, foreign request/boot/native/delivery, readonly result substitution,
forbidden response, copies and provenance. No test manufactures a native success
snapshot to replace the actual physical path.

Disposable actual Linux acceptance extends two existing fixtures:

1. Normal and recovered writable registration/process sources each exercise real
   business requests. Replace the protected file with identical bytes/permissions/
   xattr but another inode and require the request to fail. Restore the original
   inode, require GET reports zero writes, then a real POST executes once with the
   authenticated user role. Require unchanged runner rows and original file
   identity. A stopped process refuses requests and remains stopped.
2. The existing non-root Portal fixture acts as the concrete controller, using
   its explicit disposable signing material (not a browser route/current launch
   grant). Forward a real MAC-bound viewer, reject replay, changed body, fabricated
   viewer MAC and stale boot, then require rejected calls caused no writes. Check
   actual root/native/delivery/boot bindings and non-serializable copied replies.
3. The controlled backend counts all route entries as well as successful writes.
   A signed POST executes once, increments its write counter, then deliberately
   closes its socket without a response. Require an error with no opaque success
   reply. A fresh GET must show exactly one additional write and exactly two new
   route entries (that POST plus GET), proving no hidden retry even if nonce
   protection would have prevented a duplicate write. This test-only disconnect
   behavior and counters exist only in the disposable backend fixture.

Required markers/report flags include:

| Marker | Report flag |
| --- | --- |
| `NATIVE_WRITABLE_REQUEST_VERIFIED` | `native_writable_request_verified` |
| `NATIVE_WRITABLE_RECOVERED_REQUEST_VERIFIED` | `native_writable_recovered_request_verified` |
| `NATIVE_WRITABLE_REQUEST_FILE_REPLACEMENT_REFUSED` | `native_writable_request_file_replacement_refused` |
| `NATIVE_WRITABLE_STOPPED_REQUEST_REFUSED` | `native_writable_stopped_request_refused` |
| `WRITABLE_BACKEND_REQUEST_RELAY_VERIFIED` | `native_writable_backend_request_relay_verified` |
| `WRITABLE_BACKEND_REQUEST_UNCERTAIN_NO_RETRY` | `native_writable_backend_request_uncertain_no_retry` |

Fresh Linux evidence is pending. Earlier readonly exchange, writable handshake or
metadata/frame tests alone do not prove this complete writable request path.

## Remaining integration

Add current Portal writable viewer access, post-IO session/OU/audience/publisher/
runtime revalidation, managed activation and automatic install worker/scheduler.
The fixed disposable viewer in the native fixture is not evidence for these
missing Portal authorization gates. Complete positive expired-unknown recovery
through Portal, physical stop/fencing/revocation, update/migration/rollback/
retained uninstall/restore/checkpoint lifecycle, broker/secret/task capabilities,
shared SDK/React/Ant shell, neutral reference apps and real marketplace acceptance.
D5 visual/SDK and D6 production gates remain outstanding. No schema, dependencies,
toolchain, production/real credentials, Status migration, SDK freeze or beta
configuration changes are included here.
