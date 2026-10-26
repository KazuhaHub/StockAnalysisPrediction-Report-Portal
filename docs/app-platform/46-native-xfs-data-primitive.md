# Native XFS data preparation primitive

This implements the initial native filesystem part of P04/P05 data preparation.
It is an internal Go library tested only under explicit disposable Linux
acceptance. It is not a journal-admitted execution provider, installed app,
application writer grant, complete installer or production deployment.

## Why this is needed

The earlier project-quota probe demonstrated XFS enforcement using administrative
tools. Installation still had no Go primitive for setting and observing native
project IDs/limits. `internal/apprunner/data` now talks directly to the Linux UAPI:
[filesystem attributes](https://github.com/torvalds/linux/blob/master/include/uapi/linux/fs.h)
and [XFS quotas](https://github.com/torvalds/linux/blob/master/include/uapi/linux/dqblk_xfs.h).
The [XFS mount policy](https://docs.kernel.org/admin-guide/xfs.html) remains the
approved accounting/enforcement prerequisite.

The adapter uses `quotactl_fd`, `FS_IOC_FSGETXATTR` and `FS_IOC_FSSETXATTR`. No shell,
`xfs_quota` command string, block-device path or app-selected filesystem path is
used by the library. The existing x/sys version becomes a direct dependency;
no dependency version changes. The initial native ABI is Linux amd64/arm64;
other systems return unsupported. Missing kernel/ABI support refuses rather than
falling back to scanning apparent file sizes.

## API and caller obligations

`Open(existingRootFile)` creates its own pinned descriptor, retains the caller's
input file and exclusively locks that data-root inode. The configured directory
must already exist, be root-owned mode 0700, have project ID 0 without inherited
project membership, and reside on XFS with nodev/nosuid/noexec plus actual project
accounting and enforcement. Opening creates no directory or quota. Another root
handle for the same directory refuses its exclusive lock.

This is infrastructure bootstrap input, never a browser/app/descriptor field.
The future provider must use one configured data root for the deployment. A root
lock does not allocate project IDs across arbitrary independently configured
roots or unrelated XFS administrators. Root/host compromise is outside this
profile; a hostile privileged administrator can change mounts and quotas.

`Prepare(ctx, Request)` accepts only a canonical namespace UUID, a nonzero uint32
project reservation, a positive byte limit divisible by 512, and a positive inode
limit. The future journal/provider must durably reserve the namespace/project
mapping and bind it to the signed command/effect, generation, owner and writer
fence **before** calling this primitive. These arguments alone grant no such
authority. Effective platform quotas/capacity admission still belong to the
reviewed plan/provider policy.

The implementation serializes operations on its root and:

1. Checks cancellation, current enforcement and native project limits/usage.
   Existing soft/hard limits or allocated blocks/inodes refuse project reuse.
2. Exclusively creates one root-owned mode-0700 UUID child with `mkdirat`. Existing
   directories, files and symlinks are never adopted or recursively modified.
3. Opens that exact child relative to the pinned root with directory/nofollow
   constraints. Sets the fresh project's hard byte/inode limits using the pinned
   filesystem FD, then sets its project ID and inheritance flag.
4. Syncs the namespace and root directory and re-observes enforcement, device,
   inode, root ownership/mode, project/inheritance and exact hard limits.
5. Returns a current `Observation` only when all checks and cancellation checks
   succeed. No writer UID, mount, app process, capability or access grant is added.

`Inspect(ctx, namespaceID, projectID)` performs fresh native checks and returns a
snapshot without changing ownership/limits. It rejects symlinks, substituted
filesystem/owner/mode/project and disabled enforcement. `Close` releases only the
owned descriptor/lock. It never deletes a namespace, clears quotas or unmounts a
filesystem. File and quota syscalls are synchronous; context checks surround them
but do not claim to interrupt a blocked kernel syscall.

## Partial failure and recovery boundary

Any failure after namespace creation returns an empty observation joined with
`ErrIncomplete`. Native work may already exist, including quota limits after a
lost acknowledgement. It does not roll back by deleting files, clearing a project
or repeating a blind prepare. A retry cannot adopt an existing namespace/project.
The future journal reconciliation provider must inspect its originally reserved
native resources, prove ownership and writer exclusion, then decide how to resume
or require review. There is no automatic deletion/repair path in this library.

Observations are not durable command receipts or physical activation evidence.
They prove no stopped writer, generation exclusion, Docker isolation or broker
permission. They cannot move a Portal operation to ready by themselves. The
library is deliberately not called by any runtime socket, worker, server startup
or command-line service before the typed journal execution boundary is built.

## Native acceptance procedure

The existing quota workflow now also builds the Go test binary and an independent
C UAPI probe. C headers verify structure sizes, important field offsets and
ioctl/quotactl constants before native Go operations. Linux/amd64 and arm64 source
cross-compilation and vet checks supplement this; native workload acceptance runs
on the disposable Linux amd64 worker, not on the macOS host.

The Python probe retains its exact opt-in/host checks and creates/formats only its
own new loop-backed XFS image. Optional explicit native-binary/ABI-JSON paths are
CI fixture inputs; malformed arguments refuse before resource creation. It makes
one additional private data root on that owned mount. The Go fixture:

- prepares four native namespaces with 8 MiB/128-inode limits;
- refuses a second root owner, namespace replay, project reuse, canceled work,
  namespace symlinks and ordinary non-XFS directories;
- inspects the prepared resources after filesystem remount and process reopen;
- refuses fresh opening when project enforcement is explicitly disabled.

The probe temporarily grants only synthetic UID/GID 10000 access to those test
folders, after preparation. This is separate test setup, not provider behavior.
It runs the existing real byte, inode and open/unlinked workloads from private
working directories, with independent healthy-project writes and underlying
filesystem capacity checks. It restores sealed root ownership for inspection.
Disabling/re-enabling enforcement is confined to this newly owned fixture mount.
All original quota/remount/control checks still run.

Evidence flags distinguish native Go preparation, unprivileged enforcement,
reopen/remount inspection and disabled-enforcement refusal. The report continues
to state `execution_ready=false`. Device/mount ownership checks still precede
cleanup; unmount/detach must finish before deleting the fixture root. Unknown
cleanup outcomes leave the owned resources for ephemeral-worker teardown.

## Verification state and remaining work

Local portable bounds tests, runner race suites, Python refusal tests and Linux
amd64/arm64 cross-compilation/vet checks run without root filesystem mutation.
Native acceptance and exact-source CI results must be inspected on the PR before
claiming that this adapter actually enforces quotas. A cross-build is insufficient.

Remaining work: durable project allocation in the approved journal resources,
signed effect/command execution claims, native ownership receipts, uncertainty
reconciliation, writer admission/exclusion, Portal stage completion, managed
container isolation/lifecycle, and full install/update/restore/uninstall flows.
No new database shape, hidden side store, production deployment, real credentials,
Status migration, public SDK freeze or server beta configuration change is made.
