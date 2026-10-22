# Forecast verification (预测验证): data model and Dify output contract (draft 4)

**Status: draft for review. Reopens ADR 0029. Nothing here is implemented.** Once the shape is
agreed, it is folded into ADR 0029 (status → Accepted, with every overturned rule listed in §2),
and only then are failing tests written.

Two adversarial reviews against the code have been folded in. §13 lists the findings of the second
review and how each is addressed. Draft 2's §12 covered the first review, and those changes are
kept unless §13 says otherwise.

Draft 4 adds the feature name (§1.1) and the two viewing modes, monthly and custom range (§8.5). It also takes in the review of the implementation plan: terminal refusals, per-cell BH, the D4 membership rule, the `base_date` split, instant-based backtest detection and the session route for the SPA.
The build plan is in [forecast-verification-implementation.md](forecast-verification-implementation.md).

## 1. What is being decided

The owner wants "real vs predicted" as a portal feature:

- a **monthly overview** that quantifies how reports performed;
- a **per-report view** of what one report said and what then happened.

The agreed route:

- Dify emits **structured `predictions`** at ingest. They are projected from deterministic code
  nodes, never parsed from prose.
- The portal keeps them in an **append-only ledger**.
- An evaluator marks them at **fixed checkpoints**.
- The overview quantifies only **mature checkpoints** of **closed groups**. Groups are calendar
  months by default (the official view), or any day-precise range in the exploratory view (§8.5).

### 1.1 Name

The feature is called **预测验证** in the UI, and *Forecast verification* in English UI copy.

- Code identifiers use `forecast` for the feature (routes, i18n keys, Go handlers) and `prediction`
  for the stored ledger (`prediction_*` tables, the `predictions` ingest field). The ledger stores
  predictions; the feature verifies forecasts.
- Avoid these labels:
  - 「回测」: a backtest simulates on history, and this tracks live calls forward.
  - 「复盘」: taken by `nav.review`.
  - 「跟踪」: the tracking items.
  - 「战绩」: reads as a score of people.

## 2. Relationship to ADR 0029

**Kept:**

- claims live outside `tracking_items`;
- server receipt time anchors everything;
- prices are integer fen, never `REAL`;
- thresholds are compared with unadjusted prices;
- refusals are recorded outcomes;
- the evaluator ships disabled, with its reader, admin writer and UI in one stage;
- Beijing codes produce no price claims;
- there is never a per-analyst score;
- nothing is extracted from report prose.

**Overturned, with reasons.** The ADR must say so explicitly.

| ADR 0029 rule | Replaced by | Why |
|---|---|---|
| No aggregate number, ever | Cohort metrics under the gates of §8 | The owner asked for quantification. §8.3 answers the five objections. |
| A re-emitted `claim_key` supersedes (`supersedes_id`); withdrawal reconciled across reports | Each ingest is an immutable **set**; the earliest set per cohort member counts (§8.1) | 1-6-4 issues a fresh dated forecast per run. Nothing is carried across days, so there is nothing to withdraw. A revision stays visible as a later set. |
| Ledger lifetime = report retention floor | Ledger outlives reports (§6.6) | Otherwise the 365-day floor purges every set on the day its `h12` becomes resolvable. |
| Both vendors must agree exactly (`sources_disagree`) | Single vendor in v1, see D9 | The quote stack is primary-plus-fallback, not corroboration. A refusal that can never fire is worse than none. |

## 3. Scope of the first version

| Report type | Producer | In v1 | Why |
|---|---|---|---|
| 投资决策建议 | 1-6-4 投资决策 V3.16.x | **Yes** | `v3calc` / `v3risk` compute every scored number in code. |
| 综合决策 | 3-5 | No (D7) | Its values come from an LLM agent, and its claims are event probabilities. |
| 1-x sub-reports | 1-x | No | They feed 1-6-4, so scoring them would count one view twice. |

The covered report types are a Go constant (`predictionRtypes = {"投资决策建议"}`). Coverage (§8.1)
is defined over it.

**Prerequisite:** re-pull the Dify export and confirm §5.1 against the live 1-6-4 graph. The local
export is from 2026-09-17/18, maps prod `15249be5` to the V2 file, and has no node that computes the
理想买入价 / 止损价 that recent reports print.

## 4. Time: one zone, sessions from bars, nothing caller-supplied

**Zone.** Every civil date in this feature is in **`Asia/Shanghai`** (the exchange zone,
`quote.go:260`). It is not the panel timezone: that falls back to `time.Local`
(`apiv1.go:88-97`), the release image runs `TZ=UTC`, and the admin can change it later, which would
re-key cohorts. The zone name is stored on each set.

