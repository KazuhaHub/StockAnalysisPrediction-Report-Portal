# Proposal: app-runner as a service in the Portal compose project

Status: Accepted 2026-11-20. The owner accepted the recommended answer to every open question (section 13) and the release-metadata.json format change. **Implemented (P1 to P3) on branch `feat/app-runner-compose`, verified natively on a disposable VM, not merged, published or deployed; P4 (anchan) is operator-only and not done.** Section 16 records what was built, the decisions the implementation took, where it deviates from the text above, and the VM results; where section 16 and an earlier section differ, section 16 is what was built. The design above changes no database, journal or runner-config format. It builds on:

- [146](146-runner-service.md): the runner service, its startup order and the Portal connection;
- [182](182-cross-platform-backend-runtime.md): `app-runner provision-xfs-images` and the XFS images on ext4 hosts;
- [184](184-reviewed-forward-proxy.md) and [175](175-reviewed-multi-origin-and-private-network.md): the broker, the forward proxy and the address guard;
- [185](185-data-quota-and-import.md): production image sizes (data 8 GiB, checkpoint at least 16 GiB);
- 186 (in review): the proxy's pinned destinations, which add operator-edited fields to `service.json`.

Related: [07](07-handbook.md) section 4, [29](29-runner-controller-enrollment.md).

Evidence is against `main` at `bcb7c03a`.

## 1. Problem and evidence

Portal and its database run from `docker-compose.yml`. app-runner is a host binary with its own systemd unit and hand-made host state, so every install, upgrade and move is two jobs.

1. **Install is manual and long.**
   - Handbook 4.1 installs a tarball binary to `/opt/portal-app-runner/app-runner`.
   - Handbook 4.2 runs `provision-xfs-images`. It prints fstab lines but does not install them (`provision/render.go`).
   - Doc 146 has the operator create the journal, IPC root, control root, the `portal-app-runner` group and both JSON files by hand.
2. **Upgrade is separate.** The Portal image comes from GHCR (`release.yml`, `docker` job). The runner comes from `app-runner_<v>_linux_<arch>.tar.gz` (`build-runner` job, `release.yml:404`). Nothing ties the two versions together, and the handbook has no runner upgrade procedure.
3. **`/run` state is not recreated.** `ipc_root` and `control_root` sit under `/run/portal-app-runner` (`deploy/app-runner/service.example.json`). Nothing in `deploy/` recreates them after a reboot.
4. **A crash blocks restart.** The listener refuses a control socket path that already exists (`transport/transport_linux.go:167`), and `Restart=on-failure` cannot fix that.

The owner wants one compose project that holds Portal, its database and app-runner. A host is set up once with one command, then runs `docker compose up -d`. It is upgraded with `docker compose pull && docker compose up -d`. anchan is the first target. Its Portal is already one compose file, `/data/report-portal/docker-compose.yml`, which pins a fixed tag and uses the host's PostgreSQL. aaPanel's nginx sits in front.

## 2. What the code requires of the runner's environment

Each of these is a startup or operation check today. None may be weakened.

| Requirement | Where | Consequence for a container |
|---|---|---|
| The runner and dockerd share a **mount namespace**. Every holder of the daemon end of the socket must be a `dockerd` whose `/proc/<pid>/ns/mnt` equals the runner's. | `driver/daemon_peer_linux.go:21-115` | A runner in its own container mount namespace is refused. |
| `/proc/self/root` must be `/`, and `/proc/self/mountinfo` must be the daemon's mount table. | `driver/candidate_ipc_identity_linux.go:42` | The overlap check compares the runner's mount table with the mount sources of every container. |
| dockerd shares the runner's **network namespace** for network volumes. | `driver/candidate_ipc_linux.go:648` | Needs the host network. |
| The address guard enumerates the runner's own interfaces and bridges. | `broker/address_guard.go:51-90`, `hostnet/hostnet.go:21-71`, `service/private_networks.go:28-55` | In its own netns the guard would see only `eth0` and `lo`, not the host's bridges or addresses. |
| The runner mounts a 16 MiB tmpfs under `<ipc_root>/…/http` that Docker then bind-mounts into the app. | `driver/candidate_ipc_linux.go:113-160` | dockerd must see that mount. |
| Paths sent to Docker are the runner's own strings: `<data_root>/<ns>` → `/data`, and the IPC `http` and `broker` directories. | `driver/container.go:425-433` | Docker resolves them in its own mount namespace. |
| The daemon is identified by scanning `/proc/*/fd` and `/proc/<pid>/exe`. | `driver/daemon_peer_linux.go` | Needs the host PID namespace and ptrace-level read access. |
| No container, running or stopped, may mount the namespace path or an ancestor of it. | `requireNoContainerMountReferences`, `driver/data_writer_linux.go:22` | A runner container that binds the data root trips the guard on itself. |
| The XFS checks: magic, `nodev,nosuid,noexec`, `quotactl_fd(Q_XGETQSTAT)` enforcement, and root identity `{XFS UUID, inode}`. The data root is locked with `flock`. | `data/data_linux.go:89-194` | The kernel must be at least 5.14. |
| The checkpoint root is a distinct inode. | `service/runner_linux.go:84` | — |
| The rootful Docker profile: cgroup v2, no rootless, no userns, default seccomp, limits supported. | `driver/preflight.go:109-155` | — |
| CPU reservation requires the runner's affinity to equal the online set and `NCPU`. | `service/capacity.go:211-229` | No `cpuset` or CPU limit. |
| Control root `root:<gid> 0750`, socket `0660`, peer `SO_PEERCRED` uid equal to `controller_uid`. All IDs are numeric. | `transport/transport_linux.go:60-100,180`, `service/config.go:247` | Nothing reads `/etc/passwd` or `/etc/group`. |
| Portal's euid must equal the controller UID. | `internal/app/app_platform_backend_http.go:27` | Portal's `PUID` must equal `controller_uid`. |

**Same-path bind mounts are not enough** (request item 2). Binding the data, checkpoint and IPC roots at identical paths into a runner container makes Docker resolve those paths correctly, but it still fails:

- **Namespace checks.** The mount-namespace and `/proc/self/root` checks refuse, so every writable candidate, data-writer, HTTP storage release and recovery step refuses.
- **The overlap guard trips on the runner itself.**
- **Relaxing the checks is a weakening.** Making them pass means resolving through `/proc/<dockerd>/root`, or trusting binds to cover every path, and each weakens a reviewed guard.

## 3. Proposal in one paragraph

Ship app-runner as an image, `ghcr.io/kazuhahub/stockanalysisprediction-report-portal-app-runner`, published by the same CI-gated release and under the same tags as the Portal image.

**The service.** It is a service in the Portal compose file: privileged, with the host's network, PID, IPC, UTS and cgroup namespaces, and no volumes. The image's entry joins the **host mount namespace** before it executes the runner. So the runner runs as the systemd binary does today: same filesystem view, mount table, network and checks. The container is only how the binary is delivered and supervised.

**host-setup.** A new idempotent subcommand, `app-runner host-setup`, runs through the same entry: `docker compose run --rm app-runner host-setup`.
- **New host:** it provisions the host from documented defaults.
- **Existing runner host:** it **adopts** the layout already there. The existing `service.json` and `bootstrap.json` are the source of truth, and it verifies rather than recreates.
- **Changes:** it prints a plan and changes nothing without `--apply`. It refuses only state that conflicts with what it owns.

**Portal and the apps.** Portal reaches the control socket through a read-only bind of one durable host directory. Applications are still created by the runner at runtime, with their isolation unchanged.

## 4. Authority analysis

The runner today is a root process with all capabilities in the host's namespaces, and it holds the Docker socket. Docker-socket access is root on the host. A privileged container in the host mount, network and PID namespaces is therefore **not a privilege increase**.

