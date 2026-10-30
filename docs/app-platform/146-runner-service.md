# Managed app-runner service

Status: H03 development candidate. This provides a deployable long-running
service boundary for the already-reviewed runner primitives. It does not grant a
stable protocol, production approval, real credentials, package publication or a
market release.

## Explicit setup

The runtime administrator creates only infrastructure:

- a dedicated rootful Linux host or VM;
- the fixed Portal controller UID and runner endpoint GID;
- private existing journal, XFS data-root, root-only application IPC root and
  separately provisioned controller-socket root with the modes required by the
  existing journal/data/transport adapters;
- the canonical Docker Unix socket;
- `/etc/portal-app-runner/bootstrap.json` and `service.json`, root-owned and mode
  `0600`;
- the `portal-app-runner` binary and systemd unit from `deploy/app-runner/`.

The controller-socket ancestors must be traversable by the fixed Portal
controller. The reference layout uses `/run/portal-app-runner` and
`/run/portal-app-runner/control` as `root:controller-gid` mode `0750`, and the
service-created `runner.sock` as `root:controller-gid` mode `0660`. The separate
application IPC root remains `root:root` mode `0700`.

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
4. inspect the fixed Docker socket and require the native rootful profile plus
   configured reported CPU/memory floors;
5. open the constrained candidate runtime;
6. bind the fixed root/controller protected Unix endpoint with the bounded data,
   candidate and network providers;
7. accept work.

Any failed prerequisite exits non-zero without enabling execution. Static Portal
applications do not depend on this service.

The journal's exclusive owner lock rejects a duplicate runner process. SIGTERM or
SIGINT cancels the listener and in-flight sessions, then closes native handles,
the data root and journal in reverse order. The systemd unit uses bounded restart
backoff and a start limit instead of an unbounded crash loop.

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

## Verification boundary

Local tests cover strict JSON with duplicate/unknown/case-alias rejection,
private-file mode and ownership, root/controller bounds, safe paths, enrollment
pin matching, idempotent bootstrap, conflicting enrollment, CLI refusal and the
Portal environment bootstrap. Linux tests compile the concrete root/data/Docker
service path.

On source candidate `cf4084b308bb19fcea17a7e826ae80f66042f210`, full test workflow
`41010542804` and app-runner daemon workflow `41010542824` both passed. The latter
created a newly owned loopback XFS project-quota mount, bootstrapped and started
the real `serve` command, observed the managed profile from the controller UID,
stopped the service and verified socket removal, restarted it without replacing
the journal or enrollment, observed the profile again, and confirmed the Portal
root endpoint. Existing browser/native coverage continues to exercise current
viewer HTTPS and SDK network paths.

A remaining H03 gate is to connect this exact long-running service to the Portal
browser managed-installation path and record a complete managed app installation
through the UI. The service restart drill and the separately hosted Portal
browser acceptance are not being presented as that one combined flow.
