# Notification management API

Implementation contract for the notification consolidation. These handlers exist
in the current worktree; they are not a release announcement. The completed package and verification record is in
`notification-consolidation-log.md`; deployment and recovery procedures are in
`notification-operator-runbook.md`.

## Authentication and data boundaries

Every route below is under `/api/admin/notifications`, accepts the existing
cookie session, requires an administrator, and passes the existing forced
enrollment gate. These are not iframe bridge endpoints and do not accept a
machine token as a substitute for the administrator's session.

All instants returned to a client are UTC RFC3339 strings, with optional
fractional seconds. An empty string means no timestamp has been recorded. Opaque
event, channel, rule, attempt and delivery IDs are strings. Do not parse them as
numbers or derive them from a report or job ID.

Responses never return a signing secret, destination path/query, SMTP password,
sealed ciphertext or worker lease token. A channel response carries `url_host`,
`has_url`, `has_signing_secret` and `available`. The legacy raw receiver endpoint
retains its pre-existing URL response for compatibility; do not use it as the
model for a new integration's settings API.

## Endpoint inventory

| Method and suffix | Purpose |
| --- | --- |
| `GET /` | Channel/rule catalogue, registered events, account/OU principals, dispatch state, queue counts and current review token |
| `POST /channels` | Create a managed Email, Slack, raw Webhook or inbox channel |
| `PUT /channels/{id}` | Update a channel with its current revision |
| `DELETE /channels/{id}` | Delete a managed channel, remove its rules and revoke its outstanding work |
| `POST /channels/{id}/test` | One explicit external test; no subscription fan-out and no automatic retry |
| `POST /routes` | Create an event routing rule |
| `PUT /routes/{id}` | Replace a routing rule with its current revision |
| `DELETE /routes/{id}` | Delete a rule and revoke its outstanding work |
| `POST /preview` | Render a sample message, or inspect current rule recipients for a real stored event; sends nothing |
| `GET /deliveries` | Filtered delivery history with an opaque pagination cursor |
| `GET /deliveries/{id}` | One delivery, its attempt evidence and a plain-text message preview |
| `POST /deliveries/{id}/replay` | Enqueue an idempotent new delivery generation for the stored event |
| `POST /deliveries/{id}/review` | Dismiss an uncertain result without replacing its original attempt evidence |
| `POST /dispatch` | Pause dispatch, or resume against the current queue review token |

The collection endpoint is exactly `/api/admin/notifications`, without a required
trailing slash. The suffix `/` in the table denotes that endpoint.

## Channel creation and editing

Create an Email channel using the existing site SMTP transport:

```json
{
  "label": "Operations email",
  "adapter": "email",
  "recipients": ["ops@example.com"],
  "enabled": true,
  "paused": false
}
```

The response has `ok` and a `channel` object. Save its `id` and `revision`.
Creation starts at revision 1. Updates must include the current revision and a
nonempty label. The adapter cannot change after creation. `enabled` and `paused`
are optional on update and preserve their previous values when omitted.

Slack and raw Webhook inputs accept a write-only `url`. Raw Webhooks additionally
accept `signing_secret`. A blank or omitted secret field preserves the stored
value. `clear_destination` explicitly removes a URL and requires the channel to
be disabled. `clear_signing_secret` explicitly removes raw signing. An Email or
inbox channel cannot accept URL/signing fields.

New URLs and signing keys are sealed together using the existing keyring with a
channel-specific purpose binding. Slack URLs must pass the existing HTTPS host
allow-list. New raw URLs must use HTTP or HTTPS, have a host, and contain neither
userinfo nor a fragment. Raw destinations can be self-hosted; new raw channels
refuse redirects. Referenced legacy receivers retain their redirect behavior.

The management revision protects concurrent edits. A separate delivery revision
inside the non-secret channel configuration distinguishes destination changes
from administrative presentation changes. Pausing, resuming or renaming a channel
does not invalidate waiting messages. Changing recipients, credentials, address
or enabled state invalidates work captured under the old transport configuration.

### Existing configuration remains authoritative

Reserved `legacy:` channels refer to existing settings:

| Reference | Authoritative configuration |
| --- | --- |
| `smtp` | Site SMTP settings; implicit completion consent chooses the submitter's address |
| `forecast-email` | Existing forecast Email recipients plus site SMTP |
| `forecast-slack` | Existing forecast Slack URL |
| `webhook:<numeric-id>` | Existing receiver row and its subscriptions/signing key |

