# Forecast verification — runbook

**Scope.** Operating the prediction-ledger evaluator (ADR 0029, ADR 0037). Signals below describe
this source checkout, not a claim that it is deployed. Reader access, the reading-page stage switch and
their signals are covered under "Reader surfaces".

## First response, in order

### Public-source request scheduling

`/manage/quotes` includes Request scheduling. Defaults are 30 actual HTTP requests/minute per
source, of which background work may use 6. These are protective portal limits, not published
vendor quotas. Redirects count separately and sibling source hosts share their budget. Configure
limits there; no infrastructure config or database migration is needed.

The panel's counters reset on restart. A cooldown is shared by display and evaluation; wait for
the single recovery probe rather than repeatedly refreshing or clearing the cache. Traffic from
other services or portal instances sharing the egress IP is not included.

A heartbeat with `pass.waiting_for_budget` is deferred work, not a failed prediction. Successful
reads are retained for the next slice while their policy/session scope remains valid. On mode
continues automatically. Dry mode requires another Evaluate now action, preferably after
`pass.resume_at` when present. Unfinished work is derived again from durable rows after restart.

See ADR 0043 for priority, cooldown and evidence lifetime boundaries.

### Read the observation counters

Use Refresh observations on `/manage/quotes` to fetch current counters without replacing an unsaved
source order, TTL or rate policy. The display table separates live requests from the historical
fallback endpoint. Fresh cache hits, shared-load joins and completed logical loads partition the
finished single requests. Stale responses and failed requests are overlapping outcome counts,
not additional requests. A historical fallback can generate one failed live request and one
successful history request. Responses served entirely by a browser's HTTP cache never reach these
server counters.

Home-card batches count distinct symbols within each request. Their cache ratio is cached symbols
divided by requested symbols, including missing symbols in the denominator. A logical load can
make zero HTTP calls after admission deferral, or several during failover. Compare actual HTTP
traffic with the separate per-vendor scheduler table, not with the logical-load count.

Latency p95 values are bounded-histogram upper bounds over completed samples since process start,
including failures. Display latency includes shared-load waiting; vendor latency covers dispatch
through response-body close, excluding admission wait. Empty samples show a dash in the UI.
Failure details count 403, 429, 5xx and connection errors; other HTTP failures remain in the total.
Parser failures appear in the source-health record rather than being invented as HTTP failures.
All counters reset on process restart and survive a display-cache purge. Save snapshots and their
timestamps before restarting during an observation period.

### Conservative rollout and trading-day acceptance

The source checkout and green CI are pre-release evidence. They do not demonstrate production
egress availability, deployment identity or a complete observed trading session.

1. Record the candidate commit, image digest or verified archive checksum and the existing running
   version. Confirm the configured database driver and retain a restorable pre-update backup using
   the existing release runbook. This change does not alter the database shape.
2. Inventory Dify, AKTools and other consumers sharing the public IP. The portal's limiter cannot
   budget another process's traffic. Avoid overlapping bulk jobs during the initial check; retain
   the initial per-source policy of 30 total and 6 background requests/minute.
3. Probe from the actual deployment host with the candidate binary. Choose two completed trading
   sessions rather than blindly reusing the dates below. A small explicit capture can check all
   four benchmark indices and representative stocks without querying the full fixed panel:

   ```bash
   report-portal quote-probe capture \
     --out /secure/evidence/sohu-initial \
     --from 2026-10-22 --to 2026-10-23 \
     --sources sohu \
     --targets sh:000852,sh:000906,sh:000300,sh:000905,sh:600081,sz:000001
   ```

   The output directory must not exist. Inspect every record, both requested dates, code/namespace,
   prices and volume units. `complete=true` means capture finished, not that every record passed.
   The CLI does not open the application database or reuse display cache. Its process budget is
   separate from a running portal's budget, so coordinate this brief probe with other traffic.
   `--targets` replaces the fixed panel and cannot combine with `--suspended`. A smaller capture
   cannot satisfy the unchanged full-panel `compare --record` gate or grant settlement authority.
4. Deploy through the existing release path, verify the actual running version/digest and read
   `/healthz` plus the authenticated quote/forecast state. Keep Sohu disabled until its egress probe
   passes; enabling historical display still grants no settlement capability. Keep the evaluator's
   existing mode. In dry mode, use one Evaluate now action and resume after `resume_at` when deferred.
5. While that pass is pending, read a representative live quote, a history chart and a home page.
   Record the source counters, display counters, original vendor timestamps and evaluator heartbeat.
   Repeated warmed requests should increase cache hits without matching HTTP growth. Reader requests
   must not sit behind new background admission; verify the five-second admission queue bound and
   retained foreground capacity. Do not simulate a vendor outage by repeatedly hammering production.
6. Observe at least one complete trading day, including the opening, midday pause, afternoon opening
   and completed-close boundary. Capture observations at those boundaries and after a manual dry pass.
   Track `pass.waiting_for_budget`, `resume_at`, reused bodies, `due_unsettled` and
   `oldest_due_unsettled_minutes`. Pending work should progress when budget returns, without repeatedly
   fetching already retained bodies or charging prediction attempts for admission deferrals.
7. Accept only when the source dispatch spacing and last-minute counts respect configured budgets,
   readers retain priority, source cooldown blocks requests until a single recovery probe, vendor
   timestamps remain unmodified, historical rows remain explicitly non-live, and no evaluator error
   or stale-valve failure is concealed by display fallback. Record actual evidence and limitations;
   a quiet weekend or a handful of successful probes is not a full trading-day observation.

