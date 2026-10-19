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
