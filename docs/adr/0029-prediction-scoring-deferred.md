# ADR 0029 — Forecast verification: an append-only prediction ledger, and what may be counted

**Status: Accepted.** 2026-10-17, subject to the owner's sign-off on the open decisions (§12) and the
table shape (§7). Replaces the deferred version of this ADR. Amends
[ADR 0028](0028-live-quotes.md) (§10). Depends on [ADR 0035](0035-migrations-after-a-frozen-baseline.md)
(the migration step), [ADR 0017](0017-storage-cleanup.md) (the storage console),
[ADR 0024](0024-report-versions.md) (read permission) and [ADR 0022](0022-external-user-ou-tenancy.md)
(the OU tree).

The decision record here is the *what and why*. The *how* — files, signatures, DDL, algorithms, PR
order and the acceptance tests — is in
[forecast-verification-implementation.md](../forecast-verification-implementation.md), cited below as
"plan §n". The reasoning behind each rule, at length, is in
[forecast-verification-design.md](../forecast-verification-design.md) (draft 4), cited as "design §n".
Where these disagree, stop and ask; do not pick one.

> **On the file name.** This file is still `0029-prediction-scoring-deferred.md`. The name is now
> wrong, and it is kept deliberately: a shipped release note links to it by path
> (`docs/releases/0.x/v0.4.45.md:382`), and there is no ADR index to redirect. Renaming it is a
> separate, mechanical decision.

## Context

The owner asked for "real vs predicted" as a portal feature: a monthly view that quantifies how the
reports performed, and a per-report view of what one report said and what then happened.

The first version of this ADR deferred that, and its reasoning was not wrong. Its central finding
stands and is worth restating, because everything below is built on top of it rather than around it:
**`SetTracking` (`store.go:1585`) deletes and re-inserts a report's tracking rows on every ingest**
(`store.go:1608` is the DELETE, guarded by the carry-over of `status` and `review_point`), so any
machine-computed outcome stored on a tracking row is erased by the next nightly run. Whatever scores
a prediction cannot live in `tracking_items`.

Two things changed since the deferral, and only two.

**The producer can now emit structure without an LLM in the loop.** 投资决策建议 comes from 1-6-4
投资决策 V3.16.x, where `v3calc`, `v3risk` and `v3decisionadapt` compute every number this feature
would score. The values are selectable from a code node's output; none of them has to be parsed out of
prose. This was the blocker the deferred ADR named as the hard stop ("the Dify workflow must agree to
emit structured claims, and that is a conversation, not a task"), and it has been answered.

**The owner has asked for the number, having seen what it would measure.** A one-off comparison of
2026-09/10 reports against real prices found 投资决策建议's valuation ranking carried signal
(rank IC ≈ 0.36) while 重组综合决策's relative calls were inverted. That is the question this feature
exists to answer on a schedule, and it is a question the portal could not answer at all.

### The name

The feature is **预测验证** in the UI and *Forecast verification* in English UI copy.

- Code identifiers use `forecast` for the feature (routes, i18n keys, Go handlers) and `prediction`
  for the stored ledger (`prediction_*` tables, the `predictions` ingest field). The ledger stores
  predictions; the feature verifies forecasts.
- **No** 回测 (a backtest simulates on history; this tracks live calls forward), **no** 复盘 (taken by
  `nav.review`), **no** 跟踪 (the tracking items), **no** 战绩 (reads as a score of people).

## Decision

### 1. Scope: one report type in v1

| Report type | Producer | In v1 | Why |
|---|---|---|---|
| 投资决策建议 | 1-6-4 投资决策 V3.16.x | **Yes** | `v3calc` / `v3risk` compute every scored number in code. |
| 综合决策 | 3-5 | No (D7) | Its values come from an LLM agent, and its claims are event probabilities. |
| 1-x sub-reports | 1-x | No | They feed 1-6-4, so scoring them would count one view twice. |

The covered set is a Go constant (`predictionRtypes = {"投资决策建议"}`). Coverage (§8.1 of design) is
defined over it, so widening it is a code change and a conscious one.

**Prerequisite:** re-pull the Dify export and confirm the source mapping against the live 1-6-4
graph. The local export is from 2026-09-17/18, maps prod `15249be5` to the V2 file, and has no node
that computes the 理想买入价 / 止损价 recent reports print. If a contract field turns out to be written
by an LLM rather than computed by a code node, it leaves the contract.

### 2. The ledger is a set of immutable rows, and each ingest writes one

Dify emits a `predictions` object at ingest. The portal classifies it with a pure function
(`classifyPredictions`, plan §3.2), stores one `prediction_sets` row for **every covered ingest** in
any outcome, and stores its claims and pending marks alongside.

Every case writes a row, including the failures:

| Case | Report | `state` | Counted in metrics |
|---|---|---|---|
| rtype ∉ `predictionRtypes`, no `predictions` | stored | no row | — |
| rtype ∈ `predictionRtypes`, no `predictions` | stored | `absent` | no, counted as uncovered |
| `predictions` invalid | stored | `rejected` + per-rule `errors` | no, counted as uncovered |
| valid, `information_cutoff` more than 72 h before receipt | stored | `backtest` | no, listed apart |
| valid | stored | `accepted` | yes, subject to §8 |

This is what makes coverage computable from the ledger alone, and it is why coverage survives a report
purge: the row that says "this ingest was rejected" does not depend on the report still existing.

**A rejected set also writes an audit row and fires a webhook** (`predictions.rejected`), so a broken
projection reaches a person instead of sitting silently in a coverage number.

**The report is never refused because of `predictions`.** The HTTP status is 200 in every case above
and carries `predictions_status`, including `store_failed` when the ledger insert fails twice — not
5xx, because Dify would retry three times and mark failed a run whose report already exists.

### 3. Nothing is extracted from prose; the contract is projected from code

The projection node's **only arithmetic is rounding** — `Decimal(repr(x))`, quantized half-even (plan
§5.3). Everything else is selection from `v3decisionadapt.result`, `v3calc.result` and `v3risk.result`.

