# Proposal: per-installation data quota and administrator data import

Status: Accepted 2026-11-20. Phases P1 (quota) and P3 (reviewed `set_data_quota` and `import_data`, `pack-import`) are implemented; see the implementation notes at the end. P2 (hard links) is pending. The owner accepted the recommended answer to every question in section 12 and approved migration `0024` (section 11). This document changes no code, schema or contract. It builds on:

- [155](155-managed-update-checkpoint-design.md): checkpoint and restore;
- [168](168-fresh-host-restore-plan.md): fresh-host import;
- [182](182-cross-platform-backend-runtime.md): XFS images and decision D10, a 3 GiB quota for market-monitor (superseded: 6 GiB, see section 3.3).

Related: [154](154-lifecycle-action-matrix.md), [169](169-data-migration-plan.md), [184](184-reviewed-forward-proxy.md).

Evidence is against `main` at `a96036ab`.

## 1. Problem and evidence

market-monitor will run on the same host as Portal. A new installation needs a one-time seed of about 750 MB: the bar store, Panorama base data and ETF history. Its jobs cannot rebuild that cheaply. It then grows by about 100–300 MB a year (182 §2). Today the platform cannot hold this data, whichever way it arrives.

1. **The quota is fixed at 256 MiB and 65,536 inodes for every installation.**
   - `internal/app/app_platform_install_profile.go:18-27` returns `256 << 20`. The payload in `app_platform_data_intent.go:121` carries `"268435456"` and `"65536"`.
   - The same constant is checked again in `app_platform_checkpoint_intent.go:233` (`quota == 256<<20`), `app_platform_managed_update_review.go:585` and `app_platform_update_review.go:120`.
   - D10's quota was decided but never implemented.
2. **The quota cannot be raised by hand.** Start requires the live XFS limits to equal the journal evidence (`driver/candidate_runtime_linux.go:~155`, `writable_candidate_linux.go:~25`). A changed limit refuses the start.
3. **A checkpoint is capped at 512 MiB** (`journal/checkpoint.go:21`). Install, update, managed enable and repair all take one first (154, 155), so a 750 MB tree fails at the next lifecycle action.
4. **The checkpoint walker refuses hard links** (`data/checkpoint_linux.go:335`, `Nlink > 1`). market-monitor hard-links changed partitions into `snapshots/` before each write (182 §2, from `pipeline/MarketData/README.md:131-142`).
5. **Admission reserves a flat 768 MiB per namespace** (`journal/reservations.go:40`: live data plus two retained checkpoints of 256 MiB). It ignores any larger quota, so it would under-count.
6. **There is no supported way to put data into an existing installation.**
   - The runner has no per-installation command (`cmd/app-runner/main.go:21`).
   - `ImportCheckpoint` (`data/import_linux.go`) runs only in the 168 fresh-host recovery flow.
   - Restore replaces a namespace from one of its own checkpoints.
7. **Locating a namespace already works.** It is `<data_root>/<namespace UUID>`, mounted as `/data` (`driver/container.go:429`). Administrators get the UUID as `data.id` from `GET /api/admin/app-platform/installations/{id}/settings`. Files created under it inherit its XFS project through `PROJINHERIT`, so a copy from another filesystem is counted against the right quota.

## 2. Proposal in one paragraph

**Quota.** Make the data quota a per-installation reviewed value, with an inode limit beside it. The descriptor requests it, and the administrator approves it at install or raises it later with a reviewed action. Raising is online; lowering is refused in version 1.

**Checkpoints.** Scale the checkpoint cap and the admission reservation with the quota. Teach checkpoints to preserve hard links that stay inside the namespace.

**Import.** Add one administrator action, `import_data`:
1. On the runner host, an operator packs a prepared tree with a new `app-runner pack-import` command. It validates the tree against the checkpoint rules and writes a canonical archive into the runner's import inbox.
2. The administrator reviews the archive (digest, size, entries) in Portal.
3. The operation stops the backend, takes a pre-import checkpoint, replaces the namespace contents with the archive, starts, probes and activates. A failure restores the pre-import checkpoint.

The 750 MB never crosses the web or the proxy, and every step keeps the existing checkpoint, fence, journal and audit guarantees.

## 3. Per-installation quota

### 3.1 Descriptor

