# Forecast verification v2: plan

> **Identifier update (2026-10-22).** The market-data ADR called 0038 in this baseline plan is
> [ADR 0041](adr/0041-ledger-grade-market-data.md). Notifications uses 0038 and external application
> connections uses 0040. References below retain their historical numbering; the implementation
> plan and the actual ADR are authoritative. This changes no source-admission decision.

| | |
|---|---|
| Status | Proposal for owner review. None of it is implemented. |
| Date | 2026-10-18 (a Sunday) |
| Code baseline | `origin/main` at `c9b2b747`. Every file:line in this document was read at that commit. |
| Inputs | Five workstream proposals at revision 2 (OU-based access, reading-page integration, demo parity and UX, multi-source market data, reliability and governance), plus one adversarial review of the previous draft of this plan. |
| Precedence | Where a proposal disagrees with the code, this plan follows the code (§1.4). Where the proposals disagree with each other, §2 settles it. |
| Next free ids | Migration step `0005`: `migrate_steps.go:168` lists 0001–0004. ADR `0037`: the latest in `docs/adr` is 0036. |

**The owner's request, in English.**
- Build to big-company engineering standards, and plan the work properly so it gets finished.
- Do not limit the feature to managers. Use the existing OU model for access.
- Close the gap with the demo.
- Integrate the feature with the report reading page.
- Give the evaluator several market-data sources with fallback, the way live quotes already work.

This plan also answers two questions the owner left open: what to do with the test data already in the ledger, and how to govern the benchmark change.

---

## 0. Executive summary

What changes for users:

1. **The numbers become trustworthy before the first one exists.** No checkpoint has resolved in production yet. Release 1b fixes every defect that would write a wrong value into a write-once column:
   - the benchmark is fetched as the wrong instrument;
   - S0 and dN disagree with ADR 0029 §4;
   - the baseline check compares nothing;
   - halts, small bonus issues and the daily-limit backstop are mishandled.

   The evaluator stays off or in dry mode until Release 1b, the corporate-action detector (P8) and the shared hit predicate (P16a) are all deployed, the ledger start is set, and an admin has hand-checked one d5. If this lands before the evaluator writes again, nothing needs repair.
2. **The screen stops saying false things (Release 1a, about a week).**
   - A checkpoint that is not due yet reads "waiting, on or after <date>" instead of "Fetch failed".
   - The "unrecorded" warning counts only reports ingested since the ledger began. Today it reports 1259 pre-ledger reports as failed ingests.
   - The integration test runs leave the record through a write-once, audited "ledger start". They stay visible as their own count, and no row is deleted.
   - Future test runs are classified as tests when they are ingested, so they never need another cut.
3. **Each cohort has one benchmark.** A benchmark change takes effect from the next month, is picked from a closed list, and is audited. Each month shows the index it is measured against.
4. **The record is not manager-only (Release 2a, target 2026-11-13).**
   - Admins enable it per OU. The setting is inherited down the tree, and the nearest setting wins. One switch publishes it, and a pilot OU gets it first.
   - Members of enabled internal OUs open it on a top-level page.
   - External (restricted) tenants are excluded in phase 1.
   - Evaluator operations stay under Manage.
   - A single access matrix (§3.5) says who sees what on every surface, and one table-driven test enforces it.
5. **The reading page carries the forecast (Release 2b).**
   - A summary chip in the report header arrives with the report, so nothing shifts.
   - A redesigned card matches the demo.
   - The chart's overlay toggle is always there.
   - The timeline shows dots on dates with a forecast.
   - The same card appears on the run page.
   - Exact links run both ways between a report and its cohort cell.
   - Report editions (ADR 0024) each show their own set.
6. **Demo parity is judged where it can actually be seen.** Production cannot show a mature cell before November, and n ≥ 30 will take months. So parity is reviewed on a deterministic seeded dataset, served by a dev-only clock that release builds refuse. It is measured against annotated production screenshots, not against a reading of the code.
7. **Market data follows the live-quote source policy, and this plan states plainly what that does and does not buy.**
   - The evaluator walks the operator's source order, restricted to sources that explicitly declare ledger-grade bars, and keeps its own health counters. Sina runs in shadow from the start.
   - Tier 1 adds **no fallback writes for scored marks.** Tencent stays the only writer, because no other source carries the corporate-action evidence a stock mark needs.
   - What changes in an outage: settlement pauses, and it never finalises a mark (V26).
   - Fallback writes arrive in Tier 2, starting with the benchmark leg, once a second source has been measured to agree with Tencent.
8. **It is operated like a production service.**
   - Tests come first and run on both drivers.
   - A heartbeat and a watchdog in its own goroutine raise alert webhooks, and a status-only health route serves an external uptime monitor.
   - SLOs are defined, every rollout step has an audited switch, and there is a runbook.
   - The machine API becomes token-only in behaviour, not only in its route class. This closes a forced-enrolment (2FA) bypass found during this review (D2).
   - Release 1 changes no schema. A code rollback is safe for the database, but Release 1's one-shot writes are not undone, so the ADR 0027 backup is taken first.

### 0.1 Before Monday 2026-10-19 15:00 CST (owner; web UI only; no deploy)

**Action 1: switch the evaluator off.**
- **What:** If `forecast_auto` is on, switch it off (Manage → Forecast verification → Evaluator settings). The change is audited as `forecast.auto` (`forecast_admin_api.go:51-56`). See V1.
- **Why it cannot wait:** On the first tick after Monday 15:30, the evaluator writes the four accepted sets' S0 and `base_check`, and those writes are permanent (`forecast_eval_store.go:168-183`).
  - S0 would be picked from the bars of a Shenzhen-listed stock (F01).
  - `base_check='match'` would be stamped without any comparison (F03).
- **Cost and undo:** Nothing is lost. Every input is a historical bar, and the fixed evaluator catches up from them. The new finality rule (V26) makes that catch-up safe.

**Action 2: set the benchmark back to `sh000852`.**
- **What:** Change the benchmark setting back. This build does not audit benchmark saves (`forecast_admin_api.go:42-50`), so note the time in the operations log. See V2.
- **Why it cannot wait:** Every covered ingest freezes the current setting onto its set (`apiv1.go:260`). From Monday, a real report would freeze `sh000300` into an October cohort that already holds `sh000852`.
- **Cost and undo:** If CSI 300 is wanted, Release 1a schedules it from 2026-11 (P4).

**Action 3: pre-declare the ledger start.**
- **What:** Record the declaration somewhere with a server-assigned timestamp (a pushed commit on a PR branch, or an issue comment) before 15:00 CST Monday, the first S0 close. See V3.
  - If set 6 is a real report, the text is: "Production ledger start = the `made_at` of set 6; sets 1–5 are integration runs."
  - If set 6 is a test too: "Ledger start = 2026-10-19T07:00:00Z; sets 1–6 are integration runs."
- **Why it cannot wait:** A cut recorded before any set has an observable outcome is provably independent of results.
- **Cost and undo:** If the deadline is missed, the code bound (§3.2) still limits the cut to these six sets. ADR 0037 then records that the declaration came late.

**Action 4: do not edit ledger rows by hand.**
- **Why:** Raw SQL on the citable table leaves no audit row, and the in-memory cell cache survives it.

### 0.2 Read-only queries this week (owner; before the Release 1a deploy)

Each query is read-only. Its answer feeds a decision.

```sql
-- Q1 (V3, V24). Provenance of sets 1-6. A time cut separates them only if every test set precedes
-- every real one. Ids are assigned in insert order and made_at is the insert's clock, so they should agree.
SELECT id, symbol, state, made_at, report_id, emitter_app, emitter_version, payload_sha256
FROM prediction_sets ORDER BY id;
SELECT id, at, actor, target_id, detail FROM audit_log
WHERE action = 'report.ingest' AND at >= '2026-10-18' ORDER BY id;

-- Q2 (V15, maturity timing). Covered ingests and distinct symbols per month. n >= 30 distinct
-- symbols in the last two weeks of October would make 2026-11-09 a real maturity date.
SELECT substr(rdate,1,7) AS month, COUNT(*) AS reports, COUNT(DISTINCT symbol) AS symbols
FROM reports WHERE rtype = :covered_rtype   -- the one value in predictionRtypes, forecast_vocab.go:18
GROUP BY 1 ORDER BY 1 DESC LIMIT 12;

-- Q3 (P1 rollout). The heal predicate, counted before it runs. It should be exactly 20.
SELECT COUNT(*) FROM prediction_marks
WHERE status = 'refused' AND refusal = 'no_bar' AND session_date = '';
```

Run the audience queries of §3.5 as well (V10).

### 0.3 Milestones

| Milestone | Beta cut target | Contents | What the owner sees | Gate to the next step |
|---|---|---|---|---|
| M0 | Mon 2026-10-19 15:00 CST | §0.1, P0 | Nothing changes on screen | — |
| **R1a** | **Fri 2026-10-23** | P34, P12, P1, P2, P3, P5, P36, P4, P9, P10 | The "1259 failed" line disappears. Waiting copy with "on or after" dates. The card shows errors and drops its false badge. Benchmark governed, with CSI 300 scheduled if wanted. Test runs out of the record. 2FA bypass closed. Evaluator status card. | ADR 0027 backup taken; Q3 = 20 |
| **R1b** | Fri 2026-10-30 | P6, P7, P16a; P8 if the V20 qfq capture exists by Tue 10-27, otherwise R1c on Fri 11-06 | Dry-mode sample of would-be writes | Dry-run gate (§3.1) |
| Evaluator on | Target ≤ Fri 2026-11-06 | — | First real checkpoints on the card: set 6's d5 (10-26), caught up from history | Readiness: R1a, R1b, P8 and P16a deployed; ledger start set; dry-run gate recorded; V18 recipients subscribed |
| **R2a** | Fri 2026-11-13 | P13 (docs), P15, P21, P26, P27, P28, P22 | A pilot OU's members open `/forecast`, showing today's views at first | V10, V12, V14 and V22 decided; publish readiness list (P26) |
| Pilot → Default | About Fri 2026-11-20 | — | Default OU members open it | One clean week |
| **R2b** | Fri 2026-12-04 | P14, P16, P17, P18, P19, P20, P37, P23 (with ADR 0038), P24, P25 | The demo look; the reading-page chip and timeline once the stage is `full` | P18 benchmark within budget; seeded-preview review signed off |
| R3 | Fri 2026-12-18 | P29 | — | R2b in production for two weeks |
| Tier 2 | Go/no-go no earlier than 2026-12-21 | P32, P33 | — | V19 entry criteria (10 live shadow sessions after R2b) |

The cut the owner sees first is R1a, about a week out. OU access cannot responsibly come sooner than R2a, for three reasons:
- It needs the owner's sign-off on a schema shape (V10).
- Compute governance must exist before any non-admin can trigger the 10 000-permutation scoring (C16).
- A compliance review is requested before publishing (V22).

**Model tiers by lane (global CLAUDE.md).**
- Opus: plans, ADRs and demo copy (P0, P11, P13, P22, ADR 0038), and the adversarial judge pass on every PR and on release readiness.
- Sonnet: every implementation lane.
- Haiku: read and grep fan-out inside those lanes.

---

## 1. Current state and the defects found

### 1.1 What `main` has

- **Ledger.** Migration step 0004 creates `prediction_sets`, `prediction_claims`, `prediction_marks` and `prediction_purges` (`migrate_steps.go:185-330`).
  - Terminal states (`resolved`, `refused_final`) and the baseline columns are write-once. The writes in `forecast_eval_store.go` are guarded by state.
- **Ingest.** Dify posts to `POST /api/v1/reports`.
  - `bench_code` is frozen from `forecast_bench` at insert (`apiv1.go:260`).
  - Marks are created `pending` for accepted sets only, with the same `bench_code` copied onto each (`forecast_store.go:80-83`).
  - A run over history is classified `backtest` at ingest (`forecast_ingest.go:364`). That is the precedent for classifying at insert.
- **Evaluator.** A one-minute loop, off by default (`forecast_auto`). It calls Tencent `fetchTencentRange` directly, uncached, under the shared upstream slot (`forecast_eval.go:209-240`).
- **Statistics.**
  - `GET /api/admin/forecast/groups` and `/members` require `PermManage`, which only the `admin` role holds (`routes.go:310-311`, `roles.go:22-30`). Settings are admin-only (`routes.go:306-307`).
  - There are two modes: monthly (official) and custom (exploratory). P-values come from label permutation, corrected by Benjamini–Hochberg within each cell.
  - Mature official cells are memoised in memory (`forecast_groups_api.go:162-167`). Maturity is decided on `time.Now()` (`forecast_groups_api.go:31`).
- **UI.**
  - `/manage/forecast` (`App.tsx:194`) holds `ForecastPage`, `ForecastMatrix`, `ForecastCellDrawer` and `ForecastSettingsModal`.
  - `PredictionPanel` sits under the report body on `StockPage`, and `PriceChart` has an overlay.
- **Read routes.** Two session routes (`routes.go:199,202`) and two v1 twins (`routes.go:181-182`) share one implementation (`forecast_read_api.go`).

### 1.2 Production on 2026-10-18 (read-only query), with corrections

- **Six sets**, all made on the evening of **Sunday** 2026-10-18, Shanghai time. The brief said Saturday. S0 is Monday 2026-10-19 under either S0 rule.
  - Sets 1–5 are repeated test runs of 600160: one rejected (`not_object`), one absent, three accepted (`no_trade`). Their reports were deleted, so `report_id` is NULL.
  - Set 6 is 600519, accepted and eligible.
- **All 20 marks** of the four accepted sets read `refused`, `no_bar`, attempts 1, `session_date=''`.
  - This is the normal result of a set's first tick (F06), not a vendor outage.
  - `attempts=1` also means the evaluator either ran for under an hour or was switched off after its first pass. While it is on, a fixed one-hour backoff raises the attempt count every hour (`forecast_eval_store.go:120-137`). Whether it is on now is open (V1).
- **The coverage line** reads 1259 reports with no prediction row, "(the ingest failed)" (`en-US.json:811`). These are all pre-ledger reports (B1).
- **Benchmark.** All six sets froze `sh000852`, and the setting is now `sh000300`. No set has been written since the change, so no cohort is mixed yet.
- **The matrix** shows only 2026-10, with every cell "month not over". That is correct, and it may stay nearly empty for months:
  - October's earliest possible d5 maturity is **2026-11-09**: a set made Friday 10-30 after the close has S0 Monday 11-02, and its d5 is the 5th session after that.
  - A cell also needs n ≥ 30. Whether October can reach it is unknown until Q2 (§0.2).

### 1.3 Defects

**A. Evaluator: these write wrong values into write-once columns.**

| ID | Defect | Evidence | First irreversible write if the evaluator runs |
|---|---|---|---|
| F01 | **The benchmark is fetched as the wrong instrument.** `benchMarketCode` strips the `sh` prefix and then asks `marketPrefix`, which sends every `0xxxxx` code to Shenzhen. The index branch below it is unreachable for these codes. So `sh000852` (CSI 1000) is fetched as `sz000852`, a Shenzhen-listed stock, and `sh000300` as `sz000300`. The session calendar and every `bench_ret_bp` come from that series. | `forecast_eval.go:630-661`, `names.go:224-242` | Monday 10-19 around 15:30: S0 for the four accepted sets is picked from that calendar. Monday 10-26 around 15:30: set 6's d5 `bench_ret_bp`. |
| F02 | **One benchmark per tick.** The calendar is fetched for `sets[0].BenchCode`, falling back to the *current setting* when that is empty, and every set's `bench_ret_bp` in the tick comes from it. Work is ordered `made_on, id`, so the oldest set's index scores every newer set. | `forecast_eval.go:124`, `:139-141`, `:425-430` | The first tick that settles a set whose frozen code differs from the batch head's. |
| F03 | **The baseline check compares nothing.** It returns `match` whenever the calendar has a bar on S0, and S0 was chosen from that same calendar. `emitted_base_fen` is never compared, so design §9.4 is unimplemented and `mismatch` can never occur. | `forecast_eval_store.go:168-183`, `forecast_eval.go:445-464` | Monday 10-19 around 15:30, for all four accepted sets. |
| F04 | **S0 ignores the time of day.** The code takes the first session strictly after `made_on`. ADR 0029 §4 says a report received before 15:00 on session T has S0 = T. | `forecast_eval.go:445-457`; ADR 0029 §4 table | Tuesday 10-20 around 15:30 at the earliest, for a report received Monday 10-19 before 15:00. |
| F05 | **dN records one session early.** With `sessions[0]` = S0, the code records `sessions[n-1]` (S0+4 for d5) but reports the mark due only once `sessions[n]` has completed, and `sessions[n]` is the ADR's d5. `TestCheckpointSessionIsSessionBasedAndH12IsCalendar` does not reach that boundary. The demo's resolved d5 follows the ADR: 2026-09-28 for a 09-17 report, with the 09-25 closure skipped. | `forecast_eval.go:344-365`; `forecast-verification-demo.html:130` | Monday 10-26 around 15:30: set 6's d5 would record Friday 10-23's close. ADR 0029 §4 means the 10-26 close. |
| F06 | **A mark that is not due is written `refused/no_bar`, and finality ignores outages.** Before S0 exists, every first tick fails `resolveStartBar`, and `DeferForecastSet` refuses every non-terminal mark. It writes no `session_date`, uses a fixed one-hour backoff and the wall clock. Marks that are not due are later skipped without a write, so they keep the refusal. Finality then fires without a settle attempt: a `refused` mark 10 or more sessions past due becomes `refused_final` directly. For the same reason, a vendor outage longer than two weeks, or a long off or dry period followed by switching on, finalises every due mark whose next attempt fails. | `forecast_eval.go:176-191`, `forecast_eval_store.go:120-137`, `:159-166`, `forecast_eval.go:47` | The 20 production marks already carry it. For them, finality without an attempt becomes possible after the 2026-11-06 close, 10 sessions after the code's d5 session. |
| F07 | **The evaluator polls, and starves newer sets.** Selection takes every accepted set with an unsettled baseline, unconditionally, and every set with any `pending` mark, with no time filter, `ORDER BY made_on, id LIMIT 50`. While on, it fetches the calendar plus one stock series per selected set *every minute*. After S0, every accepted set keeps a pending h12 mark for about 12 months, so the 50 oldest are fetched every minute and set 51 onward is never evaluated. The query drives from `prediction_sets` with a correlated EXISTS, so `idx_pred_marks_due` is never used. | `forecast_eval_store.go:39-53`; `CountForecastSetsDue` uses the same clause (`forecast_admin_api.go:63-69`); `migrate_steps.go:307-308` | Load: now. Starvation: from 51 active sets. |
| F08 | **A mark resolves with a nil benchmark return** when the benchmark has no bar on `base_date` or on the session. The statistics then treat its excess as 0, which the hit rate counts as a miss. | `forecast_eval.go:425-430`, `forecast_groups.go:236-238`, `:431-451` | The first due mark whose benchmark bar is missing. |
| F09 | **A stock halted from S0 can never settle.** `resolveStartBar` needs a traded bar on or after S0, and `settleMark` needs `bar.Date >= baseDate`. The set is deferred forever, its baseline never becomes terminal, and its cell stays pending forever. This contradicts design §9.3 ("Neither stays refused") and ADR 0029 §11 objection 4. | `forecast_eval.go:403-412`, `:445-464` | The first set whose stock is halted on S0. |
| F10 | **Corporate-action detection misses the kinds D8 sent to the qfq-ratio fallback.** The code uses the cash-dividend element plus a gap larger than the board's daily limit, and makes no qfq fetch; its comment says none is needed. A main-board 10-for-1 bonus issue (about −9.1 %) or a ChiNext/STAR 10-for-2 (about −16.7 %) stays inside the limit and is scored as a loss. ADR 0029 §10 and design §9.2 describe a qfq detector that does not exist. | `forecast_eval.go:466-528`; ADR 0029 §12 D8 | The first due mark whose window contains such an ex-date. |
| F11 | **Bar completeness is decided by today's wall clock for every body.** Between 00:00 and 15:30 the newest bar is always dropped, whatever its date, so a complete prior-day bar disappears overnight. A suspended stock is stamped 09:00 on the latest session (`testdata/quote/tencent_fqkline_bj830799.json`), so its last traded bar is always dropped, and a halted mark would settle on the bar before it. | `forecast_eval.go:209-240`, `:293-312` | A halted mark's first settle. |
| F12 | **Marks use a baseline recomputed on every pass, not the stored one.** `forecastWorkSet` has no S0 fields, and marks are written before the baseline. If the vendor revises bars between passes, a later mark uses a different base. | `forecast_eval_store.go:13-24`, `forecast_eval.go:170-199` | Any pass after a vendor revision. |
| F13 | **Provenance is asserted, not recorded.** `SettleForecastMark` writes `source` as the constant `quoteSourceTencent`. `prediction_marks.bench_code` is copied from the set at insert, so it records the *intended* index, never the series actually used; after F01 and F02 the two differ. The read path selects neither column. | `forecast_eval_store.go:144-157`, `forecast_store.go:80-83`, `forecast_read_store.go:300-305` | It holds today only because Tencent is the only source. It becomes false the day a fallback writes. |
| F14 | **The daily-limit backstop flags ordinary moves as corporate actions.** `dailyLimitBp` gives 20 % only to the `300` and `688` prefixes, so ChiNext `301xxx` and STAR `689xxx` get 10 %, and Beijing stocks (30 %) get 10 %. The threshold is a basis-point bound on close-to-close, but the exchange computes the limit *price* rounded to the fen. A limit-up day on a cheap stock therefore exceeds 10.00 %: ¥1.05 → ¥1.16 is +10.48 %, which `bpFromRatio` rounds to 1048 bp, and it is flagged. A listing's no-limit days are unmodelled, although the code comment assumes they cannot arise. A flagged mark is excluded, so big movers and low-priced stocks are under-sampled. | `forecast_eval.go:503-527`, `:598-611` | The first due mark whose window holds such a day. |

