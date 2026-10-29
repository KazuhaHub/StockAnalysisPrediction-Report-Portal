# Claimed writable installation driver

This follows [112](112-writable-activation-access.md). The individual writable
stages now have a bounded driver for one already claimed first installation.
This is development infrastructure, not production enablement. Queue selection,
automatic background scheduling and generic managed browser launch remain
unfinished. No schema, dependency, deployment or beta-channel change is included.

## Entry and transitions

`runPlatformWritableInstall(ctx, lease, endpoint)` accepts only an install action,
managed generation 1, an explicit native endpoint and the separate writable review
format. Cancellation is checked before any query and between stages. The profile
query chooses the execution family; it is not authorization. Every existing stage
independently checks the current bound lease, review, sponsor, publisher, runtime,
restore epoch, source and evidence before committing or performing native IO.
The static driver continues to use its existing family and behavior. Both return
the internal `platformInstallRun` result shape.

A maximum of eight transitions covers the longest initial path:

| Current stage | Action | Successful next stage |
| --- | --- | --- |
| queued | Verify committed install/release/artifacts | validate |
| validate | Repeat artifact authority checks | download |
| download | Repeat stored immutable artifact checks | stage |
| stage | Complete database-resident staging | prepare_data |
| prepare_data | Dispatch and verify real data preparation | start |
| start | Confirm writer, dispatch/observe exact native start | probe |
| probe | Collect and commit real current readiness | activate |
| activate | Deliver/observe registration; verify boot; activate atomically | complete |

The artifact transitions verify already staged immutable repository content;
this does not implement a new remote marketplace downloader. Readiness is one
bounded call, not an internal polling loop. A startup not yet ready returns its
last confirmed token for the caller's subsequent bounded attempt. No branch uses
a historical data/start receipt as current readiness or a current host boot.

## Return values and resumption

On failure, return the last **confirmed** lease, never a zero token returned by a
failed sub-operation. Artifact/data/start/probe commits replace the token only
after successful acknowledgment. Writer settlement retains the existing token.
Registration may increment the operation row revision without leaving activate;
the driver immediately records its returned token before checking boot/activation.
If activation then fails, its caller receives the registration revision and must
not repeat work with the earlier token. Successful activation returns its actual
activation result and a zero lease; a completed worker has no further authority.

A start effect can already exist after a lost native response or Portal rollback.
The original writer reader deliberately accepts only its two-effect source, so
blindly running it again would reject the separately prepared start. The driver
reads the count of start effects only as a routing hint:

- Zero: run the existing writer dispatch, then the existing start dispatch.
- One: enter the strict start dispatch directly. It revalidates the applied
  writer and exact sole start effect before native IO.
- More than one: refuse. Never delete/rewrite effects to make a source fit.

A concurrent source change cannot turn this hint into authority: both dispatches
repeat their transactional source checks. The root dispatcher returns an exact
historical applied result where permitted; an unknown effect cannot become a new
writer/start permit. Recovery remains its distinct observation-only API. This
driver does not initialize or repair keys, enroll runtimes, renew/reclaim leases,
repeat uncertain writes, restart containers, clean retained data or claim that
failed native work has physically stopped. Its caller owns timeouts and scheduling.

## Verification and reproduction

SQL fixture histories exercise metadata/refusal behavior only. They do not mint
opaque native proofs or establish successful installation. The worker tests run
on SQLite and, through CI's PostgreSQL service, PostgreSQL:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 -count=1 ./internal/app \
  -run '^TestPlatform(WritableInstallRun(Pending|Refusals)|StaticInstallRun(Progress|Activation))(SQLite|Postgres)$'
