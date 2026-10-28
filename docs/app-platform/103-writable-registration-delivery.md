# Deliver and reconcile protected writable backend registration

This implements the physical dependency after
[102](102-writable-registration-intent.md). The existing signed request binds an
exact completed writable process receipt and private document SHA. This slice
adds once-only claiming, protected atomic publication, observation-only recovery
and current historical delivery reads. It does not complete app handshake,
viewer access, activation or automatic installation orchestration.

## Trusted order

1. `DispatchWritableBackendRegistration` accepts only bounded signed reference,
   descriptor/document bytes and the configured concrete native runtime. Clone
   inputs, retain the Store execution gate through all work, and clear the private
   document clone at exit. No document/key bytes enter rows, output or errors.
2. Verify current enrollment and all nested/outer signatures. Validate document
   SHA and descriptor/app/deployment/epoch/installation/release/generation/policy
   identity **before** admission or claiming. Invalid private material leaves an
   existing prepared intent unchanged and creates no file/claim.
3. Admit/replay the exact digest-only command. A prepared command first undergoes
   actual writable native/private-socket readiness. Recheck the exact current
   source/resource/own prepared row after this IO. Stopped/unready preflight
   leaves prepared state available for a later current preflight retry.
4. Under current authority, `claimWritableRegistration` commits an unknown marker
   before yielding a private Store-owned once-only attempt. Marker records claim/
   effect/payload/source/document/native identity, current control revision and
   original claim/expiry window. A failed claim commit yields no attempt.
5. Only the local attempt may enter first delivery. Require exact owner, marker,
   current original revision/expiry and unconsumed monotonic deadline. Consume it
   once before possible native effect. No public executable handle is exported.
   Reload current unknown state and original source again before native IO.
6. Native `DeliverWritableBackendRegistration` parses bounded private material
   and descriptor independently, validates managed identity and rebuilds/pins the
   exact original writable profile/image/data/root/quota/private mount evidence.
   Require one exact recorded native ID and complete running policy/ownership,
   including running and stopped private-storage reference inventory.
7. Probe the protected UID/GID/inode/peer-bound Unix readiness socket again; schema
   must match the actual descriptor. Select only the already pinned fixed broker
   directory. Apps/callers cannot provide a path, filename, mount, UID or mode.
8. Reuse the existing Linux protected-file primitive: anonymous `O_TMPFILE`,
   bounded content, claim-hash xattr, UID/GID 65532, mode 0400, fsync, then atomic
   no-replace `linkat` to `registration.json` and parent fsync. Partial private
   files are not exposed. Existing files are never overwritten or repaired.
9. Independently read the exact protected file/content/xattr/ownership/mode/link
   identity, then repeat full native/data/reference/pin checks. Return only opaque
   `WritableBackendRegistrationObservation`, with original writable preflight,
   native ID, binding/document hashes, device/inode and fresh observation time.
10. Reopen the current unknown transaction, compare exact pre-IO source/resource/
    receipt/claim/timestamps and current authority/fences. Only that concrete
    opaque result can settle. Atomically CAS the unknown command to a typed applied
    receipt. Resource, writer and start rows stay unchanged. Commit failures keep
    unknown marker even if the protected file already exists; never repeat write.

No SQL transaction spans native/socket/file IO. Closing waits for owner-gated
work. The private native helper now supports a closed infrastructure action under
before/after checks: readiness and process recovery remain read-only; only the
separate registration method supplies this fixed publication primitive.

## Unknown recovery

An unknown command never produces a new write attempt. Require a fresh signed
current registration reference whose issuance is after the original claim expiry
and no later than now. Original claim, revision, expiry and file-binding hash
stay unchanged. Recheck native readiness and current journal facts, then invoke
only `ObserveWritableBackendRegistration`.

That native method uses the same strict identity/policy/pin/data checks and fixed
protected-file reader. It does **not** invoke create, overwrite, chmod repair,
provisioning, container start/stop or any other recovery write. Missing, changed,
ambiguous, unsafe or differently bound files remain unknown. A fresh successful
observation can record an applied recovered receipt; a failed metadata commit may
be retried by another current observation without changing the file.

If the file was never published, this protocol deliberately does not infer
permission to write again. Resolving/abandoning a proven no-effect command and
retrying installation belongs to the remaining lifecycle reconciliation work.

## Metadata and readers

The claim format is
`portal.runner.writable-backend-registration-claim/1-dev.1`. Its control revision
must not precede either the source's original claim revision or its later process
recovery revision, and cannot exceed current authority. Claim must follow intent
creation, which must follow source process settlement. Its original bounded
execution window is at most 30 seconds.

