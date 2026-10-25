# Disposable Linux project-quota enforcement probe

Status: native XFS workload acceptance passed under approved D1/D3/D4 development
candidate A for the source recorded below. Later source revisions still require
their own CI verification. This is a filesystem
prerequisite for data preparation, not a production data provider, complete runtime
profile or permission to activate an installation.

## Why this precedes the data provider

The accepted runtime profile requires hard byte and inode quotas for owned app
data. A directory's apparent size, a periodic scanner, a database quota field or
Docker's advertised storage driver cannot prove those limits. In particular, a
file unlinked while a process keeps it open still consumes physical blocks.

XFS provides project accounting/enforcement and inherited project IDs. This probe
uses the documented [XFS project quota commands](https://man7.org/linux/man-pages/man8/xfs_quota.8.html)
and [kernel mount options](https://docs.kernel.org/admin-guide/xfs.html), then tests
actual allocation/writes and file creation. Metadata flags are checked only as
prerequisites; they are not the enforcement result.

## Exact isolated resource scope

`.github/workflows/app-project-quota-probe.yml` runs only on an ephemeral
GitHub-hosted Ubuntu 24.04 worker with read-only repository token permissions. It
installs `xfsprogs` on that disposable worker and calls:

```sh
sudo --preserve-env=GITHUB_ACTIONS,RUNNER_ENVIRONMENT \
  python3 scripts/acceptance/project_quota_probe.py --disposable-github-worker
```

Do not run this on a production server or existing filesystem. The script refuses
non-Linux/non-root hosts, missing explicit opt-in and environments that do not
identify themselves as GitHub-hosted Actions workers before creating any resource.
Those checks are operator misuse guards, not a security boundary against root.

All mutable targets derive from one newly created root-owned temporary directory.
The probe creates a new sparse 1 GiB regular image exclusively, attaches a new loop
device, verifies the device's backing file and formats only that verified device
as XFS. It never takes an existing disk, mount, configured application directory
or caller-supplied path as a target. It changes neither `/etc/projects` nor
`/etc/projid`; project paths/IDs are explicit on each acceptance command.

The owned mount uses `prjquota,nodev,nosuid,noexec`. The script checks the actual
mount source/type/options and project accounting/enforcement state. Each project initially
has an 8 MiB byte hard limit and 128-inode hard limit. Separate final control
steps raise only the exhausted byte project to 16 MiB and inode project to 256
inodes, verifying that those limit changes reverse their earlier denials. Private project folders
belong to synthetic UID/GID 10000 with mode 0700. Workloads run through `setpriv`
with cleared supplementary groups and no-new-privileges; a root write is not used
as the quota test workload.

There are no application secrets, publisher keys, Docker calls, network workload,
production mounts, Portal database changes, enrolled runtime or app routes.

## Required real observations

`scripts/acceptance/quota_workload.py` bounds time, bytes and file creation. A test
accepts only native EDQUOT/ENOSPC failures near the configured hard limit, with
independent evidence that the underlying filesystem still has at least 64 MiB and
1,024 inodes available. Permission errors, actual filesystem exhaustion, early
allocation failures, short writes and unrelated errors cannot pass. XFS deliberately
returns ENOSPC for project quota hard-limit failures; see the kernel's
[project quota reservation error path](https://github.com/torvalds/linux/blob/master/fs/xfs/xfs_trans_dquot.c).
The errno alone therefore never proves enforcement.

| Workload | Required observation |
| --- | --- |
| Bytes | Actual unprivileged fallocate/write/fsync operations initially succeed, then hit the native quota denial near and within 8 MiB of allocated/written data |
| Inodes | Create files under a nested inherited-project directory, then hit the native quota denial near and before exceeding 128 files |
| Open/unlinked | Unlink a still-open file, continue allocating/writing/fsyncing through its FD, and still hit the byte hard quota |
| Independent project | After each quota denial, a different project can write/fsync its own 1 MiB file |
| Remount | Unmount/remount the same owned filesystem with project enforcement, then require the exhausted byte project to remain quota constrained |
| Limit control | Keep the exhausted byte/inode files; raise only that same project's limit and require the formerly blocked allocation/create to succeed |
| Closed unlinked FD | After the still-open unlinked-file denial, close the FD and require that same project to admit a fresh write under its unchanged limit |

Each project uses a separate ID; independent writes cannot succeed by freeing the
exhausted project's files first. Limits are small enough to distinguish project
quota from filling the 1 GiB filesystem. The workload is capped at four times the
byte limit or 512 file creations and has a 30-second alarm. Missing enforcement
fails at the bounded ceiling rather than writing indefinitely.

The JSON report records native kernel/architecture, filesystem tool version,
fixed limits, individual observed denials/counts and final cleanup status. It
contains no host IDs, image paths or credentials. It always states
`execution_ready=false`: data hard quotas alone do not prove sandboxing, Docker
constraints, controller authentication, resource ownership/fencing or readiness.
The report is retained as a short-lived workflow artifact for review.

## Cleanup and refusal behavior

If attach or mount acknowledgement is lost, discover the actual owned resource
before cleanup; do not trust a local success boolean as absence. Treat failed
inspection as unknown, not unmounted. Before unmount or detach, verify that the mount still identifies the owned loop
and that the loop still points to this exact newly created image. Refuse cleanup
of substituted devices or mounts. Unmount first, detach second, remove only the
new temporary directory after both succeed. If a check or cleanup fails, do not
broaden the target or recursively delete a still-mounted filesystem; the disposable
worker teardown is the final boundary.

The probe is not a general mount API. No incoming app metadata or signed control
command can select its command, device, mount, project ID, user identity or path.
No caller-supplied success flag can set a runtime's execution readiness.

## Local and CI verification

The unprivileged workload is copied to the root-owned dedicated temporary folder;
it does not require the synthetic UID to traverse the runner's private home or
checkout. Commands use a fixed tool path/environment, with bounded diagnostics.

Local tests exercise refusal before mutation, substituted device/mount rejection,
native-denial versus actual-filesystem-exhaustion classification and the fact that an ordinary unquotaed
filesystem cannot pass the bounded inode workload. These are orchestration/error
checks, not Linux hard-quota evidence:

```sh
python3 -B -m unittest discover -s scripts -p project_quota_probe_test.py
```

The macOS host refuses the native entry before creating a temporary image. Workflow
lint must pass; the actual GitHub-hosted Linux run must pass all workload assertions
and ownership cleanup before any native result is recorded here. Do not infer
success from a green unit suite or substitute cross-compilation for the run.

Still required after a successful probe: owned data-root/runtime adapter integration,
project-ID allocation and native ownership reconciliation, durable typed effect
admission/receipts, authenticated runner transport, restoration/writer exclusion,
quota-aware capacity reservation, container CPU/memory/PID/tmp/network tests and
full static/managed installation acceptance. Ext4 and Linux arm64 remain unverified
until they pass equivalent native cases. No production rollout is authorized here.


## Recorded native evidence

[Native workflow 40195675252](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40195675252)
passed on 2026-10-25 UTC for implementation source
`3661bad8e3d6e0de21e10b2cc78c85b1049e371b`, on Ubuntu 24.04 x86_64,
kernel `6.17.0-1022-azure`, `mkfs.xfs` 6.6.0. The retained JSON reports:

- Byte and open/unlinked-FD denial at 8,323,072 allocated/written bytes, under the
  configured 8,388,608-byte project hard limit. Filesystem/project metadata and
  reservation also consume quota; the limit is not an exact payload file size.
- Nested-project inode denial after 126 regular files under a 128-inode limit.
- Native ENOSPC with over 934 million bytes and over 524,000 inodes still available
  on the underlying uncapped filesystem. Each independent project write succeeded.
- An exhausted project remained constrained after remount. Raising the same byte
  project to 16 MiB admitted another 1 MiB without deleting its original charge;
  raising the same inode project to 256 admitted a new file. Closing the previously
  unlinked FD released its charge and admitted a fresh write under unchanged limits.
- Ownership-checked unmount/detach/directory cleanup completed, with
  `execution_ready=false` throughout.

The first native run exposed the synthetic UID's inability to traverse the
checkout; the workload now lives in the dedicated root-owned fixture. The next
run exposed the initial EDQUOT-only assumption. Kernel source and actual XFS
behavior required the stricter capacity/independent-project/limit-change controls
above, rather than accepting ENOSPC alone or mislabeling it EDQUOT.

A follow-up cleanup guard also checks actual loop absence after detach before
removing its image; its newer source needs its own workflow result. None of this
promotes Portal's runtime compatibility report to execution readiness or completes
P04/P05 acceptance.
