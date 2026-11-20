# Proposal: backend apps on Linux (ext4 and others), macOS and Windows — runtime and data quota options

Status: Accepted 2026-11-20. Phases P0 to P2 are implemented (section 10);
P3 and P4 are not. No schema or contract change. Related: docs
[28](28-project-quota-acceptance.md), [46](46-native-xfs-data-primitive.md),
[49](49-data-root-identity.md), [66](66-candidate-container-policy.md),
[146](146-runner-service.md), [171](171-capacity-envelope-plan.md),
[175](175-reviewed-multi-origin-and-private-network.md).

Evidence is against `main` at `f3ac2120`. External facts about kernels,
filesystems and desktop container products are stated as precisely as known
and are marked **verify on a real host** where the behaviour depends on a
version, a build option or a product release that this repository has not
tested.

## 1. Problem and evidence

### 1.1 What the owner asked for

Apps with a backend must run on Linux, macOS and Windows, matching the Portal's
release platforms. The release matrix builds exactly six Portal binaries,
`linux`, `darwin` and `windows` on `amd64` and `arm64`
(`.github/workflows/release.yml:339-346`), and the packaging step refuses
anything other than six artifacts (`release.yml:502`). Only `./cmd/report-portal`
is built (`release.yml:385`); the app-runner binary is built only inside the
probe workflow (`.github/workflows/app-runner-probe.yml:85`, `:201`) and has no
released artifact on any platform today.

### 1.2 Where backends are refused today

- **Data layer.** The native data root is implemented only for
  `linux && (amd64 || arm64)` (`internal/apprunner/data/data_linux.go:1`); every
  other target compiles a stub that returns `ErrUnsupported` for every operation
  (`internal/apprunner/data/data_other.go:1-63`). The service that opens the
  roots has the same tag (`internal/apprunner/service/runner_linux.go:1`).
- **XFS only, even on Linux.** `openRoot` requires `euid 0`
  (`data_linux.go:90`), a root-owned `0700` directory on a filesystem whose
  `statfs` magic is `XFS_SUPER_MAGIC` with `nodev,nosuid,noexec`
  (`data_linux.go:111`, repeated on every identity read at `:168`), and reads
  the filesystem UUID through the XFS-only `XFS_IOC_FSGEOMETRY` ioctl
  (`data_linux.go:27-29`, `:182`; doc 49 lines 44-47). An ext4 directory fails
  at the magic check before any quota call.
- **Candidate runtime.** The container driver is Linux-only; on other systems
  `OpenCandidateRuntime` returns `ErrUnsupported`
  (`internal/apprunner/driver/candidate_runtime_other.go:1-15`).
- **Controller side in Portal.** Every Portal endpoint that talks to the runner
  has a `_linux.go` implementation and a `_other.go` refusal stub (24 pairs in
  `internal/app`, for example `app_platform_managed_runtime_other.go:1-13`,
  which returns `errAppPlatformRuntimeConflict`). The darwin and windows Portal
  binaries therefore cannot drive a runner even if one existed.
- **Docker profile.** Runner preflight requires a native Linux engine on the
  runner's own architecture, cgroup v2, an `overlay2`/`overlayfs` image store, a
  rootful daemon without user-namespace remapping and the built-in seccomp
  profile (`internal/apprunner/driver/preflight.go:126-153`). The daemon peer
  check additionally requires that every holder of the Docker socket connection
  be a `dockerd` in the runner's own mount namespace, and names Docker Desktop
  as refused (`internal/apprunner/driver/daemon_peer_linux.go:23-43`; doc 171
  line 677).
- **Documentation.** The handbook's administrator setup says unsupported hosts
  "remain static-only until a supported adapter is installed"
  (`docs/app-platform/07-handbook.md:86-88`). The architecture states that the
  initial managed runtime is Linux Docker and that "a remote runner is not
  supported in the first release" (`01-architecture.md:56`, `:66-68`). The
  runner service requires "a dedicated rootful Linux host or VM" with an XFS data
  root and a distinct XFS checkpoint root (`146-runner-service.md:12-14`,
  `:54-57`).

### 1.3 The quota implementation in detail

The adapter never runs a shell or quota CLI (`data_linux.go:18-19`). It uses:

| Operation | Call | Source |
| --- | --- | --- |
| Quota state | `quotactl_fd(Q_XGETQSTATV, PRJQUOTA)`; requires project accounting and enforcement flags `0x30` | `data_linux.go:21`, `:62-70`, `:78-88` |
| Read limits/usage | `quotactl_fd(Q_XGETQUOTA, PRJQUOTA)`; `ENOENT`/`ESRCH` mean "no record"; any soft limit is refused | `data_linux.go:22`, `:211-245` |
| Set limits | `quotactl_fd(Q_XSETQLIM)` with field mask `FS_DQ_BHARD|FS_DQ_IHARD` (10) | `data_linux.go:23`, `:362-372` |
| Project ID + inherit | `FS_IOC_FSGETXATTR` / `FS_IOC_FSSETXATTR`, `FS_XFLAG_PROJINHERIT` (0x200) | `data_linux.go:24-26`, `:376-387` |
| Root identity | `XFS_IOC_FSGEOMETRY` UUID at offset 64 | `data_linux.go:27-29`, `:181-186` |
| Root exclusivity | `flock(LOCK_EX|LOCK_NB)` on the pinned root FD | `data_linux.go:114` |

