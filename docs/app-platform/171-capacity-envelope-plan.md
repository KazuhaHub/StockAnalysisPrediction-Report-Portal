# Declared capacity envelope: implementation plan

Status: accepted 2026-11-13. This plan turns section 4 of
[doc 167](167-owner-decisions-remaining-acceptance.md) (OPS-08, and the part of SEC-09 the soak measures) into ordered,
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
| `reserved_cpus` | 1 on a host with at least 4 online CPUs, otherwise 0 | 0 – NCPU-1 |

At startup the runner turns the percentages into absolute budgets. It uses `MemTotal` and `NCPU`
from the Docker `/info` read it already makes. It also reads the size of the XFS data volume with
`statfs`. **Fail closed:**

- If the volume is smaller than `disk_bytes`, or a host fact cannot be read, the runner reports
  not ready with a typed reason and admits nothing.
- If the derived memory or CPU budget is smaller than one backend at the descriptor minimum, the
  runner also reports not ready.

The ready line and the readiness facts report the effective budgets, so the evidence can observe
them.

### Reserved CPUs

`reserved_cpus` is the number of the lowest-numbered online CPUs that no app container may run on.
The Portal, PostgreSQL and the runner keep them. A lower CPU weight did not fix the Portal's
latency under load (#442): quota-limited containers with many threads are released together each
period and the Portal queues behind them on the run queue, so the apps are kept off those CPUs
instead. `app_cpus` is an optional explicit app set (see the runbook).

- **Derivation.** The runner reads the online CPU list from `/sys/devices/system/cpu/online` and
  its own allowed set from `sched_getaffinity`. The reserved set is the lowest `reserved_cpus`
  online CPUs and the app set is the rest, for example `0` and `1-3` on 4 CPUs. If the online
  list, the allowed set and `NCPU` from Docker `/info` do not agree, the runner is not ready
  (`online_cpus_disagree_with_reported`). If the online list cannot be read, the allowed set
  stands in for it. An explicit `app_cpus` replaces the derived app set; it must be online, must
  not overlap the reserved CPUs and needs `reserved_cpus` above 0, or the runner is not ready
  (`app_cpus_invalid_for_host`).
- **Budget.** The effective CPU budget is `min(cpu_percent × NCPU, app CPUs × 1000)` millis, so
  the apps are never budgeted more CPU than the CPUs they may run on.
- **Create.** Every app container's `HostConfig.CpusetCpus` is the app set. A candidate, a
  writable candidate, the update-start plans (update, restore and enable starts) and a migration
  job all derive from `planCandidateContainer`, and `validContainerPlan` refuses a body whose
  `CpusetCpus` is not the set the plan was built for, even with a recomputed digest. Inspect
  equality compares `CpusetCpus` as an exact string, which is how Docker echoes it.
- **Policy digest.** A non-empty `CpusetCpus` is part of the create policy and so of the
  `policy-sha256` label. A runner that reserves CPUs computes different digests than one that does
  not, and so does one with a different app set. A container created under another setting is a
  conflict and is never adopted, started or removed by the runner.
- **Preflight.** The runner is not ready when the value is outside 0 to NCPU-1
  (`reserved_cpus_outside_host_range`) or when the managed runtime profile would exceed the
  Portal's 4096-byte decoder cap (`managed_profile_exceeds_size_cap`; only a very sparse online
  list can reach it). A reservation also reads the `CPUSet` flag of Docker `/info`
  (`cpuset_controller_unavailable`), but that check is **advisory**: dockerd derives the flag from
  the root `cgroup.controllers`, not from the controllers delegated to the slice its containers
  run in, so it can say yes where containers cannot be pinned and, rarely, no where they can.
  The enforcement is runc: it fails a container start when the container's cgroup has no cpuset
  file. systemd 244 and later enable the cpuset controller up the slice chain when `AllowedCPUs`
  is set on a unit, which is how a host that does not delegate cpuset by default is fixed (for
  example `AllowedCPUs=` on `system.slice` or the Docker unit). The native trial reads
  `cpuset.cpus.effective` from a container's cgroup, which is the proof.
- **Nothing reserved.** With `reserved_cpus` 0, `CpusetCpus` is left out of the create body, no
  CPU fact is read and no daemon flag is required. Behaviour and policy digests are as before.
- **Reported.** The ready line ends with `reserved_cpus=<list|none> app_cpus=<list|all>`, and the
  managed runtime profile's `capacity` carries `reserved_cpus` and `app_cpus`.

#### Upgrade: the Portal and the runner together

The Portal decodes the managed runtime profile strictly: it re-encodes what it decoded and refuses
any difference. A runner that reports `reserved_cpus` and `app_cpus` therefore makes an older
Portal fail every managed operation. **Upgrade the Portal and the runner together.** A runner with
`reserved_cpus` 0 omits both fields and stays readable by an older Portal.

#### In-flight operations

An install or update records the managed runtime profile it started against and compares it with the
live profile again when it settles. The comparison includes the CPU sets, as it already includes
`cpu_millis`. Changing `reserved_cpus` or `app_cpus`, resizing the host, or upgrading the runner to
this version while an operation is in flight therefore conflicts that operation
(`errAppPlatformRuntimeConflict`); start it again once the change is complete. Make the change
when no operation is running.

#### Runbook: the CPU set changed under installed apps

When the online CPUs change (a resize from 4 to 8 CPUs moves the derived app set from `1-3` to
`1-7`) or `reserved_cpus` is changed, every container created earlier no longer matches its plan.
The runner reports the cause: a container whose only difference from its plan is the CPU set is
refused with `cpuset_changed` (`driver.ErrContainerCPUSetChanged`, also an
`ErrContainerConflict`; the text names the live and planned sets). It is never adopted, started
or removed by the runner. The repair, enable and removal paths all observe the container through
the same plan, so they refuse it the same way: **the repair path cannot recreate a container under
the new CPU set**, because it has to remove the old container first and removal needs an exact
match.

