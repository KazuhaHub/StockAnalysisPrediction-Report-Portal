# Proposal: per-installation data quota and administrator data import

Status: Accepted 2026-11-20; implementation pending. The owner accepted the recommended answer to every question in section 12 and approved migration `0024` (section 11). This document changes no code, schema or contract. It builds on:

- [155](155-managed-update-checkpoint-design.md): checkpoint and restore;
- [168](168-fresh-host-restore-plan.md): fresh-host import;
- [182](182-cross-platform-backend-runtime.md): XFS images and decision D10, a 3 GiB quota for market-monitor.

Related: [154](154-lifecycle-action-matrix.md), [169](169-data-migration-plan.md), [184](184-reviewed-forward-proxy.md).

Evidence is against `main` at `a96036ab`.

## 1. Problem and evidence

market-monitor will run on the same host as Portal. A new installation needs a one-time seed of about 750 MB: the bar store, Panorama base data and ETF history. Its jobs cannot rebuild that cheaply. It then grows by about 100–300 MB a year (182 §2). Today the platform cannot hold this data, whichever way it arrives.

1. **The quota is fixed at 256 MiB and 65,536 inodes for every installation.**
   - `internal/app/app_platform_install_profile.go:18-27` returns `256 << 20`. The payload in `app_platform_data_intent.go:121` carries `"268435456"` and `"65536"`.
   - The same constant is checked again in `app_platform_checkpoint_intent.go:233` (`quota == 256<<20`), `app_platform_managed_update_review.go:585` and `app_platform_update_review.go:120`.
   - D10's 3 GiB was decided but never implemented.
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
- **Sizing for market-monitor.** The production images in D11 (8 GiB data, 8 GiB checkpoint, about 7 GiB usable) hold:
  - 3 GiB live data;
  - 2 × 3 GiB checkpoints on the checkpoint volume;
  - plus the small apps.

  The images grow online when capacity reaches 80% (handbook §4.2). We do not over-provision, because XFS cannot shrink (Q8).

### 3.4 Start evidence

The start check (`candidate_runtime_linux.go`) keeps requiring the live limits to equal the journal evidence. `set_data_quota` updates the evidence in the same fenced step as the ioctl, so the equality still holds and a hand-edited limit is still refused.

## 4. Checkpoints at the new sizes

- **Archive cap.** It becomes the namespace quota plus a fixed 64 MiB for tar overhead, instead of 512 MiB. The entry cap (100,000) becomes the inode quota.
- **Hard links (Q3).** The walker accepts a regular file with `Nlink > 1` only when every link to that inode is inside the namespace. It counts the links it finds during the walk and compares the total with `Nlink` at the end.
  - The first path seen is written as a regular file. Later paths are written as tar `TypeLink` entries pointing to it.
  - Restore and import recreate the links with `linkat` inside the target namespace, and verify the link counts on read-back.
  - A file whose `Nlink` exceeds the links found inside the namespace refuses the checkpoint, exactly as today. A namespace cannot hold such a file without a link from elsewhere on the volume, which never happens in practice.
- **Duration.** Checkpointing 3 GiB is a few tens of seconds on the NVMe hosts. It already happens only at lifecycle actions (install, update, enable, repair), never on a schedule, so nothing in normal running gets slower.
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

## 12. Owner decisions (2026-11-20)

The owner accepted the recommended answer to each question below (Q1 to Q10) and approved migration `0024` (Q9).

Q1. **Per-installation quota bounds.** Decided: 256 MiB to 16 GiB in 64 MiB steps, and 65,536 to 1,000,000 inodes. The descriptor requests and the administrator approves; market-monitor requests 3 GiB and 100,000 inodes (D10).

Q2. **Lowering a quota.** Decided: not in version 1. Lowering below usage would make the app fail its writes, and needs its own review of the usage. Only raising is supported.

Q3. **Hard links in checkpoints.** Decided: preserve links that stay inside the namespace (section 4), rather than asking apps to avoid them.

Q4. **Checkpoint retention for large namespaces.** Decided: keep two, as today. One would halve the checkpoint volume needed, but would lose the second rollback point.

Q5. **Import semantics.** Decided: replace only in version 1; the review states that current contents are replaced and a pre-import checkpoint is kept.

Q6. **Seed through the proxy as well.** Decided: no platform work. Once P1 lands, an app may still fetch its own seed through the reviewed proxy if its operator prefers; that is app code.

Q7. **Browser upload of import archives.** Decided: not in version 1; the runner-host inbox only.

Q8. **Production image sizes.** Decided: keep D11's 8 GiB data and 8 GiB checkpoint, and grow online at 80% (handbook §4.2). They hold market-monitor's 3 GiB with two checkpoints. Do not start at 20 GiB, because XFS cannot shrink.

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
  - A namespace reserves three quotas: the data and two retained checkpoints. The reservation row still stores that total, so rows written before this change read as 256 MiB.
  - With a separate checkpoint volume, `DeriveBudgets` sets `CheckpointBytes` from that volume. The data volume then counts one quota per namespace and the checkpoint volume two. Otherwise all three count against `disk_bytes`.
  - A refusal is the typed dimension `data_quota` or `data_quota_checkpoint`, shown as the error code `data_quota_capacity`, with the requested and remaining bytes in the receipt. The error code is translated in en-US, zh-CN and zh-TW.
- **Checkpoint caps (4).**
  - A checkpoint's archive cap is its quota plus 64 MiB, and its entry cap is its inode quota. An import uses the quota it carries.
  - The fixed 512 MiB in command validation is now the platform ceiling of 16 GiB plus 64 MiB.
- **Console.** The settings page shows the inode quota and the usage as a percentage of the quota.

**Deviations**

- **Update raise not applied.** The update review shows the raise, but the update keeps the approved quota (`raise_applied` is false). Applying it in the update would change the update-start evidence of every update path; it belongs with `set_data_quota`.
- **Restore walk bound.** Restore has no live quota in the runner journal, so its entry walk is bounded by the platform maximum (1,000,000) and its archive bound is the checkpoint's own verified size. The XFS project quota is the binding limit of a restore.
- **Frontend object storage** keeps its fixed 256 MiB (`platformObjectNamespaceBytes`). It is not an XFS quota.

**Not done**

- **`set_data_quota`.** There is no reviewed plan, no operation, no runner command or effect, and no `data_quota_lower_refused` check yet. The action is in the schema only.
  - Doing it properly means a new lifecycle action in the generation history chain: every history loader (install, update, resume, restore) must read the quota from the namespace row rather than the install-time receipt.
  - The runner also needs a signed command with its own receipt and a journal evidence update fenced together with the namespace row.
- **Console "Raise quota"** and its review dialog, which need `set_data_quota`. The console install review does not yet offer a smaller approval; the API does.
- **Hard links** (P2), and **import** (P3), as the phasing says.