Other XFS-specific assumptions: delete waits for XFS deferred inode
inactivation (`delete_linux.go:165-180`); the import pre-check assumes XFS's
whole-block charging (`data.go:276-281`, `import_linux.go:207-225`); the journal
records the native project as `xfs-project/v1/<id>` inside a hashed ownership
claim (`internal/apprunner/journal/data_reservation.go:20`, `:51`). The checkpoint
root is opened with the same `data.Open` and must therefore also be XFS with
project enforcement (`runner_linux.go:59`, `:73-80`). The XFS behaviour was
proven natively on amd64 only (doc 28 "Recorded native evidence"; doc 49
lines 88-95, arm64 is cross-compiled only).

### 1.4 The first real workload: market-monitor

Grounded in Apps PR #45 (`feat/market-monitor-import`, `apps/market-monitor/`):

- **Not yet an app.** No `app.v2.json`, no `frontend/` (`README.md:7-9`). The
  plan is a Go v2 backend on `packages/extensionprotocol` that schedules the
  Python jobs as background tasks and serves read endpoints (`README.md:36-40`).
  Today the "backend" is a FastMCP server on `127.0.0.1:8091`
  (`pipeline/stock_server/requirements.txt`; `pipeline/systemd/daily-stock-server.service:13-15`).
- **Data size.** Bar store `bars/` 349 MB plus `snapshots/` about 304 MB
  (`pipeline/MarketData/README.md:6`); the import README says the 2014-onward
  bar store is "about 510 MB" (`README.md:22`), so the two figures disagree. The
  deployment guide asks for at least 3 GB free: 737 MB unpacked, about 300 MB per
  year of increments, plus the virtualenv (`pipeline/README.md:57`). Peak
  memory is about 1.2 GB (`pipeline/README.md:58`).
- **Write pattern.** Parquet, JSON and CSV only; no SQLite or DuckDB. Each yearly
  partition is rewritten as `.tmp` then `os.replace`
  (`pipeline/MarketData/README.md:115`). Before each write the changed
  partitions are hard-linked into `snapshots/` (three kept, about 90 MB steady
  state; one daily update orphans about 28 MB, `:131-142`). Locks are `mkdir`
  directories, not `flock`. The README records a cleanup incident in which the
  snapshot directory looked like 22 GB but held 2.4 GB of real data, because
  most entries were hard links (`:145-147`). That is exactly the error a
  `du`-style soft quota makes (section 3.6).
- **Schedule.** Asia/Shanghai is mandatory (`pipeline/README.md:55`); jobs at
  21:00 and 23:30 Mon-Fri (bars, about 100 minutes), 00:40/00:50/08:00 Tue-Sat,
  09:00/09:50 Mon-Fri (`pipeline/README.md` section four).
- **Network.** An upstream quote API at the public IP literal
  `47.237.204.217:8000` over plain HTTP (`pipeline/MarketData/bulk_pull.py:57`),
  East Money (`push2.eastmoney.com`, `push2his.eastmoney.com`,
  `api.fund.eastmoney.com`), Tencent (`qt.gtimg.cn`, `web.ifzq.gtimg.cn`), and
  SSE/SZSE/Sina reached through `akshare` calls whose hosts are not visible in
  this source. Note for doc 175: a public IP literal over plain HTTP is accepted
  by neither a `public` target (https and DNS names only) nor a `private` target
  (allowlist prefixes must lie in RFC 1918, 100.64/10 or fc00::/7,
  `internal/appplatform/private_networks.go:52`, `:61`). That is outside this
  document, but the upstream needs a DNS name with TLS before the app can be
  reviewed.

So the workload is small (under 1 GB now, about 3 GiB with growth and
headroom), writes in bursts of whole-file rewrites of tens of MB, and depends
on hard links staying cheap. Any quota mechanism must charge hard links once.

## 2. What the current model assumes

| Assumption | Where | Holds inside a Linux VM? | Holds across a host-to-VM boundary? |
| --- | --- | --- | --- |
| Rootful native `dockerd`, same mount namespace as the runner | `preflight.go:151`, `daemon_peer_linux.go:23-43` | Yes, when the runner and dockerd run in the same VM | No: a desktop product's daemon runs in its own VM, so no local `dockerd` holds the connection |
| UID/GID 65532, read-only rootfs, dropped capabilities, `network=none` | `66-candidate-container-policy.md:37-41` | Yes | Not applicable: the containers always run in the VM |
| HTTP and broker Unix sockets in runner-owned directories, bind-mounted | `66-candidate-container-policy.md:48-49`; `01-architecture.md:83-86`; `service_broker_linux.go:200` | Yes, if the directories are on a VM-local filesystem | No: a Unix socket in a directory shared from macOS or Windows is generally not connectable from the other side (verify on a real host for each product and file-sharing mode) |
| Pinned directory FDs and `/proc/self/fd/<n>` bind sources | `candidate_ipc_linux.go:112-121`; `mount_pins_linux.go:47-91` | Yes | No: the FD is in the runner's process; dockerd in another VM cannot resolve it |
| Root-owned `0700` directories, UID/mode checks | `data_linux.go:111`, `:274-276` | Yes | No: shared folders emulate ownership and mode bits (virtiofs, gRPC FUSE, 9p/drvfs); a `stat` result is not an enforced permission |
| XFS project quota | `data_linux.go` | Yes, on a VM-local XFS disk | No: APFS and NTFS have no Linux project quota; a shared folder cannot carry one |
| Peer credentials on the controller socket (`SO_PEERCRED`) | `transport_linux.go:134-145`, `:536-562` | Yes, if Portal and runner are in the same VM | No: TCP, vsock and Hyper-V sockets carry no UID; Windows has no `SO_PEERCRED` |
| Reply authenticity | Reply is magic plus SHA-256 of the request frame, not a signature (`transport_linux.go:521-531`); trust comes from the peer UID check at `:559` | Yes, same VM | No: over a network transport a forged reply is indistinguishable without TLS or a signed reply |

