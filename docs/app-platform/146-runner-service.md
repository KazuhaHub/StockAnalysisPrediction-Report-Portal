# Managed app-runner service

Status: H03 development candidate. This provides a deployable long-running
service boundary for the already-reviewed runner primitives. It does not grant a
stable protocol, production approval, real credentials, package publication or a
market release.

## Explicit setup

Two deployments run the same binary with the same files. The compose runner
(section "Compose deployment" below, doc [187](187-compose-app-runner.md)) is set up by
`app-runner host-setup`; the systemd unit is the documented alternative and is set up by
hand as here. On a unit host the runtime administrator creates only infrastructure:

- a dedicated rootful Linux host or VM;
- the fixed Portal controller UID and runner endpoint GID;
- private existing journal, XFS data-root, distinct XFS checkpoint-root,
  root-only application IPC root and separately provisioned controller-socket
  root with the modes required by the existing journal/data/transport adapters;
- the canonical Docker Unix socket;
- `/etc/portal-app-runner/bootstrap.json` and `service.json`, root-owned and mode
  `0600`;
- the `portal-app-runner` binary and systemd unit from `deploy/app-runner/`.

The controller-socket ancestors must be traversable by the fixed Portal
controller. The reference layout uses the durable
`/var/lib/portal-app-runner/control` as `root:controller-gid` mode `0750` (so it exists
after a reboot, before any container starts), and the service-created `runner.sock` as
`root:controller-gid` mode `0660`. A host set up before doc 187 may keep
`/run/portal-app-runner` and `/run/portal-app-runner/control` as `root:controller-gid`
mode `0750`; the runner accepts that layout unchanged and `host-setup` only reports
that `/run` is rebuilt at every boot. The separate application IPC root, by default
`/run/portal-app-runner/ipc`, is `root:root` mode `0700` and is created by `serve`
when it is missing (below).

Run the enrollment step once:

```sh
/opt/portal-app-runner/app-runner bootstrap \
  --config /etc/portal-app-runner/bootstrap.json
```

The bootstrap document contains only the public controller key, its pinned
SHA-256 fingerprint, deployment ID, controller epoch and control revision. It
never contains a private key. Repeating the exact document is idempotent; changing
its identity or revision conflicts rather than replacing enrollment.

`serve` never enrolls or generates a controller key. It refuses an empty or
changed runner journal.

## Service lifecycle

```sh
/opt/portal-app-runner/app-runner serve \
  --config /etc/portal-app-runner/service.json
```

Startup is ordered:

1. open the existing private journal and verify its exclusive owner;
2. require the exact enrolled deployment/epoch/revision/fingerprint from config;
3. open the pinned XFS project-quota root and verify enforcement/identity;
4. open the distinct pinned XFS checkpoint root and require a different root
   inode identity;
5. inspect the fixed Docker socket and require the native rootful profile plus
   configured reported CPU/memory floors;
6. require the daemon to be identified as a local `dockerd` in the runner's mount
   namespace and its network namespace, and refuse before anything is created or bound
   otherwise (a runner started without the compose entry, or beside another daemon,
   fails here);
7. make the application IPC root: a missing `ipc_root` is created as `root:root` mode
   `0700` (a missing parent as `0755`), an existing parent is accepted when it is
   root-owned and not group- or world-writable (so doc 146's `root:<gid> 0750` and a
   fresh `0755` both pass), and an existing `ipc_root` with any other owner or mode
   refuses;
8. open the constrained candidate runtime;
9. remove a stale control socket and bind the fixed root/controller protected Unix
   endpoint with the bounded data, candidate and network providers. The socket a killed
   runner left is removed only while the journal's exclusive lock is held and only when
   it is a socket owned `0:<controller gid>` with mode `0660` and one link; anything
   else at that path is refused;
10. accept work.

Any failed prerequisite exits non-zero without enabling execution, and says why
without naming a configured path. Static Portal applications do not depend on this
service.

