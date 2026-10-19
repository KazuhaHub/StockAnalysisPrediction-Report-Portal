# Forecast verification v2 (预测验证 v2): implementation plan

| | |
|---|---|
| Status | Handoff document. Nothing in it is implemented. The owner's decisions it builds on are final; the DDL in §3 is the shape the owner approved on 2026-10-19 and needs no further sign-off unless a PR deviates from it. |
| Date | 2026-10-19 (Asia/Shanghai) |
| Baseline commit | `origin/main` at `af1d2cf6` (`fix(auth): hold mandated-but-unenrolled cookie at the wall on /api/v1 reads (#108)`). Every `file:line`, function, route, migration step and ADR number below was read at that commit. Files untouched since the plan's baseline (`c9b2b747`) keep the plan's line numbers; the ones that moved (`apiv1.go`, `routes.go`, `migrate_steps.go`, `security_policy.go`, `forecast_read_api.go`, `user_admin_api.go`, the locale bundles, `AppLayout.tsx`) are cited at their new lines. |
| Next free ids | **Migration step `0006`.** `(*Store).migrations()` at `migrate_steps.go:167-169` registers `0001`–`0005`; `0005` is already taken by `announcements.display` (`migrate_steps.go:426-449`). Tier 2 provenance is therefore `0007`, not the plan's `0006`. **ADR `0037`.** The newest file in `docs/adr` is `0036-account-sessions-and-login-history.md`; this plan uses `0037`, `0038`, `0039`. Re-check both lists at merge time. |
| Inputs | [forecast-verification-v2-decisions.md](forecast-verification-v2-decisions.md) (the owner's decisions), the code at the baseline, [forecast-verification-v2-plan.md](forecast-verification-v2-plan.md) (the plan: defects F01–F14, B1–B7, C1–C10, D1–D4, PRs P0–P37), ADR 0029, ADR 0022/0024/0017/0028/0035, [forecast-verification-design.md](forecast-verification-design.md), [forecast-verification-implementation.md](forecast-verification-implementation.md) (the v1 build document, whose shape this one follows), [forecast-verification-demo.html](forecast-verification-demo.html). |
| Precedence | **Decisions > code > plan.** Where the decisions page differs from the plan, the decisions win. Where the plan describes code that is not there, the code wins and this document says so (§0.3). Where this document and the plan differ, this document wins. If this document and the decisions page ever disagree, stop and ask the owner; do not pick one. |

**How to use this document.**
- Implement one PR at a time, in the order of §1. Each PR section (§4–§9) lists its files, signatures, DDL,
  algorithm, routes, UI states, i18n keys and tests.
- Write the tests first. Every test is named, and every guard test states which way it fails:
  **FC** (fail-closed: when the guard breaks, nothing is written or shown), **FO** (fail-open for the reader: a
  forecast failure never costs the reader the report), **BI** (pins both directions), **CH**
  (characterization: passes on `main` and pins today's behaviour so a change is deliberate).
- §10 is the single access matrix. §11 is the owner-token change for every run. §12–§14 are operations, rollout
  and ADRs. §15 is the handoff checklist. §16 records the owner's answers of 2026-10-19 and the one point still open.
- PR ids keep the plan's numbers (P1–P37) so the plan's analysis can be traced. Numbers the plan used for work
  that no longer exists are marked "dropped" in §1; new work is P38–P41.

---

## 0. Before you write any code

### 0.1 Rules binding the work

- **Tests first.** Every PR below lists its tests. They are written before the change and fail on `main`
  unless marked CH. Go tests sit next to their source (`internal/app/*_test.go`); vitest files sit next to the
  component.
- **Both drivers.** Every store change passes on SQLite (default) and on Postgres
  (`TEST_POSTGRES_DSN="postgres://portal:portal@localhost:5432/reports_test?sslmode=disable"`, which exists
  locally; clean up after the test). Each PR that adds a query adds its PG variant next to
  `forecast_pg_test.go` or `migration_pg_test.go`.
- **Schema only through `migrate_steps.go`, append-only (ADR 0035).** A new step is appended to
  `(*Store).migrations()`; a released step is never edited; nothing is added to `baseSchemaStmts()`; every step
  probes for existence (`e.columnExists`) so a retry is a no-op; every step declares its `columns`, `tables` and
  `indices` so `verifyMigrationProducts` (`migrate_steps.go:682`) checks them on every start and backups carry
  them; every step states its `restoreRule`. A step that back-fills data must leave `olderDumpOK` false
  (`migrate_steps.go:143-155`), so this plan never back-fills inside a step (see the boot normalisation in §3.2).
- **No `REAL`.** Prices are integer fen (`BIGINT`), ratios integer basis points (`INTEGER`).
- **The single-connection transaction rule.** SQLite runs one connection (`store.go:104`). Every write path in
  this plan computes and fetches outside any transaction, then applies in one short transaction that uses only
  the `*sql.Tx` handle (the `insertIDTx` pattern, `forecast_store.go:99`). No pool call (`s.query`,
  `s.queryRow`, `s.exec`, `GetSetting`) and no network I/O inside a transaction. Every such path has a SQLite
  test under a 2-second context deadline, so a stray pool call fails fast instead of hanging.
- **Routes only in `routes.go`,** through the class helpers (`s.public`, `s.session`, `s.admin`, `s.perm`,
  `s.bearer`, …, `routes.go:52-109`). `TestNothingRegistersARouteOutsideTheTable` and
  `TestEveryPatternIsRegisteredOnce` (`routes_test.go:31,59`) enforce it. Every new session route is asserted to
  be in `sessionRoutes()` and **not** on the enrolment allowlist `enrolmentPaths` (`security_policy.go:276-288`).
- **The machine surface and the SPA surface stay separate.** `/api/v1/*` is the machine API: its documented
  credential is a token (`authBearer`, `routes.go:170-185`). Its read handlers still admit a browser cookie
  through `canQuery` (`apiui.go:160-165`), and since #108 every one of them holds a mandated-but-unenrolled
  cookie at the enrolment wall through `v1CanRead` (`apiv1.go:418-440`) and scopes it through `queryScope`
  (`apiv1.go:404-406`). This plan relies on none of that: **the SPA never calls `/api/v1`**, no statistics route
  is ever added to `/api/v1`, and every v1 prediction read that a cookie reaches applies the same forecast scope
  as the session route (§10). Every `/api/v1` change is documented in `internal/app/openapi.json` (base
  description plus the `x-i18n` `en-US` and `zh-TW` blocks; `TestOpenAPIDocumentsEveryV1Route`,
  `openapi_test.go:153`).
- **i18n.** Every UI string goes through `t()`. Keys are flat (`"forecast.mark.refused"`) in
  `web/src/locales/zh-CN.json`, `zh-TW.json` and `en-US.json`; the parity test is
  `web/src/locales/locales.test.ts`. zh-TW is machine-generated (OpenCC s2twp) and needs a human reviewer.
  Server-written identifiers (refusal codes, OU names) are not translated per key; only closed Go enums get
  labels.
- **English for every developer-facing text**: code, comments, log lines, audit `detail` keys, error messages
  (`jsonErrorCode` messages included — `forecast_admin_api.go:46` currently returns a Chinese message and P4
  replaces it), commit messages, PR titles and bodies. Chinese appears only in `zh-*` locale values and in data
  values (the covered rtype `投资决策建议`, OU names such as `安禅内部`).
- **No AI attribution anywhere**: no `Co-Authored-By` trailer, no "generated with" line, no agent name in
  branches, commits, PR bodies, code comments or docs.
- **Branch and PR per change.** Branch names describe the work (`fix/forecast-bench-instrument`), never
  `main` directly. Each PR gets an adversarial review pass before merge.
- **No deploy, no release publish, no Dify publish, no production write without the owner.** Releases go
  through the CI-gated Release workflow as a Beta first. Every deploy is preceded by an ADR 0027 backup. The
  implementer may make read-only vendor probes (Tencent, Sina, Yahoo) to capture fixtures (V20); nothing else
  contacts a server.
- **Race lane.** Evaluator, access, alert and restore tests join the race lane: the `-run` regex at
  `.github/workflows/test.yml:175` gains `Forecast|Evaluator|Settle|Watchdog|Population|Restore|Scope|Alert`.

### 0.2 Decision status

All decisions are made. This table only says which PR each one drives. The decisions page is the source.

| Decision | Ruling (decisions page) | Drives |
|---|---|---|
| V1 evaluator off | Done in production 2026-10-19 08:27:49 CST (`forecast_auto` `1`→`0`, no audit row). Stays off until R1a, R1b and the readiness list (§1.4). | P9 readiness list |
| V2 October on CSI 1000 | Done: `forecast_bench` = `sh000852`. No set was written under `sh000300`. | P4 reconciliation (a no-op in production) |
| V3 test data | Ledger start = `2026-10-19T00:00:00+08:00` (`2026-10-18T16:00:00Z`). Sets 1–6 are integration runs. Declared in `KazuhaHub/StockAnalysisPrediction-Dify` issue #1 and its first comment, before the first S0 close. No row is deleted. | P5 |
| CSI 800 from 2026-11 | `sh000906` for new sets from the 2026-11 cohort, via V8. | P3 (allowlist), P4 (schedule) |
| V4, V5, V6, V7, V15 | Plan recommendations: code to ADR 0029 §4 / design §9.4; halts V6a(a), V6b(a); D8 with the qfq detection fallback; one shared hit predicate. | P6, P7, P8, P16a |
| V8 | Allowlist, effective at the next cohort, explicit schedule/cancel, audited, one benchmark per cohort. | P4 |
| V9 | Tier 1 (Tencent the only writer, Sina in shadow); Tier 2 later. | P23–P25, P32–P33 |
| V12 | Top-level `/forecast` with a nav item; `/manage/forecast` becomes evaluator operations. | P27 |
| V13 | Stage switch `off\|card\|full` (default `card`), membership badge, default set. **V13d replaced by the access model.** | P18, P20 |
| V14 | Exploratory tab for internal readers; external readers get no custom range. | P26, P27 |
| V16, V17, V23, V25 | Plan recommendations. | P30/P31, P15, P37, P18/P20 |
| V18 | Alerts to **both Email and Slack**; SLOs as in plan §8. | P9, P41, §12 |
| V19 | Later (Tier 2). | P32 |
| V20 | Implementer authorised to make read-only vendor probes. | P8, P7, P25 |
| V21 | Out of scope: fixed by #108. | none (P34 dropped) |
| V22 | Disclaimer on every forecast surface; the owner approves the copy; no separate compliance review. | P15, P20, P27 |
| V24 | `run_mode: "test"` on the wire, frozen at ingest. | P36 |
| V26 | Final only at ≥ 10 sessions past due **and** ≥ 4 mark-specific failed attempts; source outages never count. | P1 |
| Access model (replaces V10, V11, V13d) | Per-OU three-way scope `all`/`own`/`none`, `NULL` inherits (nearest wins), Default stores `none`, admins always `all`, `own` attribution frozen at insert (the one exception: deleting an OU re-attributes its sets to its parent, §6.4), external OUs may be `all` with protections. Settings inherit down the live OU tree as in Google Workspace (§10.1). | P38, P26, P27, §10 |
| Owner token for every run | After the Dify batch is published, inject `_rp_owner_token` into every run; amends ADR 0022 R1. | P39a, P39b, §11 |
| `owner_token` in OpenAPI | Document it in `IngestRequest`, as an early small PR. | P40 |

### 0.3 Where this document departs from the plan, and why

| # | Plan said | This document | Reason |
|---|---|---|---|
| 1 | Migration `0005` = `forecast_view`, `0006` = Tier 2 provenance | `0006` = the access model's two columns; `0007` = Tier 2 provenance | `0005` was taken by `announcements.display` after the plan was written (`migrate_steps.go:426-449`). |
| 2 | Boolean `user_groups.forecast_view`, a global `forecast_record_published` switch, external tenants denied (Phase 2) | `user_groups.forecast_scope TEXT` (`all`/`own`/`none`/NULL) plus `prediction_sets.attributed_ou BIGINT`; **no publish switch**; external tenants in scope now, with protections | Decisions page, "the access model". With every OU at `none` by default, the OU scope itself is the publish control; a second switch would be a second spelling of the same decision. |
| 3 | P34: `/api/v1` becomes token-only, e2e section C rewritten | Dropped. V21 is done by #108, which keeps the cookie on v1 reads but holds it at the enrolment wall (`v1CanRead`). This plan instead applies the forecast scope to any cookie caller of the two v1 prediction reads (P26). | Decisions page, V21. |
| 4 | Plan R1a = statements + governance; R1b = settlement correctness | R1a = **all** evaluator correctness (F01–F14) plus governance (B1, ledger start, V8 with CSI 800, V24, OpenAPI). R1a is cut as two Betas so the benchmark schedule is deployed before 2026-11-01 (§1.2). R1b = operations, the reading-page hotfix, the attribution schema and owner-token work. | The owner's instruction for this document. |
| 5 | Benchmark allowlist `sh000300`, `sh000905`, `sh000852` | Adds `sh000906` (CSI 800) | Decisions page: CSI 800 from the 2026-11 cohort. |
| 6 | Ledger start bounded by `populationBound` = 2026-10-19T07:00:00Z, value open | Value fixed at `2026-10-18T16:00:00Z`; the bound mechanism stays as a guard | Decisions page, V3. |
| 7 | Unrecorded floor from `meta['mig:0004']` | Floor captured from the **ledger start** instant when it is saved (same transaction) | The owner's instruction ("the B1 fix with the ledger-start mechanism"). One boundary, one meaning of "since the ledger began". |
| 8 | Alerts as webhook events only | Webhook events **plus** a forecast alert dispatcher that sends Email (existing `sendEmail`, `email.go:67`) and Slack (incoming-webhook text format) — P41 | V18 names Email and Slack. The portal's webhooks POST signed JSON (`webhook_run.go:33-77`), which Slack cannot render, and nothing in the code speaks Slack today. |
| 9 | App-bridge token: "accepted sets only" | The bridge token records its minting user (in memory) and prediction reads apply that user's scope; `none` gets 403 | Without it, an internal user at `none` could read every set through any installed app (`apiAppToken` mints `query` for every unrestricted user, `apps_api.go:121-141`; `appTokenEntry` carries no user, `apps_token.go:46-57`). |
| 10 | Owner token for internal runs: not covered | P30 writes the `g:<OU>` viewer principal only for an effectively restricted OU, and P39b depends on P30 instead of repeating the rule at its call site | `AddReportViewer` writes `g:<OU>` for any OU (`version.go:381-394`) and every restricted reader's principals include every OU up its chain, Default included (`report_owner.go:162-185`). Injecting tokens into internal runs without this would make internal reports readable by tenants under any `visibility=group` version — plan defect D4, amplified. |
| 11 | `fetchTencentRange` at `quote.go:1570`; `prefetch.ts:47`; unrecorded copy at `en-US.json:811`; v1 forecast routes at `routes.go:181-182` | `quote.go:1588`; `prefetch.ts:48`; `en-US.json:815`; `routes.go:184-185` | The code moved. |
| 12 | Owner-token injection is unconditional on the portal side | P39a first hides and strips reserved `_rp_*` inputs everywhere a target's input list is shown or accepted | Once the 16 Dify workflows declare `_rp_owner_token`, a target refresh (`dify_refresh.go:185-200`) copies it into the stored input list, which the batch form and CSV templates render as a user-facing field. Nothing filters `_rp_*` today (only `report_owner.go:61` mentions the prefix). |
| 13 | The one-shot heal also requires "not due under the fixed schedule" (checked in Go) | The heal matches the pre-P1 deferral signature only | By the R1a-β2 deploy the four accepted sets' d5 marks (due 2026-10-26) are due, so the check would leave 4 of the 20 carrying an attempt that never happened. Returning a due mark to `pending` only causes a fresh, recorded attempt (§5.1). |
| 14 | A benchmark schedule applies at once when the current cohort holds no in-population set | Always effective from the next cohort | Decisions page, V8: "effective at the next cohort". It also guarantees the owner's "CSI 800 from 2026-11" whatever October holds. |
| 15 | Membership and coverage over one global population | Each request computes over its population (`all`, or an `own` subtree by frozen attribution); D4 is applied inside it; the cache key and the `own` seed include the population key | The access model's `own` scope; computing D4 globally and then filtering would make an OU's numbers depend on, and reveal, other OUs' activity. |
| 16 | The dry-run gate is one item checked once | A recorded check per benchmark code in use; in mode `1` a set whose code has no check is planned, not written | CSI 800 starts in November, after the evaluator may already be on; its first sets must not be written unchecked. |

### 0.4 Review log

An adversarial review of this document produced the 18 findings below. Each is fixed in place; the last column says where. Row 19 records the owner's answers.

| # | Finding | Fix | Where |
|---|---|---|---|
| 1 | P39b cannot ride R1b: its preconditions can only hold after R1b | P39b is its own Beta after R1b: new release-table row, R1b row without it, lane diagram, §11.5 step 4, §13 row, §15 | §1.1, §1.3, §11.5, §13, §15 |
| 2 | §11.1 cites `reserved_inputs`, which P39a never records | P39a adds `ReservedInputs` to `difyTargetConfig`, written by the probe and the refresh before the input is hidden, round-tripped by the editor, with `TestProbeAndRefreshRecordReservedInputs` | §6.5, §11.1 |
| 3 | Between R2a and R2b, P10's retry error shows for `none` readers on a 403 | P26 adds the web side: `StockPage` skips both prediction requests when `me.forecast.scope === 'none'`, treats a 403 `forecast_unavailable` as render-nothing; vitest; web files in the P26 and P27 lists | §7.3, §7.4 |
| 4 | Index-wide faults were classed `calendar_mismatch`, which counts toward finality (breaks V26) | New source-class `calendar_bad` (never counts) for zero-volume index bars and a non-reproduced `s0_date`; `calendar_mismatch` stays for the per-stock subset check only; tests updated and added | §2, §4.3, §5.3 |
| 5 | Readiness item 4 could be empty (sets 1–6 are pre-live) so sets are planned and never written; the "first real checkpoints" line was wrong | Item 4 covers the active code plus every code frozen by any accepted set with a non-terminal mark, pre-live included; test added; the evaluator-on row now says which checkpoints settle first and that the first counted one is the d5 of the first set at or after the ledger start | §1.1, §1.4, §6.1 |
| 6 | P38 → P30 and P38 → P31 were missing | Dependencies stated in the lane diagram and in P30 and P31 | §1.3, §6.8, §6.9 |
| 7 | "`mintOwnerToken` unchanged" contradicts the empty-secret test | Stated as a change: an explicit empty-secret guard in `mintOwnerToken` and `ownerFromToken`, with tests that fail on `main` | §11.2, §11.4 |
| 8 | Test runs between the ledger start and the Dify `run_mode` change would count | Owner freeze on Dify console and debug runs in the R1a checklist; read-only Q6 queries (portal and Dify) before β1 ships; stop-and-report rule | §1.5, §4.6, §15 |
| 9 | External `all` could infer ledger volume from a mature cell's `n`, the drawer and the CSV | Conservative default: `n`, totals and member counts omitted for External `all` (an "n ≥ 30" badge instead); forbidden paths in the access-matrix test; new owner question | §7.3.3, §10, §16 item 8 |
| 10 | `routeRow.gate` does not exist | P26 adds the field and a `sessionGated` helper; the gated routes are registered through it; `TestForecastRoutesAreSessionRoutesBehindTheWall` and a new deny test iterate the table | §7.3.4 |
| 11 | The Email and Slack alert for `predictions.store_failed` was promised, not built | P41 adds the asynchronous ingest-path `forecastAlert` call (outside any transaction, own `recover`) and two tests; `forecastAlert` no longer fires the webhook itself | §6.2, §12.1 |
| 12 | P39b duplicated P30's group-principal rule | P39b depends on P30 (precondition 2b) and has no call-site `viewerOU` logic; the rule and its index-error test live in P30 | §0.3, §6.8, §11 |
| 13 | P38 said to move `ownerFromToken` before the insert; it already runs before it | Reworded: reuse the existing decode at `apiv1.go:316`, hoist its results, pass `ou` into `predSet` | §6.4 |
| 14 | The resolver comment "roots are not children of Default" is wrong | Corrected (an OU may be parented under Default); `DefChild` fixture, a variant with Default at `own`, and a test | §7.3.1, §10.2 |
| 15 | A dangling parent in `ix.chain` fails open for the external flag | `chain` and `restrictedEffective` return errors (`errDanglingOU`, `errCyclicOU`); the resolver answers `none` (`dangling_group`, `cyclic_chain`); P30 and P31 treat them as an index error; tests | §6.4, §7.3.1 |
| 16 | Readiness item 6 (Tencent in `quote_source_order`) gated nothing before P23 | Dropped from §1.4; added to P23's readiness; the `no_source` watchdog reason is stated as P23 onward | §1.4, §6.1, §8.8, §12 |
| 17 | The weekly-viewer counter stored a salt and `HMAC(salt, username)` hashes in `meta` | Salt and hashes in memory only; only the integer count is persisted; the restart upper-bound limit is stated; tests | §2, §7.3.5 |
| 18 | After a restore `forecast_auto` could stay `1` with empty gates, so nothing writes | The restore caps `1` at `dry` when `forecast_dry_gate` is empty, audited `restore_gates_empty`; test | §3.3, §12.3 |
| 19 | Owner answers 2026-10-19 | Google Workspace inheritance stated once (§10.1); live-tree re-parenting; OU delete re-attributes sets to the parent (root → Default) in one transaction with an audit row, replacing the 409 guard (P38, tests, error codes, ADR 0039 row, `TestNothingElseWritesAttributedOU` allow-list); membership badge hidden for internal `own`; disclaimer wording approved; `/api/stock` p95 800 ms, no adoption target yet; zh-TW ships machine-generated with an owner-arranged review; items 7 and 8 confirmed as built. §16 now lists the decisions and one open point. | §3.2, §6.4, §7.2, §7.3, §10.1, §12.2, §14, §15, §16 |

---

## 1. Releases and PR order

### 1.1 Release table

| Release | Beta target | PRs | What the owner sees | Gate to the next step |
|---|---|---|---|---|
| **R1a-β1** governance and data | Fri 2026-10-23; **must be deployed before 2026-11-01 00:00 CST** | P12, P40, P3, P5, P2, P36, P4 | The "1259 reports … ingest failed" line disappears. Ledger start settable, then set. Benchmark shown per cohort; CSI 800 scheduled from 2026-11. `run_mode` accepted. Benchmark saves audited. | Ledger start saved; CSI 800 schedule reads `effective_cohort: 2026-11` |
| **R1a-β2** evaluator correctness | Fri 2026-10-30 | P1, P6, P7, P8, P16a | Nothing visible (the evaluator is off); the heal count in the boot log and audit | ADR 0027 backup and Q3 = 20 recorded before deploy; P8 has its real qfq fixture |
| **R1b** operations, reading hotfix, attribution | Fri 2026-11-06 | P9, P41, P10, P38, P39a, P11, P30, P31 | Evaluator status card with dry mode; alert settings with a test button; waiting copy "on or after <date>" on the reading page; the card stops claiming "Fetch failed" and "not counted" | §1.4 readiness list, then the owner switches the evaluator on |
| **P39b** owner token for every run | Its own Beta after R1b; the date follows the Dify publish; never inside R1b | P39b | Internal runs are attributed, so `own` can work for internal OUs once R2a ships | Preconditions of §11.1: P38, P39a and P30 deployed, the target refresh done after P39a, the Dify batch published, the undeclared-input check answered |
| Evaluator on | Target ≤ Fri 2026-11-13 | — | The first checkpoints settled from history are those of sets 1–6 (integration runs made before the ledger start `2026-10-19T00:00:00+08:00`, so all `pre_live`: they settle and show on their cards but never count). The first counted checkpoint is the d5 of the first set made at or after the ledger start. | — |
| **R2a** access | Fri 2026-11-20 | P13, P15, P26, P27, P28, P22 | `/forecast` for members of OUs set to `all`/`own` (the owner sets `安禅内部` to `all`); OU editor field; OUs at `none` lose the card | One clean week |
| **R2b** parity, reading page, Tier 1 sources | Fri 2026-12-11 | P14, P16, P17, P18, P19, P20, P37, P23, P24, P25 | The demo look; chip, timeline dots and the run-page card once the stage is `full`; ledger chain on the quotes panel; Sina shadow | P18's `/api/stock` benchmark within budget; seeded-preview review signed off |
| **R3** cleanup | No earlier than two weeks after R2b is in production | P29 | — | — |
| Tier 2 | Separate go/no-go, no earlier than 2027-01 | P32 (migration `0007`), P33 | — | V19 entry criteria |

**Dropped plan PRs.** P0 (the declaration was made in Dify issue #1, decisions page V3); P21 (folded into P38);
P34 (V21 done by #108).

### 1.2 Why R1a is cut twice

Every covered ingest freezes the benchmark at insert (`apiv1.go:259`). If the governed schedule (P4) is not
deployed before the first November ingest, November freezes `sh000852`, and P4's rule (a schedule is always
effective from the next cohort, §4.7) then moves CSI 800 to December. So the
small, schema-free governance PRs are cut first (β1) and the larger evaluator rewrite second (β2). Both are R1a.

**Contingency if β1 is not deployed by 2026-10-31.** The owner sets the benchmark to `sh000906` through today's
settings modal at or after 2026-11-01 00:00 CST and before the first November covered ingest, and notes the time in
the operations log (today's build does not audit benchmark saves, `forecast_admin_api.go:42-50`). Today's
validator accepts the string (it misroutes it to `sz000906` internally, F01), but the frozen value is the string
`sh000906`, and P3 maps every frozen string through the allowlist, so nothing is lost. P4's boot reconciliation
then finds active = frozen and does nothing.

### 1.3 Dependencies and parallel lanes

```
R1a-β1:  P12 ─► P5 ─► P36 ─► P4          (statistics lane; serial: all touch forecastMembership)
                 └──► P2                 (needs P5's ledger start)
         P3 ───────────────► P4          (allowlist is P4's validator)
         P40                              (independent, docs + one test)
R1a-β2:  P3 ─► P1 ─► P6 ─► P7 ─► P8      (evaluator lane; serial: same functions)
         P12 + P4 ─► P16a                (statistics lane)
R1b:     P1 ─► P9 ─► P41                 (operations lane)
         P1 ─► P10                       (reading hotfix; needs predScheduleFor)
         P38 ─► P39a                     (attribution lane)
         P38 ─► P30 ─► P39b              (P30 and P31 use ouIndex.restrictedEffective and loadOUIndex, which P38 defines;
         P38 ─► P31                       P39b needs P39a and P30 deployed, and the Dify publish: it is its own Beta after R1b)
         P11                              (docs; after the R1a rulings are implemented)
R2a:     P15 ─► P26 ─► P27 ─► P28        (access lane; P26 needs P38, P16a)
         P13, P22                         (docs)
R2b:     P16 ─► P17, P18 ─► P19, P20     P14   P37   P23 ─► P24 ─► P25
R3:      P29 (after P27, P28 and two weeks of R2b in production)
```

Parallel at any time: one evaluator-lane PR, one statistics-lane PR, P40, the tenancy lane (after P38), the attribution
lane and the docs lane. Within a lane, PRs are serial because they edit the same functions; each is small enough to
review line by line.

### 1.4 Gate to switch the evaluator back on (`forecast_auto` = `1`)

`POST /api/admin/forecast {"mode":"1"}` returns 409 `forecast_not_ready` until every item holds, and the GET
response lists each item with its state (P9):

1. This binary contains R1a and R1b — **including P1**. True by construction once R1a-β2 is deployed;
   listed so the page says so, and so the P3→P1 gap is explicit: P3 (R1a-β1) records the right refusal
   codes for benchmark faults, but the source/mark-specific class — a source fault's code not counting
   toward the attempt limit or finality (V26) — arrives with P1. Until P1 is deployed a benchmark fault
   deferred by P3 would count as a mark-specific attempt and could become a final refusal, so **the
   evaluator stays off until P1 ships**. R1a-β1 ships P3 with the evaluator off; this item is the gate
   that keeps it off (`POST /api/admin/forecast {"mode":"1"}` refuses while R1a-β2 is not deployed).
2. `forecast_population_from` is set (P5).
3. The mode is currently `dry`, and the last dry pass finished without `errors` (heartbeat, P9).
4. A dry-run check is recorded for **each benchmark code the evaluator can select** (`forecast_dry_gate`, P9): the
   union of the active `forecast_bench` code and every code frozen by an accepted set that still has a non-terminal
   mark, **pre-live sets included** (the evaluator settles them like any other, §4.4). Counting only in-population
   sets would let the list be empty at the switch, because sets 1–6 are all pre-live, and every set would then be
   planned and never written (the per-set gate below). The check is one would-be d5 per code, with the stock and
   index closes on `base_date` and on the session, compared by hand against exchange-published closes, and the fetch
   recorded as `sh/000852` (or `sh/000906`). The evaluator in mode `1` still writes nothing for a set whose frozen
   code has no recorded check (it plans them as in dry mode), so the first November sets under `sh000906` wait for
   their own check.
5. Alert channels verified: a test alert delivered to **both** the configured Email recipients and the Slack
   webhook within the last 30 days (`forecast_alert_test_ok_at`, P41).

Item 6, "the ledger chain is not empty", is **not** in this list until P23 (R2b) builds the chain. Before P23 the
evaluator calls `fetchTencentRange` directly (`forecast_eval.go:211-216`), so a `quote_source_order` check would gate
nothing. P23 appends it as item 6 (§8.8).

### 1.5 Read-only checks before each deploy (owner)

```sql
-- Q1. The six sets and their order (re-run before R1a-β1; V3's cut must precede every real set).
SELECT id, symbol, state, made_at, report_id, emitter_app, emitter_version FROM prediction_sets ORDER BY id;
-- Q2. Covered ingests and distinct symbols per month (decides whether 2026-11-09 can mature a cell).
SELECT substr(rdate,1,7) AS month, COUNT(*) AS reports, COUNT(DISTINCT symbol) AS symbols
FROM reports WHERE rtype = '投资决策建议' GROUP BY 1 ORDER BY 1 DESC LIMIT 12;
-- Q3. The heal predicate, before R1a-β2. Expected: 20.
SELECT COUNT(*) FROM prediction_marks
WHERE status = 'refused' AND refusal = 'no_bar' AND session_date = '';
-- Q4. Before R2a: who would see what (accounts per OU and role, restricted flags, the tree).
SELECT COALESCE(g.name,'(Default)') AS ou, u.role,
       SUM(CASE WHEN COALESCE(u.restricted,0)=1 THEN 1 ELSE 0 END) AS account_restricted, COUNT(*) AS accounts
FROM users u LEFT JOIN user_groups g ON g.id = u.group_id
WHERE COALESCE(u.active,1)=1 GROUP BY 1,2 ORDER BY 1,2;
SELECT id, name, parent_id, COALESCE(restricted,0) AS restricted, is_default FROM user_groups ORDER BY id;
-- Q5. Before P30 and P39b: viewer rows that an unrestricted OU principal already carries (D4 exposure today).
SELECT principal, COUNT(*) FROM report_viewers WHERE principal LIKE 'g:%' GROUP BY 1 ORDER BY 2 DESC;
-- Q6. Before R1a-β1 ships, and again just before the ledger start is saved: every set made at or after the
-- ledger start, so a test run is found before it counts. Portal database, read-only:
SELECT ps.id, ps.symbol, ps.made_at, ps.report_id, ps.emitter_app, ps.emitter_version, r.run_id AS report_run_id
FROM prediction_sets ps LEFT JOIN reports r ON r.id = ps.report_id
WHERE ps.made_at >= '2026-10-18T16:00:00Z' ORDER BY ps.id;
-- Q6b. Portal database, read-only: the Dify workflow run ids the portal's own batch started in that window.
SELECT run_id, finished_at FROM batch_items WHERE finished_at >= '2026-10-18T16:00:00Z' AND run_id <> '';
-- Q6c. Dify database, read-only (confirm the table and column names against the deployed Dify version first):
-- every run of the covered workflows since the ledger start with how it was triggered.
SELECT id, app_id, triggered_from, created_at FROM workflow_runs
WHERE created_at >= '2026-10-18 16:00:00' AND app_id IN (<the covered workflows, 1-6-4 among them>)
ORDER BY created_at;
```

Q6 reading: a set whose Dify run is absent from Q6b, or whose `triggered_from` in Q6c is not the API trigger (a
console or debug run), is a test run made after the ledger start. If any is found, **stop and report it to the owner
before R1a-β1 ships**: the ledger start is the owner's decision (V3) and moving it needs a new declaration on the
Dify repository's issue #1. The portal-side `run_mode` bucket (P36) cannot classify it, because the Dify
`run_mode` producer change is a later PR (§4.6); the owner-side freeze of §15 closes that gap.

---

## 2. Shared vocabulary (Go)

New constants live with their owners; the closed enums go in `internal/app/forecast_vocab.go` and are mirrored in
`openapi.json` wherever they appear on v1 (`TestOpenAPIPredictionEnumsMatchTheGoVocabulary`,
`openapi_test.go:240`, gains a row per enum).

```go
// forecast_vocab.go additions

// Set states. predStateTest joins the four existing states (forecast_vocab.go:93-100).
const predStateTest = "test" // run_mode "test": stored and listed apart, no marks, never scored (P36)

// Run modes on the wire (P36). Absent means production.
var predRunModes = []string{"production", "test"}

// Membership statuses (P16a/P16). Order is display order.
const (
	memMember          = "member"
	memDuplicate       = "duplicate"
	memDegraded        = "degraded"
	memMismatch        = "mismatch"
	memPendingBaseline = "pending_baseline"
	memPreLive         = "pre_live"      // made before forecast_population_from (P5)
	memOffBenchmark    = "off_benchmark" // frozen code differs from the cohort's (P4)
)

// Refusal codes, in two classes (P1/P7). Codes are shown on manager surfaces only; readers see phases.
type refusalClass int

const (
	refusalMarkSpecific refusalClass = iota // counts toward attempts and finality (V26)
	refusalSource                           // never counts; the mark wakes again soon
)

var predRefusalClass = map[string]refusalClass{
	"no_bar": refusalMarkSpecific, "stale_bar": refusalMarkSpecific, "unverifiable": refusalMarkSpecific,
	"anchor_mismatch": refusalMarkSpecific,
	"calendar_mismatch": refusalMarkSpecific, // ONE stock's dates are not a subset of the index's: about this mark's series
	"bad_window":        refusalMarkSpecific, "no_base": refusalMarkSpecific,
	"fetch_failed": refusalSource, "no_bench": refusalSource, "not_index": refusalSource,
	"no_source": refusalSource, "no_corp_evidence": refusalSource,
	"calendar_lag": refusalSource, // the stale valve tripped: vendor-wide lag, nothing counts (P7)
	// calendar_bad is an index-wide fault (a zero-volume index bar, a stored s0_date the index calendar does not
	// reproduce). It is about the source, not about any one mark, so it can never count toward finality (V26).
	"calendar_bad": refusalSource,
}

// A refusal becomes final only when BOTH hold (V26).
const (
	predFinalSessionsPastDue = 10
	predFinalMinAttempts     = 4
)

// Reader phases derived on read (P10 interim, P16 full).
var predPhases = []string{"waiting_s0", "waiting", "settling", "retrying", "overdue", "resolved", "final"}

// Hit classes (P16a).
const (dirAgree = "agree"; dirDisagree = "disagree"; dirFlat = "flat"; dirUntested = "untested")
const predHitUpsideBp = 500 // |upside| >= 5 % is tested

// Evaluator modes (P9). An older binary treats anything but "1" as off (forecast_eval.go:70).
const (forecastModeOff = "0"; forecastModeDry = "dry"; forecastModeOn = "1")

// Base check gains its terminal value (P1). An older binary reads it as a member with final refusals.
const baseRefusedFinal = "refused_final"
```

```go
// forecast_access.go (P38 defines the type, P26 the resolver)
type forecastScope string

const (
	scopeAll  forecastScope = "all"  // statistics over every report in the ledger
	scopeOwn  forecastScope = "own"  // statistics over sets attributed to the viewer's OU and its descendants
	scopeNone forecastScope = "none" // fully unavailable: no record, no card, chip or overlay
)
```

**Meta keys** (all in `meta`; restore rules in §3.3):

| Key | Values | PR |
|---|---|---|
| `forecast_auto` (existing) | `0`, `dry`, `1` | P9 |
| `forecast_bench` (existing), `forecast_bench_next`, `forecast_bench_next_from` | allowlist code; `YYYY-MM` | P4 |
| `forecast_population_from` | RFC3339 UTC, write-once | P5 |
| `forecast_unrecorded_floor_id`, `forecast_unrecorded_floor_at`, `forecast_unrecorded_floor_method` | int; RFC3339; `audit` or `max_id` | P2 |
| `forecast_state_repair_premature` | the heal's count, written once | P1 |
| `forecast_eval_state` | heartbeat JSON | P9 |
| `forecast_dry_gate` | JSON `{code: {at, by, note}}` | P9 |
| `forecast_alert_state` | de-duplication JSON | P9/P41 |
| `forecast_alert_email_to`, `forecast_alert_slack_url`, `forecast_alert_test_ok_at` | comma list; secret URL (write-only); RFC3339 | P41 |
| `forecast_slots_official`, `forecast_slots_explore`, `forecast_wait_sec`, `forecast_custom_per_min`, `forecast_members_per_min` | bounded ints | P26 |
| `forecast_viewers_<ISO week>` | integer count only (no usernames, no salt, no hashes) | P26 |
| `forecast_reader` | `off`, `card`, `full` (default `card`) | P18 |
| `forecast_fallback`, `forecast_state_shadow`, `forecast_state_calls` | `off`/`shadow`; JSON | P23/P25 |

**Audit actions** (added to `auditVocabulary`, `audit.go:396-412`; each written only on a real change):
`forecast.bench`, `forecast.population`, `forecast.repair`, `forecast.eval.run`, `forecast.dry_gate`,
`forecast.alert`, `forecast.alert.test`, `forecast.reader`, `forecast.fallback`, `quote.order`. `forecast.auto`
exists (`audit.go:129`). OU scope changes ride on the existing `group.change` (`audit.go:93`) with a
`forecast_scope` `{from,to}` detail.

**Webhook events** (added to `availableEvents()`, `webhook_api.go:15-17`, each with an i18n label in all three
locales and its payload documented in the webhook reference): `predictions.store_failed` (P2),
`forecast.evaluator.unhealthy` (P9).

**Error codes** (English messages; `jsonErrorCode` on SPA/admin routes, `v1err` on v1):
`forecast_bad_bench`, `forecast_bad_mode`, `forecast_population_set`, `forecast_population_too_late`,
`forecast_population_future`, `forecast_population_month_mature`, `forecast_population_conflict` (restore), `forecast_not_ready`, `forecast_eval_off`,
`forecast_eval_running`, `forecast_bad_gate`, `forecast_bad_alert_email`, `forecast_bad_slack_url`,
`forecast_alert_failed`, `forecast_bad_scope`, `forecast_unavailable`, `forecast_custom_not_allowed`,
`forecast_busy` (503 + `Retry-After`), `forecast_rate_limited` (429),
`forecast_bad_reader`, `forecast_bad_fallback`, `invalid_param`.

---

## 3. Schema

### 3.1 Release 1a changes no schema

Its new state lives in `meta` rows and in new values of existing TEXT columns (`prediction_sets.state = 'test'`,
`prediction_sets.base_check = 'refused_final'`, new `prediction_marks.refusal` codes). An older binary ignores the
meta keys and reads the new values with the same meaning or drops them from a display count (§3.3). A code
rollback of R1a is safe for the database; R1a's one-shot writes (the heal, the floor, the reconciliation) are not
undone and are correct under the old binary.

### 3.2 Step `0006`: the access model's two columns (P38, Release 1b)

This is the DDL the owner approved on 2026-10-19: one OU column and one set column, in one step, so the acceptance
contract changes once.

```go
// migration0006 adds the forecast verification access model (ADR 0039).
//
// user_groups.forecast_scope is who may see the forecast record from this OU: 'all', 'own' or 'none',
// with NULL meaning "inherit from the parent OU" (the nearest set value wins), the same tri-state the
// enrolment columns of 0001 use. The column is nullable with no default, so applying the step changes
// nobody's access: every OU inherits, and the Default OU is normalised to 'none' at boot rather than
// here, because a step that writes data cannot let an older dump in (restoreRule.olderDumpOK).
//
// prediction_sets.attributed_ou is the OU the run that produced a set was attributed to, read from the
// signed owner token at insert and never written again. NULL means unattributed (no token, an invalid
// token, or a set written before this step) — such a set belongs to no OU's 'own' population and is
// counted only under 'all'. It is never serialised to any client.
func migration0006() migration {
	cols := []colRef{
		{table: "user_groups", name: "forecast_scope"},
		{table: "prediction_sets", name: "attributed_ou"},
	}
	return migration{
		id:      "0006",
		columns: cols,
		indices: []string{"idx_pred_sets_ou"},
		restore: restoreRule{olderDumpOK: true},
		up: func(e migExec) error {
			if !e.columnExists("user_groups", "forecast_scope") {
				if _, err := e.exec(`ALTER TABLE user_groups ADD COLUMN forecast_scope TEXT`); err != nil {
					return fmt.Errorf("add user_groups.forecast_scope: %w", err)
				}
			}
			if !e.columnExists("prediction_sets", "attributed_ou") {
				if _, err := e.exec(`ALTER TABLE prediction_sets ADD COLUMN attributed_ou BIGINT`); err != nil {
					return fmt.Errorf("add prediction_sets.attributed_ou: %w", err)
				}
			}
			// The 'own' population filters on attributed_ou within one rtype and a made_on range, the
			// same shape idx_pred_sets_group serves for 'all'.
			_, err := e.exec(`CREATE INDEX IF NOT EXISTS idx_pred_sets_ou
				ON prediction_sets(attributed_ou, rtype, made_on)`)
			return err
		},
	}
}

func (s *Store) migrations() []migration {
	return []migration{migration0001(), migration0002(), migration0003(), migration0004(), migration0005(),
		migration0006()}
}
```

- **No CHECK constraint.** The value set is closed in Go (`scopeAll|scopeOwn|scopeNone`); an unknown stored value
  resolves as `none` (fail-closed), which an older or newer build can never widen.
- **Boot normalisation, not a back-fill.** `(*Store).EnsureDefaultForecastScope()` runs at every boot right after
  `EnsureDefaultGroup()` (`store.go:279`) and at the end of every restore:
  `UPDATE user_groups SET forecast_scope='none' WHERE is_default=1 AND forecast_scope IS NULL`. Idempotent. The
  resolver additionally reads a NULL on the Default OU as `none`, so the rule holds even between the step and the
  normalisation.
- **OU id reuse.** `DeleteUserGroup` (`user_admin.go:169-186`) already clears principal rows because "a deleted
  one can come back around". A deleted OU's id is never reassigned by `AUTOINCREMENT` or a Postgres identity, but
  a restore resets identities to the restored maximum (`backup.go:593`), so an id above the dump's maximum can be
  reassigned. Decided 2026-10-19: deleting an OU is allowed and the data is kept. P38 re-attributes the OU's sets to
  its parent OU (a root OU's sets go to the Default OU) in the same transaction as the delete, so no set ever
  references a deleted id and the reuse risk does not arise (§6.4).
- **Rollback.** A binary older than step `0006` refuses to start against a database that recorded it
  (`refuseUnknownLedgerRows`, `migrate_steps.go:488-520`). Rolling back R1b or later is therefore restore-only:
  take the ADR 0027 backup before deploying R1b.

### 3.3 Restore rules

`meta` is replaced on restore (ADR 0035 §7). `restoreFrom` (`backup.go:333`) reads the live values of the keys
below **before** it replaces any row, and `finishRestore` (`backup.go:426`) applies the rules on the restore's own
transaction, through the `*sql.Tx` handle only. `TestRestoreRulesForForecastKeys` (P5 introduces it; each later PR
adds its rows) asserts every row of this table on both drivers. FC.

| Kind | Name | On restore |
|---|---|---|
| meta | `forecast_auto` | The lower of dump and live (`0` < `dry` < `1`), audited `forecast.auto` with actor `system`. A restore never switches the evaluator on. **Then, after the gate rows below are applied:** if the mode is `1` and `forecast_dry_gate` is empty or absent (always, because the gates are reset), the mode is capped at `dry` and audited `forecast.auto` with actor `system` and `{reason: "restore_gates_empty"}`. Without the cap the mode stays `1`, every set is planned and none is written (§6.1), and nothing says why. |
| meta | `forecast_bench`, `_next`, `_next_from` | From the dump; boot reconciliation (P4) re-runs and is idempotent. |
| meta | `forecast_population_from` | Carried from live when the dump lacks it. Both present and different → the restore is refused with `forecast_population_conflict`. |
| meta | `forecast_unrecorded_floor_*` | From the dump when present; otherwise re-derived from the restored audit rows and the restored ledger start (P2). |
| meta | `forecast_state_repair_premature` | From the dump; the heal re-runs on its predicate and is idempotent. |
| meta | `forecast_eval_state`, `forecast_alert_state` | Reset (deleted). |
| meta | `forecast_dry_gate`, `forecast_alert_test_ok_at` | Reset. A restored database must be re-checked before mode `1`. |
| meta | `forecast_alert_email_to`, `forecast_alert_slack_url` | From the dump. |
| meta | `forecast_reader` | The lower of dump and live (`off` < `card` < `full`). |
| meta | compute limits, `forecast_fallback`, shadow and call statistics, weekly viewer counts | From the dump. |
| column | `user_groups.forecast_scope` | From the dump; an older dump lacks it (`olderDumpOK`), every OU inherits, and `EnsureDefaultForecastScope` sets Default to `none`. So a restore can only narrow access, never widen it. |
| column | `prediction_sets.attributed_ou` | From the dump; an older dump lacks it and every set reads as unattributed (true for that dump). |
| enum value | `prediction_sets.state = 'test'` | An older binary drops it from coverage (`forecastMembership`, `forecast_groups.go:115-157`); a display gap only. |
| enum value | `prediction_sets.base_check = 'refused_final'` | An older binary reads a member with final refusals: the same meaning. |
| semantics | `prediction_marks.attempts` | Counts mark-specific failures only (V26). |

`TestRestoreCapsModeWhenGatesAreEmpty` (P9, in the same file as the other restore rows): a dump and a live database
both at `1` restore to `dry` with one `forecast.auto` audit row carrying `restore_gates_empty`; dump `0` with live
`1` restores to `0` and writes no second row; `dry` stays `dry`. FC (a restore never leaves `1` with no gate).

### 3.4 Step `0007` (Tier 2 only; shape decided at the Tier 2 go, V19)

The plan's §5 "0006" shape, renumbered: four `TEXT NOT NULL DEFAULT ''` columns
(`prediction_sets.base_source`, `prediction_sets.base_logic`, `prediction_marks.bench_source`,
`prediction_marks.logic`) and one `BIGINT` (`prediction_sets.bench_base_fen`), `olderDumpOK: true`. Not built in
this plan; P32 owns it.

---

## 4. Release 1a-β1: governance and data (schema-free)

### 4.1 P12 `test/forecast-golden-cells` — golden snapshots (CH)

**Goal.** Freeze today's statistics output so every later PR proves it changed nothing it did not mean to.

**Files.** `internal/app/forecast_golden_test.go` (new); `internal/app/testdata/forecast/golden_fixture.json`,
`golden_cells.json`, `golden_members.json` (new).

**Fixture.** One covered rtype, three months, built through `InsertPredictionSet` and the real mark writers (no
hand-written rows): duplicates; a degraded first set; a pending baseline; a `mismatch` first set; zero excess;
upside at 4.99 %, 5.00 % and 5.01 %; a mark with nil `bench_ret_bp`; a corporate-action mark; a halted mark; two
`policy_version`s; one non-standard horizon (no `h12`). It holds **no** `pre_live`, `test` or `off_benchmark` sets,
so P5, P36 and P4 leave it untouched and bring their own tests.

**Tests.**
- `TestForecastGoldenCells` (CH): `computeForecastCell` for every group × checkpoint plus slices and coverage,
  serialised with sorted keys, equals `golden_cells.json` byte for byte. Every later PR keeps it byte-identical.
  A regeneration (`go test -run TestForecastGoldenCells -update-forecast-golden`) is allowed only in its own commit
  whose message explains each changed byte (P16a's integer predicate is the one expected case, and only if the
  float rule disagreed on a fixture member).
- `TestForecastGoldenMembers` (CH): the members response for every mature cell equals `golden_members.json`. P16a
  regenerates it on purpose, in its own reviewed commit.

**Merge before** P5, P36, P4, P16a.

### 4.2 P40 `docs/openapi-owner-token` — document `owner_token` on ingest

**Goal.** `owner_token` is accepted by `v1Ingest` (`apiv1.go:167`) and verified (`apiv1.go:316`) but absent from
`IngestRequest` (its properties are `symbol, date, subtype, rtype, kind, name, title, source, run_id, time, body_md,
body_html, tracking, predictions, version`). Document it, and make the omission impossible to repeat.

**Files.** `internal/app/apiv1.go` (lift the anonymous request struct at `apiv1.go:165-195` into a named type
`v1IngestRequest` with identical fields and tags; no behaviour change), `internal/app/openapi.json`,
`internal/app/openapi_test.go`.

**OpenAPI property** (`components.schemas.IngestRequest.properties.owner_token`):

```json
"owner_token": {
  "type": "string",
  "description": "已签名的 OU 归属令牌。门户在运行输入 `_rp_owner_token` 中注入，工作流原样回传到这里。缺失、过期或签名无效时，报告与预测集都不归属任何 OU；门户从不根据普通客户端字段决定归属。"
}
```

with `x-i18n.en-US.properties.owner_token.description`: "Signed OU-attribution token. The portal injects it into the
run inputs as `_rp_owner_token`; the workflow passes it back here unchanged. When it is missing, expired or
invalid, neither the report nor its prediction set is attributed to any OU; attribution is never taken from a plain
client field." and the matching `zh-TW` text (OpenCC s2twp of the zh-CN text, reviewed).

**Tests.**
- `TestOpenAPIIngestRequestDocumentsEveryWireField`: reflects the `json` tags of `v1IngestRequest`'s top-level
  fields and asserts each is a key of `IngestRequest.properties`, and the reverse. Fails on `main`
  (`owner_token` missing). FC (on contract drift).
- `TestOpenAPILocalizedEndpoint` (existing, `openapi_test.go:118`) stays green with the new `x-i18n` entries.

**Rollback.** Revert; no runtime effect.

### 4.3 P3 `fix/forecast-bench-instrument` — fetch each set's frozen benchmark as the right instrument (F01, F02)

**Goal.** `benchMarketCode` (`forecast_eval.go:630-661`) strips the prefix, asks `marketPrefix` (`names.go:224-242`),
which sends every `0xxxxx` code to Shenzhen, so `sh000852` is fetched as `sz000852`, a stock. And one calendar per
tick is fetched for `sets[0].BenchCode`, falling back to the *current setting* (`forecast_eval.go:124,139-141`).
Replace both with an explicit allowlist and one calendar per distinct frozen code.

**Files.** `internal/app/forecast_bench.go` (new), `internal/app/forecast_eval.go`, `internal/app/forecast_admin_api.go`
(validator only; P4 does the rest), `internal/app/testdata/quote/tencent_fqkline_range_sh000852.json`,
`tencent_fqkline_range_sh000906.json`, `tencent_fqkline_range_sh000300.json`, `tencent_fqkline_range_sh000905.json`
(new, captured by read-only probe under V20, in the existing fixture format).

```go
// forecast_bench.go

// benchSpec is one allowed benchmark. Market and Sym are explicit: an index code is never routed through
// marketPrefix, which files every 0xxxxx code under Shenzhen.
type benchSpec struct {
	Code   string // the stored, prefixed form: "sh000852"
	Market string // "sh"
	Sym    string // "000852"
}

// forecastBenchAllowlist is the closed list of benchmarks a set may be frozen with (V8). Each entry has a
// committed Tencent fixture under testdata/quote.
var forecastBenchAllowlist = map[string]benchSpec{
	"sh000300": {"sh000300", "sh", "000300"}, // CSI 300
	"sh000905": {"sh000905", "sh", "000905"}, // CSI 500
	"sh000852": {"sh000852", "sh", "000852"}, // CSI 1000 (the October cohort)
	"sh000906": {"sh000906", "sh", "000906"}, // CSI 800 (from the 2026-11 cohort)
}

func benchSpecFor(code string) (benchSpec, bool) // exact key match after TrimSpace; no prefix inference
func forecastBenchCodes() []string             // sorted, for the admin GET and the Select
```

`benchMarketCode` is deleted. Its two callers are the settings save (`forecast_admin_api.go:44`) and
`indexCalendar` (`forecast_eval.go:143`).

**Algorithm (tick).**

```
sets := due sets (today's selection until P1)
byCode := group sets by set.BenchCode, preserving order
for code, group := range byCode (in order of first appearance):
    spec, ok := benchSpecFor(code)
    if !ok:                                  // empty or unknown frozen code
        defer group's due marks with "no_bench" (source class, P1 semantics; before P1: today's defer)
        continue                             // the current setting is NEVER read here
    from := min(made_on in group) - predReferenceLagDays
    cal, err := rangeBars(spec.Market, spec.Sym, from, now)
    if err: defer group with "fetch_failed" (source)
    if cal.resp.Kind != "index": defer group with "not_index" (source); continue
    if any bar in cal has volume <= 0: defer group with "calendar_bad" (source class: an index-wide fault;
                                       P3 records the code through today's defer — the "attempts unchanged,
                                       never toward finality" semantics are P1's, §5.1); continue
    for set in group: evaluateSet(set, cal)
```

`QuoteResp.Kind` is the vendor's own instrument kind (`quote.go:102-105`). The stub in tests dispatches on
`(market, code)`, not code alone, so a misrouted fetch cannot be served the index fixture.

**Tests.**
- `TestBenchAllowlistMapsExplicitly`: each allowlist code maps to `("sh", "<6 digits>")`; `000852`, `sz000852`,
  `600519`, `sh600519` and `""` are refused by `benchSpecFor` **and** by `POST /api/admin/forecast {"bench":…}`
  (400 `forecast_bad_bench`). FC
- `TestEvaluatorFetchesEachFrozenBenchmark`: sets frozen with `sh000852` and `sh000906` in one tick cause exactly
  two calendar fetches, both with market `sh`, and each mark's `bench_ret_bp` is computed from its own index
  fixture. FC
- `TestBenchmarkBodyMustBeAnIndex`: a stub that answers a stock body for `(sh,000852)` gives `not_index` and writes
  nothing to `prediction_marks` except the deferral. FC
- `TestZeroVolumeIndexBarDefersTheGroup`: an index body with one zero-volume bar defers every due mark of
  the group with `calendar_bad` and writes nothing else. The "attempts stay 0 and the mark never becomes
  final" half of the property belongs to P1 (R1a-β2), which introduces the source/mark-specific refusal
  class; it is asserted there by `TestSourceFailuresAreNotMarkSpecific` (§5.3). Between P3's deploy and
  P1's, the evaluator stays off (the readiness list of §1.4 includes P1), so no mark is ever written with
  today's default defer. FC
- `TestEmptyBenchCodeNeverReadsTheSetting`: a set with `bench_code=''` defers with `no_bench` while
  `forecast_bench` is set to a valid code; the counting store sees no read of `forecast_bench`. FC
- Existing `TestForecastSettingsRoundTrip` (`forecast_eval_test.go:467`) keeps passing for `sh000905`/`sh000300`
  (both allowed).

**Rollback.** Revert; the evaluator is off.

### 4.4 P5 `feat/forecast-ledger-start` — the write-once ledger start (V3)

**Goal.** Take sets 1–6 out of the record without touching a row: a write-once, bounded, audited boundary that
changes membership only. Value (decisions page V3): `2026-10-18T16:00:00Z`.

**Files.** `internal/app/forecast_population.go` (new), `internal/app/forecast_admin_api.go`,
`internal/app/forecast_groups.go`, `internal/app/forecast_groups_store.go`, `internal/app/backup.go` (restore rule),
`web/src/pages/manage/ForecastLedgerStart.tsx` (new), `web/src/pages/manage/ForecastPage.tsx`,
`web/src/api/types.ts`, the three locale bundles.

```go
// forecast_population.go
const setForecastPopulationFrom = "forecast_population_from"

// populationBound is the latest instant a ledger start may name: the first 15:00 Asia/Shanghai on a
// weekday strictly after the earliest made_at in the ledger. It needs no exchange calendar; a holiday
// only moves the true first S0 close later, so the bound is conservative.
func populationBound(earliestMadeAt time.Time) time.Time

func (s *Store) ForecastPopulationFrom() (time.Time, bool)               // unset → false
func (s *Store) forecastPopulationCandidates(bound time.Time) ([]populationCandidate, error)
type populationCandidate struct { ID int64; Symbol, Rtype, MadeAt, State string; Orphan bool } // never the payload

// SaveForecastPopulationFrom validates, then writes the key (and, from P2, the unrecorded floor) in one
// transaction through the tx handle only. Returns a stable code on refusal.
func (s *Store) SaveForecastPopulationFrom(from, now time.Time) (excluded []int64, byState map[string]int, code string, err error)
```

**Validation order** (each refusal writes nothing):
1. already set → 409 `forecast_population_set`;
2. `from` after `now` → 400 `forecast_population_future`;
3. `from` after `populationBound(MIN(made_at))` → 409 `forecast_population_too_late` (production bound
   `2026-10-19T07:00:00Z`; the decided value is 15 hours inside it);
4. any official cell of the first cohort is mature → 409 `forecast_population_month_mature`;
5. malformed → 400 `invalid_param`.

**Membership.** In `forecastMembership` (`forecast_groups.go:115-157`), before the D4 earliest-set rule, every set
with `made_at < from` goes to the new `cov.PreLive` count and never enters `bySymbol`. So a pre-live set can never
make a later real set a `duplicate` (plan B4). `pre_live` is a statistics classification only: such sets are
selected, settled, counted in admin counts and purged like any other, and the reading page shows their real phases
with "not counted: before the ledger start".

`forecastCoverage` gains `PreLive int \`json:"pre_live"\``.

**Routes.**

| Route | Class | Change |
|---|---|---|
| `GET /api/admin/forecast` | admin (`routes.go:310`) | adds `population: {from, bound, candidates[]}` |
| `POST /api/admin/forecast` | admin (`routes.go:311`) | accepts `population_from` (RFC3339); audited `forecast.population` with `{from, bound, excluded_set_ids, counts_by_state}`; codes above |

**UI.** `ForecastLedgerStart` inside the settings area of `/manage/forecast`:
- *unset*: an instant input shown in Asia/Shanghai with its UTC rendering beside it, the bound, the candidate list
  (id, symbol, made at, state, orphan), and a Save button that opens a confirmation requiring the admin to type the
  instant again ("permanent; survives restores");
- *set*: read-only value, "set by X at Y" with a link to `AuditPage` filtered on `forecast.population`;
- *loading*: skeleton; *error*: inline `Alert` with retry; a 409 shows its code's message.

i18n: `forecast.population.title`, `.help`, `.bound`, `.candidates`, `.confirm_title`, `.confirm_body`,
`.type_again`, `.saved`, `.set_by`, `.not_counted_pre_live`, and `err.forecast_population_set`, `_too_late`,
`_future`, `_month_mature`.

**Tests.**
- `TestLedgerStartIsBoundedWriteOnceAndAudited`: past the bound → 409; second save → 409; a mature first-cohort
  month → 409; a future instant → 400; on success the audit row lists the excluded ids and counts by state. FC
- `TestPopulationBoundIsTheFirstWeekdayCloseAfterTheEarliestSet`: the production shape (earliest set Sunday
  2026-10-18 evening CST) gives `2026-10-19T07:00:00Z`; a Friday 16:00 set gives Monday 07:00Z. BI
- `TestPreLiveSetsSettleButNeverCount`: a pre-live set is selected and settled by the evaluator and purged by
  retention like any other, is never a member, never makes a later set a duplicate, and coverage adds up. BI
- `TestLedgerStartSurvivesAnOlderRestore` (first rows of `TestRestoreRulesForForecastKeys`): carried from live when
  the dump lacks it; refused with `forecast_population_conflict` when both differ. FC
- `TestLedgerStartSaveNeverCallsThePoolInsideTheTransaction`: SQLite, 2-second deadline. FC
- Golden snapshots: `golden_cells.json` gains a `pre_live` line in every coverage block (the new
  counter the doc's own `forecastCoverage` definition adds); the values are otherwise byte-identical,
  and `golden_members.json` / `golden_fixture.json` are unchanged. The regeneration is in P5's own
  commit.
- vitest `ForecastLedgerStart.test.tsx`: unset → confirmation requires the typed value; set → read-only; a 409 shows
  the mapped message; loading and error states.
- e2e section G: preview, save, second save 409.

**Rollout.** After the R1a-β1 deploy, the admin saves `2026-10-18T16:00:00Z` (it must equal the declaration in Dify
issue #1). **Rollback.** Revert; an older build ignores the key and counts sets 1–6 again (the old population, not
wrong data).

### 4.5 P2 `fix/forecast-unrecorded-floor` — count unrecorded reports only since the ledger began (B1)

**Goal.** `countForecastUnrecorded` (`forecast_groups_store.go:128-140`, function at :134) counts every covered report with no set,
with no lower bound, and the copy asserts a cause ("the ingest failed", `en-US.json:815`). Production shows 1259
pre-ledger reports as failures.

**Files.** `internal/app/forecast_population.go`, `internal/app/forecast_groups_store.go`,
`internal/app/forecast_admin_api.go`, `internal/app/apiv1.go` (webhook at the store-failed branch,
`apiv1.go:345-352`), `internal/app/webhook_api.go`, `internal/app/webhook_run.go`, `internal/app/backup.go`,
`web/src/pages/manage/ForecastPage.tsx`, locales.

**The floor** is captured inside `SaveForecastPopulationFrom`'s transaction (P5), from the ledger start `from`:

```
rows := tx: SELECT target_id, detail FROM audit_log WHERE action = 'report.ingest' AND at >= :from
ids  := [atoi(target_id) for rows if detail.created == true]
if len(ids) > 0: floor_id, method = min(ids) - 1, "audit"
else:            floor_id, method = tx: SELECT COALESCE(MAX(id),0) FROM reports, "max_id"
write forecast_unrecorded_floor_id, _at (= from), _method   (tx handle only)
```

The `report.ingest` audit row carries `created` (`apiv1.go:300-302`). Captured once, so later audit retention
cannot move it. After a restore: from the dump when present, else re-derived the same way.

```go
// countForecastUnrecorded counts covered reports created since the ledger start that have no set row.
// ok=false while the ledger start is unset: the page then says "set the ledger start" rather than a number.
func (s *Store) countForecastUnrecorded(rtype string) (n int, since string, ok bool, err error)
// SQL: COUNT(*) FROM reports r WHERE r.rtype=? AND r.id > :floor AND r.version <> :manual
//      AND NOT EXISTS (SELECT 1 FROM prediction_sets ps WHERE ps.report_id = r.id)
func (s *Store) countPredictionsStoreFailed(since time.Time) (int, error) // audit action predictions.store_failed
```

**Webhook.** `EventPredictionsStoreFailed = "predictions.store_failed"`, fired next to the existing audit write at
the second-failure branch with `{report_id, symbol, rtype, made_at, error}`; added to `availableEvents()`.

**Routes.** `GET /api/admin/forecast` gains `unrecorded: [{rtype, n, since, store_failed_30d}]` per covered rtype.
`GET /api/admin/forecast/groups` keeps its `unrecorded` integer (the bounded count, 0 while unset) until P29; the
reader routes of P26 never carry it.

**Copy.** `forecast.cov.unrecorded` is **removed** (the old key asserted a cause) and replaced by
`forecast.cov.unrecorded_since`: zh-CN 「{{since}} 以来有 {{n}} 份覆盖类型的报告没有预测记录 · 近 30 天写入失败：{{m}}」, en-US
"{{n}} covered reports since {{since}} have no prediction row · store failures (30 days): {{m}}", linking to
`/manage/audit?action=predictions.store_failed`. A count of 0 with 0 failures hides the line.

**Tests.**
- `TestUnrecordedFloorFromIngestAudit`: 1259 pre-ledger covered reports count 0; a covered report created after the
  ledger start with no set counts 1; a manual-version report counts 0; deleting the audit rows afterwards does not
  move the floor; a restore without the floor keys re-derives it. BI
- `TestUnrecordedIsHiddenUntilTheLedgerStartIsSet`. FC (no false number)
- `TestStoreFailedEmitsWebhook` and `availableEvents()` contains it; the label exists in all three locales.
- vitest: 0 and 0 hides the line; otherwise the link carries the audit filter.

**Rollback.** Revert; the meta keys are ignored.

### 4.6 P36 `feat/forecast-test-runs` — classify test runs at ingest (V24)

**Goal.** A future Dify test run must never need another cut. The producer says so on the wire, the classifier
freezes it at insert, and the set is stored apart, gets no marks and is never scored.

**Files.** `internal/app/forecast_ingest.go` (`predPayloadWire` gains `RunMode string \`json:"run_mode"\``),
`internal/app/forecast_vocab.go`, `internal/app/forecast_groups.go`, `internal/app/openapi.json`
(`PredictionPayload.properties.run_mode`, `enum: ["production","test"]`, three languages),
`internal/app/openapi_test.go` (enum row), `web/src/components/PredictionPanel.tsx`, locales.

**Classification** (pure, inside `classifyPredictions`, `forecast_ingest.go:211`):

```
run_mode := strings.TrimSpace(wire.RunMode)
if run_mode != "" && run_mode not in predRunModes: reject("bad_enum", path "run_mode")
... existing validation ...
if the set would be accepted or backtest and run_mode == "test": set.State = predStateTest
// a test payload that fails validation is still "rejected", so a broken test run is visible as such
```

`InsertPredictionSet` already creates marks only for `accepted` (`forecast_store.go:80-89`), so a test set has none.
`predStatusValue` returns `"test"`, so the ingest response says what happened. `forecastCoverage` gains
`Test int \`json:"test"\``; `forecastMembership` counts it and never puts it in `bySymbol`.

**Order of shipping.** The portal side ships in R1a-β1, so the bucket and the label exist before any producer sends
the value. The Dify-side change (the test app sends `run_mode: "test"`; the projection stops hard-coding
`emitter.app`, which the plan located at `ops/forecast-projection/projection.py:222` in the Dify repository — not
present in the local checkout, so re-locate it) is a separate PR in the Dify repository, merged after R1a-β1 is
deployed, published only with the owner's approval.

**The gap between the ledger start and that publish.** From `2026-10-19T00:00:00+08:00` until the Dify `run_mode`
change is published, nothing on the wire says "test", so a console or debug run of a covered workflow (1-6-4) would
count as production. Two owner-side controls close the gap, both in §15's R1a-β1 checklist: an operational freeze (no
Dify console or debug runs of covered workflows until the `run_mode` PR is published), and the read-only Q6 queries
of §1.5, run before R1a-β1 ships and again before the ledger start is saved, which list every set made since the
ledger start with how its Dify run was triggered. A test run found is reported to the owner; it is not reclassified
in code (state is frozen at insert).

**Tests.**
- `TestTestRunIsFrozenAtIngest`: `run_mode:"test"` with a valid payload gives `state=test` and zero marks; absent
  or `"production"` gives today's classification; `"Test"` or `"dry"` gives `rejected` with `bad_enum` at
  `run_mode`; no code path updates `state` after insert (source scan for `UPDATE prediction_sets SET state`). FC
- `TestTestSetsHaveTheirOwnBucket`: coverage adds up exactly with test sets present. BI
- `TestOpenAPIPredictionEnumsMatchTheGoVocabulary` gains `run_mode`.
- Golden snapshots unchanged.
- vitest: a test set renders the label `forecast.panel.test_run` and no checkpoint table.

**Rollback.** Revert; an older binary drops `test` sets from every bucket (a display gap; release note says so).

### 4.7 P4 `feat/forecast-benchmark-governance` — effective-dated, allowlisted, audited benchmarks (V8, B2, B3)

**Goal.** One benchmark per cohort by construction; a change takes effect from the next cohort; every save is
audited only when something changes; the statistics see the benchmark; the CSI 800 schedule from 2026-11.

**Files.** `internal/app/forecast_bench.go`, `internal/app/forecast_admin_api.go`, `internal/app/apiv1.go`
(ingest uses the cohort's benchmark), `internal/app/forecast_groups.go`, `internal/app/forecast_groups_store.go`
(`groupSet.BenchCode`), `internal/app/server.go` (boot reconciliation), `internal/app/audit.go`,
`web/src/pages/manage/ForecastSettingsModal.tsx`, `web/src/pages/manage/ForecastMatrix.tsx`,
`scripts/e2e_check.py` (section G, lines 491-500), locales.

```go
// forecast_bench.go
const (
	setForecastBenchNext     = "forecast_bench_next"
	setForecastBenchNextFrom = "forecast_bench_next_from" // "YYYY-MM"
)

// ForecastBenchFor is the benchmark a set made in this cohort is frozen with. It is the only reader the
// ingest path uses (apiv1.go:259 today reads ForecastBench()).
func (s *Store) ForecastBenchFor(cohort string) string {
	if next := s.GetSetting(setForecastBenchNext, ""); next != "" {
		if from := s.GetSetting(setForecastBenchNextFrom, ""); from != "" && cohort >= from {
			return next
		}
	}
	return s.ForecastBench()
}

func nextCohort(cohort string) string                    // "2026-10" -> "2026-11", "2026-12" -> "2027-01"
func (s *Server) reconcileForecastBench(now time.Time) error // boot; idempotent; tx handle only
func (s *Server) promoteForecastBench(now time.Time) error   // boot (and daily from P9's housekeeping)
```

**Ingest.** `v1Ingest` computes `madeAt := time.Now().UTC()` once and passes it as `predInput.MadeAt` **and**
derives `BenchCode: s.st.ForecastBenchFor(shanghaiCohort(madeAt))`, so the frozen code and the cohort come from
the same instant.

**Settings save (explicit intents).**

| Field | Effect | Audit (`forecast.bench`) |
|---|---|---|
| `bench_schedule: "<code>"` | Refused unless in the allowlist (400 `forecast_bad_bench`). If `code` equals the active value and nothing is scheduled: no-op. Otherwise `forecast_bench_next = code`, `forecast_bench_next_from = nextCohort(cohortOf(now))`. Always the next cohort: the decisions page says "effective at the next cohort", so the plan's "apply at once when the current cohort has no in-population set" clause is not built. | `{from: active, to: code, effective_cohort}` |
| `bench_cancel: true` | Clears `next` and `next_from` when set; otherwise no-op. | `{cancelled: code}` |
| `bench: "<code>"` (legacy) | Treated as `bench_schedule` through R1b; removed in P29. | as above |
| `mode` (`0\|dry\|1`) / `enabled` (alias, `true`→`1`, `false`→`0`, through R1b) | P9 owns `dry`/`1` readiness; in P4 only `0`/`1` with the existing behaviour. Audited `forecast.auto` **only when the value changes** (today it audits every save, `forecast_admin_api.go:51-56`). | `forecast.auto {from,to}` |

All messages English (replacing the Chinese message at `forecast_admin_api.go:46`). The modal posts only the fields
the admin changed.

**Promotion.** At boot (and daily from P9): if `next` is set and `cohortOf(now) >= next_from`, set
`forecast_bench = next`, clear both, audit `forecast.bench {reason:"promote"}` with actor `system`. `ForecastBenchFor`
is already correct before promotion; promotion only makes the active value read true.

**Boot reconciliation (once per boot, idempotent).** Let `M = cohortOf(now)` and `F` = the frozen `bench_code` of
M's earliest in-population (`made_at >= forecast_population_from`), non-test accepted set ledger-wide. If `F` exists
and differs from the active value: active := `F`; if nothing is scheduled, schedule the old active value from
`nextCohort(M)`; audit both with actor `system` and `{reason:"reconcile"}`. In production the active value already is
`sh000852` (V2), so this is a no-op.

**Statistics.**
- `groupSet` gains `BenchCode string` (`forecast_groups_store.go:13-42` and its SELECT).
- `cohortBench(rtype, cohort)` = the frozen code of the cohort's earliest in-population non-test accepted set
  **ledger-wide** (computed once per request from the loaded sets plus, for `own` populations from P26, one indexed
  query over the cohort; every population uses the same cohort label).
- In `forecastMembership`, after pre-live and test are removed and before D4, an accepted set whose code differs from
  its cohort's benchmark is `off_benchmark` (counted). If a symbol's earliest set is off-benchmark, the symbol is
  out for that group, as with `degraded`.
- Every `forecastGroup` carries `bench`. A custom-range group that spans cohorts with different benchmarks is split
  into one group per benchmark (label suffixed with the code), so no cell pools two indexes.

**UI.** The settings modal's benchmark becomes a `Select` over the allowlist with index names
(`forecast.bench.sh000300` 「沪深300」, `.sh000905` 「中证500」, `.sh000852` 「中证1000」, `.sh000906` 「中证800」), showing
"Scheduled: CSI 800 from 2026-11" with a Cancel link when `next` is set. The matrix shows each group's benchmark;
an `off_benchmark` count renders as a chip.

**Production action after deploy (owner, web UI).** Schedule `sh000906`. The response must read
`effective_cohort: "2026-11"`. Check the audit row.

**Tests.**
- `TestBenchChangeIsEffectiveDatedAndReconciled`: active `sh000300` with October frozen at `sh000852` boots to active
  `sh000852` with `sh000300` scheduled from 2026-11, both audited as `system`; an ingest on 2026-11-02 freezes
  `sh000300`; one on 2026-10-30 freezes `sh000852`. FC
- `TestCSI800ScheduleFromNovember`: on 2026-10-23 `bench_schedule:"sh000906"` stores next=`sh000906`,
  from=`2026-11`; an ingest at 2026-10-31T15:59:59Z (23:59:59 CST) freezes `sh000852`, one at 2026-10-31T16:00:00Z
  freezes `sh000906`. FC
- `TestResavingTheActiveBenchIsANoOp`: no change, no cancellation, no audit row. FC
- `TestBenchCancel`: cancels and audits once; a second cancel writes nothing. BI
- `TestEnabledAliasesMode`: `enabled:false` → `0`; an unchanged value writes no audit row. BI
- `TestOffBenchmarkSetsAreExcludedAndCounted`: the cell keeps its majority metrics, coverage adds up exactly. FC
- `TestCustomRangeSlicesByBenchmark`: a range over 2026-10 and 2026-11 yields one group per benchmark; no cell's
  members mix codes. FC
- `TestBenchPromotionAtBoot`: with next_from reached, active becomes next, audited once; a second boot does nothing.
- Golden snapshots unchanged.
- vitest: the Select, the scheduled line with Cancel, and a mode toggle that posts `mode` only.
- e2e section G: a schedule round trip with its audit row; a mode-only save; a no-op save writing nothing (replaces
  today's `{"enabled": False, "bench": …}` post at `scripts/e2e_check.py:498`).

**Rollback.** Revert; an older build reads `forecast_bench`, which reconciliation keeps correct for the current
cohort. A scheduled `next` is ignored by the older build — on 2026-11-01 it would keep freezing `sh000852`; if β1 is
reverted before November, apply the §1.2 contingency.

---

## 5. Release 1a-β2: evaluator correctness (schema-free)

Defect → PR map: F01, F02 → P3 (β1); F06, F07 → P1; F04, F05, F12 → P6; F03, F08, F09, F11, F13, F14 → P7;
F10 → P8; B5 (members route scoring and outcome leak) → P16a.

### 5.1 P1 `fix/forecast-due-only-scheduling` — schedule only due work; not due is never a refusal; finality per V26 (F06, F07)

**P1 completes what P3 started.** P3 (R1a-β1) defers a benchmark fault with its own code (`fetch_failed`,
`not_index`, `calendar_bad`, `no_bench`) but through today's defer, which counts every deferral as an
attempt. P1 introduces the source/mark-specific refusal class so a source code leaves `attempts` unchanged
and never counts toward finality (V26). **The evaluator must stay off between P3's deploy and P1's** —
this PR's readiness item (§1.4 item 1) is what keeps it off — because a benchmark fault written by P3
against the old semantics would otherwise become a final refusal. This PR's `TestSourceFailuresAreNotMarkSpecific`
(runbook §5.3) is where the "attempts stay 0 / never final" half of P3's `calendar_bad` property is asserted.

**Goal.** Today every first tick before S0 refuses every mark `no_bar` with a fixed one-hour backoff and no
`session_date` (`forecast_eval.go:176-191`, `forecast_eval_store.go:120-137`); finality fires without an attempt
(`forecast_eval.go:186-191`); the selection takes every accepted set with any pending mark every minute, ordered
`made_on, id LIMIT 50`, driving from `prediction_sets` with a correlated EXISTS so `idx_pred_marks_due` is never used
(`forecast_eval_store.go:39-53`), and `CountForecastSetsDue` repeats it (`forecast_admin_api.go:63-71`).

**Files.** `internal/app/forecast_schedule.go` (new), `internal/app/forecast_eval.go`,
`internal/app/forecast_eval_store.go`, `internal/app/forecast_admin_api.go`, `internal/app/server.go` (heal at boot),
`internal/app/audit.go`.

```go
// forecast_schedule.go

// predSchedule is one checkpoint's dates. Known is false while the calendar cannot yet decide.
type predSchedule struct {
	S0     string // the start session
	Record string // the session whose close is recorded
	Due    string // the session after whose completion the mark may settle
	Known  bool
}

// predScheduleFor is the ONE statement of ADR 0029 §4's arithmetic. The evaluator passes the benchmark's
// completed sessions; the read path (P10/P16) passes a projection, where every date is a lower bound.
// P1 ships it with TODAY's rules (S0 = first session strictly after made_on; dN Record = sessions[N-1]),
// pinned by characterization tests; P6 switches the rules inside it. Nothing else computes a checkpoint.
func predScheduleFor(cp string, madeAt time.Time, horizonStd bool, frozenS0 string, sessions []string) predSchedule

// predProjectSessions returns the weekday dates (Mon–Fri, no holidays) from `from` onward. Holidays only
// delay, so a projected date is never later than the real one: a lower bound.
func predProjectSessions(from string, n int) []string

// predWakeAt is the earliest instant a mark could be due: the projected due date at 15:30 Asia/Shanghai.
// While the set's baseline is unsettled it is the earlier of that and S0's projected 15:30.
func predWakeAt(set forecastWorkSet, cp string) time.Time

// predNotYetShown is the next wake when a woken mark's due session is not yet on the real calendar:
// +10 minutes until 18:00 CST; after 18:00, 09:00 CST on the next weekday; a wake at 09:00 that still
// does not show it moves to 15:30 the same day.
func predNotYetShown(now time.Time) time.Time
```

```go
// forecast_eval_store.go

// ForecastSetsDue replaces ForecastSetsNeedingWork. It drives from the marks' due index, so set 51 is
// never starved and nothing is fetched for a set with no due mark.
func (s *Store) ForecastSetsDue(now time.Time, limit int) ([]forecastWorkSet, error)
/*
SELECT ps.id, ps.symbol, ps.made_on, ps.made_at, ps.horizon_end, ps.horizon_std, ps.bench_code,
       ps.base_check, ps.s0_date, ps.base_date, ps.s0_fen, ps.emitted_base_fen
FROM prediction_sets ps
JOIN (SELECT DISTINCT set_id FROM prediction_marks
      WHERE status IN ('pending','refused') AND next_attempt_at <= ?) due ON due.set_id = ps.id
WHERE ps.state = 'accepted'
ORDER BY ps.made_on, ps.id
LIMIT ?
*/
// A new mark has next_attempt_at = '', which sorts before every RFC3339 instant, so it is selected once
// and scheduled without a vendor call. CountForecastSetsDue uses the same subquery.

// RescheduleForecastMarks sets next_attempt_at for marks that are not due; no other column changes.
func (s *Store) RescheduleForecastMarks(tx *sql.Tx, wakes map[int64]time.Time) error

// DeferForecastMarks records a failed attempt on DUE marks only, with the session and the evaluator clock.
//   mark-specific: status='refused', refusal=code, session_date=session, attempts=attempts+1,
//                  next_attempt_at = now + predRetryBackoff(attempts+1)   (1h, 6h, 24h, 24h…)
//   source:        status='refused', refusal=code, session_date=session, attempts unchanged,
//                  next_attempt_at = now + 10m on the first source failure of this code, else now + 1h
// Guarded: WHERE id=? AND status IN ('pending','refused').
func (s *Store) DeferForecastMarks(tx *sql.Tx, marks []forecastMark, code, session string, now time.Time) error

// MarkRefusedFinal ends retries. Accepts pending or refused (today: refused only, forecast_eval_store.go:159-166).
func (s *Store) MarkRefusedFinal(tx *sql.Tx, markID int64, code string, now time.Time) error

// FinishForecastBaselineTerminal: base_check='refused_final' and every non-terminal mark refused_final
// with refusal 'no_base', in one transaction.
func (s *Store) FinishForecastBaselineTerminal(tx *sql.Tx, setID int64, now time.Time) error
```

`forecastWorkSet` (`forecast_eval_store.go:13-24`) gains `S0Date, BaseDate string; S0Fen, EmittedBaseFen *int64`.
`forecastMark` gains `NextAttemptAt, SessionDate, Refusal string`. DeferForecastSet is deleted.

**Algorithm (per set, P1 form; P6/P7 fill in the settlement).**

```
cal := calendars[set.BenchCode]              // P3
sessions := completed sessions of cal        // P6 tightens the guard
plan := forecastSetPlan{set}
for mark in set.Marks (non-terminal):
    sch := predScheduleFor(mark.cp, set.MadeAt, set.HorizonStd, set.S0Date, sessions)
    if !sch.Known || sch.Due not completed:
        if now < predWakeAt(set, mark.cp):  plan.reschedule(mark, predWakeAt(set, mark.cp))
        else:                               plan.reschedule(mark, predNotYetShown(now))   // never a refusal
        continue
    res := settle(...)                         // P6/P7
    if res.ok: plan.settle(mark, res); continue
    class := predRefusalClass[res.code]
    if class == refusalMarkSpecific
       && mark.Attempts+1 >= predFinalMinAttempts
       && sessionsPastDue(sessions, sch.Due) >= predFinalSessionsPastDue:
        plan.final(mark, res.code)             // finality only as the result of a failed attempt
    else:
        plan.defer(mark, res.code, sch.Due)
baseline: if S0 known and the baseline failed with a mark-specific code
          and sessions since S0 >= 10 and min(attempts of non-terminal marks) >= 4:
              plan.terminalBaseline()
apply plan in one transaction (P6 makes it baseline-first)
```

A processed set always leaves the due window (settled, rescheduled, or backed off), so `LIMIT 50` cannot starve a
later set.

**One-shot heal (boot).** Runs once, recorded under `forecast_state_repair_premature` with its count, audited
`forecast.repair` with actor `system` and `{count, set_ids}`:

```sql
UPDATE prediction_marks SET status='pending', attempts=0, refusal='', next_attempt_at=''
WHERE status='refused' AND refusal='no_bar' AND session_date='' AND resolved_at=''
```

Run on the tx handle. The predicate is the signature of a pre-P1 deferral (`DeferForecastSet` never wrote
`session_date`); after P1 every recorded attempt writes `session_date`, so the predicate never matches a real
attempt again and the heal is idempotent. **Departure from the plan:** the plan also required "not due under the
fixed schedule" (checked in Go). By the R1a-β2 deploy the four accepted sets' d5 marks (S0 2026-10-19, d5
2026-10-26) are due, so that check would skip 4 of the 20 and leave them with a recorded attempt that never
happened. Returning a due mark to `pending` only causes a fresh, recorded attempt, so the check is dropped. Q3
(§1.5) should read 20; the boot log line and the audit row should say 20.

**Tests.**
- `TestCheckpointArithmeticToday` (CH): pins today's `sessions[n-1]` / `len > n` and S0, including the
  `len == n` and `len == n+1` boundaries. Replaced by P6.
- `TestWaitingMarksStayPending`: the production shape (made Sunday 21:00 CST; ticks Monday 10:00, Monday 15:31,
  Tuesday 01:00) refuses nothing, keeps attempts 0, and fetches no stock series before S0's close. FC
- `TestNotDueSetsCostNoFetch`: 55 sets made Friday evening are scheduled with 0 vendor calls and cost 0 calls until
  Monday 15:30. FC (vendor load)
- `TestSelectionDoesNotStarve`: 300 sets with pending h12; set 301's due d5 is evaluated within two ticks. BI
- `TestDueSelectionUsesTheDueIndex`: `EXPLAIN QUERY PLAN` (SQLite) / `EXPLAIN` (Postgres) shows
  `idx_pred_marks_due`. FC (load)
- `TestCalendarNotYetShowingTheSessionIsNotARefusal`: a due mark at 15:31 whose calendar lacks today wakes at
  +10 min, then 18:00 → next weekday 09:00; nothing refused. FC
- `TestOutageNeverFinalises`: 20 sessions of `fetch_failed` leave attempts 0 and the mark `refused`, never final;
  one success then settles it. FC
- `TestCatchUpAfterDryPeriodNeverFinalisesOnFirstFailure`: a mark 30 sessions past due becomes final only on its
  4th mark-specific failure. FC
- `TestBackoffEscalatesOnTheEvaluatorClock`: 1 h, 6 h, 24 h, 24 h from the injected clock; `resolved_at` empty while
  refused. BI
- `TestBaselineBecomesTerminal`: 10 sessions and 4 mark-specific baseline failures → `base_check=refused_final`,
  marks `refused_final`/`no_base`; the cell is no longer pending. BI
- `TestPrematureRefusalRepairRunsOnceOnTheSignature`: the 20-mark production tuple heals; a mark deferred by the new
  code (with `session_date`) is untouched; a second boot does nothing; the audit row carries the count. FC
- `TestHealAndApplyNeverCallThePoolInsideATransaction`: SQLite, 2-second deadline. FC
- `TestDueCountMatchesSelection`.
- PG variants of the selection, the heal and the plan apply.
- Existing `TestRefusalBecomesFinalAfterTheDeadline` (`forecast_eval_test.go:436`) is rewritten to V26 in this PR
  (deliberate change, noted in the PR body).

**Rollback.** Revert while the evaluator is off. Healed rows are `pending`, which the old binary handles.

### 5.2 P6 `fix/forecast-checkpoint-definitions` — S0 and dN per ADR 0029 §4 (F04, F05, F12)

**Goal.** S0 ignores the time of day (`resolveStartBar`, `forecast_eval.go:445-464`, takes the first session
strictly after `made_on`); dN records one session early (`checkpointSession` returns `sessions[n-1]`,
`forecast_eval.go:344-368`); marks use a baseline recomputed every pass instead of the stored one
(`forecastWorkSet` has no S0 fields, marks are written before the baseline, `forecast_eval.go:170-199`).

**Files.** `internal/app/forecast_schedule.go`, `internal/app/forecast_eval.go`, `internal/app/forecast_eval_store.go`.

**Completed-session guard.** The calendar the evaluator passes to `predScheduleFor` contains only completed
sessions:

```go
// completedSessions returns the index session dates that have completed by now, ascending:
//   a bar dated before today (Asia/Shanghai) is complete;
//   a bar dated today is complete only when barIsComplete(resp, now) (stamp at or after 15:00 on that
//   date AND the clock at or after 15:30);
//   a bar dated after today never is.
func completedSessions(cal forecastSeries, now time.Time) []string
```

**S0 (the 15:00 rule).**

```go
// predS0 is the first session whose close is after made_at. sessions are completed, ascending.
// Received before 15:00:00 on session T (pre-open and the lunch break included) → T.
// Received at or after 15:00:00, or on a day that is not a session → the next session.
// Returns false while the calendar cannot decide: a date is known not to be a session only once a LATER
// session is on the calendar (vendors publish in order), so "today has no bar yet" is never read as
// "today is a holiday".
func predS0(madeAt time.Time, sessions []string) (string, bool) {
	t := madeAt.In(chinaZone)
	d := t.Format("2006-01-02")
	close := time.Date(t.Year(), t.Month(), t.Day(), 15, 0, 0, 0, chinaZone)
	beforeClose := t.Before(close)
	for _, s := range sessions {
		switch {
		case s < d:
			continue
		case s == d:
			if beforeClose {
				return d, true
			}
			continue // received after the close: the next session
		default:
			return s, true // first session strictly after d
		}
	}
	return "", false
}
```

**dN and h12.**

```
i0 := index of S0 in sessions            // S0 from the stored s0_date when set, else predS0
dN: Record = Due = sessions[i0 + N]       // the Nth session AFTER S0 (today: sessions[N-1] counted from S0)
    Known  = len(sessions) > i0 + N
h12 (standard horizons only): Record = Due = first session >= addMonths(made_on, 12); Known when one exists
```

Worked cases, all in the tests: set 6 (made Sunday 2026-10-18 evening CST) → S0 2026-10-19, d5 2026-10-26; the
demo's 2026-09-17 report (received after 15:00, demo calendar fixture without a 2026-09-25 bar) → S0 2026-09-18,
d5 2026-09-28.

**Stored baseline and atomic apply.** If `set.BaseCheck` is terminal (`match`, `mismatch`, `refused_final`), every
mark uses `set.S0Date`, `set.BaseDate`, `set.S0Fen`. Otherwise the pass computes the baseline (P7 semantics) and
applies **baseline first, then the marks, in one transaction** through `ApplyForecastPlan(tx, plan)`. A failure on
any write rolls back the baseline and every mark of the set.

**Tests.**
- `TestS0FollowsReceiptTimeOfDay`: 10:00 on session T → T; 15:00:00 → T+1; 14:59:59 → T; 12:00 (lunch) → T;
  08:00 (pre-open) → T; Sunday → Monday; a holiday Monday known only once Tuesday's bar exists. FC
- `TestS0WaitsWhileTodayIsUnknown`: received Monday 10:00, ticked Monday 15:20 (no Monday bar yet) → not known, no
  refusal, no write. FC
- `TestDNIsTheNthSessionAfterS0`: replaces the P1 CH pin; includes set 6 (d5 2026-10-26) and the demo report (d5
  2026-09-28). FC
- `TestMarksUseTheStoredBaseline`: after the baseline is written, a revised vendor body on a later pass does not move
  any mark's base. FC
- `TestPlanIsAtomicPerSetBaselineFirst`: a failure injected on mark 2 rolls back the baseline and mark 1; SQLite,
  2-second deadline. FC
- Existing `TestResolveStartBarIsTheFirstSessionAfterReceipt` and `TestCheckpointSessionIsSessionBasedAndH12IsCalendar`
  (`forecast_eval_test.go:310,203`) are replaced in this PR (deliberate).

**Rollback.** Revert while the evaluator is off.

### 5.3 P7 `fix/forecast-settlement-semantics` — baseline check, halts, completeness, consistency checks, limit backstop, provenance (F03, F08, F09, F11, F13, F14)

**Files.** `internal/app/forecast_eval.go`, `internal/app/forecast_eval_store.go`, `internal/app/forecast_settle.go`
(new: the pure settlement functions), `internal/app/forecast_vocab.go`, `internal/app/openapi.json` (refusal enum
on marks, if exposed on v1), fixtures under `testdata/quote/` (V20 probes: a currently suspended SH or SZ stock, a
delisted one, limit-up days on a `301`, a `689`, a Beijing code and a sub-¥2 stock).

**Baseline check, design §9.4 exactly (V5, F03).** Today `FinishForecastBaseline` writes `match` whenever the
calendar has a bar on S0 (`forecast_eval_store.go:171-183`) and never reads `emitted_base_fen`.

```go
// predBaseCheck compares the producer's emitted baseline with what the market printed. The reference
// session comes from made_at, never from the payload; emitted_base_as_of is a diagnostic only.
func predBaseCheck(madeAt time.Time, emittedFen *int64, stock []QuoteBar, sessions []string) string
/*
if emittedFen == nil                               -> mismatch
t := madeAt in Asia/Shanghai; d := date(t)
if d is a session AND 09:30 <= t < 15:00 (lunch included):        // during a session
    T := d; P := session before T
    match if low(T) <= *emittedFen <= high(T)  OR  *emittedFen == close(P)
else:                                                               // outside a session
    L := last completed session <= t (a session day before 09:30 → the previous session)
    match if *emittedFen == close(L) OR *emittedFen == close(session before L)
close(x) of a halted stock = its last traded close at or before x
otherwise                                         -> mismatch
the check needs T's (or L's) bar to be complete; until then the baseline is not due
*/
```

**Halts (V6a(a), V6b(a), F09, F11).**

```
stock leg for a due mark at session S:
  bar := last stock bar <= S with bar.Date >= base_date
  if bar.Date == S: resolve normally
  else (no bar on S):
     evidence     := the stock body's vendor stamp date >= S          (the vendor says it is current)
     confirmation := at least 3 completed index sessions after S, and still no stock bar after bar.Date
     valve        := more than 20 % of THIS pass's stock series end before the calendar's newest session
     if evidence:                    resolve halted=1, close = bar.Close
     elif confirmation and !valve:   resolve halted=1, close = bar.Close
     elif valve:                     defer "calendar_lag" (source class: vendor-wide lag; attempts do not move)
     else:                           defer "stale_bar" (mark-specific; within 3 sessions it cannot be final,
                                      because finality also needs 10 sessions past due)
base for a stock that did not trade on S0 (V6b):
  base_date := last traded stock bar strictly before S0 within the window (made_on - 10 days);
  if none: one extra fetch reaching back 400 calendar days (bounded look-back);
  if none there either: the baseline fails "no_base" (mark-specific) → terminal baseline after 10 sessions (P1)
  s0_fen := close(base_date); marks due during the halt resolve with ret 0 and halted=1
benchmark leg (always): bench_ret_bp = index close(S) / index close(S0) - 1   — from S0, never from base_date
```

Today the benchmark leg starts at `base_date` (`forecast_eval.go:425-430`), which would add pre-report index moves
to the excess whenever a stock is halted on S0.

**Completeness of the newest stock bar (F11).** Replaces the wall-clock-only drop in `rangeBars`
(`forecast_eval.go:226-240`):

```
for the newest bar b of a body with vendor stamp st (may be empty):
  st present:  b is final when b.Date < date(st);
               b.Date == date(st): final only when st >= 15:00 that day and now >= 15:30 that day
  st absent:   b is final when b.Date < today (Asia/Shanghai); a bar dated today is refused "unverifiable"
every earlier bar is final
```

A suspended stock stamped 09:00 on the latest session (`testdata/quote/tencent_fqkline_bj830799.json`) keeps its
last traded bar (dated before the stamp's date).

**Missing benchmark bar (F08).** If the index has no bar on S0 or on S, the mark is refused `unverifiable`
(mark-specific). It never resolves with a nil `bench_ret_bp`.

**Self-consistency checks on the vendor's own bodies** (each refusal writes nothing else):

| Check | Refusal |
|---|---|
| benchmark body `Kind == "index"` (P3) | `not_index` (source) |
| every index bar in the window has volume > 0 | `calendar_bad` (source class: index-wide) |
| stock bar dates in [`base_date`, S] ⊆ index dates | `calendar_mismatch` (mark-specific: this stock's series only) |
| the stored `s0_date` is reproduced by `predS0` over this calendar | `calendar_bad` (source class: the index calendar disagrees with every mark of the set) |

Only the per-stock subset check is `calendar_mismatch`, the one calendar refusal that counts toward finality. The two
index-wide faults are `calendar_bad`: they never increment `attempts`, never count toward the four attempts V26
requires, and a persistent one is surfaced by the watchdog's `lagging` reason (oldest due mark unsettled over 6 hours)
and by the heartbeat's refusals-by-code, not by a mark going final.
| stock close on `base_date` == stored `s0_fen` (also detects an adjusted body) | `anchor_mismatch` |
| the fetch window built by `quoteParseWindow` (`quote.go:1075`) from `made_on` | `bad_window`, with no vendor call |

**Daily-limit backstop, corrected (F14).** Today `dailyLimitBp` gives 20 % only to `300`/`688` prefixes and the
threshold is a basis-point bound on close-to-close (`forecast_eval.go:502-528`), so ChiNext `301`, STAR `689` and
Beijing stocks get 10 %, and a cheap stock's limit day (¥1.05 → ¥1.16 = +10.48 %) is flagged.

```go
// predDailyLimitBp: 2000 for 300/301 (ChiNext) and 688/689 (STAR), 3000 for marketPrefix == "bj",
// 1000 otherwise. ST stocks are NOT modelled (the main-board risk-warning limit changed in 2025; verify the
// exchange's current rule text before relying on any ST figure); their bonus issues are left to P8.
func predDailyLimitBp(symbol string) int64

// beyondLimitPrice flags a day only when its close lies outside the exchange's fen-rounded limit prices by
// more than one fen. The first five bars of a series that begins inside the fetch window are a listing's
// no-limit days and are never flagged.
func beyondLimitPrice(prevFen, closeFen int64, limitBp int64) bool {
	up := roundHalfUp(prevFen*(10000+limitBp), 10000)
	down := roundHalfUp(prevFen*(10000-limitBp), 10000)
	return closeFen > up+1 || closeFen < down-1
}
```

**Provenance (F13).** `SettleForecastMark` writes `source` from the response that produced the bar (today the
constant `quoteSourceTencent`, `forecast_eval_store.go:144-157`). In Tier 1 that is always `tencent`; the point is
that the day a second writer exists the column is already honest.

**Tests.**
- `TestBaselineCheckFollowsDesignTable`: intraday inside [low, high] matches; equal to the previous close matches;
  equal to the close two sessions back mismatches; after the close, equal to the last or the previous close
  matches; pre-open on a session day compares with the previous session; nil emitted mismatches. FC
- `TestBackdatedAsOfCannotTurnMismatchIntoMatch`. FC
- `TestHaltResolvesOnEvidenceOrConfirmation`: within 3 sessions → `stale_bar`, never final; a current stamp resolves
  `halted=1` at once; after 3 sessions resolves `halted=1`; with the valve tripped nothing is written and attempts do
  not move. BI
- `TestHaltedOnS0UsesLastPreS0Close`: `base_date < s0_date`; a d5 during the halt resolves with ret 0 and
  `halted=1`; `bench_ret_bp` runs from S0's index close. FC
- `TestHaltLongerThanTheWindowFetchesBack`: halted 60 days before `made_on` → base found by the bounded look-back;
  none within 400 days → terminal-baseline path. BI
- `TestLimitBackstopNeverFlagsATradingDay`: not flagged: +15 % on `301xxx`, +15 % on `689xxx`, +25 % on a Beijing
  code, ¥1.05 → ¥1.16, a −10 % limit-down, any move in a listing's first five bars; flagged: one fen beyond the
  rounded limit price. BI
- `TestMissingBenchBarIsUnverifiableNotResolved`, plus a statistics test that n excludes such marks. FC
- `TestNewestBarRule`: Thursday 01:00 uses Wednesday's bar; a 14:57 stamp drops today's bar; a body stamped today
  09:00 keeps its last traded bar. BI
- `TestCalendarAndAnchorChecks`: zero-volume index bar → `calendar_bad`, a stock bar off the calendar →
  `calendar_mismatch`, a non-reproduced `s0_date` → `calendar_bad`, a base close different from `s0_fen` →
  `anchor_mismatch`; each gives the matching refusal and no other write. FC
- `TestIndexWideCalendarFaultsNeverCountTowardFinality`: 12 sessions of a zero-volume index bar and of a
  non-reproduced `s0_date`, with 4 or more ticks past due, leave every mark `refused` with `attempts` 0 and none
  `refused_final`; the same 12 sessions of a per-stock `calendar_mismatch` make a mark final on its 4th attempt. BI
- `TestSourceFailuresAreNotMarkSpecific`: `fetch_failed`, `no_bench`, `not_index`, `no_source`, `calendar_lag`,
  `calendar_bad` never increment
  attempts. FC
- `TestSettleRecordsTheAnsweringSource`.
- `TestMalformedMadeOnNeverReachesAVendorURL`. FC
- Existing `TestDailyLimitFollowsTheBoard`, `TestBeyondDailyLimitNeverFlagsALegalMove`,
  `TestSettleMarkMeasuresAgainstTheBenchmark`, `TestBaselineIsWrittenOnce` (`forecast_eval_test.go:92,111,290,411`)
  are updated deliberately.

**Rollback.** Revert while the evaluator is off.

### 5.4 P8 `fix/forecast-adjust-ratio-detection` — detect bonus and rights issues by the adjusted ratio (F10, D8)

**Goal.** D8's qfq fallback was never built (`corporateActionBetween`, `forecast_eval.go:482-500`, uses the dividend
element plus the gap backstop; its comment says no qfq fetch is needed). A main-board 10-for-1 bonus (≈ −9.1 %) or a
ChiNext/STAR 10-for-2 (≈ −16.7 %) stays inside the limit and would be scored as a loss.

**Files.** `internal/app/quote.go` (new `fetchTencentAdjustRatio` next to `fetchTencentRange`, `quote.go:1588`),
`internal/app/forecast_eval.go`, `internal/app/testdata/quote/tencent_fqkline_qfq_<code>.json` and its unadjusted twin
(a real window containing a bonus issue, captured by read-only probe, V20).

```go
// fetchTencentAdjustRatio is the ONLY qfq call in the codebase (ADR 0029 §10). It returns the
// front-adjusted closes for [from, to], keyed by date. Detection only: never stored, scored or displayed;
// called only for due marks whose stock leg resolved.
func fetchTencentAdjustRatio(ctx context.Context, market, code, from, to string) (map[string]int64, error)

// adjustRatioChanged reports whether qfq/raw differs between base_date and S by more than the rounding of
// four fen-rounded prices can explain. Exact integers: compare q1*p2 with q2*p1.
func adjustRatioChanged(q1, p1, q2, p2 int64) bool {
	diff := abs64(q1*p2 - q2*p1)
	bound := (q1 + p1 + q2 + p2) / 2 + 1 // |Δ(q·p)| from ±0.5 fen on each of the four prices
	return diff > bound
}
```

`corporateActionBetween` becomes: dividend element in (base_date, S] **or** `beyondLimitPrice` on any day **or**
`adjustRatioChanged`. One qfq fetch per set per tick covers [base_date, latest due S]. A failed qfq fetch is a
source failure `fetch_failed` (the mark waits; it is never resolved without the check).

**Tests.**
- `TestQfqRatioFlagsBonusIssueUnderTheLimit`: the captured −9.09 % main-board ex-date is flagged; a −9.5 % limit-down
  day with an unchanged ratio is not; fen rounding alone never flags (property test over random prices). BI
- `TestAdjustRatioIsNeverStoredOrScored`: no column receives a qfq value (source scan for writes plus a row diff). FC
- `TestAdjustFetchOnlyForDueMarks`: a tick with no due mark makes no qfq call. FC (vendor load)
- `TestQfqFetchFailureIsASourceFailure`. FC

**Rollback.** Revert and keep the mode at `dry`.

### 5.5 P16a `fix/forecast-hit-predicate-and-members-gate` — one hit predicate; the members route scores nothing (V15, B5)

**Goal.** The drawer's `agree` uses its own float rule with no 5 % threshold and treats zero excess as disagreement
(`forecast_groups_members.go:157-166`); the members route recomputes the whole cell (10 000 permutations) on every
click only to read its state (`forecast_groups_members.go:122`) and returns outcomes whatever the cell state.

**Files.** `internal/app/forecast_groups.go` (`hitStats`, `forecast_groups.go:431-460`),
`internal/app/forecast_groups_members.go`, `internal/app/forecast_hit.go` (new),
`web/src/pages/manage/ForecastCellDrawer.tsx`, locales.

```go
// predHitClass is the single hit-rate predicate, used by hitStats and by every per-row verdict.
func predHitClass(terminalFen, s0Fen int64, excessBp int32) string {
	if s0Fen <= 0 {
		return dirUntested
	}
	d := terminalFen - s0Fen
	if abs64(d)*10000 < int64(predHitUpsideBp)*s0Fen {
		return dirUntested // |upside| < 5 %
	}
	switch {
	case excessBp == 0:
		return dirFlat
	case (excessBp > 0) == (d > 0):
		return dirAgree
	default:
		return dirDisagree
	}
}

// forecastCellState is the gate without the scoring: the same maturity rules computeForecastCell applies
// (forecast_groups.go:159-205), returning only the state. The members route calls this, never the scorer.
func forecastCellState(members, pendingBaseline []*groupSet, cp, end, today string) string
```

`forecastMember.Agree *bool` is replaced by `Direction string` (`agree|disagree|flat|untested`). Outcome fields
(`close_fen`, `ret_bp`, `bench_ret_bp`, `excess_bp`, `direction`) are sent **only when the cell state is `mature`**;
otherwise members carry identity and status only.

**Tests.**
- The cell snapshot is unchanged, or its regeneration is in its own reviewed commit explaining each byte.
- The drawer snapshot is regenerated in its own reviewed commit.
- `TestDirectionReconcilesWithHitRate`: excess-0 ties and members at 4.99 %, 5.00 %, 5.01 %;
  agree ÷ (agree + disagree + flat) equals the cell's hit rate. BI
- `TestMembersRouteScoresNothing`: a counting hook on the permutation function stays at 0; the state equals the
  matrix cell's. FC (compute)
- `TestMembersWithholdOutcomesUnlessMature`: a non-mature cell's members response has no outcome keys at all. FC
- vitest: the drawer renders the four-state direction with text (not colour alone).

**Rollback.** Revert while the evaluator is not at `1`.

---

## 6. Release 1b: operations, reading-page hotfix, attribution (contains step `0006`)

### 6.1 P9 `feat/forecast-evaluator-operations` — dry mode, heartbeat, watchdog, health route, evaluate-now, readiness

**Goal.** Operate the evaluator as a service: a mode that plans without writing, a heartbeat, an independent
watchdog, a status-only health route for an external monitor, a manual pass, and the code-enforced readiness list of
§1.4.

**Files.** `internal/app/forecast_ops.go` (new), `internal/app/forecast_eval.go`, `internal/app/forecast_admin_api.go`,
`internal/app/routes.go`, `internal/app/server.go` (start the watchdog next to the evaluator, `server.go:248`),
`internal/app/webhook_run.go`, `internal/app/webhook_api.go`, `internal/app/audit.go`,
`web/src/pages/manage/ForecastEvaluatorStatus.tsx` (new), `web/src/pages/manage/ForecastSettingsModal.tsx`, locales.

```go
// forecast_ops.go

// step is one loop iteration. It makes exactly ONE settings read and nothing else when the mode is off.
func (e *forecastEvaluator) step(s *Server) // called by loop(); recover() inside, panics counted in the heartbeat

type forecastHeartbeat struct {
	Mode       string    `json:"mode"`
	LastTickAt string    `json:"last_tick_at"`
	Pass       passStats `json:"pass"`  // kind, started/finished, sets scanned, fetches per (source,market,code),
	                                   // resolved, halted, refused by code and class, finalised, healed,
	                                   // baselines by verdict, awaiting_session, due_unsettled,
	                                   // oldest_due_unsettled_minutes, stale_valve_tripped, errors, last_error, panics
	Bench      map[string]benchStat `json:"bench"` // per code: last bar, fetched_at, source, market, code
	DrySample  []drySample          `json:"dry_sample,omitempty"` // up to 20 would-be writes with their fetch (market, code, source)
	AwaitingGate []string           `json:"awaiting_gate,omitempty"` // bench codes planned but not written: no dry-run check recorded
}

// forecastReadiness evaluates §1.4. Each item has a stable key, ok, and a detail for the page.
func (s *Server) forecastReadiness(now time.Time) []readinessItem

// forecastWatchdog runs in its own goroutine, every 60 s, with its own recover(). It does nothing while
// the mode is off. It never runs inside cleanupLoop (cleanup.go:180-201), which runs purges synchronously
// and has no recover.
func (s *Server) forecastWatchdog()
```

- **Modes.** `forecast_auto` ∈ {`0`, `dry`, `1`}. An older binary reads anything but `1` as off
  (`forecast_eval.go:70`). In `dry`, the pass computes every plan (P1/P6/P7/P8) and applies none to the ledger: the
  heartbeat carries up to 20 sampled would-be writes, each with its `(market, code, source)`. In `1`, a set whose
  frozen benchmark has no recorded dry-run check is still planned as in dry mode and listed in `awaiting_gate`.
- **Heartbeat** in `meta.forecast_eval_state`, written by every pass and only while the mode is not off.
- **Watchdog** reasons, emitted as webhook `forecast.evaluator.unhealthy` and through the P41 dispatcher:
  `stalled` (heartbeat older than 10 minutes; at most hourly), `lagging` (oldest due mark unsettled over 6 hours, or
  a weekday past 18:00 CST with a stale calendar, or the valve tripped; at most daily), `no_source` (ledger chain
  empty; at most daily; **emitted from P23 on** — before it there is no chain, the evaluator calls Tencent directly,
  and this reason never fires). De-duplicated through `forecast_alert_state`. The watchdog also runs the daily benchmark
  promotion of P4.
- **Mode `1` readiness.** `POST /api/admin/forecast {"mode":"1"}` → 409 `forecast_not_ready` with
  `{"items":[…]}` until every §1.4 item holds.
- **Dry-run gate record.** `POST /api/admin/forecast {"dry_gate":{"code":"sh000852","note":"<closes checked>"}}`
  stores `forecast_dry_gate[code] = {at, by, note}`; refused with 400 `forecast_bad_gate` for a code outside the
  allowlist or an empty note; audited `forecast.dry_gate`. Reset on restore.

**Routes.**

| Route | Class | Contract |
|---|---|---|
| `GET /healthz/forecast` | public (`s.public`) | **New.** Status only: 200 `{"ok":true}`, or 503 `{"ok":false}` when the mode is not off and the heartbeat is older than 10 minutes. No other field, ever. `/healthz` (`health.go:55`) stays database-only. Documented in `openapi.json` next to `/healthz` (`TestHealthzSpecMatchesTheHandler` pattern). |
| `POST /api/admin/forecast/evaluate` | admin | **New.** Runs one pass now in the current mode. 409 `forecast_eval_off` when off; 409 `forecast_eval_running` when a pass is in flight. Audited `forecast.eval.run`. |
| `GET /api/admin/forecast` | admin | Adds `mode`, `heartbeat`, `readiness[]`, `dry_gate`. `enabled` stays for one release. `due_sets` stays for one release. |
| `POST /api/admin/forecast` | admin | Accepts `mode` (`0\|dry\|1`) and `dry_gate`. 400 `forecast_bad_mode`. Each field audits only on a real change. |

**UI.** `ForecastEvaluatorStatus` card at the top of `/manage/forecast`: states *off*, *dry* (with the sample
table), *healthy*, *stale* (heartbeat age), *lagging* (reason), *error* (last error), *loading*, *fetch error*. The
mode becomes a three-way radio; Off → On opens a confirmation listing the readiness items, and the On option is
disabled with the failing items named while any fails. A "Record dry-run check" form per benchmark code in use.
i18n: `forecast.eval.mode.{off,dry,on}`, `forecast.eval.status.{off,dry,healthy,stale,lagging,error}`,
`forecast.eval.readiness.*` (one key per item), `forecast.eval.run_now`, `forecast.eval.gate.*`,
`err.forecast_not_ready`, `err.forecast_eval_off`, `err.forecast_eval_running`, `err.forecast_bad_mode`,
`err.forecast_bad_gate`, `webhook.event.forecast.evaluator.unhealthy`.

**Tests.**
- `TestOffStepDoesExactlyOneRead`: a counting store over the real `step()` sees one `meta` read, no writes, no
  fetches. FC
- `TestDryRunWritesNothing`: checksums of `prediction_sets` and `prediction_marks` equal before and after a dry pass;
  the sample lists `(market, code, source)`. FC
- `TestModeOneWithoutGateWritesNothingForThatBenchmark`: a `sh000906` set is planned, not written, and listed in
  `awaiting_gate`; a `sh000852` set with a recorded check is written. FC
- `TestReadinessGateCoversPreLiveBenchCodes`: a ledger holding only pre-live accepted sets frozen with `sh000852`
  and no in-population set still lists `sh000852` under readiness item 4 and refuses mode `1` (409
  `forecast_not_ready`) until its check is recorded; after the check the same pre-live sets are written (their marks
  settle) and still never count. A ledger with no accepted set at all lists the active `forecast_bench` code, so the
  item is never vacuously satisfied. FC (a set can never wait forever for a check the page does not ask for)
- `TestReadinessHasNoLedgerChainItemBeforeP23`: the readiness list has exactly items 1–5 on this PR; P23's PR adds a
  sixth. CH
- `TestPanicDoesNotKillTheLoop`, `TestPanicDoesNotKillTheWatchdog`.
- `TestWatchdogRunsWhileCleanupIsSlow`: a blocked purge does not delay the watchdog. FC
- `TestWatchdogDetectsStalledEvaluator`: one event when on and stale; nothing but one read when off;
  de-duplication holds across ticks. BI
- `TestHealthzForecastIsStatusOnly`: 200 or 503, and the body has exactly one key. FC
- `TestModeOneRequiresReadiness`: 409 until each item holds, one item at a time. FC
- `TestEvaluateNowRespectsModeAndSingleFlight`. BI
- vitest for the status card's states and the readiness-gated radio.
- e2e: a mode round trip including `dry`; evaluate returns 409 when off.

**Rollback.** Revert; an older binary reads `dry` as off.

### 6.2 P41 `feat/forecast-alert-channels` — Email and Slack for evaluator alerts (V18)

**Goal.** V18: alerts go to both Email and Slack. Webhook events alone cannot reach Slack (signed JSON, no `text`
field, `webhook_run.go:33-77`), and nothing in the code speaks Slack today.

**Files.** `internal/app/forecast_alert.go` (new), `internal/app/apiv1.go` (the store-failed branch P2 left, `apiv1.go:345-352`),
`internal/app/forecast_admin_api.go`, `internal/app/routes.go`, `internal/app/audit.go`,
`web/src/pages/manage/ForecastAlerts.tsx` (new), locales.

```go
// forecast_alert.go
const (
	setForecastAlertEmailTo  = "forecast_alert_email_to"  // comma-separated addresses
	setForecastAlertSlackURL = "forecast_alert_slack_url" // write-only secret, never returned
	setForecastAlertTestOK   = "forecast_alert_test_ok_at"
)

// forecastAlert sends one alert to every configured channel. Best effort per channel; each outcome is recorded in
// forecast_alert_state so the page shows the last delivery. The webhook event is fired by the producer, not here
// (the watchdog for forecast.evaluator.unhealthy, P2's ingest branch for predictions.store_failed), so a kind
// never produces two webhook deliveries.
func (s *Server) forecastAlert(kind, reason string, detail map[string]any) map[string]error

// slackPost POSTs {"text": "..."} with a 10 s timeout and two retries on transport/5xx. The URL must be
// https with host hooks.slack.com (a closed host list: the server never posts to an arbitrary URL).
func slackPost(ctx context.Context, url, text string) error
```

Email goes through `sendEmail` (`email.go:67`), which refuses when email is disabled or unconfigured; that refusal is
recorded as the channel's outcome.

**The ingest-path alert (`predictions.store_failed`).** §12.1 promises Email and Slack for every store failure, and P2
only emits the webhook and the audit row. This PR adds the call in `v1Ingest`'s second-failure branch, after P2's audit
write and webhook:

```go
// asynchronous: the producer is waiting on the response, a started run is never re-run, and the alert's
// network I/O (SMTP, Slack, up to 10 s each plus retries) must not delay it or sit inside any transaction.
go func() {
	defer func() { _ = recover() }()
	s.forecastAlert("predictions.store_failed", "store_failed", map[string]any{
		"report_id": id, "symbol": in.Symbol, "rtype": in.Rtype, "made_at": madeAt, "error": first.Error()})
}()
```

It runs after `InsertPredictionSet` has returned (so outside its transaction), on a detached context with the alert's
own timeouts, with its own `recover()`. One alert per failed ingest; there is no de-duplication (the runbook row says
"each"), and a failure of the alert itself is recorded in `forecast_alert_state`, never raised to the producer.

| Route | Class | Contract |
|---|---|---|
| `GET /api/admin/forecast` | admin | adds `alerts: {email_to, has_slack, last: {email:{at,ok,error}, slack:{…}}, test_ok_at}` — never the Slack URL |
| `POST /api/admin/forecast` | admin | accepts `alert_email_to` (each address validated; 400 `forecast_bad_alert_email`) and `alert_slack_url` (`""` clears; non-https or another host → 400 `forecast_bad_slack_url`); audited `forecast.alert` with the changed field names only (no values), the `changedSettingFields` convention (`audit.go:233`) |
| `POST /api/admin/forecast/alert-test` | admin | **New.** Sends a test alert to both channels; 200 with per-channel results; `forecast_alert_test_ok_at` is written only when **both** succeed; otherwise 502 `forecast_alert_failed` with the per-channel errors. Audited `forecast.alert.test`. |

**UI.** `ForecastAlerts` section on `/manage/forecast`: recipients input, Slack URL input (placeholder shows "set"
when `has_slack`), Save, "Send test alert", last delivery per channel. States: empty (no channels: a warning that
readiness item 5 cannot pass), saving, error, test success, test partial failure.

**Tests.**
- `TestAlertGoesToBothChannels`: one watchdog event → one email (test seam) and one Slack POST (httptest server
  registered through a test-only host allowance) with the reason in the text. BI
- `TestSlackURLIsWriteOnlyAndHostLocked`: GET never returns it; `http://`, other hosts and IP literals → 400. FC
- `TestAlertTestOKOnlyWhenBothSucceed`: email disabled → 502 and no `test_ok_at`. FC
- `TestAlertAuditNamesFieldsNotValues`. FC
- `TestStoreFailedAlertsBothChannelsWithoutDelayingTheIngest`: an ingest whose two insert attempts fail (injected)
  still answers `200` with `predictions: store_failed` while the Slack stub blocks for 5 seconds (the response time is
  independent of the stub), then exactly one email and one Slack POST arrive carrying the report id, and exactly one
  `predictions.store_failed` webhook delivery exists (not two). A successful ingest sends nothing. BI
- `TestStoreFailedAlertRunsOutsideAnyTransaction`: SQLite, 2-second deadline; the alert path makes no pool call before
  the insert's transaction has ended, and a panic inside a channel sender does not reach the request. FC

**Rollback.** Revert; the meta keys are ignored.

### 6.3 P10 `fix/forecast-reading-hotfix` — reading-page correctness (C1–C5, C7, C8)

**Goal.** Stop the reading page saying false things before any mark resolves.

**Files.** `web/src/pages/StockPage.tsx`, `web/src/components/PredictionPanel.tsx`, `web/src/components/PriceChart.tsx`,
`web/src/lib/forecast.ts`, `internal/app/forecast_read_api.go`, `internal/app/forecast_read_store.go`,
`internal/app/forecast_schedule.go` (a projection-only helper for the read path), locales.

| Defect | Fix |
|---|---|
| C1 stale sets after a report switch (`StockPage.tsx:191-211`) | Clear `predSets` synchronously when `repId` changes, before the request; ignore a response whose `repId` is not current. |
| C2 a failed request renders "no forecast" (`StockPage.tsx:205-207`) | An error state with an inline retry; the report body always renders (FO). |
| C3 every not-due mark reads "Fetch failed (retryable)" (`PredictionPanel.tsx:100`, `en-US.json:884`) | The server adds `phase` and `not_before` per mark from `predScheduleFor` over a weekday projection (P1): `waiting_s0`/`waiting` "on or after <date>"; `retrying` only when `session_date != ''`; `resolved`; `final`. Keys `forecast.mark.refused` / `refused_final` are replaced by `forecast.phase.*`. |
| C4 "Not counted" on every set (`PredictionPanel.tsx:73-77`) | The badge is removed until P16/P20 bring real membership. |
| C5 raw payload to every reader (`StockPage.tsx:199`, `forecast_read_api.go:43-47`) | `serveReportPredictions` and `serveQueryPredictions` ignore `include=payload` whenever the caller's scope is non-nil. The session routes keep `include=payload` for unrestricted readers. |
| C7 raw quality enum (`PredictionPanel.tsx:81`) | Translated through `forecast.quality.{normal,degraded,repaired}`. |
| C8 a marker on a date with no bar is dropped; a stray `0` when both arrays are empty (`PriceChart.tsx:825-829`) | Markers snap to the first bar on or after their date; the overlay group renders only when an array is non-empty (`(levels?.length ?? 0) > 0 \|\| (markers?.length ?? 0) > 0`). |

**Tests.**
- `TestPayloadWithheldFromScopedReader`: on the session and v1 routes, a scoped caller gets no `payload` with
  `include=payload`; a nil scope does. FC
- `TestReadPhaseNeverClaimsRetryingWithoutEvidence`: the production 20-mark tuple (after the heal: pending) reads
  `waiting` with `not_before` 2026-10-19 / 2026-10-26; a refused mark with `session_date=''` never reads
  `retrying`. FC
- vitest `StockPage` W1: switching report while B is in flight removes A's panel and overlay at once. FC
- vitest W3: a rejected predictions request still renders the body, with an inline retry. FO
- vitest: the production fixture renders "on or after 2026-10-19" and never "Fetch failed". FC
- vitest `PriceChart`: empty arrays give the same DOM as no props; a Sunday marker snaps to Monday. BI
- vitest `PredictionPanel`: no "counted" badge; the quality value is translated.

**Rollback.** Revert.

### 6.4 P38 `feat/forecast-access-schema` — step `0006`, the OU index, frozen attribution at ingest

**Goal.** Ship the approved DDL (§3.2), a strict one-read OU index the resolver, P30 and P31 use, and freeze each
set's attribution from the signed owner token at insert. No reader yet: the scope column is unused until P26.

**Files.** `internal/app/migrate_steps.go`, `internal/app/ou_index.go` (new), `internal/app/forecast_ingest.go`
(`predSetRow.AttributedOU *int64`), `internal/app/forecast_store.go`, `internal/app/apiv1.go`,
`internal/app/user_admin.go` (`EnsureDefaultForecastScope`; `DeleteUserGroup` re-attributes and runs in one transaction), `internal/app/user_admin_api.go` (`apiGroupDelete`: audit detail, cache purge),
`internal/app/store.go` (call at boot, next to `store.go:279`), `internal/app/backup.go` (normalise after restore).

```go
// ou_index.go

// ouIndex is the whole OU table read in ONE statement, with every failure surfaced. It is the strict
// counterpart of groupChain/EffectiveGroupSettings, which swallow errors (user_admin.go:439, plan D3).
type ouIndex struct {
	defaultID int64
	ous       map[int64]ouRow
}
type ouRow struct {
	parent     int64
	restricted bool    // the OU's own flag
	scope      *string // forecast_scope; nil = inherit
}

func (s *Store) loadOUIndex() (ouIndex, error) // SELECT id, parent_id, COALESCE(restricted,0), is_default, forecast_scope FROM user_groups

var (
	errDanglingOU = errors.New("ou chain: parent_id names an OU that is not in the index")
	errCyclicOU   = errors.New("ou chain: cycle or depth over 64")
)

// chain returns leaf → root, with the Default OU appended when the walk reaches a NULL parent without passing
// it — the same shape groupAncestry produces (user_admin.go:493-507), from memory. A walk that cannot be trusted
// returns an error and no ids: a non-NULL parent_id that is missing from the index (errDanglingOU), or a cycle
// or a depth over 64 (errCyclicOU). A truncated chain would drop a restricted ancestor and fail OPEN for the
// external flag, so it is never returned.
func (ix ouIndex) chain(leaf int64) ([]int64, error)
// restrictedEffective is sticky (the OU or any ancestor). It returns chain's error unchanged; P30 and P31 treat
// that error exactly like a failed index load (less sharing, a scope of nothing).
func (ix ouIndex) restrictedEffective(id int64) (bool, error)
func (ix ouIndex) subtree(root int64) []int64 // root and its descendants by parent_id, sorted;
                                              // the same edges subtreeIDs uses (security_policy.go:317-350)
```

**Ingest.** `v1Ingest` already decodes the token once, at `apiv1.go:316`, and that decode already runs before
`InsertPredictionSet` (`apiv1.go:337` and after). Nothing moves. The change is only to hoist the three results of
`ownerFromToken` out of the `if` scope into function-scope variables, and to set `predSet.AttributedOU = &ou` from them
(when `ok && ou != 0`) where `predSet.Version, predSet.Title, predSet.ReportDate` are set, before the insert call. The
token is **not** decoded a second time. `InsertPredictionSet` writes `attributed_ou` in its INSERT
(`forecast_store.go:45-66`). No other statement in the codebase writes the
column. Attribution comes only from a valid signed token; a plain client field cannot set it. Today only restricted
OUs' runs carry a token (`runInputs`, `report_owner.go:67-81`), so until P39b only external sets are attributed. The
column is never serialised (`PredictionSet` in `forecast_read_store.go:27-74` does not gain it).

**Deleting an OU re-attributes its sets (decided 2026-10-19; replaces the 409 `group_has_forecast_sets` guard).** The OU
delete path is `apiGroupDelete` (`user_admin_api.go:419`) calling `(*Store).DeleteUserGroup` (`user_admin.go:169-186`,
its only caller). Today `DeleteUserGroup` runs seven separate `s.exec` calls with no transaction (clear
`users.group_id`, delete `report_viewers`, `version_grants`, `announcement_grants` and `group_targets` rows, null the
children's `parent_id`, delete the row). P38 rewrites it as one transaction on the `*sql.Tx` handle only (the
single-connection rule, §0.1) that first reads the OU's `parent_id` and `is_default`, refuses the Default OU as today,
and then, before the existing statements, runs

```go
// target = the deleted OU's parent_id, or the Default OU's id when parent_id is NULL (a root OU).
UPDATE prediction_sets SET attributed_ou = ? WHERE attributed_ou = ?   // target, deleted id
```

and returns `(reattributed int64, target int64, err error)` from `RowsAffected`. If the parent id is non-NULL but
names no row (a dangling parent), the target falls back to the Default OU, the same fail-closed direction as
`errDanglingOU`. The handler writes the existing `group.delete` audit row after the commit (audit writes use the pool
and so cannot run inside the transaction) with two added detail keys, `forecast_sets_reattributed` and
`forecast_sets_to_ou`, always present (0 when nothing moved). It then clears `forecastGroupsCache` under
`forecastGroupsMu`, because a root OU's sets join the Default OU's population while the Default OU's subtree ids, and
so its population key, do not change. No set references a deleted id afterwards, which also removes the id-reuse risk
of §3.2. Attribution of every other set stays frozen at insert; this statement is the one sanctioned exception to
"attribution frozen at insert", and `TestNothingElseWritesAttributedOU` allow-lists it by name. The ADR 0039 row in §14
records the exception.

**Tests.**
- `TestMigration0006` on SQLite and Postgres: applies on a `0001`–`0005` database; idempotent (second run no-op; a
  hand-added column is accepted); a hand-dropped column is refused at the next start by `verifyMigrationProducts`;
  an older dump (declaring `0001`–`0005`) restores with both columns NULL and the Default OU then reads `none`. FC
- `TestDefaultOUScopeIsNormalisedToNone`: after boot and after restore, Default holds `'none'`; a second call
  changes nothing. FC
- `TestSetAttributionIsFrozenAtInsert`: an ingest carrying a token for OU A writes `attributed_ou = A`; moving the
  token's user to OU B, changing A's parent and A's scope leaves the set at A; a later re-ingest of the same report
  with a changed payload by a user in B creates a new set at B and leaves the first at A. FC
- `TestInvalidOrMissingTokenLeavesSetsUnattributed`: missing, expired, tampered and wrong-prefix tokens → NULL. FC
- `TestNothingElseWritesAttributedOU`: source scan; the only statements naming `attributed_ou` with INSERT or UPDATE
  are the INSERT in `InsertPredictionSet` and the single re-attribution UPDATE in `DeleteUserGroup`. FC
- `TestAttributionNeverSerialised`: no read route's JSON contains `attributed_ou`. FC
- `TestOUIndexSurfacesErrors`: a closed store returns an error, never an empty index. FC
- `TestOUChainRefusesADanglingOrCyclicParent`: an OU whose `parent_id` names a deleted row gives `errDanglingOU` from
  `chain` and from `restrictedEffective` (never `false`, never a shortened chain); a two-OU cycle and a 70-deep chain
  give `errCyclicOU`; a root with a NULL parent is **not** an error and gets the Default OU appended. FC
- `TestDeletingOUMovesChildrenToItsParent` and `TestDeletingRootOUKeepsChildrenAsRoots`: see §16 item 9 (P38 moves a deleted OU's children to its parent in the same transaction).
- `TestDeletingOUReattributesSetsToParent`: OU C (parent B, grandparent A) with two attributed sets and a sibling's
  set; after the delete the two sets read `attributed_ou = B`, the sibling's set is untouched, the `own` population of
  B and of A each gain the two sets, and C's former members' scores are unaffected. BI
- `TestDeletingRootOUReattributesSetsToDefault`: a root OU's sets move to the Default OU; they appear in no other
  OU's `own` population and in an `all` or admin population as before; with Default stored `own`, they appear in the
  Default OU's `own` population. BI
- `TestDeletingOUWritesAuditCounts`: the `group.delete` row carries `forecast_sets_reattributed` = the number moved and
  `forecast_sets_to_ou` = the target id; both keys are present (0) when the OU had no sets. FC
- `TestDeleteOUReattributionIsAtomic`: a failure injected after the UPDATE and before the final DELETE (a closed
  statement or a forced error) leaves the OU, its sets' `attributed_ou` and its principal rows unchanged; under a
  2-second context deadline on SQLite, so a stray pool call fails fast. FC
- `TestDeletingOUPurgesForecastCache`: a cached mature `own`/Default cell is recomputed after a root OU's delete moves
  sets into the Default OU. FC
- `TestDeletedOUIdIsNeverReferencedBySets`: after any sequence of OU deletes, no `prediction_sets.attributed_ou` names
  a missing `user_groups.id`. BI
- `TestReusedOUIdCarriesNoScope`: a restored database where an id is reassigned to a new OU — the new OU has
  `forecast_scope` NULL (inherits). BI

The deletion tests run on SQLite and, under `TEST_POSTGRES_DSN`, on Postgres.

**Rollout.** ADR 0027 backup, then deploy. **Rollback.** Restore-only (§3.2).

### 6.5 P39a `fix/dify-reserved-inputs` — hide and strip reserved `_rp_*` inputs

**Goal.** Before the 16 Dify workflows that declare `_rp_owner_token` are published and a target refresh copies it
into a stored input list, make the portal treat every `_rp_*` input as reserved: never shown, never accepted from a
client, never in a CSV template.

**Files.** `internal/app/report_owner.go` (`isReservedInput`), `internal/app/dify_api.go` (probe path, after
`c.Parameters`, `dify_api.go:63`), `internal/app/dify_refresh.go` (refresh path, `dify_refresh.go:185-200`),
`internal/app/dify_provider.go` (`difyTargetConfig` gains `ReservedInputs []string \`json:"reserved_inputs,omitempty"\``
at `dify_provider.go:34-38`; `difyTargetInputs`, `dify_provider.go:451-455`, filters stored configs too; a new
`difyTargetReserved(configJSON) []string`), the batch submit handler (strip `_rp_*` keys from every item's inputs before they are stored), `report_owner.go`
`runInputs` (delete any `_rp_*` key from the copy before injecting).

```go
// isReservedInput reports whether a Dify input name belongs to the portal. Reserved inputs are filled by
// the portal at run time (runInputs) and are never part of a target's user-facing input list.
func isReservedInput(name string) bool { return strings.HasPrefix(name, "_rp_") }
func dropReservedInputs(in []dify.Input) []dify.Input

// reservedInputNames returns the sorted names of the reserved inputs a workflow's /parameters declares. The probe and
// the refresh store them in the target's config JSON as reserved_inputs (a JSON field of an existing TEXT column:
// no schema change), BEFORE dropReservedInputs removes them from the user-facing list. Today difyTargetInputs
// (dify_provider.go:451-455) returns the stored Inputs only and the config has no such field, so without this
// recording P39b's "inject only where the workflow declares the key" branch (§11.1 item 4) would have nothing to read.
func reservedInputNames(in []dify.Input) []string
```

A refresh replaces `reserved_inputs` with what the workflow declares now (a workflow that stops declaring the input
stops receiving it); a target editor save round-trips the field like `output_subtype` and `symbol_input` do, so
re-marshalling the config never wipes it.

**Tests.**
- `TestReservedInputsNeverReachTheRunForm`: a probe and a refresh whose `/parameters` declares `_rp_owner_token`
  store and return an input list without it; an already-stored config that contains it is filtered on read. FC
- `TestClientCannotSupplyAReservedInput`: a submitted item with `_rp_owner_token` is stored without it, and
  `runInputs` never forwards a caller-supplied value (for an internal user before P39b the key is absent; for a
  restricted user the minted token replaces it). FC
- `TestProbeAndRefreshRecordReservedInputs`: a probe and a refresh whose `/parameters` declares `_rp_owner_token`
  store `reserved_inputs: ["_rp_owner_token"]` and an `inputs` list without it; a workflow that does not declare it
  stores no `reserved_inputs`; a second refresh after the workflow drops the declaration clears it; a target-editor
  save that does not touch the field leaves it intact. FC (the P39b branch of §11.1 item 4 reads a recorded fact)
- The existing `TestRunInputsInjectsOwnerTokenForRestrictedOnly` (`run_inputs_test.go:13`) stays green.

**Rollback.** Revert.

### 6.6 P39b — owner token for every run

The full section is §11, because it changes an ADR, depends on a release in another repository, and carries its own
rollout ordering.

### 6.7 P11 `docs/forecast-adr-0037` — ADR 0037 and the ADR 0029 amendments for Release 1

ADR 0037 "Forecast verification operations" and the in-place notes in ADR 0029 and the design document listed in
§14 for Release 1. Depends on R1a being merged (it describes what the code does). No tests.

### 6.8 P30 `fix/tenancy-restricted-ou-principals` — OU principals only for effectively restricted OUs (D4, V16)

**Goal.** `AddReportViewer` writes `g:<OU>` for any OU (`version.go:381-394`), and a restricted reader's principals
include every OU up its chain (`viewerScope`, `report_owner.go:162-185`), Default included. Under a
`visibility=group` version, a report requested by an account-restricted user in an unrestricted OU becomes readable
by other tenants whose chains pass through that OU.

**Depends on P38** (`ouIndex.restrictedEffective` and `loadOUIndex` are defined there). This PR is the single owner of
the group-principal rule; P39b (§11) relies on it and repeats nothing at its call site.

**Change.** Writer: `AddReportViewer` writes `g:<OU>` only when `ouIndex.restrictedEffective(ou)` returns `true, nil`;
on an index error or a broken chain (`errDanglingOU`, `errCyclicOU`) it writes the `u:` row only (fail-closed: less
sharing, never more). The rule lives inside `AddReportViewer`, so every caller (`v1Ingest` today, any later one) gets it. Reader: `viewerScope` adds `g:<id>` only for
chain OUs that are effectively restricted. Existing `g:<unrestricted OU>` rows are left in place and simply never
match again. Run Q5 (§1.5) first and attach its counts to the PR.

**Tests.** `TestOnlyRestrictedOUsShare`: (a) two unassigned account-restricted users do not see each other's reports;
(b) a user in an unrestricted container OU is not visible to its tenant children; (c) two members of one restricted
tenant still share. (a) and (b) fail on `main`; (c) guards against over-narrowing. BI. If (a) and (b) cannot be made
to fail on `main`, the PR becomes test-only.

Also in this PR (they fail on `main`; P39b's integration tests then rely on them):
- `TestIngestWritesGroupPrincipalOnlyForRestrictedOUs`: `v1Ingest` with a valid owner token for an internal
  (unrestricted) OU writes `u:<user>` and no `g:` row; a token for a restricted OU writes both. FC
- `TestOUIndexErrorWritesNoGroupPrincipal`: with `loadOUIndex` failing, and with a dangling parent on the OU's chain,
  `AddReportViewer` writes the `u:` row only. FC

### 6.9 P31 `fix/tenancy-strict-restricted-read` — an unreadable restricted flag scopes to nothing (D3, V16)

**Goal.** `isRestricted` (`version.go:363-371`) reads leniently: `GetUser` returns nil on error and the OU walk
ignores errors, so a read failure reads as "not restricted" and every scope built on it fails open for that request.

**Depends on P38** (`loadOUIndex`, `restrictedEffective`).

**Change.** `viewerScope` uses a strict form (`isRestrictedStrict(user) (bool, error)` over the account row and
`loadOUIndex`); on error, including a broken OU chain (`errDanglingOU`, `errCyclicOU`), it returns a scope whose
`where` is `1=0`.

**Tests.** `TestScopeOnUnreadableRestrictedFlag`: with the user read or the OU read failing, the reading routes
return nothing, not everything. FC

---

## 7. Release 2a: access (schema-free; reads step `0006`)

### 7.1 P13 `docs/forecast-demo-v2` — production baseline screenshots, then the demo revision

Screenshots of `/manage/forecast` and of the 600519 reading page at 1280 and 375 px, light and dark, saved under
`~/Downloads/forecast-baseline/` (the owner captures them, or authorises a read-only browser session); then the demo
HTML gains the states it lacks (waiting with "on or after"/"expected", pending reasons, final small cells,
`pre_live`/`test`/`off_benchmark` chips, the evaluator status pill, data-source states, an edition without a set,
the disclaimer, the scope states of §10 — "not enough data" for a small `own` population, the external `all` view
without volume), corrects its projected d20 and d60 to ADR 0029 §4, and shows the `/forecast` placement (V12).
Appendix A's "Today" column is corrected from the screenshots. Gate: the owner reviews it; it blocks P19 and P20.

### 7.2 P15 `feat/forecast-web-foundations`

Palette and format helpers, legend, shared methodology card, `.rp-sr-only`; copy fixes (the hit-rate help says
"|upside| ≥ 5 %; flat counts as a miss"); the `forecast.disclaimer` key — zh-CN 「研究验证用途，不构成投资建议」, en-US
"For research verification only. Not investment advice.", zh-TW from OpenCC (s2twp), shipped machine-generated and flagged for human review — the zh-CN and en-US wording is **approved
(owner, 2026-10-19)**; the zh-TW `forecast.*` regeneration with a fixed-point gate; `AuditPage`
reads `?action=`; matrix and drawer moved into `web/src/components/forecast/` with no behaviour change; `axe-core`
as a devDependency (V17).

**Tests.** Palette contrast ≥ 4.5:1 against both antd algorithms (light `red7`/`green8`, dark `red8`/`green6`, seed
`colorPrimary #1677ff`); a source scan forbidding hex and rgba literals in forecast components; zh-TW fixed point;
the `AuditPage` deep link; the existing matrix and drawer tests stay green.

### 7.3 P26 `feat/forecast-record-access` — the scope resolver, reader routes, external protections, compute governance

**Goal.** Implement the access model: every non-admin forecast surface — the record, its routes, the reading-page
data routes, the v1 prediction reads for cookie and bridge callers — asks one pure resolver over one consistent
read.

**Files.** `internal/app/forecast_access.go` (new), `internal/app/forecast_groups_api.go`,
`internal/app/forecast_groups_members.go`, `internal/app/forecast_groups_store.go`, `internal/app/forecast_read_api.go`,
`internal/app/forecast_compute.go` (new: pools, flights, per-user limits), `internal/app/apps_token.go`,
`internal/app/apps_api.go`, `internal/app/apiui.go` (`meJSON`), `internal/app/user_admin_api.go`,
`internal/app/routes.go` (the `gate` field, §7.3.4), `internal/app/routes_test.go`, `internal/app/limits.go`,
`internal/app/audit.go`, and the web side of the 403: `web/src/pages/StockPage.tsx`, `web/src/pages/StockPage.test.tsx`,
`web/src/api/types.ts` (`Me.forecast`), `web/src/auth.tsx` (exposes `me.forecast`).

#### 7.3.1 The resolver

```go
// forecast_access.go

type forecastSubject struct {
	User              string
	Admin             bool  // can(role, PermManage), the same rule as isAdmin (server.go:692-695)
	RestrictedAccount bool  // users.restricted
	GroupID           int64 // users.group_id; 0 = unassigned
}

type forecastDecision struct {
	Scope    forecastScope
	External bool    // account or any chain OU restricted: the external protections apply
	Leaf     int64   // the OU 'own' is rooted at (primary group, or Default when unassigned)
	Subtree  []int64 // sorted; set only for scopeOwn
	Source   int64   // the OU whose value decided; 0 for admin and failures
	Reason   string  // admin | ou | default_null | unknown_value | dangling_group | cyclic_chain | error | anonymous
}

// loadForecastSubject reads the account row in ONE statement (role, restricted, group_id, active) and the
// OU table with loadOUIndex (P38). Any error is returned, never swallowed.
func (s *Server) loadForecastSubject(user string) (forecastSubject, ouIndex, error)

// resolveForecastScope is the rule itself: no I/O, shared by every gate, /api/me and the admin preview.
func resolveForecastScope(sub forecastSubject, ix ouIndex) forecastDecision

// forecastAccess is what handlers call: load + resolve, with every failure a denial.
func (s *Server) forecastAccess(user string) forecastDecision
```

```
resolveForecastScope(sub, ix):
  if sub.User == ""                         → none (anonymous)
  if sub.Admin                              → all, External=false (admins are never scoped)
  leaf := sub.GroupID; if leaf == 0 { leaf = ix.defaultID }
  if leaf not in ix.ous                     → none (dangling_group)
  chain, err := ix.chain(leaf)              // leaf → root, Default appended
  if err is errDanglingOU                   → none (dangling_group)   // a parent_id missing from the index
  if err is errCyclicOU                     → none (cyclic_chain)     // never a shortened chain: it could drop a restricted ancestor
  external := sub.RestrictedAccount || any(ix.ous[c].restricted for c in chain)
  for c in chain:                           // nearest set value wins
      v := ix.ous[c].scope
      if v == nil: continue
      switch *v { case "all","own","none": scope, source = *v, c; goto found
                  default: → none (unknown_value) }         // fail-closed on a value this build does not know
  → none (default_null)                     // Default normally stores 'none'; NULL there still reads none
found:
  if scope == own: subtree := ix.subtree(leaf)   // parent_id edges only. An OU may be parented under Default, and then it
                                                 // IS in Default's subtree; a root OU (NULL parent) is not. The Default
                                                 // OU appended to a chain serves inheritance, never the subtree.
  return {scope, external, leaf, subtree, source, "ou"}
```

- **A broken chain is `none`, never a guess.** A `parent_id` that names an OU missing from the index (a corrupted
  restore; before P38, `DeleteUserGroup` cleared the children of a deleted OU, `user_admin.go:183`; P38 moves them to the deleted OU's parent, §16 item 9) resolves to `none` with reason
  `dangling_group`; a cycle or a depth over 64 resolves to `none` with `cyclic_chain`. Without this the walk would stop
  at the missing row, the external flag would be read from a shortened chain, and a tenant under a restricted ancestor
  would be treated as internal.
- **No lenient helper.** The gate never calls `isRestricted`, `isAdmin`, `GetUser`, `groupChain`,
  `EffectiveGroupSettings` or `GroupRestrictedEffective`. A source-scan test enforces it.
- **No caching of the decision.** A user moved by an SSO rule gets the new decision on the next request.
- **`own` and the tree.** Attribution is frozen per set; the subtree is read live (decided 2026-10-19, §16 item 1).
  Moving users never moves a set. An OU that is re-parented takes the new parent's inherited settings, and its
  attributed sets count under the new ancestors' `own` and leave the former ancestors'. Deleting an OU is the one case
  where a set's attribution changes: P38 re-attributes its sets to the parent OU (to the Default OU for a root).
- **A restricted account in an unrestricted OU** gets its OU's scope with the external protections.

#### 7.3.2 Populations, membership and the cache

```go
// forecastPopulation is whose sets a request computes over.
type forecastPopulation struct {
	Key string  // "all", or "ou:" + hex(sha256(sorted subtree ids)) — a re-parent gives a new key
	OUs []int64 // nil = every set; non-nil = attributed_ou IN (OUs); empty = nothing (1=0)
}

func populationFor(d forecastDecision) forecastPopulation
func (s *Store) loadForecastGroupSets(rtype, from, to string, pop forecastPopulation) ([]*groupSet, error)
```

- **Membership is computed inside the population.** D4 ("earliest accepted set per symbol per group") is applied
  to the population's own sets, so an `own` population's numbers never depend on another OU's activity and reveal
  nothing about it. The cohort's benchmark label (`cohortBench`, P4) is the ledger-wide one.
- **Cache key** = `forecastGroupCellKey(rtype, start, end, cp)` + `"\x00" + pop.Key`. `all` keeps today's key
  shape, so its cache and its golden snapshot are unchanged.
- **Seed.** `seedParts` stays `{rtype, start, end}` for `all` (golden snapshots unchanged); for `own` it appends
  `pop.Key`.
- **n ≥ 30** and the other three maturity gates apply to every population unchanged; a small `own` population
  shows "not enough data", which is correct.

#### 7.3.3 External protections (`decision.External`)

| Protection | Mechanism | Refusal / omission |
|---|---|---|
| Official monthly view only | `mode=custom` on the groups or members route | 403 `forecast_custom_not_allowed` |
| No site-wide volume | For `External && Scope == all`, **conservative default** (owner question 8, §16): the response omits `coverage` on every group and `unrecorded`; a non-mature cell carries only `state` (no `n`, `resolved`, `total`, `refused`, pending reasons); **a mature cell carries its metrics and a boolean `min_n_met: true` (rendered as an "n ≥ 30" badge) but no `n`, no `total`, no `resolved`**, because a mature cell's `n` is a count of ledger members and would reveal the ledger's volume. The members route omits `total`, per-status member counts and any `n` for these viewers (the readable rows themselves are still listed, paginated by cursor with no total), and the CSV has no `n`, coverage or count column. For `External && Scope == own` the coverage and `n` of the viewer's own population are shown (it is their own volume); `unrecorded` is never shown to non-admins. | fields absent (not zero) |
| Drawer lists only readable reports | Members are filtered by `canReadReport(report_id, viewerScope(user))` (`forecast_read_api.go:117-131`); purged sets (`report_id` NULL) are omitted for external viewers (ADR 0029 §5 fail-closed-after-purge) | rows absent |
| n ≥ 30 | unchanged gate | — |
| No membership badge, no payload, no non-accepted sets, no orphaned rows on the reading page | P10/P18 shaping keyed on `External` | fields absent |

#### 7.3.4 Routes

| Route | Class (helper) | Scope gate | Contract | Errors |
|---|---|---|---|---|
| `GET /api/forecast/groups` | session (`s.session`) | `all`/`own` (admin: `all`) | **New.** The groups response over `populationFor(d)`, shaped by §7.3.3. Never `unrecorded`. Compute governance applies. Registered with `gate = "forecast_scope"` (below). | 401 anonymous; 403 `enrolment_required` (wall); 403 `forecast_unavailable` (`none`); 403 `forecast_custom_not_allowed`; 503 `forecast_busy` + `Retry-After`; 429 `forecast_rate_limited` |
| `GET /api/forecast/groups/members` | session (`s.session`) | as above | **New.** Members of one cell in the same population; scores nothing (P16a); outcomes only when mature; rows filtered by §7.3.3. | as above |
| `GET /api/admin/forecast/groups`, `/members` | perm `PermManage` (`routes.go:314-315`) | admin | Unchanged aliases (population `all`, with `unrecorded`) until P29. | — |
| `GET /api/admin/forecast/access` | admin | — | **New.** Per OU: own value, effective scope, source OU, external flag, account counts per effective scope; plus a hypothetical `?ou=<id>&scope=<v>` preview. Resolves every account that can sign in (unassigned and root-OU members included). | 400 `forecast_bad_scope` |
| `GET /api/admin/groups` | admin (`routes.go:290`) | — | Adds `forecast_scope` (own value or null), `forecast_scope_effective`, `forecast_scope_source`, all server-resolved. | — |
| `PUT /api/admin/groups/{id}` | admin (`routes.go:292`) | — | Adds `forecast_scope`, **presence-aware** (decoded through `json.RawMessage`): omitted → unchanged; `null` → inherit (on the Default OU coerced to `"none"`); `"all"`/`"own"`/`"none"` → stored; anything else → 400 `forecast_bad_scope`. Audited in the `group.change` row as `forecast_scope: {from, to}` only when it changes. | 400 `forecast_bad_scope` |
| `GET /api/me` | session (self-resolving, `routes.go:191`) | — | Adds `forecast: {scope, external}` (navigation hints only; the server decides on every route). | — |
| `GET /api/report/{id}/predictions`, `GET /api/predictions` | session (`routes.go:203,206`) | `all`/`own` for non-admins | Unchanged shape (P10 already withholds payload from scoped callers); `none` → 403. | 403 `forecast_unavailable` |
| `GET /api/v1/reports/{id}/predictions`, `GET /api/v1/predictions` | bearer (`routes.go:184-185`) | cookie caller: the cookie user's decision; bridge token: the minting user's decision; persistent machine token: unscoped | Cookie: the session-route shape. Bridge: accepted sets only, no payload, no errors, no membership. Machine: the machine shape. | 401; 403 `enrolment_required` (#108); 403 `forecast_unavailable` |

**The `gate` annotation does not exist yet; P26 adds it.** `routeRow` today has `pattern`, `class` and `perm`
(`routes.go:44-48`), so a test cannot ask which routes sit behind the forecast scope. This PR adds
`gate string` to `routeRow` and one registration helper next to the class helpers:

```go
// gateForecastScope marks a session route whose handler must call forecastAccess before it reads the ledger.
const gateForecastScope = "forecast_scope"

// sessionGated is s.session plus the annotation. route() becomes a thin wrapper over routeGated(..., "", ...), so
// every existing registration keeps gate == "".
func (s *Server) sessionGated(mux *http.ServeMux, gate, pattern string, h handler) {
	s.routeGated(mux, pattern, authSession, "", gate, s.requireUserJSON(h))
}
```

`GET /api/forecast/groups` and `GET /api/forecast/groups/members` are registered through
`s.sessionGated(mux, gateForecastScope, …)`; nothing else sets a gate. The annotation is a claim, so a test makes it
true (`TestForecastGatedRoutesDenyANoneUser`, below): a table row that says `forecast_scope` but whose handler skips
`forecastAccess` fails there.

**Bridge tokens carry their user.** `appTokenEntry` (`apps_token.go:52-55`) gains `user string`; `mint(scopes,
user, now)`; a new `lookup(tok, now) (user string, ok bool)`. Tokens stay in memory only (no schema). The two v1
prediction handlers branch on the credential: cookie (`currentActiveUser`), bridge (`appTok.lookup`), or persistent
token (`s.st.TokenValid`).

**Statistics are never reachable by a token.** The reader routes are session routes; a Bearer token alone gets 401.

#### 7.3.5 Compute governance (before any non-admin can trigger scoring)

- Only uncached scoring of a mature cell takes a slot. Two pools: official (`forecast_slots_official`, 1–8,
  default 2) and exploratory (`forecast_slots_explore`, 0–8, default 2).
- Identical requests (same population key, rtype, range, checkpoint) share one computation on a detached context,
  with the hand-rolled flight pattern of `quote_cache.go:745-770`; a cancelled leader still serves its followers.
- A request that waits longer than `forecast_wait_sec` (1–60, default 10) gets 503 `forecast_busy` with
  `Retry-After`.
- Per user: custom mode `forecast_custom_per_min` (0–600, default 20) with one distinct query in flight; members
  route `forecast_members_per_min` (0–600, default 60). Exceeding → 429 `forecast_rate_limited`.
- Every limit is a bounded setting on the existing limits form (`limitsJSON`, `limits.go:170`), audited by the
  existing limits audit.
- Weekly distinct non-admin viewers of `/api/forecast/groups`: a random per-ISO-week salt **held in process memory
  only**; each view adds `HMAC(salt, username)` to that week's in-memory set; the hourly flush writes **only the count**
  to `forecast_viewers_<week>` (an integer: base + the set's size). Neither the salt nor any hash is ever written to
  `meta` or anywhere else, so a backup holds no value derived from a username. At week end the in-memory set and salt
  are dropped and the count stays. **Accepted limit:** a process restart mid-week empties the set and draws a new
  salt, so a viewer seen again after the restart is counted again: the figure is then an upper bound on distinct
  weekly viewers, which is enough for an adoption indicator. The flush adds the in-memory set's size to the count
  persisted at the last flush's restart point, never to a value it re-reads on every flush. There is no weekly adoption target for now: the figure is measured for one month after R2a, then the owner sets a target (§16 item 5).

**Tests.**
- `TestForecastAccessMatrix` (§10.2), covering the record, admin, reading-data and v1 rows. FC/BI
- `TestForecastScopeNearestOUWins`: leaf `own` under a parent `all` → `own`; leaf NULL under `own` → `own`; NULL all
  the way → `none`. BI
- `TestForecastScopeDefaultsToNone`: a fresh OU tree with no values → every non-admin `none`. FC
- `TestForecastScopeUnknownValueIsNone`: a stored `"everyone"` → `none` with reason `unknown_value`. FC
- `TestForecastScopeDeniesOnStoreError`: each of the two reads failing → `none` with reason `error`. FC
- `TestForecastScopeHasNoLenientRead`: source scan of `forecast_access.go` and the gate call sites. FC
- `TestForecastAdminAlwaysAll`: an admin in an OU at `none` gets `all`; `Admin` matches `isAdmin` for every role in
  `roleRegistry`. BI
- `TestOwnPopulationIsTheSubtreeByAttribution`: sets attributed to the leaf and a child count; a sibling's and
  unattributed sets do not; moving a user does not change it; re-parenting a child out removes its sets and yields
  a new population key. BI
- `TestOwnMembershipIsComputedInsideThePopulation`: OU A's later set is a member of A's `own` cell even though OU B
  ran the symbol earlier; A's numbers are byte-identical with and without B's sets. FC (isolation)
- `TestExternalAllHasNoVolume`: an external `all` response contains no `coverage`, no `unrecorded`, non-mature
  cells with `state` only, and **no `n`, `total`, `resolved` or member count on any cell, mature or not** (mature cells
  carry their metrics and `min_n_met: true`); the members response has no `total` or per-status counts; the CSV has no
  `n` or count column. The same fixture for an external `own` viewer still carries `n` and `coverage`, so the test
  cannot be satisfied by removing the fields everywhere. FC/BI
- `TestExternalCustomRangeRefused`. FC
- `TestExternalDrawerListsOnlyReadableReports`: unreadable and purged members are absent. FC
- `TestForecastRoutesAreSessionRoutesBehindTheWall`: every `/api/forecast/*` pattern is in `sessionRoutes()`, not
  in `enrolmentPaths`, and has `routeRow.gate == gateForecastScope`; and no row outside `/api/forecast/` carries that
  gate. FC
- `TestForecastGatedRoutesDenyANoneUser`: for every row in `routeTable` with `gate == gateForecastScope`, a request by
  a `none` user answers 403 `forecast_unavailable`, an anonymous one 401, and a user held at the enrolment wall 403
  `enrolment_required`; the test iterates the table, so a new gated route is covered without editing it. FC
- `TestForecastStatsNotReachableByBearer`: persistent and bridge tokens → 401 on both reader routes. FC
- `TestReaderAndAdminGetIdenticalAllRecords`: on an injected clock, an internal `all` reader and the admin alias get
  identical cells (minus `unrecorded`). BI
- `TestMeForecastMatchesTheGate`: for every matrix subject, `/api/me.forecast.scope` equals the route outcome. BI
- `TestGroupSaveForecastScope`: omitted → unchanged; `null` → NULL (Default → `none`); bad value → 400; audit only on
  change. FC
- `TestBridgeTokenCarriesItsUser`: a token minted by a `none` user → 403 on v1 prediction reads; by an `all` user →
  accepted sets only, no payload. FC
- `TestV1CookieAppliesForecastScope`: a cookie of a `none` user → 403 `forecast_unavailable` on both v1 prediction
  reads (after passing #108's wall). FC
- `TestForecastComputeIsBounded`: cached official requests never wait; exploratory saturation → 503 with
  `Retry-After`; identical requests share one scoring; a cancelled leader still serves followers; exploratory load
  cannot starve the official pool. FC (capacity)
- `TestForecastPerUserLimits`, `TestForecastLimitsAreSettings`.
- `TestForecastAccessPreviewMatchesRealAccess`: unassigned accounts and root-OU members are counted; the preview of
  a hypothetical value equals the resolver run on a modified index. BI
- `TestWeeklyViewersStoreNoUsernames`: after viewers from three accounts and a flush, `meta` holds one
  `forecast_viewers_<week>` row whose value is a bare integer; no `meta` row, no backup dump and no log line contains
  a username, a salt, or any 32- or 64-character hex string derived from one; a simulated restart mid-week continues
  from the persisted count (an upper bound) and draws a different salt. FC (privacy)
- `TestForecastScopeOwnOnDefaultCoversItsChildren`: with Default stored `own`, an unassigned member's population is
  Default plus the OUs parented under it (not a root OU); a member of an OU parented under Default (NULL scope,
  inheriting `own` from Default) has a population of that OU's subtree only. BI
- `TestForecastScopeDanglingParentIsNone`: a tenant OU whose `parent_id` names a deleted row resolves to `none`
  with reason `dangling_group` (not `all` through a shortened, unrestricted chain); a two-OU cycle resolves to `none`
  with `cyclic_chain`; both fail on a resolver that stops the walk at a missing row. FC
- vitest `StockPage` (the web side of this PR, which would otherwise show P10's retry error to readers at `none`
  between R2a and R2b): with `me.forecast.scope === 'none'` the page issues **no** request to
  `/api/report/{id}/predictions` or `/api/predictions` and renders no forecast DOM; with `me.forecast` absent (an older
  server) it asks as today; a `403 forecast_unavailable` on either request (a stale `me`) renders nothing for the
  forecast, shows no error and no retry button, and does not retry, while the report body renders (FO); any other
  failure still shows P10's inline error with retry. FC/FO
- PG variants of the population loader.

**Rollout.** Deploy (R2a is schema-free: step `0006` shipped in R1b). Every OU is `none`, so only admins see anything,
and **non-admin members of OUs at `none` lose the reading-page card they have today** — that is the decided model.
The owner then sets `安禅内部` to `all` in the OU editor (P27). **Rollback.** Fastest: set the OU values back to
`none` (audited, no deploy). Binary: revert R2a (safe, no schema).

### 7.4 P27 `feat/forecast-record-page` — `/forecast`, the operations page, the OU editor field

**Files.** `web/src/App.tsx` (route `/forecast`), `web/src/components/AppLayout.tsx` (workbench nav item next to
`AppLayout.tsx:212-215`, shown iff `admin || me.forecast.scope !== 'none'`; breadcrumb), `web/src/pages/ForecastRecordPage.tsx`
(new), `web/src/pages/manage/ForecastPage.tsx` (reduced to operations), `web/src/pages/manage/OrgUnitDetail.tsx`,
`web/src/pages/manage/ouSettings.ts`, `web/src/lib/pageTitle.ts`, `web/src/lib/csv.ts`, locales. The `Me.forecast` type
and the reading page's handling of `forecast_unavailable` shipped in P26 (`StockPage.tsx`, `StockPage.test.tsx`,
`types.ts`, `auth.tsx`); this PR only consumes `me.forecast`.

- **`/forecast`** mounts the shared matrix and drawer from `components/forecast` against `/api/forecast/groups*`
  (never `/api/admin/forecast*`). States: loading skeleton; empty ("no cohort has a result yet; the first d5 is due
  on or after <date>"); `none`/403 → antd `Result` (not a retry loop); `forecast_busy` → at most 3 retries with
  jitter, then an error with a retry button; 429 → not retried; error → `Alert` with retry. The exploratory tab is
  shown to internal readers (V14) and hidden for `me.forecast.external`. A header line shows the population ("All
  reports" / "Reports from <OU> and its sub-units") and the disclaimer.
- **`/manage/forecast`** keeps operations only: status card (P9), settings and benchmark schedule (P4), ledger start
  (P5), alerts (P41), unrecorded (P2), access summary from `/api/admin/forecast/access` with a link to the OU editor.
- **OU editor.** `OrgUnitDetail` gains a "Forecast verification" field rendered with `InheritField`
  (`web/src/pages/manage/InheritField.tsx`): options All / Own unit / None / Inherit (Inherit hidden on Default),
  the inherited value and its source OU from the server, and for an effectively restricted OU a note listing the
  external protections. The form sends the field only when changed.
- **CSV.** Every row carries `mode` and `official`; header lines carry the disclaimer, the population and the
  benchmark per group. External `all` exports have no coverage, `n` or count columns (an "n ≥ 30" flag instead); external
`own` exports keep them.

i18n: `nav.forecast_record`, `forecast.record.*` (title, population_all, population_own, empty, unavailable, busy),
`forecast.scope.{all,own,none,inherit}`, `forecast.scope.help`, `forecast.scope.external_note`,
`forecast.csv.header.*`, `err.forecast_unavailable`, `err.forecast_custom_not_allowed`, `err.forecast_busy`,
`err.forecast_rate_limited`, `err.forecast_bad_scope`.

**Tests (vitest).** The record page never requests `/api/admin/forecast*`; a 403 renders a `Result`;
`forecast_busy` retries at most 3 times with jitter and a 429 is not retried; the nav item shows iff
`admin || scope !== 'none'`; the OU field (Default shows no Inherit; restricted shows the note; unchanged field not
sent); every CSV row is marked; an external `all` export has no `n`, coverage or count column and an external `own` one does;
external hides the exploratory tab; a `none` reader mounting the nav shell and `/forecast` issues no request to a
reading-page forecast route; loading/empty/error states; axe; locale parity.

### 7.5 P28 `test/forecast-e2e-access` — e2e section G

`scripts/e2e_check.py` section G gains: an internal member of an OU at `none` gets 403 on `/api/forecast/groups` and
on `/api/report/{id}/predictions`; after `PUT /api/admin/groups/{id} {"forecast_scope":"all"}` the member gets 200;
`own` returns the subtree population; an external member at `all` gets no `coverage` and 403 on `mode=custom`;
anonymous 401; a PUT without the field leaves it unchanged; the access preview's counts match. The existing
admin-path assertions (`scripts/e2e_check.py:491-500`) stay until P29.

### 7.6 P22 `docs/forecast-adr-0039` — ADR 0039 "Forecast verification for readers"

Records the access model (three-way scope, Google Workspace style inheritance, Default `none`, admins `all`, frozen attribution with the one exception of OU deletion, external
protections), the access matrix of §10, compute governance, the reading-page integration and its stage switch, the
edition rules, and the disclaimer. Supersedes design D6 and rewrites design acceptance condition 19. No tests.

---

## 8. Release 2b: parity, reading-page integration, Tier 1 sources (schema-free)

### 8.1 P14 `chore/forecast-demo-seed` — seeded preview dataset and a dev-only clock

**Goal.** Production cannot show a mature cell before November, and n ≥ 30 may take months. Parity is reviewed on a
deterministic dataset produced by the real ingest and the real evaluator.

**Files.** `internal/app/forecast_demo_seed_test.go` (new, test-only writer), `internal/app/forecast_clock.go` (new),
`internal/app/server.go` (startup refusal), `internal/app/forecast_groups_api.go` (`time.Now()` at
`forecast_groups_api.go:31` goes through the seam), the read path's phase clock.

```go
// forecastNow is the statistics and read-path clock. RP_FORECAST_DEMO_NOW overrides it only when
// version.Version == "dev" (internal/version/version.go:14, the value an unlinked build carries).
func (s *Server) forecastNow() time.Time
// RunServer refuses to start when RP_FORECAST_DEMO_NOW is set and version.Version != "dev".
```

The seed writes a new SQLite file and a `config.yaml` beside it (listening on `127.0.0.1:8791`), refuses to overwrite
either, and produces every Appendix A state, including an `own` population below n = 30 and an external `all` view.
The owner runs:

```bash
go test ./internal/app/ -run TestWriteForecastDemoDB -forecast-demo-out="$HOME/Downloads/forecast-demo/portal.db"
RP_CONFIG="$HOME/Downloads/forecast-demo/config.yaml" RP_FORECAST_DEMO_NOW=2027-06-20T07:00:00Z go run ./cmd/report-portal
```

**Tests.** `TestForecastDemoSeedIsDeterministic` (two runs → identical groups JSON at the fixed clock; every Appendix
A state present; an existing output path refused); `TestDemoClockRefusedInReleaseBuilds` (FC).

### 8.2 P16 `feat/forecast-read-semantics` — statistics read semantics (plan §3.4, minus P16a)

- **One classifier.** `classifyMembership(sets []*groupSet, from time.Time, cohortBench map[string]string) []verdict`
  returns an **ordered slice** (member order feeds the permutation p-values); `forecastMembership` is rebuilt on it.
  Statuses: `member`, `duplicate`, `degraded`, `mismatch`, `pending_baseline`, `pre_live`, `off_benchmark`; buckets
  for `test`, `backtest`, `absent`, `rejected`; an unknown state goes to an explicit `unknown` bucket (coverage
  always adds up; today `forecastMembership` drops it, `forecast_groups.go:115-131`).
- **Cache keyed by the purge marker (B6).** Each request reads `prediction_purges` for the requested months; a purged
  month bypasses the cache and answers `purged`. `PurgedPredictionMonths` (`cleanup_store.go:392`) gains its first
  non-test caller.
- **Read fields.** On sets: `report_date`, `upside_bp` (integer, truncated toward zero), `membership` (internal `all`
  readers and admins only, §10), `display_set_id` (the latest accepted set of this report row whose `body_sha256`
  matches the current body; else the latest accepted set, with the body-changed alert), and the corrected
  `counted_set_id` (the report's set that is its cohort's member in the `all` population, 0 when none; omitted for
  restricted callers). On marks: `phase`, `not_before` (+ `expected` when the projection is calendar-backed, P37),
  `source`, `direction`. `counted_in_group`, `baseline_bar`, `baseline_fen` (declared, never filled,
  `forecast_read_store.go:52-53,70-72`) are removed; `coverage.refused` (never incremented) is removed.
- **Phases** via `predScheduleFor` over the projection, or the evaluator-published sessions when they cover the date:
  `waiting_s0`/`waiting` while the lower bound is in the future; `settling` once it has passed; `retrying` only when
  `session_date != ''`; `overdue` 48 h past the exact due date or 21 days past the lower bound; `resolved`; `final`.
  When the mode is `0` or `dry`, a separate "settlement paused" notice.
- The OpenAPI enums for `phase`, `direction`, `membership` are tested against the Go constants.

**Tests.** Cell snapshot unchanged; `TestMembershipOnReadEqualsGroupStatistics` (BI);
`TestClassifierPreservesMemberOrder` (FC, p-values); `TestUnknownStateIsCountedNotDropped` (BI);
`TestPurgedMonthBypassesCacheAcrossProcesses` (two `Server`s on one database, FC);
`TestRestrictedViewerGetsNoMembership` (byte-identical with and without another tenant's earlier set; a load error
omits the fields, FC); `TestDisplayAndCountedSetIDs` (BI); `TestNotBeforeIsALowerBound` against committed 2023–2026
closure calendars (BI); the OpenAPI enum test. Release note: `counted_set_id` is corrected.

### 8.3 P17 `feat/forecast-groups-explain` — explain fields

Per non-mature cell: counts by phase, `failed_cap_exceeded`, `not_before`. A summary strip: since-date, sets,
accepted, stocks, marks by phase, "next result on or after". Response-level `rtypes`, `min_n`, `limits`,
`generated_at`, `ledger_start`, `capabilities`, `evaluator.mode`. **All of these are volume** and are withheld from
external `all` viewers by the §7.3.3 shaping, which includes the mature cell's own `n` (shown for `own`).

**Tests.** `TestNonMatureCellsCarryPendingReasons`; `TestPendingReasonsDoNotChangeState` (500 random fixtures against
a frozen copy of today's state function, FC); `TestCapabilitiesMirrorRouteGates` (FC);
`TestExternalAllGetsNoExplainVolume` (FC).

### 8.4 P18 `feat/forecast-reader-server` — the reading-page server view and the stage switch (V13)

- **Stage switch** `forecast_reader` ∈ {`off`, `card`, `full`}, default `card` (today's footprint), audited
  `forecast.reader`; an unknown value reads as `off`. Admins see `full` regardless. For a non-admin, a reading-page
  surface requires **both** a scope other than `none` (§7.3) **and** the stage: card and overlay at `card` or `full`;
  chip and timeline dots at `full` only.
- **`/api/stock/{symbol}`** (`apiui.go:565`): `r` decides the date and the kind when it names a readable report (an
  unreadable `r` falls back to the default selection and leaks nothing); adds a top-level `prediction` summary
  (display set, stance and weight, upside, next checkpoint with phase and "on or after"/"expected", membership for
  internal `all` readers and admins, the sibling-edition link of C17) and `timeline[].predicted`, computed exactly from
  `SELECT DISTINCT report_id FROM prediction_sets WHERE symbol=? AND state='accepted'` intersected with the viewer's
  already-scoped list; only when the gates above hold and the rtype is covered; the summary is omitted on
  `X-Prefetch: 1` requests (`prefetch.ts:48`) while the timeline flags are kept; a ledger error omits the summary and
  never the report (FO).
- **`/api/run/{key}`** (`apiui.go:648`) carries the same summary.
- **Non-accepted sets and per-set errors** become admin-only on the session routes.
- **Editions (C17, V25).** Each edition's card shows its own sets; an edition with no set links a readable sibling
  edition's set; for internal `all` readers a sibling member reads "counted on edition X".
- The PR records a before/after benchmark of `/api/stock` covering real and prefetch requests.

**Tests.** `TestForecastAccessMatrix` extended to every reading surface (FC/BI); `TestStockDeepLinkByReportID` (FC);
`TestStockAndRunPayloadCarryPredictionSummary` (stage-gated; scope-gated; restricted gets no membership; a ledger error
keeps the report; timeline flags exact over 60 reports — FO); `TestPrefetchOmitsTheSummaryButKeepsTheTimeline` (BI);
`TestEditionWithoutASetLinksItsReadableSibling`, `TestSiblingEditionMemberReadsCountedOnEdition` (BI);
`TestReaderStageEverywhere` (`off` hides sets on the session routes; persistent tokens unchanged; unknown → `off`,
FC); `TestReaderStageNeverWidensScope` (stage `full` does not give an OU at `none` any surface, FC); PG variants.

### 8.5 P19 `feat/forecast-cohort-parity` — the cohort page matches the demo

Shell with header, tabs and toolbar in one card; summary strip; status pill (admins); matrix states (dashed open
cell; small cell "n = x · below 30 · not published" with no progress bar; pending cell reasons by phase, "not
before"; mature cell 18 px/600 with sign, hover and focus; AA significance tint with glyph and "q<0.05"); coverage
bar with distinct segments and `pre_live`/`test`/`off_benchmark` chips (hidden for external `all`, as is every `n`: a
mature cell shows its metrics and an "n ≥ 30" badge); legend; drawer in
mature and membership modes; CSV header lines; the population line ("All reports" / "<OU> and its sub-units").

**Tests (vitest).** A small cell shows final text and no progress bar; a payload with numbers on a non-mature cell
renders none (FC); A's drawer rows never show while B loads; membership mode has no outcome columns (FC); a settings
failure still renders the matrix; external `all` renders no coverage bar and no `n` anywhere, including the drawer headline; a11y structure and axe; 375/768/1280 px with
no horizontal page scroll.

### 8.6 P20 `feat/forecast-reading-integration` — card, chip, steps, band, table, overlay, timeline marks, editions, run page

The card matches the demo's single-report view: meta line (report date · received · edition); badges (membership for
internal `all` readers and admins only; baseline detail "Reported X = close on D" once P7's check runs; translated
quality; default-probability note); "What the report said" (stance and weight, horizon "(12 months, standard)",
terminal with signed upside versus S0, p_profit, entry); a scenario band with a cursor at the latest **completed** bar
(the Go `barIsComplete` rules mirrored through fixtures shared by Go and vitest); checkpoint steps; the checkpoint
table (signed, coloured returns; bold excess; "touched / not touched (low X)"; four-state direction; source;
phase-driven rows for unresolved checkpoints); "other predictions" as a table with exact links
(`?date=<report_date>&kind=<repKind>&r=<id>#forecast`), orphaned rows muted and shown only to internal readers; the
payload collapsed (unrestricted only); the disclaimer line. The chip renders from `prediction` in the same paint as
the body (its slot reserved on a warm render). The chart stays collapsed by default; the overlay toggle is always
visible when an accepted set exists; new optional `PriceChart` props `windowFrom`, `markers[].price`,
`markers[].snap`, staggered labels and a legend "candles: live quote · dots: settled closes (source: X)" — with no
props the output is identical to today's. Timeline dots carry an `aria-label`. `RunPage` mounts the chip and card
without the chart. Removes `PredictionPanel.tsx`.

States per component: loading (skeleton, slot reserved), empty (no set: "This report has no forecast"; edition
without a set: the sibling link), error (inline retry; the body unaffected, FO), unavailable (scope `none`: nothing
rendered, no placeholder).

**Tests (vitest).** The production fixture renders waiting copy "on or after 2026-10-26" (or "expected" under P37),
never retry or failure text (FC); the chip is in the same commit as the body and nothing above the body changes
height (FO); edition switching swaps the card under the stale-state guard (FC); overlay toggle and preference
behaviour; bar-completeness and low-window parity fixtures run by Go and vitest (BI); the disclaimer on the card and in
the chip tooltip; RunPage mount; timeline `aria-label`; scope `none` renders no forecast DOM at all (FC); axe.

**Rollout.** The owner flips `forecast_reader` to `full` when P18 and P20 are deployed, P18's benchmark is within the
800 ms budget (§16 item 5), and one week with admins already on `full` shows the `/api/stock` p95 within 800 ms. Rollback:
set `card` or `off` (immediate, audited).

### 8.7 P37 `feat/exchange-calendar-projection` — projected dates labelled "expected" (V23)

A closed exchange-closure table in Go for the current and next year (`internal/app/forecast_calendar.go`), updated
each December from the exchanges' annual notice (runbook). Used only for projected dates, labelled "expected"; a year
missing from the table degrades to the weekday "on or after" projection. Settlement never reads it.

**Tests.** `TestProjectionMatchesKnownCalendars` (the demo's 2026-09-17 report projects d20 = 2026-10-26 and d60 =
2026-12-21, BI); `TestMissingYearDegradesToOnOrAfter` (FC); `TestSettlementNeverReadsTheTable` (source scan, FC).

### 8.8 P23 `feat/quote-ledger-capability` — Tier 1 on the shared source policy, and ADR 0038 (V9)

- Two new intervals: `settled` (ledger-grade daily bars that may be written) and `settled_shadow` (compared, never
  written), each granted only explicitly (a `servesExplicit` that ignores the empty-capability wildcard of
  `quoteSource.serves`, `quote_cache.go:272`). `quoteSource` (`quote_cache.go:218`) gains `cashDividends` and
  `reportsKind` (true for Tencent; false for Sina and Yahoo). `fetchUnder` (`quote_cache.go:1089`) refuses both
  intervals, so a ledger body is never cached.
- `ledgerChain` = `quote_source_order` (`quote_cache.go:525`) restricted to `settled` grants, cash-dividend sources
  first; `ledgerPrimary` = the first member with both flags; none → `no_source` (a source failure that pauses
  settlement; both panels alert).
- `walkLedger`: uncached, outside single-flight, one upstream slot for the whole walk, windows only from
  `quoteParseWindow`, its own `ledgerHealth` (typed ledger refusals and cancellation never count as vendor failures),
  a per-tick breaker after 3 failures, and the stored refusal is the highest-priority called source's outcome.
- **Readiness item 6.** `forecastReadiness` (P9) gains `ledger_chain`: `ledgerChain` is not empty and `ledgerPrimary`
  exists. Before this PR the item does not exist (§1.4): the evaluator called `fetchTencentRange` directly, so a
  `quote_source_order` check would have gated nothing. From here mode `1` is refused (409 `forecast_not_ready`) while the
  chain is empty, and the watchdog's `no_source` reason starts firing (it could not before).
- Order saves are audited (`quote.order`); the UI confirms before a save that removes the ledger's primary.
  `GET /api/admin/quote` (`routes.go:401`) adds `settled`, `settledShadow`, `cashDividends`, `reportsKind`,
  `ledgerChain`, `ledgerPrimary`, `ledgerShadowPeer`, `ledgerHealth`.
- ADR 0038 in the same PR (§14).

**Tests.** `TestSettledRequiresAnExplicitGrant` (FC); `TestLedgerChainFollowsOperatorOrder` (Tencent removed → walk
returns `no_source`; a panicking stub is never called — FC); `TestLedgerFetchNeverCaches` (FC); `TestReadinessItemSixNeedsALedgerChain` (empty chain → 409 `forecast_not_ready`
naming `ledger_chain`; restored → mode `1` allowed again, FC);
`TestLedgerHealthIsSeparate` (BI); `TestNoPublicRangeIsSettled` (FC); `TestBreakerAndSingleSlot`;
`TestRefusalPrecedence`; vitest `QuoteSourcesPage` (no-primary alert; confirmation before removing the primary).

### 8.9 P24 `feat/cli-quote-probe` and P25 `feat/forecast-sina-shadow`

- **P24.** `report-portal quote-probe capture|compare`: writes raw bodies plus a manifest, never the database;
  `compare --record` replays the shadow comparator over a fixed panel (`sh000852`, `sh000906`, `sh000300`, 20 stocks
  across boards including `301`, `689` and Beijing codes, and supplied suspended codes); accepts Yahoo and any other
  compiled-in source as a candidate. Tests: capture never writes the database; compare shares the comparator.
- **P25.** `fetchSinaRange` trims Sina's count-based kline (`fetchSinaQuote`, `quote.go:2127`) to the window, returns
  `window_not_covered` past the 800-bar cap, treats `BarsUnavailable` as an error, and requires the newest kline close
  to cohere with the snapshot. The comparator takes at most two bodies already accepted from the primary per working
  tick, compares OHLC to the fen, date sets and both anchors, persists results in `forecast_state_shadow` and
  `forecast_state_calls`, and never touches ledger tables. `forecast_fallback` ∈ {`off`, `shadow`}; `on` → 400
  `forecast_bad_fallback` until Tier 2. Tests: `TestSinaRangeFiltersByRowDate`, `TestSinaKlineMustAgreeWithItsSnapshot`,
  `TestShadowNeverWritesTheLedger` (≤ 2 extra calls per tick; ledger rows identical to an `off` run — FC),
  `TestFallbackModesTier1` (FC).

---

## 9. Release 3 and Tier 2

### 9.1 P29 `refactor/forecast-retire-admin-aliases`

Removes `GET /api/admin/forecast/groups` and `/members` (`routes.go:314-315`) with their `unrecorded`, the `enabled`
alias and the legacy `bench` field. Depends on P27 and P28 shipped and R2b in production for two weeks.
**Tests.** `TestAdminGroupsPathIsGone` (JSON 404, not the SPA); the e2e script asserts 404. **Rollback.** Revert; a
stale tab gets the update prompt.

### 9.2 P32 `feat/forecast-provenance-and-index-fallback` (Tier 2, migration `0007`)

Migration `0007` (§3.4), then admission of the benchmark leg from a second source under plan §3.3's rules (each
benchmark return from two closes of one series; `s0_fen` only from the primary; no new exclusion class). **Entry
criteria (V19):** ≥ 60 replayed and ≥ 10 live shadow sessions with zero unexplained fen disagreements on closes and
anchors; an identity check for the index. **Tests.** `TestMigration0007` (SQLite and PG),
`TestRatiosNeverCrossVendors` (property-based), `TestBenchAnchorFillNeverFollowsResolvedMarks`,
`TestNonKindIndexOnlyByAnchor`. **Rollback.** Forward-only; backup first.

### 9.3 P33 stock-leg admission (Tier 2)

Only if a source gains corporate-action evidence, its own or through a separately granted evidence source.
**Tests.** `TestS0OnlyFromThePrimary`, `TestStockLegNeedsCorpEvidence`, `TestEvidencePairingIsRecorded`.

---

## 10. The access matrix

### 10.1 Who sees what

**Model.** The OU scope behaves like Google Workspace organizational units: a child OU inherits its parent's setting by
default and may override it, and nothing more complicated (the nearest set value on the chain wins; `NULL` means
inherit; the Default OU is the top and stores `none`). The tree is read live: an OU that moves takes the new parent's
inherited settings. The one extra rule is that each set remembers the OU its run was attributed to, frozen at insert;
deleting an OU re-attributes its sets to its parent OU (a root's to the Default OU), so no data is lost and no set
points at a deleted id. Deleting an OU is not a read path, so the matrix below needs no column for it; its behaviour
is pinned by the P38 tests.

This is the single statement of access. "Stage" is `forecast_reader` (P18); before P18 ships, the reading surfaces
behave as at stage `card`. "Readable" means the report passes the viewer's existing read scope (`viewerScope`,
ADR 0024). "External" means the account flag or any OU on the chain is restricted. Status codes are exact; a dash
means the surface does not exist for that credential.

| Subject | Record `/forecast` + `GET /api/forecast/groups` | Members drawer `GET /api/forecast/groups/members` | CSV | Reading-page card (`/api/report/{id}/predictions`, `/api/predictions`) | Chip + timeline dots (`/api/stock`, `/api/run` `prediction`, `timeline[].predicted`) | Chart overlay | Raw payload | Membership badge / `counted_set_id` | Non-accepted sets, errors, orphaned rows | `/manage/forecast`, `/api/admin/forecast*` | `/api/v1` prediction reads |
|---|---|---|---|---|---|---|---|---|---|---|---|
| **Admin** (`can(role, PermManage)`) | 200, population `all`, monthly + custom, coverage | 200, every member incl. orphaned | full | always (stage ignored) | always | yes | collapsed | yes | yes | 200 | cookie: session shape, full |
| **Internal OU at `all`** | 200, `all`, monthly + custom, coverage, no `unrecorded` | 200, every member (internal scope reads all), orphaned muted | full | stage `card`/`full` | stage `full` | stage `card`/`full` | collapsed | yes | orphaned rows yes (muted); non-accepted, errors no | 403 | cookie: session shape |
| **Internal OU at `own`** | 200, `own` population (OU + descendants by attribution), monthly + custom, its own coverage, no `unrecorded` | 200, members of the `own` population | `own` rows | stage `card`/`full`, any readable report | stage `full` | stage `card`/`full` | collapsed | **no** (the badge refers to the `all` record) | orphaned yes (muted); non-accepted, errors no | 403 | cookie: session shape |
| **Internal OU at `none`** (explicit or inherited, incl. Default) | 403 `forecast_unavailable`; no nav item | 403 `forecast_unavailable` | — | 403 `forecast_unavailable`; the SPA renders nothing | absent | absent | — | — | — | 403 | cookie: 403 `forecast_unavailable` |
| **External OU at `all`** | 200, `all`, **monthly only** (custom → 403 `forecast_custom_not_allowed`), **no coverage, no `unrecorded`, no `n`/totals on any cell** (non-mature cells `state` only; mature cells metrics plus an "n ≥ 30" badge) | 200, **readable members only**, no orphaned rows, no `total` or per-status counts | no coverage, `n` or count columns | stage `card`/`full`, readable reports only | stage `full` | stage `card`/`full` | no | no | no | 403 | cookie: scoped, no payload, no membership |
| **External OU at `own`** | 200, `own` population, monthly only, its own coverage, no `unrecorded` | 200, readable members of the `own` population | `own` rows | as external `all` | stage `full` | stage `card`/`full` | no | no | no | 403 | cookie: as external `all` |
| **External OU at `none`** | 403 `forecast_unavailable` | 403 | — | 403; nothing rendered | absent | absent | — | — | — | 403 | cookie: 403 `forecast_unavailable` |
| **Restricted account** (account flag) in an internal OU at `all` | as external `all` (the OU's scope with the external protections) | as external `all` | as external `all` | as external `all` | as external `all` | as external `all` | no | no | no | 403 | as external `all` |
| **Session held at the enrolment wall** | 403 `enrolment_required` | 403 `enrolment_required` | — | 403 `enrolment_required` | 403 (`/api/stock` is gated) | — | — | — | — | 403 `enrolment_required` | 403 `enrolment_required` (#108, `v1CanRead`) |
| **Persistent machine token** (`query`) | 401 (session route) | 401 | — | — (session route: 401) | — | — | with `include=payload` | `counted_set_id` | yes (machine shape) | 401 | 200, machine shape, unscoped |
| **App-bridge token** (minted only for unrestricted users, `apps_api.go:134-139`) | 401 | 401 | — | — | — | — | no | no | no | 401 | minting user at `none` → 403 `forecast_unavailable`; otherwise accepted sets only |
| **Anonymous** | 401 | 401 | — | 401 | 401 | — | — | — | — | 401 | 401 |

`/api/me` carries `forecast: {scope, external}` for every signed-in subject (navigation only). `/healthz/forecast` is
public and status-only for everyone.

### 10.2 `TestForecastAccessMatrix`

One table-driven Go test (`internal/app/forecast_access_matrix_test.go`), introduced by P26 for the record, admin,
card-route and v1 columns, and extended by P18 to the chip, timeline and stage columns. It runs on SQLite and, under
`TEST_POSTGRES_DSN`, on Postgres.

**Fixture.**
- OU tree: `Default` (`none`); `Internal` (`all`) with child `InternalTeam` (NULL → inherits `all`) and child
  `InternalOwn` (`own`) whose child `InternalOwnSub` (NULL → `own`, subtree root is the member's own OU);
  `InternalNone` (`none`); `ExtAll` (restricted, `all`) with child `ExtAllChild` (NULL → `all`); `ExtOwn`
  (restricted, `own`); `ExtNone` (restricted, `none`); `Weird` (stored `"everyone"` → `none`); `DefChild` (parented
  under Default, NULL scope: `none` in the base variant, `own` rooted at itself in variant B); `Dangling` (a
  `parent_id` naming a deleted row → `none`).
- Accounts: one admin in `InternalNone` (still `all`); one member per OU (including `DefChild` and `Dangling`); an
  unassigned member (Default → `none`; in variant B `own` over Default and `DefChild`); a
  restricted account in `Internal`; an account under a 2FA mandate without a factor.
- Credentials: each account's cookie; one persistent `query` token; bridge tokens minted by the `Internal` member and
  by the `InternalNone` member; no credential.
- Variant B reruns the table with Default stored `own` instead of `none`, to pin the Default-subtree rule.
- Ledger: two mature months with ≥ 30 members in `all`; sets attributed to `InternalOwn`, `InternalOwnSub`, `ExtOwn`
  and unattributed sets; one purged (orphaned) set; one rejected set; a report readable by `ExtAll` and one that is
  not.

**Assertions per (subject, probe).** The exact status code; for 200, a list of JSON paths that **must** be present and
a list that **must not** be present (`coverage`, `unrecorded`, `payload`, `membership`, `counted_set_id`, `errors`,
`attributed_ou`, members whose report is unreadable or purged; `cells.*.n` on non-mature cells for everyone, and for
**External `all`** (including a restricted account in an internal OU at `all`) on mature cells too, plus
`cells.*.total`, `cells.*.resolved`, the members response's `total` and per-status counts, and the CSV's `n` and count
columns); for `own`,
that the members are exactly the subtree's attributed sets. **Failure direction:** any status more permissive than the
table, or any forbidden path present, fails the test (FC); a required path missing fails it too (BI), so the test
cannot be satisfied by refusing everything. Adding a route under `/api/forecast/` or a field to a forecast response
without adding a row fails `TestForecastRoutesAreSessionRoutesBehindTheWall` or the forbidden-path list review.

---

## 11. P39b `feat/owner-token-every-run` — owner token for every run

### 11.1 Goal and preconditions

**Goal.** Attribute every run — internal and external — so `own` works for internal OUs (decisions page). Today
`runInputs` (`report_owner.go:67-81`) returns the caller's inputs unchanged unless `isRestricted(job.CreatedBy)`, and
no Dify workflow echoes the token, so no report is attributed.

**Preconditions (all must hold before merge):**
1. P38 deployed (the `attributed_ou` column and the strict `ouIndex`).
2. P39a deployed (reserved inputs hidden and stripped, and each target's `reserved_inputs` recorded).
2b. P30 deployed. It alone owns the group-principal rule (`AddReportViewer` writes `g:<OU>` only for an effectively
   restricted OU, and only the `u:` row on an index error). P39b does not repeat it at its call site.
3. The owner has published the Dify batch: 16 workflows (6 production entries including 1-6-4 V3.17 and 3-5 V7.4.0,
   plus 10 ingesting children) accept `_rp_owner_token` as an optional input and pass it to every ingest node as
   `owner_token` and down to ingesting children; the ingest custom tool is re-registered with `owner_token`.
4. The owner has confirmed, with one Dify test run of a workflow that does **not** declare `_rp_owner_token`, how
   Dify treats an undeclared input. If Dify ignores it, inject into every run as decided. If Dify rejects it, stop and
   ask the owner before merging (the alternative is injecting only for targets whose discovered inputs declare the
   key: P39a records exactly that fact as `reserved_inputs` in each target's config JSON (§6.5: written by the probe
   and the refresh before the input is hidden; no schema), and P39b's `runInputs` would inject only when the target's
   `reserved_inputs` contains `ownerTokenInput`. That variant is built only on the owner's answer; the default stays
   "inject into every run").

### 11.2 Change

```go
// runInputs returns the Dify inputs to send for a job's item. Every run with a submitter carries a signed
// owner-attribution token under ownerTokenInput (ADR 0022 R1, amended): internal and external alike, so
// every prediction set can be attributed to the OU its run belonged to. It never forwards a caller-supplied
// reserved input, and it never mutates the caller's map.
func (s *Server) runInputs(job BatchJob, inputs map[string]string) map[string]string {
	out := make(map[string]string, len(inputs)+1)
	for k, v := range inputs {
		if isReservedInput(k) {
			continue
		}
		out[k] = v
	}
	if job.CreatedBy == "" {
		return out // a run with no submitter is unattributed (no "u:" principal for nobody)
	}
	if tok := s.mintOwnerToken(s.st.OwnerGroupOf(job.CreatedBy), job.CreatedBy); tok != "" {
		out[ownerTokenInput] = tok
	}
	return out
}
```

`mintOwnerToken` (`server.go:613-620`) **changes by one guard**; the rest is unchanged (7-day expiry, minted when the
item runs at `batch_run.go:696`, so the OU is the submitter's primary OU at run time). Today it returns `""` only when
`ou == 0 && user == ""`, and `s.hmac` (`server.go:536-540`) signs with `[]byte(s.cfg.SecretKey)`, so an empty key
silently yields a well-formed token that any verifier holding the same empty key accepts. The startup check
(`validateSessionSecret`, `server.go:353-366`) makes this unreachable in production, and attribution now rides on the
token for every run, so the guard is stated here rather than assumed:

```go
func (s *Server) mintOwnerToken(ou int64, user string) string {
	if s.cfg.SecretKey == "" || (ou == 0 && user == "") { // new: never sign with an empty key
		return ""
	}
	...
}
// ownerFromToken gains the matching first line: if s.cfg.SecretKey == "" { return 0, "", false }.
```

**Ingest (`v1Ingest`, `apiv1.go:309-324`).** With the token decoded once (P38):

```
if ok:
    predSet.AttributedOU = ou            (P38; frozen at insert)
    StampReportOwner(id, ou)             (unchanged: first-writer-wins attribution; not a read key)
    if who != "": AddReportViewer(id, in.Date, who, ou)          // unchanged call; P30's rule decides the g: row
```

There is **no** `viewerOU` logic here. Every internal ingest would otherwise write `g:<internal OU>` rows, and because
every restricted reader's principals include every OU up its chain (Default included), internal reports would become
readable by any tenant under a `visibility=group` version (§0.3 row 10). That is prevented once, inside
`AddReportViewer`, by P30 (precondition 2b): `g:<OU>` only for an effectively restricted OU, the `u:` row alone on an
index error or a broken chain. For restricted OUs the behaviour is unchanged.

### 11.3 ADR 0022 amendment (in place, same PR)

- **R1, the injection sentence** (`docs/adr/0022-external-user-ou-tenancy.md:82-95`): "injected only for restricted
  OUs" becomes "injected into every run that has a submitter (since <release>), so prediction sets can be attributed
  per OU (ADR 0039); reserved `_rp_*` inputs are never user-facing (P39a)".
- **R1, attribution:** internal reports' `owner_group` now records the generating OU too; it remains attribution and
  audit only, never a read key (ADR 0024).
- **R1, viewer principals:** the `g:<OU>` principal is written only for an effectively restricted OU (the rule is
  P30's; this PR cites it).
- **Enforcement-points table** (`0022…md:215`): "`Server.runInputs` (mint + inject token for every run with a
  submitter)".

### 11.4 Tests

- `TestRunInputsInjectsOwnerTokenForEveryRun` (replaces `TestRunInputsInjectsOwnerTokenForRestrictedOnly`,
  `run_inputs_test.go:13`): an internal Default member gets a token for Default; an internal member of `Internal` gets
  one for `Internal`; a restricted member gets one for their OU; `CreatedBy == ""` gets none; the caller's map is not
  mutated; a caller-supplied `_rp_owner_token` is replaced. FC
- `TestInternalRunAttributesSetButSharesNoGroupPrincipal` (an integration test over P30's rule, which must already be
  in the tree): an internal ingest with a token writes `attributed_ou`, stamps `owner_group`, writes `u:<user>` and no
  `g:` row; a restricted tenant reader under a `visibility=group` version granted to them cannot read the report. FC
- `TestRestrictedRunStillSharesWithinTenant`: a restricted run still writes `g:<tenant OU>`, and a second member of
  the tenant reads it. BI
- `TestMintOwnerTokenRefusesAnEmptySecret`: with `SecretKey == ""`, `mintOwnerToken` returns `""` for a user in a real
  OU, and `ownerFromToken` rejects a token that was signed with the empty key by the pre-change code path (built in the
  test with `hmac.New(sha256.New, nil)`). Fails on `main`, where both succeed. FC
- `TestTokenMintFailureLeavesRunUnattributed`: with the empty secret `runInputs` carries no `_rp_owner_token`, the
  run still starts, and the ingest that follows leaves `attributed_ou` NULL and `owner_group` unset. FC
- (`TestOUIndexErrorWritesNoGroupPrincipal` is P30's test, §6.8; this PR does not repeat it.)
- `version_read_test.go:204` and the reuse-path tests stay green.

### 11.5 Rollout ordering

1. R1b deployed (P38, P39a). Take the ADR 0027 backup first.
2. The owner publishes the Dify batch (precondition 3) and performs check 4.
3. An admin runs the target refresh on the 16 targets (`dify_refresh.go`) and confirms `_rp_owner_token` does not
   appear in any run form or CSV template (P39a).
4. Merge and deploy P39b **as its own Beta after R1b**. It cannot ride R1b: its preconditions (P38, P39a and P30
   deployed, the target refresh done after P39a, the Dify publish, the undeclared-input check) can only hold after
   R1b is in production.
5. Verify with one internal production run (admin-only, read-only):
   `SELECT id, attributed_ou FROM prediction_sets ORDER BY id DESC LIMIT 5;` and
   `SELECT principal FROM report_viewers WHERE report_id = <id>;` → the set is attributed; no `g:` row for an internal
   OU.
6. Record the release in ADR 0022's amendment.

**Rollback.** Revert P39b: internal runs are unattributed again; sets already attributed stay attributed (frozen).
If the Dify batch is rolled back while P39b is deployed, precondition 4's answer decides whether runs still start; if
Dify rejected undeclared inputs, revert P39b first.

---

## 12. Observability, SLOs and runbook

### 12.1 Signals

- **Heartbeat** `meta.forecast_eval_state` (P9), written by the pass only while the mode is not off: mode,
  `last_tick_at`, pass statistics, per-benchmark calendar state, the dry sample, `awaiting_gate`.
- **Persisted counters:** `forecast_state_calls` (14 days of calls, failures and refusals by code, per source),
  `forecast_state_shadow` (P25), access and compute counters (requests, cache hits, flights joined, busy,
  rate-limited, refusals by reason, drawer opens) kept in memory and flushed daily to `meta`, and the weekly distinct
  viewer count (an integer; the salt lives in memory only, §7.3.5).
- **Logs** (`log/slog`, `component=forecast_eval`): one line per pass; one line per set error with `set_id`, `bench`,
  `source`, `reason`; nothing for sets that are not due. The groups routes log `forecast groups: scope= pop= mode=
  cells= scored= cached= flight= wait_ms= dur_ms=` when over 3 s. Access refusals log their reason (never the
  username of a refused anonymous caller).
- **Watchdog** (its own goroutine, 60 s, `recover`): `stalled` (heartbeat > 10 min; at most hourly), `lagging` (oldest
  due mark unsettled > 6 h, or a weekday past 18:00 CST with a stale calendar, or the valve tripped; at most daily),
  `no_source` (empty ledger chain; at most daily; from P23 on, because there is no chain before it). Nothing while off.
- **External health:** `GET /healthz/forecast` returns 503 when stalled, for an uptime monitor that also catches the
  process dying. `/healthz` stays database-only.
- **Webhook events:** `predictions.store_failed`, `forecast.evaluator.unhealthy`.
- **Alert channels (V18):** every watchdog event and every `predictions.store_failed` goes to **Email and Slack**
  through `forecastAlert` (P41; the store-failed call is made asynchronously from the ingest path, §6.2), plus the
  webhook event for any other subscriber. Delivery outcomes per channel are on
  `/manage/forecast`.
- **Audit trail:** `forecast.auto`, `forecast.bench`, `forecast.population`, `forecast.repair`, `forecast.eval.run`,
  `forecast.dry_gate`, `forecast.alert`, `forecast.alert.test`, `forecast.reader`, `forecast.fallback`, `quote.order`,
  `group.change` (with `forecast_scope`), each on a real change only.

### 12.2 SLOs (plan §8, accepted as written under V18)

| SLO | Target | Measured from |
|---|---|---|
| Settlement freshness | 99 % of marks due on session D are terminal or carry a recorded refusal by 18:30 CST on D; p95 within 30 minutes of the calendar first showing D, on vendor-healthy days | `resolved_at`, the calendar's `fetched_at` |
| Evaluator liveness | heartbeat younger than 5 minutes for 99.9 % of enabled minutes | `last_tick_at`, the external monitor on `/healthz/forecast` |
| Ledger integrity | `store_failed = 0` and `unrecorded = 0`; any occurrence is an incident | audit log, bounded count (P2) |
| Data quality | `refused_final` share below 1 % per cohort × checkpoint; maturity cap stays 5 % | marks |
| Statistics latency | monthly view p95 ≤ 3 s on a cold cache; cached requests never wait for a slot | the groups log line |
| Reading-page cost | `/api/stock` p95 ≤ 800 ms for the card → full step (P20; decided 2026-10-19, §16 item 5) | P18's benchmark, then logs |

**Error-budget rule.** While freshness or integrity is breached, evaluator feature PRs wait; fixes are exempt.

### 12.3 Runbook (`docs/forecast-verification-operations.md`, new; each entry written by the PR that creates the signal)

| Symptom | Alert | First checks | Action |
|---|---|---|---|
| `stalled` or `/healthz/forecast` 503 | Email + Slack (hourly) | heartbeat age, `panics`, `last_error`; is the process up | Restart only after reading the log; the loop recovers from panics, so a stall is a wedge. |
| `lagging` | Email + Slack (daily) | calendar last bar and source; `stale_valve_tripped`; ledger health on the quotes panel | Vendor-wide lag: wait — nothing is written and nothing becomes final (V26). One source down: check the chain. Never force a settle. |
| `no_source` (from P23) | Email + Slack (daily) | the `quote.order` audit | Restore the primary, or accept that settlement is paused. Nothing becomes final. |
| `predictions.store_failed` > 0 | Email + Slack (each) | audit filter `predictions.store_failed` | Find the ingest; the report exists, the set does not, and it cannot be re-created after the fact. Record the incident. |
| `awaiting_gate` lists a code | — (status card) | which benchmark; first set of the cohort | Hand-check one would-be d5 for that code and record the dry-run check. |
| An `off_benchmark` count appears | — | `forecast.bench` audit and reconciliation rows | Explain it in the release note; never re-score. |
| A `test` count appears | — | the emitter and `run_mode` of those sets | Expected for Dify test-app runs; if a production run was marked `test`, fix the producer (the set stays a test). |
| Setting the ledger start | — | candidate list against Dify issue #1 | Save `2026-10-18T16:00:00Z` once. Permanent; survives restores. |
| Switching the evaluator on | — | §1.4 readiness list | Record the hand-checked closes in the dry-run gate; then `1`. |
| Scheduling a benchmark | — | the response's `effective_cohort` | CSI 800: must read `2026-11`. If the deploy slipped past 2026-10-31, use §1.2's contingency. |
| An OU's members report "forecast missing" | — | `/api/admin/forecast/access` for that OU | Expected at `none`; set the OU's scope if intended (audited `group.change`). |
| Before any release deploy | — | — | ADR 0027 backup; for R1a-β2 record Q3 = 20. |
| After a restore | — | `forecast_auto`, `forecast_reader`, dry-run gates, alert test | A restore never raises the mode or the stage, resets the gates, and caps a restored `1` at `dry` (audited `restore_gates_empty`); re-check deliberately. |
| December | — | the exchanges' closure notice | Update the projection table (P37). |
| Reading-page incident | — | — | `forecast_reader=off` (immediate, audited). |
| Record incident | — | — | Set the affected OUs to `none` (immediate, audited). |
| Rolling back across step `0006` or `0007` | — | — | Restore the pre-upgrade backup; binary rollback is refused by design. |

---

## 13. Rollout and rollback per release

| Release | Before deploy | Deploy and verify | Owner actions after deploy | Rollback |
|---|---|---|---|---|
| R1a-β1 | ADR 0027 backup; Q1 | Boot log shows the benchmark reconciliation result (expected: no-op) | Save the ledger start `2026-10-18T16:00:00Z`; schedule `sh000906` (response `effective_cohort: 2026-11`); confirm the unrecorded line reads 0 since 2026-10-19 | Code revert is safe for the database. A scheduled `next` is ignored by the older build: if reverted before November, apply §1.2's contingency. |
| R1a-β2 | Backup; Q3 = 20 | Boot log and `forecast.repair` audit row show 20 healed marks | None (evaluator stays off) | Code revert (evaluator off). Healed rows stay `pending`, which the old build handles. |
| R1b | Backup (step `0006` makes later binary rollback restore-only) | `TestMigration0006` green in CI; boot shows step `0006` recorded; Default OU reads `none` | Set mode `dry`; configure Email recipients and the Slack URL; send the test alert; record the dry-run check for `sh000852`; then mode `1` | Revert individual PRs (forward fix). Binary rollback below `0006` = restore the backup. |
| P39b (its own Beta after R1b) | Preconditions §11.1 (P38, P39a, P30 deployed; Dify batch published) | §11.5 step 5 | — | Revert P39b (attributed sets stay). |
| Evaluator on | Readiness list | Status card healthy; first d5 settles from history; `/healthz/forecast` 200 | Watch freshness for a week | Mode `dry` or `0` (immediate, audited). |
| R2a | Backup; Q4 | All OUs `none`; only admins see `/forecast` | Set `安禅内部` to `all`; one clean week; further OUs as the owner chooses | Set OU values to `none` (immediate); or binary revert (no schema in R2a). |
| R2b | Backup | P18 benchmark recorded | Seeded-preview review; flip `forecast_reader` to `full` per P20's rollout | `forecast_reader=card`/`off`; binary revert (no schema). |
| R3 | Backup | `/api/admin/forecast/groups` → JSON 404 | — | Revert. |
| Tier 2 | Backup | `0007` recorded | Owner go/no-go per V19 | Restore-only. |

---

## 14. ADR and document changes

ADR numbers computed from `docs/adr` at the baseline (latest `0036`). Confirm at merge.

| Document | Change | PR |
|---|---|---|
| **ADR 0037 (new) — Forecast verification operations** | The definition repair (code brought to ADR 0029 §4 and design §9.4); not-due is not a refusal; finality only after mark-specific attempts (V26); the terminal baseline; the heal (and why the plan's "not due" check was dropped); the ledger start (membership only) and test runs at ingest; benchmark governance with the allowlist including `sh000906` and the CSI 800 schedule; the bounded `unrecorded`; the restore rules of §3.3; dry mode, the readiness list and the per-benchmark dry-run gate; heartbeat, watchdog, health route, alert channels (Email + Slack) and SLOs; the single-connection transaction rule; the escapes F01, F03, F06, F10, F14 with their causes. | P11 |
| **ADR 0038 (new) — Ledger-grade market data on the shared source policy** | Amends ADR 0029 D9 and §6: the evaluator walks the operator's chain restricted to explicit `settled` grants; one writer in Tier 1, no fallback writes for scored marks (V9); the candidate-source table; the separate evidence chain; Tier 2 entry criteria and migration `0007`. Corrects ADR 0029 §7 (a mark's `bench_code` is the frozen intent). Records at ADR 0028 §1 that stored closes are single-vendor, from an unlicensed public endpoint, integrity-checked, not corroborated. | P23 |
| **ADR 0039 (new) — Forecast verification for readers** | The three-way OU scope, Google Workspace style inheritance (a child OU inherits its parent's setting and may override it), Default `none`, admins `all`; frozen attribution and the owner token for every run, with its **one sanctioned exception: deleting an OU re-attributes that OU's sets to its parent (a root's to the Default OU) in the delete transaction, audited with counts, so no set references a deleted id** (replaces the 409 guard); re-parenting follows the live tree; the external protections; the access matrix (§10); compute governance; the reading-page integration and the stage switch; edition rules; the disclaimer. Supersedes design D6; amends ADR 0029 §5's statistics bullet. | P22 |
| **ADR 0029 (amend in place, pointing to 0037–0039)** | §5: the v1 reads admit a cookie held at the enrolment wall (`v1CanRead`, #108) and scoped by `viewerScope`; the forecast reads additionally apply the forecast scope; the bridge token carries its user; statistics access per 0039. §6: see 0038; the refusal vocabulary and its two classes; dry mode; finality per V26 (also corrects `fetchTencentRange` to `quote.go:1588`). §7: note on mark `bench_code`. §9: the `pre_live`, `test`, `off_benchmark` buckets; membership computed inside each population; one hit predicate. §10: the qfq fallback is implemented, detection-only; the corrected limit backstop. §12: new rows D10 onward — V3 (ledger start), V6b, V8 + CSI 800, V9, the access model (replacing V10/V11/V13d), V12/V14, V13, V15, V24, V25, V26. | P11 (R1 rows), P22 (access rows), P23 (V9) |
| ADR 0022 | R1 amended for the owner token on every run and the restricted-only `g:` principal (§11.3); P30's reader-side rule. | P39b, P30 |
| ADR 0024 | Editions and forecast sets (C17); the restricted-only OU principal (P30). | P18, P30 |
| ADR 0028 | Note at §1 (single-vendor stored closes) and at §3 (the detection-only qfq exception now exists). | P23, P8 |
| ADR 0017 | Note: `pre_live` and `test` sets are purged by the ledger retention target like any other; `attributed_ou` does not affect retention. | P11 |
| ADR 0035 | No change; steps `0006` and `0007` follow it. | — |
| `docs/forecast-verification-design.md` | §7 and D6 superseded (pointer to 0039); acceptance condition 19 rewritten; §9.2 corrected (dividend element, corrected price-limit backstop, qfq ratio); §9.3 base rule for an S0 halt (V6b); §10 UI updated. | P11, P22 |
| `docs/forecast-verification-operations.md` (new) | The runbook of §12.3. | P9 onward |
| `docs/forecast-verification-demo.html` | P13's revision. | P13 |

---

## 15. Handoff checklist

**Before R1a-β1**
- [ ] Re-read the decisions page; confirm nothing changed since 2026-10-19.
- [ ] Owner: **operational freeze** from now (the ledger start `2026-10-19T00:00:00+08:00` has begun counting): no Dify
      console or debug runs of any covered workflow (1-6-4 among them) until the Dify `run_mode` PR (P36's producer
      side) is published. Until then nothing on the wire marks a test run, so such a run would count as production.
- [ ] Owner: run Q6 (§1.5) now and again immediately before saving the ledger start; report every set whose Dify run
      was not started by the portal's batch (or whose `triggered_from` is a console or debug trigger). Do not ship
      R1a-β1 or save the ledger start over an unexplained test run.
- [ ] Capture the four index fixtures (`sh000300`, `sh000905`, `sh000852`, `sh000906`) by read-only probe (V20).
- [ ] P12 golden snapshots merged first.
- [ ] Owner: Q1 run; ADR 0027 backup.

**Before R1a-β2**
- [ ] Capture a real Tencent qfq + unadjusted window containing a bonus issue, plus the halt, delisting and
      limit-day fixtures (V20).
- [ ] Verify the current ST daily-limit rule text before any ST figure is coded (P7 leaves ST to P8 until then).
- [ ] Owner: Q3 = 20 recorded; backup.

**Before R1b**
- [ ] Owner: Email recipients and the Slack incoming-webhook URL for evaluator alerts.
- [ ] Backup (step `0006`).

**Before P39b** (its own Beta after R1b)
- [ ] P38, P39a and P30 deployed (R1b).
- [ ] Owner: Dify batch published; undeclared-input check (§11.1 item 4); target refresh done after P39a (and
      `reserved_inputs` visible on the 16 targets).

**Before switching the evaluator on**
- [ ] §1.4 readiness list green on the status card; dry-run check recorded for every benchmark code the list names
      (`sh000852` for the pre-live sets even before any in-population set exists; `sh000906` once November sets exist).

**Before R2a**
- [ ] Owner: Q4 run. Disclaimer wording approved (V22, 2026-10-19). zh-TW `forecast.*` family: machine-generated,
      flagged for review; **owner-arranged review** (V17), not a gate on R2a. §16 items 1–8 are decided.

**Before the `card` → `full` step**
- [ ] Owner: P18's benchmark recorded against the 800 ms `/api/stock` p95 budget (§16 item 5); the weekly adoption target set after one month of measurement following R2a; seeded-preview screenshots reviewed
      (`~/Downloads/forecast-preview/`).

**Every PR**
- [ ] Tests written first, failing on `main` (unless CH), green on SQLite and Postgres.
- [ ] Route table, enrolment-wall assertion, OpenAPI, audit vocabulary, webhook registry and labels, i18n parity
      updated as applicable.
- [ ] Single-connection transaction rule followed, with its 2-second-deadline SQLite test.
- [ ] Runbook entry and ADR delta in the same PR.
- [ ] English only in developer text; no AI attribution; branch + PR; adversarial review before merge.
- [ ] No deploy, release publish, Dify publish or production write without the owner.

---

## 16. Decided by the owner, and what is still open

The owner answered the former open questions on 2026-10-19. Their framing: the OU model must behave like Google
Workspace organizational units (§10.1): a child OU inherits its parent's setting by default and may override it, and
nothing more complicated. Each answer is built as written below.

### Decided (2026-10-19)

1. **Re-parenting follows the live tree.** An OU that moves takes the new parent's inherited settings, and its
   attributed sets count under the new ancestors' `own`. The population key keeps changing on any tree change, so no
   cached number is reused for a different population (P26). No extra column.
2. **Deleting an OU that has attributed sets is allowed, and the data is kept.** In the same transaction as the delete,
   `UPDATE prediction_sets SET attributed_ou = <parent> WHERE attributed_ou = <deleted>`; a root OU's sets go to the
   Default OU; an audit row records the counts. This replaces the 409 `group_has_forecast_sets` design and removes the
   id-reuse risk. It is the one sanctioned exception to "attribution frozen at insert" (P38, ADR 0039 row in §14).
3. **Membership badge for internal `own` readers: hidden** (as built, §10.1).
4. **Disclaimer wording approved:** zh-CN 「研究验证用途，不构成投资建议」, en-US "For research verification only. Not
   investment advice." zh-TW comes from the normal OpenCC path and the human review in item 6.
5. **`/api/stock` p95 budget: 800 ms** for the card → full step (P20). **Weekly adoption target: none for now;**
   measure for one month after R2a, then the owner sets it.
6. **zh-TW review:** the owner will find a reviewer later via a separate prompt. zh-TW ships machine-generated (OpenCC
   s2twp) and flagged for review; R2a is not blocked on it. Release checklist entry: "owner-arranged review" (§15).
7. **A restricted account inside an unrestricted OU** takes its OU's scope with the external protections applied (as
   built).
8. **External `all` readers do not see per-cell `n`, totals or member counts** (as built, the conservative default).
   External `own` readers keep `n`.

9. **Children of a deleted OU move to the deleted OU's parent.** Today `DeleteUserGroup` (`user_admin.go:183`) sets
   each child's `parent_id` to NULL, so the children become roots and silently start inheriting from the Default OU.
   Decided (consistent with item 2 and the Google Workspace model: an OU keeps inheriting along the line it was in):
   in the same P38 transaction, `UPDATE user_groups SET parent_id = <parent of deleted> WHERE parent_id = <deleted>`
   (a deleted root's children become roots, as today). The children's own sets stay attributed to them. This changes
   every inherited OU setting of those children (forecast scope, enrolment policy, quotas), which is the intended
   behaviour; the `group.delete` audit row lists the moved child ids. Tests:
   `TestDeletingOUMovesChildrenToItsParent` (child inherits the grandparent's `forecast_scope` and `totp_enroll`
   afterwards; fails closed: a dangling grandparent makes the children roots, never a cycle) and
   `TestDeletingRootOUKeepsChildrenAsRoots`, both drivers.

### Still open

None.

---

## Appendix A. Demo parity checklist

Acceptance runs on the seeded preview (P14) at 375 and 1280 px, light and dark, against the annotated production
baseline (P13). The "Today" column is corrected from P13's screenshots before P19 starts.

| Demo element | Today | After (PR) | Note |
|---|---|---|---|
| Header, tabs and toolbar in one card | separate rows | one card (P19) | |
| Report-type select | missing | a tag; a Select when more than one type (P19) | |
| Benchmark in the toolbar | missing | frozen code per group; `off_benchmark` chip (P4, P19) | |
| Population line | — | "All reports" / "<OU> and its sub-units" (P27, P19) | new with the access model |
| Monthly range | missing | newest 12 months, "show older" (P19) | |
| Custom range prefilled | blank tab | default range, presets, server limits (P19); hidden for external viewers (P26, P27) | |
| Metric help and explore banner | short; hit text wrong | full, corrected copy (P15) | |
| Open cell dashed | not dashed | dashed (P19) | |
| Small cell | text only | "n = x · below 30 · not published", no progress bar (P19); small `own` populations show the same | |
| Pending cell | done/total only | reasons by phase, progress, "not before"; h12 "from YYYY-MM" (P17, P19); external `all`: state only | |
| Mature cell | 15 px, no sign | 18 px/600, sign, hover and focus (P19); external `all`: metrics plus an "n ≥ 30" badge, no `n` (P26) | seed |
| Significance tint | fails AA | AA palette, glyph and "q<0.05" (P15) | seed |
| Coverage bar | counted and repeat share a colour | distinct segments with text; `pre_live`/`test`/`off_benchmark` chips (P19); absent for external `all` | |
| Legend and methodology | one sentence | swatch legend, shared methodology card (P15) | |
| Evaluator status | card above the matrix | header pill and drawer for admins; full status on operations (P9, P27) | deliberate change |
| Drawer | partial metrics | headline, after-fill, one-arm, slices, signed excess, four-state direction, halted tag; membership mode for non-mature cells (P16a, P16, P19); readable rows only for external viewers (P26) | |
| Report meta line | clock only | report date · received · edition (P20) | |
| "Counted in batch" | always "not counted" | classifier membership for internal `all` readers and admins; "counted on edition X" (P16, P20) | |
| Baseline badge | generic tag | "Reported X = close on D" after the §9.4 check (P7, P20) | |
| Stance, weight, horizon | stance only | all three plus signed upside (P20) | |
| Scenario band | three tags | band with a cursor at the latest completed bar (P20) | |
| Checkpoint steps | missing | phase-driven steps (P20) | |
| Checkpoint table | "Fetch failed" | phase rows with "on or after"/"expected"; signed returns; bold excess; touched; source (P10, P7, P16, P20) | |
| Expected checkpoint dates | none | calendar-backed "expected" (P37); the demo's own d20/d60 corrected (P13) | the demo was one session early |
| Other predictions | bullets linked by receipt date | table with exact links (P20) | |
| Chart overlay | collapsed, flat lines | toggle always shown; S0 window; dots at settled closes; legend with source; Sunday snap (P10, P20) | |
| Disclaimer | none | card, chip tooltip, record header, CSV (P15, P20, P27) | V22 |
| Summary in the report header | missing | chip in the same paint as the body (P18, P20) | not in the demo |
| Run page | nothing | chip and card (P20) | not in the demo |
| Record outside Manage | Manage only | `/forecast` with a nav item for OUs not at `none` (P27) | V12 |
| Scope `none` | — | no record, no card, chip or overlay; no placeholder (P26, P20) | the access model |

