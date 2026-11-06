# Fresh-host restore: implementation plan

Status: accepted 2026-11-06. This turns section 1 of
[doc 167](167-owner-decisions-remaining-acceptance.md) into ordered, test-first steps. It records
five places where doc 167 did not match the code and how each is resolved.

## Resolutions of conflicts between doc 167 and the code

1. **A paused Portal refuses every mutation.** Actors (`app_platform_actor.go`), leases
   (`app_platform_lease.go`) and managed restore review all require the pause flag to be clear,
   and nothing clears it. Resolution: a restricted *recovery mode*. While
   `app_platform_restore_paused` is set, only replacement-runtime enrollment, `restore_import`
   reviews and operations, and the per-installation resume are admitted. Every other mutation
   keeps returning `errAppPlatformControlPaused`. An administrator ends recovery, which clears the
   pause, only when no `restore_import` operation is in flight and every installation is either
   resumed or explicitly disabled.
2. **The code assumes exactly one `runtime_bindings` row** (`app_platform_runtime.go`,
   `app_platform_runtime_confirm.go`, `app_platform_data_intent.go`). Resolution: one helper,
   `platformActiveRuntime`, is used at all three sites. It selects the single enabled,
   keyed binding and ignores retired rows (`enabled=0`, no key). A replacement binding may be
   prepared only when every existing row is retired, which is the state restore already leaves.
3. **`restore-start` is admitted from local snapshot and prepare-data evidence**
   (`journal/update_start.go`), which a fresh host does not have. Resolution: a new evidence
   branch, `loadImportedStartSource`. It is taken when the source checkpoint effect is an applied
   `import-checkpoint`. It uses the same payload shape, with root, project, inode and quota read
   from the import receipt.
4. **The checkpoint manifest has no per-file digests.** `manifest_sha256` covers the whole
   archive. Resolution: the archive digest plus a per-entry SHA-256 read-back after extraction,
   following the existing restore pattern. The manifest's old source-root fields are compared
   with Portal's provenance values, never with the new host.
5. **`review_plans.action` shares the `operations.action` CHECK.** Resolution: migration 0021
   relaxes both. Doc 167's approved-changes table is amended accordingly.
6. **The old checkpoint root is not recorded anywhere Portal can read.** No stored receipt names
   the root the archive was written to, so the command cannot carry it honestly. Resolution:
   the import-checkpoint payload drops `checkpoint_filesystem_uuid` and `checkpoint_root_inode`.
   Portal's recovery set records `manifest_sha256`; once the runner verifies that the archive
   digest equals it, the manifest's own old checkpoint-root identity is authentic, so the runner
   reads those two values from the verified manifest (validating them and requiring them to
   differ from the source root) instead of from the payload. The `source_*` fields stay in the
   payload, sourced from the data-preparation receipt as before. A manifest that does not hash
   to the recorded digest is refused before any of its fields is read.

Per-installation resume: a managed `restore` operation whose source is the applied import. Its
revoke, quiesce, snapshot and restore-data stages advance on evidence only; `restore-start`, probe
and activate then run unchanged.

Migration id 0021 is reserved for the platform on `main`, in the same way as 0015–0020.

## Ordered steps (each test-first)

### Runner

1. **Journal v2.** `schemaProducts(version)` becomes versioned. The v1 kind list stays frozen.
   v2 adds `import-checkpoint`, `migrate-data`, `discard-migration` and `load-image`. `inspect`
   checks the exact SQL and the rows for both v1 and v2. A v1 file is upgraded in one
   transaction:
   - rename `runner_commands` to `_v1`;
   - create the v2 table with its exact SQL;
   - copy the rows explicitly and assert the counts match;
   - drop `_v1`, recreate the indexes, set `user_version=2`;
   - inspect again.

   Tests: `TestOpenUpgradesPopulatedV1Journal`, `TestUpgradedJournalAcceptsImportCheckpointKind`,
   `TestV2JournalRefusedByV1Inspect`, `TestTamperedV1SchemaNotUpgraded`.
2. **Contract** `control/import_checkpoint.go`, modelled on `control/restore.go`, with its own
   format and signature purpose. Tests `TestVerifyImportCheckpoint*` cover canonical JSON, unknown
   fields, stage and kind, the effect bound, expiry and a foreign deployment.
