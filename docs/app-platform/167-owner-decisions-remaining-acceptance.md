# Owner decisions for the remaining acceptance gaps

Status: accepted 2026-11-05. The owner delegated these decisions in full. Each
section records the decision, the reasoning, the rejected alternatives and the
acceptance condition that turns it into evidence. It supersedes the "needs owner
decision" notes for the rows below. It does not authorize production
deployment or a stable SDK freeze; those are the release gates in section 9.

Rows covered: `DATA-05`, `DATA-08`, `LIFE-08`, `PKG-01`, `PKG-08`, `SEC-01`,
`SEC-09`, `OPS-05`, `OPS-06`, `OPS-08`, `UX-08`, `LIFE-02`, `RUN-01`, `DEV-01`,
`DEV-04`, plus how the integration candidate reaches `main`.

Guiding rules: keep every existing fail-closed invariant (one journal binds one
data root; an `unknown` effect is settled, never retried under a new identity;
restore never swaps namespaces; executables are trusted only by verified
content digest); prefer the design that removes a class of failure over one
that adds a recovery path for it; reuse existing records before adding new ones.

## Schema and contract changes approved by this document

Approved under the delegation, to be implemented test-first as forward
migrations and contract revisions. Nothing else in this document changes a
schema.

| Store | Change | Section |
| --- | --- | --- |
| Runner journal | Schema v2: `kind` gains `import-checkpoint`, `migrate-data`, `discard-migration`, `load-image`; explicit v1→v2 upgrade with tests on a populated v1 journal | 1, 2, 3 |
| Portal platform schema | `operations.action` and `review_plans.action` gain `restore_import`; one table `app_platform_import_grants` (id, recovery_set_id, runtime_id, controller_epoch, state, consumed_ms, timestamps) | 1 |
| Portal task retention | One table `app_platform_task_occurrence_tombstones` (installation_id, occurrence_id, payload_sha256, created_ms) kept for the operations retention period | 5 |
| Descriptor contract | Revision: per-platform `manifest_digest` and `config_digest`, and per layer `diff_id` and `uncompressed_bytes`, for the backend image; `data.migrations` (single-hop `{from, to}`, migration entrypoint, wall-clock limit) | 2, 3 |
| Offline envelope | `portalapp/2` with image manifest, config and layer blobs | 3 |
| Portal↔runner transport | A separate bounded blob stream channel: chunked, SHA-256 per chunk and for the whole blob, resumable by offset, its own per-chunk deadline and total byte bound; the existing 3-second, 1 MiB control session is unchanged | 3 |
| Migration ledger | IDs 0015–0020 reserved for the platform on `main`, with a guard test | 9 |

The old runtime binding is retired with the existing `enabled=0` plus an audit
record; no new column.

## 1. DATA-05: restore onto a fresh host

**Decision: a fresh host is a new runner deployment. The restored Portal
re-homes installations to it through one reviewed operation, and each
checkpoint is imported by content under a single-use grant. The physical
identity binding is never relaxed or rebound, and the fresh host is never
required to reproduce the old filesystem identity.**

Mechanics:

1. The fresh host's runner starts with a new, empty journal and enrolls its own
   data root through the existing initial-enrollment path. The old host's
   journal travels in the bundle as read-only provenance and is never opened as
   an active journal.
2. On the restored, still-paused Portal, an administrator first enrolls the
   replacement runtime once, at deployment level, through the existing runtime
   enrollment path (a new `runtime_bindings` row with its own
   `deployment_id`); the old binding is set `enabled=0` with an audit record.
   Then, per installation, a reviewed `restore_import` operation (its own review
   plan) points `installations.runtime_id` at the new runtime and creates one
   `app_platform_import_grants` row for the chosen recovery set, bound to the
   new runtime and current controller epoch. One operation imports one
   checkpoint, well inside the 32-effect limit. Namespaces and recovery sets are
   not rebound; they follow their installation.
3. The runner command `import-checkpoint` carries the grant. It creates the
   namespace on the new root and imports the checkpoint tree after verifying
   the archive size and SHA-256, every file digest, and that
   `manifest_sha256`, `checkpoint_id` and `data_namespace_id` equal the
   recovery set's recorded values (columns that already exist). Quota and inode
   bounds are checked before any byte is written. Portal marks the grant
   consumed in the same transaction that records the applied receipt; a
   consumed or foreign-runtime grant is refused.