Normal receipt format is
`portal.runner.writable-backend-registration-delivery/1-dev.1`; file observation
and settlement must follow claim and precede original expiry. Recovered format
is `portal.runner.reconciled-writable-backend-registration-delivery/1-dev.1`;
record separate current envelope hash/revision/issuance/expiry, requiring
`original expiry < new issuance <= observation <= settlement < new expiry` and
at most 30 seconds. File binding SHA remains the original canonical claim hash.
Receipt JSON is strict/canonical and at most 8192 bytes; it contains no secret.

`readWritableRegistrationRow` independently validates prepared, unknown or applied
state before its exact row can be exempted from source history. Normal start/
readiness readers remain strict. The prepared intent reader still refuses claimed
or delivered state. `ReadWritableBackendRegistrationReceipt` accepts only applied
state under current signature, full original source/resource/fence checks and
matching exact receipt/timestamps. It reads historical evidence without native
IO. Applied dispatch replay returns the same validated history without rewriting
the file. Neither API proves current file presence, app consumption, handshake,
backend access or activation.

## Verification

SQLite/crypto tests cover durable claim/rollback, original binding, inability to
reclaim/reuse/transfer the write attempt, missing opaque evidence, current source/
resource/authority/ack/time drift, normal/recovered receipt windows and historical
renewal. Synthetic historical rows prove metadata validation only; no native
success is manufactured. Opaque file proof refuses JSON and conversion to
process/readiness evidence. Legacy protected-file tests remain unchanged.

The disposable XFS/Docker fixture adds actual cases:

- Invalid private bytes leave prepared state and publish nothing; normal delivery
  identifies the real protected file, and applied replay preserves its inode,
  ownership, mode, size and modification/change timestamps.
- File publishes, then receipt commit fails. Require exact injected error and
  durable unknown marker; independently inspect/read the synthetic protected file.
  Close/reopen both owners, wait original expiry, sign a fresh envelope, inject a
  recovery metadata failure, then retry observation. Both failures and successful
  recovery preserve the original file identity/content/timestamps and claim.
- An independently claimed but intentionally unwritten registration on a running
  real backend expires. Fresh observation must fail without creating a missing
  file or changing any deployment/command/resource row.
- A deliberately stopped backend fails preflight and keeps prepared intent; no
  file is published and no container is restarted.

Private material is synthetic and confined to disposable owned memory/storage.
Test owner-object reopen is not whole daemon kill/relaunch acceptance. Native
fixture cleanup independently validates owned containers and existing IPC, then
removes only its owned temporary socket tree. No production lifecycle route is
exposed. Parent requires four new delivery/recovery/missing/stopped markers along
with every prior source/start/readiness and owned cleanup check.

Local full runner race passed (journal 145.827s, driver 2.177s, transport 1.377s;
control/data cached). Focused registration/legacy registration race passed
(journal 17.965s, driver 1.347s), and platform regression passed in 10.201s.
Linux amd64/arm64 journal builds and arm64 driver build, vet, Linux staticcheck
and all 14 Python probe tests passed. Expanded registration/postflight race passed
in 21.877s.

Functional source `92250b151b31e7105a7aee89b6bc00bb49a0db2a` passed quota
workflow 40785510608/job 123157574157. All four new registration delivery,
file-preserving recovery, missing-file non-creation and stopped-delivery refusal
flags are true, alongside every previous writer/start/recovery/readiness and
owned-resource cleanup flag. Linux control/appplatform/transport/driver race
passed in 1.582/1.374/4.779/2.472s. These checks do not establish a completed
installation, full daemon kill/relaunch or handshake.

Daemon workflow 40785510581/job 123157573982 passed full Linux journal race in
147.957s followed by actual native policy/IPC, distinct identities and Portal
root endpoint acceptance. General workflow 40785510624 passed app race shards/
aggregate, all web test shards, lint/typecheck/build, workflow lint and Docker
image/smoke checks. Go job 123157574208 passed build/vet/staticcheck, then failed
the existing `golang.org/x/net@v0.60.0` vulnerability gate
(GO-2026-6866/GO-2026-6860); its subsequent ordinary test did not run. No
dependency change was made and these separate successes do not clear that gate.

## Next dependencies

Implement writable backend handshake/current viewer delivery, then integrate
signed controller transport, managed activation/worker and normal app install.
Physical stop/fencing, unknown no-effect lifecycle resolution, update/uninstall/
restore, capability broker, SDK/shared shell/reference apps, marketplace install
and full P00–P14 acceptance remain incomplete. Production, real credentials,
Status adoption, public SDK freeze and beta/version configuration are unchanged.