The descriptor gains an optional `data.quota` block of `{bytes, inodes}`. It holds whole numbers within platform bounds:

| Field | Range | Default |
|---|---|---|
| `bytes` | 256 MiB to 16 GiB, a multiple of 64 MiB | 256 MiB |
| `inodes` | 65,536 to 1,000,000 | 65,536 |

An app without the block keeps today's 256 MiB and 65,536, so the change is additive. The contract regenerates through `scripts/generate_app_contract.py`.

### 3.2 Review

- **Install.** The install review shows the requested quota, and the administrator may approve it or approve a smaller one. The approved value is part of the canonical review bytes, as the profile quota already is (`app_platform_install_profile.go:10`).
- **Raising later.** A reviewed action, `set_data_quota`, raises an existing installation's quota up to the platform maximum.
  1. The plan shows the current and new values, the current usage and the runner's free capacity.
  2. Consume asks the runner to apply the larger limit with the quota ioctl, while the backend may stay running. The runner first checks that its capacity admission can hold the new reservation (section 3.3).
  3. The journal evidence and `app_platform_data_namespaces` are updated in one fenced step.
- **Lowering.** Refused in version 1 (Q2).
- **Updates.** An update whose descriptor asks for more than the approved quota shows the difference in the update review. Approving it raises the quota as part of the update. An update asking for less leaves the approved quota as it is.

### 3.3 Runner admission

- **The namespace reservation becomes per namespace:**
  - the quota on the data volume;
  - plus up to two retained checkpoints of at most the quota each on the checkpoint volume. When the checkpoint root shares the data filesystem, both count against the data volume.
- **Admission.** It refuses a quota, or a raise, that would take the sum of reservations above `capacity.disk_bytes` (data) or the checkpoint share (`capacity.go:136,180`). The refusal names the shortfall, so the operator knows to grow the images (handbook §4.2).
- **Sizing for market-monitor.** Its quota is **6 GiB** (6,442,450,944 bytes) and 100,000 inodes, approved after the storage redesign in Apps `apps/market-monitor/docs/store-v2.md` (about 2.5 GB after the first year, then about +0.45 GB a year). This supersedes doc 182 D10's 3 GiB. Each checkpoint archive is capped at about 6.2 GiB (6 GiB + 100,000 x 1.5 KiB + 64 MiB), so two retained checkpoints need about 12.4 GiB on the checkpoint volume. D11's 8 GiB checkpoint image (about 7 GiB usable) cannot hold that. Production sizing:
  - checkpoint image of at least 16 GiB, grown online per handbook section 4.2;
  - data image of 8 GiB (about 7 GiB usable), which holds 6 GiB plus the small apps for now;
  - grow the data image when its capacity reaches 80%.

  XFS cannot shrink (Q8), so the images are not over-provisioned beyond that.

### 3.4 Start evidence

The start check (`candidate_runtime_linux.go`) keeps requiring the live limits to equal the journal evidence. `set_data_quota` updates the evidence in the same fenced step as the ioctl, so the equality still holds and a hand-edited limit is still refused.

## 4. Checkpoints at the new sizes

- **Archive cap.** It becomes the namespace quota plus a fixed 64 MiB for tar overhead, instead of 512 MiB. The entry cap (100,000) becomes the inode quota.
- **Hard links (Q3).** The walker accepts a regular file with `Nlink > 1` only when every link to that inode is inside the namespace. It counts the links it finds during the walk and compares the total with `Nlink` at the end.
  - The first path seen is written as a regular file. Later paths are written as tar `TypeLink` entries pointing to it.
  - Restore and import recreate the links with `linkat` inside the target namespace, and verify the link counts on read-back.
  - A file whose `Nlink` exceeds the links found inside the namespace refuses the checkpoint, exactly as today. A namespace cannot hold such a file without a link from elsewhere on the volume, which never happens in practice.
- **Duration.** Checkpointing 6 GiB is a few tens of seconds on the NVMe hosts. It already happens only at lifecycle actions (install, update, enable, repair), never on a schedule, so nothing in normal running gets slower.
- **Retention.** Two retained checkpoints, as today, now sized by the quota. Q4 asks whether large namespaces should keep only one.

## 5. Administrator import

### 5.1 Packing on the runner host

