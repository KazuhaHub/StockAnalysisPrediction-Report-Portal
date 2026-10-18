# Forecast verification (预测验证): implementation plan

**Audience:** the engineer who builds this feature. **Status:** handoff document. Nothing in it is
implemented. The *why* behind every rule is in
[forecast-verification-design.md](forecast-verification-design.md) (draft 4, cited below as
"design §n"). This document is the *how*: files, signatures, DDL, algorithms, PR order and tests.
Where the two disagree, stop and ask the owner; do not pick one.

This revision folds in an adversarial review of the first version against the code (25 findings;
§12 lists them).

A static UI mock, [forecast-verification-demo.html](forecast-verification-demo.html), sits next
to this document. Open it in a browser. Its monthly page uses made-up numbers, and its report
section uses real prices for one 2026-09-17 report. Treat it as a layout sketch, not a spec: where
it and this document differ, this document wins.

---

## 0. Before you write any code

### 0.1 Rules of this repository that bind this work

- **Tests first.** Every PR below lists its tests. Write them so they fail first (Go `_test.go` next
  to the source; vitest next to the component).
- **English** in all code, comments, logs, commits and PR text. Chinese appears only in i18n `zh-*`
  values and in data values (`rtype` names such as `投资决策建议`).
- **Schema changes are a migration step** (`internal/app/migrate_steps.go`, ADR 0035), never an
  edit to `baseSchemaStmts()`. The schema shape in §2 is still subject to the owner's sign-off
  (PR 0).
- **No `REAL` columns.** Prices are integer fen, ratios integer basis points.
- **Both report-delete paths** (`DeleteReport`, `deleteReportChunk`) change in the same PR that adds
  the tables.
- **Every route is registered through `routes.go`.** `TestNothingRegistersARouteOutsideTheTable`
  enforces it. Every `/api/v1` route is documented in `openapi.json`
  (`TestOpenAPIDocumentsEveryV1Route`).
- **All UI copy goes through `t()`**, with keys in the zh-CN, zh-TW and en-US bundles. The parity
  test is `web/src/locales/locales.test.ts`.
- **No commit trailers or agent attribution.** Branch names describe the work
  (`feat/forecast-ledger`, …).

### 0.2 Decisions that must be made first

The owner has settled the feature name (预测验证), the two viewing modes (design §8.5) and the
page placement (D6). The rest of design §12 is **open**.

| Decision | Blocks | Recommended default (design §12) |
|---|---|---|
| D1 Benchmark | PR 1 (`bench_code` is set at insert) | `sh000852` (CSI 1000) for every set |
| D2 Checkpoints | PR 1 (enum), PR 5 | `d5`, `d20`, `d60`, `d120`, `h12` |
| D3 Invalid predictions | PR 2 | Keep the report; write a `rejected` set, an audit row and a webhook |
| D4 Membership | PR 6 | The earliest `accepted` set per `(symbol, group)` decides. If that set is degraded or `mismatch`, the symbol is **excluded** for the group: a later rerun cannot replace it (no selection by rerun). |
| D5 Purge | PR 1 | Ledger outlives reports (`report_id` → NULL) |
| D7 3-5 重组决策 | scope only | Out of v1 |
| D8 Corporate actions | PR 5 | Detect from the dividend element of the unadjusted feed first (§6.4); exclude and count |
| D9 Corroboration | PR 5 | Single vendor (Tencent) in v1 |

Do not start a PR whose blocking decision is still open. Every section below is written against the
recommended defaults and marks the places where a different decision would change it.

### 0.3 Prerequisite: re-pull the Dify export

The local export of 1-6-4 is from 2026-09-17/18 and does not match prod (design §3). Re-pull it
(`download_dify_apps.py`, HTTPS base URL, `__Host-` cookies) and confirm every row of the PR 4
source table against the live graph before PR 4 starts. If a field turns out to be produced by an LLM
rather than a code node, it leaves the contract; tell the owner.

---

## 1. Constants and vocabulary (Go)

Put these in one new file, `internal/app/forecast_vocab.go`. Mirror every enum in `openapi.json` as
`enum`; these are the first enums in that file.

```go
// Report types whose ingest is covered by the ledger (design §3). Coverage is defined over this set.
var predictionRtypes = map[string]bool{"投资决策建议": true}

const (
    predSchemaV1      = "predictions.v1"
    predPayloadMax    = 64 << 10        // bytes, design §5.4
    predZone          = "Asia/Shanghai" // design §4; never the panel timezone
    predCloseCutoff   = "15:30"         // a session's bar is usable only after this, Shanghai time
    predStdHorizonTol = 7               // days, design §5.3
    predRefusalCapBp  = 500             // 5 %, design §8.2
    predMinN          = 30
    predPermutations  = 10000
    predMaxRangeMonths = 36
    predMaxWeekRangeWeeks = 26
    predBacktestSlack  = 72 * time.Hour // information_cutoff older than made_at minus this = backtest
)

// Set states (design §5.5).
const (
    predStateAbsent   = "absent"
    predStateRejected = "rejected"
    predStateBacktest = "backtest"
    predStateAccepted = "accepted"
)

// Claim kinds, stances, labels.
const (
    kindStance = "stance"; kindTerminal = "terminal_value"; kindScenario = "scenario"; kindEntry = "entry_price"
    kindStopLoss = "stop_loss" // reserved: refused until a code node computes it
    stanceLong = "long"; stanceNoPosition = "no_position"
    labelBull = "bull"; labelBase = "base"; labelBear = "bear"
)

// Upstream vocabularies, copied from the live 1-6-4 graph (re-confirm in §0.3).
var predDecisionStatus  = []string{"eligible", "no_trade"} // the only statuses that carry predictions
var predDecisionQuality = []string{"normal", "degraded", "repaired"}
var predProbMethods     = []string{"approved_valuation_source", "approved_policy_default", "subjective_committee", "unavailable"}

// Checkpoints (D2). Order matters: it is the column order everywhere.
var predCheckpoints = []string{"d5", "d20", "d60", "d120", "h12"}
var predCheckpointSessions = map[string]int{"d5": 5, "d20": 20, "d60": 60, "d120": 120} // h12 is calendar-based

// Mark status and refusal codes. "refused" retries; "refused_final" is terminal (§6.3), which is
// what lets a mature cell never change (§7.4).
const (markPending = "pending"; markRefused = "refused"; markRefusedFinal = "refused_final"; markResolved = "resolved")
const predRetryDeadlineSessions = 10 // a refusal becomes final this many sessions after its due session
var predRefusals = []string{"no_bar", "stale_bar", "unverifiable"}

// Baseline check (design §9.4).
const (baseCheckPending = "pending"; baseMatch = "match"; baseMismatch = "mismatch"; baseRefused = "refused"; baseRefusedFinal = "refused_final")

// Ingest rejection reasons. Each is stable, so tests can assert on it.
var predRejectReasons = []string{
    "not_object", "too_large", "bad_schema", "bad_enum", "bad_price", "bad_probability",
    "probability_sum", "missing_claim", "duplicate_key", "reserved_kind", "horizon_not_future",
    "symbol_out_of_scope", "bad_date",
}
```

---

## 2. Data model (PR 1)

### 2.1 Migration step

Append `migration0002()` to `(*Store).migrations()` in `migrate_steps.go`. Never edit `0001`.

```go
func migration0002() migration {
    return migration{
        id:      "0002",
        tables:  []string{"prediction_sets", "prediction_claims", "prediction_marks"},
        indices: []string{"idx_pred_sets_idem", "idx_pred_sets_group", "idx_pred_sets_symbol",
            "idx_pred_claims_key", "idx_pred_marks_cp", "idx_pred_marks_due"}, // all six; verifyMigrationProducts checks each by name
        restore: restoreRule{olderDumpOK: true}, // new and empty: a pre-step dump restores cleanly
        up:      func(e migExec) error { /* the CREATE statements in §2.2, via e.exec */ },
    }
}
```

`migration0001` declares no tables, so this is the first step that declares `tables`.
`backupTableNames` (`migrate_steps.go:292`) picks the new tables up from that list. Confirm this with
the existing backup round-trip test, extended (PR 1 tests).

The `CREATE` statements must be idempotent (`IF NOT EXISTS`), the same way the base schema writes
them. Use `pkAuto()` for primary keys. Write `TEXT`, `INTEGER` and `BIGINT` literally, as
`store.go:324-327` does. Id columns that reference another row are `BIGINT`, matching `reports.id`
on Postgres.

