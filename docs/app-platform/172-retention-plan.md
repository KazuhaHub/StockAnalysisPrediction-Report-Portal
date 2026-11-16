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
| Runner logs | 10 MiB per container, 7 days (doc 167 says per installation) | 1 MiB × 2 files per container (2 MiB) | 5 MiB × 2 files (10 MiB) per container; the age limit is a documented deviation |
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

Each class runs in its own transaction (`retentionClassTx`), in batches of at most 500 rows (`platformRetentionBatch`), with a
30-second deadline (`platformRetentionClassTimeout`) so a stuck lock cannot hold a transaction open. The sweep takes `nowMS` as a
parameter and has no clock of its own, so the tests advance time by passing a later instant. The same SQL runs on SQLite and
PostgreSQL.

**Locks.** `withOUMutation` is not what admission takes, so the classes take the writers' own locks, in the writers' order, before
the first read, and make the pin decision and the delete in the same transaction:

| Class | Locks, in order | Why |
| --- | --- | --- |
| Review plans, operations (with effects and their plans) | `lockOUMutation` (advisory 780716 on PostgreSQL), then `lockPlatformPublisherTrust` (`SHARE ROW EXCLUSIVE` on the publisher tables) | the order every operation-admitting transaction takes them (`insertPlatformOperation` callers) |
| Tasks (reduce, cap, tombstone removal) | `lockPlatformTaskAdmission` (advisory 780719) only | the only lock task admission takes; taking 780716 first would add an order no admission path uses |
| Recovery-set selection | none (read only) | it changes nothing |

On SQLite the lock helpers reduce to a write that takes the single writer lock. `TestPlatformRetentionSerializesWithAdmissionOnPostgres`
holds each lock in another transaction, waits until the lock table shows the sweep waiting (no sleep), releases the lock and
checks the sweep resumes and deletes.

**Independent classes.** A failure in one class is recorded and the next class still runs; the audit detail carries the joined
`error`, and the sweep returns it. A class that finds the Portal in the restore pause stops the sweep (`paused` in the detail).
**The pause is read again inside each class transaction** and fails closed, in addition to the check before the first class.
Counts are published only when a class commits, so a rolled-back class reports nothing.

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

An operation is removable when it completed (stage `complete` or `failed`, never `recovery_required`, never unfinished) and was last
resolved before `now - operation_days`, and **none** of these pins it. "Last resolved" is the later of `completed_ms` and `updated_ms`
(`platformOperationSettled`): a recovery reconcile that resolves an operation later updates `updated_ms` and leaves `completed_ms`,
and the period must count from the actual resolution.

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
   effect with no verdict. A closed image record (a removal at least as recent as the latest load) pins nothing by itself, but it is
   **deleted whole or not at all**: while any operation holding one of its loads is not being deleted (pinned by the live chain, too
   young, or past the batch), every operation holding one of its removals is kept too (`withholdUnclosedImageRemovals`, repeated
   to a fixed point). A load whose removal was deleted would read as loaded again in `platformLoadedImages`, and reclaim would
   remove it again, forever.

A removed installation with all its namespaces deleted, no recovery chain and no recovery set rows is therefore swept whole. A live
installation sweeps its superseded history: older updates, older disables, repeated repairs, failed attempts that are not its
latest operation.

### What deletion does

Per operation, in one transaction: its effects, then the operation, then its plan unless something still names the plan. Replaying
an idempotency key inside the period returns the original operation. After deletion the key is unknown and behaves as a new request
(the plan it named is gone too, so a new request needs a new review).

### Known limits of the rule

- **An installation that ever needed recovery is pinned for good.** Pin 2 keeps every operation of an installation that has an
  unfinished operation or one in `recovery_required`, and an operation in `recovery_required` is never deleted, so once an installation
  has had one, its whole history stays. This is safe, and it is deliberately blunt: the recovery readers walk the whole history and
  the rule was not narrowed past what could be proven from reading them.
- **Swept history is gone from two views.** The status timeline of an installation reads the audit rows of its operations; the rows of a
  deleted operation are no longer found through it (the audit rows themselves are not deleted by this sweep, only the operation that
  indexed them). The administrator view of a task shows a tombstone with no result and no attempt history. Both are accepted.

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

