# Forecast verification (预测验证): handoff

**Written 2026-10-18.** This is the state of the work, what is left, and what a new session needs to
know that is not in the two design documents. Read this first, then
[the implementation plan](forecast-verification-implementation.md), then
[the design](forecast-verification-design.md).

---

## 1. Where things stand

Five pull requests are open. All are green. **None is merged, and nothing is deployed.**

| PR | Branch | Content | Stacked on |
|---|---|---|---|
| [#98](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/98) | `docs/forecast-adr-0029` | ADR 0029 rewritten (status Accepted, pending your sign-off), ADR 0028 amended, decision list, the two design docs, the UI mock | `main` |
| [#99](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/99) | `feat/forecast-ledger` | Schema (migration `0004`), purge-by-month, both delete cascades, the storage console's sixth target | #98 (docs only; independent code) |
| [#100](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/100) | `feat/forecast-ingest` | The pure classifier, the ledger write path, audit + webhook, the contract's enums in OpenAPI | #99 |
| [#101](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/101) | `feat/forecast-read` | Three read routes over one store query; the session route the SPA uses | #100 |
| [#102](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/102) | `feat/forecast-eval` | The evaluator: sessions, S0, settling, corporate actions, the admin switch | #101 |

**Merge order is the table's order.** Each branch is cut from the one above it, so merging out of
order means rebasing. `docs/forecast-verification` — the original docs branch, pushed earlier — is
**superseded** by `docs/forecast-adr-0029`; do not merge it.

Nothing is deployed, so the feature has never run against a real vendor or a real Dify run. The
evaluator is tested entirely on fixtures with a clock seam and a fetch seam.

## 2. What is left

### 2.1 PRs 6–8 from the plan

| Plan § | Content | Notes |
|---|---|---|
| 6 | **Group statistics API** (`/api/admin/forecast/groups`), plus the member-list endpoint the drawer needs | The biggest remaining piece. Metrics, maturity gates, label permutation, per-cell BH — plan §7. |
| 7 | **UI**: `ForecastPage` (matrix + custom-range tab), the `StockPage` section, the `PriceChart` overlay | Plan §8, and the mock in this directory is the layout. |
| 8 | **e2e section** in `scripts/e2e_check.py`, release notes | Plan §9. |

### 2.2 Dify (a different repository, not started here)

`portal-tools/openapi.dify.ingest.json` and the 1-6-4 workflow's projection node. Plan §5. Work now
exists: branch **`feat/forecast-projection`** in `/Users/kazuha/Codes/StockAnalysisPrediction-Dify`
(renamed from `Kazuha/frosty-perlman-32e92e`; commits `c2de38d` and `040ae4e`, both pushed,
**nothing uploaded or published**). It lives in a worktree at that repo's
`.claude/worktrees/frosty-perlman-32e92e`, and `ops/forecast-projection/README.md` is its handoff —
read that first. Two things to know:

- **The node is written but cannot be sent yet.** It is waiting on §2.4: it must send
  `scenario_basis` / `scenario_provenance` and an RFC3339 `as_of`, and the portal refuses unknown
  fields until #100 is deployed. Do not publish before then.
- **The re-pull changed the picture.** The stale local export (2026-09-18) mapped the prod app to the
  wrong file and had no 理想买入价 / 止损价. Production is **V3.17** (app `a6481c70`), which is the
  trade-strategy version. The projection node, its 34 tests and the ingest schema are all committed
  on that branch.

Two findings in that README that are still open decisions, not bugs: the scenario terminal prices are
LLM-**extracted** (which is what `scenario_basis` / `scenario_provenance` were added to record rather
than to hide), and `baseline.as_of` arrives as an instant on some runs (§2.4).

### 2.3 Decisions still yours

`docs/forecast-verification-decisions.md` §1 and §5. The implementation used the recommended
defaults throughout: **D1** CSI 1000 (`sh000852`, frozen onto each set), **D2** `d5/d20/d60/d120/h12`,
**D3** keep the report and record a rejected set. Nothing is hard-coded in a way that makes a
different answer expensive — except D1 changes what is already written on rows, and D2 is an enum.

**D1 was re-confirmed in a later session**, along with a decision the ADR did not have: the ledger
carries `scenario_basis` as a column (§2.4). The table shape (ADR 0029 §7) still needs sign-off, and
so does the plan's §0.2 list.

### 2.4 The two contract changes that landed in this stack

Both were agreed with the Dify session and had to land on the **portal first**, because the ingest
decoder is `DisallowUnknownFields`: until `v1Ingest` knows these two fields, a producer sending them
gets the whole set refused as `bad_schema`. They are in #99 (the column) and #100 (the decoder).

**1. Two set-level fields on the producer contract.**

- `scenario_basis` — a closed enum, one value today: `"estimated"` (the terminal prices were
  projected from a model, not reported). A **column** on `prediction_sets` (`predColScenarioBasis`),
  because the statistics will group and exclude on it, the way they do on `probability_method` and
  `decision_quality`. The alternative considered and rejected was naming the tool (`"llm"`): the tool
  changes, the statistical meaning does not.
- `scenario_provenance` — a free-form object, **audit only, never a column**, kept verbatim in
  `payload`:
  ```json
  {"producer": "1-4-2/目标价合成", "producer_node": "1788061256164", "extraction": "llm",
   "gates": ["unique_labels","positive_prices","ordered_prices","source_scope","horizon_match","symbol_match"],
   "evidence": {"adoption": "quote"}}
  ```
  `producer`, `producer_node`, `extraction` and `gates[]` are free text and are **not** validated.
  `producer_node` exists because `目标价合成` is the name of **three** nodes in 1-4-2 (`1788061256164`,
  `1788062279656`, `1788683814270`), so the name alone is not a key.
  `evidence.adoption` is a closed binary, `"quote"` / `"locator"`, and is the only part with
  discriminating power: the gate accepts *either* a body quote or a locator, so this field is what
  lets the statistics separate "adopted with a quote from the report body" from "adopted with a
  locator only". The earlier name `citation` was withdrawn for exactly that reason — it claimed more
  than the code checks.

**2. `baseline.as_of` accepts RFC3339 as well as `YYYY-MM-DD`.** `v3decisionadapt` passes through
whatever the company packet's quote says, and a V3.16.2-shaped run has a full instant
(`2026-10-09T15:00:00+08:00`) where V3.17 has a civil date. The portal converts an instant to its
**Shanghai** civil date and stores that in `emitted_base_as_of`; the payload keeps the raw value. It
must not be truncated to the first ten characters: `2026-10-09T23:30:00Z` is `2026-10-10` in Shanghai,
and a truncation is off by a day.

`horizon_end` stays `YYYY-MM-DD` and is **not** widened: it is produced by code, the fix there is to
send a date, and a second accepted shape would only make the horizon comparison vaguer.


## 3. What the reviews found, and what changed because of them

Two adversarial reviews of the design and one of the implementation plan shaped both documents. The
useful parts, because a new session would otherwise re-derive them:

- **Time is anchored on server receipt**, in `Asia/Shanghai`, never on a caller-supplied date and
  never on the panel timezone (which falls back to `time.Local`, and the release image runs `TZ=UTC`).
- **Returns start at `S0`**, the first close *after* the receipt. A move the model could already see
  is not a forecast.
- **Every metric needs a null** (label permutation), and p-values are corrected **per cell**, not per
  page, so a mature cell's number never changes when a new month matures.
- **Refusals become terminal** (`refused_final`) after ten sessions, which is what makes a mature
  cell immutable by construction rather than by caching.
- **`/api/v1` reads DO admit a cookie session** through `canQuery` even though the route class is
  `authBearer` — the class only means it is outside `sessionRoutes()`. This was wrong in the plan for
  a while; corrected in `02e2d23a`. The browser route exists because a browser call belongs under the
  session policy, not because the other route is unreachable.

## 4. Things that will save the next session time

**Read the plan's §0 before writing code.** It lists the repository's binding rules (tests first,
English everywhere, migrations only through `migrate_steps.go`, no `REAL`, no attribution trailers)
and the decisions that block each PR.

**Three bugs the tests caught, worth knowing the shape of:**

1. `readTargets` (PR 1) never learned the `predictions` case, so the storage page's danger-zone
   button silently deleted nothing while the scheduled path worked. There is now a reflection test
   that walks `cleanupTargets`, so the next field added fails there.
2. ADR 0029 §7 — the DDL block the owner signs off on — was missing `prediction_purges` after the
   retention commit. Fixed in `9f2822fd`.
3. `benchMarketCode` rejected `sh000852`, the very spelling it ships as its default.

**Suite-wide tests that will fail if you forget something**, all of which caught real omissions here:

- `TestOpenAPIDocumentsEveryV1Route` — a new v1 route needs a spec entry.
- `TestEveryErrorCodeIsTranslatedInEveryLanguage` — a new `jsonErrorCode` call site needs an `err.*`
  key in all three locales.
- `TestEveryRouteIsInTheTable` / `TestTheMachineSurfaceIsNotASessionClass` — routes go through
  `routes.go`, and `/api/v1` stays class `authBearer`.
- Locale parity (`web/src/locales/locales.test.ts`).

**Locale files are insert-only.** They are large and sorted; a JSON round-trip reformats hundreds of
lines. Insert the new key at its alphabetical neighbour by text edit and the diff is one line.

**`gofmt` re-aligns whole struct literals.** Adding a field to `Server` re-indents the entire struct;
append it at the end of the block instead, or accept a large cosmetic diff.

**There is a `.env` in the Dify repo** with live credentials for the export script. Never print its
values.

## 5. Environment notes

- Local Postgres works: `TEST_POSTGRES_DSN="postgres://portal:portal@localhost:5432/reports_test?sslmode=disable"`
  runs the dual-driver tests. Clean up after.
- `go test ./...` with that DSN, plus `gofmt -l .`, `go vet ./...`, `staticcheck@2026.2.1 ./...`,
  `cd web && npm run typecheck && npx vitest run` is the full local gate. CI runs the same plus race
  and a docker smoke test.
- The repository root's `CLAUDE.md` is local-only and gitignored; it is accurate about the rules.

## 6. Model / tooling note

This session's model was set to `claude-opus-5` in the desktop app, while the account's provider
only served `deepseek/deepseek-v4.1-flash`. Every request under the other model came back **400**, and
the session could not change its own model. If a new session shows the same symptom, switch the
model in the app's picker before starting — it is not something the session can fix from inside.

No subagents were used after the point where this was noticed, and **no agent should have its model
specified by the caller**; let the app's router decide.
