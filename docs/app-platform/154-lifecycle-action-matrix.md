# Application lifecycle action matrix

Status: H13 implementation checklist, 2026-11-01. Managed restore and managed
enable now have native runner acceptance; rows without exact evidence remain
open.

| Action | Entry state and review | Immediate authority | Data/code effect | Failure/uncertain |
| --- | --- | --- | --- | --- |
| Install | no live installation; signed release + trust + audience + exact plan | review grants nothing; consumption queues work | stage immutable frontend/backend, prepare namespaced data, start/probe, activate one generation | resumable operation; unknown native effect becomes recovery review |
| Update | live enabled install; current tuple + target release/permissions diff | prior generation remains active until candidate is verified | preserve installation ID; new code/release/policy tuple; compatible migration only from a checkpoint | candidate failure may keep old generation only when data remains compatible; otherwise recovery review |
| Disable | live enabled/ready; exact current tuple | stop new viewer calls, task claims, network/secret use immediately | keep code/data/credential tombstones; physical process stop separately observed | runner offline reports stop pending, never claims stopped |
| Enable | disabled install; fresh trust/state/health review | no capability expansion by enable | start current reviewed tuple and probe before traffic | failed probe remains disabled/recovery review |
| Repair | recovery/paused state; current plan | only explicitly admitted recovery work | reconcile actual process/data/effect; no blind replay | uncertainty stays review-required |
| Uninstall | live install; exact plan + affected counts | revoke access, launch, tasks, notifications, bindings and runtime authority first | clear the dedicated plugin table; managed apps then stop/remove code and delete their backend data namespace and project quota | completion requires a signed data-deletion receipt; any uncertain stop/code/data effect remains `recovery_required` |
| Delete data | legacy retained namespace, recovery copy or orphan explicitly bound by review | no app authority | bounded namespace cleanup; revoke/delete data records and counters transactionally where Portal-owned | deletion idempotent; interrupted cleanup resumes only the recorded namespace |
| Restore | isolated restored database; controller epoch newly rotated and dispatch paused | all old live authority invalid | reconcile process/data/effect identities and revocation floors; active work becomes review | no automatic resume, delivery, provider probe or task replay |

## Current implementation

Implemented and tested:

- signed install review/consume, queued operation, staged frontend and managed
  backend paths;
- generation/fence/lease/epoch checks across install stages;
- disable and uninstall admission with immediate authority denial;
- frontend disable through `revoke -> complete` and managed disable through
  `revoke -> stop -> complete`; managed completion requires an exact stopped
  receipt, and offline/uncertain stop work remains failed or `recovery_required`
  without deleting code, data or grant rows;
- frontend enable review, idempotent admission and scheduling through
  `validate -> activate -> complete`; execution revalidates the stored release,
  current grants, audience, compatibility and data configuration, restores an
  activation for the same release tuple and never recreates a plugin table;
- managed enable review through the existing signed
  `snapshot -> enable-start -> probe -> registration -> boot -> activate`
  chain. The checkpoint binds the retained namespace and current writer fence;
  the target keeps the same release, policy and grant rows, while the new
  generation gets fresh process, readiness, registration and boot evidence.
  Completed enable history reconstructs the current viewer request path from
  the update-start registration and preserves the prior capability review IDs;
- managed repair has a separate exact review and
  `validate -> download -> stage -> quiesce -> start -> probe -> activate`
  executor. It accepts only a disabled or recovery/review-required same-release
  tuple, revalidates the current runtime, signed release, policy, grants and
  audience, and takes a fresh checkpoint at `quiesce`. Start/probe/registration/
  boot/activation then use the retained data namespace with no capability grant
  replacement. A retry may reuse only an abandoned candidate activation whose
  readiness is empty and has no observation; prepared work from a previous
  attempt remains recovery evidence and is never replayed blindly;
- checkpoint and update-start history now reconstruct `enable` and `repair`
  source operations as well as install/update/restore, so repeated
  disable/enable/repair cycles keep the current generation tuple;
- uninstall revocation of credential bindings and transactional clear/drop of
  dedicated plugin object tables;
- reviewed frontend uninstall scheduling through
  `revoke -> remove_code -> complete`, with access/capability rows removed and
  the installation made permanently unavailable;
- managed uninstall review and execution through
  `revoke -> stop -> remove_code -> remove_data -> complete`. The exact signed
  stop/remove-code commands bind the
  original ordinary or writable candidate operation/effect/receipt/native
  identity; stop and remove-code accept only opaque `exited` and `absent`
  observations. The separate signed delete-data command binds the original
  preparation receipt, project ID, device, namespace inode, quota, writer fence
  and current release. Completion requires a receipt proving the namespace tree
  and quota are absent, then clears access/capability rows and marks the
  namespace `deleted`;
