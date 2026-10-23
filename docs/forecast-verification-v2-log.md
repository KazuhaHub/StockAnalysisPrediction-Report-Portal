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
  P30 #135, P31 #136, P11 #137. R2a: P15 #143, P22 #146 (`669810fd`). The standalone Go
  1.26.10 security fix #144, upload-test timer fix #147, vendor-evidence fix #148, halt evidence
  #149 and P14 isolated seeded preview #150 (`74e8c564`) are merged. Owner-authorized dependency
  PR #138 is also merged at `8ab51334` after current-base CI and independent review passed.
  P23 #152 is merged at `08b1c1a0` after full exact-head validation. P24 #154 is merged at
  `c4f0c93c` after independent review and full exact-head CI/CodeQL.
- **In flight.** P26 #145 on `feat/forecast-record-access` has completed local validation and
  independent review; main was integrated at `af036007`, whose full CI/CodeQL and combined
  local Go/Postgres, 1018 web tests and repeated race checks passed. It remains a draft because read-only merge
  previews report shared-token text overlap with Apps PRs 141/142. Coordination authorization
  is pending; neither Apps branch nor the user's checkout was changed. The local P16/P26/P37
  integration and reviewed P17/P18 server work are preserved on `feat/forecast-reader-server`
  at `8734b287`; own-scoped purge-history evidence still needs schema approval. P37 #151 remains
  awaiting integration/revalidation of the now-merged DOMPurify #138 dependency fix.
  P23's source/runtime/admin/readiness work and ADR 0041 passed final branch-wide review, full
  exact-head CI and local browser verification before merging #152. P24's capture, integrity-checked
  offline replay, fixed-panel CLI and no-database dispatch guards are merged. Current work is P25
  `feat/forecast-sina-shadow`, started from `c4f0c93c`; the bounded Sina history foundation is
  implemented and reviewed, along with audited fallback controls and a context-local HTTP budget.
  Persisted diagnostic storage is implemented and reviewed; runtime selection, source-call tracking
  and shadow admission/execution remain outstanding.
- **Next step.** Complete P25's controls, persisted diagnostics and bounded shadow
  comparisons without admitting fallback writes. Revalidate P37 with the dependency fix. Resolve the
  purge-evidence decision and P26 integration order before P27/P28.
  P13 still requires authorized production baseline screenshots and owner
  review before P19/P20. P39b's Dify publication and undeclared-input gates are verified below;
  the R1b deployment and subsequent Portal target refresh remain outstanding. No production
  action has been performed here.
- **Known limitations.** Beijing 30%/listing-day and rights-issue captures remain missing. The
  follow-ups cover real live suspensions, delisted-response availability, 301/689/low-price
  controls and a P8 bonus/unchanged-ratio pair; they do not claim settlement from unavailable delisted history. The P15
  forecast translations are machine-generated, pending owner-arranged human review. Seeded
  responsive parity and the reading UI still require owner review. Re-check ADR numbers before P23: Apps reserves
  0038/0040 and P22 uses 0039.
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
- Rollback: revert this PR; no data or schema changes. P11 and #144 are merged. P15 awaits
  required CI on its final rebased head; independent review and full local checks passed.

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

### P22 — reader access decision record (2026-10-22)

- Prepared in the independent docs lane allowed by implementation section 1.3 while P26's Apps
  integration sequence awaits coordination. ADR 0039 is accepted design, explicitly not a claim
  that P26/P27/P28 or R2b reading-stage changes have deployed. Apps PR 141 reserves 0038 and
  PR 142 uses 0040; neither proposes 0039. Their branches and code remain untouched.
- Records three-way nearest-OU inheritance, actual Default/subtree distinctions, frozen
  attribution and its transactional OU-delete exception, population-local membership/cache
  identity, external volume protection, credential shapes, bounded computation, count-only
  adoption, reading stages, edition/display rules, disclaimer and rollout/rollback gates.
- Supersedes design D6, rewrites acceptance condition 19 with negative and positive boundaries,
  amends design access and ADR 0029 section 5/access decision row. Removes their stale token-only
  assertion: v1 cookie reads remain behind the independent enrolment wall and forecast scope.
  Report readability is never widened by aggregate permission; settled per-report checkpoints
  are distinguished from non-mature aggregate cells and drawer outcomes.
- Independent adversarial review found no actionable issue or missing P22 contract. All seven
  relative ADR links/anchors and diff whitespace checks passed. No test or runtime code is added
  by this docs-only PR; existing guard implementations and golden fixtures are unchanged. Full
  Go regression with Postgres enabled, vet, staticcheck, web typecheck, all 120 Vitest files /
  1006 tests, production web build and a serialized embed rebuild passed. CI on the exact pushed
  head remains required before merge.
- CI integration: the first P22 run passed all assertions but failed on the upload timer's
  unhandled teardown callback. Standalone fix #147 passed its exact-head CI and merged as
  `40a18767`; this branch now integrates that main commit. Both append-only log sections are
  retained. The P22 diff against main is still docs-only and its combined head needs fresh CI.

### CI unblock — upload test timer lifetime (2026-10-22)

- P22's docs-only CI passed all 120 files / 1006 assertions but failed on an unhandled callback
  from `RunAnalysisModal.test.tsx`: `window is not defined` in `useDelayState`. Installed Ant
  Design UploadList schedules its progress update after 300ms without unmount cancellation;
  the existing shared teardown only drains the Form's short debounce through a 20ms wait.
- Kept the fix out of the ADR PR. The upload integration test now owns its fake clock, advances
  user interactions, explicitly unmounts and executes pending timers inside act while jsdom still
  exists, asserts zero pending timers, and restores real time in finally. Production code and
  global test behavior are unchanged; callback exceptions and runaway chains still fail.
- The regression first found two timers after unmount without the drain; adding the drain passed
  all 12 modal tests, ten repeated upload checks, the full 120-file / 1006-test suite, typecheck,
  and full Go/Postgres regression plus vet/staticcheck. Independent review found no actionable
  issue. Exact-head CI remains mandatory; this is a targeted repair, not a blind retry of P22.

### P7/P8 follow-up — captured adjustment responses (2026-10-22)

- P22 merged as #146 (`669810fd`) after independent review and green combined-head full CI and
  CodeQL. This follow-up starts from that main, not from the held P26 or an Apps branch.
