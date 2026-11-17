# Concrete native candidate launch and opaque observations

This combines the previously separate image, mount and start adapters into a
concrete Linux-native launch API. It provides the native evidence boundary needed
by the next journal receipt integration. It does not yet persist that receipt,
connect a production runner dispatcher or activate a managed application.
No schema, persistent layout, credentials or production/beta configuration changes.

## Source map

| File | Responsibility |
| --- | --- |
| internal/apprunner/driver/candidate_runtime.go | Typed infrastructure request and opaque launch/running handles |
| internal/apprunner/driver/candidate_runtime_linux.go | Concrete quota/image/mount verification, one-shot native create/start |
| internal/apprunner/driver/candidate_runtime_other.go | Explicit unsupported behavior off Linux |
| internal/apprunner/driver/candidate_runtime_test.go | Nonforgeable JSON/zero handles and typed request validation |
| internal/apprunner/driver/candidate_runtime_linux_test.go | Actual XFS-root plus Docker launch fixture |
| scripts/acceptance/project_quota_probe.py | Invoke that fixture only on its newly owned loopback filesystem |
| .github/workflows/app-project-quota-probe.yml | Compile/invoke driver fixture with a public digest-pinned inert image |

## Trusted configuration and borrowed provider

`OpenCandidateRuntime` receives explicit infrastructure paths, the shared concrete
`data.Root` provider and its independently established filesystem UUID/root inode.
It accepts only the supported Linux rootful architecture, checked root-owned
Docker Unix socket and canonical private data root. Component-wise directory
opening and provider identity must agree. It creates no directory and repairs no
mode, ownership, filesystem or quota configuration.

The existing quota provider exclusively owns its physical root. The runtime
therefore borrows that provider instead of opening a second owner or bypassing
its lock. Its infrastructure owner must keep it alive until all runtime work has
settled. Runtime.Close does not close the shared provider. Closing the provider
unexpectedly makes fresh checks fail, including post-effect uncertainty; it never
creates a successful observation from missing evidence.

Runtime locks serialize preparation/start/cleanup/close. At most 32 live launch
handles are retained. Close invalidates their pins after in-flight synchronous
runtime work settles. Launch.Close releases that handle's descriptors without
stopping a running process. Process lifetime is a separate owned lifecycle action.

## Read-only preflight

`PrepareCandidate` takes a typed identity and exact descriptor bytes from trusted
infrastructure. It accepts no arbitrary command, Docker configuration, credential
or app-selected host path. Strict descriptor parsing binds the expected release,
backend and native declared platform. Resource limits come only from that release.

The concrete quota provider rereads the approved root/namespace/project and
requires the exact device/inode, byte/inode limits, project inheritance and valid
usage below those limits. Namespace/data identities must match the independent
evidence supplied by the caller; a stale or differently mapped namespace refuses.
The root pin remains bound to the configured pathname and to the provider's
physical identity, rather than just a matching child basename.

The driver then inspects the digest-pinned local image, builds the closed create
policy and pins all existing data/HTTP/broker mount directories. It rechecks quota
state before returning a live handle. No Docker create/start is performed by
preflight. Missing socket directories or a noncompliant data root refuse instead
of triggering unreviewed provisioning.

The returned `CandidateLaunch` has private owner/state/pins. Its snapshot contains
identity, native image ID, platform, policy hash, a path-free mount identity hash
and concrete data evidence. Snapshots are internal typed metadata, not a public
browser/wire contract or authority recovered from JSON. The later journal binding
must convert them to its validated versioned receipt representation and persist
the exact snapshot under current signed claim authority before any effect.

## One native invocation

`StartCandidate` accepts only a live launch owned by the same runtime. It rechecks
context, data/quota evidence and mount pins, then consumes the handle before the
first possible Docker effect. Copies cannot cause a second create or start.
The driver uses the checked single create and pinned initial start adapters.
Post-start quota/data and cancellation checks must also succeed before returning
a native observation. Errors after possible effects remain uncertain; the helper
never guesses an ID, retries or silently removes a possibly created process.

