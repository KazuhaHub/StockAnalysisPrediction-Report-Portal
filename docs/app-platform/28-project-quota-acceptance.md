# Disposable Linux project-quota enforcement probe

Status: native acceptance implementation under approved D1/D3/D4 development
candidate A. Actual CI results must be inspected separately. This is a filesystem
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
mount source/type/options and project accounting/enforcement state. Every project
has an 8 MiB byte hard limit and 128-inode hard limit. Private project folders
belong to synthetic UID/GID 10000 with mode 0700. Workloads run through `setpriv`
with cleared supplementary groups and no-new-privileges; a root write is not used
as the quota test workload.

There are no application secrets, publisher keys, Docker calls, network workload,
production mounts, Portal database changes, enrolled runtime or app routes.

## Required real observations

`scripts/acceptance/quota_workload.py` bounds time, bytes and file creation. A test
counts only kernel `EDQUOT` as quota denial. Permission errors, global disk-full
`ENOSPC`, short writes and unrelated failures cannot produce passing evidence.

| Workload | Required observation |
| --- | --- |
| Bytes | Actual unprivileged fallocate/write/fsync operations initially succeed, then hit EDQUOT within 8 MiB of allocated/written data |
| Inodes | Create files under a nested inherited-project directory, then hit EDQUOT before exceeding 128 files |
| Open/unlinked | Unlink a still-open file, continue allocating/writing/fsyncing through its FD, and still hit the byte hard quota |
| Independent project | After each quota denial, a different project can write/fsync its own 1 MiB file |
| Remount | Unmount/remount the same owned filesystem with project enforcement, then require the exhausted byte project to remain quota constrained |

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

Before unmount or detach, verify that the mount still identifies the owned loop
and that the loop still points to this exact newly created image. Refuse cleanup
of substituted devices or mounts. Unmount first, detach second, remove only the
new temporary directory after both succeed. If a check or cleanup fails, do not
broaden the target or recursively delete a still-mounted filesystem; the disposable
worker teardown is the final boundary.

The probe is not a general mount API. No incoming app metadata or signed control
command can select its command, device, mount, project ID, user identity or path.
No caller-supplied success flag can set a runtime's execution readiness.

## Local and CI verification

Local tests exercise refusal before mutation, substituted device/mount rejection,
strict EDQUOT error classification and the fact that an ordinary unquotaed
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
