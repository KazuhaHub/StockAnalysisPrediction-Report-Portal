# Declared capacity envelope: implementation plan

Status: accepted 2026-11-13. This plan turns section 4 of
[doc 167](167-owner-decisions-remaining-acceptance.md) (OPS-08, closing SEC-09) into ordered,
test-first steps, following the review practice of docs 168–170.

## Owner decision taken with this plan

The runner keeps its reservations in a new additive journal table, `runner_reservations`, in
journal schema version 3 (owner's choice, 2026-11-13). The Portal database is unchanged.

## What exists and what is missing

| Limit | Default | Today |
| --- | --- | --- |
| Installations, static and managed | 50 | **missing** (no count at commit) |
| Running managed backends | 8 | **missing** |
| Runner memory, backends and migration jobs | 25% of host memory | **missing** (per-container limit only) |
| Runner CPU | 50% of host CPUs | **missing** (per-container limit only) |
| Runner disk | 8 GiB of the XFS volume | **missing** (per-namespace project quota only; volume size never read) |
| Daemon image store | 10 GiB of declared uncompressed bytes | **missing** |
| Unpinned staging | 2 GiB | enforced (`platformStagingCapBytes`, `app_platform_storage_full`) |
| Pinned blobs | 8 GiB | enforced (`platformBlobCapBytes`) |
| Tasks | 10000 / 1000 / 256 | enforced (`AdmitTask`, `task_capacity`) |
| Layer beyond its declaration | refused | enforced (`RefusalUncompressedBound`, Portal `verifyLayerContent`) |

The work comes in two pull requests. **PR 4a** adds the missing limits. **PR 4b** adds the soak
harness and the acceptance evidence.

## PR 4a: enforcement

### Runner budgets

`service.Config` gains an optional `capacity` object. It is still decoded strictly, so unknown
fields are refused. When a field is absent, its default applies.

| Field | Default | Valid range |
| --- | --- | --- |
| `memory_percent` | 25 | 1–90 |
| `cpu_percent` | 50 | 1–100 |
| `disk_bytes` | 8 GiB | 1 GiB up to the volume size |
| `running_backends` | 8 | 1–64 |
| `image_store_bytes` | 10 GiB | 1 GiB – 1 TiB |

At startup the runner turns the percentages into absolute budgets. It uses `MemTotal` and `NCPU`
from the Docker `/info` read it already makes. It also reads the size of the XFS data volume with
`statfs`. **Fail closed:**

- If the volume is smaller than `disk_bytes`, or a host fact cannot be read, the runner reports
  not ready with a typed reason and admits nothing.
- If the derived memory or CPU budget is smaller than one backend at the descriptor minimum, the
  runner also reports not ready.

The ready line and the readiness facts report the effective budgets, so the evidence can observe
them.

### The reservation table (journal v3)

`runner_reservations` has these columns: `id`, `resource_id` (unique), `installation_id`, `kind`,
`memory_mib`, `cpu_millis`, `disk_bytes`, `image_bytes`, `state`, `created_ms`, `updated_ms`.

`kind` is one of:

- `backend`: a candidate, writable candidate, update start or restore start.
- `migration`: a migration job.
- `namespace`: a managed data namespace.
- `image`: a loaded image.

`state` is `held` or `released`. The table is added to `schemaProducts()`. The v2 → v3 upgrade is
additive.

**Backfill.** For each live resource that exists at upgrade time, the upgrade inserts a `held` row
at the descriptor maxima: 4096 MiB, 4000 millis, 768 MiB of disk for a namespace, and 4 GiB for
an image.

- This fails closed: an old journal can only over-count, never under-count.
- The next completed start or load of that resource replaces the backfilled amounts with the
  declared ones, inside its own transaction.

### Reservation amounts

| Kind | Memory | CPU | Disk | Image bytes |
| --- | --- | --- | --- | --- |
| `backend` | `MemoryMiB` | `CPUMillis` | — | — |
| `migration` | the backend's `MemoryMiB` | the backend's `CPUMillis` | — | — |
| `namespace` | — | — | 768 MiB | — |
| `image` | — | — | — | sum of declared `uncompressed_bytes` |

- **Backend and migration** take the target release's descriptor resources.
- **Namespace** disk is 768 MiB per managed namespace: the 256 MiB namespace quota plus two
  retained checkpoints of at most 256 MiB each (doc 167's arithmetic). It is a named constant
  that is tested against the namespace quota constant.
- **Image** bytes are counted once per distinct `config_digest`, however many installations use
  that image.

### Admission

Every start-bearing claim does its reservation and its capacity check **in the same journal
transaction as the claim, before any native call**. The start-bearing claims are prepare and
start candidate, writable candidate, update start, restore start, the migration job, data
namespace creation, and load-image.

The check sums the `held` rows plus the new one and compares each total against its budget:

- memory;
- CPU;
- disk;
- image store;
- the count of `backend` rows, against the running-backend limit.

`migration` rows count against memory and CPU but not against the backend count.

**Refusal** is a definite `reverted` receipt with the typed outcome `capacity_exhausted` and a
`dimension` field. The dimension is one of `memory`, `cpu`, `disk`, `image_store` or
`running_backends`. It is recorded with the requested amount and the remaining amount.

- No resource row and no reservation row is left behind.
- A replay returns the same receipt.
- An `unknown` effect keeps its reservation, so it fails closed.

A reservation that replaces another one on the same installation is netted inside the same
transaction. That covers an update start replacing the running backend, and restore after a
failed update. Doing so avoids refusing an update merely because the old and new backends would
briefly count twice. The netting rule is the place where the old resource's removal is already
required (the `remove-code` evidence). If the old resource is not yet proven absent, both rows
count.

### Release

A reservation becomes `released` in the same transaction that records the resource's proven
absence:

- removal of a candidate or backend;
- migration job absence;
- namespace destruction;
- image removal.

Nothing is released on a mere stop request. The runner today never removes a loaded image, so an
image reservation is released when the image is removed. PR 4a adds that removal:

- Run it when the last installation referencing a `config_digest` is removed.
- Remove the image by ID, under the image lock.
- Prove absence afterwards.

If adding removal turns out to be larger than one bounded effect, the implementer stops and
reports. The 10 GiB limit then counts loaded images without release, which is a documented,
fail-closed gap.

### Portal

- **Installation cap.** `platformInstallationCap = 50` covers static and managed installations
  together.
  - It is counted over installations whose phase is not removed.
  - It is checked early, when a review is created, and again authoritatively in the
    consumption transaction that inserts the installation.
  - It returns `errPlatformInstallationCapacity`, mapped to HTTP 429 `installation_capacity`.
  - It is a constant, like the task quotas. No settings field is added.
- **Runner refusal.** The runner's `capacity_exhausted` outcome moves the operation to a typed
  failure. Its outcome and dimension are kept in the operation's failure detail, with no DDL.
  The management UI shows one message per dimension through `t()`, with zh-CN, zh-TW and en
  keys.

### Tests first (each must fail before its implementation)

- **Config:** defaults; ranges; unknown field refused.
- **Budgets:** derivation from host facts; not ready when the volume is too small or a fact is
  missing.
- **Journal upgrade:** v2 → v3, including backfill at the maxima for live resources; a fresh v3
  journal; a newer version refused.
- **Admission:** for each dimension, the request that fits is admitted, and the one that exceeds
  by one unit is refused with the typed outcome and leaves no row behind.
- **Edge cases:** replay; an unknown effect keeps its reservation; netting for update start and
  restore start; no netting before the old resource is proven absent.
- **Release:** on proven absence only.
- **Images:** counted once per config digest; removal of the last user releases it.
- **Portal:** the 51st installation is refused at review and at consumption, on SQLite and
  PostgreSQL; a removed installation frees a slot; mapping and UI message for every dimension;
  locale parity.

## PR 4b: acceptance

A `capacity-soak` job in `app-project-quota-probe.yml`.

- **Triggers.**
  - `workflow_dispatch` with a `duration_minutes` input, default 120, with timeout 170 minutes.
  - The same harness at 5 minutes on pull requests that touch the capacity code, so it cannot rot.
  - Only the 120-minute dispatch run is cited as OPS-08 evidence.
- **Reference host.** A GitHub-hosted `ubuntu-24.04` runner (4 vCPU / 16 GiB) and a 16 GiB XFS
  project-quota loop volume, reusing the quota probe's root runner.
- **Load.**
  - **Installations:** 50 in total. 42 are static. 8 are managed backends at their declared
    limits: 512 MiB and 250 millis each, which together are exactly the 4 GiB and 2000 millis
    budgets of the reference host. The backend fixture holds its declared memory, burns its CPU
    share and forks up to its PID limit.
  - **Floods:**
    - message, log and storage floods from the backends;
    - tasks and events driven to their quotas;
    - the over-declared layer upload.
- **Excess probes**, each refused with its typed error:
  - a 51st install;
  - a 9th backend;
  - a backend over the remaining memory or CPU;
  - an image beyond the image store;
  - a task over quota;
  - a staging upload over 2 GiB.
- **Measurements:**
  - idle baseline, then under load, every minute:
    - p95 of `GET /api/home` and of one report detail;
    - batch queue dispatch latency, from the existing queue measurement;
  - host memory, CPU, PIDs, the runner and Docker log sizes, and XFS usage, sampled every
    30 seconds.
- **Pass:**
  - every excess request is refused with its typed error;
  - both p95 values stay within 1.5× of the idle baseline;
  - dispatch latency stays within 1.5× of its baseline, which is how "unchanged" is defined;
  - the summed backend memory and CPU never exceed the runner budgets.
- **Evidence** is uploaded as JSON with every sample. `06-acceptance.md`, the OPS-08 and SEC-09
  rows and a new `handoff/evidence/platform-capacity-envelope/acceptance.json` cite only the
  dispatch run.

## Risks

- **Container limits.** CPU and memory budgets bound reservations, not instantaneous use. The
  container limits enforce each backend's share. The soak measures whether the sum stays inside.
- **Backfill.** Backfilling at the maxima can make an upgraded runner refuse work until each old
  resource is restarted. That is the intended fail-closed direction.
- **Netting.** The netting rule is the subtle part. It is allowed only when the old resource's
  absence is already proven in the same transaction.
- **CI timing.** A 2-hour soak on shared CI hardware can be noisy. The latency gate compares
  against a baseline measured in the same job, not against a fixed number.

## Resolutions (PR 4a)

Where the code contradicted or under-specified the plan, the resolution is recorded here rather than silently
applied.

1. **Where admission runs.** The plan puts the reservation and the check in the claim transaction. A refusal must
   also leave no resource row, and the resource row of a candidate, writable candidate or managed namespace is
   created by the *reserve* transaction, one step before the claim. Admission therefore runs in the transaction
   that creates the resource identity (`ReserveCandidate`, `ReserveWritableCandidate`, `ReservePrepareData`),
   which is still before any native call, and the claim re-checks that a `held` reservation exists
   (`requireHeldReservation`). For an update, restore or enable start the claim is the first transaction that
   has the descriptor, so admission runs in `ClaimUpdateStart`; a refusal there reverts the start and removes the
   never started pending target (a fresh one is deleted; one that reused the row of a removed target goes back to
   `absent`, revision 4). The migration job and the image load insert their command row at the claim, so their
   refusal is the row itself, inserted `reverted`.
2. **How the declared resources reach admission.** The claim and reserve functions keep their signatures (they
   have well over a hundred call sites). Each `Dispatch*` entry point parses the descriptor and attaches the
   backend's declared memory and CPU to the context (`withBackendResources`); namespace and image amounts come
   from the command and the descriptor entry. With budgets configured and no declared resources, admission fails
   closed with `ErrCapacityPlanMissing`, which is an internal error and never a typed refusal. A `Store` with no
   budgets configured reserves and refuses nothing; only the runner service configures them, and it reports not
   ready when they cannot be derived. No production path reaches a reserve or claim function except through a
   `Dispatch*`.
3. **Netting.** Netting is implemented as an admission-time release: a `held` backend or migration reservation of
   the same installation whose resource is `desired=absent, observed=absent` in the same transaction is released
   and not counted. A resource that is only stopped is not netted. Proven absence also releases at the removal
   itself (managed removal, update-start removal, reserved-start removal, orphan settlement, migration
   settlement and discard, namespace deletion), so netting is the safety net for a row the removal missed.
4. **Backfilled amounts.** A backfilled row is replaced by the declared amounts when a claim names the same
   resource id again (the upsert replaces its own row and never counts it twice). A running backend that is never
   claimed again keeps its maximum-size row until its removal releases it, which is the fail-closed direction the
   plan accepts. Reservations are not rewritten when a start merely completes: a completed start has the same
   resource id its claim already replaced.
5. **Image removal: the plan's fallback applies.** Removing the image of the last installation that references a
   config digest is not one bounded effect. The runner executes only signed commands, and there is no command
   kind that carries an image removal; adding one means widening the `runner_commands.kind` list (a rebuild of a
   table the owner did not approve for v3) and the Portal's effect kinds. Removing the image inside the
   delete-data or removal command would be a Docker effect outside the claim-before-native-call idiom, with no
   row recording it, and "last referencing installation" is not defined by the journal today. The 10 GiB image
   store therefore counts loaded images without release, with one exception: a *rejected* load that leaves no
   image of its own releases its reservation, unless another applied or unknown load holds the same config
   digest. This is the documented, fail-closed gap; the daemon's image garbage is not reclaimed by the runner.
6. **The refusal receipt.** `portal.runner.capacity-refusal/1-dev.1` carries the command identity (deployment,
   epoch, installation, operation, generation, stage, effect number, effect id, kind, payload digest), the outcome
   `capacity_exhausted`, the `dimension`, and the requested and remaining amounts as decimal strings. It is the
   receipt of a `reverted` command row, so a replay returns the same bytes. `remaining` is never negative: a
   dimension already over budget (after budgets were lowered) reports 0. A dimension a request does not use is
   never refused, so lowering a budget cannot refuse work that needs none of it. When several dimensions
   overflow, the first in the order memory, CPU, disk, image store, running backends is named. The runner
   answers it on the start-bearing actions only (writable candidate start and read, update/restore/enable start
   and receipt, migrate-data execute and read, load-image execute and read) and on the older candidate and
   namespace endpoints; the controller validates it against the signed command it sent and returns it as a
   `*journal.CapacityRefusalError`, never as an observation.
7. **Namespace disk.** `NamespaceReservationBytes` is 768 MiB, defined as the 256 MiB quota plus two retained
   checkpoints. The Portal carries the quota as the literal 268435456, so the constant is tied to it by value in a
   test, not by a shared symbol.
8. **Volume size.** The XFS volume is read with `statfs` on the data root (blocks times block size). `disk_bytes`
   is validated against the volume at startup, not when the file is loaded, because the config loader has no
   volume. The CI service job's 1 GiB loop volume is now a 9 GiB sparse file, because a volume smaller than the
   8 GiB default makes the runner report not ready. `deploy/app-runner/service.example.json` declares the
   defaults.
9. **Portal failure detail, without DDL.** The operation's existing `error_code` column (96 bytes) carries one
   closed code per dimension: `capacity_exhausted_memory`, `_cpu`, `_disk`, `_image_store`,
   `_running_backends`. The management and status views, which map unknown codes to a generic failure, pass these
   through, and the UI status parser accepts exactly these five. The full typed detail (outcome, dimension,
   requested, remaining, stage, effect, kind) is written to the audit log as `app_platform.capacity_refused` for
   every action. Only an installation finishes with the dimension as its code; the other actions finish by their
   own review path (their codes are fixed) and keep the dimension in the audit entry. A refused image load is
   settled as `reverted` on the Portal's effect row with the typed receipt, and is not read back.
10. **Installation cap.** The cap counts installations whose phase is not `removed`, every other phase included.
    A removed installation frees its slot. The HTTP mapping is 429 `installation_capacity`. It is checked when an
    install review is created and again in the transaction that inserts the installation; the enable and repair
    reviews of an existing installation are not counted.
11. **Not covered in PR 4a.** The Portal's own effect rows for a refused migration, update-start, restore-start or
    enable-start are left as they are for any definite runner failure that is not a receipt; only the typed error
    and the audit entry are new. The release hooks of managed removal, update-start removal, orphan settlement and
    namespace deletion are one-line calls to the tested `releaseReservation`, exercised through the reserved-start
    and migration paths, not through a native removal. No Linux-only path was run.

## Resolutions (PR 4a, review round 1)

12. **Imported namespaces are admitted.** `beginImportCheckpoint` reserves the 768 MiB namespace in its claim
    transaction, with the same typed refusal as every other start-bearing claim, answered on the import execute
    and read actions. Namespace deletion releases it.
13. **Enable and repair transfer their source.** The native order was verified: `ProvisionUpdateStartSockets`
    removes the stopped source container (`removeStoppedUpdateStartSource`) before the target is created. So
    the claim of an `enable-start` transfers the source's reservation to the target when the source is a
    backend of the same installation that is stopped or absent (`transferFrom`); a running source, or another
    installation's backend, is not transferred and both count. The running settlement records the source
    absent. If the start then fails before the removal ran, the target's reservation stands for the
    installation's single backend; the source's row is not resurrected.
    Amended: the source's reservation is no longer released at the claim, before its container's native removal.
    Admission nets it in the arithmetic only (the source is the same installation's stopped or absent backend and is
    not counted next to the target), and its row stays `held` until the start's running settlement records the
    source absent and releases it in that transaction. A source that is not proven absent keeps its row.
14. **Reservations of starts that provably never ran are released.** At admission, a held backend reservation is
    released when its resource is still the pending row the reserve wrote and its start command either ended
    `reverted` or was superseded by a newer fence of the installation before any claim (a claim at a superseded
    fence is refused). A claimed (`unknown`) or applied start is never released this way, and a reserved start
    nothing has superseded keeps its reservation, because a renewed command may still claim it.
15. **The backfill uses exact amounts where the runner can observe them.** The upgrade takes image sizes from
    the declared layer sizes in the stored load-image claim (the largest when several loads declare one digest)
    and namespaces from the 768 MiB constant. Backends cannot be read at upgrade time, so they are backfilled at
    the maxima and refined at startup: before it listens, the runner reads each live container's
    `HostConfig.Memory` and `NanoCpus` from Docker by native id and replaces the maxima (`RefineEstimatedReservations`).
    A daemon that cannot answer makes the runner not ready; a container the daemon does not have, a pending
    resource with no container, and a container with no limits keep the maxima. Those rows are the *estimated
    rows*: held backend reservations still at 4096 MiB and 4000 millis (a declared amount equal to the maxima is
    counted too, so the figure is an upper bound).
16. **A refused target is deleted or restored by its own history.** A refused update start deletes its pending
    target unless a removal receipt of that very resource exists, in which case the row goes back to `absent`.
    A `remove-code` effect of the operation says nothing, since every update carries one.
17. **A refused effect settles on the Portal as reverted.** A migration the runner refuses is ended like a known
    migration failure: the Portal effect is reverted with the typed receipt in the transaction that ends the
    operation for recovery, so no discard is asked of a reverted effect and recovery restores the checkpoint. The
    same typed receipt settles any other open effect with the refused identity (start, restore-start,
    enable-start, repair, import) when the operation finishes, and a start read back by the update settlement.
18. **Volume sizing.** The volume is blocks times `Frsize`. When `checkpoint_root` is on another filesystem than
    `data_root`, its volume must hold the checkpoint share of the disk budget, two thirds (two retained 256 MiB
    checkpoints per 768 MiB namespace); otherwise the data volume covers both. An unreadable checkpoint volume is
    not ready.
19. **Readiness facts.** The managed runtime profile carries `capacity`: the effective memory, CPU, disk, image
    store and running-backend budgets and the estimated rows, as decimal strings. PR 4b reads them there. The
    ready line carries them too.
20. **Image reservations never shrink.** One image is counted once, at the largest size any load declared, and
    keeps its first holder; the other installations that reference it are not tracked in the reservation.
21. **Backend counting.** `running_backends` counts live backends: a backend reservation is held from its claim
    until the removal of its container is proven, so a stopped (disabled) backend counts like a running one. A
    disabled backend whose enable replaces it does not count twice (item 13).
22. **The installation cap guard.** On PostgreSQL the cap is serialised by the publisher-trust
    `SHARE ROW EXCLUSIVE` table lock taken by every installation mutation; on SQLite by the database write lock.
    A concurrent test shows exactly one of two consumptions wins the 50th slot.
23. **Sizing note for PR 4b.** A host reports `MemTotal` below its nominal memory, so 25% of it is below
    4096 MiB on a nominal 16 GiB runner. PR 4b must size its eight backends from the budget the runner reports
    in its readiness facts, not from the nominal host memory.

### Follow-up that blocks the integration to main merge: image removal

**Status: resolved by PR 4c (#429).** The residuals that still block the merge are listed under "PR 4c residuals" below.

**Owner decision, 2026-11-13:** the `runner_commands` rebuild that adds the `remove-image` kind is approved and is
folded into journal v3 (no v4), because v3 has not shipped to any persistent runner. CI and development journals
opened at the earlier v3 shape are disposable and are not upgraded; the runner refuses a v3 journal whose
`runner_commands` declaration predates the kind, with instructions to start from a fresh journal.

The 10 GiB image store is not reclaimed by the runner (resolution 5). The merge of the integration branch to
`main` is blocked until the owner decides how removal is carried.

Operator reclaim path until then: raise `capacity.image_store_bytes` (up to 1 TiB) and restart the runner, or move
the runner to a fresh journal and daemon by following the fresh-host procedure.

What the removal command needs:

- **A new `runner_commands.kind`.** The kind list is a `CHECK` in the closed v3 declaration of `runner_commands`
  (`remove-image` beside `load-image`). SQLite cannot alter a `CHECK`, so adding it is a rebuild of that table,
  the same copy-and-swap the v1 to v2 upgrade did, plus a changed product SQL in `schemaProducts`. That is a
  schema change of the journal. If v3 has not shipped to any runner, the owner can fold it into the v3 upgrade (no
  new version); once v3 has shipped it is v4.
- **The command itself:** a signed `remove-image` command in `control` (format, logical digest, execute and read
  purposes, expiry), a lifecycle action for execute and read in `transport`, and its verification on the
  controller.
- **The effect:** a journal dispatch with the idiom of `load-image`: claim as `unknown` before any native call,
  remove the image by ID under the load gate (the daemon route `DELETE /images/{id}` is already in the allow-list and
  `removeLoadedImage` exists in the driver; it needs an exported runtime method), prove it absent, then settle
  `applied` and release the `image` reservation in one transaction.
- **The Portal:** an effect kind and an operation stage for it (`kind` has no database enum, so no Portal DDL, but
  the Go effect-kind allow-list and the stage order change).
- **Who owns "last referencing installation": the Portal.** Only the Portal knows which releases every
  non-removed installation still references, including rollback and restore targets kept in checkpoints. The
  runner still enforces the native guard: no container of any state uses the image.
- **Also required** (round-2 review of PR 4a):
  - The `schemaProducts` freeze test and the v1 to v3 and v2 to v3 upgrade fixtures change. Folding into v3 is
    only acceptable while no runner, CI or development host included, has opened a v3 journal.
  - A removal refused by the daemon because a container still references the image (HTTP 409) is a typed
    refusal, not an unknown effect.
  - `load-image` and `remove-image` of the same digest are fenced against each other: both run under the load
    gate, and a `remove-image` claim refuses while a load of that digest is `unknown`.
  - An unknown `remove-image` is resolved by proving the image absent or present by ID. It is never resent blind.
  - The image reservation is released only at `applied`.
  - The Portal defines what an image-store refusal means while a removal is pending.

### Follow-up that blocks the integration to main merge: candidate orphan settlement

**Status: resolved by PR 4c (#429).** The residuals that still block the merge are listed under "PR 4c residuals" below.

A candidate or writable-candidate start that was claimed and then failed natively stays `unknown`, so its
reservation is never released (`journal/candidate_execution.go:173`). Update starts have `settleOrphan`;
candidates have no settlement path. This follows "an unknown effect keeps its reservation", but without a
settlement it is a permanent leak with no remedy short of a fresh journal. The fix is a candidate orphan
settlement modelled on `update_start_settle.go`: prove the native resource absent by name and label, then
settle and release in one transaction.


## Resolutions (PR 4c)

These close the two follow-ups above that blocked the integration to main merge: image removal and candidate orphan
settlement. Resolution 5 (the image store is never reclaimed) no longer holds.

### Image removal

24. **The schema change.** The `remove-image` kind is folded into the v3 declaration of `runner_commands`
    (`schemaProducts(3)` and the freeze test). The v1 and v2 upgrades rebuild `runner_commands` to that v3 product
    (the same copy-and-swap, now one function, `upgradeCommandsToV3`) and then create and backfill the reservation
    table, so both end in the new shape; the version stays 3. A journal already at the earlier v3 shape is not
    converted: `inspectAt` recognises its `runner_commands` declaration and refuses it with `ErrEarlierV3Journal`,
    which matches `ErrIncompatible` and says to start from a fresh journal. The file is not touched. No Portal
    DDL and no other journal change was made.
25. **Where the Portal step runs.** It is not an operation stage. The operation stage list is a `CHECK` on the Portal
    `operations` table, so a new stage is a Portal schema change; and a stage in the middle of an operation would sit
    in front of start-bearing work. The step is a sweep, `reclaimPlatformImages`, that runs after an operation ends
    (the two places the scheduler already sweeps image blobs, the operation completing and the operation finishing
    for review) and on every idle tick, next to the load-image settlement. An uninstall and an update are ordinary
    operations, so both are covered; a cancelled or failed one is covered too, which is harmless because the
    computation reads references, not the operation's outcome. The effect kind `remove_image` is in the Go allow-list
    (`platformEffectKindAllowed`, journal stage `stage`).
26. **Identity of the effect.** Journal stage `stage`, kind `remove-image`, effect number 2 to 32 (number 1 of that
    stage is the load, and the runner's stage list is closed, so no stage of its own could be added). The command is
    hosted by the latest finished operation of the installation that loaded the image, and only while no operation of
    that installation is running, so its fence and generation are the installation's own and never below a floor a
    newer operation set. The effect id is derived from the host operation and the load effect it undoes, so every
    attempt and tick signs the same effect; the number is the one the row already has, or the next free in the stage.
    The row carries the intent (release, platform, manifest, config digest, layers, load effect) in its receipt until
    the runner's verdict replaces it, so a later tick can rebuild and read back the same command.
27. **"Last referencing installation", computed by the Portal.** An image is unreferenced when no release the Portal
    still needs carries its config digest. The needed releases are `loadPlatformReleasePins` (the active release of
    every installation that still exists, every release a retained recovery checkpoint or recovery bundle could
    restore, candidate and recovery-set releases, releases of unfinished operations, unsettled loads, unrecovered
    failures, open review plans) plus the previous release of each existing installation (the source of its latest
    completed update, kept for rollback). The candidate images are the latest applied load of each config digest per
    runtime that no applied removal is at least as recent as. It fails closed in four ways: a release that cannot be
    read returns an error and no target; a load receipt that cannot be read is never removed; a runtime with a load
    that has no verdict is left out whole; an image already loaded again after its removal is a target again, one
    removed and not reloaded is not. A removal that is begun and whose image is referenced again before it
    completes is not completed: its row is closed `reverted` with the outcome `referenced_again`, and the runner's
    unknown claim, if there is one, keeps its reservation.
28. **Runner behaviour** (`journal/remove_image.go`). The claim is committed as `unknown` before any native call, under
    the load gate, so it is fenced against a load of the same digest; the claim is refused, typed and with no row
    written, while a load of that digest is `unknown` (`ErrRemoveImageLoadPending`). The native guard lists
    containers twice (the daemon's ancestor filter, and a scan of every container's image ID) and refuses while any
    container of any state uses the image; the delete is sent with `force=false` and the daemon's 409 is the same
    typed refusal. A refusal settles `reverted` with the outcome `image_in_use` and keeps the reservation. The
    removal is by ID, followed by a proof of absence by ID, and then the effect settles `applied` and the image
    reservation is released in one transaction, and only there. An unknown removal is resolved by `ImagePresentByID`
    first: an image already gone settles `applied` (state `absent`) with no removal call, a present one goes through
    the guard again, an unanswered check settles nothing. The read-back answers the receipt, `pending` while the gate
    is held, `orphaned` for an unknown effect with the gate free, or `absent` for no row, under the gate.
29. **Installation-wide gates ignore the new rows.** Every runner and Portal reader that ignores `load-image` or
    `load_image` rows now ignores `remove-image` or `remove_image` rows too (26 journal statements, 19 Portal
    statements), so removal never holds back or raises a floor of a start-bearing command. Tests inject the row into
    every command family's fixtures and, on the Portal, into the readers that guard managed access, the candidate
    intent, install admission, the removal selection and the enable operation.
30. **An image-store refusal while a removal is pending.** The load refusal stays the typed
    `capacity_exhausted_image_store` and is not retried. The operator message (`err.capacity_exhausted_image_store`,
    in the three locales) says the image store is full, that images no installation uses are reclaimed only after the
    app versions that referenced them are removed, and that an administrator can raise the image store budget; it does
    not promise a reclaim within minutes. No key was added.
31. **Native coverage.** The image delivery acceptance child now runs the reclaim step after its uninstall and
    asserts the `remove_image` effect is `applied` with a `removed` receipt for the delivered image ID; the parent
    asserts, from the daemon itself, that exactly one image `delete` event names that ID, that the ID is gone, and
    that every other image of the worker is unchanged. The removal runs after the parent has finished inspecting the
    daemon and after the uninstall, so no later step of that test needs the image. The writable scheduler native
    test installs reference images, which carry no `backend.images` and so no load effect: it can never reclaim one,
    and an update keeps the previous release pinned.

### Candidate orphan settlement

32. **What settles.** A claimed candidate or writable candidate start whose command window has expired, with the
    execution gate free and the container proven absent by name and label, settles in one transaction: the effect
    `reverted` with the typed receipt `candidate_never_ran`, the resource `absent`, the reservation `released`. A
    container in any state, a failed inspection, a held gate or an open window changes nothing. A replay returns the
    same receipt (`lookupRevertedAnswer`).
33. **Where it is wired.** The settlement runs from the entry points the Portal's scheduler already reaches through the
    transport: `DispatchCandidate` (the unknown branch), `DispatchWritableCandidate`, and `ReconcileWritableCandidate`
    (which the writable start stage calls as `reconcilePlatformWritableStart`). The runner answers a settled start with
    the receipt over both lifecycle transports, and the controller side validates it against the exact signed command
    and returns it as `*journal.CandidateOrphanSettledError`. No new command kind was added, so the Portal needs no new
    effect; it receives a definite typed end where it used to receive only an uncertain error.
34. **The Portal's own effect row.** `settleOrphanedStartEffect` (`app_platform_candidate_orphan.go`) turns
    `CandidateOrphanSettledError` into a `reverted` row for the start effect (stage `start`, effect 1), keeping the typed
    receipt, compared on operation, effect id, payload digest, runtime, controller epoch and an admission fence no newer
    than the lease's. The scheduler ends the operation in the same transaction through the effect-settlement hook the
    capacity refusal uses, with the closed error code `start_never_ran` (public allow-list, status view and the three
    locales), and the audit entry `app_platform.candidate_never_ran` is written there once. A replay of the same receipt
    against the row it closed succeeds and changes nothing; a receipt that disagrees with the row settles nothing and is
    treated as the ordinary failure. The plain candidate start has no scheduler route and the install review finisher
    accepts only the writable format, so only the writable start is ended end to end; the plain candidate settles the
    same row through the same function.

### Round-2 minors from PR 4a

35. **Enable-start transfer.** The source's reservation is no longer released at the claim. Admission nets a stopped or
    absent source of the same installation, so the start is still admitted, and the row is released with a rows-affected
    check in the settlement that records the source absent (`settleEnableSourceAbsent`).
36. **`settleRefusedEffect`.** It now runs in the transaction of `finishAs`, under the same lease, fence and epoch, with
    the effect's payload digest compared in the UPDATE; a stale lease settles nothing, and a failure of the operation's
    end rolls the settlement back.

### Not proven here

- Every Docker path (the driver removal against a real daemon, the image delivery acceptance's removal and the
  `delete` event proof) runs only in the Linux native CI job; nothing was run against Docker locally.
- The Portal tests of the reclaim step run on SQLite only; the SQL is portable and goes through the same helpers as
  the load settlement, but no PostgreSQL run covers the new queries until CI.
- The Portal reaches the removal through the scheduler hooks and the idle tick; a test shows the idle tick and the
  hook send it, and a source check shows both operation endings call the hook, but no test runs a whole update or
  uninstall through the scheduler into a removal.

## Resolutions (PR 4c, review round 1)

37. **Remove versus install.** Closed on both sides. Portal: the references are computed again in the transaction that
    inserts the `remove_image` row (`prepareRemoval`), which takes the OU mutation lock and the publisher-trust lock
    every installation mutation takes (the SHARE ROW EXCLUSIVE table lock on PostgreSQL, the write lock on SQLite). An
    operation admitted first is pinned by the unfinished-operation and open-plan pins and stops the removal there; a
    removal inserted first is seen by any later load of the image, which resolves it inline
    (`resolvePlatformRemovalsForLoad`, item 45) rather than waiting. Runner: the signed command carries a
    watermark, `known_loads`, the effect ids of every load of the image the Portal saw applied on the runtime (one to
    32, canonical, ascending). The claim is refused with the typed `ErrRemoveImageReferencedAgain` when the journal
    holds an applied or unknown load of the digest outside the list, with no row written; a reverted load never
    counts. The error did not exist before; it means exactly this. The wire carries no reply for a pre-claim refusal,
    so the Portal sees an uncertain execute and a typed absence on read-back, leaves the row prepared, and on the next
    tick abandons a removal whose stored watermark no longer matches the loads it sees (`referenced_again`). An image
    with more than 32 loads is kept. Both interleavings are tested on SQLite; PostgreSQL is not reachable from this
    host.
38. **Enable source release.** `settleOrphan` (both branches) now releases the enable start's source through
    `settleEnableSourceAbsent`, in its transaction, after two absence observations of the source. Reachable since
    item 46.
39. **Orphan settlement on the Portal.** A runner answer of `candidate_never_ran` settles the Portal's start effect
    `reverted` with the typed receipt (lease, fence, runtime, effect id and payload compare-and-swap, one audit entry)
    and the operation ends failed with the closed code `start_never_ran`. A replay settles once. The plain candidate
    start has no scheduler route today; its settlement is tested alone.
40. **Retry of a refused removal.** The effect id carries an attempt number (0 keeps the old id). A refusal
    (`image_in_use`, the daemon's own 409 after the guard as `image_conflict`, `referenced_again`,
    `abandoned_outcome_unknown`) is tried again on a later tick as a new effect after a backoff of ten minutes per
    attempt made, capped at six hours. An applied removal is never retried, and no attempt begins beside an open one.
41. **Recovery pause.** The step does nothing while the Portal is paused for recovery: an image removed then could be
    one the recovery is about to restore.
42. **Pins.** Tests keep the image of an unfinished or queued operation's release, a `recovery_required` failure, an open
    review plan, and a release sharing its config digest with a pinned one; an image of another runtime is never sent
    through this runtime's endpoint.
43. **Orphan window.** Two absence observations a settle delay apart (two seconds) are required before a candidate or
    update-start orphan is settled. A daemon that completes a create later than that window still leaves a container
    the settlement has recorded absent; the namespace sweep that follows a settled update start removes it, a
    candidate has no such sweep, and this residual window is accepted.
44. **Untag events.** Docker emits `delete` for an image removed by ID that has no tag and `untag` only when a tag is
    removed, so the delivery evidence's rejection of `untag` events should hold for the delivered image, which carries
    no name. This is reasoning about the daemon, not something run here; the native job is the check.

## Resolutions (PR 4c, review round 2)

45. **A load resolves an open removal of its image inline.** Reclaim runs only after an operation ends and on idle ticks,
    so a load that waited for an open removal would have used up its bounded waits and failed the operation. The load
    path (`runPlatformImageLoad`, after its "already applied" short-circuit) now reads each open removal of its digest
    back under that removal's own identity: a receipt settles the row (applied: the image is gone and the load goes
    on as a normal load that uploads the blobs again; reverted: the image is still there); pending, the runner holding
    the gate, is a bounded wait; orphaned, a typed absence, a row never sent, or one whose host operation can no
    longer settle it, is closed abandoned (`referenced_again` or `abandoned_outcome_unknown`) and the load goes on.
    Reinstalling the same app while its removal is unknown completes (tested through `ensurePlatformReleaseImage`).
46. **Orphaned enable and restore starts are settled.** `control` accepts `enable-start` and `restore-start` besides
    `update-start` in the settle command and the reserved removal, with the same read-signed start reference and
    payload binding. The journal side needed no change, because those starts are stored as update-start rows; the
    enable source is released through `settleEnableSourceAbsent` in the same transaction, only after two absence
    observations. On the Portal, `settlePlatformHeldStarts` drives the existing held-start steps for finished enable and
    repair operations from the idle settlement tick, and a restore resume's orphan is settled from the recovery
    reconciliation (`never_ran` reverts the effect and returns the installation to review). Not wired: a managed
    restore that is not a recovery resume (its start command comes from a different loader), and an `enable-start`
    the runner holds only as reserved. After an orphan settles, an enable or repair stays `recovery_required`; the
    Portal does not complete the operation on its own.
47. **Open removals fail closed.** The open rows are read with no window, and a row whose intent cannot be parsed is
    treated as open for every image of its runtime, then closed `abandoned_outcome_unknown` by the step.
48. **Attempt cap.** At most eight attempts per image (the effect numbers of one host operation are limited to 32).
    After that the step stops without logging on every tick and `GET /api/admin/app-platform/storage-status` lists the
    image under `reclaim_refused` with its attempts and the last typed reason (`image_in_use`, `image_conflict`,
    `referenced_again`, `abandoned_outcome_unknown`, `unknown`).
49. **Recovery pause fails closed.** The pause is read with the strict reader: an unreadable pause stops the step, and
    the pause is read again in the transaction that inserts the row.
50. **The settlement closure.** `finishAs` strips the refused-effect settlement from the context before it calls the blob
    sweep and the image reclaim; tested.
51. **The second absence look is a heuristic.** The two-second delay between the two absence observations of an orphan
    settlement narrows the window in which a late daemon create is missed; it does not prove none can follow. The
    data execution gate is held during the delay.

### Follow-ups that block the integration to main merge: PR 4c residuals

Recorded from the round-3 review of PR 4c (#429). Each one is on a failure path, and each fails closed or
leaves work for an administrator. None of them may remain when the integration branch merges into `main`.

- **Orphaned restore start outside a recovery resume.** The runner admits capacity when it claims the start
  (`ClaimUpdateStart`), but nothing on the Portal sends the settlement for a managed restore that is not a
  recovery resume. Its target reservation is therefore held until an operator steps in. Fix: settle it from
  the path that finishes the restore, as `settlePlatformHeldStarts` does for enable and repair.
- **Enable or repair left `recovery_required` after its orphan settles.** No reservation leaks, but the
  operation needs an administrator. Product decision required: either complete it automatically as failed, or
  keep it for review with a clear message.
- **An enable start held only as reserved.** It holds no reservation, because admission happens at the claim,
  but it leaves a pending resource row behind. Either settle it or document why the row is inert.
- **A transient error abandons a removal.** In `resolveOpenRemoval`, an error reading the host lease or the
  release abandons the removal as `abandoned_outcome_unknown`, even when the runner has applied it. That is
  safe, because the load reloads the image, but the applied verdict is lost from the history. Fix: abandon only
  on typed errors.
- **Held-start settlement starves itself.** `platformHeldStartsToSettle` reads `LIMIT 8` in a fixed order, so
  eight starts that keep failing block every later one. Fix: rotate with a cursor, like the load settlement.
- **The late-create window.** The second absence check two seconds later narrows the window but does not prove
  absence (item 51). Fix: prove absence from the daemon's event stream, or leave the reservation held until
  the next reconcile has seen the resource absent twice, across the gate.
