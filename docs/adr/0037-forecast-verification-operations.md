# ADR 0037 — Forecast verification operations

**Status: Accepted.** 2026-10-20. Amends [ADR 0029](0029-prediction-scoring-deferred.md) and the
design document; depends on [ADR 0017](0017-storage-cleanup.md) (retention),
[ADR 0022](0022-external-user-ou-tenancy.md) (the OU tree, the owner token),
[ADR 0024](0024-report-versions.md) (read permission) and
[ADR 0035](0035-migrations-after-a-frozen-baseline.md) (the migration ledger).

ADR 0029 decided what the prediction ledger is and what may be counted. It described an evaluator
whose arithmetic was, in several places, not the arithmetic the design required. This record is the
repair: which definitions were wrong, what they are now, how the evaluator is operated as a service,
and which defects escaped the first implementation and why. It is the decision record for Release 1;
the build order and tests stay in the implementation document, cited as "plan §n".

## The definition repair

**S0 and dN follow the receipt time of day.** A report received before 15:00 Shanghai on a session
takes that session as S0; received at or after the close, or on a non-session, it takes the next
session. `dN` is the Nth session **after** S0, and the session whose close is recorded is the same
session. The first implementation had S0 slide on a later pass and counted `dN` from the wrong end,
which moved every mark's date without changing any status — the kind of error that makes a table
look settled while measuring a different window (plan F04/F05/F12).

**The baseline is terminal once written.** S0, the base date and the base price are stored on the
set when they first resolve and are never recomputed, even if a later vendor body revises the bar.
A NULL `s0_fen` on a terminal row means the return cannot be computed; it is never a reason to
recompute the price. Returns start at the stored base, never at the emitted quote; for a stock halted
on S0 that base is its last printed close before S0, while the benchmark leg still starts at S0.

**Not being due is not a refusal.** A mark whose due session has not appeared, or whose wake is in
the future, is scheduled. It carries no attempts and cannot become final through the ordinary path.
The one exception is the set whose START cannot resolve at all: after four attempts and ten sessions
past S0 the baseline is made terminal and every non-terminal mark is finalised with it, because the
set can never be scored and leaving it open would keep a cell pending for ever. The first implementation
refused every mark on its first tick before S0, which is why a one-shot heal exists at boot: those
rows carry an attempt that never happened. The heal matches only the pre-P1 signature and is
idempotent (plan F06/F07).

**Finality is only the result of failed attempts.** A mark becomes `refused_final` when it has at
least four mark-specific attempts **and** ten completed sessions have passed since its due session.
A source-class failure — the vendor did not answer, the calendar is missing, the body is not an
index — never increments attempts and can never make a mark final. A refusal that is still refused
after the clock has run out is final and is never retried again; that is what fixes a mature cell's
inputs (plan §5.1, V26).

**The settlement checks are the answer, not options.** A mark is resolved only from prices the
market printed: the newest bar must be for a completed session and must be vouched for by the
vendor's own timestamp; a stock halted on S0 starts from its last traded close inside a bounded
look-back; a missing benchmark bar makes the mark `unverifiable`; a zero-volume index bar or a stored
S0 the calendar does not reproduce is an index-wide source failure (`calendar_bad`, never final); a
stock bar missing from the index calendar (`calendar_mismatch`) or an anchor that moved
(`anchor_mismatch`) is mark-specific and does count toward finality. The daily-limit backstop for an
unreported corporate action is board-aware (10 % main board, 20 % ChiNext/STAR, 30 % Beijing) and is
computed in fen, not percent (plan F03/F09/F11/F13/F14).

**A corporate action is detected from the adjusted ratio.** The dividend element the unadjusted feed
carries on ex-dates names cash dividends; it does not mention bonus or rights issues, which are the
case that halves a price. One qfq fetch per set per tick compares the adjusted/unadjusted ratio at
the base and at the due session, exactly, in integers; a flagged mark is stored but excluded from
metrics and counted. A failed check never resolves a mark (plan F10, ADR 0028 §10's narrow
exception).

## Governance and the ledger's edges

**The ledger start is write-once.** A single instant, bounded by the oldest set in the ledger, marks
where counting begins. Sets made before it are `pre_live`: they settle and show on their cards, and
never count. A test run is classified at ingest from the producer's declared `run_mode` and lives in
its own bucket; it is never a symbol's member and never a duplicate. Deleting the start is not a
feature; a restore carries it forward and refuses a conflicting one.

**The benchmark is effective-dated and frozen.** A set is written with the benchmark that is active
for its cohort and keeps it for ever. The allowlist is a closed set (`sh000300`, `sh000905`,
`sh000852`, `sh000906`); a schedule is effective from the next cohort, is audited, and never
re-scores anything already on the ledger. A set frozen with a code the evaluator cannot fetch is
deferred, never scored against the current setting.

**Unrecorded reports are counted only since the ledger began.** A covered report with no set is a
producer or ledger fault, and the count is bounded by the ledger start so it cannot begin with years
of history. A store failure after the retry is an incident with its own audit row, webhook and
alert; the response still succeeds, because the report exists and a started run is never re-run.

## Operations

**The evaluator has three modes: off, dry, on.** Off is off in the sense an operator means it: one
settings read and nothing else. Dry runs the same planner and drops the plan at the one write door,
recording up to twenty would-be writes with the vendor, market and code that produced them. On
writes, but only for a benchmark whose dry run an admin has checked by hand; a benchmark without a
check is planned and listed in the heartbeat, never written. An older binary reads `dry` as off,
which is also the rollback story.

