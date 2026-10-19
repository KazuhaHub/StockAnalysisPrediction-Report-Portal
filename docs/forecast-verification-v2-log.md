# Forecast verification v2 — implementation log

Decisions taken while implementing v2 that the documents did not settle, plus the small doc/code
reconciliations made in the same PR that touched them. Newest last. Each entry: date, the question,
what I chose, why, and how to back out.

This file is not a decision record the owner must ratify; it is the running record the owner reads at
each milestone. Anything that changes product behaviour, security, schema (beyond the agreed DDL) or a
release deadline is NOT logged here — it is raised with the owner and, once answered, appears in the
decisions page.

---

## 2026-10-19 — R1a-β1 start

### Merge order for the owner-verified PRs
#113 (docs) and #112 (the float-architecture fix) were merged first, as instructed, so P12 (#110) and
P3 (#114) could be rebased onto `main` with single-purpose diffs. #111, #110, #114 then squash-merged.

### P5: `populationBound` holidays
The build document defines `populationBound` as "the first 15:00 Asia/Shanghai on a weekday strictly
after the earliest `made_at`", deliberately holiday-agnostic (a holiday only moves the true first S0
close later, so the bound stays conservative). I implemented it exactly so — no exchange calendar — and
added a `BI` test for the production shape (Sunday 2026-10-18 evening CST → `2026-10-19T07:00:00Z`) and
the Friday-shape case. Back out: none needed; it is the documented rule.

### P5 review follow-ups (adversarial review, 2026-10-19)
- **Write-once under a race.** The pool read is a fast path only; the authoritative gate is the
  conflict-safe write (`INSERT … ON CONFLICT DO NOTHING` + `RowsAffected == 0`). This also stops a
  damaged stored value from being overwritten. Chosen over a schema constraint because `meta` is the
  frozen baseline. Back out: none — it is strictly safer.
- **A refused population write inside a multi-field POST.** The handler now parses/validates every
  field first and applies the ledger start before the benchmark, so a refusal leaves the benchmark
  unchanged. The alternative (rejecting mixed requests) would have changed the endpoint's contract.
- **Malformed input vs write-once ordering.** §4.4 lists "malformed" last, but the handler must parse
  the instant before a store call can see it, so a malformed value on an already-set ledger answers
  400 `invalid_param`, not 409 `forecast_population_set`. Both are refusals and write nothing; the
  ordering is unobservable in any other respect. Kept as-is; corrected in the §4.4 note if the owner
  wants the strict order.
- **`madeAtBefore` direction for an unparseable `made_at`.** Treated as NOT pre-live (fail-open to
  showing data). `made_at` is always server-written RFC3339, so this is unreachable; the alternate
  direction would silently hide a live set from the record, which is the worse failure. Kept, and
  documented in the function comment.
- **`forecast.population.not_counted_pre_live`** is reserved for the reading page (P10/P16); it is in
  the three locale files now so the parity test holds and P10 has the exact key to use.
- **An empty or NULL stored value.** Presence is now a row count, not a value read, so a present-but-
  empty key is still `set` (the UI does not offer a form whose save would always be refused) and the
  GET reports it `damaged` rather than as an untrusted instant. The restore prior's "unset" check is
  unchanged for an empty value (it carries from live); that direction is safe either way.

### P2 review follow-ups (2026-10-19)
- **The floor is captured once, not re-derived per read.** An earlier version re-derived it when the
  keys were absent; after audit retention that would read `MAX(reports.id)` at request time and
  perpetually count 0. Now it is stored at save time, re-derived once by `finishRestore` on the tx, and
  ensured once at boot for a boundary that predates P2; the read path only reads the stored key and
  shows nothing (`ok=false`) if it is absent.
- **`store_failed_30d` is computed once per GET**, not per covered rtype (it is ledger-wide).
- **The admin page's "since" date is rendered in Asia/Shanghai** (the boundary is a Shanghai instant),
  not by truncating the UTC string.
- **The audit deep link now actually filters:** `AuditPage` seeds its action filter from `?action=`.
  This also fixes the pre-existing `predictions.reject` and `forecast.population` links.