`app-runner health --config` checks that the control socket is a socket owned
`0:<controller gid>` with mode `0660` and that `connect()` succeeds (the runner drops
the connection, because the peer is not the controller UID); it takes no lock.
`app-runner version` prints the release identity the build was stamped with, and the
ready line ends with `version=<tag> commit=<short sha>`.

The journal's exclusive owner lock rejects a duplicate runner process. SIGTERM or
SIGINT cancels the listener and in-flight sessions, then closes native handles,
the data root and journal in reverse order. The systemd unit uses bounded restart
backoff and a start limit instead of an unbounded crash loop; the compose service
restarts `unless-stopped` with Docker's growing delay, and every retry fails closed in
the same startup checks.

## Portal connection

Portal does not accept a runtime endpoint from a browser, app or package. The
deployment sets all three values together:

```text
RP_APP_RUNNER_SOCKET
RP_APP_RUNNER_CONTROLLER_UID
RP_APP_RUNNER_CONTROLLER_GID
```

An absent set leaves managed execution unavailable and static apps unchanged. A
partial or invalid set is logged without paths/details and leaves managed
execution unavailable. A valid set is bound once at startup through the existing
atomic endpoint slot.

## Compose deployment

Doc [187](187-compose-app-runner.md) ships the runner as an image,
`ghcr.io/kazuhahub/stockanalysisprediction-report-portal-app-runner`, with exactly
Portal's tags, as the `app-runner` service of `docker-compose.yml` (profile
`app-runner`). The service is privileged in the host's PID, network, IPC, UTS and
cgroup namespaces with no volumes, and the image's entry joins the **host mount
namespace** with `nsenter` before it executes the runner, from a descriptor it opened
before the switch (`PORTAL_APP_RUNNER_EXEC_FD`, which the runner checks is its own
executable and marks close-on-exec first of all). The runner therefore sees what the
systemd binary sees, and every check above is unchanged.

`app-runner host-setup` sets the host up (plan without `--apply`, adopt of an existing
layout, refusals) and prints the `.env` lines; Portal reaches the control socket
through a read-only bind of the control directory that Docker never creates, with
`PUID` equal to the controller UID. The deployment values of the section above are then:

```text
RP_APP_RUNNER_CONTROL_DIR=/var/lib/portal-app-runner/control   # host side of the bind
RP_APP_RUNNER_SOCKET=/var/lib/portal-app-runner/control/runner.sock   # inside Portal's container
RP_APP_RUNNER_CONTROLLER_UID=41001
RP_APP_RUNNER_CONTROLLER_GID=41001
```

The handbook (section 4.0 to 4.7) has the procedures: set-up, whole-project upgrades
under one `PORTAL_VERSION`, moving a unit host to compose and back, and the fresh-host
procedure.

## Verification boundary

Local tests cover strict JSON with duplicate/unknown/case-alias rejection,
private-file mode and ownership, root/controller bounds, safe paths, enrollment
pin matching, idempotent bootstrap, conflicting enrollment, CLI refusal and the
Portal environment bootstrap. Linux tests compile the concrete root/data/Docker
service path.

On integrated source candidate `e622c7f3`, full test workflow `41049324619`,
quota/browser workflow `41049324669` and app-runner daemon workflow `41049324581`
passed.

The app-runner daemon workflow created a newly owned loopback XFS project-quota
mount, bootstrapped and started the real `serve` command, observed the managed
profile from the controller UID, stopped the service, verified socket removal,
restarted it without replacing the journal or enrollment, observed the profile
again, and confirmed the Portal root endpoint.

The quota/browser workflow now starts the same `service.Serve` boundary inside its
managed Portal fixture. The Portal child completes install review, data
preparation, candidate start, readiness, backend registration/RPC delivery and
current-viewer SDK browser requests through that service. After the child
finishes, the parent stops the service, reopens the journal and XFS root, verifies
the running candidate and writable witness, checks full quota evidence, removes
only owned containers and IPC storage, and confirms the Journal lock is reusable.

Production deployment, external credentials, stable SDK/API freeze and market
publication remain separate decisions.