**Switching on is gated by what has been exercised, not by a calendar.** The readiness list requires
the build to contain the full evaluator, the ledger start to be set, a dry pass to have finished
without errors, a hand check for every benchmark the evaluator can select, and a test alert that
reached both channels in the last thirty days. The mode is refused — with the failing items — until
every one holds. The list is code, not advice.

**The loop is observable and the watchdog is independent.** Every pass writes a heartbeat: counters,
fetches by source, market and code, the newest bar per benchmark, the dry sample, and the codes
awaiting a gate. A separate goroutine with its own recovery reports `stalled` (heartbeat older than
ten minutes, at most hourly) and `lagging` (a due mark unsettled over six hours, the stale valve
tripped, or a benchmark calendar behind the exchange after the close, at most daily). A status-only
public route, `/healthz/forecast`, answers one boolean and never the heartbeat. `no_source` is not
emitted before the ledger chain exists, because there is nothing to check.

**Alerts go to Email and Slack.** The webhook event remains the extension surface, fired once by the
producer; the portal's own two channels deliver to every configured recipient and to a Slack webhook
whose URL is write-only and host-locked. A test alert writes the readiness stamp only when both
channels succeed, and changing a channel deletes the stamp. A failing alert never fails the operation
that produced it.

**One connection, one transaction.** A migration and a restore do all of their work — probes, DDL,
data, ledger rows, audits — through the transaction's own handle. On SQLite the pool is a single
connection, so a pool call made while that transaction holds it is a deadlock, not a wait. This is a
rule about every step and every restore path, not a preference.

## Restore

A restore replaces `meta`, so the ephemeral evaluator keys are reset rather than carried: the
heartbeat, the alert state, the dry gate and the alert verification. The mode becomes the lower of
the dump and the live database (`0` < `dry` < `1`), audited only on a real change, and a result of
`1` is capped at `dry` with `reason: restore_gates_empty`, because the gates were just reset and
mode 1 without them would plan every set and write none. A restore never switches the evaluator on.
Reader access is restored as the dump recorded it: an older dump that lacks the access column can
only narrow (every OU inherits, and the Default is normalised to none), while a dump taken after step
`0006` carries its OU scopes verbatim and may be wider than the live database's.

## The escapes

Five defects reached the first implementation despite the design. Naming their causes is the point:

- **F01 — the wrong instrument.** `sh000906` was routed to `sz000906`, and the evaluator would score
  a stock's close as the benchmark leg. A vendor body that does not call itself an index is now
  refused, and every frozen code is mapped through the allowlist.
- **F03 — the baseline was never checked.** `FinishForecastBaseline` wrote `match` unconditionally,
  so a set whose emitted quote was not a price the market printed still scored. The check now
  compares against the receipt session's printed range or the last completed close, and a mismatch
  excludes the set from the metrics.
- **F06 — not due was treated as failed.** A mark before its session was refused with an attempt,
  and enough ticks made it final. Scheduling and refusal are now different outcomes, and a source
  failure is not an attempt.
- **F10 — a bonus issue was invisible.** The dividend element is a cash-dividend element; a 10-for-10
  bonus halves the unadjusted close and looked like a crash. The qfq ratio detects it, one call per
  set per tick, and only when a due mark can actually resolve.
- **F14 — the limit backstop was wrong in both directions.** It recognised 20 % only on `300`/`688`
  prefixes, so ChiNext `301`, STAR `689` and Beijing used the 10 % main-board limit and their ordinary
  limit days were flagged as corporate actions; its basis-point bound also flagged a cheap stock's
  legal limit day. It is now board-aware (10/20/30 %) and compares fen-rounded limit prices with a
  one-fen tolerance.

The common cause is not carelessness about the arithmetic; it is that each rule was written against
a plausible model of the vendor rather than against a recorded body. The fix that prevents a repeat
is procedural: a refusal vocabulary with two classes, a terminal baseline, and tests that pin the
exchange's own numbers.

## Consequences

A settled mark is now expensive to change and cheap to trust: its price came from a completed
session, its start was written once, its failures are counted only when they are its own, and a
corporate action cannot masquerade as a return. The cost is more state (a baseline, a source, a
refusal class) and an operator who must exercise the dry run and both alert channels before the
switch is available. That is the trade ADR 0029's "what may be counted" demands: a number a reader
can cite is worth a gate an operator has to pass.

Rolling back across the access schema is restore-only; an older binary refuses a database that
records a step it does not know, which is a deliberate refusal rather than a corruption.

## Alternatives considered

- **Keep the evaluator off and publish only the contract.** Rejected: the ledger already ingests,
  and a ledger nobody settles is a growing liability.
- **Let the first pass settle history.** Rejected: the definition repair changes which session a
  mark means; forcing a one-shot re-settle would rewrite rows a reader may already have cited. The
  heal is bounded to the pre-P1 refusal signature instead, and new rules apply to new passes.
- **Trust the emitted quote as the baseline.** Rejected: it is an LLM-selected number, and a
  mismatch would be invisible in every return. The ADR keeps the check and excludes mismatches.
- **A second vendor as corroboration.** Deferred to ADR 0038's Tier 2; one vendor is a refusal that
  can never fire, and the v1 budget is for correctness, not corroboration.