**B. Statistics and governance**

| ID | Defect | Evidence |
|---|---|---|
| B1 | `unrecorded` counts every covered report with no set row and has no lower bound, so every pre-ledger report counts. The copy also asserts a cause: "the ingest failed". | `forecast_groups_store.go:128-140`, `en-US.json:811` |
| B2 | The benchmark save has five problems:<br>• It accepts any code `benchMarketCode` accepts.<br>• It applies the code to the next ingest.<br>• It writes no audit row.<br>• It returns a non-English error message.<br>• The settings modal posts `{enabled, bench}` on every save, and the auto toggle is audited even when nothing changed. | `forecast_admin_api.go:42-56`, `ForecastSettingsModal.tsx:50`, `apiv1.go:260` |
| B3 | The statistics cannot see benchmarks: `groupSet` has no `bench_code`, so a cohort spanning two indexes would pool them. | `forecast_groups_store.go:13-42` |
| B4 | Test runs decide real membership. Under D4 the earliest accepted set per symbol per month decides, so the three accepted `no_trade` 600160 runs make any real October 600160 set a duplicate that is never scored. `forecastMembership` also drops any state it does not know without counting it, so a new state value breaks coverage arithmetic on an older binary. | `forecast_groups.go:115-154` |
| B5 | The members route has three problems:<br>• It recomputes the whole cell (10 000-permutation scoring) on every drawer click, uncached, only to read its state.<br>• It returns returns and excess for resolved members whatever the cell state, so the maturity gate holds only in the UI.<br>• Its `agree` applies no 5 % threshold and treats zero excess as disagreement, unlike the hit rate. | `forecast_groups_members.go:115-165` |
| B6 | The cell cache is not keyed by the purge marker, as ADR 0029 §8 requires. `PurgedPredictionMonths` has no non-test caller. | `forecast_groups_api.go:162-187`, `cleanup_store.go:390-392` |
| B7 | Several read fields are wrong or empty:<br>• `counted_set_id` is the earliest accepted set *of the report*, not the statistics' member, contradicting its OpenAPI description.<br>• `counted_in_group`, `baseline_bar` and `baseline_fen` are declared but never filled.<br>• `coverage.refused` is declared but never incremented. | `forecast_read_api.go:139-154`, `openapi.json:2008`, `forecast_read_store.go:52-53,70-72`, `forecast_groups.go:44-55` |

**C. Reading page**

| ID | Defect | Evidence |
|---|---|---|
| C1 | After a report switch, the previous report's sets stay on screen until the new request resolves. | `StockPage.tsx:191-211` |
| C2 | A failed request renders as "no forecast". | `StockPage.tsx:205-207` |
| C3 | Every mark that is not due reads "Fetch failed (retryable)". | `PredictionPanel.tsx:100`, `en-US.json:880` |
| C4 | "Not counted" appears on every set, because the field behind it is never filled. | `PredictionPanel.tsx:73-77` |
| C5 | The raw payload goes to every reader, external tenants included. | `StockPage.tsx:199`, `forecast_read_api.go:43-47` |
| C6 | "Other predictions" links use the receipt date. `/api/stock` falls back to the newest date when the date does not match. | `PredictionPanel.tsx:208`, `apiui.go:589-592` |
| C7 | The decision quality is shown as a raw identifier. | `PredictionPanel.tsx:81` |
| C8 | The chart drops a marker whose date has no bar (a Sunday report). When both overlay arrays are empty, it renders a stray `0`. | `PriceChart.tsx:825-829` |
| C9 | The "latest close" comes from the live snapshot, which may be intraday. | `StockPage.tsx:521` |
| C10 | The drawer link uses the raw `reports.kind`, not the folded kind `/api/stock` matches on, so a legacy or empty kind opens the wrong tab. | `forecast_groups_store.go:160-188` |

**D. Access, tenancy and authentication**

| ID | Defect | Evidence |
|---|---|---|
| D1 | The record is manager-only by design (design §12 D6). The SPA renders the matrix only after the admin-only settings call succeeds. The admin statistics paths have three consumers: `ForecastPage.tsx:98`, `ForecastCellDrawer.tsx:51` and `scripts/e2e_check.py:491-494`. | `routes.go:306-311`, `ForecastPage.tsx:63` |
| D2 | **Cookie sessions on `/api/v1` bypass forced enrolment (security).**<br>• The v1 routes are registered as `authBearer` (`routes.go:170`, `:90`), so they sit outside `sessionRoutes()` (`routes.go:100`), and `TestTheMachineSurfaceIsNotASessionClass` checks only the class.<br>• Yet `canQuery` admits a cookie session (`apiui.go:160-165`), never calls `gateEnrolment`, and `queryScope` applies that user's scope (`apiv1.go:405-407`).<br>• So an account held at the mandatory-enrolment wall can still read full reports through `GET /api/v1/reports/{id}` and every other `canQuery` handler: nine ungated call sites (`apiv1.go:103,411,479,524,541,562,582`, `forecast_read_api.go:24,74`). Only `/api/symbols` gates (`server.go:1079`).<br>• No product caller needs the cookie. The SPA never calls `/api/v1`, and the app bridge sends `credentials: 'omit'` (`AppView.tsx:85-96`). The only cookie consumer is e2e section C (`scripts/e2e_check.py:221-271,415`).<br>• ADR 0029 §5 says "/api/v1 is token-only", and that is false in behaviour. | as cited |
| D3 | *Found by reading the code; needs a failing test.* `isRestricted` reads leniently. `GetUser` returns nil on error, and the OU walk ignores errors, so any read failure reads as "not restricted", and every scope built on it fails open for that request. | `version.go:363-371`, `user_admin.go:439` |
| D4 | *Found by reading the code; needs a failing test.* `AddReportViewer` writes `g:<OU>` for any OU, restricted or not. A restricted reader's principals include every OU up their chain. Under a `visibility=group` version, a report requested by an account-restricted user in an unrestricted OU (Default, or a shared container) becomes readable by other tenants whose chains pass through that OU. | `version.go:373-392`, `report_owner.go` (`viewerScope`) |

**E. Why it looks far from the demo.** There are five causes, and only the first is styling:
1. Presentation pieces are missing (see Appendix A).
2. About ten statements on screen are false: C3, C4, B1, the dead "counted" badge, raw enums, and the progress bar on cells that can never progress.
3. The server does not send what the demo shows: pending reasons, membership, phase, report date, upside, source.
4. No production dataset can show the mature look for months.
5. The demo's own projected dates are inconsistent. Its d20 (10-23) and d60 (12-18) for the 09-17 report are `sessions[N−1]`, while the ADR gives 10-26 and 12-21. Its d120 (2027-03-26) assumes an exchange holiday calendar that a weekday projection (03-15) cannot reproduce. P13 corrects the demo, and V23 decides the calendar.

### 1.4 Corrections to the five inputs

1. 2026-10-18 is a Sunday.
2. `prediction_marks.bench_code` *is* written, at insert, from the set (`forecast_store.go:80-83`).
   - Two proposals said it is never written.
   - So the reading-page proposal's display gate, which shows excess only when the mark's `bench_code` equals the set's, protects nothing: the two are always equal.
   - The real protection is to ship F01 and F02 before any mark resolves. Per-mark provenance comes with Tier 2.
3. `sh000300` is misrouted as well, to `sz000300`, not only `sh000852`.
4. The first irreversible write is Monday's baseline (S0 and `base_check`), not the 10-26 d5 that one proposal's table gave.
5. While on, the evaluator already behaves like a poller (F07). ADR 0029 §11 refuses exactly that.
6. The qfq detector of ADR 0029 §10 and design §9.2 does not exist, and D8's settled fallback was never implemented.
7. The v1 forecast routes admit cookies (D2). The consequence is an enrolment bypass across the whole v1 read surface, not only an inaccurate sentence in ADR 0029.
8. The release-gate e2e script is a third consumer of the admin statistics paths, and the only consumer of cookie-on-v1.
9. Two proposals both claimed ADR 0037 and migration 0005. This plan assigns them as follows:
   - ADR 0037 operations, ADR 0038 market data, ADR 0039 readers;
   - migration 0005 `forecast_view`, migration 0006 Tier 2 provenance.
10. The daily-limit table is wrong beyond ST stocks (F14).

---

## 2. Cross-workstream conflicts and their resolution

**C1. OU scoping vs caching vs immutable cells.**
- **Positions.**
  - Access proposal: one global record; the OU decides who may see it, never what is in it.
  - UI proposal: thread a cohort-scope parameter through every loader now.
  - Governance proposal: any per-OU population must be frozen at insert, and n ≥ 30 is not an anonymity floor.
- **Resolution.**
  - Phase 1 has exactly one population per (report type, group). Cache keys, permutation seeds and immutability are untouched.
  - The access decision is computed per request, outside the record, and is never cached.
  - No scope parameter is added now. The group loader gains a filter struct, which the read path needs anyway for its symbol filter, but it gets no scope field.
  - A per-OU or per-tenant population is Phase 2. It must bring a key frozen at insert and its own cache key.
- **Why.** A population derived from live grants changes over time: grants are edited, `report_viewers` rows go on purge, and SSO rules move users. That breaks ADR 0029 §9. And a small OU's population is a per-person score, which ADR 0029 §11 refuses.

**C2. Multi-source vs one price basis vs provenance.**
- **Resolution.**
  - Tier 1 has one writer, Tencent, chosen through the shared policy, so every set has one price basis by construction. `source` is recorded from the response (F13).
  - A second writer arrives only in Tier 2, one leg at a time:
    - **Benchmark leg first.** Indices have no corporate actions. Both endpoints of a benchmark return come from one series.
    - **Stock leg.** Only from a source that reproduces `s0_fen` to the fen on `base_date` and carries corporate-action evidence, its own or from a separately granted evidence source (§3.3).
  - The provenance step (0006) ships with Tier 2, before any second writer exists.
  - After Release 1b, no settlement-logic change ships without that step.
- **Why.** A return computed across two vendors' prices is not a return. A provenance column added after mixed writes cannot be back-filled honestly.

**C3. Shared source order vs a separate forecast order.**
- **Resolution.** Use the shared `quote_source_order`, restricted to sources with an explicit `settled` grant. The empty-capability wildcard never applies to ledger intervals.
  - The derived ledger chain is printed on both panels.
  - Ledger health is counted separately from live health.
  - Saving an order that would remove the ledger's primary asks for confirmation.
  - Order saves are audited.

  The governance proposal's concern about provenance is met by the per-row `source` and the audit, not by a second editable list.
- **Why.** An operator who removes a vendor must stop every call to it, the evaluator's included. And two editable lists can disagree.

**C4. Where the reading page gets its data.**
- **Positions.**
  - Reading-page proposal: a new parallel route, `GET /api/stock/{symbol}/forecast`, keyed like `/api/stock`.
  - UI proposal: a summary inside `/api/stock` and `/api/run`.
- **Resolution: a hybrid.**
  - The header summary and the timeline and tab hints ride on `/api/stock` and `/api/run`. They render above the body, so arriving in the same response means no layout shift and no protocol for detecting a mismatched selection.
  - The full card below the body comes from the existing per-report session route, keyed by the report id that `/api/stock` returned. There is no second selection and no stale-report race.
  - Ledger work on `/api/stock` runs only when the reader stage is `full` and the report type is covered.
  - **Prefetch.** A request carrying `X-Prefetch: 1` (`prefetch.ts:47`) omits the per-report summary but keeps `timeline[].predicted`, which is one indexed query per symbol. The neighbour prefetch (`StockPage.tsx:236-242`) therefore never triples the summary work.
    - A warm render reserves the chip's slot when the selected date's flag is true.
    - The real request, which `StockPage` always issues (`StockPage.tsx:118-133`), fills the slot. So nothing shifts and no stale phase stays on screen.
  - A ledger error omits the summary and never the report.
  - The PR records a before/after benchmark of `/api/stock` that covers both the real and the prefetch requests.
- **Why.** A parallel route runs `NewBySymbol` twice per view on the same single SQLite connection (`store.go:104`), and it needs a mismatch protocol. Neither is needed here.

**C5. Reading page vs the manage page.**
- **Resolution.** One cohort view component with two mount points, each with its own job:
  - `/forecast` is the record: OU-governed, and its body is the demo's cohort view.
  - `/manage/forecast` is evaluator operations, admin-only.

  Links run both ways:
  - The card links to its cohort cell ("view in the record"), shown only when `/api/me.forecast_view` is true.
  - Drawer rows link to the report (`#forecast`).

  The placement is the owner's call (V12), made with a mock.

**C6. Voiding test data vs append-only.**
- **Resolution, for the past.** A write-once, bounded **ledger start** boundary, stored in `meta`.
  - No rows are touched.
  - Sets before the boundary go to a `pre_live` coverage bucket.
  - **The boundary is a statistics classification only.** `pre_live` sets are settled like any other accepted set, so every operational path (selection, due counts, heartbeat, watchdog, purge, reading-page phase) needs no special case. The cost is the vendor calls for four test sets.
  - Setting the boundary is refused once any official cell of the first cohort is mature. It must also be set before the evaluator is switched to `1`.
- **Resolution, going forward.** A test run is classified at ingest and frozen on insert (V24), like `backtest`. It is outcome-blind because it is decided before any price exists. A one-time boundary cannot cover the next Dify test run.
- **Rejected.** Per-set voids after the fact, including the reserved `excluded_test` status:
  - A void decided after S0 is not outcome-blind: the reading page already shows the post-S0 price path.
  - Voiding a symbol's earliest set either promotes a rerun, which D4 forbids, or drops the symbol, which does not help the 600160 case.

**C7. Fix at the source vs read around legacy rows.**
- **Resolution: both, with separate roles.**
  - The evaluator never refuses a mark that is not due.
  - A one-shot heal returns the 20 production marks to `pending`. This is a guarded transition between non-terminal states, and it is counted.
  - The read path still derives `phase` from stored fields through the shared schedule function. Readers need "on or after" dates and an `overdue` state, and the derivation must be correct for any row shape.
  - "Retrying" is shown only when there is evidence of an attempt (`session_date` set).

**C8. Mixed benchmarks: refuse the cell, exclude the minority, or gate labels.**
- **Resolution.** Prevent mixing at the source: effective-dated changes, a closed allowlist, and reconciliation at boot.
  - The defensive rule is `off_benchmark`. A cohort's benchmark is the code of its earliest in-population accepted set. Sets with another code are excluded and counted. If a symbol's earliest set is off-benchmark, the symbol is out for that group, as with `degraded`.
- **Rejected.** `mixed_bench` as the rule. One stray set would blank a whole cohort's metrics.
- **Labels.** Each group shows its frozen code. That label is true once F01 and F02 ship, which happens before any mark resolves.

**C9. Halts: stamp evidence vs a 3-session confirmation; a terminal baseline vs a pre-S0 base.**
- **Resolution.**
  - A halt resolves `halted=1` on the last traded close in either of two cases:
    - the vendor's stamp date has reached the due session (evidence);
    - three completed sessions have passed with no newer bar (confirmation). A safety valve suspends confirmation for a pass when more than 20 % of that pass's stock series lag the calendar.
  - A stock that did not trade on S0 takes its base from its last traded close before S0 (V6b). Two rules make that sound:
    - **The benchmark leg always starts at S0's index close**, never at `base_date`. Otherwise the excess return would include index moves from before the report arrived.
    - **The look-back is bounded but wide.** If the default window (`made_on − 10` days, `predReferenceLagDays`) holds no pre-S0 bar, one extra fetch reaches back 400 calendar days. With none there either, the set follows the terminal-baseline path.
  - A terminal baseline (`base_check='refused_final'`, marks `refused_final` with code `no_base`) remains for baselines that still cannot be established 10 sessions after S0.
- **Why.** ADR 0029 §11 objection 4 requires halts and delistings to resolve, not refuse. The terminal baseline stops an unresolvable set from blocking its cell forever, and it counts toward the 5 % cap.

**C10. A fallback without corporate-action evidence: refuse, or store `corp_action=2`.**
- **Resolution.** Refuse with `no_corp_evidence` and retry. There is no third exclusion class. In Tier 1 this cannot fire.
- **Why.** A mark without evidence is not a scored outcome, and a new class would need its own disclosure and maturity rules.

**C11. Rollout switches.** The proposals had three overlapping controls: a reading-page kill switch defaulting on, a reader-UI switch defaulting off, and a publish switch.
- **Resolution.** Three audited settings, each with one job (§6.2):
  - `forecast_reader` = `off|card|full`, defaulting to `card`, which is today's footprint. The step to `full` is a named rollout step in P20.
  - `forecast_record_published`, defaulting to off.
  - per-OU `forecast_view`, defaulting to deny.

  Admins always see everything, so problems can be diagnosed. Admins also see the `full` stage before readers do, which gives a natural observation period.

**C12. Which set the card shows, and what `counted_set_id` means.**
- **Resolution.**
  - `display_set_id` is the latest accepted set *of this report row* whose body hash matches the current body. If none matches, it is the latest accepted set, shown with the body-changed alert. It does not depend on membership, so the display never jumps when membership changes.
  - `counted_set_id` is corrected to its documented meaning: the report's set that is its cohort's member, 0 when none is, and omitted for restricted callers. The release notes say so.
  - The second "primary" id one proposal suggested is dropped. No runtime consumer exists outside the SPA in either repository.

**C13. Agreement vocabularies.**
- **Resolution.** One exact-integer predicate, shared by `hitStats` and every per-row verdict. It has four states: `agree`, `disagree`, `flat` (zero excess), and `untested` (|upside| < 5 %).
  - agree ÷ (agree + disagree + flat) equals the cell's hit rate.
  - The "no benchmark" miss reason disappears, because F08 is fixed at the source: a mark without a benchmark return is refused `unverifiable`, never resolved.
  - It ships in Release 1b (P16a), before any cell can mature.

**C14. Who loses the raw payload: scoped readers only, or every non-admin.**
- **Resolution.**
  - Withhold the payload from every restricted reader, now (P10).
  - Unrestricted readers keep it, in a collapsed "raw contract" section.
  - Non-accepted sets and per-set errors become admin-only (P18).

**C15. Scheduling: wake-up times vs fixed full passes.**
- **Resolution.** Wake-up times on marks, using the existing `next_attempt_at` column, with the selection driven from `idx_pred_marks_due(status, next_attempt_at)` (§3.1).
  - From the governance proposal it adopts one rule: a calendar that does not yet show the due session is not a refusal. The mark wakes again every 10 minutes until 18:00 CST, then at 09:00 the next day.

**C16. Compute governance vs a wider audience.**
- **Resolution.** Before any non-admin can reach the statistics:
  - the members route scores nothing (P16a, Release 1b);
  - custom mode gets its own computation pool, single-flight and per-user limits;
  - official mode's cold-cache scoring gets a separate pool.

  The pools and limits ship in the access PR, behind the publish switch.

