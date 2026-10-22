# Forecast verification — runbook

**Scope.** Operating the prediction-ledger evaluator (ADR 0029, ADR 0037). Every signal below exists
in the current release; entries for later surfaces (the ledger chain's `no_source`, reader access,
the reading-page stage switch) are added by the PR that creates them.

## First response, in order

1. Read the status card on `/manage/forecast` (mode, last pass, last error, the counters and
   `awaiting_gate`), and the Alerts card for the last delivery per channel. The heartbeat's
   per-benchmark bars and the `panics`/`stale_valve_tripped` flags are in
   `GET /api/admin/forecast` (`heartbeat`) when the card's derived state is not enough.
2. `GET /healthz/forecast` answers one boolean. 503 with the mode on means the heartbeat is missing or
   older than ten minutes; the process may still be up.
3. Read the boot/loop log for `forecast evaluator:` lines. A per-set error names the set; a pass
   error is the pass.
4. Do not force a settlement by editing rows. Every write in a pass goes through one plan; a manual
   `UPDATE` is outside every guard the evaluator has.

## Signals

### `stalled` (Email + Slack, at most hourly) or `/healthz/forecast` 503

The heartbeat is older than ten minutes while the mode is not off.

- Check the process is up and `/healthz` (database) is green.
- Read `last_error` and `panics` on the card. The loop recovers from a panic and keeps going; a stall
  with no panic is a wedge (a blocked store call, a stuck vendor request).
- Restart only after reading the log. The loop derives due work from rows, so a restart resumes; it
  does not replay anything.

### `lagging` (Email + Slack, at most daily)

One of: a due mark unsettled for over six hours, the stale-data valve tripped, or a benchmark
calendar still ending before today after 18:00 CST on a weekday.

- Look at the heartbeat's per-benchmark last bar and source, and at `stale_valve_tripped`, in
  `GET /api/admin/forecast`.
- A vendor-wide lag is waited out: while the valve is tripped no mark is resolved from a missing bar;
  a deferral still records the reason, session and wake, no attempt moves, and nothing can become
  final. Evidence of a halt (a body stamped at or after the session) can still resolve a mark. Never
  force a settle.
- One source down: inspect the reported Tencent request or source failure and wait for recovery.
  Settlement fetches Tencent directly; changing the quotes panel's configured source order does
  not change the evaluator's source.

### `awaiting_gate` names a benchmark

The mode is `on`, the set's frozen benchmark is planned, but no hand check is recorded for that code,
so nothing is written for it.

- Hand-check one would-be d5 for that code: the stock's and the index's closes on `base_date` and on
  the due session, against the exchange's published closes.
- Record the check in the card's form. It is audited (`forecast.dry_gate`) and stored per code.

### `predictions.store_failed` (Email + Slack, each occurrence)

A covered report was stored and its ledger set was not, after one retry. The set cannot be re-created
after the fact.

- Find the report in the audit filter `predictions.store_failed`; the detail carries the error.
- Record an incident. The report is readable; the ledger simply has no set for it.

### A `test` count appears

The producer declared `run_mode: "test"`. A test set is never a member and never a duplicate.

- Expected for Dify test-app runs. If a production run is marked `test`, fix the producer; the set
  stays a test.

### An `off_benchmark` count appears

A set was frozen with a code other than its cohort's active benchmark.

- Explain it in the release note for the cohort. Never re-score: the frozen benchmark is the record.

## Controlled changes

### Switching the evaluator on

The readiness list is code. Until every item holds, `POST /api/admin/forecast {"mode":"1"}` answers
409 `forecast_not_ready` and names the failing items.

1. `dry`, then "Evaluate now"; read the sample and `last_error`.
2. Record a dry-run check for every benchmark in use (the card lists the codes).
3. Send a test alert that reaches **both** channels (the readiness stamp is written only then).
4. Switch to `1`. The change, the actor and the readiness list are audited (`forecast.auto`).

### Scheduling a benchmark

- Choose a code from the closed allowlist. The response's `effective_cohort` says when it applies.
- CSI 800 for November: the schedule must read `effective_cohort: 2026-11`. If the governance deploy
  slipped past 2026-10-31, use the contingency in plan §1.2 and note the time in the operations log.

### Setting the ledger start

