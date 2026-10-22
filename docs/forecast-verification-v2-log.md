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
  1.26.10 security fix #144, upload-test timer fix #147, captured qfq-response fix #148,
  halt evidence #149 and P14 seeded preview #150 (`74e8c564`) are merged.
- **In flight.** P26 #145 on `feat/forecast-record-access` has completed local validation and
  independent review; main was integrated at `af036007`, whose full CI/CodeQL and combined
  local Go/Postgres, 1018 web tests and repeated race checks passed. It remains a draft because read-only merge
  previews report shared-token text overlap with Apps PRs 141/142. Coordination authorization
  is pending; neither Apps branch nor the user's checkout was changed. The locally validated
  P16/P26/P37 integration checkpoint is `10190278`. P17 cell explanations are in progress on
  `feat/forecast-groups-explain`, stacked locally on that checkpoint, without an open PR.
  Its response metadata and retention-range guards are implemented, but independent response-level
  review remains outstanding. P18 is proceeding locally on `feat/forecast-reader-server`.
  Own-scoped purge-history isolation still awaits approval of organization-evidence storage.
  Independent P37 #151 is held by the unchanged production dependency audit; existing DOMPurify
  update #138 needs permission to reconcile and merge. No red CI has been merged.
- **Next step.** Complete P17's remaining review/integration gates and P18's reading-page server view,
  obtain the purge-evidence schema and dependency PR decisions, and resolve P26's integration
  sequence before P27/P28.
  P13 still requires authorized production baseline screenshots and owner
  review before P19/P20. P39b's Dify publication and undeclared-input gates are verified below;
  the R1b deployment and subsequent Portal target refresh remain outstanding. No production
  action has been performed here.
- **Known limitations.** Beijing 30%/listing-day and rights-issue captures remain missing. The
  follow-ups cover real live suspensions, delisted-response availability, 301/689/low-price
  controls and a P8 bonus/unchanged-ratio pair; they do not claim settlement from unavailable delisted history. The P15
  forecast translations are machine-generated, pending owner-arranged human review. Seeded
  responsive parity still requires the later layout/reader work and owner review. Re-check ADR numbers before P23: Apps reserves
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

### P26 — integrate independently validated main changes (2026-10-22)

- Integrates main at `fa250ced`: P22 #146, upload timer lifetime #147 and captured adjustment
  parsing #148, each independently reviewed with green exact-head CI and CodeQL before merge.
- The merge had only a running-log conflict. Both sides' append-only evidence is retained; the
  progress summary distinguishes previously green P26 `27a5a6af` from the new combined head,
  which must be validated again. No runtime conflict required a manual resolution.
- P26 remains a draft awaiting authorized Apps integration coordination. This merge does not
  claim that its shared-token overlap with Apps PRs 141/142 is resolved and changes neither
  Apps branch nor the original user checkout. No production action is included.
- Independent merge review found no actionable issue and verified all imported files except
  the resolved log are byte-identical to main. Combined full Go tests with Postgres enabled,
  vet/staticcheck, web typecheck, 120 Vitest files / 1018 tests, production build and serialized
  embed rebuild passed. Three-repeat bridge/snapshot/captured-fixture race validation and
  exact-head CI remain to be confirmed; the Apps sequencing hold remains independent of them.

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

### P16 — ordered membership classifier checkpoint (2026-10-22, in progress)

- Starts from merged main `74e8c564` in an independent statistics lane. P26 draft #145 has
  integrated that main as `af036007`, with full local validation and exact-head test run
  39805130175 / CodeQL run 39805121956 completed successfully. It still awaits Apps integration
  ordering; this P16 work neither removes that hold nor changes either Apps branch.
- Extracted the pure ordered membership classifier and rebuilt the statistics wrapper on it.
  Canonical symbol/receipt/id order preserves the permutation inputs. Pre-live and non-accepted
  rows do not reserve a symbol; pending/degraded/mismatch and off-benchmark first decisions
  cannot be replaced by a rerun. Unknown states now remain visible in an explicit coverage
  bucket instead of disappearing, and never become members.
- Tests first failed for the absent classifier and unknown bucket. Explicit precedence/order
  tests and 500 deterministic fixtures against an independent frozen old implementation pass;
  existing golden cells/members remain byte-identical without fixture changes. Independent
  checkpoint review found no actionable issue. Full Go/SQLite/Postgres regression, vet and
  staticcheck passed; three repeated classifier/golden race passes also passed (10.037 s),
  including the final unknown-does-not-reserve and per-cohort-intent controls.