Requests are already authenticated at the message layer: every connection must
carry a valid signed intent in addition to the peer UID
(`transport_linux.go:152-157`). Replies and receipts are not; that is the main
gap a split host-to-VM deployment would have to close (section 4.3).

## 3. Linux options

### 3.1 Native ext4 project quota

**What the kernel provides.** ext4 gained project quota in Linux 4.5 (the
`project` feature, e2fsprogs 1.43 or later). It requires the `quota` feature
(journaled quota in hidden system inodes; project quota is not supported with
legacy external quota files) and inodes of at least 256 bytes, which is the
`mkfs.ext4` default for filesystems that are not tiny. A new filesystem is
created with `mkfs.ext4 -O quota,project` and mounted with `prjquota`; with the
`quota` feature, accounting is always on and the mount option turns on
enforcement. Verify on a real host: the exact interaction of `-O quota`,
`-Q prjquota` and the mount option with the installed e2fsprogs.

**How the existing calls map.** The adapter already uses the XFS-format
`Q_X*` quotactl commands, but the kernel quota layer translates them to the
generic `quotactl_ops` for filesystems that are not XFS: `Q_XGETQSTATV` to
`get_state`, `Q_XGETQUOTA` to `get_dqblk`, `Q_XSETQLIM` to `set_dqblk`, all of
which ext4 implements through `dquot_quotactl_sysfile_ops`. The state flags are
derived from the generic quota state, so the `0x30` check
(`data_linux.go:84`) should pass on an ext4 mount with project accounting and
enforcement. `quotactl_fd` itself needs Linux 5.14 or later on both
filesystems (`data_linux.go:64`). `FS_IOC_FSGETXATTR`/`FS_IOC_FSSETXATTR` with a
project ID and `FS_XFLAG_PROJINHERIT` are supported on ext4 since 4.5 when the
`project` feature is on. Changing a project ID is permitted only from the
initial user namespace, which the root runner is in. Verify on a real host:
every one of these mappings, by running the doc 28 workload against ext4.

**What changes in code** (an `ext4` root profile beside the XFS one):

1. Accept `EXT4_SUPER_MAGIC` as well as XFS at `data_linux.go:111` and `:168`,
   and pin the profile at open so a root cannot change type between reads.
2. Replace the XFS geometry UUID read (`:181-186`) with
   `EXT4_IOC_GETFSUUID` (Linux 6.0 or later) or the generic `FS_IOC_GETFSUUID`
   (recent kernels; verify the first version on a real host). Ubuntu 24.04's
   6.8 kernel has the ext4 ioctl.
3. "No record" semantics: XFS answers `ENOENT` for an unused project ID; the
   generic layer usually returns a zero record instead. `prepareLocked` already
   requires a zero record (`:339-345`), so this should be compatible; confirm
   natively.
4. Root writes: the generic quota code skips hard limits for a task with
   `CAP_SYS_RESOURCE` (`ignore_hardlimit` in `fs/quota/dquot.c`). Apps run with
   all capabilities dropped (doc 66 line 38), so app writes are held. The
   runner's own restore and import writes run as root and **would not be held
   on ext4**. The import pre-check (`import_linux.go:207-225`) stays mandatory,
   and restore/import writes should move to a helper process that has dropped
   `CAP_SYS_RESOURCE`. Whether XFS exempts root must also be verified; doc 28
   deliberately never used a root writer (lines 51-52).
5. Footprint: ext4 also charges extent-tree and external xattr blocks, and
   directories grow in blocks; recalibrate `importFootprint` (`data.go:276`)
   with a native fixture.
6. Delete: the XFS inodegc wait (`delete_linux.go:171-176`) is harmless on ext4,
   which frees blocks at the last close.
7. Journal: keep `xfs-project/v1/` for XFS roots and add a distinct
   `ext4-project/v1/` prefix for ext4 roots, so a claim digest records which
   profile it was made on (`data_reservation.go:20`, `:51`). A journal never
   mixes profiles.
8. Errors: ext4 answers `EDQUOT`, XFS `ENOSPC` (doc 28 lines 63-66). The
   workload classifier must accept both, together with independent evidence
   that the filesystem itself is not full.
9. Inodes: ext4's inode table is fixed at `mkfs` time (the default
   bytes-per-inode ratio). An inode hard limit still works, but global inode
   exhaustion becomes a host capacity input that XFS's dynamic allocation does
   not have. Capacity reporting (doc 171 item 8, line 386) must also read
   `f_files`/`f_ffree`.

**Can an existing ext4 `/data` be converted in place?** Only offline.
`tune2fs -O project` (and changing the `quota` feature) is refused on a mounted
filesystem by current e2fsprogs, and the `project` feature is refused when the
inode size is 128 bytes (verify both with the installed version). If `/data` is
a separate partition, conversion is an unmount window, a `tune2fs`, an
`e2fsck -f`, and a remount with `prjquota`. If `/data` is the root filesystem,
it needs a rescue boot. Existing files keep project 0, which is fine because
the runner only ever creates fresh namespaces. Operationally this is a
maintenance window on a production host, and it is the main reason section 3.2
is the primary recommendation.

**How to detect and advertise.** The runner already reports Docker findings;
add a root-profile finding (`xfs-prjquota` or `ext4-prjquota`, plus kernel
version and whether quota is journaled). Advertise it through a separate
runner capability query, not by adding fields to the managed runtime profile:
the profile is compared with `==` against stored plans, which is why doc 175
amendment 1 used a separate query
(`175-reviewed-multi-origin-and-private-network.md:243-248`).

### 3.2 A dedicated XFS filesystem in a loop-mounted image file (recommended primary)

