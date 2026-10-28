# Fresh writable backend readiness under current authority

This follows [100](100-writable-start-recovery.md). A container start receipt
proves a historical process observation, not a functioning application endpoint.
This slice checks the backend's actual readiness over its protected private Unix
socket while preserving the exact writable source/claim and current authority.
It does not register the backend, activate an installation or expose an RPC.

## Entry points and execution order

1. Call `Store.ObserveWritableCandidateReadiness(ctx, raw, signature, native,
   descriptor)` only from trusted platform infrastructure with bounded signed
   reference bytes, the reviewed descriptor and the configured native runtime.
   The native ID, path, environment, Docker options and readiness result are never
   accepted from the application or browser.
2. Clone inputs and hold the Store execution gate through preparation, native IO
   and postflight. Closing the Store waits for this work. Refuse missing runtime,
   missing/oversized descriptor, closing owner or cancelled/deadline-expired work.
3. `prepareWritableCandidateReadiness` opens the strict applied writable view.
   Authenticate current enrollment/key/revision/epoch and the signed logical
   effect. Validate exact original data and writer receipts, no acknowledgements,
   current fences, complete original/reconciled running receipt and independently
   checked running revision-2 resource/history. Unknown/prepared starts refuse
   even when an actual container or healthy socket already exists.
4. Reparse descriptor digest/backend/platform/schema and reconstruct expected
   comparison metadata from exact persisted preflight and verified sources.
   `writableCandidateExpectedSnapshot` is shared with unknown-start recovery, but
   returns no executable or native authority. Readiness accepts both valid normal
   and recovered running receipts; it does not rewrite either receipt window.
5. Bound native IO by current signed expiry. Native
   `CheckWritableCandidateReadiness` reuses the private observation helper from
   recovery, with the exact recorded native ID required. That helper checks the
   supported daemon, immutable image, closed writable plan, protected data/root
   identities and actual quota/permissions. It pins existing protected IPC/data
   paths and requires the complete preflight snapshot to equal original evidence.
6. Enumerate all running and stopped container references to data/HTTP/broker
   storage; require one unambiguous native ID equal to the journal's recorded ID.
   Inspect its full native policy/labels/identity/mounts and require running,
   unpaused, not restarting/dead. Refuse a competing process-local launch handle.
7. Only after those checks, perform the trusted internal readiness probe. The
   private callback is package-internal, not a public callback/native interface;
   neither journal callers nor app code can insert work into it. Probe timeout
   is two seconds inside the bounded native observation (five seconds total) and
   signed request deadline.
8. Dial only the infrastructure-derived
   `<socket-root>/<installation>/<generation>/http/app.sock`. Existing socket
   checks require a canonical absolute path within the Unix path bound, socket
   mode 0600, UID/GID 65532, one link and unchanged inode before/after connect.
   `SO_PEERCRED` verifies the actual peer identity. No Portal cookie, token or
   secret is sent. GET `/health/ready` uses bounded HTTP headers/body, no proxy,
   compression or redirect following, and requires status 200 plus JSON.
9. Reuse the strict existing backend readiness decoder: exact known protocol,
   explicit `ready: true`, canonical positive declared data schema and no unknown
   or duplicate properties. The returned data format must match the current
   signed descriptor/source. A merely healthy arbitrary URL cannot pass.
10. After the socket response, repeat data/quota checks, held/fresh mount pin
    validation, all-container inventory and complete native running/policy
    inspection. Native identity cannot change during the probe. Return opaque
    `WritableCandidateReadinessObservation`, distinct from process execution and
    recovery evidence, with complete writable binding and observation time.
11. `finishWritableCandidateReadiness` opens a new current applied transaction.
    Require byte-exact same receipt, original source, resource, intent times and
    valid current authority/fences/history. Independently validate the opaque
    native result against expected preflight, native ID and declared schema.
    Observation must fall between preparation and postflight. Recheck expiry and
    closing state around commit before returning transient metadata.

