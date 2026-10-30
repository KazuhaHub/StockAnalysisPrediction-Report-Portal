# ADR 0047 — My tasks: one tray for a user's own runs and exports

**Status: Proposed.** 2026-10-29. Builds on ADR 0045 (data export tasks), ADR 0046 (reader export) and
ADR 0004 / 0008 (run queue and its multifactor priority).

## Context

The top bar had an export task tray. It listed every administrator's export tasks to an administrator
and a reader's own to a reader. Runs (Dify batch jobs) lived somewhere else entirely: the queue page
and the batch console. A person who submitted a run and an export had two places to look, and neither
told them how long they would wait.

Two further facts shaped the change:

- `GET /api/admin/batch/jobs` returned every user's jobs to any holder of `run_batch`. The `operator`
  role holds `run_batch` without `manage`, so an operator could read every colleague's runs, inputs
  included. Runs can only be submitted by `run_batch` holders, so this was the only way a non-admin
  saw the queue, and the listing was wider than the people it served needed.
- A queued task says nothing about the work in front of it. The queue page already computes "N ahead"
  for runs; exports had nothing comparable.

## Decision

### One tray, own tasks only

The export tray is replaced by a **My tasks** tray. It lists the caller's own tasks of both kinds:
runs (`batch_jobs.created_by = user`) and exports (`export_jobs.requested_by = user`). An administrator
sees only their own in the tray as well; the administrator consoles (export history, queue page) keep
showing everyone's.

The tray is mounted once in the shell for a user who is an administrator, holds `run_batch`, or has
reader export allowed. Anyone else gets neither the button nor a request.

### "N tasks ahead" — a count, never who

A queued task carries `ahead`, the number of tasks that will start before it.

- **Run:** `queue.Ahead(itemByID(waiting, id), waiting)` over the same `queuedItems()` the queue
  console uses, with the same rules: a queued job that is blocked by a preset window, or whose `run_at`
  has not arrived, is *scheduled* (`scheduled: true`) and gets no count.
- **Export:** the export worker is a single goroutine (`exportLoop`) that claims the oldest queued job
  ordered by `(created_at, id)` (`claimNextExportJob`). So
  `ahead = queued jobs strictly before it in that order + jobs currently running` (the running count is
  0 or 1). Jobs created later never count. Ties on `created_at` are broken by `id`, as the worker does.

The count covers everyone's tasks of that kind, which is what makes it honest, but the response never
says whose they are.

### Schedulers are deliberately not merged

Runs use Dify capacity and are ordered by the multifactor priority of ADR 0008 (base, age, fair
share, urgent tickets). Exports are local work served first in, first out. Merging them would make an
export wait behind Dify capacity it does not use, or a run behind a file write. The tray presents the
two lists side by side and each "ahead" is computed within its own scheduler; no cross-kind ordering
is implied.

### Run queue visibility is tightened

For a caller who is not an administrator (`PermManage`), `GET /api/admin/batch/jobs` returns only the
jobs they created, and `total` counts only theirs. The search `q` still works, within their own jobs.
Administrators are unchanged.

The list was not the only leak. `GET /api/admin/batch/jobs/{id}` (detail) had no ownership check at
all, so an operator could enumerate ids and read other users' jobs: submitter, every row's inputs,
errors and Dify run ids. Every `run_batch` route that takes a job or recurring-task id now applies one
rule: the creator or an administrator is served, anyone else is answered **404**, the same answer as
an unknown id (as ADR 0046 does), so ids do not leak existence. The routes are job detail, job cancel,
row cancel, and recurring-task detail, update, enable, run-now and delete. Cancel, row cancel and the
recurring routes previously answered 403 to a non-owner; they answer 404 now.