```

The CI step `Writable installation worker` runs before the existing vulnerability
gate, so database coverage remains observable even when that later gate fails.
Pending writer/prepared-start/probe/registration cases repeat attempts against an
absent endpoint and require unchanged storage, exact retained token and no fake
activation. Refusals cover cancellation, absent endpoint, static/readonly family,
generation/action/stage, expired lease, sponsor/runtime/restore revocation and a
prepared start with a corrupted writer receipt. The latter must fail source
validation, proving start routing does not bypass the writer checks.

Disposable Linux quota acceptance extends the existing real reviewed writable
installation instead of introducing unowned extra containers:

1. Start its driver at queued. Inject a writer-intent audit failure after actual
   artifact/data completion. Require the returned start token, one real data
   effect and no candidate; remove the transaction fault to continue.
2. Inject writer settlement failure after actual root writer completion. Require
   the unchanged start token and original rollback state; independently read and
   settle the original native receipt using the existing acceptance checks.
3. Run the driver through the prepared-start path with a start settlement fault.
   Require retained token/source; independently verify original native identity
   and complete the existing historical-result path.
4. At probe, verify actual readiness and run the driver with a readiness audit
   fault. Require the exact original lease and no partial readiness commit.
5. At activate, run the driver with registration settlement failure. Read its
   actual original registration result. Then remove that fault, inject activation
   audit failure and run the driver again: registration commits, activation rolls
   back, and the returned row revision must be the new registration revision.
6. Resume with the real applied registration. Activation audit failure preserves
   pending/candidate/no-grant state. Remove the fault and require actual complete/
   ready/audience state, discarded lease and original data/process/file identities.
   Continue existing current-user/OU requests and post-IO revocation acceptance.

The quota report requires seven `native_portal_writable_worker_*` flags for these
boundaries, in addition to existing full physical quota, protected-file, native
identity and owned-cleanup checks. This proves interrupted stages of one concrete
installation, with explicit independent observations between failures. It is not
an uninterrupted automatic queued-to-ready service or a public browser launch test.

Initial functional source: `aaceb17c5ddc3d2ffc7a150639424edddd6d6e4f`.
The first native workflow 40828635821/job 123297423523 failed because the parent
Portal fixture did not relay the new child worker markers to the Python report.
The first relay edit `2b1859bc` placed its checks before the output buffer existed
and failed Linux compilation. Source `5f32c95095cfe11e48ac9e00daa43c964e5754e7`
moves those checks after child completion, outside resource cleanup; both Linux
architecture builds and vet/staticcheck passed again before final acceptance.
Neither failed native run is accepted as final evidence.

Final source `5f32c95095cfe11e48ac9e00daa43c964e5754e7` passed
[quota workflow 40829332469](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40829332469),
job 123299658093. Checkout verified its merge into the parent `eb48a2c6`.
The downloaded JSON report has all seven worker flags true, all preceding native
checks true, full Portal quota witness true and `owned_resources_cleaned=true`.
Disposable registry cleanup also succeeded. `execution_ready=false` remains
intentional: these acceptance results do not enable a production runner.
Local focused SQLite checks passed (1.112s), worker/static race checks passed
(9.180s), broader writable/legacy access SQLite checks passed (9.470s), Linux
amd64/arm64 test compilation and Linux vet/staticcheck passed, and all 14 Python
quota-probe tests passed.

General workflow 40828635851, Go job 123297423674, passed the explicit writable
lifecycle SQLite/PostgreSQL suite (52.269s), activation/current-access and legacy
access suite (22.154s), and worker/static-driver SQLite/PostgreSQL suite (8.339s).
Build, vet, full Linux staticcheck, published market and arm64 numeric checks also
passed. The later vulnerability gate failed the existing `golang.org/x/net`
v0.60.0 GO-2026-6866/6860 findings (reports fix v0.62.0); ordinary full tests were
skipped. No dependency change or suppression is included.

The same explicit suites also passed on final source `5f32c950` in general
workflow 40829332473/job 123300058040: lifecycle SQL 62.589s, current/legacy access
SQL 29.017s and worker/static SQL 10.856s. Build/vet/full Linux staticcheck,
published market, arm64 numeric checks, web checks, Docker smoke and all three app race shards/aggregate passed. The Go job again failed only the existing vulnerability gate
and skipped ordinary full tests. Shard 1/job
123300058092 passed full journal race checks (232.159s) and its Portal app subset
(141.991s); the race aggregate job 123302350080 passed. No standalone daemon
workflow was triggered by this Portal/fixture-only change. These results apply to
the named functional source; later documentation-only heads may trigger CI again.

## Acceptance scope

This is a P05 implementation continuation. It advances LIFE-03 stage failure/
resumption and the installer portion of LIFE-02. It does not complete LIFE-02's
clean-market installation/opening requirement or all of LIFE-01..LIFE-05. Existing
idempotency, lease and concurrency contracts remain independently required.

## Next implementation

Add an explicitly bootstrapped writable queue/scheduler using this driver. Select
only canonical writable reviews, retain exact runtime/fence/epoch binding, and
refuse silent takeover of expired work with resources. Exhausted/uncertain work
requires an honest retained-resource recovery state; a database fence alone must
not be described as physical writer revocation. Keep the existing portable static
startup service independent. Then connect managed browser routes, seamless SDK
shell and remaining update/disable/uninstall/recovery/broker/reference-app work.
The scheduler must not blindly reuse the static worker's three rapid retries
for startup readiness: the controlled backend intentionally completes its actual
256 MiB / 65,536-inode quota probe before opening its socket. Use an explicit
bounded startup wait/backoff for read-only readiness probes, within the exact
live lease and attempt deadline. Each probe must revalidate current authority;
never retry start to wait for readiness. Renew only at serial boundaries so a
heartbeat cannot race revision-changing stage transactions. Test cancellation,
slow successful startup, deadline exhaustion, expired ownership and process
identity retention with actual native execution. A timed-out mutation remains
uncertain until its concrete historical/observation-only reader proves otherwise.
Production rollout and public SDK freeze remain separate approval gates.