**C17. Report editions (ADR 0024) and the card.**
- **Resolution.** Each edition is its own report row and carries its own sets; `prediction_sets.version` is frozen (`migrate_steps.go:228`).
  - The card on an edition shows that edition's own sets.
  - An edition with no set says so, and links to a sibling edition's set when the viewer can read that edition.
  - Membership keeps D4 (earliest accepted set per symbol per type and month, across editions). For unrestricted readers, when the member is a sibling edition's set, the badge reads "counted on edition X", not "duplicate". Restricted readers get no membership field at all (V13b).
  - Switching edition with `VersionSwitcher` swaps the card under the C1 stale-state guard.
  - Whether non-default editions should carry predictions at all is V25.

**C18. Cookie on `/api/v1`: shape it, gate it, or remove it.**
- **Resolution.** Remove it (V21, P34): `/api/v1` becomes token-only in behaviour.
  - e2e section C moves to the session routes that read the same reports (`/report/{id}/md`, `/api/report/{id}/versions`).
  - The "cookie on a v1 route" credential shape disappears from §3.6.
  - ADR 0029 §5's sentence becomes true instead of being amended.
- **Why.** It is the smallest change that closes D2. It is what the route class and its test already claim, and no product caller depends on the cookie.

---

## 3. Workstreams

### 3.1 Evaluator correctness and scheduling (P1, P3, P6–P9, P16a)

**Goal.** Every write-once value is correct. Vendor calls are made only for due work.

**Definitions (P1 introduces the helper; P6 sets the rules).**

```go
// predScheduleFor is the one statement of ADR 0029 §4's arithmetic. The evaluator passes the
// benchmark's completed sessions; the read path passes a projection (§3.7), so there every date is
// a lower bound ("on or after") unless the projection is calendar-backed ("expected").
type predSchedule struct{ S0, Record, Due string }

func predScheduleFor(cp string, madeAt time.Time, horizonStd bool, frozenS0 string, sessions []string) (predSchedule, bool)
```

- **S0.** The stored `s0_date` when there is one. Otherwise, the first session whose close is after `made_at`: received before 15:00 on session T gives T; otherwise the next session.
- **dN.** `Record = Due = sessions[N]`, where `sessions[0]` = S0. Today the code uses `Record = sessions[N-1]` and `Due = sessions[N]`.
- **h12.** `Record = Due` = the first session on or after `made_on` + 12 months, for standard horizons only.
- **Introduction.** P1 introduces the function with today's rules, pinned by characterization tests. P6 changes the rules inside it, per V4. The evaluator, the read path and the wake-ups all call it, and nothing else computes a checkpoint date.
- **Stored baseline.** Marks use the stored baseline (F12). The baseline and a set's marks are applied in one transaction, baseline first.

**Transactions on a single-connection pool (binding rule).** SQLite runs with `SetMaxOpenConns(1)` (`store.go:104`).
- Every write path in this plan follows two steps:
  1. Fetch and compute outside any transaction.
  2. Apply in one short transaction that uses only the `*sql.Tx` handle (the pattern of `insertIDTx`, `forecast_store.go:93-99`).
- No pool-level `s.query`, `s.queryRow` or `GetSetting` may run inside the transaction: on SQLite it deadlocks.
- No network I/O may run inside the transaction: it would block every reader.
- This covers P6's per-set apply, the heal, the benchmark reconciliation and the ledger-start save.
- Each of these has a SQLite test run under a 2-second context deadline, so a stray pool call fails fast instead of hanging.

**Settlement semantics (P7, P8).**

- **Baseline check, design §9.4 exactly (V5).**
  - The reference session comes from `made_at`, never from the payload.

    | Receipt (Shanghai time) | `match` when `emitted_base_fen` … |
    |---|---|
    | During a session (09:30 ≤ t < 15:00 on a session day; the lunch break counts) | … lies within that session's [low, high], or equals the previous session's close |
    | Outside a session | … equals the close of the last completed session at or before `made_at`, or of the session before it |

  - A halted stock's close for a session is its last traded close.
  - A missing emitted price is `mismatch`.
  - `emitted_base_as_of` is a diagnostic only.
- **Halts (V6).** These follow C9, including the benchmark leg from S0 and the 400-day look-back.
- **Missing benchmark bar.** A mark is refused `unverifiable` (F08). It never resolves with a nil.
- **Completeness (F11).**
  - A bar dated before the vendor stamp's date is final.
  - The newest bar is dropped when it is dated today before 15:30, or when it carries a stamp on its own date before 15:00.
  - With no stamp, bars dated before today are final, and today's bar is refused `unverifiable`.
- **Self-consistency checks on Tencent's own bodies.**
  - The benchmark body must report `Kind=="index"`, else `not_index`.
  - Index bars must have volume > 0, else `calendar_mismatch`.
  - Stock bar dates in [`base_date`, session] must be a subset of the index dates, else `calendar_mismatch`.
  - The stored `s0_date` must be reproduced by the calendar, else `calendar_mismatch`.
  - The stock body's close on `base_date` must equal the stored `s0_fen`, else `anchor_mismatch`. This also detects an adjusted body.
- **Daily-limit backstop, corrected (F14, P7).** The backstop must never flag an ordinary trading day. Its misses are left to the qfq detector.
  - **Limits by board:**
    - `300`/`301` (ChiNext) and `688`/`689` (STAR): 20 %;
    - `marketPrefix == "bj"`: 30 %;
    - everything else: 10 %.
  - **Limit price, as the exchange computes it.** A day is flagged only when its close lies outside [round(prev × (1 − limit)), round(prev × (1 + limit))] by more than one fen, with each bound rounded to the fen. A basis-point threshold is not used.
  - **No-limit days.** The first five bars of a series that begins inside the fetch window are treated as a listing's no-limit days and are never flagged.
  - **ST stocks are not modelled.** The main-board risk-warning limit was changed in 2025. The implementer verifies the current rule text against the exchange's publication before relying on any ST figure. Until then, ST bonus issues are left to the qfq path.
- **Corporate actions, D8 as decided (P8, V7).**
  - Keep the dividend element and the corrected backstop.
  - Add `fetchTencentAdjustRatio` (ADR 0029 §10), used for detection only and only for due marks. It flags a corporate action when `qfq_close / raw_close` differs between `base_date` and the session by more than the rounding error of two fen-rounded prices, so rounding alone never flags.
  - Its numbers are never stored, scored or displayed.
- **Refusal vocabulary.** A closed Go enum in `forecast_vocab.go`, in two classes:
  - **Mark-specific:** `no_bar`, `stale_bar`, `unverifiable`, `anchor_mismatch`, `calendar_mismatch`, `bad_window`, `no_base`.
  - **Source failure:** `fetch_failed`, `no_bench`, `not_index`, `no_source`.
  - Codes are shown on manager surfaces only.
  - Readers see phases: waiting, settling, retrying, overdue, resolved, final.
- **Source.** `source` is written from the response, not from a constant (F13).

**Scheduling (P1).**

```sql
-- due work only, driven from idx_pred_marks_due(status, next_attempt_at)
SELECT ps.id, ... FROM prediction_sets ps
JOIN (SELECT DISTINCT set_id FROM prediction_marks
      WHERE status IN ('pending','refused') AND next_attempt_at <= ?) due
  ON due.set_id = ps.id
WHERE ps.state = 'accepted'
ORDER BY ps.made_on, ps.id LIMIT 50
```

- A new mark's `next_attempt_at` is `''`, which sorts before every RFC3339 instant, so it needs no separate branch.
- `CountForecastSetsDue` uses the same subquery.
- `TestDueSelectionUsesTheDueIndex` asserts the plan on both drivers (`EXPLAIN QUERY PLAN` on SQLite, `EXPLAIN` on Postgres).
- **Wake-ups.**
  - A pending mark's `next_attempt_at` is the earliest instant `predScheduleFor` could report it due, over a weekday projection, at 15:30 Shanghai time.
  - While the baseline is unsettled, a mark wakes at the earlier of its own wake-up and the baseline's (S0's close).
  - A new set has `''`. It is selected once and scheduled without a vendor call.
- **Not due is never a refusal.**
  - If a mark wakes and the real calendar does not yet show its due session, it wakes again in 10 minutes until 18:00 CST, then at 09:00 the next day. Nothing is refused.
- **`DeferForecastMarks(marks, code, session, now)`.**
  - It touches **due** marks only, and writes `session_date` and the evaluator's clock.
  - A **mark-specific** refusal increments `attempts` and backs off with `predRetryBackoff(attempts)`: 1 h, 6 h, 24 h, then 24 h.
  - A **source failure** does not increment `attempts`. It wakes again in 10 minutes, then hourly.
- **Finality (V26).**
  - A mark becomes final only when it is 10 or more sessions past due **and** it has at least 4 mark-specific failed attempts. By the backoff schedule, those attempts span at least 31 hours.
  - A vendor outage, an empty ledger chain, or a long off or dry period therefore never finalises a mark.
  - `MarkRefusedFinal` accepts `pending` or `refused`.
- **Terminal baseline.** If the baseline is due and still failing with mark-specific refusals 10 sessions after S0, `base_check` becomes `refused_final` and every non-terminal mark becomes `refused_final` with `no_base`.
  - An older binary reads such a set as a member with final refusals, which is the same meaning.
- **One-shot heal.** It runs once, recorded under the meta key `forecast_state_repair_premature` together with its count, and is audited as `forecast.repair` with actor `system`.
  - Its predicate: `status='refused' AND refusal='no_bar' AND session_date=''`, **and** the mark is not due under the fixed schedule (checked in Go).
  - Matching marks become `pending` with `attempts=0` and `refusal=''`.
  - Q3 (§0.2) counts the candidates beforehand. In production the heal should touch exactly 20 marks.
- **Bounded work.** A processed set always leaves the due window: it settles, is rescheduled, or backs off. So `LIMIT 50` cannot starve set 51.

**Modes (P9).**
- `forecast_auto` takes `0`, `dry` or `1`. An older binary treats anything other than `1` as off.
- **Dry mode** plans every write and writes none to the ledger. The heartbeat carries a sample of would-be writes, each with its fetch `(market, code, source)`.
- `POST /api/admin/forecast/evaluate` runs one pass now, in the current mode.

**Dry-run gate (V1).** Once Release 1b is deployed, an admin checks, for each benchmark in use:
- one would-be d5: the stock and index closes on `base_date` and on the session, against exchange-published closes;
- that the fetch is recorded as `sh/000852`.

The mode moves to `1` only when the whole readiness list holds:
- the gate above has been recorded;
- P8 and P16a are deployed;
- the ledger start is set;
- the alert recipients (V18) are subscribed.

**Single writer.** The guarded writes already make overlapping passes safe, and the in-process `running` flag prevents overlap. A Postgres advisory lock is deferred until more than one replica exists (V18 asks whether the deployment is a single replica).

### 3.2 Data governance (P2, P4, P5, P36)

**Ledger start (P5, V3).** Stored in `meta` as `forecast_population_from` (RFC3339 UTC).
1. **Write-once.** Only an admin can set it, only once. The save is audited as `forecast.population`, with the excluded set ids and counts by state.
2. **Bounded.** The value must be at or before `populationBound`: the first 15:00 Shanghai time on a weekday strictly after the ledger's earliest `made_at`.
   - It needs no exchange calendar. A holiday only moves the bound earlier, which is the conservative direction.
   - For production the bound is **2026-10-19T07:00:00Z**.
3. **Refused once mature.** The save is refused if any official cell of the first cohort is mature.
4. **No selection by state.** Every set before the instant is `pre_live`, whatever its state, and is shown as its own coverage count.
5. **Statistics only.** `pre_live` is a membership status and nothing else. Such sets are evaluated, counted and purged like any other set. On the reading page, a `pre_live` set shows its real phases and reads "not counted: before the ledger start".
6. **Readiness.** It must be set before the evaluator is switched to `1`.
7. **Survives a restore** (see the restore rules in §5).

**Test runs at ingest (P36, V24).**
- A wire field, `predictions.run_mode`, with the values `"test"` or `"production"`. An absent field means production.
- `test` is frozen at insert into `state='test'`: the set is stored and listed apart, gets no marks, is never scored, and has its own coverage bucket.
- The classifier stays pure, as `classifyPredictions` is today (`apiv1.go:246-261`).
- **Order of shipping.**
  - The portal side ships in Release 1a. The coverage bucket and reader are therefore in production before any producer sends the value.
  - The Dify-side change follows after deploy: the test app sends `run_mode: "test"`, and the projection stops hard-coding `emitter.app = "1-6-4"` (Dify repo, `ops/forecast-projection/projection.py:222`) in favour of the real app identity.
- **Older binaries.** One that predates P36 drops `state='test'` from every bucket (B4), so its coverage would not add up by the number of test sets. That is a display gap on rollback, not wrong data, and the release note says so.

**Benchmark governance (P4, V8).**
- **Closed allowlist in Go.** `sh000300` (CSI 300), `sh000905` (CSI 500) and `sh000852` (CSI 1000). Each is an explicit `code → (market, code)` entry with a committed fixture, never inferred by `marketPrefix`.
- **Effective dating.**
  - A schedule during cohort M stores `forecast_bench_next` with `forecast_bench_next_from` = M+1, unless the ledger holds no in-population set in M; then it applies at once.
  - Ingest uses `ForecastBenchFor(cohortOf(made_at))`.
  - The pending value is promoted at boot and by the daily housekeeping.
- **Explicit intents on the settings route.**
  - `bench_schedule: <code>` schedules a change, and `bench_cancel: true` cancels one. Re-posting the active value is a no-op, never a cancellation.
  - The settings form posts only the fields that changed.
  - `enabled` is accepted as an alias of `mode` (`true` → `1`, `false` → `0`) through Release 1b, and removed in Release 2b.
  - Every field writes an audit row only when its value actually changes.
  - e2e section G (`scripts/e2e_check.py:497-500`) is updated: it posts `mode` alone, and asserts that a no-op save writes no audit row.
- **Boot reconciliation (Release 1a, once).** If the active setting differs from the frozen code of the current cohort's earliest in-population set:
  - the active value goes back to the frozen code;
  - the admin's value becomes a change scheduled from M+1;
  - both are audited with actor `system`.

  In production this turns `sh000300` into "CSI 300 from 2026-11", which the admin can cancel, even if action 2 of §0.1 was missed.
- **Statistics.**
  - The `off_benchmark` rule from C8 applies.
  - Custom ranges that span cohorts with different benchmarks are sliced by benchmark.
  - Every group row carries its `bench`.
- **Audit.** `forecast.bench` records `{from, to, effective_cohort}`, `{cancelled}`, or `{reason: reconcile|promote}`.

**Bounded "unrecorded" (P2).**
- **The floor.** It is captured once, at Release 1a's first boot, and re-derived after a restore.
  - Take the ledger's start instant: `meta['mig:0004']`, which the step ledger writes as an RFC3339 timestamp (`migrate_steps.go:647`).
  - `floor_id` is one less than the smallest report id among `report.ingest` audit rows (`apiv1.go:299-309`) that are at or after that instant and carry `created=true`. If there is no such row, it is `MAX(reports.id)`.
  - The floor is stored with the method that produced it.
  - Because it is captured once, later audit retention cannot move it.
- **The count.** Covered reports with `id > floor_id`, a non-manual version, and no set row.
- **Alongside it:** `store_failed_30d`, read from the audit log, which also covers overwrites of older report ids.
- **Copy.** "{n} covered reports since {date} have no prediction row · store failures (30 days): {m}", linking to the audit filter.
- **Webhook.** A new event, `predictions.store_failed`, registered in `availableEvents()` (`webhook_api.go:15-17`).
- **Visibility.** Admins only.

**Delete audits.** The delete paths that null `report_id` (`store.go:1924`, reached from `apiv1.go:496` and the report editor) add `detached_set_ids` to their audit detail.

### 3.3 Market-data sources (P23–P25; Tier 2 in P32–P33)

**What fallback can and cannot buy today.**
- A stock mark counts only with corporate-action evidence, and today only Tencent carries any (its cash-dividend element, plus the qfq ratio from P8).
- Sina carries no such evidence and does not report the instrument kind.

So with today's sources, a second writer adds **zero** scored stock marks in every outage scenario. Tier 1 therefore delivers the control and visibility half of the owner's requirement. The availability half is bought differently:
- **V26:** an outage pauses settlement and never finalises a mark.
- **Tier 2's benchmark leg:** removes the index as a single point of failure.
- **A source with corporate-action evidence, or a licensed feed:** is what would remove Tencent as the single point of failure for stock marks.

**Candidate sources.**

| Source | In the portal | Unadjusted daily bars | Index bars and identity | Corporate-action evidence | Licence |
|---|---|---|---|---|---|
| Tencent | Shipped; the ledger's primary | Yes | Yes; reports `Kind` | Cash-dividend element; qfq ratio (P8) | None: undocumented public endpoint |
| Sina | Shipped | Count-based kline, 800-bar cap | Index codes, but no kind | None | None |
| Yahoo | Compiled in, opt-in, off by default (`quote_yahoo.go:762`) | Chart API; A-share `.SS`/`.SZ` coverage unmeasured | `instrumentType`; `quoteYahooKind` maps only `EQUITY` (`quote_yahoo.go:747-752`); index identity unmeasured | `events=div,split` (splits would cover bonus issues) and `adjclose`; neither used today, A-share accuracy unmeasured | None: the file's own header calls it undocumented and unlicensed |
| EastMoney | Not implemented | Unmeasured | Unmeasured | Publishes ex-rights data; unmeasured | None known |
| A licensed vendor feed | Not implemented | Yes, by contract | Yes | Yes, by contract | Paid; a procurement decision. The only route to a corroborated, citable record. |

Every "unmeasured" cell becomes a `quote-probe` capture item (V20) before anyone relies on it.

**Tier 1.**
- **Capabilities.** Two new intervals, `settled` (ledger-grade daily bars that may be written) and `settled_shadow` (compared, never written).
  - Each is granted only explicitly: `servesExplicit` ignores the empty-capability wildcard (`quote_cache.go:284-292`).
  - `quoteSource` gains two flags, `cashDividends` and `reportsKind`. Both are true for Tencent and false for Sina and Yahoo.
  - `fetchUnder` refuses both intervals, so a ledger body can never be cached.
- **Chain.**
  - `ledgerChain` is `quote_source_order` restricted to `settled` grants, with cash-dividend sources first.
  - `ledgerPrimary` is the first chain member with both `cashDividends` and `reportsKind`. If there is none, the walk returns `no_source`, a source failure that pauses settlement, and both panels raise an alert.
- **`walkLedger`.**
  - It is uncached, runs outside single-flight, and holds one upstream slot for the whole walk.
  - It takes only a window built by `quoteParseWindow`, so a malformed `made_on` becomes `bad_window` with no vendor call.
  - It keeps a separate `ledgerHealth`. Typed ledger refusals and context cancellation never count as vendor failures.
  - A per-tick breaker stops calling a source after 3 failures.
  - The stored refusal is the highest-priority called source's outcome.
- **Sina in shadow.**
  - A new `fetchSinaRange` trims a count-based kline to the window.
  - It returns `window_not_covered` past the 800-bar cap, treats `BarsUnavailable` as an error, and requires the newest kline close to cohere with the snapshot.
  - The comparator takes at most two bodies already accepted from the primary per working tick. It compares OHLC to the fen, the date sets, and both anchors, and never touches ledger tables.
  - The results persist in `meta`: `forecast_state_shadow` and `forecast_state_calls`.
  - `forecast_fallback` takes `off` or `shadow`. The value `on` returns 400 until Tier 2.
- **`report-portal quote-probe capture|compare` (P24).** The owner runs it, because it contacts vendors.
  - It writes raw bodies and a manifest, and never writes the database.
  - `compare --record` replays the same comparator over a fixed panel: `sh000852`, `sh000300`, 20 stocks across boards (including `301`, `689` and Beijing codes), and any supplied suspended codes.
  - It accepts Yahoo and any other compiled-in source as a candidate.

**Tier 2 (a separate go/no-go, V19).**
- **Entry criteria, read from persisted statistics:**
  - at least 60 replayed sessions and 10 live shadow sessions, with zero unexplained fen disagreements on closes and anchors;
  - for the stock leg, measured corporate-action evidence;
  - for the benchmark leg, an identity check (`reportsKind`, or a name check against the allowlist).
