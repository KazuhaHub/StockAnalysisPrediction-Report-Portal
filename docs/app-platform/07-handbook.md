# Developer and operator handbook specification

Status: required workflows for the future platform. The v2 tools, product pages
and endpoints described here do not exist yet. Do not paste proposed commands
into production. [Implementation work packages](04-implementation.md) distinguish
existing repository checks from future deliverables.

## 1. Where work belongs

| Work | Repository / owner |
| --- | --- |
| Generic host, app shell, account/OU policy and platform APIs | Portal repository |
| First runner/control/broker adapter | Portal repository, separately built executable/image |
| SDK, templates, package/publish validation | Shared Apps repository |
| Individual business apps and reference samples | Shared Apps repository under `apps/<id>` |
| Production data, runtime keys and deployment receipts | Protected server storage, never Git |

Do not copy a business app into Portal to make an API integration easier. Do not
change the core analysis queue to run app tasks. Adding a new app after v2 release
must require only a compatible package and its declared permissions/configuration.

## 2. Developer walkthrough to implement and verify

The eventual CLI examples below are a specification for P11. Their names can be
changed at D3, but the complete workflow and outputs must remain documented and
tested. Current `scripts/create-app.py` creates a legacy SDK frontend and is not
this v2 managed-app generator.

```text
portal-app create notes-sample --template static
portal-app create digest-sample --template managed
portal-app dev
portal-app validate
portal-app test
portal-app pack --output dist/
portal-app inspect dist/<app>-<version>.portalapp
```

Expected developer steps:

1. Choose static or managed. The generated project includes the manifest schema,
   typed locales, frontend root, tests, package config and a compatible SDK pin.
   Managed mode adds a backend Unix-socket entrypoint, safe health route, explicit
   data schema and example idempotent task/event handlers.
2. Run the development host. It supplies a fake authenticated viewer, current
   OU ancestry, themes/locales/timezone and denied/allowed capability scenarios.
   It never copies the developer's production cookie, credentials or database.
3. Describe routes and page metadata through SDK APIs. Use host title/actions
   and navigation; render business content only. All user copy uses typed locales.
4. Add only required capabilities. Specify optional feature behavior and network
   destinations. Do not request raw credential read when broker use is sufficient.
5. Test normal and denied cases, unsaved drafts, keyboard/focus, narrow layout,
   background retry and revision/identity changes. Do not test only component render.
6. Build a complete package. Its backend includes runtime libraries and other
   build dependencies. It cannot download arbitrary executable dependencies at
   installation or startup outside the signed artifact contract.
7. Inspect without executing. Verify compatibility, routes, capabilities, digests,
   resource defaults and frontend/backend identity.
8. Install through the normal local acceptance market/permission flow. Avoid a
   special development endpoint that bypasses publication/authorization checks.
9. Publish using the guarded signing pipeline; externally download and verify the
   actual artifacts before adding the version to the canonical market.

The generator integration test must perform these steps using a clean checkout,
not depend on globally installed tools or hidden symlinks. Fixture secrets must
be unmistakably disposable and cannot be allowed to reach real external services.

## 3. Developer checklist for a first app

- Manifest and signed artifact set match the code actually served/executed.
- No parent DOM/cookie/local storage access and no private host imports.
- Shared root, theme/language/timezone and a registered route.
- Required/optional capabilities and denied-state explanations.
- Foreground writes happen only after deliberate actions; rerender/locale changes
  cannot repeat them. SDK retries reads only within documented rules.
- Background handlers have idempotency, deadlines and safe classifications.
- Credentials absent from logs, errors, frontend bundles and app metadata.
- Explicit schema compatibility and migration/rollback fixtures.
- Stop/uninstall/recovery behavior tested before publication.

## 4. One-time runtime administrator setup

This procedure must be delivered as a versioned tested deployment guide in P04/
P13. It is not a request to modify the current server.