- This is not P16 completion. Remaining work includes read-time membership against full cohort
  populations, display/counted set identity, read fields and integer upside, restricted/error
  omission, cross-process purge markers, revised phases and OpenAPI contract tests. No P16 PR
  is ready to merge, and no schema or production action is included.

### P16 — read identity and membership checkpoint (2026-10-22, in progress)

- Read membership now uses the shared classifier over complete monthly populations, independent
  of report revision limits and symbol pagination. Restricted readers never load the global
  population; failed population loads omit membership and counted identity without losing the
  readable report. Tests compare restricted responses byte for byte before and after another
  tenant's earlier decision.
- Display identity prefers the latest accepted revision matching the current body, with the
  latest accepted revision as fallback. Counted identity comes only from the monthly member.
  Compact identity scans preserve both selected revisions beyond the 200-set detail limit;
  supplemental loads require both report and set identity and retain viewer filtering.
- Added report dates and exact integer upside truncated toward zero, with overflow rejection.
  Existing report-local counted helpers were removed. SQLite and Postgres tests cover 221
  revisions, timestamp ties, cross-report supplemental-load rejection and paginated symbol reads.
  The test fixture writer now uses the existing portable insert-ID helper on both drivers.
- Independent checkpoint review found no actionable issue. Full Go/SQLite/Postgres regression,
  build, vet, staticcheck, web typecheck, 120 Vitest files / 1006 tests and production SPA build
  passed. Existing golden snapshots remain unchanged. Final serialized embed rebuild also passed.
- This checkpoint is not P16 completion or merge readiness: persistent purge checks, phases,
  drawer population semantics and OpenAPI remain, as does reconciliation with P26's protected
  access rules. No schema, Apps-branch, deployment or workflow-publication change is included.

### P16 — persistent purge-read checkpoint (2026-10-22, in progress)

- Statistics and drawer requests now check persisted retention markers before consulting cell
  caches. Marker query/scan failures return a database error rather than serving cached metrics.
  Monthly ranges retain months represented only by markers; those rows and drawers return
  `purged` with `purged_at`, without scoring or reusing a mature cell.
- Exploratory populations exclude marked cohorts and disclose sorted `excluded_months` with
  dates. A request with exclusions bypasses full-population cache entries; unaffected requests
  retain their existing cache behavior. Later writes into a marked cohort cannot resurrect it.
  Optional client types describe the new metadata; final visual rendering remains with P19.
- Tests first failed for the absent purge response fields. The final test uses two independent
  file-backed SQLite connections and two servers, primes the reader's mature cache, and runs the
  writer's real retention transaction. Mixed-range controls verify exact surviving metrics,
  all surviving drawer members and matching exclusions, including same-symbol rows reintroduced
  into the marked month. Marker-table failure refuses both monthly/custom statistics and drawers.
- Independent review requested the mixed-range control; the added control closed that gap and
  follow-up review found no actionable issue. Full Go/SQLite/Postgres regression passed
  (32.676 s for the app package), as did vet/staticcheck, three repeated purge/golden race passes
  (13.802 s), web typecheck and the 18 targeted matrix/export tests. Golden files are unchanged.
- This remains an unmerged P16 checkpoint. Phases, drawer population semantics, OpenAPI and
  P26 access reconciliation still need completion and final review. No schema or production
  action was performed.

### P16 — read-phase and published-calendar checkpoint (2026-10-22, in progress)

- Added the seven reader phase constants. An unresolved mark waits until its 15:30 lower bound,
  then settles; it becomes overdue after 21 days without an exact date, or 48 hours after an
  evidenced exact due date. Only a valid recorded due session can produce `retrying`; malformed,
  contradictory early and future session records do not manufacture a retry. Terminal statuses
  win, and repeated annotation clears previous derived dates and phases.
- Readers can reuse immutable copies of evaluator-validated completed calendars without fetching
  market data. Publication occurs only after index/volume validation and excludes intraday and
  future sessions. A calendar window replaces rather than joins previous windows. Missing
  receipt coverage or a frozen start absent from the window retains the conservative fallback;
  an incomplete window cannot establish an exact deadline. No settlement arithmetic changed.
