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

## Current progress (2026-10-23)

- **Released and deployed.** v2026.43 (Beta, main `5359857e`) carries R1a-β1, R1a-β2 and R1b. It was
  deployed after a full backup taken with the previous release and verified by a throwaway restore.
  Steps `0005` and `0006` applied, and the 20 premature refusals were repaired at boot. The owner set the
  ledger start to `2026-10-18T16:00:00Z` (sets 1-6 excluded) and scheduled CSI 800 (`sh000906`) from
  cohort 2026-11 through the administrator page; both writes are audited. The pre-release read-only
  checks found Q3 = 20, Q5 = 0, and no console or debug run of the covered workflow since the ledger start.
- **Merged since.** P26 #145 (`dd24e285`), P16-P18 #160 (`a383f107`), P28 #161 (`38569ef5`), reader
  signals and runbook #162 (`d9db4d9f`), P27 #163 (`02c7c4bc`), and the migration-order guard #159.
  R2a is complete except the `own` scope.
- **Held.**
  - Purge ownership evidence, step `0009`, draft #165 on `feat/forecast-purge-evidence-r2a`. It merges only after
    PR 141 (`0007`) and PR 142 (`0008`). Until then every own-scoped record, drawer and custom read
    answers 503 `forecast_purge_history_unavailable` by design.
  - P39b, draft #157. Waiting only for the run-target refresh after the v2026.43 deploy.
  - P19, draft #166 on `feat/forecast-cohort-parity-r2a`, is replayed onto current main and validated.
  - P20, draft #168 on `feat/forecast-reading-integration`, is stacked on #166. The original background
    task had stopped before starting P20; the reading-card integration is now implemented and tested.
  - P13 demo revision, draft #167 on `docs/forecast-demo-v2-review`. The owner's review and production
    baseline screenshots remain visual acceptance gates for P19/P20.
- **Next step.**
  - The owner reviews the revised demo. Appendix A's "Today" column still needs production baseline
    screenshots.
  - An administrator refreshes the run targets, then P39b merges as its own Beta.
  - After PR 141/142 land, rebase #165 while preserving the shipped migration-list prefix. The current
    draft appends `0009` after `0010` and the container e2e now checks exact own-subtree populations.
  - Review #167, #166 and #168 as drafts. Full reader rollout still requires the P18 performance gate
    and one week of administrator observation; creating the drafts changes no production setting.
  - After PR 141 lands, re-enable scoped bridge reads on its shared token binding.
- **Known limitations.** Unchanged from 2026-10-22, plus: zh-TW strings added by P27 are OpenCC output
  pending human review.
- **Evaluator state.** `forecast_auto` is off. Before mode `1` the readiness list still needs a dry pass,
  dry-run checks for `sh000852` and `sh000906`, and a test alert delivered to both channels.

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

### P17/P18 — review repair validation and server requirement audit (2026-10-22)

- Exact application/test head `228a9f0a` passed full Go regression with Postgres enabled (app
  package 61.285 s), three-repeat targeted race checks with Postgres disabled (11.509 s), vet,
  staticcheck and the subsequent serialized Go embed build. Independent read-only follow-up
  closed the nonstandard-horizon h12 finding and found no additional actionable P18 issues.
  Separate P17 response-metadata review also completed without actionable findings, including
  retained-range scalars, classified phases, mature totals, purge exclusion, capabilities and
  external-all omission. Review used SQLite, not a second concurrent Postgres process.
- Added positive controls proving that a standard-horizon set still has a next h12 obligation
  when its four session checkpoints are terminal, whether h12 is absent or stored pending.
  Both-driver targeted controls passed (0.928 s). The fixes therefore remove only inapplicable
  obligations rather than hiding every missing checkpoint.
- Remeasured the repaired application source `49b49b40` using the unchanged opt-in benchmark,
  three serial repetitions per driver. Maximum full real/prefetch/mixed p95 values were
  9.128/1.254/9.101 ms on SQLite and 12.767/3.655/12.606 ms on Postgres. All 800 ms assertions
  passed; the performance document records these separately from the earlier before/after run.
