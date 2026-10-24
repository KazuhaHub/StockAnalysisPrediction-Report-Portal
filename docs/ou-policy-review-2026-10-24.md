# OU policy integration review

Reviewed 2026-10-24 against main `ebd08fac`. This is a code and storage review, not an
implementation or a deployment record. No application behavior, database schema or local database
content was changed. The existing [framework](ou-policy-framework.md) and
[feature template](ou-policy-feature-template.md) incorporate these findings.

## Integrated foundations

Forecast record access (PR 145), operational notifications (PR 141), and external application
backend connections are on main. Their core contracts are usable now; framework work no longer
needs to wait for those branches. Other reader and presentation work remains in separate worktrees.

Reusable pieces already exist:

- `ou_index.go`: one-query OU snapshot, bounded ancestry, deterministic subtree output, errors on
  missing parents/cycles, transaction-aware loading.
- `forecast_access.go`: pure account/tree resolution and a repeatable-read account/tree snapshot on
  Postgres. `adminGroupsWithAccess` already returns own/effective/source for forecast access.
- `forecast_group_settings.go`: presence-aware input, validation before mutation for the forecast
  field, exact from/to changes, and no-op detection for that field.
- `extension_identity.go`: live authority, actual ancestry validation, account generation and
  creation-time-bound opaque OU/account identities.
- Notification configuration: transactional writes and optimistic revisions, plus explicit
  revocation/cleanup. Audience routing remains separate from authorization.
- `InheritField` and `OrgUnitDetail`: existing controls can consume a common server contract.
- ADR 0035's ordered transactional migration ledger and backup enumeration include new tables.

## Verified defects and gaps

| Priority | Finding | Consequence and evidence |
| --- | --- | --- |
| High | OU update is not atomic | `apiGroupSave` writes name, weight and governance before validating a requested parent. A rejected cyclic move returned HTTP 400 but persisted a new name and weight 77. Several write errors are ignored. Create also inserts before parent validation. |
| High | Empty run allow-list is indistinguishable from unset | `SetGroupTargets(child, [])` removes rows; `resolveGroupTargets` then inherits the parent's rows. A restricted child explicitly saved empty was still allowed to run the parent's target. |
| High | Target deletion leaves authorization references | `DeleteTarget` removes only the target row. A diagnostic retained the corresponding `group_targets` row after deletion. A stale grant alone cannot run a missing target, but id reuse can attach it to a different target. |
| High | Browser and backend disagree on parent inheritance | With Default max-queued 3/TOTP true, parent 9/false, and unset child, the backend resolves 9/false and `resolveOU` resolves 3/true. This affects governance and enrolment displays. Existing unit tests miss this three-level parity case. |
| High | Invalid membership assignment can fall into Default | `SetPrimaryGroup` converts an unknown group or unreadable existence probe into a clear. SSO explicitly guards against this, but the store contract itself is unsafe for other callers and races. Require an existing destination and surface errors in an atomic assignment path. |
| Medium | OU deletion changes account policy implicitly | `DeleteUserGroup` clears direct members to Default, moves forecast sets/evidence, and cleans known principal tables in a transaction. Clearing can remove a restricted OU's controls. Require an explicit destination or block populated deletion, with impact preview. |
| Medium | Tree validation differs by consumer | `groupChainErr` surfaces SQL failures but silently truncates cycles/depth and tolerates missing rows. Forecast's index rejects those paths, while extension identity walks actual parents and has a different root/default contract. |
| Medium | Default/topology validation is incomplete | Creating an OU without a parent creates another root; the API permits detaching and does not ensure a parent exists. `loadOUIndexFrom` selects the first Default without an ordering/duplicate check and stops at it without validating its parent. |
| Medium | Lifecycle coverage omits migration tables | `principalTables` in `principal_sweep_test.go` discovers baseline declarations only. `notification_route_grants` is swept by production deletion but is not covered by that generic discovery. Extend coverage to the declared migration products or actual test database catalog. |
| Medium | Repeated reads and inconsistent snapshots | Governance reads Default, membership, each parent and each policy row. Restricted report reads separately reload restriction, OU index, membership and version grants. Admin OU responses read the graph and group rows separately. Shared operation-scoped snapshots reduce queries and make provenance consistent. |

The priority labels describe implementation order, not a claim that the local development database
currently contains an exploited or corrupt state. Reproduction used isolated temporary test stores.

## Policy inventory

