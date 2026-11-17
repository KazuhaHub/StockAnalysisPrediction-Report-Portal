# Portal managed candidate startup and receipt recovery

This continues P04/P05 after document 78. The private managed installation path
now signs a durable candidate intent, dispatches actual native startup and settles
the authenticated running receipt into `probe`. An actual running process is not
application readiness. No public managed install route, app-use grant or active
application is created by this phase.

## Source map

| Source | Responsibility |
| --- | --- |
| `internal/app/app_platform_candidate_intent.go` | Current managed/data authority, stable candidate/effect records and lease-bounded signing |
| `internal/app/app_platform_candidate_stage.go` | Concrete dispatch, receipt-only recovery and atomic `start → probe` settlement |
| `internal/app/app_platform_candidate_endpoint_{linux,other}.go` | Root endpoint calls or explicit unsupported-platform refusal |
| `internal/apprunner/driver/candidate_sockets_linux.go` | Automatic fixed-name communication directory provisioning under the protected root |
| `internal/apprunner/journal/candidate_execution.go` | Provisioning inside the existing durable one-shot native claim |
| `internal/app/app_platform_candidate*_test.go` | SQL/intent refusals and actual owned native-process acceptance |
| `internal/app/app_platform_managed_install_native_linux_test.go` | Non-root Portal startup, failed-settlement recovery and no-access assertions |

## Intent and storage

The caller holds a current initial managed-install lease at `start`. Preparing
the intent performs no native IO. In a short transaction, it checks the current
runtime binding/key, controller epoch, publisher signature/trust, admitting admin,
lease/fence and exact reviewed target. It requires one active namespace and the
original applied data receipt under the same installation, runtime, release,
generation and fence. The canonical receipt must match its signed logical command,
root UUID/inode, schema, quota and namespace metadata. The receipt's hash becomes
an immutable input to later native result validation.

The existing activation table supplies the stable resource identity: one row with
`state=candidate`, `readiness_evidence={}` and `observed_ms=NULL`. This records the
intended candidate, not a running or ready assertion. The existing operation-effect
table records `stage=start`, effect slot 1, `kind=start-candidate`, logical digest,
runtime/epoch/fence and `state=prepared`. No new table, migration or metadata-key
storage is introduced. No app-use grants may exist.

A retry reuses exactly these two records. Partial rows, unknown effects, changed
data history, active/readiness-bearing candidates, takeover or stale authority
refuse. The start command derives all IDs, release digest, native platform,
sandbox and data/writer metadata from this validated state. Its signature expires
within 30 seconds and at least one second before the Portal lease. Temporary
decrypted seed and signing-key bytes are cleared. Browser input supplies neither
an executable path, native container ID nor proof of success.

## Actual native execution

`dispatchPlatformInstallCandidate` prepares the durable intent and probes the
managed runtime again, requiring the exact reviewed native target. It sends the
signed command and exact signed descriptor through the existing authenticated
root/controller execution channel. The root journal applies its admission and
data-history/fence checks, marks the effect uncertain, and issues its one-shot
candidate claim before any native mutation.

Within that existing claim, the concrete runtime now provisions fixed directories
below its root-owned socket base: installation UUID, generation, `http` and
`broker`. The two parents remain root-owned mode 0700; `http` is owned by the app
UID/GID 65532 mode 0700, and `broker` by root mode 0755. Creation uses held parent
descriptors, no-follow directory opens, exact ownership/mode checks and fsync.
Existing unexpected directories/symlinks refuse rather than being repaired.
Names never come from app paths. Cancellation refuses before new creation.

The normal read-only native preflight then pins data/image/policy/mount identities;
the journal binds that preflight and rechecks signed authority before actual
container creation/start. Uncertain failures retain the original effect and do
not recreate a native resource. Provisioning does not add a broker socket, app
credentials or writable-data authorization. Existing image inspection still
requires the reviewed image to be available locally; automatic image fetching
remains a separate required integration.

## Settlement and interrupted acknowledgement

Only the opaque root-controller `CandidateReceiptObservation` can settle startup.
Portal requires a fresh observation, exact endpoint/controller identity and command
digest, a receipt matching the signed candidate command, and the original data
receipt hash. The receipt's preflight must match the data root, project/device/
inode and quotas. A historical running receipt establishes no current app health.

The SQL transaction reloads current runtime, lease, actor/trust, intent and data
history, verifies the signed envelope and atomically marks the start effect
applied and advances the operation to `probe`, including its audit record.
Candidate readiness remains empty/unobserved, installation remains pending and
no active activation or access grant exists. Static activation cannot complete
this managed operation.

If native startup succeeds but Portal settlement fails, the original prepared
intent remains. `completePlatformInstallCandidate` renews the same logical command
under the current unchanged lease and reads the original receipt. It does not
request another start. Native execution replay also returns the same receipt and
container ID. Unknown native effects and takeover require reconciliation; this
phase does not supply general unknown-effect repair or automatic cleanup.

## Acceptance

Common SQLite/PostgreSQL tests use explicitly synthetic repository/data metadata
only to check signing, exact stable retries, candidate/no-grant state and refusal
of disabled runtime/key, inactive sponsor, restore pause, expired lease, takeover,
changed namespace/receipt, active candidate, unknown/partial effect and zero opaque
proof. They cannot establish native startup. Linux directory tests cover stable
identity, unexpected permissions, symlinks/traversal and cancellation.

The actual Linux quota fixture uses a public digest-pinned inert pause image. Its
non-root Portal child installs a signed managed package through the real data
stage, records the candidate intent and calls actual dispatch. A fixture-only SQL
audit failure rolls back Portal settlement after the root runner starts the
container. The child checks the retained prepared/start state, removes the failure
and recovers by receipt read to `probe`. Execution replay must match the persisted
receipt exactly; the application remains pending with no health or use grant.

The root parent independently checks the actual running container and every
ownership label against the authenticated fixture command, matches its native ID
to the journal receipt and deletes only that exact owned test container. The
acceptance script requires `MANAGED_INSTALL_CANDIDATE_PROBE` before reporting
`native_portal_managed_candidate_start_to_probe: true`.

Functional source `441a26083fee4c7e30999015ece5bb024bf2c8c5` passed native quota
workflow [40613886646](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40613886646),
job 122572074822. Managed candidate start-to-probe, managed data-to-start and
owned-resource cleanup are true in its completed enforcement log. Linux transport
and driver race checks passed in 4.540/1.492 s; the latter includes actual Linux
communication-directory creation/refusal tests.

Local focused SQLite/PostgreSQL race passed in 20.119/15.318 s. Runner/CLI race
passed (journal 81.723 s); Linux amd64/arm64 Portal test compilation,
vet/staticcheck and 13 Python tests passed. General CI 40613886608 passed web
checks/tests, workflow lint and Docker smoke. Its Go job failed at the existing
x/net vulnerability gate, skipping its later non-race test step; general CI is
not all green. The first app race shard and separate daemon workflow were still
running at this evidence capture.

This contributes partial LIFE-01/02/03 and RUN-03/07 coverage; it does not complete
market installation, full isolation, all failure states or application readiness.
General CI has an existing dependency vulnerability gate failure tracked in
document 78; this work does not change module or toolchain versions.

## Remaining platform work

Connect actual backend readiness, broker/registration and controlled writer
authorization before activation. Automatic verified image fetching, managed
scheduling/startup, bounded socket-storage policy, native reconciliation and
complete update/rollback/uninstall remain, followed by neutral reference apps,
market/SDK publication and the full acceptance matrix. No production deployment,
real credentials, beta configuration, Status migration or SDK freeze changes.
