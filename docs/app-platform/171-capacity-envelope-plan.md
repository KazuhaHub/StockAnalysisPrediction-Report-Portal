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