- Committed test-only scheduled-closure ranges and source URLs from SSE's 2023, 2024, 2025 and
  2026 notices. The independent session enumeration checks 39,854 receipt/time/checkpoint/frozen
  start combinations for the lower-bound property, preserving existing normalized leap-day month
  arithmetic. These are scheduled-calendar test evidence, not runtime calendars or vendor bars.
- Initial tests exposed the old indefinitely-waiting/retrying behavior and retained derived
  metadata. New controls cover nanosecond deadline boundaries, immutable input/output copies,
  invalid-publication retention, no read-time network call and concurrent publication/readers.
  Independent checkpoint review found no actionable implementation issue. Full Go/SQLite/Postgres
  regression passed (45.015 s app package); vet/staticcheck, web typecheck and all 120 Vitest files /
  1006 tests passed. Three repeated phase/calendar/concurrency/golden race passes also passed
  (40.334 s). Golden fixtures are unchanged.
- This does not complete P16: source/direction fields, paused notices, drawer population semantics,
  OpenAPI and protected-access reconciliation remain. No schema or production action is included.

### P16 — outcome fields, pause notice and read contract checkpoint (2026-10-22, in progress)

- Read marks now expose their stored source and use the shared hit predicate for resolved
  direction. Missing inputs and corporate-action exclusions are untested; unresolved/final-refusal
  marks do not carry a direction. Dual-driver tests pin agreement, disagreement, flat results,
  missing data, exclusions and both sides of the absolute five-percent threshold.
- Both reading responses expose a separate `settlement_paused` notice for off/dry/unknown modes.
  The stock page keys it to the displayed report with the existing stale-response guard. The
  panel renders the pause notice separately from settling/overdue phases in all three UI locales.
  Tests verify pending navigation and a late abandoned response cannot retain another report's
  notice; older responses without the optional flag remain supported.
- Removed never-filled `counted_in_group`, `baseline_bar` and `baseline_fen` read fields and their
  obsolete client fixture values. Added complete read set/claim/mark/response OpenAPI schemas,
  linked both machine-route responses, documented display/counting identity and corrected the
  old report-local counting description. Tests lock phase/direction/membership to Go constants,
  cover every exported read field and verify response bindings in all three localized specs.
- Tests first failed for absent fields and pause rendering. Independent review found that an
  empty symbol query still serialized `sets: null`, contradicting the new array contract. A new
  route regression reproduced it; the store now returns an empty non-nil slice, and follow-up
  review confirmed closure with no further actionable issue. Existing golden files are unchanged.
- Final validation: full Go/SQLite/Postgres regression passed (48.744 s app package), as did vet,
  staticcheck, three repeated outcome/empty-result/privacy/golden/OpenAPI race passes (21.608 s),
  web typecheck and all 120 Vitest files / 1007 tests. Production SPA build and the serialized Go
  embed rebuild also passed.
- P16 remains unmerged: drawer membership population/link semantics, removal of the unused
  coverage refusal bucket, final contract audit and P26 protected-access reconciliation remain.
  Apps integration PRs #141/#142 remain open at their previous heads; P26 #145 remains held.
  No schema, production deployment or workflow publication was performed.

### P16 — member population and readable links checkpoint (2026-10-22, in progress)

- The legacy management drawer accepts `population=all` to explain all group members and
  first-decision pending-baseline candidates. Duplicate reruns never become additional members.
  Exclusion reasons distinguish pending baselines, missing marks, pending/refused/final marks,
  corporate actions, non-standard h12 horizons and unknown mark statuses. The default/scored
  population stays unchanged; the statistical gate receives the same inputs as before.
- Non-mature cells send no outcomes, including for individually resolved marks. Even in a mature
  cell, excluded rows carry no prices, returns, upside or direction. Membership mode invokes no
  permutation scorer. Tests cover mixed exclusions, duplicate reruns, mature exclusions and h12.
- Report links use the existing report-read scope in one batched metadata query; an unreadable or
  concurrently removed report loses its report ID and link metadata. Visible reports use `repKind`
  rather than raw stored categories. SQLite/Postgres controls prove both a granted readable
  report and a hidden sibling, including version grants and a legacy category alias.
- Initial tests failed for the absent mode and raw category. Fixture setup now uses the actual
  account restriction setter and version grants rather than assuming account upsert grants read
  access. Independent review found no actionable issue. Full regression exposed missing UI
  translations for the new error code; all three locales now cover it. Golden files are unchanged.