### 2.2 DDL

```sql
CREATE TABLE IF NOT EXISTS prediction_sets(
  id {pkAuto},
  report_id BIGINT,                         -- NULL after the report is purged
  symbol TEXT NOT NULL DEFAULT '',
  rtype TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  report_date TEXT NOT NULL DEFAULT '',     -- the report's own (caller) date, display only
  made_at TEXT NOT NULL,                    -- RFC3339 UTC, server receipt
  made_on TEXT NOT NULL,                    -- YYYY-MM-DD, Asia/Shanghai
  cohort TEXT NOT NULL,                     -- YYYY-MM, Asia/Shanghai
  zone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
  state TEXT NOT NULL,                      -- absent|rejected|backtest|accepted
  errors TEXT NOT NULL DEFAULT '',          -- JSON array of {reason, path, detail}
  payload TEXT NOT NULL DEFAULT '',
  payload_truncated INTEGER NOT NULL DEFAULT 0,
  payload_sha256 TEXT NOT NULL DEFAULT '',
  body_sha256 TEXT NOT NULL DEFAULT '',
  emitter_app TEXT NOT NULL DEFAULT '', emitter_version TEXT NOT NULL DEFAULT '',
  calculator TEXT NOT NULL DEFAULT '', schema_name TEXT NOT NULL DEFAULT '',
  policy_version TEXT NOT NULL DEFAULT '', probability_method TEXT NOT NULL DEFAULT '',
  probability_transform TEXT NOT NULL DEFAULT '',
  decision_status TEXT NOT NULL DEFAULT '', decision_quality TEXT NOT NULL DEFAULT '',
  information_cutoff TEXT NOT NULL DEFAULT '',
  horizon_end TEXT NOT NULL DEFAULT '', horizon_std INTEGER NOT NULL DEFAULT 0,
  emitted_base_fen BIGINT, emitted_base_as_of TEXT NOT NULL DEFAULT '', emitted_base_source TEXT NOT NULL DEFAULT '',
  s0_date TEXT NOT NULL DEFAULT '',         -- calendar S0 (index session), §6.3
  base_date TEXT NOT NULL DEFAULT '',       -- the stock's first traded bar on or after S0
  s0_fen BIGINT,                            -- the stock's unadjusted close on base_date
  base_check TEXT NOT NULL DEFAULT 'pending',
  bench_code TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pred_sets_idem   ON prediction_sets(report_id, payload_sha256);
CREATE INDEX        IF NOT EXISTS idx_pred_sets_group  ON prediction_sets(rtype, made_on, state);
CREATE INDEX        IF NOT EXISTS idx_pred_sets_symbol ON prediction_sets(symbol, made_on);

CREATE TABLE IF NOT EXISTS prediction_claims(
  id {pkAuto},
  set_id BIGINT NOT NULL,
  claim_key TEXT NOT NULL,
  kind TEXT NOT NULL,
  stance TEXT NOT NULL DEFAULT '', label TEXT NOT NULL DEFAULT '',
  price_fen BIGINT, prob_bp INTEGER, weight_bp INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pred_claims_key ON prediction_claims(set_id, claim_key);

CREATE TABLE IF NOT EXISTS prediction_marks(
  id {pkAuto},
  set_id BIGINT NOT NULL,
  checkpoint TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  refusal TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL DEFAULT '',
  session_date TEXT NOT NULL DEFAULT '', bar_date TEXT NOT NULL DEFAULT '',
  halted INTEGER NOT NULL DEFAULT 0, corp_action INTEGER NOT NULL DEFAULT 0,
  close_fen BIGINT, low_fen BIGINT, high_fen BIGINT,
  ret_bp INTEGER, bench_ret_bp INTEGER, bench_code TEXT NOT NULL DEFAULT '',
  resolved_at TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pred_marks_cp  ON prediction_marks(set_id, checkpoint);
CREATE INDEX        IF NOT EXISTS idx_pred_marks_due ON prediction_marks(status, next_attempt_at);
```

Notes:

- The column is `schema_name` rather than `schema`, for clarity next to "migration schema". (`schema` is non-reserved on Postgres; the rename is not required by the dialect.)
- `idx_pred_sets_idem` allows any number of NULL `report_id`s on both SQLite and Postgres (NULLs are
  distinct in unique indexes on both), so purged sets never collide. Assert this in a test on both
  drivers.
- No foreign keys, consistent with the rest of the schema. Integrity is enforced by the write path
  and by the cascade below.
- Group queries filter on `rtype` equality plus a `made_on` range, so the index leads with
  `rtype`. `cohort` is kept for display and export.

### 2.3 Cascade on report delete (D5)

In both of these, in the same transaction, after the `reports` row is deleted:

```sql
UPDATE prediction_sets SET report_id = NULL WHERE report_id = ?
```

- `store.go:1882` `DeleteReport`.
- `cleanup_store.go:217` `deleteReportChunk`. Run it only when the guarded `DELETE FROM reports
  WHERE id=? AND sent_at=?` matched a row, like the other child tables.

Update the doc comment on `deleteReportChunk`. Today it explains the viewer and revision rows but
not the id-reuse argument (ADR 0029 §cascade). Say there that `prediction_sets` is **nulled, not
deleted**, and why.

### 2.4 Storage console

In `cleanup_api.go:172` `apiCleanupUsage`, add a category `predictions`: rows and bytes across the
three tables; `eligible` is the target's own predicate count. The ledger is the sixth cleanup target
(ADR 0029 §8), off by default. Add i18n
`storage.cat.predictions`. There is no change to `cleanupTargets`.

### 2.5 PR 1 tests

- `TestMigration0002AppliesOnBaseline`: on a fresh v0.4.72-shape DB the step applies, `mig:0002` is
  recorded, and `verifyMigrationProducts` passes. Run on SQLite and on Postgres when
  `TEST_POSTGRES_DSN` is set.
- `TestMigration0002OlderDumpRestores`: a dump taken before the step restores, and the tables exist
  afterwards and are empty.
- `TestBackupIncludesPredictionTables`: a backup round-trip carries rows in all three tables.
- `TestDeleteReportNullsPredictionSets` and `TestCleanupChunkNullsPredictionSets`: claims and marks
  survive, `report_id` is NULL, and a new report row inherits nothing. On both drivers.
- `TestPredSetIdemIndexAllowsManyNullReportIDs`: on both drivers.
- `TestStorageUsageListsPredictions`.

---

## 3. Ingest (PR 2)

### 3.1 Request and response

The v1 ingest request (`apiv1.go:161`, an anonymous struct) gains:

```go
Predictions json.RawMessage `json:"predictions"`
```

Keep it raw. The classifier must see the bytes in order to tell an object from a string and to
enforce the size cap.

The success response gains `"predictions_status"`, with one of these values:

- `"none"`: the rtype is not covered and there is no payload;
- `"absent"`, `"rejected"`, `"backtest"`, `"accepted"`;
- `"store_failed"`.

A `rejected` response also carries `"predictions_errors": [...]`. The HTTP status is **200 in every
case**. The report itself is never refused because of `predictions` (design §5.5).

### 3.2 Pure classifier

New file `internal/app/forecast_ingest.go`:

```go
type predInput struct {
    Raw        json.RawMessage
    Rtype      string
    Symbol     string    // after normalizeSymbol
    MadeAt     time.Time // server receipt, UTC
    BodySHA256 string
}

type predSetRow struct { /* every prediction_sets column except id/report_id */ }
type predClaimRow struct { /* every prediction_claims column except id/set_id */ }
type predError struct { Reason, Path, Detail string }

// classifyPredictions has no side effects and makes no DB calls. It returns the set row to write
// (nil when nothing is written) and the claims (empty unless state is accepted or backtest).
func classifyPredictions(in predInput) (*predSetRow, []predClaimRow, []predError)
```

**Order of checks.** The first failing check decides `rejected`, but collect all errors that can be
collected.

1. **Raw empty or `null`.** If `predictionRtypes[rtype]`, return state `absent`. Otherwise return
   nil (no row).
2. **Size.** `len(Raw) > predPayloadMax` → `too_large`. Store the first 64 KiB and set
   `payload_truncated=1`.
3. **Shape.** The first non-space byte must be `{`, else `not_object`. This is what catches Dify
   passing an unparseable template result as a string.