1. Confirm a supported Linux runtime profile and sufficient isolated disk/resources.
   Record architecture, Docker mode, kernel and quota support. Unsupported hosts
   remain static-only until a supported adapter is installed. Sections 4.2 to
   4.4 give the XFS volume recipe for ext4 hosts, the per-platform support
   matrix, and the Linux VM recipe for macOS and Windows.
2. Obtain the platform-compatible runner from the official verified release.
   Verify signature/digest and compatibility before execution.
3. Prepare owner-only runtime directories. Proposed logical roots are control,
   journal, verified artifacts, installation data, sockets and recovery. Resolve
   final filesystem ownership/path layout in D1/D2, not from an app manifest.
4. Provision the runner's restricted control socket and infrastructure trust keys
   outside Git. Mount only the required control interface into Portal. Never mount
   the Docker socket or application data roots into Portal/app frontends.
5. Start the runner with explicit host-wide CPU/memory/disk reservations and app
   quotas. Ensure the Portal process, database and OS retain measured headroom.
6. Enroll that deployment through the reviewed local bootstrap mechanism; this
   is platform infrastructure pairing, performed once, not per-app pairing.
7. In the management UI inspect runtime compatibility/health and run a harmless
   isolated self-test. Confirm no direct app egress or host port is possible.
8. Configure trusted publishers and applicable outbound/private-network policy.
   Catalogue selection alone never grants executable-code trust.
9. Create and restore an isolated fixture recovery set, including required keys.
   Configure an approved off-host backup destination separately.
10. Enable managed installation after verification. From then on, app admins use
    Portal to install/configure/update/stop applications without shell access.

Reference deployment files must include exact supported profiles, backup paths,
ownership, health checks, safe log limits and how to undo bootstrap. If the runner
needs a privilege the profile does not provide, fail with a clear reason rather
than suggesting broad permissions as a routine fix.

### 4.1 The runner binary

Official releases carry `app-runner_<version>_linux_amd64.tar.gz` and
`app-runner_<version>_linux_arm64.tar.gz` beside the Portal archives. They are
listed in the same `SHA256SUMS.txt`; verify with `sha256sum -c` before running
anything. Each archive holds `app-runner`, the systemd unit, the example
configuration and the licence files. There is no `app-runner` for macOS or
Windows and no prebuilt VM image: the VM recipe in section 4.3 is the supported
way to run backend apps on those systems. Install the binary as
`/opt/portal-app-runner/app-runner` (the path the unit uses).

### 4.2 XFS volumes for backend apps

The runner enforces hard byte and inode limits with XFS project quota, and it
opens exactly two roots: a data root and a distinct checkpoint root. Both must
be XFS mounted `prjquota,nodev,nosuid,noexec`, root-owned and mode `0700`. If
either is anything else the runner refuses to start with a message that names
the filesystem and this section, and backend apps cannot be installed. Static
apps never depend on the runner and are unaffected.

- **The host already has an XFS partition or LV:** mount two volumes on it as
  above and skip to the configuration step.
- **The host is ext4 (the production `/data` is):** do not convert it. Provision
  two preallocated, loop-mounted XFS images on it with the runner's own command.
  No change to the data layer is involved, and the images keep the quota, journal
  and identity code on its tested filesystem.
- **btrfs or ZFS hosts:** the command works on btrfs (it sets `chattr +C` before
  preallocating). For ZFS, create two zvols and format them XFS instead; the
  command refuses a ZFS directory. Native ext4 project quota is a later,
  optional adapter and is not available.

Production sizing is 8 GiB for the data image and 8 GiB for the checkpoint
image. Run once, as root, after creating the image directory on the host disk
and the parent of the mount points. Every directory from each path up to `/` must
be owned by root and not group- or world-writable (a root-owned sticky directory
such as `/tmp` is accepted), with no symbolic link in the path:

```sh
install -d -m 0700 /data/portal-runner
install -d -m 0755 /srv/portal-runner
/opt/portal-app-runner/app-runner provision-xfs-images --dry-run \
  --data-image /data/portal-runner/data.img --data-size 8G \
  --checkpoint-image /data/portal-runner/checkpoint.img --checkpoint-size 8G \
  --mount-data /srv/portal-runner/data --mount-checkpoint /srv/portal-runner/checkpoint
# Review the plan, then run the same command without --dry-run.
```

What it does, and what it will not do:

1. Validates the request (absolute clean paths, sizes from 2G, nothing nested
   inside anything else), then inspects both volumes before changing either.
2. Creates each image with `fallocate`, never sparse. A sparse image turns a
   full host disk into I/O errors inside XFS, which shuts the whole runner down
   instead of failing one app's write. An existing image that has become sparse
   is reported as degraded and, while it is unmounted, re-preallocated in place
   with `fallocate` (written data is kept); it is never deleted, and if it is
   mounted the command only warns and asks you to unmount and run it again.
3. Attaches it with `losetup --direct-io=on`, runs `mkfs.xfs -K` (no discard,
   and without `-f`) and checks the image is still fully allocated, because a
   discard on a loop device punches holes in the backing file. It then mounts it `-t xfs -o prjquota,nodev,nosuid,noexec`, sets the mount point to
   `0700` and creates the `root` directory inside.
4. Runs the runner's own data-root checks on that `root` directory (the same
   code `serve` runs: XFS magic, mount flags, ownership and mode, project quota
   enforcement, root identity). A failure undoes what this run made.
5. Prints the `service.json` roots, a suggested `capacity.disk_bytes`, and the
   `/etc/fstab` lines or systemd mount units plus a `RequiresMountsFor=` drop-in
   for `portal-app-runner.service`. It does not edit anything under `/etc`;
   install one of the printed alternatives yourself.
6. Refuses, changing nothing, when an image path holds anything that is not a
   root-owned `0600` preallocated XFS image of the requested size, when a mount
   point is non-empty or already mounted by something else, when an image is
   already attached or mounted elsewhere, or when the image directory is not on
   ext4, XFS or btrfs. A failed rollback step stops the rollback, so a mount that
   would not unmount is never followed by detaching or deleting what is under it. It never unmounts or detaches anything it did not create
   in the same run. Running it again after success only re-verifies; after a
   reboot without persistence it re-attaches and mounts the existing images
   without formatting.

Configure the runner with the printed roots: `data_root` is
`/srv/portal-runner/data/root` and `checkpoint_root` is
`/srv/portal-runner/checkpoint/root`. **Set `capacity.disk_bytes` to the printed
value.** The runner is not ready when `disk_bytes` (default 8 GiB) is larger than
the data volume's usable size, or when the checkpoint volume holds less than two
thirds of it, and XFS reserves part of an image for its log, so an 8 GiB image
reports slightly less than 8 GiB (the command prints 7 GiB for the 8 GiB
production images). The example `service.example.json` shows 8 GiB for a
volume that is larger than that.

Operations:

- **Persistence:** install the printed fstab lines (preferred) or mount units, and
  the drop-in, then `systemctl daemon-reload`. The fstab lines carry `nofail`, so a
  missing image does not fail the boot (the runner's `RequiresMountsFor=` still
  keeps it from starting), and `X-fstrim.notrim`, because Ubuntu's `fstrim.timer`
  would otherwise trim the loop-mounted XFS and punch holes in the preallocated
  image. A mount unit cannot carry that option; with units, also disable or
  override `fstrim.timer` for these mounts (verify on the host). After a reboot
  `mount(8)` attaches the loop device itself, probably without direct I/O (check
  on the host); that changes performance only.
- **Growth** (one-way; XFS cannot shrink): with the volume mounted, grow the
  file (`fallocate -l <new size> <image>`), refresh the loop device
  (`losetup -c <loop device>`, found with `losetup -j <image>`), then
  `xfs_growfs <mount point>`. Raise `capacity.disk_bytes` and restart the runner.
  Plan growth when capacity reporting shows 80% used.