If 403/429 errors recur, let the shared cooldown run and investigate other traffic on the same IP.
Do not raise the limit or clear the cache to force recovery. Pause further manual evaluation when
needed; source availability and ledger readiness remain separate checks. If rollback is needed,
use the recorded previous image digest or verified binary with the existing deployment procedure.
Disable the optional display source through the UI if its history quality is the issue. No database
rollback or forced settlement is part of this change.

For a manual Docker Compose deployment using the repository's service names, first complete the
backup and identity checks above from the existing deployment directory. Add a temporary override
with `services.report-portal.image` pinned to the **published candidate tag or digest**. The file
names and loopback port below match the repository template; confirm them against the running
deployment rather than replacing its Compose file or infrastructure settings.

```bash
docker compose -f docker-compose.yml -f compose.quote-candidate.yaml pull report-portal
docker compose -f docker-compose.yml -f compose.quote-candidate.yaml up -d --no-deps report-portal
docker compose -f docker-compose.yml -f compose.quote-candidate.yaml exec -T report-portal /app/report-portal version
curl --fail --silent --show-error http://127.0.0.1:8790/healthz
```

After startup, run the bounded probe through `docker compose ... exec -T report-portal
/app/report-portal quote-probe capture ...` to use the actual container's network and client.
Use a new private directory under the existing `/app/data` mount for `--out`, retain its manifest
and inspect every record. This CLI path does not open the database. Leave the historical source off
until the actual-container probe passes. For rollback, pin the recorded previous immutable image
reference in the same override and recreate only `report-portal`; verify its version and health.

### Degraded display and optional historical fallback

A failed live refresh may display the last successful response for up to fifteen minutes after
its cache expiry, with a warning and its original vendor timestamp. It does not extend that expiry
or become evaluator evidence. If no usable live response remains, an enabled Sohu source can provide
prior-day historical daily charts through the separate `/api/quote-history/{symbol}` route.

Sohu ships disabled. Before enabling it, run a bounded `quote-probe capture --sources sohu` from
the deployment's actual egress, inspect every record's error and retain the raw evidence. Beijing
is deliberately unsupported. A completed capture means the capture finished, not that every
instrument succeeded. The local fixed-panel measurement is not production-egress validation.
Enable the source in `/manage/quotes` only after this check, then verify the historical label,
date and source on a daily chart during a live-source outage. Intraday requests must still fail
or use a real intraday source; historical closes must not appear as live quotes.

The reader surface retries the live path using its current outage advice, even while a historical
chart remains cached. Neither enabling this source nor clearing the display cache changes ledger
source authority. See ADR 0044 for the wire and cache boundaries.

1. Read the status card on `/manage/forecast` (mode, last pass, last error, the counters and
   `awaiting_gate`), and the Alerts card for the last delivery per channel. The heartbeat's
   per-benchmark bars and the `panics`/`stale_valve_tripped` flags are in
   `GET /api/admin/forecast` (`heartbeat`) when the card's derived state is not enough.
2. `GET /healthz/forecast` answers one boolean. 503 with the mode on means the heartbeat is missing or
   older than ten minutes; the process may still be up.
3. Read the boot/loop log for `forecast evaluator:` lines. A per-set error names the set; a pass
   error is the pass.
4. Do not force a settlement by editing rows. Every write in a pass goes through one plan; a manual
   `UPDATE` is outside every guard the evaluator has.

## Signals

### `stalled` (Email + Slack, at most hourly) or `/healthz/forecast` 503

The heartbeat is older than ten minutes while the mode is not off.

- Check the process is up and `/healthz` (database) is green.
- Read `last_error` and `panics` on the card. The loop recovers from a panic and keeps going; a stall
  with no panic is a wedge (a blocked store call, a stuck vendor request).
- Restart only after reading the log. The loop derives due work from rows, so a restart resumes; it
  does not replay anything.

### `lagging` (Email + Slack, at most daily)

One of: a due mark unsettled for over six hours, the stale-data valve tripped, or a benchmark
calendar still ending before today after 18:00 CST on a weekday.

- Look at the heartbeat's per-benchmark last bar and source, and at `stale_valve_tripped`, in
  `GET /api/admin/forecast`.
- A vendor-wide lag is waited out: while the valve is tripped no mark is resolved from a missing bar;
  a deferral still records the reason, session and wake, no attempt moves, and nothing can become
  final. Evidence of a halt (a body stamped at or after the session) can still resolve a mark. Never
  force a settle.
- One source down: inspect the reported Tencent request or source failure and wait for recovery.
  Settlement follows the quotes panel's enabled source order and explicit ledger grants. Tencent
  remains the only Tier 1 writer; disabling it refuses settlement rather than using a display or
  shadow source. Restoring it permits normal retry without spending source-outage attempts.
- A source that keeps failing is benched across passes, not just inside one: after a pass in which it
  failed and never answered, it is not asked again for one minute, then five, fifteen, thirty for
  each further failed pass. A single success clears the bench and the streak. The vendor's own last
  error is what the planner records while a source is benched, so a refusal still names why.
- Read the error, not only the status. A non-2xx now carries the method, path, HTTP version,
  `Server`, `Retry-After` and a short summary of the body. `403` with an HTML interstitial and a CDN
  `Server` is a WAF; `429` or a `Retry-After` is a rate limit; `501`/`404` with a body naming another
  address is an endpoint that moved. The first asks for quieter traffic, the second for a wait, the
  third for a code change — and the body is the only thing that tells the first from the third.

### The vendor is throttling us

Symptom: several sources fail at once, the reader's own price requests fail with the same status,
and the evaluator's heartbeat shows the same host failing across passes. Check, from the deployment
itself, one request per endpoint:

```
curl -4 --http1.1 -A 'Mozilla/5.0' -e 'https://gu.qq.com/' -o /dev/null -w '%{http_code}\n' \
  'https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=sh000852,day,2026-10-01,2026-10-24,800,bfq'
```