- V20 read-only captures produced seven complete Tencent responses, documented with request
  parameters, SHA-256, an independent issuer notice and explicit evidence limits in
  `internal/app/testdata/quote/forecast-vendor-evidence.md`. No forecast golden output changes.
- The real qfq bodies expose `qfqday`, whereas the P8 fetch called the day-only parser. The new
  actual-fetch regressions first failed with 11/0 and 37/0 raw/adjusted rows. This would keep due
  marks at source failure because the required adjusted closes are absent, rather than silently
  resolving unchecked prices. The fix selects qfqday only inside the detection-only fetch and
  retains the shared field-count, price, candle, snapshot and identity drift gates.
- Missing/null/empty or malformed adjusted rows fail; they never fall back to raw `day` even
  when raw rows are present. Tests for null/missing/empty first failed against a naive fallback.
  Ordinary readers retain unadjusted rows when both keys exist and never consume qfq-only bars.
- The real transfer's -7.46% raw close move is deliberately not relabelled as the plan's
  theoretical -9.09%. It still exercises the intended under-limit miss: no cash ExDates signal,
  no price-limit signal, a ratio-only corporate-action flag, and unchanged raw close/return.
  Captured ordinary limit-down and 301/689/fen-rounded low-price controls are not excluded.
- Beijing historical probes returned no daily rows; real halt/delist/Beijing evidence remains a
  gap, not synthetic data passed off as capture. No production writes, deployment, evaluator
  enablement, Dify publication, schema change or Apps edits are part of this follow-up.
- Independent adversarial review found no actionable issue. Full Go regression, separate full
  Postgres regression, vet, staticcheck, three repeated captured-fixture race runs, web typecheck,
  all 120 Vitest files / 1006 tests, production web build and a subsequent root-level Go embed
  build passed. All seven captures retain every response byte plus one terminal LF and match
  their documented SHA-256. Exact-head full CI and CodeQL remain required before merge.

### P7 follow-up — live suspension and delisted-response evidence (2026-10-22)

- Starts from main `fa250ced`, independently of P26 and both Apps branches. P26's normal main
  integration at `840e9a9c` passed full exact-head CI/CodeQL and local combined validation, but
  remains a draft for shared-token integration ordering; no coordination message is authorized.
- Six full read-only Tencent captures and independent issuer/exchange references are recorded
  in `internal/app/testdata/quote/forecast-halt-evidence.md`, with parameters and SHA-256.
  Existing forecast golden outputs and schema are unchanged.
- A short live halt has fewer than three later sessions: current-stamp evidence resolves it even
  with the lag valve set; removing evidence refuses stale/lag instead. A longer halt uses the
  real classifier, transactional ledger writer, evaluator and qfq fetch replay to settle d5
  during a halt that began on S0. Stock return is 0; benchmark return is -365 bp from S0, not
  -363 bp from the earlier traded base_date. Source and flags are asserted on the stored row.
- A delisted instrument returns empty recent history plus a current-looking snapshot, and real
  2019 candles in a separate historical window. The pure baseline resolver rejects both arrays
  for a 2026 receipt. A real evaluator replay of the empty recent response does not promote its
  snapshot to a baseline: s0_fen remains NULL, no mark resolves, delivery failure spends no
  attempts, and no qfq call occurs. This is source-availability evidence,
  not a claim that unavailable delisted history can settle or that the parser detects delisting.
- Tests first failed for absent capture files. Three temporary production mutations proved
  guard effectiveness: wrong benchmark anchor, ignored stamp and snapshot-to-candle fabrication
  each failed its captured test. All mutations were reverted. Corrected stale developer comments
  about non-cash event text and synthetic ratio examples; no runtime semantics are changed.
- Independent review found one documentation overclaim (pure historical-array checks versus
  recent-response evaluator replay); the log and provenance now distinguish those layers, and
  follow-up review confirmed closure with no further findings. Full Go/Postgres regression,
  vet/staticcheck, three repeated captured/long-look-back race runs, web typecheck, all 120
  Vitest files / 1006 tests, production web build and serialized embed rebuild passed. Final
  captured/arithmetic checks passed after the comment/error-text corrections. Six bodies match
  their original response bytes plus terminal LF and documented SHA-256. Fresh exact-head CI
  remains required before merge; no runtime, schema or production change is included.
### P14 — isolated seeded preview and read clock (2026-10-22)

- Added a test-only writer that reserves a new SQLite database and sibling infrastructure-only
  config, listens on loopback port 8791, generates private random preview credentials, and refuses
  existing DB/config/WAL/SHM paths, including existing and dangling symlinks. Config writes use the
  reserved descriptor; failure cleanup checks file identity before removing its own reservations.
- Historical receipts enter through a private per-server test seam and the real v1 ingest. The
  real evaluator settles deterministic synthetic market bars through its existing fetch seams.
  No schema or golden fixture changed. The stored evaluator remains off; no ingest token remains
  in the generated database. Normal server bootstrap can create its own default API token.
- Seeded open/pending/small/mature states, corrected positive/negative signals, neutral and one-arm
  output, two mature policy slices, fills, frozen benchmark changes, coverage exclusions, halted
  and corporate-action marks, final refusal, body-change and edition-isolation cases. Included
  small own/sub-OU populations and restricted external-all accounts for the later access UI.
- `RP_FORECAST_DEMO_NOW` affects only statistics and read phases in dev builds. Empty/invalid
  values fail startup; any set value fails a non-dev build before config generation or DB writes.
  Receipt, evaluator, authentication, retention and token clocks remain independent.
- Initial failures exposed a duplicate entry claim and the exact backtest-slack boundary; the
  writer now asserts every ingest classification. Independent review found missing policy
  slices, which were added and asserted. Browser inspection found a pending-baseline fault
  blocking the positive cohort; a failing cohort-specific test pins its isolation in June.
  Report timestamps now agree with ledger receipts through body re-ingest. Final review found
  no further actionable issue.
- Validation: two independent seeds produce byte-identical groups JSON; matrix/drawer states,
  scenario inventory, edition isolation, attribution and private permissions pass. The linked
  release executable refuses demo clocks before creating any output. Full Go/SQLite/Postgres
  regression, build, vet, staticcheck, three targeted race passes, web typecheck, 120 test files /
  1006 tests, and production SPA build passed. Local server smoke checks returned 200 for
  health/login/groups/SPA and a mature January cell with q < 0.001. The real browser rendered the
  seeded matrix. This is not the later responsive parity sign-off or production baseline review.