3. **Native primitive** `(*Root).ImportCheckpoint` (Linux, XFS), with a stub elsewhere. The
   verification order:
   - the identities of both roots;
   - archive opened with `O_NOFOLLOW`, regular file, 0400, size;
   - SHA-256 of the archive;
   - the canonical manifest against the payload and the source provenance values, its old
     checkpoint root being trusted only because the digest above matched;
   - the entry walk, with totals;
   - the quota bound, checked before any create;
   - prepare, extract, sync, then a per-entry read-back.

   Native tests: a tampered entry, a wrong checkpoint or namespace, and a quota smaller than
   needed must each refuse with the namespace absent; plus a success read-back.
4. **Admission, reservation and claim** (`journal/import_checkpoint.go`), following
   `DispatchPrepareData` and `beginRestore`:
   - verify the signature against `runner_deployment`;
   - refuse a duplicate grant, checkpoint or namespace, or an existing namespace resource;
   - reserve with a new project id;
   - bind the root marker as `unknown` before any native call.

   Tests: `TestImportCheckpointRefusesExistingNamespace`,
   `TestImportCheckpointRefusesSecondEffectSameGrant`, `TestImportClaimCommitsUnknownBeforeNative`.
5. **Root history.** `requireDataRootHistory` scans
   `kind IN ('prepare-data','import-checkpoint')`, decoding each kind with its own format. Tests:
   `TestRootHistoryScansImportCheckpoint`, `TestUpgradedV1JournalStillRefusesForeignRoot`.
6. **Settlement and replay.** `DispatchImportCheckpoint`, `ReadImportCheckpointReceipt` and the
   transport case. Tests: `TestImportReplayReturnsSameReceipt`,
   `TestImportUnknownReconcilesNotRetries`.
7. **Start admission.** `loadImportedStartSource`, plus the native request reading the imported
   source. Tests: `TestRestoreStartAdmitsImportedSource`,
   `TestRestoreStartImportedRefusesRootMismatch`,
   `TestRestoreStartImportedRefusesProvenanceMismatch`. Existing restore-start tests stay green.

### Portal

8. **Migration 0021.** Adds table `import_grants`:
   - columns: `id`, `recovery_set_id`, `runtime_id`, `controller_epoch`, `state`
     (`issued`/`consumed`/`revoked`), `consumed_ms`, `created_ms`, `updated_ms`;
   - foreign keys to `recovery_sets` and `runtime_bindings`;
   - a check that `consumed_ms` is set exactly when the state is `consumed`;
   - a unique index on `(recovery_set_id, runtime_id)`;
   - a unique partial index on `recovery_set_id WHERE state='issued'`;
   - an index on `(runtime_id, state)`.

   It also relaxes the `action` CHECK on `operations` and `review_plans` to admit
   `restore_import`:
   - on Postgres, the named constraint is dropped and re-added;
   - on SQLite, the stored table SQL is rewritten under `writable_schema` (a strict superset
     with no on-disk change), followed by `integrity_check`. Prove this works on modernc SQLite
     first; the fallback is a rebuild of the child tables.

   0015 keeps a frozen legacy action list, and its verifier accepts either form. Tests:
   `TestMigration0021RelaxesActionCheck` (SQLite and PG),
   `TestMigration0021VerifyOnUpgradedAndFresh`, `TestSchemaUpgradeCLIAcceptsPre0021Database`.
9. **Recovery mode.** Tests: `TestPausedPortalRefusesNonRecoveryMutation`,
   `TestPausedLeaseOnlyRestoreImport`, `TestEndRecoveryRequiresSettledInstallations`.
10. **Replacement runtime.** Tests: `TestReplacementRuntimeAfterRestore` (old row `enabled=0`,
    audit row, new `deployment_id`), `TestReplacementRefusedWhenOldBindingLive`,
    `TestDataRuntimeIgnoresRetiredBinding`.
11. **`restore_import`.** Consuming the review requires pause, an installation and recovery set
    in `review_required`, a verified manifest, and a single transaction. It repoints
    `installations.runtime_id`, issues the grant and creates the operation, which moves
    Queued → Validate → RestoreData → Complete. The grant is consumed by a conditional update
    (exactly one row) in the same transaction as the applied receipt; replay accepts only an
    identical receipt. Tests: `TestRestoreImportRepointsRuntimeAndIssuesGrant`,
    `TestConsumedGrantRefused`, `TestForeignRuntimeGrantRefused`,
    `TestGrantConsumedAtomicallyWithReceipt`.
12. **Restore revokes issued grants.** Test: `TestRestoreRevokesIssuedImportGrants`.
13. **Bundle.** The manifest lists the Portal dump and keyring, each retained checkpoint archive
    with its digest, and the old journal as provenance, copied and never opened. It also carries
    `platformRequiredReleaseBlobs`, derived from the last activation per installation plus the
    releases pinned by recovery sets. Static frontends already travel in the dump. Managed backend
    content is reported as pending until section 3's blob store exists. The cross-process recovery
    test is extended to cover this.