| Term | Definition |
|---|---|
| `made_at` | Server receipt instant (UTC RFC3339). |
| `made_on` | Shanghai civil date of `made_at`. |
| `cohort` | Shanghai `YYYY-MM` of `made_at`. Caller dates never move a set between cohorts. |
| Session calendar | The bars of the set's **benchmark index** (§9.3). The code has no trading calendar (`quote_cache.go:58-66`), and index bars exist on every session, including the days a stock is halted. |
| Completed session | A session whose bar may be used only after 15:30 Shanghai on that date. Tencent serves today's unfinished bar during trading (fixture `tencent_fqkline_sh000001.json`, captured 14:57), and a resolved mark is permanent. |
| Start bar `S0` | The first session whose **close is after `made_at`**. Received before 15:00 on session T → `S0` = T. Received after 15:00, or on a non-session day → the next session. Every return and every fill starts at `S0`'s close. Nothing the model could have seen when it priced the stock is counted as forecast. |
| Window | Sessions strictly after `S0`, up to the checkpoint. Used for lows, highs and fills. |
| Checkpoints | `d5`, `d20`, `d60`, `d120` = the 5th, 20th, … index session after `S0`. `h12` = the first index session on or after `made_on` + 12 months, standard-horizon sets only (§5.3). |

## 5. Dify output contract

### 5.1 Projection node

1-6-4 gets one new **code node** after `v3riskfix` and before the ingest node `94cd6bcaI0`. It reads
`v3decisionadapt.result`, `v3calc.result` and `v3risk.result`.

**Its only arithmetic is rounding:**

- Values are converted with `Decimal(repr(x))` and quantized half-even: prices to `0.01`,
  probabilities and weights to `0.0001`.
- Plain `round()` on a binary float is not decimal half-even (`round(8.655, 2)`).
- Scenario probabilities are then adjusted on the largest term so they sum to exactly `1.0000`.

Everything else is selection:

| Contract field | Source (confirm on the live graph) |
|---|---|
| `baseline.price` / `.as_of` / `.source` | `decision.entry_price` / `entry_price_as_of` / `entry_price_source` (`company_quote` or `derived_summary.*`) |
| `information_cutoff` | `decision.information_cutoff` |
| `horizon_end` | `decision.investment_horizon_end` |
| `decision_status` | `v3calc.decision_status` |
| `decision_quality` | `v3decisionadapt` quality (`normal` / `degraded` / `repaired`) |
| `probability_method` | `decision.probability_method` (`approved_valuation_source`, `approved_policy_default`, `subjective_committee`, `unavailable`) |
| `probability_transform` | `rule_id` of any calibration applied |
| `policy_version` | version of `v3config.policy_json` |
| `stance` | §5.3 |
| scenarios | `calc.scenario_results[]` |
| `expected_terminal`, `p_profit` | `calc.expected_terminal_price`, `calc.p_profit` |
| `entry_price` | `calc.suggested_entry.recommended_buy_price` |
| `target_weight` | `risk.target_total_weight` |
| `emitter.*` | app, `V3.16.x`, `calc.calculator_version`, `schema_version` |

The ingest node gains a `predictions` **object** parameter, and
`portal-tools/openapi.dify.ingest.json` gains the property. Dify renders object parameters from a
template and passes a string when the rendered text is not valid JSON. The portal rejects a string
value with reason `not_object` (§5.5).

### 5.2 Shape

```json
{
  "predictions": {
    "schema": "predictions.v1",
    "emitter": { "app": "1-6-4", "version": "V3.16.2", "calculator": "scenario-calc-3.5", "decision_schema": "decision.v3.4" },
    "policy_version": "…",
    "probability_method": "approved_valuation_source",
    "probability_transform": null,
    "decision_status": "eligible",
    "decision_quality": "normal",
    "information_cutoff": "2026-10-17T23:59:59+08:00",
    "baseline": { "price": "8.52", "as_of": "2026-10-16", "source": "company_quote" },
    "horizon_end": "2027-10-17",
    "claims": [
      { "key": "stance",            "kind": "stance",         "stance": "long", "target_weight": "0.1000" },
      { "key": "expected_terminal", "kind": "terminal_value", "price": "9.95", "p_profit": "0.7000" },
      { "key": "scenario.bull",     "kind": "scenario",       "label": "bull", "price": "14.00", "probability": "0.2500" },
      { "key": "scenario.base",     "kind": "scenario",       "label": "base", "price": "10.00", "probability": "0.4500" },
      { "key": "scenario.bear",     "kind": "scenario",       "label": "bear", "price": "6.50",  "probability": "0.3000" },
      { "key": "entry_price",       "kind": "entry_price",    "price": "8.65" }
    ]
  }
}
```

### 5.3 Stance, nothing emitted, standard horizon

**Stance table:**

| `decision_status` | `target_total_weight` | Result |
|---|---|---|
| `eligible` | > 0 | `long` |
| `eligible` | 0 | `no_position` |
| `no_trade` | any | `no_position` |
| `research_price_advice` | any | no predictions (this branch means the current price was missing) |
| `invalid_input`, `insufficient_data` | any | no predictions |

