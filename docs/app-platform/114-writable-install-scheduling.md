# Writable installation queue and bounded startup readiness

This follows [113](113-writable-install-worker.md). It adds a private explicitly
bootstrapped writable queue/service mode around the claimed-operation driver.
The portable frontend startup service retains its original constructor; this
change does not enable native execution in production, alter beta configuration,
introduce tables, change dependencies or adopt Status.

## Queue eligibility and ownership

`claimNextPlatformWritableInstall(ctx, worker)` selects only the separate writable
review format. The existing frontend/native-static selectors remain distinct.
The SQL selection is a bounded hint (oldest 32 rows); values are bound parameters
and only closed internal constants supply SQL clauses. A writable hint requires:

- Initial install, managed flag 1, source generation 0, target generation 1.
- No completion, and queued/validate/download/stage/prepare_data/start/probe/activate.
- Absent or expired lease; enabled/pending installation at generation 1.
- An installation runtime and the same current controller epoch on both records.
- The canonical writable projection's format prefix.

The shared transaction retains OU/publisher locking and runtime preflight before
installation/operation locks. Re-read actual operation state, generation/family,
lease expiry, installation eligibility and review profile after locking. No seed
is retained by selection. Complete install authority, reviewed runtime/release,
archive, sponsor, publisher, capabilities and audience checks still run through
the original admitted-install validator. The prefix alone grants no authority.

A genuinely side-effect-free expired initial operation may be claimed with a new
fence, row/installation revision and worker lease. Repeat final current authority
and lease checks before commit and record the usual claim audit. Another worker
cannot steal a still-live lease; the previous token cannot heartbeat after takeover.
Static/frontend selectors cannot adopt writable work.

## Expired or exhausted work is retained honestly

A namespace, effect, candidate, access grant or granted capability is execution
residue. A writable start/probe/activate stage is also treated as possible native
work even if resource rows are missing. The selector must not reclaim such work
for dispatch. Instead it performs the existing denial-only review transition:

1. Require the exact installation control fence and non-overflowing revisions.
2. For managed work, `platformInstallReviewRuntime` reconstructs the consumed
   canonical writable projection and verifies its digest, operation request
   digest, controller epoch and writable sandbox profile. Initial review
   installation_id remains NULL; the operation supplies the actual installation.
3. Require that installation's runtime to equal the exact reviewed runtime.
   Reject readonly managed families and malformed/mismatched bindings.
4. Require expiry for queue recovery, or the exact live worker token for retry
   exhaustion. Retain all original effects, receipts, data and candidates.
5. Classify no-effect work as review_required; possible/existing effects as
   recovery_required. Increment the installation fence/revision and close the
   operation with an audit, clearing its owner/lease atomically.
6. Recheck the database clock/control state at commit.

The installation CAS pins the runtime as well as epoch/revision/fence. Existing
static failure paths still require NULL runtime. The failure helper deliberately
does not require a revoked runtime to become usable again before its work can be
denied. It creates no new execution or viewer authority.

This database fence prevents later Portal admission; it does **not** prove a
running process has physically stopped or that its mounted writer is revoked.
Physical stop/fencing and subsequent concrete recovery are separate unfinished
lifecycle work. Never report residue as cleaned or restart an unknown mutation
just because a new worker exists.

## Explicit service mode

`newPlatformOperationEngine` (built on `newPlatformWritableInstallScheduler`) requires the existing
valid explicit native endpoint, worker UUID and interval bounds. Empty frontend bootstrap is refused.
Construction starts no goroutine. `serve` retains its supported-Linux and exact non-root controller
UID gate, single-start guard, cancellation/join behavior and serial gate.

This scheduler is the platform's **operation engine**, not only the writable install worker, and
production runs it. When a trusted runner endpoint is bound at startup (`RP_APP_RUNNER_*`, validated
at startup), the installer service starts it beside the frontend scheduler, with its own worker
identity, and stops and joins both at shutdown. With no endpoint bound only the frontend scheduler
runs. Neither scheduler's failure to start prevents the other from running; each failure is logged.
In one serial loop the engine runs, in this order of selection:

- writable installs;
- managed updates, repairs and enables;
- delete-data;
- managed uninstall and disable;
- managed restore;
- while a restore pause is on (and nothing else): recovery imports and resumes, and the reconcile of
  imports and resumes it left unsettled;
- when idle: the settlement of failed migrations and open starts, of image loads and image
  removals, and the reclaim of images no release references.