4. **Decode** into a struct with `DisallowUnknownFields()`. Failure → `bad_schema`. Unknown fields
   are refused here, unlike the report body, because the contract is new and strict.
   **Every price, probability and weight field is typed `json.RawMessage`**, never `string`.
   `encoding/json` refuses a JSON number for a string field at decode time, which would surface as
   `bad_schema` and make the `bad_price` rule unreachable.
5. `schema == "predictions.v1"`, else `bad_schema`.
6. **Enums** (`decision_status`, `decision_quality`, `probability_method`, `kind`, `stance`,
   `label`) → `bad_enum`. `kind == "stop_loss"` → `reserved_kind`.
7. **Prices:** `parseFen(raw json.RawMessage) (int64, error)`. The input must be a JSON **string** matching
   `^\d{1,7}(\.\d{1,2})?$`. A JSON number, a third decimal, a sign or an exponent → `bad_price`.
   Conversion is integer arithmetic on the digits, never `strconv.ParseFloat`.
8. **Probabilities and weights:** `parseBp(raw json.RawMessage) (int32, error)`. A string matching
   `^(0(\.\d{1,4})?|1(\.0{1,4})?)$`; otherwise `bad_probability`.
9. **Claims.** Keys are unique (`duplicate_key`). Required: `stance`, `terminal_value`, and the
   three `scenario` labels exactly once each (`missing_claim`). Scenario `prob_bp` must sum to
   exactly 10000 (`probability_sum`).
10. **Dates.** `horizon_end` and `baseline.as_of` are `YYYY-MM-DD`; `information_cutoff` is
    RFC3339. Anything else → `bad_date`.
11. **Horizon.** Compute `made_on` from `MadeAt` in `predZone`, never with `panelLocation()`.
    `horizon_end <= made_on` → `horizon_not_future`. Then
    `horizon_std = |horizon_end − (made_on + 12 months)| ≤ 7 days`.
12. **Symbol.** It must be six digits, `marketPrefix(symbol) ∈ {sh, sz}`, and not 200xxx or 900xxx.
    Otherwise `symbol_out_of_scope`. An empty symbol is also out of scope.
13. **Backtest.** Compare **instants**, not dates: `information_cutoff < made_at − predBacktestSlack`
    (72 h) → state `backtest`. The set is still stored with its claims, but gets no marks.
    - A date comparison would misclassify ordinary runs. `v3config` defaults the cutoff to
      `report_date T23:59:59+08:00`, so a run finishing after Shanghai midnight, or one dated the
      previous trading day, would look historical.
    - The slack covers weekends and the UTC-sandbox day lag.
    - Confirm on the live graph (§0.3) what the ingest date node feeds into `report_date`.
14. Otherwise state `accepted`.

The set row always records `made_at`, `made_on`, `cohort`, `zone`, `payload` (verbatim, capped),
`payload_sha256` (of the raw bytes), `body_sha256`, the emitter/policy fields when decodable,
`bench_code` (from D1) and the emitted baseline.

### 3.3 Write path in `v1Ingest`

Today's order is at `apiv1.go:245-307`.

1. **Before `RegisterType`** (`apiv1.go:246`), and after `normalizeSymbol`, call `classifyPredictions`. It writes nothing. Confirm in code that the normalised symbol is available at that point; if it is not, move the normalisation up rather than classifying on the raw symbol.
2. **Ingest the report exactly as today**, then capture the report id.
3. **If a set row was returned**, call
   `s.st.InsertPredictionSet(reportID, set, claims) (setID int64, dup bool, err error)`, in a
   transaction of its own:
   - insert the set (on `idx_pred_sets_idem` conflict, return `dup=true`, change nothing and skip
     the rest);
   - insert its claims;
   - for `accepted`, insert one `pending` mark per checkpoint. `h12` only when `horizon_std = 1`.
4. **On error**, retry once. If it still fails, set `predictions_status = "store_failed"`, log, and
   call `WriteAudit` with action `AuditPredictionsStoreFailed`. Do not return 5xx (design §6.5).
5. **For `rejected`**:
   - `s.recordChange(r, "", AuditPredictionsRejected, "report", id, map[string]any{"errors": …})`
     (`audit.go:187`). This is the same call ingest already uses (`apiv1.go:283`), so the IP and the
     token name are kept. Do not call bare `WriteAudit`.
   - `fireEvent(EventPredictionsRejected, {report_id, symbol, rtype, made_at, errors})`.

New constants:

| Constant | Value | Where |
|---|---|---|
| `AuditPredictionsRejected` | `"predictions.reject"` | `audit.go`, **and** appended to `auditVocabulary` (`audit.go:373`) |
| `AuditPredictionsStoreFailed` | `"predictions.store_failed"` | `audit.go`, **and** appended to `auditVocabulary` |
| `EventPredictionsRejected` | `"predictions.rejected"` | `webhook_run.go`, added to `availableEvents()` (`webhook_api.go:15`) |

i18n keys: `audit.a.predictions.reject` and `audit.a.predictions.store_failed`. Actions use
`audit.a.*` (`AuditPage.tsx:347`); `audit.t.*` is for target types. Also
`webhook.event.predictions.rejected`. The vocabulary/label tests (`audit_gaps_test.go:59`,
`audit_auth_test.go:357`) fail until all of these exist in every locale file.

### 3.4 OpenAPI

- **`IngestRequest`** gets a `predictions` object schema, with enums for every closed vocabulary
  and `pattern` for price and probability strings.
- **Response:** add `predictions_status` (enum) and `predictions_errors`.
- **`TestOpenAPIIngestRequestMatchesRuntimeValidation`** (`openapi_test.go:39`) will need the new
  field. Extend it so the patterns and enums in the spec are the ones the Go validator uses. Derive
  both from the §1 constants, or test them against each other.
- **Dify-side copy:** keep `portal-tools/openapi.dify.ingest.json` (Dify repo) in sync in PR 4.

### 3.5 PR 2 tests

These are design §11 #1–9 and #21. All go in `forecast_ingest_test.go`: table-driven tests for the
classifier, plus HTTP tests through `v1Ingest`.

- **Classifier table.** One row per reason in `predRejectReasons`, plus these boundaries:
  - `"8.655"`, JSON number `8.65`, `"-1.00"` and `"1e2"` → `bad_price`;
  - `"1.0001"` and `"0.33333"` → `bad_probability`;
  - scenario sums of 9999 and 10001 → `probability_sum`; 10000 accepted;
  - a string-valued payload → `not_object`;
  - 64 KiB + 1 byte → `too_large` with `payload_truncated=1`;
  - an unknown field → `bad_schema`;
  - `stop_loss` → `reserved_kind`.
- **Horizon.**
  - Receipt at 2026-10-17T23:30Z (Shanghai 10-18) with `horizon_end` = 2026-10-18 →
    `horizon_not_future`. Run with the panel timezone set to UTC to prove it is not used.
  - `horizon_end` one day short of 12 months → `horizon_std=1`.
  - 6 months → accepted, `horizon_std=0`.
- **Symbols.** `bj` 830799, 900901, 200002 and an empty symbol → `rejected` set, and the report
  is still stored.
- **No early writes.** An invalid payload writes no rows in `report_types` / `report_versions`
  beyond those written by the same request without `predictions`. Diff the row counts.
- **Coverage rows.** A covered rtype without `predictions` → an `absent` row; an uncovered rtype →
  no row.
- **Idempotency.** An identical retry → one set and `dup=true`. A changed payload → a second set.
- **Store failure.** Inject the failure through a test hook on `InsertPredictionSet`: two failures
  → 200, `store_failed`, and an audit row.
- **Side effects of a rejection.** One audit row, and one webhook delivery attempt with event
  `predictions.rejected`.

---

## 4. Read API for one report and one symbol (PR 3)

### 4.1 Routes

**Machine routes**, in `routes.go`, registered with `v1(...)` (`s.bearer`). The v1 surface is
**token-only**: `routes.go:33` says "a token opens it, a session does not", and
`TestTheMachineSurfaceIsNotASessionClass` (`routes_test.go:123`) enforces that. Document both in
`openapi.json` with `"x-scope": "query"`.

| Route | Handler | Returns |
|---|---|---|
| `GET /api/v1/reports/{id}/predictions` | `v1ReportPredictions` | every set ever written for the report (all states), oldest first, each with claims and marks, plus `counted_set_id` (§6.2, for the monthly group) |
| `GET /api/v1/predictions?symbol=&from=&to=&limit=&cursor=` | `v1QueryPredictions` | sets, newest first, paged |

