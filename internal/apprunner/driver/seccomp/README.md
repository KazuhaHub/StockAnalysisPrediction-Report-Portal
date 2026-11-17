# Managed candidate seccomp base

`moby-default.json` is an unchanged copy of
https://github.com/moby/profiles/blob/4b8214fc4971e06172938a1f1554ccff6647ee80/seccomp/default.json
from the Moby profiles project, licensed under Apache-2.0 (see `LICENSE`).
The exact source SHA-256 is
`6416b47770785a41ac59073cdc77d9fe98517df2799dc83ef207e622de3053f6`.
No upstream NOTICE file was present at that revision.

The application platform changes the profile in `../seccomp.go`: it removes the
unconditional `ioctl` allowance and adds five argument-constrained requests:
TCGETS, TIOCSPGRP, TIOCGWINSZ, FIONREAD and FIONBIO. Other upstream rules, architecture
mapping, capability conditions and default-deny behavior are preserved. This
derived policy is named `ioctl-terminal-socket/v1-dev`. FIONBIO changes descriptor
nonblocking mode; TIOCSPGRP assigns a terminal foreground process group, and the other three
requests query terminal/socket state. Docker init calls TIOCSPGRP on non-TTY
stdin too; the kernel must return ENOTTY rather than the filter returning EPERM.

Docker receives the complete compact policy as an inline security option before
the image entrypoint runs. The closed container policy hashes those bytes and
checks the exact option on every container observation. There is no app-selected
profile, host path, network fetch, fallback to an unconfined profile or dependence
on an app cooperating with a launcher. A frozen base update requires review and
fresh native acceptance; changing the Docker daemon alone does not replace it.

This tightens the development candidate profile. It does not grant writable data
or prove the later writer-fencing lifecycle. Apps needing other ioctls require a
reviewed platform contract change, not a per-app escape setting.