- Added the preview runbook and corrected the plan command to print the generated credentials
  with `go test -v`. The runbook explicitly separates synthetic evaluator data from captured
  vendor evidence and live chart quotes; later reader/layout PRs still own their UI states.
- Rollback: revert this PR and stop the local preview. No production deployment, workflow
  publication, evaluator enablement, Apps-branch edit or database migration was performed.

### P23 capability-boundary checkpoint (2026-10-22, in progress)

- Started `feat/quote-ledger-capability` from main `74e8c564`, independently of the held access
  integration. P17/P18 implementation, validation and review remain preserved on
  `feat/forecast-reader-server` at `8734b287`; this branch does not contain that stack.
- Read-only inspection of Apps PRs 141/142 found no overlap with `quote.go`, `quote_cache.go` or
  the new capability tests. No Apps branch, original checkout or production state was changed.
- Added internal-only `settled` and `settled_shadow` capability identifiers. They remain outside
  the public display interval inventory and range map. An absent declaration still grants the
  existing display wildcard but cannot grant ledger access. Explicit grants preserve market and
  code boundaries, distinguish writer from shadow, and reject unresolved markets.
- Display cache entry and source-dispatch paths refuse ledger intervals before any cache lookup,
  flight creation or upstream call. Red tests demonstrated both undeclared authorization and a
  warm-cache hit before the repair; the initial focused controls passed after it. Added controls
  for all three display fetcher shapes and unchanged cache occupancy on refusal.
- This is a P23 foundation, not P23 completion. Source flags/grants, operator-order ledger walk,
  isolated health/breaker/single-slot handling, refusal precedence, evaluator wiring, readiness
  item 6, order auditing/confirmation and panel fields remain outstanding. ADR 0038/0040 belong
  to Apps and 0039 to reader access; the source-policy ADR must use the next free number after
  rechecking reservations.
- No source is newly admitted to settlement, no schema is changed and no production action is
  authorized by these tests. The own purge-evidence schema proposal and prior integration holds
  remain undecided.
- Code/test head `8e7ca037` passed the full Go regression with local Postgres enabled (app
  package 37.119 s), three-repeat targeted race checks (1.737 s), vet, staticcheck and build.
  Independent read-only review found no actionable issues within the foundation's scope;
  it did not review the intentionally absent walker, evaluator, readiness or panel integration.
- The production SPA build also passed (763 ms). Its original process handle was no longer
  available at the next observation, so the subsequent Go embed build was rerun explicitly and
  passed rather than inferring success from the missing handle. No frontend source changed.

### P23 operator-order source selection checkpoint (2026-10-22, in progress)

- Added a pure ledger-chain resolver over the already-loaded operator order and explicit settled
  grants. It stable-partitions cash-dividend sources first, keeps priority within each class,
  skips disabled/unknown sources, deduplicates names and preserves market/code predicates. An
  empty input order remains empty rather than silently restoring shipped display defaults.
- Added separate evidence flags to the source type and primary selection requiring both cash
  dividends and reported instrument kind. Either flag alone is insufficient. Shadow-only grants
  cannot enter the writer chain; an absent primary remains an explicit false result for the
  future walker to turn into `no_source` rather than falling back to a display vendor.
- Tests were written before the resolver and failed with the empty implementation, then passed
  with operator-order, stable-partition, primary-removal, duplicate/unknown-name, positive market
  and negative market/code controls. Focused checks passed (0.413 s). No actual vendor grant or
  evidence flag is enabled yet; this checkpoint
  changes no evaluator call site, source order setting, database or production behavior.
- Code/test head `1432d42e` passed full Go regression with local Postgres enabled (app package
  34.320 s), three-repeat targeted race checks (1.900 s), vet, staticcheck and build. Independent
  read-only review found no actionable issues in operator ordering, partition stability, explicit
  grants, primary requirements or display compatibility. The review used SQLite only and did not
  cover the still-absent vendor admission, source walk, evaluator, readiness or panel behavior.

### P23 uncached source-walk checkpoint (2026-10-22, in progress)

- Added a ledger-only bounded range walk. It validates the window through `quoteParseWindow`,
  rejects unbounded dates, resolves the instrument, requires a primary and holds one shared
  upstream slot across the whole chain. It never enters display cache or single-flight. A
  missing range fetcher or nil success response is an error, never an accepted price series.
- Tier 1 calls only explicitly granted sources with both evidence flags. A source without
  corporate-action or instrument-kind evidence cannot become a writer just because a primary
  failed. Actual shipped grants remain disabled pending the full wiring/readiness change.
- Each evaluator pass will own a fresh `ledgerTick`: three transport/parser failures stop further
  calls to that source in that pass. Typed ledger refusals do not trip the breaker or change vendor
  health. Caller cancellation stops the walk without recording vendor failure. Separate ledger
  health snapshots preserve the display counters, and the first called source's refusal wins
  when every called source fails. A new pass reopens its own breaker.
- Tests first failed against an empty walk, then passed for invalid windows, absent primary,
  primary failure plus evidence-free fallback, nil responses, refusal priority, repeated uncached
  success, health isolation, typed-refusal neutrality and cancellation. A saturated-slot fixture
  leaves exactly one upstream slot available and exercises two sources under a two-second context
  deadline, asserting the slot remains held during both calls and is released afterward.
- Initial focused tests passed (0.556 s); full Go/Postgres, repeated race/static checks and
  independent review are pending. No evaluator call site, admin API, actual vendor grant, source
  order, schema, Apps file or production state changed. This remains incomplete P23, not a new
  settlement admission or authorization to enable the evaluator.
- The initial full Go/Postgres run passed (app package 34.861 s), as did three-repeat targeted
  race checks (1.625 s), vet, staticcheck and build. Independent review then found that an exhausted
  breaker incorrectly returned `no_source` for an otherwise configured primary. A new exact-error
  control failed on the fourth walk before preserving that source's real failure in pass state.