**Browser route.** `GET /api/report/{id}/predictions` is registered with `s.session(...)`
(`routes.go:59`), so the whole-portal session policy (enrolment gate, `sessionRoutes()`) applies.
Its handler is `apiReportPredictions`, it returns the same body as the per-report v1 route, and it
shares the store function. The SPA uses **only** this route; it never calls `/api/v1`.

### 4.2 Access

- **v1 routes:** the scope comes from `s.queryScope(r)` (`apiv1.go:332`), which is nil for Bearer
  callers.
- **Browser route:** the scope comes from `s.viewerScope(user)` (`report_owner.go:162`), which is
  nil for users who are not restricted.

- **Live sets** (`report_id NOT NULL`): `JOIN reports r ON r.id = ps.report_id`, then append
  `sc.where("r.")`, the same pattern as `QueryTracking` (`store.go:1624`). When the report itself is
  unreadable, `/reports/{id}/predictions` answers 404, the same as reading the report.
- **Purged sets** (`report_id IS NULL`): included **only when `sc == nil`**. A restricted caller
  cannot prove access to a report that no longer exists. **Fail-closed.**
- **`owner_group` is never read.** A test sets a readable group on an unreadable report and expects
  a refusal.

### 4.3 Response shape

The same for both routes:

```json
{
  "ok": true,
  "sets": [{
    "id": 812, "report_id": 13920, "state": "accepted",
    "symbol": "603893", "rtype": "投资决策建议", "title": "…", "report_date": "2026-10-16",
    "made_at": "2026-10-16T14:40:12Z", "made_on": "2026-10-16", "cohort": "2026-10",
    "decision_status": "eligible", "decision_quality": "normal",
    "probability_method": "approved_policy_default", "policy_version": "…",
    "horizon_end": "2027-10-16", "horizon_std": true,
    "baseline": { "emitted_fen": 17680, "emitted_as_of": "2026-10-16", "check": "match" },
    "s0": { "date": "2026-10-19", "fen": 17550 },
    "bench_code": "sh000852",
    "body_changed": false,
    "errors": [],
    "claims": [{ "key": "stance", "kind": "stance", "stance": "long", "weight_bp": 1000 }, "…"],
    "marks": [{ "checkpoint": "d5", "status": "resolved", "session_date": "2026-10-26", "bar_date": "2026-10-26",
                "close_fen": 18010, "ret_bp": 262, "bench_ret_bp": -110, "halted": false, "corp_action": false }, "…"]
  }],
  "next_cursor": ""
}
```

- `body_changed` compares the set's `body_sha256` with the hash of the report's current `body_md`.
- `payload` is **not** included in list responses. Fetch it with `?include=payload` on the
  per-report route.

### 4.4 PR 3 tests

- Scope tests mirroring the tracking ones: restricted, unrestricted, Bearer.
- The browser route refuses an anonymous caller (it is covered by `TestSessionRoutesRefuseAnAnonymousCaller`).
- `TestTheMachineSurfaceIsNotASessionClass` still passes.
- Purged-set visibility.
- The `owner_group` decoy.
- Paging stability.
- The openapi coverage test passes.

---

## 5. Dify projection node (PR 4, in the Dify app, not this repo)

### 5.1 Placement

1-6-4 gets a new **code** node, `预测投影` / `predictions-projection`. Its upstreams are
`v3decisionadapt`, `v3calc`, `v3risk` (after `v3riskfix`); its downstream is the ingest node
`94cd6bcaI0`. It outputs one variable, `predictions` (object), which the ingest node passes as the
`predictions` parameter. The ingest node currently reads its date and run id from `94cd6bcaD0`. The
projection only has to be an ancestor of the ingest node; it does not have to be its direct
predecessor. Add the property to `portal-tools/openapi.dify.ingest.json` and re-register
the custom tool so Dify sees it.

### 5.2 Source mapping

Confirm each row on the live graph (§0.3).

| Output field | Source |
|---|---|
| `baseline.price` / `as_of` / `source` | `decision.entry_price` / `entry_price_as_of` / `entry_price_source` |
| `information_cutoff` | `decision.information_cutoff` |
| `horizon_end` | `decision.investment_horizon_end` |
| `decision_status` | `calc.decision_status` |
| `decision_quality` | `v3decisionadapt` quality field |
| `probability_method` | `decision.probability_method` |
| `probability_transform` | `rule_id` of the calibration applied, else `null` |
| `policy_version` | `policy_json` version |
| `stance` / `target_weight` | table below + `risk.target_total_weight` |
| scenarios | `calc.scenario_results[]` (`scenario_id`, `terminal_price`, `probability_used`) |
| `expected_terminal` / `p_profit` | `calc.expected_terminal_price` / `calc.p_profit` |
| `entry_price` | `calc.suggested_entry.recommended_buy_price` |
| `emitter` | `{app: "1-6-4", version: "V3.16.x", calculator: calc.calculator_version, decision_schema: decision schema_version}` |

Stance table, from design §5.3:

| `decision_status` | `target_total_weight` | Output |
|---|---|---|
| `eligible` | > 0 | `long` |
| `eligible` | 0 | `no_position` |
| `no_trade` | any | `no_position` |
| anything else | any | **emit nothing**: the node outputs `null`, and the portal records `absent` |

### 5.3 Rounding: the node's only arithmetic

```python
from decimal import Decimal, ROUND_HALF_EVEN

def dec(x):
    # repr() gives the shortest string that round-trips the float, so Decimal sees what Python printed,
    # not the binary expansion. round(8.655, 2) on a float is NOT decimal half-even.
    return Decimal(repr(x)) if isinstance(x, float) else Decimal(str(x))

def price(x):  return str(dec(x).quantize(Decimal("0.01"),   rounding=ROUND_HALF_EVEN))
def prob(x):   return str(dec(x).quantize(Decimal("0.0001"), rounding=ROUND_HALF_EVEN))

def scenario_probs(ps):
    q = [dec(p).quantize(Decimal("0.0001"), rounding=ROUND_HALF_EVEN) for p in ps]
    gap = Decimal("1.0000") - sum(q)
    # absorb the residue in the term with the largest RAW value; ties go to the last index
    i = max(range(len(q)), key=lambda k: (dec(ps[k]), k))
    q[i] += gap
    return [str(v) for v in q]
```

Required test vectors. Run them in a scratch Python before pasting into the node, and keep them as
the node's documentation:

| Input | Expected |
|---|---|
| `price(8.655)` | `"8.66"` (repr is `8.655`; half-even goes to the even digit 6) |
| `price(8.650000000000001)` | `"8.65"` |
| `price(207.55)` | `"207.55"` |
| `scenario_probs([0.33333, 0.33333, 0.33334])` | `["0.3333", "0.3333", "0.3334"]` (sum 1.0000) |
| `scenario_probs([0.25, 0.5, 0.25])` | `["0.2500", "0.5000", "0.2500"]` |

### 5.4 Rollout in Dify

1. Apply on the **test copy** of 1-6-4 first.
2. Draft-run one symbol and confirm the portal response shows `predictions_status: "accepted"`.
3. Confirm the set through `GET /api/v1/reports/{id}/predictions`.
4. Publish the prod app.
5. If 1-6-4 is also consumed as a workflow-tool by another app, re-sync that tool provider after
   publishing. A re-published sub-workflow is **not** picked up by callers until it is re-synced.
6. Do not move node positions on the canvas through the API while someone has the canvas open.

**Done when** a day of prod runs shows only `accepted` / `absent` sets, and no `rejected` sets.

---

## 6. Evaluator (PR 5)

### 6.1 Switch and settings

| Key in `meta` | Default | Meaning |
|---|---|---|
| `forecast_auto` | `"0"` | the evaluator runs only when `"1"` |
| `forecast_bench` | `"sh000852"` | D1 default for new sets (sets keep their own `bench_code`) |

Admin routes in `routes.go`, through `s.admin(...)`, shaped like `geo_update.go:539`
(`apiGeoSave`):

- `GET /api/admin/forecast/settings`
- `POST /api/admin/forecast/settings`

The UI switch ships in this PR. A setting without a writer and a UI is not allowed.

### 6.2 Loop