`CandidateRunningObservation` is constructed only after that concrete path sees
consistent running state. Its private validity bit cannot be set through a caller
struct literal, and neither native handle permits JSON marshal/unmarshal into
executable/native-result authority. Zero values refuse snapshots. A copied public
snapshot is historical data and cannot be passed back as an opaque observation.

The observation includes the preflight snapshot, exact native ID and observation
time. It is not application health, writer authority or activation. A running
process can exit later, so future readiness and durable reconciliation need fresh
owned observations. No production API is added for cleanup; the private support/
test helper stops/removes only the exact observation/plan under its originating
runtime and retains all existing ownership checks.

## Combined Linux/XFS/Docker fixture

The quota workflow now compiles the driver test binary, resolves the official
Kubernetes pause fixture to a repository digest and supplies explicit test-only
binary/image/socket metadata to its existing disposable loopback probe. Only the
closed official image reference is accepted by the probe. Partial configuration,
floating tags and relative binary/socket paths refuse before new resources.
Existing opt-in, Linux-root and GitHub-hosted guards remain mandatory.

The script creates a separate private candidate root on the newly owned XFS
filesystem and provides independently read filesystem UUID/root inode. The native
test uses the real quota provider to prepare a synthetic namespace/project, sets
up private ephemeral socket directories and runs the concrete API. It verifies
changed data evidence refusal, matching preflight/running snapshots, one-shot
invocation, owned cleanup, close invalidation and continued shared-provider access.
The inert image does no application data workload; existing hard-quota workload
tests remain separate. Cleanup uncertainty still requires disposable-worker
teardown rather than an unowned forced cleanup.

Native fixture output must actually contain its PASS record before the evidence
report sets `native_candidate_quota_mount_start_once`. Ordinary portable skips,
Linux cross-compilation or a fabricated snapshot are not native acceptance.
The workflow runs this before its later remount/UUID replacement phases, and
retains the existing owned mount/loop cleanup checks.

## Remaining integration

Bind the preflight snapshot into the existing durable candidate claim before
StartCandidate, then atomically persist the opaque running observation as the
resource's native identity and a typed command receipt. Define candidate recovery
for lost create/start/settlement acknowledgements. Provision/check socket service
identity, implement authenticated readiness, the data access/active-writer
transition, backend registration and broker before managed installation acceptance.
No production enrollment, SDK freeze, Status migration, D5/D6 acceptance or full
platform completion is inferred from this native API.

The runtime also requires the existing daemon metadata profile at open and again
before native effects. Rootless, user-remapped, incompatible cgroup/seccomp or
other unsupported metadata refuses. This does not turn the metadata report's
execution_ready flag on or substitute for complete sandbox enforcement evidence.

## Local validation record

Portable runner/CLI race regression passed (journal 69.319 seconds); final driver
race tests passed in 1.642 seconds. The probe configuration/refusal suite passed
13 Python tests. Linux-target vet/staticcheck and Linux amd64/arm64 driver
compilation passed. New concrete quota-plus-process execution is Linux-only and
still requires its actual disposable workflow result; portable skips are not
recorded as native evidence.

## Native combined execution evidence

Functional source 8ae37acf67ec813ab49c1f03926c91de807c8e30 passed the actual
combined quota workflow 40455635442. Its owned XFS report recorded
native_candidate_quota_mount_start_once=true and owned_resources_cleaned=true.
That key is set only after the exact native driver test passes, including actual
quota/root checks, pinned launch, one-shot refusal, owned process cleanup and
shared-provider close behavior. The existing enforcement, receipt transport and
filesystem replacement/cleanup phases also passed.

This is a native inert-process observation boundary, not persisted journal
settlement or complete managed-app readiness. Main CI separately found a shell
style issue in repeated GITHUB_ENV redirects; the workflow now groups those
writes without changing the native functional source. That correction requires
its own workflow lint check; no full main-CI success is inferred from native
acceptance alone.
The corrected workflow passed local actionlint v1.7.12, including ShellCheck.
The same functional source also passed daemon workflow 40455635317. These native
results are recorded by commit and workflow, independently of pending general CI.
