# Proposal: app-runner as a service in the Portal compose project

Status: Proposed 2026-11-20. Awaiting owner decisions (section 13). This document changes no code, schema or contract. It builds on:

- [146](146-runner-service.md): the runner service, its startup order and the Portal connection;
- [182](182-cross-platform-backend-runtime.md): `app-runner provision-xfs-images` and the XFS images on ext4 hosts;
- [184](184-reviewed-forward-proxy.md) and [175](175-reviewed-multi-origin-and-private-network.md): the broker, the forward proxy and the address guard;
- [185](185-data-quota-and-import.md): production image sizes (data 8 GiB, checkpoint at least 16 GiB).

Related: [07](07-handbook.md) section 4, [29](29-runner-controller-enrollment.md).

Evidence is against `main` at `bcb7c03a`.

## 1. Problem and evidence

Portal and its database run from `docker-compose.yml`. app-runner is a host binary with its own systemd unit and hand-made host state, so every install, upgrade and move is two jobs.

1. **Install is manual and long.**
   - Handbook 4.1 installs a tarball binary to `/opt/portal-app-runner/app-runner`.
   - Handbook 4.2 runs `provision-xfs-images`. It prints fstab lines but does not install them (`provision/render.go`).
   - Doc 146 has the operator create the journal, IPC root, control root, controller group and both JSON files by hand.
2. **Upgrade is separate.** The Portal image comes from GHCR (`release.yml`, `docker` job). The runner comes from `app-runner_<v>_linux_<arch>.tar.gz` (`build-runner` job, `release.yml:404`). Nothing ties the two versions together, and the handbook has no runner upgrade procedure.
3. **`/run` state is not recreated.** `ipc_root` and `control_root` sit under `/run/portal-app-runner` (`deploy/app-runner/service.example.json`). Nothing in `deploy/` recreates them after a reboot.
4. **A crash blocks restart.** The listener refuses a control socket path that already exists (`transport/transport_linux.go:70`), and `Restart=on-failure` cannot fix that.

The owner wants one compose project that holds Portal, its database and app-runner. A host is set up once with one command, then runs `docker compose up -d`. It is upgraded with `docker compose pull && docker compose up -d`. anchan is the first target. Its Portal is already one compose file, `/data/report-portal/docker-compose.yml`, which pins a fixed tag and uses the host's PostgreSQL. aaPanel's nginx sits in front.

## 2. What the code requires of the runner's environment

Each of these is a startup or operation check today. None may be weakened.

| Requirement | Where | Consequence for a container |
|---|---|---|
| The runner and dockerd must share a **mount namespace**. Every holder of the daemon end of the socket must be a `dockerd` whose `/proc/<pid>/ns/mnt` equals the runner's. | `driver/daemon_peer_linux.go:21-115` | A runner in its own container mount namespace is refused: "a containerised daemon" is named as a refusal case. |
| `/proc/self/root` must be `/`, and `/proc/self/mountinfo` must be the daemon's mount table. | `driver/candidate_ipc_identity_linux.go:42` | The overlap check compares the runner's mount table with the mount sources of every container. |
| dockerd must share the runner's **network namespace** for network volumes. | `driver/candidate_ipc_linux.go:648` | Needs the host network. |
| The address guard enumerates the runner's own interfaces and bridges (`docker0`, `br-*`, `virbr*`, …). | `broker/address_guard.go:51-90`, `hostnet/hostnet.go:21-71`, `service/private_networks.go:28-55` | In its own netns the guard would see only `eth0` and `lo`. It would not deny the host's bridges or addresses. |
| The runner mounts a 16 MiB tmpfs under `<ipc_root>/…/http` that Docker then bind-mounts into the app. | `driver/candidate_ipc_linux.go:113-160` | dockerd must see that mount. |
| Paths sent to Docker are the runner's own strings: `<data_root>/<ns>` → `/data`, and the IPC `http` and `broker` directories. | `driver/container.go:425-433` | Docker resolves them in its own mount namespace. |
| The daemon is identified by scanning `/proc/*/fd` and `/proc/<pid>/exe`. | `driver/daemon_peer_linux.go` | Needs the host PID namespace and ptrace-level read access. |
| The XFS checks: magic, `nodev,nosuid,noexec`, `quotactl_fd(Q_XGETQSTAT)` enforcement, and root identity `{XFS UUID, inode}`. | `data/data_linux.go:89-194` | The kernel must be at least 5.14. |
| The checkpoint root is a distinct inode. | `service/runner_linux.go:84` | — |
| The rootful Docker profile: cgroup v2, no rootless, no userns, default seccomp, limits supported. | `driver/preflight.go:109-155` | — |
| CPU reservation requires the runner's affinity to equal the online set and `NCPU`. | `service/capacity.go:211-229` | The container must not get a `cpuset` or CPU limit. |
| Socket and directory modes: control root `root:<gid> 0750`, socket `0660`, peer `SO_PEERCRED` uid equal to `controller_uid`. All IDs are numeric. | `transport/transport_linux.go:60-100,180`, `service/config.go:247` | Nothing reads `/etc/passwd` or `/etc/group`. |
| Portal's euid must equal the controller UID. | `internal/app/app_platform_backend_http.go:27` | Portal's `PUID` must equal `controller_uid`. |