**Standard horizon.** A set is standard when `horizon_end` = `made_on` + 12 months ± 7 days.

The Dify sandbox runs in UTC, so between 00:00 and 08:00 Shanghai its `report_date` is a day early
and `horizon_end` comes out a day early too. The ±7-day band absorbs that, and test 3 pins it.

A non-standard set is still stored and still scored at `d5`–`d120`. It has no `h12` mark.

### 5.4 Encoding rules (portal-enforced)

- **Prices:** strings, ≤ 2 decimals, converted exactly to fen. A JSON number or a third decimal is
  rejected.
- **Probabilities and weights:** strings in `[0, 1]`, ≤ 4 decimals, converted to basis points.
  Scenarios must sum to exactly 10 000 bp.
- **Closed enums**, as Go constants and OpenAPI `enum`s: `kind`, `stance`, `label`,
  `decision_status`, `decision_quality`, `probability_method`. `stop_loss` is reserved and refused.
- **`horizon_end`** must be after `made_on`.
- **Symbol:** the report's normalised symbol must be an A-share on `sh` or `sz`. Refused for
  predictions (never for the report):
  - `bj` codes;
  - Shanghai B 900xxx (which `marketPrefix` maps to `bj`);
  - **Shenzhen B 200xxx** (mapped to `sz`, `names.go:236`, but priced in HKD);
  - empty or dropped symbols.
- **Size:** `predictions` must be ≤ 64 KiB.
- **Shape:** an object (not a string). Keys are unique. Required claims: `stance`,
  `terminal_value`, three scenarios.

### 5.5 What the portal does with each case

Every case writes **one set row**, so coverage is computed from the ledger alone (§8.1) and survives
report purges.

| Case | Report | Set row `state` | Counted in metrics |
|---|---|---|---|
| rtype ∉ `predictionRtypes`, no `predictions` | stored | no row | — |
| rtype ∈ `predictionRtypes`, no `predictions` | stored | `absent` | no, counted as uncovered |
| `predictions` invalid | stored | `rejected` + per-rule `errors` | no, counted as uncovered |
| valid, `information_cutoff` more than 72 h before `made_at` (instants, not dates) | stored | `backtest` | no, listed apart |
| valid | stored | `accepted` | yes, subject to §8 |

A rejected set also:

- writes an **audit row** (`AuditPredictionsRejected`, reasons in detail);
- fires a webhook event **`predictions.rejected`**, so a broken projection reaches a person instead
  of sitting silently in coverage.

The HTTP response is 200 in every case above and carries `predictions_status`.

## 6. Data model

One migration step, **`migration0002`** in `migrate_steps.go`, with
`restoreRule{olderDumpOK: true}`. It declares four tables, one `cleanup_runs` column and their indices,
and is never edited
after release. Nothing is added to `baseSchemaStmts()`.

Units: `_fen` = integer fen; `_bp` = integer basis points.

### 6.1 `prediction_sets`

| Column | Type | Notes |
|---|---|---|
| `id` | pk | |
| `report_id` | int null | NULL once the report is purged (§6.6) |
| `symbol`, `rtype`, `version`, `title`, `report_date` | text | copied report identity, for display after purge; **never used for access** |
| `made_at`, `made_on`, `cohort`, `zone` | text | §4 |
| `state` | text enum | `absent` / `rejected` / `backtest` / `accepted` |
| `errors` | text | JSON list of reasons, for `rejected` |
| `payload` | text | verbatim `predictions`, ≤ 64 KiB; when a rejection is for size, only the first 64 KiB plus `payload_truncated = 1` |
| `payload_sha256` | text | |
| `body_sha256` | text | hash of the `body_md` that arrived with the set |
| `emitter_app`, `emitter_version`, `calculator`, `schema_name` | text | `schema` is reserved on Postgres |
| `policy_version`, `probability_method`, `probability_transform`, `decision_status`, `decision_quality` | text | |
| `information_cutoff`, `horizon_end`, `horizon_std` | text, text, int | |
| `emitted_base_fen`, `emitted_base_as_of`, `emitted_base_source` | int, text, text | |
| `s0_date`, `base_date`, `s0_fen` | text, text, int null | calendar S0 (counts checkpoints); the stock's close on S0, or its last printed close before S0 when it was halted that day; that unadjusted close. Returns and the fill window start at `base_date`; the benchmark leg starts at `s0_date` (§4, §9.3). Filled by the evaluator. |
| `base_check` | text enum | `pending` / `match` / `mismatch` / `refused` (§9.4) |
| `bench_code` | text | chosen at insert (D1) |

The set is insert-only. The evaluator may fill `s0_*` and `base_check` only through
`WHERE base_check IN ('pending','refused')`.