- Validation after that correction: full Go/SQLite/Postgres regression passed, as did vet,
  staticcheck, three repeated drawer/link/purge/golden race passes (23.730 s), web typecheck and
  the 10 focused matrix/locale tests. The full 120-file / 1007-test web run passed before the
  error-message-only locale addition.
- This checkpoint does not remove the P26 integration hold or make P16 merge-ready. No schema,
  Apps-branch, deployment or workflow-publication change is included.

### P16 — retired coverage field and frozen characterization (2026-10-22, in progress)

- Removed the never-populated `coverage.refused` field from the live Go/TypeScript contracts and
  client fixtures. Checkpoint refusal counts remain: the raw-response regression proves one
  final refusal still appears on a mature checkpoint while all 35 decisions remain members in
  coverage. Its initial failure confirmed that the retired live field was still present.
- Preserved the archived golden files byte for byte through an explicit test-only coverage DTO.
  It copies every live counter and adds only the historical zero refusal placeholder in the
  original key order. It describes frozen characterization serialization, not today's API.
  A reflection guard gives every live counter a distinct nonzero value and checks matching
  names, JSON tags and exact values; it also rejects unexpected archive-only fields. New live
  coverage fields therefore cannot silently disappear from characterization assertions.
- Independent review of both the approach and implementation found no actionable issue. The
  raw API, adapter guard and golden tests pass without regenerating any fixture. This resolves
  the plan's live-field removal while retaining its unchanged-statistics characterization gate.
- Validation: full Go/SQLite/Postgres regression, vet/staticcheck, three repeated raw-contract /
  adapter/golden/member/link race passes (12.904 s), web typecheck and all 120 Vitest files /
  1007 tests passed. Production SPA build and the serialized embed rebuild passed. No archived
  fixture was regenerated.
- P16 still needs final protected-access reconciliation with P26 and an integrated requirement
  audit before a merge-ready PR. No schema, Apps-branch or production change is included.
### P26 — integrate seeded preview and captured halt evidence (2026-10-22)

- Normal merge of main `74e8c564` brings independently validated #149 and P14 #150 into the
  held P26 draft. Both histories' append-only evidence is retained in this log.
- Resolved overlapping reader functions by retaining P26's authorization and protected-set
  projection while selecting P14's `forecastNow` for periods and read phases. Custom/member
  ceilings and adoption observations keep their separate operational clock.
- New real-seed integration tests exercise the own/sub-OU population below 30, external-all
  metrics without coverage or sample size, none denial and exact operational-week adoption.
  Moving the demo clock cannot replenish either request ceiling. Temporary wrong-clock
  mutations failed the period assertion, adoption persistence and both limiter subtests;
  every mutation was restored before validation.
- Independent review found no actionable issue in the conflict resolutions or new tests and
  confirmed incoming seed/clock/evidence files match main. Full Go regression with Postgres,
  build/vet/staticcheck, web typecheck, 120 Vitest files / 1018 tests and three-repeat
  access-matrix/demo-clock race checks passed (174.475 s). Supplemental three-repeat
  bridge/snapshot/captured-fixture race checks also passed (221.801 s). The serialized
  SPA/embed rebuild passed; exact-head CI remains required for the new combined commit.
- P26 stays a draft for the existing Apps integration sequencing hold. No Apps branch,
  original user checkout, production database, workflow publication or evaluator mode changed.

### P16 / P26 — uncommitted reader integration checkpoint (2026-10-22)

- Resolved the overlapping read, group, member and stock-page implementations in the P16
  worktree, preserving population authorization, protected projections and reader semantics.
  The integration remains uncommitted and is not merge-ready.
- Purge exclusions now distinguish exploratory cache entries and in-flight computations
  without changing statistical seeds. A two-store shared-database regression holds an older
  calculation open, purges June, then verifies a new request independently scores July.
  Removing the purge cache tag makes the new request join the blocked flight and fail;
  the mutation was restored and the regression subsequently passed three times.
- Integration tests verify restricted read responses do not change with another report's
  membership decision, and purged external reader responses retain retention metadata
  without exposing volume fields. Independent review found no additional actionable issue.
- Validation: full Go regression without Postgres passed (app package 38.551 s), vet and
  staticcheck passed, targeted integration race checks passed (3.071 s), and web typecheck
  plus all 120 Vitest files / 1019 tests passed. The subsequent full Go regression with
  Postgres passed (app package 45.604 s), followed by the production SPA build and serialized
  Go embed rebuild. The existing mixed static/dynamic locale import warning remains.