4. `requireDataRootHistory` scans `import-checkpoint` alongside `prepare-data`,
   so an imported namespace is held to the same one-root rule.
5. An applied `import-checkpoint` receipt is admitted as a start data source,
   exactly as `restore-start` is admitted today. Resuming an installation is
   then a reviewed managed start; until an administrator resumes it, the
   installation stays `review_required` with restore pause, rotated controller
   epoch, cleared execution authority and paused notification dispatch (the
   state proven in doc 166).
6. The bundle includes the content-addressed blobs of every active release and
   every release pinned by a retained recovery set (section 3), so a managed
   app restarts on the new host without any catalogue.

Same-host restore (data root intact) keeps the current path unchanged.

Residual risk, stated rather than solved: a bundle is a complete copy of the
keyring and data. Restoring the same bundle into two separate Portals creates
two independent deployments; nothing inside one Portal can detect the other.
Bundles are protected like the keyring, and the handbook says so.

Rejected:

- *Require the original filesystem identity.* After a lost disk the only way to
  comply is cloning the UUID, which doc 49 notes the check cannot detect.
- *Rebind the old journal to the new root.* It weakens "one journal, one root"
  for every deployment to serve a rare path.

Acceptance (`DATA-05`): on a newly provisioned disposable Linux host with a
fresh XFS project-quota volume and no catalogue access, restore the bundle,
run `restore_import`, import, decrypt the fixture secret, verify data digests,
`/healthz` 200, confirm every restored installation's release blobs are
present and every external effect stays paused, then resume one managed app
and run its fixed task. Refusals with no namespace created:
tampered file digest; manifest or checkpoint ID disagreeing with the recovery
set; a consumed grant presented again; a grant presented by a different runner
deployment; quota smaller than the checkpoint; a populated v1 journal upgraded
to v2 still refusing a foreign root.

## 2. LIFE-08: application data migration

**Decision: migrate in place in the existing namespace while the source
release is stopped, with the already-verified pre-update checkpoint as the
untouched source of truth. Rollback is the existing verified restore. An
`unknown` migration is settled before anything else happens.**

This keeps doc 155's rules (same namespace across update and restore, readiness
bound to the pinned mount profile) and still makes recovery independent of the
app writing a correct migration, because the checkpoint — not the namespace —
is what recovery returns to.

Protocol:

- Descriptor: `data.migrations` lists single-hop steps `{from, to}`; `to`
  equals `data.schema`, `from` equals the installed schema. Multi-hop chains are
  a later extension. Updates with no matching step keep today's refusal.
- Order inside the existing managed update: quiesce, verified checkpoint,
  source stopped, then a `migrate-data` effect on the `migrate` stage, then
  target start and readiness.
- Job: the runner starts the target image with the declared migration
  entrypoint against the namespace, `network=none`, no user traffic, no broker,
  credential or task access, the backend's memory/CPU/PID bounds and a hard
  wall-clock limit (default 10 minutes; the descriptor may lower it). The
  command binds operation, source schema, target schema, checkpoint receipt and
  a writer fence strictly above the source's and strictly below the one
  `update-start` will use.
- Commit point: the job writes the `.portal-data-schema` marker containing `to`
  as its last action and exits 0. The runner records `applied` only on exit 0,
  matching marker and namespace within quota; the target must then pass
  readiness before activation.
- Binding to start: when the descriptor declares a migration step,
  `update-start` admission requires the operation's applied, not reverted,
  `migrate-data` receipt (source and target schema, fence used) and refuses
  otherwise.

Failure and lost acknowledgement:

- A job that exits non-zero, writes no or a wrong marker, or whose target fails
  readiness is a known failure: the operation enters `recovery_required`.
- A runner crash or lost ack leaves the effect `unknown`. Before any choice is
  offered, a journaled `discard-migration` effect settles it: it only proves
  the migration container is absent (stopping it if needed) and moves the
  migration effect to `reverted`. The existing `restore` effect on the
  `restore_data` stage then restores the checkpoint into the same namespace,
  with the existing recovery-set bookkeeping. If that restore is itself
  uncertain, it is read back under its own identity; otherwise the operation
  stays `recovery_required` as today.