- **A separate corporate-action evidence chain.**
  - An evidence source declares a `corpEvents` capability and may serve evidence without serving bars.
  - A Tier 2 stock leg may then pair bars from one explicitly granted source with evidence from another, covering the same window. For example: Sina bars with Yahoo split events, once both are measured.
  - Each pairing is recorded in the Tier 2 provenance columns.
- **Rules, starting with the benchmark leg:**
  - each benchmark return uses two closes from one series;
  - `s0_fen` is established only from the primary;
  - a stock-leg body must reproduce `s0_fen` to the fen;
  - no new exclusion class.
- **Schema.** One owner-signed step, 0006 (§5).

### 3.4 Statistics read semantics (P12, P16a, P16, P17)

- **Golden snapshots first (P12, Release 1a, before P4, P5 and P36).**
  - **The cell snapshot.** It records `computeForecastCell` for every checkpoint, plus slices and coverage, over a fixture that includes duplicates, a degraded first set, a pending baseline, zero excess, small upside, a nil terminal, a corporate-action mark and two policy versions. It is committed while passing on `main`. Every later PR leaves it byte-identical.
    - The fixture holds no `pre_live`, `test` or `off_benchmark` sets, so P5, P36 and P4 leave it unchanged and bring their own tests.
    - If P16a's integer predicate disagrees with the float one on any fixture member, that disagreement is a float defect. It is regenerated in its own reviewed commit that explains each changed byte.
  - **The drawer snapshot.** It records the members list separately, because P16a changes the drawer's `agree` rule on purpose. P16a regenerates it in its own reviewed commit.
- **One membership classifier (P16).**
  - `classifyMembership(sets) []verdict` returns an ordered slice, never a map, because member order feeds the permutation p-values.
  - `forecastMembership` is rebuilt on it.
  - Statuses: `member`, `duplicate`, `degraded`, `mismatch`, `pending_baseline`, `pre_live`, `off_benchmark`. Sets with states `test`, `backtest`, `absent` and `rejected` are counted in their own buckets.
  - An unknown state is counted in an explicit `unknown` bucket rather than dropped, so coverage always adds up.
- **One hit predicate (P16a, V15).**

  ```go
  // predHitClass is the single hit-rate predicate, used by hitStats and by every per-row verdict.
  func predHitClass(terminalFen, s0Fen int64, excessBp int32) string {
      if s0Fen <= 0 { return dirUntested }
      d := terminalFen - s0Fen
      if abs64(d)*10000 < int64(predHitUpsideBp)*s0Fen { return dirUntested }
      switch {
      case excessBp == 0:            return dirFlat
      case (excessBp > 0) == (d > 0): return dirAgree
      default:                        return dirDisagree
      }
  }
  ```

  The subset test moves from float to exact integer arithmetic. It ships in Release 1b, and the evaluator cannot move to `1` without it (§3.1), so no cell can mature under the old rule.
- **Members route (P16a).**
  - The gate and the scoring are split, and the route scores nothing (B5).
  - Outcome fields are sent only when the cell is mature.
  - Later, in P16: `population=all` lists every member with its exclusion reason; the kind is folded through `repKind` (C10); link fields are sent only for reports the viewer can read.
- **Cache keyed by the purge marker (B6, P16).** Each request reads `prediction_purges` for the requested months. A purged month bypasses the cache and answers `purged`.
- **Read fields (P16).**
  - On sets: `report_date`, `upside_bp` (integer, truncated toward zero), `membership` (unrestricted callers only), `display_set_id`, and the corrected `counted_set_id`.
  - On marks: `phase`, `not_before` (with `expected` when the projection is calendar-backed, V23), `source` and `direction`.
  - `predMarkPhase` uses `predScheduleFor` over the projection, or the evaluator-published sessions when they cover the date. It returns:
    - `waiting_s0` or `waiting` while the lower bound is still in the future;
    - `settling` once that bound has passed;
    - `retrying` only when `session_date != ''`;
    - `overdue` 48 h past the exact due date, or 21 days past the lower bound;
    - `resolved` or `final`.
  - When the evaluator is off or in dry mode, a separate "settlement paused" notice is shown.
  - The OpenAPI enums are tested against the Go constants.
- **Explain fields (P17).**
  - Per non-mature cell: counts by phase, `failed_cap_exceeded`, and `not_before`.
  - A summary strip: since-date, sets, accepted, stocks, marks by phase, and "next result on or after".
  - `rtypes`, `min_n`, `limits`, `generated_at`, `ledger_start`, `capabilities` and `evaluator.enabled`.
  - `coverage.refused` is removed, because it was never populated.

### 3.5 OU-based access to the record (P21, P26, P27)

**The decision is one consistent read and one pure function.**

```go
// resolveForecastView is the rule itself, with no I/O, shared by the gate, /api/me and the admin preview.
func resolveForecastView(sub forecastSubject, ix forecastAccessIndex, published bool) forecastViewDecision {
    if sub.admin { return allow("admin") }                    // admins are never scoped
    if !published { return deny("unpublished") }
    chain, dangling := ix.chain(sub.group)                     // mirrors groupChainErr; Default appended
    if dangling { return deny("error") }
    if sub.restricted { return deny("restricted") }            // account flag, from the same row read
    for _, id := range chain { if ix.ous[id].restricted { return deny("restricted") } } // sticky OU flag
    for i := len(chain) - 1; i >= 0; i-- {                     // nearest OU wins
        if v := ix.ous[chain[i]].view; v != nil { return decision(*v, "ou", chain[i]) }
    }
    return deny("unset")                                       // Default NULL reads as deny
}
```

- **Two reads.** The account row (`role, restricted, group_id`) is one statement, and the whole `user_groups` table is another. An error in either is a denial.
- **No lenient helper.** The gate never calls `isRestricted`, `isAdmin`, `GetUser`, `groupChain` or `EffectiveGroupSettings`. A source-scan test enforces this.
- **No caching of the decision.** A user moved by an SSO rule gets the new decision on their next request.
- **Routes and the enrolment wall.**
  - `GET /api/forecast/groups` and `/members` are registered through the existing `s.session` helper, so they keep class `authSession`, are in `sessionRoutes()`, and are held by the enrolment wall (`security_policy.go:362`).
  - A `forecastReader` wrapper inside the handler applies the view decision. `routeRow` gains a `gate` annotation (`gate: "forecast_view"`) that records it, and no new `authClass` is introduced.
  - Tests assert three things: that every `/api/forecast/*` pattern is in `sessionRoutes()`, that it is not on the enrolment allowlist (`security_policy.go:276-288`), and that it carries the annotation.
  - These routes omit `unrecorded`. The admin paths remain as aliases until P29.
- **Group save.** `forecast_view` is presence-aware:
  - omitted leaves it unchanged;
  - `null` inherits, except on the Default OU, where it is stored as 0;
  - `true` on an effectively restricted OU returns 400 `forecast_view_restricted_ou`.
- **Admin preview.** `GET /api/admin/forecast/access` returns the decision and the number of accounts allowed, now and under a hypothetical value. It resolves every account that can sign in, so unassigned accounts and members of root OUs are counted.
- **Compute governance (C16).**
  - Only uncached scoring of a mature cell takes a slot.
  - There are two pools: official (default 2) and exploratory (default 2).
  - Identical requests share one computation on a detached context, using the hand-rolled flight pattern in `quote_cache.go:747`.
  - A request that waits longer than `wait_sec` (default 10 s) gets 503 `forecast_busy` with `Retry-After`.
  - Per-user limits: custom mode 20 a minute with one distinct query in flight; members route 60 a minute.
  - Every limit is a bounded setting on the existing limits form (`limits.go:170`).
- **Phase 2 (external tenants, V11).** Not in this plan. Any Phase 2 design must meet all of these:
  - a population key frozen at insert, never derived from live grants;
  - official monthly cells only, with coverage suppressed below a k;
  - no custom ranges, which are day-granular and allow differencing;
  - drawer rows only for readable reports;
  - an explicit amendment to ADR 0029 §5's fail-closed-after-purge rule for aggregates;
  - no population defined by a single tenant or person;
  - a compliance sign-off (V22).

**Access matrix.** This is the single statement of who sees what. In it, "card" means shown at reader stage `card` or `full`, and "full" means shown at stage `full` only. At stage `off`, non-admins see no reading-page surface.

| Subject | Card | Chip, timeline dots | Raw payload | Membership badge | Orphaned sets in "other predictions" | Non-accepted sets, errors | Record: `/forecast`, groups, members, CSV | `/manage/forecast` | `/api/v1` prediction reads |
|---|---|---|---|---|---|---|---|---|---|
| Admin | Always | Always | Collapsed | Yes | Yes | Yes | Always, published or not | Yes | Not with a cookie (P34) |
| Internal reader; OU allows; published | card | full | Collapsed | Yes | Yes, muted | No | Yes (exploratory tab per V14) | 403 | Not with a cookie |
| Internal reader; OU allows; unpublished | card | full | Collapsed | Yes | Yes, muted | No | 403 `unpublished` | 403 | Not with a cookie |
| Internal reader; OU denies or unset | card | full | Collapsed | Yes | Yes, muted | No | 403 `denied` or `unset` | 403 | Not with a cookie |
| Restricted account, or member of a restricted OU | card, readable reports only (V13d) | full (V13d) | No | No field | No | No | 403 `restricted` | 403 | Not with a cookie |
| Session at the mandatory-enrolment wall | 403 `enrolment_required` on every surface | | | | | | | | |
| Persistent machine token (`query`) | — | — | With `include=payload` | `counted_set_id` | Yes | Yes (machine shape) | No route | No | Yes |
| App-bridge token (minted only for unrestricted users, `apps_api.go:121-141`) | — | — | No | No | No | No | No route | No | Accepted sets only |
| Anonymous | 401 on every surface | | | | | | | | |

`TestForecastAccessMatrix` turns every row into table-driven assertions over every listed route: the status code, plus the presence or absence of each field. P26 introduces it for the record and the admin surfaces, and P18 extends it to the reading surfaces.

**Measure the audience before deciding (V10).**

```sql
SELECT COALESCE(g.name,'(Default)') AS ou, u.role,
       SUM(CASE WHEN COALESCE(u.restricted,0)=1 THEN 1 ELSE 0 END) AS account_restricted, COUNT(*) AS accounts
FROM users u LEFT JOIN user_groups g ON g.id = u.group_id
WHERE COALESCE(u.active,1)=1 GROUP BY 1,2 ORDER BY 1,2;
SELECT id, name, parent_id, COALESCE(restricted,0) AS restricted, is_default FROM user_groups ORDER BY id;
SELECT COUNT(*) FROM users WHERE COALESCE(active,1)=1 AND last_seen >= '<30 days ago>';
```

If almost no unrestricted non-admin reader exists, Phase 1 reaches almost nobody, and the owner should know that before choosing.

### 3.6 Reading-page integration (P10, P18, P20)

- **Hotfix first (P10, Release 1a):**
  - the stale-state guard (C1);
  - an error state with a retry link (C2);
  - interim phase copy from `predScheduleFor` (P1): a pending mark reads "waiting, on or after <date>", never "Fetch failed" (C3);
  - the dead "not counted" badge removed until membership exists (C4);
  - the quality enum translated (C7);
  - the PriceChart `0` fixed and markers snapped to the bar on or after their date (C8);
  - the server withholds `payload` whenever the caller's scope is non-nil (C5). After P34 that is the session routes only, because v1 callers are machines.
- **Server view (P18).**
  - `/api/stock/{symbol}`:
    - `r` now decides the date and the kind when it names a readable report;
    - a top-level `prediction` summary (display set, stance and weight, upside, next checkpoint with its phase and "on or after" or "expected", membership for unrestricted callers, sibling-edition link per C17);
    - `timeline[].predicted`, computed exactly from `SELECT DISTINCT report_id FROM prediction_sets WHERE symbol=? AND state='accepted'`, intersected with the viewer's already-scoped list;
    - all of this computed only when the stage is `full` (or the caller is an admin) and the type is covered;
    - the summary omitted on `X-Prefetch: 1` requests, with the timeline flags kept (C4).
  - `/api/run/{key}` carries the same summary.
  - The per-report session route returns the §3.4 fields.
- **Machine credentials on v1 (after P34).**

  | Caller | What it gets |
  |---|---|
  | App-bridge token | Accepted sets only; no payload; no errors |
  | Persistent machine token | The machine shape, plus the additive fields |

  A cookie alone on any `/api/v1` route gets 401.
- **Card (P20).** It matches the demo's single-report view:
  - meta line: report date, received, edition;
  - badges: membership (unrestricted only), baseline detail, translated quality, default-probability note;
  - "What the report said": stance and weight, horizon "(12 months, standard)", terminal with signed upside versus S0, p_profit, entry;
  - a scenario band with a cursor at the latest **completed** bar, mirroring `barIsComplete` through fixtures shared between Go and vitest;
  - checkpoint steps;
  - the checkpoint table: signed and coloured returns, bold excess, "touched / not touched (low X)", a four-state direction, source, and phase-driven rows for checkpoints not yet resolved;
  - "other predictions" as a table with exact links (`?date=<report_date>&kind=<repKind>&r=<id>#forecast`); orphaned rows are muted and shown to unrestricted readers only;
  - the payload collapsed (unrestricted readers only);
  - the disclaimer line (V22).
- **Chip.** It renders inside the report card from `prediction`, in the same paint as the body. It is either present or absent; on a warm render its slot is reserved (C4). It never shifts the body.
- **Overlay.**
  - The chart stays collapsed by default, and the toggle is always visible when an accepted set exists. Switching it on opens the chart for that view without changing the stored preference.
  - New optional props:
    - `windowFrom` (shading from S0);
    - `markers[].price` (a dot at the settled close);
    - `markers[].snap`;
    - staggered labels;
    - a legend with the caption "candles: live quote · dots: settled closes (source: X)".
  - With no props, the output is identical to today's.
- **Timeline and tabs.** A dot with an `aria-label` marks each date that has a forecast. The marks are exact, so the absence of a dot is a true statement.
- **Editions.** C17's rules.
- **Run page.** `RunPage` mounts the same chip and card, with no chart.
- **Stage rollout (`card` → `full`, owner as admin).** The owner flips `forecast_reader` to `full` when three things hold:
  - P18 and P20 are deployed;
  - P18's recorded `/api/stock` benchmark, real and prefetch requests together, is within the budget set under V18;
  - one week of production logs, with admins already seeing `full`, shows the `/api/stock` p95 within budget and no FO incident.

  To roll back, set `card` or `off`. The change is immediate and audited.
- **Not in scope.** Keeping the report type while stepping dates, and badges on home cards. These are follow-ups if wanted.

### 3.7 Demo parity and the UX bar (P13–P15, P19, P20, P37)

**Definition of done for every UI PR.**
1. Loading, empty, error and partial states for every component, each with a vitest.
2. WCAG 2.2 AA: text contrast ≥ 4.5:1, computed against both antd algorithms. Significance carries a glyph plus "q<0.05" text, never colour alone.
3. Layout works at 375, 768 and 1280 px, with no page-level horizontal scroll.
4. Dark mode by construction: a source scan forbids hex and rgba literals in forecast components.
5. All copy through `t()`, with zh-CN, zh-TW and en-US in parity.
6. No number without its n, no outcome for a cohort that is not mature, and no label the computation did not use.
7. Screenshots of the seeded preview (light and dark, 375 and 1280) are attached to the PR and saved under `~/Downloads/forecast-preview/` for the owner.

**Pieces.**
- **Production baseline first (P13 prerequisite).**
  - Screenshots of `/manage/forecast` and of the 600519 reading page, at 1280 and 375 px, light and dark, annotated against the demo. The owner captures them, or authorises a read-only browser session for the implementer; either way they are saved under `~/Downloads/forecast-baseline/`.
  - Appendix A's "Today" column is then corrected from what the screen shows.
- **Seeded preview (P14).**
  - A test-only tool drives the real ingest and the real evaluator with deterministic bars and a fixed clock.
  - It writes a new SQLite file plus a `config.yaml` beside it (listening on `127.0.0.1:8791`), and refuses to overwrite either.
  - It produces every Appendix A state.
  - The server gains a clock seam for this only. `RP_FORECAST_DEMO_NOW` overrides the groups route's `time.Now()` (`forecast_groups_api.go:31`) and the read path's phase clock. It is honoured only when `version.Version == "dev"`, the value an unlinked build carries (`internal/version/version.go:14`). A release build that sees the variable refuses to start.
  - The owner runs:

    ```bash
    go test ./internal/app/ -run TestWriteForecastDemoDB -forecast-demo-out="$HOME/Downloads/forecast-demo/portal.db"
    RP_CONFIG="$HOME/Downloads/forecast-demo/config.yaml" RP_FORECAST_DEMO_NOW=2027-06-20T07:00:00Z \
      go run ./cmd/report-portal
    ```
- **Demo revision (P13).** The demo HTML stays a local file (`docs/forecast-verification-demo.html`). It gains:
  - the states it lacks:
    - waiting with an "on or after" or "expected" date;
    - pending reasons;
    - final small cells;
    - `pre_live`, `test` and `off_benchmark` chips;
    - the evaluator status pill;
    - data-source states;
    - an edition without a set;
    - the disclaimer;
  - a correction of its projected d20 and d60 to ADR 0029 §4 (§1.3 E5);
  - the record-page placement mock for V12.
- **Palette (P15).** The first palette step that reaches ≥ 4.5:1 on its tint, asserted: light `red7`/`green8`, dark `red8`/`green6`, under the App seed `colorPrimary #1677ff`.
- **Projected dates (P37, V23).** Under V23 (a), a closed exchange-closure table in Go covers the current and next year and is updated each December from the exchanges' annual notice (a runbook entry). It is used only for projected dates, which are then labelled "expected", and never for settlement, which always uses the index calendar. A year missing from the table degrades to the weekday "on or after" projection.
- **zh-TW.** The `forecast.*` family is regenerated with a prefix option, and gated as a fixed point of OpenCC s2twp. It still needs a human reviewer.

### 3.8 Security and tenancy hardening (P34, P30, P31)

- **P34 fixes D2 (Release 1a, V21).**
  - `/api/v1` read handlers stop reading the cookie: `canQuery` is split into a token-only `canQueryToken`, which v1 uses, and today's behaviour, which only `/api/symbols` keeps, gated.
  - Written first, a behavioural test fails on `main`: a session at the enrolment wall reads a report through `GET /api/v1/reports/{id}`.
  - After the fix, `TestTheMachineSurfaceRefusesACookie` asserts that every `GET /api/v1/*` route answers 401 to a valid cookie alone.
  - e2e section C moves its external-reader assertions to `/report/{id}/md` and `/api/report/{id}/versions`.
- **P30 fixes D4.** OU principals are written and read only for OUs that are effectively restricted.
- **P31 fixes D3.** A failed read of a restricted flag scopes the request to nothing (`1=0`) instead of everything.

Each is written test-first. If a failing test cannot be produced on `main` for P30 or P31, that PR becomes test-only.

### 3.9 Compliance and data licensing (V22)

- **Why.** The record publishes a track record of investment recommendations to a wider audience. The card already reaches external tenants today. And the record's stored closes come from public endpoints with no licence (§3.3).
- **What the plan adds.**
  - A compliance sign-off (by the owner, or counsel the owner names) is a readiness item before the publish switch is first flipped, and again before any Phase 2 design.
  - A disclaimer key, `forecast.disclaimer`, in all three locales: shown on the card, in the chip's tooltip, and in the record's header and CSV header lines. Its wording is the owner's.
  - ADR 0038 states that the ledger's stored closes are single-vendor, from an unlicensed public endpoint, integrity-checked but not corroborated. It names a licensed feed as the only route to a corroborated record.

---

## 4. Owner decisions

Each entry gives the options, the recommendation (marked **★**), and what the decision blocks. Approved decisions become rows D10 onward in ADR 0029 §12 (§9).

### Needed now (before Monday 2026-10-19 15:00 CST)

**V1. Pause the evaluator.**
- **Options:** (a) ★ off now; dry mode after Release 1a; on after the readiness list in §3.1. (b) Leave it on.
- **Why:** At about 15:30 Monday it writes unverified, permanent baselines (F01, F03).
- **Also needed:** Is it on now? Is production a single replica?
- **Blocks:** nothing. It protects the four sets.

