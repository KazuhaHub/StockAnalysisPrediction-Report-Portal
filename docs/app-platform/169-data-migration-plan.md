# Application data migration: implementation plan

Status: accepted 2026-11-06. Turns section 2 of
[doc 167](167-owner-decisions-remaining-acceptance.md) into ordered, test-first steps, following
the lessons recorded in [doc 168](168-fresh-host-restore-plan.md). No Portal DDL is needed:
`app_platform_operation_effects.kind` has no enum check. Journal v2 already lists `migrate-data` and
`discard-migration`, and its stage enum includes `migrate`.

## Owner decisions taken with this plan

- **Completing an unknown restore under the same claim is approved.** The extraction from a
  verified checkpoint is convergent, and finishing the original claim is not a retry under a new
  identity. Doc 167 supersedes doc 155's "never automatic repeated restore" for this case. Doc 155
  §5, which says `migrate` is a no-op only, is also superseded.
- **Automatic settlement restores without a review.** The verified pre-update checkpoint is the
  reviewed source of truth, and both recovery choices start from it.
- **A lost runner journal outside recovery is treated as host loss.** The operator uses the
  fresh-host restore (doc 168). No normal-mode abandon is added.

## Resolutions of conflicts between doc 167 §2 and the code

1. The `migrate` stage removes the source today. That stays effect 1 (`remove-code`), and
   `migrate-data` is effect 2.
2. Managed update review refuses any schema change. It uses `PlanDataUpgrade` instead.
3. Fences and formats:
   - migration writer fence is S+1, target is S+2;
   - activation sets the fence and `data_format = to` exactly;
   - the candidate and readiness format is `to`.
4. The settlement effects attach to the failed update operation, which stays `recovery_required`.
   An idle driver runs them in both normal and paused mode. The operation model does not change.
5. A checkpoint restores only once. Roll back takes its restore stage on evidence alone;
   restore-start already accepts revision 4.
6. An unknown restore is completed under the same claim. It runs detached and bounded, it can be
   read back under a new restore read purpose, and the runner sends a typed absence only under the
   gate. Absence is definite only after the signed window, and only then is the restore resent
   under the same effect id.
7. Roll back after a failure with no applied start advances quiesce on evidence. This applies when
   no start was reserved, or when settlement already removed it.
8. Retry is a new `update` plan carrying `recovers_operation_id`, admitted from
   `recovery_required`. Its quiesce and remove-code steps advance on the failed operation's removal
   evidence, and it takes a fresh checkpoint of the restored data. The launch predicate also
   accepts a completed update that recovers the failed operation, or that operation's root.
9. Container create gains `Entrypoint` and `Cmd`, pinned by the policy digest.
10. Restore gets its own read signature purpose.

## Contract

The descriptor's `data.migrations` holds at most 8 steps. Each step has:

- `from` and `to`: integers in the schema domain;
- `entrypoint`: 1–16 printable ASCII strings, each at most 256 bytes, the first an absolute path;
- `wall_clock_seconds`: optional, between 1 and 600.

Validation rules:

- migrations require a backend;
- `to` equals `data.schema`, and `from < to`;
- `from` values are unique.

`PlanDataUpgrade(current, target)` returns the step whose `from` equals the current schema.
Otherwise the existing compatibility rule applies, with no step. Otherwise it refuses.

Regenerate with `scripts/generate_app_contract.py` and update the digest pins.

## Commands

Each command has command, read and effect signature purposes. Portal signs the execute signature
and the read signature with the command.

**`migrate-data`**: stage `migrate`, effect 2.

- **Admission**, in one transaction:
  - `remove-code` applied and the source resource absent;
  - no other live namespace container;
  - checkpoint verified;
  - fences S < S+1 < S+2, with S equal to the checkpoint writer fence;
  - marker absent or equal to `from`;
  - one migration per operation, plus root history;
  - the job resource is reserved;
  - the effect is claimed `unknown` before any native call.