- Binding implementation section 8.3 audit: `forecast_groups_explain_test.go` covers non-mature
  phase counts, cap boundaries, earliest dates, 500 frozen state comparisons and external-all
  volume omission. `forecast_groups_metadata_test.go` covers typed JSON round trips, route-gate
  capability parity, scoped retained ranges, mature/terminal counts and purged-month exclusion.
  These are local implementation checks, not resolution of the separate own purge-history leak.
- Binding implementation section 8.4 audit: stage defaults, unknown/off denial, independent scope
  gates, admin full and audited changes are covered by `forecast_reader_stage_test.go` and the
  real-mux reading-stage matrix. Deep-link readable/hidden/foreign-ID controls are in
  `stock_deeplink_test.go`. Stock/Run summaries, selected-body identity, ledger fail-open behavior,
  full-stage gating and prefetch omission are in `forecast_reading_summary_test.go`; the scoped
  60-date timeline fixture and reading matrix check positive and negative flags. Edition fixtures
  cover empty editions, readable siblings, actual counted editions, cross-analysis/cohort denial
  and machine/bridge shape preservation. Session non-accepted/error redaction and token isolation
  are exercised by the credential/access matrix. Store/route variants run on both drivers.
- This closes the local P17/P18 implementation/review audit, not their integration, CI, release
  or rollout gates. P20 owns the chip/card/overlay/timeline presentation; production baseline
  review and a week of admin full-stage observations remain unperformed. The owner has confirmed
  that own readers must not see global purge months; the proposed evidence side table and its
  compatible upgrade path still need approval before any schema write. No production action,
  schema change, Apps edit or separately owned PR mutation was made.
- A fresh frontend check at documentation head `f417d340` passed typecheck, all 120 Vitest files /
  1020 tests (41.26 s), the production SPA build and the subsequent serialized Go embed rebuild.
  No golden fixture changed since `10190278`.
  Read-only remote checks still show P26 #145 draft with successful required checks, dependency
  #138 open with a failed Go check, and P37 #151 open with a failed Docker check. No PR was changed.

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

### P25 post-ledger runtime integration (2026-10-22, in progress)

- Copies at most two unique, accepted primary instrument bodies, with explicit first/last anchors,
  and executes candidates only after normal primary ledger processing completes. Early cancellation
  or panic suppresses candidate work while retaining available primary-call diagnostics. Each sample
  rechecks fallback mode, operator order and primary identity. The independent candidate deadline is
  fifteen seconds; upstream concurrency is shared, but responses, transport budget and health are not.
  Candidates never return to settlement planning. Sampling is opportunistic, not representative or
  Tier 2 admission evidence. No shipped source has received a shadow grant at this checkpoint.
- Uses the shared exact-fen comparator after identity, market, currency, timezone, history flags and
  completed-bar checks. Persists calls, failures, neutral refusals and bounded comparison evidence
  using the reviewed atomic storage. Includes primary adjustment observations; candidate failures,
  panic, timeout, mismatches and storage failures cannot retry or alter primary ledger writes.
- Red/green controls compare the complete ledger checksum against shadow-off output and assert
  that it already matches before candidate execution. They cover agreement, one-fen mismatch,
  missing anchor, wrong source, missing history, cached/adjusted data, errors, panic and deadline.
  Saturated-slot cancellation never enters or bills the adapter. A hidden quota-denial test first
  reproduced misattribution as a source failure; a transport denial counter fixes attribution even
  when an adapter degrades its error. A real fourth-request HTTP failure remains a failure. Independent
  review found duplicate quota refusals; recording each result once closes it, with a pinned guard.
- Full Go/SQLite/Postgres passed (app 37.996 s), build/vet/staticcheck passed, and three targeted
  runtime/budget/walk race repetitions passed. Independent follow-up review closed all findings.
  CI's scoped race selector now explicitly includes the complete ledger-checksum test. No live vendor
  call, production change, evaluator enablement, Apps edit or alternate ledger writer occurred.
