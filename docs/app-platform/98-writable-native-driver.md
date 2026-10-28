# Concrete writable candidate preflight and native start

This development slice follows [97](97-writable-candidate-reservation-and-claims.md).
It adds the concrete closed Docker builder, protected native preflight handle and
one-shot writable start. It is not yet connected to journal preflight binding,
process receipt settlement, a public runner endpoint or managed activation.

## Trusted input and types

`WritableCandidateLaunchRequest` contains the already verified candidate tuple,
exact descriptor/platform/data allocation and `WritableCandidateBinding`. The
binding carries start effect/claim UUIDs, logical payload, stable resource
ownership, preparation receipt hash and writer receipt hash.

These values must come from the current journal claim and source verification.
The native driver checks their shape and concrete native facts; it does not
verify controller signatures or durably admit work. A plain request or parsed
snapshot is not execution authority. This adapter supports the initial generation
and writer fence only, under the approved supported rootful Linux profile.

`WritableCandidateLaunch` wraps private held pins owned by one concrete runtime.
It is distinct from the legacy `CandidateLaunch`; JSON marshal fails and decode
clears/refuses it. Its path-free snapshot binds exact claim, candidate/data tuple,
immutable image, platform, full create-policy hash, mount metadata hash, fixed
writable profile and seccomp hash.

`WritableCandidateRunningObservation` is a separate opaque type produced only by
the concrete typed start. It binds that complete preflight, actual native Docker
ID and observation time. JSON cannot construct it. It proves an observed process
state, not application readiness, registration, activation or physical revocation.

## Closed policy

`planWritableCandidateContainer` starts from the independently tested closed
read-only builder and changes only:

- The exact platform-owned `/data` bind becomes writable.
- Reserved labels identify the writable profile and exact start effect/claim,
  payload/ownership and both source receipt hashes. The complete policy is
  rehashed and bound to the private plan.

Everything else remains fixed: immutable local image configuration, UID/GID
65532, read-only root filesystem, no network, all capabilities dropped,
no-new-privileges, fixed filtered seccomp, CPU/memory/swap/PID limits, bounded
log/temp/shared memory, private namespaces, restart disabled and only the three
reviewed data/HTTP/broker mounts. The broker directory stays read-only and only
the private bounded HTTP directory is otherwise writable.

No environment/command/UID/mode/mount/port/Docker option can be selected by the
caller. The only environment values remain the two platform socket paths. There
is no test flag or app-specific branch in this production builder. Old builder
bytes and the old sealed data check remain unchanged.

## Provision and preflight order

1. After durable claiming, call `ProvisionWritableCandidateSockets` with the
   exact current request. It reuses the fixed owned IPC layout and its existing
   protected directories, marker checks and bounded tmpfs. It never repairs
   unexpected ownership. Earlier read-only provisioning remains separate.
2. Check current immutable image/platform and the writable allocation: exact
   configured root UUID/inode, namespace device/inode, project inheritance,
   hard byte/inode limits and bounded usage. Root namespace must already be
   root:65532 `02770`; this adapter does not chmod/chown, grant permissions or
   repair sealed/partial data.
3. Refuse other local live preflight pins for the namespace. Inspect bounded
   Docker inventory including stopped containers, resolving direct/parent/child/
   symlink references. Every actual data reference conflicts; no label exemption
   or same-name adoption applies.
4. `PrepareWritableCandidate` builds the closed plan and pins the entire protected
   mount chains. Data leaf pins require root:65532 `02770`; the old profile still
   requires root:root `0700`. The configured root and concrete quota provider must
   identify the same root/device. HTTP storage is still the independently bounded
   private tmpfs; broker pins keep the original root-owned read-only profile.
5. Reopen paths and compare held/fresh inode, device, owner and mode. Reinspect
   data allocation after pinning, then return only the live typed handle.

Native methods are bounded by a five-second context in addition to the caller's
signed deadline. This does not replace current journal authorization.