- The owner explicitly rejected exposing global purge months to own-scoped readers.
  Historical purge rows cannot establish organization attribution after their source sets
  have been deleted. A minimal organization-evidence side table and unknown-history handling
  were proposed for approval; no schema change has been written. Current draft marker reads
  still require that resolution before release. No Apps branch or production state changed.

### P37 — exchange-backed reading projections (2026-10-22)

- Implements build document section 8.7 and owner decision V23(a) independently from P16/P26.
  The verified SSE 2026 annual closure notice is recorded beside the runtime table. No
  unverified 2027 dates are included; a missing year anywhere in a checkpoint interval falls
  back to a weekday lower bound without an expected flag. Government make-up weekends stay closed.
- Reading marks carry optional `expected: true` only for covered projections. The panel chooses
  expected versus on-or-after wording; all locales and the OpenAPI read descriptions document
  the distinction. This is derived response metadata, not a database column or settlement input.
- Tests pin the demo's d20=2026-10-26 and d60=2026-12-21, receipt-close boundaries, holiday starts,
  unknown-year and frozen-S0 fallback, and clearing derived qualifiers on terminal marks.
  Independent review found a frozen S0 outside the generated window could be substituted while
  retaining the expected flag. A failing regression reproduced it; qualification now requires
  that the frozen S0 be preserved. Follow-up review found no additional actionable issue.
- The source guard rejects projection-table use outside the read annotation path, including
  package initializers. Temporarily adding a table lookup to evaluator wake scheduling made
  that guard fail; the mutation was restored. A runtime test separately pins the old wake date.
- Validation: full Go/SQLite regression, full Go regression with Postgres (app package 35.608 s),
  vet/staticcheck, three-repeat targeted race checks (3.544 s), web typecheck and all 120 Vitest
  files / 1007 tests passed. The production SPA build and serialized Go embed rebuild passed.
  December maintenance is documented in the operations runbook. No production, Apps branch,
  schema, evaluator mode or pending P16/P26 merge was changed.
- P16 integration must preserve the new qualification flag alongside its published-calendar
  and overdue rules; a scheduled closure is not exact observed evidence for its 48-hour rule.

### P16 / P37 — local projection integration checkpoint (2026-10-22)

- Preserved P16's phase vocabulary, calendar publication, protected reads and derived metadata
  alongside P37's expected flag. Updated the reflected OpenAPI mark schema, source-boundary
  guard and panel tests. The standalone P37 PR remains unchanged and unmerged.
- Published completed sessions and recorded due-session evidence outrank scheduled closures,
  including a synthetic unexpected-open-day control. A published S0 also prevents the read
  from claiming it is still waiting for an already observed start. No read fetch was added.
- Expected dates never become exact evidence for the 48-hour rule. A deliberate mutation that
  treated the expected flag as exact evidence failed the new deadline test and was restored.
  Independent review additionally found the estimate was delaying the 21-day deadline.
  Added a failing boundary regression, then separated the display estimate from the weekday
  phase clock: for the September 17 d20 fixture, display remains expected October 26, settling
  starts at the October 16 lower-bound close, and overdue starts November 6. Follow-up review
  found no remaining actionable issue.
- Final full Go regression with Postgres passed (app package 55.284 s); vet/staticcheck and
  three-repeat targeted race checks passed (3.411 s). Web typecheck and all 120 Vitest files /
  1020 tests passed. The SPA production build and serialized Go embed rebuild passed.
- This is a local integration checkpoint, not merge readiness. The own-scoped purge-history
  evidence schema still awaits approval, as does reconciliation/merge of the existing DOMPurify
  dependency PR #138 that blocks P37 CI. No production, Apps branch, schema or evaluator mode
  was changed, and no separately owned PR was modified.

### P17 — pending-cell explanations checkpoint (2026-10-22, in progress)

- Added request-time phase counts, the final-refusal-cap flag and the earliest outstanding
  not-before date/expected qualifier to non-mature cells. The classified member and pending-baseline
  populations feed the explanation, with the same standard-horizon filter for h12. Invalid timing
  has an explicit unknown bucket rather than silently disappearing. Counts include resolved and
  final phases so they reconcile with the full selected population, not only failed attempts.
- Read the already-existing frozen `s0_date` alongside group metadata on both drivers; no schema
  change is involved. SQLite and Postgres tests prove explanations retain that frozen start.
