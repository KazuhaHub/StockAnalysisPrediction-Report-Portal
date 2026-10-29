# ADR 0045 — Data export tasks

**Status: Proposed.** 2026-10-29. Builds on ADR 0017 (storage cleanup) and ADR 0034 (database baseline).

## Context

v2026.44.5 shipped a synchronous Markdown export for reports on a dedicated Export page. Every log
the portal keeps now needs the same treatment, and the synchronous design does not scale to it:

- It holds every selected report in memory and issues one `GetNew` per report (N+1).
- A browser `fetch` → `Blob` download keeps the whole file in tab memory; leaving the page loses it.
- There is no progress, no history, and an export is audited as N `report.read` rows.
- The audit log alone can hold millions of rows (`report.read` is about 70% of it).

Administrators expect the pattern of large admin consoles such as Google Workspace: choose data,
narrow it with filter chips, see a live count and preview, submit an export, keep working, and
collect the file from a task tray when it is ready.

### Does the log storage itself need restructuring?

No. Reviewed table by table:

| Source | Storage | Verdict |
|---|---|---|
| Audit log | `audit_log`, indexed on `at`, `(actor, at)`, `(action, at)`, `(target_type, target_id, at)` | Sound. Keyset export over `(at, id)` uses `idx_audit_at`. |
| Sign-ins | `auth.*` rows of `audit_log`; `login_history` is a per-user capped copy | A filtered view of the audit log, not a second source of truth. |
| Runs | `batch_jobs`, `batch_items`, `recurring_runs` | Sound; `created_at` indexed. |
| Notification deliveries | `notification_deliveries`, `notification_attempts`, BIGINT ms stamps | Sound. |
| Cleanup runs | `cleanup_runs`, ring buffer | Sound; tiny. |

The one defect is a query, not a shape. `audit_log.at` holds two formats: RFC 3339 UTC since
2026-08-03 and a space-separated local stamp for the two days before it. `ListAudit` orders by
`(at LIKE '%T%') DESC, at DESC`, which no index can satisfy, so every console page sorts the whole
filtered set. Because the legacy rows cover only 2026-08-01..03, and plain `at DESC` already orders
them correctly against every later day, the prefix only affects ordering within those three days.
It is removed, and paging becomes keyset on `(at, id)`. Rewriting the legacy stamps would be a
runtime data conversion, which ADR 0034 rules out, for no visible gain.

## Decision

### One export registry, one engine

Each exportable source is a Go `exportSource`: its name, its columns, its filter fields (typed:
enum, text, date range, user, IP), the permission it requires, and a streaming row producer. The
page, the count/preview API and the worker all read the registry, so adding a source is one entry.

Sources at launch: reports (Markdown ZIP, CSV of metadata), audit log, sign-ins, runs, notification
deliveries, cleanup runs. Log sources export as CSV (UTF-8 with BOM, for Excel) or JSON Lines.

### Streaming, constant memory

- The producer reads in keyset batches of 1,000 rows (`WHERE (at, id) < (?, ?) ORDER BY at DESC, id DESC`),
  so no read transaction is held for the whole export and SQLite writers are never starved.
- Rows go straight into a `csv.Writer`, a JSON encoder or a `zip.Writer`; memory does not grow
  with the export.
- Reports read body and metadata in the same batched query; the per-report `GetNew` disappears.
- The generator-version filter becomes a SQL `LIKE` prefilter on `source`/`title`, then the exact
  `generatorVersion` check in Go.
- Geo location for IPs is resolved per row at export time, as the console does.

### Background tasks

