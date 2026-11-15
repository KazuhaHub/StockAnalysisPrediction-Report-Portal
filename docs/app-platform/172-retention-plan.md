# Retention defaults and the retention sweeper: implementation plan

Status: implemented 2026-11-15 on `feat/app-platform-retention`. This plan turns section 5 of
[doc 167](167-owner-decisions-remaining-acceptance.md) (`DATA-08`) into ordered, test-first steps, following the review
practice of docs 168–171. It records every resolution between the decision text and the code, and the pin rule the
sweeper applies, which is the part a reviewer has to read.

No Portal DDL is needed and none was added: no table, no column, no index, no migration step. The adjustable values live in
`meta`, the tombstone of a task is the task row itself, and the audit record is an `audit_log` row.

## What the decision asks for, and what the code held

| Record | Policy (doc 167 §5) | Before | Now |
| --- | --- | --- | --- |
| Review plans | valid 15 minutes; unconsumed expired plans swept; consumed plans kept with their operation | the 15 minutes was a literal in 12 places, each with an overflow guard | one constant, `platformReviewPlanValidityMS`; unconsumed plans removed 24 hours after expiry |
| Completed operations and evidence | 90 days, adjustable 90–365 | never removed | swept by the pin rule below |
| Terminal task and event history | 30 days and 10000 rows per installation; occurrence tombstone kept for the operations period | never removed | reduced to a tombstone, tombstone removed after the operations period |
| Runner logs | 10 MiB per installation, 7 days | 1 MiB × 2 files per container (2 MiB) | 5 MiB × 2 files (10 MiB) per container; the age limit is a documented deviation |
| Recovery sets | latest 2 per installation, adjustable 1–2 | unbounded | selection and setting implemented; removal blocked, see section 6 |
| Uninstalled data | until explicit deletion | already so | already so; a test proves the sweep never touches a retained namespace |

## Resolutions of conflicts between doc 167 §5 and the code

1. **Twelve validity sites, not five.** The survey named five places that write `+900000`. There are twelve non-test sites in
   `app_platform_*.go` (install, update, managed update, removal, restore, restore resume, restore import, delete data, frontend
   enable, managed enable, recovery actions, trust quarantine), each with a `MaxInt64` overflow guard that repeats the literal. All now use
   `platformReviewPlanValidityMS`, and `TestReviewPlanValidityIsOneNamedConstant` fails on a literal `900000` in any
   non-test `app_platform_*.go` file.
2. **A deleted recovery set still holds its operation.** `app_platform_recovery_sets.operation_id` is a foreign key with
   `NO ACTION`, and a set marked `deleted` keeps its row. An operation that any recovery set row names, in any state, can
   therefore not be deleted, and the sweep does not try. The checkpoint operation of every set stays as a tombstone. No cascade
   was added (doc 13 §10: no cascade may erase retained business data).
3. **Secret bindings also reference plans.** The survey named `capability_grants.review_plan_id`. `secret_bindings.review_plan_id`
   is the same kind of reference, and both are checked.
4. **The image ledger lives in operation effects.** `platformLoadedImages` rebuilds "what is loaded on the runner" from the
   `load_image` and `remove_image` effects of all operations, and sends the known loads of an image with a removal. Deleting those
   effects blindly would leak daemon images or shrink the known-load list. This is the one reader the survey did not list, and it
   is pinned (section 3).
5. **Idle audit rows.** The decision asks for one audit row per sweep. A sweep that finds nothing still writes one, so the last
   sweep is always readable, and the cadence keeps that cheap: an idle Portal sweeps once an hour, and a Portal with no
   application platform records does not sweep at all.

## 1. Scheduling

`cleanupTick` (the minute ticker of ADR 0017) calls `platformRetentionTick` first, before its own due check. The platform
sweep keeps its own cadence:

- one sweep per `platformRetentionInterval` (1 hour), stamped in `meta` (`app_platform_retention_last_sweep_ms`);
- a sweep that stopped at a batch bound sets `app_platform_retention_backlog`, and the next minute tick continues until the
  backlog is drained;
- nothing runs when no review plan and no task exists, so a Portal that never used the platform writes no audit row;
- nothing runs, and nothing is stamped, while the Portal is in the restore pause. An unreadable pause marker stops the sweep with
  an error (fail closed), as the image reclaim does.

