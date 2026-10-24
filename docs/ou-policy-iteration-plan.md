# Unified OU iteration plan

Status: iteration plan refreshed on 2026-10-24. Current implementation base: main `ca952799`;
the earlier integration review used `ebd08fac`. The working branch already contains partial,
uncommitted implementation. Its presence is not evidence that a work package has passed acceptance.
This plan builds on the
[integration review](ou-policy-review-2026-10-24.md), [framework](ou-policy-framework.md),
and [feature template](ou-policy-feature-template.md).

## Execution checkpoint

This iteration covers every consumer in the ledger below. The checkpoint records source inspection,
not a fresh test run. Completion requires the final verification gates, even where an implementation
or a regression test is already present. Keep existing working changes and review them against this
plan rather than starting a second implementation.

| Area | Present in the working branch | Remaining acceptance work |
| --- | --- | --- |
| Shared facts and resolution | Common graph, subject snapshot, nearest/sticky/bundle results and writer serialization | Complete the call-site inventory; prove callers reuse consistent facts and preserve domain exemptions. |
| Administration and membership | Atomic OU create/save and individual/bulk membership helper | Make a combined account edit atomic across its OU and other submitted fields; migrate SSO configuration and assignment under the same validation boundary. |
| Target collections and storage | Typed presence header, ordered migration, explicit inherit/empty/nonempty handling | Verify current/older restore, deletion and id reuse, schema verification, all enforcement surfaces and changed-only audit. |
| Report, forecast and identity | Shared graph/snapshot adapters in several consumers | Inspect every read/export/token/background path; record retained compatibility wrappers and their failure contracts. |
| API and editor | Server policy results, preview and collection-mode controls | Remove browser inheritance fallback; preview the full draft; prevent stale preview use; show field-specific sources, global constraints and topology/move impacts. |
| Lifecycle | Topology diagnostics, blockers for member/SSO references, and every grant writer (version, announcement, report viewer) validated under the shared OU mutation lock | Commit selected orphan handling with cleanup; exercise move/delete concurrency and legacy-root review. |
| Closure | Regression and driver test files exist | Run final checks against the completed tree, amend ADRs, record evidence and complete the consumer ledger. |

Implement the remaining transaction and lifecycle gaps before declaring the API/UI contract final.
Then remove obsolete resolution paths, complete cross-surface tests, and run the release gates.
Do not automatically merge, deploy, or reparent stored legacy roots as a consequence of this plan.

## Outcome and owner decisions

One iteration will move every OU consumer onto shared identity, topology, validation and typed
resolution infrastructure. Completion means callers actually use the infrastructure, the editor
shows its results, and lifecycle/restore tests cover the participating tables. A template alone
does not satisfy the iteration.

The owner specified a Google Workspace-style tree: Default is the unique top-level OU, an unset
ordinary setting takes the nearest explicit parent value, and Default supplies the final baseline.
The owner approved the recommended target-collection storage direction on 2026-10-24: a dedicated
`group_target_policies` presence header beside the existing `group_targets` entries. This approval
covers that header shape, not additional version-policy tables, revision columns, indexes, foreign
keys or a generic JSON policy store.

Unification preserves the different questions each feature answers. Ordinary inheritance, sticky
security requirements, replacement collections, audience union, actual subtree population and
historical attribution are explicit adapters over the same facts. Report grants, forecast scope,
role permissions and notification audience remain independent authorization boundaries.

The single-root target changes the legacy root contract in ADR 0039. New writes must satisfy the
target; old roots get an impact report and an explicit reparent operation. Existing roots are not
automatically attached to Default because that would change historical forecast populations and
external backend ancestor grants.

## Coverage ledger

Each row must have migrated call sites and an enforcement/contract test before completion. A file
that stores an OU id does not necessarily perform inheritance; raw historical ids must stay raw.
Report content groups and link-layout groups are not organizational units and are outside this
unification, even where names overlap.