- The recovery surface then offers exactly two choices: **roll back** (restart
  the source release on the restored data — the existing path) or **retry**
  (start a new update operation behind a new review plan, which begins from the
  restored data). There is no "assume it finished" choice, and no retry inside
  the failed operation.

Static apps keep their fixed-format plugin tables; Portal never runs app DDL.

Acceptance (`LIFE-08`): fixtures A (compatible, no migration), B (successful
migration), C1 (job exits non-zero), C2 (marker missing or wrong), C3 (runner
killed after the job exits, before the ack), C4 (runner killed while the job is
still writing), C5 (readiness fails on migrated data), C6 (runner killed during
the settling restore), C7 (migration reverted, then `update-start` attempted
in the same operation — refused). For every C case: after settlement the
namespace digest equals the checkpoint, no target activation, both choices
offered, and each choice tested to completion.

## 3. PKG-01 / PKG-08 / SEC-01: managed image delivery

**Decision: a managed app's backend image travels inside the signed package as
OCI image content. Online install downloads the same package from the
catalogue. There is no registry client in this release, so there are no
registry credentials.**

Package and contract:

- `portalapp/2` adds `blobs/<sha256>` members holding the image manifest,
  config and layers for each declared platform. The signed descriptor binds,
  per platform, the manifest digest and the config digest. A package carries
  only the platforms it declares; install refuses when the host platform is
  absent.
- Members named `manifest.json`, `repositories`, `index.json` or `oci-layout`
  inside a package are refused. Portal never forwards a publisher-supplied
  layout to the daemon.

Loading (`load-image` runner command):

- The runner builds the load archive itself from verified blobs only, with an
  `index.json` it writes and that carries no name or tag annotations, so a load
  cannot retag any existing image, including Portal's own.
- Blobs move from Portal to the runner over the new bounded blob stream channel
  (schema table above), authenticated like the control transport but separate
  from it. The runner hashes the exact byte stream it writes into the load
  archive; there is no host path handoff and no check-then-use gap.
- Layers are decompressed by the runner while hashing: the result must match
  the declared `diff_id`, and decompression stops and refuses at the declared
  `uncompressed_bytes`, so a layer cannot expand beyond its declaration inside
  Docker's storage.
- Loads are serialized in the runner. It compares the daemon's `(ID, RepoTags)`
  pairs before and after, ignoring the expected ID, and refuses on any other
  difference. An image that is missing before a start (pruned, never loaded on
  this host) is reloaded from pinned blobs.
- `InspectBackendImage` is revised: the image ID must equal the config digest
  (classic store) or the manifest digest (containerd store), detected from the
  daemon; `RepoDigests` is no longer required for loaded images. Registry-style
  `backend.image` references remain accepted only in the development profile
  for images already present locally, so existing development releases keep
  working; the production profile requires `portalapp/2`.

Storage (PKG-08):

- Downloads stream into `<data>/app-staging/` as `.partial` files resumed with
  `Range`/`If-Range` across process restarts; bytes are re-hashed on every use.
  Unpinned staging is capped at 2 GiB with least-recently-used eviction;
  orphans are removed on start.
- Blobs of every active release and every release pinned by a retained recovery
  set move to `<data>/app-blobs/<sha256>`, outside eviction, capped at 8 GiB
  (install refuses when a new release would not fit). Entries in use by a load
  are locked against removal. Package limit is 384 MiB, per-blob limit 256 MiB.
- File names are the hex digest only; any other name in either directory is
  ignored and removed. No database table: the filesystem is the index.

SEC-01 in this release proves the negative: no image pull is ever issued, no
Docker credential helper or `~/.docker/config.json` is read, and a production
descriptor without `portalapp/2` content is refused. Registry pull with a
deployment-scoped credential is deferred to its own ADR.

Rejected: registry pull by digest now (credential store, new outbound path,
private-mirror exceptions to safe fetch, for small first-party-enrolled
images); a `docker save` tar or a publisher-supplied OCI layout passed to the
daemon (retag and member-precedence attacks).

Acceptance: PKG-01 installs the Scheduled Digest app from an offline
`portalapp/2` file and from the catalogue, with identical release, digests and
running image; a crafted package carrying a tag annotation or `manifest.json`
is refused and the daemon's image list is unchanged. PKG-08 interrupts a
download, restarts Portal, resumes from the `.partial`, corrupts a cached blob
and proves re-download, fills each cap to prove bounded refusal with no partial
release, and proves an active release's blobs survive cache pressure. SEC-01
runs the negative checks above.

