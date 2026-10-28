# Bounded private HTTP communication storage

This development phase closes the unbounded writable HTTP-directory gap before
normal managed activation. Public managed launch/execution readiness remain
closed. There is no production deployment, schema change or Status migration.

## Fixed storage boundary

The private `/run/portal-app/http` bind source is now a dedicated tmpfs with:

- 16 MiB of data blocks and 1024 total filesystem inodes;
- nodev, nosuid, noexec and private mount propagation;
- UID/GID 65532 and mode 0700 for the app-facing root;
- a platform-only trusted ownership marker covering the complete candidate
  identity, record kind and provisional IPC profile.

It stays at the existing fixed communication path. App descriptors cannot select
its filesystem, capacity, mount options, source or destination. It consumes no
business-data project quota and grants no application data writer access. Docker
memory, swap, PID, root-filesystem, `/tmp`, shared-memory and log limits remain
separately required. The tmpfs data-block limit is not a claim that every kernel
metadata allocation is charged as a filesystem data block; the app's memory
containment remains a distinct boundary.

The implementation deliberately does not assume that file ownership or capability
dropping is a blanket prohibition on all project/file-attribute operations. Linux
has separate inode-owner and user-namespace/project-state checks; see the primary
[Linux v6.8 file-attribute checks](https://github.com/torvalds/linux/blob/v6.8/fs/ioctl.c)
and [XFS implementation](https://github.com/torvalds/linux/blob/v6.8/fs/xfs/xfs_ioctl.c).
This is the reason for an independent bounded IPC filesystem. No claim of an
exploitable data-writer path is made: candidate data remains inaccessible/read-only,
and future writable-data admission still needs its own adversarial acceptance.

## Creation and native admission

`ProvisionCandidateSockets` runs only after the existing durable candidate claim.
The protected installation/generation ancestors and read-only broker directory
retain their existing owner/mode checks. The HTTP directory is fresh-created as
a root-owned underlay. A trusted base marker is written and synced before mount.
The mount target is the already opened directory's kernel proc-FD alias, not an
app-selected pathname. The new mount is made private and receives its distinct
trusted mount marker. `trusted.*` is used instead of an app-writable `user.*` tag.

An existing HTTP directory must already be the exact bounded mount with the exact
identity and permissions. Plain legacy directories, absent/foreign markers and
partial underlays are refused without repair or automatic replacement. Errors
after creation/mount remain uncertain under the original candidate effect and
require reconciliation. An OS reboot loses tmpfs; historical receipts cannot
recreate it or assert that the old process is still running.

Every mount-pin acquisition and validation checks filesystem type, configured
data-block count, inode limit, flags, UID/GID/mode and the trusted identity again.
These are used by candidate creation/start, readiness, registration, boot and
request forwarding before/after native IO. Changing a limit or marker is refused
even when the directory inode itself did not change.

The new fixed `org.portal.app.ipc-profile` label enters the complete Docker policy
hash. Existing unbounded container policies therefore do not become accepted by
merely upgrading the host binary. This is a provisional development-profile
tightening, not public SDK/profile freeze or automatic adoption/migration.

## Explicit quiescent release

Runtime/launch Close releases native pins; it does not unmount communication
storage from a live app. `ReleaseCandidateHTTPStorage` is a separate internal
cleanup primitive, not a new signed lifecycle endpoint or browser permission.
Its caller must serialize the corresponding lifecycle and remove the owned
container under that operation's authority first.

The primitive rejects live local launch pins and reads a bounded Docker container
inventory. A container referencing this directory, a parent bind or a child bind
prevents cleanup, including stopped containers retaining mounts. Failed, oversized,
malformed or null inventory is refused. Only then are protected ancestors and the
exact trusted storage identity rechecked. It uses ordinary unmount, never forced
or lazy unmount, and removes only the empty matching root-owned marked underlay.
Foreign/unmarked resources are never recursively deleted. A known matching base
left by an interrupted provision/release can be removed only in this explicit
quiescent cleanup path; it cannot be treated as a mounted runtime resource.

This primitive does not complete normal uninstall/restart/restore orchestration.
Those operations still need current journal/fence authority, truthful resource
settlement and recovery handling. Its bounded inventory can refuse cleanup on a
large or uncertain host rather than assume there are no references.

Inventory uses a dedicated bounded array decoder, preserving the general daemon
reader's object-only contract. Each container and mount projection rejects
duplicates, case aliases and missing bind sources. Host bind aliases are resolved
conservatively; unresolved sources refuse cleanup. Resolver waits honor the cleanup
deadline with at most one native filesystem lookup in flight process-wide. Timed-out
work keeps its slot until it actually returns, so retries cannot accumulate blocked
threads. This is a point-in-time check under the caller's lifecycle exclusion;
unapproved host-administrator rebinding/retargeting of runtime-private paths is
outside the admitted profile and must be quiesced before cleanup.

## Verification

Disposable native fixtures use the same provision/validation/release paths. Their
cleanup first removes exact owned containers and closes pins; IPC unmount errors
fail acceptance rather than being silently ignored. Root-parent, runner journal
and protected transport fixtures no longer pre-create unbounded app-owned HTTP
directories. The read-only registration/broker boundary is unchanged.

New native mount-pin acceptance runs a child as UID/GID 65532 with only an owned
directory FD. It must fill file content until the kernel refuses writes within
16 MiB, then create empty files until the inode limit refuses more. It removes
only its own exclusive-created probe files. The child must be unable to read or
overwrite the trusted identity marker. Separate cases change byte/inode limits
or trusted markers under the root fixture and require pinned validation to fail;
an unmarked plain directory is neither adopted nor released. Cancellation before
creation must leave no directory.

The UID probe executes a bounded, root-owned, unwritable copy in an owned `/run`
directory; the original root-only acceptance artifact stays unchanged. Only the
owned HTTP directory FD is handed to that child, and the execution copy is removed
after it exits.

Actual native candidate acceptance also checks that live pins prevent release,
and that a second runtime instance with no local pins still refuses cleanup while
the real Docker container references the mount. These tests are evidence only
after their own disposable Linux runs succeed; local compilation is insufficient.

Local Go 1.26.10 checks use `GOWORK=off`, `GOMAXPROCS=2`, `-p 2`. Driver/transport/
journal race passed (driver 1.663s, transport cache hit, journal 85.263s). Linux
amd64 driver and Portal test binaries compiled; vet and Linux/default staticcheck
passed. Native byte/inode enforcement and cleanup remain pending their own CI
evidence. No server beta, dependency/toolchain, credential or database changes.

Initial native runs 40660999840/job 122723699610 and 40660999937/job 122723699587
failed acceptance: cleanup reused an object-only JSON reader for Docker's array
inventory, and the root-only test artifact could not execute as UID 65532. The
dedicated array decoder/projection tests and isolated executable copy correct
these integration errors. Those failed runs are not native acceptance evidence;
fresh byte/inode, real-reference and owned-teardown runs remain mandatory.
