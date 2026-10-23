# ADR 0041 — Ledger-grade market data on the shared source policy

**Status: Accepted decision; local implementation in progress.** The owner approved Tier 1 in
V9 on 2026-10-19. This records that decision, not production enablement or a Tier 2 go/no-go.
Amends [ADR 0028](0028-live-quotes.md) §1, [ADR 0029](0029-prediction-scoring-deferred.md)
§6/D9 and [ADR 0032](0032-session-aware-live-quote-refresh.md) §6. Operations and finality remain
governed by [ADR 0037](0037-forecast-verification-operations.md).

The implementation plan originally reserved 0038 for this decision. Notifications now uses
0038 and external application connections uses 0040; this decision uses 0041 instead. No Apps
decision or branch is changed by that renumbering.

## Context

Display quotes and durable settlement have different failure costs. Display failover can replace
an answer on the next load; settlement fixes inputs to an immutable monthly record. A cached
14:57 body served at 15:31 must not become a completed close. Nor may an operator disable Tencent
for display while an invisible direct settlement call continues to use it.

The shared source order is therefore the policy boundary, but ordinary display capabilities are
not enough. A stock close needs corporate-action evidence and an index needs instrument identity.
The shipped display alternatives do not establish both. Tier 1 improves control and observability,
not settlement availability during a Tencent outage.

## Decisions

### 1. Explicit grants, with no display wildcard inheritance

`settled` admits daily bodies for ledger evaluation; `settled_shadow` admits comparison only.
Neither is a public display range. Both require explicit grants, including an actual supported
market and the source's symbol predicate. An empty display capability list grants neither.
`fetchUnder` and the display source-call path reject both before fetching, caching or joining
display single-flight work.

Grants and evidence flags are compiled declarations, not operator-editable capability claims.
The operator controls enabled membership and priority through the source order.
Sources declare `cashDividends` and `reportsKind` independently. Tencent has both and an explicit
`settled` grant for Shanghai and Shenzhen. Beijing history and overseas display support do not
implicitly admit settlement. Sina and Yahoo have neither evidence flag and no writer grant.
No shadow grant is advertised before its range fetcher/comparator is implemented in P25.

### 2. The operator's chain governs settlement, without fallback writes in Tier 1

For each requested instrument, `ledgerChain` restricts `quote_source_order` to explicit settlement
grants, removes duplicate names and stably puts cash-dividend sources first. `ledgerPrimary` is
the first member with both evidence flags. Unknown or disabled sources cannot enter the chain.
No qualified primary means `no_source`, not a direct Tencent call or a display fallback.

Only Tencent is admitted as a writer in Tier 1. Enabling Sina or Yahoo for charts cannot make
either write scored marks. A source outage pauses settlement; source-class refusals do not spend
mark-specific attempts or make a mark final. Existing session-completion, integer-fen, corporate-
action, dry-run and per-benchmark write gates remain in force.

`walkLedger` uses bounded windows validated by `quoteParseWindow`, outside display caching and
single-flight, holding one shared upstream slot for the whole walk. Calls within one evaluator
pass are serialized without sharing bodies. A source stops being called after three delivery or
parser failures in that pass. The next pass resets the breaker, not diagnostic history. Typed
ledger refusals and caller cancellation do not charge vendor health. The highest-priority called
source's failure is preserved; an exhausted breaker does not fabricate an empty configuration.

### 3. Single-vendor integrity is not corroboration

Stored Tier 1 closes come from one undocumented, unlicensed public endpoint. Parser identities,
price bounds, date/window checks and completion checks are integrity checks, not independent
corroboration, exchange certification or a licence to redistribute data. The owner accepts this
Tier 1 limit. Two public mirrors agreeing would still not prove adjustment conventions, halt
semantics or legal rights. A licensed feed is a separate procurement route, not an assertion that
a paid contract by itself satisfies every correctness gate.

The display cache remains memory-only. The ledger owns its existing schema, unadjusted basis,
session semantics and immutable outcomes. Correction remains governed by the ledger's existing
finality/restore rules, not by replacing a cached quote. A mark's `bench_code` is frozen benchmark
intent, not the identity of a supplier. Additional source/evidence provenance belongs to Tier 2;
this ADR adds no table, column or migration.