## 4. OPS-08 / SEC-09: declared capacity envelope

**Decision: Portal enforces counts; the runner enforces host resources from
explicit start parameters. This is the declared maximum for this release.**

Reference host for measurement: a disposable 4 vCPU / 16 GiB Linux host (the
GitHub-hosted standard runner class) with a 16 GiB XFS project-quota volume.

| Limit | Enforced by | Default |
| --- | --- | --- |
| Installations, static and managed | Portal | 50 |
| Running managed backends | runner | 8 |
| Runner memory budget (backends and migration jobs) | runner | 25% of host memory |
| Runner CPU budget | runner | 50% of host CPUs |
| Runner disk budget | runner | 8 GiB of the XFS volume |
| Unpinned package staging | Portal | 2 GiB |
| Pinned release blobs | Portal | 8 GiB |
| Daemon image store (sum of declared `uncompressed_bytes` of loaded app images) | runner | 10 GiB |
| Tasks | Portal | existing 10000 global / 1000 per app / 256 per installation |

Disk arithmetic for the runner budget: each managed installation reserves its
256 MiB namespace quota plus two retained recovery checkpoints of at most
256 MiB each, so 768 MiB; eight managed installations need 6 GiB, leaving 2 GiB
for checkpoint creation during an update. Migration needs no extra copy
(section 2). Pinned blobs: eight managed apps keeping an active and a previous
release of at most 384 MiB need 6 GiB, leaving 2 GiB for static frontends and
one in-flight update. Admission refuses any managed app whose declared memory, CPU or
disk reservation does not fit the remaining budget, regardless of the
descriptor maxima.

Acceptance (`OPS-08`, closes `SEC-09`): on the reference host, 50
installations with 8 managed backends at their declared limits, tasks and
events saturated to quota, message, log and storage floods, and a layer that
decompresses beyond its declaration (refused), over a 2-hour
soak recording memory, PIDs, logs and disk. Pass: every excess request refused
with a typed error; Portal p95 latency for the home page and report detail
within 1.5x of the idle baseline; batch queue dispatch latency unchanged; host
memory and CPU stay within the runner budgets.

Owner decision 2026-11-13: within max(1.5x idle, idle + 5 ms), measured with load generated outside the measured process. A window passes when its p95 is at most
the larger of 1.5 times the idle p95 and the idle p95 plus 5 ms, for the home page, the report page and the queue's
dispatch decision.

## 5. DATA-08: retention defaults

**Decision: adopt the doc 05 proposal values, corrected so retention never
breaks idempotency or references; two values are administrator-adjustable.**

| Record | Default | Adjustable |
| --- | --- | --- |
| Review plans | valid 15 minutes; unconsumed expired plans swept; consumed plans kept with their operation | no |
| Completed operations and evidence (this row also holds idempotency) | 90 days | yes, 90–365 days |
| Terminal task and event history | 30 days and 10000 rows per installation; a task's occurrence tombstone (occurrence ID and payload hash) is kept for the operations period so a replayed occurrence is still deduplicated | no |
| Runner logs | 10 MiB per installation, 7 days | no |
| Recovery sets | latest 2 per installation | yes, 1–2 (the disk arithmetic in section 4 assumes at most 2) |
| Uninstalled data | until explicit deletion | no |

Never collected regardless of age: unresolved effects, `unknown` or
`review_required` work, operations referenced by a retained recovery set or
import grant. Adjustable values live in the database `meta` table, edited in
the existing management UI, not `config.yaml`. The sweeper does not exist yet;
implement it test-first.

Acceptance: a time-travel test advances the clock past each limit and proves
deletion of eligible rows only, retention of every pinned or unresolved row,
replay of an idempotency key within 90 days still returning the original
operation, replay of a swept task occurrence still deduplicated, and an audit
record of each sweep.

## 6. OPS-05 / OPS-06: deployment drills

**Decision: both drills run on a disposable, egress-denied staging host, not on
production.**

- The host has no outbound network except what the drill operator opens for
  image transfer; Dify, SMTP, webhook targets and the market are unreachable by
  construction.
- Before the copy's first boot, the drill disables scheduled and recurring runs,
  webhooks, batch targets and mail delivery in the copied database.