Frontend (static) operations belong to the frontend scheduler. The runner-side generation-1
service-task registration kind that the engine's installs need ships with it (doc 170).

### A runner that cannot be reached

Failing to reach the runner (control socket absent or its runtime directory gone, connection
refused or not accepted) is not a verdict on any operation: nothing was sent. The engine therefore
never counts such a failure toward an operation's attempt budget (three). It backs the whole tick
off with a bounded exponential delay (the interval doubling to 30 s, +-20% jitter), renews the
active operation's lease each tick so a long outage never lets it lapse into review, probes the
socket first on every tick while it is down, and resumes the same operation when the runner answers.
Only definite runner verdicts, and failures after the runner was reached, count. The transport marks
the former with `transport.ErrRunnerUnreachable`; a process-wide count of such dials backs the
classification up where a stage wrapped the error without `%w`. Logging records state changes only:
one line when the runner becomes unreachable, a reminder at most every five minutes, one line when it
is reachable again; never one per tick or per item. A runner that dies after receiving a command is
not covered: that stays an ordinary failure, read back by effect id as before.

A writable tick selects its own queue, renews only at a serial boundary, invokes
the writable driver and retains the last confirmed result token. No concurrent
heartbeat races stage commits. Success clears ownership locally after actual
activation; three failed attempts use the same exact review/recovery settlement.
An expired token is discarded, so a later selection reviews possible effects
instead of silently continuing them. Reviewed work cannot re-enter the queue.

## Startup waiting is observation-only

A controlled app performs its actual 256 MiB / 65,536-inode quota checks before
opening its readiness socket. Three fast static-style retries could otherwise
misclassify normal startup as installation failure. Writable scheduler mode uses
`waitPlatformWritableCandidate` at probe:

- Its context is bounded by both the parent attempt's 20-second deadline and a
  10-second startup wait. Each unsuccessful native observation waits 200 ms with
  a cancellation-aware timer; there is no sleep after cancellation.
- Every iteration re-prepares the current strict source and verifies the exact
  root runtime. Authority, lease, runtime or source preparation failure returns
  immediately. No stale snapshot becomes a polling permit.
- Only the concrete native readiness observation is retried. It performs no
  writer, start, delivery, restart or data repair.
- After one valid observation, call the readiness commit exactly once and return
  its result, including any SQL/audit/source error. Never treat a failed commit
  as startup latency. The existing proof freshness/current-source checks remain.
- The caller retains its last confirmed lease on timeout/failure. A later serial
  tick may renew its still-live token and observe again; it cannot repeat an
  uncertain native mutation as a readiness retry.

The direct driver retains its previous single-probe behavior for independently
controlled callers. Startup-wait mode is selected only by the writable scheduler.
No user-supplied mode, callback or readiness flag is exposed.

## Tests and exact scope

The SQL queue fixture creates canonical reviewed/runtime metadata; its historical
source receipts are synthetic and never establish native success. SQLite and
PostgreSQL suites cover:

| Case | Required result |
| --- | --- |
| Family separation | Static/frontend queues ignore writable work |
| Initial claim | Exact queued operation and first fence |
| Competing live claim | No replacement lease or storage change |
| Expired no-effect claim | New fence; old heartbeat denied |
| Expired writer/start/probe/activate | Recovery state, retained receipts, no grants |
| Revoked runtime with residue | Recovery denial still possible; no native dispatch |
| Sponsor/publisher revocation | No-effect operation denied for review |
| Runtime/review/request/restore mismatch | No execution lease or new resources |
| Failure binding mismatch/readonly source | No failed-state or evidence mutation |
| Retry exhaustion | Same pending effect retained; lease closed; later tick idle |
| Cancellation/empty bootstrap | No claimed work or storage mutation |
| Readiness runtime revocation | Immediate refusal, no deadline wait or mutation |

The dedicated CI step runs these cases plus legacy queue/retry regressions before
the existing vulnerability gate:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 -count=1 ./internal/app \
  -run '^TestPlatform(WritableQueue(Claim|Recovery|Authority)|WritableFailureBinding|WritableSchedulerRetries|InstallQueue(Claim|Recovery)|InstallSchedulerRetries)(SQLite|Postgres)$|^TestPlatformWritable(SchedulerCancellation|ReadinessWaitRevocation)SQLite$'