`app-runner pack-import --from <directory> --out <inbox>/<name>.tar` runs as root on the runner host.
- **Validation.** It walks the tree with the same rules as the checkpoint walker:
  - regular files and directories only, with hard links inside the tree allowed;
  - no symlinks, no special files, no setuid or sticky bits;
  - names valid UTF-8, at most 255 bytes, depth at most 64;
  - no reserved `.portal-checkpoint-manifest.json`.
- **Ownership is normalised.** Every entry is written as `65532:65532`, directories `0750` and files `0640`, because the source tree's owners mean nothing to the namespace.
- **Output.** A canonical tar whose first entry is an import manifest: format `portal.app/data-import/1`, entry count, total bytes, a SHA-256 of the archive body, and the packer version.
- **Inbox.** It writes into the import inbox, `<checkpoint_root>/.portal-imports/`, which is root-owned and mode 0700. It never overwrites an existing file. It prints the archive's SHA-256, size and entry count.

Packing a prepared tree on the host keeps the transfer off the web, the proxy and the browser.

### 5.2 Review

- **Plan.** `POST /api/admin/app-platform/plans` with action `import_data`, the installation, and the archive's SHA-256 as the operator copied it from `pack-import`.
- **Checks.** Before writing the plan, Portal asks the runner to confirm:
  - that exactly one inbox archive has that SHA-256;
  - its manifest's size and entry count;
  - that it fits the installation's quota.
- **What the review shows:**
  - the archive digest, its size against the quota, and its entry count against the inode quota;
  - that the current contents will be replaced;
  - that a pre-import checkpoint will be kept for rollback.
- **Version 1 replaces only.** Merging into existing data is not supported (Q5). For a new installation the namespace is empty, so replacing is what a seed needs.

### 5.3 Operation

`import_data` runs the managed lifecycle pattern of 154 and 155:

`validate -> quiesce -> snapshot -> import -> start -> probe -> activate -> complete`

- **validate:** the archive digest still matches, it still fits, and the installation is still in the reviewed state.
- **quiesce:** stops the backend and bumps the writer fence.
- **snapshot:** takes the pre-import checkpoint under the new caps (section 4) and records a recovery set.
- **import:** the runner clears the namespace's children and extracts the archive into the same namespace inode, as restore does (`restore_linux.go:31-35`). It reuses `restoreExtract` and `importFits`, verifies the read-back against the manifest, and writes a receipt with the bytes and inodes observed from the project quota. Portal records `observed_bytes` from the receipt.
- **start, probe, activate:** the same as managed repair, with the same release, grants and generation rules.
- **On failure:** after `import`, a failure restores the pre-import checkpoint and leaves the installation as it was, or in `recovery_required` if that restore also fails, as in 155.

After `complete`, the next lifecycle checkpoint includes the imported data, so a later restore keeps the seed. The inbox archive is deleted only when `complete` is reached; a failed import keeps it for another try.

### 5.4 Audit and console

- **Audit:** review plan creation, operation start, every stage, and the final receipt (digest, bytes, inodes).
- **Console:** the installation settings page shows the quota with usage, plus "Raise quota" and "Import data" actions. Each opens its review dialog, in en-US, zh-CN and zh-TW.
- **Handbook:** a section on the full operator procedure: find the namespace, prepare the tree, `pack-import`, review in Portal, verify.

## 6. Alternatives considered

- **Copy by hand into the namespace while it is disabled.** Rejected:
  - Nothing records what was copied.
  - Ownership and modes are easy to get wrong.
  - Portal's `observed_bytes` goes stale.
  - The next enable checkpoint would be the first to see the data.

  It also fails today on the quota and the checkpoint cap. The handbook will say not to do it.
- **A seed job through the reviewed proxy (184).** Possible once quotas exist, and suitable when the operator cannot place files on the host. But it pulls 750 MB through the 2 GiB daily budget and depends on an upstream that must be reviewed and reachable. Kept as the app-level fallback (Q6).
- **Importing a prepared checkpoint through the 168 recovery flow.** Rejected: that flow exists for fresh-host disaster recovery, needs a recovery set from the old host, and pauses the whole Portal.
- **Uploading the archive through the browser.** Rejected for version 1. 750 MB through the admin UI needs resumable upload, temporary storage and size limits that the inbox avoids. Q7 asks whether to add it later.
- **App stops using hard links instead of the checkpoint change.** That costs market-monitor about 90 MB of steady-state copies and more write churn, and every future app that uses hard links would hit the same refusal. The checkpoint change is bounded and keeps restore exact.