**V2. The benchmark right now.**
- **Options:** (a) ★ set it back to `sh000852` today, and say whether CSI 300 is wanted from 2026-11 (P4 would schedule it). (b) Keep `sh000300`: any covered ingest from Monday freezes CSI 300 into October.
- **Blocks:** nothing directly. It feeds P4's reconciliation.

**V3. Test data already in the ledger.**
- **Options:**
  - (a) Delete rows by hand. Rejected: unaudited, the cache survives it, and it sets a precedent on the citable table.
  - (b) ★ A write-once, bounded ledger start (§3.2) that changes only membership.
  - (c) Leave them. Then three test runs decide October's 600160 membership.
  - (d) Per-set voids. Rejected (C6).
- **Sub-question:** Is set 6 a real report? Q1 (§0.2) confirms the ordering.
- **Deadlines:** the declaration before Monday 15:00 CST; the setting itself before the evaluator is switched to `1`.
- **Blocks:** P5.

### Needed before Release 1a is cut (by Wednesday 2026-10-21)

**V8. Benchmark governance.**
- **Options:**
  - (a) ★ an allowlist, effective at the next cohort, scheduled and cancelled by explicit intents, audited only on real change, reconciled at boot, with `off_benchmark` exclusion and a benchmark per row;
  - (b) status quo;
  - (c) freeze the benchmark for good, which contradicts D1;
  - (d) a `mixed_bench` whole-cell refusal (C8).
- **Blocks:** P4.

**V21. The machine API and cookies.**
- **Options:**
  - (a) ★ token-only now (P34). It closes the enrolment bypass (D2), makes the route class true, and makes ADR 0029 §5 true. The cost is rewriting e2e section C.
  - (b) Keep cookies, gated: reclassify the nine handlers as `authSessionOrBearer` and call `gateEnrolment`, as `/api/symbols` does. A per-credential response shape then stays in §3.6, and the machine surface keeps a browser credential.
- **Blocks:** P34.

**V24. Test runs going forward.**
- **Options:**
  - (a) ★ a wire field `run_mode: "test"`, frozen at ingest into `state='test'` (§3.2), plus the Dify-side change;
  - (b) a dedicated test ingest token. That needs either a column on the token table (schema) or a `meta` list of token ids, which is the per-entity `meta` pattern ADR 0035 rejects;
  - (c) nothing. Every future test run then needs another cut, and the ledger start is write-once.
- **Blocks:** P36 and the Dify change.

**V26. When a refusal becomes final.**
- **Options:**
  - (a) ★ only at 10 or more sessions past due **and** after at least 4 mark-specific failed attempts. Source failures never count (§3.1).
  - (b) today's rule, 10 sessions past due. An outage longer than two weeks, or switching on after a long dry period, finalises every due mark whose next attempt fails, and that counts toward the 5 % cap.
- **Blocks:** P1.

### Needed before Release 1b is cut (by Wednesday 2026-10-28)

**V4. Checkpoint arithmetic.**
- **Options:** (a) ★ fix the code to ADR 0029 §4: S0 from `made_at` with the 15:00 rule, and dN = the Nth session after S0. This is also what the demo's resolved d5 shows. (b) Amend the ADR to the code: S0 = the first session after the receipt date, and dN = S0+(N−1). That discards a session of information for every intraday receipt and contradicts every document.
- **Effect on existing sets:** none. All six get S0 = 10-19 either way.
- **Blocks:** P6.

**V5. Baseline check.**
- **Options:** (a) ★ design §9.4 exactly, keyed on `made_at`, with `emitted_base_as_of` as a diagnostic only. (b) Key it on `emitted_base_as_of`. Rejected: a caller-supplied date would move the reference session.
- **Blocks:** P7.

**V6. Halts.**
- **V6a. How a halt is recognised:**
  - (a) ★ the vendor stamp has reached the due session, **or** 3 completed sessions pass with no newer bar, with the 20 % lag valve;
  - (b) evidence only. A delisting with a frozen stamp would then end `refused_final`, counted toward the cap, which contradicts ADR 0029 §11 objection 4.
- **V6b. The base for a stock that did not trade on S0:**
  - (a) ★ its last traded close before S0, fetched with a bounded 400-day look-back when the default window has none. The benchmark leg starts at S0's index close. Marks due during the halt resolve with return 0 and `halted=1`.
  - (b) The design's first traded close on or after S0. Marks due during a halt that starts at S0 cannot settle until the stock resumes.
- **Blocks:** P7.

**V7. Corporate actions.**
- **Options:** (a) ★ implement D8 as settled, with `fetchTencentAdjustRatio` for detection only, and correct the limit backstop (F14) so it never flags a trading day. (b) Amend D8 to "dividend element plus gap backstop", and accept that bonus issues between −9 % and −17 % are scored as losses.
- **Needs:** a real qfq capture (V20) by Tuesday 2026-10-27, or P8 moves to Release 1c.
- **Blocks:** P8. The evaluator stays in dry mode until P8 is deployed.

**V15. Per-row hit semantics.**
- **Options:** (a) ★ a four-state direction from the exact-integer predicate `hitStats` uses. Flat counts in n but not as a hit, which is unchanged. A mark is never resolved without a benchmark return. It ships in Release 1b. (b) Keep the drawer's own rule, which contradicts the cell.
- **Blocks:** P7 (`unverifiable`) and P16a.

**V20. Vendor captures.**
- **Options:** the owner runs `quote-probe capture` (available after P24; before it, a manual `curl` capture into `testdata/quote` in the existing fixture format), or authorises read-only probes by the implementer.
- **Needed:**
  - **By Tuesday 10-27:** a Tencent qfq and unadjusted window containing a bonus issue.
  - **Later:**
    - a currently suspended Shanghai or Shenzhen stock;
    - a delisted one;
    - limit-up days on a `301`, a `689`, a Beijing code and a sub-¥2 stock;
    - Sina index, stock and ex-dividend days;
    - Yahoo `.SS` and `.SZ` stock and index bodies with `events=div,split`.
- **Blocks:** P8 (a real qfq body is required, matching how `testdata/quote` fixtures are kept), the halt refinement in P7 (its interim form fails closed), and P24/P25.

### Needed before the evaluator is switched on

**V18. Operations targets.**
- **Questions:**
  - Who receives the alert webhooks?
  - Are the §8 SLO targets accepted?
  - What `/api/stock` p95 budget applies (for the stage step in §3.6)?
  - What weekly adoption target applies to distinct non-admin viewers of `/forecast`?
- **Blocks:** the alert subscription (the events exist either way), the SLO text in ADR 0037, and the `full` stage step.

### Needed before Release 2a

**V10. Who may open the record, and in what shape.**
- **Options:**
  - (a) ★ a global publish switch plus a per-OU `forecast_view` column (migration 0005). The nearest OU wins, Default NULL means deny, admins are always allowed, and restricted accounts are always denied in phase 1. This is the same tri-state, inherit-down-the-tree pattern migration 0001 used for enrolment policy (`user_groups.totp_enroll` and its siblings).
    - **Shipping (★):** 0005 and its reader ship together in Release 2a, behind `published=0`. The owner batches deploys, and a binary rollback across 0005 is restore-only either way. Take the backup.
    - **The alternative:** split them across two releases. That costs one more deploy cycle and buys a safe binary rollback from the reader release.
  - (b) A side table, `forecast_grants(principal)`, with `g:<OU>` and `u:<name>` principals and the shared picker (`web/src/components/principals.ts`). It is consistent with `version_grants` and `announcement_grants` (`store.go:638`, `:392`) and can grant one account, but it cannot deny below an allowed OU.
  - (c) A new role permission. No DDL, but it ignores the OU tree, which the owner asked for.
  - (d) Every unrestricted user, with no switch.
- **Before choosing:** run the §3.5 audience queries.
- **Also:** sign off the shape in §5.
- **Blocks:** P21, P26, P27.

**V12. Where the record lives.**
- **Options:**
  - (a) ★ a top-level `/forecast` with a workbench nav item, while `/manage/forecast` becomes evaluator operations;
  - (b) `/forecast` with no nav item, reached from the reading page and the account menu;
  - (c) relax `/manage/forecast`. Rejected: it is admin UI with an admin rail.
- **Decided on:** the mock in P13.
- **Blocks:** P27.

**V14. The exploratory tab for non-admin readers.**
- **Options:** (a) ★ available, as the demo shows it: p\*, a banner that cannot be closed, and `mode` and `official` on every CSV row. (b) Admin-only.
- **Blocks:** P27's configuration.

**V22. Compliance and licensing.**
- **Options:** (a) ★ a compliance sign-off before the first publish and before Phase 2, a disclaimer on every forecast surface, and the licensing statement in ADR 0038 (§3.9). (b) Publish without review.
- **Blocks:** flipping the publish switch (not the merge of P26), and the disclaimer copy in P20 and P27.

### Needed before Release 2b

**V9. Market-data sources.**
- **Options:**
  - (a) ★ Tier 1: the shared policy with an explicit grant, Tencent as the only writer, Sina in shadow, Yahoo and the others measured as candidates. ADR 0038 records, at the point of ADR 0028 §1's promise, that the ledger's stored closes are single-vendor and integrity-checked, not corroborated. The owner explicitly accepts that Tier 1 adds no fallback writes for scored marks. Tier 2 is a later decision.
  - (b) A separate forecast order (C3).
  - (c) Full fallback now. Rejected: zero extra scored marks.
  - (d) Status quo. The operator then cannot see or stop evaluator egress.
- **Blocks:** P23–P25 and ADR 0038.

**V13. The reading page.**
- **V13a. The stage switch:** ★ `forecast_reader` = `off|card|full`, default `card`, stepped to `full` per §3.6.
- **V13b. Membership badge:** ★ unrestricted readers only; restricted readers get no field at all. A "duplicate" status would reveal another tenant's earlier analysis.
- **V13c. Default set:** ★ the latest accepted set of the report row that matches the body, rather than the earliest.
- **V13d. Is the reading stage global or per OU?**
  - (a) ★ one global stage for everyone. Restricted readers get the card and chip on reports they can read, without payload, membership, errors, or orphaned rows.
  - (b) A per-OU stage: a second nullable column in 0005, `user_groups.forecast_reader`, with the same nearest-wins rule.
  - (c) Restricted readers get no forecast surfaces at all.

  Subject to V22.
- **Blocks:** P18, P20.

**V16. Tenancy hardening (D3, D4).**
- **Recommendation:** ★ ship P30 and P31 once their failing tests reproduce the problem.
- **What the owner should know:**
  - P30 narrows sharing under `visibility=group` for account-restricted users who sit in unrestricted OUs.
  - P31 turns a database error into an empty read for that one request.
- **Blocks:** P30, P31.

**V17. Quality gates.**
- **Recommendation:** ★ `axe-core` inside vitest, blocking (one devDependency). A Playwright screenshot lane stays deferred, given the CI-minutes decision of 2026-09-19; seeded-preview screenshots are attached by hand.
- **zh-TW:** regenerate the prefix only, and the owner names a human reviewer.
- **Blocks:** P15.

**V23. Projected checkpoint dates.**
- **Options:**
  - (a) ★ ship an exchange-closure table for projections only, labelled "expected", with a December runbook update and a weekday fallback (§3.7). It matches the demo's look.
  - (b) Keep the weekday projection, and change the demo copy to "on or after". In October and February the shown date can be five or more sessions early.
- **Blocks:** P37 and the date copy in P19 and P20.

**V25. Report editions.**
- **Options:**
  - (a) ★ D4 unchanged across editions, each edition's card showing its own sets, plus the display rules in C17. Also confirm on the Dify side that predictions are emitted only on the default edition.
  - (b) Only default-edition sets can be members, a classifier change. An analysis whose external edition was ingested first would then never count.
- **Blocks:** the edition rules in P18 and P20.

### Later

**V11. External tenants (Phase 2).** ★ Deny in phase 1. Phase 2 is a separate decision under the constraints in §3.5. Blocks nothing in this plan.

**V19. Tier 2 schema shape.** Migration 0006, options in §5. Decided only at the Tier 2 go.

---

## 5. Schema changes

**Release 1 changes no schema.** Its new state lives in `meta` rows and in new values of existing TEXT enums. An older binary ignores them, or reads them with the same meaning. `meta` is replaced on restore (ADR 0035 §7), so each key has a stated restore rule, enforced by `TestRestoreRulesForForecastKeys`:

| Kind | Name | Values | On restore |
|---|---|---|---|
| meta | `forecast_auto` (existing) | `0`, `dry`, `1` (an older binary reads `dry` as off) | The lower of the dump's and the live value (0 < dry < 1), audited as `system`. A restore never switches the evaluator on. |
| meta | `forecast_bench`, `forecast_bench_next`, `forecast_bench_next_from` | allowlist code; `YYYY-MM` | From the dump. Boot reconciliation then re-runs; it is idempotent and derived from the ledger. |
| meta | `forecast_population_from` | RFC3339 UTC, write-once | Carried from the live database when the dump lacks it. If both hold it with different values, the restore is refused with `forecast_population_conflict`. The bound still limits any later save. |
| meta | `forecast_unrecorded_floor_id`, `_at`, `_method` | int; RFC3339; `audit` or `max_id` | Re-derived from the restored audit rows |
| meta | `forecast_state_repair_premature` | the heal's count, written once | From the dump. The heal re-runs on its predicate, which matches only marks that carry the premature signature, so it is idempotent. |
| meta | `forecast_eval_state`, `forecast_alert_state` | heartbeat JSON; alert de-duplication | Reset |
| meta (Release 2+) | `forecast_reader`, `forecast_fallback`, `forecast_state_shadow`, `forecast_state_calls`, compute limits | see §6.2 | From the dump |
| meta (Release 2+) | `forecast_record_published` | `0`, `1` | The lower of dump and live. A restore never publishes. |
| enum value | `prediction_sets.base_check` = `refused_final` | — | An older binary reads it as a member with final refusals, the same meaning |
| enum value | `prediction_sets.state` = `test` | — | An older binary drops it from coverage (B4); a display gap only |
| enum value | `prediction_marks.refusal` codes (§3.1) | free TEXT column; closed in Go | — |
| semantics | `prediction_marks.attempts` | counts mark-specific failures only (V26) | — |

**Step 0005 (Release 2a, expand only; needs sign-off under V10).** The recommended shape (a):

```go
// migration0005 adds the per-OU switch for who may open the forecast verification record.
// NULL inherits from the parent OU; 0 denies; 1 allows; on the Default OU, NULL reads as deny.
// The record is unpublished by default and the default is deny, so applying this step changes
// nobody's access. It never opens anything to a restricted account.
func migration0005() migration {
    col := colRef{table: "user_groups", name: "forecast_view"}
    return migration{
        id: "0005", columns: []colRef{col},
        restore: restoreRule{olderDumpOK: true}, // an older dump restores with NULL = inherit / deny
        up: func(e migExec) error {
            if e.columnExists(col.table, col.name) { return nil }
            _, err := e.exec(`ALTER TABLE user_groups ADD COLUMN forecast_view INTEGER`)
            return err
        },
    }
}
```

- Under V13d (b), the same step also adds `user_groups.forecast_reader TEXT` (NULL inherits).
- The alternative shape (b), under V10, is an allow-list side table:

  ```sql
  CREATE TABLE IF NOT EXISTS forecast_grants(
      principal TEXT NOT NULL PRIMARY KEY,   -- 'g:<ou id>' or 'u:<username>'
      created_at TEXT NOT NULL,
      created_by TEXT NOT NULL DEFAULT '')
  ```

  It would be declared in the step's `tables` so that backups carry it (ADR 0035 §8).

**Step 0006 (Tier 2 only; shape decided at the Tier 2 go, V19).** The recommended shape: four defaulted TEXT columns, plus one BIGINT benchmark anchor that lets a second vendor's index body be checked to the fen.

```go
func migration0006() migration {
    cols := []colRef{
        {"prediction_sets", "base_source"}, {"prediction_sets", "base_logic"},
        {"prediction_marks", "bench_source"}, {"prediction_marks", "logic"},
        {"prediction_sets", "bench_base_fen"},
    }
    return migration{
        id: "0006", columns: cols, restore: restoreRule{olderDumpOK: true},
        up: func(e migExec) error {
            for _, c := range cols {
                if e.columnExists(c.table, c.name) { continue }
                typ := `TEXT NOT NULL DEFAULT ''`
                if c.name == "bench_base_fen" { typ = `BIGINT` } // fen, never REAL
                if _, err := e.exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.name + ` ` + typ); err != nil {
                    return err
                }
            }
            return nil
        },
    }
}
```

- An empty value means "written before this step": Tencent, settlement logic v1.
- `base_logic` and `logic` also record a Tier 2 evidence pairing (§3.3).
- The alternative is the four TEXT columns without the anchor. The benchmark leg would then be admitted on `reportsKind` alone.

**Rules for every step.**
- Steps are appended to `migrations()` and never edited once released.
- Nothing is added to `baseSchemaStmts()`.
- No REAL columns.
- Every step is probed for existence, so it is idempotent.
- Every step is tested on SQLite and Postgres.
- If two unreleased steps would ship in one release, fold them into one step before release.
- A binary older than a database's steps is refused at start (`migrate_steps.go:460-492`). So a release carrying a step can be rolled back only by restoring a pre-upgrade backup. Take that backup (ADR 0027) before deploying.

---

## 6. Routes, permissions and switches

### 6.1 Routes

All routes are registered in `routes.go`, and the route-table tests are updated with them.

| Route | Class (helper) | Extra gate | Change | PR |
|---|---|---|---|---|
| `GET /api/v1/*` (every read) | bearer | token only | A cookie alone gets 401. `TestTheMachineSurfaceRefusesACookie`. | P34 |
| `GET /healthz/forecast` | public | — | **New.** Status only: 200 `{"ok":true}`, or 503 `{"ok":false}` when the mode is not off and the heartbeat is stale. No other field. For an external uptime monitor. | P9 |
| `GET /api/admin/forecast` | admin | — | Adds: `mode`; `bench` {active, next, next_from, allowlist}; `population` {from, bound, candidates (id, symbol, rtype, made_at, state, orphan; never the payload)}; evaluator heartbeat and status; `unrecorded` per covered rtype {n, since, store_failed_30d}; `reader`; `published`; access summary; `runtime`; `ledger_chain`; `fallback`; `shadow`; readiness lists (evaluator on, publish). `due_sets` stays for one release. | P2, P4, P5, P9, P18, P25, P26 |
| `POST /api/admin/forecast` | admin | — | Takes `mode` (`enabled` as an alias through Release 1b), `bench_schedule`, `bench_cancel`, `population_from` (write-once), `reader`, `published`, `fallback`. Each field writes its own audit row, and only on a real change. Error codes: `forecast_bad_bench`, `forecast_bad_mode`, `forecast_population_set`, `forecast_population_too_late`, `forecast_population_future`, `forecast_population_month_mature`, `forecast_bad_fallback`, `forecast_not_ready`, `invalid_param`. All messages in English. | P4, P5, P9, P18, P25, P26 |
| `POST /api/admin/forecast/evaluate` | admin | — | **New.** Runs one pass in the current mode. Returns 409 `forecast_eval_off` or `forecast_eval_running`. Audited `forecast.eval.run`. | P9 |
| `GET /api/admin/forecast/access` | admin | — | **New.** The access preview (§3.5). | P26 |
| `GET /api/admin/forecast/groups` | perm `PermManage` | — | Additive explain fields. Kept as an alias, with `unrecorded`, until P29, which removes it (JSON 404). | P17, P29 |
| `GET /api/admin/forecast/groups/members` | perm `PermManage` | — | Scores nothing; outcomes only for mature cells (P16a). Later `population=all` and the folded kind (P16). Alias until P29. | P16a, P16, P29 |
| `GET /api/forecast/groups` | session (`s.session`; `routeRow.gate = forecast_view`) | `forecast_view`; enrolment wall | **New.** No `unrecorded`. Compute governance applies. | P26 |
| `GET /api/forecast/groups/members` | session (`s.session`; `routeRow.gate = forecast_view`) | `forecast_view`; enrolment wall | **New.** Rate-limited per user. | P26 |
| `GET /api/admin/groups` | admin | — | Adds `forecast_view`, `forecast_view_inherited` and `forecast_view_effective`, all server-resolved. | P26 |
| `PUT /api/admin/groups/{id}` | admin | — | Adds `forecast_view`: presence-aware, coerced on Default, 400 on a restricted OU. | P26 |
| `GET /api/me` | session (self-resolving, `routes.go:188`) | — | Adds `forecast_view` (bool) and `forecast_reader` (stage). Navigation hints only; the server still decides. | P18, P26 |
| `GET /api/stock/{symbol}` | session | stage `full` or admin | `r` decides date and kind; adds `prediction` (omitted on `X-Prefetch: 1`) and `timeline[].predicted`. | P18 |
| `GET /api/run/{key}` | session | stage `full` or admin | Adds `prediction`. | P18 |
| `GET /api/report/{id}/predictions`, `GET /api/predictions` | session | report read rule, and stage ≠ `off` for non-admins | Adds the §3.4 fields. `payload` withheld from scoped callers (P10). Non-accepted sets and errors become admin-only (P18). | P10, P16, P18 |
| `GET /api/v1/reports/{id}/predictions`, `GET /api/v1/predictions` | bearer (token only after P34) | the token decides the shape (§3.6) | Persistent token: machine shape plus additive fields, with `counted_set_id` corrected. Bridge token: accepted sets only. OpenAPI updated in both languages. | P34, P16, P18 |
| `GET /api/admin/quote` | admin (`routes.go:397`) | — | Adds `settled`, `settledShadow`, `cashDividends`, `reportsKind`, `ledgerChain`, `ledgerPrimary`, `ledgerShadowPeer`, `ledgerHealth`. | P23 |
| `POST /api/admin/quote` | admin (`routes.go:398`) | — | Unchanged, apart from an audit row on order saves. The UI confirms before removing the ledger's primary. | P23 |