- The scoring builder has a read-time wrapper. Explanations are copied onto response cells after
  computation and never enter mature-cell memos. Pure computation and archived golden fixtures
  remain unchanged. A fixed 500-fixture independent gate oracle exercises open, pending, small
  and mature outcomes; metadata must not alter any state.
- External-all shaping removes every explanation field, including from a deliberately decorated
  mature cell and recursively from slices. The positive leak fixture failed before adding those
  removals; own readers retain their explanations, and shaping never mutates its source response.
  Independent review found no actionable issue in this checkpoint.
- Validation: full Go regression with Postgres passed (app package 53.364 s), vet/staticcheck,
  three-repeat targeted race checks (33.185 s), web typecheck and all 120 Vitest files / 1020 tests
  passed. The production SPA build and serialized Go embed rebuild passed.
- This is not P17 completion. The response-level summary, covered types, limits, timestamps,
  ledger start, evaluator mode and capabilities (with route-gate parity and external-all omission
  tests) remain to implement. No PR, production action, Apps-branch change or database DDL was made.

### P17 — response metadata checkpoint (2026-10-22, review pending)

- Added retained-range summary scalars, classified checkpoint phase totals and the earliest
  outstanding result date. Mature counts reuse resolved/scored/corporate-action/refusal counters;
  purged cells and stale mature-cell waiting metadata cannot enter pending totals or next dates.
  No new query or scorer invocation is used to construct the summary.
- Added covered types, minimum sample size, active range/compute/request limits, UTC generation
  time, ledger start, evaluator mode and permission capabilities. The capability helper is shared
  with route authorization and tested against monthly/custom/member gates across admin, internal
  and external all/own/none subjects. Capabilities do not promise free capacity or rate budget.
- External-all requests skip explain generation, and response shaping independently omits every
  metadata field even when handed a deliberately decorated response. Own-scoped positive controls
  retain scoped counts and existing frozen S0 semantics. The own-cache isolation test fixes its
  clock so required generation timestamps do not invalidate the cross-population control.
- Full regression initially found that an anonymous pointer to an unexported metadata type could
  serialize but not decode. Exported the metadata type and added typed admin/reader JSON round-trip
  guards. Full Go regression with Postgres then passed (app package 54.176 s). Targeted race checks
  passed three times (3.175 s); vet/staticcheck, web typecheck and all 120 Vitest files / 1020 tests
  passed. The production SPA build and serialized Go embed rebuild passed.
- Independent review of this response-level checkpoint did not complete because the review
  runtime reported exhausted credits. The earlier cell-level review does not cover this addition.
  Do not represent P17 as reviewed or merge-ready; its requirement/contract audit and independent
  review remain outstanding. No PR was opened, no production or Apps branch changed, and the
  pending schema/dependency authorization boundaries remain unchanged.

### P17 — summary retention and contract audit (2026-10-22)

- Added a real-route cross-month regression for summary retention boundaries. A marked June
  containing a late ledger row stays excluded; a June-July custom range counts July only and
  cannot import August into its scalar or phase totals. The existing monthly record still counts
  July and August separately, including the same symbol appearing in both cohorts.
- Documented every response-level explanation field and its population/denominator in the
  operations runbook. In particular, accepted rows include repeat editions while checkpoint
  phase counts use classified members; retained counts are not historical all-time totals.
  Capabilities are permission hints, not available capacity, and delayed next dates may be past.
- These fields remain on session/admin statistics surfaces, not the machine API. No new machine
  endpoint or OpenAPI route was added. The earlier typed response round-trip guard remains intact.
- The new route test passed; three repeats of the summary/metadata/privacy/capability race tests
  passed (3.714 s), and diff checks passed. No production code changed in this checkpoint.
  Independent response-level review, final P17 requirement audit and existing authorization
  holds remain outstanding; this is not merge readiness.

### P18 — reading-stage gate checkpoint (2026-10-22, in progress)

- Added the existing-meta `forecast_reader` setting with closed off/card/full values, default
  card, unknown-value refusal and an admin full override. Scope remains an independent gate;
  full cannot widen none. Session prediction routes, including v1 cookie reads, apply the gate;
  persistent machine tokens and the independently authorized record remain unchanged.
- The settings endpoint validates the stage before writes and records `forecast.reader` only
  on an actual transition. Added audit translations in all three locales. No schema changed.
- Added helper, settings/audit and real-route tests. The first route run exposed a missing
  session-signing configuration in the test fixture; repaired the fixture and reran validation.
  Full Go regression with Postgres, three-repeat targeted race checks, vet/staticcheck, web
  typecheck and all 120 Vitest files / 1020 tests passed.