## 7. Interaction with existing guarantees

- **Fences and leases:** every runner step carries the operation fence, as in 155. A stale fence refuses.
- **Data-root identity:** unchanged. The namespace inode, project ID and directory profile (`root:65532 02770`) stay the same, because import and restore extract into the existing inode.
- **Migration jobs (169):** the import replaces `.portal-data-schema` with whatever the archive carries. A missing marker means data format 0. The review shows the archive's marker value next to the release's expected format and refuses a mismatch.
- **Restore:** a restore to a checkpoint older than the import removes the seed, as for any other data. The pre-import checkpoint is listed in the console like any checkpoint.

## 8. Failure codes

| Code | When |
|---|---|
| `data_import_archive_missing` | No inbox archive has the reviewed digest |
| `data_import_archive_invalid` | The manifest or tree fails validation |
| `data_import_too_large` | Bytes or entries exceed the quota |
| `data_quota_capacity` | The runner cannot reserve the quota or the raise |
| `data_quota_lower_refused` | A lower quota was requested (version 1) |
| `data_format_mismatch` | The archive's data schema marker differs from the release's |

All of them need en-US, zh-CN and zh-TW translations.

## 9. Test plan (these must fail first)

**Quota:**
- An install with `data.quota` sets the requested limits; without the block it keeps 256 MiB and 65,536.
- `set_data_quota` raises the limits online and updates the journal evidence; the next start passes.
- A hand-edited limit still refuses start.
- Admission refuses a raise beyond capacity, naming the shortfall.
- Lowering is refused.

**Checkpoints:**
- A 2 GiB namespace checkpoints and restores within the new cap.
- Hard links inside the namespace survive checkpoint and restore with equal link counts.
- A link count larger than the links found in the namespace refuses.
- The entry cap follows the inode quota.

**Import:**
- `pack-import` refuses symlinks, special files, setuid, the reserved name and an over-deep tree.
- It normalises owners and modes, and never overwrites.
- Review refuses an unknown digest and an archive larger than the quota.
- The operation replaces the contents, records `observed_bytes`, and the next checkpoint includes the data.
- A failure after `import` restores the pre-import checkpoint.
- A data-format mismatch is refused at review.

**Native:** one runner acceptance test on a provisioned XFS image that imports a 300 MB tree with hard links, then checkpoints and restores it. The real 750 MB seed is checked on the production host, not in CI.

## 10. Phasing

- **P1, quota:** the descriptor field, review, runner admission, `set_data_quota`, and the checkpoint caps scaled to the quota. On its own this lets market-monitor fetch its seed through the proxy.
- **P2, hard links in checkpoints.**
- **P3, import:** `pack-import`, the `import_data` action, console and handbook.

The three phases ship as separate PRs.

## 11. Schema change — approved by the owner on 2026-11-20 (migration 0024)

One additive migration after `0023`, numbered `0024`:

1. **`app_platform_data_namespaces` gains `quota_inodes`,** a non-null integer defaulting to 65,536. Existing rows get 65,536, which is today's fixed value. `quota_bytes` already exists (`app_platform_schema.go:39`); only its fixed value changes.
2. **The action CHECK constraint** on `app_platform_review_plans.action` and `app_platform_operations.action` (`platformActions()`) gains `set_data_quota` and `import_data`.
3. **The `app_platform_operations.stage` CHECK** gains `import`.

The runner journal is unchanged: its volume evidence already records the byte and inode limits per namespace. SQLite and PostgreSQL both get the migration, with up and down tests, as 0022 and 0023 did.

### Runner journal schema v5 — approved by the owner on 2026-11-20

The paragraph above says the runner journal is unchanged. That holds for the quota evidence the P1 work records, but not for the two new signed commands: each needs a durable `runner_commands` row, and that table's `kind` list is a closed CHECK. The owner approved **journal schema v5**: the `kind` CHECK gains `set-data-quota` and `import-data`, and `runner_commands` is rebuilt on upgrade the way v3 did it, from v1, v2, v3 and v4, keeping every row in admission order. No table or column is added, the stage list is unchanged (the commands use the existing stages `prepare_data` and `restore_data`), and the Portal schema stays at `0024`. An older binary refuses a v5 journal as a newer file.

