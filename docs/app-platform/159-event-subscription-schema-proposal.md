# D2 proposal: authorized application event subscriptions

Status: awaiting owner approval. No migration is applied.

## Decision

Add one Portal table in append-only migration `0020`:
`app_platform_event_subscriptions`.

Delivery reuses `app_platform_tasks`; there is no second event queue or global
webhook. The initial closed event catalogue is:

- `report.ingested`, resource `report`, current report ID; requires current
  `reports.read` authority;
- `batch.job.finished`, resource `job`, current owner-scoped job ID; requires
  current `background.run`.

No extension namespace, arbitrary event type, user field, payload filter or
destination is accepted.

## Table

| Column | Type | Rule |
| --- | --- | --- |
| `id` | ID | UUID primary key |
| `installation_id` | ID | deferred `NO ACTION` FK to current installation |
| `app_id`, `publisher_id` | T64 | immutable snapshots |
| `event_type` | T96 | closed catalogue |
| `resource_type` | T64 | closed resource kind |
| `resource_id` | T256 | exact report/job identity |
| `state` | T24 | active, paused, revoked, review_required |
| `generation`, `policy_revision` | R | exact approved tuple |
| `release_sha256` | SHA | exact approved release snapshot |
| `controller_epoch` | ID | exact control epoch |
| `max_inflight` | N | fixed 1..16 |
| `revision` | R | monotonic |
| `created_ms`, `updated_ms` | MS | explicit UTC |

Checks enforce the event/resource catalogue, exact snapshots, bounded counters
and `updated_ms>=created_ms`.

Unique key: `(installation_id,event_type,resource_type,resource_id)`.

Indexes: active subscription due/order by installation, event type/resource and
state/revision.

## Delivery

Task handler `app.event.delivery` is fixed. For each source event and active
subscription, the source transaction creates one task with:

- occurrence `event:<event-id>:<subscription-revision>`;
- exact subscription revision/generation/policy/release/epoch;
- bounded payload containing event ID, event type, resource identity,
  `occurred_ms` and a safe sequence (`occurred_ms,event_id`);
- `expires_ms` no later than the source event retention instant.

The task claim is the subscription admission. Replaying the same source event
touches no second task. Out-of-order delivery carries the sequence; the app
must de-duplicate by event ID and ignore older sequences. A lost response stays
`uncertain` and is never retried automatically.

## Lifecycle, quotas and restore

- Global 10,000, per-app 1,000 and per-installation 256 active tasks include
  event deliveries.
- At most 16 subscriptions per installation and 1,000 globally.
- Disable/uninstall/publisher revocation fences new creation; uninstall deletes
  this installation's subscriptions and cancels invalid tasks.
- Update/repair/generation/policy changes require the subscription to rebind
  explicitly; stale subscriptions become `review_required`.
- Restore pauses all subscriptions as `review_required`; task restore already
  moves nonterminal deliveries to `review_required`.
- Backup/restore includes the table. Older dumps create it empty.

## Acceptance

SQLite and PostgreSQL schema/primary/FK/checks/indexes; source transaction
rollback creates neither event task nor delivery; duplicate occurrence one
task; duplicate/out-of-order/lost acknowledgement; unauthorized resource and
redacted count refusal; per-app flood; disable/uninstall/revoke/update race;
restore review; logical backup round trip.