- This is a local, unreviewed checkpoint, not P18 completion or release readiness. Stock/run
  summaries, timeline flags, prefetch/deep-link/edition behavior, the extended access matrix and
  the benchmark remain outstanding. The own-scoped purge-evidence schema and upgrade path still
  require approval. No Apps worktree, production setting or separately owned PR was changed.

### P18 — reading navigation and timeline checkpoint (2026-10-22, in progress)

- Added the resolved `forecast_reader` hint to the shared login/me payload without changing
  `forecast.scope` or the independent record permission. Tests cover all/own/none, external own,
  admin overrides, unknown settings and repeated live policy reads. Updated the SPA contract types.
- A readable `r` now selects its own stock-report date and category before default selection,
  overriding stale date/category parameters. Selection searches only the already-scoped symbol
  list; foreign-symbol, absent, invalid and unreadable IDs fall back without disclosing a date.
  The new positive deep-link test failed before the handler change, then passed on both drivers.
- Added full-stage stock timeline flags from accepted, covered, report-backed ledger identities
  intersected with the caller's readable reports. Prefetch retains the flags. A 60-day route
  fixture covers internal all/own, external all, none and admin, rejected and unsupported rows,
  same-day unreadable accepted reports, every stage, and real/prefetch requests on both drivers.
  A fault-injected ledger read failure omits the flags while preserving the report and navigation.
- Isolated fixture versions from the default version configuration and cleaned temporary stage
  settings so the shared Postgres test database does not inherit test policy changes. The final
  full Go/Postgres regression passed (app package 52.506 s); three-repeat targeted race checks
  passed (9.088 s), as did vet/staticcheck, web typecheck and all 120 Vitest files / 1020 tests.
  The production SPA build and subsequent serialized Go embed rebuild passed.
- Reading summaries, sibling-edition behavior, the final access matrix and the 800 ms performance
  gate remain outstanding. Independent review has not covered this checkpoint. No schema, Apps
  branch, production setting or separately owned PR changed; this is not P18 completion.

### P18 — selected-body reading summary checkpoint (2026-10-22, in progress)

- Added the optional stock/run `prediction` summary at full stage, using an explicit allowlist:
  display set, stance/weight, frozen-baseline upside, body-change warning and the next nonterminal
  checkpoint with the existing phase/date/expected semantics. Missing marks remain obligations;
  unknown timing cannot invent a phase or deadline. Resolved and final-refusal marks are skipped.
- Reused compact identity selection rather than a bounded revision page. The existing 220-revision
  regression now also proves that the reading summary retains the body-matching display set and
  distinct counted set, on SQLite and Postgres. Rejected revisions cannot become summary chips.
- A deliberate concurrent-ingest regression initially paired a future prediction with the old
  selected response body. Added a body-hash-aware identity helper that still rechecks visibility,
  and derive selection/body-change state against the body already selected for this response.
  The regression failed before this repair and passed afterward; existing read callers retain
  their current-body behavior through the unchanged wrapper.
- Internal own and external readers receive no membership or counted identity and do not invoke
  global membership classification. Prefetch omits summaries and keeps scoped timeline flags.
  Fault injection against sets, claims and marks preserves the body on both stock and run routes.
  Added contract types and documented omission, body-selection, privacy and date semantics in the runbook.
- Full Go/Postgres regression passed (app package 66.206 s), as did three-repeat targeted race
  checks (16.127 s), vet/staticcheck, web typecheck, all 120 Vitest files / 1020 tests and the SPA
  production build. The subsequent serialized Go embed rebuild passed. Independent review has not
  covered this checkpoint.
- Sibling-edition links, counted-edition presentation, final access-matrix expansion and the
  recorded 800 ms performance gate remain outstanding. No PR, production action, Apps change or
  schema change was made; the own purge-evidence approval remains pending.

### P18 — readable-edition association checkpoint (2026-10-22, in progress)

- Added explicit empty-edition envelopes and readable sibling links. An edition without an
  accepted set does not borrow stance, display identity, body-change status or checkpoint data.
  Links group by symbol/report-date/type, never title, exclude the current version and carry exact
  report/set/version/cohort identities with a report-ID deep link ending in `#forecast`.