Each class runs in its own transaction under `withOUMutation`, the lock the other platform mutations take, in batches of at most
500 rows (`platformRetentionBatch`). The sweep takes `nowMS` as a parameter and has no clock of its own, so the tests advance
time by passing a later instant. The same SQL runs on SQLite and PostgreSQL.

## 2. Review plans

An unconsumed plan is removed when `expires_ms` is more than 24 hours in the past and nothing references it: no operation, no
capability grant, no secret binding. A consumed plan is never removed on its own: it goes with its operation, and only when no
grant or binding still names it.

## 3. Operations and evidence: the pin rule

Retention is `app_platform_retention_operation_days` (default 90, 90–365, clamped on read, refused out of range on save).

### Readers of old operations

Every non-test query that reads `app_platform_operations` was surveyed (about 110 sites in 90 files). They fall into these groups;
each group states what it needs from an operation that is no longer running.

| Group | Representative readers | What they need |
| --- | --- | --- |
| Idempotent replay | `platformFindRemovalCommit` (`app_platform_removal_review.go`), the trust quarantine, every `uq_ap_operation_idempotency` lookup | the row, for as long as a replay of its key must return it |
| Launch and first-generation history | `app_platform_launch.go`, `app_platform_managed_access.go` (requires exactly one operation of the installation), `app_platform_writable_history.go`, `app_platform_writable_launch.go`, `app_platform_task_backend.go` (the generation 1 install) | the completed install operation and its plan |
| Current-generation history | `app_platform_writable_update_history.go`, `app_platform_resume_history.go`, `app_platform_restore_history.go`, `app_platform_capability_snapshot.go` | the completed operation whose `target_generation` is the installation's current generation, and the install |
| Status and settings | `app_platform_status_http.go` (latest operation by `updated_ms`), `app_platform_settings_http.go` | the installation's latest operation |
| Image references | `platformReferencedConfigDigestsIn` (the source release of the latest completed update), `platformLoadedImages` | the latest completed update; the load and removal effects of an image still loaded |
| Delete data | `app_platform_data_delete.go`, `app_platform_delete_data_review.go`, `app_platform_removal_never_started.go` | the install operation and its `prepare_data` effect for as long as the namespace exists; **every** operation of an installation that never completed an install |
| Recovery | `app_platform_launch.go` (resolved-recovery predicate), `app_platform_recovery_*.go`, `app_platform_restore_*.go`, `app_platform_recovery_bundle.go` | every operation of an installation that has an unfinished or `recovery_required` operation; the operation that cut a retained set; the source and data operations a checkpoint receipt names |
| Plans reached through grants | `app_platform_network_request.go` (grant, plan, operation join) | the operation of a plan a capability grant names |
| Admission guards | `completed_ms IS NULL` counts in install, update, removal, restore, enable and recovery admission | unfinished operations, which are never touched |

### The rule

An operation is removable when it completed (stage `complete` or `failed`, never `recovery_required`, never unfinished) before
`now - operation_days`, and **none** of these pins it:

1. **Unresolved effect.** One of its effects is `prepared` or `unknown`.
2. **Recovery chain.** Its installation has an unfinished operation or an operation in `recovery_required`. Their readers walk the
   whole history (the never-started evidence reads every operation, the launch predicate compares completion times), so the whole
   installation is kept.
3. **Recovery set.** Any recovery set row names it as `operation_id`, in any state (the foreign key), or it is the source operation
   or the source data operation named by the snapshot receipt of a set that is not deleted. A retained set whose receipt cannot
   be read pins its whole installation.
4. **Import grant.** An import grant in `issued` or `consumed` names a set that names it (also covered by 3, kept as its own
   fixture because the decision names it).
5. **Plan reference.** A capability grant or a secret binding names its review plan.
6. **Retained data.** The installation is removed but still has a data namespace that is not `deleted`: delete data reads the
   install operation and its effects.
7. **Live chain.** The installation is not removed, and the operation is one of: a completed `install`; a completed operation
   whose `target_generation` is the installation's current generation or later; the latest completed operation of its action; or
   the installation's latest operation by `updated_ms`.
