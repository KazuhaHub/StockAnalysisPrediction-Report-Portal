# Native candidate mount identity pins

This adds Linux-only native directory checks around the private candidate create
adapter. It follows [candidate claims](68-candidate-execution-claims.md), but is
not yet wired to those journal claims, a production service or container startup.
There is no new persistent configuration, schema, mount creation or permission
change in the production helper.

## Source and lifecycle

`internal/apprunner/driver/mount_pins_linux.go` pins existing directories using
O_PATH/O_DIRECTORY/O_NOFOLLOW/O_CLOEXEC directory descriptors opened component by
component from /. It does not accept a manifest path or follow a symlink through
an ancestor. The checked create plan derives every source from configured roots,
installation/generation and namespace IDs. The private data-device/inode input
must eventually come from the independently checked native data receipt.

The runtime caller must be root in the approved rootful profile. Root and
ancestor directories must be owned by root and protected from group/other writes,
with only the established root-owned sticky-directory exception. Leaf policy is
closed: the data namespace is root:root mode 0700, the HTTP directory is
65532:65532 mode 0700 and the separate broker directory is root:root mode 0755.
Ancestors remain inaccessible to unrelated host users. No missing path is created
and no ownership/mode is repaired silently. Unsafe or incomplete infrastructure
must be explicitly provisioned before native execution can be integrated.

The helper retains descriptors for leaves and ancestors, binding device/inode,
owner/group and complete mode. Held descriptors prevent recycling the pinned
inode. Validation compares held fstat identities and freshly reopened component
paths. A replaced ancestor, changed owner/mode, symlink or different data identity
refuses. Normal creation of socket/file entries does not invalidate the directory
identity. Pins are local live resources; they are not JSON credentials, durable
journal receipts, quota proofs or application readiness.

Close is idempotent and releases all descriptors. A mutex prevents Close from
invalidating the pin set during a synchronous create. Closed pins and a different
create-plan digest cannot be used again.

## Create boundary and uncertainty

`createPinnedCandidateHTTP` validates before Docker access, calls the closed
create adapter once and validates again before returning its native ID. An unsafe
initial path makes no Docker request. If directory identity changes after a
successful create response, the helper returns an uncertain effect and no usable
success ID. It does not retry, adopt by name, delete arbitrary paths or pretend a
possibly created container does not exist. Native reconciliation must resolve that
case under current signed authority and exact observed ownership.

Docker still resolves host path strings for bind mounts. The approved threat
model trusts the root-owned runtime host and daemon; it does not claim to defeat
a malicious host administrator/kernel that swaps and restores paths during the
request. Root-owned private ancestors keep an application from replacing its
mount source. Descriptor pins and before/after checks are not a replacement for
that host trust or native quota/sandbox verification.

## Tests and disposable Linux execution

The existing real stopped-container fixture now provisions explicitly synthetic
private directories, derives synthetic device/inode metadata, pins them and uses
the checked create wrapper. The Docker create/inspect/non-forced cleanup test
still never starts the image. Its empty data directory is not hard quota evidence.

Additional root-only GitHub-hosted native cases cover held identity, normal file
entry changes, close invalidation, changed inode/ancestor/owner/mode, initial or
introduced symlinks, wrong plan/data proof, cancellation, refusal before daemon
access and uncertainty after a simulated successful create. They run under the
same explicit disposable acceptance flag and are skipped in ordinary tests.
The workflow expands its compiled-binary test selection to include these cases.

macOS cannot execute these Linux descriptor tests. Linux compilation, vet and
staticcheck are useful prerequisites, but actual native workflow execution on the
current commit is required before recording acceptance evidence.

## Remaining execution work

The root-owned data namespace is pinned exactly as prepared by the current quota
provider. This alone does not prove that UID 65532 can read application data;
normal data access requires the separate reviewed ownership/access transition.
The helper does not chown a verified namespace or relax its permissions to make a
container start. Do not interpret successful pins as an application ready state.

Bind these pins and actual project-quota/root evidence into the durable claim and
receipt path. Provision/verify socket directories and broker socket identity,
then implement start/readiness/stop and candidate-specific recovery. Complete the
active-writer transition and automatic backend registration before managed install
acceptance. Production/beta settings, real credentials, Status migration, SDK
freeze, D5/D6 and complete platform lifecycle remain outside this change.

## Local verification record

Portable runner/CLI race regression passed (journal 68.584 seconds). Linux
amd64/arm64 driver compilation and Linux-target vet/staticcheck passed. New
native mount cases are gated and have not been executed by these macOS checks;
the corresponding disposable CI result remains required.