**Same-path bind mounts are not enough** (request item 2). Binding the data root, checkpoint root and IPC root at identical paths into a runner container makes Docker resolve those paths correctly, but it still fails:

- **Namespace checks.** The mount-namespace check refuses, and so does the `/proc/self/root` check, so every writable candidate, data-writer, HTTP storage release and recovery step refuses.
- **The overlap check trips on the runner itself.** `requireNoContainerMountReferences` (`driver/candidate_ipc_identity_linux.go:20-160`) refuses any container whose mount source is the namespace path or an ancestor of it. The runner's own container, binding the data root, is such a container.
- **Relaxing the checks is a weakening.** It would need either rewriting the overlap check to resolve through `/proc/<dockerd>/root`, or trusting binds to cover every path, and each is a weakening of a reviewed guard.

## 3. Proposal in one paragraph

Ship app-runner as an image, `ghcr.io/kazuhahub/stockanalysisprediction-report-portal-app-runner`, published from the same release and under the same tags as the Portal image.

**The service.** It is a service in the Portal compose file: privileged, `network_mode: host`, `pid: host`, and no volumes. The image's entry joins the **host mount namespace** before it executes the runner. So the runner runs exactly as the systemd binary does today: same host filesystem view, same mount table, same network, same checks. The container is only how the binary is delivered and supervised.