8. **Image ledger.** Its load or removal effects are the live record of an image: an image whose latest applied load is newer than
   its latest applied removal, an applied effect whose receipt cannot be read, and every image effect of a runtime that holds an
   effect with no verdict. A closed image record (a removal at least as recent as the latest load) pins nothing.

A removed installation with all its namespaces deleted, no recovery chain and no recovery set rows is therefore swept whole. A live
installation sweeps its superseded history: older updates, older disables, repeated repairs, failed attempts that are not its
latest operation.

### What deletion does

Per operation, in one transaction: its effects, then the operation, then its plan unless something still names the plan. Replaying
an idempotency key inside the period returns the original operation. After deletion the key is unknown and behaves as a new request
(the plan it named is gone too, so a new request needs a new review).

### Consequence of the rule that the decision did not state

Pins 3 and the foreign key mean the checkpoint operation of every recovery set outlives its 90 days, and pin 8 keeps the
operations that loaded an image still on a runner. An operation that is pinned stays replayable past 90 days. The 90 days is the
lower bound for an unpinned operation, not an upper bound for all of them.

## 4. Tasks and events

All three steps use only columns that exist and leave `occurrence_id`, `payload_sha256` and `handler` alone, the columns the
admission dedup reads (`app_platform_task_store.go`).

- **Reduce.** A task in `succeeded`, `failed` or `cancelled` that completed more than 30 days ago gets `payload` set to an empty
  blob and `result_json` set to NULL, and its attempt rows are deleted. A tombstone is a terminal task with an empty payload, no
  result and no attempts.
- **Cap.** Per installation, when more than 10000 terminal unreduced rows exist, the oldest ones beyond the cap are reduced in the
  same way.
- **Remove.** A tombstone whose `completed_ms` is older than the operations period is deleted as a whole row.
- `waiting`, `leased`, `running`, `uncertain` and `review_required` tasks are never touched.

A replayed occurrence of a reduced task finds its row and returns it. A replay with another payload hash or handler conflicts. After
the row is removed the occurrence is a new request.

Event deliveries are tasks with handler `app.event.delivery` and follow the same rules. Their pending count reads only
non-terminal rows.

## 5. Runner logs

`appContainerLogMaxSize` is `5m` and `appContainerLogMaxFile` is `2`: at most 10 MiB per container, written by Docker's
`json-file` driver and removed with the container. The options are part of the plan body and so of the policy digest, and
`validContainerPlan` now refuses a body whose log options differ, even with the digest recomputed (the same guard the CPU
settings have). This changes the policy digest of every container, which is acceptable before release; a container created
under the old options is a plan mismatch like a CPU-set change.

**Documented deviation: the 7-day age limit is not enforced.** The `json-file` driver rotates by size only. The bound that applies
is the size bound, and the logs go away when the container is removed. Enforcing an age needs a log shipper or a runner task
that truncates files, which is new runner work outside this change. The page states the size bound and does not claim an age.

The RUN-08 evidence measured a flood against the old `max-size 1m` policy. It is not re-run here: that statement in
`evidence/platform-runtime-data-matrix/acceptance.json` describes the policy it measured and is not updated to 5m.

## 6. Recovery sets

`app_platform_retention_recovery_sets` (default 2, 1–2) is stored and clamped like the other value, and
`platformRecoverySetsBeyondRetention` selects the retained sets past the latest N of each installation, leaving out a set held by an
import grant in `issued` or `consumed`, and a set in `restoring`.

**Pruning is blocked on runner work and is not implemented.** A set is marked `deleted` in two places only: uninstall, and the
delete-data stage. Both delete the whole namespace on the runner (`delete-data` carries a namespace id, a project id and an inode,
no checkpoint id), and no runner command deletes one checkpoint archive: there is no `delete_checkpoint`, no prune, and the
checkpoint lifecycle (`checkpoint`, `import_checkpoint`, `restore`) never removes an archive. Marking a set `deleted` without its
archive would leak runner disk and hide the archive from the bundle that carries it. So the sweep selects and reports the sets
beyond the retention (`recovery_sets_beyond` in the audit row and on the page) and changes no set. Doc 167's disk arithmetic, which
assumes at most two sets, is therefore not yet enforced by this sweep; the unblocking work is a runner command that deletes one
checkpoint by id under the same fence rules as `delete-data`.

## 7. Uninstalled data

