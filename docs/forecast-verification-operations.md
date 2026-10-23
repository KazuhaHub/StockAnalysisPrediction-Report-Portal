# Forecast verification — runbook

**Scope.** Operating the prediction-ledger evaluator (ADR 0029, ADR 0037). Signals below describe
this source checkout, not a claim that it is deployed. Entries for later surfaces (reader access,
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
  Settlement follows the quotes panel's enabled source order and explicit ledger grants. Tencent
  remains the only Tier 1 writer; disabling it refuses settlement rather than using a display or
  shadow source. Restoring it permits normal retry without spending source-outage attempts.

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

Readiness item 6 (`ledger_chain`) now requires an explicitly enabled settlement chain with a
primary carrying both evidence capabilities. Removing it prevents switching from dry to mode 1.
If removed while running, the evaluator card warns instead of claiming health, and the watchdog
emits `no_source` at most daily through the existing webhook/email/Slack path. The watchdog checks
this even before a first heartbeat exists; off mode remains silent with only its mode read.

This contract describes local P23 work, not a production rollout. Per-instrument runtime checks,
the other readiness gates and deployment approval still apply. Do not enable the evaluator on
the strength of configuration diagnostics alone.

Source-order saves additionally write `quote.order` with the effective `from` and `to` source
arrays, actor, organizational unit and request address. Equivalent spellings, repeated saves and
an explicit shipped order versus its reset form do not create duplicate transitions. Because the
order now controls durable settlement, this dedicated transition and its audit commit together;
an audit failure rolls the order back and returns an error before other submitted settings are
written. The existing general policy audit remains separate. This adds no database shape.

## Offline source measurement (P24)

`quote-probe` is a standalone command. It never loads `config.yaml` or `RP_CONFIG`, opens a
database, changes source grants/order, or writes evaluator health, shadow state or ledger rows.
Only `capture` makes vendor GET requests. `compare` reads local evidence without a network client.
Do not use agreement here as a settlement grant or as approval for Tier 2.

Capture a bounded window into a **new** directory (its parent must already exist):

```sh
report-portal quote-probe capture --out ./probe-2026-10-19 \
  --from 2026-10-19 --to 2026-10-20 --sources tencent,sina,yahoo
```

The fixed panel is three indices (`sh:000852`, `sh:000906`, `sh:000300`) and twenty stocks:
`sh:600000,sh:600519,sh:600036,sh:601318,sh:601899,sh:688001,sh:688981,sh:689009,sz:000001,sz:000333,sz:000651,sz:002594,sz:002415,sz:300750,sz:300059,sz:301308,sz:301269,bj:830799,bj:835185,bj:920002`.
It is a measurement set, not an investment recommendation. Append operator-supplied suspended
codes with `--suspended sz:000004,bj:430047`; these are examples of syntax, **not claims that those
securities are currently suspended**. Each extra must be unique and not already in the fixed panel.
Suspension is not inferred from missing data. Any compiled-in source can be listed in `--sources`,
including Yahoo despite its lack of settlement admission. Unknown or duplicate sources are errors.

The output directory is private (0700), and files are private (0600), exclusively created, never
overwritten. The manifest declares the requested source/target matrix and window, fetch mode,
response URLs/status/timestamps, raw byte counts/hashes, parsed-response hashes and errors.
Raw files preserve the HTTP response body before GBK/parser conversion; they are not a packet
capture of compressed wire traffic. Each body is capped at 512 KiB; oversize and partial reads
remain failed evidence. A `count` fetch means the source was asked for up to 800 daily bars,
**not** that it supports a bounded range. Cancellation or local storage failure leaves partial
evidence for inspection. `complete` means all requested attempts finished, not that all succeeded.

Replay with two explicit anchor dates inside the captured window:

```sh
report-portal quote-probe compare --in ./probe-2026-10-19 \
  --primary tencent --anchor-start 2026-10-19 --anchor-end 2026-10-20 --record
```

Choose dates for which the reference is expected to supply bars. Both primary anchors must exist;
otherwise that instrument is unavailable. The same date may be used for both anchors to inspect
one session. The replay trims count/range series to the manifest window, then calls the shared
shadow comparator: exact integer-fen OHLC and date-set differences, including both anchors, with
deterministic date/field ordering. Volume is not part of the price comparison. Candidate missing
dates/anchors are mismatches; failed/truncated/empty evidence is unavailable, never agreement.
Identity/source, adjustment/cache flags and all supplied bars are validated before comparison.

JSON goes to stdout. `--record` additionally requires the entire fixed panel and creates
`comparison.json` in the capture directory exclusively. It can record unavailable/mismatching
results; it is not a success certificate. An existing report is never replaced. Without
`--record`, replay writes nothing and may inspect a smaller valid capture. Missing/tampered files,
incomplete matrices, unsupported versions, non-regular/symlink evidence, hash/byte-count failures
and invalid JSON fail the command. Reads have individual and aggregate bounds (8 MiB manifest,
2 MiB parsed response, 512 MiB total). The report includes the manifest SHA-256; hashes protect
against accidental corruption, not a malicious capture author. Preserve the entire directory
when sharing evidence. Neither replay nor recording follows URLs in the manifest.

Exit zero means the command produced a report/capture; inspect every result's `match`, `mismatch`
or `unavailable` status. A nonzero exit means command/input/storage/integrity failure. No live
measurement or production rollout is implied by the synthetic regression tests.

## Bounded Sina shadow comparison (P25)

The administrator settings modal on `/manage/forecast` offers **Off** and **Shadow comparison only**.
The existing source order still determines whether Sina is enabled. The shipped comparison grant is
Shanghai/Shenzhen only, using the bounded Sina adapter; it grants neither public display ranges nor
settlement. Sina and Yahoo still have no dividend/kind evidence flags or writer grant. Off is the
default. `POST /api/admin/forecast {"fallback":"on"}` is refused with `forecast_bad_fallback`;
effective off/shadow transitions commit with the dedicated `forecast.fallback` audit.

Shadow does not enable evaluation or trigger an idle pass. During a working evaluator pass it copies
at most two unique bodies already accepted from the qualified primary, retaining first/last anchors
and every intervening date/OHLC. Only after primary ledger processing completes does it read the
candidate, with a separate fifteen-second deadline and shared upstream concurrency. It rechecks mode,
enabled membership and primary identity before each sample. An aborted primary pass makes no shadow
calls. Candidate data never enters the settlement planner, cache or source-health counters.

There are two independent limits: two logical candidate reads and four HTTP request attempts per
pass. One Sina observation normally needs snapshot plus history; redirects also consume HTTP budget.
A fifth attempt is blocked before transport and recorded as `shadow_budget`, not a vendor failure.
A genuine fourth-request failure is still a failure. Waiting for a concurrency slot does not bill a
source call. Caller deadline/cancellation and typed refusals are neutral, not source faults.

Sina's newest untrimmed kline must agree with the snapshot date and close. History is never silently
replaced by a display-only degradation; an unproven window beyond the full 800-row tail is refused as
`window_not_covered`. The shared comparator checks integer-fen OHLC, the date union and both anchors.
A missing candidate anchor is a mismatch; unavailable evidence is not agreement. Comparisons are
opportunistic samples, not a representative study, independent exchange corroboration, a feed licence
or Tier 2 writer admission. Use the fixed-panel P24 process for deliberate replay measurements.

The administrator-only `GET /api/admin/forecast` includes `fallback` and `diagnostics`. The latter's
`status` is `empty`, `ready` or `unavailable`; empty/missing/damaged telemetry must never be read as
healthy zeros. `shadow` and `calls`, when readable, are one atomic snapshot of the existing-meta keys
`forecast_state_shadow` and `forecast_state_calls`. A damaged pair is retained and not overwritten.
No raw response body or vendor error text is stored here. Counts include logical primary/adjustment
reads, failures and typed refusals; `shadow_http_attempts` covers candidates only, not primary HTTP.

Retained state has up to fourteen UTC days ending at the latest observation and twenty recent results.
The explicit latest-record timestamp may be old when there is no work or evaluation is off; it is not
a live health certificate. A delayed observation cannot rewind retention or the timestamp. Each result
keeps total differences and at most thirty-two details; truncation never turns a mismatch into a match.
Counter-day dates are UTC; price-bar dates use the instrument timezone. Prices in the difference table
are integer fen. The card shows the active comparison mode separately from retained history.

For `mismatch`, preserve the result/window/anchors and capture deliberate evidence with P24; do not
rewrite settled rows or exclude disagreement. For `no_shadow`/`primary_changed`, inspect enabled source
order. For `shadow_budget`, inspect redirects/request counts rather than extending the budget. For
unavailable diagnostics, investigate retained state and storage logs; do not delete evidence to make
the card green. To stop new candidate calls, set comparison Off; the audit/history remain and primary
evaluation is unchanged. No database change or ledger repair is needed to revert this code. Deployment
and evaluator enablement still require their separate owner approvals and release gates.