The contract is strict, because it is new and there is no legacy producer to stay compatible with:
a JSON object (not a string), closed enums, prices as strings of at most two decimals converted
exactly to integer fen, probabilities as strings in `[0,1]` of at most four decimals converted to
basis points, scenarios summing to exactly 10 000 bp. Prices are **integer fen**, never `REAL`: the
schema has zero `REAL`/`DOUBLE`/`FLOAT`/`NUMERIC` columns today and this store is dual-driver, where
`REAL` is an 8-byte double on SQLite and a 4-byte `float4` on Postgres, so a boundary comparison could
resolve differently per driver in the one table whose purpose is being citable.

`stop_loss` is a **reserved** kind: refused until a code node computes it.

### 4. Time is server receipt in Asia/Shanghai, and nothing caller-supplied moves it

Every civil date in this feature is in **`Asia/Shanghai`** (`quote.go:260`), not the panel timezone:
that falls back to `time.Local` (`apiv1.go:88-97`), the release image runs `TZ=UTC`, and an admin can
change it later, which would re-key cohorts. The zone name is stored on each set.

| Term | Definition |
|---|---|
| `made_at` | Server receipt instant (UTC RFC3339). |
| `made_on` | Shanghai civil date of `made_at`. |
| `cohort` | Shanghai `YYYY-MM` of `made_at`. |
| Session calendar | The bars of the set's benchmark index. The code has no trading calendar (`quote_cache.go:58-66`), and index bars exist on every session, including days a stock is halted. |
| Completed session | A session whose bar may be used only after 15:30 Shanghai on that date. |
| Start bar `S0` | The first session whose close is after `made_at`. Received before 15:00 on session T → `S0` = T; after 15:00, or on a non-session day → the next session. |
| Checkpoints | `d5`, `d20`, `d60`, `d120` = the 5th, 20th, … index session after `S0`. `h12` = the first index session on or after `made_on` + 12 months, standard-horizon sets only. |

`made_at` is stamped from receipt, never from the payload. `validReportDate` (`apiv1.go:28`) accepts
any well-formed calendar date and `ingestInstant` (`apiv1.go:69`) honours a client-supplied RFC3339
`time` verbatim — both correct for what they do, and both would let any holder of an ingest token post
a report dated last quarter whose horizon has already passed. A backdated report date changes neither
the cohort nor `S0`.

**Nothing the model could have seen when it priced the stock counts as forecast.** Every return and
every fill starts at `S0`'s close, not at the report's own baseline.

### 5. The read path inherits the report's permission, and fails closed after a purge

- **While the report exists**, a set is readable exactly when its report is: join to `reports` and
  apply the report read filter, the `QueryTracking` pattern (`store.go:1633`).
  - `owner_group` is **not** consulted. ADR 0024 retired it as a read key
    (`report_owner.go:88-96`: `report_viewers` is the only table consulted for ownership).
  - 1-6-4 sends no `owner_token`, so the column would be NULL anyway.