- managed uninstall failure/uncertainty is denial-only and lands in
  `recovery_required`; a prepared effect left by a crash is reclaimable only to
  record that review and is not automatically reissued;
- reviewed standalone `delete_data` accepts only one exact installation and
  namespace ID. The plan binds the retired activation/release, data revision,
  writer fence, prepare-data operation/effect/receipt, quota, project, device,
  inode, ownership digest, recovery-set digest, controller epoch and actor
  authority. Admission changes only that namespace to `deleting`, clears its
  owner and advances the writer fence; it does not remove code, access grants or
  the dedicated plugin table. Completion requires an exact signed runner
  receipt. Same-key retries replay one operation, two competing plans admit at
  most one operation, and an uncertain prepared effect stops at
  `recovery_required`;
- a disposable GitHub-hosted Linux/XFS/Docker acceptance run completed ordinary
  and writable managed uninstall end to end, verified the exact plugin table was
  removed, the managed namespace became absent, and its project quota was
  cleared;
- restore at the frontend clear-on-uninstall crash point keeps the installation
  removed, recreates no plugin table or live activation, preserves an empty
  object store, and forces the unfinished operation to `recovery_required`
  under the restore pause;
- restoration pause, controller-epoch rotation and credential/binding revocation
  floors;
- backup of control, credential, object and plugin-table products; and
- operation recovery classification for interrupted stages/effects.

Still required for H13 completion:

- full concurrent operator acceptance across enable/update/repair, not only
  delete-data admission;
- native repair execution against the disposable Linux/XFS/Docker runner;
- static frontend repair and end-to-end lifecycle evidence for every row,
  including lost confirmations.

The standalone delete-data path has SQLite and PostgreSQL 18.6 repository
coverage for stale plans, concurrent confirmations, two-admin conflicts,
transaction rollback and uncertain runner results. A native standalone
delete-data acceptance run has not yet replaced the existing managed-uninstall
native evidence.

Run `41330590646` completed install, A-to-B update, failed-update recovery,
managed disable, managed enable, a real reader backend request after enable, and
clear-on-managed-uninstall through `stop -> remove_code -> remove_data`. The
same workflow later skipped the separate one-image UUID replacement check
because the disposable loop remained externally held; that skip is recorded as
an open identity-test gap, not as lifecycle evidence.

No row may be made safe by a UI-only check, a cached generation, a copied database
or a successful database mutation that was not observed at the actual runtime.

The pure upgrade/rollback data gate is now implemented in
`internal/appplatform/data_lifecycle.go`: target data schema must not move
backwards, must explicitly list the current schema as readable, and must not
write on startup; rollback requires the previous release to explicitly read the
migrated schema. Malformed, duplicate or out-of-range declarations are refused.

Normal static frontend update admission is also implemented in
`app_platform_update_review.go`. It verifies the live source generation,
release, policy, installation revision and grant digest; evaluates the target
signed package and capability diff; enforces the data-upgrade gate; and consumes
an exact admin review into an `update` operation with
`source_generation=N, target_generation=N+1`. Admission deliberately leaves the
live installation generation/phase and source activation unchanged until a
frontend candidate reaches verified activation.

The frontend update executor now claims queued updates after ordinary installs,
resumes the durable `queued -> ... -> activate` stages, and activates the target
only in the final transaction. That transaction retires the source activation,
creates the target generation/policy activation with a canonical readiness
proof, replaces capability grants, and advances the installation revision.
Launch revalidates the exact completed install/update operation, reviewed plan,
release/document digests, generation/policy, and current access audience. Empty
readiness, a stale generation, or an old audience digest cannot launch.
Expired workers can be reclaimed at their persisted stage with a new fence;
installation revision is a reviewed lower bound, while the current generation,
policy, release, phase, audience and capability digest remain exact checks.

Retry exhaustion is denial-only: the update operation records
`update_retry_exhausted` and the source generation remains active. The
management HTTP path and Portal application panel expose the same reviewed
plan/confirm flow for install, frontend update, and frontend uninstall,
including generation and permission-diff review. The same panel identifies the
currently installed release and requires a separate danger-styled uninstall
confirmation. Uninstall object cleanup is clear-on-uninstall and runs inside the
reviewed admission transaction; a failure after table cleanup rolls the cleanup,
counters, authority revocation, and operation insert back together. After
confirmation, the frontend removal scheduler completes the logical code-removal
stages and clears access/capability rows; retry exhaustion leaves the app
unavailable rather than restoring authority.
