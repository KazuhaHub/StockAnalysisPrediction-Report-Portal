# Physical data-root identity

The native data primitive now requires an infrastructure-supplied filesystem
UUID and root-directory inode for every preparation and inspection. This closes
the physical-target ambiguity before connecting execution claims to native work.
It does not complete the installer or make the runtime production-ready.

## Interface and trust

`data.RootIdentity` contains a canonical nonzero lowercase filesystem UUID and a
positive root inode. It deliberately excludes Linux device numbers, which need
not remain stable across remounts. `Root.Identity(ctx)` observes this identity
through the already pinned descriptor; it does not look the configured pathname
up again or automatically authorize what it finds.

`Root.Prepare(ctx, expected, request)` and
`Root.Inspect(ctx, expected, namespace, project)` require this explicit expected
identity. There is no unpinned overload. Under the root mutex each compares the
expectation to fresh native observations before touching a namespace, and checks
again before returning a successful observation. A mismatch returns `ErrUnsafe`.
A failure after creation still includes `ErrIncomplete`; do not blindly replay,
remove the namespace, or assume a successful quota update means completion.

The expectation belongs to trusted runtime configuration, never an app package,
app capability call or app-selected path. Do not implement a caller as
`root.Prepare(ctx, root.Identity(ctx), request)` on every invocation: that would
silently trust a replacement root. Initial enrollment and subsequent journal
binding must establish and retain the expectation independently.

Every observation also rechecks root ownership/private permissions, directory
type, original open device, XFS type, nodev/nosuid/noexec, root project zero with
no inheritance, and active project accounting/enforcement. Open validates that
native UUID observation is supported before returning a usable root. Canceled
or closed roots return no identity. Unsupported platforms retain refusal stubs.

A root pathname renamed and replaced after Open cannot retarget its pinned file
descriptor. Opening the replacement produces a different inode and rejects the
old expectation. UUID/inode is a storage identity check, not cryptographic
attestation against a privileged administrator cloning a filesystem with the
same UUID and inode metadata. Host root remains outside the app threat boundary.

## Native ABI

The adapter reads the UUID from `XFS_IOC_FSGEOMETRY_V1` on the pinned directory
FD, using the [kernel XFS UAPI](https://github.com/torvalds/linux/blob/master/fs/xfs/libxfs/xfs_fs.h).
The supported Linux amd64/arm64 layout is 112 bytes, UUID offset 64, ioctl
`0x80705864`. The disposable amd64 job independently compiles installed XFS
headers to measure these values and compares them with Go constants before any
native preparation. Separate C translation units avoid duplicate fsxattr
structures from Linux and XFS headers. C and XFS development headers are only
acceptance dependencies; production code invokes no CLI and needs no cgo.

## Reproduce acceptance

On the development host:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 ./...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -race ./internal/apprunner/data
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go vet ./internal/apprunner/data
python3 -B -m unittest discover -s scripts -p project_quota_probe_test.py
```

Use the `app project quota probe` workflow for actual native behavior. It refuses
ordinary hosts before allocating a fixture. The workflow provisions one new
owned loopback image on a disposable GitHub-hosted Linux worker and supplies
expected UUID from `blkid -p` and expected inode from Python stat, independently
of the Go observation. Native tests cover:

- Correct identity, independent C ABI checks, wrong UUID/inode and zero pins.
- No namespace or quota mutation on preflight identity mismatch.
- Replaced path vs pinned descriptor and same-filesystem replacement inode.
- Widened permissions after Open, cancellation and closed-root refusal.
- Existing real byte/inode/open-unlinked limits and fresh remount inspection.
- Disabled project enforcement refusal.
- After all quota controls, confirmed unmount of only the owned loop image,
  generation of a new UUID with xfs_admin, remount, then rejection of old expected
  identity for both prepare and inspect. The test also checks inode is unchanged
  and no new namespace was created.
- Exact owned mount/loop cleanup; Python tests refuse foreign/substituted loop
  ownership and uncertain unmount before UUID mutation.

Do not run the privileged workflow commands against a developer or production
data disk. The report remains `execution_ready: false`. Cross-compilation of
arm64 is not native arm64 enforcement evidence. Source `0473cecac83120b3e402e196434a2d4b96e4fe73` passed
[native run 40296737513](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40296737513).
The downloaded report confirms identity checks, changed-UUID refusal, native
preparation/unprivileged quota enforcement, remount inspection, disabled-
enforcement refusal and owned cleanup. The root/path replacement and permission
checks are assertions in the passing native Go phase. This is disposable amd64
evidence; the broader main and daemon workflows have independent status.

## Remaining integration

No tables, columns, journal versions or side stores change. Root observations are
not durable command receipts. The next executor must persist an immutable
physical root binding in the approved journal before mutation, retain it across
attempts/history, consume the live claim once, call this concrete native provider,
then atomically settle a typed physical receipt and resource observation. Unknown
attempt reconciliation, writer exclusion/admission, controller receipt delivery
and Portal stage completion still remain. No startup, worker/socket dispatch,
managed install activation, real credentials, Status migration, production
privileges or server beta settings change here.
