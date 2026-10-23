# Forecast verification v2 — owner decisions

Companion to [forecast-verification-v2-plan.md](forecast-verification-v2-plan.md) (the plan) and
[forecast-verification-v2-implementation.md](forecast-verification-v2-implementation.md) (the build
document). The plan numbers decisions V1–V26. This page records what the owner decided on
2026-10-18/19. Where a decision here differs from the plan's recommendation, **this page wins**.

## Already done in production (2026-10-19)

| # | Decision | Status |
|---|---|---|
| V1 | Evaluator off until Releases 1a/1b and the readiness checks are done | Done 08:27:49 CST: `forecast_auto` `1`→`0` (direct `meta` update; no audit row, because the running build does not audit benchmark saves). Verified: no evaluator log line after the change. |
| V2 | October stays on CSI 1000 | Done: `forecast_bench` `sh000300`→`sh000852`. No set was written under `sh000300`. |
| V3 | Test data | **Ledger start = `2026-10-19T00:00:00+08:00` (`2026-10-18T16:00:00Z`). Sets 1–6 are all integration runs.** Set 6 was first classified as production, then corrected: its Dify run has `triggered_from = debugging` (a console test run). Declaration and correction: `KazuhaHub/StockAnalysisPrediction-Dify` issue #1 and its first comment, both timestamped before the first S0 close. No row is deleted. |

## Decided

| # | Decision |
|---|---|
| Benchmark from 2026-11 | **CSI 800 (`sh000906`)** for new sets from the 2026-11 cohort, scheduled through the governed benchmark change (V8). Tencent serves its unadjusted daily bars (checked 2026-10-19). |
| V8 | Plan recommendation (a): allowlist, effective at the next cohort, explicit schedule/cancel, audited, one benchmark per cohort. |
| V21 | **Out of scope here.** Fixed separately and merged as #108 (`fix(auth): hold mandated-but-unenrolled cookie at the wall on /api/v1 reads`). The build document treats it as done and must not redo it. |
| V24 | Plan recommendation (a): `run_mode: "test"` on the wire, frozen at ingest; Dify sends it on test runs. |
| V26 | Plan recommendation (a): final only at ≥ 10 sessions past due **and** ≥ 4 mark-specific failed attempts; source outages never count. |
| V4, V5, V6, V7, V15 | Plan recommendations (fix the code to ADR 0029 §4 / design §9; halts per V6a(a), V6b(a); corporate actions per D8 with the qfq detection-only fallback; one shared hit predicate). |
| V20 | The implementer is authorised to make read-only vendor probes (Tencent, Sina, Yahoo) to capture fixtures. |
| V18 | Alerts go to **both Email and Slack**. SLO targets: plan §8 as written. |
| V12 | Plan recommendation (a): top-level `/forecast` with a nav item; `/manage/forecast` becomes evaluator operations. |
| V14 | Plan recommendation (a) for internal readers. External readers: see the access model below (no custom range). |
| V22 | A disclaimer on every forecast surface. **Wording approved 2026-10-19:** zh-CN 「研究验证用途，不构成投资建议」, en-US "For research verification only. Not investment advice." zh-TW comes from the normal OpenCC path and the human review (V17). No separate compliance review. |
| V9 | Plan recommendation (a): staged. Tier 1 follows the operator's source order with health counters, Tencent remains the only writer for scored marks, Sina in shadow. Tier 2 (fallback writes) is a later go/no-go. |
| V13 | Plan recommendation (a) for the stage switch (`off|card|full`, default `card`), membership badge, default set. **V13d is replaced by the access model below** (an OU at `none` gets no reading-page surfaces). |
| V16, V17, V23, V25 | Plan recommendations. **V17 (zh-TW), 2026-10-19:** zh-TW ships machine-generated (OpenCC s2twp) and flagged for review. The owner will find a reviewer later via a separate prompt; R2a is not blocked on it, and the release checklist lists it as "owner-arranged review". |
| Performance and adoption | `/api/stock` p95 budget **800 ms** for the card to full step (decided 2026-10-19). Weekly adoption target: none for now; it is measured for one month after R2a, then the owner sets it. |
| V19 | Later (Tier 2). |

## Changed from the plan: the access model (replaces V10, V11, V13d)

The owner chose a per-OU **three-way scope** that covers internal and external tenants in one model,
instead of the plan's boolean `forecast_view` with external tenants deferred to a Phase 2.

The OU model behaves like Google Workspace organizational units: a child OU inherits its parent's setting
by default and may override it, and nothing more complicated. The tree is read live, so an OU that moves
takes the new parent's inherited settings.

| Scope | Meaning |
|---|---|
| `all` | Statistics over every report in the ledger. |
| `own` | Statistics over reports attributed to the viewer's OU **and its descendant OUs**. |
| `none` | Fully unavailable: no record page, **and no forecast card, chip or overlay on the reading page**. |
| `NULL` | Inherit from the parent OU; the nearest set value wins. |