New file `internal/app/forecast_eval.go`, with `type forecastEvaluator struct { mu sync.Mutex; running bool }`.
Start it from `server.go` beside the other loops (`server.go:232-235`).

```go
func (e *forecastEvaluator) loop() {
    for range time.Tick(time.Minute) {
        if s.st.GetSetting("forecast_auto", "0") != "1" { continue } // no fetch, no write
        if !e.tryStart() { continue }                                 // single-flight
        e.tick(ctx, now)                                              // bounded work per tick
        e.finish()
    }
}
```

**Due work** each tick:

- **Baseline:** sets with `state='accepted' AND base_check IN ('pending','refused')`, plus marks
  with `status IN ('pending','refused') AND next_attempt_at <= now`.
- **Batching:** group the due work by set, and process at most 50 sets per tick.
- **Fetches:** call `fetchTencentRange(ctx, market, code, from, to)` (`quote.go:1570`) **directly**:
  - one fetch per **set** for the stock;
  - one fetch per tick for the **index**, shared by every set.

  The window runs from `made_on − 10` calendar days (so the baseline check has the previous closes
  even after a weekend or holiday) to `min(today, latest due session)`.
- **Rate limiting:** wrap every fetch in `release, err := s.quotes.acquire(ctx)` (`quote_cache.go:905`)
  and `defer release()`, so the vendor ceiling shared with readers is respected.
- **Do not use `fetchUnder`, and do not use the cache.**
  - `fetchUnder` walks the operator's source chain (Yahoo, Sina, …), which breaks D9.
  - It also serves cached bodies, so a response fetched at 14:57 could be served at 15:31 and
    freeze an intraday price.
  - Every per-set window would also be a new cache key, flooding the LRU that readers share.
- **Source:** record `resp.Source` in `source`. If it is not Tencent, refuse `unverifiable`.

### 6.3 Algorithms

All dates are in `Asia/Shanghai`, resolved with `quoteZone("Asia/Shanghai")` (`quote.go:632`).

**Completed session.** A bar dated `D` is usable only if **both** of these hold:

- the fetch was made (wall clock, no cache) at or after `D 15:30` Shanghai;
- when the response carries a vendor timestamp (`resp.Snapshot.AsOf`, the vendor's clock),
  `AsOf ≥ D 15:00`.

Tencent returns today's unfinished bar during trading (fixture
`internal/app/testdata/quote/tencent_fqkline_sh000001.json`, `AsOf` 14:57:03). That body must never
resolve anything, **whatever `now` is**.

**Session calendar.** The ordered dates of the **benchmark index's** completed bars. A stock's own
bars are never used for counting.

**S0 (start session).** The first calendar session `D` such that `D 15:00 Shanghai > made_at`.
`made_at` in the lunch break or before the open still gives S0 = that day; on a non-session day it
gives the next session.

| `made_at` (Shanghai) | S0 |
|---|---|
| Session T, 10:00 | T |
| Session T, 16:00 | T+1 |
| Saturday | Monday |
| T is a holiday | next session |

There are two base dates, and each formula below names which one it uses:

- **`s0_date`** = calendar S0. Checkpoint counting (`dN`) uses it.
- **`base_date`** = the stock's first traded bar on or after S0 (equal to S0 unless the stock is
  halted that day). `s0_fen` is the stock's unadjusted close on `base_date`.
  - Stock returns, benchmark returns, and the low/high window all start at `base_date`, so stock
    and benchmark always share one base date.
  - If the stock has no bar on or after S0 by the first due checkpoint, every due mark refuses
    `no_bar` and retries.

**Checkpoint session.**

- `dN` is the N-th calendar session strictly after S0.
- `h12` is the first calendar session on or after `made_on + 12 months`.
- A mark becomes due once its session date is completed.

**Resolve a mark:**

```
bar := last stock bar with base_date <= date <= session_date        (never a bar before base_date)
if no such bar                                      -> refuse no_bar, retry
if bar.date < session_date                          -> halted = 1   (suspension or delisting)
close_fen = bar.close (unadjusted)
low_fen/high_fen = min low / max high over stock bars in (base_date, session_date]   (empty -> NULL: no fill)
ret_bp = round_half_even((close_fen / s0_fen − 1) × 10000)
bench_ret_bp = same with the index closes on base_date and session_date
corp_action = detectCorpAction(stock, S0, bar.date)   (§6.4)
status = resolved; resolved_at = now; source = "tencent"
UPDATE ... WHERE id=? AND status <> 'resolved'      -- 0 rows affected = someone else resolved it; fine
```

**Retry schedule.** Refusals retry with backoff: +1 h, +6 h, +24 h, then every 24 h.
`stale_bar` means the vendor's last bar is older than the latest completed calendar session; that
is a vendor lag, so retry.

**Finality.** When a refused mark is still refused `predRetryDeadlineSessions` (10) calendar
sessions after its due session, it becomes `refused_final` and is never retried again. The same
applies to a refused baseline check (`base_check = refused_final`). This is what makes a mature
cell's inputs fixed (§7.4).

**Baseline check** (design §9.4), on the first pass for a set:

- `made_at` inside a session (09:30–15:00 on a calendar session): `match` if
  `emitted_base_fen ∈ [low, high]` of that session's stock bar, or equals the previous session's
  close.
- otherwise: `match` if it equals the close of the last completed session, or the one before it.
- else `mismatch`; vendor failure → `refused`, then retry, then `refused_final` (above).

**Guarded set update.** The set is insert-only except these five columns: `s0_date`, `base_date`,
`s0_fen`, `base_check`, and `bench_code` when it is empty. They are written once:

```sql
UPDATE prediction_sets SET s0_date=?, base_date=?, s0_fen=?, base_check=?
 WHERE id=? AND base_check IN ('pending','refused')
```

### 6.4 Corporate-action detection (D8)

**Step 0: try the unadjusted feed first.** On an ex-dividend date, a Tencent bfq day row carries a
**seventh element**, a dividend object (`quote.go:1121`). Before writing any qfq code:

- capture fixtures for a cash dividend, a 送转 and a 配股;
- check whether that element is present and describes each one.

If it covers all three, `detectCorpAction` is just "any day row in `(base_date, bar.date]` has a
seventh element". No qfq fetch and no ADR 0028 amendment are needed. Use the fallback below only
for the kinds that element misses.

**Fallback (qfq ratio).** `quote.go:986` says "NEVER qfq". That rule protects **displayed prices**.
Add one narrowly named function, `fetchTencentAdjustRatio(ctx, market, code, from, to string) (map[string]float64, error)`.
It fetches the qfq series over the same range and returns `qfq_close / bfq_close` per date. Its
comment cites this document and the ADR amendment, and it is the only qfq call in the codebase. Never
store or display its numbers.

`detectCorpAction` returns true when the ratio on S0 and the ratio on `bar.date` differ by more than
0.1 %. Either a dividend or a bonus issue changes the ratio. A limit-down day does not.

If D8 is decided the other way (score on the frozen adjusted ratio), this function becomes the
scoring path. Do not build both.

### 6.5 PR 5 tests

All are fixture-based; no network in tests.

- **Switch off.** With `forecast_auto="0"`: zero fetches (an injected fetcher counts calls) and
  zero writes.
- **Admin writer.** The settings route round-trips, and a non-admin is refused.
- **S0 table** (§6.3): every row, run with the panel timezone set to UTC.
- **Partial bar.** The 14:57 fixture (`AsOf` 14:57:03) resolves nothing, even with the fetch
  clock at 15:31. A body whose `AsOf` is ≥ 15:00, fetched at ≥ 15:30, resolves.
- **No cache.** The evaluator never reads from or writes to `s.quotes`' LRU. A spy on
  `get`/`put` counts zero calls.
- **Fill window.** A low printed on S0 or before it, including the receipt day before `made_at`,
  never counts as a fill.
