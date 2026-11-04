# D2 proposal: bounded application tasks

Status: owner approved on 2026-11-03. Migration `0019`, persistence, worker,
service scheduling endpoint, restore handling and administrator controls are
implemented. Native `app.backend.task` exchange acceptance remains a separate
runtime integration gate.

## Decision

Add two Portal tables in append-only migration `0019` (`0018` is already the
application plugin-table migration):

- `app_platform_tasks`: current durable task identity, immutable execution tuple,
  state, retry budget, lease/fence and bounded result; and
- `app_platform_task_attempts`: one append-only row per claim/execution attempt,
  including safe failure/uncertain evidence.

This is not a per-plugin business table and does not reuse the analysis queue,
notification worker or runner journal.

## `app_platform_tasks`

| Column | Type | Rule |
| --- | --- | --- |
| `id` | ID | platform UUID primary key |
| `installation_id` | ID | current installation; deferred NO ACTION |
| `app_id`, `publisher_id` | T64 | immutable snapshot |
| `occurrence_id` | T128 | unique with installation |
| `handler` | T64 | closed platform handler registry ID |
| `payload` | B8192 | bounded canonical input |
| `payload_sha256` | SHA | exact payload digest |
| `state` | T24 | waiting, leased, running, succeeded, failed, cancelled, uncertain, review_required |
| `generation`, `policy_revision` | R | exact approved generation/policy |
| `release_sha256` | SHA | exact approved release |
| `controller_epoch` | ID | current control epoch |
| `attempts`, `max_attempts` | N | max 5 total attempts |
| `fence`, `revision` | R | monotonic; overflow refused |
| `worker_id`, `lease_id` | ID? | both present only while leased/running |
| `lease_until_ms` | MS? | finite 30s..10m |
| `next_attempt_ms` | MS | waiting/retry instant |
| `expires_ms` | MS | absolute task lifetime |
| `error_code` | T96? | closed safe code |
| `result_json` | J8192? | bounded non-secret result |
| `completed_ms` | MS? | present only for succeeded/failed/cancelled |
| `created_ms`, `updated_ms` | MS | explicit UTC |

Checks require unique `(installation_id,occurrence_id)`, handler/payload/enum
bounds, attempt/lease state agreement, terminal timestamp agreement and
`updated_ms>=created_ms`. Indexes cover due claim, installation, state/lease and
expiry. Quotas are global 10,000 active tasks, 1,000 per app and 256 per
installation.

## `app_platform_task_attempts`

Primary key `(task_id,attempt)`. Immutable claim fields include worker/lease IDs,
fence, execution-tuple digest and started/lease instants. Completion fields are
state, finished instant, safe error code, result/evidence SHA and bounded JSON.
Foreign key to task uses deferred NO ACTION. Task deletion/purge is a separate
reviewed operation.

## Worker and authority

A separate `app-task-worker` command claims one due task transactionally:

1. select a waiting task with `next_attempt_ms<=now` and an unexpired task;
2. conditionally set state/attempt/fence/lease;
3. insert the attempt row in the same transaction;
4. issue an H07 service assertion for `tasks.schedule`; and
5. dispatch only the task's registered fixed handler.

Before start, external effect, confirmation and completion, the worker reloads the
H07 assertion plus current installation generation/policy/release/epoch,
capability grants, lease/fence and task revision. Disable/uninstall/capability
revocation/restore invalidates the claim. A lost external confirmation becomes
`uncertain`; no automatic retry. Retry is allowed only from a normal failed
attempt with a future bounded backoff. Restore marks all nonterminal tasks
`review_required`.

The initial fixed registry contains:

- `app.backend.task`: bounded call to the app's existing own-backend `/v1/`
  route; no arbitrary command, path template, callback or destination; and
- `scheduled-digest.run`: the H12 neutral reference handler.