- Added a context-aware pass gate so concurrent walks cannot overrun the three-failure ceiling;
  each still performs its own uncached fetch rather than joining a shared result. Twenty callers
  sharing one pass now observe the same actual transport failure with exactly three vendor calls.
  Added saturated-upstream cancellation and wrapped typed-refusal controls. Focused walk tests
  passed (1.210 s); post-repair full checks and review follow-up remain pending.
- Post-repair application/test head `c3bc3865` passed full Go regression with local Postgres
  enabled (app package 50.775 s), three-repeat targeted race checks (2.259 s), vet, staticcheck
  and build. Independent follow-up closed the breaker finding with no additional actionable
  issue, confirming pass/upstream release paths, failure priority and wrapped refusal handling.
  Review also ran three race-enabled walker repetitions with Postgres disabled.
- Added a test-only follow-up for a caller cancelled while queued behind the pass gate, then
  reusing the same tick successfully after release. This pins both cancellation and subsequent
  progress without a live vendor or database. Three race-enabled walker repetitions including
  this control passed (1.829 s). No runtime code changed after `c3bc3865`.

### P23 evaluator source-path checkpoint (2026-10-22, in progress)

- Granted Tencent explicit Tier 1 settled access for Shanghai/Shenzhen series, with its measured
  cash-dividend and instrument-kind evidence flags. Beijing lacks usable history and overseas
  display capability does not imply ledger admission. Sina/Yahoo still have no writer grant or
  evidence flags. No Tier 2 fallback was admitted.
- Runtime evaluator range fetches now use the configured uncached ledger walk instead of a
  hard-coded Tencent call. Each tick starts fresh breaker state and releases it on exit. Existing
  isolated arithmetic-test fetch seams retain their previous behavior; new integration controls
  leave that seam nil and exercise the actual source resolver. Settled-bar completion filtering,
  frozen baselines, write doors and benchmark checks are unchanged.
- Typed source refusals survive benchmark, stock and bounded look-back planning. In particular,
  a missing configured primary records `no_source`, not a generic fetch failure, and never adds
  a mark-specific attempt. Unknown parser/transport failures remain `fetch_failed`.
- Red controls showed the old path still attempted a direct Tencent request after the operator
  removed it; a test transport intercepted the request without reaching the network. After the
  change, no-source planning and zero attempts pass on both SQLite and Postgres. Positive runtime
  controls prove repeated calls do not cache, unfinished bars remain excluded, dry mode never
  resolves marks and mode 1 without a benchmark check still plans without writing. Initial SQLite
  controls passed (0.705 s); the Postgres no-source control passed (0.661 s).
- Readiness integration is deliberately not included in this checkpoint: Apps notification PR
  141 also edits `forecast_ops.go`. That shared-file integration, readiness item 6, alerts, panel,
  order confirmation and the source ADR remain outstanding. No Apps branch was edited, no schema
  changed and no production deploy or evaluator enablement occurred. Full checks and independent
  review of the runtime source-path change remain pending; P23 is not release-ready.
- Runtime application/test head `1f4a79b9` passed full Go/Postgres regression (app package
  34.980 s), three-repeat targeted race checks (4.712 s), vet, staticcheck and build.
  Independent read-only review found no actionable implementation issues within this scope.
  It did not review deferred readiness, alerts or admin integration and used SQLite only.
- Added test-only evaluator-level controls after review: five sets share exactly three stock
  delivery failures in one tick, the next tick resets the breaker, and typed stock/widened-lookback
  refusals preserve `no_source` with zero attempts. Initial controls passed (0.577 s); final full
  regression and repeat checks including these controls remain pending. Runtime code is unchanged
  from the reviewed head.
- Final application/test head `13251660` passed full Go/Postgres regression (app package
  34.262 s), three-repeat targeted race checks including the added evaluator controls (6.796 s),
  vet, staticcheck and build. Test-only follow-up review confirmed the shared/reset breaker
  fixture and actual widened-lookback path with no findings. No golden fixture or frontend source
  changed. This closes the runtime-source checkpoint, not P23's remaining integration/CI gates.

### P23 administrator source diagnostics checkpoint (2026-10-22, in progress)

- Extended the existing admin quote response with per-source explicit settled/shadow market arrays
  and cash-dividend/instrument-kind evidence flags. Added configured ledger-chain/primary/shadow
  summaries and separate ledger-health records. Missing grants and unused health are arrays,
  absent primary/shadow are empty strings, and unset health timestamps remain empty strings.
- Configuration summaries describe enabled declarations across their markets; runtime still
  checks the actual instrument and symbol predicate. They are not a substitute for readiness.
  The runbook documents this distinction and the unchanged deployment/enablement boundary.
- Red tests first showed all new diagnostics absent. Focused tests then passed (0.526 s), proving
  shipped grants, disabled-primary omission, operator order, explicit shadow selection, display
  wildcard exclusion, distinct display/ledger failure counters, health preservation on cache clear
  and denial to a non-admin reader through the real session/admin wrappers.
- This changes only quote-source backend files outside Apps PRs 141/142's listed file sets.
  Frontend panel rendering, source-order audit/confirmation, readiness/alerts and the source ADR
  remain outstanding. No schema, production, Apps branch or separately owned PR changed. Full
  Go/Postgres, repeated race/static checks and independent review are pending at this checkpoint.
- Application/test head `746c31bd` passed full Go/Postgres regression (app package 33.041 s),
  three-repeat targeted race checks (3.537 s), vet, staticcheck and build. Independent read-only
  review found no actionable issues in array/empty shapes, declarations, admin authorization,
  wildcard exclusion or health separation/cache-clear behavior. Review used SQLite only.
- Added a test-only direct-helper control for duplicate/unknown configured source names and a
  primary declaring both writer/shadow grants: the primary is not its own peer. Runtime source
  is unchanged after the reviewed head. Three race-enabled admin-test repetitions including this
  control passed (3.414 s). Frontend, readiness, alert and audit work remain unreviewed
  and outstanding; no production, schema or Apps action was performed.

### P23 source-order audit checkpoint (2026-10-22, in progress)

- Added the dedicated `quote.order` action and three locale labels (zh-TW generated through the
  normal OpenCC script). It records effective source arrays, not raw spelling, and preserves the
  actor/OU/address. Equivalent or repeated order saves do not add a transition. The existing
  general quote-policy audit is unchanged and remains a separate record.