Tombstones are outside the 10000-row cap: the cap counts terminal tasks that still hold a payload, result or attempts. Tombstones
are small and are removed by age, so an installation can hold more than 10000 rows in all while it holds at most 10000 unreduced ones.
The tombstone count in the audit detail is rows affected by the delete, not rows selected.

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

**Rollout, as for the CPU-set change in [doc 171](171-capacity-envelope-plan.md).** The log options are part of the create policy,
so every app container created before the upgrade is a policy-digest conflict: the runner refuses to adopt, start or remove it.
For each affected installation, stop the app in the Portal, remove its stopped container by hand as root on the runner host
(`docker rm portal-app-<resource id>`, without `-f`; data is not in the container, so nothing is lost), then start or reinstall
the app, and the runner creates a container under the new options. Where the runner's journal still records the removed container
as live and the runner keeps refusing it, start the runner on a fresh journal as the operator procedure for a rebuilt host does.
That last step was not exercised here and is stated as procedure, not as a tested path. Upgrade the Portal and runner together,
and do it when no operation is running.

The native log-flood test (`TestNativeDataWriterContainer`) bounds a 24 MiB flood by the named constants,
`appContainerLogBoundBytes` (2 files times 5 MiB) plus the 1 MiB headroom the 1 MiB policy had; the flood is larger than the bound so
the test cannot pass with rotation off. It runs in CI (`enforce-disposable-project-quota`) and not locally.

**Documented deviation: the 7-day age limit is not enforced.** The `json-file` driver rotates by size only. The bound that applies
is the size bound, and the logs go away when the container is removed. Enforcing an age needs a log shipper or a runner task
that truncates files, which is new runner work outside this change. The page states the size bound and does not claim an age.

The RUN-08 evidence measured a flood against the old `max-size 1m` policy. It is not re-run here: that statement in
`evidence/platform-runtime-data-matrix/acceptance.json` describes the policy it measured and is not updated to 5m.

## 6. Recovery sets

`app_platform_retention_recovery_sets` (default 2, 1-2) is stored and clamped like the other value, and
`platformRecoverySetsBeyondRetention` selects the `verified` sets past the latest N `verified` sets of each installation, leaving out a
set held by an import grant in `issued` or `consumed`. Only `verified` sets take a place: a `restoring`, `review_required` or
`preparing` set never takes a verified set's place and is never selected, so the last verified fallback is never pruned because of
them. "Newer" is the runner's order: `created_ms` greater, or equal and the checkpoint id smaller. The selection feeds `prunePlatformRecoverySets`, which asks the
runner to delete the checkpoint of each `verified` set, and `recovery_sets_beyond` in the audit row reports the sets still pending.


### The correction to what this section said before

An earlier version of this section said uninstall and delete-data "delete the whole namespace on the runner". That holds for the
namespace only. `delete-data` carries a namespace id, a project id and an inode and the data root, never the checkpoint root, and no
runner path ever set `runner_checkpoints.state='deleted'`. The delete-data stage marks the recovery sets `deleted` on the Portal, but
their archives (`<checkpoint_root>/.portal-checkpoints/<checkpoint_id>.tar`, at most 512 MiB each, two retained per namespace) and
their `verified` journal rows stayed on the runner. Deleting a namespace therefore leaked up to 1 GiB of runner disk and left a
`verified` checkpoint that the recovery bundle could still carry. The pruning below closes this leak as well: a set whose namespace is
deleted is a second source of work for the same sweeper (a "leak backlog"), and the same command clears it.

### The runner command

`delete-checkpoint` (format `portal.runner.delete-checkpoint-command/1-dev.1`, execute, read and effect purposes of its own) deletes
one retained checkpoint. It shares the journal stage `remove_data` with `delete-data` and takes effect number 2 to 32; number 1 stays
the delete-data effect and the stage list stays closed (doc 171 resolution 26). Its payload binds the recovery set id (carried into the
receipt only), the checkpoint id, the namespace, the operation that cut the checkpoint, the release, the manifest digest, the archive
size, the data format and the writer fence. The command runs under the data execution gate that checkpoint, restore, import and migrate
take, so the claim serializes with every reader of an archive.

1. **Pre-claim refusals** write no row: a fence or generation below the installation's floor, and an installation with a `prepared` or
   `unknown` command that is installation work (`ErrDeleteCheckpointInstallationBusy`). The busy refusal is deliberately blunt: the
   journal stores only the payload digest of a restore, migration, import or update start, so it cannot tell which one names this
   checkpoint, and it refuses for all of them. The sender's read-back then finds no claim and tries again later.