## Deferred and residual

- The managed half of the `DATA-05` acceptance (blobs present, one managed app resumed and running
  its task) needs section 3's `portalapp/2` blob store and `load-image`. `DATA-05` is closed only
  after both.
- Resuming notifications and tasks after recovery reuses the existing review flows. Ending
  recovery leaves them paused until each is reviewed.
- XFS quota block rounding must be included in the import's byte bound.
- One bundle restored into two Portals cannot be detected from inside either one, as stated in
  doc 167.
- `writePlatformRecoveryBundle` is not reachable from any command or route. The existing `backup`
  CLI takes one output file and the admin routes take no host paths, while the bundle needs a
  directory, the host path of every retained checkpoint archive and the runner journal path. It is
  exercised by tests only, and it reads the dump, retained sets and releases through separate
  reads, relying on its before/after retained-set comparison to refuse a moving database. Wiring
  it, and reading all three in one read transaction, is left until the operator command that
  supplies those paths exists.
- The runner's read-receipt answer is "receipt" or a closed connection. The Portal's import
  reconciliation therefore treats a failed read as unknown and re-sends the same effect, which the
  runner reconciles rather than repeats. A definite "no row" answer from the runner, which would
  let the Portal revert the effect and revoke the grant without a review, needs a distinct reply
  frame; the Portal side (`errPlatformImportAbsent`) is in place and the production transport does
  not yet produce it.

## Review follow-ups