- Source order now governs durable settlement, so its dedicated audit and setting update share
  one transaction. Audit failure rolls the order back and refuses the request before other
  submitted fields are written. The transaction acquires SQLite's writer lock before its read
  and locks the Postgres settings row; all work uses the transaction handle. No schema changed.
- Red controls showed the previous handler wrote no dedicated transition and reported success
  after an audit failure. Focused audit/vocabulary/locale/legacy-policy tests passed after the
  change (0.727 s), including both drivers. Ten concurrent order changes form a serial audit
  chain ending at the persisted configuration under a two-second context deadline.
- Read-only merge previews at pre-change head `24003238` were clean against Apps PR 141 head
  `b4741355` and PR 142 head `c4402084`; inspected audit hunks are disjoint. Post-change previews,
  full checks and independent review remain pending. No Apps branch or separately owned PR was
  edited. UI confirmation, readiness/alerts, panel presentation and the source ADR remain open.
- Initial application/test head `9d622e4c` passed full Go/Postgres regression (app package
  43.964 s), three-repeat targeted race checks (6.655 s), vet/staticcheck, frontend typecheck,
  all 120 Vitest files / 1006 tests (44.54 s), and the production SPA build (902 ms).
  The standalone source branch has fewer web tests than the separate P17/P18 integration stack.
  Independent review found no actionable issue in this initial scope.
- Post-change read-only merge previews were also clean against the same Apps heads (trees
  `4ab55e26` and `d828a5e3`). This proves textual compatibility only, not combined runtime
  verification, and neither branch was changed or merged.
- Strengthened validation exposed two additional boundaries: a permitted NULL meta value failed
  to match the existing reader's shipped-default semantics, and independent SQLite connections
  could transiently contend. Added failing controls, coalesced the nullable read, and bounded
  contention retries around the whole rolled-back transaction with context-aware delays. The
  two-connection fixture and both-driver concurrency fixture also assert the actor OU snapshot.
  Final post-repair checks and follow-up review remain pending.
- Final application/test head `d6763b24` passed full Go/Postgres regression (app package
  33.568 s), three-repeat SQLite race checks including cross-connection cancellation (6.262 s),
  vet, staticcheck and the serialized Go embed build. Frontend files are unchanged from the
  validated label/build checkpoint. Independent follow-up found no actionable repair issue and
  additionally ran two race-enabled SQLite repetitions. The final read-only Apps merge previews
  were clean (`0b3240ad`, `b8e8a465`); semantic integration and exact-head CI remain separate.
- This closes the source-order auditing checkpoint only. No source was deployed, no schema or
  Apps branch changed, and no separately owned PR was edited or merged. Source-order confirmation,
  frontend diagnostics, readiness/alerts and the source ADR still need implementation/integration.

### P23 quote-source management UI checkpoint (2026-10-22, in progress)

- Added optional settlement grants/evidence and a separate configuration/health card. Missing
  server fields retain the old display; unavailable health is not rendered as an empty or healthy
  history. The card states that configured declarations are not runtime readiness approval.
- Saving an order that removes the configured settlement primary requires confirmation. Cancel
  and unmount before confirmation write nothing. A pending-save guard suppresses duplicate
  requests and resets after rejection so the operator can retry. Loading feedback starts only
  when the request is sent, not while waiting for the confirmation.
- Initial failing tests pinned missing confirmation and no-primary diagnostics. Page tests use
  disabled theme motion so jsdom does not retain a closing modal over the next interaction.
  Component tests cover unavailable/empty and source-specific telemetry, plus axe markup checks
  in both themes (not visual contrast/layout, which jsdom cannot measure).
- Typecheck, all 121 Vitest files / 1014 tests (40.98 s), production SPA build (1.13 s), and the
  subsequent serialized Go embed build passed. One additional test-only duplicate-click/failure
  retry control then passed with both focused files: 27 tests (4.94 s). Independent read-only
  review found no actionable issue in confirmation, cleanup, compatibility or diagnostics.
- This is local work only: no deploy, schema edit, Apps branch mutation or separately owned PR
  change. Readiness item 6, watchdog integration and the source ADR remain outstanding. The
  own-scoped purge-history evidence proposal still requires owner approval before any DDL.

### P23 readiness and no-source watchdog checkpoint (2026-10-22, in progress)

- Appended readiness item 6, `ledger_chain`, preserving the first five keys. It uses the same
  explicit configured grants as the source panel and additionally requires both evidence flags
  on a primary. Display wildcards and display-only sources cannot satisfy it. Runtime still
  checks eligibility for each actual instrument, independently of these configuration gates.
- Switching from dry to mode 1 now fails with `forecast_not_ready` naming `ledger_chain` when
  that primary is absent, and succeeds after restoration when the other gates hold. Removing
  a primary while already running does not rewrite the mode: settlement refuses via the ledger
  walker, the evaluator card warns and does not claim health, and the watchdog sends `no_source`
  through the existing webhook/email/Slack path at most daily. Its check precedes the missing-
  heartbeat early return. Off mode still performs only the mode read and writes nothing.
- Initial red controls exposed all three missing boundaries: the sixth item, the mode gate and
  the no-source emission. Tests cover display-only orders, missing grants/evidence, restoration,
  daily repeat/suppression and both delivery channels. The frontend tests reproduce removal in
  both dry and running modes. Independent read-only review found no actionable issue and
  suggested an explicit absent-heartbeat control; added it together with an off single-read
  assertion and reran targeted race checks three times (8.559 s).
- Full Go/Postgres tests passed (app package 49.775 s), as did vet/staticcheck, typecheck, all
  121 Vitest files / 1017 tests (50.54 s), the production SPA build (761 ms), and the subsequent
  serialized Go embed build. The absent-heartbeat addition after the full run is test-only.
- No database shape, production setting, Apps branch or separately owned PR changed. This
  checkpoint does not complete P23: the source ADR and exact-head CI/integration remain open.

### P23 source ADR and integration preparation (2026-10-22)

- Added ADR 0041 for the approved V9 Tier 1 policy. Live PR-head checks confirmed Apps 141/142
  still reserve 0038/0040; the source decision was renumbered rather than changing their files.
  Updated ADR 0028's durability promise, ADR 0029's direct-call/D9 text and ADR 0032's stored-price
  boundary, with current implementation-plan pointers and a historical-plan identifier note.