**Never added:** any statistics route on `/api/v1`, or one reachable with an app-bridge token. Bridge tokens carry no OU-bound identity.

### 6.2 Switches

| Switch | Storage | Values (default after upgrade) | Governs | Audit |
|---|---|---|---|---|
| `forecast_auto` | meta | `0` / `dry` / `1` (unchanged; owner sets `0` now) | whether the evaluator writes; `1` is refused with `forecast_not_ready` until the readiness list in §3.1 holds | `forecast.auto` |
| `forecast_bench` (+ `_next`) | meta | allowlist (`sh000852`) | the benchmark of new cohorts | `forecast.bench` |
| `forecast_population_from` | meta | write-once (unset) | the ledger start (membership only) | `forecast.population` |
| `forecast_reader` | meta | `off` / `card` / `full` (`card`) | reading-page surfaces for non-admins; an unknown value reads as `off` | `forecast.reader` |
| `forecast_record_published` | meta | `0` / `1` (`0`) | the record for non-admins; `1` is refused with `forecast_not_ready` until the publish readiness list holds | `forecast.publish` |
| `user_groups.forecast_view` | column (0005) | NULL / 0 / 1 (NULL = deny) | which internal OUs may open the record | `AuditGroupChange` |
| `forecast_fallback` | meta | `off` / `shadow` (`off`); `on` only in Tier 2 | second-source comparison | `forecast.fallback` |
| compute limits | meta, via the limits form | `forecast_slots_official` 1–8 (2), `forecast_slots_explore` 0–8 (2), `forecast_wait_sec` 1–60 (10), `forecast_custom_per_min` 0–600 (20), `forecast_members_per_min` 0–600 (60) | statistics compute | the existing limits audit |

Admins always pass every reader gate. Nothing here goes into `config.yaml`.

---

## 7. PR plan

**Definition of done for every PR.**
- Tests are written first and fail on `main` unless marked CH. They pass on SQLite and on Postgres (`TEST_POSTGRES_DSN`).
- Evaluator, access and restore tests join the race lane: the `-run` regex at `.github/workflows/test.yml:174-175` gains `Forecast|Evaluator|Settle|Watchdog|Population|Restore`.
- Any write path follows the transaction rule in §3.1, with its deadline-bounded SQLite test.
- Housekeeping is current:
  - i18n parity and `err.*` keys;
  - the audit vocabulary;
  - the route table, including the enrolment-wall assertion for every new session route;
  - OpenAPI wherever v1 changes;
  - for any new webhook event: its entry in `availableEvents()` (`webhook_api.go:15-17`), its i18n label in all three locales, and its payload documented in the webhook reference.
- Docs are current: a runbook entry and the ADR delta.
- No Chinese characters in developer-facing text.
- Work happens on a branch with a PR, never straight to `main`. An adversarial Opus review pass precedes merge.
- Releases go through the CI-gated Release workflow as a Beta first. Nothing is deployed without the owner asking. Every release deploy is preceded by an ADR 0027 backup.

**Legend for test direction.**
- **FC (fail-closed):** when the guard breaks, nothing is written or shown.
- **FO:** a forecast failure never costs the reader the report.
- **BI:** the test pins both directions.
- **CH:** characterization; it passes on `main` and pins current behaviour so that a change is deliberate.

### Release 1a: stop the false statements and close the hole (schema-free; Beta target Fri 2026-10-23)

**Parallel lanes (Sonnet).**
- Security: P34.
- Statistics: P12 → P5 → P36 → P4. P4 also needs P3.
- Evaluator: P1 → P3, then P9.
- P2 is independent. P10 comes after P1.

**Rollout.**
1. ADR 0027 backup.
2. Q3 = 20 recorded.
3. Deploy.
4. The first boot's log line and audit row show 20 healed marks and the benchmark reconciliation.
5. The admin sets the ledger start (the pre-declared value) and the mode `dry`.

**Rollback.** A code revert is safe for the database. It does not undo the heal, the reconciliation or the floor capture, and all three are correct under the old binary. Restore the backup only if the owner wants the pre-deploy state exactly.

**P0: `docs/forecast-ledger-start-declaration`.** One paragraph recording the V3 pre-declaration in `docs/forecast-verification-operations.md`.
- **Depends on:** the answer to V3. **Deadline:** before 2026-10-19 15:00 CST.
- **Tests:** none.
- **Rollback:** not applicable. The server timestamp is the evidence.

**P34: `fix/v1-token-only`. The machine API accepts tokens only (D2, V21).**
- **Contents:** §3.8, plus the e2e section C rewrite.
- **Depends on:** V21.
- **Tests:**
  - `TestEnrolmentWallHoldsOnTheMachineSurface`: a session with pending mandatory enrolment reads a report through `/api/v1/reports/{id}` on `main`, and gets 401 after the fix. FC
  - `TestTheMachineSurfaceRefusesACookie`: every `GET /api/v1/*` route, with a valid cookie only, answers 401. FC
  - `TestSymbolsStillGatesTheSession` (CH).
  - Bridge and persistent-token reads are unchanged. BI
  - e2e section C passes on session routes.
- **Rollback:** a code revert, which reopens D2.

**P12: `test/forecast-golden-cells`.** The cell snapshot and the drawer snapshot (§3.4), both CH, committed while passing on `main`. It must merge before P5, P36, P4 and P16a.

**P1: `fix/forecast-due-only-scheduling`. Schedule only due work; a mark that is not due is never refused.**
- **Contents:**
  - the `predScheduleFor` helper, at today's rules;
  - wake-ups and the due-only selection driven from marks (§3.1);
  - removal of the unconditional baseline branch, here and in `CountForecastSetsDue`;
  - `DeferForecastMarks` (due marks only, with `session_date`; mark-specific failures increment `attempts`, source failures do not; escalating backoff; evaluator clock);
  - finality per V26; `MarkRefusedFinal` accepts `pending|refused`;
  - the terminal baseline;
  - the one-shot heal.
- **Depends on:** V26. It needs no other ruling: the due gate is the same under both dN rules, and the heal's "not due" check uses the earlier, ADR S0, so it is conservative under both S0 rules.
- **Tests:**
  - `TestCheckpointArithmeticToday` (CH): pins today's `sessions[n-1]` / `len > n` and S0, including the `len == n` and `len == n+1` boundaries.
  - `TestWaitingMarksStayPending`: the production shape (made Sunday 21:00; ticks Monday 10:00, Monday 15:31, Tuesday 01:00) refuses nothing, keeps attempts at 0, and fetches no stock before S0's close. FC
  - `TestNotDueSetsCostNoFetch`: 55 sets made Friday evening are scheduled with 0 calls and cost 0 calls until Monday 15:30. FC (on vendor load)
  - `TestSelectionDoesNotStarve`: 300 sets with pending h12; set 301's due d5 is evaluated within two ticks. BI
  - `TestDueSelectionUsesTheDueIndex`: the query plan uses `idx_pred_marks_due` on both drivers. FC (on load)
  - `TestCalendarNotYetShowingTheSessionIsNotARefusal`: a due mark at 15:31 whose calendar lacks today wakes again at +10 min; nothing is refused. FC
  - `TestOutageNeverFinalises`: 20 sessions of source failures leave `attempts` at 0 and the mark `refused`, not final; one success then settles it. FC
  - `TestCatchUpAfterDryPeriodNeverFinalisesOnFirstFailure`: a mark 30 sessions past due becomes final only after 4 mark-specific failures. FC
  - `TestBackoffEscalatesOnTheEvaluatorClock`: 1 h, 6 h, 24 h; `resolved_at` stays empty while refused. BI
  - `TestBaselineBecomesTerminal`: 10 sessions of mark-specific baseline failures gives `base_check=refused_final` and marks `refused_final no_base`; the cell is no longer pending. BI
  - `TestPrematureRefusalRepairRunsOnceOnTheSignature`: the production tuple heals; a due mark deferred with `session_date` set is untouched; a second run does nothing; the audit row carries the count. FC
  - `TestHealAndApplyNeverCallThePoolInsideATransaction`: SQLite, 2-second deadline. FC
  - `TestDueCountMatchesSelection`.
  - The PG variants of the selection and of the heal.
- **Rollout:** Release 1a; the evaluator stays off.
- **Rollback:** a code revert. Healed rows are `pending`, which the old binary handles; it would re-refuse them before they are due, which is today's behaviour.

**P2: `fix/forecast-unrecorded-floor`. Count unrecorded reports only since the ledger began.**
- **Contents:**
  - the floor (§3.2), captured once at boot and re-derived after a restore;
  - the count and `store_failed_30d`;
  - the copy fix (the old key is removed in this PR);
  - the `predictions.store_failed` webhook event, registered;
  - `unrecorded` per covered rtype on `GET /api/admin/forecast`.
- **Depends on:** none.
- **Tests:**
  - `TestUnrecordedFloorFromIngestAudit`: 1259 pre-ledger reports count 0; a covered report created after `mig:0004` with no set counts 1; a manual version counts 0; the floor survives audit retention and is re-derived after a restore. BI
  - `TestStoreFailedEmitsWebhook`, and its presence in `availableEvents()`.
  - A vitest for the copy (a count of 0 hides the line).
  - The locale parity test.
- **Rollback:** a code revert; the meta keys are ignored.

**P3: `fix/forecast-bench-instrument`. Fetch each set's frozen benchmark as the right instrument.**
- **Contents:**
  - the explicit allowlist map (F01);
  - a bare code is refused, by the settings save and by the evaluator;
  - one calendar per distinct frozen code per tick (F02);
  - an empty code gives `no_bench`, and the current setting is never read;
  - benchmark identity (`Kind=="index"`) and volume checks.
  - The test stub dispatches on market as well as code.
- **Depends on:** P1 (same function).
- **Tests:**
  - `TestBenchAllowlistMapsExplicitly`: `sh000852` gives `(sh,000852)` and `sh000300` gives `(sh,000300)`; bare `000852` and `600519` are refused, by the save too. FC
  - `TestEvaluatorFetchesEachFrozenBenchmark`: two codes cause two calendar fetches with `market=sh`, and each mark's benchmark return comes from its own index. FC
  - `TestBenchmarkBodyMustBeAnIndex`: a stock body gives `not_index`, and nothing is written. FC
  - `TestEmptyBenchCodeNeverReadsTheSetting`. FC
- **Rollback:** a code revert.

**P5: `feat/forecast-ledger-start`. The write-once population boundary (membership only).**
- **Contents:**
  - §3.2's ledger start;
  - the `pre_live` coverage bucket and membership status;
  - the restore rule for the key;
  - the settings section with its preview list and a permanence confirmation.
- **Depends on:** P12, V3.
- **Tests:**
  - `TestLedgerStartIsBoundedWriteOnceAndAudited`: past the bound gives 409; a second save gives 409; a mature first-cohort month gives 409; a future instant gives 400; excluded sets are never candidates; the earliest in-population set decides; coverage adds up; the audit lists the set ids. FC
  - `TestPreLiveSetsSettleButNeverCount`: a `pre_live` set is selected, settled and purged like any other, and is never a member. BI
  - `TestLedgerStartSurvivesAnOlderRestore`: carried when the dump lacks it; refused on conflict. FC
  - The golden snapshots are unchanged.
  - A vitest: the field becomes read-only once set.
  - e2e: a preview, save, 409 round trip.
- **Rollback:** a code revert. An older build ignores the key and counts the sets again; that is not wrong data, only the old population.

**P36: `feat/forecast-test-runs`. Classify test runs at ingest (V24).**
- **Contents:**
  - `run_mode` on the wire contract (openapi `predictions` schema, both languages);
  - `state='test'` (no marks, never scored);
  - its coverage bucket;
  - a reading-page label.
  - The Dify-side change (the test app sends `run_mode: "test"`; the projection stops hard-coding `emitter.app`) is a separate PR in the Dify repository, merged after Release 1a is deployed.
- **Depends on:** P12, V24.
- **Tests:**
  - `TestTestRunIsFrozenAtIngest`: `run_mode: "test"` gives `state=test` with no marks; absent or `production` gives today's classification; the value is never changed after insert. FC
  - `TestTestSetsHaveTheirOwnBucket`: coverage adds up. BI
  - The golden snapshots are unchanged.
- **Rollback:** a code revert. Test sets then fall out of every bucket on the older binary (§3.2).

**P4: `feat/forecast-benchmark-governance`. Benchmarks are effective-dated, chosen from the allowlist, audited and reconciled.**
- **Contents:**
  - §3.2's benchmark rules, explicit intents and real-change auditing;
  - `groupSet.BenchCode` and `off_benchmark`;
  - a per-group `bench`;
  - the `forecast.bench` audit;
  - the settings modal becomes a `Select` showing any scheduled change, with a cancel link, and it posts only changed fields.
- **Depends on:** P3, P36, V2, V8.
- **Tests:**
  - `TestBenchChangeIsEffectiveDatedAndReconciled`: active `sh000300` with October frozen at `sh000852` boots to active `sh000852` with `sh000300` scheduled from 2026-11, audited as `system`; an 11-02 ingest freezes `sh000300`. FC
  - `TestResavingTheActiveBenchIsANoOp`: no cancellation, no audit row. FC
  - `TestEnabledAliasesMode`: `enabled:false` gives `0`, and an unchanged value writes no audit row. BI
  - `TestOffBenchmarkSetsAreExcludedAndCounted`: the cell keeps its majority metrics and coverage adds up exactly. FC
  - `TestCustomRangeSlicesByBenchmark`.
  - A vitest: the Select, the schedule, the cancel link, and a toggle that posts `mode` only.
  - e2e section G: a schedule round trip and its audit row, a mode-only save, and a no-op save writing nothing.
- **Rollout:** reconciliation runs once at first boot.
- **Rollback:** a code revert. An older build reads `forecast_bench`, which reconciliation has made correct for the current cohort.

**P9: `feat/forecast-evaluator-operations`. Dry mode, heartbeat, watchdog, health and status.**
- **Contents:**
  - `step()` makes exactly one settings read, and nothing else when off;
  - `dry` mode;
  - `recover()` in the loop;
  - the heartbeat in `meta`, written only when the mode is not off;
  - `log/slog` lines;
  - **the watchdog in its own goroutine**, started beside the evaluator loop (`server.go:248`) with its own `recover`, never inside `cleanupLoop` (which runs purges synchronously and has no `recover`, `cleanup.go:180-200`); it emits `forecast.evaluator.unhealthy` (reasons `stalled`, `lagging`, `no_source`), registered and de-duplicated;
  - `GET /healthz/forecast` (§6.1);
  - the evaluate-now route;
  - the readiness list behind mode `1`;
  - a `ForecastEvaluatorStatus` card;
  - the mode radio, with confirmation for Off → On.
- **Depends on:** P1. Alert recipients are V18.
- **Tests:**
  - `TestOffStepDoesExactlyOneRead`: a counting driver over the real `step()` sees one `meta` read, no writes and no fetches. FC
  - `TestDryRunWritesNothing`: ledger checksums are equal before and after; the sample lists `(market, code, source)`. FC
  - `TestPanicDoesNotKillTheLoop`, and `TestPanicDoesNotKillTheWatchdog`.
  - `TestWatchdogRunsWhileCleanupIsSlow`: a blocked purge does not delay the watchdog. FC
  - `TestWatchdogDetectsStalledEvaluator`: one event when on and stale; a single read and nothing more when off; de-duplication holds. BI
  - `TestHealthzForecastIsStatusOnly`: 200 or 503, with no other fields. FC
  - `TestModeOneRequiresReadiness`: 409 `forecast_not_ready` until every item holds. FC
  - A vitest for the status card's states (off, dry, healthy, stale, lagging, error).
  - e2e: a mode round trip including `dry`; evaluate returns 409 when off.
- **Rollback:** a code revert; an older binary reads `dry` as off.

**P10: `fix/forecast-reading-hotfix`. Reading-page correctness.**
- **Contents:** §3.6's hotfix list.
- **Depends on:** P1 (phase copy).
- **Tests:**
  - `TestPayloadWithheldFromScopedReader`: on the session routes, a scoped caller gets no payload with `include=payload`; a nil scope does. FC
  - `StockPage` W1: switching report while B is in flight removes A's panel and overlay at once. FC
  - W3: a rejected predictions request still renders the body, with an inline retry. FO
  - The production fixture renders "waiting, on or after 2026-10-19" and never "Fetch failed". FC
  - `PriceChart`: empty arrays give the same output as no props; a Sunday marker snaps to Monday. BI
  - `PredictionPanel`: no "counted" badge; the quality value is translated.
- **Rollback:** a code revert.

### Release 1b: correctness before irreversibility (schema-free; Beta target Fri 2026-10-30)

**Lanes.**
- Evaluator (Sonnet): P6 → P7 → P8.
- Statistics: P16a, after P4.
- Docs (Opus): P11.

If the qfq capture is not in hand by Tuesday 10-27, P8 ships alone as Release 1c (target Fri 2026-11-06), and the evaluator stays in dry mode until it is deployed.

**P6: `fix/forecast-checkpoint-definitions`. S0 and dN per ADR 0029 §4.**
- **Contents:**
  - `predScheduleFor` switches to the V4 rules;
  - the evaluator includes the receipt day's session for intraday receipts;
  - marks use the stored baseline (F12);
  - each set's plan is applied in one short transaction, baseline first, under the §3.1 rule.
- **Depends on:** P1, P3, V4.
- **Tests:**
  - `TestS0FollowsReceiptTimeOfDay`: 10:00 on session T gives T; 15:00:00 gives T+1; lunch break gives T; pre-open gives T; Sunday gives Monday. FC
  - `TestDNIsTheNthSessionAfterS0`: replaces the CH pin from P1; includes the demo's 2026-09-17 report, whose d5 is 2026-09-28. FC
  - `TestMarksUseTheStoredBaseline`. FC
  - `TestPlanIsAtomicPerSetBaselineFirst`: a failure injected on mark 2 rolls back the baseline and mark 1; SQLite, 2-second deadline. FC
- **Rollback:** a code revert, while the evaluator is still in dry mode.

**P7: `fix/forecast-settlement-semantics`. Baseline check, halts, completeness, consistency checks, the limit backstop and refusals.**
- **Contents:**
  - §3.1 settlement semantics, except the qfq detector;
  - the corrected limit backstop (F14);
  - V6b's benchmark leg and look-back;
  - the refusal enum and its two classes;
  - `source` from the response.