### 4. Administration and failure visibility ship with the policy

The admin quote route exposes explicit grants, both evidence flags, configured chain/primary,
comparison peer and separate process-local `ledgerHealth`. Configuration summaries describe
declared markets, not readiness of every instrument. Missing telemetry is not healthy telemetry.
Clearing display cache clears neither display nor ledger diagnostic history.

Effective order changes and the dedicated `quote.order` audit commit in one transaction; an audit
failure rolls the order back. Equivalent/no-op saves do not invent transitions. The UI confirms
before a save removes the configured primary; cancellation or leaving the confirmation writes
nothing. Repeated save clicks cannot create concurrent requests.

Readiness item 6, `ledger_chain`, requires a nonempty configured chain and a qualified primary.
It gates entry into mode 1 in addition to the other readiness items. Removing the primary during
operation does not rewrite the mode: the walker refuses, both management panels warn, and the
watchdog emits `no_source` at most daily through the existing alert path. Off mode remains silent.

### 5. Candidate measurements are not admissions

This table records the approved investigation scope, not fresh vendor measurements or guarantees.
P24 captures raw bodies and manifests without writing the database; P25 provides bounded shadow
comparison. Unmeasured capabilities must stay ungranted.

| Source | Present scope | Missing evidence before wider admission |
|---|---|---|
| Tencent | Tier 1 writer for explicit Shanghai/Shenzhen instruments; cash signal plus detection-only adjusted/unadjusted ratios | Undocumented public delivery, no independent corroboration or feed licence |
| Sina | Display source; count-based history capped at 800 bars; P25 comparison-only candidate | Window coverage, snapshot/history coherence and index identity measurements; no declared corporate-action evidence |
| Yahoo | Opt-in display source; candidate for A-share stock/index and event captures | A-share coverage/accuracy and index identity; dividend/split/adjusted-close evidence not admitted merely because an endpoint can return it |
| EastMoney | No ledger implementation; investigation candidate only | Deployment reachability, symbol mapping, bars, identity and corporate-action evidence all need measurement; no licence established here |
| Licensed feed | No implementation or procurement commitment | Contracted coverage, redistribution rights and measured conformance to the same integrity/evidence gates |

P25 may compare at most two already-accepted primary bodies per working tick. OHLC, date sets and
both anchors are compared to the fen. Results/call counts live in meta, never ledger tables;
`forecast_fallback` is `off` or `shadow`, never fallback writing. P24 replay uses that same
comparator. Neither work item is claimed complete by this ADR.

### 6. Tier 2 requires a separate decision and approved database shape

Persisted evidence must show at least 60 replayed sessions and 10 live shadow sessions, with zero
unexplained fen disagreements on closes and anchors. Benchmark admission additionally needs
identity evidence; stock admission needs measured corporate-action evidence. A later `corpEvents`
capability may form a separate evidence chain, pairing bars and evidence covering the same window.
The pairing must be recorded, not inferred from an enabled display source.

Each benchmark return must use two closes from one series; S0 comes only from the primary, and a
stock alternative must reproduce the frozen S0 anchor to the fen. No new exclusion class may hide
disagreement. The plan calls the future provenance step `0007`, subject to rechecking identifiers
and owner approval of its shape and upgrade path. No such step is authorized or implemented here.
Tier 2 remains outside this build's scope and no earlier than the implementation plan's 2027-01
boundary; elapsed time alone never grants admission.

## Verification and rollout

The P23 tests cover explicit grants/wildcard refusal, operator-order removal without vendor calls,
uncached runtime use, completion checks, isolated health, bounded breakers and cancellation,
refusal precedence, readiness refusal/restoration, daily no-source alerts, both delivery channels,
atomic order audits and UI confirmation/cleanup. Fixtures prove parser behavior against captured
bytes, not future vendor behavior. P24/P25 replay/live measurements and exact-head CI remain
separate evidence. Nothing here enables evaluation, deploys a build or publishes a workflow.

Rollback before deployment is a source revert. Operationally, disabling evaluation stops its
work; restoring a qualified source permits retry without spending outage attempts. Do not enable
evaluation merely because configuration diagnostics pass. Production rollout still requires the
owner's approval, backup and all release gates in the implementation plan.