- The ADR distinguishes integrity from corroboration, compiled grants from operator ordering,
  and local P23 control/observability from pending P24/P25 measurements. It records candidate
  evidence gaps and the later separate evidence chain/Tier 2 decision without authorizing any
  schema change or fallback writer. Document review found one wording issue conflating compiled
  grants with operator authority; corrected it. Relative links resolve and diff checks pass.
- Expanded the PR race selector with `Ledger|QuoteOrder|Readiness`; these new concurrent paths
  otherwise did not all match the existing forecast selector. The added selection passed locally
  under race (9.437 s). No application source changed after the prior full local validation.
- Read-only merge previews at runtime head `5ff9465b` were clean against the verified Apps heads
  `b4741355` and `c4402084` (trees `92bf3b6d` and `8bfe71d4`). This establishes textual compatibility
  only, not combined behavior or permission to merge those branches. Final docs/workflow-head
  previews, branch-wide review and exact-head CI remain required before merging P23.
- Final branch-wide independent review found no confirmed actionable issue or missing P23 §8.8
  requirement. The prior breaker/terminology findings are closed. Combined targeted race checks
  passed with Postgres disabled in that independent review. At docs/workflow head `b2cf51c6`, the
  final read-only Apps previews were also clean (trees `5189479e` and `65177496`); no Apps branch
  was changed or merged. P23 is submitted as draft PR #152 for exact-head CI. Browser layout/
  contrast validation remains separate, and no production rollout is authorized by the review.

### P23 browser verification and dependency unblock (2026-10-22)

- Verified the actual embedded UI against a new isolated synthetic preview database. Light/dark
  settlement diagnostics and the narrow confirmation dialog are readable; cancelling removal and
  reloading retains Tencent. Read-only database inspection found no `quote.order` audit and
  `forecast_auto=0`. Screenshots are local evidence, not production baselines or vendor captures.
  The preview listener and temporary browser tab were closed; no production data was copied.
- P23 head `067b9028` passed Go/Postgres, race, frontend, workflow lint and all CodeQL analyses,
  but Docker's production dependency audit failed on DOMPurify. The audit gate was not weakened.
- The owner explicitly authorized validating and merging existing PR #138. Its original go-test
  failure was the stale Go1.26.9 baseline's GO-2026-6809 finding, not the dependency patch.
  Merged main into that PR without rewriting history; the current-base diff remained only the
  manifest and lockfile update from DOMPurify 3.4.16 to 3.4.17. Independent review found no blockers.
- At dependency head `d7d7ff33`, local typecheck, 120 Vitest files / 1006 tests (111.45 s), SPA build
  (1.20 s), Go build/vet/staticcheck and full SQLite/Postgres Go tests (app 46.676 s) passed. The
  production audit has zero critical/high/moderate findings; five pre-existing low findings remain.
  Exact-head CI run 39882851649, all CodeQL checks and Docker smoke/E2E passed before squash merge
  to main at `8ab51334`. No attribution trailer was included in the merge message.
- Integrated that main update into P23 at `21725f0b`; combined local regression and new exact-head
  CI are pending. P37 needs the same dependency integration/revalidation. No Apps branch,
  production setting, deployment or database shape was changed.

### P23 merge and P24 comparator foundation (2026-10-22)

- P23 head `a829788c` passed all exact-head CI checks (test run `39883951228` plus CodeQL),
  including Docker after the dependency fix. Combined local Go/Postgres tests passed (app 86.572 s),
  as did typecheck, 121 Vitest files / 1017 tests (116.75 s), SPA build (709 ms), serialized Go build,
  vet and staticcheck. The final read-only Apps merge previews were clean (`2624cfbf`, `db035a20`)
  against unchanged verified heads. No branch of that separate work was changed or merged.
- Re-read the final diff and retained the independent review/guard evidence in PR #152's body,
  then squash-merged it to main at `08b1c1a0`. No production rollout or evaluator enablement followed.
- Started `feat/cli-quote-probe` from that new main. Added a pure, I/O-free comparison foundation
  for P24 replay and later P25 shadow use. It compares exact integer-fen OHLC and the union of dates,
  identifies either anchor's mismatches, preserves differing prices and returns deterministic
  date/field ordering without mutating either body. Agreement is not source admission.
- Empty, duplicate-date, malformed-date or invalid-price evidence is not agreement. Both primary
  anchors must exist; a missing candidate date remains an explicit mismatch rather than an ignored
  row. Volume is deliberately not a price comparison. Initial tests failed before implementation;
  a fixture-ordering error was corrected with an explicit OHLC-ordered helper. Three targeted
  race repetitions passed (1.860 s), including non-anchor OHLC, both anchors and exact-fen values.
- This is a foundation checkpoint, not P24 completion: command dispatch, raw captures, manifest
  safety, fixed-panel replay and candidate-source controls remain. It adds no schema, settings,
  network calls or runtime shadow execution. Independent review and full branch validation remain
  required before submitting P24.

### P24 raw-capture foundation (2026-10-22, in progress)

- Independent comparator review found no confirmed issue; exact-fen/date/anchor comparisons,
  deterministic output and no I/O/input mutation were verified. It remains shared groundwork,
  not runtime shadow admission.
- Added capture orchestration with no Store/config dependency. It accepts only registered sources
  and validated canonical targets/windows, reserves a new 0700 directory, writes 0600 raw/parsed
  files exclusively, and records hashes, response status, capture time, content type and bounded-
  body/error metadata. Count-based candidate captures are explicitly labeled rather than claimed
  as bounded range support. Candidate measurement never changes grants or source order.
- A private context-local client override records only probe requests. Ordinary requests still
  use the original pooled vendor client; the process-wide pointer is never swapped. Binary bodies
  are preserved before parser/GBK conversion. HTTP/non-2xx/read failures remain recorded evidence;
  local file errors abort, and cancellation leaves an incomplete manifest without further calls.
  A complete manifest means all requested attempts finished, not that vendors supplied valid data.
- Tests failed before implementation. Additional controls cover private permissions, independent
  SHA-256 expectations, existing directory/symlink refusal, bad input before network/output,
  Yahoo as a display-only candidate, oversized/partial bodies, cancellation, local write collision
  and context isolation. No test here performs a live vendor request.