- Internal all readers and admins receive a separate counted-edition identity. The report's own
  counted ID stays unchanged; a full-stage duplicate badge is suppressed when the member belongs
  to a readable sibling. Own and external readers cannot request global classification even by
  passing an affirmative helper flag; the negative guard failed before enforcing the view policy.
- Card-stage session routes carry the same optional edition metadata, including v1 cookie reads.
  Real persistent-query and bridge credentials retain their existing shape. Each edition's card
  retains only its own sets. Later output in the same version cannot hide an older counted report.
- Added SQLite/Postgres route fixtures for differing titles, all/own/external/admin subjects,
  private versions, exact links, card/full stages, empty and populated JSON round trips and
  retained counted identity. Separate controls reject other symbols/dates/types. A cross-cohort
  regression initially showed a newer own-report member hiding the displayed month's sibling;
  repaired both summary and card lookup gates without changing counted-ID selection semantics.
  A September sibling cannot label an October receipt as counted.
- Final full Go/Postgres regression passed (app package 55.672 s); three-repeat targeted race
  checks passed (17.123 s), along with vet/staticcheck, web typecheck and all 120 Vitest files /
  1020 tests. The production SPA build and subsequent serialized Go embed rebuild passed.
  Independent review has not covered this checkpoint.
- Final access-matrix expansion, the recorded performance gate and P20 presentation remain
  outstanding. No schema change, Apps-worktree edit, production action or separately owned PR
  mutation was made. The purge-evidence approval and existing integration holds remain pending.

### P18 — route-stage matrix and local performance checkpoint (2026-10-22)

- Extended `TestForecastAccessMatrix` through the real mux for off/card/full/unknown settings,
  both Default scope variants and both database drivers. Stock/run, hidden reports, prefetch,
  session/v1 predictions, me hints and stage-independent record/member controls add 4,032 probes
  across 21 credential/OU subjects. None leaves readable bodies intact while omitting forecast
  metadata; enrolment and broken-policy walls retain their exact refusals. Positive required
  paths prevent a blanket denial from satisfying the matrix. Counted-edition metadata joined
  the recursive own/external/bridge forbidden-path list. Fixture settings restore presence as
  well as values so absent reader configuration cannot accidentally become an off-valued key.
- Added an opt-in, shape-validated reading measurement over 240 reports, 60 dates, 41.5 KB bodies
  and 1,120 accepted sets. It includes real session/security/gzip wrappers, alternating real and
  prefetch requests and actual all-population sibling classification where authorized. Used the
  identical measurement source against exported pre-P18 source `0f0c477f` and current application
  source `5a483f8b`, serially, with three repetitions on SQLite and local Postgres 18.6.
- Recorded the method, source checksum, every profile's before/card/full p95 and limitations in
  `forecast-reading-performance.md`. Worst full real-request p95 was 16.471 ms; worst mixed p95
  was 15.765 ms, below the local 800 ms budget. These in-process warm local measurements do not
  replace production transport/load observations or the owner-controlled full-stage rollout.
- Full Go/Postgres regression passed (app package 63.221 s); three-repeat SQLite route-matrix
  race checks passed (120.568 s), as did vet/staticcheck. Frontend sources are unchanged from the
  preceding validated checkpoint. The Go embed rebuild passed. The measurement is skipped by
  default in regular tests.
- P18 still needs independent review and a final requirement audit before PR/merge readiness.
  This fixture does not resolve own-scoped purge history; its evidence schema still awaits
  approval. P20 UI/production observation and Apps/dependency integration holds remain separate.
  No production action, schema change, Apps edit or separately owned PR mutation was made.

### P18 — independent-review checkpoint repair (2026-10-22, validation pending)

- Independent review confirmed an inapplicable next h12 checkpoint on nonstandard-horizon sets
  after their four session checkpoints were terminal. Added Stock/Run regressions for absent
  and persisted pending h12 rows on SQLite and Postgres; both controls failed before skipping
  inapplicable h12 during synthesis and next-checkpoint selection.
- Requirement audit additionally found that malformed receipt timing erased known terminal
  phases before summary selection. Added failing phase and Stock/Run regressions for resolved
  and final-refusal marks. Terminal phases now derive before receipt-date parsing, while unknown
  nonterminal timing still carries no fabricated phase/date/expected promise.
- Targeted driver regressions passed (0.824 s). Full regression, repeat performance measurement
  and review follow-up remain outstanding at this code checkpoint. P17 metadata review and
  existing schema/integration/production authorization holds remain separate.