- **Defaults:** every OU is `none` (the Default OU stores `none`, not NULL). The owner sets
  `安禅内部` to `all`. Admins always see `all`.
- **Attribution for `own` is frozen at insert.** Each set records the OU its run was attributed to
  (from the signed owner token), and never recomputes it. Moving users between OUs or changing grants
  never moves historical sets. The one exception is deleting an OU (below).
- **Re-parenting follows the live tree** (decided 2026-10-19). When an OU moves, it takes the new
  parent's inherited settings, and its attributed sets count under the new ancestors' `own` (and leave the
  former ancestors'). No extra column; cached populations are keyed by the tree, so a tree change never
  reuses a number computed for a different population.
- **Deleting an OU that has attributed sets is allowed and the data is kept** (decided 2026-10-19). In the
  same transaction as the delete, the OU's sets are re-attributed to its parent OU
  (`UPDATE prediction_sets SET attributed_ou = <parent> WHERE attributed_ou = <deleted>`); a root OU's
  sets go to the Default OU. An audit row records the counts. This replaces the earlier idea of refusing
  the delete (409), and it removes the id-reuse risk because no set references a deleted id. It is the one
  sanctioned exception to "attribution frozen at insert".
- **The deleted OU's child OUs move to its parent** (decided 2026-10-19), in the same transaction, so they
  keep inheriting along the same line (Google Workspace model). Today they would become roots and silently
  inherit from the Default OU. A deleted root's children stay roots.
- **External (restricted) OUs may be set to `all`, with protections:**
  - official monthly view only (no custom range, which allows differencing);
  - the cell drawer lists only reports the viewer can read;
  - no site-wide volume (no report totals or unrecorded counts);
  - the n ≥ 30 gate as everywhere.
- `own` uses the same gates; small OUs will often show "not enough data", which is correct.
- **Membership badge for internal `own` readers: hidden** (decided 2026-10-19; the badge describes the `all`
  record, which they do not see).
- **A restricted account inside an unrestricted OU** takes its OU's scope with the external protections
  applied (decided 2026-10-19, as built).
- **External `all` readers do not see per-cell `n`, totals or member counts** (decided 2026-10-19; the
  conservative default, because a mature cell's counts reveal how many reports the ledger holds). External
  `own` readers keep `n`, since it is their own volume.
- This is a schema change (an OU column and a set column). The owner approved the shape on
  2026-10-19; the exact DDL is in the build document and needs no further sign-off unless it
  deviates from this page.

## Changed from the plan: attribution for every run

- **Today:** the portal injects the signed owner token (`_rp_owner_token`, `report_owner.go`
  `runInputs`) only into restricted OUs' runs, and no Dify workflow echoes it back, so no report is
  attributed.
- **In progress (parallel, Dify side):** 16 workflows (6 production entries including 1-6-4 V3.17
  and 3-5 V7.4.0, plus 10 ingesting children) are being wired to accept `_rp_owner_token` as an
  optional input and pass it to every ingest node as `owner_token`, and down to ingesting children.
  The ingest custom tool was re-registered with `owner_token`. Publish pending owner approval.
- **Decided:** once that Dify batch is published, the portal injects the owner token into **every**
  run, internal and external, so `own` works for internal OUs too. This amends ADR 0022 R1
  ("injected only for restricted OUs"). It ships as its own PR, after the Dify publish.
- **Also:** document `owner_token` in the portal's `internal/app/openapi.json` `IngestRequest`
  (it is accepted at runtime but undocumented), as a small early PR.

## P25 source-read and HTTP budgets (2026-10-22)

The owner delegated the counting-unit choice after the ambiguity was raised. A source read is
one complete candidate observation: Sina needs both snapshot and kline, so it normally costs two
HTTP requests. Preserve the plan's maximum of two comparisons of already-accepted primary bodies,
and independently cap HTTP request attempts at four per working pass. Redirects consume that
budget; they must not create an unbounded extra allowance. Retain both counters so two observations
cannot be mistaken for two network requests. This preserves the planned sample size while making
the actual upstream cost explicit. No retries beyond the budget or alternate ledger writers are
admitted. Execute shadow work after primary ledger processing with its own bounded deadline, so
candidate failure or slowness cannot consume primary settlement time.

## Own-scoped purge history (2026-10-22)

The owner refused disclosure of global purge months to own readers and approved
`prediction_purge_owners` with primary key `(rtype, cohort, attributed_ou)`, pre-delete transactional
snapshot, current-subtree reads, same-transaction OU reattribution with deduplication, and no guessed
legacy backfill. Existing supported databases require an explicit upgrade; startup must not
silently create the table. See ADR 0039 for the evidence and serialization contract. Schema approval
does not authorize production upgrade, and the own-history release hold stays until upgrade,
business-path wiring, dual-driver access tests and independent review are complete.
