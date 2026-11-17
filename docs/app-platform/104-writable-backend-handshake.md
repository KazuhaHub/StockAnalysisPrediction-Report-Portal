# Bind writable backend handshake to current delivered file and process

This follows [103](103-writable-registration-delivery.md). Delivery proves a
protected file was published, but does not prove an application read the key or
responds as the current backend. This slice adds the fixed private handshake
exchange under current signed writable registration authority. It returns physical
transport evidence; the host must independently verify the MAC, exact challenge,
expiry and boot identity before using it for pairing or activation.

## Trusted execution order

1. `Store.ExchangeWritableBackendHandshake` accepts bounded signed registration
   reference, exact descriptor, a bounded handshake challenge and the configured
   concrete native runtime. Clone reference/descriptor, acquire the Store owner
   execution gate and retain it through preparation, native IO and postflight.
   Refuse closing/cancelled owners, oversized/missing inputs or invalid token shape.
2. `prepareWritableBackendHandshake` opens current applied registration facts.
   Verify current enrollment/key/revision/epoch and all nested signatures, exact
   applied original/reconciled process and delivery receipts, current fences,
   acknowledgements, strict lifecycle history and exact recorded resource state.
   Prepared/unknown file delivery cannot authorize handshake.
3. Parse the stored protected-file receipt and reviewed descriptor; reconstruct
   the expected original writable preflight and exact file device/inode/native
   ID/content hash/claim-binding hash. These are independently checked comparison
   metadata, not caller-selected path/native identity or an executable handle.
4. Bound all subsequent IO by current signed expiry. Native
   `ExchangeWritableBackendHandshake` uses the complete shared writable native
   verifier: supported daemon, immutable image, exact closed policy/ownership,
   protected root/data/quota, bounded trusted IPC and one unique running native
   resource across running/stopped mount-reference inventory. Competing live
   process-local launch handles and substituted policy/data/private mounts refuse.
5. Under runtime exclusion and held mount pins, `checkWritableBackendFile` selects
   only the infrastructure-derived fixed broker directory from those pins. Reuse
   the fixed protected registration reader: no symlink/nonregular/extra links,
   exact UID/GID 65532 and mode 0400, bounded content and original claim-binding
   xattr. Require recorded device/inode and document SHA exactly. Private bytes
   are cleared before returning and never enter the request, result or error.
6. Exchange only POST `/v1/handshake` on the fixed private `http/app.sock`. Reuse
   the existing bounded HTTP transport: canonical path and Unix length bound,
   mode 0600, UID/GID and SO_PEERCRED identity, unchanged socket inode across
   connect, no proxy, redirects, compression, Cookie or Authorization. Two-second
   HTTP limit is inside the five-second full native limit and signed deadline.
   Require status 200/JSON, bounded headers/body and strict answer token decoding.
7. After HTTP, reread the exact protected file/device/inode/content/xattr, then
   repeat held/fresh pin, current writable data/quota, all-container inventory
   and full running native-policy checks. Only the complete successful path
   produces opaque `WritableBackendHandshakeObservation`. It contains full
   writable file/process binding, challenge hash, answer token and observation
   time, and cannot be reconstructed by JSON or converted to effect evidence.
8. `finishWritableBackendHandshake` reopens a current applied transaction. Require
   exact unchanged original source/data/writer/resource/start/delivery receipt,
   own-row metadata and current authority/fences/history. Compare complete native
   file binding with prepared facts, exact challenge hash and observation time
   between preparation and current postflight. Check expiry/closing before and
   after commit. No SQL transaction spans native or socket IO.
9. Return separate canonical transient format
   `portal.runner.writable-backend-handshake/1-dev.1`: logical registration payload
   hash, exact delivery receipt hash, native ID, challenge hash, answer and observed
   time. Strict parsing rejects readonly format, malformed hashes/times/tokens,
   duplicate/unknown properties, trailing data and oversized metadata.
10. The host must verify this answer with its private derived registration signer
    and its exact outstanding challenge, checking time and boot identity. A parsed
    evidence object and a syntactically valid answer prove no MAC or access grant.
    Host/controller transport and pairing state are subsequent dependencies.

This path creates no launch handle, file, receipt, acknowledgement, generation,
viewer grant or activation. It never repairs, writes a private file, provisions
IPC, changes permissions or starts/stops a process. Parsed historical delivery
is never substituted for the fresh physical file and running container checks.

## Refusal behavior