Unique index: `(report_id, payload_sha256)`. It is the idempotency key for retries. On both
SQLite and Postgres, a NULL `report_id` never collides.

Other indexes: `(cohort, rtype, state)` and `(symbol, made_on)`.

### 6.2 `prediction_claims`

One row per claim of an `accepted` or `backtest` set:

| Column | Notes |
|---|---|
| `set_id`, `claim_key` | unique together |
| `kind`, `stance`, `label` | enums; `stance` and `label` are null unless the kind uses them |
| `price_fen`, `prob_bp`, `weight_bp` | nullable |

Insert-only.

### 6.3 `prediction_marks`

One row per `accepted` set × checkpoint, created `pending` together with the set:

| Column | Notes |
|---|---|
| `set_id`, `checkpoint` | unique together; checkpoint is `d5` / `d20` / `d60` / `d120` / `h12` |
| `status` | `pending` → `refused` (retry) → `resolved`, or `refused_final` after 10 sessions past due. Both `resolved` and `refused_final` are terminal. |
| `refusal` | `no_bar`, `stale_bar`, `unverifiable` |
| `attempts`, `next_attempt_at` | |
| `session_date` | the checkpoint session, from the index calendar |
| `bar_date` | the stock's last traded bar at or before `session_date` |
| `halted` | 1 when `bar_date` < `session_date` (halted or delisted) |
| `corp_action` | 1 when an ex-date falls in `(S0, session_date]` (§9.2) |
| `close_fen`, `low_fen`, `high_fen` | unadjusted; low and high are taken over the window |
| `ret_bp`, `bench_ret_bp` | §9.2 |
| `resolved_at`, `source` | |

### 6.4 Not stored

- No verdict column. Hit/miss is computed on read by one Go function.
- Nothing is written to `tracking_items`.

### 6.5 Write path

`v1Ingest` (`apiv1.go:245-307`) writes through separate pool statements. `resolveVersion` writes
through the pool, and SQLite has one connection (`store.go:98`). So the set cannot share the
report's transaction.

1. **Classify** `predictions` with a pure function, before `RegisterType`. No writes.
2. **Ingest the report** as today.
3. **Insert the set** (any state), its claims and its pending marks, in one transaction of its own.
   A retry is idempotent on `(report_id, payload_sha256)`.
4. **If step 3 fails**, retry it once in process. If it still fails, return 200 with
   `predictions_status: "store_failed"`, log, and attempt an audit row.
   - Not 5xx: Dify would retry three times, then mark a run failed whose report already exists.
   - The coverage page also lists live covered-rtype reports that have no set row as
     "unrecorded", so this case stays visible.

### 6.6 Purge, retention, storage

**Purge.** `DeleteReport` (`store.go:1882`) and `deleteReportChunk` (`cleanup_store.go:217`) run
`UPDATE prediction_sets SET report_id = NULL` for the deleted ids. This happens in the same
transaction, and the change lands in the same commit that adds the tables. Sets, claims and marks
survive, and a later report reusing an id inherits nothing.

**Storage.** The storage console's usage list (`cleanup_api.go:195-218`) gains a
`predictions` category. The ledger also gets its own retention target (the sixth; see ADR 0029 §8), which
ships disabled and deletes a whole month at a time — added after review, and the ADR records why the
original "no cleanup target in v1" reasoning was wrong.

**Size bound.** Payloads are capped at 64 KiB per set. At about 100 reports a month that is
≤ 6.4 MB a month in the worst case, and typically about 2 KB per set.

**Backup.** The tables are listed through the migration's `tables` (`migrate_steps.go:292`), so
backup and restore pick them up.

## 7. Access

- **While the report exists**, a set is readable exactly when its report is: join to `reports` and
  apply the report read filter (the `QueryTracking` pattern, `store.go:1634`).
  - `owner_group` is **not** used. ADR 0024 retired it as a read key (`report_owner.go:91-93`).
  - 1-6-4 sends no `owner_token`, so the column would be NULL anyway.
- **After purge** (`report_id` NULL), a set is readable only by unrestricted users. A restricted
  user cannot prove access to a report that no longer exists. **Fail-closed.**
- **Per-report and per-symbol routes** sit on `/api/v1` with `query` scope, for machine callers only. The v1 surface is token-only (`routes.go:33`). The SPA reads a report's sets through a **session** route, `GET /api/report/{id}/predictions`, under the same rules above.
- **The group statistics route** is `/api/admin/forecast/groups`, registered with `PermManage`, not
  `/api/v1`.
  - v1 authorizes by token scope alone (`apiui.go:160`, `server.go:981`).
  - The app bridge mints `query` tokens for any unrestricted user (`apps_api.go:121-141`), so a v1
    route cannot be limited to managers.
  - The monthly page is therefore a **core SPA page** (settles D6).