- **Backdating moves nothing** (design §11 #13). A backdated report `date` or `baseline.as_of`
  changes neither `cohort`, `s0_date` nor any `session_date`.
- **Guarded set update.** Once `base_check` is `match`, `mismatch` or `refused_final`, the update
  affects 0 rows.
- **Finality.** A mark refused for 10 sessions past due becomes `refused_final` and is never fetched
  again.
- **Halt.** The index has bars for D1..D10 and the stock is missing D4..D10 → `d5` resolves with
  `halted=1` on the D3 close.
- **Delisting.** The stock stops forever → every later checkpoint resolves with `halted=1`; none
  refuses.
- **Corporate action.** A recorded fixture spanning a known 送转 or dividend → `corp_action=1`. A
  limit-down fixture → `corp_action=0`.
- **Resolved is terminal.** The guarded UPDATE on a resolved mark affects 0 rows.
- **Retry schedule.** Refused → `next_attempt_at` follows the backoff.
- **Baseline check.** One case per row of §6.3.

Record any new fixtures under `internal/app/testdata/quote/`. That directory has no README today;
add one in this PR, with a line per fixture saying when and how it was captured.

**The settings switch UI ships in this PR.** Create `ForecastPage.tsx` here as a shell: the route,
the nav item, and the header with the 评估设置 modal. PR 7 fills in the matrix. A setting must
not ship without its UI (design §9.1).

---

## 7. Group statistics API (PR 6)

### 7.1 Route

`GET /api/admin/forecast/groups` is registered with
`s.perm(mux, pattern, PermManage, s.apiForecastGroups)`, **not** on `/api/v1`. v1 authorizes by
token scope only, and the app bridge mints `query` tokens for every unrestricted user (design §7).

Query parameters:

| Param | Values | Notes |
|---|---|---|
| `mode` | `month` (default) / `custom` | `month` ignores `from`, `to` and `group`. A request that passes them gets `400 month_takes_no_range`; nothing is silently ignored. |
| `rtype` | default `投资决策建议` | must be in `predictionRtypes` |
| `from`, `to` | `YYYY-MM-DD` | required for `custom`; Shanghai `made_on` |
| `group` | `week` / `month` / `quarter` / `all` | `custom` only |

Refusals use `jsonErrorCode(w, 400, code, msg)` (`apiui.go:50`, body `{error, code}`), the
envelope every `/api/admin/*` route and the SPA client already use. Do not use `v1err`. Codes:

- `range_inverted` (`from > to`);
- `range_too_long` (more than 36 months);
- `week_range_too_long` (week grouping over 26 weeks);
- `bad_date`, `bad_group`, `bad_mode`.

### 7.2 Groups

- **`month` mode:** one group per calendar month from the earliest `made_on` in the ledger to the
  current month.
- **`custom` mode:** split `[from, to]` into groups.
  - ISO weeks run Monday–Sunday, and quarters are calendar quarters.
  - A period cut by `from` or `to` becomes its own group, labelled `YYYY-MM-DD – YYYY-MM-DD`.
  - A whole month is labelled `YYYY-MM`.
  - `all` is a single group.

Each group carries `label`, `start`, `end` and `official` (true only in `month` mode).

### 7.3 Membership and coverage, per group

```
rows := prediction_sets where made_on in [start,end] and rtype = ?
coverage := counts of rows by reason:
  absent | rejected | backtest                            (by state)
  degraded   (accepted, decision_quality <> 'normal')
  mismatch   (accepted, base_check = 'mismatch')
  duplicate  (accepted, normal, match, but not the earliest made_at for its symbol in this group)
  member     (the earliest accepted+normal+match set per symbol in this group)
reports  := count of all prediction_sets rows of rtype in the group, in any state
            (every covered ingest writes one row, so this is the ingest count)
```

`unrecorded` is **not** per group. `reports.sent_at` is caller-supplied (`apiv1.go:69`), rewritten
on every upsert (`store.go:1871`) and not uniformly UTC, so it cannot place a report in a group.
Instead, the response carries one top-level number:

> `unrecorded` = live reports of a covered rtype that have **no** `prediction_sets` row at all
> (`LEFT JOIN … WHERE ps.id IS NULL`).

This number catches the `store_failed` case.

**Membership follows D4.** Per symbol, take the earliest `accepted` set in the group.

- If it is `normal` and `match`, it is the member.
- If it is degraded or `mismatch`, the symbol is excluded and counted under that reason. A later
  set for the same symbol in the same group is counted as `duplicate`, never promoted.
- If its `base_check` is still `pending` or `refused`, the symbol counts as `pending_baseline` and
  blocks maturity, exactly like a pending mark. If it becomes `refused_final`, the symbol is
  excluded with reason `refused`.

### 7.4 Maturity, per (group, checkpoint)

A cell is mature iff **all** of these hold:

1. `end < today` (Shanghai);
2. every member's mark for this checkpoint is `resolved` or `refused_final`. None is `pending`,
   and none is `refused`, because a retryable refusal could still change the numbers. No symbol is
   `pending_baseline`. For `h12`, only `horizon_std` members count.
3. `refused_final ≤ 5 %` of members.
4. `n ≥ 30`, where `n` = members with a resolved mark and `corp_action = 0`.

Because every input of a mature cell is terminal (`resolved`, `refused_final`, or a final baseline
check), **a mature cell can never change again**. This is what makes the immutability of monthly
cells (design §8.5) true by construction, rather than by caching.

Otherwise the cell returns `{state: "pending", resolved, total}`, `{state: "open"}` (the group has
not ended) or `{state: "small", n}`, and **no metric values at all**. A test asserts that the keys
are absent, not zero.

### 7.5 Metrics (mature cells only)

Per member, from its resolved mark:

- `excess_bp = ret_bp − bench_ret_bp`.
- `upside = terminal_fen / s0_fen − 1`, from the claims.

| Key | Computation |
|---|---|
| `spread_bp` | mean excess(`long`) − mean excess(`no_position`). Needs ≥ 1 member in each arm, else `null` with reason `one_arm`. |
| `ic` | Spearman(upside, excess). Ties get average ranks. Pearson on the ranks. |
| `hit` | among members with \|upside\| ≥ 5 %: the share with sign(excess) = sign(upside). `hit_null` = max(share of positive excess, share of negative excess) on **that same subset**, with its `n` reported. |
| `fill` | share of members whose `low_fen ≤ entry_fen`; `fill_ret_bp` = mean of `close_fen / entry_fen − 1` over those. Flagged `derived: true`. |
| `band` (`h12` only) | share of members with `bear_fen ≤ close_fen ≤ bull_fen` |
| `calib` (`h12` only) | among members with `probability_method ≠ approved_policy_default` and no transform: mean `p_profit` vs the share with `ret_bp > 0` |

**p-values (label permutation).** For `spread`, `ic` and `hit`: shuffle the members' (stance,
upside) pairs across members while their excess stays fixed, `predPermutations` times. Moving the
pair together is valid for each metric, because each statistic depends only on the pairing of a
member's prediction with an excess.

- `spread` and `ic` are two-sided about 0: `p = (1 + #{|perm| ≥ |obs|}) / (1 + B)`.
- `hit` is two-sided about its null:
  `p = (1 + #{|perm − hit_null| ≥ |obs − hit_null|}) / (1 + B)`.
- A `one_arm` spread has no p and is not in any BH family.

**Seed.** SHA-256 of `(rtype, group start, group end, checkpoint, metric)`, **without** `mode`. A
whole-month custom group therefore gets exactly the monthly cell's p.

**BH, per cell.** The family is that cell's metrics (and its slices), not the whole page. A cell's
`q` then depends only on the cell, so a mature cell's `q` is fixed and identical in both modes.

- Cross-cell multiplicity is left to the reader. The page says so in its legend ("各格独立校正").
- Page-wide BH would change old cells' `q` every time a new month matured, and would make the
  monthly and custom answers differ.

**Policy slices.** When a cell's members span more than one `policy_version`, also return
`slices[]` per policy. Each slice has its own maturity (n ≥ 30) and joins its cell's BH family.

**Cost.**

- Compute the ranks of excess once per cell. For `ic`, permute the rank vector of upside, so each
  permutation is O(n) with no re-sort.
- At the maximum bounds (36 monthly groups × 5 checkpoints = 180 cells, plus slices; or 26 weekly
  groups × 5 = 130), at about 100 members, that is ≈ 180 × 3 × 10⁴ × 100 ≈ 5.4 × 10⁸ simple
  operations.
- Budget: **under 3 s on CI at the maximum bounds**, measured by the benchmark in §7.8. If it is
  slower, cut work on cells that are not mature, never `B`.
- **Monthly mode** caches mature cells by `(rtype, cohort, checkpoint)`. They are immutable by
  §7.4, so no version key is needed.
- **Custom mode** reuses a cached monthly cell when a group equals a whole month. Otherwise it is
  computed on request and never stored.

### 7.6 Response