- **After a purge** (`report_id` NULL), a set is readable only by unrestricted users. A restricted
  reader cannot prove access to a report that no longer exists. **Fail-closed.**
- **`/api/v1` is token-only** (`routes.go:33`; enforced by
  `TestTheMachineSurfaceIsNotASessionClass`, `routes_test.go:123`), so the SPA reads a report's sets
  through a **session** route, `GET /api/report/{id}/predictions`, under the same rules.
- **The group statistics route is `/api/admin/forecast/groups`, gated on `PermManage` — not
  `/api/v1`.** v1 authorizes by token scope alone (`tokenOK`, `server.go:1002`), and the app bridge
  mints `query` tokens for any unrestricted user (`apiAppToken`, `apps_api.go:121`), so a v1 route
  cannot be limited to managers. This is why the monthly page is a **core SPA page** rather than an
  app.

### 6. The evaluator ships disabled

A `geoUpdater`-shaped loop (`geo_update.go:501`): one-minute tick, single-flight, due work derived
from `next_attempt_at` on every pass so nothing depends on in-memory state. It is **off by default**
(`forecast_auto = "0"`) and its reader, admin writer and UI ship in the same stage, the way
`setGeoAuto` does (`geo_update.go:539`).

The precedent is `autoLoop` (`geo_update.go:503`), not `scheduleLoop`: a loop that calls a **third
party of its own choosing, on every install, whether the operator wanted it or not** carries a switch.
A sixth always-on loop egressing to Chinese market-data vendors by default — including on installs
where those hosts are unreachable — would invert that.

The evaluator calls `fetchTencentRange` (`quote.go:1570`) **directly**, under `s.quotes.acquire`
(`quote_cache.go:905`), and **never through `fetchUnder` and never through the cache**: `fetchUnder`
walks the operator's source chain, which breaks D9, and it serves cached bodies, so a body fetched at
14:57 could be served at 15:31 and freeze an intraday price. A bar may resolve a mark only once its
session has **completed**.

Refusals are a recorded outcome with a retry, not a gap: `no_bar`, `stale_bar`, `unverifiable`, each
with `attempts` and `next_attempt_at`. A refusal that is still refused 10 sessions past due becomes
`refused_final` and is never retried again — which is what makes a mature cell's inputs fixed (§8).

### 7. The tables

One migration step in `migrate_steps.go`, declaring three tables and their indices, never edited after
release. Nothing is added to `baseSchemaStmts()`. **This is the shape the owner signs off on, and PR 1
does not start until they have.**

```go
func migration0004() migration {
    return migration{
        id:      "0004",
        tables:  []string{"prediction_sets", "prediction_claims", "prediction_marks"},
        indices: []string{"idx_pred_sets_idem", "idx_pred_sets_group", "idx_pred_sets_symbol",
            "idx_pred_claims_key", "idx_pred_marks_cp", "idx_pred_marks_due"},
        restore: restoreRule{olderDumpOK: true}, // new and empty: a pre-step dump restores cleanly
        up:      func(e migExec) error { /* the CREATE statements below, via e.exec */ },
    }
}
```

**The id is `0004`, not the plan's `0002`.** `migration0002` (`users.last_used_version`,
`migrate_steps.go:251`) and `migration0003` (`user_sessions` / `login_history`,
`migrate_steps.go:183`) already exist on `main`. The step is appended to `migrations()`
(`migrate_steps.go:167`), which is append-only, so this is a renumber and not a choice — flagged for
the owner because the implementation plan names it `0002` in six places (plan §2.1 and two of its
test names).

