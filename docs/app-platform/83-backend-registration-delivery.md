# Protected backend registration delivery

This development phase adds actual native bootstrap file publication and durable
uncertain-outcome recovery. It does not complete automatic Portal RPC delivery,
application boot handshake, request forwarding, app-use grants or activation.
The current path remains initial managed installation, generation/policy 1.

## Invocation and authorization

The infrastructure entry point is
`Store.DispatchBackendRegistration(ctx, raw, signature, descriptor, document, native)`.
The signed command remains the digest-only contract from document 82. Private
bytes are a separate bounded argument (at most 4096 bytes), never a journal value.
Raw command, signature and descriptor are bounded before cloning. The dispatcher
clones and clears its private document copy; callers must clear their own copies.

The caller needs a concrete native runtime. Applications and browser payloads
cannot supply a filesystem path, native container ID or callbacks. Native paths
come exclusively from the already configured protected runtime roots and the
verified candidate identity.

## Durable state sequence

1. Take the shared execution gate and verify current independently enrolled
   controller authority, both signature domains, descriptor and exact document
   identity/digest before admitting an intent.
2. Under a write-intent transaction, require the same generation/fence floors,
   no conflicting pending command, original applied candidate and full running
   receipt/resource checks, and original applied quota/data evidence.
3. For prepared work only, create a fresh random claim and commit `unknown`
   before native file IO. The marker binds effect/payload, source receipt digest,
   native container ID, document digest and claim time. No secret is persisted.
4. Reconstruct a native launch preflight and require its snapshot to equal the
   original started candidate's preflight. Recheck current journal authority and
   the exact marker after this IO, immediately before the effect.
5. Publish the file once, or for unknown work use the read-only observation path.
   Both paths check actual running container policy, pinned mounts and concrete
   quota/data evidence before and after file IO. Signed expiry bounds the attempt.
6. Settle only an opaque observation created by the concrete native runtime,
   after another transaction revalidates the exact source/history/marker. Record
   public binding/digest, native device/inode and observation/settlement times.

An applied replay returns its historical delivery receipt under current signed
journal authority. It does not claim the file still exists or the app is healthy.
A zero/parsed observation cannot settle an effect. Receipt parsing alone grants
no authority. Shutdown releases pins without deleting an active workload/file.

## Native file rules

The fixed destination in the existing root-owned broker directory is
`registration.json`, visible inside the app as
`/run/portal-app/broker/registration.json`. The broker directory is already
mounted read-only by the platform container policy. The file is a regular file,
owner/group 65532, mode 0400, exactly one link and bounded size.

Publication uses `openat(O_TMPFILE)` relative to the pinned directory. An
anonymous file is written completely, assigned a public claim-binding xattr
`user.portal.registration`, chowned/chmodded and fsynced before publication.
`linkat(AT_EMPTY_PATH)` publishes to the one fixed filename without replacement;
then the parent directory is fsynced. Unsupported filesystems fail closed.
No named temporary file exposes partial key bytes to the app. Existing names
are never removed, overwritten, chmodded or repaired by this path.

The binding is SHA-256 of the canonical durable random claim marker. Recovery
requires that exact xattr in addition to byte equality, ownership, mode, size,
link count, descriptor-relative no-follow opening and stable inode/metadata.
Thus an older file with coincidentally identical registration bytes is not
accepted for a new claim. FIFO/directory/symlink/hardlink and oversized or changed
files are refused. Reads are bounded and temporary read buffers are cleared.

## Failure and retry behavior

If file publication may have occurred but SQL settlement fails, leave `unknown`.
A retry with current authorization only observes the exact existing claim-bound
file. It never resumes anonymous creation, repairs a file or invents success from
a JSON receipt. Missing/mismatched evidence remains uncertain for later explicit
operator reconciliation. A prepared claim cannot be obtained twice.

Authority can change during IO. Post-IO validation refuses settlement if current
source/floors/history no longer match; the effect remains uncertain. This phase
provides no rollback promise for a file already published. Later lifecycle
reconciliation must retire files/workloads under separately verified authority.

## Tests and native acceptance

Journal tests verify durable unknown claiming, claim refusal before admission and
on replay, source/resource/data/floor/acknowledgment conflicts, malformed receipts
and refusal of a zero native observation. Synthetic metadata tests are explicitly
not native proof. Linux file tests cover no-follow/type/link/mode/content/binding
and size refusals. Root-only anonymous publication checks cancellation, no
overwrite, stable inode and absence of named temporary files.

The actual Linux fixture passes prepared Portal registration bytes through a
new inherited one-way OS pipe to its root parent, with a separate public descriptor
and digest. It never puts the private document in environment, stdout or a
plaintext staging file. The root test controller still signs the sample delivery
command: this fixture transport is not the production managed RPC.

The parent deliberately aborts only the registration SQL settlement after native
publication. It requires a mode-0400 file and an unknown journal effect, removes
the fixture trigger, retries using observation, requires the same inode, and
checks historical replay. The acceptance script requires
`BACKEND_REGISTRATION_DELIVERY_RECONCILED` before reporting
`native_backend_registration_delivery_reconciled: true`.

Local focused journal race, full runner/CLI race (journal 85.947 s), focused
Portal SQLite/managed regressions (15.951 s), Linux amd64/arm64 compilation,
vet/staticcheck and 14 Python tests passed. Functional source
`fbbaadb617726438f74aed016894bc4f23122377` passed native quota workflow
[40620897099](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40620897099),
job 122594632732: `native_backend_registration_delivery_reconciled`, actual
backend readiness and owned cleanup are true; exact registry cleanup passed.
The root native candidate test also invokes the atomic-publication contract,
so cancellation/no-overwrite/temporary-file checks execute under Linux root.
Linux transport/driver race passed in 4.630/1.602 s. These results prove actual
protected publication and same-inode recovery, not application consumption,
handshake or production controller RPC.

Daemon probe 40620896826 also passed. General CI 40620897103 completed with all
three app race shards and aggregate, web checks/test shards, workflow lint and
Docker smoke passing. Go job 122594659749 passed build/vet/staticcheck and market
validation, then failed the existing x/net v0.60.0 vulnerability gate
(GO-2026-6866 and GO-2026-6860, reported fixed in v0.62.0), skipping the following
non-race Test step. General CI is not all green.

## Remaining platform work

Connect the protected production controller-to-runner registration channel and
Portal effect/receipt settlement. Add real app boot handshake and current
viewer/OU-bound forwarding, broker/provider denial, writer authorization and
managed activation with fresh health checks. Bounded writable socket storage,
automatic image fetching and scheduling/startup, full cleanup/reconciliation,
upgrade/rollback/uninstall/reference apps and cross-repository SDK publication
remain. No public SDK freeze, real credentials, production deployment, beta
configuration, database schema or module/toolchain versions change here.