```json
{
  "ok": true,
  "mode": "custom", "official": false,
  "range": { "from": "2026-12-10", "to": "2027-04-25", "group": "month" },
  "checkpoints": ["d5", "d20", "d60", "d120", "h12"],
  "unrecorded": 0,
  "groups": [{
    "label": "2026-12-10 – 2026-12-31", "start": "2026-12-10", "end": "2026-12-31",
    "coverage": { "reports": 74, "member": 52, "duplicate": 11, "absent": 2, "rejected": 1, "backtest": 0,
                  "degraded": 3, "mismatch": 1, "refused": 0, "pending_baseline": 0 },
    "cells": {
      "d5":  { "state": "mature", "n": 51, "refused": 1, "corp_action": 0,
               "spread_bp": 310, "spread_p": 0.012, "spread_q": 0.041,
               "ic": 0.21, "ic_p": 0.020, "ic_q": 0.047,
               "hit": 0.61, "hit_n": 38, "hit_null": 0.55, "hit_p": 0.18, "hit_q": 0.30,
               "fill": 0.33, "fill_ret_bp": -120, "fill_derived": true,
               "slices": [] },
      "d20": { "state": "pending", "resolved": 31, "total": 52 },
      "h12": { "state": "open" }
    }
  }]
}
```

### 7.7 CSV export

Done client-side from this response (`web/src/lib/csv.ts`). It writes one row per group ×
checkpoint × metric. The first lines are comments carrying `mode`, `official`, `range`, `group` and
`generated_at`, so an exploratory export can never pass for an official one.

### 7.8 PR 6 tests

These are design §11 #13 and #16–18 and #23–26. Statistics tests use hand-computed fixtures:

- `spearman` with ties against a worked example;
- the permutation p-value is reproducible with a fixed seed and in `(0, 1]`;
- BH against a textbook vector.

Maturity:

- each failing condition yields `pending` / `open` / `small`;
- metric keys are absent (not zero) in every non-mature state;
- n = 29 → `small`, n = 30 → mature;
- refusals at 5 % → mature, at 6 % → `pending`.

Membership:

- two sets for one symbol in a group → one `member`, one `duplicate`;
- a `pending` baseline blocks maturity.

Modes:

- a whole-month custom range grouped by month equals the monthly numbers exactly, with
  `official=false`;
- a partial week is labelled with its real dates;
- monthly mode with `from` → `400 month_takes_no_range`;
- every bound refusal returns its code;
- two monthly requests at different `now` return identical mature cells.

Access:

- non-`PermManage` → 403;
- an app-bridge `query` token → refused.

Mature cells never change:

- a mature cell's inputs are all terminal;
- flipping a `refused_final` mark is impossible through the evaluator (guard test);
- the monthly p and q equal the custom whole-month p and q.

Performance: `BenchmarkForecastGroupsMaxBounds`, at 180 cells × 100 members with slices, under 3 s
on CI.

---

## 8. UI (PR 7)

### 8.1 Verification page 「预测验证」

- **Route:** `/manage/forecast` under the existing `<AdminOnly><ManageLayout/>` tree in
  `App.tsx`.
- **Nav:** a nav item in `web/src/pages/manage/ManageLayout.tsx`, in group `nav.group.content`,
  key `forecast`, label `t('nav.forecast')`. **Hover prefetch:** warm only
  `/api/admin/forecast/settings`. `WARM_ON_HOVER` (`ManageLayout.tsx:38-58`) is for cheap GETs, and
  a hover must never start the group statistics.
- **Component:** `web/src/pages/manage/ForecastPage.tsx`.
- **Header:** title, one-line subtitle, and buttons 导出 CSV and 评估设置. The latter opens the PR 5
  switch in a modal; that modal is already in PR 5.
- **Tabs** (antd `Tabs`): 按月 (`month`) and 自定义区间 (`custom`). The active tab and the custom
  parameters live in the URL query (`?mode=custom&from=&to=&group=`), so a view can be linked.
- **Custom toolbar:** `DatePicker.RangePicker` (see `AnnouncementPage.tsx:586` for the pattern,
  without `showTime`) and a `Segmented` for 周 / 月 / 季 / 整段. Client-side bound checks only for
  feedback; the server is the authority, and its refusal codes map to i18n messages.
- **Exploratory banner:** in the custom tab, a persistent warning `Alert`, which cannot be closed.
  Every p in that tab renders as `p*`, with a legend line.
- **Metric switcher:** `Segmented` for 多空价差 / 排序 IC / 方向命中 / 买入价成交, each with a
  one-line explanation underneath.
- **Matrix:** rows are groups (newest first), columns are checkpoints plus 覆盖.
  - Mature cells show value, `q` (or `p*`), `n`, and the excluded count. They are coloured by sign
    only when `q < 0.05`: red is positive and green is negative, the A-share convention, which the
    legend states.
  - Other states render as in the demo.
- **Coverage cell:** a stacked bar plus "计入 n / 报告 N". `rejected > 0` is highlighted, with a
  link to the audit log filtered on `predictions.reject`.
- **Drawer** (antd `Drawer`) on a mature cell:
  - the metric table (value, null, p, q, n);
  - the member list (stock, receipt date, stance, upside, excess, agreement);
  - each stock links to `/stock/{symbol}?date={report_date}&kind={kind}`. `kind` is the report's
    **top-level** kind (e.g. 投资决策), not the rtype: `apiStock` matches `kind` against
    `repKind` (`apiui.go:596`) and silently falls back to the first kind on a miss. Put the kind on
    the member row (join the live report). Purged sets get no link.
  - Member rows need a list endpoint: add `GET /api/admin/forecast/groups/members?…&label=&checkpoint=`
    in PR 6, with the same parameters plus the cell.

### 8.2 Report section on the reading page

A report is read on `StockPage` (`web/src/pages/StockPage.tsx`, route `/stock/:symbol`). There is
no separate report page and no tabs. The UI adds two things there.

**1. A 「预测验证」 card** inside `rp-reader__doc`, below the report body. It renders only when the
selected report has at least one set. It is collapsible, with the open state remembered in
`localStorage` behind try/catch. It contains:

- **Badges:** counted-in group, baseline check, decision quality, and a probability-method note.
- **报告当时说:** stance, horizon, terminal value vs S0, `p_profit`, entry price (labelled
  "由终值按策略推算"), and the three-scenario band with the latest close marker.
- **检查点:** a stepper plus a table. Resolved rows show close, return, benchmark, excess, the entry
  touch, and agreement. Pending rows show their expected session date and the note "收盘 15:30 后结算，结算后不再改变".
- **Latest close line:** the current close, explicitly marked 不计分.
- **这只股票的其他预测:** the sets of other reports for the same symbol, linking to their dates.
- **入账原文:** a collapsible payload view (`?include=payload`).
- **`body_changed` warning:** when true, 「本报告正文在入账后有改动，以上为入账时的预测」.

**2. Chart overlay on the existing quote card** at the top of `StockPage`. `PriceChart` is
hand-drawn SVG (`PriceChart.tsx:20`). Extend it (`:60`) with **two** optional props:

```ts
levels?: { price: number /* fen */; label: string; tone: 'neutral' | 'primary' | 'warning' }[]
markers?: { date: string; label: string }[]
```

- Render them only when provided, so the other `PriceChart` users (`QuotesApp`) are unchanged.
- **Levels do not stretch the y-domain.** The domain stays bar-driven, so the chart reads the same
  with or without the overlay. A level outside the domain is drawn as an edge tag at the top or
  bottom of the plot, e.g. 「↑ 终值 294.00（图外）」.
- **Markers outside the bars' date range are dropped.** Checkpoints in the future are not drawn.
- `StockPage` passes S0, the terminal value and the entry price as `levels`, and the report date and
  resolved checkpoints as `markers`, when the selected report has an accepted set.
- A small checkbox on the quote card, 「叠加预测价位」, toggles them (default on).

**Data.** `GET /api/report/{id}/predictions`, the session route from §4.1. The SPA never calls
`/api/v1`.

### 8.3 i18n

The source of truth is the `.json` locale files `web/src/locales/{zh-CN,zh-TW,en-US}.json`.
`bundles/*.ts` only import them and need no edit. zh-TW is generated
from zh-CN with OpenCC s2twp, then flagged for review.