2. **The claim**, in one transaction, writes the command row `unknown` with a claim marker and sets the checkpoint row to `deleted`
   (`revision+1`). From that commit on restore, migrate, update start and checkpoint read-back refuse the checkpoint without being
   changed, because each requires `verified` or `restoring`. The decision is irreversible once claimed.
3. **Refusals after identity is read** write the row and settle it `reverted` with a typed outcome and leave the checkpoint and archive
   untouched: `checkpoint_identity_mismatch` (a payload field differs from the runner's row, the installation included),
   `checkpoint_in_use` (`restoring`), `checkpoint_not_verified` (`preparing`, `review_required`, or a `verified` row whose revision is
   neither 2 nor 4, which is the state after a completed restore), `checkpoint_latest` (the newest non-deleted checkpoint of a
   namespace whose volume resource is not `absent`: defence in depth, because the Portal never selects it) and
   `checkpoint_unrecorded` (an archive file exists but the runner has no row: the runner never deletes a file it has no record of).
4. **Absent** settles `applied` with outcome `absent` in the claim transaction: no row and no file (the fresh-host case, where the old
   host's checkpoint was never recorded), or a row already `deleted` by another effect.
5. **The native step**, after the commit: the archive is opened relative to the verified checkpoint directory without following links,
   must be a regular file with one link and exactly the claimed size (otherwise `ErrFenceReconciliation` and the effect stays `unknown`,
   never settled blind), is unlinked, the directory is synced and the absence is confirmed. The archive is not hashed again.
6. **Settle** `applied`, outcome `removed`, with `archive_found`. An interrupted deletion is finished by repeating step 5: an archive
   already gone settles as `removed` with `archive_found` false, and an unanswerable stat settles nothing.

The namespace reservation is the flat 768 MiB constant and already assumes two retained checkpoints, so a deletion releases nothing.
The command row is ignored by every installation-wide reader, through the one shared kind list in the journal.

**Journal shape.** The kind list of `runner_commands` gained `delete-checkpoint` in journal v3 itself, with no v4 (owner decision
2026-11-16, the precedent being the `remove-image` kind, doc 171 resolution 24). Version 3 has not shipped in any release, and
`integration/app-platform` is not an ancestor of `main`. The v1 and v2 upgrades rebuild `runner_commands` to the new declaration, and a
journal at either earlier v3 shape (before `remove-image`, and before `delete-checkpoint`) is refused with `ErrEarlierV3Journal`, which
says to start from a fresh journal. `runner_checkpoints` needs no change (`deleted` is already in its state check). There is no Portal
schema change.

### The Portal driver

`prunePlatformRecoverySets` runs where `reclaimPlatformImages` runs: when an operation ends, and on the idle tick, throttled together to once a
minute (never per operation ending), and deferred while any operation of any installation is in flight, so it adds
no reads or lock waits under churn. The deferral lasts up to an hour after a pass that did nothing; a pass that made progress (a
deletion settled or running) lets the next follow after a minute even while operations run, so a backlog drains at one window a
minute instead of one an hour. A busy installation never starves the others: selection leaves out each installation that is not
idle, so an installation is pruned at its next idle moment. The selection reads take no lock; the admission locks are taken only by the short decision,
settlement and strand transactions of a deletion that is really begun. A deletion pending on the runner lets the next tick read it back. It does not run inside the retention transaction classes, which are database-only.

- **Candidates.** (a) the `verified` sets beyond the retained count, except the newest `verified` set of a namespace that is not
  `deleted`: the runner's latest-checkpoint guard is per namespace while the count is per installation, so the selection mirrors it
  and a refusal never burns an effect number; (b) the leak backlog: `deleted` sets of a `deleted` namespace with no applied deletion;
  (c) a set whose earlier effects are all stranded (below). Excluded: a set whose installation has an unfinished operation or an
  operation in `recovery_required`, a set held by an import grant in `issued` or `consumed`, a set in `restoring`, a `review_required`
  set, and everything during the restore pause (read again, failing closed). New deletions have a window of their own, so deletions
  that cannot progress never stop the others.
- **The effect.** Kind `delete_checkpoint`, hosted by the latest finished operation of the installation, at the next free `remove_data`
  number of 2 or above. The id is derived from the set, its checkpoint and the attempt only, not from the host, so every tick and
  restart signs the same id and a lost receipt is read back under it. The row is written, under the admission locks, before the command
  leaves the Portal. With no free number the set waits for a later operation, and a retry never takes the last eight numbers
  of a host, so a refusal that repeats cannot use the numbers new deletions need.
- **Settle.** `applied` marks a retention candidate's set `deleted` and writes an `app_platform.recovery_set_pruned` audit row.
  `checkpoint_latest` and `checkpoint_in_use` are retried under the next attempt with a backoff, at most eight times. Only refusals count
  toward that cap and the backoff: stranded and superseded rows never do, and the retry of a stranded deletion is exempt from the cap,
  because the decision was made and only a verdict can end it. `checkpoint_identity_mismatch`, `checkpoint_unrecorded` and `checkpoint_not_verified` are final: they write an
  `app_platform.recovery_set_prune_refused` audit row and the storage status lists them as needing an operator.
- **A deletion with no verdict is never reverted.** The runner's claim is irreversible and may have happened, so no Portal path
  closes a `prepared` or `unknown` effect without the runner's word. When the installation's fence moves after the row was written
  (a later operation ended), the old host can no longer sign for it: the row is marked `stranded` (a member of its receipt) and
  stays open, so the set stays decided and restore and the bundle keep refusing it. The next pass signs a new attempt on the
  installation's current host. Its receipt settles the question and closes the stranded rows: an `applied` receipt closes them as
  `superseded_applied`, and a refusal proves the earlier claim never happened (a claim marks the runner's row deleted before it
  touches the file, so a later effect id would have answered `absent`) and closes them as `superseded_not_claimed`. A row issued to a
  retired runtime, or open for a day with no verdict (a stranded row included: a new attempt should have settled it by then), stays open and is listed by the storage status (`runtime_retired`,
  `outcome_unknown`) for an operator.
- **A decided deletion is not retained.** `platformRetainedPredicate` excludes a set that a `delete_checkpoint` effect names in
  state `prepared` or `unknown` (an `applied` one retires its set in the transaction that settles it). The sets of the open effects
  are read once per query and named in the predicate. The read is bounded by the live sets, never by the history of the ledger:
  open effects come through the state index, and an applied one is found through the unique effect id, one lookup per derived
  attempt number (sixteen) of each live set. The selection and the storage status read the deletions of their candidate sets the
  same way. Once the decision commits, the bundle writer stops
  requiring the archive, the retention pins stop holding its operations, a restored Portal (whose dump carries the effect row) agrees,
  and restore admission, import-grant issuance and restore-from-import refuse the set (an `applied`, `prepared` or `unknown` effect
  decides it; a refused one does not) with the existing "recovery set unavailable"
  error. A bundle written while a deletion commits between its two reads refuses (database changed).
- **Retention pin 9.** An operation holding a completed deletion's effect is kept while its set row exists, and the operation holding
  a refused attempt is kept until a deletion of the same set is applied, whether or not the set is already `deleted` (a leak-backlog
  set is deleted from the start), because that row is the attempt count. A swept refusal would reset the count, and the same effect
  id would be signed again under another host, which the runner refuses as a conflict, leaving the row open for ever. Pinning was
  chosen over putting the host in the effect id because it keeps the id derivation of the design (set, checkpoint, attempt) and one
  rule for every row that carries a count. Without the pin the
  sweep would delete the effect after 90 days, the leak backlog would select the set again, and the runner would answer `absent` for
  ever.
- **Effect counters.** Every Portal effect counter that ignores `load_image` and `remove_image` rows ignores `delete_checkpoint` rows
  through the one shared kind list. An open deletion deliberately does not block new operations on the installation: it is hosted by
  a finished operation, and the decided-state rule above is what keeps restore, import and the bundle away from its set. Blocking
  operations on an effect the Portal may never settle (a retired runtime) would make the installation unusable.

Doc 167's disk arithmetic, which assumes at most two sets, is now enforced eventually and not at once: a set beyond N waits for an
idle installation and a free slot. The storage page reports `recovery_set_pruning` as enabled.

## 7. Uninstalled data

Unchanged: retained until explicit deletion. `TestPlatformRetentionNeverTouchesRetainedData` sweeps 400 days ahead and proves the
namespace, its recovery set, the installation and the operation that holds them are all unchanged.

## 8. Management page

The storage page (`StoragePage.tsx`) has a new section, "Application platform", with `AppPlatformRetentionCard`. It shows the two
adjustable values with their bounds, the fixed policy read-only (the numbers come from the server), the last sweep from its audit row,
and, while pruning is blocked, a notice that recovery-set pruning is not available. Pruning is now enabled (section 6), so the notice no longer shows.

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
| Recovery selection | `TestPlatformRetentionSelectsRecoverySetsBeyondTheLatestN` |
| Runner: claim marks the checkpoint deleted before the file is touched; every refusal, the busy installation, the floors, identity binding, replay, the three crash windows, the shared gate with restore | `delete_checkpoint_test.go` in `internal/apprunner/journal` (fake native seam) |
| Runner: every kind list and reader ignores a delete-checkpoint row; both earlier v3 shapes refused with bytes unchanged; fresh journal accepts the kind | `TestEveryCommandFamilyAdmitsAfterAnAppliedOrUnknownDeleteCheckpointRow`, `TestInstallationWideCommandReadersIgnoreLoadImageRows`, `remove_image_schema_test.go` |
| Runner: signed command purposes and payload binding; transport actions and reply binding | `delete_checkpoint_test.go` in `internal/apprunner/control`, `delete_checkpoint_transport_test.go` |
| Runner: the archive is unlinked, other archives are byte-identical (CI only, root fixture) | `TestNativeDataPrimitive` (`NATIVE_DATA_CHECKPOINT_DELETE_VERIFIED`) |
| Portal: the oldest sets beyond the retention are pruned and the newest never; held sets are never selected; a decided deletion is not retained and not restorable; lost receipts are read back under one effect id; refusals retried or final; the leak backlog through the real delete-data stage; pin 9; every effect counter ignores the kind | `app_platform_recovery_prune_test.go`, `app_platform_recovery_prune_admission_test.go`, `app_platform_recovery_prune_leak_test.go`, `app_platform_image_reclaim_guard_test.go` |
| Runner log bound | `TestAppContainerLogsAreBoundedToTenMiB`, `TestContainerPlanWithOtherLogOptionsIsRefused` |
| Closed image record deleted whole or not at all; busy runtime keeps its records | `TestPlatformRetentionKeepsTheRemovalOfAnImageWhoseLoadIsPinned`, `…KeepsEveryImageRecordOfABusyRuntime` |
| Secret-binding plan reference, later generations, resolution time | `…PlanReferencedBySecretBindingIsKept`, `…KeepsOperationsOfALaterGenerationOnALiveInstallation`, `…CountsFromTheLatestOfCompletedAndUpdated` |
| Independent classes, pause re-read inside each class | `…ClassesRunIndependently`, `…ClassesRecheckThePauseInsideTheirTransaction` |
| Admission locks taken (PostgreSQL) | `TestPlatformRetentionSerializesWithAdmissionOnPostgres` |
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
- **Recovery-set pruning** (section 6) is proven by unit tests against a fake runner and by the runner journal tests against a fake native seam. The native archive removal (`TestNativeDataPrimitive`) and the lifecycle socket need a root runner and run only in CI; they have not been run. The transport between the Portal and the runner is checked by signature and reply binding tests, not end to end. PostgreSQL ran the SQL-level pruning tests and the lock-order test; the full-fixture PostgreSQL variants could not be run on the development machine.
- **Fresh-host archives.** An archive a bundle carried and an operator placed on a new runner has no row there, so the runner answers `absent` or `checkpoint_unrecorded`. Pruning such files stays operator work.
- **Checkpoint root on its own filesystem.** If the checkpoint root is a separate filesystem (`HostFacts.CheckpointSeparate`), a deletion frees that filesystem, which no budget tracks. This gap already existed and is not widened.
- **The 7-day log age** is not enforced (section 5), and RUN-08's flood measurement was taken at 1 MiB, not 5 MiB.
- **Concurrent admission.** The sweep takes the admission locks (section 1) and a PostgreSQL test shows it waits for them. It was not
  run against live concurrent installs, and the lock order was compared with the admission paths by reading them, not by a
  deadlock search.
- **Unbounded scans.** The image ledger, the retained-set receipts and the per-installation task count are read in full inside the
  class transactions; only the deletes are batched. The 30-second class deadline bounds a bad case but does not make the scan
  incremental.