- **Takeover.** A lease takeover raises the installation fence, so an import effect is signed again
  at the fence it was admitted under (the runner's claim binds it), never at the new one. A
  non-fresh effect is read first; applied settles it and consumes the grant, a definite no-row
  reverts the effect, revokes the grant and ends the operation as failed, and anything else sends
  the same effect again.
- **One import per epoch.** A review is refused when any grant of the installation's recovery sets
  was consumed, or any `restore_import` completed, in the current controller epoch.
- **Runner-free recovery actions.** `recovery_disable` settles any restored installation by
  disabling it; `frontend_resume` activates a restored static frontend release again. Both are
  reviewed on the plans route and committed on the operations route as one transition, recorded as
  an operation admitted already complete. A review refuses an installation with an unsettled
  import effect.
- **Pre-restore trust.** A restore records the publishers that were enabled before it in
  `app_platform_restore_trusted_publishers` (meta). Recovery verifies releases only against those
  publishers; a missing record trusts none. Ending recovery removes the record.
- **Unsettled start.** A resume ended with a reserved, unsettled start stays `recovery_required`
  and keeps its activation, namespace and recovery-set state.
- **Not done.** The golden pre-0021 SQLite fixture and second pooled connection test: the SQLite
  pool is a single connection, so there is no second connection to reparse the rewritten schema,
  and the rewound-store test already builds the legacy shape. The native import test on a second
  XFS root is compile-only on this host.

## Second-round review follow-ups

- **Unknown wedge.** While the Portal is paused and the scheduler is idle, a finished restore import
  whose effect is still unknown is read back from the runner under its own identity and fence: an
  applied receipt consumes the grant and completes the operation, a definite absence (after every
  command signed for the effect has expired) reverts the effect and revokes the grant, anything else is
  kept. A resume left with an unsettled start is reconciled the same way (third-round follow-ups below),
  or settled by the owner decision below.
- **Abandon replacement runtime.** A recovery-mode action on its own route (`control/abandon-runtime`,
  a prepare that lists the affected work with a digest and a confirm that names the digest) retires the
  enrolled replacement binding, revokes its issued grants, marks its unsettled import and resume effects
  reverted ("runtime abandoned", no native action), ends its in-flight operations and returns its
  installations to review. It is not a review plan: that table is installation-shaped (an application
  and a publisher are required), so the decision is a deployment-level confirmation instead. The
  confirmation is idempotent through a meta record of its outcome, and a changed listing is refused. A new replacement with
  a fresh journal and deployment is then enrolled and the imports redone. An import that landed on a
  retired runtime no longer counts against the one-import-per-epoch rule.
- **Claimed import.** A claimed native import runs detached from the connection to a definite end, bounded
  by five minutes plus two seconds per MiB of archive. The Portal polls by read-receipt.
- **Absence.** The runner takes the data execution gate before it reports an import absent. The Portal
  records the latest expiry of any command it signed for an effect in the effect's own receipt column
  (no schema change) and treats an absence as definite only after that expiry plus a margin.
- **Frontend resume.** A resumed static frontend carries its capability rows to the resumed policy
  revision, so it launches and can take a frontend update.
- **Managed resume.** A resumed managed app carries its capability rows to the resumed policy revision in
  the same way, is read back for launch and backend access through its restore-start and registration
  evidence (as a completed update is), and settles a failed update it resumed from. Ordinary
  (snapshot-based) restores are not read back for access by this path.
- **Recovery disable.** A managed installation disabled in recovery is recorded as managed. Once recovery
  has ended it is uninstalled by a removal that changes Portal rows only (no runner call, its data and
  recovery sets recorded as deleted).
- **Pre-restore trust.** The trusted-publisher record is built from the restored dump's own publisher
  rows; a dump taken during recovery keeps the record it carries.

## Third-round review follow-ups

- **Operations out of `recovery_required`.** The operation model has three more transitions, all from
  `recovery_required` only: `Settle` (to `failed`, once every effect is reverted or never applied),
  `Recover` (to `complete`, one revision after the failure) and `Reopen` (to the stage after the one that
  failed, at the revision `Advance` would have given it). A reverted import and an operation an abandon
  settled now end `failed`, so neither blocks launch for ever. The resolved-recovery predicate accepts a
  resume whose source generation is at or beyond the failed operation's, because an abandon advances the
  installation's generation; the review plan's checkpoint reference is what binds the resume.
- **Unknown resume start.** The runner answers a `restore-start` read-receipt (and only that) with a typed
  absence reply when its journal holds no command row for the effect, taken under the data execution
  gate. While paused and idle the scheduler re-reads the original start effect at its stored fence. An
  applied receipt settles the effect and reopens the operation at the probe stage on a lease at that
  fence, which the unchanged stage code runs to ready (only while the installation's fence is still that
  fence). A start the runner holds in state prepared or unknown stays unknown until the runtime
  is abandoned. (The fourth round changed what an absence reply means; see below.)
- **Abandon.** The idempotency record is keyed by runtime and controller epoch. An installation the
  administrator already disabled in recovery is left alone and listed apart. The data roots that imports
  on the runtime were applied to are recorded in meta and a replacement whose import lands on one is
  refused. A runtime abandoned before any import was applied has no root to record. A consumed grant on a
  retired runtime no longer blocks a new import review.

## Fourth-round review follow-ups

- **Resume after an abandon.** A recovery set can carry the consumed grant of a retired runtime beside the
  replacement's own. The resume loader selects its grant by the import receipt's grant id and the plan's
  runtime instead of assuming a single live grant, so resume review and execution, and the resumed
  backend's launch history, work after abandon and a second import.
- **Absence of a reserved start.** A start effect that carries a recorded reservation was acknowledged by
  the runner, so an absence reply for it means the runner's journal lost the row. The reconcile audits
  that once and reverts nothing: the effect stays unknown, the operation `recovery_required`, and only an
  abandon settles it. This replaces the earlier revert-on-absence rule. A start without a reservation is
  read too, because the runner may have accepted the reserve and the reply been lost: ending the resume
  keeps it `recovery_required`, and only the runner's typed absence, after every signed command has
  expired, reverts the effect, settles the operation to `failed` and returns the installation to review.
  Any other answer keeps it unknown until the runtime is abandoned.
- **Replacement data root.** Confirming a replacement observes the runner's profile and records the data
  root it reports in meta (`app_platform_replacement_root`, one record, no DDL). Abandoning the runtime
  carries that root into the abandoned roots beside the roots of applied imports, so an unknown or lost
  import still leaves its root recorded. A replacement whose reported root is an abandoned one is refused at
  confirm (the binding stays enabled so it can be abandoned in turn). Import review is refused for a runtime
  with no recorded root or an abandoned one; a confirm whose profile call failed is completed by repeating
  the confirm.
- **Recovery-only operation states.** The revision one past an ordinary operation's last stage (a failure
  settled out of `recovery_required`, a completion recovered out of it) is restored by the snapshot loader,
  and offered by `Settle`, `Recover` and `Reopen`, only for the restore import and restore actions whose
  recovery paths write it.
- **Read-receipt signature purpose.** Import and restore-start read-receipt commands are signed under their
  own purposes (`portal.app/runner-data-import-read/v1`, `portal.app/runner-data-update-start-read/v1`).
  The runner verifies the purpose per action, so a captured read cannot be submitted as an execute and an
  execute signature is not a read. The Portal signs both signatures with the command, so the read after a
  lost execute reply uses the read one. Ordinary update-start and reservation reads are unchanged.
