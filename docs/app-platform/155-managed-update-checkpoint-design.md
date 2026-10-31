# Managed update checkpoint and execution gate

Status: design gate, 2026-10-31. This document does not claim that managed
update execution is implemented.

## Decision

An ordinary managed update cannot reuse the initial-install candidate command
unchanged. The install path requires one applied `prepare_data` command for the
same operation and generation, together with one data resource reservation. An
update intentionally preserves the installation identity, data namespace and
writer fence while changing the release and generation. Treating install data
metadata as update evidence would fabricate history and is refused.

The first managed update implementation must therefore add four closed pieces:

1. a quiescent checkpoint primitive over the existing XFS project namespace;
2. a signed runner command that creates and verifies that checkpoint;
3. a separate generation-bound update candidate command that references the
   checkpoint and the exact stopped source generation; and
4. a restore or compatible rollback path before target activation becomes
   visible to users.

No review route, scheduler claim or `managed=1` update operation may be exposed
before those pieces are available.

## Current constraints

- `internal/apprunner/journal/candidate_reservation.go` derives candidate data
  evidence only from `prepare_data` commands for the candidate's own operation
  and target generation.
- `runner_resources` currently carries one resource row per native identity and
  binds the data namespace to the container generation.
- The installer's ordinary candidate mount is read-only. Writable candidates
  deliberately mount application data read-write and can change bytes after
  start.
- `app_platform_recovery_sets` already has a closed schema with states
  `preparing`, `verified`, `restoring`, `deleted` and `review_required`, but no
  producer or verifier exists.
- The data adapter can create and inspect a project-quota namespace. It has no
  checkpoint, restore or recursive delete primitive.
- Portal completion, runner journal completion, Docker state and XFS state do not
  share one transaction. Every boundary must be recoverable by exact identity.

## Checkpoint primitive

Checkpoint input is reconstructed only from trusted journal and Portal state:

- root filesystem UUID and inode;
- exact namespace ID and XFS project ID;
- device and namespace inode;
- byte and inode limits;
- writer fence and admission fence;
- release digest and data format;
- a fresh server-generated checkpoint ID.

The source process must first be observed stopped. The runner then opens the
namespace relative to its pinned root descriptor with `O_DIRECTORY|O_NOFOLLOW`;
it never accepts a caller path. It walks only regular files and directories.
Symlinks, device nodes, sockets, FIFOs, hard-link ambiguity, traversal names,
unexpected ownership, foreign project IDs and unbounded file counts are refused.

File content and metadata are copied into a bounded checkpoint directory owned by
the runner root. The manifest records canonical relative names, file type, mode,
owner identity, size and SHA-256. The manifest is fsynced, then its digest and
total bytes are returned as opaque native evidence. The checkpoint remains
`preparing` until the runner journal atomically records that evidence and Portal
records the same manifest digest.

A copy is not considered verified merely because the process returned. It must be
read back and rehashed before either store changes the recovery set to `verified`.

## Journal protocol

The exact format is a new signed lifecycle command family, not a relaxed
candidate command:

- `checkpoint`: source installation, source generation, source release, source
  start receipt digest, namespace, writer fence and checkpoint ID;
- `update-start`: source checkpoint receipt, target operation and generation,
  target release, target policy revision and runtime identity;
- `restore`: verified checkpoint ID and the exact recovery-set/operation
  identity;
- `read`: retrieves only an applied receipt for the same signed logical command.

Each command persists intent before native IO. Unknown outcomes are not retried
under a new identity. The journal verifies that the source process resource is
stopped and that no prepared or unknown command remains before checkpointing.

`update-start` reserves a new container resource ID while allowing the old
stopped resource to retain the same namespace. The two rows must be explicitly
related by installation, namespace, source generation, target generation and
source checkpoint. Existing install admission remains unchanged.

## Portal operation tuple

A managed update review binds:

- installation, app and publisher identity;
- source and target release digests;
- source and target generations;
- source active policy revision and target policy revision;
- runtime ID and control revision;
- exact data namespace, schema, writer fence and recovery-set ID;
- target capability selection and current actor/trust revisions.

Consumption creates one `managed=1` update operation with
`source_generation=N`, `target_generation=N+1`. It does not change the live
activation, generation or policy until target readiness and registration are
verified.

## Ordered execution

1. `queued -> validate`: recheck actor, publisher, runtime, source activation,
   grants, source process receipt and exact data namespace.
2. `download -> stage`: verify the immutable target release and backend image.
3. `quiesce`: send the existing signed managed stop for the source generation
   and require an opaque exited observation.
4. `snapshot`: create the quiescent checkpoint and persist both native and Portal
   manifest evidence.
5. `migrate`: initially support only the same data schema and an explicit no-op
   migration completion. Any schema change remains unavailable until a bounded
   migration command and postcondition verifier exist.
6. `start`: start the target candidate with `update-start`, preserving the data
   namespace and increasing the writer/control generation fences.
7. `probe`: require authenticated target readiness.
8. `activate`: switch one active generation/policy tuple, retire the source
   activation, replace capability rows and complete the operation in one Portal
   transaction.

No stage may be skipped because an earlier update happened to succeed.

## Failure and rollback

- Before checkpoint verification, the source release remains the only admissible
  release. A stopped source may be restarted only by a separately observable
  enable/recovery action.
- After a verified checkpoint but before target readiness, failure enters
  `recovery_required`. The old release may restart only when the data schema is
  still readable; otherwise the checkpoint must be restored first.
- After any target process may have mounted writable data, a source restart
  requires either the explicit same-schema proof before target start or a
  verified restore.
- After target activation commits, the old generation is retired. Rollback is a
  new reviewed operation, not an implicit mutation of completed history.
- Lost native acknowledgements preserve unknown effects and never trigger an
  automatic repeated start, checkpoint or restore.

## Required tests

Portable and repository tests:

- namespace/path/symlink/device/ownership/project/quota refusals;
- deterministic manifest order and digest;
- crash before and after checkpoint creation;
- journal unknown/apply/CAS and repeated-read behavior;
- Portal recovery-set uniqueness, exact source binding and rollback;
- stale runtime/release/policy/checkpoint/generation refusals;
- no target activation without applied checkpoint and readiness evidence.

Disposable Linux/XFS/Docker tests:

- create real data, stop a real source candidate, checkpoint it, start a distinct
  target candidate and verify the exact container identities;
- inject target readiness failure and prove compatible source rollback;
- mutate the namespace after checkpoint, force restore and verify the restored
  manifest/content;
- crash at each external boundary and reconcile without duplicate effects;
- verify retained bytes, project limits and no foreign namespace access.

## Implementation order

1. Native quiescent checkpoint/read-back primitive and refusals.
2. Journal checkpoint command, receipt, crash recovery and repository tests.
3. Portal recovery-set preparation/verification transaction.
4. Distinct update-start admission with source-checkpoint binding.
5. Managed update executor and activation transaction.
6. Restore/rollback executor and disposable Linux acceptance.
7. Only then expose managed update through scheduler and management HTTP.