`GET /api/admin/batch/queue` keeps its shape. For an administrator it is the whole queue. For anyone
else `waiting`, `running`, `running_rows`, `scheduled` and `done_today` count their own jobs, so the
summary agrees with the list under it; `budget`, `reserved` and `my_priority` are unchanged. Capacity is
not the caller's own, though: whether a new run starts at once depends on the whole queue. So the
summary also carries `queue_running_rows` and `queue_waiting`, the whole queue's concurrent runs and
due waiting jobs, for everyone (numbers only, no identities; for an administrator they equal
`running_rows` and `waiting`). The run dialog's idle/full banner and the "Max at once" tag compare
these with `budget`; the summary tiles keep the own counts. The queue
page hides the submitter filter from a non-administrator, whose list holds only their own runs.

This is a behaviour change for operators: the queue page now shows an operator their own runs rather
than the whole queue. The "N ahead" on their own queued runs still reflects the whole queue, as a count.

### Export ownership follows ADR 0046

A non-administrator sees an export in the tray only if `requested_by == user` **and** the job was
submitted through the reader endpoints (`isReader()`). A non-reader row under their name is never
theirs. An administrator sees their own jobs of either kind. Each export item carries URLs for the
endpoint family that will accept it:

| job | download | cancel | link |
|---|---|---|---|
| reader job | `/api/exports/{id}/file` | `/api/exports/{id}/cancel` | `/exports` |
| non-reader job (administrator's) | `/api/admin/exports/{id}/file` | `/api/admin/exports/{id}/cancel` | `/manage/exports` |

Runs cancel through `/api/admin/batch/jobs/{id}/cancel` and open at `/queue`. The queue page has no
per-job deep link, so a run opens the queue page.

## API

`GET /api/tasks?limit=N`, cookie session. `limit` defaults to 10 and is capped at 50. It bounds the
finished tasks only: every active task of the caller is always returned, plus the most recent finished
ones up to `limit` in total across both kinds. Active tasks come first, newest submitted first; finished
tasks follow ranked by finish time (creation time when none was recorded), newest first. Ranking happens
in the query, before the `limit` cut, so an old run that has just finished is not displaced by newer ones
that finished earlier, and still raises its completion toast. The queries are
bounded to the caller's own rows, in the store, not filtered in Go over a global list.

```json
{"tasks":[{
  "kind": "run" | "export",
  "id": "7",
  "name": "target name | export name",
  "detail": "runs only: short preview of the first row's inputs",
  "status": "native status of the kind",
  "done": 1, "total": 3,
  "ahead": 2,
  "scheduled": true,
  "error": "failed exports",
  "counts": {"succeeded":1,"partial":0,"failed":0,"cancelled":0},
  "created_at": 1761700000000, "finished_at": 0,
  "download": "export done only",
  "cancel": "when cancellable",
  "link": "where Open goes"
}]}
```

- `status` is the kind's own vocabulary. Run: `queued, running, cancelling, finished, cancelled,
  expired`. Export: `queued, running, done, failed, canceled, expired`.
- `ahead` is present only on a queued task that is counted; `scheduled` only on a deferred run.
- `done`/`total`: a run's settled rows over its rows (live counts while running); an export's rows
  written over its estimate.
- `counts` is present on runs, so the client can tell a clean finish from one with failures.
- Times are unix milliseconds for both kinds (run timestamps are stored as local strings and converted).
- A run carries `cancel` and `link` only when the caller holds `run_batch` (an administrator does); a
  caller who lost the permission still sees the run's history without actions the server would refuse.
- A reader export carries `download` only while the caller's resolved export policy allows export, the
  condition `GET /api/exports/{id}/file` enforces. An administrator's own admin export keeps its download.
- An expired export has no `download`, as in ADR 0045; only a `done` export is downloadable.
- A user with no tasks, or no ability to have any, gets `{"tasks":[]}`.

The run listing behind the tray reads only the caller's rows, with no total, and no index is added.

The browser applies poll replies in request order: a reply that arrives after a newer one was applied
describes an older state and is dropped, so overlapping polls cannot announce a completion twice. A run
that finished with failures says how many failed; one with only partial results says that instead, and
never "0 failed".

The browser polls `/api/tasks?limit=10` every 3 s while anything is in flight and every 30 s otherwise,
and refreshes on window focus and when a page announces an export change. A task seen in flight that
later shows as finished raises one completion toast: export done (with a download action), export
failed, run finished, run finished with failures, run where nothing succeeded, run expired. A task
cancelled by its owner is silent.

No schema change: the endpoint reads `batch_jobs` and `export_jobs` as they are.

## Acceptance conditions

Each is covered by a test in `internal/app/tasks_api_test.go` unless noted.

1. Only the caller's own runs and exports are listed; another user's never appear —
   `TestMyTasksListsOnlyTheCallersOwnRunsAndExports`.
2. A non-administrator sees only reader exports under their name —
   `TestMyTasksReaderSeesOnlyReaderExportsUnderTheirName`.
3. An administrator sees their own exports of either kind and nobody else's —
   `TestMyTasksAdminSeesOwnExportsOfEitherKindButNotOthers`.
4. Download and cancel URLs name the endpoint family that accepts the job, and the advertised cancel
   URL works — `TestMyTasksDownloadAndCancelUrlsPerFamily`, `TestMyTasksRunUrlsAndName`.
5. Run `ahead` mirrors the queue console; a scheduled run has none —
   `TestMyTasksRunAheadCountAndScheduled`.
6. Export `ahead` counts earlier queued jobs and the running one, not later ones, breaking ties by id;
   a running export has none — `TestMyTasksExportAheadCount`, `TestMyTasksRunningExportHasNoAhead`.
7. Every active task is returned, `limit` bounds finished ones and is capped, order is active first
   then newest first — `TestMyTasksLimitBoundsTerminalOnly`.
8. Finished runs carry unix-ms times, counts and no cancel URL — `TestMyTasksTerminalRunsCarryTimesAndCounts`.
9. A user with nothing gets an empty array; anonymous callers are refused —
   `TestMyTasksEmptyListForAUserWithNothing`.
10. A non-administrator's `GET /api/admin/batch/jobs` lists only their own jobs, `total` counts only
    theirs, search stays inside them; an administrator is unchanged —
    `TestBatchJobsListIsOwnOnlyForNonAdmins`.
11. Every `run_batch` route taking a job or recurring-task id answers a non-owner 404 and changes
    nothing; owner and administrator are served — `TestRunBatchRoutesAnswer404ToNonOwners`, plus the
    404 expectations in `batch_ops_api_test.go`, `recurring_ops_api_test.go` and `recurring_test.go`.
12. Finished tasks are ranked by finish time before the limit —
    `TestMyTasksTerminalRankedByFinishTime`.
13. A non-administrator's queue summary reports their own counts; an administrator's stays global —
    `TestBatchQueueSummaryIsOwnForNonAdmins`; the submitter filter is hidden from a non-administrator —
    `QueueTable.test.tsx`.
14. A run item has no `cancel` or `link` for a caller without `run_batch` —
    `TestMyTasksRunActionsNeedRunBatch`.
15. A reader export loses its `download` when export is switched off; an administrator's own export
    keeps it — `TestMyTasksReaderDownloadNeedsExportPolicy`.
16. A stale poll reply is dropped and no completion is announced twice — `useMyTasks.test.tsx`; a
    partial-only run never reads "0 failed" — `TaskTray.test.tsx`.
17. The own-filtered listings and the export "ahead" count run on Postgres —
    `TestPostgresMyTasksQueries` (skipped unless `TEST_POSTGRES_DSN` is set).
18. A non-administrator's summary carries the whole queue's capacity next to their own tiles, and the
    run dialog and the "Max at once" tag use it (a cap filled by others is never "idle") —
    `TestBatchQueueSummaryCarriesGlobalCapacityForEveryone`, `RunAnalysisModal.queue.test.tsx`,
    `QueueTable.test.tsx`.
19. The tray lists both kinds, shows "N tasks ahead" and "scheduled", uses each item's own download and
    cancel URLs, toasts once on in-flight to finished, and is absent for a user with no task
    capability — `web/src/components/TaskTray.test.tsx`, `web/src/lib/useMyTasks.test.tsx`,
    `web/src/components/AppLayout.test.tsx`.