A `200` after a quiet period confirms request pressure rather than a moved endpoint. Then, in
order: switch the evaluator to `0` (dry no longer polls, but a mode-1 ticker does), let the cooldown
elapse rather than retrying by hand, and only then decide whether the deployment needs a different
egress. Do not switch dry on expecting it to idle at a lower volume — a dry pass is now an operator
action, so the right stop is `0`, and the right restart is a single manual dry pass when the vendor
answers again.

### `awaiting_gate` names a benchmark

The mode is `on`, the set's frozen benchmark is planned, but no hand check is recorded for that code,
so nothing is written for it.

- Hand-check one would-be d5 for that code: the stock's and the index's closes on `base_date` and on
  the due session, against the exchange's published closes.
- Record the check in the card's form. It is audited (`forecast.dry_gate`) and stored per code.

### `predictions.store_failed` (Email + Slack, each occurrence)

A covered report was stored and its ledger set was not, after one retry. The set cannot be re-created
after the fact.

- Find the report in the audit filter `predictions.store_failed`; the detail carries the error.
- Record an incident. The report is readable; the ledger simply has no set for it.

### A `test` count appears

The producer declared `run_mode: "test"`. A test set is never a member and never a duplicate.

- Expected for Dify test-app runs. If a production run is marked `test`, fix the producer; the set
  stays a test.

### An `off_benchmark` count appears

A set was frozen with a code other than its cohort's active benchmark.

- Explain it in the release note for the cohort. Never re-score: the frozen benchmark is the record.

## Controlled changes

### Switching the evaluator on

The readiness list is code (`forecastReadiness` in `internal/app/forecast_ops.go`). Until every item
holds, `POST /api/admin/forecast {"mode":"1"}` answers 409 `forecast_not_ready` and names the failing
items. There are six: `binary`, `population` (the ledger start is set), `dry_pass`, `dry_gate`,
`alerts` and, item 6, `ledger_chain`.

1. `dry`, then "Evaluate now"; read the sample and `last_error`. The pass MUST produce a sample for
   this item to pass — "no error" is also what a refused vendor looks like. If the card says
   `dry_pass_no_source`, not one vendor body came back: check the quote sources and the egress before
   going further. If it says `dry_pass_no_plan`, the vendor answered and nothing was due; wait for a
   checkpoint and run it again.
2. Record a dry-run check for every benchmark in use (the card lists the codes).
3. Configure at least one alert channel (email recipients, a Slack webhook, or both) and send a test
   alert. It goes to every configured, enabled channel, and the readiness stamp is written only when
   all of them deliver. A channel paused in the notification settings takes no part; resuming it
   deletes the stamp, so test again.
4. Check item 6, `ledger_chain`: the quote-source panel must show an enabled settlement chain whose
   primary carries both evidence capabilities. Without it settlement refuses every mark and the
   watchdog reports `no_source`, so the mode is refused rather than allowed to idle. If the item is
   red, repair the source order on the panel (a save that removes the primary asks for
   confirmation and is audited as `quote.order`); do not work around it.
5. Switch to `1`. The change, the actor and the readiness list are audited (`forecast.auto`).

### Scheduling a benchmark

- Choose a code from the closed allowlist. The response's `effective_cohort` says when it applies.
- CSI 800 for November: the schedule must read `effective_cohort: 2026-11`. If the governance deploy
  slipped past 2026-10-31, use the contingency in plan §1.2 and note the time in the operations log.

### Setting the ledger start

Write-once, bounded by the oldest set, and declared in the Dify repository's issue before the first
S0 close. Preview the candidate list on the page, then save once. It survives restores.

## Deploy and restore

### Before a release

A release that contains step `0009` (the owner-approved `prediction_purge_owners` table, which lets
an `own` reader be shown only the purged months of their own OU subtree) is deployed like any other:
pull the new image and restart. Startup applies the step. The one thing to get right is the backup,
which must come from the **previous** release; read
[forecast-purge-ownership-upgrade.md](forecast-purge-ownership-upgrade.md) for how to take it under a
moving tag, and for rolling back across the step, which is restore-only.

Take the ADR 0027 backup. For R1a-β2 also record the heal predicate's count (Q3 = 20). A binary older
than step `0006` refuses a database that records it: rolling back across the access schema is
restore-only.

### Release with the purge ownership evidence step (`0009`)

Step `0009` is an ordinary automatic step. The full notes are
[forecast-purge-ownership-upgrade.md](forecast-purge-ownership-upgrade.md); the points that are easy to
miss are:

- **Take the backup with the previous release, before anything pulls.** The new binary's `backup`,
  `restore`, `adduser`, `security`, `recompute-kinds` and `freeze-names` all open the store the way startup
  does and apply pending steps first, so a backup taken with them already declares `0009` and the previous
  release cannot restore it. Under `pull_policy: always` (the shipped compose file), `docker compose run`
  pulls too: record the running container's image id first and back up from it with `docker run
  --volumes-from <the stopped container> --env-file <an RP_* file only you can read>`, deleting the file
  afterwards.
- **Check the backup's size.** `backup` prints its table and row counts on stderr; they must match the
  deployment, and the previous release's restore dry run must list non-zero `users` and `reports` rows.
- **Optionally see what will run.** `report-portal schema-upgrade` lists the pending steps and changes
  nothing; `--apply` applies them offline with the same runner. Neither is needed for a deploy. The list is
  in the build's list order, which is merge order and not id order: `0007`, `0008` and `0009` were reserved
  by branches that were still open when the announcement step `0010` reached main, so they follow it. For a
  database that has recorded `0001` to `0006`, a release that also ships the notification (`0007`) and
  extension (`0008`) steps lists `[0010, 0007, 0008, 0009]`; `0009` is always last. An id that looks out of
  order in that list is not an error.
- Evidence starts empty and is never back-filled. Months purged before the upgrade stay visible to `all`
  readers and the administrator views and are never disclosed to own readers.

### After a restore

A restore resets the heartbeat, the alert state, the dry gate and the alert verification. The mode
becomes the lower of the dump and live values, and a restored `1` is capped at `dry`
(`restore_gates_empty`, audited). Re-check deliberately: run a dry pass, record the gates, send the
test alert, then switch on if that is intended.

The evaluator mode and the reading stage follow one rule: a restore never raises them. **The OU
access settings do not.** They come back exactly as dumped and can open access that the live database
had closed, so a restore needs the extra check below.

- **Reading stage.** `forecast_reader` becomes the lower of the dump and the live value
  (`off` < `card` < `full`). Both sides are read the way readers read them: an unset value is the
  default `card`, but an empty, unrecognised or unreadable value counts as `off`. A result that
  differs from the dumped value is audited as `forecast.reader` with actor `system`. After a restore
  from a dump taken at `full`, the stage is therefore whatever the live database had if that was
  lower; raise it again deliberately, one step at a time.
- **OU scopes and attribution come back exactly as dumped, and that can widen access.**
  `user_groups.forecast_scope` and `prediction_sets.attributed_ou` are restored verbatim; the only
  adjustment is that the Default OU is set to `none` when it comes back unset. The audit log is part
  of the dump too. A restore from a backup taken before a record incident therefore re-opens every OU
  that was `all` or `own` when the backup was taken, with no audit row for it, and the incident's own
  `group.change` rows are gone with the restore. Only a dump from before step `0006` can narrow
  instead: it has no scopes, so every OU inherits and the Default OU reads `none`.
  1. Before restoring, save `GET /api/admin/forecast/access` (the live values), because the restore
     replaces them.
  2. After the restore, and before readers return, read it again and compare the two. After a
     record incident, or any OU scope change since the backup was taken, assume widening until
     proven otherwise.
  3. Set every OU that should be closed back to `none` in the OU editor (see "Record incident"), then
     confirm with the endpoint. Do not tell anyone access is as it was until this comparison is done.
- **Counters.** The `forecast_signals_<date>` rows and the weekly viewer counts come back from the
  dump. They are observations, not settings; a gap in them after a restore is the restore, not an
  outage.

## December projection-calendar maintenance

After the exchanges publish their annual closure notices, verify the current and next calendar
years in `internal/app/forecast_calendar.go` against those notices. Record the source URL beside
each year's data and include later exchange amendments. Exchange weekends remain closed even
when the government designates them as make-up working days. Do not substitute a public-holiday
calendar for exchange evidence, or invent an unpublished year's dates.

Run `TestProjectionMatchesKnownCalendars`, `TestMissingYearDegradesToOnOrAfter` and
`TestSettlementNeverReadsTheTable`, plus the reading-panel tests. The last test guards the boundary:
this table changes reading-page estimates only, never settlement or evaluator wake scheduling.
An interval crossing a missing year must retain the weekday "on or after" wording rather than
claim an expected exchange date. In October 2026 the verified runtime table covers 2026 only;
2027 remains deliberately absent pending its annual notice and December review.

## Reader statistics explanation contract

The session-only groups response and its temporary admin alias may carry the following flattened
metadata. These fields are not a new machine API and must not be added to `/api/v1` routes merely
to expose the reader summary. External-all readers receive none of the fields in this section;
own readers receive only their scoped population. A missing field is not a measured zero.

| Field | Meaning |
| --- | --- |
| `summary.since`, `sets`, `accepted`, `stocks` | Earliest retained receipt date, retained row count, accepted row count, and distinct nonempty symbols in the requested range and population. Accepted includes repeat editions; it is not the statistical member count. |
| `summary.marks_by_phase` | Checkpoint obligations of classified members and pending-baseline candidates represented by the returned cells. Duplicate, rejected and other excluded rows do not create obligations. The h12 population includes standard horizons only. Mature cells contribute resolved and final counts; purged cells contribute nothing. |
| `summary.next_result_not_before`, `expected` | Earliest outstanding date across the returned cells. It can already be in the past when settlement is delayed. `expected` qualifies an exchange-schedule estimate, not a promised or observed result. Tied estimates retain the flag only if every tied date is qualified. |
| `rtypes`, `min_n` | Sorted covered report types and the minimum publishable sample size. |
| `limits` | `max_range_months`, `max_week_groups`, `official_slots`, `explore_slots`, `wait_sec`, `custom_per_min`, and `members_per_min`; the configured ceilings, not remaining budget. |
| `generated_at`, `ledger_start` | UTC response-generation instant and the declared ledger boundary. An empty ledger start means unset, not the beginning of retained history. |
| `capabilities` | `monthly`, `custom`, and `members` permission hints from the same decision used by route gates. They do not bypass fresh authorization, validation, rate limits or scorer capacity. |
| `evaluator.mode` | `0`, `dry`, or `1`; a paused evaluator is distinct from a failed checkpoint. |

Non-mature cells carry `pending_reasons`, `failed_cap_exceeded`, and, when known, `not_before` with
its optional `expected` qualifier. Phase counts include resolved and final entries to reconcile
the selected population; unclassifiable timing is counted as `unknown`. The failure-cap flag uses
the same final-refusal ratio and strict threshold as the existing maturity gate. Explanations are
request-time copies, not additional scoring and not entries in the mature-cell cache.

Retention exclusions apply before summary construction. A late row in a marked month cannot
resurrect that month in summary counts. These scalars describe retained data, not all-time totals;
the separate purge metadata must remain visible where authorized. An own reader's purge metadata
comes from the purge ownership evidence (ADR 0039): only months its current subtree owned sets in when
retention removed them. Every other emptied month is an ordinary empty month for that reader, and months
purged before the evidence table existed are never disclosed to it. Release of own-scoped reads still
waits for a release carrying step `0009` to be deployed and the review recorded in the implementation log.

## Reading-page summary contract (P18 draft)

The session stock/run response may carry `prediction` at stage `full`; admins resolve to full,
but none remains an independent refusal for non-admins. `X-Prefetch: 1` omits the summary while
retaining scoped stock timeline flags. These additions do not expand the machine API.

- `display_set_id` selects the latest accepted set matching the body already chosen for this
  response, falling back to the latest accepted set. Compact selection is not capped by the
  200-row card response. A concurrent ingest must not pair a future body's prediction with the
  body being returned. `body_changed` qualifies the same selected response body.
- `stance`, optional `weight_bp` and optional `upside_bp` reuse the selected set's claims and
  frozen-baseline arithmetic. Missing numbers are not zero. Payloads and diagnostics never
  belong to this summary.
- `next_checkpoint` is the first nonterminal checkpoint in the established checkpoint order.
  It carries the existing reading `phase`, `not_before` and `expected` semantics; a resolved or
  final-refusal checkpoint is not outstanding, even when receipt timing is unreadable. Missing
  applicable marks remain pending obligations; nonstandard horizons have no next h12 checkpoint,
  whether an h12 mark is absent or an old pending row exists.
  Unknown timing omits phase/date rather than manufacturing a deadline. A not-before date
  may already be past while settlement is delayed; an expected date is not observed evidence.
- `membership` and `counted_set_id` are available only to internal all readers and admins.
  Internal own and every external reader receive neither, and do not trigger global membership
  classification. Report readability is independently rechecked when constructing a summary.
- A ledger failure omits the summary, not the report body or its navigation. Absence can also
  mean prefetch, disabled stage, unsupported type or no accepted prediction; it is not proof of
  a failed evaluator.

`has_prediction` distinguishes a populated summary from an empty edition with a readable
`sibling` link. An empty edition never borrows the sibling's stance, display identity, body-change
warning or checkpoint. The linked `report_id`, `set_id`, `version`, `cohort` and exact `url`
identify the actual other edition. Candidates share symbol/report-date/type, not title; they
must be another version and independently readable. Fallback uses the newest eligible report,
then its body-matching accepted set. Older readable reports remain eligible for the link to the
edition that actually counted, even if a later generator output replaced it in the switcher.

Internal all readers and admins may receive `counted_edition`. For a populated current edition,
it must belong to the same receipt cohort. It does not change the report's own `counted_set_id`.
An own-report member from a later cohort cannot hide the displayed cohort's counted edition.
The full-stage summary suppresses a duplicate badge in favor of this explicit edition identity.
Per-set classifier results on the card route remain factual; presentation should prefer the
counted-edition label rather than describing the other edition's member as a duplicate.
Own and external readers never receive counted-edition metadata or cause global classification.

Card/full session prediction reads carry the same optional `sibling` and `counted_edition`
metadata, including when using a cookie on the v1 report route. Persistent machine and app-bridge
credentials retain their existing shape. Stage off still refuses non-admin session predictions.
Those session reads also carry `covered`, whether the ledger covers the report's type. A covered
report with no set made no forecast and shows the card's empty state; an uncovered type shows no
card at all. The field is omitted when the type cannot be read, which the card treats as unknown
and keeps showing itself.
These are server contracts; the P20 card presentation and stale-edition UI guards remain separate.

The stage access matrix now exercises these server surfaces through the real mux on both drivers
and both Default-scope variants. The [local performance checkpoint](forecast-reading-performance.md)
records an actual pre-P18 source comparison and the card/full budget, including prefetch. These
checks do not replace P20 presentation tests, independent review or production observation, and
are not authorization to enable full in production.

## Reader surfaces: signals and incidents

Two switches govern what a non-administrator reads, and they are independent. Each OU has a forecast
scope (`all`, `own` or `none`; a child inherits its parent unless it sets its own; the Default OU
reads `none`; administrators always read `all`). The reading stage `forecast_reader` (`off`, `card`,
`full`; administrators always read `full`) governs the per-report surfaces and never widens a scope.
The statistics page (`/forecast`, `GET /api/forecast/groups` and `/groups/members`) follows the scope
alone. Both changes are audited, need no restart, and apply to the next request.

### Signals

**Slow statistics request.** One log line for each statistics request, on the reader route and the
temporary admin alias, that took longer than three seconds:

```text
forecast groups: scope=own pop=ou mode=month cells=85 scored=1 cached=0 flight=0 wait_ms=0 dur_ms=3412
```

| Field | Meaning |
| --- | --- |
| `scope`, `pop` | The effective scope (`all` or `own`) and the kind of population (`all`, or `ou` for a subtree). Never an account name and never the OU ids. |
| `mode` | `month`, `custom`, or `other` for a value the route rejects. |
| `cells` | Group by checkpoint cells the request needed. |
| `scored` | Cells whose scoring this request started and saw complete. |
| `cached` | Cells answered from the mature-cell memo. |
| `flight` | Cells that joined a computation another request had already started. |
| `wait_ms` | The longest any one of its cells waited for a scoring slot, or for an answer when it gave up. Zero for a cell that took a free slot or joined a running flight. |
| `dur_ms` | The whole request. |

Reading it: `scored` above zero with `wait_ms` near zero is a cold cache, not contention. A `wait_ms`
close to the wait ceiling (`forecast_wait_sec`) means the pool was saturated and the request ended in
a 503 `forecast_busy` (compare the `busy` counter). `flight` above zero with `scored=0` is a request
queued behind identical work. The performance target is a monthly view at or under three seconds
on a cold cache; cached requests never wait for a slot.

**Access refusals.** `forecast access: refused reason=<code> n=<count>`, at most one line a minute
for each reason; `n` is how many refusals of that reason the line stands for. The line never names
an account, a path or a query. The counters below are exact; the log only says it is happening.

| Code | Refused because |
| --- | --- |
| `ou_none` | The account's effective scope is `none` (the expected answer for an OU nobody has opened up). |
| `default_null` | No OU on the chain sets a value and the Default OU is unset. |
| `dangling_group`, `cyclic_chain` | The OU tree is broken (a parent that no longer exists, or a loop); the account resolves to `none` until it is repaired. |
| `unknown_value` | A stored scope that is not `all`, `own` or `none`. |
| `error` | The account or the tree could not be read; the portal refuses rather than guesses. |
| `anonymous` | No signed-in user. |
| `custom_not_allowed` | An external account asked for a custom range. By design. |
| `stage_off` | The reading stage is `off` and the account's scope would otherwise admit it. |
| `bridge_token` | An installed app's token asked for a forecast read; refused until the shared token binding lands. |
| `other` | A reason outside this list (a bug if it is ever non-zero). |

**Counters.** The portal counts these in memory and flushes them every hour to `meta`, one row for
each UTC day, `forecast_signals_<YYYY-MM-DD>`, holding a flat JSON object of integers. A flush that
has nothing new changes nothing, and rows older than 14 days are deleted. Names come from a closed
list, so no account, token or hash can reach a row.

| Counter | Counts |
| --- | --- |
| `requests` | Statistics requests that passed the access gate (served or failed after it). |
| `cache_hits` | Cells answered from the memo. |
| `flights_joined` | Cells that joined another request's computation. |
| `scored` | Cells scored. |
| `busy` | 503 `forecast_busy` answers. |
| `rate_limited` | 429 `forecast_rate_limited` answers, on both reader routes. |
| `drawer_opens` | Member lists served (the cell drawer); an invalid request is not an open. |
| `refused.<code>` | Refusals, by the codes above. |

```sql
SELECT k, v FROM meta WHERE k LIKE 'forecast_signals_%' ORDER BY k;
```

A restart loses at most the events since the last hourly flush. An unreadable row is never
overwritten: the flush logs `forecast signals: flush failed` every hour and keeps the counts in
memory; deleting the damaged row lets the next flush write them. The weekly distinct-viewer figure
(`forecast_viewers_<ISO week>`, an integer; the figure is an upper bound after a restart) is the
adoption indicator and is separate.

### An OU's members report "forecast missing"

Expected for every OU at `none`, which is where each starts; nothing is broken.

1. `GET /api/admin/forecast/access` lists every OU with its stored value (`forecast_scope`, null for
   inherit), the value it resolves to (`forecast_scope_effective`), the OU that supplied it
   (`forecast_scope_source`), whether it is external, and how many accounts resolve to each scope.
   `?ou=<id>&scope=all|own|none|inherit` previews a change and reports how many accounts it
   would change (`affected`) without writing anything.
2. If the OU resolves to `none` and that is not intended, set its scope in the OU editor
   (`PUT /api/admin/groups/{id}` with `forecast_scope`). It is audited as `group.change` with the
   `forecast_scope` change, and applies to the next request.
3. If it resolves to `all` or `own` and the account still reads nothing, look at that day's refusal
   counters and log lines. `stage_off` means the reading stage is `off` (below). `dangling_group`
   or `cyclic_chain` means the tree needs repairing in the OU editor. `unknown_value` means the scope
   needs saving again. `error` means the database could not be read; look at the database and the
   log. `custom_not_allowed` and `bridge_token` are by design.
4. An `own` reader sees only the sets attributed to their OU's subtree. An OU with no attributed sets
   yet reads an empty record, not an error.

### Reading-page incident: `forecast_reader=off`

The reading-page card, its summary or the timeline marks are wrong or too slow (`/api/stock` over its
800 ms budget).

Set the reading stage to `off`: `POST /api/admin/forecast {"reader":"off"}`, or the settings dialog
on `/manage/forecast`. It is immediate, needs no restart or schema change, and is audited as
`forecast.reader` with `{from,to}` only when the value really changes. Non-administrators are then
refused on the per-report prediction reads (403 `forecast_unavailable`, counted as `stage_off`),
which the reading page treats as absence: no forecast and no error. Administrators still read
`full`, so the defect can be reproduced. The statistics page is a different surface and stays as the
OU scopes leave it. `card` is the middle step: the per-report card without the full-stage summary and
timeline marks.

Recovery goes up one step at a time (`card`, then `full`), each a recorded change. A restore never
raises the stage (see "After a restore").

### Record incident: set the affected OUs to `none`

Use this when a reader may see records they should not (a population, attribution or scoping defect),
or when the statistics themselves are suspect and need to stop being read.

Set the forecast scope of each affected OU to `none` in the OU editor. It is immediate and audited
(`group.change`). A child that inherits stops reading too, but an OU that has its own explicit value
does not follow its parent, so setting the Default OU to `none` is not enough on its own: set the
explicit ones. When the extent is not yet known, set every OU. Administrators keep `all`, so the
record can still be inspected. No cache needs purging: a cell is keyed by the population it was
computed for. Confirm with `GET /api/admin/forecast/access` (every affected OU resolves to `none`)
and with `refused.ou_none` rising, then follow "Recording an incident". Access is given back by the
same edit, as a decision and not before the cause is understood. If only the reading page is
involved, `forecast_reader=off` is the smaller step.

A restore during or after the incident undoes this step. OU scopes come back as the backup recorded
them, so every OU that was `all` or `own` at backup time is open again, without an audit row and with
the incident's edits gone from the log. Save `GET /api/admin/forecast/access` before restoring, compare
it afterwards, and set the incident OUs back to `none` before readers return (see "After a restore").

## Recording an incident

Record the time, the mode, the signal, the affected sets or benchmark codes, what the log said, and
the action taken. A settlement that was waited out (a vendor lag) is not an incident against the
portal; a `store_failed` or a forced manual edit is.

## Ledger-source diagnostics (P23 local implementation)

The administrator-only `/api/admin/quote` response includes each source's explicit `settled` and
`settledShadow` market arrays plus `cashDividends` and `reportsKind` evidence flags. Empty grants
are empty arrays, not null. A display-only wildcard never becomes a ledger grant.

Top-level `ledgerChain`, `ledgerPrimary` and `ledgerShadowPeer` summarize enabled declarations in
operator order, with cash-dividend writers first. These are configuration diagnostics across the
declared markets, not proof that every benchmark or stock is eligible: runtime additionally checks
the actual market/code and the source's symbol predicate. An absent primary or shadow peer is an
empty string. No shadow peer is advertised until it has an explicit enabled shadow grant.

`ledgerHealth` is a separate array of source health records, with UTC RFC3339 timestamps and an
empty string for an event that has not happened. It never overwrites the display health fields
on `sources`. Clearing the display cache clears neither health history nor a cooldown. Ledger
refusals and caller cancellation do not count as vendor failures; three delivery/parser failures
stop calls to that source for the current evaluator pass, preserving the actual failure rather than
misreporting an absent configuration. A pass in which the source failed and never answered also
benches it OUTSIDE the pass — one minute, then five, fifteen, thirty, cleared by any success — and
the observations recorded while benched carry the refusal `cooldown` rather than `breaker`. The
cooldown is process state, so a restart forgets it; the health history is not, and a restart does
not erase it either.

The quote-source management page shows these declarations and the separate settlement history.
Unavailable telemetry is distinct from an empty process-local history; neither claims readiness.
A save removing the configured primary requires confirmation. Cancelling or leaving that
confirmation writes nothing, and repeated save clicks cannot create concurrent requests.

Readiness item 6 (`ledger_chain`) now requires an explicitly enabled settlement chain with a
primary carrying both evidence capabilities. Removing it prevents switching from dry to mode 1.
If removed while running, the evaluator card warns instead of claiming health, and the watchdog
emits `no_source` at most daily through the existing webhook/email/Slack path. The watchdog checks
this even before a first heartbeat exists; off mode remains silent with only its mode read.

This contract describes local P23 work, not a production rollout. Per-instrument runtime checks,
the other readiness gates and deployment approval still apply. Do not enable the evaluator on
the strength of configuration diagnostics alone.

Source-order saves additionally write `quote.order` with the effective `from` and `to` source
arrays, actor, organizational unit and request address. Equivalent spellings, repeated saves and
an explicit shipped order versus its reset form do not create duplicate transitions. Because the
order now controls durable settlement, this dedicated transition and its audit commit together;
an audit failure rolls the order back and returns an error before other submitted settings are
written. The existing general policy audit remains separate. This adds no database shape.

## Offline source measurement (P24)

`quote-probe` is a standalone command. It never loads `config.yaml` or `RP_CONFIG`, opens a
database, changes source grants/order, or writes evaluator health, shadow state or ledger rows.
Only `capture` makes vendor GET requests. `compare` reads local evidence without a network client.
Do not use agreement here as a settlement grant or as approval for Tier 2.

Capture a bounded window into a **new** directory (its parent must already exist):

```sh
report-portal quote-probe capture --out ./probe-2026-10-19 \
  --from 2026-10-19 --to 2026-10-20 --sources tencent,sina,yahoo