The canonical editor writes these same locations; it does not create independent
copies of their credentials. The old SMTP, forecast and raw receiver interfaces
continue to see the same values. Forecast changes invalidate its verification
stamp. Disabled or paused forecast channels cannot satisfy the readiness test.

Reserved implicit channels cannot be selected by a new routing rule: their
producer already owns subscription/consent selection. Create a managed channel
for a new rule. Reserved SMTP/forecast channels can be disabled or paused rather
than deleted. Deleting a referenced legacy receiver removes the same underlying
receiver through either interface.

## Routing rules

```json
{
  "label": "Operations inbox",
  "channel_id": "<inbox-channel-id>",
  "events": ["forecast.evaluator.unhealthy", "predictions.store_failed"],
  "recipient_mode": "grant",
  "enabled": true,
  "mandatory": false,
  "grants": ["g:12", "u:operator"]
}
```

`events` is an array on save. Stored/listed route objects expose the selected
events as a comma-separated string, matching the store representation. Frontend
bindings distinguish `NotificationRouteInput` from `NotificationRoute` for this
reason. Unknown or duplicate selectors are refused.

Recipient strategies are:

- `grant`: Email or inbox candidates selected by explicit account or OU grants.
  OU grants include their descendants. Empty grants select nobody.
- `creator`: Email or inbox for the job creator; only `batch.job.finished` is a
  valid selector for this strategy.
- `destination`: One fixed administrator-configured Email/Slack/raw destination.
  It does not accept account/OU grants or a mandatory flag. Membership of a shared
  Slack destination is managed in Slack, not by the portal inbox permission check.

A routing grant does not grant access to a report, job or operations data.
Candidate selection intersects current resource authority. Sending repeats the
account, route, permission and preference checks. `mandatory` can override an
operational preference; it cannot override a revoked permission or audience.
Passwords, recovery links and verification links never use this routing system.

Rule updates require `revision`. Deletion takes a JSON body containing the
current revision. A removed account or OU cannot acquire a new grant from a
concurrent stale save: identity deletion and grant writes share exclusion on
Postgres, and save revalidates the principals in its own transaction.

## Explicit tests and preview

An external channel test takes:

```json
{
  "revision": 1,
  "event_type": "report.ingested",
  "to": "operator@example.com"
}
```

`event_type` chooses synthetic message content and defaults to `report.ingested`.
`to` applies only to Email. Without it, fixed channel recipients are used, falling
back to the testing administrator's Email when the channel has no fixed address.
Invalid addresses are refused before recording an attempt.

The test is recorded as `notification.test`, not as a real report/job event.
Only the selected destination receives it. Its worker-independent request has
one attempt and no retry. At most two explicit tests run per server, and a test
cannot run concurrently with another leased delivery for the same channel.
Global/individual pause does not make a selected explicit test resume old work.

An accepted test returns its delivery ID and state. HTTP 502 returns a failed or
uncertain result with the same evidence. A destination change during the test
returns HTTP 409 with `notification_config_changed`; historical evidence remains
truthful about the snapshot that was actually tested. Testing one forecast
channel does not write the two-channel forecast readiness stamp.

`POST /preview` accepts either a registered `event_type` or an existing `event_id`.
A synthetic sample illustrates formatting only and returns empty target arrays.
A real event preview computes current explicit rule targets and original-resource
authority without storing recipients, consuming consent or sending a message.
Legacy producer-owned outputs remain listed separately through channel settings.

## History and replay

History filters are `state`, `channel_id`, `event_type`, `cursor` and `limit`.
The default limit is 50 and the maximum is 100. The ordering is newest creation
instant, then descending opaque ID. Pass `next_cursor` unchanged; do not construct
or parse its contents in the browser. Delivery details omit lease tokens and
credentials and return a plain-text preview rather than executable HTML.

`accepted` means a relay accepted the request; it does not prove that a person
read it. `uncertain` means upstream acceptance cannot be established and automatic
resend has stopped. A deleted destination or account cannot undo an in-flight
request: unfinished attempts are classified uncertain, while unsent work is
canceled. A recreated username cannot inherit or replay the old person's mail.

Replay input:

```json
{
  "request_id": "<one-stable-UUID-for-this-action>",
  "channel_revision": 3,
  "route_revision": 1,
  "confirm_duplicate": true,
  "reason": "Receiver recovered; resend requested"
}
```

