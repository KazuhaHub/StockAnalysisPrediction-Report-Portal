# Statistics

The administration statistics page is an independent query surface at `/manage/statistics`.
It uses the administrator-only `/api/admin/statistics` endpoint and does not add database tables
or collect additional personal activity.

## Counting rules

- Reports: count stored report records, excluding internal pipeline artifacts. Group by category,
  report type, symbol or report date. Date filters use the civil report date.
- Runs: count stored jobs, not their input rows or retries. Group by status, execution target or
  submitter. Deleted jobs are no longer counted.
- Accounts: count current account records. Group by role, organizational unit, source or enabled
  flag. This is a current inventory, not historical account-state reconstruction.

Creation-date filters represent inclusive panel-timezone days. They are converted to the runtime
local wall-clock representation used by existing `created_at` values. Empty bounds include all
stored dates. Records without a creation date are included only when the bounds allow them.

## Scale and export

Group and filter in SQL. JSON returns at most 100 groups per page with record and group totals.
Search applies literally to group labels; SQL wildcard characters are escaped. Identifiers come
from a fixed registry. Every query is cancellable and has a 60-second deadline.

The CSV endpoint uses the same filters but ignores pagination and streams grouped rows from the
SQL cursor. It does not load all report or job bodies into application or browser memory. CSV is
UTF-8 with a BOM, uses standard quoting, and prefixes formula-like external labels as literal text.
A failed export terminates the response instead of presenting a completed partial result.

Queries and downloads reflect the data available when each request runs. A download is not a
transactional snapshot of an earlier page view. The export contains aggregates only, not report
content, passwords, tokens or other credential material.