| Domain | Current backend semantics | Migration boundary |
| --- | --- | --- |
| Ticket weight, urgent eligibility, queue cap, run window | Nearest explicit ancestor, built-in baseline at the end | Preserve backend behavior; correct the browser's flat Default fallback. Urgent's combined label is derived from independent settings, potentially from different source OUs. |
| Run quota and period | Nearest quota replaces the pair; omitted/invalid period on a configured quota means day | Keep the pair atomic and preserve explicit zero as unlimited. |
| TOTP/passkey enrolment | Nearest explicit ancestor, bounded by global enablement | Return both OU value and final global-floor result; do not treat enrolment permission as a mandate or invalidate an existing factor. |
| Restricted tenancy, second-factor mandate | Sticky restrictive floor across ancestry | Descendants cannot clear an ancestor's requirement. Keep these security floors explicit even though ordinary settings can be overridden. |
| Forecast scope | Nearest explicit `all`/`own`/`none`; Default NULL means none; admin exemption | Keep scope source separate from the account's own population root and from report grants. |
| Run target collection | Nearest nonempty list; no list means deny for restricted readers | Requires approved presence storage to represent override-with-none. |
| Version grants | Account's nonempty grants, else nearest OU's nonempty grants | The same empty-versus-unset limitation exists here. Define whether explicit deny-all is required before migrating storage or semantics. It also needs error-aware reads: a failed nearer query must not become parent grants. |
| Announcement and notification recipients | Audience principals along a chain | Audience membership is not scalar nearest-value resolution. Notification resource authorization still runs independently. |
| External backend identity | Opaque ids for the actual parent chain | Keep encoding, creation time, account generation and current authority; never append a synthetic Default audience. |
| Base run priority | Primary OU override, else system setting | This is the remaining legacy fallback. Parent-chain priority would be a separate product behavior change. |

## Single-root compatibility decision

The owner's target is one Default root and nearest-parent inheritance. Current ADR 0039 explicitly
permits non-default roots and distinguishes policy inheritance from actual subtree membership.
Existing tests intentionally preserve that behavior, including children becoming roots when an
old root is deleted. The framework draft is the proposed change; it does not silently supersede
the accepted reader contract.

An automatic `parent_id=Default` rewrite is not neutral. Default-own readers would gain those roots'
historical attributed sets, subtree cache keys would change, and external backends would receive
an additional ancestor identity. Do not perform that rewrite as a cleanup or schema backfill.

The implementation should first report invalid/legacy topology with affected OU, account and
population counts. New OU creation can use Default when no parent is supplied; Default cannot move;
non-default detachment and nonexistent parents should be refused. Existing non-default roots need
an explicit reviewed reparenting operation and an ADR 0039 amendment. This is a data/permission
transition even if it needs no schema change.

Graph extraction therefore needs two operations during transition: actual parent ancestry for
subtrees/identity, and a named compatibility policy chain that can append Default. A shared node
snapshot does not imply that every feature should see the same synthetic edges.

## Storage assessment and proposal

The current use of typed nullable columns for scalar overrides is reasonable. The table is still
small enough that moving every scalar to another table would add joins and migration work without
solving the verified correctness defects. Keep topology/identity on `user_groups`; use typed
per-domain side tables for larger new domains and structured collections. Avoid a generic
`ou_policies(key,json)` store or per-OU keys hidden in `meta`.

Two concrete options were compared for target collection presence. Following this review, the owner
approved Option A on 2026-10-24. The [iteration plan](ou-policy-iteration-plan.md) records the approved
shape and its implementation gates; no schema implementation has been written yet.

| Option | Shape and state model | Tradeoff |
| --- | --- | --- |
| A, recommended | A typed `group_target_policies` header keyed by `group_id`, beside existing `group_targets` entries. A header means an explicit replacement, even with zero entries. Inherit removes both header and entries. | Keeps collection presence beside its domain, without another policy flag on `user_groups`. Requires lifecycle, backup and restore coverage for the new table. |
| B | A nullable `targets_configured` flag on `user_groups`. NULL preserves legacy inference; false explicitly inherits; true uses the entries even when empty. | Smaller join footprint but grows the central table and couples target policy presence to every OU row. |

For Option A, preserve old nonempty lists when the header is absent: no header plus existing entries
is a legacy configured list; no header and no entries is inheritance. New override writes always
write the header and entries atomically. This compatibility read also makes an older dump without
the new table retain its behavior. It must be documented as a legacy decoder, not the template for
new collection storage. Never infer the historical intent of an old empty list: the old database
did not record it. Only a new explicit override can create deny-all.

