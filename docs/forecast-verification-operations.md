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

## Recording an incident

Record the time, the mode, the signal, the affected sets or benchmark codes, what the log said, and
the action taken. A settlement that was waited out (a vendor lag) is not an incident against the
portal; a `store_failed` or a forced manual edit is.

## Ledger-source diagnostics (P23 local implementation)

The administrator-only `/api/admin/quote` response includes each source's explicit `settled` and
`settledShadow` market arrays plus `cashDividends` and `reportsKind` evidence flags. Empty grants
are empty arrays, not null. A display-only wildcard never becomes a ledger grant.

Top-level `ledgerChain`, `ledgerPrimary` and `ledgerShadowPeer` summarize enabled declarations in
operator order, with cash-dividend writers first. These are configuration diagnostics across the
declared markets, not proof that every benchmark or stock is eligible: runtime additionally checks
the actual market/code and the source's symbol predicate. An absent primary or shadow peer is an
empty string. No shadow peer is advertised until it has an explicit enabled shadow grant.

`ledgerHealth` is a separate array of source health records, with UTC RFC3339 timestamps and an
empty string for an event that has not happened. It never overwrites the display health fields
on `sources`. Clearing the display cache clears neither health history. Ledger refusals and caller
cancellation do not count as vendor failures; three delivery/parser failures stop calls to that
source for the current evaluator pass, preserving the actual failure rather than misreporting an
absent configuration. A new pass resets its breaker, not the recorded diagnostic history.

The quote-source management page shows these declarations and the separate settlement history.
Unavailable telemetry is distinct from an empty process-local history; neither claims readiness.
A save removing the configured primary requires confirmation. Cancelling or leaving that
confirmation writes nothing, and repeated save clicks cannot create concurrent requests.

This contract describes the local P23 work, not a production rollout. Readiness item 6 and watchdog
integration remain separate work. Do not enable the evaluator on the strength of configuration
diagnostics alone.

Source-order saves additionally write `quote.order` with the effective `from` and `to` source
arrays, actor, organizational unit and request address. Equivalent spellings, repeated saves and
an explicit shipped order versus its reset form do not create duplicate transitions. Because the
order now controls durable settlement, this dedicated transition and its audit commit together;
an audit failure rolls the order back and returns an error before other submitted settings are
written. The existing general policy audit remains separate. This adds no database shape.