```

Disposable native acceptance changes the original real reviewed writable fixture:
start the actual non-root `serve` loop with a long interval, let it select its own
queued operation and advance artifacts/data, then inject the existing writer-
intent audit fault. Wait for retry_pending, cancel and join the service before
reading its mutable state. Require one failed attempt, the same admitted operation,
first fence, start-stage progress, one actual data effect and no candidate. No
manual operation claim or stage advancement supplies this progress. Continue the
existing independent writer/start/registration/activation failure tests using
that exact retained token and original data/process/file identities.

The original readiness fixture now uses the actual bounded waiter before its
audit rollback test. It must return the commit error before its 10-second wait
expires; after removing the fault it succeeds through the same waiter. Parent
and Python report require `PORTAL_WRITABLE_SCHEDULER_CLAIMED_PROGRESS` and
`PORTAL_WRITABLE_READINESS_WAIT_CONFIRMED`, producing two additional report flags.
Prior native/protected-file/quota/cleanup checks remain mandatory.

This proves actual background claiming/progress and bounded real readiness, with
an intentionally interrupted background attempt. It does **not** yet prove an
uninterrupted background queued-to-ready install, generic browser opening, restart
reconciliation or full lifecycle acceptance. P05/LIFE-02 and LIFE-03 progress must
not be confused with full P00–P14 completion.

Local focused queue/legacy tests, expanded race checks (16.158s; final-source
recheck including failure bindings 17.912s), failure-binding checks (0.795s), Linux amd64/arm64 builds, Linux vet/staticcheck and Python14 passed.
Functional source `966f478d72c844598be610f0aa2bef49a5b63b09` passed
[quota workflow 40831373753](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40831373753),
job 123306128129. Checkout verified its merge into parent `29d30ebc`.
The report has both new background-progress/readiness-wait flags true, all prior
native checks true, full Portal quota witness true and owned cleanup true.
Disposable registry cleanup succeeded; `execution_ready=false` remains intentional.

General workflow 40831373762/job 123306128447 passed explicit SQLite/PostgreSQL
lifecycle SQL (74.213s), current/legacy access SQL (37.602s), worker/static SQL
(14.164s) and queue/scheduling/legacy SQL (14.940s). Build/vet/full Linux
staticcheck, published market and arm64 numeric checks passed. The later existing
x/net v0.60.0 GO-2026-6866/6860 vulnerability gate failed; ordinary full tests
were skipped. No dependency change or suppression is included. All three app
race shards and aggregate job 123308112561 passed; shard 1/job 123306128452
ran full journal race checks (239.214s) and its Portal subset (71.878s). Web
checks, workflow lint and Docker font/smoke checks also passed. No standalone
daemon workflow was triggered by this Portal/fixture-only change. Results apply
to the named functional source; later documentation-only heads can trigger CI
again and must not be confused with that completed evidence.

## Next vertical slice

Demonstrate a fresh managed app completing queued-to-ready entirely through the
service, including slow startup, exact current access and independent owned
cleanup. Add cancellation during startup and restart/expiry recovery evidence.
Then connect generic managed launch/assets/bridge to the shared browser shell;
continue physical lifecycle/broker/tasks/SDK/marketplace reference-app work.
Production rollout, public SDK freeze and Status adoption remain separate gates.

## Addendum 2026-11-21: the console plans the writable format

Up to v2026.47.6 the console's `POST /plans` planned a runner-backed install as
`portal.managed-install-review/1-dev.1`, a format only test drivers execute; the operation engine
claims only `portal.writable-install-review/1-dev.1`, so such an install stayed `queued` with no
log line. Every automated path that ran installs to completion called the writable review functions
directly and never went through the route.

- `POST /plans` now plans the writable format, and refuses a runner-backed install
  (`app_platform_requirements_unavailable`) when the runner does not advertise the writable sandbox,
  since nothing would run it.
- `POST /operations` refuses a managed-format plan (`app_platform_plan_changed`); the administrator
  plans again.
- The operation engine fails a queued, effect-free managed-format install with error code
  `install_format_unsupported` (audit row, one log line) and removes its installation, since it took
  no effect. An install queued by an earlier build is therefore closed by the first fixed build, and
  the administrator plans it again; nothing needs cancelling first. An install it cannot close (one
  with effects, or a row it cannot read) is left alone and logged once, and the engine carries on.
- `TestConsolePlansARunnerInstallTheEngineClaims` guards the route in every CI run (only the runner's
  observation is faked); the capacity soak and the native completion test now also install through
  the console routes against a real runner.