Unchanged: retained until explicit deletion. `TestPlatformRetentionNeverTouchesRetainedData` sweeps 400 days ahead and proves the
namespace, its recovery set, the installation and the operation that holds them are all unchanged.

## 8. Management page

The storage page (`StoragePage.tsx`) has a new section, "Application platform", with `AppPlatformRetentionCard`. It shows the two
adjustable values with their bounds, the fixed policy read-only (the numbers come from the server), the last sweep from its audit row,
and a notice that recovery-set pruning is not available yet.

- `GET /api/admin/app-platform/retention` and `PUT /api/admin/app-platform/retention`, behind `s.admin` (`PermManage`), the same
  permission as the cleanup routes.
- A save validates both values before storing either, refuses out-of-range values (no clamping on save), and sends only what changed.
- All copy goes through `t()` with keys in `zh-CN`, `zh-TW` and `en-US`.

## Tests

Written first, and each failed before the implementation existed (the package did not compile without it). A mutation check removed
the unresolved-effect pin, the recovery-chain pin, the capability-grant pin, the open-image pin and the tombstone reduction in turn;
each made a named test fail.

| Acceptance condition | Test |
| --- | --- |
| Deletion of eligible rows only, every class | `TestPlatformRetentionReviewPlans`, `…Operations`, `…Tasks`, `…TaskCapPerInstallation` |
| Every pinned row retained, one fixture per pin | `TestPlatformRetentionOperations` (nine scenarios, one per pin and per group above) |
| Idempotency inside the period, new request after | `TestPlatformRetentionIdempotency` |
| A swept task occurrence still deduplicates, another hash conflicts, and past the period it is new | `TestPlatformRetentionSweptTaskOccurrenceStaysDeduplicated` (through `admitPlatformTask`) |
| 10000-row cap (injected small cap), batches | `…TaskCapPerInstallation`, `…DefaultTaskCap`, `…WorksInBoundedBatches` |
| One audit row per sweep | `TestPlatformRetentionWritesOneAuditRowPerSweep` |
| Nothing while paused, fail closed on an unreadable marker | `…DoesNothingWhilePaused`, `…FailsClosedWhenThePauseIsUnreadable` |
| Settings clamp on read, validate on save | `…ConfigClampsOnRead`, `…ConfigAPIValidatesOnSave`, `…ConfigAPIReportsTheLastSweep` |
| The scheduler tick reaches the sweep | `TestPlatformRetentionTickReachesTheSweep`, `…TickWaitsAnIntervalUnlessThereIsBacklog` |
| Retained namespaces untouched | `TestPlatformRetentionNeverTouchesRetainedData` |
| Recovery selection, nothing marked deleted | `TestPlatformRetentionSelectsRecoverySetsBeyondTheLatestN` |
| Runner log bound | `TestAppContainerLogsAreBoundedToTenMiB`, `TestContainerPlanWithOtherLogOptionsIsRefused` |
| One validity constant | `TestReviewPlanValidityIsOneNamedConstant` |
| Web | `AppPlatformRetentionCard.test.tsx`, `StoragePage.test.tsx`, the locale parity test |

Every sweep test runs on SQLite and, when `TEST_POSTGRES_DSN` is set, on PostgreSQL, through `platformTestStore`.

## Not proven

- **Real-time elapsed behaviour.** Every limit is exercised by advancing the `nowMS` argument. The tick tests use the wall clock only
  to place fixtures in the past. Nothing ran a Portal for 90 days.
- **Production volume.** The sweep scans the effects of image kinds and the recovery sets in full each time, and groups terminal tasks
  per installation. It was not timed against a large database.
- **The reader survey is a reading, not a proof.** The pin rule keeps what the readers named in the table need. A reader added later
  that walks old operations in a way the rule does not cover would break silently; the rule is the place to extend, and the
  scenario tests are where to add its fixture.
- **Recovery-set pruning** is not done (section 6).
- **The 7-day log age** is not enforced (section 5), and RUN-08's flood measurement was taken at 1 MiB, not 5 MiB.
- **Secret binding pin.** The plan-reference pin is tested through a capability grant. The secret-binding branch is the same query
  shape and is not exercised by its own fixture.
- **Concurrent admission.** The sweep serialises with other platform mutations through `withOUMutation`. It was not run against live
  concurrent installs.