| Condition | Required result |
| --- | --- |
| Delivery is prepared/unknown or source/current signature is stale | Refuse before native exchange |
| Container absent/stopped/paused/restarting or policy differs | Refuse, retain historical state, never start it |
| Private file missing, changed, unsafe or rebound to another claim | Refuse, never repair/write it |
| Same content/mode/UID/xattr but a new inode | Refuse exact recorded identity |
| HTTP peer/path/inode/schema/status/body boundary fails | Refuse the response |
| Authority/source/resource/ack/receipt changes during exchange | Refuse at current postflight |
| Answer belongs to another challenge or signing key | Host verifier rejects, regardless of successful transport |

Inventory/native verification remains point-in-time under trusted admitted
lifecycle exclusion, not a lock against hostile host root. Current boot identity
is established through host verification, not inferred solely from Docker labels.

## Verification

Local metadata tests cover canonical separate evidence format and malformed/
ambiguous inputs, missing native runtime, unknown delivery refusal and zero opaque
exchange rejection. Current SQLite/crypto tests mutate authority, acknowledgement,
source receipt, native identity, delivery receipt and timestamps before both
preflight/postflight; cancelled work also refuses. No synthetic test manufactures
native successful handshake evidence. Legacy handshake HTTP/decoder/refusal tests
remain unchanged and apply to the shared socket transport.

Disposable actual XFS/Docker acceptance extends both normal and recovered private
registration delivery. The real backend reads its protected registration file
and answers a fresh host challenge. An independent host signer verifies the MAC
and nonempty boot ID; another challenge and a changed key must fail. A second
fresh challenge to the same original process returns the same verified boot ID.
The successful exchange preserves exact durable rows and private file metadata.

The test then temporarily replaces the owned synthetic registration file with
identical bytes, UID/GID/mode and claim-binding xattr but another inode; exchange
must refuse. Restore the original owned file and verify exchange succeeds. Rename
fault injection changes ctime deliberately, so the restoration assertion compares
original inode/device/content/mode, while pure exchange before injection compares
the full metadata stamp. Durable deployment/command/resource rows never change.
Finally stop the normal fixture explicitly through test infrastructure; handshake
must fail, retain stopped state and preserve all rows. These are isolated test
mutations, not production lifecycle APIs.

Parent requires four markers:
`native_writable_handshake_possession_verified`,
`native_writable_handshake_file_replacement_refused`,
`native_writable_handshake_preserves_journal`,
`native_writable_stopped_handshake_refused`.
Every prior real writer/start/recovery/readiness/file delivery and owned cleanup
check must continue passing. Focused handshake race passed (journal 6.241s,
driver 1.294s), platform regression passed in 8.073s. Linux amd64/arm64 journal
and arm64 driver test builds, vet, Linux staticcheck and all 14 Python probe tests
passed. Full local runner race passed (journal 153.907s, driver 1.898s,
transport 1.321s; control/data cached).

Functional source `9543e04253038fd8c1fdd21de081cffad01877ef` passed actual quota
workflow 40786578360/job 123161172419. All four new handshake possession,
same-content inode refusal, journal preservation and stopped-backend refusal
flags are true, alongside owned-resource cleanup and prior writable checks.
Linux control/appplatform/transport/driver race passed in
2.011/1.593/5.360/3.077s. Actual backend answers were independently verified by
the host signer, including challenge/key refusal and stable nonempty boot ID.

Daemon workflow 40786578356/job 123161172391 passed full Linux journal race
in 223.745s, followed by actual native policy/IPC, distinct controller/runner
identity and Portal root endpoint acceptance. This does not establish complete
writable controller transport, pairing state, activation or daemon crash/relaunch.

General workflow 40786578414 passed app race shards/aggregate, all web test
shards, lint/typecheck/build, workflow lint and Docker image/smoke checks. Go job
123161173409 passed build/vet/staticcheck then failed the existing
`golang.org/x/net@v0.60.0` vulnerability gate (GO-2026-6866/GO-2026-6860);
its subsequent ordinary test did not run. Dependencies remain unchanged and
separate successful checks do not clear that gate.

## Next dependencies

Connect host/controller signed transport and current boot-verification result,
then viewer request forwarding and managed activation/worker integration. Physical
stop/fencing, no-effect unknown resolution, update/uninstall/restore, broker,
SDK/shared shell, neutral reference apps, marketplace install and full P00–P14
acceptance remain incomplete. Production, real credentials, Status adoption,
public SDK freeze and beta/version configuration are unchanged.