| Consumer and entry files | Shared infrastructure | Feature behavior to retain |
| --- | --- | --- |
| OU CRUD and listing: `user_admin.go`, `user_admin_api.go` | Validated graph, typed policy results, atomic writes and impact preview | Default is protected; new ordinary OUs require a real parent; an omitted update parent leaves it unchanged. |
| Account create/edit/bulk and registration: `apiui.go`, `user_admin_api.go`, `register.go` | Strict membership mutation and assigned/effective OU facts | Explicit clear and a newly registered unassigned account mean Default; missing account, missing destination and read failure must not become a clear. |
| Governance: `user_admin.go`, `tickets.go`, `batch_run.go`, `batch_api.go`, `run_quota.go` | Nearest and atomic-bundle resolvers; one operation-scoped decision | Queue cap, run window, urgent settings and quota retain their applicability/admin exemptions. Quota number and period travel together. |
| Base run priority: `batch_run.go` | Shared subject facts and a named legacy resolver | Preserve primary override then system default in this iteration; parent-chain priority is a separately visible behavior change, not part of mechanical extraction. |
| Workflow entitlement: `group_targets.go`, `batch_api.go` and run-adjacent handlers | Presence-aware replacement collection | Surface intersection and restricted-user default-deny; list, submit, upload, recurring and chat entry points must agree. |
| Security: `security_policy.go`, `security_cli.go`, enrolment gate and factor handlers | Nearest enrolment, sticky mandate, global floors and strict subtree | Existing factors remain usable when enrolment is withdrawn; a mandate cannot disappear on a broken chain; preserve enrolment/recovery route exemptions. |
| Report versions and tenancy: `version.go`, `report_owner.go`, `version_api.go` | Strict subject snapshot, version collection adapter, validated principals | Account grant precedence, restricted-ancestor sharing, version visibility, admin/machine boundaries and same-day reuse remain intact. |
| Report reads, manual audience and exports: `apiui.go`, `apiv1.go`, `server.go`, `report_edit_api.go` | The shared read decision and principal helpers | Lists, direct body, Markdown/PDF/archive, manual reports and app-token reads apply the same existing read boundary. |
| Ingest attribution: `report_owner.go`, `apiv1.go`, `forecast_store.go` | Current subject facts and explicit historical attribution adapter | Signed token, first-writer report ownership, frozen set attribution, deleted/recreated identity checks and reserved inputs stay unchanged. |
| Forecast: `forecast_access.go`, `forecast_access_preview.go`, `forecast_group_settings.go`, population/read stores | Shared graph, nearest scope, actual subtree and provenance | `own` roots at the member OU, not the scope source; external response shaping and report-readable evidence remain independent. |
| Retention and OU deletion: `forecast_purge_owners.go`, `user_admin.go` | Common lifecycle service and actual-subtree facts | Existing ownership locks, evidence transfer, deduplication, reattribution exception and cache invalidation remain transactional. |
| Announcements: `announcement_api.go`, `report_edit_api.go` | Audience-chain adapter, principal validation and directory | Union audience; Default's existing all-audience encoding; no admin bypass; preview matches feed; preserve explicit orphan handling. |
| Notifications: `notification_worker.go`, `notification_admin.go`, `notification_channel_store.go` | Audience-chain adapter and lifecycle validation | Routing never grants resource access; current delivery/inbox authorization, account generations and consent remain authoritative. |
| SSO: `identity.go`, `sso_admin.go`, `sso_login.go`, `sso_provider.go`, `sso_rules_store.go`, `internal/ssorules` | Destination validation, strict assignment and lifecycle reference inventory | Rule evaluation remains feature-specific; assignment remains current and invalidates sessions when required; JSON rules are validated, not silently rewritten. |
| External applications: `extension_identity.go`, `extension_principals.go` | Actual ancestry and identity facts in the existing transaction | Preserve opaque encoding, creation time, account generation, live session/role/app binding and repeatable-read guarantees. |
| Audit: `audit.go`, login history and audit call sites | Shared actor facts distinguishing assigned OU from effective OU | Keep historical actor values and attribution meanings; log committed changes, not attempted or rejected changes. |
| Admin UI: `UsersPage`, `OrgUnitDetail`, `OrgUnitPicker`, `ForecastAccessSummary`, SSO and recipient pickers | Typed API results, one tree presentation helper, reusable policy fields | Server owns effective values; browser may organize labels/tree selection but cannot decide security policy. |
| Backup, restore and schema tests: `backup.go`, migration runner and lifecycle tests | Approved migration products and complete reference inventory | Frozen baseline, append-only steps, contiguous ledger and restore transactions remain authoritative. |

## Common contracts

### Subject and graph

Provide a transaction-aware loader and an immutable operation-scoped snapshot. Subject facts
distinguish assigned OU, effective OU, absent/inactive account, role, account restriction and the
identity facts needed by the caller. Anonymous and machine requests are explicit contexts; neither
is converted into a Default member simply to obtain a result.

