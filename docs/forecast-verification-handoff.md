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

`portal-tools/openapi.dify.ingest.json` and the 1-6-4 workflow's projection node. Plan §5. The
session that was handed this did not get anywhere — **start it fresh, in
`/Users/kazuha/Codes/StockAnalysisPrediction-Dify`**, and read plan §5 and §0.3 first. Two things
that will bite whoever picks it up:

- **The local export is stale** (2026-09-18). It maps prod app `15249be5` to the *V2* file, and it
  contains no node computing the 理想买入价 / 止损价 that recent reports print. Re-pull with
  `download_dify_apps.py` before trusting any field mapping.
- **It must not run before PR 2 is deployed.** The projection node talks to a running portal.

### 2.3 Decisions still yours

`docs/forecast-verification-decisions.md` §1 and §5. The implementation used the recommended
defaults throughout: **D1** CSI 1000 (`sh000852`, frozen onto each set), **D2** `d5/d20/d60/d120/h12`,
**D3** keep the report and record a rejected set. Nothing is hard-coded in a way that makes a
different answer expensive — except D1 changes what is already written on rows, and D2 is an enum.

Also unanswered: **the table shape** (ADR 0029 §7) needs your sign-off, and the plan's §0.2 list.

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