> **Amended 2026-10-20.** Reader access will be extended in R2a by the access model planned as ADR
> 0039 (per-OU `all`/`own`/`none`, a top-level `/forecast` page, compute governance); it is not
> written yet. Until it ships, P30/P31 apply: only an effectively restricted OU is a group
> principal, and an unreadable restriction scopes to nothing.

## 8. What is quantified

### 8.1 Groups and membership

A **group** is a set of Shanghai receipt dates (`made_on`). In the official view a group is one
calendar month, the `cohort` column; in the exploratory view it is whatever §8.5 defines. Everything
below is stated per group, so both views run the same code.

**Members.** Per `(symbol, group)`, the **earliest `accepted` set decides** (D4).

- If it is `normal` and `match`, it is the member.
- If it is degraded or `mismatch`, the symbol is excluded for that group. A later rerun is counted
  as `duplicate` and never promoted, so selection by rerun is impossible.

- This removes repeat runs of a scheduled symbol and upgrade-day reruns under a new title.
- It also makes members roughly independent within a cohort, which §8.3 needs.

The report section labels a set **"counted"** only if it is that member. It does not label the
report's own first set.

**Coverage** is computed from the ledger alone: members, and non-members by reason. The reasons
are `absent`, `rejected`, `backtest`, `degraded`, `mismatch`, `refused` and `duplicate`.

### 8.2 Maturity

A cell (group × checkpoint) shows numbers only when **all** of these hold:

1. the group's last date has passed: the month has closed, or the custom range's end date is
   before today;
2. every member's mark for that checkpoint is **terminal**: `resolved`, or `refused_final` (10
   sessions past due). No baseline check is still pending or retryable. A mature cell's inputs
   therefore never change, which is what makes it immutable;
3. `refused_final` marks are ≤ 5 %;
4. the (slice) n ≥ 30.

Until then the cell shows its maturity count and nothing else.

Marks with `corp_action = 1` are **excluded** from metrics and counted next to them (§9.2).
`halted` marks are included, scored on the last traded close.

### 8.3 Metrics

Each metric is reported with its `n`, its null and a p-value. P-values are corrected with
**Benjamini–Hochberg within each cell** (that cell's metrics and slices), so a mature cell's `q` is
fixed and identical in both viewing modes. The page legend states that cells are corrected
independently. The permutation seed excludes the viewing mode. Slices by `policy_version` /
`probability_method` are shown only where the slice has n ≥ 30.

**Null procedure: label permutation.** Within a group, members' predictions are shuffled across
members while their realised excess returns stay fixed, 10 000 times. This keeps the cross-sectional
market move, which draft 2's sign-flip test assumed away.

| Metric | Checkpoints | Definition | Null |
|---|---|---|---|
| Stance spread | all | mean excess(`long`) − mean excess(`no_position`) | label permutation |
| Rank IC | all; "early signal" before `h12` | Spearman(`expected_terminal` / `s0_fen` − 1, excess) | label permutation |
| Directional hit rate | all | among \|upside\| ≥ 5 %, share whose excess has the same sign | larger base rate on that subset, plus label permutation |
| Entry fills | all; labelled derived (`entry` = `expected_terminal` / (1+0.15) by policy) | share touched in window; mean return from fill | — |
| Scenario band | `h12` | share of closes in [bear, bull] | — |
| p_profit calibration | `h12`; only `probability_method ≠ approved_policy_default` and no `probability_transform` | mean `p_profit` vs realised share positive | — |

### 8.4 ADR 0029's five objections

| Objection | Answer |
|---|---|
| 1. No verdict domain | Closed enums in Go and OpenAPI. Metrics are defined over numbers. |
| 2. No baseline | Excess over a recorded benchmark, with a label-permutation null and an `n` for every number. There is no bare "accuracy %". |
| 3. Self-selected denominator | Projection is code. Every covered ingest leaves a set row, so coverage, with its reasons, comes from the ledger. |
| 4. Refusals correlate with losses | Halts and delistings resolve on the last traded close. Real data refusals are capped at 5 %, and corporate actions are excluded and counted. |
| 5. Horizon vintage | Fixed checkpoints; closed groups only; every member must be past the checkpoint; `h12` holds standard horizons only. |

### 8.5 Two viewing modes, one engine

The verification page has two tabs over the same computation.

| | **Monthly** (按月, default) | **Custom range** (自定义区间) |
|---|---|---|
| Range | Whole calendar months, from the first cohort to the latest | `from` / `to` dates, day precision, Shanghai `made_on` |
| Grouping | Month | Week (ISO, Mon–Sun), month, quarter, or `all` (the whole range as one group) |
| Status of the numbers | **Official.** A mature cell never changes again, and can be cited. | **Exploratory.** A persistent 「探索」 banner; every p-value is marked "not corrected for range selection"; CSV exports carry `mode=exploratory`, the range and the grouping. |
| Partial periods | None | A week, month or quarter cut by `from` / `to` is a group of its own, labelled with its actual dates (e.g. `2027-03-10 – 2027-03-31`), never as the full period. |

**Exploratory numbers stay apart from official ones:**

- A custom range that happens to equal whole months with monthly grouping returns the same
  numbers. It is still labelled exploratory, because the label is about how the range was chosen.
- No exploratory result is cached in or written to the ledger; it is computed on request.
- The monthly tab never takes parameters that change its numbers.

**Bounds.** `from` ≤ `to`; the range is at most 36 months; `group=week` is allowed for ranges of
at most 26 weeks. That keeps the permutation cost and the BH family bounded. A request outside
these bounds is refused with a code, never truncated.

**Why both.** The monthly view is the record: it is decided by the calendar, not by the reader. The
custom view answers questions the calendar does not, such as "since the V3.17 prompt change" or "this
quarter". Because that freedom lets a reader search for a flattering window, its numbers are
labelled and never presented as the record.

## 9. Evaluator

### 9.1 Loop

The loop is shaped like `geo_update.go:501`: a one-minute tick and single-flight. It is **off by
default** (`forecast_auto = "0"`), and its reader, admin writer and UI ship together.

Due work is derived from `next_attempt_at` on every tick. Nothing depends on the in-memory
`lastAt`.

Fetches are **one range fetch per set**, covering `S0` through the latest due checkpoint, so all of
a set's checkpoints come from one call. They go through the quote stack (`quote.go`,
`quote_cache.go` semaphore). A bar may resolve a mark only once its session has **completed**
(§4).