- Independent capture review found a parent-directory substitution gap: O_EXCL protected leaf
  files but not a replaced parent path. The failing regression reproduced writes into a foreign
  directory. All writes now use a retained os.Root; the original directory retains every file,
  the foreign directory stays empty, and a final identity check refuses to mark replaced/moved
  output complete. Follow-up review closed the finding without additional actionable issues.
- Final application/test head passed full Go/Postgres regression (app 35.809 s), build, vet and
  staticcheck. Three repeated targeted race runs including the independent hash control passed
  (2.136 s). Frontend files are unchanged. CLI dispatch, fixed panel, offline integrity checks,
  replay/record output and an end-to-end no-database CLI guard remain before P24 can be submitted.
  No schema, production, Apps branch or separately owned PR changed in this checkpoint.

### P24 offline replay and command integration (2026-10-22)

- Added `quote-probe capture|compare` dispatch before the server/configuration path. Capture uses
  the fixed three-index/twenty-stock panel across main, ChiNext, STAR, 689 and Beijing codes, plus
  unique operator-supplied suspended codes. Source selection accepts every compiled-in candidate,
  including Yahoo, without granting settlement or changing runtime source order/health.
- The version-1 manifest now declares the source/target matrix, allowing replay to reject missing,
  reordered or duplicate attempts rather than silently shrink the denominator. Offline replay
  validates complete/version/window/anchors, canonical targets, identity/source, unadjusted and
  uncached parsed evidence, all bar validity, exact generated leaf names, regular-file/inode checks,
  byte counts and SHA-256 hashes. Reads use a retained directory root and individual/aggregate
  limits; no manifest URL or source fetcher is executed. Hashes detect corruption, not forgery by
  the evidence author. Raw bodies and normalized parser output are both retained and verified.
- Count-based history is explicitly labeled and filtered by row date to the capture window before
  the common shadow comparator. Exact-fen OHLC, date sets and explicit anchors are compared;
  missing candidate dates remain mismatches, whereas failed/truncated/empty evidence or missing
  primary anchors is unavailable. No agreement grants admission. Both anchors can be one session.
- Comparison JSON is deterministic and includes the manifest hash. Without `--record`, replay is
  read-only. `--record` requires the whole fixed panel, rechecks the manifest identity and creates
  a private `comparison.json` exclusively; it records failures/mismatches as such, not a pass
  certificate. Existing files/symlinks are never replaced. The operations runbook documents flags,
  panel, limits, exit semantics, evidence trust boundary and rollback (revert the command; existing
  evidence remains files, with no database/state repair).
- New tests failed before replay/CLI implementation. Controls cover shared-comparator equivalence,
  repeat determinism, exact one-fen mismatches, matches and count-window isolation, primary/candidate
  failures, missing anchors, malformed/unknown/trailing JSON, hashes/lengths, path traversal,
  symlinks, identity/source/adjustment/cache refusal, incomplete matrices and read budgets. Full-panel
  capture/record uses only a local fake vendor; the database sentinel and directory contents stay
  identical. Linked-binary help/error checks use a named-pipe `RP_CONFIG` tripwire: an accidental
  configuration read/server start would hang and fail. Output errors, existing record/symlink and
  a changed manifest before recording are explicit failures. These guards are positive and negative
  controls, not source-text matching. The CI scoped race lane now includes both probe/comparator.
- Independent comparator, capture and full CLI/replay reviews passed. The capture review's real
  parent-directory substitution finding was fixed and regression-tested in the earlier checkpoint.
  Final local full Go/SQLite/Postgres passed (app 34.512 s); build, vet and staticcheck passed.
  Targeted race tests passed three repetitions (5.959 s). Web typecheck, all 121 files/1017 tests
  (41.70 s) and production SPA build (688 ms) passed. The linked-binary dispatch guard also passed.
  Exact-head PR CI remains the merge gate. No live vendor measurement, production, schema,
  evaluator enablement, Apps branch mutation or fallback write occurred.
- Final guard review found the initial manifest-replacement test accepted any recording error, so
  the unrelated partial-panel refusal could mask removal of the intended hash gate. Tightened it
  to require the exact manifest-change failure. Temporarily removing that gate now fails with the
  unrelated panel error, and restoring it passes. This was a regression-assertion defect, not a
  missing runtime guard; no production code changed after the full implementation review.
  Follow-up independent review closed it, and three final targeted race repetitions passed (5.764 s).

### P24 merge and P25 bounded history foundation (2026-10-22)

- P24 exact head `6a33df58` passed full CI run `39889303053` (Go/SQLite/Postgres, scoped race
  4m9s, frontend, workflow lint and Docker/font/smoke gates) and CodeQL run `39889301435`.
  Main-only full-race/cross-compile lanes were skipped by design on the pull request, not called
  passing. Reconfirmed Apps #141/#142 heads unchanged and clean read-only merge previews before
  squash-merging #154 to `c4f0c93c`. PR evidence records both review findings, fixes and guards.
  No release, deployment, production measurement or Apps mutation followed.
- Started P25 from that main. `fetchSinaRange` validates the window before HTTP, requests the full
  800-bar count tail through the existing snapshot/history fetcher, and treats display-path
  `BarsUnavailable` degradation as an error rather than shadow evidence. Ordinary display behavior
  is unchanged. No source is advertised as a shadow peer at this foundation stage.
- The range helper validates source/adjustment/cache flags, all dates/prices and duplicate dates,
  sorts a copy, and checks the newest untrimmed kline date and exact-fen close against the snapshot
  in the instrument's timezone. Checking before trimming also catches a bad newest row outside the
  requested historical window. A full 800-row tail beginning after the requested start returns
  typed `window_not_covered`; a shorter series does not invent pre-listing or suspended sessions.
  The returned copy contains only row dates inside the requested window; empty history is refused.
- Tests failed before implementation. Captured stock fixtures are served by a context-local fake
  transport with actual GBK conversion and assertions for the 800-row request. Controls cover
  one-fen disagreement with otherwise valid OHLC, snapshot-date/stamp mismatch, reversed rows and
  no input mutation, unavailable/empty/nil/duplicate/malformed/over-cap histories, source and cache/
  adjustment mismatches, exact cap coverage, cancellation and unsupported/invalid requests before
  HTTP. No test or command here makes a live vendor request.
