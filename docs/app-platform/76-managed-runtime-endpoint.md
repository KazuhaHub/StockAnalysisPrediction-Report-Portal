# Shared managed runtime endpoint

This continues P04/P05 after document 75. One provisioned endpoint can now serve
data preparation and candidate execution using the same shared runner journal and
concrete data provider. It removes the need for separate data/start socket
configuration in later Portal integration. No production listener starts here.

## Source map

| Source | Responsibility |
| --- | --- |
| `transport/managed_controller_linux.go` | Explicit combined constructor on one protected root/controller socket |
| `driver/candidate_runtime_linux.go` | Exact borrowed-provider object and root-pin validation |
| `driver/candidate_runtime_other.go` | Unsupported-platform refusal |
| `transport/managed_controller_linux_test.go` | Zero/missing configuration refuses without a socket |
| `transport/candidate_execution_native_linux_test.go` | One real endpoint: cross-UID data preparation, native launch and replay |
| `transport/data_receipt_native_linux_test.go` | Explicit fixture-only data execution child mode |

Paths are relative to `internal/apprunner/`.

## Construction and ownership

`ListenManagedController(ctx, path, uid, gid, store, root, pin, native)` requires:

1. Root server execution, valid context and configured concrete providers.
2. The exact `*data.Root` object borrowed by the native runtime, not a different
   object with similar pathname or UUID text.
3. The exact root identity supplied when that runtime was opened.
4. A currently available provider and compatible daemon under the existing checks.
5. The existing protected root/controller socket profile and explicit non-root
   controller UID/group.

`CheckSharedRootConfiguration` checks object/pin equality under the runtime lock,
then uses the existing read-only provider/daemon validation. `ListenDataController`
revalidates the root and creates the one endpoint. Before `Serve` starts, the
combined constructor installs candidate execution with that same journal. Neither
channel can accidentally receive another journal pointer through this constructor.

The caller retains all provider ownership until `Serve` has stopped its sessions.
Runtime close never closes the borrowed shared data provider. Configuration checks
are infrastructure availability checks, not `execution_ready`, writer admission,
application health or permission to activate.

## Capability sets and protocols

Existing narrower constructors retain their capability sets. Read-only listeners
are not upgraded, and candidate-only/data-only listeners remain available for
bounded infrastructure use. The new constructor explicitly enables both typed
operations and their historical receipt reads on one endpoint.

The existing data and candidate protocols keep separate magic, signed command
domains, effects, framing and receipt types. Kernel peer authentication precedes
routing. Candidate execution keeps its bounded descriptor/30-second session;
data and read deadlines remain unchanged. No command, native ID, filesystem path,
callback or success receipt can be selected by application code.

An application installation still needs the verified data receipt for the same
operation/generation before a candidate can be reserved or started. Sharing an
endpoint does not relax current signatures, original fences, retained root
history, pending-work barriers, one-shot execution or atomic receipt settlement.
Applied retries preserve existing native identities; unknown work refuses.

## Native acceptance

The owned quota workflow extends `TestNativeCandidateExecutionTransport`:

1. Begin with a fresh namespace that does not exist in the shared native root.
2. Use an actual configured runtime to reject a changed root inode and a different
   provider object, without creating a socket.
3. Open the combined endpoint once. Execute a separately launched non-root
   controller child to request signed data preparation through that endpoint.
4. Read the actual applied data receipt in the root fixture and independently
   inspect its native namespace/project/inode. Replay the data request and require
   the native observation to remain identical.
5. Through the same endpoint and controller identity, exercise the existing actual
   candidate launch, historical read and same-native-ID replay checks. UID/group,
   unsigned-frame and unknown no-create refusals still apply.
6. Require controller direct-journal DAC denial and full-owner/native-ID cleanup.

The fixture prepares no data through an in-process dispatch shortcut. It still
uses synthetic public test identities and an inert public image, not production
credentials or application business requests. Temporary socket mount directories
are fixture setup; automatic application socket staging remains separate work.

The probe emits these only after the exact native fixture reports PASS:

- `native_managed_endpoint_shared_provider_refusal: true`
- `native_managed_endpoint_data_and_candidate_cross_uid: true`

Functional source `265b9fdb00e5eb15b4900abb175eaf5c5b966e99` passed quota
workflow [40607439801](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40607439801).
Its completed job 122550353436 reports both combined-endpoint keys and
owned-resource cleanup as true. The Linux transport race suite passed in 4.810 s.
`execution_ready` remains false; general CI and daemon checks are separate.

Local full runner/CLI race checks passed (journal 78.726 s), all 13 Python probe
tests passed, and Linux vet/repository-pinned staticcheck plus amd64/arm64 test
compilation passed. These results do not complete Portal stage integration or
prove writable data, app readiness or activation.

## Remaining work

Portal signed candidate intent/stage delivery/receipt settlement, automatic
runtime startup/provisioning, artifact/socket staging, native unknown-effect
reconciliation, readiness, writer/broker access, registration and the complete
managed app lifecycle remain. Execution readiness stays false. No schema,
persistent layout, production/beta configuration, real credential, Status migration
or SDK freeze changes in this phase.