- **The store-failure line is per rtype and names it** (the rtype prefix), so a second covered type
  would not produce ambiguous duplicate lines.
- **Read-path/copy note (no permission change):** the old groups-page "N reports have no prediction
  row" warning is removed and the CSV's `# unrecorded=` value now means "since the ledger start"
  instead of "all history". This is the §4.5 copy change, not a scope change.
- **Legacy audit timestamps.** The floor query compares `audit_log.at >= from` lexically. Pre-v0.4.15
  audit rows use a different local format and sort earlier, which is harmless for a recent boundary
  and only latent if a boundary were ever set before that release. Noted, not changed.

### P36 decisions (2026-10-19)
- **Golden snapshot delta.** §4.6's test list said "golden snapshots unchanged", but §4.6 also adds
  `forecastCoverage.Test`; `golden_cells.json` serialises the whole struct, so every coverage block
  gains a `test` line. Regenerated in P36's own commit; values are otherwise byte-identical and
  `golden_members.json` / `golden_fixture.json` are unchanged. The doc line was corrected.
- **The test label lives in the existing panel.** A test set now renders the panel (it previously
  returned null when the only set was not accepted) with `forecast.panel.test_run` and without the
  checkpoint table. `counted_set_id` still skips test sets, so it never marks one as counted.
- **Adversarial review follow-ups (P36).** Fixed before merge: `forecast.state.test` added to all three
  locales (the other-sets list renders `forecast.state.*`); the panel prefers an accepted set over a
  newer test set and never shows the counted badge for a test; the test seed pins `made_at`/`made_on`
  (the ingest path uses the real clock); the "no state UPDATE" guard now scans every non-test `.go`
  file with a whitespace/case-insensitive regex, not just `forecast_*.go`; a backtest + `run_mode=test`
  case is pinned; `forecastCoverage.pre_live`/`test` added to the web type and the matrix bar/legend.

### P4 decisions (2026-10-19)
- **Golden delta.** §4.7 said "golden snapshots unchanged", but the same section adds
  `forecastCoverage.OffBenchmark`, which `golden_cells.json` serialises. Every coverage block gains
  `off_benchmark: 0`; all values are otherwise byte-identical. Regenerated in P4's own commit and the
  doc line corrected.
- **`mode` is 0/1 only in P4.** `enabled` is the alias; `mode:"dry"` is refused rather than stored,
  because dry-run readiness is P9's. The evaluator's existing `!= "1"` test keeps any future value off.
- **Reconciliation is boot-only for now.** P9's housekeeping will call `promoteForecastBench` daily;
  until then the effective-dated reader (`ForecastBenchFor`) already returns the right code for the new
  cohort, so the active meta value merely lags by a boot.
- **Off-benchmark is per set, not per symbol, except for the earliest.** Every accepted set whose
  frozen code differs from its cohort's benchmark is counted and excluded; if the symbol's earliest
  accepted set is off-benchmark the whole symbol is out (later sets are counted off-benchmark too), as
  §4.7 requires. In the default population the earliest set defines the benchmark, so that branch is
  defensive.
- **`counted_set_id` respects off-benchmark.** The report page's pointer now skips a set whose
  `bench_code` differs from its cohort's benchmark, so it cannot show a green "counted" badge on a set
  the statistics exclude. The cohort benchmark is the ledger-wide query, not the loaded slice.
- **A legacy `bench` plus `bench_cancel` is refused** as ambiguous, the same as `bench_schedule` plus
  `bench_cancel`; the cancel intent alone still works.
- **Adversarial review follow-ups (P4).** Fixed before merge: a save promotes a due-but-unpromoted
  schedule first, so cancelling/rescheduling cannot revert the current cohort mid-month; the pending
  pair is read in one statement and a malformed cohort is ignored; `counted_set_id` judges each
  candidate against its own cohort; the member drawer derives the benchmark with the ledger-wide query
  (and `populationFrom`), matching the matrix; the coverage bar accounts for `off_benchmark`.
- **Accepted, not a bug:** posting the *active* code as `bench_schedule` while a different change is
  pending replaces it (§4.7's table: a no-op only when nothing is scheduled). The modal posts only
  changed fields, and the legacy `bench` spelling is retired in P29.