## 12. Owner decisions (2026-11-20)

The owner accepted the recommended answer to each question below (Q1 to Q10) and approved migration `0024` (Q9).

Q1. **Per-installation quota bounds.** Decided: 256 MiB to 16 GiB in 64 MiB steps, and 65,536 to 1,000,000 inodes. The descriptor requests and the administrator approves; market-monitor requests 6 GiB and 100,000 inodes (supersedes D10's 3 GiB).

Q2. **Lowering a quota.** Decided: not in version 1. Lowering below usage would make the app fail its writes, and needs its own review of the usage. Only raising is supported.

Q3. **Hard links in checkpoints.** Decided: preserve links that stay inside the namespace (section 4), rather than asking apps to avoid them.

Q4. **Checkpoint retention for large namespaces.** Decided: keep two, as today. One would halve the checkpoint volume needed, but would lose the second rollback point.

Q5. **Import semantics.** Decided: replace only in version 1; the review states that current contents are replaced and a pre-import checkpoint is kept.

Q6. **Seed through the proxy as well.** Decided: no platform work. Once P1 lands, an app may still fetch its own seed through the reviewed proxy if its operator prefers; that is app code.

Q7. **Browser upload of import archives.** Decided: not in version 1; the runner-host inbox only.

Q8. **Production image sizes.** Decided (corrected 2026-11-20): a checkpoint image of at least 16 GiB, because two retained 6 GiB checkpoints need about 12.4 GiB, and a data image of 8 GiB, which holds market-monitor's 6 GiB plus small apps. Grow online at 80% (handbook section 4.2). Do not start larger, because XFS cannot shrink. D11's 8 GiB checkpoint image is too small for this quota.

Q9. **Schema change in section 11.** Decided: approved as migration `0024`.

Q10. **Order.** Decided: P1 first; it unblocks market-monitor's quota on its own. Then P3 import, which gives the fast host-side seed. P2 hard links is needed before P3 if market-monitor's seed contains its `snapshots/` links. Alternatively the seed can be packed without `snapshots/`, which the app rebuilds, so P3 can come before P2.

## Implementation notes (P1)

P1 is partly done. What landed, where it differs from sections 3, 4 and 11, and what is left.

**Landed**

- **Descriptor (3.1).** `data.quota {bytes, inodes}` in `descriptor-v2.schema.json`, validated by `appplatform.ValidDataQuota`. The Go and TypeScript products are regenerated.
- **Migration `0024` (11).** `quota_inodes`, the two actions and the `import` stage, for SQLite and PostgreSQL, with up, restore and rewind tests.
  - The control schema now has three frozen shapes, because 0015, 0021 and 0024 each widen the same CHECK lists. Each step's verifier accepts its own shape and the current one.
  - `import_data` and the `import` stage are only representable. Nothing accepts a plan with either action, and `set_data_quota` is not accepted yet either (see below).
- **Approved quota (3.2).**
  - An install plan carries the approved bytes and inodes and the requested pair, in the canonical bytes.
  - `POST /api/admin/app-platform/plans` for `install` takes the optional decimal strings `data_quota_bytes` and `data_quota_inodes`. Each must be within bounds and not above the request.
  - The prepare-data command, the namespace row and every check that compared with the fixed 256 MiB now use the approved values. A plan written before the fields reads as 256 MiB and 65,536.
  - Restore import (168) uses the namespace's own `quota_inodes`.
- **Update review.** A target descriptor that asks for more than the installation's current quota shows the current, requested and raise values. A request for the same or less shows nothing and keeps the approved quota.
- **Runner admission (3.3).**
  - A namespace reserves its quota for the data and two retained checkpoints at the full archive cap each, so a fully reserved volume can always hold both. The data share is one reservation row and the checkpoints are a second row (`checkpoints/<namespace>`), so the two volumes can be counted apart. Rows written before this change (one row of 768 MiB, no checkpoint row) are read, never rewritten, as the 256 MiB / 65,536 namespace they were: 256 MiB of data and two checkpoints at the 512 MiB floor of the cap. Once the namespace is admitted again it holds its own two rows and is counted once.
  - With a separate checkpoint volume, `DeriveBudgets` sets `CheckpointBytes` from that volume. The data volume then counts the quota and the checkpoint volume the two caps. Otherwise all of it counts against `disk_bytes`.
  - The runner holds every prepare-data and import command to the platform bounds (256 MiB to 16 GiB in 64 MiB steps, 65,536 to 1,000,000 inodes), whatever the controller signed.
  - A refusal is the typed dimension `data_quota` or `data_quota_checkpoint`, shown as the error code `data_quota_capacity`, with the requested and remaining bytes in the receipt. The error code is translated in en-US, zh-CN and zh-TW.
- **Checkpoint caps (4).**
  - A checkpoint's archive cap is `max(512 MiB, quota + inode quota x 1.5 KiB + 64 MiB)`, and its entry cap is its inode quota. An import uses the quota it carries. The restore re-verify and the delete walk use the same entry cap (the delete walk is bounded by the namespace's inode quota).
  - **Correction to section 4.** "Quota plus 64 MiB" ignores tar's per-entry overhead, which is not bounded by bytes of data: each entry costs a 512-byte header, plus a PAX header and padding for a long or non-ASCII name, 1.5 KiB in all. 700,000 empty files under a 256 MiB quota already exceed quota plus 64 MiB. The 512 MiB floor keeps every checkpoint that was valid under the old fixed cap valid, so restore and import of old checkpoints still work. The arithmetic refuses quotas outside the platform bounds, so it cannot overflow.
  - **1.5 KiB per entry is a sizing rule, not a bound.** It is realistic for reservations, but a path of about 4,000 bytes needs roughly 5.5 KiB of PAX header. A tree of pathologically deep paths can therefore exceed the cap. Its lifecycle checkpoint is then refused whole (`ErrCheckpointTooLarge`, "raise the quota"), the partial archive is removed, and nothing is lost: the installation fails closed at that lifecycle step. Raising the quota raises the cap.
  - The ceiling in command validation is the cap of the largest namespace: 16 GiB, 1,000,000 entries of 1.5 KiB and 64 MiB.