- P25 remains incomplete: administrator controls/diagnostics presentation, shipped Sina shadow-only
  grants, final end-to-end admission guards and branch-wide review/full CI are next. Back out by
  reverting this integration; diagnostics are ignored by an older build and no ledger repair is needed.

### P25 administrator controls and diagnostics (2026-10-22, in progress)

- The administrator settings response now reads the atomic diagnostic pair using the request context.
  Explicit empty/ready/unavailable states distinguish uninitialised telemetry from measured zero and
  damaged evidence. A decode/read failure returns no partial state or raw error/body; the reader never
  repairs or replaces evidence. Existing administrator authentication covers both GET and POST;
  routed tests deny an ordinary reader and allow an administrator. No reader-scope surface changed.
- The settings modal offers only off and shadow and posts only changed fields. Its copy explains
  third-party reads, two logical candidate observations versus four HTTP attempts, source-order
  membership, post-ledger execution and no evaluator enablement or alternate writing. A status tag
  shows the current comparison mode without treating retained history as ongoing comparison work.
- The diagnostic card shows retained daily per-source logical calls (including adjustment checks),
  source failures, refusal-code counts and explicitly shadow-only HTTP attempts, followed by daily
  comparison counts and the twenty bounded recent results. Evidence includes both anchors, total
  differences and compared dates, exact integer-fen values and a truncation warning. Daily counter
  dates are UTC; bar dates are labelled in the instrument timezone. Copy states that sampling is
  opportunistic and agreement never grants ledger admission. Missing/damaged telemetry shows a
  warning or no-record message, not fabricated zero tables. Locale keys cover all three languages;
  Traditional Chinese is regenerated through the existing OpenCC workflow.
- The new backend/UI tests failed before implementation. Controls cover no-record versus corruption,
  preserved damaged input, genuine retained state, route permissions, shadow-only changes without
  benchmark/evaluator changes, no-op saves, unavailable/partial telemetry, anchors, exact-fen values,
  a recorded zero versus a missing value, and unhidden truncation. Independent review found no
  actionable issue. Full Go/SQLite/Postgres passed (app 55.845 s), build/vet/staticcheck passed,
  three targeted admin/fallback race repetitions passed (14.218 s), and all 122 frontend files /
  1022 tests, typecheck and production SPA build passed. Final focused locale/UI checks were repeated
  after clarifying the trading-date header; final complete-branch CI remains a separate gate.
- An isolated synthetic preview exercised the actual embedded SPA and administrator save endpoint:
  off to shadow to off, including keyboard selection. The evaluator remained off throughout, verified
  from the preview database. Desktop dark-mode diagnostics and modal layouts were inspected; no
  responsive breakpoint, live candidate result or production acceptance is claimed. The temporary
  browser and server were closed. No production, live vendor, Apps, schema or source grant changed.
- P25 is still in progress. Shipped Sina comparison-only admission, end-to-end grant guards and final
  full-branch review/CI remain before submission. Back out by reverting these admin/UI changes;
  diagnostic evidence stays in the existing meta keys and no ledger repair is needed.

### P25 shipped comparison grant and final local review (2026-10-22)

- Added Sina's explicit Shanghai/Shenzhen `settled_shadow` grant and registered the bounded range
  adapter only after comparator, runtime, persisted diagnostics and administrator controls were in
  place. Its cash-dividend/kind flags remain false, `settled` remains absent, and public display-range
  grants do not expand. Tencent remains the only Tier 1 writer; Beijing, overseas and Yahoo shadow
  admission remain absent. Default fallback remains off. The quote panel now truthfully declares
  Sina as its enabled comparison peer. P24 capture now selects Sina's bounded adapter and retains
  any coverage/coherence failure as failed evidence; it grants nothing and still writes no database.
- Grant tests failed before wiring and passed afterward. The market matrix rejects alternate writers
  even when Tencent is disabled, with no vendor call. A real registered Sina adapter runs captured
  snapshot/history bytes through the pass-local transport budget, shared comparator and durable
  result path: four accepted dates, both anchors, two measured HTTP requests, match and one-fen
  mismatch, no ledger or health changes. No global vendor client is replaced. Further guards prove
  mode/order/primary revocation between samples, aborted-pass suppression, unique two-body sampling
  and copied primary buffers. These are implementation controls, not new live market measurements.