## Required journal handoff before start

The caller must durably bind the exact native snapshot to its current one-shot
claim and recheck current authority/source/fences **before** invoking start.
That integration is still outstanding in this slice. Do not mark a pending
reservation ready or dispatch this adapter from a browser request.

`StartWritableCandidate` requires this runtime's live writable handle. The old
`StartCandidate` explicitly refuses the writable profile; the new wrapper also
refuses a read-only handle. Copies share the same native consumed state.

Immediately before create, recheck daemon compatibility, exact writable data,
held/fresh path pins and all actual Docker references. Only the current held
handle can be exempt from local-pin exclusion; actual Docker references still
conflict. A reference appearing after preflight therefore blocks start.

Before the first possible Docker mutation, consume the native handle. Create
using the inspected immutable image ID and exact hashed policy, then start and
independently inspect exact native identity, config, labels, mounts and running
state. Revalidate data/pins after the effect and return opaque process evidence.
Any possibly effectful error remains uncertain. Never automatically retry,
adopt a same-named container, change permissions or remove an uncertain resource.

Inventory checks are point-in-time under the closed single-owned-runtime/journal
lifecycle model. They are not a global lock against trusted host root/Docker
administrators introducing unapproved aliases or resources. Ordinary apps have
none of that host authority.

## Verification

Unit tests verify the separate control/native profile identity, exact claim
labels and receipt-sensitive policy hash, unchanged closed constraints outside
data access/labels, immutable old plan, initial-generation refusal, binding/body
mismatch, old/new handle separation and JSON/zero-proof refusal.

The disposable XFS/Docker writer fixture now uses the actual typed provision,
preflight and start APIs. It no longer alters create JSON or injects a special
writer environment flag. The fixture image detects only its fixed writable data
profile and runs its existing bounded syscall/attribute/byte/inode probe before
health; normal sealed fixtures keep their previous behavior.

Native acceptance requires:

- Legacy preflight/start refusal on writable data/handles.
- Exact opaque preflight/binding and local duplicate-pin refusal.
- An actual stopped data reference inserted **after** preflight must block start.
  The test removes that owned stopped fixture before a fresh pre-effect attempt;
  it does not retry an ambiguous create/start.
- Actual typed create/start, opaque process evidence and repeated-start refusal.
- Another runtime must refuse the actual running data reference.
- Existing actual unprivileged root-ownership/attribute protection, hard byte and
  inode exhaustion, per-child project/inheritance checks and independent healthy
  sibling/global capacity remain passing under the new exact policy.
- Strict owned typed cleanup and IPC/registry cleanup complete in the disposable
  environment. This private test cleanup is not a public stop/fencing API.

The parent requires `NATIVE_WRITABLE_CANDIDATE_DRIVER_START_VERIFIED` before
emitting `native_writable_candidate_driver_start_verified=true`. Native bindings
in this driver fixture are explicitly synthetic trusted inputs; it does not
claim current journal authorization or a full installation from that alone.

Local full runner race passed (journal 120.159s, transport 1.893s; other packages
cache hits). Driver race passed in 2.296s and platform regression in 8.063s.
Vet/default Linux staticcheck, Linux amd64/arm64 driver and fixture builds and
all 14 Python probe tests passed. Native evidence will be recorded after the
functional run. No production,
credentials, Status migration, server beta, schema, dependency/toolchain or public
SDK changes are included.

## Next implementation

Connect the current journal claim to this concrete adapter: bind opaque native
preflight before create/start, recheck current authority immediately before IO,
atomically settle actual opaque process evidence and add exact running-history
reads plus observation-only unknown-start recovery. Then wire actual readiness,
registration/handshake and managed activation/normal workers. Physical stop/
fencing, broker, SDK/shared shell/reference apps, update/uninstall/restore and
full P00–P14 acceptance remain incomplete.