- **Repair:** stop the runner, unmount, then `xfs_repair <image or its loop
  device>`. The host's `e2fsck` checks only the outer filesystem.
- **Backup:** never copy a live image; it is not crash-consistent. The platform's
  own recovery checkpoints and restore bundles are the supported path. Exclude
  both image files from host backups.
- **Host disk full:** the images keep their reserved blocks, so a full outer
  filesystem does not reach an app's writes; the app still meets its own quota
  as `ENOSPC`. Do not rely on this for the rest of the host.

### 4.3 Platform support

"Hard" means a kernel-enforced byte and inode limit. Native Portal here means
the Portal binary of that operating system.

| Platform and arrangement | Backend apps | Quota |
| --- | --- | --- |
| Linux amd64, XFS partition or LV | Supported | Hard (XFS project quota) |
| Linux arm64, XFS partition or LV | Supported once native arm64 evidence has passed | Hard |
| Linux ext4 host, two loop-mounted XFS images (section 4.2) | Supported (recommended for ext4) | Hard |
| Linux btrfs host (images with `+C`) or ZFS host (XFS on a zvol) | Supported through the same image or zvol | Hard |
| Linux ext4 without project quota and no XFS volume | Refused with an operator message; static apps only | None |
| Linux with native ext4 project quota | Not available; a later optional adapter | n/a |
| Linux rootless Docker | Not supported | n/a |
| macOS or Windows: Linux Portal and runner inside an administrator-managed Linux VM (section 4.4) | Supported with acknowledged risk; Apple Silicon only after native arm64 evidence | Hard (XFS disk in the VM) |
| Windows: Portal and runner in a WSL2 distribution | Evaluation only, after the WSL kernel passes the runner's preflight | Hard if the kernel has XFS and quota |
| macOS or Windows native Portal driving a runner in a VM | Not supported | n/a |
| macOS or Windows with Docker Desktop, OrbStack, Colima's socket, Podman or Rancher Desktop as the engine | Not supported | n/a |
| macOS or Windows native Portal, static apps | Supported | Frontend sandbox only |

There is no soft (scanning) quota mode and none is planned: a periodic scan
cannot bound a writer between scans.

### 4.4 Backend apps on macOS and Windows: the Linux VM recipe

The darwin and windows Portal binaries run static apps only. To run backend apps
on a Mac or a Windows machine, run the **Linux** Portal build, the runner,
rootful Docker and the XFS volumes together inside one Linux VM that the
administrator manages. Everything in sections 4 to 4.2 then applies unchanged
inside the guest; the host only runs the VM and forwards Portal's HTTPS port.
This is supported with acknowledged risk. Before choosing it, the administrator
must accept that:

1. administrators of the host can read and alter the VM and its disks, and so all
   app data and Portal secrets;
2. the VM must run continuously for scheduled work, and a sleeping or shut-down
   host defers jobs (the guest clock must resync after sleep);
3. the forwarded port exposes Portal on the host's network;
4. the guest's Linux, kernel and Docker are patched by the administrator, not by
   the host's operating system;
5. the licence terms of any desktop virtualization or container product used are
   the administrator's responsibility. The recipe needs no Docker Desktop.

Guest requirements: a full Linux distribution with systemd, a kernel of 5.14 or
later (the quota calls need it), cgroup v2, docker-ce installed **inside the
guest** (the runner refuses a daemon it cannot see as a local `dockerd`, which is
why Docker Desktop and its engine sockets are not supported), the guest's own
architecture matching the Portal and runner archives (`linux/arm64` on Apple
Silicon, which needs native arm64 evidence first), and at least the CPUs and
memory your `minimum_reported_*` and capacity settings declare.