- **Console.** The settings page shows the inode quota and the usage as a percentage of the quota.

**Deviations**

- **Update raise not applied.** The update review shows the raise, but the update keeps the approved quota (`raise_applied` is false). Applying it in the update would change the update-start evidence of every update path; it belongs with `set_data_quota`.
- **Restore walk bound.** A restore's entry walk (re-verify and clear) is bounded by the namespace's own inode quota, read from its prepare-data receipt in the runner journal. The platform maximum (1,000,000) stands in only when the journal does not know the quota. Its archive bound is the checkpoint's own verified size, and the XFS project quota is the binding limit either way. The walk holds its entries in memory, at most the inode quota of them.
- **Hard links** are still refused by the walker (P2).
- **Migration shape.** `quota_inodes` carries a CHECK of 65,536 to 1,000,000 (a table constraint on PostgreSQL, part of the column on SQLite, which cannot add a table constraint to an existing table) and the PostgreSQL verifier checks its default is 65,536. A database at 0023 verifies under the 0015 and 0021 verifiers through the 0021-era shape.
- **Plans written before the upgrade** carry no inode or request fields. They read as 256 MiB and 65,536, still stage and consume, and the rebuilt plan is compared in that form. The install, activation, writer, candidate and checkpoint checks now bind the namespace to the quota the install review approved, bytes and inodes.
- A frontend-only descriptor with `data.quota` is refused at parse.
- **Frontend object storage** keeps its fixed 256 MiB (`platformObjectNamespaceBytes`). It is not an XFS quota.

**Not done**

- **`set_data_quota`.** There is no reviewed plan, no operation, no runner command or effect, and no `data_quota_lower_refused` check yet. The action is in the schema only.
  - Doing it properly means a new lifecycle action in the generation history chain: every history loader (install, update, resume, restore) must read the quota from the namespace row rather than the install-time receipt.
  - The runner also needs a signed command with its own receipt and a journal evidence update fenced together with the namespace row.
- **Console "Raise quota"** and its review dialog, which need `set_data_quota`. The console install review does not yet offer a smaller approval; the API does.
- **Hard links** (P2), and **import** (P3), as the phasing says.

## Implementation notes (set_data_quota, P3)