**host-setup.** A new idempotent subcommand, `app-runner host-setup`, provisions the host through the same entry: `docker compose run --rm app-runner host-setup`. It:
- prints a plan and changes nothing without `--apply`;
- creates the XFS images (by calling doc 182's provisioning code), the fstab lines, the directories, the configs and the enrollment;
- uses numeric IDs only;
- refuses any existing state that does not match.

**Portal and the apps.** Portal reaches the control socket through a read-only bind of one durable host directory. Applications are still created by the runner at runtime, with the isolation they have today.

## 4. Authority analysis

The runner today is a root process with all capabilities in the host's namespaces, and it holds the Docker socket. Docker-socket access is root on the host. A privileged, host-PID, host-network container in the host mount namespace is therefore **not a privilege increase**.

| Authority | systemd unit today | Compose service |
|---|---|---|
| UID / capabilities | root, full set (`User=root`, no hardening in the unit) | root, full set (`privileged: true`) |
| Mount namespace | host | host (the entry joins PID 1's) |
| Network namespace | host | host (`network_mode: host`) |
| PID namespace | host | host (`pid: host`) |
| Devices | host `/dev` | host `/dev`, through the host mount namespace |
| seccomp / AppArmor | unconfined | unconfined (`privileged`) |
| Docker socket | `/run/docker.sock` | the same path, in the host mount namespace (no bind) |
| cgroup | `portal-app-runner.service` | the container's cgroup, with no CPU, cpuset or memory limit |
| Who can stop or replace it | root (`systemctl`) | root, and the `docker` group, which is already root-equivalent |
| Binary provenance | tarball checked against `SHA256SUMS.txt` | registry image checked by its digest (see below) |
| Restart | `on-failure`, 5 s, start limit 5 per 300 s | `unless-stopped`, with Docker's increasing restart delay |

What does change:

- **Binary provenance.** The image digest is recorded on the release draft next to the Portal digest (`DIGEST:` line, `release.yml`). anchan keeps fixed tags with `pull_policy: missing`, and its deploy procedure checks the digest as it does for Portal.
- **Restart behaviour.** No start limit, so a failing prerequisite retries forever with growing delay. Every retry fails closed in the same startup checks (146 §Service lifecycle), so this is noise, not risk.
- **`docker exec` into the runner container is a host root shell.** The same is already true of any `docker run --privileged` by a docker-group member, so no new principal gains anything.

**Portal gains nothing.** It gets one read-only bind of the control directory. It still has no Docker socket, data root, IPC root or journal (handbook §4 step 4).

**The runner opens no TCP listener.** The control socket and the broker sockets are Unix sockets, and the forward proxy runs over the broker socket (184 §5.5). So host networking publishes nothing. P1 checks this (section 11).

## 5. Image and entry

- **Base.** `Dockerfile.app-runner` uses `debian:bookworm-slim`, pinned by digest like `Dockerfile.release`. Its `util-linux` provides `nsenter`.
- **Contents.** It copies the prebuilt static `bin/linux-${TARGETARCH}/app-runner` (CGO disabled, as built by `build-runner`), and nothing else: no xfsprogs (Q2) and no docker CLI.
- **User and labels.** There is no `USER`; the image runs as root. It carries the OCI labels `revision` and `version`.
- **Entry.** `/usr/local/bin/portal-app-runner-entry` does three things:
  1. It refuses unless it can open `/proc/1/ns/mnt` and `/proc/1/ns/net`, PID 1's mount namespace differs from its own, and PID 1's network namespace equals its own. That means `pid: host` and `network_mode: host`.
  2. It opens the runner binary on descriptor 3.
  3. It runs `exec nsenter --mount=/proc/1/ns/mnt -- /proc/self/fd/3 "$@"`. The runner is executed from the descriptor it already holds, so the image file runs, while the root directory, the mount table and every path are the host's. After the switch, `/proc/self/root` is `/`.
- **Inherited descriptors.** The runner closes any inherited descriptor above 2 as its first action (new, P1).
- **Other subcommands.** Every subcommand (`serve`, `health`, `host-setup`, `bootstrap`, `inspect-daemon`, `pack-import`) goes through the entry, so all of them see the host. Doc 185's `pack-import` inbox is therefore used exactly as today.

Go cannot join a mount namespace after its threads start (`setns(CLONE_NEWNS)` refuses a multi-threaded caller), and the release build has no cgo. That is why the entry uses `nsenter` rather than code in the runner.

## 6. Compose service

Added to `docker-compose.yml` under a profile, so a static-only install is unchanged:

```yaml
  app-runner:
    # Same release and tag as report-portal. Production pins a fixed tag (see section 9).
    image: ghcr.io/kazuhahub/stockanalysisprediction-report-portal-app-runner:${PORTAL_VERSION:-latest}
    container_name: report-portal-app-runner
    profiles: ["app-runner"]          # enabled by COMPOSE_PROFILES=app-runner in .env
    pull_policy: always
    restart: unless-stopped
    # Not a privilege increase: the systemd unit ran as root in the host's namespaces with the
    # Docker socket (doc 187 section 4). The entry joins the host mount namespace.
    privileged: true
    network_mode: host                # address guard and network volumes need the host netns
    pid: host                         # daemon identification scans /proc; the entry needs PID 1
    userns_mode: host
    # No volumes, ports, devices, cpus, cpuset or memory limit, on purpose (doc 187 section 2).
    command: ["serve", "--config", "/etc/portal-app-runner/service.json"]
    stop_signal: SIGTERM
    stop_grace_period: 30s            # was TimeoutStopSec=30s
    healthcheck:
      test: ["CMD", "/usr/local/bin/portal-app-runner-entry", "health",
             "--config", "/etc/portal-app-runner/service.json"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 60s
    logging:
      driver: json-file
      options: { max-size: "10m", max-file: "5" }
```

`report-portal` gains:

```yaml
    environment:
      PUID: ${PORTAL_UID:-10001}
      PGID: ${PORTAL_GID:-10001}
      # All three empty (the default) = managed execution off, static apps unchanged.
      RP_APP_RUNNER_SOCKET: ${RP_APP_RUNNER_SOCKET:-}
      RP_APP_RUNNER_CONTROLLER_UID: ${RP_APP_RUNNER_CONTROLLER_UID:-}
      RP_APP_RUNNER_CONTROLLER_GID: ${RP_APP_RUNNER_CONTROLLER_GID:-}
    volumes:
      # Same path inside and outside. Read-only: Portal can connect to the socket but cannot
      # create, replace or remove anything in the directory. The default source is a harmless
      # empty directory in the project folder on hosts without a runner.
      - ${RP_APP_RUNNER_CONTROL_DIR:-./app-runner-control}:/var/lib/portal-app-runner/control:ro
```

The two services are wired like this:

- **No `depends_on` between them.** Portal binds the endpoint at startup without connecting (`app_platform_runtime_bootstrap.go`), and static apps never depend on the runner (146).
- **Neither container mounts the data root or the IPC root,** so `requireNoContainerMountReferences` never sees them.
- **`docker compose down` stops only the two project containers.** App containers are not compose services, and their `RestartPolicy` is `no`. They keep running, unmanaged until the runner returns, exactly as with `systemctl stop` today.

**Healthcheck (`app-runner health`, new).** It checks, in the host namespace:
- the control socket exists as a socket owned by `0:<controller_gid>` with mode `0660`;
- a `connect()` succeeds.

The runner drops the connection, because the peer is not the controller UID. Health never takes the journal lock, so it cannot race a starting runner. It is informational only (`docker compose ps`, upgrade checks), and nothing depends on it.

## 7. Paths

The defaults below come from the examples and doc 182. host-setup accepts each as a flag.

| Item | Host path | Owner, mode | Created by |
|---|---|---|---|
| Configs | `/etc/portal-app-runner/{service,bootstrap}.json` | `root:root 0600`, dir `0700` | host-setup |
| Runner state parent | `/var/lib/portal-app-runner` | `root:root 0755` | host-setup |
| Journal | `/var/lib/portal-app-runner/journal` | `root:root 0700` | host-setup (the files: bootstrap) |
| Image scratch (`image_root`) | `/var/lib/portal-app-runner/images` | `root:root 0700` | host-setup |
| XFS image files | `/var/lib/portal-app-runner/volumes/{data,checkpoint}.img` (anchan: `/data/portal-runner/`) | `root:root 0600`, dir `0700` | host-setup through provision |
| Data root | `/srv/portal-runner/data/root` | XFS, `root:root 0700` | provision |
| Checkpoint root | `/srv/portal-runner/checkpoint/root` | XFS, `root:root 0700` | provision |
| Control root | `/var/lib/portal-app-runner/control` (was `/run/...`) | `root:<gid> 0750` | host-setup |
| Control socket | `/var/lib/portal-app-runner/control/runner.sock` | `root:<gid> 0660` | serve |
| IPC root | `/run/portal-app-runner/ipc`, parent `0755` | `root:root 0700` | serve at startup (new, P1) |

- **The control root moves to durable storage** (Q4). After a reboot the directory already exists when either container starts. Under `/run`, a Portal container that started first would make Docker auto-create the bind source as `root:root 0755`, and the runner would then refuse it.
- **The IPC root stays on `/run`.** Its per-generation tmpfs mounts are per boot. serve creates `ipc_root` and its parent when they are missing, with the exact owner and mode. If they exist with any other owner or mode, serve refuses, as for every other root. That closes problem 3.
- **Stale control socket (new, P1, Q5).** serve removes a socket left at `control_socket` only if both hold:
  - it holds the journal's exclusive lock, which proves no other runner is serving;
  - `lstat` shows a socket owned `0:<controller_gid>`, mode `0660`, link count 1.

  Anything else at that path is refused, as today. That closes problem 4.

## 8. host-setup

```sh
docker compose run --rm app-runner host-setup                 # plan only
docker compose run --rm app-runner host-setup --apply \
  --data-size 8G --checkpoint-size 16G \
  --image-dir /data/portal-runner \
  --controller-uid 10001 --controller-gid 10001 \
  --enrollment /root/portal-runtime-enrollment.json
```

`docker compose run` activates the profile of the service it targets, so host-setup works before `.env` enables the profile. `--rm` is required so no stopped setup container is left behind; one would show up in `containers/json?all=1`. The host must already have a Docker daemon that passes the profile check; installing Docker is not host-setup's job.

### 8.1 Steps

1. **Preflight. This step only reads.** It refuses unless all of these hold:
   - It runs in the host mount, network and PID namespaces, as euid 0.
   - The kernel is at least 5.14 (`quotactl_fd`).
   - The Docker profile check (`inspect-daemon`) passes, and the daemon is identified in the runner's mount namespace.
   - These host tools are present: `losetup`, `mkfs.xfs`, `mount`, `umount`, `fallocate` and `blkid`, plus `chattr` for btrfs (Q2).
   - The controller UID and GID are nonzero and not the overflow ID.
   - Neither ID is already assigned in the host's `/etc/passwd` or `/etc/group`. Those files are parsed as plain files; accounts from LDAP or another NSS source are not seen, and the plan says so.
2. **Plan.** It prints every directory, file, image, mount, fstab line and config key it would create, and every item that already matches. Without `--apply` it stops there (exit 0).
3. **Directories.** It creates each directory in section 7 that is missing, with the exact owner and mode. Every ancestor must be root-owned and not group- or world-writable.
4. **XFS volumes.** It calls doc 182's `provision` package with the sizes and paths. Its rules are unchanged: never sparse, `mkfs.xfs -K` only on a new image, no format of an existing one, rollback of this run only. The defaults are data 8 GiB and checkpoint 16 GiB; the checkpoint refuses less than 16 GiB unless `--checkpoint-size-below-production` is given (185 Q8).
5. **fstab.** It adds provision's rendered lines (`loop,prjquota,nodev,nosuid,noexec,nofail,X-fstrim.notrim`).
   - It writes a temporary file in `/etc`, checks it with `findmnt --verify --tab-file`, fsyncs it and renames it over `/etc/fstab`.
   - The first time only, it keeps `/etc/fstab.before-portal-app-runner`.
   - No `RequiresMountsFor=` drop-in is needed: if a mount is missing at boot, the runner refuses at its XFS check and retries.
6. **Configs.** It writes `service.json` from the documented defaults, provision's roots and `capacity.disk_bytes`, the IDs and the enrollment document. It writes `bootstrap.json` from the enrollment document. Both are `0600`. The enrollment document is the one Portal produces when an administrator prepares a runtime binding (29, 146); host-setup does not change how it is produced. Without `--enrollment`, it writes neither file, reports `enrollment pending` and exits 3.
7. **Enrollment.** It runs the existing `bootstrap` code path, which is idempotent; a different identity conflicts.
8. **Report.** It prints the `.env` lines for the compose project:

   ```text
   COMPOSE_PROFILES=app-runner
   PORTAL_UID=10001
   PORTAL_GID=10001
   RP_APP_RUNNER_CONTROL_DIR=/var/lib/portal-app-runner/control
   RP_APP_RUNNER_SOCKET=/var/lib/portal-app-runner/control/runner.sock
   RP_APP_RUNNER_CONTROLLER_UID=10001
   RP_APP_RUNNER_CONTROLLER_GID=10001
   ```

   It prints them and does not write them, because the compose project directory is not known to the runner.

### 8.2 Refusal rules

- **Existing state that does not match is never changed.** That covers a directory with another owner or mode, a config file whose bytes differ, an fstab line for the same mount point or image with other fields, an image of another size or type, and a mount point that is busy.
  - host-setup refuses and names the item and the difference.
  - The operator fixes it or removes it by hand.
  - An exact match is a no-op, so running it a second time after success changes nothing and says so.
- **It never deletes, unmounts or detaches** anything it did not create in the same run (182 rule).
- **It never edits** `/etc/passwd`, `/etc/group`, `/etc/docker`, systemd units, ufw or packages. Numeric IDs need no account (section 2), so no host user database is written from a container. A host operator who wants a name for the GID may add one by hand. The plan says so, and the runner does not care.
- **It never writes Portal's files,** its `.env` or its volumes.

### 8.3 What it touches, exactly

- **Writes:**
  - `/etc/fstab`, plus a one-time backup;
  - `/etc/portal-app-runner/`;
  - `/var/lib/portal-app-runner/`;
  - the image directory;
  - `/srv/portal-runner/`;
  - loop devices, through `losetup`;
  - the journal, through bootstrap.
- **Reads:** `/etc/passwd`, `/etc/group`, `/proc`, `/sys` and the Docker socket.

## 9. Upgrade

**Floating tags (repo default).** `docker compose pull && docker compose up -d`.

- **Recreate.** Both containers are recreated when their image changed. The runner is stopped with SIGTERM within 30 s and started again.
- **Effect on apps.** App containers keep running. Backend requests and broker calls fail for those seconds, as with a systemd restart today. Journalled work resumes from its claims (146).
- **Tag promotion.** `release-channels.yml` promotes the runner tag and the Portal tag in the same job, runner first.
- **Mixing.** A pull between the two promotions can mix versions for seconds. That fails closed: every surface carries a `1-dev.1` format and refuses a mismatch, and machine support reports `unsupported` (`app_platform_machine_support.go:35-70`).

**Production (anchan).** The project stays one compose file, because aaPanel's compose page broke on multi-file projects. It pins `PORTAL_VERSION` to a fixed tag with `pull_policy: missing`. The existing deploy procedure applies, with three additions:
- check the runner image digest against the release draft;
- after `up -d`, wait for `app-runner ready` in `docker compose logs app-runner`, and for `health`;
- read the managed profile in the admin console.

**Ordering.** There is no handshake to order against. Compose starts both in parallel, which is correct because Portal does not need the runner at startup. One `PORTAL_VERSION` drives both images, so a compose file cannot pin them apart by accident.

**Journal upgrades.** A newer runner upgrades the journal on open (as v4 and v5 did). An older runner refuses a newer journal. Before an upgrade whose release notes name a journal schema change, the rollback limit is that release (section 10.3).

## 10. Migration from systemd and rollback

This is for a host that already runs app-runner under systemd. No data moves: the compose runner reads the same configs, journal, roots and images in the host mount namespace.

### 10.1 Before

1. Use the release whose image matches the installed binary, or upgrade the binary first. One version change at a time.
2. Record `systemctl status portal-app-runner`, the ready line, the installations and their states in the admin console, and `findmnt` for both roots.
3. Run `docker compose run --rm app-runner host-setup` (plan only).
   - **What it should report.** Every existing item matches. The only planned changes should be the new control root, the fstab lines if they were never installed, and nothing else.
   - **If it plans more.** Any refusal or unexpected change stops the migration here.

### 10.2 Switch

1. Set `control_root` and `control_socket` in `service.json` to the durable path. This is the one config edit; host-setup prints it and does not make it, because the file already exists.
2. `systemctl stop portal-app-runner && systemctl disable portal-app-runner`.
   - The old socket is removed on a clean stop.
   - App containers keep running.
3. `docker compose run --rm app-runner host-setup --apply`.
   - It creates the control root, and adds the fstab lines if they are missing.
   - Everything else is a no-op.
4. Add the printed `.env` lines.
   - Set `PORTAL_UID` and `PORTAL_GID` to the controller IDs the host already uses.
   - Portal's entry re-owns `/app/config` and `/app/data` if they change.
5. `docker compose up -d`, then check:
   - `app-runner ready` in the logs;
   - `health` passes;
   - the admin console shows the same runtime identity;
   - each enabled backend answers.

**Safety net.** The journal's exclusive `flock` and the data root's own lock refuse a second runner. A unit that is still running makes the compose runner exit with "in use". The reverse also holds. Both can never serve at once.

### 10.3 Rollback

1. `docker compose stop app-runner`, then remove `app-runner` from `COMPOSE_PROFILES`.
2. Revert `control_root` and `control_socket` (or keep the durable path, which works for the unit too), and revert Portal's `RP_APP_RUNNER_*` values.
3. `systemctl enable --now portal-app-runner`, with the binary from the **same release** as the image.
   - Rolling back across a journal schema change is refused by the older binary, as it is today.
   - Going back further needs the 168 restore path, not a rollback.
4. `docker compose up -d report-portal` and check as in 10.2.

The journal, the installed applications and their `/data` are never copied, so neither direction can lose them.

### 10.4 Fresh host (anchan)

1. Install the compose file with the new service.
2. Bring up Portal: `docker compose up -d report-portal`. On anchan the database is the host's.
3. Prepare the runtime binding in the admin console and save the enrollment document on the host.
4. `host-setup` (plan), then `host-setup --apply --enrollment …`.
5. Add the `.env` lines, then `docker compose up -d`.
6. Confirm the runtime in the admin console.

## 11. Test plan (these must fail first)

Each condition says which way its guard fails. All are refusals, not fallbacks. The native ones run in the existing `app-runner-probe.yml` workflow (rootful Docker), which is path-filtered, so no new CI load lands elsewhere.

**Entry:**
- Without `pid: host`, the entry exits non-zero before executing the runner. It fails closed.
- Without `network_mode: host`, the entry exits non-zero. It fails closed.
- With both set, the runner process's `/proc/self/ns/mnt` equals PID 1's and `/proc/self/root` is `/`.
- The runner holds no inherited descriptor above 2.

**Runner startup (new behaviour):**
- serve refuses before binding the socket when the daemon is not identified in its mount namespace.
  - Today that refusal is deferred to the first overlap check. P1 makes serve fail fast at startup.
  - The test runs serve without the entry, inside a plain privileged container. It fails closed.
- serve refuses before binding when dockerd is in another network namespace. It fails closed.
- serve creates a missing `ipc_root` with `root:root 0700`, and refuses an existing one with any other owner or mode. It fails closed.
- A stale `runner.sock` (socket, `0:<gid>`, `0660`) is removed only while serve holds the journal lock. Each of these is refused and left in place:
  - a regular file;
  - a socket with another mode or owner;
  - any socket while a second runner holds the lock.

  It fails closed.
- With a `cpuset` set on the container, a runner configured with `reserved_cpus` > 0 is not ready. That fails closed, as today; the test pins that the compose file sets no `cpuset`.
- The runner owns no TCP or UDP listening socket. The test checks with `ss -lntup` against the runner's PID.

**Overlap guard (regression):** a container that bind-mounts the data root, standing in for a wrongly configured runner or setup container, makes a writable candidate refuse with the overlap error. It fails closed.

**Portal:**
- Portal running with `PUID` different from `RP_APP_RUNNER_CONTROLLER_UID` logs "endpoint is unavailable", and managed execution stays off. It fails closed.
- With all three variables empty, static apps work and no runner is contacted.
- The control bind is read-only: an attempt by Portal's UID to create a file in the directory gets `EROFS`. Connect still works.

**host-setup:**
- **The plan changes nothing.** The test hashes the host state before and after.
- **`--apply` on a clean host** creates every item in section 7. A second `--apply` reports no changes and exits 0.
- **Each mismatch refuses and changes nothing:**
  - a directory with mode `0755` instead of `0700`;
  - `service.json` whose bytes differ;
  - an fstab line for the mount point with other options;
  - an existing image of another size.
- **ID collisions:** a controller UID or GID present in the host `/etc/passwd` or `/etc/group` refuses.
- **Checkpoint size:** a size below 16 GiB without the override refuses.
- **Missing tools:** if `mkfs.xfs` is missing, host-setup refuses in preflight, before any change, and names the package.
- **Enrollment pending:** without `--enrollment` it exits 3 and writes no config.
- **fstab write:** a `findmnt --verify` failure leaves `/etc/fstab` untouched.
- **Reboot:** after a reboot of the probe VM, or a simulated one where the test unmounts, detaches and clears `/run`, `docker compose up -d` brings the runner back to ready, with no manual step.

**Compose lifecycle (native):**
- **Setup:** host-setup with small sizes (`--checkpoint-size-below-production`, 2G), then a fixture enrollment and `up -d`.
- **Profile:** the controller-UID container observes the managed profile.
- **Restart without loss:** `docker compose restart app-runner` keeps the journal and enrollment, and a running candidate survives it.
- **Down:** `docker compose down` leaves app containers running and removes the control socket.
- **Health:** health turns unhealthy while the runner is stopped and healthy after it is ready.

**Release:**
- **Tag guard:** the runner image job refuses to replace an existing fixed tag (`release_guard.py`).
- **Digests:** the draft carries both digests.
- **Channels:** channel promotion refuses when the runner tag and the Portal tag would point at different releases.

## 12. CI and release changes

- **`release.yml`:** a `docker-runner` job.
  - **Placement.** It needs `build-runner` and `smoke-arm64`, and runs beside `docker`.
  - **Build.** It uses `Dockerfile.app-runner` for `linux/amd64` and `linux/arm64` with the same build method as the Portal image. It pushes only the fixed tag, with its own `buildcache`.
  - **Guards.** It uses the same "refuse to replace an already-published fixed tag" step, and adds a `RUNNER_DIGEST:` line to the draft.
  - **Tarballs.** They stay, for the systemd alternative (Q8).
- **`build-runner`:** stamp `-X` version and commit, which `app-runner version` and the ready line print. It is new and small.
- **`release-channels.yml`:** promote and verify the runner image by digest next to Portal's, under the same channel tags.
- **`app-runner-probe.yml`:** the native compose lifecycle test (section 11), built from the local tree. The assertion that `portal.env.example` matches `service.example.json` (`:71`) moves to the new control path.
- **The `test.yml` image smoke** stays amd64 Portal only. The runner image is smoke-tested in the probe workflow.

## 13. Open questions (recommended answers)

Q1. **Join the host mount namespace instead of running on same-path binds (request item 2).** Recommended: accept. Section 2 shows that binds fail the daemon-identity and overlap guards, and that making them pass would weaken both. Joining the host namespace keeps every check byte-for-byte and needs no propagation flags. Docker resolves every path identically because there is only one mount namespace.

Q2. **Where the XFS tools come from.** Recommended: from the host. host-setup uses doc 182's code unchanged, with the host's `xfsprogs` and `util-linux`. Preflight refuses, naming the package, if they are missing; Ubuntu Server ships both. Using the image's tools would mean creating loop devices and mounts from the container's mount namespace. That pins that namespace and makes `loop/backing_file` unreadable from the host, which the runner's own checks read.

Q3. **Controller UID and GID.** Recommended: Portal's existing `PUID` and `PGID`, default `10001:10001`, as numeric IDs in config with no host account. On anchan this keeps Portal's file ownership and the `docker exec -u 10001:10001` backup step unchanged. host-setup refuses if either ID is used on the host. The example `41001` remains valid for hosts that prefer it.

Q4. **Durable control directory** `/var/lib/portal-app-runner/control`. Recommended: accept. It removes the reboot ordering hazard between Portal and the runner.

Q5. **Stale socket removal under the journal lock.** Recommended: accept. It is the only way a crashed runner restarts without a manual step, and the lock proves that nothing else is serving.

Q6. **A compose profile for the runner**, enabled by `COMPOSE_PROFILES=app-runner` in `.env`. Recommended: accept. The zero-clone static install keeps working with a plain `docker compose up -d`, and a runner host also keeps a plain `up -d`.

Q7. **Image name and tag.** Recommended: `ghcr.io/kazuhahub/stockanalysisprediction-report-portal-app-runner`, with exactly Portal's tags. One `PORTAL_VERSION` variable selects both images.

Q8. **Keep the systemd unit.** Recommended: keep it as a documented alternative. It costs little: same binary, same configs, and `app-runner host-setup` also runs directly on a host. It serves the macOS/Windows Linux VM recipe (handbook 4.4), where compose is optional. The handbook leads with compose.

Q9. **The `health` subcommand** (socket check plus `connect`, no lock). Recommended: add it, and make nothing depend on it.

Q10. **Fail fast on namespace mismatch.** serve refuses at startup when the daemon is not identified in its mount and network namespaces, instead of failing on the first overlap check. Recommended: accept. A misconfigured container should not look ready.

Q11. **Resource limits on the runner container.** Recommended: none, as under systemd. A cpuset breaks the CPU-reservation check, and a memory limit has no measurement behind it. Revisit with capacity data.

Q12. **Docs and examples.** Recommended:
- the handbook §4 leads with compose and host-setup, and §4.1 to 4.2 become the systemd alternative;
- 146 gains a "Compose service" section and the new control path;
- `deploy/app-runner/` keeps the unit;
- `service.example.json` and `portal.env.example` take the durable control path and `10001`;
- the repo's `docker-compose.yml` gains the profiled service.

## 14. Schema change — needs owner approval

None.

- **Portal database:** no migration.
- **Runner journal:** no schema or format change. It stays as it is, at the same path.
- **`service.json` and `bootstrap.json`:** formats `portal.app-runner.config/1-dev.1` and `portal.app-runner.bootstrap/1-dev.1` are unchanged, with no new field. Only example values change: the control path and the IDs.
- **New `.env` keys** are deployment settings, not a stored format.

If implementation shows that a format must change, it comes back to the owner first.

## 15. Phasing

- **P1, runner code:**
  - `host-setup` (plan/apply, wrapping `provision`), `health` and `version`;
  - fail-fast namespace check, `ipc_root` creation, stale socket removal and descriptor close;
  - the tests in section 11 that need no compose.
- **P2, image and release:**
  - `Dockerfile.app-runner` with the entry;
  - the `docker-runner` job, channel promotion, the probe workflow's compose lifecycle test.
- **P3, deployment files and docs:**
  - the profiled service in `docker-compose.yml`;
  - the examples in `deploy/app-runner/`;
  - handbook §4 and doc 146.
- **P4, anchan.** Only when the owner asks, per the release and deploy boundaries:
  - add the service to `/data/report-portal/docker-compose.yml`;
  - run host-setup with `--image-dir /data/portal-runner`, 8G data and 16G checkpoint;
  - enrol and `up -d`.
  - First verify on the host: kernel 5.14 or later, cgroup v2, the Docker profile, xfsprogs, and that 10001 is free.

P1 to P3 ship as separate PRs. P2 depends on P1 for the entry's subcommands.