- **Job**: the target image runs with the step's entrypoint under these constraints:
  - `network=none`;
  - the backend's memory, CPU and PID bounds;
  - read-only rootfs, all capabilities dropped, the writable seccomp profile;
  - one `/data` read-write mount;
  - no broker, socket, credential or task access;
  - no restart.
- **Run**: under the data execution gate, detached, bounded by the wall clock plus 2 minutes. The
  job is created, started and waited for, and killed at the deadline. Its exit code is inspected.
  It is then removed, and absence is proven by name and by label. Finally the marker is read
  (`O_NOFOLLOW`, regular file, at most 32 bytes) and quota usage is checked.
- **Receipts**:
  - `applied` only on exit 0, a matching marker, and usage within quota;
  - any other definite outcome becomes `reverted` with a typed outcome, after absence is proven;
  - if removal fails, the effect stays `unknown`.
- **Read-back** returns one of: the receipt, `pending` (gate held), `orphaned` (`unknown` with the
  gate free), or a typed absence, given only under the gate for a missing row. An unknown row is
  never rerun.

**`discard-migration`**: stage `migrate`, effect 3.

- Under the gate, it finds the job container by name and labels, stops and removes it, and proves
  absence.
- One runner transaction applies the discard, reverts `migrate-data` (outcome `discarded`) and
  marks the resource absent.
- It refuses an applied `migrate-data`. A replay returns the same receipt.

**Restore**: the existing restore effect on `(update op, restore_data, 1)`, with the
recovery-set bookkeeping, plus resolution 6.

## Ordered steps

### Runner (steps 1–8)

1. Descriptor contract and `PlanDataUpgrade`.
2. Control contracts for `migrate-data` and `discard-migration`, and the restore read purpose.
3. Driver: migration container plan, entrypoint support, `RunMigrationJob`,
   `ProveMigrationAbsent`, `ReadSchemaMarker`.
4. Journal `migrate-data`: admission, claim, detached run, receipts, read-back.
5. Journal `discard-migration`.
6. Restore settlement: completion under the same claim, detached run, read-back with typed absence.
7. Update-start binding. It requires the applied migration receipt, refuses a discarded one,
   checks the fence order, and refuses a tainted namespace.
8. Transport actions and typed absence replies.

### Portal (steps 9–14)

9. Managed update review binds the migration step.
10. The migrate stage sends `migrate-data` after `remove-code`. A lost acknowledgement is resolved
    by read-back. A known failure moves the operation to `recovery_required`.
11. Start and activation use the fences and format above. Fixture B launches after activation.
12. Settlement driver:
    - removes the target first if a start was reserved;
    - reads back, then discards `migrate-data`;
    - restores, with read-back for an uncertain restore;
    - acts on absence only after the signed window;
    - refuses if the installation fence has moved.
13. Recovery choices (roll back, retry) on the existing plan and operation routes. Both are refused
    until settlement completes. There is no in-operation retry. Launch must work after either
    choice and after a retry of a retry.
14. Settings projection `migration_recovery` and the two buttons in the management UI. Then the
    acceptance harness for fixtures A, B and C1–C7, checking that the namespace digest equals the
    checkpoint after settlement.

The named failing tests for each step are listed in the review record of this branch's pull
request.

## Risks

- App code runs with read-write data. Seccomp, quota, inode and PID bounds are the only
  containment.
- The marker is self-declared, so a buggy migration can claim success. Readiness and the retained
  checkpoint are the backstop.
- A writer-fence value is reused across mutually exclusive futures: S+1 serves both the migration
  and a rollback start. Safety rests on absence proofs.
- The generation N+1 candidate row is shared by retry and roll back, so concurrent reviews must be
  serialized.
- The wall clock, kill and removal must fit inside the Portal's polling bound.
- Image presence depends on section 3 (`load-image`).
