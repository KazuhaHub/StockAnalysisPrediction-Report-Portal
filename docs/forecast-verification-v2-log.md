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

### P1 decisions (2026-10-19)
- **Wake-only scheduling.** Before fetching a calendar, every non-terminal mark whose earliest
  possible due (a weekday projection, so a lower bound) is still in the future is rescheduled from
  the projection alone. A Friday batch therefore costs no vendor call until Monday's close, and the
  batch limit is spent on work, not on marks that cannot be due.
- **Today's arithmetic is preserved, including the extra session.** `predScheduleFor` keeps the
  present `len > n` rule and `sessions[n-1]` record; P6 changes the definition inside it. The
  characterization test pins the `len == n` / `len == n+1` boundary.
- **The heal signature drops the plan's "not due" clause**, as §5.1 says: by the β2 deploy four d5
  marks are due and the check would leave them with a phantom attempt. Returning them to pending only
  causes a fresh, recorded attempt.
- **Finality needs both halves.** A mark-specific failure becomes final only after 4 recorded
  attempts and 10 completed sessions past its due session; a source failure never increments attempts
  and never finalises. An unknown refusal code is treated as a source fault (retry forever is
  recoverable; finalising on an unclassified fault is not).
- **`predNotYetShown` during the session.** +10 minutes until 18:00, except a wake in the 09:00 hour
  moves to 15:30 the same day (rather than re-waking every ten minutes through the session); after
  18:00 it is 09:00 on the next weekday.
- **The P9 readiness gate is not built here.** This PR makes item 1 of §1.4 true (the binary contains
  P1); P9 adds the `forecast_not_ready` refusal and the per-item GET. The evaluator remains off.
