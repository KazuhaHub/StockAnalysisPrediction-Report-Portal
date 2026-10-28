# Carry writable handshake over the authenticated controller endpoint

This follows [104](104-writable-backend-handshake.md). The native/journal exchange
exists, but the non-root Portal controller needs a bounded authenticated way to
request it and identify the returned evidence. This slice adds a separate writable
handshake frame to the existing explicitly configured candidate/managed endpoint.
It does not yet connect Portal operation/boot state or expose app viewer access.

## Request and reply contract

- Request magic: `PWHQ0001`; reply magic: `PWHS0001`. Legacy readonly handshake
  remains `PRHQ0001`/`PRHS0001` with its original verifier and behavior.
- Request header: 8-byte magic, 32 fresh random bytes and 4-byte big-endian signed
  command length. Follow with bounded raw writable registration command, 64-byte
  signature, existing bounded descriptor frame, 4-byte challenge length and exact
  challenge bytes. Require EOF. No private registration document/key is sent.
- Reply header: 8-byte reply magic, SHA256 of the **complete exact request frame**
  and 4-byte body length. Body is canonical writable handshake evidence (at most
  8192 bytes). Require EOF; reject truncated/trailing/oversized data, another magic
  or mismatched request digest. Fresh random request bytes prevent reply reuse
  across otherwise identical commands/challenges.

The wire values are provisional development contracts, not a public SDK freeze.
No schema, new socket service, daemon configuration or deployment is introduced.

## Client checks

`HandshakeWritableControllerBackend` requires the current process to be the
configured non-root controller UID and uses the existing root-controller endpoint
profile. The host supplies its exact current expected protected-file receipt in
addition to signed reference, descriptor and challenge. That expected receipt is
comparison metadata and cannot supply native provenance.

Before connecting:

1. Bound and clone raw/signature/descriptor and authority key. Verify the complete
   nested writable preparation/writer/start/registration signatures under current
   authority and signed deadline, using the new registration signature domain.
2. Canonically encode/parse the expected writable delivery receipt; require its
   effect, logical payload, running-source hash and document hash to match the
   independently verified signed request. Normal/recovered delivery histories
   retain their original claim/recovery windows. Reject zero/foreign receipts.
3. Parse descriptor and require managed backend and exact signed release digest.
4. Use the existing endpoint checks: protected canonical socket/ancestor path,
   configured root ownership/mode/group, exact kernel peer credentials and
   unchanged socket identity around connect. The controller cannot substitute a
   same-group user or app-owned socket. This API does not accept a URL.

IO uses the existing three-second session timeout and caller cancellation. Send
one frame, half-close writes, read one bounded correlated reply, require EOF.
Parse only the separate canonical writable handshake format. Require exact
registration payload, expected delivery receipt SHA, expected native ID and
challenge SHA. Native observation time must be inside this request/receive
interval; wall clock must not move backwards and context must remain valid.
Reverify the current signed reference at receive time before returning proof.

## Opaque transport result

`WritableBackendHandshakeObservation` retains:

- Exact writable handshake evidence and request command SHA.
- Cloned controller authority/key/fingerprint.
- Authenticated endpoint profile and controller/runner identities.
- Local receipt time with its process monotonic component.

The type refuses JSON encoding/decoding. Its authority getter clones the key.
A same-UID fake server test deliberately returns `RootController: false`; such a
result is framing evidence only and must fail later production host provenance
checks. No JSON evidence object can manufacture an authenticated fresh result.

The host still must check exact configured endpoint/current controller/operation,
expected source/delivery, bounded freshness and private MAC/challenge/time/boot
verification before using a result. This slice does not implement that new
writable Portal consumer; the old readonly host boot consumer remains unchanged.
Do not pass writable results through the readonly observation or boot path.

## Runner checks

The server authenticates the kernel controller UID **before reading the magic**,
using the existing bounded eight-session server and cancellation handling. The
new handler requires the explicitly configured concrete candidate runtime and
journal; an ordinary readonly listener has no such provider and refuses.

Read bounded frame components and strict EOF. Invoke only
`Store.ExchangeWritableBackendHandshake` with current signed authority and exact
descriptor/challenge. That journal path revalidates applied original/recovered
source/delivery and actual private file/process before and after the fixed HTTP
exchange, as specified in 104. Any failure closes without a successful reply.
Marshal only successful bounded canonical evidence and bind it to the complete
request digest. No handler callback or browser boolean can replace the concrete
native/journal path. No file or ledger mutation follows from the request.

## Verification and limits

Portable tests verify the full nested writable request/expected receipt fixture
and opaque proof JSON/clone rules. Linux fake-socket tests exercise accepted
framing, nonce/digest mismatch, wrong magic, oversized/truncated/trailing reply,
stale/future native times, wrong registration/delivery/native/challenge bindings,
malformed answer, unknown JSON and readonly evidence format. Expected foreign
receipt tests reject before using a missing endpoint. Ordinary readonly listeners
must refuse without invoking their backend.

These mock socket cases prove framing/correlation/refusal only. A separate
disposable `TestNativeWritableHandshakeTransport` creates an actual new owned
preparation/writer/process/registration through concrete journal APIs on the shared
XFS root and journal. It starts an explicit root candidate listener, then copies
the test binary to an owned readable directory and launches a child with the
configured non-root controller UID/GID. The child receives signed public reference,
descriptor, expected receipt and short-lived challenge only; it receives no key
or private registration document. A root parent independently verifies the real
backend answer's MAC and nonempty boot ID. Another UID sharing the controller
group and the approved UID using another group must be refused. No system account
or Docker socket permission is modified. Cleanup independently inspects exact
native ownership before stopping/removing the owned fixture and releasing its IPC.
The parent requires `native_writable_handshake_cross_uid_verified` and
`native_writable_handshake_controller_peer_denial_verified`; previous native
checks must keep passing. This test composes the new channel and real backend,
but Portal current operation/boot integration remains unfinished.

Local full runner race passed (transport 1.285s; other packages cached), portable
fixture/proof race passed in 1.451s, platform regression cached. Linux amd64/arm64
transport test builds, vet and Linux staticcheck passed before adding composed
native acceptance; its updated build/static checks and fresh Linux workflows are
pending. Passing an existing quota/daemon test alone is not new composed evidence.

## Next dependency

Complete full writable controller lifecycle transport and connect current Portal boot verification,
viewer forwarding and activation/worker. Physical fencing/lifecycle, capability
broker, SDK/shared shell, reference apps, real marketplace install and full
P00–P14 acceptance remain incomplete. Production, real credentials, Status
adoption, public SDK freeze and beta/version configuration remain unchanged.
