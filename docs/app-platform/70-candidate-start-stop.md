# Owned candidate start and stop adapters

This extends the private closed container adapter from create/inspect/remove to
initial start and bounded stop. It remains unwired from production runner commands,
claims, receipts or installation activation. No schema, configuration, credentials,
server beta setting or application access policy changes.

## Source map

| File | Responsibility |
| --- | --- |
| internal/apprunner/driver/container_lifecycle.go | Fixed native-ID start/stop actions and post-effect observations |
| internal/apprunner/driver/container_lifecycle_test.go | Transitions, identity refusal, lost acknowledgements and current authority |
| internal/apprunner/driver/container_observe.go | Strict nested state projection and early cancellation |
| internal/apprunner/driver/mount_pins_linux.go | Pins held and checked across initial start |
| internal/apprunner/driver/container_linux_test.go | Explicit inert process fixture for real native lifecycle |
| .github/workflows/app-runner-probe.yml | Public official fixture preparation on disposable Linux only |

## Fixed lifecycle

Start inspects the exact native container ID and compares the complete existing
owner/policy/mount tuple before any mutation. A created container may receive one
POST to `/v1.47/containers/<native ID>/start`. Already-running state is returned as
a read-only observation; no new start is sent. Exited containers refuse: crash
recovery/restart admission must use its own current authority instead of silently
replaying initial start after a failure. No start is attempted for paused,
restarting, dead, inconsistent or unknown state.

Stop uses the same identity/policy observation. Created/exited state needs no
mutation. A running owned container receives one POST to the fixed stop endpoint
with signal=SIGTERM and t=10. Caller input cannot choose a command, force mode,
signal, timeout, host URL or Docker API path. This is Docker's normal bounded stop
procedure; the engine may kill a process after its grace period. The helper does
not expose an independent kill or forced-remove API.

Only 204 or 304 permits a subsequent observation; neither status alone is a
confirmed running/stopped result. After every possibly effectful request, reread
exact native ownership and effective policy. Start requires consistent running
state, while stop requires exited state. A changed fence/identity, immediate
process exit, still-running process or failed observation returns uncertainty.
A lost transport response never triggers a retry or same-name adoption.

The existing HTTP client/command context bounds waiting. In particular a slow
stop can outlive the client's response deadline even though the daemon continues
its bounded stop action. Return uncertainty in that case; do not claim that HTTP
cancellation undoes the action or that a missing response proves no effect. Native
reconciliation and typed durable receipts remain necessary.

The inspector now explicitly validates nested State field spellings/duplicates,
rather than relying on encoding/json's case-insensitive aliases. Already-canceled
requests fail before reaching even a custom transport. Daemon error bodies are
never copied to public errors.

## Mount and process boundaries

Initial native start uses `startPinnedCandidateHTTP`, holding the live mount-pin
mutex across before/after validation, action and observations. Close cannot
invalidate its descriptors in flight. Initial unsafe paths make no daemon request;
a changed path after effect is uncertain. Stop intentionally does not require
fresh mount pins, so stopping the exact owned process remains possible after a
mount infrastructure fault; all native-ID owner/policy checks still apply.

A running-state observation is not `/health/ready`, a quota proof, a broker grant,
a writer lease or an application activation receipt. The root-owned data mount
still needs its separate reviewed access transition before an application can
use it. No permission is relaxed in order to make the fixture run.

## Disposable Linux fixture

The workflow resolves the public official Kubernetes `registry.k8s.io/pause:3.10`
image to its repository digest before invoking tests. The fixture descriptor uses
that exact digest and native image inspection. Kubernetes' [pause implementation](https://github.com/kubernetes/kubernetes/blob/master/build/pause/linux/pause.c)
is an inert waiting process with normal termination handling; it provides no
Portal backend, data workload or readiness endpoint.

The explicitly enabled root-only GitHub-hosted test creates its synthetic owned
container under the existing fixed sandbox policy and pinned empty mounts, starts
and observes it running, refuses removal while running, stops and observes exit,
then refuses automatic restart and performs ownership-checked non-forced cleanup.
No arbitrary test command is introduced into the production create policy.
Existing stopped BusyBox creation and mount substitution tests also remain.

Ordinary test execution skips these native fixture cases. Actual execution of
the explicit root step on the current revision is required; compilation and
portable mocked HTTP tests are not substituted for it. The test has no production
credentials or data and does not authorize production host access.

## Remaining platform integration

Connect current signed claims, physical root/quota/socket proofs and the closed
lifecycle through a concrete native runner. Persist typed native observations and
reconcile uncertain effects before allowing later stages. Implement application
readiness over its authenticated Unix socket, active-writer/data access transition,
automatic backend registration, broker, crash-loop policy and full install/update/
stop/uninstall/recovery acceptance. Keep runtime execution readiness false until
all required enforcement/admission evidence exists. Production enrollment, SDK
freeze, Status migration and D5/D6 remain separate.

## Local verification

Runner/control/data/driver/journal/transport and CLI race regression passed
(journal 70.173 seconds; driver 1.472 seconds). Linux-target vet/staticcheck and
Linux amd64/arm64 test compilation passed. Native lifecycle and mount-start
boundary execution remain pending the explicit disposable CI run; these local
checks do not prove that a process actually started or stopped.

## Actual native execution

Functional source 71105b28a04aebafe7965043661988aa00a7adb3 passed disposable Linux
workflow 40448345362. The explicit root step actually ran
TestDockerAcceptanceCandidateLifecycle and recorded the process running,
running-removal refusal, stopped state and automatic-restart refusal; its
ownership-checked cleanup completed. TestNativeCandidateMountPinsStartBoundary
also passed, confirming unsafe mount state makes no start request. Ordinary-phase
skips are not counted as this evidence. Distinct controller/runner identity and
Portal endpoint confirmation also passed in the same workflow.

This native result is an inert fixture process lifecycle, not application health,
quota enforcement through the claim, authenticated broker access or a complete
managed installation. Full main CI was still running when this record was added.