```sql
CREATE TABLE IF NOT EXISTS prediction_sets(
  id {pkAuto},
  report_id BIGINT,                         -- NULL after the report is purged
  symbol TEXT NOT NULL DEFAULT '',
  rtype TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  report_date TEXT NOT NULL DEFAULT '',     -- the report's own (caller) date, display only
  made_at TEXT NOT NULL,                    -- RFC3339 UTC, server receipt
  made_on TEXT NOT NULL,                    -- YYYY-MM-DD, Asia/Shanghai
  cohort TEXT NOT NULL,                     -- YYYY-MM, Asia/Shanghai
  zone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
  state TEXT NOT NULL,                      -- absent|rejected|backtest|accepted
  errors TEXT NOT NULL DEFAULT '',          -- JSON array of {reason, path, detail}
  payload TEXT NOT NULL DEFAULT '',
  payload_truncated INTEGER NOT NULL DEFAULT 0,
  payload_sha256 TEXT NOT NULL DEFAULT '',
  body_sha256 TEXT NOT NULL DEFAULT '',
  emitter_app TEXT NOT NULL DEFAULT '', emitter_version TEXT NOT NULL DEFAULT '',
  calculator TEXT NOT NULL DEFAULT '', schema_name TEXT NOT NULL DEFAULT '',
  policy_version TEXT NOT NULL DEFAULT '', probability_method TEXT NOT NULL DEFAULT '',
  probability_transform TEXT NOT NULL DEFAULT '',
  decision_status TEXT NOT NULL DEFAULT '', decision_quality TEXT NOT NULL DEFAULT '',
  information_cutoff TEXT NOT NULL DEFAULT '',
  horizon_end TEXT NOT NULL DEFAULT '', horizon_std INTEGER NOT NULL DEFAULT 0,
  emitted_base_fen BIGINT, emitted_base_as_of TEXT NOT NULL DEFAULT '', emitted_base_source TEXT NOT NULL DEFAULT '',
  s0_date TEXT NOT NULL DEFAULT '',         -- calendar S0 (index session)
  base_date TEXT NOT NULL DEFAULT '',       -- the stock's first traded bar on or after S0
  s0_fen BIGINT,                            -- the stock's unadjusted close on base_date
  base_check TEXT NOT NULL DEFAULT 'pending',
  bench_code TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pred_sets_idem   ON prediction_sets(report_id, payload_sha256);
CREATE INDEX        IF NOT EXISTS idx_pred_sets_group  ON prediction_sets(rtype, made_on, state);
CREATE INDEX        IF NOT EXISTS idx_pred_sets_symbol ON prediction_sets(symbol, made_on);

CREATE TABLE IF NOT EXISTS prediction_claims(
  id {pkAuto},
  set_id BIGINT NOT NULL,
  claim_key TEXT NOT NULL,
  kind TEXT NOT NULL,
  stance TEXT NOT NULL DEFAULT '', label TEXT NOT NULL DEFAULT '',
  price_fen BIGINT, prob_bp INTEGER, weight_bp INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pred_claims_key ON prediction_claims(set_id, claim_key);

CREATE TABLE IF NOT EXISTS prediction_marks(
  id {pkAuto},
  set_id BIGINT NOT NULL,
  checkpoint TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  refusal TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL DEFAULT '',
  session_date TEXT NOT NULL DEFAULT '', bar_date TEXT NOT NULL DEFAULT '',
  halted INTEGER NOT NULL DEFAULT 0, corp_action INTEGER NOT NULL DEFAULT 0,
  close_fen BIGINT, low_fen BIGINT, high_fen BIGINT,
  ret_bp INTEGER, bench_ret_bp INTEGER, bench_code TEXT NOT NULL DEFAULT '',
  resolved_at TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pred_marks_cp  ON prediction_marks(set_id, checkpoint);
CREATE INDEX        IF NOT EXISTS idx_pred_marks_due ON prediction_marks(status, next_attempt_at);
```