New table, added by migration step `0012` (ADR 0034's ledger), never by `baseSchemaStmts`: the
baseline stays frozen, and the step is forward-only, idempotent and declares its table and indices
so the runner re-verifies them on every start. Nothing is back-filled, so the step is
`olderDumpOK`.

```sql
CREATE TABLE IF NOT EXISTS export_jobs(
  id TEXT PRIMARY KEY,               -- 128-bit random hex
  source TEXT NOT NULL,              -- registry key
  format TEXT NOT NULL,              -- csv | jsonl | md_zip
  params TEXT NOT NULL,              -- JSON: filters, columns, time zone, report options
  requested_by TEXT NOT NULL,
  status TEXT NOT NULL,              -- queued | running | done | failed | canceled | expired
  rows_estimate BIGINT NOT NULL DEFAULT 0,
  rows_done BIGINT NOT NULL DEFAULT 0,
  bytes BIGINT NOT NULL DEFAULT 0,
  file_name TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,        -- unix ms, like the notification tables
  started_at BIGINT NOT NULL DEFAULT 0,
  finished_at BIGINT NOT NULL DEFAULT 0,
  expires_at BIGINT NOT NULL DEFAULT 0)
CREATE INDEX IF NOT EXISTS idx_export_jobs_created ON export_jobs(created_at DESC)
CREATE INDEX IF NOT EXISTS idx_export_jobs_status ON export_jobs(status, expires_at)
```

- The file lives at `<dataDir>/exports/<id>.<ext>`; the path is derived, never stored. It is written
  to `<id>.part` and renamed on success, so a crash never leaves a file that looks complete.
- One worker runs jobs in order (two-way concurrency is a constant). Progress is `rows_done`
  against `rows_estimate`, flushed every batch.
- Restart: `running` jobs return to `queued` and their `.part` file is removed.
- Cancel: a queued job is marked canceled; a running one stops at the next batch boundary.
- Download: `GET /api/admin/exports/{id}/file` with `http.ServeContent` (Content-Length, Range,
  resumable), outside the gzip middleware, with an RFC 5987 filename.
- Expiry: 7 days. An hourly sweep deletes expired files and marks rows `expired`; rows older than
  90 days are deleted. Export files appear as a category on the storage usage page.

### Audit

One `export.create` entry when a task is submitted (source, format, filters, estimate) and one
`export.download` per download. No per-report `report.read` rows.

### Filter model

Studied on the live Google Workspace Admin console (Audit and investigation; Users), whose
interaction model this follows while the visual design stays the portal's own.

A filter is an expression tree, not a flat set of query parameters:

```json
{"op": "and", "items": [
  {"field": "rtype", "operator": "in", "values": ["汇总", "估值分析"]},
  {"field": "kind", "operator": "in", "values": ["投资决策"]},
  {"op": "or", "items": [ ... ]}
]}
```

- Each source declares its fields and, per field, the operators it allows (`is`, `is_not`, `in`,
  `not_in`, `contains`, `before`, `after`, `between`). The server compiles the tree to parameterized
  SQL from that whitelist only; an unknown field or operator is a 400.
- Bounded like Workspace's activity rules: at most 20 conditions and 3 levels of nesting.
- The same tree drives the count, the preview and the export, so what is previewed is what is
  exported. It is stored in `export_jobs.params` and travels in the URL (compact base64url JSON).

### Interface

- **Data source rail.** Sources on the left with row counts, like the Users page's tree rail.
- **Report type tree.** Report sources get a tree in the rail: kind (大类) → subtype (小类), with a
  search box and a Single / Multi select toggle, as Workspace's organizational unit tree. Checking a
  kind selects the kind itself (`kind in`), so subtypes added to it later are included, the same as
  "include child organizational units"; checking individual subtypes selects only those.
- **Two filter modes on the same tree**, as Workspace's Filter and Condition builder tabs:
  - *Filter* (default): chips in one bar. "Add filter" opens a searchable field list; a field opens
    a popover with an operator (Is / Is not / …) and a searchable multi-value picker, then Apply.
    Chips combine with AND; each chip can be edited or removed; "Clear filters" resets.
  - *Advanced*: AND / OR groups of rows (field, operator, value), with "Add condition" and "Nest"
    up to the depth limit. A tree that chips can express switches back to Filter without loss; one
    they cannot keeps Advanced and says so.
- **Results.** Unlike Workspace, which runs on an explicit Search click, the count and preview
  refresh as the filter changes (debounced). "Showing 1–50 of N", rows per page, sortable time
  column, and a "Manage columns" dialog (drag to reorder, remove, add) whose choice is also the
  export's column set.
- **Export dialog.** Export name, format, time zone (panel or UTC), and for reports YAML front
  matter per file, an `index.csv` manifest and folder layout (flat, by date, by symbol). States the
  row count being exported.
- **Task tray.** An icon in the top bar for administrators, like Workspace's hourglass: in-progress
  tasks with progress, finished ones with a download link and expiry, failed and canceled ones with
  the reason; an empty state that says what appears there. A toast announces completion on any page.
- **Export history** on the Export page: recent tasks of every administrator, with requester,
  source, filter summary, rows, size, status and expiry.
- Other pages (audit log, run history) link to Export with their current filter pre-filled.

## API

All endpoints are administrator-only (cookie session, `s.admin`). Times on the wire are unix
milliseconds unless a field says otherwise. Errors use the existing `jsonError` shape.

### Filter tree

```ts
type Filter = Group
type Group = { op: 'and' | 'or'; items: (Group | Cond)[] }
type Cond  = { field: string; operator: Operator; values: string[] }
type Operator = 'is' | 'is_not' | 'in' | 'not_in' | 'contains' | 'before' | 'after' | 'between'
```

`before`/`after` take one value, `between` two (inclusive), `is`/`is_not`/`contains` one, `in`/`not_in`
one or more. Date fields take `YYYY-MM-DD` (civil date in the panel time zone); datetime fields take
RFC 3339 or `YYYY-MM-DD`. An empty group matches everything. Limits: 20 conditions, depth 3 (the
root group is depth 1). In URLs the tree is base64url-encoded JSON in the `f` query parameter.

### `GET /api/admin/exports/sources`

```ts
{ sources: Source[] }
type Source = {
  key: 'reports' | 'audit' | 'signins' | 'runs' | 'notifications' | 'cleanup'
  formats: ('csv' | 'jsonl' | 'md_zip')[]       // first is the default
  fields: Field[]
  columns: { key: string; default: boolean }[]  // export/preview column set, in default order
  time_field: string                            // the field the time presets act on
  total: number                                 // unfiltered row count
}
type Field = {
  key: string
  type: 'enum' | 'text' | 'date' | 'datetime' | 'number'
  operators: Operator[]
  options?: string[]                            // enum values (identifiers, not labels)
  tree?: { kind: string; subtypes: string[] }[] // reports only, on the 'kind'/'rtype' pair
}
```

Field and column keys are Go-defined identifiers; the client labels them through i18n
(`export.field.<key>`, `export.column.<key>`). Enum option values are data and are shown as-is,
except audit actions, which the client already labels.

### `POST /api/admin/exports/preview`

Request `{ source, filter, columns?: string[], page?: number, page_size?: number (≤200) }`.
Response `{ total: number, rows: Record<string, string | number | null>[] }`, rows newest first.

### `POST /api/admin/exports`

Request:

```ts
{ source; format; filter; columns?: string[]; name?: string; tz?: 'panel' | 'utc';
  options?: { front_matter?: boolean; index_csv?: boolean; layout?: 'flat' | 'date' | 'symbol' } }
```

Response: the created `Job` (status `queued`), HTTP 201.

```ts
type Job = {
  id: string; source: string; format: string; name: string; requested_by: string
  status: 'queued' | 'running' | 'done' | 'failed' | 'canceled' | 'expired'
  rows_estimate: number; rows_done: number; bytes: number; file_name: string; error: string
  created_at: number; started_at: number; finished_at: number; expires_at: number
  filter: Filter; columns: string[]
}
```

### `GET /api/admin/exports?limit=50&active=1`

`{ jobs: Job[] }` newest first, every administrator's. `active=1` returns only `queued` and
`running` (the task tray polls it).

### `GET /api/admin/exports/{id}`, `POST /api/admin/exports/{id}/cancel`

The job; cancel returns the updated job (409 if already finished).

### `GET /api/admin/exports/{id}/file`

The file via `http.ServeContent`; 404 unknown, 409 not finished, 410 expired.

## Addendum — forecast source and the entry-price screen (2026-10-29)

A reader asked for every investment-decision V3.17 report of the last two months with its entry
price, stop loss and probability-weighted terminal value, to find stocks now trading below the
entry price. Markdown export plus offline extraction answers the history; the structured part is
already in the forecast ledger (ADR 0029) for every set accepted since the ledger started:

| Wanted | Ledger | Note |
|---|---|---|
| Entry price | `prediction_claims` kind `entry_price` | Optional per set: present when the producer sends it. |
| Probability-weighted terminal value | kind `terminal_value` | Required on every accepted set (the reader card's "expected terminal value"). |
| Stop loss | — | Not stored. `stop_loss` is a reserved kind that ingest refuses until a code node computes it. Out of scope here; it is a producer and ADR 0029 decision. |

A seventh source, `forecasts`, joins after the six above land:

- One row per accepted prediction set. Columns: `set_id`, `report_id`, `symbol`, `name`,
  `report_date`, `made_at`, `rtype`, `version`, `stance`, `entry_price`, `terminal_value`,
  `latest_price`, `latest_at`, `vs_entry_pct`. Prices render as yuan from fen.
- Fields: `report_date` (date), `symbol`, `rtype`, `version`, `stance` (enum),
  `latest_per_symbol` (enum `yes`/`no`; the screen's default is `yes`, the newest set per symbol),
  `below_entry` (enum `yes`/`no`).
- `latest_price` comes from the existing batch quote path (`fetchBatchUnder`, one upstream call per
  chunk, shared cache), never one call per row, and honours the operator's quote-disclosure switch:
  with quotes off, `below_entry` and the latest-price columns are absent from the source rather
  than empty. A symbol the vendor cannot price is excluded from `below_entry = yes` and shown blank,
  never treated as below.
- `below_entry` needs a price for every candidate before it can count, so the server fetches
  quotes for the filtered candidate set (bounded by the same 20-condition filter and a hard cap on
  candidate symbols, refused with a clear error above it). Preview and export share the computation.
- Acceptance: a set without `entry_price` never matches `below_entry = yes`; quotes off removes the
  field; one upstream batch per chunk regardless of row count; `latest_per_symbol` picks the newest
  `made_at` per symbol.

## Acceptance conditions (each becomes a test)

1. Exporting 50,000 audit rows keeps heap growth under a fixed bound (fails closed: the test fails
   if memory scales with rows).
2. The streamed audit export equals `ListAudit` for the same filter, in the same order.
3. A report export issues a bounded number of queries independent of report count.
4. A job interrupted mid-run is requeued on restart and its `.part` file is gone.
5. A canceled running job stops within one batch and leaves no file.
6. An expired job's file is deleted and its download answers 410.
7. A non-admin cannot list, create or download exports (403).
8. One export produces exactly one `export.create` audit row, regardless of row count.
9. Download supports `Range` and reports the exact `Content-Length`.
10. Both SQLite and Postgres pass the export store tests.
11. Filter trees beyond 20 conditions or 3 levels, or naming a field or operator the source does
    not declare, are refused with 400 before any SQL is built.
12. Checking a kind includes a subtype added to that kind after the filter was saved.
13. Step `0012` is idempotent, verifies its declared table and indices, and a database missing them
    after the ledger says it ran is refused at start.

## Consequences

The synchronous `POST /api/admin/report-exports/download` is removed in favour of tasks; the list
endpoint becomes the generic count/preview endpoint. `export_jobs` arrives through migration
step `0012`, not through the frozen baseline, and changes no existing table, so the v0.4.72
acceptance contract is unchanged.