- **Depends on:** P6, V5, V6, V7, V15. Fixtures for halt refinement come from V20; until then the rule's interim form fails closed.
- **Tests:**
  - `TestBaselineCheckFollowsDesignTable`: intraday inside [low, high] matches; equal to the previous close matches; equal to the close two sessions back is a mismatch; after the close, equal to the last or previous close matches. FC
  - `TestBackdatedAsOfCannotTurnMismatchIntoMatch`. FC
  - `TestHaltResolvesOnEvidenceOrConfirmation`: within 3 sessions it is `stale_bar`, never final; a later bar or a current stamp resolves `halted=1` at once; after 3 sessions it resolves `halted=1`; with the valve tripped, nothing is written. BI
  - `TestHaltedOnS0UsesLastPreS0Close` (V6b): `base_date < s0_date`; a d5 during the halt resolves with return 0 and `halted=1`; `bench_ret_bp` runs from S0's index close, not `base_date`'s. FC
  - `TestHaltLongerThanTheWindowFetchesBack`: a stock halted 60 days before `made_on` finds its base through the bounded look-back; with none in 400 days, the terminal-baseline path. BI
  - `TestLimitBackstopNeverFlagsATradingDay`: these are not flagged: +15 % on a `301xxx`; +15 % on a `689xxx`; +25 % on a Beijing code; limit-up from ¥1.05 to ¥1.16; a −10 % limit-down day; any move in a listing's first five bars. A gap one fen beyond the rounded limit price is flagged. BI
  - `TestMissingBenchBarIsUnverifiableNotResolved`, plus a statistics test that n excludes such marks. FC
  - `TestNewestBarRule`: at Thursday 01:00 Wednesday's bar is used; a 14:57 stamp drops today's bar; a body suspended and stamped today 09:00 keeps its last traded bar. BI
  - `TestCalendarAndAnchorChecks`: a zero-volume index bar, a stock bar off the calendar, a non-reproduced `s0_date`, or a base close different from `s0_fen` gives the matching refusal and no write. FC
  - `TestSourceFailuresAreNotMarkSpecific`: `fetch_failed` and `not_index` never increment `attempts`. FC
  - `TestSettleRecordsTheAnsweringSource` and `openapi_test.go`.
  - `TestMalformedMadeOnNeverReachesAVendorURL`. FC
- **Rollback:** a code revert.

**P16a: `fix/forecast-hit-predicate-and-members-gate`. One hit predicate; the members route scores nothing and withholds outcomes.**
- **Contents:**
  - `predHitClass` in `hitStats` and in the drawer;
  - the members route split into gate and scoring;
  - outcomes only for mature cells.
- **Depends on:** P12, P4, V15.
- **Tests:**
  - **The cell snapshot is unchanged**, or the regeneration is in its own reviewed commit (§3.4).
  - The drawer snapshot is regenerated in its own reviewed commit.
  - `TestDirectionReconcilesWithHitRate`: excess-0 ties and members at 4.99 % / 5.00 % / 5.01 %; agree ÷ (agree + disagree + flat) equals the cell's hit rate. BI
  - `TestMembersRouteScoresNothing`: the scoring counter stays 0, and the state equals the matrix cell's. FC (compute)
  - `TestMembersWithholdOutcomesUnlessMature`. FC
- **Rollback:** a code revert, while the evaluator is not yet at `1`.

**P8: `fix/forecast-adjust-ratio-detection`. Detect bonus and rights issues by the adjusted ratio (D8).**
- **Contents:** `fetchTencentAdjustRatio` (detection only, due marks only), wired into `corporateActionBetween`. The ADR 0029 §10 note is corrected in P11.
- **Depends on:** P7, V7, and the V20 qfq capture.
- **Tests:**
  - `TestQfqRatioFlagsBonusIssueUnderTheLimit`: a −9.09 % main-board ex-date is flagged; a −9.5 % limit-down day with an unchanged ratio is not; fen rounding alone never flags. BI
  - `TestAdjustRatioIsNeverStoredOrScored`. FC
  - `TestAdjustFetchOnlyForDueMarks`. FC (on vendor load)
- **Rollout:** the evaluator stays in dry mode until P8 is deployed.
- **Rollback:** a code revert, and set the mode back to dry.

**P11: `docs/forecast-adr-0037`.** ADR 0037, plus the amendment notes in ADR 0029 and the design doc (§9).
- **Depends on:** the rulings V1–V8, V15, V21, V24 and V26. It can merge with or after Release 1b.
- **Tests:** none.

### Release 2a: access for a pilot OU (Beta target Fri 2026-11-13; contains migration 0005)

**Lanes.**
- Docs (Opus): P13, P22.
- UI foundations: P15.
- Access (Sonnet): P21 → P26 → P27, then P28.

**Rollout.**
1. ADR 0027 backup.
2. Deploy. Published stays `0`, so only admins see anything.
3. The owner checks the publish readiness list (P26).
4. Pilot: one OU for a week.
5. Then Default.

**P13: `docs/forecast-demo-v2`.** The production baseline screenshots, then the demo revision (§3.7), including the V12 mock and the corrected projected dates.
- **Depends on:** the screenshots (owner, or an authorised read-only session).
- **Gate:** the owner reviews it. It blocks P19 and P20, and informs V12.

**P15: `feat/forecast-web-foundations`.**
- **Contents:**
  - the palette and format helpers, legend, shared methodology card and `.rp-sr-only`;
  - copy fixes: the hit-rate help says "|upside| ≥ 5 %; flat counts as a miss", plus the IC and spread nulls;
  - the `forecast.disclaimer` key (wording per V22);
  - the zh-TW prefix regeneration with its fixed-point gate;
  - `AuditPage` reads `?action=`;
  - matrix and drawer moved into `components/forecast` with no behaviour change;
  - the `axe-core` devDependency.
- **Depends on:** V17.
- **Tests:**
  - palette contrast against both algorithms;
  - the source scan for colour literals;
  - zh-TW fixed point;
  - the `AuditPage` deep link;
  - the existing matrix and drawer tests stay green.

**P21: `feat/groups-forecast-view-store`. Migration 0005 and store functions.**
- **Contents:** the step (shape per V10) and its store functions.
- **Depends on:** V10 sign-off.
- **Tests:**
  - `TestMigration0005` on SQLite and PG: applies on a 0001–0004 database, is idempotent, restores an older dump with NULL, and a hand-dropped column is refused.
  - `TestReusedOUIdCarriesNoForecastView`: an explicit id is re-inserted.
- **Rollback:** restore only.

**P26: `feat/forecast-record-access`. OU-governed access to the record (§3.5).**
- **Depends on:** P21, P16a, P15.
- **Tests:**
  - `TestForecastAccessMatrix`, covering the record and admin rows of the matrix. FC/BI
  - `TestForecastRecordRefusedWhenNoOUSetsIt`. FC
  - `TestForecastViewNearestOUWins`. BI
  - `TestForecastRecordAdminAlwaysAllowed`.
  - `TestForecastRecordRefusesRestrictedEvenWhenAllowed`. FC
  - `TestForecastViewDeniesOnStoreError`: a unit test of each read failing. FC
  - `TestForecastAccessHasNoLenientRead`: a source scan. FC
  - `TestForecastRoutesAreSessionRoutesBehindTheWall`: in `sessionRoutes()`, not on the enrolment allowlist, and annotated. FC
  - `TestForecastStatsNotReachableByBearer`. FC
  - `TestReaderAndAdminGetIdenticalRecords`: on an injected clock. BI
  - `TestReaderResponseOmitsUnrecorded`.
  - `TestMeForecastViewMatchesTheGate`.
  - `TestGroupSaveForecastView`: an omitted field is unchanged, and Default is coerced. FC
  - `TestForecastPublishSwitchRequiresReadiness`. FC
  - `TestForecastComputeIsBounded`: cached official requests never wait; exploratory saturation returns 503 with `Retry-After`; identical requests share one scoring; a cancelled leader still serves its followers; exploratory use cannot starve the official pool. FC (capacity)
  - `TestForecastPerUserLimits`.
  - `TestForecastLimitsAreSettings`.
  - `TestForecastAccessPreviewMatchesRealAccess`: unassigned accounts and members of root OUs are counted. BI
- **Publish readiness list.** The publish switch accepts `1` only when every item holds:
  - Release 1a and 1b deployed, and the evaluator on after its readiness list;
  - the ledger start set;
  - the bounded `unrecorded` count;
  - a benchmark shown per cohort;
  - compute governance live;
  - the compliance sign-off recorded (V22).

  The purge-marker cache key and the seeded-preview review are added to the list when P16 and P14 ship. Until then the pilot is told the record is pre-parity.
- **Rollback:**
  - fastest: unpublish, which is audited and needs no deploy;
  - finer: OU values;
  - binary: restore only, across 0005.

**P27: `feat/forecast-record-page`. The record page and its controls.**
- **Contents:**
  - `/forecast` per V12, mounting the shared matrix and drawer from `components/forecast` (restyled later by P19);
  - `/manage/forecast` reduced to operations: status, settings, ledger start, benchmark schedule, publish switch with the readiness list, access summary, `unrecorded`;
  - the OU editor control, rendered from the server's inherited value;
  - the nav item;
  - CSV `mode` and `official` on every row, and the disclaimer header line;
  - capped retry with jitter.
- **Depends on:** P26, P15, V12, V14, V22.
- **Tests:**
  - the record page never requests `/api/admin/forecast`;
  - a 403 renders a Result, not a retry loop;
  - `forecast_busy` retries at most 3 times with jitter, and a 429 is not retried;
  - the nav item shows if and only if `forecastView || admin`;
  - the OU editor (Default shows Allow/Deny only);
  - every CSV row is marked;
  - locale parity.

**P28: `test/forecast-e2e-access`.** e2e section G gains:
- unpublished means 403 for an internal member;
- the preview count matches;
- after publishing, the member gets 200;
- a restricted member gets 403, an anonymous caller 401;
- a PUT without the field leaves it unchanged;
- a cookie-only v1 request gets 401.

The existing admin-path assertions stay until P29.
- **Depends on:** P26, P34.

**P22: `docs/forecast-adr-0039`.** ADR 0039 (readers: OU-governed access and reading-page integration). It supersedes design D6 and rewrites design acceptance condition 19.
- **Depends on:** V10, V12, V13, V14, V22, V25.

### Release 2b: parity, reading-page integration and Tier 1 data (Beta target Fri 2026-12-04)

**Lanes.**
- UI: P19, P20.
- Statistics: P16 → P17 and P18.
- Seed: P14.
- Calendar: P37.
- Data: P23 → P24 → P25.

**P14: `chore/forecast-demo-seed`. The seeded preview dataset and the dev clock seam.**
- **Depends on:** Release 1b merged.
- **Tests:**
  - `TestForecastDemoSeedIsDeterministic`: two runs give identical groups JSON at the fixed clock; every Appendix A state is present; an existing output path is refused.
  - `TestDemoClockRefusedInReleaseBuilds`: with `version.Version != "dev"` and `RP_FORECAST_DEMO_NOW` set, startup fails. FC

**P16: `feat/forecast-read-semantics`. Statistics read semantics (§3.4, except what P16a shipped).**
- **Depends on:** P16a, P6, P5, P36, P4.
- **Tests:**
  - **The cell snapshot is unchanged.**
  - `TestMembershipOnReadEqualsGroupStatistics`. BI
  - `TestClassifierPreservesMemberOrder`. FC (p-values)
  - `TestUnknownStateIsCountedNotDropped`. BI
  - `TestPurgedMonthBypassesCacheAcrossProcesses`: two `Server`s on one database. FC
  - `TestRestrictedViewerGetsNoMembership`: byte-identical with and without another tenant's earlier set; a load error omits the fields. FC
  - `TestDisplayAndCountedSetIDs`. BI
  - `TestReadPhaseNeverClaimsRetryingWithoutEvidence`. BI
  - `TestNotBeforeIsALowerBound`, against committed 2023–2026 exchange closure calendars (new testdata). BI
  - the OpenAPI enum test.
- **Release note:** `counted_set_id` is corrected.

**P17: `feat/forecast-groups-explain`. Explain fields.**
- **Depends on:** P16.
- **Tests:**
  - `TestNonMatureCellsCarryPendingReasons`.
  - `TestPendingReasonsDoNotChangeState`: 500 random fixtures checked against a frozen copy of today's state function. FC
  - `TestCapabilitiesMirrorRouteGates`. FC

**P18: `feat/forecast-reader-server`. The reading-page server view.**
- **Contents:**
  - §3.6's server pieces, including the prefetch rule and the edition rules;
  - the `forecast_reader` stage and its audit;
  - token shaping on v1;
  - non-accepted sets and errors become admin-only.
- **Depends on:** P16, V13, V25.
- **Tests:**
  - `TestForecastAccessMatrix`, extended to the reading surfaces. FC/BI
  - `TestStockDeepLinkByReportID`: an unreadable `r` falls back to the default selection and leaks nothing. FC
  - `TestStockAndRunPayloadCarryPredictionSummary`: the stage gates it; a restricted viewer gets no membership; a ledger error omits the summary and keeps the report; the timeline flags are exact over 60 reports. FO
  - `TestPrefetchOmitsTheSummaryButKeepsTheTimeline`. BI
  - `TestEditionWithoutASetLinksItsReadableSibling` and `TestSiblingEditionMemberReadsCountedOnEdition`. BI
  - `TestReaderStageEverywhere`: `off` hides sets on the session routes; persistent tokens are unchanged; an unknown value reads as `off`. FC
  - `TestV1ShapesByToken`. FC
  - PG variants.
  - A recorded before/after `/api/stock` benchmark, real and prefetch requests.

**P19: `feat/forecast-cohort-parity`. The cohort page matches the demo.**
- **Contents:**
  - the shell with header, tabs and toolbar;
  - the summary strip, status pill, matrix states and legend;
  - the drawer in mature and membership modes;
  - CSV header lines.
- **Depends on:** P13, P15, P17.
- **Tests:**
  - Matrix: a small cell shows final text and no progress bar; a payload with numbers on a non-mature cell renders none. FC
  - Drawer: A's rows never show while B loads; membership mode has no outcome columns. FC
  - Page: a settings failure still renders the matrix.
  - The a11y structure and axe.

**P20: `feat/forecast-reading-integration`. Card, chip, steps, band, table, overlay, timeline marks, editions and the run page.**
- **Depends on:** P13, P15, P18.
- **Tests:**
  - The production fixture renders waiting copy and "on or after 2026-10-26" (or "expected" under V23), never retry or failure text. FC
  - The chip is in the same commit as the body; a warm render reserves its slot; nothing above the body changes height. FO
  - Edition switching swaps the card under the stale-state guard. FC
  - Overlay toggle and preference behaviour.
  - Bar-completeness and low-window parity fixtures, run by Go and vitest. BI
  - The disclaimer is present on the card and in the chip tooltip.
  - RunPage mount.
  - Timeline `aria-label`.
  - axe.
- **Removes:** `PredictionPanel.tsx`.
- **Rollout:** the `card` → `full` step in §3.6.

**P37: `feat/exchange-calendar-projection`.** Under V23 (a): the closure table, the projection, its "expected" label and the December runbook entry.
- **Tests:**
  - `TestProjectionMatchesKnownCalendars`: the demo's 2026-09-17 report projects d20 = 2026-10-26 and d60 = 2026-12-21. BI
  - `TestMissingYearDegradesToOnOrAfter`. FC
  - `TestSettlementNeverReadsTheTable`: a source scan. FC

**P23: `feat/quote-ledger-capability`. Tier 1 capability on the shared source policy, and ADR 0038.**
- **Contents:** §3.3 Tier 1, apart from Sina and the probe; ADR 0038 in the same PR.
- **Depends on:** P7, V9.
- **Tests:**
  - `TestSettledRequiresAnExplicitGrant`. FC
  - `TestLedgerChainFollowsOperatorOrder`: with Tencent removed, the walk returns `no_source`, a source failure, and a panicking stub is never called. FC
  - `TestLedgerFetchNeverCaches`. FC
  - `TestLedgerHealthIsSeparate`. BI
  - `TestNoPublicRangeIsSettled`. FC
  - `TestBreakerAndSingleSlot`.
  - `TestRefusalPrecedence`.
  - A `QuoteSourcesPage` vitest: the no-primary alert, and the confirmation before a save that removes the primary.

**P24: `feat/cli-quote-probe`.**
- **Depends on:** P23.
- **Tests:**
  - capture writes files plus a manifest, and never writes the database;
  - compare shares the comparator;
  - Yahoo is accepted as a candidate.

**P25: `feat/forecast-sina-shadow`.**
- **Depends on:** P23, P24, and the V20 captures.
- **Tests:**
  - `TestSinaRangeFiltersByRowDate`.
  - `TestSinaKlineMustAgreeWithItsSnapshot`.
  - `TestShadowNeverWritesTheLedger`: at most 2 extra calls per working tick; ledger rows identical to an `off` run; statistics survive a new `Server`. FC
  - `TestFallbackModesTier1`: `on` returns 400. FC
  - the wired-settings test.

### Release 3 (target Fri 2026-12-18)

**P29: `refactor/forecast-retire-admin-aliases`.** Removes `/api/admin/forecast/groups*` and their `unrecorded`, and the `enabled` alias.
- **Depends on:** P27 and P28 shipped, and Release 2b in production for two weeks.
- **Tests:** `TestAdminGroupsPathIsGone` (JSON 404); the e2e script asserts 404.
- **Rollback:** a code revert. A stale tab gets the update prompt.

### Independent (any release, after V16)

**P30: `fix/tenancy-restricted-ou-principals`.** D4 on both the writer and the reader side. Existing `g:<unrestricted OU>` rows are left in place and simply never match again. Run a read-only count first.
- **Tests:** `TestOnlyRestrictedOUsShare`:
  - (a) two unassigned account-restricted users do not see each other's reports;
  - (b) a user in an unrestricted container OU is not visible to its tenant children;
  - (c) two members of one restricted tenant still share.

  (a) and (b) fail on `main`; (c) guards against over-narrowing. BI

**P31: `fix/tenancy-strict-restricted-read`.** D3.
- **Tests:** `TestScopeOnUnreadableRestrictedFlag`: the reading routes return nothing, not everything. FC

### Tier 2 (separate go/no-go, V19; no earlier than 2026-12-21)

**P32: `feat/forecast-provenance-and-index-fallback`.**
- **Contents:** migration 0006, then admission of the benchmark leg under §3.3.
- **Tests:**
  - `TestMigration0006` on SQLite and PG;
  - `TestRatiosNeverCrossVendors` (property-based);
  - `TestBenchAnchorFillNeverFollowsResolvedMarks`;
  - `TestNonKindIndexOnlyByAnchor`.
- **Rollback:** forward-only. Take a backup first.

**P33: stock-leg admission.** Only if a source gains corporate-action evidence, its own or through a separately granted evidence source.
- **Tests:** `TestS0OnlyFromThePrimary`; `TestStockLegNeedsCorpEvidence`; `TestEvidencePairingIsRecorded`.

---

## 8. Observability, SLOs and runbook

**Signals.**
- **Heartbeat (`meta.forecast_eval_state`).** It is written by the pass and only while the mode is not off. It carries:
  - the mode;
  - `last_tick_at`;
  - the last pass: kind, start and finish times, sets scanned, fetches per source, marks resolved, halted, refused by code and class, finalised and healed, baselines by verdict, `awaiting_session`, `due_unsettled`, `oldest_due_unsettled_minutes`, whether the stale valve tripped, errors, last error and panics;
  - per benchmark: the calendar's last bar, when it was fetched, and the source, market and code;
  - the dry-run sample.
- **Persisted counters.**
  - `forecast_state_calls`: 14 days of calls, failures and refusals by code, per source.
  - `forecast_state_shadow`.
  - Access and compute counters (requests, cache hits, flights joined, busy, rate-limited, refusals by reason, drawer opens) are kept in memory and flushed daily to `meta` as counts.
  - **Weekly distinct non-admin viewers of `/forecast`.** Each week has a random salt held in `meta`. Each view adds `HMAC(salt, username)` to that week's set, which is flushed to `meta` hourly, so a restart loses at most an hour. When the ISO week ends, the count is kept and the set and salt are deleted. No username is ever stored.
- **Logs.** `log/slog` with `component=forecast_eval`: one line per pass, and one line per set error with `set_id`, `bench`, `source` and `reason`. Nothing is logged for sets that are not due.
  - The groups route logs `forecast groups: mode= cells= scored= cached= flight= wait_ms= dur_ms=` when it exceeds 3 s.
  - Access refusals log their reason.
- **Watchdog (its own goroutine, every 60 s, with `recover`).** It does nothing while the mode is off. Otherwise it raises `forecast.evaluator.unhealthy` when:
  - `stalled`: the heartbeat is older than 10 minutes;
  - `lagging`: the oldest due mark has been unsettled for more than 6 hours, or a weekday has passed 18:00 CST with a stale calendar, or the valve tripped;
  - `no_source`: the ledger chain is empty.

  `stalled` alerts at most hourly; the others at most daily.
