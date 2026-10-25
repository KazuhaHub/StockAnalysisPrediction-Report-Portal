# ADR 0043 — Public quote request scheduling

**Status: Accepted.** 2026-10-25. Builds on ADR 0028, ADR 0031, ADR 0032, ADR 0037 and ADR 0041.

## Context

The display cache already coalesces identical loads, batches home-card snapshots and bounds
concurrency. None of those controls limits requests over time. Forecast evaluation also performs
uncached range reads and an adjusted-price check. Repeated dry evaluation previously exhausted a
public vendor's tolerance and disrupted display quotes from the same egress address.

No documented safe request frequency was found for the Tencent or Sina web endpoints. Operator
limits are therefore protective policy, not an assertion about a vendor's quota or availability.

## Decision

### Admit actual HTTP requests at the transport boundary

One process-wide scheduler wraps the production vendor transport. Redirects pass through it as
separate requests. Tencent's quote and history hosts share one source budget, as do Sina's hosts.
The scheduler also covers the compiled Yahoo adapter and known Eastmoney and Sohu hosts.
Unknown hosts, including local fixture servers, are not assigned a market-data policy.

Each source starts at 30 requests/minute with a background sublimit of 6/minute. Dispatches are
spaced; idle time does not accumulate burst credit. At most one background request is in flight
per source. Live snapshots and intraday views precede display history; background evaluation
cannot enter ahead of waiting readers. Already dispatched requests are not preempted.

Reader queues are bounded to 64 entries per source and five seconds of scheduler waiting, also
subject to the caller's own context. Background work is not retained as a waiting goroutine: it
receives a typed admission deferral and keeps its existing work record pending.

Limits are editable in the quotes management page and stored under `quote_vendor_rates` in the
existing settings store. No database shape or infrastructure configuration changes are required.
Each total is bounded to 2–60/minute and each background sublimit to 1–half its total. Corrupt
stored policies fall back to protective defaults. Saves validate policies before changing settings.

### Share source cooldown across display and evaluation

HTTP 403, 429 and 5xx responses cool the source for escalating intervals of one, five, fifteen
and thirty minutes. A valid longer `Retry-After`, in seconds or HTTP-date form, is respected.
Expiry admits only one recovery probe. Successful recovery clears the streak; an unrelated
success already in flight cannot clear a newly opened cooldown.

The existing ledger breaker remains a separate integrity-path control. Admission deferrals do
not count as vendor calls, vendor failures, mark attempts or terminal prediction refusals.

### Resume an unfinished evaluation without borrowing display cache

The two-minute evaluation slice remains bounded. Successful stock, benchmark and adjusted-price
reads are retained in memory only while an unfinished logical pass is waiting for admission or
has exhausted its slice deadline. Selection and guarded writes use current database rows on every
slice. Each group must still collect all its required stock evidence before applying the existing
stale-feed valve. An admission deferral during a corporate-action check leaves its plan unwritten.

Evidence is scoped by source-order policy, evaluator mode, Shanghai civil date and the completed
session boundary. Crossing those boundaries invalidates it. It is also bounded to six hours and
256 entries. The original vendor stamp and fetch instant are retained; resuming does not turn
an intraday body into a completed close or claim it was newly fetched. A completed pass discards
its temporary evidence. Restart discards it too; durable pending marks remain the recovery source.

Dry evaluation still runs only on an operator action. A deferred dry pass must be continued with
Evaluate now. On mode continues through the normal ticker. A waiting heartbeat explicitly states
that work is deferred rather than silently reporting a completed evaluation.

Sina shadow comparison needs two HTTP requests, snapshot then history. If history admission is
deferred or its per-pass HTTP budget is exhausted, retain only the independently parsed snapshot
and request history on the next selected comparison. This separate continuation holds at most
64 entries for six hours, scoped by evaluator mode, fallback mode, source order, Shanghai civil
date and completed-session boundary. Repeated deferrals preserve the original timestamp and
expiry. Completion or a non-admission error discards the entry; restart also discards it. Display
queries and deliberate probes never consume this continuation, and it grants no ledger authority.

The full untrimmed history must still agree with its snapshot before comparing the requested
window. If a resumed snapshot and newly fetched history describe different observations, discard
the partial evidence and record `incomplete`, without counting a vendor delivery failure. Fresh
two-request incoherence retains its existing error behavior. Each actual HTTP request remains
charged to its dispatching pass, including the successful first component of a deferred read.

### Expose protection and progress

The quotes panel shows configured limits, actual requests since process start and in the last
minute, network/HTTP failures, deferrals, reader queue depth, in-flight requests, oldest queue
wait and cooldown/probe status. These are process observations, not vendor guarantees or durable
accounting. The evaluator shows waiting state and the earliest known admission time.

Display efficiency is measured separately for live and historical request paths, with started and
finished requests, fresh cache hits, shared-load joins, completed logical loads, failures and stale
responses. Batch traffic has its own per-request deduplicated symbol counts; its cache ratio uses
symbols rather than HTTP calls. Cache clearing does not erase these process observations.

Fixed-size latency histograms report p95 bucket upper bounds. Display latency includes waiting and
failed requests; vendor latency covers dispatch through body close. Actual vendor failures identify
HTTP 403, HTTP 429, server errors and connection failures separately. Other HTTP errors remain in
the total. A manual observations refresh preserves unsaved settings; discarding those settings
preserves the latest observations. No unbounded per-symbol metrics or durable accounting is added.

## Limits and follow-up

This process cannot account for Dify, AKTools or another portal instance sharing its public IP.
Deployment-wide budgets still require coordination outside this scheduler. Strict reader priority
can delay background work under sustained foreground demand; existing backlog alerts remain useful.

Adding a display source never grants settlement authority. Sohu is the next independent daily
history candidate; production-egress probes and fixture validation must precede admission. BaoStock
requires separate coverage and deployment investigation. Persistent historical storage would be a
separate schema decision, not an incidental part of request scheduling.

## Verification

Tests cover foreground priority, background reserve, cancellation, bounded queues, no idle bursts,
sibling-host accounting, redirect accounting, shared cooldown and a single recovery probe. Evaluator
tests admit one upstream operation per slice, prove successful reads are not repeated, compare
resumed settlement with an unrestricted pass and verify dry waiting leaves the ledger unchanged.
Policy tests exercise admin validation, partial saves, corrupt settings and runtime policy loading.