### 9.2 Returns and corporate actions

`ret_bp` is the **unadjusted price return** `close_fen / s0_fen − 1`. The benchmark uses the same
formula; index levels have no ex-dates.

Corporate actions cannot be ignored: a 10-for-10 bonus issue reads as −50 %. They are **detected**
by a change in the vendor's adjusted/unadjusted ratio between `S0` and `session_date`, using a qfq
fetch for **detection only**.

- The dividend element the unadjusted feed carries on an ex-date names a **cash** dividend; it does
  not mention a bonus, capitalisation or rights issue. The ratio is therefore the primary signal for
  a bonus or rights issue, not a fallback for the element's gaps.
- That is a narrow exception to the ADR 0028 "never qfq" rule, which is about displaying prices. The
  ADR records it, and the comparison is exact in integers with a one-fen bound.
- A flagged mark is resolved and stored, but excluded from metrics and counted (§8.2).
- The gap heuristic of draft 2 is dropped. The remaining backstop is a move beyond the board's
  daily limit, computed from fen-rounded limit prices (10 % main board, 20 % ChiNext/STAR, 30 %
  Beijing); an ordinary limit day is not a corporate action.

### 9.3 Sessions, halts, delisting

Sessions come from the **benchmark index's bars**. At a checkpoint, the stock's last traded bar at
or before `session_date` is used, and `halted = 1` is set when it is earlier.

This covers suspensions and delistings alike. Neither stays refused, so neither can quietly hollow
out a cell.

**Halted on S0 (V6b).** If the stock did not trade on S0, its starting price is the last close it
printed strictly before S0, found in the reference window and then in one bounded 400-day
look-back. The stock's return runs from that stored base (`base_date`/`s0_fen`); the benchmark leg
runs from S0's index close, so the index's pre-report move is never added to the excess. A stock with
no bar anywhere in that window is a mark-specific failure, not a source failure.

### 9.4 Baseline check

The emitted baseline is the LLM agent's quote, which code only selects. It is checked against what
the market actually printed.

| Receipt timing | `match` when the emitted price is… |
|---|---|
| During a session | within [low, high] of that session, or equal to the previous close |
| Outside a session | equal to the last completed close, or the one before |

`mismatch` sets are excluded from metrics and counted (§8.1). Returns never use the emitted price;
they start at `S0`.

## 10. UI

> **Amended by P10/P20.** The reading panel's mark column uses the server's phase vocabulary
> (`waiting_s0` / `waiting` "on or after <date>" / `retrying` only with a recorded due session /
> `resolved` / `final`), never "fetch failed" for a mark that is merely not due. The full reading-page
> integration (card, chip, timeline dots, run-page card) is P20 in R2b.

- **Report section: a 「预测验证」 card on `StockPage`** (`/stock/:symbol`; there is no separate report page and no tabs), below the selected report's body. The price levels are drawn on the existing quote chart at the top of the page, through a new optional `PriceChart` prop. The card shows:
  - the set's own payload, not the current body;
  - lines for `S0`, terminal value, entry price and the scenario band on the existing K-line chart;
  - the checkpoint marks;
  - the "counted" label (§8.1);
  - a "body changed since this set" note when the hashes differ.