1. **Create the VM.** macOS: any manager that gives a full, user-managed Linux
   guest, for example Lima or Colima with `vmType: vz`, UTM, Parallels or VMware
   Fusion. Windows: a Hyper-V VM (Windows Pro, Enterprise or Server), or another
   full hypervisor. Windows' reference is Hyper-V; WSL2 is for evaluation only
   because it runs per signed-in user, its lifecycle and idle shutdown depend on
   the WSL version and `.wslconfig`, it has no start-at-boot-without-a-user mode,
   and its kernel's XFS, quota and cgroup v2 support must be verified (it also
   needs `systemd=true` in `/etc/wsl.conf` for the runner unit).
2. **Give the guest two extra virtual disks** (or two LVs), one for app data and one
   for checkpoints, each at least as large as section 4.2 recommends. This is the
   VM equivalent of the loop images: the disk is a real XFS volume inside the
   guest. A host with a thick-provisioned (fixed) virtual disk avoids the sparse
   image failure mode; prefer fixed over dynamic disks.
3. **Format and mount them inside the guest** (as root, on disks you confirmed
   empty with `lsblk`), then use the `fstab` lines below with the disks' UUIDs
   from `blkid`:
   ```sh
   mkfs.xfs /dev/vdb && mkfs.xfs /dev/vdc
   install -d -m 0700 /srv/portal-runner/data /srv/portal-runner/checkpoint
   # /etc/fstab:
   # UUID=<data uuid>       /srv/portal-runner/data       xfs prjquota,nodev,nosuid,noexec 0 0
   # UUID=<checkpoint uuid> /srv/portal-runner/checkpoint xfs prjquota,nodev,nosuid,noexec 0 0
   mount -a
   chmod 0700 /srv/portal-runner/data /srv/portal-runner/checkpoint
   install -d -m 0700 /srv/portal-runner/data/root /srv/portal-runner/checkpoint/root
   ```
   Device names differ per hypervisor; always use the UUIDs.
4. **Install Docker, the runner and Portal in the guest** as sections 4 to 4.1
   describe, using the `linux` archives that match the guest's architecture.
   Set `data_root` and `checkpoint_root` to the `root` directories above and
   `capacity.disk_bytes` to what the volumes can honour (section 4.2).