Graph operations expose actual ancestry, actual subtree and a clearly named legacy policy fallback
chain. Missing/duplicate Default, Default with a parent, non-default root, missing parent, cycles
and depth overflow have explicit diagnostics. An unrelated corrupt branch is reported by the health
inventory; each enforcement adapter defines the validation needed for its selected chain/subtree.
Never truncate a broken chain into a successful permission result.

Load the tree once per operation. Reuse subject/graph/policy facts across internal calls, with no
permission cache surviving the request or background operation. Use the same database transaction
for facts that must agree; preserve Postgres repeatable-read and SQLite's single-connection rules.
Do not call a pooled reader while holding the sole SQLite transaction connection.

### Typed policy results and errors

Provide small typed operations: nearest explicit value, sticky restrictive floor, atomic bundle,
configured replacement collection and audience/identity adapters. Feature values remain typed;
SQL construction and feature dispatch do not depend on arbitrary string policy keys.

Admin results expose own value, effective value, inherited state, source and availability. A source
can be an OU, an account, a system baseline or a global constraint. Composite urgent status can have
different sources per contributing field; a sticky requirement may have multiple imposing ancestors.
Unavailable is a distinct result, not a permissive effective zero. Sensitive source details remain
administrator-only.

Writes distinguish omitted, explicit inheritance and explicit zero/false/empty. Keep existing API
fields during conversion and add typed structured results. Preview accepts proposed parent plus
overrides and invokes the enforcement resolver without saving. The final writer rereads and
revalidates under the mutation lock; preview is not permission to commit stale assumptions.

Enforcement errors fail closed where they could widen access or bypass a security gate. Admin pages
show unavailable and a retry/repair path. Preserve feature-specific anonymous, admin, global-floor
and recovery exemptions explicitly. Status codes keep the endpoint's existing envelope conventions.

### Lifecycle references

Inventory references by meaning: live membership/topology, grants and SSO configuration, immutable
attribution, historical audit, and external opaque identities. Cleanup is not a blanket deletion
of every numeric OU reference. Preserve historical audit and frozen attribution except the already
approved forecast deletion exception.

Every new OU/domain mutation validates before writes and commits its related database changes as
one transaction. Rejected and no-op operations produce no change audit; successful changes capture
bounded before/after facts. Preserve the established best-effort ordinary audit contract, and keep
existing atomic dedicated audit paths atomic. This iteration does not redefine audit durability.

## Work packages and dependency order

| Package | Dependencies | Deliverable and acceptance gate |
| --- | --- | --- |
| OU-01: contracts and fixtures | None | Freeze this coverage ledger; add three-level, restricted-sibling, legacy-root, malformed-tree and identity-reuse fixtures. Record approved storage and intended ADR amendments. |
| OU-02: shared graph and subject | OU-01 | Extract `ouIndex` into policy-independent graph/identity facts; add transaction-aware loaders, actual/compatibility traversal and precise errors. Old/new results agree on valid fixtures. |
| OU-03: typed resolution and atomic administration | OU-02 | Add typed results and shared mutation validation. Fix partial OU create/save, strict individual/bulk/SSO membership assignment, ignored OU-related write errors and post-commit audit. Validate every submitted policy, including enrolment and forecast, against the proposed state. |
| OU-04: approved target policy presence | OU-02, OU-03 | Add the approved header through a new ordered migration; preserve old list decoding; implement atomic inherit/empty/nonempty writes, error-aware reads and target-delete cleanup. Older/current restore and target-id reuse tests pass. |
| OU-05: migrate all backend consumers | OU-02, OU-03, OU-04 for target users | Move every coverage-ledger row to shared facts/resolvers. Replace direct lenient traversals and silent read fallbacks, preserve domain adapters, and prove all read/run/token/background paths use the migrated decision. |
| OU-06: authoritative API and UI | OU-03 through OU-05 | Add consistent own/effective/source/availability and proposed-state preview; migrate editor, workflow collection, tree/pickers and summaries; remove browser policy calculations after parity tests pass. |
| OU-07: topology and lifecycle transition | OU-03 through OU-06 | Enforce the unique-root contract on new writes; expose legacy health/impact; protect populated deletion; include SSO/announcement/notification impacts; amend ADR 0039 and exercise explicit root reparenting. |
| OU-08: closure and rollout evidence | OU-01 through OU-07 | Remove obsolete traversal implementations, finish the coverage ledger, run SQLite/Postgres/concurrency/restore/UI gates, and publish the operator transition and reusable feature recipe in repository docs. |