- **Verification page 「预测验证」 (core SPA under 管理, manager only), with two tabs: 按月 and
  自定义区间 (§8.5).**
  - The group × checkpoint matrix with maturity, n, nulls and BH-adjusted p-values.
  - A coverage strip broken down by reason.
  - A cell opens its member list, and each row links to that report on `StockPage`.

## 11. Acceptance conditions (failing tests first)

Each states its direction.

1. **Precision.** `"8.655"`, the JSON number `8.65`, and a string-valued `predictions` (reason
   `not_object`) each produce a `rejected` set plus an audit row and a `predictions.rejected`
   webhook. The report is stored. *Ledger fail-closed, report kept.*
2. **Scenario sum.** 9 999 / 10 001 bp are rejected; 10 000 is accepted.
3. **Horizon.** Equal to `made_on` → rejected, tested at 23:30 UTC with the panel tz set to UTC.
   A UTC-sandbox horizon one day early stays standard. +6 months → non-standard (no `h12`).
4. **Symbols.** `bj`, 900xxx, **200xxx** and an empty symbol each produce a `rejected` set and a
   stored report.
5. **Vocabulary.** An unknown enum value, or `stop_loss`, is rejected.
6. **No writes before validation.** An invalid payload causes no write beyond what the same
   request without `predictions` causes.
7. **Covered rtype without predictions.** It writes an `absent` set; other rtypes write none.
8. **Retries.** An identical retry gives one set. A changed payload gives a second set, and the
   member is still the earliest.
9. **Membership.** Two runs of one symbol in one cohort (any titles) give one member.
10. **Resolved is terminal.** The update guard affects 0 rows on a resolved mark.
11. **Purge.** `DeleteReport` **and** `deleteReportChunk` null `report_id`, and the ledger rows
    survive, on SQLite and Postgres. After purge, a restricted user is refused the set and an
    unrestricted user can read it.
12. **Anchoring.** Receipt at 10:00 on T → `S0` = T; at 16:00 on T → `S0` = T+1; on Saturday →
    `S0` = Monday. A low before `S0` never counts as a fill. Run with the panel tz set to UTC.
13. **Backdating and backtests.** A backdated report `date` or `baseline.as_of` changes neither
    the cohort nor `S0`. An `information_cutoff` more than 72 h before `made_at` gives `backtest`.
14. **Partial bars.** The 14:57 fixture's last bar never resolves a mark. The same bar after
    15:30 does.
15. **Halts and delisting.** An index-calendar fixture with stock bars missing → `halted = 1`,
    resolved on the last traded close.
16. **Corporate actions.** A fixture spanning a bonus issue → `corp_action = 1`, excluded from
    metrics and counted.
17. **Maturity gate.** An open month, refusals at 6 %, or a slice with n = 29 → no numbers.
    Refusals at 5 % with n = 30 → numbers.
18. **Statistics.** Every number carries n, its null and a BH-adjusted p. The label-permutation
    null is reproducible with a fixed seed.
19. **Access.**
    - The cohort route is refused to non-`PermManage` users, including holders of app-bridge
      tokens; R2a replaces this with the per-OU scope planned as ADR 0039.
    - The per-report route is refused for an unreadable report.
    - `owner_group` is never consulted (a test sets it to a readable group on an unreadable report).
    - A `g:` viewer row is written only for an effectively restricted OU, and an unreadable
      restriction or a broken OU chain scopes to nothing (P30/P31).
20. **Rounding.** A Dify-node unit test feeds `8.655`, `8.650000000000001` and probabilities
    `[0.33333, 0.33333, 0.33334]` and checks half-even decimal output summing to `1.0000`.
21. **Store failure.** When the set insert fails twice, the response is 200 with `store_failed`,
    and the report appears as "unrecorded" in coverage.
22. **Migration.** Step 0002 applies on a v0.4.72-shape database. An older dump restores. The
    usage list includes `predictions`.
23. **One engine.** A custom range equal to whole months, grouped by month, returns numbers identical
    to the monthly tab, but is labelled `exploratory`.
24. **Partial periods.** A custom range starting mid-week with week grouping yields a first group
    labelled with its real dates. Its maturity uses its own last date.
25. **Range bounds.** `from > to`, a range over 36 months, or `group=week` over 26 weeks is refused
    with a code. Nothing is truncated.
26. **Monthly numbers are fixed.** No query parameter changes a mature monthly cell. Two requests
    a day apart return identical numbers for every mature cell.

## 12. Order of work and open decisions

**Order of work:**

1. Re-pull the Dify export and confirm §5.1.
2. Decide D1–D9, then rewrite ADR 0029 (including the §2 overturns and the ADR 0028 qfq exception).
   Owner sign-off on the schema.
