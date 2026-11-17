# Authenticated managed runtime profile and Portal compatibility input

This continues P04/P05 after document 76. Portal previously authenticated only the
runner's enrolled control identity. Its install requirements therefore had no
runner compatibility target and correctly refused every backend. This phase adds
fresh, authenticated configuration metadata and a Portal consumer for compatibility
matching. It does not remove the existing managed-install refusal or activate apps.

## Source map

| Source | Responsibility |
| --- | --- |
| `internal/apprunner/driver/candidate_runtime*.go` | Current platform/sandbox/root metadata from the exact configured provider |
| `internal/apprunner/transport/managed_runtime.go` | Canonical profile, internal wire validation and opaque copied observation |
| `internal/apprunner/transport/managed_runtime_linux.go` | Managed-only peer-authenticated probe and current enrollment/profile binding |
| `internal/app/app_platform_managed_runtime.go` | Current admin/runtime binding before and after network IO; compatibility matching |
| `internal/app/app_platform_managed_runtime_{linux,other}.go` | Concrete endpoint call or unsupported-platform refusal |
| `internal/app/app_platform_data_native_linux_test.go` | Real root runner/non-root Portal profile and existing installation regression |

## What the profile means

The managed endpoint reports the declared experimental runner target
`1.0.0-dev.1`, backend contract target `portal.backend/1`, actual native platform,
sandbox profile and data-root UUID/inode. Inode wire values are decimal strings,
including the full uint64 domain. These are configuration/protocol facts, not an
application response, capacity guarantee, writer lease or broker grant.

`execution_ready` is explicitly false. Version/platform compatibility alone never
establishes readiness, installation completion or permission to execute/activate.
The existing install API still rejects managed releases until their actual review,
staging, execution and readiness stages are connected. Prerelease target matching
continues to require explicit prerelease opt-in in manifest ranges. Portal CalVer
and the configured stable/beta channel are not protocol versions and do not change.

## Probe and provenance

1. Kernel peer authentication precedes the new `PRMQ0001` read-only query. Only a
   combined endpoint with the same concrete journal/data/candidate providers can
   answer; narrower listeners refuse.
2. The query is a fresh 32-byte nonce after its magic, followed by EOF. It creates
   no command, namespace, container or SQL intent. Existing 3-second read deadlines
   and cancellation apply; execution-session promotion does not apply here.
3. The root server reads its current enrolled public identity, obtains fresh
   configuration from the exact borrowed provider/root and compatible daemon, and
   rereads enrollment. Identity/key/revision changes during inspection refuse.
4. The reply is `PRMP0001`, the exact nonce, a bounded (4 KiB) canonical profile and
   public controller identity, plus current observation time. No seed or host data
   pathname is sent.
5. The client validates its independently configured public authority before IO,
   requires the protected endpoint/kernel server UID, exact nonce/magic/size/EOF,
   canonical metadata and the same controller deployment/epoch/revision/key.
   A future or pre-request observation timestamp refuses.
6. The resulting opaque observation retains receive time, endpoint profile and
   copied public authority. JSON and zero values cannot mint provenance. Profile
   values and public-key getters do not alias private proof state.

The runtime profile protocol is provisional and not a public SDK freeze.

## Portal consumption

`observePlatformManagedRuntime` requires a current administrator and enabled,
explicitly confirmed runtime binding. It loads the current controller epoch,
binding revision and sealed key's public identity in a short actor/trust
transaction, clearing the temporary decrypted seed immediately. It performs the
OS-authenticated probe outside all SQL transactions, then rechecks the same admin
authority and exact runtime binding in a second short transaction. Expired/stale
observations, changed roles/identity/OU authority, disabled/replaced runtime or a
private test endpoint refuse. No review, installation, effect, grant or runtime
enablement is written by this method.

`platformManagedRuntimeCompatibility` accepts only those fresh root-controller
facts. It passes the observed runner/platform/backend targets to the existing
`EvaluateCompatibility`. Its result means matching declarations only. Current
required-capability, publisher/review, lease/fence, data, native execution and
readiness checks remain separate and mandatory. No backend architecture is
selected from a browser value or hardcoded to the Portal process architecture.

## Verification

Common tests validate canonical metadata, uint64 precision, invalid root/protocol/
platform, forbidden ready=true, unknown/trailing fields, copied getters and opaque
JSON reset. Linux framing tests cover a synthetic private-profile reply, nonce/
magic/length/truncation/trailing errors, stale/future timestamps, changed authority
and a read-only listener's refusal. They prove framing only, not native facts.
Portal unit checks reject caller-created zero facts and prevent missing-runtime
probes from creating application authority.

The owned quota workflow passes the configured Docker socket to its existing real
Portal acceptance. The root fixture opens the combined endpoint with the shared
journal/data provider. Its separately executed non-root Portal child then:

- Uses its current confirmed runtime binding and admin session to obtain an actual
  native profile, requiring the native platform and execution_ready=false.
- Matches a sample managed declaration against that observed target, while proving
  the existing install requirements still refuse managed admission.
- Continues the prior actual data dispatch, static stage/activation/launch and
  background installation checks, so the probe cannot substitute for installation.

The parent requires the child's `MANAGED_PROFILE_OBSERVED` marker. The script emits
`native_portal_authenticated_managed_profile: true` only after that parent test
actually passes with the marker. Functional source
`71cadb7f17c992052977ce9a343f904d0864bda6` passed quota workflow
[40609387623](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40609387623),
job 122556907621. The completed enforcement log reports that Portal profile key
and owned-resource cleanup as true; execution_ready remains false. Linux transport
race checks passed in 4.469 s. General CI and daemon checks are separate.

Local full runner/CLI race checks passed (journal 79.429 s), focused Portal race
checks passed (2.462 s), all 13 Python probe tests passed, and Linux vet,
repository-pinned staticcheck plus amd64/arm64 compilation passed. These checks do
not complete managed review, stage execution or application readiness.

## Remaining work

Managed review/admission, signed candidate intent and stage settlement, artifact/
socket staging and automatic runtime startup still need integration. Native
unknown-effect reconciliation, real readiness, writer/broker access, registration,
lifecycle and reference-app acceptance also remain. Execution readiness stays
false. No schema, storage layout, production/beta configuration, real credential,
Status migration or SDK freeze changes in this phase.
