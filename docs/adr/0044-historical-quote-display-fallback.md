# ADR 0044 — Historical quote display fallback

**Status: Accepted.** 2026-10-25. Builds on ADR 0028, ADR 0031, ADR 0032 and ADR 0043.

## Context

An unavailable live source currently removes the entire quote panel. A recently successful
response can remain useful if its original observation timestamp and degraded state remain
visible. An independent historical source can also preserve the chart, but cannot replace a live
snapshot when its response has no observation timestamp.

Sohu's history endpoint returns dated OHLC rows, volume in lots and amount in ten-thousand CNY.
It echoes distinct `cn_` stock and `zs_` index namespaces. It does not provide a live observation
timestamp, a market-session state or settlement-grade corporate-action evidence. Its public
history page exposes the endpoint and units but publishes no request quota or availability promise.

## Decision

### Bound stale display reuse

The existing bounded display LRU retains an expired entry for at most fifteen additional minutes.
A failed refresh may return a shallow copy marked `stale`, preserving the original vendor stamp.
Failures never renew the entry's expiry or grace period. A successful refresh replaces it normally.
The response uses `private, no-store`, and the strip explicitly identifies the failed update.
Candidate trading hours use conservative outage retry advice rather than the expired session flag.
The evaluator continues to refuse all display-cache inputs.

### Separate history from live quotes

Sohu is compiled in and disabled by default. The existing source-order setting is its only enable
switch. Enabling it grants prior-day daily chart display only, for measured Shanghai/Shenzhen stock
symbol namespaces and four explicitly mapped benchmark indices. It grants no snapshot, intraday,
settlement or settlement-comparison capability. Beijing and unmeasured index namespaces are refused.

`GET /api/quote-history/{symbol}` is a separate session-authenticated endpoint. It requires a daily
range or bounded date window and selects historical-only sources. Cache and single-flight keys use
a separate namespace, so historical responses never warm live snapshots or home cards. Existing
`/api/quote/{symbol}` clients retain their live contract.

A live quote failure advertises `historyAvailable` only when an enabled source covers the requested
historical target and interval. The SPA follows that hint through the fixed same-origin history
route. If history also fails, the original live error and retry advice remain authoritative.
Successful historical fallback also uses the current live error's retry advice; an older cached
history response must not cause an immediate retry loop at an already passed opening time.

Historical responses carry `historyOnly` and `historyThrough`. Their snapshot timestamp remains
empty. The strip displays a historical-close label, civil date, currency and source, without live
movement or session indicators. Same-day and future rows are excluded even after local close:
a date alone cannot prove the current session is complete.

### Validate before displaying

The parser requires one successful envelope, exact namespace/code echo, the measured ten/eleven
column shapes, descending unique in-window dates, positive ordered OHLC, nonnegative volume/amount
and agreement between each row's change percentage and its prices. Prices use integer arithmetic.
Results are reversed to ascending order. Oversized windows are refused rather than silently
truncated; only count-based requests trim their calendar headroom to the requested count.

## Measurement and limits

Local-egress capture on 2026-10-25 used the existing `quote-probe` CLI for 2026-10-22 through
2026-10-23. All seventeen mainland stock samples and three benchmark samples in its fixed panel
passed parsing. Three Beijing samples were rejected before HTTP as unsupported. A separate
`600081` sample and the fourth benchmark (`000905`) were also measured. The production egress
still needs its own probe before enabling Sohu there. These observations do not establish uptime.

The small committed fixtures retain the response fields the parser consumes; aggregate statistics
are not part of the adapter contract. Sohu's rounded lot volumes can differ from another source's
exact share volumes. Agreement on a few closes is not a ledger-quality or licensing assessment.

No database shape, persistent history store, Dify configuration or production source setting changes.

## Verification

Tests cover stale expiry without renewal, original timestamp preservation, immutable shared values,
browser cache policy, outage retries, malformed historical rows, same-day exclusion, opt-in defaults,
capability and cache isolation, history-route refusal of intraday requests and clear UI provenance.
The frontend fallback tests cover the structured availability hint, unchanged windows, primary-error
preservation and recovery polling without stale cached scheduling advice.

Primary reference: [Sohu historical quotes](https://q.stock.sohu.com/cn/600081/lshq.shtml).