This is one iteration with separately reviewable commits or PRs, not one inseparable patch. Plan
for sequential integration of shared contracts and storage; implementation may prepare independent
UI fixtures after the API contract is stable. No additional chats or delegation are implied.

## Approved target collection storage

The approved new header has one identity column: `group_id BIGINT NOT NULL PRIMARY KEY`. Existing
`group_targets(group_id,target_id,surfaces)` remains the entry table. The migration id is selected
from the then-current append-only registry at implementation time; do not reserve a number while
other branches may merge steps. Declare/verify the header's key and nullability, and include it in
backup enumeration. Do not change the frozen baseline or edit released steps.

| Persisted state | Meaning |
| --- | --- |
| Header exists, zero entries | Explicit override: allow no targets. |
| Header exists, entries exist | Explicit override: use exactly these entries. |
| No header, entries exist | Legacy configured nonempty list; preserve its current replacement semantics. |
| No header, no entries | Inherit from the next OU, ultimately restricted default-deny. |

New override writes always create the header. Inherit removes header and entries atomically. The
legacy decoder preserves older databases and older dumps without backfilling an intent that was
never stored. A dump claiming the migration must contain the header table; a pre-step dump may
lack it only because the decoder preserves its behavior. Test the `olderDumpOK` declaration rather
than assuming it from the table being additive.

Deleting a target must preserve configuration presence, not just remove dangling rows. Before
removing a legacy OU's last target entry, materialize its header in the same transaction. Otherwise
cleanup would turn an explicit configured list into inheritance and potentially grant the parent's
targets. Deleting an OU removes its header and entries in its existing lifecycle transaction.
Validate target and OU existence, duplicate entries and allowed surface values before writing.

Version grants migrate to the shared strict collection infrastructure while retaining their
documented nonempty-account/nearest-nonempty-OU legacy semantics. Explicit empty version denial
would require a separate account/OU presence design and approval; the approved target header
cannot encode it. Additional indexes require measured evidence and a separate schema proposal.

## Database and query review

Review storage by meaning and by measured use, not just by reducing the number of tables. Existing
nullable typed OU columns remain appropriate for scalar overrides. Keep actual topology and current
membership separate from historical attribution and audience/grant relations. Do not materialize
inherited values into every descendant: moving an OU or changing a parent should not require a
fan-out rewrite with stale effective rows.

| Review | Evidence and outcome required |
| --- | --- |
| Representation | For each policy, document null/unset versus explicit zero, false or empty; fix the target collection ambiguity with the approved header only. |
| Reference integrity | Inventory baseline and migrated tables plus OU ids embedded in SSO rules; classify blocking, cleanup and retained historical references. Test missing targets and identifier reuse. |
| Transaction boundaries | Trace OU/account/SSO writes, grant replacement, target deletion and OU deletion. Validation and related mutations must share the serialized writer boundary; no partial success or audit for a rejected change. |
| Read consistency | Measure subject/tree loads across shallow and deep chains and representative OU/account sizes. Remove depth-dependent queries and per-descendant preview count queries; batch related reads within the same operation snapshot. |
| Query plans | Inspect SQLite and PostgreSQL plans for measured slow membership, subtree, grant and preview queries. Report row counts, query counts and plans before proposing a new index or denormalization. |
| Compatibility | Verify append-only migration products, older/current backup restore, malformed products, refusal/rollback and restart with disposable databases. Keep the frozen baseline unchanged. |

The delivery records both improvements and deliberately retained storage, with the reason. No new
schema shape beyond the approved header is assumed. If measurements justify another table, column,
constraint or index, prepare the concrete proposal before implementing that extra shape.

## Move, delete and legacy-root handling

- New OU without a specified parent is created under Default. An explicit invalid/zero parent for
  an ordinary OU is rejected; an omitted parent during update preserves its current placement.
  Default cannot move or be deleted. Mutations check target existence and cycles under the same
  serialized writer boundary so concurrent moves cannot create a cycle after separate prechecks.
- Move preview reports changed effective policies and affected account/subtree counts, forecast
  population changes, and external ancestor identity changes. Recheck these facts before commit;
  never grant access on the basis of a browser preview.
- A populated OU cannot be deleted through the old implicit-to-Default path. Require an explicit
  prior member move or a deliberate destination in the reviewed delete operation. The safe initial
  implementation may block deletion until those members are moved. Missing SSO targets must be
  corrected or explicitly removed before deletion; keep the rules' configuration visible.
