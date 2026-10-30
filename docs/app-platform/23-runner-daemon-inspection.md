# Read-only runner daemon inspection

Status: first P04 adapter step, development only. Candidate A permits isolated
Linux acceptance; it does not authorize production enrollment or deployment.
This adds `cmd/app-runner` and `internal/apprunner/driver`, without changing Portal
startup, database shape, installed apps or server `beta` configuration.

## Command and meaning

On a dedicated disposable Linux acceptance worker:

```sh
GOWORK=off go run ./cmd/app-runner inspect-daemon --socket /run/docker.sock
```

The socket argument is mandatory and must be canonical. If an administrator's
configured path is `/var/run/docker.sock`, obtain its canonical path with
`readlink -f /var/run/docker.sock`; the program deliberately refuses a symlink
alias. It never reads `DOCKER_HOST`, Docker contexts or registry credentials.
There is no serve/enroll/start/exec/container/image/volume command yet.

Exit 0 means diagnostic collection completed, including a possibly incompatible
daemon. Inspect the JSON `metadata_compatible` and findings; **execution_ready is
always false** at this implementation stage. Inspection is not installation
admission. Exit 1 reports a safe inspection error; exit 2 rejects command syntax.

The report keeps four required areas explicitly unverified: actual hard quotas,
sandbox enforcement, available capacity/reservations and controller authentication.
Do not turn these into green checks based on Docker configuration or this command
exiting successfully. P04's complete hostile-workload acceptance is still missing.

## Fixed I/O boundary

The public inspector refuses non-Linux hosts and architectures other than native
amd64/arm64 before contacting a socket. It supports no remote Docker transport.
On Linux it checks:

- Absolute clean filesystem path, bounded Unix socket length, no symlink aliases,
  root-owned socket, one hard link and zero Unix other-user permission bits.
  Docker-group access is permitted; ACLs and group membership are not attested.
- Parent directories owned by root/the runtime UID, without group/world writes
  except a root-owned sticky ancestor such as `/tmp`. A private child directory
  must still protect the socket. Docker group access is root-equivalent authority;
  this guard is not a way to make arbitrary socket access safe.
- Kernel `SO_PEERCRED` UID 0 on the connected Unix peer, followed by a second
  path/ownership/inode check. A user-owned fixture cannot impersonate rootful Docker.

The HTTP transport ignores proxies, disables connection reuse and redirects,
uses only that checked Unix socket, and sets a five-second overall timeout with
a one-second response-header deadline. It performs only `GET /version` and, when
the daemon supports the pinned API range, `GET /v1.47/info`. No daemon response can
select another URL, path or action. Cookie and Authorization headers are absent.

Each response is bounded to 1 MiB, UTF-8 JSON, depth 32, 2,048 fields per object and
4,096-byte keys. Exact duplicate keys and case/Unicode aliases of projected root
fields refuse before typed decoding. Valid unknown Docker fields remain ignored
for forward compatibility; they are never copied into the report.

The projection follows the [Docker Engine v1.47 API](https://docs.docker.com/reference/api/engine/version/v1.47/).
The daemon's reported minimum/maximum API versions must include 1.47. Version and
info must agree on server version; supported architecture spellings normalize to
native amd64/arm64. Reports contain bounded version/platform/capacity numbers and
fixed findings, not host names, daemon IDs, proxy settings, registry configuration,
labels, root paths or upstream error bodies.

## Advertised compatibility is not enforcement

The initial candidate checks Linux/native architecture, cgroup v2 with systemd or
cgroupfs, advertised memory/swap/CPU/PID controls, overlay2/overlayfs image storage,
rootful/unmapped user profile and the reported built-in/default seccomp profile.
Missing flags fail; malformed/duplicate security metadata cannot declare support.
Unknown versions, roots, drivers or unsupported profiles do not trigger a fallback.

Rootless and userns-remapped profiles remain unverified. This is an initial
adapter limit, not a statement that those modes cannot enforce isolation. Docker
documents [rootless execution and UID mapping](https://docs.docker.com/engine/security/rootless/);
support here requires the same actual quota/mount/identity enforcement evidence
as the first approved profile.

Reported total memory/CPU are not currently available capacity. Image storage
driver names do not prove project quotas for app data. Seccomp/cgroup flags do
not prove the configuration of a future app container. None of these claims
sets `execution_ready=true` or admits a package/resource.

## Verification workflow

Local unit/race tests use in-memory HTTP transports, not a real local daemon.
They check exactly two read-only requests, API negotiation before versioned calls,
redaction, incompatible claims, strict response parsing, bounded errors,
cancellation, redirect refusal and the CLI's closed command/argument surface.
The malformed-response regression was observed failing when its strict projection
check was removed.

Linux-specific tests create only their own temporary fixture sockets. They verify
unsafe paths/modes/links, actual kernel peer credentials and rejection of a
non-root peer. They do not alter a host Docker configuration.

`.github/workflows/app-runner-probe.yml` runs native tests and the command against
the standard ephemeral ubuntu-24.04 worker daemon. The integration test runs only
with an explicit `PORTAL_APP_RUNNER_ACCEPTANCE_SOCKET`. It logs the redacted report
and requires metadata compatibility while still requiring execution readiness to
remain false. This workflow performs no container, image, volume or filesystem
quota changes and receives no production credentials or deployment permissions.
CI output is actual native evidence; cross-compilation alone is not.

Next steps are authenticated control enrollment/commands, bounded verified
artifacts, owned data/quota enforcement, a typed constrained container adapter and
real hostile fixtures. Keep their proof separate from this inspection report.

Local evidence: full Go tests/build/vet/staticcheck, driver/CLI race tests, workflow
lint and Linux/Windows test cross-compilation pass.

Native evidence: [workflow run 40189381328](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40189381328)
passed on 2026-10-25 UTC for source
`92bb2016d31bf28fbce30a9d838b0c42e0d2ab6c`. The disposable Ubuntu 24.04
Linux/amd64 worker reported Docker Engine 28.0.4, metadata compatibility true and
execution readiness false. Both the real-socket integration test and CLI passed;
the driver/CLI race tests also passed on that worker. This records native socket
and metadata evidence only. No quota exhaustion, sandbox escape, app execution,
controller authentication or available-capacity acceptance was performed.