- Independent branch-wide review found no admission or P24 integration defect, but one combined
  SQLite race run exposed fallback-save `SQLITE_BUSY` errors. Isolated repeats and the initial full
  local race run were green; the failing combined run was still treated as a blocker. A deterministic
  700 ms held-writer regression reproduced premature failure at 0.36 s with the old six-attempt
  retry window. The writer now retries complete rolled-back transactions within a five-second total
  context bound, honoring any earlier caller deadline, with capped exponential jitter. Tests retain
  all original concurrency/audit assertions and additionally prove exactly one audit after release
  and no persisted transition after caller timeout. Five targeted race repetitions passed (14.612 s),
  and the previously failing independent combined suite passed (20.308 s). Follow-up review closed
  the finding and reported no remaining actionable issue.
- Final full Go/SQLite/Postgres after hardening passed (app 37.463 s), build/vet/staticcheck passed.
  The earlier complete runtime/diagnostic/admission race suite passed three repetitions (75.101 s);
  all 122 frontend files / 1022 tests and typecheck passed on this unchanged frontend. Production
  SPA build is repeated before submission. The runbook documents controls, budgets, neutral refusals,
  retention, unavailable telemetry, immutable ledger handling and rollback; ADR 0041 records the
  actual comparison-only grant. No source, baseline schema, production, Dify, Apps or release action
  outside the described local implementation occurred.
- Local P25 implementation/review is complete, not its merge or rollout gates. Exact-head full CI,
  current Apps merge previews and final PR review remain before merge. Revert the implementation to
  stop runtime comparisons; existing meta diagnostics are ignored by an older build. No ledger
  repair is needed and comparison agreement never authorizes Tier 2 writes.

### P25 merge and P37 current-main integration (2026-10-22)

- P25 #155 merged as `402a0fa04c493096130d5f26ab9dfbbf693ca14b` after independent branch-wide
  review and exact-head validation at `f95ba872d523c89c4e4c4de4d7198e2d5ffeae32`. Full PR CI
  `39897457304` and CodeQL `39897453146` passed. The scoped race job completed successfully;
  main-only full-race shards and release cross-compilation were skipped by policy, not passed.
  Read-only merge previews against Apps PR 141 head `b4741355118c28d95dcf3c096115963ce5936f01`
  and PR 142 head `c4402084f245d957e32d758339f1053853307664` were textually clean. Neither
  Apps branch changed, and combined runtime acceptance was not claimed. Final local embedded-SPA
  inspection declared Tencent as the sole writer and Sina as the comparison-only peer; evaluation
  and comparison remained off, and the isolated preview server/browser were stopped.
- Resumed P37 #151 in its existing clean calendar worktree, preserving its prior implementation
  and tests. Merged current main `402a0fa0`; only the execution log conflicted. Both historical
  sections are retained, and current progress now reflects actual merges and remaining gates.
  DOMPurify 3.4.17 is included from the owner-approved #138 merge, without a duplicate dependency
  change or audit bypass. Complete current-base validation and independent re-review are next.

- Current-base full Go/SQLite/Postgres passed (app 92.973 s); build, vet and staticcheck passed.
  Three targeted projection/read-isolation race repetitions passed (2.746 s). Web typecheck and
  all 122 test files / 1023 tests passed. The production dependency audit has zero moderate/high/
  critical findings and five existing low-severity KaTeX/transitive findings; no audit bypass was
  used. SPA build and serialized embed build are checked before committing the integration.
  The SSE 2026 annual notice and current official closure listing were re-read: all seven table
  spans match the notice, while no verified 2027 notice was found in that listing. Unknown years
  therefore remain deliberately absent rather than inventing next-year closure data. Independent
  current-base review and exact-head PR CI remain required before merge.

### P37 receipt-consistent frozen baseline hardening (2026-10-22)

