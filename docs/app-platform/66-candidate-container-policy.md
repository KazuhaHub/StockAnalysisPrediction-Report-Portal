# Closed candidate container creation policy

This implements the low-level P04 candidate container create/inspect/remove
adapter following [immutable image inspection](65-managed-backend-image-inspection.md).
These functions remain package-private and have no production service route or
runner command dispatcher. They are not an alternative to signed journal
admission, physical path pins, quota/writer fencing or the complete lifecycle.
No database layout, production configuration or server beta setting changes.

## Code and identities

`internal/apprunner/driver/container.go` constructs a bounded Docker create body
from exact parsed descriptor bytes, the immutable native image observation,
platform-owned UUIDs/counters and infrastructure data/socket roots. Manifest
values never select a host mount, Docker option, command, container name or API
path. Roots are canonical, disjoint absolute paths; namespace and generation
subpaths derive only from canonical platform identities. Unix socket paths obey
the Linux pathname length limit.

Ownership labels bind deployment, controller epoch, installation, operation,
resource, data namespace, release digest, generation and control/writer fences.
A policy digest covers the canonical create policy before its own digest label
is inserted; private plan bytes also carry an integrity hash. These hashes detect
changed plan bytes and bind observations. They are not signatures or execution
authority. Future journal integration must supply and revalidate that authority.

A deterministic name derives from the resource UUID. The name is only an index;
all observations/removal use the exact 64-hex native container ID and compare
ownership and effective policy. A same-name response or HTTP conflict never
permits adoption, overwrite or deletion.

## Fixed create request

| Area | Candidate policy |
| --- | --- |
| Image | Inspected native sha256 configuration ID; no name/tag re-resolution |
| Identity | UID/GID 65532:65532, fixed hostname and working directory |
| Privilege | Nonprivileged, drop ALL capabilities, no-new-privileges |
| Root/network | Read-only root filesystem, network disabled and network=none |
| Namespaces | Private cgroup/IPC; no requested host PID/UTS/user namespace |
| Resources | Exact reviewed memory, no extra swap, NanoCpus and PID limits |
| Temporary space | 16 MiB noexec/nosuid/nodev /tmp, 16 MiB private shared memory |
| Open files | Hard/soft nofile limit 1024 |
| Lifetime | Init enabled, SIGTERM, stop timeout 10 seconds, no automatic restart/removal |
| Health | Image health checks disabled; platform readiness probing remains separate |
| Logs | json-file, max-size 1m and max-file 2 |
| Data | Only the owned namespace, read-only for the candidate |
| HTTP socket | Only this installation/generation's HTTP directory, writable |
| Broker socket | Separate installation/generation directory, read-only |
| Mount behavior | Explicit bind mounts, rprivate, no automatic source-directory creation |

The only supplied environment values name the two in-container Unix sockets.
No host environment, Portal session, control secret or broker credential is
forwarded. The image's own entrypoint/command and unrelated image environment
remain part of its immutable code; it is not a source of host authority. Inherited
anonymous volumes were already refused by image inspection.

This read-only candidate does not have a write lease. Docker cannot turn an
existing read-only bind into a writable bind in place. The later active-writer
transition must explicitly define and journal its new container/resource and
writer ownership; it must not quietly weaken this candidate profile. Mount
permissions/readability and quota admission are not established by lexical root
validation or by this plan.

## Effect and reconciliation behavior

Create sends exactly one bounded JSON POST to the fixed Docker create endpoint.
Only a valid 201 with a canonical native ID and no warnings is a successful
response. A 409 is an identity conflict. Transport loss, cancellation after
admission, malformed/oversized response, warnings or other status produce an
uncertain effect requiring durable reconciliation. There is no automatic retry,
start, force removal or same-name adoption.

`container_observe.go` rereads the exact native ID and checks its name, image,
owner labels, projected create settings, approved environment values and effective
bind mounts. Extra host authority such as capabilities, devices, ports, alternate
runtimes, links and additional binds is refused. Owned-prefix unknown labels and
changed fences also refuse. Only consistent created/exited/running states are
observations; paused/restarting/dead/unknown state requires reconciliation.

Remove first performs the same identity/policy observation and accepts only
created/exited state. It sends a non-forced DELETE with volume deletion disabled.
If another actor starts the container between inspection and deletion, Docker's
non-forced deletion must refuse. Running/foreign/uncertain resources are not
cleaned by a name or a guessed ID. Any lost deletion acknowledgement is uncertain.

These policy checks are not a claim that every daemon security option is
certified. Before start, the supported runtime must still prove the complete
sandbox, seccomp behavior, hard quotas, current mount identities, private sockets,
capacity and signed command authority. `execution_ready` remains false.

## Tests and native fixture

Portable tests check the fixed request, reviewed limits and mount access modes;
changed inputs/plans are rejected before daemon access. They also cover create
conflict/ambiguity and ownership/image/fence/user/network/mount/log substitution.
Running or uncertain observations never reach DELETE. Removal tests assert the
exact native-ID path and force=false/v=false.

The Linux-only candidate acceptance test requires an explicit flag, root and
GitHub-hosted disposable-runner markers. It uses the previously resolved official
BusyBox fixture digest, synthetic descriptor/IDs and private empty directories
under /run. It creates a stopped container, inspects the effective configuration,
and removes it only through the ownership-checked non-forced adapter. It never
starts the image and does not claim hard quota, data access or readiness evidence.
If policy inspection refuses, cleanup also refuses; the disposable runner is
then the quarantine boundary. Normal tests and production binaries do not run
this fixture.

The existing daemon workflow compiles and invokes that isolated test. Actual CI
execution must pass on the current revision before recording native evidence;
macOS unit tests or Linux cross-compilation are not substituted for it.

## Next integration

Add signed managed container commands, journal resource claims/receipts and
physical root/socket/quota checks; then start/readiness/stop, automatic backend
registration and active-writer transition. Connect these to the managed install
worker and broker. Keep production enrollment, real credentials, Status migration,
final SDK freeze and D5/D6 acceptance separate.

Local validation passed driver race tests (1.445 seconds), vet, staticcheck and
Linux amd64 test compilation. A subsequent integrity check also binds the private
plan's owner fields to its canonical label/policy bytes before create, inspect or
remove; its final driver race regression passed in 1.452 seconds, followed by
vet, staticcheck and Linux compilation. Native CI remains a separate requirement.