Write-once, bounded by the oldest set, and declared in the Dify repository's issue before the first
S0 close. Preview the candidate list on the page, then save once. It survives restores.

## Deploy and restore

### Before a release

Take the ADR 0027 backup. For R1a-β2 also record the heal predicate's count (Q3 = 20). A binary older
than step `0006` refuses a database that records it: rolling back across the access schema is
restore-only.

### After a restore

A restore resets the heartbeat, the alert state, the dry gate and the alert verification. The mode
becomes the lower of the dump and live values, and a restored `1` is capped at `dry`
(`restore_gates_empty`, audited). Re-check deliberately: run a dry pass, record the gates, send the
test alert, then switch on if that is intended.

## December projection-calendar maintenance

After the exchanges publish their annual closure notices, verify the current and next calendar
years in `internal/app/forecast_calendar.go` against those notices. Record the source URL beside
each year's data and include later exchange amendments. Exchange weekends remain closed even
when the government designates them as make-up working days. Do not substitute a public-holiday
calendar for exchange evidence, or invent an unpublished year's dates.

Run `TestProjectionMatchesKnownCalendars`, `TestMissingYearDegradesToOnOrAfter` and
`TestSettlementNeverReadsTheTable`, plus the reading-panel tests. The last test guards the boundary:
this table changes reading-page estimates only, never settlement or evaluator wake scheduling.
An interval crossing a missing year must retain the weekday "on or after" wording rather than
claim an expected exchange date. In October 2026 the verified runtime table covers 2026 only;
2027 remains deliberately absent pending its annual notice and December review.

## Reader statistics explanation contract

The session-only groups response and its temporary admin alias may carry the following flattened
metadata. These fields are not a new machine API and must not be added to `/api/v1` routes merely
to expose the reader summary. External-all readers receive none of the fields in this section;
own readers receive only their scoped population. A missing field is not a measured zero.

| Field | Meaning |
| --- | --- |
| `summary.since`, `sets`, `accepted`, `stocks` | Earliest retained receipt date, retained row count, accepted row count, and distinct nonempty symbols in the requested range and population. Accepted includes repeat editions; it is not the statistical member count. |
| `summary.marks_by_phase` | Checkpoint obligations of classified members and pending-baseline candidates represented by the returned cells. Duplicate, rejected and other excluded rows do not create obligations. The h12 population includes standard horizons only. Mature cells contribute resolved and final counts; purged cells contribute nothing. |
| `summary.next_result_not_before`, `expected` | Earliest outstanding date across the returned cells. It can already be in the past when settlement is delayed. `expected` qualifies an exchange-schedule estimate, not a promised or observed result. Tied estimates retain the flag only if every tied date is qualified. |
| `rtypes`, `min_n` | Sorted covered report types and the minimum publishable sample size. |
| `limits` | `max_range_months`, `max_week_groups`, `official_slots`, `explore_slots`, `wait_sec`, `custom_per_min`, and `members_per_min`; the configured ceilings, not remaining budget. |
| `generated_at`, `ledger_start` | UTC response-generation instant and the declared ledger boundary. An empty ledger start means unset, not the beginning of retained history. |
| `capabilities` | `monthly`, `custom`, and `members` permission hints from the same decision used by route gates. They do not bypass fresh authorization, validation, rate limits or scorer capacity. |
| `evaluator.mode` | `0`, `dry`, or `1`; a paused evaluator is distinct from a failed checkpoint. |

Non-mature cells carry `pending_reasons`, `failed_cap_exceeded`, and, when known, `not_before` with
its optional `expected` qualifier. Phase counts include resolved and final entries to reconcile
the selected population; unclassifiable timing is counted as `unknown`. The failure-cap flag uses
the same final-refusal ratio and strict threshold as the existing maturity gate. Explanations are
request-time copies, not additional scoring and not entries in the mature-cell cache.

Retention exclusions apply before summary construction. A late row in a marked month cannot
resurrect that month in summary counts. These scalars describe retained data, not all-time totals;
the separate purge metadata must remain visible where authorized. Historical own attribution is
still pending the separately approved evidence-storage design, so this contract is not release
readiness for the current draft.

## Recording an incident

Record the time, the mode, the signal, the affected sets or benchmark codes, what the log said, and
the action taken. A settlement that was waited out (a vendor lag) is not an incident against the
portal; a `store_failed` or a forced manual edit is.