An approved migration would add only the typed header and declared products, without backfilling
policy intent or changing the frozen baseline. An older-dump allowance would be valid only with
tests proving the compatibility decoder above. A backup claiming the new migration must include
the header table and its empty-override rows. Header and entries must be loaded from one snapshot
and updated, cleared or deleted in one transaction. The same tests must run on SQLite and Postgres.
An older binary does not enforce the new empty override; rollback remains restore of the retained
pre-upgrade backup with the previous release under the migration-ledger contract.

`version_grants` needs a separate decision because account-level overrides also participate; a
target-only header should not be stretched into a generic principal policy engine.

Other database observations:

- `users(group_id)`, `user_groups(parent_id)` and `group_targets(target_id)` are candidates, not
  automatic additions. The local SQLite plan for the membership join creates an automatic covering
  index on `users.group_id`. Measure on representative account/OU counts before an approved index
  migration; a one-OU development database cannot establish a performance benefit.
- Relations are currently application-owned rather than foreign-key enforced. Notification ADR
  0038 explicitly keeps transactional cleanup. A blanket FK retrofit would affect delete order,
  restore and both drivers; tighten lifecycle tests and write validation first.
- SSO provider defaults and JSON assignment rules also contain OU references. Deletion impact must
  include them. SSO's assignment guard currently refuses an unknown destination rather than silently
  clearing it, so the stale rule becomes a failed login until corrected.
- Baseline and migration-ledger code already give a strong upgrade/restore boundary. Root repair,
  policy framework extraction and baseline/schema changes should remain independent review units.
- AGENTS.md still describes additions to the frozen baseline as the only column mechanism, while
  accepted ADR 0035 and current code use append-only migration steps. This documentation conflict
  should be reconciled explicitly; it does not remove the owner's required schema approval.

## Implementation units

1. Existing-shape correctness: atomic OU create/save with all validation before writes, checked
   errors and post-commit audit; transactional target-grant cleanup; strict membership assignment.
2. Shared graph and typed results: operation-scoped loading, actual/compatibility ancestry,
   nearest/sticky/atomic primitives, validation diagnostics, source and availability.
3. Server-authoritative editor: additive own/effective/source contract and proposed-state preview;
   migrate `OrgUnitDetail` and remove corresponding browser resolution; use cross-layer fixtures.
4. Topology/lifecycle: amend the root contract, block invalid new writes, report legacy topology,
   and require a destination/impact review for member moves and OU deletion. Preserve forecast
   ownership/purge locks and external identity generations.
5. Owner-approved collection storage: target collection presence, migration, older/current restore,
   lifecycle cleanup and explicit inherit/empty/nonempty UI. Decide version collection semantics
   separately.

These units should remain separately reviewable. A graph refactor must not also widen a forecast
population, rewrite historical attribution or change principal encoding. The feature template is
the checklist; typed helpers, a shared API contract and reusable controls are the implementation.

## Validation and limits

- Focused existing Go tests passed on SQLite: OU ancestry and API, forecast scope/snapshot/admin
  result, deletion/attribution, sticky mandate, enrolment inheritance, principal sweep, and external
  identity ancestry.
- Four temporary opt-in diagnostic Go tests ran against isolated stores: three failed as expected
  for rejected-save atomicity, empty target overrides and target-grant deletion; the backend
  nearest-parent control passed. The diagnostic file was removed after observation.
- Direct execution of the browser's TypeScript resolver reproduced the 3/true versus 9/false
  mismatch described above.
- The initial focused frontend run passed 35 tests in two files; the detail suite could not load
  because local dependencies lacked `axe-core`. After synchronizing dependencies from the existing
  lockfile, all three focused files passed: 65 tests (`ouSettings`, `OrgUnitDetail`, `OrgUnitPicker`).
  No manifest or lockfile change was required.
- The local `data/portal.db` was opened read-only: integrity check `ok`, one OU/one Default, no
  non-default roots, dangling parents, dangling memberships or stale target references, and no
  target grant rows. It records migrations 0001-0005 only; it was not opened through the current
  application or upgraded. It does not exercise a real hierarchy or the integrated migrations.
- `TEST_POSTGRES_DSN` was unset. No live Postgres or production database validation was performed.
  This review is not an exhaustive performance or data-integrity audit of every feature table.
