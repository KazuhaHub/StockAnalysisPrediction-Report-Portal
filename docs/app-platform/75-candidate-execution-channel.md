# Signed candidate execution channel

This continues P04/P05 after document 74. A provisioned non-root controller can
request an initial candidate launch through the local root runner and receive its
actual journal receipt. This is development infrastructure, not production runner
startup, managed registration, app readiness or application activation.

## Source map

| Source | Responsibility |
| --- | --- |
| `transport/candidate_execution_linux.go` | Explicit execution listener, concrete provider check, client and descriptor framing |
| `transport/candidate_receipt_linux.go` | Shared receipt response checks with distinct read/execute frames |
| `transport/transport_linux.go` | Peer-first routing and bounded execution session promotion |
| `driver/candidate_runtime_linux.go` | Read-only configured-provider/daemon check before opening an execution listener |
| `transport/candidate_execution_linux_test.go` | Read-only listener, missing provider and descriptor refusal |
| `transport/candidate_execution_native_linux_test.go` | Real cross-UID launch, replay, read and forbidden-request acceptance |

Paths are relative to `internal/apprunner/`.

## Provisioning and authority

`ListenCandidateController` requires root, an existing shared journal and a live
concrete `CandidateRuntime`. `CheckConfiguration` verifies the borrowed root and
daemon are available; a nil, zero or closed runtime refuses. That check grants no
execution readiness, writer lease or app access. The caller retains ownership
until all server sessions have stopped.

This API does not create users, directories, trust, an image, data namespace or
backend registration. It does not start from the runner CLI. An ordinary
`ListenController` endpoint continues to refuse execution. Data preparation and
candidate execution listeners currently have separate explicit constructors;
combined production runtime provisioning remains part of later integration.

The root runner authenticates kernel controller UID before any frame. The client
requires the protected root/controller endpoint and kernel server UID. The signed
candidate intent binds exact release bytes and the immutable logical identity.
Current journal admission, data receipt/root history, original fences, one-shot
claim, native preflight and atomic receipt settlement remain mandatory.

## Execution framing

Read frames retain `PRKR0001` / `PRKP0001`. Execution uses distinct experimental
`PRKE0001` / `PRKF0001` magic values; no SDK/protocol freeze is implied.

Execution request layout:

1. 8-byte execution magic.
2. Fresh 32-byte nonce.
3. 4-byte big-endian command length, bounded by `control.MaxCommandBytes`.
4. Exact command bytes and 64-byte candidate signature.
5. 4-byte big-endian descriptor length, 1 through `MaxDescriptorBytes` (128 KiB).
6. Exact managed descriptor bytes, then request-side EOF.

Both ends parse the managed descriptor and require its SHA-256 to match the
signed candidate release. This carries immutable declaration bytes, not an
arbitrary Docker configuration, path, command callback, native ID or network URL.
The concrete runtime rechecks the same digest and derives its fixed sandbox plan.
Signature verification independently gates journal admission/execution; matching
a digest alone is never authority.

The reply uses the existing bounded canonical receipt envelope, with execution
reply magic and SHA-256 over the complete request, including descriptor length
and bytes. Read and execution replies cannot substitute for one another. Opaque
observation/copy boundaries remain those in document 74.

## Deadlines and uncertainty

The server first gives peer-authenticated frame headers the existing 3-second
budget. Only an explicitly enabled execution request can promote to an absolute
30-second deadline measured from connection handling start. The original close
callback must be stopped successfully before its context is cancelled; then the
execution context installs its own deadline and cancellation callback. Ordinary
read, admission, enrollment and data behavior keep their existing deadlines.

The client execution exchange is bounded to 30 seconds. The signed command's
own expiry is checked independently and bounds the one-shot native invocation;
network promotion never renews signed authority. Cancellation, timeout, malformed
reply or lost acknowledgement can follow actual effects. Preserve the exact
logical effect and request fresh signed authority to inspect its retained state.
Never replace its identity to retry creation.

Prepared requests may make one native attempt. Applied replay returns its original
receipt without create/start. Unknown requests refuse; native reconciliation is
still a separate missing path. A running receipt is historical process evidence,
not fresh readiness or permission to activate.

## Acceptance

Local checks retain the full runner/CLI race suite, Linux vet/repository-pinned
staticcheck, amd64/arm64 test compilation and Python refusal tests. Unit refusal
checks exercise a valid signed/descriptor execution frame against a read-only
listener, zero/unconfigured providers and invalid/oversized descriptors.

The existing owned quota workflow runs `TestNativeCandidateExecutionTransport`
after native journal and receipt-channel tests. It reuses the shared journal/data
provider and monotonic project allocator, prepares one fresh namespace and opens
a real configured candidate runtime with private fixture socket mounts. A
separately executed non-root controller sends the exact signed descriptor-bound
request to the root server. Assertions include:

- Wrong UID, wrong group and an unsigned direct execution frame create no native
  candidate before the accepted request.
- The accepted request launches an actual digest-pinned inert process and settles
  its real native ID/receipt; independent Docker inspection matches the full owner.
- Repeating execution and reading through that endpoint preserve the same receipt
  and exactly one native ID.
- An earlier unknown candidate refuses and is not physically recreated.
- The controller cannot read the runner journal directly.
- Fixture cleanup checks the full owner tuple and removes only exact native IDs;
  it does not add a production stop/remove endpoint.

The probe emits these only after the exact native test reports PASS:

- `native_candidate_execution_cross_uid_actual_start: true`
- `native_candidate_execution_replay_same_native_id: true`

Current-source native acceptance is pending CI. Earlier read-only or in-process
native receipt evidence cannot substitute for this cross-UID launch path.

## Remaining work

Portal request issuance/delivery/stage settlement, combined runtime provisioning,
artifact staging, native unknown-effect reconciliation, application readiness,
writer access, broker, automatic registration and full managed lifecycle remain.
Execution readiness stays false. No storage layout, production/beta configuration,
real credential, Status migration or SDK freeze changes in this phase.