The probe in doc 28 already creates "a new sparse 1 GiB regular image", attaches
a loop device and formats it as XFS (`28-project-quota-acceptance.md:39-44`),
and the capacity soak used a loop-backed 16 GiB XFS volume on an ext4 runner
disk (doc 171 lines 1236, 1637). The same construction, made permanent, gives
an ext4 host a real XFS project-quota root with **no change to the quota
code**:

- An image file on the existing ext4 `/data` (for example
  `/data/portal-runner/data.img` and `checkpoint.img`), owned by root and mode
  `0600`, in a root-only directory.
- **Preallocate, do not leave it sparse.** Use `fallocate -l <size>` so the
  backing blocks are reserved on ext4. With a sparse image, a full host
  filesystem turns guest writes into I/O errors on the loop device, and XFS
  shuts the filesystem down on a failed metadata write. That is a
  whole-runner outage rather than one app's `ENOSPC`. Preallocation costs the
  full size up front (for example 8 GiB data plus 8 GiB checkpoint,
  matching doc 167's reference budget, line 260).
- Attach with `losetup --direct-io=on` (Linux 4.10 or later) to avoid caching
  pages twice, and mount `xfs loop,prjquota,nodev,nosuid,noexec`. Persist it
  with an fstab entry or a systemd mount unit, with the runner unit ordered
  after it (`RequiresMountsFor=`).
- **Growth:** grow the file (`fallocate` to the new size), refresh the loop
  device's capacity (`losetup -c`), then `xfs_growfs` online. XFS cannot shrink;
  record that as a one-way operation.
- **fsck:** `xfs_repair` on the unmounted image; the host's `e2fsck` checks only
  the outer filesystem.
- **Crash consistency:** the loop driver turns the guest's flush and FUA
  requests into `fsync` of the backing file, so XFS's journal ordering is
  preserved across a host crash, at the price of one extra filesystem layer.
  Verify on a real host with a power-cut test (section 7).
- **Performance:** for market-monitor (tens of MB rewritten per day, about
  100 minutes of mostly network-bound jobs) the double layer is immaterial.
  The soak's loop volume ran at 1-3% utilisation (doc 171 line 1662).
- **Backup:** never copy the live image file as a backup; it is not
  crash-consistent unless frozen (`xfs_freeze`). The platform's own recovery
  checkpoints and restore bundles (docs 155, 166, 168) are the supported path,
  and host backups should exclude the image files.
- **Roots:** the code only requires the data and checkpoint roots to have
  different pinned identities (`runner_linux.go:80`), so they could be two
  directories on one XFS image. Two images are recommended, so that a full
  data volume cannot block checkpoints.
- **Root requirements:** creating and attaching the image is a one-time
  administrator step (root), the same class of step as creating the XFS
  partition in doc 146 today. The runner itself never mounts anything.

This also works on btrfs hosts (set `chattr +C` on the empty image first so
copy-on-write does not fragment it) and on ZFS hosts (put XFS on a zvol instead
of a file).

### 3.3 LVM thin volumes

A thin LV formatted XFS is equivalent to section 3.2 without the file layer.
Per-app thin LVs (one filesystem per installation) would give each app a hard
size without project quota, but they conflict with the single pinned root and
FD model (`runner_linux.go:54-80`), need device-mapper and `mkfs`/`mount` from
the runner (which today never mounts or runs a CLI), and overcommit the pool:
when a thin pool fills, every volume in it errors or blocks. **Acceptable only
as the administrator's way to provide the single XFS data volume**, not as a
per-app mechanism.

### 3.4 btrfs qgroups, ZFS datasets, overlay

- **btrfs qgroups** limit a subvolume's referenced or exclusive bytes. Enabling
  quotas has a known cost on large or snapshot-heavy filesystems; "simple
  quotas" arrived in Linux 6.7. Each app would need a subvolume (ioctls or
  `btrfs` CLI), and there are no inode limits. Not recommended.
- **ZFS** `refquota` per dataset is robust, and OpenZFS also has project quota
  through `FS_IOC_FSSETXATTR`, but limits are set through ZFS ioctls or the
  `zfs` CLI, not `quotactl`, and the module is out of tree on most
  distributions. Not recommended as an adapter. XFS on a zvol (section 3.2) is
  the way to support a ZFS host.
- **overlay** has no quota of its own.

### 3.5 Docker volume and storage size options

`docker run --storage-opt size=` limits only the container's writable layer,
and only on `overlay2` over XFS with `pquota` (or on `devicemapper`, `btrfs`,
`zfs`, `windowsfilter`). The runner uses a read-only root filesystem and a
16 MiB tmpfs (doc 66 lines 39, 42), and app data is a bind mount
(doc 66 line 47), so this option limits nothing that matters. The `local`
volume driver accepts a `size=` option only for `type=tmpfs` (memory, not
persistent). Neither is a data quota.

### 3.6 Soft quota by periodic accounting

A scanner that sums usage every *T* seconds and stops or freezes the container
over the limit cannot bound usage between scans:

- A writer at NVMe speed (1-3 GB/s buffered) overshoots by 10-30 GB in a 10 s
  interval, that is 3 to 10 times a 3 GiB quota, and fills the host disk first.
  Even with cgroup v2 `io.max` at 50 MB/s, a 60 s interval allows 3 GB of
  overshoot, 100% of the quota.
- `du`-style accounting cannot see files that are unlinked but still open, the
  case doc 28 tests (`28-project-quota-acceptance.md:11-13`, `:83`). It
  double-counts hard links unless it tracks inodes, and market-monitor's own
  incident (22 GB apparent against 2.4 GB real,
  `pipeline/MarketData/README.md:145-147`) shows how misleading that is.
- Enforcement by stopping the container mid-write can leave a half-written
  partition. The app's `.tmp` plus `os.replace` protects readers but not a
  killed writer's temporary files.

