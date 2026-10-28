# Native data write-access transition and adversarial trial

## Scope in the implementation sequence

The syscall boundary in [91](91-candidate-seccomp.md) is accepted on the
disposable amd64 runtime. The next dependency is a physical namespace permission
transition that preserves ownership, project inheritance and hard quotas while
allowing the fixed application UID/GID to write. This document describes that
primitive and its isolated trial. It does **not** declare managed writer
authorization, writer revocation or automatic activation implemented.

There is no new table, migration, metadata side file, production dispatcher,
credential, image-channel change or public SDK contract. The existing namespace
inode's group and mode change. Application files created by the disposable probe
are test data, not a new platform persistence layout.

## Physical states and exact API

| State | Owner | Group | Mode | Accepted observer |
| --- | --- | --- | --- | --- |
| Sealed prepared namespace | root (0) | root (0) | `0700` | `Root.Inspect` |
| Write-access namespace | root (0) | app (65532) | `02770` | `Root.InspectWriteAccess` |
| Partial, changed or unknown | Any other tuple | Any other tuple | Any other mode | Neither; no automatic repair |

The namespace root remains owned by root. Group write grants app access without
granting inode-owner chmod/chown authority; setgid preserves the app group on new
children. Project inheritance is a separate filesystem attribute, protected by
the fixed seccomp profile. A directory mode alone is never a project-quota or
single-writer proof.

`Root.EnableWriteAccess(ctx, rootIdentity, allocation)` accepts the independently
pinned data root and the exact native allocation observation. It:

1. Holds the existing provider mutex; the provider already holds the exclusive
   root lock. Refuse closed/invalid identities, cancellation and invalid inputs.
2. Recheck the actual root filesystem UUID/inode and project enforcement.
3. Require the existing sealed namespace's namespace ID, project ID, device,
   inode, hard byte/inode limits and inheritance to match the allocation. Current
   observed usage must be nonnegative and within hard limits. Historical used
   bytes are not an immutable namespace identity.
4. Open the namespace through the pinned parent with no-follow/directory flags.
   Compare its inode/device and exact sealed owner/group/mode again. Never chown
   an arbitrary path or an app-selected target.
5. Set only group 65532, keeping UID 0; then set mode `02770`. Check cancellation
   before the first mutation and between the two mutations.
6. Sync the namespace and provider root, independently observe the fixed new
   profile, recheck the allocation and root identity, and return a typed
   `WriteAccessObservation`.

This method cannot choose a UID/GID/mode, resize quotas, change project ID,
replace the namespace or create missing data. Existing writable/partial/drifted
directories are refused. The sealed observer now also checks GID 0 explicitly.

`Root.InspectWriteAccess(ctx, rootIdentity, namespaceID, projectID)` reads the
fixed new profile and quota allocation without mutation. Its result contains no
controller authority, lease, writer fence, process identity or capability grant.
Callers must compare the returned physical allocation with their admitted
records; an observation is never a bearer credential.

## Failures and recovery rules

An error after the first group mutation includes `ErrIncomplete` and returns no
successful observation. Cancellation is not evidence that the mutation did not
happen. A group-changed/mode-sealed partial state is refused on retry, as is an
already writable namespace. Neither observer rewrites a mode or owner.

There is intentionally no `DisableWriteAccess` or close-based revocation API.
Changing directory permissions does not invalidate file descriptors already open
inside an app. `Root.Close` releases the provider; it does not revoke those
descriptors or magically stop a container. Future writer fencing must stop and
verify all old processes/references before switching ownership/fences.

Before any production caller is added, it must bind a durable claimed effect,
current signed authority, exact namespace allocation and expected writer fence;
exclude current/old native writers, handle uncertainty through reconciliation,
and record physical results only under still-current authority. These are future
integration requirements, not behavior supplied by this primitive.

## Native verification procedure

The existing disposable quota workflow owns one fresh loopback XFS filesystem
and compiles the data and driver test binaries. No real Portal data or arbitrary
host Docker resource is selected.

### Permission transition

`TestNativeDataWriteAccess` runs only in the initial, explicit disposable root
phase. It prepares a separate 8 MiB/128-inode namespace, checks altered
allocation/root/cancellation refusal without mutation, and races two physical
transition attempts. Exactly one must succeed; the other must refuse. This tests
provider serialization, not cross-container writer exclusion. It checks root
ownership/setgid, provider reopen, sealed-observer refusal after transition,
partial-state refusal without repair, and permission drift refusal.

The parent probe requires `NATIVE_DATA_WRITE_ACCESS_TRANSITION_VERIFIED` before
setting `native_go_data_write_access_transition=true`.

### Actual application and XFS boundary

`TestNativeDataWriterContainer` runs only with the explicit disposable registry
image and root/daemon configuration. It prepares a separate namespace, provisions
bounded private sockets while the allocation is sealed, and makes the physical
permission transition. It first requires the production `PrepareCandidate` to
**refuse** that write-access namespace. The ordinary builder must still mount
data read-only.

Only inside this test, derive a fixture create policy that changes data RW and
adds `PORTAL_APP_WRITER_PROBE=1`. Retain the exact syscall, UID/GID, capability,
network, memory/PID/CPU, logging and bounded socket policies. Rehash and observe
the whole test policy. This is an isolated physical trial, not an admitted
managed installation or a production writer launch.

Before health is bound, the known fixture binary must:

- Pass the existing actual seccomp/identity startup checks.
- Confirm root:65532 mode `02770` and receive EPERM for app chmod/chown of the
  namespace root.
- Create an app-owned child directory and attempt native/compat/upper-word
  filesystem setters to clear inheritance/change project ID; all must be EPERM.
- Allocate extents in bounded 64 KiB steps until byte allocation is refused near
  the 8 MiB hard limit. It cannot allocate more than one step beyond that bound
  even if enforcement fails.
- Truncate the extent file and sync before the independent inode test, then
  create empty children until refusal near the 128-inode hard limit. Keep a
  pre-created evidence file open so reporting does not need another inode.

The host waits for actual bounded readiness over the UID/GID-checked Unix socket,
stops the exact owned container, then independently verifies the namespace's
identity, owner/mode, project inheritance, unchanged limits and inode usage. It
reads project attributes from every actual child and directory through no-follow
descriptors. Every child must remain in the same nonzero project; directories
must retain project inheritance. Evidence is bounded to 4096 bytes. The host also
requires ample global filesystem space and verifies a separate healthy project's
write succeeds, distinguishing per-project denial from a globally full device.

The parent probe requires `NATIVE_FILTERED_DATA_WRITER_VERIFIED` before setting
`native_filtered_data_writer_verified=true`. Cleanup stops/removes only the
matching fixture container, releases the owned bounded socket mount, and then
removes the owned fixture directories. It never force-removes a foreign or
uncertain container.

## Evidence and remaining work

Implementation is pending native acceptance. Local build/test evidence cannot
establish the actual permission, XFS or container boundary.

Next: independently authenticated/journaled writer admission, namespace and
process fencing, interruption/restart reconciliation, a production writable
candidate profile, broker authority and normal managed activation. Continue the
SDK/reference-app, update/uninstall/recovery and complete P00–P14 acceptance work.
No production deployment, Status adoption or server beta configuration is changed.