To avoid it, before resizing set `capacity.app_cpus` to the set the installed containers already
use (read it from the ready line's `app_cpus=`), for example `"app_cpus": "1-3"` with the default
`reserved_cpus`. The app set, and every plan and digest, then stay the same across the resize; the
new CPUs stay unused until `app_cpus` is changed deliberately.

To recover once the set has changed:

1. Either restore the old set, by setting `app_cpus` to the value in the `cpuset_changed` text's
   container side and restarting the runner (no container is touched), or move to the new set:
2. Stop the affected apps in the Portal, so no container is running.
3. Remove each stopped container by hand with the Docker CLI as root on the host:
   `docker rm portal-app-<resource id>`. This is the only step outside the runner; it needs the
   container to be stopped and does not use `-f`. Data is not in the container, so nothing is lost.
4. Start or reinstall the app. The runner now finds no container, creates one under the new set
   and adopts it.

The manual removal in step 3 and the runner's handling of an absent container are not covered by
the CI native trial, which does not change the CPU set under a live container.

**CPUs offlined after start.** The online list is read when the runner starts. A CPU taken offline
later is caught at the next runner restart. Until then a create that names it fails with Docker's
"Requested CPUs are not available", which the runner reports as `app_cpus_unavailable`
(`driver.ErrAppCPUsUnavailable`, an uncertain effect like any refused create). Restart the runner
so it derives the set again, then follow the runbook above if the set moved.

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
  - both p95 values stay within 1.5× of the idle baseline (superseded by the owner's floor, resolution 59);
  - dispatch latency stays within 1.5× of its baseline, which is how "unchanged" is defined (the same bound, resolution 59);
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


### Inventory overlap by device and inode

**Status: resolved; see "Resolution (inventory overlap by device and inode)" below.** The
text that follows is the problem as it was recorded, and the comparison by `stat` it proposes was replaced by the mount table.

`candidateIPCReferenced` (`internal/apprunner/driver/candidate_ipc_linux.go`) decides whether a container mounts a data
namespace by comparing path strings after `filepath.EvalSymlinks`. `EvalSymlinks` does not follow bind mounts, so the
comparison cannot see through one. A local volume created with `type=none,o=bind,device=<data root>` is reported by the
daemon with a `/var/lib/docker/volumes/<id>/_data` source. That path names no overlap, the check passes, and a container
the runner did not start can mount a data namespace without being detected. Every caller of the inventory has the same
gap: the data writer, its recovery, and the writable candidate recovery.

Not fixed in the PR that admitted local anonymous volumes (a daemon-managed volume the list reports with an empty
`Source`); that change only stopped refusing the host for a shape it cannot misuse.

The fix is to compare identity, not names. `stat` each mount source and the namespace together with each of its
ancestors up to the data root, and report an overlap when any `st_dev`/`st_ino` pair of a source equals a pair of the
namespace or of one of its ancestors, or the source lies inside the namespace by the same measure (an ancestor walk of the
source reaches the namespace's `st_dev`/`st_ino`). A bind-backed local volume then resolves to the real directory it
mounts. A source that cannot be stat-ed for a reason other than absence stays a refusal, as it is today.

Native test required: a disposable runner creates a local volume with `type=none,o=bind,device=<namespace>` and a
container using it, and the writer must return `ErrContainerConflict`.

## Resolution (inventory overlap by device and inode)

**Status: resolved.** The native acceptance run is green: the foreign container job of the app runner probe workflow, run 43766579257
(`https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/43766579257/job/132973024277`), passed
`TestDockerAcceptanceBindBackedVolumeIsAConflict`, `TestBindMountAliasIsAnOverlap` and `TestDockerDialerRecordsThePeerMountNamespace` as
root against a real daemon, with the daemon listing the bind-backed volume as `/var/lib/docker/volumes/<name>/_data`. Real hosts show
mounts whose root is not a path (`nsfs` namespace files, root `net:[...]`); the table reader skips them.

This answers the follow-up recorded above, together with the absent-bind-source gap. The inventory
(`candidateIPCPass.referenced` in `driver/candidate_ipc_identity_linux.go`, behind `candidateIPCReferenced`) decides an overlap
by where a path really lives as well as by its name, in the data writer, its recovery and the writable candidate recovery alike,
because they share it. No schema, journal or wire change.

**Semantics.**

- **Place.** A mount source and the namespace are each resolved to a place: the device (major:minor) of the filesystem they are on
  and their path within that filesystem, read from `/proc/self/mountinfo` (field 3 is the device, field 4 the mount's root within
  the filesystem, field 5 the mount point; octal escapes are decoded). The covering mount is the one with the longest mount
  point, the latest of several at the same point. Two paths overlap when they are on the same device and one place's path is a
  prefix of the other's: the same directory, an ancestor or a descendant. That sees a bind mount of the namespace, of the data
  root or of a nested directory, including a bind-backed local volume, with no walk of the namespace: the cost depends on the
  mount table, never on data an application controls. (An earlier version of this change enumerated directories below the
  namespace under a bound of 4096; that let an application make its own writer refuse and was dropped.) It also reaches a
  namespace that does not exist yet.
- **Name.** The canonical source equals the namespace, is a prefix directory of it, is `/`, or lies under it. Kept as before.
- **Symlinks.** A source is canonicalised by `candidateIPCCanonicalize`, which follows links one hop at a time with at most 40 hops
  (the kernel's bound). A dangling link, a link partway along a path that does not exist, and a chain are judged by where they land,
  because Docker creates a missing bind source on each start and follows the link once the target exists. A cycle or too many hops
  is `ELOOP`, a refusal.
- **Absent source.** A component that does not exist ends the walk and the rest is appended as written, so an absent source is judged
  by where it would be created. Only Binds (`-v`) create a missing source; `HostConfig.Mounts` refuses one. The listing does not say
  which created a mount, so a missing source of any mount type is treated alike, which fails closed and is accepted.
- **Fail closed.** A mount table that cannot be read or parsed, a path no mount entry covers, a runner that is chrooted
  (`/proc/self/root` is not `/`), a lookup error other than absence, and a cancelled context are all refusals.
- **Recursive binds.** A Docker bind is recursive unless it says otherwise, and nothing the container list shows says otherwise, so every source is
  treated as recursive: a source also overlaps when any mount whose mount point lies strictly under the source's canonical path exposes a tree
  that overlaps the namespace (its device and its root within it, with the same prefix test). `-v /srv:/x` with `/srv/y` a bind of `/data` is
  therefore a reference to `/data/ns-a`. A non-recursive bind is never trusted as such: that needs a container inspect per container and
  only ever removes refusals, so an `o=bind` volume device is also taken as recursive.
- **Mount namespace and the daemon.** The mount table is the runner's own, so the check assumes the runner and dockerd share the host mount
  namespace. `SO_PEERCRED` names the process that listened, which is systemd under socket activation and the proxy behind a forwarder, so it
  does not identify dockerd. Instead, once per client (on its first dial), the socket's inode is found by path in `/proc/net/unix` and every
  process with a descriptor on it is found through `/proc/*/fd`. The socket is trusted only when at least one holder's executable is a
  `dockerd` and every holder is in the runner's mount namespace; socket activation (systemd and dockerd both hold it) passes and a forwarder
  to a remote daemon (no local dockerd holds the socket) is refused. Every dial is then checked: the answering process must be one of the
  vetted holders and in the runner's mount namespace, and one failure taints the client. The inventory read refuses a client that has not seen
  a vetted daemon. A client that was not built by `newDockerClient` has no socket peer and is not asked; only a test builds one.
- **Mount choice.** Of several mounts at the longest mount point the one mounted on top by the mount tree (parent ids) is used, not the last
  in the file.
- **Absent paths.** A `..` after a missing component can return to a directory that exists, and what follows it is resolved for real again.
- **Bind-backed local volumes.** A bind-backed local volume is mounted on the host only while a container runs, so a created or stopped container
  lists only the daemon's own `_data` directory, which the mount table cannot see through. For that reason a mount of `Type: "volume"` with
  the `local` driver is also inspected by name (`GET /volumes/{name}`, the only volume route on the runner's allow-list, GET only; creating,
  removing, listing and pruning stay closed, and a test pins that). When `Options.o` contains `bind` or `rbind` (with `type` none or absent),
  `Options.device` is taken as one more source of the container and judged exactly like a bind source: placed by the mount table, symlinks
  followed, an absent device under the namespace a reference. Any other local volume is judged as before. A failed inspect, a driver that
  differs from the list, options that are not a flat object of strings, a bind option with another filesystem type, a missing or relative
  device, and a local volume mount without a valid name are refusals. A local volume that is not a bind but whose options name a host path or
  a block device is refused (`ErrContainerConflict`): an overlay's `lowerdir`, `upperdir` or `workdir`, any `device` other than the literal
  `tmpfs`, or any option value that contains a `/`, since an unknown option that looks like a path is treated conservatively. A volume with no
  options, or only options that name no path (a size, a uid), keeps the path-based judgement. A volume is inspected once per pass whatever the
  number of containers that mount it; a pass over 300 containers on 300 volumes against a daemon on a unix socket took about 35 ms with one
  connection per request, so no connection reuse was added (the 5 s pass deadline is not approached).
- **Lookups.** Every filesystem lookup, the source canonicalisation, the namespace canonicalisation and the mount table read, runs under
  the one-in-flight slot and is abandoned when the context ends, so a stuck mount cannot hold the caller past its deadline. A lookup that
  does not return keeps the slot until it does; further lookups wait for it or give up at their own deadline.

**Behaviour change.** An absent source under the namespace used to be "not a reference" by design. That test is replaced by
`TestSourceAbsentUnderTheNamespaceIsAReference`, which pins the reverse for a bind and a volume mount and pins that a source landing
beside the namespace still passes. The foreign-container behaviour is unchanged: a container with no mount under the namespace does not
stop the writer, and a local volume with an empty `Source` is still skipped.

**Tests.** `candidate_ipc_identity_linux_test.go` (synthetic mount tables, so no privilege is needed): overlap for the namespace, an
ancestor, the filesystem root, a descendant, a sibling and an unrelated directory; an alias that names cannot see; a namespace that does
not exist yet; a different device; mount table parsing and stacking; unreadable, malformed and uncovering tables; absent sources inside
and beside; links into the namespace, into a subdirectory that does not exist, relative, chained, partway along an absent path, a link
elsewhere, cycles and the hop bound; cancellation and a stalled namespace lookup or mount table read. `daemon_peer_linux_test.go`: the mount
namespace verdict. Root-only: `TestBindMountAliasIsAnOverlap` makes real `mount --bind` mounts of the namespace, the data root, a
descendant and a sibling and reads the real mount table.

**Where the native tests run.** `.github/workflows/app-runner-probe.yml`, job `foreign-container-beside-writer`, on a GitHub-hosted
runner as root with a real daemon: `TestDockerAcceptanceBindBackedVolumeIsAConflict` creates a `type=none,o=bind` local volume of a
namespace, starts a container on it, requires that the daemon lists a `/volumes/` source, and requires `ErrContainerConflict` for the
namespace and none for a sibling; it also requires a conflict for a created container that was never started on a bind-backed volume, and for a created container whose bind
source is absent under the namespace.
`TestBindMountAliasIsAnOverlap` and `TestDockerDialerRecordsThePeerMountNamespace` run there too. The job runs on every pull request that
touches `internal/apprunner/**`. Everywhere else (the unit job runs unprivileged) the root-only tests skip.

**Limits.**

- The check is a point-in-time observation. A link retargeted, a directory renamed or a mount made between the check and the start is
  not seen, and it is not exclusion against host root. This is the rename-during-check race. Within one pass the mount table is read
  once but the lookups are several calls: a source is resolved (symlinks followed) and later placed by the table as two separate steps,
  and the table, the namespace and each source are read at different instants, so a change between them can pair a path with a stale place.
- A holder of the daemon socket that exits, with its pid reused before a later dial, would be taken for itself. A pidfd would close it
  and is not used.
- A mount hidden because a later mount covers its parent is not detected as hidden; the covering is judged only at the same mount
  point. A submount that is covered is treated as visible, which can only add a refusal.
- A filesystem the mount table gives one device number for different trees (a btrfs subvolume layout with identical numbers), or one
  that mints a different number per mount of the same tree, can hide or invent an overlap. A mount hidden beneath a later mount of its
  parent is mapped by longest mount point.
- A path through a regular file is `ENOTDIR`, a refusal.

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

## Resolution (refused install removal)

This closes the gap recorded in resolution 53 on the soak branch (`feat/app-platform-capacity-soak`): a managed install the
runner refused for capacity had already prepared its data namespace, which holds 768 MiB of the runner's disk budget, and
no flow could release it. Eight such refusals exhausted the budget for good. Items here are unnumbered on purpose: the soak
branch continues the numbering at 52, and the two sequences are joined when the branches meet.

**What the refused install looks like.** The trace in resolution 53 names the phase `review_required`. The code ends such an
install in `recovery_required`, with the install operation at stage `recovery_required`:
`finalizePlatformInstallForReview` treats any install that already owns a resource (here the namespace and the applied
`prepare_data` effect) as an unknown outcome, and `review_required` is reached only by an install that owns none. The
variant therefore admits both phases, and a test pins the phase the refusal really produces. Either phase is admitted only on
the evidence below, never on the phase.

**Eligibility** (`platformLoadNeverStartedEvidence`, `app_platform_removal_never_started.go`). It runs in the review and again
in the consuming transaction, because both load the removal projection, and a third time at every later stage through
`validatePlatformAdmittedRemoval`, which compares what it loads with the evidence the review named. All of it must hold:

- the action is uninstall, the installation is managed, enabled at generation 1, and its phase is `review_required` or
  `recovery_required`; no active activation exists;
- the installation has exactly one operation, its first-generation install, which ended
  `recovery_required` with an error code and no lease. Stage `failed` is not admitted: an install that owns a namespace
  always ends `recovery_required` (the resource makes its outcome unknown), so `failed` with a namespace is not
  reachable, and the close below accepts only `recovery_required`, so the two cannot disagree;
- every start-bearing effect of that operation (`start-candidate`, which is both the candidate and the writable candidate,
  `update-start`, `enable-start`, `restore-start`) is `reverted`. An applied start may have run; an unknown or merely
  prepared one may have, so they are refused too. This is stricter than "not applied or unknown" on purpose;
- every other effect is settled, none lies past the start stage (`load_image` and `remove_image` effects included: an
  unsettled one blocks the variant and the close alike, a settled one does not), and the `prepare_data` effect is applied,
  on this runtime and epoch, at a fence no newer than the install operation's own, with a receipt that names the
  installation's namespace and data format;
- the namespace is the installation's only one and is `active`.

Each condition refuses for its own reason, and an installation in `review_required` or `recovery_required` that is not
eligible gets `errAppPlatformPlanChanged` for every cause, as before the variant existed. Anything else fails closed with
`errAppPlatformPlanChanged`, the error the ordinary review gives. An install whose start is
unsettled stays refused until the orphan settlement (item 39) has reverted the start; the real settlement is tested to open
the variant. The review carries the evidence (`never_started` in the projection: the install operation, the release and the
`prepare_data` effect id, digest, receipt digest and admitted fence), so consumption detects a change. The field is omitted
from every ordinary review, whose JSON is unchanged.

**Steps.** The operation passes the stages in the model's order and runs no runner effect for `stop` and `remove_code`
(`advancePlatformNeverStartedRemoval`; the model is untouched). The delete-data intent takes the release, the review, the
fence, the namespace identity and the preparation receipt from the failed install and its applied `prepare_data` effect
instead of from a completed install and an activation, and signs the existing `delete-data` command. Admission also retires
the install's candidate activation. The ordinary settlement then marks the namespace deleted, the installation removed and
the operation complete, in one transaction. Effects are validated by a variant of the removal rule that allows only the
`delete-data` effect.

**Runner.** One change was needed, contrary to the preference for none. `loadDeleteDataSource` binds the deletion to the
namespace's container and refuses with "container history" when there is none, and a start refused at admission leaves no
container resource row behind (the plan's Admission section: a refusal leaves no resource or reservation row). When the namespace has no container, the deletion is now bound to the volume
(`requireDeleteDataNeverStarted`): the installation has no container resource at all, no command past the data stage is
anything but reverted, and the release, the generation (+1) and the writer fence (+1) match the volume the install
prepared. A namespace with any container keeps the container binding unchanged. The signed command format is unchanged and
verifies with the existing control verification. `settleDeleteData` was split so that its journal half
(`settleDeleteDataSnapshot`, which releases the reservation) is reachable by a journal test without a native data root.

**Closing the failed install.** The settlement of the never-started removal also moves the failed install operation from
`recovery_required` to `failed`, in the same transaction, keeping its error code and writing the audit entry
`app_platform.never_started_install_closed` with the removal's operation id (`closePlatformNeverStartedInstall`). The
transition is a new, explicit model method, `Operation.SettleNeverStarted`, limited to the install action and to
`recovery_required`; `Settle` is unchanged and still limited to the restore actions, and `platformNoPriorInstall` is
unchanged. It runs only on the removal's own reviewed never-started evidence, verified at the top of the same
transaction, and refuses while any effect of the install is unsettled. Because it shares the transaction, a failure of the
closing rolls the removal back and the application stays occupied (fail closed); the same receipt converges on retry. After the removal the
application can be installed again, with a fresh namespace.

**Not changed.** The uninstall of a started installation (its install operation stays `complete`) and its plan check and
phase gate; the standalone delete-data review, which still requires a retired activation and a completed install (the
uninstall is the way out for a refused install).

**Recovery bundle and trust quarantine.** Neither treats this installation specially. The quarantine considers only
installations in phase `ready` with an active activation and asks for a disable, which the variant refuses. The bundle's
`unrecovered` release pin excludes installations whose desired state is removed, which admission sets, and the retired
candidate activation is not pinned.

**Tests.** Portal (`app_platform_refused_install_removal_test.go`): an install refused at capacity is uninstalled through
the review and consumption API and ends removed, with no stop or remove-code effect and the delete-data command bound to
the install's preparation receipt; the scheduler loop passes the two stages; an unsettled start (prepared, unknown,
applied) is refused and a reverted one admitted, including through the real orphan settlement; each eligibility condition
fails closed on its own; a started install keeps the ordinary path and review; a replayed consumption admits one operation;
a settlement failure rolls everything back and the retry settles; the failed install is closed with the removal, atomically, and the application can be installed again with a fresh namespace; an ordinary removal leaves its install complete; a crash after the prepared effect fails closed to
recovery without a second runner command. Runner (`delete_data_never_started_test.go`): a namespace with no container is
accepted only with reverted starts, the volume binding holds for each field, a container keeps the container binding, and a
signed command is admitted and settled so that the namespace is absent and its reservation released, after which a
namespace-sized plan fits a budget of exactly one namespace.

**Not proven here.** PostgreSQL: the local database refuses the migration with a permission error, so the PostgreSQL
variants are left to CI. The native lifecycle (`app_platform_data_native_linux_test.go`) does not include a refused-then-removed
install: its runner has no capacity budget, so a refusal needs a configured budget and a second installation through the whole
harness, which is not a cheap addition and would run only in CI.

## Resolution (relay admission before session reads)

This closes the Portal defect the soak branch (`feat/app-platform-capacity-soak`, resolutions 63 to 72 and its findings
table) recorded: the report page and the batch scheduler missed their latency bounds on a file-backed SQLite Portal while
the message flood ran.

**Cause.** Every backend relay attempt paid the session reads before it reached its admission budget, and about 91% of
attempts under the flood ended in the budget's 429. On SQLite the reads queue on the pool's single connection, so a refused
attempt delayed everything else. Counting the driver's operations, one refused attempt issued 10 statements over 9 connection
acquisitions: the cookie check and user read in `currentActiveUser`, the activity stamp when due, the enrolment gate's
policy transaction (two reads) and setting read, then the `platformHTTPReader` reads (user, session, account generation).

**Decision.** `platformBudgetPrecheck` wraps every session route guarded by a `platformDocumentBudget`, outside
`requireUserJSON`: the backend, network and network stream relays, and the launch, status, apps, reports, objects and
document routes. When the route's budget is full it answers that route's own 429 (the same function writes it from the
authoritative path, so status, headers and body are one definition) before any database read. `full()` is a non-blocking
capacity read.

- It never takes a slot. A slot held before authentication could be held by an anonymous client, so the authoritative
  `acquire` stays after the session reads, and a request that passes the pre-check and loses the race to another request
  gets the same 429 as before.
- **401 or 429 for a full budget.** Answering 429 before authentication would turn an anonymous client's 401 into a 429 and
  tell it that the budget is full. The pre-check therefore answers 429 only for a request carrying a session cookie this
  server signed and that has not expired (`platformSessionCookieSigned`: the HMAC, the expiry and the session message
  shapes `verify` routes, `v2|`, `v1|` and the legacy `user|exp`; no database read, which `verify` cannot offer because it
  reads the session row). The signing key also signs the owner attribution token, so a valid signature alone does not
  qualify: that token has a different shape and is refused the pre-check. Every other request, whether with no cookie, a
  malformed one, a forged one, an expired one or an attribution token, takes the unchanged path and gets the unchanged 401.
  A caller learns that the budget is full only if it already holds a credential the server issued.
- **The visible change.** A properly signed session cookie is answered with 429 while the budget is full even when the
  unchanged path would have refused it differently: a revoked session, a disabled account, an expired account
  (`accountExpired`), a stale `SessionRev` after a password change, and a cookie the enrolment gate would refuse (403, or 503
  when the policy cannot be read). The next attempt after the budget frees is refused as before. The client learns nothing
  about the session. No authorization outcome changes: nothing is admitted that was refused.
- **Activity tracking.** A request refused by the pre-check skips `touchSeen` and the `user_sessions.last_seen` stamp. Neither
  is security-relevant: session expiry uses the signed `expires_at` and the revision, never `last_seen`, and no idle timeout
  exists. `last_seen` is shown in the sessions list and the administrator user list (and orders the former), and a refused
  attempt is not activity the application served. A user whose only traffic is refused relay attempts is not otherwise
  affected, because the page and every other route still stamp it.
- Validation refusals that precede authentication today (origin, content type, path) are likewise not reached by a signed
  client while the budget is full. They are not authorization outcomes, and the same request is refused the same way once a
  slot is free.
- Routes that take the slot deep inside an administrator handler (`platformManagementSlot`) are unchanged: they sit behind
  the administrator permission gate and carry no flood.

**Statement cache.** The two session reads named in the defect, and the cookie check and user read of `currentActiveUser`,
already run through the statement cache (prepared once, never on the driver's direct path). The enrolment gate's policy read
(`loadOUPolicySubject`, a read-only transaction on the raw pool) does not: its two statements run on the driver's direct
path on every request. It is no longer on the refused path, and is left for a separate change because it is the shared
authentication gate of every session route.

**Tests** (`app_platform_admission_test.go`). A full budget with a signed cookie answers the identical 429 on all nine routes
with zero database operations (recording spy driver); an unsigned, malformed, mis-signed, wrong-key or expired cookie gets
exactly the answer it gets with a free budget; the pre-check never holds a slot, an unauthenticated client never leaves one
held, and a request that passes the pre-check and loses the acquire gets the same 429; the session reads use the cache.
`BenchmarkPlatformRelayFloodSmallReadP95` (file-backed SQLite, one connection, about 3500 paced relay attempts a second
against a full budget, 2000 small reads): small-read p95 263 to 776 microseconds with authentication first, 76 to 98
microseconds with admission first (p50 37 to 83, against 33 to 42).

## PR 4b implementation notes

PR 4b is the acceptance harness: no production code, no schema and no journal change. OPS-08 stays pending until a real 120-minute dispatch run has passed, and SEC-09 stays partial even then; the records are written so that only the run URL and the
numbers need filling (`handoff/evidence/platform-capacity-envelope/acceptance.json`).

### What was added

- `capacity-soak` job in `.github/workflows/app-project-quota-probe.yml`: `workflow_dispatch` with `duration_minutes`
  (default 120, at most 140, `timeout-minutes` 170), `soak_only` and the diagnostic `soak_variant`; five minutes, with a diagnosis, on pull
  requests touching the capacity code, the code behind the latency fixes or the soak (the path filters gained
  `internal/app/app_platform_capacity*`, `internal/app/app_platform_installation_capacity*`, the soak's Python test and, in round 1,
  `internal/apprunner/**` and the relay, store, batch and queue files of resolution 71). The existing two jobs are skipped when `soak_only` is set.
- `scripts/acceptance/capacity_soak.py` (with `scripts/capacity_soak_test.py`): builds the owned 16 GiB XFS volume with the
  quota probe's ownership checks, prepares the runner's journal with the existing native journal test, runs the Go
  acceptance while sampling the host every thirty seconds, then judges the Go evidence and the samples together.
- `scripts/acceptance/soak_app/` (backend fixture, `Dockerfile`, `build-image.sh`, `cleanup-image.sh`): the soak backend
  and its disposable local registry. It reads its limits from its own cgroup, so the soak needs no control channel
  into the container.
- `internal/app/app_platform_capacity_soak_native_linux_test.go`: the parent (root, owns the runner service, as the image
  delivery acceptance does) and the Portal child. `app_platform_capacity_soak_plan_test.go` and
  `app_platform_capacity_soak_evidence_test.go` hold the helpers and run in the ordinary suite in milliseconds; the native
  scenario is gated on `APP_CAPACITY_SOAK_IMAGE` and skips otherwise, so the package's test time does not change.

### Requirement to implementation

| Requirement | Where it is met |
| --- | --- |
| 50 installations, 42 static and 8 managed | `installStatic` (frontend install through the ordinary scheduler path) and `installManaged` (the writable install worker, a real runner) |
| Eight backends sized from the reported budget | `soakBackendSizing` over `ManagedRuntimeProfile.Capacity` (item 23) |
| Backend holds memory, burns CPU, forks to its PID limit, floods logs and storage | `scripts/acceptance/soak_app/soak.go` |
| Messages | the flood process sends every backend a signed request through the Portal's HTTP route continuously (`startFlood`); see resolution 55 |
| Tasks and events to their quotas | `floodQuotas`; the per-application quota is unit level (resolution 54) |
| Over-declared layer upload | `probeLayers`, through the offline import route |
| Excess probes | `probeBackends`, `probeLayers`, `probeStaging`, `probeInstallationCap`, `floodQuotas` |
| Idle baseline, then per-minute p95 and dispatch latency | `measureIdle`, `load`, `soakSummarize` |
| 30-second host samples | `Sampler` in `capacity_soak.py` |
| Pass criteria | `evaluate` in `capacity_soak.py`, tested in `capacity_soak_test.py` |

### Resolutions (PR 4b)

52. **Sizing leaves one minimum backend unreserved.** Each backend declares `floor((budget - one minimum backend) / 8)`,
    not `floor(budget / 8)`. The runner names the first overflowing dimension in the order memory, CPU, disk, image store,
    running backends (item 6), so with the budgets exactly filled a ninth backend is refused as `memory`, never as
    `running_backends`. Leaving 16 MiB and 25 millis free lets a minimum ninth backend fit memory and CPU and be refused
    by the count itself; the memory-over and CPU-over probes then ask for one unit more than what remains. The eight
    together still fit, a ninth does not, and the result is checked to lie inside the descriptor ranges unclamped.
53. **Refused probe installations are retired through the real managed uninstall, on the declared envelope.** Eight managed
    namespaces reserve 6 GiB of the 8 GiB disk budget (768 MiB each), and a backend refused for memory, CPU or the count has
    already prepared its namespace (the data stage precedes the start). The first smokes found that nothing could remove such an
    install (the review plan was refused, so the third probe was refused with `disk`), and the soak worked around it by marking
    the rows removed and raising the disk budget to 10 GiB. The never-started removal (resolution "refused install removal"
    above, #434) closes that gap, and both workarounds are gone: each probe's installation is retired by the ordinary
    managed uninstall (`retire`), which must complete and leave the installation `removed`; a refusal fails the soak and is
    recorded as `retired: "refused"` with the refusal in the probe's detail. The evidence records `retired:
    "managed_uninstall"` per probe and the verdict checks it. The runner runs on its declared 8 GiB disk budget, so a `disk`
    refusal of a later probe would show a namespace that was not released. Static installations use the frontend profile
    and hold no runner namespace, as in production.
54. **Task quotas.** The per-installation quota is reached on seven managed installations and the global quota on the
    eighth, after a backlog of waiting tasks attributed to the static installations (inserted with the columns the
    admission itself writes, since a static installation has no backend and cannot schedule tasks) brings the Portal to
    9999 active tasks: one more is admitted and the next is refused although its installation holds about 100 and its
    application far fewer, so only the global quota can have refused it. The per-application quota (1000) needs four
    installations of one application and the cap holds fifty applications, so it is proven at its boundary by
    `TestTaskQuotasRefuseAtTheirOwnBoundary` and recorded as a unit-level probe. The managed releases declare
    `background.run`, `tasks.schedule`, `events.subscribe` and `reports.read` as required capabilities, which the install
    review grants itself: the real grant path. Direct grant rows, which the first version used, do not satisfy the event
    subscription and are not used.
55. **Messages.** The fixture has no outbound channel to the platform, so the message flood is the platform's inbound
    signed request path, driven from a separate process over the Portal's HTTP route, plus the backends' log lines. The
    relay is driven at the Portal's admission cap of 2 concurrent requests (`platformDocumentBudget`, `app_platform_http.go`);
    excess attempts are refused 429 `app_platform_busy`, about 92% of the attempts of the first smokes. That is the real
    production admission behaviour, so the load stays as it is, and it is described as it is: one attempt is in flight per
    backend, at most two are admitted Portal-wide, and the rest are typed refusals. The evidence records, for every window, the
    attempts, the answered and the busy counts and every other outcome by status and code (`classes`); the verdict requires a
    minimum of answered requests in every window, a typed refusal for the rest (no 5xx, no transport error) and a live flood
    process with a fresh heartbeat throughout (resolution 65). "As many messages as the platform lets a backend send" is not
    exercised as a backend-originated flood; this is a stated gap.
56. **Image store: native when the daemon disk holds it, unit level otherwise.** The first version claimed the store needs "three
    real 4 GiB images". The refusal happens when the third is reserved, so two loaded images (8 GiB declared) are enough, and the
    probe now runs natively. A descriptor declares at most 4 GiB of layers for one image and at most 1 GiB for one layer (the first
    version declared eleven 1 GiB layers and the descriptor parser refused it), so each probe image is the soak backend's executable
    as a first layer and four filler layers of zeros that bring the declared total to the maximum less 1 MiB (4293918720 bytes). A
    layer of zeros compresses to a few MiB, so what crosses the Portal's offline import and the control socket is small while the
    daemon holds the real bytes; layers differ by seed so no two images share a blob. The images are imported through the real
    offline route and installed through the ordinary writable install worker, with a memory declaration one MiB over the runner's
    whole budget: the runner loads the image (the load precedes the start's admission) and refuses the start for memory, so no
    container is ever created from a 4 GiB image (the first native attempt started them, and the daemon's container create timed out
    on one of three smokes). Two images load and reserve 8 GiB; the third is refused `capacity_exhausted_image_store` at its load. The
    three installs are retired through the managed uninstall and the image reclaim removes both images. The harness measures the free
    disk of the Docker store before the run (the job also records `df`); the probe runs natively only when it holds 8 GiB plus
    6 GiB of headroom, and otherwise stays at unit level citing `TestLoadImageReservesTheDeclaredBytesOnce`,
    `TestLoadImageRefusedRunsNothingAndReplays` and the image store case of `TestAdmissionRefusesEachDimensionByOneUnit`, with the
    measured free bytes as the recorded reason. The hosted runners had 90 to 94 GB free on `/dev/root` (the daemon store and the
    loop volume's backing file share it), so the probe ran natively in every smoke of this round.
57. **Staging cap.** One upload is bounded at 384 MiB (`MaxManagedOfflineEnvelopeBytes`) and at most two management
    requests run at once, so no upload can exceed 2 GiB and two in-flight uploads cannot reach it. The cap guards the sum
    of what is held and in flight. The soak reserves 384 MiB at a time against its own real store until the sixth is
    refused and checks the typed 507 `app_platform_storage_full`; `TestStagingCapIsTwoGiBAndRefusesTheOneThatDoesNotFit` proves the
    exact boundary of the production value without writing a byte.
58. **Over-declared layer.** The crafted package declares one byte less than its first layer holds and is refused by the
    import route with HTTP 400 `app_platform_invalid_request`, storing no release. The runner's refusal of the same
    bytes (`uncompressed_bound`) is proven by the blob channel tests; the soak does not sign a blob command of its own.
59. **Latency.** Home and the report page are `GET /api/home` and `GET /api/stock/{symbol}` of a seeded report, served
    in process through the real routed handler with a session cookie. Batch queue dispatch latency is the
    queue's admission decision over a backlog of queued jobs (`queuedItems` and `queue.Admit`), the measurement the existing
    isolation test uses, not the duration of a Dify run. The first version used a backlog of 50 jobs, the figure of the existing
    isolation test; the backlog is 200 queued jobs, chosen in commit `5017f1aa` after the first smoke at 50 and before the
    evidence run. The same run also times the decision over the first 50 of them
    (`soakQueuedItems`, the same pricing and filters) and records it as `dispatch_shallow`, so the effect of the backlog
    is visible; only the 200-job decision is judged, and if it fails the gate the finding goes to the owner rather than
    to a tuned fixture. A smoke at 50 jobs passed with ratios of about 5 (before the floor); the 200-job figure is the one judged. The baseline pools the idle windows (100 samples of each series
    per window); the loaded samples of every minute are pooled for the verdict (see the interpretation below).
    **Owner decision 2026-11-13: within max(1.5x idle, idle + 5 ms), measured with load generated outside the measured
    process.** The first smoke measured p95 of 3 to 25 ms against a sub-millisecond idle baseline (9 to 18 times), and
    with the soak's own flood goroutines paused the ratios fell to 1.4 to 2.0: a ratio alone cannot judge a baseline
    that small, and the generator shared the measured process. The verdict is now `latency_threshold_us(idle) =
    max(idle * 3 / 2, idle + LATENCY_FLOOR_US)` with `LATENCY_FLOOR_US = 5000`, tested at both rules and where they meet
    (10 ms), and the evidence records the effective threshold of every window.
    **Verdict computation (engineering decision, not the owner's): pooled p95 over the loaded period; per-window p95 are
    diagnostics.** The owner decided only the floor. Pooling is the engineering lead's reading of the sentence "Portal p95
    latency ... within 1.5x of the idle baseline" of doc 167 section 4, adopted after the first smokes and before the evidence
    run; doc 167 records the two separately. Doc 167 asks for the p95 over the soak, one figure, so the verdict pools every loaded
    sample of the run into one p95 per series and compares it with the threshold from the idle p95 (itself pooled over the
    idle windows). For every series the evidence and the job's step summary list each window's p95 and its threshold, the
    count of windows over it and the worst window; none is hidden and none decides. The strict rule that judged every
    one-minute window was the earlier implementation choice, stricter than the criterion and hostage to one scheduler
    hiccup in about a hundred samples on a shared runner (two smokes failed on single windows, once by 281 microseconds);
    it was replaced before the 120-minute evidence run for that reason. The flood generator is a second process
    (`TestPlatformCapacitySoakFlood`, started by the child) that drives the Portal's real
    `POST /api/app-platform/installations/{id}/backend` route over loopback, one request in flight per backend, so the
    measured process carries the platform's work for those requests and none of the generator's. The refusal flood of
    task admissions that ran in the measured process is gone from the load; the quotas are still driven to their limits
    and probed once, before the load, by `floodQuotas`. The quiet windows of the first smoke were removed.
60. **Host budget judged on the kernel's counters.** Summed backend CPU is the cgroup `usage_usec` delta over each
    thirty-second interval when it can be read (a rate sampled over a second can read a few percent above a quota the
    kernel enforces exactly over longer windows) and Docker's own figure otherwise; the evidence says which. Memory is
    Docker's usage without reclaimable cache.
61. **Journal and defaults.** The runner uses its default budgets (25% of memory, 50% of CPUs, 8 GiB disk, 10 GiB image
    store, eight backends) on a journal prepared with the existing native journal test, which holds no reservations
    because that store configures no budgets.

62. **Proof that the backends ran their loads.** The backend's own log is the flooded stream and rotates in seconds, so it is
    no channel. Each minute, after the window's samples, the child asks every backend for its status through a signed request
    (`GET /v1/soak/status`: memory held, threads started, CPU burners, log bytes written, storage written and the number of
    times the namespace filled to ENOSPC). The last window's statuses must show every backend held at least half of its memory
    limit, started at least eight threads, burned CPU, wrote its log and filled its data namespace; a backend that answered
    the readiness probe and did nothing would otherwise pass.

### Resolutions (PR 4b, review round 1)

The numbering continues from 62. Evidence runs of this round are pull request smokes and five-minute diagnostic dispatch runs; none
is evidence for OPS-08.

63. **The session outlives the run (blocker).** The session and cookie of the soak child expired an hour after it started
    (`platformActorTestServer`), 55 minutes into a 120 minute load, after which every home and stock request and the flood would
    answer 401. The child now writes the session with `soakSessionLifetime(minutes, baseline)`, the run plus an hour, to the
    session table and signs the cookie with that expiry; the evidence records `session_expires_ms` and the verdict requires it to be
    after the load. `TestSoakSessionOutlivesTheRun` pins the derivation.
64. **The runner service outlives the run (found by the first smoke of this round).** The shared fixture starts the runner service as a
    test process with `-test.timeout=10m`. The first smokes ended before ten minutes, but with the three minute baseline the service
    was killed in the middle of the load, and a 120 minute run would have lost it after ten minutes. The fixture takes
    `APP_RUNNER_SERVICE_TEST_TIMEOUT` (test code, no production change) and the soak sets `soakRunnerServiceTimeout`: the run, the
    baseline and half an hour.
65. **The message flood is checked in every window.** The old check needed `sum(ok) > 0` over the run. Now every one-minute window
    must show at least 20 answered requests, a typed `429 app_platform_busy` for the rest (the evidence keeps every outcome by status
    and code per window; a 5xx, an untyped answer or a transport error fails), and a live flood process whose counters file was
    written within ten seconds (the flood writes a heartbeat once a second; the child records whether the process is alive when each
    window ends).
66. **The backends are checked in every window from their own cgroup counters.** The host sampler reads each backend's `cpu.stat`,
    `memory.current`, `memory.max`, `cpu.max`, `pids.current` and `pids.max` every thirty seconds. In every one-minute window each
    backend must have used at least 70% of its CPU quota (from the counter's delta over the window's intervals), held `memory.current`
    at least 85% of its limit, and its own status counters (log bytes and requests served) must advance between windows. Over the run
    each backend's PIDs must reach 85% of its limit. A backend that missed a status request is judged across the gap, and missing more
    than max(1, 2%) of the windows fails. The achieved fractions per backend are in the verdict and the step summary.
67. **Backends at their declared limits, as achieved.** The fixture holds 85% of its memory limit (60% before) and sets a Go soft memory
    limit at 95% of the cgroup's, so the collector runs before the kernel kills the container; it parks threads up to the PID limit
    less 8 (24 before). In the smokes of this round (eight backends, 497 MiB, 240 millis, 64 PIDs each): CPU used 0.998 of the quota at
    the lowest in every window; `memory.current` at least 0.997 of the limit (it includes the page cache of the storage flood, so
    the held 85% is the floor, not the figure); PIDs at 57 to 60 of 64, 0.89 to 0.94 (41, 0.64, before). The achieved numbers, not
    "at the limit", are what the dispatch run's evidence and the docs cite.
68. **Unit level probes are reported as such.** `image_beyond_the_image_store` (when it stays at unit level) and
    `task_over_application_quota` are recorded `level: "unit"` with `observed: null`, no pass, and the names of their tests; the
    old records set `Observed` equal to `Expected`. They are excluded from the native "every excess request refused" check and listed
    separately, and the job runs the cited tests (`go test -json`) before the soak and hands the result to the verdict, which fails if a
    cited test did not pass.
69. **The measured Portal is production-shaped and runs the real batch scheduler (SEC-09).** The measured Portal is a file-backed SQLite
    database opened with `OpenStore`, as the server opens it (the first version used the in-memory template store of the unit
    suite). It runs the real scheduler: `scheduleTick` (and `scheduleLoop`) with the real Dify provider against a local stub Dify, a
    standing backlog of 200 queued jobs and three long runs that hold the budget's slots, as a busy queue does. A scheduler admits what
    it can, so the backlog is kept in place by that: every slot is held, and each probe job (priority 90 against the backlog's 50)
    is given exactly one extra slot for its own lifetime (the budget setting is raised by one and lowered again before the probe's run
    ends), so the admission that starts it is the scheduler's own and the backlog is never drained
    (`TestSoakBatchProbeIsStartedByTheRealSchedulerAndLeavesTheBacklogQueued`, which fails if the budget is not lowered). A job is
    enqueued every three seconds; its enqueue-to-start latency is the time from before `CreateBatchJob` to the request reaching the stub,
    pooled and judged with the same floor rule as the pages (`batch_start`), idle baseline included. The task worker and the event
    delivery coordinator are not started: the soak's tasks are admitted with a run time an hour out, so none is due, and the event
    subscription is fenced and its source events kept; running them under the load is not done and stays a stated gap, as are
    backend-originated requests (the fixture has no outbound channel) and PostgreSQL.
70. **Probe descriptions say what they drive.** The staging probe is a store-level probe (384 MiB reservations against the real staging store,
    the refusal mapped by the real error handler); driving the upload route would write gigabytes of unpinned packages, since the route
    holds at most 768 MiB at once. The event probe floods 64 source events at a subscription that allows one delivery in flight and asserts the
    fence (`review_required`, reason `inflight_limit` in the audit log), every source event kept and one delivery task. The task probes call
    `admitPlatformTask` in the Portal process, the function the backend task route calls after it verifies the service identity; no backend sends
    them. The layer probe is the Portal's offline import route; the runner's refusal of the same bytes is covered by the blob channel unit tests.
71. **Sampling and host checks.** A window's 100 samples are spread evenly across its minute (one every 600 ms) instead of a burst; the
    host samples must number at least 90% of those expected and failed samples are counted from the error rows; host free memory must stay
    above 10% (the container caps sit below the budgets, so the two budget checks alone cannot fail); the dispatch configuration's baseline
    (three minutes) is the smoke's too; `duration_minutes` is capped at 140 and validated in the job; the pull request path filters gained the
    code behind the latency fixes (`internal/apprunner/**`, `app_platform_backend_http*`, `app_platform_managed_access*`,
    `app_platform_writable_request*`, `store.go`, `store_stmtcache*`, `batch_run.go`, `internal/queue/**`) and lost `go.mod` and `go.sum`.
72. **Diagnostic variants.** The dispatch run takes a `soak_variant` input (`memory-db`, `no-batch-probes`) that removes one part of the scenario
    to tell their shares apart. A variant is recorded in the evidence and never counts as OPS-08 evidence.

73. **OPS-08 evidence is a PostgreSQL run; SQLite is a recorded known limit (owner decision 2026-11-14).** The owner runs the Portal on
    PostgreSQL in production, so the evidence run measures that shape. The `capacity-soak` job starts a `postgres:16-alpine` service (the health
    check and DSN form of `test.yml`) and the measured Portal opens it with `OpenStore("postgres", dsn)`, the code the server runs. The soak
    drops and recreates the schema before it starts (only on a loopback DSN, `soakPostgresLocal`), so every run begins empty, and the evidence
    records `db_driver` and the server version. Everything else is unchanged: runner, backends, floods, probes, the real batch scheduler against
    the stub Dify, the verdict and the diagnostics. A `soak_db` input (`postgres` by default, `sqlite-file`) keeps the file-backed SQLite
    measurement runnable; a SQLite run never sets `counts_as_ops08_evidence` (`TestSoakDatabaseIsPostgresByDefault...`,
    `DatabaseTests`). The pull request smoke runs PostgreSQL and gates; no separate SQLite smoke runs on pull requests. The diagnostic minute
    reports the pool wait count and total, the share of 2 ms samples with every connection in use, and the mean occupancy of the pool (10
    connections on PostgreSQL, 1 on SQLite).

    **PostgreSQL runs natively on the runner, not as a service container.** The first runs with a `postgres:16` service container failed every
    managed install at the runner's data writer (`data writer access requires reconciliation: invalid application runtime response`), SQLite
    runs included, because the container was present on the host: the writer inspects every container for mount references and refuses one the
    runner did not start. The job therefore starts the runner image's own PostgreSQL 16 (`systemctl start postgresql`, role and database created
    fresh each run, version recorded: 16.15). The image store probe's installs also run the soak executable as their data writer before their start
    is refused, so the fixture runs no load in a container whose memory limit exceeds 1 GiB (`loadsEnabled`).

    **First PostgreSQL smoke (run 43598019520, `4d8cd626`; not evidence).** Every probe, flood, backend and host check passes (probe codes:
    `image_store` native `capacity_exhausted_image_store` with the two images' starts refused `capacity_exhausted_memory`, `ninth_backend`
    `capacity_exhausted_running_backends`, `memory_over_budget`, `cpu_over_budget`, `app_platform_invalid_request`, `app_platform_storage_full`, `installation_capacity`,
    `review_required:inflight_limit`, `task_capacity` twice; achieved fractions CPU 0.997 to 0.999, memory current 0.997, PIDs 0.875 to 0.922). All four
    latency series fail (idle / pooled loaded / bound, microseconds; windows 1 to 5):

    | Series | idle / pooled / bound | windows |
    | --- | --- | --- |
    | home | 1764 / 10055 / 6764 fail | 10320, 10039, 8589, 8967, 11885 |
    | report | 3781 / 33989 / 8781 fail | 42216, 20316, 44054, 24433, 33580 |
    | dispatch@200 | 989 / 9573 / 5989 fail | 5166, 13302, 9318, 10359, 9573 |
    | batch start | 11284 / 72222 / 16926 fail | 62217, 40275, 84492, 50795, 55303 |

    The pool is not the cause: in the diagnostic minute the pool of 10 had 0 waits, 0% of samples with every connection in use and a mean occupancy of
    2.6% idle and 11.5% loaded. The Portal's own CPU per home request is unchanged (p95 0.84 ms idle, 1.04 ms loaded) while the wall p95 goes from
    1.9 to 23 ms, so the time is spent off CPU in the round trips to PostgreSQL: the host is 80% to 85% busy for the whole load (4 vCPU shared by the
    eight backends at about 1.9 cores, the PostgreSQL server processes, the Portal, the flood process, the runner and Docker; 1% before the load).
    The CPU profile puts 58% of the loaded minute in the relay's admitted requests. Nothing was tuned in response; the 120 minute run on this shape
    would fail all four series. Whether PostgreSQL on a separate host (as the owner runs it) passes is not measured: the reference host has one.

    **Lowering the runner CPU budget (non-evidence PostgreSQL runs, owner decision 2026-11-14).** The `soak_cpu_percent` input overrides the
    runner's `capacity.cpu_percent` (default 50) in the soak runner's config; the backends are still sized from the budget the runner reports (eight
    of them, so each CPU share shrinks), and the value is recorded in the evidence. A run with an override is evidence only if it equals the default. The
    default was not changed. Five-minute runs on PostgreSQL (idle / pooled loaded / bound, microseconds; windows 1 to 5):

    | | 50% (run 43598019520) | 37% (run 43599586089) | 25% (run 43599595728) |
    | --- | --- | --- | --- |
    | runner CPU budget / each backend | 2000 / 240 millis | 1480 / 180 | 1000 / 120 |
    | host busy during the load, mean / max | 80% to 85% | 70.1% / 71.6% | 63.2% / 64.5% |
    | summed backend CPU, mean | about 1900 millis | 1440 | 960 |
    | home | 1764 / 10055 / 6764 fail | 2062 / 10518 / 7062 fail | 1746 / 9288 / 6746 fail |
    | report | 3781 / 33989 / 8781 fail | 2383 / 9077 / 7383 fail | 2972 / 17676 / 7972 fail |
    | dispatch@200 | 989 / 9573 / 5989 fail | 884 / 3206 / 5884 pass | 946 / 3882 / 5946 pass |
    | batch start | 11284 / 72222 / 16926 fail | 9172 / 20910 / 14172 fail | 11169 / 43752 / 16753 fail |

    Per-window p95 at 37%: home 9352, 12446, 9518, 10662, 10814; report 8557, 10001, 8284, 10296, 8462; dispatch 2965, 3798, 2771, 3506, 2750; batch
    start 27363, 19966, 16459, 21560, 19989. At 25%: home 11290, 8304, 8749, 8435, 10289; report 13302, 19721, 11621, 22926, 14706; dispatch 3705, 3494,
    3611, 5005, 4372; batch start 43752, 69933, 20640, 47459, 27345. Every backend still used at least 0.998 of its quota, held at least 0.996 of its
    memory and reached 0.89 to 0.94 of its PIDs, with every other check green. No level passes, so no stability rerun was made. Lowering the backends'
    share removes most of the load's growth in the dispatch series, but home and report stay at about 9 to 10 ms against bounds of about 7 ms: the host stays
    63% to 70% busy at 25% and 37%, so the Portal's own competitors (PostgreSQL, the flood process, the runner, Docker and the Portal) are still more than
    the idle host's.

    **Who uses the CPU (PostgreSQL, five-minute non-evidence runs).** The host sampler now reads `/proc/<pid>/stat` (utime plus stime) every thirty
    seconds and groups the processes: the measured Portal (`TestPlatformCapacitySoakChild`), the flood generator, PostgreSQL, the app runner
    (`TestPlatformRunnerServiceChild`), `dockerd`, `containerd` and its shims, and the eight backends, which are summed from their cgroups' `cpu.stat`
    (processes inside a backend container are skipped in the `/proc` pass). "Other" is the host's busy time less the named groups: it holds everything
    not named, including kernel work, the sampler itself, the root test process, the registry container and processes that exit between samples. The
    verdict records each group's share of all CPUs (percent of four vCPUs) for the idle period and for every loaded window, with the flood's admitted
    and refused requests per second (`cpu_breakdown`; the step summary prints it). Runs: default 50% (dispatch 43601742346; the pull request smoke
    43601735934 agrees), 37% (dispatch 43601752349). Mean over windows 2 to 5 (window 1 includes the backends' warm-up), percent of the host:

    | Period | host busy | Portal | flood | PostgreSQL | runner | docker, containerd | 8 backends | other | admitted / refused per second |
    | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
    | idle, 50% | 0.9 | 0.3 | 0.0 | 0.3 | 0.0 | 0.0 | 0.0 | 0.3 | none |
    | loaded, 50% (43601742346) | 81.6 | 6.9 | 0.5 | 0.2 | 10.9 | 4.4 | 48.0 | 10.4 | 7.5 / 113 |
    | loaded, 50% (43601735934, with diagnosis) | 85.7 | 8.1 | 0.6 | 0.2 | 12.1 | 4.7 | 48.0 | 11.9 | 6.0 / 112 |
    | idle, 37% | 0.6 | 0.2 | 0.0 | 0.2 | 0.0 | 0.0 | 0.0 | 0.2 | none |
    | loaded, 37% (43601752349) | 68.9 | 6.7 | 0.4 | 0.1 | 11.4 | 4.3 | 36.0 | 9.9 | 10.8 / 116 |

    Per-window values at 50% (43601742346, minutes 1 to 5): host busy 81.6, 81.0, 81.4, 81.9, 82.0; Portal 7.0, 6.8, 7.0, 6.9, 7.2; runner 11.0, 10.9, 11.0,
    10.8, 11.3; other 10.5, 10.2, 10.3, 11.1, 10.5. At 37% (43601752349): host busy 65.2, 68.8, 68.7, 69.3, 68.9; Portal 7.1, 6.6, 6.7, 6.8, 6.7; runner 9.5, 11.3,
    11.5, 11.6, 11.4; other 26.4, 10.2, 9.6, 10.0, 9.9. Both runs fail home, report and batch start (home 19536 us against a bound of 6718 at 50%, 17800
    against 5970 at 37%; report 47763 against 7973 and 17801 against 7050; batch start 71798 against 16674 and 38581 against 12171) and pass dispatch only at
    37%; the pull request smoke failed home (40876 against 7391).

    Would the Portal have enough headroom if the flood process and the Portal's work on refused attempts were removed? No, not from CPU. The flood generator
    uses 0.5% to 0.6% of the host. The Portal's CPU profile for the loaded minute of the pull request smoke (23.3 s of CPU in 60 s, 8.1% of the host) puts 13.64 s
    in the pre-check wrapper, of which 13.54 s is the admitted requests it passes on (`requireUserJSON`, then `apiPlatformBackend`); what the wrapper keeps
    for itself, the 429 answers, is 0.10 s, and the whole of `net/http.(*conn).serve` outside the wrapper (reading and writing every request, admitted or refused)
    is 0.51 s: at most about 0.6 s of 240 CPU-seconds, 0.25% of the host. Together that is under 1% of the host: 85.7% busy would be about 85% busy. The CPU is
    taken by the load the soak is meant to apply: the eight backends at their quota (48% at the default budget, 36% at 37%, fixed by the budget), the runner
    that carries the admitted relay requests and the backends' traffic (11% to 12%), Docker's daemons moving the backends' log flood (4% to 5%) and about 10% that
    is not any named process. The refused-attempt storm is a test artifact (a remote browser would not keep eight attempt loops running), but removing it does not
    change the contention; the admitted traffic, the backends and the runner do. At 37% the host is still 69% busy and home and report are still about 3 times
    their bounds, so the contention does not scale with the host's idle share alone: PostgreSQL's own share is 0.1% to 0.2%, so the round trips to it are waiting
    on the scheduler behind the backends' cgroup-limited threads, not on its CPU.

    **Production cgroup layout and the app containers' CPU weight (#442, `fcacdb37`; PostgreSQL, 5 minutes, default 50%, dispatch 43648480436 on `15832cd4`, not
    evidence).** The soak now starts the measured Portal (`soak-portal.scope`) and the runner (`soak-runner.scope`) each in a transient scope under `system.slice` at
    `CPUWeight=100` (`systemd-run --scope`, the Portal dropping to its uid inside the scope with `setpriv`; the job fails if `systemd-run` cannot do this, it does not fake
    the layout), so they are siblings of the backends' `docker-<id>.scope` as production's compose containers and systemd unit are. PostgreSQL is the runner image's own
    `postgresql@16-main.service` under `system-postgresql.slice`. The flood generator is started by the Portal and so shares its scope (about 0.5% of the host). The
    sampler records `/proc/<pid>/cgroup` of each group and `cpu.weight` along each path (`cgroup_layout` in the evidence). Recorded: `/system.slice` 100; Portal and
    flood `/system.slice/soak-portal.scope` 100; runner `/system.slice/soak-runner.scope` 100; PostgreSQL `/system.slice/system-postgresql.slice` 100 and its service
    unit with no `cpu.weight` file of its own; a backend `/system.slice/docker-<id>.scope` **35** (CpuShares 256). Without #442 the backend scope read 100.

    | Series (us) | idle / pooled / bound | windows 1 to 5 |
    | --- | --- | --- |
    | home | 1776 / 20384 / 6776 fail | 20696, 12235, 19126, 23221, 23997 |
    | report | 3196 / 32884 / 8196 fail | 25525, 40229, 29084, 48803, 21646 |
    | dispatch@200 | 1055 / 7123 / 6055 fail | 7438, 5348, 9687, 5460, 7610 |
    | batch start | 29868 / 64991 / 44802 fail | 58512, 72330, 32162, 72872, 33035 |

    CPU by group, mean of windows 1 to 5 (percent of the host): host busy 83.6, Portal 7.3, flood 0.5, PostgreSQL 0.3, runner 11.6, docker 4.8, backends 48.0, other 11.2;
    admitted 7.7 and refused 114 requests per second. Every other check passes (backends at 0.998 of their quota). The weight changed nothing visible: the host is 83% busy, not
    saturated, so the run queue is not where the Portal's time goes and a weight that only arbitrates a contended CPU has little to arbitrate. The latency is as before, and
    this run's idle batch start (29868 us) is itself much slower than earlier runs' (5000 to 11000), which moves its bound; no tuning was done.

    **Where the Portal's off-CPU time goes: pressure, disks and PostgreSQL (dispatch 43649765859 on `26af2c2c`, PostgreSQL, default 50%, production-mirroring layout; not
    evidence).** The host sampler now reads, every thirty seconds, `/proc/pressure/{cpu,io,memory}` (some and full, avg10 and the total's delta as the stalled share of the
    interval) and the same three files under the Portal scope, the PostgreSQL unit and one backend scope; `/proc/diskstats` for the device of PostgreSQL's data directory
    and the XFS loop device (utilisation from I/O ticks, await, IOPS, write rate); `procs_running`, `procs_blocked`, I/O wait, dirty and writeback pages; and PostgreSQL's
    commit rate, WAL syncs per second and mean WAL sync time (`pg_stat_wal`, with `track_wal_io_timing` on in the soak's own PostgreSQL). Series, pooled loaded against
    bound (us): home 2466 idle / 17735 / 7466, report 4475 / 27011 / 9475, dispatch@200 1559 / 10083 / 6559, batch start 15159 / 55567 / 22738; all four fail.

    Layout: PostgreSQL's data directory `/var/lib/postgresql/16/main`, the XFS loop volume's backing file and Docker's root all live on `sda1` (8:1), one disk; the XFS
    mount is `loop0` (7:0) on that backing file, so the backends' storage flood and PostgreSQL write to the same device.

    | Signal | idle mean / max | loaded mean / max |
    | --- | --- | --- |
    | host CPU pressure, some avg10 (percent of time) | 5.6 / 33.2 | **90.6 / 91.5** |
    | host I/O pressure, some avg10 | 0.09 / 0.5 | 0.18 / 0.9 |
    | host memory pressure, some avg10 | 0 / 0 | 0 / 0 |
    | Portal scope CPU pressure, some / full avg10 | 0 / 0 | **12.8 / 9.7** (max 13.2 / 10.2) |
    | Portal scope I/O and memory pressure | 0 | 0 |
    | PostgreSQL unit CPU pressure, some / full | 0 / 0 | 6.6 / 6.1 |
    | PostgreSQL unit I/O pressure | 0.03 | 0 |
    | one backend scope CPU pressure, some | none | 24.6 (its quota's throttling) |
    | backend scope I/O pressure | none | 0.005 |
    | `sda1` utilisation / await (ms) | 0.3 / 0.83 | 1.2 / 0.31 (max 2.9 / 0.65) |
    | `sda1` IOPS / write KiB/s | 13 / 859 | 61 / 19238 (max 170 / 67275) |
    | `loop0` utilisation / await (ms) | 0 / 0.22 | 1.5 / 1.37 (max 5.2 / 2.8) |
    | `procs_running` | 1 / 1 | 10.8 / 17 |
    | I/O wait (percent of CPU time) | 0.06 / 0.26 | 0.13 / 0.68 |
    | dirty pages (KiB) | 17033 / 94004 | 13814 / 22248 |
    | PostgreSQL commits / WAL syncs per second | 97 / 7.1 | 291 / 36.5 |
    | PostgreSQL mean WAL sync time (ms) | 0.18 / 0.22 | 0.34 / 0.39 |

    CPU pressure dominates, I/O and memory do not. During the load the host has at least one runnable task waiting for a CPU 91% of the time, with 11 tasks running on 4 vCPUs on
    average (17 at most), and the Portal scope itself waits for CPU 12.8% of the time and has nothing running 9.7% of the time, against 0 idle; its I/O and memory pressure stay at
    0. The disk is nowhere near busy: 1% to 3% utilised, 0.3 ms await, no writeback backlog (dirty pages are lower loaded than idle, where the setup's writes sit). The XFS volume's
    storage flood writes 18 MiB/s on average (66 MiB/s peak) through `loop0` onto `sda1` without saturating it. PostgreSQL's commit latency does not rise materially: its mean WAL sync
    goes from 0.18 to 0.34 ms at three times the commits, and its own CPU pressure (6.6%) is larger than its I/O pressure (0). Disk contention is not the candidate: the Portal's
    off-CPU time is run-queue waiting. That is not a CPU-share problem the weight could fix, since the Portal scope is at weight 100 against the backends' 35 and still waits: with
    quota-limited containers, 24 to 56 threads each, being released at the same moment of each scheduling period, the Portal's wake-ups queue behind a burst of runnable threads,
    which a weight does not shorten (this explanation is an inference from the pressure figures, not tested).

    **Reserved CPU 0 (#443, `323d273f`; PostgreSQL, 5 minutes, default 50%, dispatch 43654065624 on `6bab151d`; not evidence).** The runner's ready line printed
    `reserved_cpus=0 app_cpus=1-3`; the budget stayed 2000 millis (`min(2000, 3000)`), eight backends of 240 millis. Every backend container's `cpuset.cpus.effective` is `1-3`
    (all eight recorded in `backend_cpusets`). Per-CPU busy from `/proc/stat` (percent, mean over the period, max in brackets):

    | | CPU 0 | CPU 1 | CPU 2 | CPU 3 |
    | --- | --- | --- | --- | --- |
    | idle | 0.7 (1.3) | 0.7 (1.1) | 0.6 (1.0) | 0.7 (0.9) |
    | loaded | 67.8 (69.9) | 83.5 (85.1) | 85.1 (86.6) | 83.0 (84.7) |

    CPU 0 stays free of backend load, and carries the Portal, PostgreSQL, the runner, Docker and the flood: 68% busy, against 83% to 85% for the three app CPUs. CPU by group (mean of
    windows 2 to 5, percent of the host): host busy 80.5, Portal 6.8, flood 0.4, PostgreSQL 0.3, runner 11.9, docker 4.3, backends 48.0, other 8.8; admitted 10 and refused 116 per second.

    Series, pooled loaded against bound (us; idle / pooled / bound; windows 1 to 5), with the unreserved layout of the previous run (43649765859) for comparison:

    | Series | now | windows | before |
    | --- | --- | --- | --- |
    | home | 2083 / 8470 / 7083 fail | 8179, 7941, 6775, 10506, 8194 | 2466 / 17735 / 7466 fail |
    | report | 2242 / 10630 / 7242 fail | 9859, 10957, 8709, 11189, 9006 | 4475 / 27011 / 9475 fail |
    | dispatch@200 | 856 / 3360 / 5856 pass | 3422, 2612, 4582, 3360, 3146 | 1559 / 10083 / 6559 fail |
    | batch start | 8756 / 30176 / 13756 fail | 49282, 30176, 23983, 19270, 29653 | 15159 / 55567 / 22738 fail |

    Pressure stalls (idle / loaded mean): Portal scope CPU some avg10 0 / 9.9, full 0 / 6.5, I/O and memory 0 / 0; host CPU some 0.01 / 86.8; PostgreSQL unit CPU some 0 / 5.4; a backend
    scope CPU some none / 34.3 (the quota); processes running 1 / 8.8 (13 at most); PostgreSQL mean WAL sync 0.06 / 0.13 ms. The Portal still waits for CPU about 10% of the time, down
    from 12.8%. Home and report fall to about 1.2 to 1.5 times their bounds (from 2.4 to 3.5 times) and dispatch passes; home, report and batch start still fail. Nothing was tuned.

    **Two reserved CPUs (`soak_reserved_cpus=2`; PostgreSQL, 5 minutes, default 50% budget; dispatch 43655116984 on `ddaf14fa`; not evidence).** The new input overrides the runner's
    `capacity.reserved_cpus` (default 1 on four or more CPUs), is recorded in the evidence and makes a run non-evidence unless it equals the default. The ready line printed
    `reserved_cpus=0-1 app_cpus=2-3`; the budget stayed 2000 millis, eight backends of 240 millis; every backend's `cpuset.cpus.effective` is `2-3`. Per-CPU busy (percent, mean, max):
    idle 0.7 to 0.8 on every CPU; loaded CPU 0 57.0 (57.9), CPU 1 57.9 (60.5), CPU 2 98.2 (99.0), CPU 3 98.2 (98.8): the two app CPUs are saturated by the eight backends, and the two
    reserved CPUs are 57% busy. CPU by group (mean of windows 2 to 5, percent of the host): host busy 78.8, Portal 6.5, flood 0.4, PostgreSQL 0.2, runner 11.5, docker 4.3, backends 47.8,
    other 8.0; admitted 9.2 and refused 116 per second. Portal scope pressure (idle / loaded): CPU some avg10 0 / 12.1, full 0 / 6.7, I/O 0 / 0; host CPU some 0.01 / 76.7; PostgreSQL unit CPU
    some 0 / 8.0; backend scope CPU some none / 44.8; processes running 1 / 11.9; PostgreSQL mean WAL sync 0.07 / 0.12 ms.

    | Series | idle / pooled / bound (us) | windows 1 to 5 | one reserved CPU (43654065624) |
    | --- | --- | --- | --- |
    | home | 2063 / 11512 / 7063 fail | 8588, 11063, 12388, 13536, 11311 | 2083 / 8470 / 7083 fail |
    | report | 2335 / 13552 / 7335 fail | 13643, 13004, 13608, 10541, 14744 | 2242 / 10630 / 7242 fail |
    | dispatch@200 | 796 / 4192 / 5796 pass | 4153, 3066, 4801, 5160, 4192 | 856 / 3360 / 5856 pass |
    | batch start | 9229 / 35350 / 14229 fail | 35873, 19584, 26002, 22704, 35350 | 8756 / 30176 / 13756 fail |

    Two reserved CPUs are no better than one: home, report and batch start fail by more than with `reserved_cpus=1` (home 11.5 ms against 8.5 ms), and the Portal scope waits for CPU as much
    (12.1% against 9.9%) even though the reserved CPUs are only 57% busy. So the remaining wait is not a lack of free CPU for the Portal: the three app CPUs of `reserved_cpus=1` (83% to 85%)
    treat the Portal better than the two saturated ones, and the Portal's wake-ups appear to be disturbed by the neighbours it shares the host with rather than by their share. No stability
    rerun was made: nothing passed. Nothing was tuned.

    **Pinning the platform to the reserved CPUs (`soak_pin_platform`; hypothesis: the Portal's threads wake on the saturated app CPUs and queue behind the backends; PostgreSQL, 5
    minutes, default 50% budget; not evidence).** The input starts the Portal's scope with `systemd-run -p AllowedCPUs=<reserved set>` and sets PostgreSQL's unit to the same set
    with `systemctl set-property --runtime` before the soak starts; each process's affinity is recorded from `/proc/<pid>/status` (`Cpus_allowed_list`) in the evidence. The runner
    could not be pinned: pinned to CPU 0 it refuses to start, `app-runner not ready: capacity: online_cpus_disagree_with_reported` (it compares the CPUs it can use with those Docker
    reports), so it stays on 0-3 in both runs, as does the flood's parent test process. The flood shares the Portal's scope and is pinned with it.

    | | (a) `reserved_cpus=1` pinned to CPU 0, run 43656621825 | (b) `reserved_cpus=2` pinned to 0-1, run 43657628078 |
    | --- | --- | --- |
    | recorded affinity: Portal, flood, PostgreSQL / runner | 0, 0, 0 / 0-3 | 0-1, 0-1, 0-1 / 0-3 |
    | backend cpuset | 1-3 | 2-3 |
    | per-CPU busy loaded, CPU 0 / 1 / 2 / 3 (idle about 0.2 to 1.7) | 65.6 / 78.3 / 79.7 / 79.3 | 59.1 / 60.2 / 97.7 / 97.9 |
    | host busy; Portal, flood, PostgreSQL, runner, docker, backends, other (percent of host, windows 2 to 5) | 77.9; 6.3, 0.3, 0.1, 11.0, 4.1, 48.0, 8.1 | 79.8; 6.7, 0.4, 0.2, 11.9, 4.3, 47.8, 8.3 |
    | admitted / refused per second | 10.3 / 115.7 | 9.5 / 115.8 |
    | Portal scope CPU pressure some / full avg10 (idle 0) | 28.6 / 11.8 | 13.6 / 7.6 |
    | Portal scope I/O pressure | 0.001 | 0 |
    | PostgreSQL unit CPU some | 12.2 | 8.8 |
    | PostgreSQL mean WAL sync (ms, idle 0.06 / 0.07) | 0.84 (max 2.95) | 0.12 |
    | home idle / pooled / bound (us) | 1212 / 13309 / 6212 fail | 2342 / 9782 / 7342 fail |
    | home windows 1 to 5 | 11341, 12649, 13473, 15873, 11761 | 9471, 8513, 10012, 12323, 9782 |
    | report | 2085 / 20877 / 7085 fail | 2835 / 12631 / 7835 fail |
    | report windows | 18945, 26322, 14815, 24735, 14700 | 12538, 9430, 14487, 15019, 11395 |
    | dispatch@200 | 757 / 5206 / 5757 pass | 955 / 4639 / 5955 pass |
    | dispatch windows | 5206, 4903, 4309, 5246, 5023 | 3544, 4158, 5046, 3999, 4908 |
    | batch start | 13168 / 35169 / 19752 fail | 9781 / 28252 / 14781 fail |
    | batch start windows | 31807, 45392, 31577, 38777, 23830 | 21077, 24690, 23666, 35636, 25604 |

    The hypothesis is not supported. Pinned to CPU 0 (a) the Portal waits 28.6% of the time, more than unpinned (9.9% with one reserved CPU, 43654065624), and PostgreSQL's WAL sync mean rises
    to 0.84 ms: the Portal, PostgreSQL, the runner, Docker and the rest of the host's own work all compete for one CPU (65.6% busy on average). Pinned to CPUs 0-1 (b) the Portal waits 13.6%
    on CPUs that are 59% to 60% busy and cannot wake on the saturated CPUs 2-3 at all, yet waits as much as unpinned with two reserved CPUs (12.1%). So the wait is not caused by wake-ups landing
    behind the backends. Home and report stay at 1.3 to 3 times their bounds in both runs, dispatch passes in both and batch start fails. No stability rerun: nothing passed. Nothing was tuned.

    **Four hypotheses about the reserved CPUs, tested (PostgreSQL, 5-minute dispatch runs, none evidence; run IDs 43659458611, 43669177366, 43659473079, 43669219337).**

    *H4, SMT and kernel.* The runner is a 4-vCPU Intel Xeon Platinum 8573C VM on kernel `7.0.0-1017-azure` (EEVDF, which is the scheduler from kernel 6.6). `lscpu` reports 2 threads per core, 2 cores, one socket,
    and `thread_siblings_list` is `cpu0: 0-1, cpu1: 0-1, cpu2: 2-3, cpu3: 2-3`. So the four vCPUs are two physical cores, and with `reserved_cpus=1` CPU 0's sibling (CPU 1) is an app CPU spinning a
    backend. With `reserved_cpus=2` the reserved CPUs 0-1 are one whole core and the apps hold the other, so nothing shares a core with a backend, yet the Portal still waits 12% to 13% (below). The
    sibling sharing exists but does not explain the wait. The evidence records `topology` (kernel, `eevdf`, `smt`, `thread_siblings`, `lscpu`).

    *H2, the relay is expensive: the runner's profile.* A runner-only CPU profile of the loaded diagnostic minute (new `soak_runner_profile` input; the Portal child marks the minute, the parent signals the runner to
    start and write a pprof profile, and the flood's counters are read at both marks): 26.08 CPU-seconds in 60.06 s (43% of one CPU, 10.9% of the host), against **324 relayed (answered) requests, 5.4 a second** (6788
    refused, 113 a second; the runner does nothing for a refused one). That is **80 ms of runner CPU per relayed request** (the whole process; 62 ms of it inside the two request handlers). No single function
    dominates: the cost is spread across verification work repeated on every request, and it is the same work twice, because each relayed request is a handshake and a request, each about 31 ms:

    | cumulative | share | function (file:line) |
    | --- | --- | --- |
    | 10.06 s | 38.6% | `transport.(*Server).handleWritableBackendHandshake` (`transport/writable_backend_handshake_linux.go:159`) |
    | 9.91 s | 38.0% | `transport.(*Server).handleWritableBackendRequest` (`transport/writable_backend_request_linux.go:188`) |
    | 4.34 s | 16.6% | `driver.validProjection` (`driver/http.go:162`): strict validation of the Docker inspect document |
    | 3.94 s | 15.1% | `driver.inspectCandidateContainerHTTP` (`driver/container_observe.go:49`): a Docker inspect round trip, parsed with `encoding/json` |
    | 3.39 s | 13.0% | `journal.(*Store).beginWritableRegistration` (`journal/writable_backend_registration_delivery.go:64`), with `loadWritableRegistrationSource` 3.25 s (`journal/writable_backend_registration_intent.go:53`): SQLite reads of the journal |
    | 3.17 s | 12.2% | `driver.managedCandidateReference` (`driver/writable_candidate_recovery_linux.go:27`) |
    | 2.91 s | 11.2% | `journal.(*Store).prepareWritableBackendExchange` (`journal/writable_backend_handshake.go:46`) |
    | 2.06 s | 7.9% | `driver.(*candidateMountPins).validate` (`driver/mount_pins_linux.go:169`) |
    | 2.01 s | 7.7% | `driver.readCandidateIPCInventory` (`driver/candidate_ipc_linux.go:271`) |
    | 1.86 s | 7.1% | `path/filepath.EvalSymlinks`, 1.69 s of it under `driver.resolveCandidateIPCSource` (`driver/candidate_ipc_linux.go:483`) |
    | 5.63 s | 21.6% | `encoding/json` decoding in all of the above; `Syscall6` alone is 10.2% flat |

    The largest single item is validating a Docker inspect of the backend's container on every request (`validProjection` and `inspectCandidateContainerHTTP`, about 16% each, overlapping), then the journal's
    registration reads and the re-resolution of the container's IPC directory and mounts. Not fixed here. The prediction of about 48 ms per request is low: measured 62 to 80 ms.

    *Docker and "other".* By command name (percent of the host, loaded): `dockerd` 4.1 to 4.3, `containerd` 0.35 to 0.39, `containerd-shim` 0.28 to 0.41. The processes inside "other" that the sampler can see add up to about 1%:
    `provjobd` 0.28 to 0.40, the sampler's own Python 0.07 to 0.11, `Runner.Worker`, `rcu_sched`, several `kworker` kinds (events, unbound, writeback, flush, loop0 at 0.02) and `ksoftirqd` each under 0.1;
    `loop0`, `jbd2` and `xfs` threads do not appear. The other 8% to 12% of the host that the breakdown labels "other" is therefore CPU time no sampled process owns: the host's modes in the last run were user 23.2,
    nice 48.6 (the backends' burners), system 12.9, iowait 0.5, **steal 0.0**, softirq 0.8, irq 0.0, idle 14.1. It is not hypervisor steal and not interrupt work; the likely owner is short-lived processes that exit between
    30-second samples (the sampler's own `docker` and `psql` invocations, `systemd-run`), whose time `/proc/<pid>/stat` loses. Not verified.

    *H1 and H3, priority instead of placement: `soak_platform_cpu_weight=1000`* on the Portal scope and PostgreSQL's unit (the runner's scope stays at 100), no pinning:

    | | (a) `reserved_cpus=1`, weight 1000 (43659473079) | (b) `reserved_cpus=2`, weight 1000 (43669219337) | unweighted, 1 reserved (43654065624) / 2 reserved (43655116984) |
    | --- | --- | --- | --- |
    | per-CPU busy loaded, CPU 0 / 1 / 2 / 3 | 65.2 / 87.9 / 86.6 / 86.4 | 69.5 / 71.1 / 99.8 / 99.8 | 67.8 / 83.5 / 85.1 / 83.0; 57.0 / 57.9 / 98.2 / 98.2 |
    | host busy; Portal, flood, PostgreSQL, runner, docker, backends, other (percent of host) | 81.7; 7.1, 0.5, 0.3, 11.5, 4.4, 48.0, 10.4 | 85.4; 8.1, 0.6, 0.3, 11.9, 5.4, 46.7, 12.4 | 80.5; 6.8, 0.4, 0.3, 11.9, 4.3, 48.0, 8.8 |
    | Portal scope CPU pressure some / full avg10 | 9.9 / 5.7 | 12.7 / 6.9 | 9.9 / 6.5; 12.1 / 6.7 |
    | PostgreSQL unit CPU some | 9.6 | 8.5 | 5.4; 8.0 |
    | home idle / pooled / bound (us) | 1743 / 13021 / 6743 fail | 1931 / 12259 / 6931 fail | 2083 / 8470 / 7083 fail; 2063 / 11512 / 7063 fail |
    | home windows | 14626, 11550, 9672, 13531, 12613 | 11574, 12279, 12621, 11038, 13030 | |
    | report | 11797 / 30893 / 17695 fail | 3602 / 22017 / 8602 fail | 2242 / 10630 / 7242 fail; 2335 / 13552 / 7335 fail |
    | report windows | 30937, 54033, 12531, 28414, 22716 | 16882, 29538, 15701, 32218, 15949 | |
    | dispatch@200 | 1132 / 5164 / 6132 pass | 1048 / 5988 / 6048 pass | 856 / 3360 / 5856 pass; 796 / 4192 / 5796 pass |
    | dispatch windows | 6036, 4600, 4251, 5164, 4548 | 4641, 6612, 5245, 5937, 5717 | |
    | batch start | 21846 / 79605 / 32769 fail | 11972 / 51050 / 17958 fail | 8756 / 30176 / 13756 fail; 9229 / 35350 / 14229 fail |
    | batch start windows | 79605, 77366, 40816, 64668, 76042 | 44185, 53071, 25984, 52511, 28607 | |

    **Against the predictions.** H1 and H3 (the Portal queues behind the platform's own threads, a wake-up delay per round trip, fixed by priority): not supported. With a tenfold weight the Portal scope's CPU pressure stays at 9.9%
    and 12.7%, where the prediction was well below 13%, and home stays at 12 to 13 ms (bound about 7 ms), worse than the unweighted runs; report and batch start are worse too (the batch start's wait did not improve less than the pages, it got
    worse with both). Weights arbitrate between sibling cgroups that contend, and the Portal scope waits as much with 1000 as with 100. H4 (SMT): the premise holds, since CPU 0 shares a core with CPU 1, but `reserved_cpus=2` removes the sharing and the wait stays. H2 (the relay is
    expensive): supported, and understated: 80 ms of runner CPU per relayed request, double work per request (handshake plus request), roughly 12% of the host for 5 relayed requests a second, plus `dockerd` at 4% to 5% moving the logs; the CPUs the platform keeps
    are 57% to 70% busy mostly with this. No run passed, so no stability rerun. Nothing was tuned.

74. **The latency criterion becomes absolute under full load (owner decision 2026-11-15).** After #445 (`5defa0ab`) cut the runner's CPU per relayed request, the 5-minute PostgreSQL smoke on the default
    configuration (production-mirroring layout, 50% budget, `reserved_cpus` 1, no pinning, no weight; run 43675027780 on `076e9ce8`) still missed the relative gate on three series:

    | Series (us) | idle / pooled / relative bound | windows 1 to 5 |
    | --- | --- | --- |
    | home | 1744 / 13650 / 6744 | 11426, 12394, 15277, 11934, 16007 |
    | report | 3283 / 27905 / 8283 | 31093, 11778, 30181, 14873, 30088 |
    | dispatch@200 | 1028 / 5723 / 6028 (pass) | 5509, 4394, 6362, 5306, 6755 |
    | batch start | 35096 / 84846 / 52644 | 74775, 30637, 112257, 32767, 94908 |

    Runner CPU per relayed request fell from 80 ms (26.08 CPU-s for 324 relayed requests, run 43669177366) to **35.5 ms (15.53 CPU-s for 437 relayed requests in the 60 s profile window, 7.3 a second)**; the runner's share of
    the host went from about 11.5% to 7.7%. Its remaining cost is dominated by the journal's SQLite reads on both the handshake and the request (`beginWritableRegistration`, `journal/writable_backend_registration_delivery.go:64`,
    21.6%; `prepareWritableBackendExchange`, `journal/writable_backend_handshake.go:46`, 18.7%). Per-process breakdown (percent of the host, windows 2 to 5): host busy 82.3, Portal 7.9, flood 0.5, PostgreSQL 0.2, runner 7.7,
    docker 5.1, backends 48.0, other 12.3; admitted 8.7 and refused 115 per second; per-CPU busy 68.4 / 86.7 / 86.6 / 86.1; Portal scope CPU pressure some 13.4, full 8.7. The relative gate is therefore still missed, and the
    owner's decision applies: home and report pooled p95 at most 50 ms, batch start at most 100 ms (and, the same day, dispatch@200 at most 25 ms, below). The verdict keeps the old relative threshold, the ratio to idle and every window as diagnostics
    (`bound_kind`, `relative_threshold_us`, `within_relative_threshold`, `pooled_ratio`); the constants are `HOME_P95_BOUND_US`, `REPORT_P95_BOUND_US` and `BATCH_START_P95_BOUND_US`. The checks are named
    `home_p95_within_absolute_bound`, `report_p95_within_absolute_bound` and `batch_start_p95_within_absolute_bound`.

    **Smokes on the absolute bounds (`f27cd4ca`, PostgreSQL, default configuration; the pull request smoke 43676220497 with the diagnosis, dispatch runs 43676229780 and 43677201531).** Pooled p95 (us), windows 1 to 5, with the
    bound:

    | Series | 43676220497 | 43676229780 | 43677201531 | bound |
    | --- | --- | --- | --- | --- |
    | home | 21618 (23742, 21218, 20981, 21915, 21073) pass | 25929 (25554, 21146, 27521, 24557, 29876) pass | 33111 (34804, 37428, 26309, 34037, 25242) pass | 50000 |
    | report | 19047 (19793, 18801, 17863, 16704, 24148) pass | 31871 (29645, 37288, 24279, 39818, 28882) pass | 39202 (30924, 45519, 41625, 39202, 32269) pass | 50000 |
    | batch start | 30403 (30873, 26367, 30403, 27403, 21101) pass | 58929 (64889, 42165, 44948, 58929, 44681) pass | 59386 (67424, 54256, 47686, 57937, 46383) pass | 100000 |
    | dispatch@200 (idle / pooled / relative bound) | 870 / 6459 / 5870 **fail** (5739, 7623, 7045, 4429, 6459) | 1287 / 8527 / 6287 **fail** (8452, 7024, 8255, 9855, 8280) | 1495 / 11414 / 6495 **fail** (11275, 10166, 10725, 11752, 12129) | idle + 5 ms |

    Home, report and batch start pass their absolute bounds in all three runs, with margins of 1.5 to 2.6 times on the pages and 1.7 to 3.3 times on the batch start, but the pooled p95 grew from run to run (home 21.6, 25.9, 33.1 ms) while the host got busier
    (80%, 80%, 87% busy; the Portal scope's CPU pressure 11.6% and 16.1% in the last). **Dispatch@200 fails its relative gate in all three**, by 0.6, 2.2 and 4.9 ms, after passing in the earlier runs (3.4 to 5.7 ms against bounds of 5.6 to 6.1 ms): its gate leaves
    idle + 5 ms and the pooled p95 of the decision is now 6.5 to 11.4 ms. Every other check passes. Not tuned.

    **Dispatch@200 follows (owner decision 2026-11-15).** The three smokes above failed dispatch@200's relative gate by 0.6 to 4.9 ms (pooled p95 6.5, 8.5 and 11.4 ms), after passing in earlier runs, so the owner judged it on an absolute pooled
    p95 of at most 25 ms at full load as well. The constant is `DISPATCH_P95_BOUND_US = 25000` and the check `dispatch_p95_within_absolute_bound`; its relative threshold and ratio to idle stay recorded as diagnostics, and the
    50-job shallow measurement (`dispatch_shallow`) stays recorded and is not judged.

    **Smokes on all four absolute bounds (`885750fd`, PostgreSQL, default configuration, 5 minutes; none is evidence).** Three runs are green: dispatch runs 43678298699 and 43679294042 and the pull request smoke 43678283415 (its second attempt, with the
    diagnosis on). Pooled p95 (us) and windows 1 to 5:

    | Series (bound) | 43678298699 | 43679294042 | 43678283415, attempt 2 |
    | --- | --- | --- | --- |
    | home (50000) | 37281 (32911, 40304, 34004, 41086, 29437) | 20141 (15696, 20256, 19473, 20494, 21284) | 9634 (8053, 11974, 9171, 9753, 9264) |
    | report (50000) | 29737 (28522, 31530, 28877, 31259, 27088) | 24568 (18258, 29615, 21598, 26509, 25178) | 9765 (10414, 9538, 9068, 8832, 10791) |
    | dispatch@200 (25000) | 10275 (10791, 10055, 9667, 12136, 7609) | 6280 (6266, 6475, 7832, 5017, 5742) | 2815 (2575, 2624, 2815, 3904, 2744) |
    | batch start (100000) | 55722 (54920, 65878, 40193, 57725, 43398) | 51942 (51942, 34433, 40040, 61172, 56056) | 24146 (34402, 26158, 22169, 20259, 16928) |

    The first attempt of the pull request smoke failed on report (113.8 ms, windows 61874, 131780, 107772, 114556, 108778) and batch start (114.4 ms, 72797, 29467, 186577, 136527, 100177). That host's disk was the cause, not the Portal: in the
    pressure and disk readings of that run PostgreSQL's data device was 20.7% utilised on average (46.9% at most) with a mean await of 7.0 ms, its mean WAL sync took 4.3 ms (0.1 to 0.3 ms in the green runs; 1.1 ms in 43679294042), and the host's I/O pressure
    averaged 6.9%, while the CPU figures were those of the other runs (Portal CPU pressure 12.4%, host 79% to 80% busy). A noisy shared disk can therefore fail the gate on the report page and the batch start, which write or wait on commits; the
    absolute bounds leave a factor of 1.3 to 5 over the green runs, not over a disk this slow.

    Measurement history of the relative gate (pooled p95 against it, five-minute runs on PostgreSQL unless noted; none evidence): SQLite file-backed, 2 to 3 of the four series failing in every run (resolution 73); PostgreSQL on the
    first run, all four failing (home 10.1 ms against 6.8, report 34.0 against 8.8, batch start 72.2 against 16.9); at 37% and 25% of the CPU budget, home and report still about 9 to 18 ms against 6 to 8; with the app containers'
    CPU weight (#442) 17.7 ms and 27.0 ms; with CPU 0 reserved (#443) 8.5 ms and 10.6 ms (dispatch passing for the first time); two reserved CPUs, platform pinning, platform weights 1000: no better (12 to 13 ms); with the cheaper
    relay (#445) 13.7 ms and 27.9 ms in a run with the diagnosis on. The host is two physical cores (four SMT threads): the apps burn their 2 CPUs of quota continuously and the platform shares what is left, so a relative floor of
    a few milliseconds over an idle host's 1 to 3 ms cannot hold under full declared load.

    **Known limit of the SQLite Portal.** On a SQLite Portal, sustained app-backend relay traffic raises page and scheduler latency beyond the
    gate. The cause is the single connection: the relay's admission cap keeps two admitted requests in flight at all times, and they hold the
    one connection for about a third of the time (connection occupied 26.5% to 33.5% of 2 ms samples in the diagnostic minute against 5.6% to
    13.8% idle, 6111 to 31948 pool waits a minute). Measured this round, five-minute pull request smokes and dispatch runs, none evidence
    (idle / pooled loaded / bound, microseconds):

    | Run | report page | batch start |
    | --- | --- | --- |
    | 43568339184 (`3e3d591`) | 3523 / 50611 / 8523 fail | 7852 / 112261 / 12852 fail |
    | 43570051027 (`edfcb92`) | 1795 / 8102 / 6795 fail | 5532 / 16334 / 10532 fail |
    | 43570060321 (`edfcb92`, no batch probes) | 1863 / 12681 / 6863 fail | not run |
    | 43571196806 (`56e6881a`) | 1770 / 5552 / 6770 pass | 5758 / 26467 / 10758 fail |
    | 43590645282 (`85376438`, with #440) | 3147 / 14924 / 8147 fail | 9598 / 48249 / 14598 fail |

    The in-memory store (run 43568354287, `memory-db`) passed the report page and failed the batch start (4174 / 16911 / 9174). #440 removed
    the database work of a refused attempt (pool waits down 81%) but the admitted requests remain. A SQLite read pool is a tracked follow-up, and
    until it lands no claim is made that the SQLite envelope is proven.

75. **Round 2 of the review of PR #433 (before the 120-minute run).**

    - **Evidence is only the all-default PostgreSQL dispatch run.** `counts_as_ops08_evidence` is empty-blockers: the run passed, measured at least 120 minutes on
      `postgres`, was a `workflow_dispatch`, and had every diagnostic input at its default (`soak_db`, `soak_cpu_percent`, `soak_reserved_cpus`, `soak_pin_platform`,
      `soak_platform_cpu_weight`, `soak_runner_profile`, `soak_variant`, and the diagnosis switch). Each blocker is listed in the verdict (`evidence_blockers`). A unit test reads the
      workflow's own inputs and fails for any that is not classified in `EVIDENCE_NEUTRAL_INPUTS` or `DIAGNOSTIC_INPUTS`, so a new input cannot silently count.
    - **The reviewed commit is stated and compared.** A new `expected_commit` input (a full SHA) is compared with `GITHUB_SHA`; when given and different, the run is not evidence. The verdict records `commit`,
      `expected_commit` and `commit_matches_expected` (null when none was named: the dispatcher is expected to name it, and the run records that it did not).
    - **The Portal description is built from the real driver and version** (`soakPortalOf`), so a PostgreSQL run no longer says SQLite and no longer lists PostgreSQL as not exercised.
    - **SEC-09 stays partial after OPS-08 passes**: the task worker and event delivery coordinator are not exercised by the soak. The checklist, the evidence template, doc 06 and the workflow say so; doc 167's
      original sentence is unchanged and carries a dated engineering note.
    - **Memory and PIDs are judged per window.** The memory a backend holds is its own `held_bytes` (page cache is not held memory), at least 85% of its limit in every window, not only the last; the cgroup's
      `memory.current` stays recorded as a diagnostic (`memory_current_min`, `memory_current_peak`). PIDs are judged per window at least 0.85 of the limit (`backends_held_their_pid_limit_in_every_window`; the run peak is a diagnostic).
      The CPU check is named `backends_used_their_cpu_quota_in_every_window`.
    - **The runner's journal is sampled** every 30 seconds (database, write-ahead log and shared-memory files of its journal directory) and may grow by at most 256 MiB over the load
      (`runner_journal_growth_within_bound`; no reading is a failure).
    - **Disk-health precheck; the result `inconclusive_slow_disk`.** Two readings taken before the load decide it: the idle baseline's mean WAL sync time (`pg_stat_wal`, weighted by syncs; bound `DISK_PRECHECK_WAL_SYNC_MS` = 2 ms) and a
      new fsync probe run before anything else (200 writes of 4 KiB, each with `fdatasync`, on the PostgreSQL data device; median bound `DISK_PRECHECK_PROBE_P50_MS` = 1 ms, p95 and maximum recorded). If the probe trips, the run ends before the
      load; if the idle WAL mean trips, the verdict is replaced after the run. The result is then neither passed nor failed, never evidence, the job exits 3 with an error message, and it is simply rerun. A disk that degrades **during** the load is not
      looked at: that stays a failure of the run.

      **Verification against run 43678283415 attempt 1 (the slow-disk host).** The idle baseline of that attempt measured a sync-weighted mean WAL sync of **1.31 ms** (simple mean of the intervals 1.54 ms, maximum 2.99 ms), under the
      2 ms bound, so **the WAL signal alone would not have flagged it**; the 4.3 ms quoted for that host is the loaded mean (4.17 ms weighted). The other idle readings do not separate it either: the idle disk await averaged 10.4 ms there and 9.9 ms in the
      green run 43679294042. Idle WAL sync means of the green runs: 0.06 (43678283415 attempt 2), 0.16 (43678298699) and 0.70 ms (43679294042). The signal that decides is therefore the fsync probe, whose 1 ms median bound sits between those greens and the 1.3 ms
      of that host; it was not measured in any earlier run, so its bound is a reading of those idle values and is to be checked against the probe readings the smokes now record (`disk_probe` in the evidence).
    - **Known risk for the 120-minute run: variance between hosts.** Two runs of the evidence configuration on one commit (`885750fd`) measured home at 20.1 ms (43679294042) and 37.3 ms (43678298699) against the 50 ms bound, and batch start at 51.9 and 55.7 ms against 100 ms. A
      2-hour pooled p95 averages over more of that variance than a 5-minute one but is not immune to a slow host; a failure is judged as it stands, and the evidence records the precheck readings beside it for the reviewer (`precheck`, `disk_probe`).
    - **Smoke on the hardened harness (`c47e6928`, pull request smoke 43681321227, PostgreSQL, default configuration, 5 minutes; not evidence: five minutes, a pull request, the diagnosis on).** Green on the first attempt. Pooled p95: home 17272 us (bound
      50000), report 29677 us (50000), dispatch@200 5356 us (25000), batch start 50992 us (100000). Precheck not tripped: fsync probe median 0.261 ms, p95 0.396 ms, maximum 19.8 ms (200 writes of 4 KiB on `/var/lib/postgresql/16`); idle WAL sync mean
      0.663 ms. PIDs held at least 0.875 of the limit in every window; the runner journal grew 0 bytes over the load (database 110 KiB, no write-ahead log pages outstanding).

### Findings of the revised harness: the latency bound on a production-shaped Portal (not evidence)

Everything in the harness passes in the smokes of this round except two latency series, and that is a finding about the Portal, not the
harness. Runs, all five minutes, none is evidence:

| Run | Portal | home idle / pooled loaded / bound (us) | report | dispatch | batch start |
| --- | --- | --- | --- | --- | --- |
| smoke 43570051027 (`edfcb92`) | file-backed, probes | 678 / 3853 / 5678 pass | 1795 / 8102 / 6795 **fail** | 614 / 3829 / 5614 pass | 5532 / 16334 / 10532 **fail** |
| smoke 43571196806 (`56e6881a`) | file-backed, probes | 658 / 3338 / 5658 pass | 1770 / 5552 / 6770 pass | 587 / 3220 / 5587 pass | 5758 / 26467 / 10758 **fail** |
| smoke 43590645282 (`85376438`, with #440) | file-backed, probes | 1069 / 5853 / 6069 pass | 3147 / 14924 / 8147 **fail** | 906 / 5604 / 5906 pass | 9598 / 48249 / 14598 **fail** |
| smoke 43568339184 (`3e3d591`) | file-backed, probes | passes | 3523 / 50611 / 8523 **fail** | passes | 7852 / 112261 / 12852 **fail** |
| dispatch 43570060321 (`edfcb92`, `no-batch-probes`) | file-backed, no probes | 674 / 4955 / 5674 pass | 1863 / 12681 / 6863 **fail** | 602 / 4901 / 5602 pass | not run |
| dispatch 43568354287 (`3e3d591`, `memory-db`) | in-memory, probes | 804 / 6343 / 5804 **fail** | 1040 / 4520 / 6040 pass | 863 / 4224 / 5863 pass | 4174 / 16911 / 9174 **fail** |

The runs vary a great deal (the report page's pooled p95 ranged from 8 to 51 ms), but three things are stable. (1) On the file-backed
database the report page misses its bound in three of four runs, with and without the batch probes, and passes by 1.2 ms in the fourth; the same flood on the in-memory
store passes it, which is why the first smokes, which used the in-memory store, passed with a large margin. (2) The real scheduler's enqueue-to-start
p95 is 5 to 8 ms idle and 16 to 112 ms under the flood; it misses the floor rule on both stores, so the executor stays available (a job
starts within a fifth of a second at the worst) but not within the bound. (3) The flood is the cause, not the soak's own work (the variant without probes shows it): in the diagnosis of smoke 43568339184 the block
profile of the loaded minute attributes 31.7 s of goroutine blocking to `database/sql.(*DB).conn` (the single SQLite connection), 19.5 s of it
to `currentActiveUser`. Every flood attempt, the 429 refusals included, pays two session reads (`requireUserJSON`'s `currentActiveUser` and
`platformHTTPReader`) before the relay's admission cap is applied in `apiPlatformBackend`, at 120 to 230 attempts a second; the CPU profile puts
56% of the loaded minute's CPU in the relay, with `_sqlite3RunParser` (statement preparation in the relay's read transactions, which the statement
cache does not cover) at 18%. Moving the admission cap ahead of the session check, or
the relay's read statements onto the cache, are production changes: this PR makes none. The decision is the owner's, and the 120 minute run
will fail these two checks on the same code. SEC-09 therefore stays partial even after a run.

### Diagnosis of the home page latency under load (smoke run 43541609397)

The pull request smoke profiles the measured process for one idle and one loaded minute (`APP_CAPACITY_SOAK_DIAGNOSE=1`: CPU,
block and mutex profiles, a goroutine dump, per-phase timings of the home and report pages as wall time against thread CPU
time, database pool statistics, a 2 ms sample of the connection in use, garbage collector and scheduler figures; artifact
files under `diag/`). The home page failed the pooled gate in two of three earlier runs with 11 to 19 ms windows against a
0.7 ms idle. This run passed the gate, but its loaded diagnostic minute reproduced the tail, and it says where it comes from.

| | idle minute | loaded minute |
| --- | --- | --- |
| home, wall p50 / p95 / max | 650 / 753 / 1827 us | 883 / **22898** / 81361 us |
| home, CPU p95 | 632 us | 967 us |
| home, off-CPU p95 (wall minus thread CPU) | 693 us | **22070 us** |
| report, wall p95 / off-CPU p95 | 913 / 776 us | 4151 / 2875 us |
| database pool: waits for a connection / total wait | 0 / 0 ms | **43439 / 43529 ms** in 60 s |
| the single connection in use (share of 2 ms samples) | 5% | **38%** |
| GC cycles / total pause / p99 pause | 49 / 3.7 ms / 0.23 ms | 304 / 165 ms / 2.6 ms |
| scheduler latency p50 / p99 | 0.06 / 0.13 ms | 0.004 / 5.2 ms |
| mutex profile, total | | 43 ms |

**What dominates.** Waiting for the database connection, not computing and not the host. A home request's CPU time is the
same loaded as idle (under 1 ms); the whole increase is off-CPU, and the block profile puts it in `database/sql` waiting for a
connection (`chanrecv1`/`selectgo` under `QueryRow`, `QueryContext`, `BeginTx`, `Exec`: 116 s of blocked time in the minute,
over the callers of every flood and page). `connectStore` opens SQLite with `SetMaxOpenConns(1)` (`store.go:136`), so every
read and write of every request queues for one connection, and each of home's ten reads (`viewerScope`, `SearchNewLatest`,
`NewTypes`, `ReportKinds`, `presentVersions`, `LinkGroups`, `Links`, `KindColors`, plus the session reads) can wait. Their own
cost is unchanged (7 to 160 us p95 idle) and none of them reads installations, tasks or events: home issues the same ten
reads however many there are, so it is not the N+1 class of the dispatch decision.

**Who holds the connection.** The backend request relay. 66% of the process's CPU in the loaded minute (17.1 of 26 s) is
`apiPlatformBackend`, and 11.8 s of it runs inside `withPlatformBackendRead`, a database transaction, per
`withPlatformWritableBackendAccess`, three of which run for every relayed request
(`app_platform_writable_request.go:63-120`). Inside those transactions the Portal verifies the signed registration
(`control.VerifyWritableBackendRegistration`, 7.1 s: ed25519 and strict JSON decoding of the same documents on every request),
loads the backend's history (`loadPlatformWritableBackendHistory`, 5.8 s) and parses descriptors. 23% of the process's
CPU is SQLite statement preparation (`sqlite3Prepare`): the statements are prepared on each call. The connection is
therefore held for CPU-bound work, and 38% of the time it is held while a page waits.

**What it is not.** The Go mutex profile is empty (43 ms). The garbage collector paused 165 ms in the minute (p99 2.6 ms), an
order of magnitude below the 22 ms tail. Scheduler latency p99 is 5 ms. The goroutine dump (one instant) caught no waiter.
The soak's database is in-memory SQLite, so a WAL checkpoint plays no part here; the pool limit and the work done under it
are the same as production's.

**Is it a Portal defect?** Yes, in the class "work done while holding the only SQLite connection": on a production SQLite
Portal a busy app backend slows every page by queueing it behind that app's request verification. It is invisible on
PostgreSQL (ten connections). Minimal fixes, none implemented here: (1) in `withPlatformBackendRead` read the rows in the
transaction, release it, and verify and decode afterwards; or (2) verify the registration once per
`(installation, generation, release digest, policy)` and reuse the result, since the same documents are verified on every
request; either removes most of the hold time. A larger one is a read-only pool beside the writer for file-backed SQLite.
The 120-minute run will show whether the tail is bounded at the pooled p95; until a fix lands, a pass depends on how hard the
flood leans on the connection (this soak keeps one request in flight per backend).

#### After the fix (#437, `ffb697ba`): smoke run 43548154827

#437 releases the single SQLite connection before a relayed backend access verifies, decodes or signs, and caches successful
control-command verifications for their validity window. The same smoke (the verdict, the diagnostics and the profiling
unchanged) measured:

| diagnostic minute, loaded | before | after |
| --- | --- | --- |
| waits for a database connection | 43439 | 34300 |
| total wait for the connection | 43529 ms | **16179 ms** |
| the single connection in use (2 ms samples) | 38% | **34%** |
| home wall p95 | 22898 us | **7704 us** |
| home off-CPU p95 | 22070 us | **6663 us** |
| report wall p95 / off-CPU p95 | 4151 / 2875 us | 8873 / 6910 us |
| GC total pause / scheduler p99 | 165 ms / 5.2 ms | 141 ms / 5.2 ms |

The pooled verdict of the run (idle p95, threshold, pooled loaded p95): home 772 us, 5772 us, **5417 us (pass)**; report 1239 us,
6239 us, **6386 us (fail by 147 us)**; dispatch at 200 jobs 761 us, 5761 us, **4131 us (pass)**. Per window (home, report,
dispatch p95 in us): 3626 6325 4131; 3836 6203 3135; 4333 6099 3971; 5030 6259 5497; 7905 7849 3958. Home and dispatch pass; report
sits at about 6.2 ms in every window, 5 ms above its idle p95, and the pooled figure is just over its threshold.

**What dominates now.** The relay still occupies the connection a third of the time, and the profile says with what: the work
under the connection is no longer verification but the SQL itself. 57.6% of the loaded process's CPU is still
`apiPlatformBackend`, 13.9 s of it inside the read transactions (`platformBackendReadTx`), and the largest part of that is
SQLite statement preparation and parsing (`sqlite3Prepare`, 35% of all CPU; statements are prepared on every call, as
before), above all in `loadPlatformWritableBackendHistoryPhase` and `finishPlatformWritableInstallHistory` (6.2 s). Three read
transactions still run per relayed request, each re-reading the install history. The remaining cost of a page under relay load
is therefore queueing behind those queries (home off-CPU p95 6.7 ms, down from 22 ms). Nothing here was tuned: the soak is
unchanged, and the gate on report is reported as it came out.

#### After the second fix (#438, `185ab15d`): smoke run 43563565682

#438 adds a SQLite-only prepared-statement LRU at the store's executor (no change on PostgreSQL) and makes `GET /api/stock/{symbol}`
read its decision data in one snapshot (9 connection acquisitions down to 4 for a page with a report). The smoke, with the verdict,
diagnostics and fixture unchanged, passed every check.

| | after #437 | after #438 |
| --- | --- | --- |
| pooled loaded p95: home (threshold) | 5417 (5772) us | **1605 (5234) us** |
| pooled loaded p95: report (threshold) | 6386 (6239) us, fail | **1915 (5389) us** |
| pooled loaded p95: dispatch at 200 jobs (threshold) | 4131 (5761) us | **1757 (5480) us** |
| windows over the threshold, home / report / dispatch | 1 / 3 / 0 of 5 | **0 / 0 / 0 of 5** |
| worst window p95, home / report / dispatch | 7905 / 7849 / 5497 us | 3244 / 2320 / 2666 us |
| diagnostic minute: waits for a connection | 34300 | **22073** |
| total wait for the connection | 16179 ms | **4234 ms** |
| the single connection in use | 34% | **15.5%** |
| home off-CPU p95 | 6663 us | **2224 us** |
| report off-CPU p95 | 6910 us | **810 us** |
| statement preparation (`sqlite3Prepare`) share of CPU | 35% | **22.7%** |
| process CPU in the minute | 29.7 s | 18.7 s |

Per window (home, report, dispatch p95 in us): 1164 1474 1577; 3244 1044 1443; 980 2320 2509; 2038 2169 2666; 651 1462 922. The idle p95
of each series also fell (home 234, report 389, dispatch 480 us). Statement preparation is still 22.7% of the CPU: the relay's
read transactions (`platformBackendReadTx`, 60% of CPU cumulative) still prepare a share of their statements, so a further
reduction is available there, but nothing about the gate needs it.

### After #440 (`0874b426`, admission before session reads): smoke 43590645282 on `85376438` (not evidence)

The branch merged `integration/app-platform` and nothing else changed: verdict, diagnostics and fixture are those of `56e6881a`
(run 43571196806). Both failed only the batch start series; the new run also failed the report page.

| Series (us) | `56e6881a`: idle / pooled / bound | windows 1 to 5 | `85376438`: idle / pooled / bound | windows 1 to 5 |
| --- | --- | --- | --- | --- |
| home | 658 / 3338 / 5658 pass | 3573, 2795, 3258, 3704, 2658 | 1069 / 5853 / 6069 pass | 5460, 6766, 5574, 5706, 6109 |
| report | 1770 / 5552 / 6770 pass | 5133, 6044, 5660, 6255, 4764 | 3147 / 14924 / 8147 **fail** | 13995, 13583, 14287, 15080, 15589 |
| dispatch@200 | 587 / 3220 / 5587 pass | 1720, 3182, 3394, 3691, 2373 | 906 / 5604 / 5906 pass | 3999, 5680, 4754, 5668, 5285 |
| batch start | 5758 / 26467 / 10758 **fail** | 26467, 26393, 15813, 20345, 27340 | 9598 / 48249 / 14598 **fail** | 58980, 42529, 39836, 47715, 42724 |

Diagnostic minute (pages only, back to back), idle then loaded, `56e6881a` against `85376438`:

| | `56e6881a` | `85376438` |
| --- | --- | --- |
| idle home / report p95 (us) | 396 / 1260 | 950 / 3242 |
| loaded home / report p95 (us) | 3463 / 6843 | 5978 / 12859 |
| pool waits, loaded minute (count, total) | 31948, 9556 ms | 6111, 6666 ms |
| connection occupied, idle / loaded (share of 2 ms samples) | 5.6% / 26.5% | 13.8% / 33.5% |
| block profile, `database/sql.(*DB).conn` | 8.39 s | 5.93 s |
| block profile, `currentActiveUser` | 4.36 s | 1.71 s |
| CPU profile, share in the relay's `apiPlatformBackend` | 66% | 63% |
| flood answered / refused per minute | about 670 / 6870 | about 395 / 6950 |

What #440 did, measured: the refused attempt no longer touches the database. The CPU profile attributes 0.05 s (`platformJSONBusy`) and
0.03 s (`platformSessionCookieSigned`) to the pre-check over a minute of about 6950 refusals, roughly 11 microseconds each and no connection
acquisition, against ten statements over nine acquisitions before. Pool waits fell by 81% and the blocked time on the connection by 29%.

What it did not do: the p95 of the pages and of the batch start did not improve in this run, because the second run's host was slower in
every figure including the idle ones (idle home 1069 us against 658, idle report 3147 against 1770, connection occupancy 13.8% idle against
5.6%), and what remains in the loaded minute is the work of the requests the relay does admit. At the cap, two admitted relay requests are in
flight at all times (395 to 670 a minute, each about 14 ms of CPU and eight read statements in one read transaction, with Ed25519 signing and
JSON); the CPU profile puts 63% of the minute in `apiPlatformBackend` under `requireUserJSON`, with statement preparation and the JSON decode
the largest flat entries. This is the profile of the failing series; nothing in the harness was tuned in response. The two figures to compare
across runs are therefore the pool wait count and the refused-attempt cost; the latency series vary with the host by more than the effect of the
change (the report page's pooled p95 has ranged from 5.5 to 51 ms across the runs of this round).

### Evidence files

`capacity-soak-evidence.json` (format `portal.capacity-soak/1-dev.1`: run identity including the branch, the commit and the event, host
facts including the Docker store's free disk, the Go child's evidence with every probe, window and flood class, every host sample with each
backend's cgroup counters, the unit test results, the runner log series and the verdict with each check and the achieved fractions),
`samples.jsonl`, `child-evidence.json`, `unit-tests.json`, `disk-before.txt`, `runner-log.jsonl`, `portal-parent.log`, `portal-child.log` and
`runner.log`, uploaded as the `capacity-soak-evidence` artifact (90 days for a dispatch run).

### PR 4b evidence

**The 120-minute dispatch run passed (OPS-08).** Run [43683877826](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/43683877826), a `workflow_dispatch`
of `feat/app-platform-capacity-soak` at `8121574b90c9aa8110ada71dcfe8a19604608e50` with `expected_commit` matching, `soak_only=true`, `duration_minutes=120`, PostgreSQL 16.15 (`db_driver=postgres`).
The `capacity-soak` job ran on 2026-11-15 from 06:05:44Z to 08:11:10Z (7455 s of harness time). Verdict `passed=true`, `counts_as_ops08_evidence=true`, `evidence_blockers=[]`, 25 of 25 checks. Artifact `capacity-soak-evidence`
(90 days); the numbers are recorded in `handoff/evidence/platform-capacity-envelope/acceptance.json`.

Pooled loaded p95 (the verdict, resolution 74), with the idle baseline and the worst one-minute window as diagnostics (microseconds):

| Series | Idle p95 | Pooled p95 | Bound | Samples | Worst window | Windows over the bound |
| --- | --- | --- | --- | --- | --- | --- |
| home | 2108 | 10817 | 50000 | 12000 | 17265 (minute 73) | 0 |
| report | 2346 | 17498 | 50000 | 12000 | 25963 (minute 83) | 0 |
| dispatch@200 | 948 | 9902 | 25000 | 12000 | 14856 (minute 119) | 0 |
| batch start | 9113 | 43686 | 100000 | 2400 | 59403 (minute 87) | 0 |

Other results:

- **Disk-health precheck (resolution 75) did not trip:** idle WAL sync mean 0.064 ms; fsync probe median 0.187 ms, p95 0.213 ms, maximum 0.435 ms (200 writes of 4 KiB). The probe's 1 ms bound sits well above this host.
- **Probes:** all ten native probes were refused with their typed errors; the four that install something were retired by the managed uninstall. The image store probe ran natively. The per-application task quota is proven at unit level, and its tests passed in the job.
- **Backends:** eight backends of 488 MiB and 240 mCPU each (runner budgets 3925 MiB and 2000 mCPU); none crashed, was OOM-killed or restarted. CPU used at least 0.996 of the quota in every window, PIDs at least 0.891 of the limit in every window, memory held at the held fraction in every window. Summed backend memory peaked at 3.66 GB and CPU at 1980 mCPU, inside the budgets.
- **Message flood:** 80045 requests answered and 830028 refused 429 `app_platform_busy`, no other class, and answered, typed and alive in every window.
- **Runner journal:** grew 1.24 MB over the load against a 256 MiB bound. **Host:** 240 of 240 samples, none failed; lowest available memory 68% against the 10% floor. The session outlived the run.

**Remaining limits.**

- SEC-09 stays partial: the task worker and event delivery coordinator are not exercised by the soak, and no request originates from a backend. Its OPS-08 dependency is met.
- The per-application task quota is proven at unit level only, and the runner's own refusal of an over-declared layer is covered by the blob channel unit tests.
- One host class, one run: the earlier two runs of the same configuration measured home at 20.1 and 37.3 ms (5 minutes), so a rerun on another host can differ; the bounds leave a factor of 1.7 to 4.6 over the worst windows and pooled values of this run.

**Follow-ups that still block the integration to main merge.**

- **SQLite known limit and its read pool.** On a SQLite Portal the relay's two admitted requests keep the single connection about a third occupied and the gate is missed (resolution 73); a SQLite read pool is the tracked follow-up, and no claim is made that the SQLite envelope is proven.
- **PR 4c residuals** (the section above): the orphaned restore start, enable or repair left `recovery_required`, the enable start held only as reserved, a transient error abandoning a removal, held-start settlement starving itself, and the late-create window.
- **SEC-09 partial:** exercising the task worker and event delivery coordinator under the load, and a backend-originated message or task flood.
