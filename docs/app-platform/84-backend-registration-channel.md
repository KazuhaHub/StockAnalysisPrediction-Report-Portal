# Authenticated backend registration channel

This development phase replaces the private-key test pipe with an authenticated
controller-to-runner Unix channel. It connects the real registration dispatcher
from document 83 and tests its uncertain-outcome recovery across the non-root/
root boundary. It does not yet create/settle the Portal operation effect or
connect the normal scheduler, backend boot handshake or managed activation.

## API and endpoint eligibility

Linux infrastructure callers use
`transport.DeliverControllerRegistration(ctx, path, uid, gid, raw, signature,
authority, descriptor, document)`. The current process must be the explicitly
configured non-root controller UID. The endpoint uses the existing protected
root-owned Unix socket profile, parent/socket inode/ownership/mode checks and
kernel peer credential checks. The server authenticates the peer UID before
reading even the magic header.

Only endpoints already configured with a concrete candidate execution runtime
and journal can dispatch registration. Ordinary read-only or data-only listeners
refuse this frame without reading its private portion. No application-facing
HTTP route, ambient runtime discovery, credential enrollment or directory
provisioning is added.

## Request and reply framing

The provisional request magic is `PRBQ0001`; reply magic is `PRBS0001`. A request
contains, in order:

1. 8-byte magic, 32 random nonce bytes, big-endian uint32 command byte length;
2. exact signed registration command bytes and 64-byte signature;
3. big-endian uint32 descriptor length and exact descriptor bytes;
4. big-endian uint32 private document length and exact document bytes;
5. Unix write-half EOF, with no trailing bytes.

Command and descriptor sizes use their existing contract maxima. The private
document is 1–4096 bytes. The client bounds all inputs before copying them,
verifies current nested/outer signatures and exact descriptor/document identity
before connecting, and clears its private copy and complete private request frame
on every return path. The caller still owns clearing its input document.

After peer authentication, the server bounds and verifies public command and
descriptor material against independently enrolled current controller trust before
allocating/reading the private document. It bounds the document, requires EOF,
and calls the concrete journal dispatcher. It clears the private read buffer on
all return paths. Errors close the connection without sending secret-bearing
error payloads. No private document enters a log, environment, journal row or
fixture staging file.

The reply has an 8-byte magic, SHA-256 of the entire nonce-bearing request frame,
a uint32 receipt size and canonical public receipt bytes, followed by EOF. The
client requires the exact frame digest, canonical bounded receipt, matching
signed logical effect/payload/document digest, nonfuture historical settlement,
and still-valid signed envelope when the reply finishes. Cancellation and a
30-second bound apply on both sides. An interrupted request may already have
published the file: retain the same effect and reconcile, never mint a replacement.

## Provenance and historical replay

`BackendRegistrationObservation` is an opaque transport result with cloned public
controller key, exact endpoint metadata, receive time, command digest and public
delivery receipt. JSON export/import is refused. A private same-UID frame test
cannot manufacture `RootController` provenance.

The reply may represent an earlier exact delivery. Receive time proves this
response's authenticated provenance; it does not refresh application health,
boot identity or physical delivery time. `BackendRegistrationReceipt.MatchesCommand`
compares canonical historical metadata and the exact logical digest; it does not
verify signatures, current files or application consumption.

## Actual non-root/root acceptance

The Portal fixture still prepares actual material after current native readiness.
It signs a sample registration intent with the disposable fixture controller key
and sends private bytes through the real protected channel as UID/GID 41001.
The root parent sees only public command bytes and synchronization events over
an inherited pipe. That pipe no longer carries a private document.

The root parent installs a fixture-only SQL settlement failure before allowing
the first RPC. The first RPC must return no observation after file publication;
the journal must remain unknown and the file must be mode 0400. The parent then
removes the trigger and allows the same-intent retry. The child requires root
endpoint provenance, exact effect/command digest and stable historical replay;
the parent independently requires the same inode and an applied exact journal
receipt. The script requires `BACKEND_REGISTRATION_RPC_RECONCILED` before emitting
`native_backend_registration_rpc_reconciled: true`.

The sample authorization is explicitly not a fabricated Portal effect row. The
normal Portal operation-effect transaction, renewal and receipt settlement are
next work; no app-use, broker, writer or activation grant is created here.

## Validation and remaining work

Linux framing tests cover nonce/magic/length/truncation/trailing/unknown-field
refusal, effect/document mismatch, nonfuture historical replay, read-only endpoint
refusal, cancellation, malformed private material, public-key getter isolation
and opaque observation serialization. Mock frames test parsing only and retain
private-profile provenance. Pure journal tests compare exact and changed receipts.
Full local runner/CLI race passed (journal 84.888 s); focused registration
journal race passed (5.657 s). Linux amd64/arm64 compilation, vet/staticcheck
and 14 Python tests passed. Linux-only framing/native tests remain pending;
cross-compilation is not substituted for a real root/non-root result.

After Portal durable delivery orchestration, add real backend boot handshake,
current viewer/OU-bound forwarding, default-deny provider/broker and writer
authorization, and managed activation with fresh health checks. Bounded writable
socket storage, image fetching, scheduling/startup, full cleanup/reconciliation,
upgrade/rollback/uninstall/reference apps and cross-repository SDK publication
remain. No production deployment, beta configuration, real credentials, database
schema, public SDK freeze or module/toolchain version changes are included.
