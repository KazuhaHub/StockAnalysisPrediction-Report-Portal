# ADR 0046 — Reader report export governed by OU policy

**Status: Proposed.** 2026-10-29. Builds on ADR 0045 (data export tasks), ADR 0022 (OU tree) and the
feature-owned OU policy model (#197).

## Context

ADR 0045 gave administrators background export tasks. Readers (non-administrators) still have no way
to take more than one report at a time, yet the most common request behind 0045 — "give me every
V3.17 report as Markdown so a local model can read it" — comes from readers. Bulk export is also the
easiest way to move a whole research library out of the portal, so it must be something an
administrator grants deliberately, per organizational unit, with limits.

## Decision

### Scope of the reader feature

- Source: `reports` only. Format: Markdown ZIP only (front matter, `index.csv` and the
  flat/date/symbol layouts stay available as options). No CSV/JSONL, no column choice.
- What a reader can export is exactly what they can already read: the reader's report scope
  (`viewerScopeSnapshot`: restriction, audience principals, granted versions) is applied to the
  count, the preview and the worker's query. The scope is re-resolved when the worker starts, not
  frozen at submit, so a revoked grant is honoured by a job still in the queue.
- Readers see, cancel and download only their own tasks, and only tasks submitted through the reader
  endpoints: a non-reader row under the same name is never theirs. Another user's task id answers 404, never
  403, so ids do not leak existence. Administrators keep seeing every task in the admin console.

### Policy — three OU columns (owner-approved 2026-10-29)

Migration step `0013` adds three nullable columns to `user_groups`, with no default:

| column | type | meaning |
|---|---|---|
| `export_allowed` | INTEGER | 1 allowed, 0 not allowed, NULL inherit |
| `export_max_reports` | INTEGER | most reports one export may contain, NULL inherit |
| `export_daily_quota` | INTEGER | most reports per user per panel-timezone day, NULL inherit |

and one index, `idx_export_jobs_requester ON export_jobs(requested_by, created_at)`, which the
daily usage query needs. `restoreRule{olderDumpOK: true}`: applying the step changes nobody's
access because every column is NULL and every OU inherits.

The Default OU is the root of inheritance and may not inherit. At boot and on restore — the same
places `ensureDefaultForecastScope` runs — `ensureDefaultExportPolicy` sets the Default OU's NULL
columns to `0 / 200 / 1000`. Writing that data in the step itself would block an older dump.

Resolution uses the shared nearest-explicit-value resolver (`nearestOUPolicy`) for all three: a
child inherits unless it overrides, and the console shows own value, effective value and source OU.
`export_allowed` is deliberately *not* sticky: an administrator may switch export on for one
sub-OU without switching it on for its parent.

Bounds: `export_max_reports` 1..5000, `export_daily_quota` 1..100000. A quota below the per-export
maximum is allowed (it simply becomes the binding limit) but the console shows a hint.

Administrators (`PermManage`) are not governed by this policy; they use the admin console.

### Daily usage

Usage for a user on a panel-timezone day is the sum, over their export jobs created that day, of:
`rows_estimate` for `queued` and `running`, `rows_done` for `done`, `cancelled` and `failed`.
A job is charged at submit for what it was estimated to hold and refunded down to what it actually
wrote once it ends.

### Enforcement

On `POST /api/exports`, in this order:

1. Policy `allowed` false → 403 `{"error":"export_disabled"}`.
2. Filter invalid → 400, as ADR 0045.
3. Count the matching reports with the reader's scope. Zero → 400 `{"error":"export_empty"}`.
4. Count > `max_reports` → 422 `{"error":"export_too_large","limit":N,"count":C}`.
5. `used_today + count > daily_quota` → 429 `{"error":"export_quota","limit":Q,"used":U,"count":C}`.
6. Insert the job with `rows_estimate = count` and `params.cap = count`, `params.reader = true`.

Steps 5 and 6 are serialised per requester so two concurrent submits cannot both pass the quota:
on Postgres the transaction takes `SELECT … FROM users WHERE username=? FOR UPDATE`; on SQLite the
store's pool holds a single connection (`SetMaxOpenConns(1)`), so the whole read-then-insert
transaction runs alone. That pool setting is what the guarantee rests on, not the transaction mode;
a change to it must revisit this path.

The job also records `params.max_id`, the highest report id at submit. The scan runs newest first,
so without that ceiling a report ingested while the job waits would displace the oldest report the
reader was counted and charged for.

Deleting an account deletes its export jobs and their files with the rest of its per-username
state, so a later account reusing the name inherits no tasks, files or daily usage.

The worker, for a job with `params.reader`:

- re-resolves the policy; if `allowed` is now false the job fails with `export_disabled`;
- applies the reader's scope re-resolved at start;
- stops after `params.cap` reports, so reports ingested between submit and start can never push a
  job over the limit it was charged for.

### Audit

The same two actions as ADR 0045, once each: `export.create` on submit and `export.download` on a
full download. The detail gains `"scope":"reader"` so the audit console can tell the two apart.
A policy change is audited with the action the OU console already uses for policy edits, with
`{"field","from","to"}`.

### Interface

- **Reader page** `/exports`: the same components as the admin Export page in a reader mode —
  type tree, filter chips and condition builder over the reports source, live count and preview,
  an export dialog limited to Markdown ZIP — plus a usage line ("used today: 120 of 1000, at
  most 200 per export"). The submit button is disabled with the reason when the count exceeds either limit, so the
  422/429 paths are the backstop, not the normal experience.
- **Entry**: a user-menu item, shown only when the resolved policy allows export.
- **Task tray**: readers get the same tray, fed by the reader endpoints.
- **Policy console**: a "reader export" settings section on the admin Export page — an OU tree on the
  left, three `InheritField` controls on the right (allowed, max per export, daily quota) showing own
  value, effective value and source, as Workspace's service settings do. The Export page owns the
  policy; the OU detail page does not duplicate it.

## API

Reader endpoints, cookie session, `requireUserJSON`. An administrator calling them is answered from
the admin path's rules (no policy, full scope) so the shared components need not branch.

- `GET /api/exports/policy` → `{allowed, max_reports, daily_quota, used_today, remaining}`
- `GET /api/exports/sources` → the `reports` source only, Markdown ZIP only
- `POST /api/exports/preview` → as ADR 0045, reader scope applied
- `POST /api/exports` → see Enforcement
- `GET /api/exports?limit=50&active=1` → own tasks only
- `GET /api/exports/{id}`, `POST /api/exports/{id}/cancel`, `GET /api/exports/{id}/file` → own only;
  others 404. Once export is switched off for the reader, `file` answers 403 `export_disabled`:
  switching export off also stops files already produced from leaving. A download does not
  re-check the read scope: a finished archive reflects the scope at the time the worker ran, and a
  grant revoked afterwards does not reach into it during the file's 7-day life. Switching export
  off for the OU is the lever for withdrawing files already produced.

Internal plumbing report types (`internalTypes`) are excluded from everything a reader sees here:
count, preview, archive, type tree and version options, as every other reader listing does.

Error bodies carry `{"error": code, "code": code}` plus the limit fields above, so the SPA can
translate them. For an administrator, `GET /api/exports/policy` adds `"unlimited": true`.

Admin policy endpoints, `requireAdminJSON`:

- `GET /api/admin/exports/policy` → every OU's own and resolved values
- `PUT /api/admin/exports/policy/{ou}` → body fields `allowed`, `max_reports`, `daily_quota`, each
  tri-state: absent = unchanged, `null` = inherit (refused for the Default OU), a value = override.

## Acceptance conditions (each becomes a test)

1. Step `0013` adds exactly the three columns and the index on both drivers; an older dump restores.
2. A fresh database and a restored older dump both resolve the Default OU to `false / 200 / 1000`.
3. A child OU with NULLs inherits its parent's values; an override wins; the source OU is reported.
4. A reader whose OU disallows export gets 403 `export_disabled` on submit and `allowed:false`
   from the policy endpoint.
5. A reader never exports a report outside their read scope (restricted account, ungranted
   version), in count, preview or file.
6. Count above `max_reports` → 422; above the remaining quota → 429; exactly at the limit passes.
7. Two concurrent submits that each fit but together exceed the quota: exactly one succeeds.
8. Usage refunds: a cancelled job is charged `rows_done`, not `rows_estimate`.
9. A report ingested after submit does not raise a job above its `cap`.
10. Switching export off after submit fails the queued job with `export_disabled`.
11. A reader cannot list, read, cancel or download another user's task (404).
12. The reader API refuses any source but `reports` and any format but Markdown ZIP (400).
13. Audit rows carry `"scope":"reader"`; one `export.create` and one `export.download` per action.
14. A policy edit with `null` on the Default OU is refused; bounds are enforced (400).

## Consequences

Three columns join the OU policy row; the frozen v0.4.72 baseline is unchanged because they arrive
through the migration ledger. Readers gain a feature whose blast radius is bounded by three limits an
administrator controls per OU, and which is off for everyone until an administrator turns it on.