- Independent review found no actionable issue; optional coverage was added. Full local
  Go/SQLite/Postgres passed (app 35.372 s), build/vet/staticcheck passed, and three targeted race
  repetitions across range/probe/comparator passed (5.507 s). Frontend files are unchanged.
  Runtime selection/limits, shared comparisons, fallback controls, persisted diagnostics and final
  full-branch review/CI remain P25 work. This adds no schema, settings, runtime writes or admission.
  Back out by reverting this helper; no database/state repair is needed.

### P25 fallback controls and explicit network budget (2026-10-22, in progress)

- The owner delegated the ambiguous call-counting choice. Preserved at most two full candidate
  observations and added an independent ceiling of four HTTP RoundTrip attempts per working
  pass: Sina normally needs snapshot plus kline for each observation. Redirects consume the same
  budget, rather than obtaining extra requests. Both units are documented in the decisions/plan.
  These limits are comparison-only; they grant no alternate writer. Post-ledger scheduling and an
  independent bounded deadline remain required when integrating execution.
- Added a context-local cloned-client transport with a mutex-protected shared HTTP budget. It
  rejects an exhausted budget before the underlying transport; cancelled requests are not sent.
  Tests exercise actual redirects, two complete fixture-based Sina reads consuming four requests,
  concurrent attempted overrun, isolation across passes, cancellation and the unchanged global
  client. This is the network-limit foundation, not runtime enforcement of two selected bodies.
- The admin settings reader/writer exposes `fallback` as off/shadow only. Invalid strings,
  non-string values and null return `forecast_bad_fallback` before any submitted field is written;
  omitted fallback preserves its value. Unsupported stored values, including on, read as off.
  Each effective transition and `forecast.fallback` audit commit together using the existing meta
  table, with SQLite/Postgres serialization, whole-transaction contention retries and attributed
  from/to values. Repeated saves do not create no-op audits. Audit failure rolls the transition
  back. No database table, column or acceptance contract changed.
- Tests failed before implementation. New controls cover invalid mixed-field writes, reader/writer
  round trips, no-op suppression, rollback on an unavailable test audit table, concurrent serial
  audit chains on SQLite/Postgres and independent SQLite Stores, NULL persisted values, omitted
  fields and cancellation. Full regression caught missing translations for the new error code;
  those are fixed in all three locales, with audit-action labels and regenerated Traditional
  Chinese. An initial test fixture used a string instead of the numeric OU identifier; corrected
  it to assert the real stored type. Neither issue changed runtime policy.
- Independent control/budget reviews found no actionable issue; optional coverage was added.
  Final full Go/SQLite/Postgres passed (app 35.745 s), build/vet/staticcheck passed, three targeted
  race repetitions passed (6.827 s), web typecheck and 25 locale tests passed. CI's scoped race
  names now include the new Sina, budget and fallback guards. Full frontend/PR CI and final
  branch-wide review will be repeated for complete P25 before submission.
- P25 is not complete: selection of accepted primary bodies, post-ledger comparisons through the
  shared comparator, persistent shadow/call diagnostics, administrator UI controls/status and final
  admission tests remain. No production call, deployment, Apps branch change, fallback ledger
  write or runtime shadow grant occurred. Back out by reverting these controls/helpers; any saved
  local fallback key is inert/off in the previous build, and no ledger repair is needed.

### P25 durable diagnostic storage (2026-10-22, in progress)

- Added versioned storage in the two planned existing-meta keys, `forecast_state_shadow` and
  `forecast_state_calls`. One transaction reserves/locks the pair in stable order, loads both,
  aggregates per-source logical reads, failures, typed refusal counts and explicitly shadow-only
  HTTP counts, then writes both. A single-statement reader sees one snapshot; bounded,
  context-cancellable retries handle transient SQLite contention without mixing snapshots.
  This adds no table, column, migration or database acceptance-contract change.
- Keeps fourteen UTC-day summaries, the chronologically latest twenty results, and at most
  thirty-two differences per detailed result. The full difference count and a truncation flag
  remain explicit; truncated mismatches cannot become agreement. Persistence copies the comparison
  rather than mutating a live result. No raw quote body or vendor error text enters these counters.
- Missing state is distinguishable from damaged state: defaults apply only when both rows were
  genuinely absent. Persisted objects must explicitly carry the supported version, timestamps,
  arrays and consistent day/counter/evidence shapes. Existing empty/SQL-NULL rows, JSON null,
  missing/unsupported versions, partial pairs, invalid counters, duplicate days, missing agreement
  evidence and integer wrap are refused without overwriting the retained evidence. Consumers must
  show an error as unavailable telemetry, not as a healthy zero; runtime/UI integration remains.
- Independent review found a real retention-watermark defect: a delayed old run could discard
  newer observations and regress `updated_at`. The failing regression reproduced it. Retention
  now uses the maximum of the previous and incoming timestamps; in-window late results merge,
  expired late results are ignored, and latest-result trimming sorts by timestamp rather than
  commit order. Follow-up review closed it. Two further findings were fixed: persisted objects
  could inherit a missing version from defaults, and concurrent SQLite readers could return
  `SQLITE_BUSY`. Explicit zero-value decoding and bounded whole-read retries closed both without
  weakening the failing assertions. Remaining optional controls were added.
- Tests failed before implementation and before hardening. Coverage includes close/reopen
  durability, fourteen-day retention, out-of-order and expired writes, concurrent serial writers
  plus snapshot readers on SQLite/Postgres and independent SQLite connections, corrupted JSON and
  structurally invalid valid JSON, overflow, detail bounds/no input mutation and cancellation.
  An abort trigger in an isolated SQLite test database fails the second update and proves that
  neither half of the pair survives. No production schema or database was used for injection.
- Final full Go/SQLite/Postgres passed (app 36.356 s); build, vet and staticcheck passed. Three
  final targeted diagnostic race repetitions passed (16.068 s). Independent follow-up review
  closed every finding. Frontend files are unchanged by this checkpoint. No live vendor,
  production, Apps branch, evaluator enablement or ledger write occurred.
- These are storage helpers, not P25 completion. Post-ledger sample selection/execution, primary
  and candidate call observation, shared runtime comparison, diagnostics/status UI, shadow grants
  and final full-branch review/CI remain. Back out by reverting the helpers; existing diagnostic
  meta values are ignored by an older build, and no ledger repair is needed.