`{pkAuto}` is the store's existing dialect helper (`store.go:160`: `BIGINT GENERATED ALWAYS AS
IDENTITY PRIMARY KEY` on Postgres, `INTEGER PRIMARY KEY AUTOINCREMENT` on SQLite). Id columns that
reference another row are `BIGINT`, matching `reports.id` on Postgres. `TEXT`/`INTEGER`/`BIGINT` are
written literally, as `store.go:328` does. The `CREATE` statements must be `IF NOT EXISTS`, the same
way the base schema writes them.

What each part is for:

**`prediction_sets`** — one row per covered ingest, insert-only.
- Identity/display copied from the report (`symbol`, `rtype`, `version`, `title`, `report_date`) so the
  row stays readable after a purge. **Never used for access** (§5).
- Time: `made_at`, `made_on`, `cohort`, `zone` (§4).
- Outcome: `state`, `errors`, `payload` (verbatim, capped at 64 KiB) with `payload_truncated`, and the
  two hashes. `payload_sha256` is of the raw bytes; `body_sha256` is of the `body_md` that arrived with
  the set.
- Contract echoes, for the record and for the group statistics.
- Evaluator-filled, and nothing else: `s0_date`, `base_date`, `s0_fen`, `base_check`, plus `bench_code`
  when it is empty. Written once, guarded by `WHERE base_check IN ('pending','refused')`.
- `bench_code` is chosen at insert (D1, §12).
- Unique index `(report_id, payload_sha256)` is the idempotency key for retries. On both SQLite and
  Postgres a NULL `report_id` never collides, so purged sets never collide either.
- Group queries filter on `rtype` equality plus a `made_on` range, so the index leads with `rtype`.
  `cohort` is kept for display and export.

The column is `schema_name`, not `schema`, for clarity beside "migration schema". (`schema` is
non-reserved on Postgres; the rename is not a dialect requirement.)

**`prediction_claims`** — one row per claim of an `accepted` or `backtest` set, unique on
`(set_id, claim_key)`, insert-only. `kind`, `stance` and `label` are enums; `stance` and `label` are
empty unless the kind uses them.

**`prediction_marks`** — one row per `accepted` set × checkpoint, created `pending` with the set.
`status` runs `pending` → `refused` → `resolved`, or `refused_final` after 10 sessions past due; both
`resolved` and `refused_final` are terminal (§6). `halted` is 1 when `bar_date < session_date`;
`corp_action` is 1 when an ex-event falls in the window (§10).

**No verdict column.** Hit/miss is computed on read by one Go function.

**No foreign keys**, consistent with the rest of the schema. Integrity is the write path's job and the
cascade's (§8).

**Why a new table rather than columns on an existing one.** The house rule is to prefer a column and
to need an ADR-worthy reason for a table; this is one. `tracking_items` is rewritten wholesale on every
ingest (§Context), so it cannot hold an immutable record; `reports` is upserted on identity, so it
cannot hold more than one set per report; and a set is a *thing with claims and marks hanging off it*,
not a property of either.

### 8. Purge nulls the link; the ledger outlives the report

`DeleteReport` (`store.go:1891`) and `deleteReportChunk` (`cleanup_store.go:217`) run
`UPDATE prediction_sets SET report_id = NULL` in the same transaction that deletes the report row,
in the same commit that adds the tables. Sets, claims and marks survive, and a later report reusing an
id inherits nothing.

ADR 0029's deferred version said the opposite — that the ledger's lifetime is the reports' retention
floor. **That is overturned**, because the reports retention floor is 365 days
(`minReportsRetentionDays`, ADR 0017) and a set's `h12` becomes resolvable twelve months after it was
made: the floor would purge every set on the day its long-horizon mark became answerable.

The nulled-not-deleted shape is also what makes the id-reuse argument true rather than merely stated.
`DeleteReport`'s comment already explains that ids are reassigned and a later report inherits orphan
rows; `deleteReportChunk`'s doc comment does not repeat it, so **PR 1 adds it there**, naming
`prediction_sets` and saying it is nulled rather than deleted and why.

#### The ledger does have a retention period, and the first draft of this section was wrong

An earlier version of this ADR ended §8 by saying the ledger had **no cleanup target in v1** and the
storage console listed it with `eligible = 0`. That is overturned, and the reasoning that produced it
is worth recording because it looked sound.

What the argument above actually establishes is that **the ledger must not inherit the report's
lifetime** — a set outlives its report, which is why the link is nulled rather than cascaded. The
first draft extended that into "the ledger therefore cannot have a retention period at all", and the
extension does not follow. These are different claims:

- *A set must not be deleted on its report's schedule.* True, and it is what the nulling is for.
- *A set must never be deleted.* Not the same statement, and not one anything here defends.

The owner asked for the second to be dropped, and the shape of the target is what makes it safe. It is
the sixth entry in ADR 0017's cleanup console, and it inherits that ADR's rules: **it ships disabled**,
it is gated on `PermManage`, and it is a **danger-zone** target in the UI requiring a live-count
confirmation.

**The predicate does the protecting, not the day count.** A month is eligible only when every one of
these holds for every set in it:

| Condition | What it rules out |
|---|---|
| `report_id IS NULL` | Every set whose report still exists, however old it is. This is why the target can never touch a live report. |
| No mark that is `pending` or `refused` | Every set whose answer is still coming. An unsettled set is never eligible, however long it has been unsettled. |
| `settled < cutoff` | Where `settled` is the date the set became final |

The floor is **180 days** (`minPredictionsRetentionDays`) against the reports target's 365, and that
inversion is deliberate rather than a loosening: **the safety of this target is structural, not
numeric.** The first two conditions are properties of the ledger's shape, and no number an operator
types can weaken them. What the floor actually defends is the one thing the predicate cannot — an
operator setting the days to 0 and emptying the ledger in a single pass. The reports floor has to be
long because a report's age *is* its protection; this target's age is not.

**The unit of deletion is a whole month of one report type, not a set.** This is the load-bearing
detail, and it exists because per-set deletion breaks two things:

- **A mature cell's immutability** (§9). Removing some sets of a closed month moves that month's `n`
  and every metric on it, while the page keeps showing the same label. A number that silently changed
  is worse than one that disappeared.
- **Membership (D4).** Membership is "the earliest `accepted` set per `(symbol, group)` decides". Delete
  only that set and the next-earliest rerun is **promoted** to member — exactly the
  selection-by-rerun D4 exists to forbid.

So a month is purged only when every set in it is eligible, and the pass writes a marker
(`prediction_purges`) recording the `(rtype, cohort)` and the date. The monthly page shows a purged
month as **purged on `<date>`** rather than recomputing it, because a purged month that rendered as a
month with no predictions would be a different and far better-looking claim. An exploratory range that
covers a purged month excludes that month and says so.

**No result snapshot.** The numbers disappear as a unit and the page says why. Freezing them first
would need a table of its own, which is a schema-shape decision; the owner's call for v1 is not to have
one.

**Two set shapes had to be given an age.** `absent`, `rejected` and `backtest` sets carry no marks at
all, so "every mark is terminal" is vacuously true and `MAX(resolved_at)` is NULL. Compared against a
cutoff, such rows are either never eligible (growing for ever) or instantly eligible the moment their
report is purged. The age is therefore `substr(COALESCE(MAX(resolved_at), made_on), 1, 10)`: a set's own
age, so purging its report never makes it immediately fair game. The truncation is what makes an
RFC3339 instant comparable with a civil-date cutoff at all. This also fixes the second shape —
`accepted` sets whose every mark ended `refused_final` do carry marks, and §6.3 has the evaluator stamp
`resolved_at` whenever a mark becomes terminal, so those age normally instead of being stuck for ever.

**Two interactions that have to be stated rather than discovered:**

- **This target depends on the reports target.** The predicate requires an already-purged report, so
  with reports cleanup off it can never match anything. The storage page says so in the target's hint;
  a target that silently never fires reads as broken.
- **The mature-cell cache must be invalidated on a purge.** A cached mature cell for a month that a
  pass just removed is stale by definition. The purge writes its marker in the same transaction, so the
  cache is keyed on it and a purged month is served as purged.

**And the reason it is a danger-zone target is not "a report can be re-ingested".** That was this
section's earlier wording and it is wrong twice over: a started run is never re-run (the money
invariant), and the report body is not reproducible from its row. The honest reason is that **a set
records what was claimed at a moment that has passed, and that cannot be rebuilt at all** — which makes
this target harder to undo than the reports target rather than easier.

### 9. What is quantified, and the four gates in front of it

An overview quantifies only **mature checkpoints of closed groups**. A group is a set of Shanghai
receipt dates: a calendar month in the official view, or whatever the exploratory view defines.

**Membership (D4).** Per `(symbol, group)`, the **earliest `accepted` set decides**. If it is `normal`
and its baseline check is `match`, it is the member. If it is degraded or `mismatch`, the symbol is
excluded for that group and a later rerun is counted as `duplicate` and never promoted — so selection
by rerunning is impossible.

**A cell (group × checkpoint) is mature only when all four hold:**

1. the group's last date has passed;
2. every member's mark for that checkpoint is terminal (`resolved` or `refused_final`) — no pending or
   retryable refusal, and no member whose baseline check is still pending;
3. `refused_final` marks are ≤ 5 %;
4. the slice's n ≥ 30.

**Because every input of a mature cell is terminal, a mature cell can never change again.** That is
what makes the monthly view's immutability true by construction rather than by caching — and it is the
property the deferred ADR's objection 5 asked for.

Marks with `corp_action = 1` are excluded from metrics and counted next to them. `halted` marks are
included, scored on the last traded close, so a suspension or a delisting can never quietly hollow out
a cell.

**Metrics** (each with its `n`, its null and a p-value): stance spread, rank IC, directional hit rate,
entry fills, scenario band (`h12`), and `p_profit` calibration (`h12`). P-values use a **label
permutation** null — members' predictions shuffled across members while their realised excess returns
stay fixed — which keeps the cross-sectional market move that a sign-flip test would assume away.
Correction is **Benjamini–Hochberg within each cell**, and the permutation seed excludes the viewing
mode, so a mature cell's `q` is fixed and identical in both modes.

**Two viewing modes, one engine.** The page has tabs **按月** (monthly, the default and the official
record) and **自定义区间** (a day-precise range, grouped by week, month, quarter or all, labelled
exploratory). Exploratory numbers are computed on request and never written to the ledger; every
p-value is marked "not corrected for range selection"; CSV exports carry the mode, the range and the
grouping. The monthly tab takes no parameter that changes its numbers.

### 10. ADR 0028's "never qfq" rule is amended, narrowly

ADR 0028 §3 and `quote.go:986` say **NEVER qfq**, and that rule protects **displayed** prices: a
front-adjusted series is re-based on every corporate action, so a bar read in July is a different
number in December, and comparing a nominal target price against it is a systematic bias against every
upside target.

The ledger cannot ignore corporate actions either — a 10-for-10 bonus issue reads as −50 % — so it
**detects** them from a change in the vendor's adjusted/unadjusted ratio between `S0` and the
checkpoint session. This is a narrow exception, and its bounds are:

- one function, `fetchTencentAdjustRatio`, named for exactly this use, and the only qfq call in the
  codebase;
- **detection only**: its numbers are never stored, never scored and never displayed;
- a flagged mark is resolved and stored, but **excluded from metrics and counted**.

D8 records a preference for doing this from the dividend element that Tencent's unadjusted day rows
already carry on ex-dates (`quote.go:1121`: a day row is six strings *except on an ex-dividend date*,
where a seventh element is an object), falling back to the qfq ratio only for the kinds that element
misses — in which case no amendment to the display rule is needed at all. Whichever way D8 goes, the
`fetchTencentAdjustRatio` bounds above hold.

The amendment is recorded at the point of ADR 0028 where the promise is made, not only here.

### 11. What we still refuse, and why the objections no longer block

The deferred version refused any aggregate number for five reasons. They are answered, not waved away:

| Objection | Answer |
|---|---|
| 1. No verdict domain to count over | Closed enums in Go and in OpenAPI. Metrics are defined over numbers, not over three suggestion strings in a React file. |
| 2. No baseline | Excess over a recorded benchmark, with a label-permutation null and an `n` on every number. There is no bare "accuracy %". |
| 3. Self-selected denominator | The projection is code, not a prompt's choice. Every covered ingest leaves a set row, so coverage — with its reasons — comes from the ledger. |
| 4. Refusals correlate with being wrong | Halts and delistings resolve on the last traded close rather than refusing. Real data refusals are capped at 5 %, and corporate actions are excluded and counted. |
| 5. Horizon vintage | Fixed checkpoints, closed groups only, every member past the checkpoint, and `h12` holds standard horizons only. |

**Still refused, unchanged:**

- **A per-analyst score. Ever.** `reports.author` is `''` for everything a workflow produced, and ADR
  0022 describes the only report→person chain as "fragile, unenforced". The schema cannot substantiate
  a claim about an identifiable person.
- **LLM-extracting claims from report text.** `review_point` is free text and the reviewer would be
  checking claims the analysis never made.
- **A verdict column**, and anything written to `tracking_items`.
- **A bare accuracy percentage** with no null and no `n`.
- **A periodic whole-market poller.** The evaluator fetches per due set, on demand.

### 12. Open decisions (owner sign-off required)

The architecture above is decided. These are choices *within* it, and a PR whose blocking decision is
open does not start.

| Decision | Blocks | Recommended default |
|---|---|---|
| **D1. Benchmark** — one index for all, or chosen by board? | PR 1 (`bench_code` is set at insert) | `sh000852` (CSI 1000) for every set |
| **D2. Checkpoints** — `d5`/`d20`/`d60`/`d120`/`h12`? | PR 1 (enum), PR 5 | as listed |
| **D3. Invalid predictions** — keep the report? | PR 2 | Keep it; write a `rejected` set, an audit row and a webhook |
| **D4. Membership** — earliest set decides, no rerun promotion? | PR 6 | as §9 |
| **D5. Purge** — does the ledger outlive the report? | PR 1 | Yes (§8) |
| **D7. 3-5 重组决策** — in or out of v1? | scope only | Out of v1 |
| **D8. Corporate actions** — dividend element first, qfq fallback? | PR 5 | Yes (§10) |
| **D9. Corroboration** — one vendor, or two? | PR 5 | One vendor (Tencent) in v1, which overturns the deferred ADR's `sources_disagree` |

Two further questions the implementation plan raises and this ADR does not settle:

- whether flipping `forecast_auto` should also write an audit entry. The recommendation is that it
  should, matching other admin settings that write `audit.t.*`.
- the exact label text of any UI string not already named in plan §8.3.

## What this replaces

ADR 0029's deferred version is replaced, not merely extended. Four of its rules are **overturned**, and
the ADR says so explicitly rather than dropping them quietly:

| Deferred rule | Replaced by | Why |
|---|---|---|
| No aggregate number, ever | Cohort metrics under the gates of §9 | The owner asked for quantification, having seen what it measures. §11 answers the five objections. |
| A re-emitted `claim_key` supersedes (`supersedes_id`); withdrawal reconciled across reports | Each ingest is an immutable **set**; the earliest set per cohort member counts (§9) | 1-6-4 issues a fresh dated forecast per run. Nothing is carried across days, so there is nothing to withdraw. A revision stays visible as a later set. |
| Ledger lifetime = report retention floor | The ledger outlives reports (§8) | Otherwise the 365-day floor purges every set on the day its `h12` becomes resolvable. |
| Both vendors must agree exactly (`sources_disagree`) | Single vendor in v1 (D9) | The quote stack is primary-plus-fallback, not corroboration. A refusal that can never fire is worse than none. |

**Kept from the deferred version**, because the research behind them holds:

- claims live outside `tracking_items`;
- server receipt time anchors everything, never a payload-supplied instant;
- prices are integer fen, never `REAL`;
- thresholds are compared against unadjusted prices;
- a refusal is a recorded outcome with a retry, not a gap;
- the evaluator ships disabled, with its reader, admin writer and UI in one stage;
- Beijing codes (`bj`) produce no price claims;
- there is never a per-analyst score;
- nothing is extracted from report prose.

## Consequences

- **The portal acquires a track record for one report type.** What that costs is a strict ingest
  contract the producer must keep emitting, and a ledger that must be right about time.
- **A mature cell is immutable by construction, not by caching.** The monthly view can be cited; the
  exploratory view is labelled wherever it appears, including in its own CSV.
- **The Dify side is a hard dependency with a manual step.** A re-published sub-workflow is not picked
  up by callers until its tool provider is re-synced, and the rollback for the whole feature is
  "set `forecast_auto=0` and unwire the projection node", because the ingest classifier never refuses
  a report.
- **Line numbers in `docs/forecast-verification-*.md` have drifted.** They were written against the
  docs branch, and `main` has advanced 18 commits since. The citations in *this* ADR were re-checked
  against `main`; the design and implementation documents were not, and every mismatch I found is
  listed for the owner in the accompanying report rather than silently reconciled.
- **The migration step's id in the implementation plan is stale.** Plan §2.1 calls it
  `migration0002`, and `main` already has `migration0002` (`users.last_used_version`,
  `migrate_steps.go:251`) and `migration0003` (`user_sessions` / `login_history`,
  `migrate_steps.go:183`). The step must be renumbered to **`0004`** and appended to `migrations()`
  (`migrate_steps.go:167`). This is a factual collision, not a design choice, and PR 1 cannot start
  until the owner confirms the number.

## Alternatives considered

- **Mirror the outcome into `tracking_items.status`.** Rejected: `SetTracking` carries over any status
  that is neither empty nor `pending`, so a machine verdict written there would be inherited by the
  next re-run as if a person had decided it, and would overwrite a human judgement that disagreed.
- **Score at a single horizon** rather than at checkpoints. Rejected: objection 5 (horizon vintage)
  has no answer without fixed checkpoints, and the "early signal vs 12-month" question is the
  interesting one.
- **Cache the monthly view's numbers** instead of deriving immutability from terminal inputs.
  Rejected: a cache makes immutability a promise about a cache, and a bug in invalidation silently
  rewrites history.
- **Put the group statistics on `/api/v1`** so the SPA and machine callers share one route. Rejected:
  v1 authorizes by token scope alone, and the app bridge mints `query` tokens for every unrestricted
  user, so a manager-only route on v1 is unenforceable (`apps_api.go:121`).
- **Score the adjusted (`qfq`) series**, with a proportional-adjustment proof. Not rejected on
  principle, but deferred: it would make the display amendment in §10 unnecessary and it needs a proof
  this design does not have. D8 decides between it and detection-and-exclusion.
