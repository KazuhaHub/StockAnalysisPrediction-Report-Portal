# OS-authenticated candidate receipt channel

This continues P04/P05 after document 73. A controller can now request an applied
candidate receipt from the shared runner journal through the existing explicitly
provisioned local endpoint. No listener is started automatically, and this change
does not expose native candidate execution or activate an application.

## Source and API

All paths below are relative to `internal/apprunner/transport/`.

- `candidate_receipt.go`: opaque observation, immutable receipt bytes, copied
  getters and shared endpoint profile metadata.
- `candidate_receipt_linux.go`: bounded candidate-specific framing, signed request
  verification, existing OS-authenticated connection, server read and client proof.
- `transport_linux.go`: dispatch the candidate read magic only after existing
  kernel peer UID authentication.
- `candidate_receipt_test.go`, `candidate_receipt_linux_test.go`: canonical public
  vector, opaque/copy boundaries, private-profile framing and malformed replies.
- `candidate_receipt_native_linux_test.go`: actual shared-journal receipt across a
  root runner/non-root controller boundary, refusal and journal DAC checks.

`ReadCandidateReceipt` is the private same-user development API.
`ReadControllerCandidateReceipt` requires the provisioned root-owned endpoint and
exact non-root controller UID/group. A private observation is never a controller
observation. Platform-independent receipt/opaque types compile on other systems;
the local transport adapter remains Linux-only.

## Wire steps and checks

The experimental query magic is `PRKR0001`; reply magic is `PRKP0001`. They are
separate from data receipt, data execution, enrollment and admission-ack frames.
No public SDK/protocol freeze is implied.

1. Clone the request bytes, signature and independently configured authority key.
   Verify the complete candidate signature and bounded envelope before connecting.
2. Require the existing canonical endpoint path, protected parent/socket ownership
   and permissions. Verify the kernel server UID before sending request bytes.
   The root server verifies the kernel controller UID before reading any header.
3. Send 8-byte magic, fresh 32-byte random nonce, 4-byte big-endian command length,
   exact command bytes and 64-byte signature. Close the request write side.
   Command length is bounded by the existing control protocol limit.
4. The server requires exact framing and EOF, then calls only the real journal's
   `ReadCandidateRunningReceipt`. That read rechecks current signed authority,
   original fences, resource, data/preflight history and pending work. Unknown or
   altered evidence produces no receipt response. No create/start path is called.
5. The reply contains candidate reply magic, SHA-256 of the entire request frame,
   4-byte body length and canonical receipt bytes. Bodies are limited to 16 KiB;
   malformed, truncated, oversized or trailing data refuses.
6. The client requires exact magic, request hash, canonical receipt and logical
   `MatchesCommand`. It refuses a future settlement timestamp, cancellation,
   backward time and an expired/changed signed authority snapshot at completion.
   The existing session deadline/cancellation closes blocked socket IO.
7. Mint an opaque observation containing the canonical receipt bytes, cloned
   controller authority, request/command hashes, receive time and authenticated
   endpoint profile. JSON serialization/deserialization cannot mint authority.

The receive-time age is a transport freshness bound. The enclosed receipt remains
historical process evidence; it is not a fresh native running/readiness check or
permission to activate an application.

## Nested copy boundary

Candidate receipts contain a pointer to nested preflight metadata. Storing a
receipt value and returning it directly would let callers mutate an already
minted observation through that shared pointer. The observation instead holds
private canonical bytes. Each `Receipt()` parses a fresh value, and `Controller()`
clones the public key. Neither getter aliases the proof's private state. Zero
observations return empty metadata and zero receive time.

## Verification

The fixed public JSON vector checks logical/ownership binding and canonical
parsing independently of native execution. Private-profile socket tests cover
fresh nonce/request hashes, copied controller/receipt metadata, opaque JSON reset,
wrong request hash/protocol, oversized/truncated/trailing/invalid/cross-command
responses, admission ACK substitution and future settlement evidence. These tests
prove framing/copy behavior only.

The owned quota workflow additionally runs `TestNativeCandidateReceiptTransport`
after the actual native journal candidate fixture. It reuses the same shared
journal and native data root, signs a fresh request for that actual historical
candidate receipt and independently matches its data inode/project. The container
may already be cleaned: this verifies historical receipt transport, not health.
A root server then serves the receipt to a separately executed non-root controller.
The fixture checks accepted identity/profile/inode, a different UID in the same
group, a wrong group, an unsigned direct frame, an unknown candidate and denial of
opening the runner journal directly. No second journal or publisher enrollment is
created. Only explicit disposable Linux/root CI configuration enables this test.

The script emits `native_candidate_receipt_cross_uid_and_journal_dac: true` only
if that exact native fixture reports PASS. Extended native channel acceptance is
pending its current-source workflow; document 73 alone cannot prove it.

## Remaining work

The candidate execution channel, Portal request/delivery/stage settlement,
unknown-effect native reconciliation, application readiness, data writer admission,
broker, automatic backend registration and full managed lifecycle remain.
Execution readiness stays false. No schema, persistent layout, production/beta
configuration, real credential, Status migration or SDK freeze changes here.