3. **Portal:** the migration, classification, write path, purge and access, plus tests 1–11, 13,
   19, 21 and 22.
4. **Dify:** projection node (test 20) on the test app, then publish. Coverage counts from here.
5. **Evaluator** (off by default): tests 10, 12 and 14–18.
6. **UI:** the panel, then the monthly page.

There is no backfill.

**Open decisions:**

- **D1. Benchmark.** CSI 1000 for all, or chosen by board (CSI 300 / ChiNext / STAR 50)? Recorded
  per set either way.
- **D2. Checkpoints.** `d5 / d20 / d60 / d120 / h12`?
- **D3. Invalid predictions.** Proposed: keep the report, write a `rejected` set, an audit row and
  a webhook.
- **D4. Membership.** Proposed: the earliest accepted set per `(symbol, group)` decides; if it is degraded or `mismatch`, the symbol is excluded, never replaced by a rerun.
- **D5. Purge.** Proposed: the ledger outlives reports, has its own off-by-default retention target, and shows in
  the usage list.
- **D6. Monthly page.** Settled by §7: a core page, manager only. The page has a monthly tab and a
  custom-range tab (§8.5), and is named 预测验证 (§1.1). Both are settled by the owner on 2026-10-17.
  **Superseded in R2a** by the access model planned as ADR 0039: the record moves to a top-level
  `/forecast` page for members of OUs with a scope, and `/manage/forecast` becomes the operations
  page.
- **D7. 3-5 重组决策.** Out of v1, or ask its owner for a deterministic value table first?
- **D8. Corporate actions.** Proposed: detect from the dividend element that Tencent's unadjusted
  day rows carry on ex-dates (`quote.go:1121`); fall back to the qfq ratio only for the kinds that
  element misses. Exclude and count. Alternative: score on a frozen adjusted ratio (needs a
  proportional-adjustment proof).
- **D9. Corroboration.** Proposed: single vendor in v1, which overturns ADR 0029. Alternative:
  fetch Tencent and Sina on the evaluator path and keep `sources_disagree`.

## 13. Second review: findings and changes

| # | Finding | Change |
|---|---|---|
| 1 | T−1 baseline counted moves the model had already priced | Everything starts at `S0`, the first close after `made_at` (§4). |
| 2 | An intraday partial bar could resolve a mark permanently | Only completed sessions (after 15:30 Shanghai) resolve (§4, test 14). |
| 3 | The copied `owner_group` is a retired read key and is NULL for 1-6-4 | Join-to-report access; unrestricted-only after purge (§7). |
| 4 | "`PermManage`-only" on `/api/v1` is unenforceable, and the bridge mints query tokens | Cohort route moved to `/api/admin`; monthly page is core (§7). |
| 5 | Corporate actions were flagged but still scored, and the gap heuristic hits limit-down days | Detection by qfq ratio; excluded and counted (§9.2). |
| 6 | Halts slid checkpoints when counting on the stock's own bars | Index bars are the calendar (§9.3). |
| 7 | Delisting refused forever | Same rule as halts: last traded close, `halted = 1`. |
| 8 | `sources_disagree` could never fire | Removed; recorded as an overturn; D9. |
| 9 | Backtest runs were scored as live | `information_cutoff` in the contract; `backtest` state (§5.5). |
| 10 | `base_check` used the wrong vocabulary and had no effect | Defined against the session range; `mismatch` excluded and counted (§9.4). |
| 11 | `decision_quality` was missing | Added; only `normal` counts. |
| 12 | `probability_method` vocabulary was wrong | Real values; exclusion keyed on `approved_policy_default`; transform recorded. |
| 13 | P-values were invalid (repeats, asymmetry, multiplicity) | One member per `(symbol, cohort)`, label permutation, BH, n ≥ 30. |
| 14 | Coverage drifted after purge | Every covered ingest writes a set row (`absent` / `rejected` / …). |
| 15 | The ledger had no storage path, and its payload was unbounded | 64 KiB cap; usage category; a cleanup target with its own predicate (revised after review — the first version said there was none, see ADR 0029 §8). |
| 16 | Timezone not pinned (panel tz falls back to UTC in the image) | `Asia/Shanghai` throughout, stored per set. |
| 17 | 200xxx B-shares passed the symbol check | Refused explicitly. |
| 18 | ADR 0029's supersede/withdrawal rules were dropped silently | Listed as overturned with reasons (§2). |
| 19 | A rejected set was visible only in coverage; string-typed object params | Audit row, webhook, `not_object` reason. |
| 20 | A 5xx tail produced failed runs with stored reports; the line reference was wrong | 200 with `store_failed` and an "unrecorded" listing; reference fixed to `apiv1.go:245-307`. |
| 21 | Missing tests | Tests 14–16 and 19–21 added; the panel's "counted" label is tied to membership. |
