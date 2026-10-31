# Application lifecycle action matrix

Status: H13 implementation checklist, 2026-10-31. This is not evidence that
every row is implemented.

| Action | Entry state and review | Immediate authority | Data/code effect | Failure/uncertain |
| --- | --- | --- | --- | --- |
| Install | no live installation; signed release + trust + audience + exact plan | review grants nothing; consumption queues work | stage immutable frontend/backend, prepare namespaced data, start/probe, activate one generation | resumable operation; unknown native effect becomes recovery review |
| Update | live enabled install; current tuple + target release/permissions diff | prior generation remains active until candidate is verified | preserve installation ID; new code/release/policy tuple; compatible migration only from a checkpoint | candidate failure may keep old generation only when data remains compatible; otherwise recovery review |
| Disable | live enabled/ready; exact current tuple | stop new viewer calls, task claims, network/secret use immediately | keep code/data/credential tombstones; physical process stop separately observed | runner offline reports stop pending, never claims stopped |
| Enable | disabled install; fresh trust/state/health review | no capability expansion by enable | start current reviewed tuple and probe before traffic | failed probe remains disabled/recovery review |
| Repair | recovery/paused state; current plan | only explicitly admitted recovery work | reconcile actual process/data/effect; no blind replay | uncertainty stays review-required |
| Uninstall | live install; exact plan + affected counts | revoke access, launch, tasks, notifications, bindings and runtime authority first | clear owner-approved app object table; retain or delete managed backend namespace according to reviewed effect; remove code after drain | partial physical stop/code removal remains `recovery_required`; access stays denied |
| Delete data | uninstalled or explicitly retained namespace; separate plan | no app authority | bounded namespace cleanup; revoke/delete data records and counters transactionally where Portal-owned | deletion idempotent; interrupted cleanup resumes only the recorded namespace |
| Restore | isolated restored database; controller epoch newly rotated and dispatch paused | all old live authority invalid | reconcile process/data/effect identities and revocation floors; active work becomes review | no automatic resume, delivery, provider probe or task replay |

## Current implementation

Implemented and tested:

- signed install review/consume, queued operation, staged frontend and managed
  backend paths;
- generation/fence/lease/epoch checks across install stages;
- disable and uninstall admission with immediate authority denial;
- uninstall revocation of credential bindings and transactional clear/drop of
  dedicated plugin object tables;
- reviewed frontend uninstall scheduling through
  `revoke -> remove_code -> complete`, with access/capability rows removed and
  the installation made permanently unavailable;
- managed uninstall review and execution through
  `revoke -> stop -> remove_code -> complete`. The exact signed command binds the
  original ordinary or writable candidate operation/effect/receipt/native
  identity; stop and remove-code accept only opaque `exited` and `absent`
  observations. Completion clears access/capability rows, makes the installation
  unavailable, and retains the managed data namespace as `retained` for a
  separate reviewed deletion;
- managed uninstall failure/uncertainty is denial-only and lands in
  `recovery_required`; a prepared effect left by a crash is reclaimable only to
  record that review and is not automatically reissued;
- restore at the frontend clear-on-uninstall crash point keeps the installation
  removed, recreates no plugin table or live activation, preserves an empty
  object store, and forces the unfinished operation to `recovery_required`
  under the restore pause;
- restoration pause, controller-epoch rotation and credential/binding revocation
  floors;
- backup of control, credential, object and plugin-table products; and
- operation recovery classification for interrupted stages/effects.

Still required for H13 completion:

- managed update A-to-B execution (the current generic update executor is
  frontend-only);
- native disposable acceptance of managed uninstall, including restoration
  around stop/remove-code and reconciliation with the live runner;
- reviewed managed namespace deletion after the retained state (frontend plugin
  object data is already cleared at uninstall admission);
- retained-data adoption is superseded for the owner-approved static object
  store by clear-on-uninstall; managed namespace retention/deletion remains;
- explicit `delete_data` review/consume path after uninstall;
- full concurrent operator acceptance;
- end-to-end lifecycle tests for every row above, including lost confirmations.

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