Doc 28 already rejected scanners as proof of a limit (lines 9-13). **Not
recommended, even with an acknowledgement:** a host-wide disk-full outage
affects Portal, its database and the other tenants, not only the app that
caused it.

### 3.7 Linux recommendation

- **Primary: XFS project quota on a dedicated XFS volume.** Use a partition or
  LV where one exists. On an ext4-only host such as the production `/data`, use
  two preallocated loop-mounted XFS images. No change to the data, journal or
  quota code; the runner only learns to report that its roots are loop-backed.
  Migration from existing XFS hosts is a no-op.
- **Fallback: native ext4 project quota** (section 3.1) for hosts whose
  administrator will not accept loop images and can convert or create an ext4
  filesystem with `quota,project` offline. It needs the code changes listed in
  section 3.1 and its own native evidence.

| Property | Primary (XFS volume or loop image) | Fallback (ext4 project quota) |
| --- | --- | --- |
| Code change in the data layer | None | Profile, UUID, footprint, journal prefix, root-writer helper |
| Root requirement | One-time setup (image, loop, mount) | One-time offline `tune2fs` or `mkfs` |
| Crash consistency | XFS journal over loop; verify with a power-cut test | Native ext4 journal plus journaled quota |
| Backup/restore | Platform checkpoints; exclude image files from host backup | Platform checkpoints |
| Moving data from XFS hosts | Unchanged | Checkpoint export/import (doc 168); footprint recalibrated |
| Capacity reporting | `statfs` on the root reports the image filesystem (doc 171 item 8) | `statfs` plus inode table limits |
| Host disk full | Preallocation keeps the image's blocks; host full does not reach the guest | Shared with everything else on `/data` |

## 4. macOS and Windows options

### 4.1 Option M1: the whole stack inside a dedicated Linux VM (recommended)

Run the **Linux** Portal binary, the runner, rootful `dockerd` and the XFS data
and checkpoint disks all inside one Linux VM that the administrator manages.
The host only virtualizes and forwards the Portal's HTTPS port.

- Everything in section 2 holds unchanged: the controller socket, peer
  credentials, socket directories, pinned FDs and project quota all stay inside
  one Linux kernel. This is the same deployment as a Linux host. Doc 146
  already allows "a dedicated rootful Linux host or VM" (line 12).
- **macOS:** any VM manager that gives a full, user-managed Linux guest, such as
  Lima/Colima (`vmType: vz`), UTM, Parallels or VMware Fusion. Docker
  must be installed inside the guest (docker-ce), not used from a desktop
  product. Give the guest a second virtual disk and format it XFS with
  `prjquota` inside the guest. Whether a given manager can format an added disk
  as XFS itself, or the administrator does it inside the guest, must be
  verified per product. Apple Silicon guests are `linux/arm64`, so this depends
  on native arm64 evidence (decision D9) and on arm64 app images (for
  market-monitor, pandas and pyarrow publish aarch64 wheels).
- **Windows:** a Hyper-V VM (Windows Pro, Enterprise or Server), or any other
  full hypervisor, with a second VHDX formatted XFS inside the guest. WSL2 is
  possible but weaker as a server: it runs per signed-in user, its lifecycle and
  idle shutdown depend on the WSL version and `.wslconfig`, it has no native
  "start at boot without a user" service mode, and the Microsoft kernel's XFS,
  quota and cgroup v2 configuration must be verified (a custom kernel is
  possible through `.wslconfig`). `systemd=true` in `/etc/wsl.conf` is required
  for the runner unit.
- **Transport:** none to design. Portal's controller socket stays a Unix socket
  inside the guest.
- **What weakens:** a host administrator can read or modify the VM disk images,
  the same trust as host root on Linux. A laptop that sleeps misses schedules
  (market-monitor's 21:00 job); the platform's background scheduler must catch
  up after a missed window, and the guest clock must resync after sleep. The
  forwarded Portal port is a new exposure on the host.
- **Licensing note (factual):** none of M1 needs Docker Desktop. If Docker
  Desktop is used anyway, its subscription terms require a paid plan for
  commercial use in organisations above the small-business threshold (stated
  by Docker as 250 or more employees or more than USD 10 million annual revenue
  at the time of writing; verify current terms). OrbStack also requires a paid
  licence for commercial use. Lima, Colima, Podman Desktop and Rancher Desktop
  are open source.

### 4.2 Option M3: the runner on the macOS or Windows host using a desktop container product

Docker Desktop, OrbStack, Colima, Podman Desktop and Rancher Desktop all run
containers in a Linux VM and expose a forwarded engine socket on the host.

- The runner is Linux-only (section 1.2) and its peer check refuses a daemon it
  cannot see as a local `dockerd` (`daemon_peer_linux.go:29-34`).
- Bind mounts cross the VM boundary through virtiofs, gRPC FUSE or 9p. Ownership
  and modes are emulated. Unix sockets created on one side of a shared folder
  are generally not connectable from the other (the products special-case their
  own engine and SSH-agent sockets). `fsync` durability is mediated by the
  file-sharing layer. inotify is forwarded approximately. Many-small-file I/O is
  much slower than on a VM-local disk. Verify on a real host per product and
  mode.
- APFS has volume-level quotas (an APFS volume can carry a quota size) but no
  per-directory project quota. NTFS has per-user quotas, and directory quotas
  only through File Server Resource Manager on Windows Server. Neither is
  reachable from the Linux containers.
- A desktop product's own VM disk limit (for example Docker Desktop's
  configurable virtual disk limit on macOS, or the WSL2 VHDX maximum on Windows)
  is one shared ceiling for all containers and images, not a per-app quota.