- **Adversarial review follow-ups (P1).** Fixed before merge: a calendar with no session after the
  receipt reschedules instead of refusing (the baseline `no_bar` is reserved for "the start session
  exists but the stock has no bar"); `Due` is now the session after which the mark may settle, so
  `sessionsPastDue` starts at 0 and the 10-session conjunct is real (`predWakeAt` wakes on the same
  session); a source fault only refuses marks that could be due, leaving far-off checkpoints on their
  wake; a baseline deferral records S0 as its session, so the heal cannot mistake it for the pre-P1
  signature; a finalised mark records the failed attempt that finalised it; an unschedulable checkpoint
  terminalises with `no_schedule` instead of spinning; `baseline_check=refused_final` is rendered; the
  EXPLAIN tests now use the production selection text (and PG's `SET LOCAL` on the same connection).

### P6 decisions (2026-10-19)
- **S0 follows the 15:00 rule.** A receipt before 15:00 on a session starts that session; at or after
  15:00, or on a non-session, starts the next. A date is known not to be a session only once a later
  session is on the calendar, so a bar that has not arrived yet is never read as a holiday.
- **dN is the Nth session after S0**, and `Record` and `Due` are the same session: the mark may settle
  as soon as that session has completed. P1's extra-session rule and its `len > n` pin are replaced.
- **A terminal baseline is used as stored.** A later pass never recomputes S0/base/price from a revised
  vendor body.
- **The plan's transaction already writes the baseline first** (P1); P6 adds a failure seam to
  `ApplyForecastPlan` and pins that a failure on the second mark rolls the baseline and the first mark
  back.
- **Adversarial review follow-ups (P6).** Fixed before merge: a stored S0 missing from the calendar
  falls back to the receipt-based session for scheduling only (never an endless wake); a terminal
  baseline with a NULL `s0_fen` omits the return instead of recomputing the price from a revised body;
  a guarded baseline write that affects 0 rows aborts the plan (`errForecastBaselineRaced`) so a racing
  pass cannot settle on its own values; the unknown-today test now calls the evaluator path rather
  than being short-circuited by the wake-only pre-pass; a known h12 and a future bar are pinned.
- **P7 decisions.**
  - The V20 probe fixtures (a live suspended stock, a delisted one, limit days on 301/689/bj, a
    sub-¥2 stock) were not captured: they are read-only vendor probes and a synthetic bar can pin
    every rule they were meant to cover. The tests use synthetic series and the existing benchmark
    fixtures; this is the one deviation from §5.3's file list.
  - The stale valve is computed once per benchmark group from every stock body in that pass (more
    than 20% ending before the calendar's newest completed session). A fetch error is not a stale
    body, so a partial outage cannot trip it.
  - The bounded look-back (400 days) reuses the body it fetched for the marks too; otherwise the
    narrow window would refuse the very halt it just resolved.
  - The listing exemption for the daily-limit backstop is "the series' first five bars when the
    series begins at or after the window's base date".
  - A receipt after the close on a date the calendar does not show yet waits rather than comparing
    with a previous close: it may be a session whose bar has not completed, or a holiday.
  - The golden snapshots are unchanged: the fixture writes the baseline and marks directly, so P7's
    evaluator rules are not on that path. The comment in forecast_golden_test.go records this.
  - Halt evidence is stricter than §5.3's pseudo-code: a same-day vendor stamp counts only when it
    is at or after the close. A cached 14:57 body proves the stock had not traded by 14:57, not that
    it did not trade that session; the plan's "stamp date >= S" would settle a halt on a partial body.
- **P8 decisions.**
  - The qfq fixture pairs from §5.4 were not captured; every rule is pinned with synthetic raw and
    adjusted closes, including a property test that fen rounding alone never flags. This is the
    second deviation from a P-list's fixture column, for the same reason as P7's V20 probes.
  - `adjustRatioChanged` uses the plan's exact integer comparison with the +1 fen bound. A
    degenerate (zero or negative) price never flags.
  - One qfq call per set per tick covers [base_date, latest due session], and only when at least one
    mark is actually due. A failed call defers every due mark `fetch_failed` (source class).
  - A missing adjusted close for either the base or the due date is also `fetch_failed`: the check
    is part of the answer, never skipped.
- **P8 review follow-ups.** The qfq call is made only when at least one due mark's stock leg can
  actually resolve (bar present and both index dates printed), not merely when a mark is scheduled;
  and `TestAdjustFetchOnlyForDueMarks` now asserts d5 resolves, so the adjusted map is proven used
  rather than merely fetched.
- **P16a decisions.**
  - `predHitClass` thresholds on the raw fen upside, `abs(terminal-s0)*10000 < 500*s0`, so 4.99% is
    untested and 5.00% is tested, exactly as the plan's `|d|*10000 < predHitUpsideBp*s0Fen`.
  - A zero excess is `flat` and is counted in the hit-rate denominator (agree+disagree+flat), because
    the plan defines the hit rate over the tested subset; a flat row is a tested row that did not
    agree. A member with NO benchmark excess is `untested` and is not in the denominator at all:
    there is nothing to agree or disagree with (F08).
  - `golden_members.json` is regenerated in its own commit: `agree` (bool) → `direction` (four
    states). Cell states, n, and every member's identity and outcome fields are unchanged.
  - `golden_cells.json` is regenerated in the same commit: `hit_n` falls by one in every resolved
    column because one fixture member has no benchmark excess and is now untested rather than flat.
    The hit/null/p values move with the denominator; no other metric changes.
  - The members route returns the gate's `state` and no counts: the drawer's headline already comes
    from the clicked cell, so repeating the counts was dead surface. A non-mature cell sends
    identity and status only.
- **P16a review follow-ups.** A member with no benchmark excess is `untested`, not `flat` (F08): the
  cell's hit denominator now excludes it, which is why `golden_cells.json` moved from 34 to 33 in
  every resolved column. The members route reads the official month's cached cell state so a
  backdated set cannot make the drawer disagree with the matrix, and `TestMembersRouteScoresNothing`
  clicks twice to cover the memo. The gate's response counts and the unused UI fields were dropped.
  The two snapshot files are regenerated in their own commit with the byte-level explanation above.

## Current progress (2026-10-22)

- **Merged.** R1a-β1: P5 #116, P2 #117, P36 #118, P4 #119. R1a-β2: P1 #120, P6 #121,
  P7 #122, P8 #123, P16a #124. R1b: P9 #127, P41 #129, P10 #131, P38 #133, P39a #134,
  P30 #135, P31 #136, P11 #137. R2a: P15 #143. The standalone Go 1.26.10 security fix #144
  is merged; `origin/main` is at `ad618c1f`.
- **In flight.** P26 #145 on `feat/forecast-record-access`, in the isolated worktree. The strict
  resolver, reader routes/populations/redaction, compute governance, per-user settings/limits,
  cookie and bound-bridge prediction gates, OU settings/audit/preview, weekly count-only adoption
  and cross-surface SQLite/Postgres matrix are implemented. Independent final review and local
  validation passed, including older-server hint compatibility. Latest-head CI and the Apps
  integration sequence remain before merge. The parallel Apps branches remain untouched;
  shared identity fields and `mintForApp` match their proposed interface. A read-only merge
  preview still reports overlapping token edits; keep this PR a draft until those are resolved.
- **Next step.** Complete P26, then P27, P28 and P22. P13 still requires production baseline screenshots and owner
  review before P19/P20. P39b's Dify publication and undeclared-input gates are verified below;
  the R1b deployment and subsequent Portal target refresh remain outstanding. No production
  action has been performed here.
- **Known limitations.** P7/P8 still lack the real vendor fixtures documented below. The P15
  forecast translations are machine-generated, pending owner-arranged human review. Seeded
  responsive preview screenshots depend on P14. Re-check ADR numbers before P22/P23: the parallel
  notifications PR #141 already proposes ADR 0038.
  P30's legacy `g:<Default>` ambiguity and P31's missing-account behavior remain as recorded below.
- **Evaluator state.** `forecast_auto` remains off pending the owner-controlled readiness gates.

### P9 — operating the evaluator

- **Mode.** `forecast_auto` now carries `0` / `dry` / `1`. `step` reads it first and, when it is
  off, performs exactly that one read: the off path has no heartbeat, no write and no fetch. The old
  binary reading `dry` as off is deliberate and is the rollback story.
- **Dry writes are prevented at one door.** Every ledger write in a pass goes through
  `applyPlan`; it drops the plan in dry mode, and in mode 1 drops it for a benchmark with no recorded
  hand check (`forecast_dry_gate`), recording the code in the heartbeat's `awaiting_gate`. A separate
  "what would happen" path would have drifted from the real one, which is why the same planner runs.
- **Readiness is the §1.4 list, items 1–5.** Item 6 (ledger chain) is absent until P23 and a test
  pins the count at five. Item 5 reads `forecast_alert_test_ok_at`, which P41 writes; until then
  mode 1 is reachable only by failing that item, so the evaluator stays off after this PR even if
  every other item holds. That is the release order, not an oversight.
- **The gate codes are the union of the active benchmark and every code frozen by an accepted set
  with a non-terminal mark.** Pre-live sets are included on purpose: the evaluator settles them, and
  counting only in-population sets would leave the list empty at the switch. With no accepted set at
  all the active code is listed, so the item is never vacuously true.
- **Watchdog.** Its own goroutine, its own recover, one settings read while off. `stalled` after ten
  minutes (at most hourly), `lagging` on a due mark over six hours, a tripped valve, or a benchmark
  calendar still ending before today after 18:00 CST on a weekday (at most daily). `no_source` is not
  emitted before P23, as the plan says: there is no ledger chain to check yet.
- **Restore (§3.3).** `forecast_eval_state`, `forecast_alert_state`, `forecast_dry_gate` and
  `forecast_alert_test_ok_at` are deleted on this transaction; the mode is the lower of dump and
  live (`0 < dry < 1`), audited only on a real change; and a result of `1` is capped at `dry` with
  `{reason:"restore_gates_empty"}`, because the gates were just reset and mode 1 without them would
  plan every set and write none. The audit row is written through the restore's own transaction.
- **Health route.** `GET /healthz/forecast` is public and answers exactly `{"ok":…}`, 503 when the
  mode is not off and the heartbeat is missing or older than ten minutes. A switched-off evaluator is
  200.
- **Deviations from the plan's test list.** `TestWatchdogRunsWhileCleanupIsSlow` is a timing
  assertion (the watchdog shares no lock with cleanup), and the panic tests drive `step` and the
  watchdog's safe wrapper rather than an infinite ticker. `Healed` stays 0: the repair runs at boot,
  not per pass, and nothing in P9 changes that.

### P41 — alert channels

- **Two channels beside the webhook.** `forecastAlert` sends one message to the configured recipients
  (through `sendEmail`, so a disabled or unconfigured SMTP is a recorded channel outcome, not a
  silent drop) and to the Slack webhook. The webhook event itself is still fired by the producer —
  the watchdog and the store-failure branch — so a reason produces exactly one webhook delivery and
  one delivery per channel. A failing alert never fails the operation that produced it.
- **The Slack URL is write-only and host-locked.** It is stored to be POSTed to, never returned, and
  only `https://hooks.slack.com` is accepted (a test-only loopback allowance exists for httptest). A
  transport error from `net/http` carries the URL, so the error is scrubbed before it is stored,
  logged or shown.
- **`forecast_alert_state` changed shape.** It now holds `{last: {reason: at}, channels: {email:
  {at,ok,error}, slack: …}}`. `last` is the watchdog's de-duplication; `channels` is the page's last
  delivery. An older flat map is ignored once, which re-fires each reason one time after the upgrade.
- **The test alert is the readiness item.** `POST /api/admin/forecast/alert-test` writes
  `forecast_alert_test_ok_at` only when BOTH channels succeed and answers 502 with the per-channel
  errors otherwise. A real change to either channel deletes the stamp: the previous test no longer
  proves the channel that is configured now.
- **The ingest path is asynchronous.** The `predictions.store_failed` alert runs in its own goroutine
  with its own recover, after the ledger write has returned, so the producer's response never waits
  on SMTP or Slack and the alert never runs inside a transaction.
- **Not in the e2e script.** A test alert needs a real mailbox and Slack webhook; section G's
  readiness check therefore still sees item 5 fail on the throwaway portal. The channels are pinned
  by unit tests with the mail seam and an httptest Slack stub.
- **P41 review follow-ups.** The scrub now removes every spelling of the URL (`raw`, `u.String()`,
  `u.RequestURI()`), because `url.Parse` normalises an uppercase scheme and the raw string alone
  would have missed. Redirects are refused (`CheckRedirect` → `ErrUseLastResponse`): a 302 would
  leave the host list and turn the POST into a GET whose 2xx looks like a delivery. A change to the
  SMTP configuration now clears `forecast_alert_test_ok_at` too, and a stamp in the future is not
  accepted. The card requires BOTH channels for readiness (not "any"), treats a stamp older than
  thirty days as unverified, keeps a typed webhook when the save is refused, and translates the three
  known channel errors.

### P10 — reading-page hotfix

- **C1.** The predictions effect clears `predSets`/`predCounted` and the error as soon as the report
  id changes, before the request, and drops a response whose id is no longer current. The panel and
  the chart overlay read the same state, so both stop describing the previous report immediately.
- **C2.** A failed request sets an error with an inline retry; it never renders as "this report made
  no prediction". The report body is independent of the request and always renders.
- **C3.** `PredictionMark` gains `phase` and `not_before`. The read routes fill them from
  `predScheduleFor` over a weekday projection (no vendor call), with the vocabulary
  `waiting_s0` / `waiting` / `retrying` / `resolved` / `final`. `retrying` requires a recorded
  `session_date`: a mark that has never attempted is waiting, which is the whole defect. The
  `forecast.mark.refused*` strings are removed.
- **C4.** The counted / not-counted badge is gone until real membership ships. Every set used to say
  "not counted", which was not a fact about that set.
- **C5.** `include=payload` is honoured only for a nil (unrestricted) scope. Both read routes share
  the same helper, so the session and v1 credentials cannot diverge.
- **C7.** The quality enum is translated (`forecast.quality.*`).
- **C8.** A marker with no bar snaps to the first bar on or after its date; one past the series is
  still dropped. The overlay group's guard is a boolean, so an empty pair of arrays no longer renders
  a stray `0`.
- **Deviation.** The production 20-mark tuple is represented by a synthetic set pinned to the same
  receipt (2026-10-18 21:00 CST, S0 2026-10-19, d5 2026-10-26); the repository has no fixture of
  those rows. W1 and W3 use a real `MemoryRouter`, not the router mock the older page tests use.
- **P10 review follow-ups.** A marker older than the chart's first bar is dropped again rather than
  pinned to the left edge (the snap only applies inside the series). The predictions state is one
  value keyed by the report id, so the render that already shows report B's header can never paint
  report A's marks — the effect alone left one frame of the old panel. The C5 test now covers the
  list route as well as the per-report route, and the phase test uses a stored S0 that differs from
  the projection (2026-10-20), a recorded session before its due date, and an assertion through the
  handler, so neither the stored-S0 rule nor the wiring can regress silently. The W1 test also
  covers a late answer for the report that was left. Note: `viewerScope` still fails open if the
  account/OU read fails; that is P31's, and C5 inherits it.

### P38 — the access schema

- **Step `0006`, exactly as approved (§3.2):** `user_groups.forecast_scope TEXT` (NULL = inherit),
  `prediction_sets.attributed_ou BIGINT` (NULL = unattributed), and `idx_pred_sets_ou`. No CHECK
  constraint; the closed set is enforced in Go and an unknown value resolves as `none`. No back-fill:
  the Default OU is normalised to `none` at boot and at the end of every restore, on the restore's
  own transaction, so an older dump still loads.
- **A strict OU index (`ou_index.go`).** The whole table in one read; `chain`, `restrictedEffective`
  and `subtree`, plus `effectiveScope` (nearest non-NULL up the line, with a NULL Default read as
  none — the helper P26 will use). A dangling parent is `errDanglingOU`; a cycle or a chain over 64
  is `errCyclicOU`; a truncated chain is never returned, because that would drop a restricted
  ancestor and fail open.
- **Frozen attribution.** `v1Ingest` decodes the owner token once (it already did, before the
  insert), hoists the OU out of the branch, and sets `AttributedOU` before `InsertPredictionSet`,
  which is the only INSERT that names the column. A missing or invalid token leaves it NULL.
- **Deleting an OU is one transaction.** Its sets are re-attributed to its parent (or the Default for
  a root or a dangling parent) before the row goes; its children move to that parent (or become
  roots), instead of the old behaviour of silently making them roots and re-inheriting from the
  Default; its principal rows go as before. The handler audits `forecast_sets_reattributed`,
  `forecast_sets_to_ou` and `children_moved`, and drops the forecast group cache, because a root's
  sets join the Default's population without changing its population key.
- **Deviations.** The Postgres twin covers the step, the index and the delete's re-attribution; the
  trigger-based atomicity test and the cache/audit tests are SQLite-only (no equivalent injectable
  failure without a rule). The reused-id test simulates the id coming back by inserting an explicit
  id after the delete, which is what the restore's identity reset produces. The pre-P38 wind-back
  helper now also removes `0006`'s products and ledger row: a database "before the forecast step"
  must not claim a later step whose tables it no longer has.
- **Rollback is restore-only.** An older binary refuses a database that records `0006`
  (`refuseUnknownLedgerRows`), so R1b needs the ADR 0027 backup before deploy.
- **P38 review follow-ups.** A dangling grandparent makes the deleted OU's children **roots**, not
  children of the Default: a root is outside the Default's subtree, so its sets cannot join the
  Default's `own` population — the fail-closed direction. An ingest carrying a token for an OU that
  has since been deleted leaves the set unattributed (the token lives seven days). The audit's
  `forecast_sets_to_ou` is 0 when nothing moved, and `children_moved` is `[]` rather than null. The
  older-dump tests now actually run: the index is dropped before the column, a set exists to come
  back NULL, and Postgres has its own case. The boot normalisation is tested through a reopen, not
  the helper. The group cache gained a generation counter, so a cell computed across a purge (an
  in-flight request that missed the cache before the OU delete) is not stored afterwards.

### P39a — reserved inputs

- **`_rp_*` is the portal's namespace.** `isReservedInput` gates four places: the probe and the refresh
  remove such inputs from the user-facing list and record the names in the target's config JSON
  (`reserved_inputs` — an existing TEXT column, no schema change); stored configs are filtered on
  read (editor, target list, run form, CSV); the run form's submit deletes every reserved key from
  every row before it is stored or audited; and `runInputs` strips them from the caller's copy before
  injecting the minted token, so a caller-supplied value is never forwarded, even for an internal
  user.
- **The recorded names are a fact, not a guess.** A refresh replaces `reserved_inputs` with what the
  workflow declares now, so a workflow that stops declaring the key stops receiving it. An editor
  save that omits the field keeps it; a client can only record genuine `_rp_*` names.
- **Absent vs empty.** The refresh apply treats an absent `reserved_inputs` as "the client did not
  know about this field" and keeps the stored value (an older SPA bundle); a present list, even
  empty, is the workflow's answer now. The shipped UI always sends it, so a dropped declaration
  clears it.
- **P39a review follow-ups.** A workflow that gains or loses only the reserved key has an identical
  user-facing list, so the refresh preview now counts the recorded list as a change: without that the
  confirm never ran, and the names were neither written after the Dify publish nor cleared when a
  workflow dropped the declaration. The preview's diff and the audit compare the *filtered* list, so
  a pre-P39a config no longer shows a reserved input as a removal. Recurring-task templates strip
  reserved rows on save (they were the one row-storing path that bypassed the submit strip). The
  recorded names are validated and deduped on read as well as on write.

### P30 — OU principals only for restricted OUs

- **Writer.** `AddReportViewer` writes `g:<OU>` only when `ouIndex.restrictedEffective(ou)` is true
  with no error. An index read failure or a dangling/cyclic chain writes the `u:` row only: less
  sharing, never more. The rule lives in the writer, so every caller inherits it.
- **Reader.** `viewerScope` adds `g:` for the chain's effectively restricted OUs only, plus the
  Default — the "all" audience is stored as the Default principal, and dropping it would have
  silently broken "everyone". An unrestricted container is therefore not a principal, so a report
  attributed to it no longer reaches the tenants below it. Existing `g:<unrestricted OU>` rows are
  left in place and match nothing.
- **Q5 is an owner-side check.** This branch has no production database; the runbook's
  `report_viewers` count by principal should be attached before deploy. The reader-side rule makes
  those rows inert even if they remain.
- **Tests.** Two account-restricted users in one unrestricted OU do not share; a restricted tenant
  does not see its unrestricted container's report; two members of the restricted tenant still
  share; ingest writes a group row only for a restricted OU; a dangling chain and an unreadable
  index write no group row; and an "all" audience still reaches a scoped reader.
- **P30 review follow-ups.** The reader-side rule now has its own test: a legacy `g:<unrestricted
  container>` row written directly (as every pre-P30 build did) must not match a member of that
  container or of a tenant below it, and the scope must not carry the container principal. That test
  fails with the old reader and passes with the new one. **Known limitation:** `g:<Default>` cannot
  be made inert, because the same principal encodes the "all" audience and a legacy ingest row for a
  Default-attributed run is byte-identical to it; those rows stay readable, and the log must not
  claim otherwise. The Default is normally unrestricted, so the exposure is the pre-P30 one, not a
  new one; closing it would need a different "all" encoding (an owner decision, not P30's). The
  comments that still described "every OU up the chain" and "both principals always recorded" were
  corrected, and the reader comment now says plainly that `isRestricted` is still lenient until P31.

### P31 — an unreadable restriction scopes to nothing

- `isRestrictedStrict` reads the account row through `userRestrictedRow`, which distinguishes "no such
  row" from a read failure, then the OU chain through the strict index. A read failure, a dangling
  parent or a cycle returns an error; `viewerScope` turns that into `ownerScope{denyAll: true}`, whose
  `where` is `1=0`. A missing account row (a machine caller, a test identity) is not an error and
  remains unscoped, because there is no restriction to read.
- The lenient `isRestricted` stays for the callers that only ask "may this account configure things";
  the reading path uses the strict form. `viewerScope` is also strict about its own index read and
  chain walk, so an account-flagged restricted user cannot end up with a scope that silently lost a
  restricted ancestor.
- Tests: an unreadable account table and an unreadable OU table both deny on the report route (404)
  and return no sets on the list route; a dangling chain denies; a readable restriction still reads
  its own report; an ordinary internal account still gets no scope.
- **P31 review follow-ups.** Same-day reuse now refuses a deny-all scope: it would otherwise hand
  back another OU's report and write a `u:` viewer row for a caller whose restriction could not be
  read, turning a failure into a silent, unreadable success. The list-route test now seeds a
  prediction set, so its assertion has something the unscoped bug would have returned. A missing
  account row stays unscoped (a session route has already validated the account; the window is a
  request racing a deletion), while a read failure still denies — the comment now says exactly that
  instead of calling it a machine caller.

### P11 — ADR 0037 and the Release 1 amendments

- **ADR 0037** records the definition repair (S0/dN, terminal baseline, not-due, finality, settlement
  checks, the adjusted-ratio detection), governance (ledger start, test runs, benchmark schedule,
  unrecorded), operations (modes, readiness, dry gate, heartbeat, watchdog, health, alert channels,
  the single-connection rule), the restore rules, and the five escapes (F01, F03, F06, F10, F14)
  with their causes.
- **ADR 0029** gains a dated amendment block and in-place notes at §5, §6, §7, §9, §10 and §12; the
  new settled decisions (V3, V6b, V8, V9, V18, V24, V26, the access model, the owner token) are in
  §12's table.
- **ADR 0017** notes that `pre_live`/`test` sets are purged by the ledger's own retention target and
  that `attributed_ou` is not a retention key. **ADR 0022** records the restricted-only `g:` principal
  and the reserved `_rp_*` inputs. **ADR 0024** records the P30 amendment to the "both principals"
  paragraph.
- **The design document** points from §7 and D6 to ADR 0039 (R2a), corrects §9.2 (the dividend element
  is a cash signal; the ratio is primary; the exact fen limit backstop), adds the S0-halt rule to
  §9.3, notes the phase vocabulary in §10, and extends acceptance condition 19 with the forecast
  scope.
- **`docs/forecast-verification-operations.md` is created** with the signals that exist today
  (stalled, lagging, awaiting_gate, store_failed, test/off_benchmark counts, the switch-on sequence,
  scheduling, the ledger start, deploy/restore). Later surfaces add their own rows.
- **Review follow-ups.** The source-outage runbook now explicitly describes the evaluator's direct
  Tencent fetch, not the quote panel's configurable source order. The adjusted-price detection
  exception points to ADR 0028 §3 and ADR 0029 §10 rather than the integer-price section.

### P15 — web foundations (2026-10-22)

- Moved the matrix, drawer and their existing tests into `components/forecast`; routes, query
  contracts, maturity gates and metric values are unchanged. The audit deep link was already
  implemented and tested by P2, so its files were not changed.
- Added algorithm-derived significance colours (light red7/green8; dark red8/green6), tested
  against their actual tints at AA contrast, and a glyph plus textual significance cue. A source
  scan rejects literal colours in shared, admin and reading-panel forecast components.
- Added shared probability/signed-number formatting, a significance legend, methodology and the
  approved disclaimer. Corrected the absolute-upside threshold, flat misses, missing-excess
  exclusion, and spread/IC null explanations. Reader-page integrations remain P20.
- Added `--prefix=forecast.` to the existing OpenCC generator. It reconverts only that family,
  preserves unrelated reviewed translations (including keys absent from the source), and removes
  stale keys only inside the selected family. Tests pin repeat-run byte stability, empty-prefix
  rejection and the forecast fixed point. The family remains pending owner-arranged human review.
- Review found and fixed prefix regeneration deleting unrelated stale keys. The generator's
  status message now distinguishes a deliberate family reconversion from placeholder repair.
- Validation: Go build, vet, all SQLite tests, the full app suite with Postgres, staticcheck, web
  typecheck, 120 web test files / 1006 tests, and the production build passed. The extra prefix
  preservation regression and updated source scan passed after the full run. Axe checks pass for
  shared guidance under both themes; jsdom cannot validate layout or contrast, so contrast is
  measured separately and seeded visual review remains pending P14. No golden fixture changed.
- Independent adversarial review found no actionable issues. Browser checks of the real shared
  components in a temporary local harness covered 375/768/1280 px in both themes, with keyboard
  focus and no page-level horizontal overflow. The owner spotted uneven checkpoint heights;
  boxes now stretch to their row's tallest content (83 px at 1280; 126 px at 375/768 in this
  fixture). Screenshots are saved under `~/Downloads/forecast-foundations-p15-401f6cc4/`.
  This component preview does not replace P14's seeded full-page acceptance.
- Merged as #143 after full CI on `cb77ccba` (run 39766606158), including SQLite/Postgres,
  race, web and container smoke/E2E checks. Independent review and full local checks passed.
  Rollback: revert the PR; no data or schema changes.

### P39b precondition audit (2026-10-22)

- Read the live Dify Console API: all 16 published workflows declare the optional owner input;
  all 18 ingest-node and 11 child-tool bindings are correct. All 12 workflow-tool providers carry
  the input and report `synced=true`. Publication occurred on 2026-10-19, so the earlier handoff's
  pending-publication note is stale. No workflow was modified or published during this audit.
- With owner authorization for tests, ran the existing wait-only test workflow once with `n=0`
  and an undeclared fake owner input. Run `57abe025-2ee3-4ee8-a282-ab788578a58e` succeeded on all
  three nodes; the normalized workflow inputs omitted the undeclared key. The Console draft
  runtime therefore ignores it. No LLM, HTTP, report ingest or email node exists in that graph.
- Repeated the same authorized test through the published Service API using the test app's existing
  key (no key created or exposed). `POST /v1/workflows/run` returned HTTP 200; run
  `f6a85b7b-73ce-4116-8294-9b0986e84a2c` succeeded on all three steps with `waited_minutes=0`.
  The undeclared input does not reject a run through the same API path Portal uses. This closes
  the undeclared-input runtime question in §11.1; deployment and target refresh remain separate gates.
- Read-only container inspection found Portal `v2026.42.3`, revision `c9b2b747`, started
  2026-10-18. P38/P39a/P30 are not deployed, so P39b remains blocked on the R1b production rollout
  and the subsequent Portal target refresh. No deployment or production-data write was performed.

### P26 — access implementation in progress (2026-10-22)

- The pure resolver reads the account fields once and the strict OU index in one read-only
  transaction (Postgres repeatable read). It denies missing/inactive accounts, errors, unknown
  values and broken chains; nearest-value inheritance and external restrictions are independent
  passes, so a leaf override cannot hide a restricted ancestor. No authorization result is cached.
- `own` uses the leaf's actual parent-edge subtree; Default's appended inheritance role does not
  make unrelated root OUs descendants. The loader filters `attributed_ou` before membership is
  computed. Existing callers explicitly retain `all` until their request gates are integrated.
- Population cache and seed helpers retain the exact `all` identity and add the sorted subtree
  digest for `own`. Existing golden snapshots are unchanged. A deny population has a non-nil
  empty OU slice, never the nil slice that means the whole ledger.
- Tests were added first and failed for the missing resolver, population loader and identity
  helpers. SQLite/Postgres tests pin attribution, account moves, subtree changes, account reads
  and failure directions. Another OU's earlier set cannot displace the own population's member.
  A deterministic Postgres interleaving starts and ends denied: using read-committed isolation
  incorrectly mixed the old account and new tree and failed by granting `all`; repeatable read
  passes, and the next request observes the committed account move.
- This is a partial implementation, not an access rollout. No new reader route is registered yet.
  Remaining: per-user limits and the limits form before route registration, route gates and full access matrix,
  cookie/bridge prediction shaping, `/api/me`, OU
  settings and preview, compute pools/flights/rate limits, count-only weekly adoption, StockPage
  suppression and the complete security/regression suite. No schema change is included.
- Checkpoint validation: build, vet, full SQLite Go suite, full Postgres app suite, staticcheck,
  web typecheck, 120 Vitest files / 1006 tests and production web build passed. The existing
  attribution source guard initially rejected the new reader; it now permits only that loader's
  single read predicate and explicitly forbids writes there, retaining all original writer checks.
  Independent adversarial review found no actionable issues in this foundation checkpoint.
- Reader handler checkpoint: groups and members now share population-aware implementations with
  the admin aliases, which retain their existing response shapes. Internal `all` records match
  admin records except `unrecorded`; own period bounds, seeds and cached cells are isolated from
  other OUs. The clock is injectable on both handlers.
- External `all` shaping omits coverage, unrecorded and every denominator/count, recursively
  including policy slices. Mature cells retain metrics and `min_n_met`; non-mature cells carry
  state only. Copies protect cached cells from redaction. External `own` retains its own counts.
  External members are report-readable only, omit purged/orphaned rows, and paginate after that
  filtering with opaque cursors and no global total. Custom ranges and none scopes are refused
  before query validation or ledger reads. Two refusal messages were added to all locales, with
  zh-TW produced by OpenCC.
- Review found the member drawer taking frozen matrix state but recomputing counts after a
  backdated insert. It now takes frozen n/refused/corporate-action counts and reconstructs the
  mature gate's resolved/total counts. Resolved and pending backdated inserts failed first;
  regressions now pass, including a cell with both excluded outcome classes. Follow-up review
  confirmed the issue closed. Existing golden snapshots remain unchanged.
- Reader checkpoint validation: build, vet, full Go suite (SQLite), full Postgres app suite,
  staticcheck, web typecheck, 120 Vitest files / 1006 tests and production web build passed.
  The extra excluded-count regression also passed after the full Go runs were started.
- Compute checkpoint: only uncached mature scoring occupies a slot. Official and exploratory
  pools are independent; identical population/range/checkpoint requests share a detached flight.
  An official follower promotes a queued exploratory flight rather than waiting behind its pool;
  an already-running shared computation is not duplicated. Caller cancellation does not cancel
  followers. Waiting is bounded, and scorer panics release capacity with a generic failure.
- Groups handlers now return a retryable `forecast_busy` (503 plus `Retry-After`) rather than a
  partial record when capacity is unavailable or the request wait expires. Cache hits and
  non-mature gates bypass slots. The existing limits API reads/writes all five bounded settings;
  omitted values remain unchanged, and invalid values are rejected before writes. The two
  per-user settings are not yet enforced; their limiter and the web form remain pending.
- Review identified a generation race when a purge occurred after loading sets but before worker
  admission: stale inputs could acquire the new generation and refill the memo. The request now
  captures generation before any population reads, carries it through flight/cache operations,
  and rejects changed generations. A deterministic test deletes sets and purges at the existing
  settings-read seam between loading and admission. A late-generation mutation failed by serving
  stale HTTP 200; the fixed path answers 503, leaves the memo empty, and reloads on the next call.
  Follow-up review confirmed the issue closed. No reader route has been registered yet.
- The plan assumed an existing limits audit, but the security-save audit did not record limit
  values. Resolved numeric before/after limits are now recorded under the existing `policy.change`
  action with target `security/limits` immediately after a real change. This also covers the
  existing partial-save behavior when a later policy block fails validation. Tests failed first
  for missing values and for that unaudited partial-save path, then passed; unchanged saves do
  not create another limits-change row. No secret or schema change is involved.
- Compute checkpoint validation: build, vet, full SQLite Go suite and full Postgres app suite,
  staticcheck, three targeted race repetitions, web typecheck, 120 Vitest files / 1006 tests and
  production build passed. Final full Go tests with Postgres enabled passed again after the
  limits-audit refinement. Independent review confirmed the generation and audit fixes; the
  remaining P26 requirements above still block merge.
- Matrix alignment correction: replaced unreliable percentage heights inside native table cells
  with shared grid tracks and stretched flex cells. Mixed mature, pending, small and open states
  now share the same top/bottom edges within each row, including wrapped text and exclusions.
  Group labels and metadata wrap within the first track instead of overlapping a checkpoint.
  Browser checks at 1100, 740 and 360 pixel container widths confirmed equal card rectangles
  in all six fixture rows and no label overflow. Added a regression test; all 26 forecast
  component/page tests, typecheck and production build passed. Independent review closed the
  long-label overflow finding. Apps code and production state were not changed.
- Reader governance checkpoint: custom/member request ceilings are per-user, independent fixed
  windows. Zero removes only the frequency ceiling. A user may have one distinct custom query
  in flight; identical followers share scoring but spend request budget. Detached flights retain
  the query lease after an HTTP cancellation, releasing it only when the background work finishes.
  Rate refusal is `forecast_rate_limited` (429 plus `Retry-After`). Existing admin aliases retain
  their behavior. The security form now exposes all five bounded settings and preserves zero.
- Registered the two reader statistics routes through the new `forecast_scope` gate annotation.
  Route-table tests enforce session-only authentication, the enrolment wall, `none` denial, and
  refusal of persistent and bridge bearer credentials. Positive all/own sessions receive records,
  never `unrecorded`, and cannot use the admin aliases. Revoking scope after memo population
  immediately refuses the same session. The tests first failed for the absent gate/registration;
  the added positive fixture also required the signing configuration omitted by machine fixtures.
- Independent limiter/settings/route review found no actionable issue. Full Go tests with
  Postgres enabled passed; all 120 Vitest files / 1008 tests, typecheck, build, vet, staticcheck,
  production web build and three targeted race repetitions passed. The final scope-revocation
  extension passed separately after the full run. Remaining P26 work: prediction cookie/bridge
  gates and shaping, me hints, OU editing/audit/preview, weekly count-only adoption, StockPage
  suppression and the complete cross-surface access matrix. This draft is not ready to merge.
- Reading-page hint checkpoint: `meJSON` now returns only `forecast: {scope, external}` from the
  strict resolver, including the shared login payload. Auth context defaults a missing hint to
  `none`. StockPage skips both prediction reads at `none`, immediately hides loaded forecasts on
  revocation and suppresses only the server's 403 `forecast_unavailable`; other failures retain
  the existing inline retry. Tests first failed for absent hints, unwanted reads and retry UI.
- Review found that an enabled-to-enabled policy change (internal to external, or all to own)
  could leave old data displayed. Both requests and result states now carry the full user/scope/
  external identity; symbol history also carries its symbol. Derived values reject old-policy
  results before effect cleanup. Dynamic regressions clear the old value, require both refetches,
  and then show the new value. A mutation removing the full-identity effect dependencies failed
  both regressions; restoring them passed. Independent follow-up review closed the finding.
- Hint checkpoint validation: full Go suite with Postgres enabled, vet, staticcheck, web typecheck,
  all 120 Vitest files / 1016 tests and production build passed. Server-side prediction gates and
  bridge shaping remain pending; navigation suppression does not substitute for those gates.
- Cookie prediction checkpoint: both SPA prediction reads and both v1 cookie reads now resolve
  current forecast permission before ledger access. `none` answers `forecast_unavailable` after
  the independent enrolment wall. External/scoped views return accepted, report-backed sets only;
  the SQL filters precede LIMIT so newer rejected or orphaned sets cannot hide readable results.
  Payload, errors and membership pointers are omitted. Persistent machine reads keep diagnostics,
  rejected sets and orphaned history. New failure-direction tests first reproduced the old 200
  for `none`, membership leakage and rejected rows consuming a one-row limit. Store controls run
  on SQLite and Postgres. Existing positive fixtures now explicitly grant forecast access; broken
  account/tree fixtures expect the new strict 403 instead of the old report-level 404.
- Review found an external decision could race account deletion or restriction removal and then
  receive nil (unrestricted) legacy report scope. The shared view composition now substitutes
  deny-all for an external decision plus nil scope, matching the member drawer. Deterministic
  deletion and unrestriction interleavings first leaked on both report and symbol reads, then
  passed with the fallback. Independent follow-up review confirmed the finding closed.
- Apps PRs 141 and 142 both extend app-token identity using username, session revision and account
  creation identity. Their shared files remain untouched here; bridge prediction identity is still
  pending and this draft must not merge before that integration and all other P26 requirements.
- Final cookie checkpoint validation: full Go suite with Postgres enabled passed after the
  interleaving fix; three targeted race repetitions, vet and staticcheck passed. No web changes
  were made in this checkpoint. Independent review has no remaining finding in this slice.
- OU scope settings checkpoint: PUT group settings decodes `forecast_scope` as RawMessage so
  omission leaves it unchanged, explicit null restores inheritance (Default stores `none`), and
  only `all`, `own` or `none` are accepted. Invalid values are refused before any existing group
  mutation; tests also prove they do not rename/restrict the OU or emit an audit change.
  The scope save uses a transaction and locks the prior value on Postgres, returning an exact
  from/to delta only on change for the existing `group.change` row. No schema changes were made.
- GET groups now supplies the own nullable value, strict effective scope and source OU. Tests
  first failed for absent persistence and missing inherited fields, then passed, including exact
  audit deltas, omitted/null distinction, Default normalization and Postgres storage controls.
  Independent review found no actionable issue. Added the new refusal translation in all three
  locales, with Traditional Chinese generated through OpenCC. The access impact preview remains
  pending, along with bridge integration, adoption counts and the complete access matrix.
- OU checkpoint validation: full Go suite with Postgres enabled, three targeted race repetitions,
  vet and staticcheck passed. Web typecheck, 28 locale/generator tests and production build passed.
  The full error-code parity guard caught the missing new translation before it was added. The
  final Go run was serialized after the frontend build to avoid the existing dist cleanup/embed
  race during concurrent builds. The branch remains a draft; no production state changed.
- Admin impact preview checkpoint: `GET /api/admin/forecast/access` reports each OU's nullable
  own scope, strict effective scope/source, external flag and direct-member account counts, plus
  overall counts. Unassigned members count under Default; root-OU members and admin overrides
  are included. Snapshot reads active accounts, the panel timezone and the OU tree through one
  read-only transaction (repeatable read on Postgres), and returns no account identities.
- Optional `ou` and `scope` parameters preview all/own/none/inherit on an in-memory tree only.
  Invalid, missing or duplicate parameters fail before the snapshot; a missing OU is rejected.
  The response marks a preview and counts accounts whose access changes, excluding source-only
  changes. Tests compare the hypothetical overview against a real saved scope, prove the preview
  left persistent policy unchanged, cover Default inheritance and admin-only routing, and check
  active-account snapshot loading on Postgres. No new schema or production write is involved.
- Independent review caught expired accounts being included. The snapshot now uses the panel-tz
  civil date with the same inclusive cutoff rule as login/session validation. The regression
  first counted the expired account, then passed for expired/today/future/unlimited controls in
  Pacific/Kiritimati. Follow-up review confirmed the issue closed. Failed store reads return an
  error rather than a plausible empty summary. Bridge integration, weekly adoption counts and
  the complete P26 access matrix remain pending.
- Preview checkpoint validation: full Go regression with Postgres enabled, three targeted race
  repetitions, vet, staticcheck and diff checks passed after the expiry fix. The extra closed-store
  regression passed separately after the full run began. Existing required CI checks on the
  preceding OU-settings commit are green; this preview commit still requires its own CI run.
- Weekly adoption implementation: successful non-admin reader-groups responses add a per-UTC-ISO-week
  HMAC to an in-memory set under a fresh random salt. The hourly loop writes only a bare integer
  `forecast_viewers_<week>` equal to this process's initial persisted baseline plus its set size.
  Repeated flushes are idempotent; restart draws a fresh salt and intentionally yields an upper
  bound. Failed observations never affect the response and log only a constant diagnostic.
- Week rollover clears the salt and set, retaining only an integer if the old week's flush needs
  retry. A monotonic week watermark ignores late older-week requests. Tests cover duplicate and
  concurrent observations, successful-reader-only counting, ISO-year rollover, failed flush retry,
  restart baselines, corrupt/overflow counts and SQLite/Postgres storage. A backup dump contains
  no test viewer username, salt or digest; tracking meta rows contain only the expected key and
  integer. No schema changes or production writes occurred.
- Weekly implementation validation: full Go suite with Postgres enabled, three targeted race
  repetitions, vet and staticcheck passed; additional invalid-baseline and privacy/log checks
  passed separately. Independent review was retried after an availability failure and remains
  pending until a result is received. This checkpoint is not eligible for merge without review.
- Independent weekly review completed with no actionable findings. Its scheduler validation gap
  is covered by a shared due-decision helper and deterministic before/exact/after-hour and ISO-week
  boundary tests. The live minute ticker itself is not advanced through a wall-clock boundary.
  Hourly persistence necessarily permits losing the latest unflushed observations on abrupt
  termination; the adoption indicator is not an exact analytics ledger.
- Access-table reconciliation: internal non-admin prediction readers now receive accepted sets
  without error diagnostics, while retaining payload and orphaned history. Internal `own` readers
  also omit membership pointers because those refer to the all-population record. Admin and
  persistent-machine diagnostic contracts remain unchanged. Both SQL filters precede LIMIT.
- Regression tests first exposed rejected rows, errors and own membership, then passed after the
  view-policy correction. Positive controls retain internal payload/orphans and compare both SPA
  reads against their real cookie-authenticated v1 counterparts. Independent review found no
  actionable issue. Full Go regression with Postgres enabled, three targeted race repetitions,
  vet and staticcheck passed; the final orphan/v1 extensions passed in the race run separately.
- Cross-surface matrix checkpoint: `TestForecastAccessMatrix` exercises the real mux on SQLite
  and Postgres, with both Default-none and Default-own trees, one account per specified OU,
  unassigned/restricted/admin/mandated subjects, anonymous requests, persistent query tokens and
  bridge-token denial on session routes. Two resolved months include frozen own/subtree
  attribution, a genuinely purged member, rejected diagnostics, and readable/unreadable reports.
- Explicit fixture expectations pin exact members and own sample sizes, both mature all-months
  and their numeric spread, required payload/membership/count fields and recursive forbidden
  paths. The pre-existing authentication wrappers take precedence: a dangling tree returns 503
  `enrolment_policy_unavailable` before forecast resolution, and missing admin permission returns
  plain 403 before checking enrolment. These stricter refusals are tested without changing the
  independent authentication policy. Pure resolver tests separately pin dangling-tree `none`.
- Independent review found that own groups could disappear without failing the original loop.
  Both fixture months are now required exactly once. A temporary empty-own-groups mutation fails
  this check; temporarily removing internal reader/no-membership policy also fails on diagnostic
  and membership leaks. Both mutations were restored. Follow-up review found no remaining issue.
- Matrix checkpoint validation: SQLite/Postgres matrix passed, three race repetitions passed,
  and the full Go suite with Postgres enabled, vet, staticcheck and diff checks passed. Shared
  Postgres runs were serialized; an initial overlapping invocation was cancelled and discarded.
  Bound bridge prediction subjects remain to be added with the shared token-identity integration;
  this matrix checkpoint does not claim that pending P26 contract or P27/P18 UI contracts complete.
- Bridge integration checkpoint: the shared app-token registry now carries its minting user's
  username, session revision and account creation identity. `mintForApp` and its API call match
  the interfaces already present in both parallel Apps PRs; its body is byte-identical in both.
  No notification or extension capability was added, no second identity registry was introduced,
  and neither parallel branch was changed. This is an in-memory binding, not a schema change.
- Prediction reads preserve cookie precedence and the persistent query token's diagnostic shape.
  Bound bridge tokens recheck current scope and account identity/active/expiry/enrolment state.
  Identity freshness and scope use the same strict account/tree snapshot. Unbound bridges deny;
  all allowed bridges, including admin-minted ones, return accepted report-backed sets only,
  without payload, errors or membership. Newly external readers use current report visibility.
- Bridge regressions first reproduced none-scope HTTP 200 and diagnostics/orphan/stale-account
  disclosure, then passed. The matrix now includes tokens actually minted by the Internal and
  InternalNone members, with no skipped v1 probes. Extra controls cover none-to-all updates,
  restriction filtering before LIMIT, denied-cookie precedence, exact expiry boundaries,
  non-query/unbound/nil registries and closed-store denial. Independent slice/follow-up review
  found no actionable issue. Full Go/Postgres regression, build, vet, staticcheck and three
  targeted bridge/matrix/snapshot race repetitions passed.
- Final requirements reconciliation corrected the older-server navigation contract: an omitted
  `me.forecast` remains undefined and preserves reading-page prediction requests; explicit `none`
  still prevents both requests and immediately hides existing forecast data. Legacy identity is
  distinct from known policy identities. Auth and reading-page regressions failed before the
  correction and passed afterward. This supersedes the earlier deny-by-default hint fallback.
- Final independent P26 audit found no actionable finding or missing implementation requirement.
  Local validation passed: full Go suite with Postgres enabled, three targeted race repetitions,
  build/vet/staticcheck, web typecheck, all 120 Vitest files / 1018 tests, production web build,
  and a serialized Go embed rebuild after that build. Latest pushed-head CI remains required.
- Integration hold: read-only `merge-tree` against Apps PR 141 reports `apps_token.go`; against
  PR 142 it reports `apps_api.go` and `apps_token.go`. Shared mint code is identical, but textual
  overlap is not a clean integration. Neither branch nor the user's checkout was modified.
  Coordination authorization was requested before messaging the other development conversation;
  do not merge this draft until the shared edits have a verified integration order and clean
  combined validation. No schema, release, deployment, production data or Dify publish occurred.