- **External health.**
  - `GET /healthz/forecast` returns 503 when stalled, for an uptime monitor that also catches the process itself dying.
  - `/healthz` stays database-only.
- **Webhook events.** `predictions.store_failed` and `forecast.evaluator.unhealthy` join `availableEvents()`, with i18n labels and documented payloads.
- **Audit trail.** `forecast.auto`, `forecast.bench`, `forecast.population`, `forecast.reader`, `forecast.publish`, `forecast.fallback`, `forecast.repair`, `forecast.eval.run`, OU changes and source-order saves. Each is written on a real change only.

**SLOs.** These are targets the owner accepts or adjusts in ADR 0037 (V18).

| SLO | Target | Measured from |
|---|---|---|
| Settlement freshness | 99 % of marks due on session D are terminal, or carry a recorded refusal, by 18:30 CST on D. p95 within 30 minutes of the calendar first showing D, on days the vendor is healthy. | `resolved_at`, the calendar's `fetched_at` |
| Evaluator liveness | Heartbeat younger than 5 minutes for 99.9 % of enabled minutes | `last_tick_at`, and the external monitor on `/healthz/forecast` |
| Ledger integrity | `store_failed = 0` and `unrecorded = 0`. Any occurrence is an incident. | audit log, floor count |
| Data quality | `refused_final` share below 1 % per cohort × checkpoint. The maturity cap stays at 5 %. | marks |
| Statistics latency | Monthly view p95 at most 3 s on a cold cache. Requests answered from cache never wait for a slot. | the groups log line |
| Reading-page cost | `/api/stock` p95 regression within the budget set under V18 from P18's recorded benchmark | the PR benchmark, then logs |

**Error-budget rule.** While the freshness or integrity SLO is breached, evaluator feature PRs wait until the cause is fixed. Fixes are exempt.

**Runbook (lives in `docs/forecast-verification-operations.md`; each entry is written in the PR that creates the signal).**

| Symptom | First checks | Action |
|---|---|---|
| `stalled`, or `/healthz/forecast` 503 | The heartbeat's age and its `panics` and `last_error`; whether the process is up | Restart only after reading the log. The loop recovers from panics, so a stall means a wedge. |
| `lagging` | The calendar's last bar and source; `stale_valve_tripped`; ledger health on `/manage/quotes` | Vendor-wide lag: wait. Nothing is written, and nothing becomes final (V26). One source down: check the chain. Do not force a settle. |
| `no_source` | The `quote_source_order` save in the audit log | Restore the primary to the order, or accept that settlement is paused. Nothing becomes final. |
| `store_failed` above 0 | The audit filter `predictions.store_failed` | Find the ingest that failed. The report exists, but the ledger row does not; it cannot be re-created after the fact. |
| An `off_benchmark` count appears | The `forecast.bench` audit and the reconciliation rows | Explain it in the release note. Never re-score. |
| A `test` count appears in a cohort | The emitter and `run_mode` of those sets | Expected for Dify test-app runs. If a production run was marked `test`, fix the producer; the set stays a test run. |
| Setting the ledger start | The candidate list in the settings modal against the pre-declaration | Save once. The value is permanent and survives restores. |
| Switching the evaluator on | The readiness list and the dry-run gate in §3.1 | Record the hand-checked numbers in the operations log. |
| Before any release deploy | — | Take the ADR 0027 backup; for Release 1a, record Q3 = 20. |
| After a restore | `forecast_auto` and `forecast_record_published` | A restore never raises either. Re-enable deliberately. |
| December | The exchanges' annual closure notice | Update the projection table (P37). |
| A reading-page incident | — | Set `forecast_reader=off` (immediate, audited). |
| A record incident | — | Unpublish (immediate, audited). |
| Rolling back across 0005 or 0006 | — | Restore the pre-upgrade backup. Binary rollback is refused by design. |

---

## 9. ADR changes

| Document | Change |
|---|---|
| **ADR 0037 (new): forecast verification operations.** | It records:<br>• the definition repair, with the code brought to §4 and §9.4;<br>• not-due is not a refusal; finality only after mark-specific attempts (V26); the terminal baseline;<br>• the heal;<br>• ledger start (membership only) and test runs at ingest;<br>• benchmark governance (amends D1);<br>• the bounded `unrecorded`;<br>• the restore rules for forecast keys;<br>• dry mode, the readiness lists and the dry-run gate;<br>• the heartbeat, independent watchdog, health route and SLOs;<br>• the transaction rule on the single-connection pool;<br>• the escapes F01, F03, F06, F10 and F14 with their causes (a validator that accepted its own wrong spelling; a check that compared a series with itself; a stub that drove the UI copy; a settled decision left unimplemented; a limit modelled as a percentage instead of a price). |
| **ADR 0038 (new): ledger-grade market data on the shared source policy.** | Amends ADR 0029 D9 and §6: the evaluator walks the operator's chain, restricted to explicit `settled` grants; one writer in Tier 1, with no fallback writes for scored marks, accepted by the owner (V9); the candidate-source table; the separate evidence chain; Tier 2 entry criteria. Corrects ADR 0029 §7: a mark's `bench_code` is the frozen intent; provenance comes in Tier 2. Records at ADR 0028 §1 that the ledger's closes are single-vendor, from an unlicensed public endpoint, and integrity-checked, not corroborated. Notes at ADR 0032 §6 which stored-price contract items are met and which are not. |
| **ADR 0039 (new): forecast verification for readers.** | OU-governed record access and the publish switch, which supersede design D6 and amend ADR 0029 §5's statistics bullet. The access matrix. Phase 1 excludes restricted accounts. The reading-page integration, the `forecast_reader` stage and its rollout. Edition rules. Payload withheld from scoped readers. Membership withheld from restricted callers. No outcome before maturity, enforced on the server. The compliance gate and disclaimer. |
| **ADR 0029 (amend in place, pointing to 0037–0039).** | §5: "/api/v1 is token-only" is now true in behaviour (P34); statistics access per 0039.<br>§6: see 0038; the refusal vocabulary and its two classes; dry mode; finality per V26.<br>§7: note on mark `bench_code`.<br>§9: the `pre_live`, `test` and `off_benchmark` buckets; per-row direction from one predicate.<br>§10: the qfq fallback is now implemented, detection-only, bounds unchanged; the corrected limit backstop.<br>§12: new rows D10 onward, one per approved decision that changes a settled one: V3, V6b (including the benchmark leg from S0 and the bounded look-back), V8, V9, V10/V12/V14, V13, V15, V24, V25, V26. V4, V5 and V7 bring the code to existing text and need no new row; under V4(b) or V7(b) they would. |
| ADR 0002 | A note: the machine API is token-only in behaviour, not only in its route class (P34). |
| `docs/forecast-verification-design.md` | §7 and D6 superseded (pointer to 0039); acceptance 19 rewritten; §9.2 corrected (dividend element, corrected gap backstop, qfq ratio); §9.3 base rule for an S0 halt (V6b); §10 UI updated. |
| ADR 0028 | A note at §1 (above) and at §3: the detection-only qfq exception now exists. |
| ADR 0022 / ADR 0024 | Only if P30 lands: OU principals share only through effectively restricted OUs. ADR 0024: editions and forecast sets (C17). |
| ADR 0035 | No change. Steps 0005 and 0006 follow it. |

ADR numbers are confirmed at merge, after checking `docs/adr` again.

---

## 10. What we still refuse

These are consistent with ADR 0029 §11, and extended where this plan opens new surfaces:

- **No per-analyst score, ever. Also no per-OU or per-tenant score**, and no population selector that is a person, an OU or a single tenant. A small population is a personal score by another name.
- **No statistics on `/api/v1`, and none reachable with an app-bridge token. No browser credential on `/api/v1` at all.**
- **No outcome before maturity.** No return, excess or direction for a cell that is not mature, enforced on the server. No number below n = 30. No bare accuracy percentage without its null and its n.
- **No deleting or voiding ledger rows**, by hand or by feature. Population changes are boundaries in `meta` or classifications frozen at insert, audited and outcome-blind. **No rerun promotion** (D4 is unchanged).
- **No re-scoring.** No mature cell ever changes. Benchmark and definition changes apply to cohorts not yet written. After Release 1b, no settlement-logic change ships without the provenance step.
- **No return computed across two vendors.** No stored price from a source without an explicit `settled` grant. No cached body ever settles a mark.
- **No finality from an outage.** A mark becomes final only on its own evidence.
- **No label the numbers did not use.** No "baseline matches" without the §9.4 comparison. No "counted" without the statistics' own classifier. No "retrying" without evidence of an attempt. No "expected" date without a calendar behind it.
- **No periodic whole-market poller.** The evaluator fetches for due work only.
- **No LLM extraction of claims from report text, no verdict column, and nothing written to `tracking_items`.**
- **No backfill of the 1259 pre-ledger reports.**
- **No forecast content in Markdown or PDF exports.**
- **No usernames in adoption metrics.**
- **No new third-party runtime dependency for metrics** (Prometheus) in this plan. `axe-core` is a test-only devDependency, and only under V17.

---

## 11. Risks

1. **The owner actions in §0.1 are missed.** Wrong baselines become permanent from Monday 15:30.
   - *Mitigation:* the Release 1a notes carry a read-only query listing sets with `s0_date` set, or marks with `resolved_at` set, before the deploy. P7's anchor and calendar checks refuse further marks on a wrong S0. Any repair of written rows is an owner decision.
2. **A cell matures under the old hit rule.**
   - *Mitigation:* mode `1` is refused until P16a is deployed (§3.1). Q2 shows whether 2026-11-09 is a real maturity date.
3. **The 3-session halt presumption** permanently resolves as halted a single stock whose vendor series lags while the index is fresh.
   - *Mitigation:* the evidence path comes first, the valve covers vendor-wide lag, and V20 captures refine the rule.
4. **The ledger start gives an admin a one-time selection power.**
   - *Mitigation:* it is bounded to sets made before the first S0 close, pre-declared, audited, survives restores, and is disclosed by the `pre_live` count.
5. **Producers mislabel runs.** A production run sent as `test` is never scored. A test run sent without the field is scored.
   - *Mitigation:* the `test` count is shown per cohort, and the runbook entry covers it. The Dify-side change ships with a projection test.
6. **Tencent remains a single point of failure for scored marks.**
   - *Mitigation:* the failure becomes visible, alerting and non-finalising (V26). Only a second source with corporate-action evidence, or a licensed feed, removes it (Tier 2).
7. **The audience may be small**, if few internal non-admin readers exist.
   - *Mitigation:* run the §3.5 queries before V10, and set the adoption target under V18.
8. **Membership and the timeline add work to `/api/stock`** on a single SQLite connection.
   - *Mitigation:* the work runs only at stage `full` and for covered types; prefetch skips the summary; a failure omits only the summary; P18 records a benchmark; the `full` step waits on it.
9. **Binary rollback across 0005 is refused, and 0005 ships with its reader.**
   - *Mitigation:* the backup before deploy, `published=0` by default, and unpublishing as the operational rollback.
10. **P34 breaks an unknown cookie consumer of `/api/v1`.**
    - *Mitigation:* none exists in the SPA, the bridge or e2e after the rewrite. A 401 is loud, and the revert is one PR.
11. **Parity stays subjective.**
    - *Mitigation:* the annotated production baseline, the seeded preview and its screenshots are the acceptance gate (Appendix A).
12. **Some vendor and rule facts are unmeasured:** suspended and delisted stamps, qfq bodies around bonus issues, Sina's adjustment basis and index coverage, Yahoo's A-share coverage, and the current ST limit rule.
    - *Mitigation:* every dependent rule fails closed, or leaves the case to a stricter detector, until the capture or the rule text exists (V20).
13. **Compliance review delays the publish.**
    - *Mitigation:* the review gates only the publish switch. Everything merges and deploys, and admins see it all.
14. **zh-TW is machine-generated.**
    - *Mitigation:* the prefix-only regeneration and a named reviewer (V17).
15. **Several workstreams change the same evaluator and classifier functions.**
    - *Mitigation:* the lanes are serialised (P1 → P3 → P6 → P7 → P8; P12 → P5 → P36 → P4 → P16a), and each PR is small enough to review line by line.
16. **The target dates slip.**
    - *Mitigation:* none of them is a correctness deadline. The only hard deadline is Monday 15:00. Every other gate is a readiness check that the code enforces.

---

## Appendix A: demo parity checklist

Acceptance runs on the seeded preview at 375 and 1280 px, in light and dark, against the annotated production baseline (P13). The "Today" column is corrected from those screenshots before P19 starts.

| Demo element | Today | After (PR) | Note |
|---|---|---|---|
| Header, tabs and toolbar in one card | Separate rows | One card (P19) | |
| Breadcrumb | None | None | House style |
| Report-type select | Missing | A tag; a Select when there is more than one type (P19) | |
| Benchmark in the toolbar | Missing | Frozen code per group; `off_benchmark` chip (P4, P19) | |
| Monthly range | Missing | Newest 12 months, with "show older" (P19) | Client-side |
| Custom range prefilled | Blank tab | Default range, presets, limits from the server (P19) | |
| Metric help and explore banner | Short, and the hit text is wrong | Full copy, corrected (P15) | |
| Open cell dashed | Not dashed | Dashed (P19) | |
| Small cell | Text only | Final "n = x · below 30 · not published", no progress bar (P19) | |
| Pending cell | done/total only | Reasons by phase, progress, "not before"; h12 shows "from YYYY-MM" (P17, P19) | |
| Mature cell | 15 px, no sign | 18 px/600, `+` sign, hover and focus (P19) | Seed |
| Significance tint | Fails AA | AA palette, glyph and "q<0.05" text (P15) | Seed |
| Coverage bar | counted and repeat share a colour | Distinct segments with text; `pre_live`, `test` and `off_benchmark` chips (P19) | |
| Legend and methodology | One sentence | Swatch legend and a shared methodology card (P15) | |
| Evaluator status | A card above the matrix | Header pill and drawer for admins; full status on the operations page (P9, P27) | Deliberate change |
| Drawer | Partial metrics | Headline, after-fill, one-arm, slices, signed excess, four-state direction, halted tag; membership mode for non-mature cells (P16a, P16, P19) | |
| Report meta line | Clock only | Report date · received · edition (P20) | |
| "Counted in batch" | Always "not counted" | Membership from the classifier, unrestricted readers only; "counted on edition X" for siblings (P16, P20) | |
| Baseline badge | Generic tag | "Reported X = close on D", once the §9.4 check runs (P7, P20) | |
| Stance, weight, horizon | Stance only | All three, plus signed upside (P20) | |
| Scenario band | Three tags | Band with a cursor at the latest completed bar (P20) | |
| Checkpoint steps | Missing | Phase-driven steps (P20) | |
| Checkpoint table | "Fetch failed" | Phase rows with "on or after" or "expected"; signed returns; bold excess; touched; source (P10, P7, P16, P20) | |
| Expected checkpoint dates | None | Calendar-backed "expected" dates (V23 a, P37), or "on or after" (V23 b); the demo's own d20 and d60 corrected (P13) | The demo was off by one session |
| Other predictions | Bullets linked by receipt date | Table with exact links (P20) | |
| Chart overlay | Collapsed, flat lines | Toggle always shown; S0 window; dots at settled closes; legend with source; Sunday snap (P10, P20) | |
| Disclaimer | None | On the card, chip tooltip, record header and CSV (P15, P20, P27) | Not in the demo; V22 |
| Summary in the report header | Missing | Chip in the same paint as the body (P18, P20) | Not in the demo |
| Run page | Nothing | Chip and card (P20) | Not in the demo |
| Record outside Manage | Manage only | `/forecast` per V12 (P27) | The demo's breadcrumb assumed manager-only |

---

## Changes from critique

1. **Sequencing of the hit rule.** The shared hit predicate and the members-route maturity gate moved into Release 1b (P16a). Mode `1` is refused until P16a is deployed. Q2 counts covered ingests and distinct symbols per month.
2. **Golden snapshots.** P12 moved to the start of Release 1a, ahead of P4, P5 and P36. It is split into a cell snapshot (byte-identical across later PRs) and a drawer snapshot, which P16a regenerates in its own reviewed commit.
3. **Enrolment bypass on `/api/v1` (D2).** Verified at `apiui.go:160-165` with nine ungated call sites. It is now a security defect with its own PR (P34) and decision (V21): v1 becomes token-only and e2e section C moves to session routes. The cookie-on-v1 row is gone from §3.6, and ADR 0029 §5 is made true instead of amended.
4. **Forecast reader routes.** They use the existing `authSession` helper plus a `routeRow.gate` annotation, with tests that keep them in `sessionRoutes()` and behind the enrolment wall.
5. **Daily-limit table (F14).** `301` and `689` added at 20 %, Beijing at 30 %, and the limit computed as a fen-rounded price, since cheap stocks' limit days exceed 10.00 %. Listing days are exempt. ST is deliberately not modelled: the rule changed in 2025 and must be verified, and the qfq path covers it.
6. **V6b.** The benchmark leg always starts at S0's index close, and a bounded 400-day look-back covers long halts. Both have tests and are recorded in the D10 row.
7. **`pre_live`.** It is now a statistics classification only, and the sets settle normally. This removes the special cases the critique listed (counts, heartbeat, watchdog, phase, purge).
8. **Forward test runs.** Added a classification frozen at ingest (`state='test'`, V24, P36), with the Dify-side change (the hard-coded `emitter.app`) and the older-binary coverage note. Q1 checks set ordering.
9. **Restore rules.** Each new `meta` key has one. The ledger start survives restores, and a restore never raises the evaluator mode or the publish switch.
10. **Release split.** Release 1 is split into 1a (false statements, governance, security) and 1b (settlement correctness), with P8 falling to 1c if the capture slips. Each says what the owner sees.
11. **Benchmark form.** Explicit `bench_schedule` and `bench_cancel` intents, forms that post only changed fields, `enabled` as a one-release alias, audit on real change only, and e2e section G updated.
12. **Due selection.** It now drives from `idx_pred_marks_due` with a join; the redundant `''` branch is dropped; a query-plan test runs on both drivers.
13. **Transactions.** Stated the single-connection rule (compute outside, apply through the tx handle only), with deadline-bounded SQLite tests.
14. **Watchdog.** It moved to its own goroutine with `recover`, out of `cleanupLoop`. A status-only `/healthz/forecast` serves external monitoring.
15. **Seeded preview.** Added a dev-only clock seam that release builds refuse, the exact owner command, and outputs in `~/Downloads`.
16. **Candidate sources.** Added Yahoo, EastMoney and a licensed feed to §3.3 with their capabilities and licences, and a separate corporate-action evidence chain for Tier 2.
17. **Fallback honesty.** The summary and V9 now state that Tier 1 adds no fallback writes for scored marks. Finality counts mark-specific attempts only (V26), so outages never finalise.
18. **Access matrix.** Added with a table-driven test, plus the per-OU reading-stage question (V13d).
19. **Stage rollout.** Defined the `card` → `full` step: who, when, the criterion and the rollback.
20. **Prefetch.** It omits the summary but keeps the timeline flags and reserves the chip slot; it is included in P18's benchmark.
21. **Editions.** Defined the ADR 0024 display and membership rules (C17), with V25 and tests.
22. **Schedule.** Added a milestone table with target dates, model tiers per lane, and the first cut (R1a). It explains why the OU pilot is R2a and not one week.
23. **V10 trade-off.** Shipping 0005 with its reader, versus splitting them; the `forecast_grants(principal)` alternative shape is offered.
24. **Compliance and licensing.** Added a gate (V22, §3.9) and a disclaimer key.
25. **Rollback claim.** Corrected: an ADR 0027 backup before every deploy, and the heal predicate counted read-only first (Q3).
26. **Webhook events.** Registry, labels and payload documentation added to the definition of done and to the PRs that create the events.
27. **Weekly viewers.** The counter now uses salted per-week hashes, flushed hourly and deleted at week end.
28. **Projected dates.** Added V23 and P37. Verified that the demo's projected d20 and d60 are one session early, while its resolved d5 follows the ADR.
29. **Production baseline.** Screenshots before P13 are now a prerequisite, and Appendix A's "Today" column is corrected from them.