- Current-base adversarial review found another qualifier edge: a frozen open S0 within the
  generated session window could be preserved while contradicting receipt-derived S0. Receipt
  `2026-09-17T16:00:00+08:00` with frozen `2026-09-16` incorrectly claimed an expected d5 date.
  New earlier/same-day-after-close/later frozen-date controls reproduced the defect before repair.
  Qualification now additionally requires that the exchange-schedule `predS0` reproduce frozen S0;
  otherwise it retains the existing unqualified weekday fallback without changing stored data.
- Every valid receipt/holiday fixture now also supplies its correct frozen S0 and requires the
  identical qualified schedule, preventing a blanket frozen-baseline refusal. Three targeted
  local race repetitions passed (2.683 s); independent follow-up race repetitions passed (2.602 s)
  and explicitly closed the finding with no further actionable issue. Final full Go/Postgres
  regression is repeated after the repair; unchanged frontend validation remains recorded above.

### P37 merge and current-main reader-server reconciliation (2026-10-22)

- P37 #151 merged at `5359857e340e543c8a16d0673ae9968c5c4d1267`, reviewed head
  `94c6de2dabb53ccef32fa684af0a28984cd82a58`, after full PR CI `39902017406` and CodeQL
  `39902013987` passed. The final scoped race job passed; main-only full-race shards and release
  cross-compilation were skipped on that PR, not claimed passed. P25's post-merge main pipeline
  `39897793291` also passed all full-race shards and release cross-compilation. No deployment or
  evaluator enablement followed either merge.
- Resumed the preserved `feat/forecast-reader-server` checkpoint `8734b287` and integrated main
  `5359857e`, without switching or editing the original checkout or either Apps branch. Conflict
  resolutions keep both reader-stage and fallback fields, independent pre-write validation, both
  audit actions/locale labels, published-calendar precedence and all historical execution sections.
  Traditional Chinese labels are regenerated, not translated by hand. P37's receipt-consistent
  frozen-S0 qualification remains in the combined read path; neither estimates nor shadow data
  become observed calendar evidence or settlement input.
- Added combined guards for invalid mixed reader/fallback requests writing neither field, valid
  independent settings retaining both audit actions, and receipt-consistent qualification through
  the reader annotation wrapper without modifying stored S0. Existing published-calendar and
  conservative 21-day deadline controls remain intact. Focused controls passed. Full combined
  Go/SQLite/Postgres passed (app 76.870 s); all 122 frontend test files / 1036 tests, typecheck,
  staticcheck, vet, SPA production build and serialized embed build passed. A first Go build ran
  during npm's dependency replacement and saw a transient missing node_modules directory; it was
  repeated successfully after installation, without changing source or weakening a check.
- This is an integration checkpoint, not release approval. The known own-scoped global purge-month
  visibility defect remains unresolved: the proposed `(rtype, cohort, attributed_ou)` evidence key,
  pre-delete snapshot transaction, live-subtree filtering, OU-deletion reattribution/deduplication,
  no guessed historic backfill and explicit supported-database upgrade path were submitted to the
  owner for approval before any DDL. P26's shared-token Apps integration ordering is still held.
  No new table, column, migration or schema acceptance-contract change is written here. P13's
  baseline/owner review, production rollout gates and later reader UI remain outstanding.
- Three combined runtime/reader/projection/ledger race repetitions passed (66.418 s). Independent
  integration review found no new actionable issue; separate three-repeat reader/projection controls
  passed (6.673 s), and published-calendar concurrency plus shadow ledger-isolation checks passed
  (42.755 s). The review explicitly preserves the known purge-history hold rather than treating
  a clean merge or green tests as reader-scope release approval.

### P27 reader-request and export foundations (2026-10-22, in progress)

- Continued non-DDL frontend work on the held integration branch, without opening a new PR or
  changing Apps. Added a reader-only groups loader for `/api/forecast/groups`, with no management
  alias or administrator prefetch reuse. Denied/unresolved permission makes no request and clears
  retained results. Query/identity changes hide stale results; cleanup discards late responses and
  pending retry timers. Future page wiring must include user/access/population changes in identity.
