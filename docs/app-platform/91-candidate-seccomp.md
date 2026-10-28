# Candidate syscall boundary before writable data

## Purpose and dependency

The initial candidate still mounts its quota namespace read-only. Before adding
writer authority, the runtime must stop an application from using filesystem
attribute ioctls to change project inheritance or ownership on its own files.
Dropping capabilities alone is not a proof that every owner-allowed file
attribute operation is denied. This change establishes that syscall boundary;
it does not grant writable data or complete managed activation.

The Linux implementation separates owner checks, user-namespace checks and
individual attribute checks. Review the primary implementation when changing
the writer threat model:

- https://github.com/torvalds/linux/blob/v6.8/fs/ioctl.c
- https://github.com/torvalds/linux/blob/v6.8/fs/xfs/xfs_ioctl.c
- https://docs.docker.com/engine/security/seccomp/

These references explain the boundary to enforce. They are not evidence of an
escape in an accepted candidate, whose data remains read-only.

## Closed runtime policy

1. Embed the exact Apache-2.0 Moby default profile at revision
   `4b8214fc4971e06172938a1f1554ccff6647ee80`. The source file, full license,
   provenance URL and source hash live in `internal/apprunner/driver/seccomp/`.
   Release archives and the Portal image include the license/provenance files;
   independently distributed runner binaries must ship these files as well.
   Runtime admission never fetches a profile from a URL or accepts an app path.
2. Preserve its default-deny action, architecture map and every existing syscall
   rule except the unrestricted `ioctl` allowance. Replace that allowance with
   exact argument-1 equality rules for TCGETS (`0x5401`), TIOCSPGRP (`0x5410`), TIOCGWINSZ (`0x5413`),
   FIONREAD (`0x541b`) and FIONBIO (`0x5421`). FIONBIO changes nonblocking mode;
   TIOCSPGRP assigns a terminal foreground process group. Neither is a
   filesystem attribute setter. Every other ioctl is denied.
3. Send the compact complete profile to Docker as an inline `seccomp=` security
   option, beside `no-new-privileges:true`. Docker installs it before the image
   entrypoint. No application cooperation, shell wrapper or extra app container
   is involved. Keep the existing UID/GID, dropped capabilities, no-network,
   read-only root/data, mount pins and resource bounds.
4. Add `ioctl-terminal-socket/v1-dev` to the closed ownership labels. The full
   security option participates in the existing policy hash, not just this name.
   Every native container observation checks the exact option. Missing,
   unconfined, broadened or changed policies are identity conflicts.
5. Keep the create body below the existing 32768-byte bound. Do not relax framing
   limits to accommodate a profile. Profile construction failure refuses the
   candidate; it never falls back to Docker defaults or unconfined execution.

The frozen default differs from an arbitrary future daemon default. Daemon
preflight still requires a supported seccomp-capable daemon, while the explicit
candidate policy is independently fixed. Future upstream updates require source,
license, syscall-diff and native acceptance review. Old candidates with different
closed policy bytes cannot be silently adopted or restarted.

## Acceptance and execution

Local contract tests check the upstream source hash, preserve every non-ioctl
rule and top-level field, require the five exact argument rules and reject an
unexpected source shape. Container-observation refusal tests cover removed,
unconfined and broadened security options. Existing lifecycle/transport tests use
the real derived create policy.

The actual disposable ready backend runs a mandatory self-check before it binds
its health socket. It verifies UID/GID 65532, no effective capabilities,
NoNewPrivs and seccomp filter mode. With FD -1 it then requires:

- EPERM for native/compat FS_IOC_SETFLAGS, FS_IOC_FSSETXATTR, filesystem getters,
  unlisted requests and an upper-word alias of an allowed request. Seccomp acts
  before descriptor lookup; an unfiltered invalid FD would return EBADF.
- EBADF for each of the five admitted terminal/socket requests, proving the
  filter does not just deny every ioctl.
- EPERM for io_uring_setup, preserving the default-deny alternative submission
  boundary.

Any failed self-check exits before readiness. The Portal child and parent require
the entrypoint-verification marker after actual readiness. The native quota
probe emits `native_candidate_seccomp_entrypoint_verified=true` only when that
chain succeeds. Native readiness, registration, boot, current-viewer requests and
owned resource cleanup must still pass; a static JSON inspection is insufficient.

At implementation time local driver/transport race passed (2.244s/1.738s).
Linux amd64/arm64 fixture and driver test-binary builds passed. Platform
regression passed in 7.994s; vet and default Linux staticcheck passed. Both native
workflows are pending; this document does not assert native enforcement yet.

Initial native runs 40667492092/job 122745577047 and 40667491834/job
122745576233 failed at candidate startup; these are not accepted enforcement
results. A diagnostic-only rerun also failed. Tini v0.19.0 unconditionally calls
`tcsetpgrp` during child setup and tolerates ENOTTY/ENXIO, but treats a seccomp
EPERM as fatal:
https://github.com/krallin/tini/blob/v0.19.0/src/tini.c#L149-L177
The initial four-request policy omitted TIOCSPGRP. Add only that exact terminal
request; preserve non-TTY creation and all filesystem-attribute refusals. Fresh
native entrypoint and full request/cleanup acceptance remain required.

## Still required

Writable namespace ownership, quota escape adversarial cases on actual data,
single-writer fencing and revocation are separate implementation work. They must
pass before writable mounts are admitted. Managed automatic activation, broker
capabilities, normal worker scheduling, update/recovery/uninstall, SDK/reference
apps and full P00–P14 acceptance remain incomplete. There is no production,
server beta, schema, dependency/toolchain, credential or Status adoption change.
