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