- Docker Desktop's Enhanced Container Isolation runs containers in user
  namespaces, which preflight refuses (`unmapped_user_profile`,
  `preflight.go:152`).

**Not supported.** Making it work would mean rewriting the isolation model
(sockets, pins, ownership, quota) for a VM the runner does not control.

### 4.3 Option M2: Portal on the host, runner in a VM, over a network transport

The darwin or windows Portal binary would talk to a runner inside a Linux VM
over TCP, vsock (Virtualization.framework's virtio socket on macOS, Hyper-V
sockets on Windows) or a relayed named pipe. What it would need:

- Porting the 24 `_linux.go` controller endpoints in `internal/app` and the
  transport client (`transport_linux.go:486-562`) to a non-Unix transport.
- A replacement for peer credentials. TCP and vsock carry no UID, and vsock
  carries only a context ID. The obvious design is mutual TLS with a pinned
  runner key from enrollment, plus signed replies, because today's reply is an
  unsigned hash (`transport_linux.go:529-531`).
- An enrollment, revocation and node-authority design, which the architecture
  explicitly defers (`01-architecture.md:66-68`).

It gives the administrator nothing M1 does not (the Portal UI is the same) and
it widens the attack surface. **Not recommended now.** If a remote runner is
ever needed for other reasons (one Portal, several runner hosts), it should be
its own design and would then also cover this case.

## 5. Per-platform matrix

"Hard" means a kernel-enforced byte and inode limit that the app cannot exceed.
Network isolation means `network=none` plus broker-only egress (doc 66,
doc 175). Credential custody means secrets stay in Portal custody and reach the
runner only as reviewed bindings (docs 150, 176).

| Platform and arrangement | Status | Isolation | Quota | Network isolation | Credential custody | Acceptance needed |
| --- | --- | --- | --- | --- | --- | --- |
| Linux amd64, XFS partition or LV | Supported (current profile) | Full doc 66 policy | Hard (XFS project) | Yes | Portal | Existing evidence (docs 28, 46, 49, 146) |
| Linux arm64, XFS partition or LV | Supported once arm64 evidence exists | Full | Hard | Yes | Portal | Run docs 28/46/49/146 natively on an arm64 runner |
| Linux ext4 host, loop-mounted XFS images | **Supported (recommended)** | Full | Hard | Yes | Portal | docs 28 and 146 workflows on an ext4 host with loop images; host-full test; power-cut test |
| Linux ext4 with `quota,project`, native adapter | Supported after the ext4 adapter ships | Full | Hard (generic quota) | Yes | Portal | ext4 version of doc 28 (bytes, inodes, unlinked-open FD, remount, raise limit); root-writer test |
| Linux ext4 without project quota and no loop image | Not supported for backends (static apps only) | n/a | None | n/a | n/a | Preflight refusal test |
| Linux btrfs or ZFS host | Supported through a loop image (btrfs, `+C`) or XFS on a zvol | Full | Hard | Yes | Portal | Same as the loop row |
| Linux rootless Docker | Not supported (preflight `rootful_profile`) | n/a | n/a | n/a | n/a | Unchanged |
| macOS amd64/arm64, Portal + runner in a Linux VM (M1) | **Supported with acknowledged risk** | Full inside the guest; host admin trusted | Hard (XFS disk in the guest) | Yes inside the guest | Portal in the guest | Linux rows inside a reference guest (arm64 for Apple Silicon); sleep/resume schedule test; port-forward exposure check |
| Windows amd64/arm64, Portal + runner in a Hyper-V or other full VM (M1) | **Supported with acknowledged risk** | Full inside the guest | Hard | Yes | Portal in the guest | As macOS; boot-without-user test |
| Windows, Portal + runner in a WSL2 distribution | Supported with acknowledged risk only after the WSL kernel passes preflight and doc 28; evaluation use | Full inside the guest if the kernel passes | Hard if XFS and quota are present | Yes | Portal in the guest | Kernel feature check; lifecycle test (sign-out, idle) |
| macOS/Windows native Portal, runner in a VM (M2) | Not supported | n/a | n/a | n/a | n/a | Needs the remote-runner design |
| macOS/Windows with Docker Desktop, OrbStack, Colima engine socket, Podman or Rancher Desktop (M3) | Not supported | n/a | n/a | n/a | n/a | Unchanged refusal |
| macOS/Windows native Portal, static apps | Supported (today) | Frontend sandbox only | n/a | Frontend CSP | Portal | Existing |

**What the administrator must explicitly acknowledge for the M1 rows:** (1) the
host's administrators can read and alter the VM, its disks and therefore all app
data and Portal secrets; (2) the VM must run continuously for scheduled work,
and a sleeping or shut-down host defers jobs; (3) the forwarded port exposes
Portal on the host's network; (4) the VM's Linux, kernel and Docker are
patched by the administrator, not by the host OS; (5) any desktop container
product's licence terms are the administrator's responsibility.

## 6. Recommendation

1. **Linux:** support ext4 hosts now by putting the runner's two roots on
   preallocated, loop-mounted XFS images (or a real XFS partition or LV). Ship
   the operator recipe and an acceptance workflow, no data-layer code. Build the
   native ext4 adapter only if a host appears that cannot accept a loop image.
2. **macOS and Windows:** support backend apps by running the Linux build of
   Portal and the runner together inside an administrator-managed Linux VM
   (M1), labelled "supported with acknowledged risk". The darwin and windows
   Portal binaries stay static-only, and the handbook says so.
3. Do not support desktop container products as the runner's engine (M3), do
   not build a host-to-VM transport now (M2), and do not offer a soft quota.