- Child reparenting, forecast set/evidence transfer, grant/header cleanup and the selected orphan
  treatment must succeed together. Preserve prediction ownership lock order and retention fencing;
  compute audit counts from the committed operation and invalidate the relevant population cache.
- Existing roots remain visible as legacy topology until explicitly moved with impact review.
  No startup/backfill silently changes them. The temporary compatibility chain is implemented once
  and has a named call-site allowlist; healthy new data uses the unique-root path. Diagnostics and
  management repair remain reachable when a business policy cannot be resolved.
- Do not rewrite report ownership history, audit actor ids, creation timestamps, external opaque
  id encoding or signed owner-token formats as a side effect of this iteration. Verify identity
  reuse and queued-job attribution timing against their current contracts.

## Verification and completion

Use failing regression tests before each correctness fix. The common fixtures must prove Default,
unset member, nearest parent, explicit zero/false, quota pair, sticky floors, account/OU grants,
inherit/empty/nonempty target collections, unknown values and store failure. Add cross-layer fixtures
where Default, parent and leaf intentionally differ; stored state, preview, admin response and
runtime enforcement must agree, including the previously reproduced max-queue/TOTP mismatch.

Exercise rejected-save rollback, no-op/changed audit, stale destination and concurrent account/OU
delete/move, source-query failure without parent-grant fallback, target deletion/id reuse, existing
factor use, queued execution, and retention/OU-delete serialization. Lifecycle discovery includes
tables introduced by migrations, not only `baseSchemaStmts`.

Test every public surface that can disclose or run: cookie and v1 reads, direct report bodies,
exports, app bridge, forecast readers/previews, upload/run/batch/chat/recurring/scheduled entry
points, SSO login, announcement feed/preview, notification dispatch/inbox and external proxy identity.
Authentication, role, report grant and OU policy must remain independent checks.

Before completion, run Go build/vet/test, the relevant race/concurrency cases, frontend typecheck,
tests and production build. Run Postgres integration against an isolated test database as well as
default SQLite tests. Validate pre-step upgrade and older/current backup restore, damaged products,
rollback refusal and restart behavior with disposable databases. Never use the local development
or production database as a migration fixture.

Measure query counts at multiple chain depths and representative OU/account sizes. A subject/graph
load must use a fixed number of queries independent of depth; collection/domain rows may be fetched
in bounded batches. Assert that nested calls reuse the snapshot. Preserve live permission changes
between operations, and test that unchanged valid graphs produce identical access decisions.

Finish with a call-site scan for `groupChain`, `groupChainErr`, `groupAncestry`, `groupParent`,
`subtreeIDs`, direct `parent_id` traversal, `PrimaryGroupOf`/`DefaultGroupID` error swallowing, and
browser `resolveOU`. No security decision may use an obsolete lenient path; any retained wrapper
must delegate to the shared implementation, be error-aware, and have a documented caller contract.
SQL for historical filtering and UI-only tree presentation is allowed with an explicit ledger entry.

The delivery includes the shared implementation, migrated consumers, approved header migration,
server API/UI, regression/driver/restore evidence, amended ADRs and updated feature template. Rollout
starts with a backup from the previous release and a topology/impact inspection; normal ordered
migrations run on startup under ADR 0035. Legacy root reparenting is a separate explicit operation.
Rollback uses the retained pre-upgrade backup with its previous binary. Follow the current owner
instructions for repository artifacts and preserve the requirement for approval of new schema shapes.

## Reviewable delivery and rollout order

1. Shared graph, identity and typed resolver contracts with malformed-tree and inheritance tests.
2. Atomic OU/account/SSO writers and lifecycle serialization, including rejected/no-op behavior.
3. Target presence migration, collection readers/writers, cleanup and restore tests.
4. All backend consumer migrations, with each ledger row naming its migrated callers and evidence.
5. Authoritative editor, full-draft preview, move impact, topology repair presentation and locale parity.
6. ADR amendments, storage/query findings, reusable feature recipe and final verification results.

These are review boundaries inside one iteration. Update this document with actual commands,
outcomes and unresolved limitations as they are verified; do not infer success from test filenames.
Before rollout, take the previous-release backup, inspect topology, and review changes on a test OU.
Normal ordered startup migrations may add the approved storage; legacy-root moves require a separate
explicit operator action after the impact has been reviewed. Wider rollout follows the completed
SQLite, PostgreSQL, restore, concurrency and frontend gates.