- The production `secret_key` reaches the host only over the operator's SSH
  session onto a tmpfs file, is never written to disk, and is destroyed with
  the host.
- OPS-05 deploys the platform candidate through the production procedure
  (single compose file, fixed version tag) against the database copy and
  verifies the loaded image digest and version.
- OPS-06 forces a failed platform rollout there and restores the protected
  recovery set, verifying Portal/runtime compatibility and key recovery before
  any activation.
- The host is destroyed after the drill; the database copy never leaves it.

Executing the drill touches the production server (to take the copy) and is
started by the owner explicitly at that time. Production adoption of the
platform is a separate deployment after section 9's gates.

## 7. Human-only acceptance

No automated run by an implementer may be relabelled as satisfying these.

| Row | Beta gate | Stable gate |
| --- | --- | --- |
| `DEV-01` | A fresh agent session with no repository history follows only the tutorial from a clean checkout | One human non-implementer does the same |
| `LIFE-02`, `RUN-01` | Neutral-operator script run on a disposable host by a session that did not implement the feature | Human replay |
| `UX-08` | — | Owner runs a scripted VoiceOver audit of the lifecycle and app-boundary paths |

## 8. DEV-04: licence policy

**Decision: this policy applies to every shipped Portal and Apps artifact.**

- Allowed: MIT, BSD-2/3-Clause, ISC, Apache-2.0, 0BSD, Zlib, Unlicense, CC0,
  BlueOak-1.0.0, Python-2.0, MPL-2.0 (file-level, unmodified), and the
  project's own AGPL-3.0 code. `dompurify` is taken under Apache-2.0 from its
  dual licence.
- Elected licence: `github.com/golang/freetype` (via the captcha dependency) is
  used under the FreeType License, recorded in the SBOM review.
- EPL-2.0 is not GPL-compatible without a Secondary License, and a Vite bundle
  embedded in the binary is not a separate component. `elkjs` arrives as a
  direct dependency of `mermaid` 12 and is used only for the ELK layout option.
  Decision: alias `elkjs` to an empty module in the Vite build, do not support
  the ELK layout, and add a build test that fails if ELK code appears in the
  output.
- Denied: GPL-2.0-only, SSPL, BUSL, Commons Clause, EPL without a GPL Secondary
  License, any licence without a grant, unknown licence.
- CI gate: the SBOM licence scan fails on a denied or unknown licence. `elkjs`
  is recorded as an explicit "not shipped" exclusion, valid only while the ELK
  build test passes.
- Apps by third parties are separate works; the platform imposes no licence on
  them. First-party apps stay AGPL-3.0.

This is an engineering policy set by the copyright holder; seek counsel before
distributing the platform commercially to third parties.

## 9. Integration and release order

PR 364 (about 900 commits, 1176 files) is not reviewable as one change, and
its platform migrations are 0015–0020: once they reach `main`, any release cut
from `main` would apply the platform DDL to production. Therefore:

- First, a small PR to `main` reserves migration IDs 0015–0020 for the platform,
  with a guard in the migration order test that fails if `main` defines any of
  them; `main`'s next migration is 0021. This keeps the regular merges below
  collision-free.
- Create the long-lived branch `integration/app-platform` from current `main`.
  The work reaches it as an ordered stack of pull requests, each reviewed and
  CI-green on its own:
  1. contracts, descriptor, signing corpus and SDK types (no runtime);
  2. Portal platform storage (migrations 0015–0020) and control store;
  3. market, install, static apps and management UI;
  4. app runner and managed runtime;
  5. tasks, events, notifications, reviewed network and secrets;
  6. backup, restore and recovery;
  7. reference apps, handbook and evidence.
- Sections 1–5 and 8 are implemented on top, test-first, one PR each.
- `main` is merged into the integration branch regularly; `main` releases
  continue unaffected.
- The integration branch merges into `main` once, when the beta gate passes,
  with the platform disabled by default.

Release gates:

- **Beta**: stack and sections 1–5, 8 merged with acceptance evidence; OPS-05
  and OPS-06 passed on staging; agent-run gates in section 7; licence gate
  green; then the single merge into `main` and a beta release.
- **Stable**: human gates in section 7; final release artifacts rebuilt and
  re-scanned; at least one beta deployed and observed before promotion.