For market-monitor this means production `/data` (ext4) gets two XFS images.
For example, an 8 GiB data image holds a 3 GiB app quota with room for a second
app, and an 8 GiB checkpoint image holds the recovery checkpoints. No code
change is needed in the data layer before the app can be installed. Its other
blockers are doc 175 (upstream hosts, see section 1.4) and doc 183 (Dify
access).

## 7. Implementation outline (no code in this document)

| Phase | Content | Rough size |
| --- | --- | --- |
| P0 | Operator recipe in `deploy/app-runner/`: image creation (`fallocate`), `losetup --direct-io=on`, XFS `mkfs` with project quota, fstab or mount units, `RequiresMountsFor`, growth and repair procedures, backup exclusion. Runner preflight reports whether each root is loop-backed and its backing filesystem (read from `/sys/block/loopN/loop/backing_file`, diagnostic only). | 1-2 PRs, small |
| P1 | Acceptance: a workflow variant of `app-runner-probe.yml` and the doc 28 probe on an ext4 runner disk with loop images (already the soak's layout), plus a host-full test (fill the outer ext4 and show the preallocated image is unaffected) and a scripted crash test (kill the VM or loop device mid-write, then `xfs_repair -n` clean, and the namespace still quota-constrained). | 1-2 PRs, small-medium |
| P2 | Release the `app-runner` binary for `linux/amd64` and `linux/arm64` with the same signing and digest flow as Portal. Native arm64 evidence for docs 28/46/49/146 on a GitHub arm64 Ubuntu runner. | 2 PRs, medium |
| P3 | VM appliance guide for macOS (one reference manager) and Windows (Hyper-V), with a cloud-init or provisioning script for the guest, and the handbook change ("backend apps on macOS/Windows run inside a Linux VM"). Acceptance is manual on owner hardware: hosted macOS CI runners do not offer nested virtualization on Apple Silicon. | 1-2 PRs plus a manual run |
| P4 (optional) | Native ext4 adapter (section 3.1): profile selection, UUID ioctl, footprint calibration, `ext4-project/v1/` journal prefix, root-writer helper without `CAP_SYS_RESOURCE`, capability query, ext4 native probe workflow. | 4-6 PRs, medium-large |
| Deferred | Remote runner transport (M2), only with its own design. | Large |

## 8. Test plan (these must fail first where they describe new behaviour)

1. Preflight on an ext4 data root reports the profile and refuses
   (today: `ErrUnsafe` at `data_linux.go:111`), and reports `supported` on a
   loop-mounted XFS image on the same host.
2. Doc 28's workload table on the loop image: byte, inode, unlinked-open FD,
   independent project, remount, raise limit, closed unlinked FD.
3. Host-full: fill the outer ext4; the app's quota denial is still the native
   project denial, and the runner and the XFS filesystem stay healthy.
4. Sparse-image negative control (development only): the same fill on a sparse
   image produces I/O errors. This documents why preallocation is mandatory.
5. Crash: abrupt power-off of a test VM during a namespace write; after reboot
   the image mounts, `xfs_repair -n` is clean and the quota still holds.
6. (P4) The ext4 version of 2, plus a root restore write that would exceed the
   quota is refused by the import pre-check and by the helper.
7. (M1) The full doc 146 service workflow inside a macOS arm64 guest and a
   Hyper-V guest; a missed 21:00 schedule after host sleep is caught up once.
8. M3 refusal stays: preflight against a Docker Desktop engine socket reports
   the daemon peer refusal.

## 9. Owner decisions (2026-11-20)

The owner decided every question below on 2026-11-20 and accepted the recommended answer for each (D1 to D11).


D1. Linux primary for ext4 hosts. Decided: XFS data and checkpoint roots on two preallocated loop-mounted images (or a real XFS partition or LV where one exists); no data-layer code change.

D2. Build the native ext4 project-quota adapter. Decided: yes as the fallback, phase P4, not blocking market-monitor; build it only when a host cannot use D1.

D3. Offer a soft (scanning) quota mode, even with an acknowledgement. Decided: no.

D4. Model for backend apps on macOS and Windows. Decided: M1, Linux Portal + runner inside an administrator-managed Linux VM, "supported with acknowledged risk"; native darwin/windows Portal stays static-only.

D5. Build a host-to-VM transport (M2) now. Decided: no; only as part of a future remote-runner design.

D6. Accept Docker Desktop, OrbStack, Colima's engine socket, Podman or Rancher Desktop as the runner's engine. Decided: no.

D7. Windows reference hypervisor. Decided: Hyper-V (or another full VM); WSL2 only for evaluation, after its kernel passes preflight and doc 28.

D8. Ship runner binaries and a VM image. Decided: release `app-runner` for `linux/amd64` and `linux/arm64`; ship a documented provisioning recipe, not a prebuilt VM image.

D9. Require native arm64 evidence before supporting Apple Silicon hosts. Decided: yes.

D10. Initial market-monitor quota. Decided: 3 GiB bytes and 100,000 inodes, reviewed after one year of growth (about 300 MB per year, `pipeline/README.md:57`).

D11. Preallocated image sizes for production. Decided: 8 GiB data, 8 GiB checkpoint (doc 167's reference budget), grown online when capacity reporting shows 80%.

## 10. Implementation notes (P0 to P2)

The document has no sections 10 to 12 before this one; these notes are section
10. Nothing here changes a schema, a contract, the data layer, the journal or the
managed runtime profile.

### 10.1 What shipped

| Phase | Shipped | Where |
| --- | --- | --- |
| P0 | `app-runner provision-xfs-images` (D1): validates the request, inspects both volumes before changing either, creates preallocated images, attaches with `losetup --direct-io=on`, runs `mkfs.xfs` without `-f`, mounts `prjquota,nodev,nosuid,noexec`, runs the runner's own data-root checks (`data.Open` and `Identity`) on `<mount>/root`, and prints the `service.json` roots, a `capacity.disk_bytes` and fstab or systemd mount lines. It edits nothing under `/etc`, refuses foreign content and foreign mounts, never unmounts or detaches what it did not create in the same run, rolls back only what the run made, and only re-verifies when run again. `--dry-run` reports the plan. | `cmd/app-runner/provision.go`, `internal/apprunner/provision/` |
| P0 | Startup refusal: a data or checkpoint root on any filesystem other than XFS stops `serve` with an operator message naming the root's role, the filesystem kind and the recipe, and never a path. Each root's kind is logged at start, including whether it is loop-backed and the kind of the filesystem holding the backing file (read from `/sys/dev/block/7:N/loop/backing_file`, diagnostic only). A root that fails the data-root checks while on XFS now says so. No wire field, capability query or profile field was added; the profile comparison of doc 175 is untouched. | `internal/apprunner/service/roots_linux.go`, `internal/apprunner/rootfs/` |
| P0 | Handbook sections 4.1 to 4.4: the runner binary, the XFS volume recipe with growth, repair and backup, the per-platform matrix of section 5, and the Linux VM recipe for macOS and Windows (D4, D7). | `07-handbook.md` |
| P1 | A workflow job, `provision-xfs-images`, on an ext4 filesystem with no reserved blocks: dry run changes nothing, foreign content is refused, preallocation and direct I/O are checked, a rerun is idempotent, the outer filesystem is filled and the data image is still writable while a project limit still denies with the native error, `xfs_repair -n` is clean, and a reattached image keeps its limit. | `.github/workflows/app-runner-probe.yml` |
| P2 | `app-runner` for `linux/amd64` and `linux/arm64` (D8) in `release.yml`: its own `build-runner` job (the six-platform matrix and the six portal archives are unchanged), the same provenance check (`check-release-build.sh` takes an optional command name), archives named `app-runner_<tag>_linux_<arch>.tar.gz` in the same `SHA256SUMS.txt`, an arm64 command-surface smoke before the tag, and a pre-merge compile in `test.yml`'s `release-targets` step. | `.github/workflows/release.yml`, `test.yml`, `scripts/check-release-build.sh` |
| P2 | Native arm64 evidence jobs (D9): the doc 146 service workflow and the container probes run on `ubuntu-24.04-arm` (a matrix leg of `inspect-disposable-daemon`, whose amd64 check name is unchanged), the provisioning job runs on both architectures, and `native-quota-evidence-arm64` runs the doc 28, 46 and 49 native workload on an owned loop volume. | `.github/workflows/app-runner-probe.yml` |

### 10.2 Findings and deviations

- **D11's 8 GiB image does not satisfy the default capacity budget.** The runner
  is not ready when `capacity.disk_bytes` (default 8 GiB) exceeds the data
  volume's `statfs` size, or when the checkpoint volume is smaller than two
  thirds of it (`capacity.go:173`, `:180`). XFS reserves part of an 8 GiB image
  for its log, so an 8 GiB data image reports a little under 8 GiB. The
  provisioning command therefore prints a `capacity.disk_bytes` (7 GiB for the
  production images) and the handbook tells the operator to set it. The owner
  may prefer larger images (for example 9 GiB data) instead; the sizes are
  command arguments.
- **Loop-image hazards found in review.** A discard through a loop device punches
  holes in the preallocated image, so the command formats with `mkfs.xfs -K`,
  re-checks the allocation after formatting, and the printed fstab lines carry
  `X-fstrim.notrim` (a mount unit cannot, so the handbook says to disable
  `fstrim.timer` for those mounts) and `nofail`. An existing sparse image is
  re-preallocated in place while unmounted, never deleted. The minimum image is
  2 GiB, so the suggested `disk_bytes` is always at least 1 GiB. Every directory
  above an image or mount point must be root-owned and not group- or
  world-writable (a root-owned sticky directory such as `/tmp` is allowed), the
  backing filesystem must be ext4, XFS or btrfs, and a failed rollback step stops
  the rollback.
- The recipe is a command, not only a document in `deploy/app-runner/`, which is
  what P0 listed; `deploy/app-runner/` is unchanged and the handbook carries the
  procedure.
- **P1 is partial.** The host-full check is done as described. Not done: the
  sparse-image negative control (test 4), the power-cut crash test (test 5, it
  needs a VM), and running the doc 28 workload table against the provisioned
  images themselves: `project_quota_probe.py` creates and owns its own loop
  image, so it is run on that image (and on arm64) rather than refactored to
  accept an external mount. The new job's quota check is a single byte limit.
- **The new workflow jobs have not run yet.** Writing them needed root, loop
  devices and `xfsprogs`, which the development host does not allow; they were
  checked with `actionlint` and a syntax check of every run block only. The first
  dispatch may need adjustments. The arm64 legs in particular are untested:
  until `native-quota-evidence-arm64` and the arm64 legs have passed, Apple
  Silicon guests stay unsupported (D9).
- The release carries a `SHA256SUMS.txt` entry per archive and the embedded
  provenance check; like the Portal archives there is no separate signature or
  attestation, because the existing pipeline has none. `app-runner` has no
  version stamp (it has no `version` command); the archive name and the build's
  embedded commit identify it.

### 10.3 Remaining

- **P3:** the manual acceptance of the VM recipe on a macOS arm64 guest and a
  Hyper-V guest (the handbook text is written, untested), plus a cloud-init or
  provisioning script for the guest if the owner wants one.
- **P1 leftovers:** sparse negative control, power-cut test, doc 28 table on a
  provisioned image.
- **P2 leftover:** passing arm64 evidence (the jobs exist).
- **P4 (optional, not started):** native ext4 adapter (D2).