No SQL transaction spans native/socket IO. No resource/command/acknowledgement,
revision, receipt or app data is changed by readiness. No storage is provisioned,
no permissions are repaired and no process is created/restarted/stopped here.
The shared native observation helper retains recovery's full before/after checks;
adding readiness does not make recovery invoke HTTP.

## Result contract

`WritableCandidateReadiness` uses separate format
`portal.runner.writable-candidate-readiness/1-dev.1`, with canonical JSON up to
4096 bytes. It records logical payload hash, exact running receipt hash, native
ID, protocol, data format and observation time. The original readonly readiness
parser refuses this format, and the writable parser refuses readonly format,
extra/duplicate properties, ambiguous encoding and malformed identity/counters.

This parsed metadata is not opaque native proof or durable readiness. Only the
concrete private-socket/native observation may reach the postflight path. A
serialized/decoded/zero native readiness value cannot provide that proof.

A successful result describes a point-in-time observation under current signed
authority. It is not a registered connection, active generation, viewer access
grant, writer revocation or promise that the backend will remain available.
Registration and activation must independently revalidate their current sources.

## Refusal and recovery behavior

| Situation | Behavior |
| --- | --- |
| Process exists but start remains unknown | Refuse readiness; reconcile the original effect first |
| Receipt is applied but actual container stopped or missing | Refuse readiness; do not start it or rewrite historical receipt |
| Native ID, policy, writable allocation or private mount changes | Refuse before response is trusted |
| Another process references private storage, including a stopped one | Refuse regardless of endpoint response |
| Socket path/UID/GID/peer/inode is unsafe | Refuse the endpoint |
| Backend returns non-ready, malformed protocol/schema, redirect or oversized body | Refuse readiness |
| Authority/source/resource/ack/time changes during IO | Refuse the result at current postflight |
| Health probe transiently fails during normal startup | Caller may perform a new current readiness check; never repeat start |

Inventory and native checks are bounded point-in-time observations under trusted
lifecycle exclusion. They do not lock Docker against a malicious host root.

## Verification and evidence boundary

Local SQLite/crypto tests cover separate canonical result format, strict parser
refusal, current applied source preparation, unknown refusal, rejection of zero
opaque proof and unchanged historical records. Postflight drift tests require
current authority/source/fence/ack/native/resource/time/cancellation rejection
before the opaque-proof validation, so a zero proof cannot hide a missing reread.
Native readiness values refuse JSON and conversion to process/recovery evidence.

Disposable actual XFS/Docker acceptance tests both a normal applied process and
a recovered one over its actual UID-bound private readiness socket. They compare
complete durable deployment/command/resource rows before and after readiness,
require exact original receipt/payload/native binding, and refuse wrong native ID
or backend data format. A process whose start remains unknown cannot report
readiness. The normal fixture is then explicitly stopped by test infrastructure;
readiness must fail, keep it stopped and preserve all journal rows. This explicit
test cleanup/state injection is not exposed as a production lifecycle action.

Parent acceptance requires all markers:
`native_writable_current_readiness_verified`,
`native_writable_recovered_readiness_verified`,
`native_writable_readiness_preserves_journal`,
`native_writable_stopped_readiness_refused`.
Existing actual writer/start/recovery, quota, readonly paths and cleanup tests
must continue to pass. Expanded writable journal race passed in 31.329s, driver
race in 1.309s and platform regression in 8.515s. Linux amd64/arm64 driver/journal
test builds, vet, Linux staticcheck and all 14 Python probe tests passed. Full
runner race and actual native acceptance results are pending.

## Next dependencies

Implement current writable automatic registration intent/delivery, handshake and
viewer delivery, then managed activation/worker integration. Physical fencing and
stop/uninstall/update/restore, capability broker, SDK/shared shell, neutral
reference apps, real marketplace install and full P00–P14 acceptance remain.
Production deployment, real credentials, Status adoption, SDK freeze and server
beta/version configuration remain outside this change.