P3 and the reviewed `set_data_quota` action are implemented, apart from the item listed under "Not done".

**Runner**

- **Journal schema v5** (section 11): the two kinds, the upgrade from v1 to v4, and tests for every starting version.
- **Commands.** `set-data-quota` (stage `prepare_data`) and `import-data` (stage `restore_data`) are signed like the other data commands, with their own execute, read and settlement purposes. The runner claims each in one transaction before the native call (row `unknown`), and settles it to `applied` with its receipt afterwards; an effect left unknown is finished under the same claim, never claimed again. A read-back answers with the receipt or a typed status (`pending`, `orphaned`, `absent`, `failed`).
  - **Known failure.** `unknown` is only the crash window (the process died between the claim and the native result). When the native call returns an error the outcome is known, so the row is settled `reverted` (no schema change; the journal's reverted state) with a failure record: a closed code (`import_archive_missing`, `import_archive_ambiguous`, `import_archive_invalid`, `import_too_large`, `import_format_mismatch`, `import_unsafe`, `import_incomplete`, `import_failed`, `quota_raise_unsafe`, `quota_raise_lower`), the namespace, and whether the namespace may have been changed (`dirty`). A reverted row blocks no later command and no checkpoint or restore of the installation, which is what lets the rollback restore after a failed import apply. The same signed command answers the stored failure and never runs the native call again; execute and read-back reply it as the typed status `failed` with the code. An import that failed before it cleared anything is clean; an incomplete import, and any error the runner does not recognise, is dirty. A raise is recorded failed only when the native layer refused it with nothing changed (`ErrQuotaRaiseUnsafe`, `ErrQuotaLower`); a raise that may have applied (`ErrQuotaRaiseIncomplete`) or failed otherwise stays `unknown`, and sending the same command converges it.
  - **Dirty namespace.** While a dirty failed import is the namespace's latest, no `update-start`, `enable-start` or `restore-start` is reserved or claimed for it. Only a restore of that namespace applied later (in admission order) lifts it, so a half-imported namespace is never started by accident.
  - Raise: checks the volume resource, the install's prepare-data receipt, that the old limits equal the journal's current quota, and capacity admission (raising both reservation rows, the checkpoint row included). A refusal is the typed `data_quota` dimension and the shortfall. The native raise (`Root.RaiseQuota`) is online, refuses lowering and refuses a hand-edited live limit.
  - Import: requires no running container, the operation's own applied pre-import checkpoint (verified and kept), and the namespace's current quota. The native import replaces the contents in the same namespace inode (`restoreExtract`, `importFits`, restore read-back) and returns the bytes and inodes the project quota reports.
- **Quota chain.** The install's prepare-data receipt stays immutable, because later checkpoints rebuild its digest. The current quota is its limits followed by every applied `set-data-quota` row of the namespace (only that namespace's rows are read, and the 4,096-link bound is per namespace), each starting where the last ended. Checkpoint sources (archive and entry caps), the update-start evidence, restore bounds, delete-data and managed removal read the last link; the stop and remove-code checks accept any link for the quota a start recorded at its own time.
- **Data query** (`data-query`, no journal row): the questions a review asks, signed by Portal. Does exactly one inbox archive carry a digest (and its size, entries, content bytes and data schema marker), would a larger quota fit the runner's capacity (a rolled-back dry run of admission), and the deletion of an inbox archive after a completed import.
- **`pack-import` and the natives** are as in the first pass of these notes: validation with the checkpoint walker's rules (hard links refused until P2), normalised owners and modes, a canonical archive in `<checkpoint_root>/.portal-imports/` that is never overwritten. The printed digest is that of everything after the manifest block.

**Portal**