5. **Start on boot.** Enable the Portal and runner units and make the VM start with
   the host (a Hyper-V automatic start action, Lima's start-at-login, or the
   manager's equivalent). Then run the inspection and self-test steps of
   section 4; after a host sleep, confirm that a missed scheduled window runs once.
6. **Forward only Portal's HTTPS port** to the host, and restrict it to the
   interfaces that need it. Never forward the Docker socket, the runner's control
   socket or the SSH port beyond what administration requires.

This recipe has not yet had its manual acceptance run on macOS or Hyper-V
hardware (doc 182 phase P3); until it has, treat the steps as a reference to
adapt and verify, not as a tested procedure.

### 4.5 Pinned proxy destinations (doc 186)

Use this when a backend app must reach a service that is served on the same host, such as market-monitor's quotes API behind aaPanel nginx and an frp tunnel, through the reviewed forward proxy (`network.proxy`, doc 184). The broker refuses loopback, private and host-interface addresses for ordinary proxy hosts, and that does not change. A **pin** is the one narrow exception: one exact name, always reached on port 443 by the app, that the broker dials at one address and port without resolving the name. It takes two keys: you list the destination in the runner's root-owned configuration (this section), and a Portal administrator selects it in the proxy review of the installation. A pin the operator has not listed is never granted and never dialled.

The procedure changes the host's nginx and the runner's configuration only when the operator runs it. Nothing here is done by Portal or by the runner.

1. **Check the runner runs in the host network namespace.** A pin such as `127.0.0.1:20443` is an address of the runner's own namespace. With the systemd unit shipped in `deploy/app-runner/portal-app-runner.service` it is (no `PrivateNetwork=`, no `NetworkNamespacePath=`). Under Compose the service must use `network_mode: host` and `pid: host`, the configuration file must stay root-owned mode `0600` through a read-only bind mount, and the runner must run as root. The runner enforces the namespace rule itself: with `proxy_pins` set it refuses to start with `proxy_pins_netns` unless it identifies its Docker daemon in its own network namespace, and every Docker request fails if a later connection is not. Do not work around that refusal; fix the deployment.
2. **Add the dedicated nginx listener.** Use `deploy/app-runner/proxy-pin-listener.nginx.example.conf` as the starting point: one `server` block for the name on `127.0.0.1:20443`, a `default_server` block that rejects every other SNI, a `421` for any other host (including an absolute-URI request line), and the fixed name as the upstream `Host`. Never pin the shared `127.0.0.1:443`: through it the app could send any other site's `Host` header and the broker cannot see inside TLS. The certificate is the site's own (Let's Encrypt via DNS-01), because the app verifies it against the name. Keep the file in an include that aaPanel does not manage: aaPanel rewrites `/www/server/panel/vhost/nginx/<site>.conf` when the site is edited in the panel. Which include point your aaPanel keeps across edits is for you to confirm on the host; the probe below is the proof.
3. **Run the acceptance probe, as the operator, on the host.** `sh scripts/probe-proxy-pin-listener.sh --nginx-include <the include file> --upstream-log <the frp upstream log>` (add `--name`, `--other-host`, `--port` or `--address` if yours differ). It sends requests to the loopback listener and reads the include and the socket table; it changes nothing. Every check must pass: the request verifies the certificate; another `Host` answers 421; another SNI is rejected with no certificate; an absolute-URI request line for another site answers 421; the include fixes the upstream `Host` (never `$http_host`) and listens on loopback only; the upstream log shows the fixed name; and exactly one socket listens on the port, `127.0.0.1:20443`. If you cannot read the upstream log, pass `--no-upstream-log` and look at the frp upstream by hand. Do not continue after a failure. **Run the probe again after any aaPanel edit of the site.**
4. **List the destination and restart the runner.** Add the block to `/etc/portal-app-runner/service.json` (`deploy/app-runner/service.proxy-pins.example.json` is the plain example with exactly this block added):

   ```json
   "proxy_pins": [{"host": "quotes.internal.anchan.kazuhahub.com", "address": "127.0.0.1", "port": 20443}]
   ```

   Entries are exact `{host, address, port}` destinations, at most 16, sorted by host then address then port, never a prefix or a wildcard. The runner refuses to start if an address is in the fixed denied set (cloud metadata, link-local, multicast, reserved, IPv4-mapped, translation and tunnelling forms), a name is not a plain lower-case DNS name, a port is outside 1 to 65535, or the list is unsorted or has a duplicate. Loopback is allowed for a pin; RFC 1918, tailnet and bridge addresses are allowed only because you list them here. The change takes effect at the restart. Restarting the runner closes every egress session; Portal opens the next ones on its own.
5. **Review the pin in Portal.** Open the installation's settings, then the proxy panel. The runner now advertises the entry, so the exact host `quotes.internal.anchan.kazuhahub.com` (in the list of approved hosts) offers a select with the destinations you listed for that name. Choose it and review: the dialog shows the pin as added, with a warning that the name will connect to that address on the runner's machine without DNS. Confirm to consume it. The pin shows next to its host as "pinned to 127.0.0.1:20443". If you later remove the entry from `proxy_pins`, the pin stays reviewed but is shown as not permitted by the runner, and the broker refuses tunnels to the name (`address_denied`) until you list it again or remove the pin in a new review.

Upgrading the runner and Portal in either order is safe: the runner keeps answering the old capability query exactly as before, and a new Portal falls back to it when it talks to an older runner. A pinned grant needs a runner that knows pins: after a downgrade, Portal opens the session without the proxy rather than with the proxy minus its pins.

## 5. Everyday administrator procedures

### Install

Open Market, select a compatible app, inspect publisher/permissions/resource/data
requirements, choose optional grants and app-use OUs, then confirm. Follow named
progress stages. Once installed, open the first-use wizard only if business
configuration is required. The wizard never requests an internal backend address,
container command or signing key. If a runtime is absent, explain the one-time
infrastructure requirement before review, with an administrator guide link.

### Update

Review the exact target and changed capabilities/schema/resource effects. Confirm
the recovery point and app-only maintenance impact. Observe the same operation
after page reload. Do not uninstall an app as a normal update technique. If
upgrade fails, use a platform-offered verified recovery path rather than changing
container tags manually or starting an older binary against unknown data.

### Disable and enable

Disable blocks access and new work immediately at the broker and reports runner
shutdown as pending until acknowledged. Enable rechecks the same trusted release
and health, preserving approved settings. Neither action means uninstall.

### Uninstall and data clearing

Review stopped work, revoked integrations and the dedicated table/managed
namespace that will be cleared. For managed apps, completion requires runner
evidence for stop, code removal, namespace deletion and quota cleanup. After
completion the application has no authority or background work. Reinstall starts
with a new installation identity and empty data. Legacy or recovery data that
cannot be cleared inline has a separate `delete_data` confirmation and clearly
states backup-copy coverage.

### Restore

Select a verified compatible recovery set; show possible loss of writes after its
checkpoint. Restore in app maintenance with fenced workers. Review credentials,
grants, destinations, subscriptions and uncertain external deliveries. Resume
individual capabilities deliberately. Restore never sends a notification merely
because a prior queue record exists.

### Raising the quota and importing data

Use this to seed an installed backend app with a prepared tree, for example market-monitor's bar store (doc 185). Never copy files into the namespace by hand: nothing records what was copied, owners and modes are easy to get wrong, `observed_bytes` goes stale and the next checkpoint would be the first to see the data. Import replaces the namespace contents; merging is not supported. Both actions are reviewed on the installation's settings page and audited.

**Raise the quota first, if the tree needs it.** "Raise quota" asks for new byte and inode limits (never lower than the current ones). The review shows current and new limits, the current usage and whether the runner's capacity can hold the larger reservation (the namespace's quota plus two retained checkpoints). If it cannot, the review names the shortfall: grow the images (section 4.2) and review again. The raise is online, with the app running.

1. **Find the namespace.** Open the installation's settings (`GET /api/admin/app-platform/installations/{id}/settings`). The `data.id` is the namespace UUID, `<data_root>/<UUID>` on the runner host. Confirm the quota in the same view is large enough for the tree and its inode count.
2. **Prepare the tree** on the runner host, outside the data root and the checkpoint root. It may hold only regular files and directories: no symbolic links, special files, setuid, setgid or sticky bits, no file named `.portal-checkpoint-manifest.json`, no name over 255 bytes, no path deeper than 64 levels. Hard links are refused until doc 185 phase P2 (pack without them or copy them as separate files). A directory `a` next to a sibling such as `a.txt` is refused: rename one of them. Put `.portal-data-schema` at the root, holding the data format the release expects; without it the archive is format 0 and the review refuses it.
3. **Pack it**, as root: `app-runner pack-import --from /srv/seed --out <checkpoint_root>/.portal-imports/<name>.tar`. It normalises owners to 65532:65532, directories to 0750 and files to 0640, writes into the root-owned 0700 inbox, never overwrites, and prints the SHA-256, size and entry count. Copy the SHA-256.
4. **Review in Portal.** Disable the app first: the import replaces the namespace while the backend is stopped, and an enabled app is refused. Choose "Import data" on its settings page and paste the SHA-256. The review shows the digest, size against the quota, entries against the inode quota and the archive's data format against the release's. It states that the current contents will be replaced and that a pre-import checkpoint is kept. Confirm twice: the review, then the confirmation. The operation stops at `validate` if the runner no longer finds exactly that archive.
5. **Verify.** After the operation completes, the app is enabled again. Check the observed size on the settings page and confirm the app reads the data. The archive is deleted from the inbox only now, after the operation completes; a failed import keeps it for another try, and if the deletion itself fails the import still stands and the archive stays in the inbox until you remove it (the audit log records `app_platform.data_import_archive_remove_failed`). If the import or the start fails, the pre-import checkpoint is restored and the app stays disabled as it was (error `managed_import_data_rolled_back`); if that restore cannot be proven the installation needs repair. Until a restore of that namespace has applied, the runner refuses to start an app whose import failed half-way. A restore to a checkpoint older than the import removes the seed.

Sizing for market-monitor (quota 6 GiB, 100,000 inodes): each checkpoint archive is about 6.2 GiB, so two retained checkpoints need about 12.4 GiB. Provision a checkpoint image of at least 16 GiB, and a data image of 8 GiB (about 7 GiB usable) that holds the 6 GiB plus small apps; grow the data image when capacity reaches 80% (section 4.2).

## 6. Troubleshooting playbook

| Symptom | Inspect first | Safe response |
| --- | --- | --- |
| Managed install unavailable | Runtime health/compatibility and preflight findings | Fix the one-time runtime setup; do not install half the package |
| Stuck install/update | Operation ID, stage and observed runner result | Reconcile/retry same operation, never start duplicate commands |
| App page blank | Platform handshake error, safe console diagnostics, asset integrity | Retry document negotiation after cause is fixed; never relax sandbox |
| Wrong language/theme | Initial presentation and SDK protocol compatibility | Correct shared contract; do not add a second global preference |
| Backend crash loop | Safe readiness/log/resource evidence | Circuit-break and diagnose; keep data, do not grant privilege |
| Network/secret denied | Effective binding/capability revision and fixed destination | Review exact intended grant; do not widen to all networks/secrets |
| Background work stopped | Pause state, lease, handler result and trust revision | Correct policy/config then deliberately resume |
| Old app missing after uninstall | Retained-data record and approved package compatibility | Use supported reinstall/adoption, not manual DB asset edits |
| Restore cannot decrypt | Recovery set/key association | Refuse activation; recover matching protected keys or report key loss |
| Portal restored but runner differs | Controller epoch and checkpoint mismatch | Fence old authority and reconcile, never blindly accept runner state |

Logs expose operation IDs, stages, classified errors and resource statistics.
Sensitive upstream responses, DSNs, bearer links, provider credentials and user
payloads are excluded. A support bundle requires an explicit preview of included
files and destination; diagnostics are not automatically sent to a publisher.

## 7. Release and channel policy

Application versions and artifacts are immutable. The app market may offer a
stable/beta channel selector, but installation resolves one reviewed descriptor
digest and update never silently follows a moving tag. Default first-release
updates are administrator initiated; automatic updates require a later explicit
permission/maintenance/data compatibility policy.

Portal server deployments retain their existing `beta` channel configuration.
Do not rewrite server Compose image references to fixed versions/digests as part
of this project. Record the resolved digest/version for verification and recovery
without changing that configured channel. Runner compatibility and its channel
policy must be explicit; do not assume Portal's rolling tag can be reused for app
packages or a differently versioned runner.

The existing Portal release workflow remains authoritative for Portal releases.
Use exact-main CI, required bilingual reader notes and guarded artifact/channel
publication. A documentation PR must not dispatch a release or change a server.

## 8. Handoff template for every implemented work package

```text
Work package and acceptance case IDs:
Accepted decision/schema revision:
Source PRs and exact commits:
Contracts added or changed:
Tests run, fixture and results:
Failure/recovery evidence:
Known unsupported behavior:
Public artifact identity, if released:
Production changes, if separately authorized:
Next eligible work package:
```

Fill with actual evidence. Keep credentials and protected backup paths/material
out of public attachments when disclosure could enable access. Do not write
"complete" while required source, released-byte or runtime evidence is missing.