Key families: `nav.forecast`, `forecast.page.*`, `forecast.tab.month|custom`,
`forecast.group.week|month|quarter|all`, `forecast.metric.*` (+ `.help`), `forecast.cell.*`,
`forecast.cov.*`, `forecast.err.<code>` (one per server refusal code),
`forecast.panel.*`, `forecast.mark.*`, `audit.a.predictions.*`,
`webhook.event.predictions.rejected`, `storage.cat.predictions`.

Use **预测验证** as the zh-CN name everywhere and *Forecast verification* in en-US. Never use
「回测」, 「复盘」 or 「战绩」 (design §1.1).

### 8.4 PR 7 tests (vitest)

- **Matrix rendering.** Each cell state renders correctly, and a non-mature cell shows no number
  even if the payload wrongly carries one: the component reads metrics only when `state ===
  'mature'`.
- **Custom tab.** The exploratory banner is present and cannot be dismissed; p renders as `p*`;
  each server error code maps to its message.
- **URL state.** A round-trip through the query string works.
- **CSV.** The header lines carry mode, official and range.
- **`PriceChart`.** No `levels` → identical output to today (snapshot); with `levels` → lines
  rendered.
- **StockPage section.** Hidden when there is no set; shows `body_changed` when the flag is true;
  the latest-close line is labelled 不计分.
- **Locale parity** passes.

---

## 9. End-to-end and release (PR 8)

- **`scripts/e2e_check.py`:** add a section `G. forecast verification`, in the existing style
  (`print("\nG. ...")` and `check(...)`).
  1. Ingest with a valid `predictions` → `accepted`.
  2. An invalid one → `rejected`; the audit row exists; the report exists.
  3. A covered rtype without predictions → `absent`.
  4. `/api/v1/reports/{id}/predictions` returns the set.
  5. `/api/admin/forecast/groups` → 403 for a non-manager, 200 for an admin.
  6. The settings round-trip, with `forecast_auto` left `"0"`.
- **ADR:** ADR 0029 rewritten in PR 0. ADR 0028 gets a short amendment for the single qfq detection
  function (§6.4).
- **Release notes:** reader-facing text for the release workflow. Mention that the evaluator ships
  off, and how to turn it on.

---

## 10. PR order and dependencies

| PR | Content | Depends on | Design §11 tests |
|---|---|---|---|
| 0 | ADR 0029 rewrite and ADR 0028 amendment; owner sign-off on §2 DDL; D1–D5, D7–D9 recorded | — | — |
| 1 | Migration 0002, cascade, storage category | 0 | 11 (purge, storage part), 22 |
| 2 | Ingest classifier and write path, audit, webhook, OpenAPI | 1 | 1–8 (ingest halves), 21 (store_failed response) |
| 3 | Read API: v1 machine routes plus the browser session route | 2 | 11 (access part), 19 (per-report) |
| 4 | Dify projection node (Dify repo/app) | 2 deployed | 20 |
| 5 | Evaluator, settings, corporate-action detection, `ForecastPage` shell with the switch | 1 | 10, 12, 13, 14–16 |
| 6 | Group statistics API, member list | 5 | 7 and 8 (coverage halves), 9, 17, 18, 19 (groups), 21 (`unrecorded`), 23–26 |
| 7 | UI: matrix on `ForecastPage`, StockPage section, `PriceChart` overlay | 3, 6 | UI parts of 1, 23 |
| 8 | e2e section, release notes | 7 | — |

- PRs 2–3 and 5 can proceed in parallel after PR 1.
- PR 4 needs PR 2 **deployed**, because Dify talks to the running portal.
- Deploy PRs 1–3 before PR 4, so the first Dify run is accepted rather than rejected.

**Rollout:**

1. Deploy 1–3.
2. Run PR 4 on the Dify test app, then on prod.
3. Watch coverage for a few days: `rejected` should be 0.
4. Deploy 5 with `forecast_auto="0"`; turn it on; watch the `d5` marks resolve.
5. Deploy 6–7.
6. The first mature monthly cell appears about a week after the first full month closes.

**Rollback:**

- Migration steps are forward-only. To stop the feature, set `forecast_auto="0"`, and remove the
  projection node or unwire its parameter in Dify.
- The ledger keeps what it has.
- The ingest classifier never refuses a report, so the portal needs no rollback for ingest to keep
  working.

---

## 11. Handoff checklist

- [ ] Read design §1–§13 and ADRs 0029, 0035, 0017, 0028, 0024.
- [ ] Owner decisions D1–D5 and D7–D9 recorded (PR 0).
- [ ] Dify export re-pulled; §5.2 confirmed against the live 1-6-4.
- [ ] Owner sign-off on the §2 DDL.
- [ ] Postgres available locally for the dual-driver tests (`TEST_POSTGRES_DSN`).
- [ ] Each PR opens with its tests failing, and closes with them passing on both drivers.
- [ ] No route outside `routes.go`; `openapi.json` updated with every v1 change.
- [ ] Locale parity green; zh-TW flagged for human review.

**Questions this document cannot answer, which go to the owner:**

- anything in §0.2 that is still open;
- whether `forecast_auto` should be per-install (`meta`) only, or also need an audit entry when
  flipped. The recommendation is that it should, matching other admin settings that write
  `audit.t.*`.
- the label text of any UI string not given here, beyond the key families in §8.3.

---

## 12. Review of the first version: findings and changes

| # | Finding | Change |
|---|---|---|
| 1 | `/api/v1` is token-only; the SPA cannot use it | Added the browser session route `GET /api/report/{id}/predictions` (§4.1, §8.2) |
| 2 | The partial-bar guard used `now`; a cached 14:57 body could resolve at 15:31 | Gate on fetch time **and** vendor `AsOf`; test that a 14:57 body never resolves (§6.3, §6.5) |
| 3 | `fetchUnder` walks the source chain and the cache | Call `fetchTencentRange` directly under `s.quotes.acquire`; no cache; record the source (§6.2) |
| 4 | A JSON number would fail at decode as `bad_schema` | Price and probability fields typed `json.RawMessage` (§3.2) |
| 5 | Page-wide BH and a mode-dependent seed made cells mutable and unequal across modes | BH per cell; seed without `mode` (§7.5) |
| 6 | Retrying refusals could change a mature cell | `refused_final` after 10 sessions; maturity requires terminal inputs (§6.3, §7.4) |
| 7 | The hit-rate p was one-sided | Two-sided about `hit_null`; `one_arm` has no p (§7.5) |
| 8 | Scenario rounding vector was wrong | Tie-break on raw value, then last index (§5.3) |
| 9 | Hover prefetch would start the statistics | Warm only the settings GET (§8.1) |
| 10 | Wrong error envelope for an admin route | `jsonErrorCode` (§7.1) |
| 11 | Wrong audit i18n family; vocabulary missing | `audit.a.*`, `auditVocabulary`, `recordChange` (§3.3) |
| 12 | `unrecorded` was built on caller-supplied `sent_at` | One global join-based count; `reports` defined from the ledger (§7.3) |
| 13 | The drawer link used `rtype` as `kind` | Link with the top-level kind; no link for purged sets (§8.1) |
| 14 | The fetch window was too short for the baseline check | Start at `made_on − 10` days (§6.2) |
| 15 | S0 was ambiguous for halted stocks | Calendar `s0_date` and stock `base_date`, each formula naming one (§2.2, §6.3) |
| 16 | The guarded set update was missing | Added, with a test (§6.3, §6.5) |
| 17 | Membership contradicted D4 | The earliest accepted set decides, with no replacement by rerun (§0.2, §7.3) |
| 18 | Migration index list was wrong | All six names (§2.1) |
| 19 | The PR-to-test mapping was wrong; tests 13 and fill-before-S0 were missing | Remapped (§10); tests added (§6.5) |
| 20 | The PR 5 switch had no page | `ForecastPage` shell moves into PR 5 (§6.5, §10) |
| 21 | The cost bound was understated | Max-bounds benchmark under 3 s; O(n) rank permutation (§7.5, §7.8) |
| 22 | The overlay was under-specified | No y-domain stretch; edge tags; out-of-range markers dropped (§8.2) |
| 23 | A dividend element already exists in the bfq feed | Evaluate it first; qfq is the fallback (§6.4) |
| 24 | Date-based backtest classification would catch ordinary runs | Instant comparison with 72 h slack (§3.2) |
| 25 | Small wrong references | Fixed: `apiv1.go:246`, `ManageLayout` path, README, locale source of truth, `schema` rationale, index column order, Dify node wiring |