Use route revision 0 when the original delivery had no explicit rule. A replay
of accepted/uncertain evidence requires duplicate confirmation. Active work cannot
be replayed. The server checks current channel/rule revisions and current recipient
authority. The worker checks authority and configuration again before sending.

Reusing the same `request_id` for the same original delivery returns the same new
delivery ID, including after a lost HTTP response. Replacing it creates a separate
administrator action; the UI must retain it while retrying that action. The new
delivery points to `replay_of`, has a new generation and a fresh 24-hour delivery
lifetime. It does not rewrite the original attempt, add another business event,
recreate inbox state or re-run a workflow/provider.

Review input for an uncertain result uses `decision: "dismiss"`, a nonempty
`reason`, and the delivery's current `updated_at`. The original attempt stays
uncertain. The delivery becomes canceled with `reviewed_uncertain`, so any later
replay still requires duplicate confirmation. This action cannot mark an unknown
relay result as confirmed acceptance.

## Pausing and restoring dispatch

`POST /dispatch` with `{"paused":true}` stops future normal work. Network work
already in progress can still finish; its acceptance evidence remains recorded.
Authentication mail uses its separate executor and is unaffected.

The response and overview include `counts` and `review_token`. To resume, send
`{"paused":false,"review_token":"<current-token>"}`. The server compares the
token with the current outstanding queue inside a transaction. A changed queue
requires a fresh review and returns HTTP 409. Review does not make an uncertain
attempt safe to resend; those rows still require their own explicit replay.

Logical restore always pauses operational dispatch and invalidates prior leases.
Inspect uncertainty and the pending queue before resuming. A backup cannot prove
which external requests were accepted after its snapshot. This API never changes
the run queue, paid execution state or workflow scheduling.

## Frontend integration

`web/src/api/notifications.ts` contains the typed HTTP bindings and DTOs. It
separates revision-bearing inputs from returned records and keeps instants as
strings. The shared DELETE helper now accepts an optional JSON body for revision
checks; existing callers without a body retain their behavior.

The remaining UI must localize catalogue/state/error labels, keep write-only
inputs blank, retain replay request IDs while retrying, and refresh its overview
after a revision conflict. Do not render preview text as HTML or expose these
administrator bindings to an iframe app.


### Task creator identity

Task notifications bind a resource reference to the account generation captured
at submission (`<job-id>@<generation>` internally). This reference is not a public
report URL and is never an arbitrary browser redirect. Recreating a username
cannot make its new holder the notification owner of old jobs, including jobs
that an administrator explicitly retries later. Jobs from before ownership was
recorded fail closed for new personal creator rules; no historical consent or
identity is inferred.

## Optional retention

The overview now includes `retention` with `enabled`, `history_days` and
`inbox_days`. `PUT /api/admin/notifications/retention` atomically saves those three
fields. Both day counts must be between 1 and 3650. Cleanup defaults to disabled;
30-day closed history and 90-day inbox values are suggested initial settings,
not an automatically enabled deletion policy. The management Retention tab makes
that choice explicit. Saving policy does not immediately delete records or send
messages.

The operational coordinator checks cleanup at most hourly. Global dispatch pause
also pauses cleanup, preserving the evidence an operator is reviewing after a
restore. Each pass removes up to 500 wholly closed, old event delivery families
and 1000 old inbox rows. A family containing any pending, leased, retrying,
uncertain or recent delivery remains intact, including its replay links and
attempt evidence. An operator must review uncertainty before it can age out.

An old event body is compacted only after both configured age windows have passed
and all delivery/inbox references are gone. The event's small identity/type/
resource/time metadata and a SHA-256 payload digest remain as deduplication
records. Capture still recognizes an identical retried source occurrence and
rejects reuse with different content. These tombstones are not expired by this
policy. The compacted body cannot be previewed or replayed because no delivery
or inbox reference survives its compaction.

For setup recipes and the inactive signed n8n receiver example, open the management
Setup Recipes tab. Public, credential-free downloadable files are bundled at
`/notification-presets/README.md` and
`/notification-presets/n8n-signed-receiver.json`. The guide distinguishes Email,
Slack text and raw JSON adapters, explains explicit tests, and states the extra
runtime and durable-idempotency steps needed before a workflow has real effects.