```

The fixed panel is three indices (`sh:000852`, `sh:000906`, `sh:000300`) and twenty stocks:
`sh:600000,sh:600519,sh:600036,sh:601318,sh:601899,sh:688001,sh:688981,sh:689009,sz:000001,sz:000333,sz:000651,sz:002594,sz:002415,sz:300750,sz:300059,sz:301308,sz:301269,bj:830799,bj:835185,bj:920002`.
It is a measurement set, not an investment recommendation. Append operator-supplied suspended
codes with `--suspended sz:000004,bj:430047`; these are examples of syntax, **not claims that those
securities are currently suspended**. Each extra must be unique and not already in the fixed panel.
Suspension is not inferred from missing data. Any compiled-in source can be listed in `--sources`,
including Yahoo despite its lack of settlement admission. Unknown or duplicate sources are errors.

The output directory is private (0700), and files are private (0600), exclusively created, never
overwritten. The manifest declares the requested source/target matrix and window, fetch mode,
response URLs/status/timestamps, raw byte counts/hashes, parsed-response hashes and errors.
Raw files preserve the HTTP response body before GBK/parser conversion; they are not a packet
capture of compressed wire traffic. Each body is capped at 512 KiB; oversize and partial reads
remain failed evidence. A `count` fetch means the source was asked for up to 800 daily bars,
**not** that it supports a bounded range. Cancellation or local storage failure leaves partial
evidence for inspection. `complete` means all requested attempts finished, not that all succeeded.

Replay with two explicit anchor dates inside the captured window:

```sh
report-portal quote-probe compare --in ./probe-2026-10-19 \
  --primary tencent --anchor-start 2026-10-19 --anchor-end 2026-10-20 --record