The registry is now implemented in `internal/appplatform/task_handler.go`.
Resolution requires the app identity and current `tasks.schedule` plus
`background.run`; `scheduled-digest.run` is additionally app-bound and requires
`reports.read`. Payloads are canonical bounded JSON objects. URL, callback,
command, path, method, headers, credential, recipient and SQL fields are refused
recursively.
`app.backend.task` has one fixed own-backend path, `/v1/tasks/run`; the payload
cannot name a method, path, callback or destination.

Admission, backoff, execution deadline and result classification are implemented
in `task_worker_contract.go`: global/app/installation active-task ceilings,
1-second exponential retry delay capped at six hours, deadline never beyond the
lease, safe result/error-code bounds, no result release for uncertain work and no
retry after the final attempt.

No task may use `secrets.use`, service impersonation or a recipient/destination
from payload. Notification/network/files resources require their own H08/H06/H05
authority paths.

## Backup, UI and acceptance

Both tables are declared migration products and enter logical backup/restore.
Restore pauses dispatch, classifies in-flight attempts `uncertain`, and requires
explicit review before resume.

Admin UI lists state, attempts, due time, safe error and result summary; it can
cancel/review but cannot edit immutable execution identity.

Acceptance must cover concurrent claims, attempts/fence drift, quota fairness,
browser-closed execution, restart/restore, disable/uninstall/revoke during run,
external success with lost confirmation, no automatic replay, SQLite and
PostgreSQL, and backup round trip.

## Implementation status

- `migration0019` creates exactly `app_platform_tasks` and
  `app_platform_task_attempts`. The task row keeps publisher and release values
  as immutable snapshots; neither is a foreign key. Only the current
  installation is referenced, with deferred `NO ACTION`.
- `POST /api/app-platform/installations/{installation}/tasks/background` accepts
  one H07 service assertion with `tasks.schedule`. The closed body is
  `occurrenceId`, `handler`, `payload`, and optional bounded `runAt`/`expiresAt`.
  Occurrence replay is stable; changed bytes under the same occurrence return
  `409`.
- `cmd/app-task-worker` owns the separate polling process. A task claim, fence
  increment, revision increment and attempt insert commit together. PostgreSQL
  uses `FOR UPDATE SKIP LOCKED`; SQLite relies on its single-writer transaction.
- A separate signed service-task command binds the task, lease, installation,
  generation, policy, controller epoch, release, service identity and body. The
  runner validates the exact registration and forwards fixed
  `POST /v1/tasks/run` to the current backend without manufacturing a viewer.
  The initial native transport supports the initial managed generation; the
  shared app response contract is `portal.app-task.response/1-dev.1`.
- Start and completion re-read the installation tuple, release, policy, epoch,
  capability grants, handler registry, lease, fence and revision. Result bytes
  use the same secret/command/destination refusal as payloads.
- Disable, uninstall, publisher revocation and release activation invalidate
  pending or in-flight work. Restore converts every nonterminal task to
  `review_required` and closes in-flight attempts as `uncertain`.
- The administrator API and Apps page expose state, due time, attempts, safe
  error and result summary. Cancellation cannot edit identity. Uncertain or
  review-required work is closed only by an explicit failed/cancelled review and
  never replayed automatically.

The Scheduled Digest reference app now implements the independent backend and
frontend for the fixed `scheduled-digest.run` handler. Portal maps that handler
only to `POST /v1/tasks/digest`; the runner verifies the exact registration and
service identity before forwarding it. Native acceptance now starts the exact
service image under the Portal-managed candidate, delivers the protected
registration, verifies the boot handshake and executes the fixed digest task.
Evidence is in
[Scheduled Digest evidence](handoff/evidence/platform-scheduled-digest/acceptance.json)
and [managed exact image](handoff/evidence/platform-scheduled-digest-managed/acceptance.json).
The task acceptance fixture seeds the final activation rows after delivery and
boot, so it is not a neutral-operator market walkthrough. Update-generation
service-task registration variants also remain gated.