| Authority | systemd unit today (`deploy/app-runner/portal-app-runner.service`) | Compose service |
|---|---|---|
| UID / capabilities | root, full set, no hardening directives | root, full set (`privileged: true`) |
| Primary GID | `Group=portal-app-runner` | 0. No check depends on it: every runner-created owner is set explicitly (`fchown`), and `UMask=0077` leaves no group bits. Covered by a P1 test. |
| umask | `UMask=0077` | `0077`, set by the entry before `exec` |
| Open files | `LimitNOFILE=65536` | `ulimits: nofile: 65536` |
| Mount namespace | host | host (the entry joins PID 1's) |
| Network / PID / IPC / UTS / cgroup namespace | host | host (`network_mode`, `pid`, `ipc`, `uts`, `cgroup: host`) |
| Devices | host `/dev` | host `/dev`, through the host mount namespace |
| seccomp / AppArmor | unconfined | unconfined (`privileged`) |
| Docker socket | `/run/docker.sock` | the same path, in the host mount namespace (no bind) |
| cgroup placement | `portal-app-runner.service` | the container's cgroup; no CPU, cpuset or memory limit |
| Who can stop or replace it | root (`systemctl`) | root, and the `docker` group, which is already root-equivalent |
| Binary provenance | tarball checked against `SHA256SUMS.txt` | image digest recorded in `release-metadata.json`, verified before channel promotion (section 12) |
| Start and stop ordering | `After=`/`Requires=docker.service` | inside dockerd's lifecycle (below) |
| Restart | `on-failure`, 5 s, start limit 5 per 300 s | `unless-stopped`, with Docker's increasing restart delay |

**Lifecycle under dockerd.**
- **Stopping Docker.**
  - *Today:* `systemctl stop docker` first stops the runner, because of `Requires=`, and dockerd then stops the app containers.
  - *Compose:* dockerd stops the runner and the app containers together. Each gets SIGTERM and its own stop timeout, bounded by dockerd's `shutdown-timeout`, which defaults to 15 s. That can be shorter than `stop_grace_period`.
  - *Effect:* the runner's shutdown cancels sessions and closes the journal. It stops no app, so the ordering change loses nothing, and every journalled step resumes from its claims.
  - *After Docker starts again:* app containers stay stopped (`RestartPolicy` `no`), as today, and the runner returns (`unless-stopped`).
- **With `live-restore`.**
  - *Today:* restarting dockerd also restarts the unit, through `Requires=`.
  - *Compose:* the runner container and the apps keep running. The runner's next Docker connection is identified again against the new dockerd PID, because identification happens per connection.
  - *Gap:* in-flight Docker calls fail during the restart and are retried by their operations. A P2 native test covers one `systemctl restart docker` with and without `live-restore`.
- **Boot.** fstab mounts (`nofail`) come up before `docker.service`. If a mount is missing, the runner fails its XFS check and retries. This replaces the `RequiresMountsFor=` drop-in.

What does change:

- **Binary provenance.** It moves from a checksummed tarball to a digest-verified image.
- **No start limit.** A failing prerequisite retries forever with a growing delay. Every retry fails closed in the same startup checks (146), so this is noise, not risk.
- **Host root shell.** `docker exec` into the container is a host root shell. That was already true for any docker-group member.

**Portal gains nothing.** It gets one read-only bind of the control directory, and still has no Docker socket, data root, IPC root or journal (handbook §4 step 4).

**The runner opens no TCP or UDP listener.** The control socket and the broker sockets are Unix sockets, and the forward proxy runs over the broker socket (184 §5.5). So host networking publishes nothing (test in section 11).

## 5. Image and entry

- **Base.** `Dockerfile.app-runner` uses `debian:bookworm-slim`, pinned by digest like `Dockerfile.release`. Its `util-linux` provides `nsenter`.
- **Contents.** It copies the prebuilt static `bin/linux-${TARGETARCH}/app-runner` from `build-runner`, and nothing else. It adds no xfsprogs (Q2) and no docker CLI.
- **User and labels.** There is no `USER`, and it carries the OCI labels `revision` and `version`.
- **Entry.** `/usr/local/bin/portal-app-runner-entry` does four things:
  1. It refuses unless all three hold:
     - it can read `/proc/1/ns/{mnt,net,pid}`;
     - PID 1's PID and network namespaces equal its own, which means `pid: host` and `network_mode: host`;
     - PID 1's mount namespace differs from its own.
  2. It sets `umask 0077`.
  3. It opens the runner binary on descriptor 3.
  4. It runs `exec nsenter --mount=/proc/1/ns/mnt -- /proc/self/fd/3 "$@"`, with the environment variable `PORTAL_APP_RUNNER_EXEC_FD=3`.

  The image file is executed from a descriptor opened before the namespace switch. The root directory, the mount table and every path are the host's, and `/proc/self/root` is `/`.
- **Descriptor 3.** It cannot be close-on-exec: the shell's `exec` of `nsenter` would close it before `nsenter` executes it. So the runner, as its first action, marks exactly the descriptor named by `PORTAL_APP_RUNNER_EXEC_FD` close-on-exec, after checking that it is its own executable (`fstat` equals `/proc/self/exe`). It closes nothing else and changes nothing for any other descriptor. Nothing it starts later, such as provision's `losetup` or `mkfs.xfs`, inherits the descriptor.
- **Why `nsenter`.** Go cannot join a mount namespace once its threads have started, and the release build has no cgo, so the switch must happen in `nsenter` rather than in the runner.
- **Subcommands.** All of them go through the entry: `serve`, `health`, `version`, `host-setup`, `bootstrap`, `inspect-daemon` and `pack-import`. So all of them see the host, and doc 185's `pack-import` inbox is used as today.

## 6. Compose service

Added to `docker-compose.yml` under a profile, so a static-only install is unchanged:

```yaml
  app-runner:
    image: ghcr.io/kazuhahub/stockanalysisprediction-report-portal-app-runner:${PORTAL_VERSION:-latest}
    container_name: report-portal-app-runner
    profiles: ["app-runner"]          # enabled by COMPOSE_PROFILES=app-runner in .env
    pull_policy: always               # anchan: missing, with a fixed PORTAL_VERSION
    restart: unless-stopped
    # Not a privilege increase: the systemd unit ran as root in the host's namespaces with the
    # Docker socket (doc 187 section 4). The entry joins the host mount namespace.
    privileged: true
    network_mode: host
    pid: host
    ipc: host
    uts: host
    cgroup: host
    userns_mode: host
    ulimits:
      nofile: { soft: 65536, hard: 65536 }
    # No volumes, ports, devices, cpus, cpuset or memory limit, on purpose (doc 187 section 2).
    command: ["serve", "--config", "/etc/portal-app-runner/service.json"]
    stop_signal: SIGTERM
    stop_grace_period: 30s
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

`report-portal` changes:

```yaml
    # One variable drives both images. The line was hard-coded :latest before.
    image: ghcr.io/kazuhahub/stockanalysisprediction-report-portal:${PORTAL_VERSION:-latest}
    environment:
      PUID: ${PORTAL_UID:-10001}
      PGID: ${PORTAL_GID:-10001}
      # All three empty (the default) = managed execution off, static apps unchanged.
      RP_APP_RUNNER_SOCKET: ${RP_APP_RUNNER_SOCKET:-}
      RP_APP_RUNNER_CONTROLLER_UID: ${RP_APP_RUNNER_CONTROLLER_UID:-}
      RP_APP_RUNNER_CONTROLLER_GID: ${RP_APP_RUNNER_CONTROLLER_GID:-}
    volumes:
      - type: bind
        source: ${RP_APP_RUNNER_CONTROL_DIR:-./app-runner-control}
        target: /var/lib/portal-app-runner/control
        read_only: true               # Portal can connect, but cannot create or replace anything
        bind:
          create_host_path: false     # a missing directory fails loudly; Docker never invents one
```

How the two services relate:

- **Static hosts.** `create_host_path: false` means the default source must exist. The zero-clone header gains `mkdir -p app-runner-control` (Q12).
  - **On runner hosts** the source is the durable directory made by host-setup.
  - **A source that is missing** stops Portal with a message naming the path. That is better than a `root:root 0755` directory that the runner would then refuse.
- **No `depends_on` between the two.** Portal binds its endpoint at startup without connecting (`app_platform_runtime_bootstrap.go`), and static apps never depend on the runner (146).
- **Neither container mounts the data root or the IPC root,** so `requireNoContainerMountReferences` never sees them.
- **`docker compose down` stops only the two project containers.** App containers are not compose services. They keep running, unmanaged until the runner returns, as with `systemctl stop` today.

**Healthcheck (`app-runner health`, new).** In the host namespace it checks two things:
- the control socket exists as a socket owned by `0:<controller_gid>` with mode `0660`;
- `connect()` succeeds.

The runner drops the connection, because the peer is not the controller UID. Health never takes a lock, so it cannot race a starting runner. Nothing depends on it. (As built, the Docker healthcheck cannot run the entry: see section 16.3, item 1.)

## 7. Paths

The defaults apply to a **new** host. On an existing host the paths come from its `service.json` (section 8.2).

| Item | Default host path | Owner, mode | Created by |
|---|---|---|---|
| Configs | `/etc/portal-app-runner/{service,bootstrap}.json` | `root:root 0600`, dir `0700` | host-setup |
| Runner state parent | `/var/lib/portal-app-runner` | root-owned, not group- or world-writable | host-setup |
| Journal | `/var/lib/portal-app-runner/journal` | `root:root 0700` | host-setup (the files: bootstrap) |
| Image scratch (`image_root`) | `/var/lib/portal-app-runner/images` | `root:root 0700` | host-setup |
| XFS image files | `/var/lib/portal-app-runner/volumes/{data,checkpoint}.img` (anchan: `/data/portal-runner/`) | `root:root 0600`, dir `0700` | host-setup through provision |
| Data root | `/srv/portal-runner/data/root` | XFS, `root:root 0700` | provision |
| Checkpoint root | `/srv/portal-runner/checkpoint/root` | XFS, `root:root 0700` | provision |
| Control root | `/var/lib/portal-app-runner/control` (was `/run/...`) | `root:<gid> 0750` | host-setup |
| Control socket | `<control_root>/runner.sock` | `root:<gid> 0660` | serve |
| IPC root | `/run/portal-app-runner/ipc` | `root:root 0700` | serve at startup (new, P1) |

- **The control root moves to durable storage** (Q4). After a reboot it exists before either container starts.
- **The IPC root stays on `/run`.** Its per-generation tmpfs mounts last one boot.
  - At startup, serve creates a missing `ipc_root` as `root:root 0700`, and refuses one with any other owner or mode.
  - A missing parent directory is created `root:root 0755`.
  - An existing parent is accepted when it is root-owned and not group- or world-writable, the same ancestor rule every root uses.
  - So doc 146's layout, `/run/portal-app-runner` as `root:<controller-gid> 0750`, is accepted unchanged, and so is a fresh `0755`. This closes problem 3.
- **Stale control socket (new, P1, Q5).** serve removes a leftover socket at `control_socket` only when both hold:
  - it holds the journal's exclusive lock;
  - `lstat` shows a socket owned `0:<controller_gid>` with mode `0660` and link count 1.

  Anything else at that path is refused, as today (`transport_linux.go:167`). This closes problem 4.

## 8. host-setup

```sh
docker compose run --rm app-runner host-setup                 # plan only; takes no lock
docker compose run --rm app-runner host-setup --apply [flags]
```

- **Profile.** `docker compose run` activates the profile of the service it targets, so host-setup works before `.env` enables the profile.
- **`--rm` is required.** A stopped setup container would appear in `containers/json?all=1`.
- **Docker.** The host must already have a Docker daemon that passes the profile check.

### 8.1 Ownership: what host-setup decides and what it leaves alone

host-setup owns only these items:
- the `service.json` fields that name **paths** (`docker_socket`, `journal_dir`, `data_root`, `checkpoint_root`, `image_root`, `ipc_root`, `control_root`, `control_socket`);
- `controller_uid` and `controller_gid`;
- the `controller` block and `format`;
- the whole of `bootstrap.json`.

Every other field is the operator's. That covers `capacity` (including a raised `disk_bytes`), `network_*`, `minimum_reported_*`, `private_networks`, `allow_local_reference_images` and doc 186's pinned destinations.
- **New config.** host-setup writes those fields once, from defaults and provision's suggested `disk_bytes`.
- **Existing config.** It never rewrites them.
- **Comparison.** It compares the parsed values of the fields it owns, never the bytes.

### 8.2 Modes

- **New host** (no `service.json`):
  - It provisions everything in section 7 from defaults or flags.
  - Without `--enrollment` it writes no config, reports `enrollment pending` and exits 3. The document is the one Portal produces when an administrator prepares a runtime binding (29, 146).
- **Adopt** (`service.json` exists): the existing `service.json` and `bootstrap.json` are the source of truth.
  - **Flags.** A path or ID flag that disagrees with the parsed existing value refuses. Omitted flags take the existing values.
  - **Volumes.** For each root it verifies the existing mount with the runner's own checks (XFS, flags, quota, identity). It recreates nothing.
    - **Loop image:** for a loop-mounted image, found through `findmnt` and `loop/backing_file`, it checks the image with provision's rules and adds the fstab line if it is missing.
    - **Other volume:** a partition or LV that the operator mounted is accepted as it is, with no fstab change.
    - **Missing root:** a root that is not mounted refuses. host-setup does not guess which image belongs to it.
  - **Enrollment.** An existing enrollment is recognised: the journal is enrolled with the `controller` block of `service.json`, which is read through the journal's read-only open. Then no `--enrollment` is needed, and the exit status is 0. An `--enrollment` that differs from the enrolled identity conflicts, as `bootstrap` does.
  - **Directories.** It creates only what is missing, which in a systemd migration is the new control root.

### 8.3 Steps

1. **Preflight. This step only reads.** It refuses unless all of these hold:
   - **Namespaces and user:** it runs in the host mount, network and PID namespaces, as euid 0.
   - **Kernel:** at least 5.14.
   - **Docker:** the profile check passes, and the daemon is identified in the runner's mount namespace.
   - **Host tools:** these are present in the host's `/usr/sbin:/usr/bin:/sbin:/bin`: `losetup`, `mkfs.xfs`, `mount`, `umount`, `fallocate`, `blkid` and `findmnt`, plus `chattr` for btrfs (Q2).
   - **The runner is stopped.** It must be able to take the journal lock and the data-root lock without waiting.
     - If it cannot, it refuses with `the runner is running: stop it first (docker compose stop app-runner, or systemctl stop portal-app-runner)`.
     - It takes those locks only with `--apply`, and holds them until it exits, so a runner that starts meanwhile exits with "in use".
     - The plan-only mode takes no lock, so it is safe while the runner is serving.
   - **The controller IDs are acceptable** (section 8.4).
2. **Plan.** It prints every item it would create, every item that already matches, and every operator-owned field it leaves alone. Without `--apply` it stops here and exits 0.
3. **Directories.** It creates each missing directory in section 7 with the exact owner and mode. Every ancestor must be root-owned and not group- or world-writable.
4. **XFS volumes** (new host only). It calls doc 182's `provision` package; its rules are unchanged. The defaults are data 8 GiB and checkpoint 16 GiB. A checkpoint below 16 GiB refuses unless `--checkpoint-size-below-production` is given (185 Q8).
5. **fstab.** It adds provision's rendered lines when they are missing.
   - **How it writes:** it writes a temporary file in `/etc`, checks it with `findmnt --verify --tab-file`, fsyncs it and renames it over the file. The first time only, it keeps `/etc/fstab.before-portal-app-runner`.
   - **systemd:** on a host where `/run/systemd/system` exists, it then runs `systemctl daemon-reload`, so systemd's generated mount units match the new fstab. Without that, systemd keeps stale units until the next boot and warns about the edit. host-setup runs in the host mount namespace, so this is the host's own `systemctl` talking to the host's PID 1.
6. **Configs** (new host only). It writes `service.json` and `bootstrap.json`, mode `0600`, from defaults, provision's roots and `disk_bytes`, the IDs and the enrollment document.
7. **Enrollment.** It runs the existing `bootstrap` code path, which is idempotent; an identity that differs conflicts.
8. **Report.** It prints the `.env` lines, and does not write them, because the compose project directory is not known to the runner:

   ```text
   COMPOSE_PROFILES=app-runner
   PORTAL_VERSION=<the release tag>
   PORTAL_UID=41001
   PORTAL_GID=41001
   RP_APP_RUNNER_CONTROL_DIR=/var/lib/portal-app-runner/control
   RP_APP_RUNNER_SOCKET=/var/lib/portal-app-runner/control/runner.sock
   RP_APP_RUNNER_CONTROLLER_UID=41001
   RP_APP_RUNNER_CONTROLLER_GID=41001
   ```

### 8.4 Controller IDs and the host user database

- **The runner uses numeric IDs only**, so host-setup never writes `/etc/passwd` or `/etc/group`. It reads them as plain files. LDAP and other NSS sources are not seen, and the plan says so.
- **Adopt.** The IDs in the existing `service.json` are accepted whatever the host database says, because they were decided when the host was set up. A systemd host's `portal-app-runner` group, the unit's `Group=`, therefore never blocks migration or rollback.
- **New host.** The requested IDs:
  - **Accepted:** an ID that no host account uses.
  - **Accepted:** a group named `portal-app-runner` (doc 146's name) whose GID equals `controller_gid`.
  - **Refused:** an ID held by any other user or group. The message names the account.
- **Naming the GID by hand is not suggested.** A group added later that holds `controller_gid` does not affect any later run: adopt mode never consults the database for configured IDs.

### 8.5 Refusal rules

- **Conflicts.** host-setup refuses, names the item and the difference, and changes nothing when any of these holds:
  - an owned field disagrees with the value it would use;
  - a directory has another owner or mode;
  - an fstab line names the same mount point or image with other fields;
  - an image has another size or type;
  - a mount point is busy;
  - an ID conflicts.

  Operator-owned fields never conflict. Running it again after success changes nothing and says so.
- **It never deletes, unmounts or detaches** anything it did not create in the same run (doc 182's rule).
- **It never edits** `/etc/passwd`, `/etc/group`, `/etc/docker`, systemd unit files, ufw or packages. `systemctl daemon-reload` reloads the unit files; it changes none.
- **It never writes** Portal's files, `.env` or volumes.

**What it writes:**
- `/etc/fstab`, plus a one-time backup;
- `/etc/portal-app-runner/`;
- `/var/lib/portal-app-runner/`;
- the image directory;
- `/srv/portal-runner/`;
- loop devices, through `losetup`;
- the journal, through bootstrap.

**What it reads:** `/etc/passwd`, `/etc/group`, `/proc`, `/sys` and the Docker socket.

## 9. Upgrade

**Version skew is not detected today.**
- **The format strings are protocol versions, not release versions.** The `1-dev.1` strings on each surface change only when that protocol changes, so two different releases usually carry the same strings.
- **Machine support is a capability probe** (`app_platform_machine_support.go:35-70`), not a version check.
- **So the guard is the deployment.** One `PORTAL_VERSION` selects both image lines.

**The rules:**
- Always upgrade the whole project: `docker compose pull && docker compose up -d`, with no service names.
- Never `docker compose pull report-portal` or `up -d report-portal` alone across a version change.
- The handbook and the compose header say so.
- Q13 asks whether to add a real release-identity check.

**Floating tags (the repo default).**
- **Restart.** Both containers are recreated when their image changed. The runner gets SIGTERM, stops within 30 s and starts again.
- **Effect on apps.** App containers keep running, but backend requests and broker calls fail for those seconds, as with a systemd restart today. Journalled work resumes from its claims.
- **Promotion.** `release-channels.yml` moves both channel tags in one job, after verifying both digests against the same release (section 12).
- **The remaining gap.** A pull that lands between the two tag writes can mix releases for those seconds. Production avoids it with fixed tags.

**Production (anchan).**
- **The compose file.** It stays one file, because aaPanel's compose page broke on multi-file projects. It pins `PORTAL_VERSION` to a fixed tag with `pull_policy: missing`.
- **The procedure.** The existing deploy procedure applies, with the tag edit now made once in `.env`, plus three checks:
  - the runner digest against `release-metadata.json`;
  - `app-runner ready` in `docker compose logs app-runner`, with `version` showing the expected tag;
  - the managed profile in the admin console.

**Journal upgrades.**
- A newer runner upgrades the journal on open, as v4 and v5 did.
- An older runner refuses a newer journal.
- So when a release's notes name a journal schema change, that release is the rollback limit (section 10.3).

## 10. Migration from systemd and rollback

This applies to a host that already runs app-runner under systemd. No data moves. The compose runner reads the same configs, journal, roots and images in the host mount namespace, and host-setup runs in adopt mode.

### 10.1 Before

1. Use the release whose image matches the installed binary, or upgrade the binary first. One version change at a time.
2. Record:
   - `systemctl status portal-app-runner` and the ready line;
   - the installations and their states in the admin console;
   - `findmnt` for both roots.
3. While the unit is still running, run `docker compose run --rm app-runner host-setup`. It is plan only and takes no lock.
   - **Expected.** It adopts: every root, image, directory and the enrollment match. The only planned items are fstab lines, if they were never installed.
   - **Control path.** It also reports that the configured `control_root` is under `/run`. It reports this and does not refuse, because `/run` still works; the step below moves it.
   - **Anything else.** Any other refusal or plan stops the migration here.

### 10.2 Switch

1. Edit `control_root` and `control_socket` in `service.json` to the durable path. This is the one edit to an owned field, and the operator makes it. host-setup prints it and never rewrites an existing file.
2. `systemctl stop portal-app-runner && systemctl disable portal-app-runner`. The old socket is removed on a clean stop, and app containers keep running.
3. `docker compose run --rm app-runner host-setup --apply`.
   - **What it creates.** The new control root as `root:<controller_gid> 0750`, plus any missing fstab lines with `daemon-reload`. No `--enrollment` is needed, because the enrollment is recognised (8.2).
   - **What it leaves.** `/run/portal-app-runner`, still `root:<gid> 0750` from 146, stays as it is and is accepted by serve (section 7).
4. Add the printed `.env` lines. `PORTAL_UID`, `PORTAL_GID` and the `RP_APP_RUNNER_CONTROLLER_*` values are the IDs from the existing `service.json`.
5. `docker compose pull && docker compose up -d`, then check:
   - `app-runner ready` in the logs;
   - `health` passes;
   - the admin console shows the same runtime identity;
   - each enabled backend answers.

**Safety net.** The journal's exclusive `flock` and the data root's own lock refuse a second runner, in both directions. A unit still running makes the compose runner exit with "in use", and makes host-setup `--apply` refuse.

### 10.3 Rollback

1. `docker compose stop app-runner`, then remove `app-runner` from `COMPOSE_PROFILES`.
2. Keep the durable control path, which works for the unit too, or revert the two fields. Set Portal's `RP_APP_RUNNER_*` to match.
3. `systemctl enable --now portal-app-runner`.
   - **Binary.** Use the tarball binary from the **same release** as the image.
   - **Group.** The unit's `Group=portal-app-runner` is still on the host; nothing removed it.
   - **Journal schema.** Crossing a journal schema change is refused by the older binary, as today. Going further back is the 168 restore path, not a rollback.
4. `docker compose up -d`, then check as in 10.2.

The journal, the installed applications and their `/data` are never copied, so neither direction can lose them.

### 10.4 Fresh host (anchan)

1. **Precheck on the host.** First check:
   - the kernel is 5.14 or later;
   - cgroup v2;
   - the Docker profile;
   - `xfsprogs` is installed;
   - UID and GID 41001 are free;
   - whether aaPanel's compose page reads `.env` and `COMPOSE_PROFILES`.

   If aaPanel does not read them, anchan's host-specific compose file drops the `profiles:` key and states its values literally, and the procedure is the CLI one: `cd /data/report-portal && docker compose …`.
2. **Install the compose file** with the new service, then start Portal: `docker compose up -d report-portal`. On anchan, Portal's database is the host's.
3. **Get the enrollment document.** In **Manage → Apps → Publishers**, press **Initialize application platform**, then **Prepare runtime binding** on the **Application runner** card, and save the document on the host (`GET /api/admin/app-platform/runtime`, `POST /api/admin/app-platform/control/runtime` with `prepare`).
4. **Provision.** Run `host-setup` (plan only), then:

   ```sh
   host-setup --apply --image-dir /data/portal-runner --data-size 8G --checkpoint-size 16G --enrollment …
   ```
5. **Start.** Add the `.env` lines, which change `PORTAL_UID` and `PORTAL_GID` to 41001 (Q3), then `docker compose up -d`.
6. **Confirm** the runtime: **Confirm runtime** on the same card (`confirm` on the same route).

## 11. Test plan (these must fail first)

Each condition says which way its guard fails. All of them are refusals, not fallbacks. The native tests run in `app-runner-probe.yml` (rootful Docker, path-filtered), so no new CI load lands elsewhere.

**Entry:**
- Without `pid: host`, or without `network_mode: host`, the entry exits non-zero before the runner runs. Fails closed.
- With both, the runner's `/proc/self/ns/mnt` equals PID 1's, `/proc/self/root` is `/`, and its umask is `0077`.
- The descriptor named by `PORTAL_APP_RUNNER_EXEC_FD` is close-on-exec in the runner, and a child started by host-setup does not hold it.
- If the variable names a descriptor that is not the runner's own executable, the runner exits non-zero. Fails closed.

**Runner startup (new behaviour):**
- **Fail fast on the mount namespace.** serve refuses before binding the socket when the daemon is not identified in its mount namespace (run without the entry, in a plain privileged container). Fails closed.
- **Fail fast on the network namespace.** serve refuses when dockerd is in another network namespace. Fails closed.
- **The IPC root:**
  - a missing `ipc_root` is created as `root:root 0700`;
  - an existing one with another owner or mode refuses;
  - a parent of `root:<gid> 0750` (146's layout) and one of `root:root 0755` are both accepted;
  - a group-writable parent refuses.

  Fails closed.
- **Stale socket.** A socket that is `0:<gid>`, `0660` is removed only while serve holds the journal lock. These are each refused and left in place, failing closed:
  - a regular file;
  - another mode or owner;
  - any socket while a second runner holds the lock.
- **Primary GID 0.** A runner whose primary GID is 0 produces exactly the owners and modes the checks require on the journal, the image scratch, the IPC directories and the socket. This is a parity test against the unit's `Group=`.
- **No cpuset.** With a `cpuset` on the container and `reserved_cpus` > 0, the runner is not ready. That fails closed, as today. A compose lint asserts the file sets no `cpuset`, `cpus` or memory limit for the runner.
- **No listener.** The runner owns no TCP or UDP listening socket (`ss -lntup` filtered to its PID).

**Overlap guard (regression).** A container that bind-mounts the data root makes a writable candidate refuse with the overlap error. Fails closed.

**Portal:**
- **UID mismatch.** If `PUID` differs from `RP_APP_RUNNER_CONTROLLER_UID`, Portal logs "endpoint is unavailable" and managed execution stays off. Fails closed.
- **All three variables empty.** Static apps work, and no runner is contacted.
- **Read-only bind.** Creating a file in the bound directory gets `EROFS`, and connecting still works.
- **Missing source.** A missing control-directory source stops the Portal container from being created, with an error naming the path. Fails closed.

**host-setup, new host:**
- **Plan only.** The plan changes nothing; the host state is hashed before and after.
- **First apply.** `--apply` creates every item in section 7.
- **Second apply.** A second `--apply` reports no changes and exits 0.
- **Each conflict refuses and changes nothing:**
  - a `0755` journal directory;
  - an fstab line for the mount point with other options;
  - an image of another size;
  - a UID held by another host account.
- **A matching `portal-app-runner` group is accepted.**
- **A small checkpoint size.** Below 16 GiB without the override, it refuses.
- **A missing host `mkfs.xfs`.** It refuses in preflight, before any change, and names the package.
- **No enrollment.** Without `--enrollment` it exits 3 and writes no config.
- **A failed `findmnt --verify`.** `/etc/fstab` is left untouched.
- **Reload.** `daemon-reload` runs after a successful fstab change, and only then.

**host-setup, adopt (the systemd layout from 146 and the shipped example):**
- **The shipped example layout.** It uses data root `/srv/portal-apps/data`, checkpoint root `/var/lib/portal-app-runner/checkpoints`, group `portal-app-runner` and an enrolled journal. The plan reports every item as matching, and `--apply` without `--enrollment` exits 0.
- **Operator edits.** After raising `capacity.disk_bytes` or adding 186 pinned destinations, the next run still reports no changes.
- **A disagreeing path flag** refuses.
- **An `--enrollment` with another identity** conflicts.
- **An unmounted configured root** refuses.
- **A running runner.** With the runner running, `--apply` refuses with the "runner is running" message, and the plan still works. A runner started during `--apply` exits with "in use".

**Lifecycle (native):**
- **Setup.** Run host-setup with small sizes, a fixture enrollment, then `up -d`. The controller-UID container observes the managed profile.
- **Restart.** `docker compose restart app-runner` keeps the journal and enrollment, and a running candidate survives.
- **Down.** `docker compose down` leaves app containers running and removes the control socket.
- **Health with the runner stopped.** `docker compose run --rm app-runner health` exits non-zero.
- **Health status.** It becomes `healthy` in `docker inspect` within `start_period` after `app-runner ready`.
- **Docker restart.** After `systemctl restart docker`, with `live-restore` off, the runner returns to ready. With it on, the runner keeps serving and its next Docker call passes identification.
- **Reboot.** After a reboot (or a simulated one: unmount, detach, clear `/run`), `docker compose up -d` returns the runner to ready with no manual step.
- **Migration.** From a systemd fixture: migrate per 10.2, then roll back per 10.3. The same installation answers at each step, and its `/data` checksum is unchanged.

**Release:**
- The runner image job refuses to replace an existing fixed tag.
- It refuses to push when the tag has moved off the setup commit.
- `publish` does not run when the runner image job failed.
- `verify-target` refuses a release whose metadata lacks a valid runner digest for a release that should have one, or whose runner tag no longer matches its recorded digest.

## 12. CI and release changes

- **`docker-runner` job in `release.yml`.**
  - **Needs.** It needs `[setup, build-runner, release, smoke-arm64]`, the same chain as `docker` (`release.yml:695`). That chain runs through `tag` (`:248`, which needs `ci`, `build`, `build-runner` and `smoke-arm64`) and `release` (`:520`).
  - **Tag recheck.** Before pushing, it runs `docker`'s "Recheck the immutable tag before image publication" step, refusing if the tag no longer points at `setup.outputs.sha`.
  - **No replacement.** It also runs `release_guard.py guard` on `<runner image>:<tag>`.
  - **Build and push.** It builds `Dockerfile.app-runner` for `linux/amd64,linux/arm64` with the same build method and pinned actions as `docker`. It pushes only the fixed tag, with its own `buildcache`, and exposes its digest as a job output.
  - **Tarballs stay,** for the systemd alternative (Q8).
- **Exercising the image before publication.**
  - **The arm64 binary** is already started by `smoke-arm64` (`release.yml:455`) before the tag exists.
  - **The images.** After pushing, `docker-runner` runs each arch's image by digest:
    - **amd64:** on the job's runner;
    - **arm64:** in a follow-up step on `ubuntu-24.04-arm`, the runner `smoke-arm64` uses.

    Each run executes `version` through the entry, with `--privileged --pid host --network host`. It checks:
    - that the entry reaches the runner in the host mount namespace;
    - that the printed tag and commit match;
    - that the entry refuses without `--pid host`.
  - **Gate.** A failure fails the job, so `publish` never runs.
- **Metadata.** The `release-metadata.json` step moves out of `docker` into a small `record-metadata` job that needs `[setup, docker, docker-runner]`.
  - **Format.** The file gains a per-image map: `{tag, commit, recorded_at, images: {"portal": {image, digest}, "app-runner": {image, digest}}}`.
  - **Compatibility.** The legacy top-level `image` and `digest` keep Portal's values, so older readers and older releases still validate.
- **`publish`.** It needs `[setup, docker, docker-runner, record-metadata]`, so a release is never published, and so never promoted, without its runner image.
- **`release_guard.py`.**
  - **Checks.** `validate_metadata` checks each image entry. `verify-target` takes the image key and verifies each image's fixed tag against its recorded digest, refusing on any mismatch.
  - **Older releases.** A release from before this change has no `images` map. It is verified for Portal only, and channel promotion moves only the Portal tag for it.
- **`release-channels.yml`** (`:206`). Verification runs for both images of the target release, and only then are both channel tags pointed, by digest, in the same job.
- **`build-runner`** stamps the version and commit with `-X`. `app-runner version` and the ready line print them.
- **`app-runner-probe.yml`** runs the native tests of section 11. Its assertion that `portal.env.example` matches `service.example.json` (`:71`) moves to the new control path.

## 13. Open questions (recommended answers)

Q1. **Join the host mount namespace instead of running on same-path binds (request item 2).** Recommended: accept. Section 2 shows that binds fail the daemon-identity and overlap guards, and that making them pass would weaken both. With one mount namespace, Docker resolves every path identically, and no propagation flags are needed.

Q2. **Where the XFS tools come from.** Recommended: the host's `xfsprogs` and `util-linux`.
- **Why not the image's.** After the namespace switch, provision's fixed lookup in `/usr/sbin:/usr/bin:/sbin:/bin` resolves in the host's filesystem, so the image's copies are not reachable without code changes.
- **Running in the container instead** would create loop devices and mounts from the container's mount namespace. That pins the namespace and leaves `loop/backing_file` unreadable from the host, which the runner's checks read.
- **On Ubuntu Server** both packages ship. Preflight names any that are missing.

Q3. **Default controller UID and GID.** Recommended: **41001**, the value in the shipped examples, with Portal's `PUID` and `PGID` set to match.
- **Why not 10001.** It is a common non-root UID inside third-party images. Without userns remapping, such a process presents the controller's UID if it ever reaches the socket. Signed intents still guard every command, but a distinctive ID keeps the peer check meaningful.
- **Cost on anchan.** Portal's files are re-owned once by its entry, and the backup step's `docker exec -u` changes to `41001:41001`.
- **Alternative.** Keeping 10001 avoids that change, at the cost above.

Q4. **Durable control directory** `/var/lib/portal-app-runner/control`. Recommended: accept.

Q5. **Stale socket removal under the journal lock.** Recommended: accept.

Q6. **A compose profile**, enabled by `COMPOSE_PROFILES=app-runner` in `.env`. Recommended: accept for the repo file. anchan follows its P4 precheck (10.4 step 1).

Q7. **Image name and tags.** Recommended: `ghcr.io/kazuhahub/stockanalysisprediction-report-portal-app-runner`, with exactly Portal's tags, selected by one `PORTAL_VERSION`.

Q8. **Keep the systemd unit** as a documented alternative. Recommended: keep it. It uses the same binary and configs, `host-setup` runs directly on a host too, and it serves the VM recipe (handbook 4.4). The handbook leads with compose.

Q9. **`health` subcommand** (socket check plus `connect`, no lock). Recommended: add it, and make nothing depend on it.

Q10. **Fail fast on a namespace mismatch** at serve startup. Recommended: accept.

Q11. **Resource limits on the runner container.** Recommended: none beyond `nofile`, as under systemd.

Q12. **Static installs need one `mkdir app-runner-control`,** because of `create_host_path: false`. Recommended: accept, and add it to the zero-clone header.
- **Effect.** A host that replaces its compose file without making the directory gets a clear start error, not a wrong directory.
- **Docs and examples.** Handbook §4 leads with compose and host-setup. Doc 146 gains a compose section and the new control path. `service.example.json` and `portal.env.example` take the durable path. `deploy/` keeps the unit.

Q13. **A real Portal-to-runner release check.** Recommended: not in this change. One `PORTAL_VERSION`, whole-project upgrades and fixed production tags cover it. If wanted later, the runner would advertise its release tag on the machine-support channel and Portal would show a mismatch in the admin console. That is a protocol addition with its own review.

## 14. Schema change — needs owner approval

- **Database and journal:**
  - Portal database: no migration.
  - Runner journal: no schema or format change.
- **Runner configs.** The `service.json` and `bootstrap.json` formats (`portal.app-runner.config/1-dev.1`, `portal.app-runner.bootstrap/1-dev.1`) are unchanged, with no new field. Only example values change: the control path.
- **One release-artifact format changes, and needs approval.** `release-metadata.json` gains the additive `images` map (section 12). It is not a database or journal schema. Older readers keep working through the legacy keys, and older releases validate as Portal-only.
- **New `.env` keys** are deployment settings.

If implementation shows any other format must change, it comes back to the owner first.

## 15. Phasing

- **P1, runner code.**
  - New subcommands: `host-setup` (new, adopt, plan and apply), `health` and `version`.
  - serve changes: the fail-fast namespace check, `ipc_root` creation, stale socket removal, and close-on-exec for the entry's descriptor.
  - The section 11 tests that need no compose.
- **P2, image and release.**
  - `Dockerfile.app-runner` and the entry.
  - The release jobs: `docker-runner`, `record-metadata`, the image smoke, `publish` needs, and the guard and channel changes.
  - The probe workflow's native tests.
- **P3, deployment files and docs.**
  - `docker-compose.yml`: the profiled service and one `PORTAL_VERSION`.
  - The `deploy/app-runner/` examples.
  - Handbook §4 and doc 146.
- **P4, anchan,** only when the owner asks, per the release and deploy boundaries. The 10.4 precheck comes first, then the fresh-host procedure.

P1 to P3 ship as separate PRs. P2 depends on P1 for the entry's subcommands.

## 16. Implementation notes

Built on top of the doc 186 branch (`feat/app-platform-proxy-pins`), as local commits on `feat/app-runner-compose`. No Portal database migration, no runner journal change and no `service.json` or `bootstrap.json` format change was needed; the one release-artifact format change is the `images` map of section 12, as approved.

### 16.1 What was built

- **P1, runner code.**
  - `app-runner host-setup`, `health` and `version` (`cmd/app-runner`). The planning, adoption and refusals live in `internal/apprunner/hostsetup`, with every effect behind an `Env`, so the whole flow is tested against a temporary root with fakes.
  - `serve`: the fail-fast namespace check (`driver.CheckDaemonNamespaces`: mount namespace first, then network), `ipc_root` creation (`service.ensureIPCRoot`), stale control socket removal under the journal lock (`transport.RemoveStaleControlSocket`, which takes the `*journal.Store` as the proof that the lock is held), the `in use` refusal text, and the entry descriptor handling (`internal/apprunner/hostns`).
  - The ready line ends with `version=<tag> commit=<short sha>`. The version and commit are appended, not inserted, because the probe workflow and the image-delivery evidence parser read the line from `app-runner ready profile=`.
- **P2, image and release.** `Dockerfile.app-runner`; `deploy/app-runner/portal-app-runner-entry`; the `docker-runner` job; `smoke-runner-image-arm64`; `record-metadata`; `publish` needing all of them; `release_guard.py` per-image verification; `release-channels.yml` verifying both images before either channel tag moves; the runner binary stamped with the release identity; and `scripts/probe-runner-image.sh`, which runs each architecture's image by digest before anything is recorded.
- **P3, deployment files and docs.** The profiled `app-runner` service and the one `PORTAL_VERSION` in `docker-compose.yml`; the examples at the durable control path; handbook 4.0 to 4.7; doc 146; the unit kept, with a header naming it the alternative; `deploy/app-runner/compose.env.example`.
- **P4.** Not done and not touched. The procedure is handbook 4.7 (section 10.4 of this document, made executable).

### 16.2 Decisions the implementation took

1. **host-setup flags.** The text says "flags" without naming them. They are `--apply`, `--enrollment FILE`, `--config-dir`, `--release`, `--controller-uid`, `--controller-gid`, `--docker-socket`, `--journal-dir`, `--image-root`, `--ipc-root`, `--control-root`, `--data-root`, `--checkpoint-root` and, for a new host, `--image-dir`, `--data-size`, `--checkpoint-size`, `--checkpoint-size-below-production`. Zero or empty means "not given": a new host takes the default, an adopted host keeps its value, and a given flag that disagrees refuses. On a new host `--data-root` and `--checkpoint-root` must be `<mount point>/root`, because provision keeps each volume's own root an ordinary mount point.
2. **The enrollment document** is the public bootstrap document Portal produces (`service.ParseEnrollment`, as strict as `LoadBootstrapConfig`). Its `journal_dir`, if present, is ignored: host-setup owns where the journal lives and writes `bootstrap.json` with its own.
3. **Adopt and `bootstrap.json`.** An existing `bootstrap.json` is compared with the controller identity of `service.json` (a conflict if it differs) and never rewritten. A missing one is not created, because once the journal is enrolled nothing reads it.
4. **The plan reads the enrollment without the owner lock,** through a new read-only reader, `journal.ReadControllerDeployment`. The text assumed the journal had one; it had only the internal probe. The reader writes, creates and upgrades nothing, and refuses a directory that is not private.
5. **The plan while the runner serves.** The runner's own data-root checks take the data root's lock, so a plan that finds the root locked records "held by a running runner, which passed the runner's own checks when it started" instead of failing. `--apply` takes the locks first and refuses with the section 8.3 message.
6. **Locks and order.** `--apply` plans first (every conflict refuses before anything changes), then takes the journal and data-root locks that can be taken (a new host takes them as soon as the directories and volumes exist), then runs the steps in order: directories, volumes, fstab, `daemon-reload`, configuration files, enrollment. `daemon-reload` runs only after a fstab change this run made.
7. **fstab matching** compares the parsed fields: the same image, mount point and `xfs`, with `loop,prjquota,nodev,nosuid,noexec` present. Extra options (`nofail`, an operator's own) do not conflict. A line that names the mount point or the image with anything else does. `findmnt --verify` warnings alone pass; any error fails and `/etc/fstab` is untouched.
8. **New-host `service.json` defaults** are the shipped example's, with `capacity.disk_bytes` from provision's suggestion (`RecommendedDiskBytes` of the measured usable sizes) and `network_entries` 32 (16.3, item 2).
9. **The `.env` report** prints `RP_APP_RUNNER_SOCKET` as the path **inside Portal's container** (`/var/lib/portal-app-runner/control/runner.sock`, the compose bind target) whatever the host's control root is, and a placeholder comment for `PORTAL_VERSION` when the runner build is not a release tag.
10. **Release metadata.** `verify-target` takes `--image-key portal|app-runner|all` (default `portal`, so older callers are unchanged) and always verifies **every recorded image's** fixed tag against its digest, so neither channel tag can move to half a release. A release that declares an `images` map must carry exactly `portal` and `app-runner`, each `{image, digest}` with the path its key implies, and its portal entry must equal the legacy keys. A release without the map is Portal-only, and its runner key yields nothing to point at. `scripts/recover-release-metadata.py` still records the legacy, Portal-only shape: a failed `record-metadata` job is re-run, not recovered.
11. **Test hooks.** The entry reads `PORTAL_ENTRY_PROC`, `PORTAL_ENTRY_RUNNER` and `PORTAL_ENTRY_NSENTER`, so `scripts/app_runner_entry_test.sh` runs against a fake `/proc` and `nsenter` without privilege, and `probe-runner-image.sh` swaps the program for the image's shell to read the namespace the entry really lands in. The compose file never sets them.
12. **Silent not-ready returns.** Every remaining silent refusal of `serve` (the roots, the identity, the Docker profile, capacity, the image scratch, the container runtime, the control endpoint) now logs a path-free reason. They were found because one of them hid the defect in 16.3, item 2.
13. **Deployment drills.** `scripts/acceptance/ops_deploy_drills.py` pins the one Portal image line of the compose file. Its pattern now excludes the `-app-runner` image, and it makes `app-runner-control` beside the compose file, as the header asks.

### 16.3 Deviations from the text above, and defects found

1. **The healthcheck cannot run the entry.** `docker exec` joins the mount namespace of the container's init, and the init is the runner, in the host's mount namespace. The image's files, including `/usr/local/bin/portal-app-runner-entry`, do not exist there (the first VM run showed `no such file or directory` and an `unhealthy` status). As built, the healthcheck is `CMD-SHELL` and uses only the host's `sh`, `sed` and `cat`: it reads the container's cgroup from `/proc/self/cgroup`, finds the runner among `cgroup.procs` by `readlink /proc/<pid>/exe`, and runs `/proc/<pid>/exe health --config ...`. It needs cgroup v2, which the runner's profile already requires. `docker compose run --rm app-runner health` (a fresh container through the entry) is the same check, and is what an operator runs. Nothing depends on the status, as designed.
2. **A shipped example the runner refuses.** `service.example.json` (and the proxy-pins example) said `network_entries: 1024`. `LoadConfig` accepts 16 to 4096, but the broker's pools refuse more than `MaxNetworkPoolEntries` (32) when the runner starts, so a runner started from the example loaded its configuration and then never became ready (`ErrDenied` from `newNetworkProvider`, silent until 16.2 item 12). The examples and the host-setup default now say 32, and a test holds the shipped examples inside the pool bounds. `LoadConfig`'s range is left as it was, because a stricter range changes what an existing configuration may say; lowering it to 16 to 32 is a one-line follow-up for the owner to decide.
3. **The arm64 image check is a job,** `smoke-runner-image-arm64` (a step cannot change runner). `record-metadata` and `publish` need it as well as `[setup, docker, docker-runner]`, so a runner image that cannot start on arm64 is never recorded, published or promoted.
4. **The release job also refuses** to replace draft assets when either fixed image tag already exists (the Portal guard it already had, plus the runner's).
5. **Not covered by an automated test.** Portal's UID mismatch needs no new test: `bindPlatformBackendHTTP` already refuses a euid that is not the controller UID, and the all-empty case is already tested. "Down leaves application containers running" was not exercised, because the probe installs no application. The section 11 overlap-guard regression is the existing driver test. Portal's read-only bind and its missing source are verified in the probe with the runner image standing in for the Portal image.

### 16.3a Review fixes after the merge of main

1. **Healthcheck dial.** The driver's structural guard (`TestOnlyTheDockerClientConstructorDialsTheSocket`) rightly refused a net dial in `service/health_linux.go`. The probe now lives with the other dials of the runner's own control socket, `transport.ProbeControlSocket` (`transport_linux.go`, already in the guard's egress list); `health` keeps the owner and mode checks and calls it. The guard's allow-list is unchanged.
2. **`LoadConfig` pool bounds.** `network_active` above `broker.MaxNetworkPoolActive` (8) or `network_entries` above `broker.MaxNetworkPoolEntries` (32) is refused at load with `ErrPoolBounds` (an `ErrConfig`, so callers are unchanged), and `serve` logs the reason. This replaces the follow-up offered in 16.3 item 2; the lower bounds (1 and 16) are as before.
3. **Order of the runner image push.** `docker-runner` builds the amd64 image with `load: true`, runs `probe-runner-image.sh` on it, and only then pushes the multi-arch fixed tag; the pushed amd64 digest is still run afterwards and arm64 stays in its own job (its failure after the push cannot be checked earlier). `release_guard.py guard` now tells a runner tag, or a Portal tag with a runner tag beside it, to re-run the failed jobs of the original run, and `recover-release-metadata.py` refuses to write Portal-only metadata when a runner image exists for the tag. Only a Portal-only tag is still pointed at the recovery script.
4. **Channel moves.** `release_guard.py check-move CURRENT --target TARGET` refuses moving a channel to a release without a runner image while the channel's current target has one (and refuses an unreadable current target); `release-channels.yml` runs it for each channel it is about to set, after verification and before any tag moves. The deliberate override is the `allow_runner_regress` dispatch input.
5. **Update instructions.** The Update line of the compose header says to `mkdir -p app-runner-control` first.
6. **fstab.** A symbolic `/etc/fstab` is refused at the plan (the rename would replace the link); a path with whitespace, backslash, `#`, quotes, control or non-ASCII characters is refused with that reason rather than escaped; `pquota` counts as `prjquota` in an existing line.

### 16.3b Fixes after the first CI run (PR 494)

1. **fstab verification judges only what host-setup adds.** `findmnt --verify` on the whole table failed on a CI worker whose `/etc/fstab` already carries an error of its own. The check now verifies a file holding just the new lines (`--tab-file`), while the full new table is still written beside `/etc/fstab` and renamed over it atomically; an error in the added lines still refuses and leaves `/etc/fstab` untouched, and the existing lines are kept byte for byte.
2. **The network acceptance runs the runner in another network namespace on purpose.** `platformNetworkNamespaceFixture` re-runs the whole data-endpoint acceptance under `unshare --net` (Portal and the runner isolated, dockerd and the mount namespace shared), which the unconditional network-namespace half of the start check refused. Socket activation was not involved (the VM's Docker is socket-activated and passed). The start log now carries the underlying error. What needs a shared network namespace is still checked where it is used (pinned proxy destinations at start, every network volume), so `service.ServeWith(..., ServeOptions{SeparateNetworkNamespace: true})` drops only the start-time network half: the mount namespace stays mandatory (`driver.CheckDaemonMountNamespace`), `Serve` and the command keep the full check, and a test pins that `cmd/app-runner` never references the option. Only that acceptance's child sets it.

### 16.4 Verification

- **Unit and script tests,** all passing: `internal/apprunner/hostsetup` (new host, adopt, every refusal of section 11, run against a temporary root; the shipped example is adopted verbatim), `hostns`, `service` (`ensureIPCRoot`, health, example bounds), `transport` (stale socket), `driver` (namespaces), `journal` (read-only reader) and `cmd/app-runner`; `scripts/app_runner_entry_test.sh`, `scripts/compose_app_runner_test.py`, `scripts/release_guard_test.py` (metadata, `verify-target`, workflow structure) and the ops drill tests. A mutation pass over the guards (the fstab conflict, each stale-socket condition, the journal-lock proof, the `ipc_root` mode and parent rules, the descriptor identity and close-on-exec, the lock acquisition, the plan and apply split, the reload condition, the entry's namespace checks, the metadata checks and the `publish` needs) killed every mutation. `actionlint`, `shellcheck`, `staticcheck 2026.2.1`, `go vet` and `gofmt` are clean on what changed.
- **Disposable VM** (`test-xfs-native`: Ubuntu 24.04.5, kernel 6.8.0-147, Docker 29.1.3 from `docker.io`, Compose 2.40.3, 4 vCPU, 3.9 GB, ext4 root). The image was built on the build host and loaded into the VM; nothing was published.
  - `probe-runner-image.sh`: the entry reaches the program in PID 1's mount namespace with root `/` and umask `0077`; `version` prints the stamped identity; the entry refuses without `pid: host` and without `network_mode: host`.
  - `probe-compose-runner.sh`, every stage passing.
    - **Set-up.** The plan changes nothing. `host-setup --apply` on the fresh VM creates every item of section 7 (4 GiB images, `service.json` with `disk_bytes` 3 GiB, the enrollment) and prints the `.env` lines. A second `--apply` reports no changes and changes nothing. A `0755` journal directory refuses and changes nothing.
    - **Startup.** `up -d` reaches `app-runner ready ... version= commit=` and `healthy`. The runner is in the host mount and network namespaces with root `/`, umask `0077`, 65536 open files, descriptor 3 close-on-exec and no listening socket. The control socket is `0:41001 0660`, and `serve` made `ipc_root` as `0:0 0700`. The managed profile is observed as UID 41001.
    - **Lifecycle.** `restart` keeps the journal and the enrollment. A `SIGKILL`ed runner leaves its socket and the next start removes it. `down` removes the socket, and `health` then fails. After unmounting, detaching and clearing `/run`, mounting from the fstab lines and `up -d` returns to ready with no manual step.
    - **Portal's bind.** It is read-only (`EROFS`), and a missing source stops the container with an error naming the path, without Docker creating it.
  - **Migration on a real systemd unit.**
    - The unit runs on the doc 146 layout (control under `/run`, every directory `root:41001 0750`). The plan adopts it while it serves, matches every item and reports the `/run` control root.
    - `--apply` refuses with the "runner is running" message and changes nothing. A compose runner started beside the unit exits with `in use` while the unit keeps serving.
    - After the unit is stopped, the switch to the durable path creates only the new control root, with no `--enrollment`, and compose reaches ready on the same enrollment. Rolling back to the unit on the same path returns it to ready.
  - **Beyond the probe script.** A `--cpuset-cpus` on the container makes the runner refuse (`capacity: online_cpus_disagree_with_reported`). `systemctl restart docker` with `live-restore` off returns the runner to ready. With it on, the runner container, its PID and its serving socket survive, and a later Docker call is identified.
- **Not run:** the release workflow itself (no tag, no registry write), the arm64 image on real arm64 (it runs in the release's own job), the data checksum of an installed application across the migration (no application was installed), and anything on anchan.
