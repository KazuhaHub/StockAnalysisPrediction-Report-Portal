# ADR 0039 — Forecast verification for readers

**Status: Accepted decision; rollout incomplete.** 2026-10-22. Records the owner's decisions of
2026-10-18/19. Supersedes design D6 and the manager-only statistics rule in
[ADR 0029](0029-prediction-scoring-deferred.md#5-the-read-path-inherits-the-reports-permission-and-fails-closed-after-a-purge).
Complements [ADR 0037](0037-forecast-verification-operations.md),
[ADR 0022](0022-external-user-ou-tenancy.md) and [ADR 0024](0024-report-versions.md).

This is not a deployment record. P26 implements the access server contract; P27/P28 deliver its
reader page and end-to-end access checks. The reading-stage server and presentation changes are
P18/P20 in R2b. Their current state is tracked in the
[implementation log](../forecast-verification-v2-log.md).
The [owner decisions](../forecast-verification-v2-decisions.md) take precedence over older proposals;
the [build document](../forecast-verification-v2-implementation.md#10-the-access-matrix) owns the complete
cross-surface matrix and acceptance fixtures.

## Context

The original statistics page was manager-only. Opening a machine API route would not make it a
reader permission: a persistent query token and a user's short-lived app token both authorize
queries, but neither implies that the user may see a ledger-wide record. Hiding a navigation item
would leave that disclosure intact.

Readers also need two distinct things: a report's predictions, governed by the report's visibility,
and aggregate evidence about predictions, governed by the population the reader is allowed to
inspect. Report grants cannot be used to infer the latter, and an aggregate permission must never
widen the former. External readers add a further boundary: even a permissible aggregate metric
must not reveal the site's production volume or allow custom-range differencing.

## One OU permission, resolved on every request

`user_groups.forecast_scope` is `all`, `own`, `none`, or NULL for inheritance. Resolution follows
the live OU tree, Google Workspace style: the nearest explicit setting wins. An unassigned account
uses Default; a root OU inherits through Default without becoming its child. Default stores `none`;
NULL there still resolves to `none`. A newly created OU inherits and is unavailable unless its
nearest effective setting grants access. Administrators with `PermManage` resolve to `all`.

`own` is rooted at the account's primary OU, not at the ancestor that supplied the setting. Its
population is that OU plus its actual descendants. Thus an unassigned account at Default-own sees
Default and OUs parented under it, not unrelated roots; a child inheriting own is rooted at itself.

External protection is sticky and independent of scope: an account restriction or any restricted
OU on its chain makes it external. A restricted account inside an internal OU keeps that OU's scope
but receives the external protections. An external OU may explicitly receive `all`.

The resolver is pure over one strict account/tree snapshot; Postgres uses repeatable-read
isolation. Failed reads, inactive/missing accounts, unknown values, dangling parents, cycles and
excessive depth deny rather than shortening a chain or guessing. There is no cached permission
decision. Login, account expiry and the forced-enrolment wall remain independent prerequisites;
their earlier refusal can take precedence over the forecast gate.

OU settings distinguish presence: an omitted `forecast_scope` leaves it unchanged; null restores
inheritance (Default normalizes to none); an invalid value refuses before mutation. A changed
value records an exact from/to delta in the existing group-change audit. The admin overview and
hypothetical preview resolve active, non-expired accounts from a consistent snapshot, including
unassigned/root members and admin overrides, without returning account identities or persisting
the hypothetical setting.

`/api/me` exposes `{scope, external}` as a navigation hint, not an authorization capability. An `own` reader
also receives `ou`, the display name of the OU their population is rooted at (their own unit, not the ancestor that
decided the scope), so the record page can say whose reports it covers; nothing is added for the other scopes, and
a failed name read omits the field.
An explicit none suppresses reading-page requests and DOM. An omitted hint from an older server
preserves its legacy reading requests; the response is still authoritative. A stale hint followed
by `403 forecast_unavailable` renders nothing, while other forecast failures retain an inline
retry and never remove the report body.

## Populations and historical attribution

Step `0006` already provides `prediction_sets.attributed_ou`. Attribution is frozen at insert from
the signed owner token. Moving an account or changing its report grants does not move old sets.
Injecting that token for every run is the separate, deployment-gated P39b rollout; this ADR does
not imply it has occurred or backfill unattributed history.

The subtree is live. Re-parenting an OU moves its attributed sets into its new ancestors' own
populations and out of the old ones. Deleting an OU is the sole exception to frozen attribution:
one transaction re-attributes its sets to its parent, or Default for a deleted root, reparents its
children to the same parent, deletes the OU and records the counts in the audit. A root's children
remain roots. No set may retain a reference to the deleted OU.

### Purge ownership evidence (owner-approved 2026-10-22; wired, release held)

`own` must not disclose the site's global purge-month history. The owner approved a separate
`prediction_purge_owners` table, keyed by `(rtype, cohort, attributed_ou)`, with non-null report
type/month and a positive, non-null OU id. Before deleting a month's sets, retention snapshots
their distinct proven OU attributions in the same transaction as the global purge marker and
set deletion. Scoped marker reads join this evidence to the current allowed subtree. Multiple
descendant owners do not multiply a month. All readers keep the global history contract.

OU deletion transfers this evidence to the same parent/Default destination as sets, deduplicating
before removing the old id, in the same transaction as OU removal. If no valid destination exists,
unprovable evidence is removed rather than left attached to an id that might be reused. Purge and
OU-delete transactions must take the shared ownership lock **before their first database read or
set mutation**. Postgres uses a transaction-scoped advisory lock; SQLite reserves the single writer
before reading. Locking only the final evidence write permits a concurrent OU deletion to miss an
uncommitted snapshot and is insufficient.

Both transactions are wired this way. `DeletePredictionsBefore` nominates months from a scan that
runs outside the transaction (the single-connection rule), then begins its transaction, takes the
lock, and verifies each nominated month again from fresh reads before it snapshots the month's
owners, deletes its claims, marks and sets, and writes the marker. `DeleteUserGroup` takes the lock
before it reads the OU, moves the sets, then moves the evidence to the same destination whether or
not any set is left, because retention may already have deleted them all. The two therefore
serialize in either order: whichever commits second reads the other's committed result, so no
evidence can name an OU that is gone and no OU id is free to be reused while evidence still
mentions it. The acceptance tests hold one transaction at a blocking row while the other queues on
the lock, in both orders, and show that each transaction reads only after it holds the lock.

Existing global markers without ownership evidence remain visible to all but are hidden from own;
there is no guessed historical backfill. This new table requires an explicit operator upgrade for
existing supported databases, not automatic DDL at server startup. The upgrade step must deliver,
together with the table, fresh initialization, startup shape verification, membership in the
dump/restore table set (so a restore replaces the evidence with the dump's rows instead of leaving
rows keyed by OU ids the restored tree may reuse) and rollback instructions. The reader routes read
the table only where it exists and fail closed where it does not. Because the step is explicit, a
build that has it refuses an un-upgraded database at startup, so that fail-closed path is reached
only by a table damaged while the portal runs. No released migration is edited and the frozen
baseline stays unchanged. Approval of the schema is not authorization to change a production
database.

The two reader routes read purge history through one view. The global markers still remove sets
from every computation and decide cache identity, so no partial data from a month that retention
has emptied is ever shown to anyone. What a population is *told* comes from the evidence: for an
own reader, the range extension, the custom exclusion list, the purged groups and cells and the
drawer's purged state use only the months whose evidence names the current subtree. All readers,
the administrator aliases and external-all readers keep the global history unchanged.

A month purged globally but not disclosed to an own reader must be indistinguishable from a month
in which that reader simply holds no sets. The monthly record lists it as an ordinary group
computed over the sets the site-wide filter left, which is none, and its drawer answers as any
empty month in range does. Leaving the month out, or answering `label_not_found` for it, would
turn a gap in an otherwise complete calendar into the very disclosure the owner refused. Two things
keep the month honest. Its cells carry a cache identity derived from the marker, so a cell
memoised before the purge (by another process this one has not heard from) never answers; and sets
inside an undisclosed purged month do not count when the first month of an own calendar is chosen,
so a set written after the purge cannot start the calendar and leave an empty leading month.
Months before the reader's own first month are not found, exactly as any month before it.

If the evidence table cannot be read, including before the explicit upgrade has created it, an own
read answers `503 forecast_purge_history_unavailable` and never falls back to the global markers;
reads that do not need evidence never touch the table. From the moment of the upgrade retention
records and OU deletion carries the evidence, so the table is complete for everything purged after
that point; everything purged before it stays global history.

The implementation assigns this evidence to step `0009`. The migration list is in merge order, not id
order: it is appended after every step that reached main before it (the announcement step `0010` today,
and the notification `0007` and extension `0008` steps, which merge first), so `0009` is always the last
entry and a backup written by the release before it is a prefix of the list. The unapproved Tier 2
provenance proposal takes an unused id when it is approved (`0011` at the time of writing), subject to a
fresh identifier check, and is appended after this step. Existing supported databases use the offline
`schema-upgrade` command (inspection by default, mutation only with `--apply`). Startup refuses an
unrecorded explicit step before applying even earlier automatic steps. A fresh database creates the
step with the rest of its schema. Every startup checks the evidence columns, exact primary key,
non-null types and positive-owner constraint; a recorded but damaged step is refused, not repaired.
The startup refusal names the [upgrade runbook](../forecast-purge-ownership-upgrade.md), which also
covers running the command in a container (the entrypoint ignores arguments), taking the pre-upgrade
backup with the previous release (the new binary refuses an un-upgraded database for every command
that opens the store), stopping a refused container that a restart policy keeps restarting, and the
same `--apply` covering the notification and extension steps when they ship in the same release.
Retention, OU deletion and the reader routes now all use the evidence. The own-history release hold
remains until the upgrade has run on the target databases and the independent review of the wiring
is recorded.

Membership is computed **after population filtering**. D4's earliest accepted set per symbol and
group is chosen inside that population, so another OU's earlier report cannot displace an own
member or change its numbers. The cohort benchmark remains ledger-wide. Every population keeps
the same maturity gates, including n >= 30; small own populations correctly publish no metrics.

Own cache identities include a hash of the sorted subtree IDs, and own permutation seeds include
that population identity. Re-parenting therefore cannot reuse a different population's result.
The all-population cache key and seed stay unchanged. These are access/population rules, not a
change to the statistical method or a reason to regenerate golden results.

## Reader record and credential boundaries

The record is a core SPA page at `/forecast`, not an iframe app and not an operations console.
It uses cookie-session routes `GET /api/forecast/groups` and `/api/forecast/groups/members`.
Both require a fresh all/own decision after enrolment. `/manage/forecast` becomes evaluator
operations; `/api/admin/forecast/*` remains manager-only. The existing groups/members aliases
retain admin diagnostics until the separately gated P29 removal.

| Subject | Record and member drawer | Prediction reads | Payload / membership / diagnostics |
|---|---|---|---|
| Admin | All population, monthly and custom; admin aliases retain unrecorded | Full cookie view, stage ignored | Payload available on request; membership and diagnostics retained |
| Internal all | All population, monthly and custom; coverage, never unrecorded; orphaned members retained | Accepted sets of any readable report; internal orphan history retained | Collapsed payload; membership retained; rejected sets and errors omitted |
| Internal own | Own subtree, monthly and custom; own coverage, never unrecorded | Any readable report, not merely own-attributed reports; internal orphan history retained | Collapsed payload; no membership; rejected sets and errors omitted |
| External all, including restricted account in internal all | Monthly only; no volume; drawer lists readable reports only | Accepted, readable, report-backed sets only | No payload, membership, errors or orphaned rows |
| External own | Own subtree, monthly only; own counts allowed; drawer lists readable reports only | As external all | As external all |
| Any non-admin at none | `403 forecast_unavailable`; no navigation item | Same refusal; no card, chip, timeline mark or overlay | No forecast data |
| Enrolment-held session | `403 enrolment_required` before business data | Same independent wall, including v1 cookie reads | No business data; a missing admin permission may refuse earlier |
| Persistent query token | `401` on session and admin routes | Unscoped v1 machine contract | Payload with `include=payload`; membership, non-accepted states, errors and orphan history retained |
| App-bridge token | `401` on record, drawer and admin routes | `403 forecast_unavailable` on both v1 prediction reads, whoever minted it; the ledger is not read | No forecast data (fail closed until the shared token binding lands) |
| Anonymous | `401` | `401` | None |

The SPA uses `/api/report/{id}/predictions` and `/api/predictions`. The v1 report and symbol
prediction routes also admit a valid cookie through the independent enrolment wall; their
cookie shape follows the same scope rules. Route classification as bearer is not a token-only
claim. An unreadable report answers 404, not a list that confirms its existence.

App-bridge tokens get no forecast reads. The in-memory app-token registry records scopes and
expiry but no user, so a bridge token carries no account whose forecast scope it could stand for,
and the portal does not invent one here. The v1 prediction reads therefore resolve their
credential in a fixed order: a cookie session (its own decision and read scope), then a persistent
query token (the unchanged machine contract), and then nothing else. Any other Bearer that the v1
read gate admitted is a bridge token and is refused with `403 forecast_unavailable` before the
ledger is touched. This is deliberately stricter than the behaviour before this ADR, where a
bridge query token received the unscoped machine shape.

The refusal holds until the shared token binding from the notification work lands, which records
the minting identity in the existing app-token registry. A follow-up then re-enables scoped bridge
reads on that binding: the minting user rechecked on every read (account incarnation and session
revision, active state, expiry, current scope, enrolment), accepted report-backed sets only, and no
payload, membership, errors or orphans even for an admin-minted bridge. Until then no parallel
forecast identity registry or broader app capability is introduced. The access decision itself is
unchanged: it is taken only from a cookie session's strict account and tree snapshot.

An account whose OU chain is broken (a dangling or cyclic parent) resolves to none. A dangling
parent also leaves the enrolment policy unreadable, which every other route reports as
`503 enrolment_policy_unavailable`; on the forecast reads the definite answer, `403
forecast_unavailable`, is returned instead, so the subject sees what the table promises a none
account. A cyclic chain does not trip the wall: the policy walks past a cycle, so such an account
reaches the forecast gate and is refused there with the same `403 forecast_unavailable`. A
transient store error is not a structural reason and keeps the retryable 503.

## External protection is response shaping, not CSS

External viewers cannot use custom ranges on either reader route: the response is
`403 forecast_custom_not_allowed`. This prevents differencing over selected intervals.

For external all, every group omits coverage and every reader response omits unrecorded. A
non-mature cell contains state only. A mature cell retains its metrics and `min_n_met: true`, not
n, total, resolved, refusal/corporate-action counts or metric/slice denominators. The UI renders
the threshold assurance without the sample size: once per cell, as its own badge, because it is a
fact about the cell's member count. A metric's own denominator (hit, IC, band, calibration) is a
smaller subset with no minimum of its own, so the badge is never drawn in a metric's sample slot.
Explain/summary fields added later must not reintroduce that volume. CSV has no n, coverage or
count columns for these readers; its header says the flag belongs to the cell.

Every groups response also states the population it covers (`scope`, and for `own` the name of the
unit the population is rooted at as `scope_ou`), taken from the same decision that selected the
cohort. The record page and its export label results from that statement, not from the `/api/me`
hint, which is read once per session and can be stale.

The external drawer omits site-wide totals and per-status counts, filters out unreadable reports
and purged sets **before cursor pagination**, and uses no global total. Readable rows may still be
listed; suppressing every row would not satisfy the positive access contract. External own keeps
its population's coverage and sample counts because those are its own volume. No non-admin gets
unrecorded, regardless of scope.

Redaction copies shared results rather than modifying cached cells. Accepted/report-backed read
filters run before SQL LIMIT, so newer rejected, orphaned or unreadable sets cannot crowd out a
readable accepted result. Attribution metadata is never a reader response field.

## Computation is governed before readers can trigger it

Only an uncached mature-cell computation takes a slot. Official and exploratory work use separate
pools, so exploratory saturation cannot starve official records. Identical population/range/type/
checkpoint requests share detached work; cancelling the leading HTTP request does not cancel its
followers. Cache hits and maturity checks do not wait for scoring capacity. Purge generations are
captured before reads, so work started against a purged population cannot repopulate the cache.

| Existing settings UI key | Range | Default |
|---|---|---|
| `forecast_slots_official` | 1–8 | 2 |
| `forecast_slots_explore` | 0–8 | 2 |
| `forecast_wait_sec` | 1–60 | 10 |
| `forecast_custom_per_min` | 0–600 | 20 |
| `forecast_members_per_min` | 0–600 | 60 |

Custom mode also permits only one distinct query in flight per user; zero frequency limits mean
unlimited frequency, not removal of that guard. Wait exhaustion returns `503 forecast_busy` with
Retry-After, never a partial record. Per-user exhaustion returns `429 forecast_rate_limited`.
Settings are bounded, stored in existing meta settings and audited; none belongs in config.yaml.

Weekly non-admin record adoption is an indicator, not an identity ledger. A random per-UTC-ISO-week
salt and HMAC set stay in memory; hourly persistence writes only the integer baseline plus set
size under `forecast_viewers_<week>`. No salt, username or digest is persisted. Repeated flushes
are idempotent; week rollover discards private material. Restart may count the same viewer again;
abrupt termination may lose unflushed observations. There is no adoption target yet: the owner
sets one after observing a month of R2a.

The reader surfaces are observable without recording who read. Process counters (statistics
requests, cache hits, flights joined, scored cells, busy, rate-limited, drawer opens and refusals by
reason code) are flushed hourly into one `meta` row per UTC day, `forecast_signals_<date>`: a flat
JSON object of integers over a closed list of names, so a name, token or hash cannot reach it. A
flush adds only the events counted since the last successful one, so repeating it changes nothing and
a failed one is retried without loss or double counting; an unreadable row is left untouched. Rows
older than 14 days are deleted. A statistics request slower than three seconds logs one line with the
scope and population kinds and its cell, cache, flight and wait figures, and a refusal logs its reason
code at most once a minute per reason; neither line carries an account, an OU id, a path or a query.
The operations runbook interprets them.

## Reading stages, editions and presentation

R2b adds audited `forecast_reader` = off/card/full, default card; an unknown value is off. Admins
see full regardless. Scope is an independent prerequisite: full never gives a none OU any reading
surface. Card and overlay require card/full; report chips and stock/run timeline marks require
full. The record's permission is not this reading-stage switch. Before that switch ships, the
reading footprint is card. Failures in summaries leave reports readable, and prefetch omits the
heavy summary while retaining correctly scoped timeline flags.

Each edition shows its own sets. An edition without a set says so and may link only to a readable
sibling edition's set. D4 remains across editions; display selection is separate from membership:
prefer the latest accepted set matching the current body, otherwise the latest accepted set with
a body-changed warning. A sibling member reads "counted on edition X" for internal all readers
and admins, never as a duplicate and never as a membership disclosure to own/external readers.
Edition switches must clear old data before rendering the next edition's card or overlay.

Non-mature group cells and their member drawers publish no outcome metrics; this does not hide
an individual report's already-settled checkpoint. Waiting dates are lower bounds, labelled
"on or after", or "expected" when backed by the projection calendar; projection never drives
settlement. Forecast surfaces carry the approved English disclaimer: "For research verification
only. Not investment advice." Chinese UI strings use the approved locale entry; Traditional
Chinese is generated through OpenCC and flagged for owner-arranged human review, not held out as
reviewed translation.

The owner enables full only after P18/P20 are deployed, a week of admin full-stage observation,
and `/api/stock` p95 within 800 ms for real and prefetch traffic. A reading incident rolls back to
card or off through the audited setting, without hiding the report itself.

## Alternatives and consequences

- A boolean forecast permission cannot express own populations or the owner's external access
  decision. A second per-OU stage would duplicate the access model and was not selected.
- Filtering after membership would let another tenant change own results. Hiding counts in the
  browser would leave them available on the wire. Neither is a permission boundary.
- Treating an app query token as a machine token would bypass none and expose diagnostics. A bridge
  token records no minting user today, so it is refused outright; once the shared token binding
  lands, the minting user must be part of prediction authorization when scoped bridge reads return.
- Recomputing attribution from the current user would rewrite history. The live tree determines
  subtree inclusion, while stored attribution identifies the OU; deletion is the explicit exception.

The cost is isolated population caches, strict read snapshots, bounded computation and a larger
access matrix. The benefit is one permission model shared by the page, read routes, preview and
navigation, with executable negative **and** positive controls on SQLite and Postgres. Fast access
rollback sets OU scopes to none through the audited editor; stage rollback is separate. Neither
changes the existing ledger, migration baseline or evaluator enablement gates.