- A 403 becomes terminal unavailable, with a no-op retry callback. A 429 or non-busy failure is not
  automatically retried. Only 503 `forecast_busy` allows three additional attempts, with exponential
  bounded jitter; an explicit error retry starts a new bounded cycle. Tests pin request counts,
  actual jitter delays, permission revoke/regrant, identity switches and unmount cancellation.
- The CSV helper accepts explicit reader context: single-line disclaimer/population and benchmark
  headers, and mode/official/benchmark on every row. Reader exports omit site-wide unrecorded totals;
  external-all exports remove the sample-size column even if malformed input carries counts, using
  only a mature publication-gate flag. Non-mature sample sizes are not exported for any reader.
  Existing administrator export behavior is unchanged until the management page is split in P27.
- New tests failed before implementation. A test hook initially returned its mock as an unintended
  cleanup callback, and timer-spy restoration briefly reinstalled fake timers during global cleanup;
  both test harness issues were fixed without changing production behavior or weakening assertions.
  Eighteen final focused tests and typecheck passed. Full frontend regression passed all 123 files /
  1044 tests. Lint completed with existing unrelated hook warnings; no new warning was introduced.
  Production SPA and serialized embed builds are repeated at the checkpoint. Independent foundation
  review found no actionable issue; final defensive retry/count guards are reviewed separately.
- P27 is not complete: record route/page states, permission-sensitive navigation, reader drawer
  wiring, operations-page separation/access summary, OU editor inheritance and full accessibility /
  browser acceptance remain. These helpers are not yet wired to a route, and their tests do not prove
  page-level enforcement. The schema, Apps integration ordering and production gates remain held;
  no new database shape, production setting or evaluator mode has been written.
- Independent follow-up reviewed the final unavailable-state retry and malformed non-mature count
  guards, reran all eighteen focused tests and reported no new finding. Page-level integration and
  the held release/security decisions remain separate acceptance work.

### Own purge-evidence store foundation (2026-10-22, in progress)

- The owner approved the `(rtype, cohort, attributed_ou)` evidence table and explicit upgrade path.
  Recorded the decision in ADR 0039 and the decision register. Added isolated store primitives for
  idempotent table creation, pre-delete distinct attribution capture, parent transfer/deduplication,
  and population-filtered marker loading. Nil population retains global history; an empty subtree
  returns none; historic global-only markers never grant own visibility. Missing evidence storage
  returns an error for an own read rather than falling back to global history.
- Tests failed before implementation, then passed on SQLite and Postgres. They cover positive own
  and unrelated populations, legacy-only history, duplicate owners and destination collisions,
  transaction rollback, no-destination removal, invalid/null ownership and unavailable evidence.
  Test-owned tables are removed on cleanup, including assertion failure. A source fixture with a
  literal placeholder report type is explicitly normalized in this new test rather than silently
  changing unrelated existing fixtures.
- Independent review identified a PostgreSQL purge/OU-delete race: evidence inserted but not yet
  committed could be missed by deletion, leaving a reused OU id. Added a transaction-only ownership
  lock, requiring both future callers to acquire it before their first read or set mutation. The
  primitives reacquire it defensively; Postgres uses a transaction-scoped advisory lock and SQLite
  reserves its writer before reading. A deterministic PostgreSQL interleaving uses lock timeout to
  prove exclusion, then verifies transfer after the first commit. Three dual-driver repetitions
  passed (1.534 s). Independent follow-up closed the foundation finding and reran SQLite three times
  (0.523 s); closure is conditional on correct early-lock wiring in both business transactions.
- These primitives are deliberately not registered in automatic startup migrations or wired into
  business routes yet. Explicit upgrade, fresh initialization, startup verification, backup/restore,
  retention/OU-delete/read integration and their route-level access/concurrency acceptance remain.
  Therefore the known own-history release hold is not resolved by this foundation. No production
  database, Apps checkout, evaluator setting, workflow or release was changed.