```

Choose dates for which the reference is expected to supply bars. Both primary anchors must exist;
otherwise that instrument is unavailable. The same date may be used for both anchors to inspect
one session. The replay trims count/range series to the manifest window, then calls the shared
shadow comparator: exact integer-fen OHLC and date-set differences, including both anchors, with
deterministic date/field ordering. Volume is not part of the price comparison. Candidate missing
dates/anchors are mismatches; failed/truncated/empty evidence is unavailable, never agreement.
Identity/source, adjustment/cache flags and all supplied bars are validated before comparison.

JSON goes to stdout. `--record` additionally requires the entire fixed panel and creates
`comparison.json` in the capture directory exclusively. It can record unavailable/mismatching
results; it is not a success certificate. An existing report is never replaced. Without
`--record`, replay writes nothing and may inspect a smaller valid capture. Missing/tampered files,
incomplete matrices, unsupported versions, non-regular/symlink evidence, hash/byte-count failures
and invalid JSON fail the command. Reads have individual and aggregate bounds (8 MiB manifest,
2 MiB parsed response, 512 MiB total). The report includes the manifest SHA-256; hashes protect
against accidental corruption, not a malicious capture author. Preserve the entire directory
when sharing evidence. Neither replay nor recording follows URLs in the manifest.

Exit zero means the command produced a report/capture; inspect every result's `match`, `mismatch`
or `unavailable` status. A nonzero exit means command/input/storage/integrity failure. No live
measurement or production rollout is implied by the synthetic regression tests.

## Bounded Sina shadow comparison (P25)

The administrator settings modal on `/manage/forecast` offers **Off** and **Shadow comparison only**.
The existing source order still determines whether Sina is enabled. The shipped comparison grant is
Shanghai/Shenzhen only, using the bounded Sina adapter; it grants neither public display ranges nor
settlement. Sina and Yahoo still have no dividend/kind evidence flags or writer grant. Off is the
default. `POST /api/admin/forecast {"fallback":"on"}` is refused with `forecast_bad_fallback`;
effective off/shadow transitions commit with the dedicated `forecast.fallback` audit.

Shadow does not enable evaluation or trigger an idle pass. During a working evaluator pass it copies
at most two unique bodies already accepted from the qualified primary, retaining first/last anchors
and every intervening date/OHLC. Only after primary ledger processing completes does it read the
candidate, with a separate fifteen-second deadline and shared upstream concurrency. It rechecks mode,
enabled membership and primary identity before each sample. An aborted primary pass makes no shadow
calls. Candidate data never enters the settlement planner, cache or source-health counters.

There are two independent limits: two logical candidate reads and four HTTP request attempts per
pass. One Sina observation normally needs snapshot plus history; redirects also consume HTTP budget.
A fifth attempt is blocked before transport and recorded as `shadow_budget`, not a vendor failure.
A genuine fourth-request failure is still a failure. Waiting for a concurrency slot does not bill a
source call. Caller deadline/cancellation and typed refusals are neutral, not source faults.

When shared scheduling defers Sina's history request after a successful snapshot, the next selected
comparison resumes history instead of requesting that snapshot again. The same applies when the
per-pass HTTP cap blocks the second component. This bounded in-memory continuation expires after
six hours without renewal and is invalidated by evaluator mode, fallback mode, source order,
Shanghai civil date or completed-session boundary changes. It holds at most 64 entries and is
independent of display cache. Each real HTTP request is counted in the pass that dispatches it.
Dry mode still requires another Evaluate now action; On mode uses its normal ticker. If prices or
dates no longer agree between the retained snapshot and the new history, the comparison is
`incomplete`; the entry is discarded and no vendor delivery failure or ledger write is recorded.

Sina's newest untrimmed kline must agree with the snapshot date and close. History is never silently
replaced by a display-only degradation; an unproven window beyond the full 800-row tail is refused as
`window_not_covered`. The shared comparator checks integer-fen OHLC, the date union and both anchors.
A missing candidate anchor is a mismatch; unavailable evidence is not agreement. Comparisons are
opportunistic samples, not a representative study, independent exchange corroboration, a feed licence
or Tier 2 writer admission. Use the fixed-panel P24 process for deliberate replay measurements.

The administrator-only `GET /api/admin/forecast` includes `fallback` and `diagnostics`. The latter's
`status` is `empty`, `ready` or `unavailable`; empty/missing/damaged telemetry must never be read as
healthy zeros. `shadow` and `calls`, when readable, are one atomic snapshot of the existing-meta keys
`forecast_state_shadow` and `forecast_state_calls`. A damaged pair is retained and not overwritten.
No raw response body or vendor error text is stored here. Counts include logical primary/adjustment
reads, failures and typed refusals; `shadow_http_attempts` covers candidates only, not primary HTTP.

Retained state has up to fourteen UTC days ending at the latest observation and twenty recent results.
The explicit latest-record timestamp may be old when there is no work or evaluation is off; it is not
a live health certificate. A delayed observation cannot rewind retention or the timestamp. Each result
keeps total differences and at most thirty-two details; truncation never turns a mismatch into a match.
Counter-day dates are UTC; price-bar dates use the instrument timezone. Prices in the difference table
are integer fen. The card shows the active comparison mode separately from retained history.

For `mismatch`, preserve the result/window/anchors and capture deliberate evidence with P24; do not
rewrite settled rows or exclude disagreement. For `no_shadow`/`primary_changed`, inspect enabled source
order. For `shadow_budget`, inspect redirects/request counts rather than extending the budget. For
unavailable diagnostics, investigate retained state and storage logs; do not delete evidence to make
the card green. To stop new candidate calls, set comparison Off; the audit/history remain and primary
evaluation is unchanged. No database change or ledger repair is needed to revert this code. Deployment
and evaluator enablement still require their separate owner approvals and release gates.