- **`set_data_quota`.** Stages `queued -> validate -> prepare_data -> complete`; no generation changes and the installation keeps running. The review shows current and new bytes and inodes, usage and the runner's capacity answer; lowering is `data_quota_lower_refused`, an unchanged or out-of-bounds request `data_quota_invalid`, and a raise the runner cannot hold is reviewed with its shortfall named and cannot be confirmed (`data_quota_capacity`). The commit that records the receipt updates the namespace row (`quota_bytes`, `quota_inodes`, `observed_bytes`, revision) and completes the operation in one fenced transaction.
- **History loaders.** The namespace row is compared with the last link of the Portal's chain (the install review's approval, then each completed raise), not with the install plan: the approved-quota check every source loader shares, the install-time backend history (row revision and update time allow the raises), the update history (a start's recorded quota may be any link; its rebuilt command uses the recorded one), and the live sources that feed new commands (current quota). Fresh-host import reads the namespace row, which is current. Tests cover the install-time history, the update history, a start after a raise (which carries the raised quota) and the failure and refusal paths, on SQLite and PostgreSQL.
- **`import_data`.** A same-release lifecycle with one more stage: `validate -> quiesce -> snapshot -> import -> start -> probe -> activate -> complete`. The review requires the runner's confirmation of exactly one archive with the reviewed digest, a data schema marker equal to the release's data format (`data_format_mismatch`), and a size and entry count inside the quota (`data_import_too_large`); errors are `data_import_archive_missing` and `data_import_archive_invalid`. `validate` asks the runner again. The `import` effect records `observed_bytes` from the receipt. On `complete` the scheduler asks the runner to delete the archive, and only then: a failed or unfinished import keeps it. Removal is best effort. The import is complete either way, so a removal that fails is logged and written as `app_platform.data_import_archive_remove_failed`, the archive stays for the operator, and the completed import is neither failed nor rolled back; a removal that works writes `app_platform.data_import_archive_removed`.
- **Failure after the import** restores the pre-import checkpoint (a runner restore under the second effect number of the stage) and leaves the installation disabled and unchanged (`managed_import_data_rolled_back`). If that restore cannot be proven the operation ends `recovery_required` and the existing repair path applies. A failure before the import changes nothing (`managed_import_data_unavailable`). When the runner answers the import (or the raise) with the typed `failed` status, Portal takes this failure path at once instead of spending its retries: an import restores its checkpoint, a raise fails plainly with the installation and quota untouched. The failed import's row on the runner is `reverted`, so that restore is not refused for unsettled work.
- **Audit.** Review, intent, every stage, the import (digest, entries, observed bytes), the rollback and the archive deletion each write an `audit_log` row; the same for the raise.
- **Console.** The settings page offers "Raise quota" and "Import data" with review dialogs in en-US, zh-CN and zh-TW. The import commit asks once more through `confirmAction`. The settings projection reports `quota_raise_eligible` and `import_eligible`.
- **Handbook** section "Importing data" now covers the whole procedure, including raising the quota first.

**Deviations**

- **Import requires a disabled installation.** The proposal's quiesce stops the backend; here the installation is disabled first, and `quiesce` is the verified stopped state. The review and the console say so, and an enabled installation is refused with `data_import_requires_disabled`.
- **Review capacity is the runner's admission answer** (fits, or the dimension and shortfall), not a free-bytes figure.
- **The review checks size with the manifest's numbers** (content bytes against the byte quota, entries plus the namespace root against the inode quota); the exact block-rounded footprint is checked by the runner at the import.
- **Journal kinds and stages.** The two commands share the stages `prepare_data` and `restore_data` with other effects (no stage was added); the Portal records the import under its own effect stage `import`.
- **An unknown raise** (one that may have applied; a raise the native layer refused is failed, not unknown) ends `recovery_required` after its attempts. The runner finishes an effect left unknown when the same signed command arrives, but Portal does not yet reconcile one automatically; a limit above the journal evidence refuses a start until the raise is reviewed again.

**Tests**

- Run: journal v1 to v4 upgrades, the two commands (claim, settle, replay, unknown, capacity, refusals), the quota chain, the data query, transport actions and read purposes, control commands, the data natives that need no XFS, `pack-import`, the operation models, the Portal flows on SQLite and PostgreSQL (`postgres:16-alpine`, removed afterwards), the web tests and the typecheck.
- Written, **not run**: `TestNativeRaiseQuotaAndImportData`. It needs the disposable XFS fixture.

**Not done**

- **Update raises quota.** An approved update that asks for more still keeps the approved quota (`raise_applied` stays false); raising is the separate reviewed action. Folding it into the update changes the update's start evidence on every update path. The design that fits: run the same `set-data-quota` effect between the checkpoint and the start, derive the Portal's quota chain from applied `set-data-quota` effect receipts (which also covers an update's own raise, in flight), and count the extra effect in the registration's base. It needs its own review.
- **Hard links** (P2).