- Full Go/SQLite/Postgres regression passed (app 61.328 s), as did build, vet, staticcheck and
  three SQLite race repetitions (2.213 s). The initial full run correctly rejected the evidence
  file under the existing attribution-source allowlist; the exact allowlist now includes this
  approved snapshot path and separately forbids it from inserting/updating/deleting/replacing
  prediction sets. Existing sanctioned writers remain unchanged. Independent follow-up reviewed
  this guard extension and reran its SQLite controls three times (0.518 s). No frontend source was
  changed at this checkpoint, and frontend acceptance is not claimed.

### 2026-10-23 — takeover, release and the R2a merges

- The previous implementer stopped mid-checkpoint with the explicit step for purge ownership evidence
  uncommitted. The work was carried to a new branch unchanged and validated: full Go suite on SQLite and
  Postgres. It was then committed.
- **Step id.** The step was first registered as `0007`, which PR 141 also uses (PR 142 uses `0008`). It is
  now `0009`, and the unapproved Tier 2 provenance proposal takes the next free id. Announcements then
  merged `0010` (#158) ahead of all three. Because a backup's header must be a prefix of `migrations()`,
  the list is ordered by merge, not by id. #159 pins that order in `TestMigrationListKeepsItsMergeOrder`.
- **P26 and the Apps branches.** P26's only textual conflicts with PR 141/142 came from its copy of their
  app-token identity binding. P26 now fails closed instead. A bridge Bearer gets 403 `forecast_unavailable`
  on both v1 prediction reads and reads nothing from the ledger. That is stricter than main before it,
  where a bridge query token received the unscoped machine shape. `apps_token.go` and `apps_api.go` are
  unchanged from main. A follow-up restores scoped bridge reads on the shared binding once PR 141 lands.
  ADR 0029, ADR 0039, the design doc and this plan were amended to match.
- **Own-scoped purge reads (P16-P18).** Disclosure uses the subtree's evidence, set exclusion uses the
  site-wide history, and an undisclosed purged month is indistinguishable from an empty one. A
  `forecast_reader` restore keeps the lower stage.
- **Review and integration.** Each unit was implemented test-first and passed at least two rounds of
  independent adversarial review with distinct lenses before its PR. Squashed predecessors were merged
  into stacked branches as `-s ours` merges, only after their trees were checked to be identical to the
  branch's own copy.
- **P28.** The e2e run against current main passed 152/152 on a throwaway portal. P27 was checked in the
  browser on the isolated seeded preview: the record page, the operations page and the OU editor field.

### 2026-10-23 — draft review handoff

- **Purge ownership (#165).** Replayed only the own-evidence commits onto the current main. Startup
  refuses an unrecorded explicit step before automatic DDL, and the offline command verifies the
  accepted baseline. Backup/restore retains the merge-order prefix. Retention and OU deletion acquire
  the same ownership lock before inspecting rows. The final review found stale migration-order test
  assumptions and destructive tests that left the shared Postgres evidence table missing; both are
  corrected. SQLite and Postgres full suites passed, and the release-image smoke now verifies the
  exact 3/1/1/0 subtree populations rather than the pre-upgrade 503 hold.
- **Cohort record (#166).** The recovered P19 branch is replayed without predecessor commits. Web
  typecheck, all 127 test files (1417 tests), production build and backend checks passed. CI exposed a
  drawer entrance/retry focus race; focus now goes to stable content after the animation, and the
  follow-up CI is green.
- **Reading integration (#168).** Stock and run cards use the display selection independently of
  monthly membership, clear prior access/revision data, and isolate forecast errors from the body.
  The summary slot is reserved on warmed responses. Shared Go/vitest fixtures pin completed bars and
  the open-left/closed-right low window. The read API adds the linked report category for exact
  date/category/ID navigation, without a schema change. The full web suite passes 130 files and 1437
  tests; Go build/vet and SQLite/Postgres suites pass. Synthetic browser checks cover 375/768/1280 px,
  stock/run mounting, pending/settled cards, contrast/axe and scope-none suppression.
- **Demo (#167).** Browser sweep covers seven scopes, three views, two widths and light/dark themes,
  with no page overflow or script errors. This proves the draft demo's behavior, not production
  baseline fidelity or owner visual acceptance. Traditional Chinese remains generated pending human
  review. No release, production database, workflow or evaluator mode changed during this handoff.

