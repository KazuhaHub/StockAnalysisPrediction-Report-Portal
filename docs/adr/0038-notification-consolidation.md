# ADR 0038 — Durable operational notifications and isolated authentication mail

## Status

Accepted storage direction, implementation in progress. 2026-10-21. The owner
approved Option A in `docs/notification-storage-proposal.md`: dedicated records,
existing configuration references, post-commit event capture, reusable-account
cleanup, and paused delivery after restoring a backup. This ADR specifies that
decision; it does not declare source adoption or the user interface complete.

## Context

Authentication email, task completion email, forecast Email/Slack and raw Webhooks
have different lifetimes. Previously, completion consent lived only in memory,
raw receivers each spawned a goroutine, and Slack and Webhooks separately owned
retry. A server restart could lose intent or interrupt an unknown external result.
There was no common delivery history or user inbox.

The system is core infrastructure. A future Status app can consume it through
authorized extension APIs. This change does not implement monitoring, balances,
`/status`, arbitrary plugin execution, or changes to run admission and scheduling.
The extension boundary in ADR 0002 remains in force.

## Decision

### Authentication mail has its own bounded executor

Password reset and signup verification use two active workers and capacity for
32 waiting tasks. Admission is nonblocking; overload produces a fixed internal
diagnostic. Public recovery responses do not reveal whether an account exists,
whether the executor admitted a message, or whether SMTP accepted it. Existing
token expiry, eligibility and throttling still apply.

Authentication messages and their bearer links never enter operational event,
attempt, replay or inbox storage. Shutdown cancels active protocol operations.
Operational delivery cannot consume authentication worker capacity.

### Migration 0007, without a new baseline

The accepted v0.4.72 baseline remains byte-for-byte unchanged. Migration 0007 is
an ordered ADR 0035 step with portable SQLite/Postgres DDL, product verification
and backup enumeration. It creates these objects:

| Table | Identity and important fields |
| --- | --- |
| `notification_events` | Opaque `id`; unique `source_key`; `event_type`; exact JSON `payload`; resource type/id; occurrence and optional expiry instants |
| `notification_channels` | Opaque `id`; adapter/label; `legacy_ref`; non-secret JSON config; purpose-sealed secret; enabled/paused flags; monotonic revision; creation instant |
| `notification_routes` | Opaque `id`; channel id; registered event selectors; recipient strategy; enabled/mandatory flags; monotonic revision |
| `notification_route_grants` | Composite `(route_id,principal)`; existing account and OU principal encoding |
| `notification_deliveries` | Opaque `id`; event/channel/route/account references; state, attempt count, due/expiry instants; lease token/expiry; sending flag; captured config revisions; generation/replay reference; unique nullable replay key; sanitized outcome and protocol code |
| `notification_attempts` | Opaque `id`; delivery/lease references; start/end instants; classified outcome/error code and protocol code |
| `notification_recipients` | Opaque `id`; unique `(event_id,username)`; read/dismiss instants |
| `notification_preferences` | Composite `(username,event_type)`; enabled flag |
| `job_notification_intents` | Job id; submitting account; creation instant and one-shot consumption instant |
| `notification_source_cycles` | Unique source key; generation; stable occurrence id; pending completion snapshot |
| `notification_config_mappings` | Unique legacy key to canonical channel reference |

IDs are text except existing numeric job references. Internal instants are Unix
milliseconds in `BIGINT`; HTTP DTOs must explicitly render UTC RFC3339 instants.
Boolean flags use portable integer storage. As with existing application-owned
relations, lifecycle operations perform explicit transactional cleanup rather
than introducing cross-baseline foreign keys.

The delivery uniqueness constraint is
`(event_id,channel_id,route_id,username,generation)`. Route and account references
use empty strings when absent. Initial delivery generation is zero. A replay is
a new, linked generation; it must not mutate the original attempt or invoke a
paid business operation.

Indexes cover due claims `(state,next_at,lease_until,created_at)`, account inbox
`(username,dismissed_at,read_at,event_id)`, attempt history
`(delivery_id,started_at)`, and event chronology `(created_at,id)`.

### Capture after business success

An event and its bounded delivery/inbox fan-out commit in one transaction. A
source occurrence key, rather than the report id, identifies a notification.
Reusing that key with a different payload, event type or resource is an error.
The legacy JSON body stays unchanged; event metadata is not wrapped around it.

Limits are 64 KiB per event, 1,000 outgoing deliveries and 10,000 inbox candidates
per capture, 10,000 active deliveries globally and 1,000 per channel. Overload
rolls back the entire capture and reports a fixed diagnostic; it cannot reverse
a successful report ingest or restart a run. Postgres serializes the short
capacity/capture transaction with advisory key 780714. No network operation runs
inside that transaction.

A crash between a report commit and post-commit capture remains a documented
loss window. This decision does not claim a transactional report outbox or
exactly-once external delivery. A report update needs its own occurrence; its
reused report id alone is insufficient.

### Completion evidence is separate from consent and event capture

The task submission transaction records future explicit opt-in together with the
job and items. Existing jobs gain no inferred consent.

The terminal job transaction records completion evidence in the source-cycle
table. A pointer `job:<id>` carries a generation and occurrence token. A separate
immutable `job-completion:<job-id>:<token>` row stores the terminal job snapshot. Manual
retry advances the pointer inside the existing retry transaction; a second
completion within the same clock second still has a different token. Repeated
completion callbacks share one token.

After commit, a coordinator captures the event and its raw receiver deliveries,
plus the opted-in submitter's delivery/inbox entry. It consumes consent and clears
the pending snapshot in that same capture transaction. A failed capture leaves
both available for retry. A restarted coordinator scans at most 100 pending
snapshots per pass. It never calls a workflow provider or re-executes a job.
Durable evidence is recorded as part of the business transition; delivery queue
capacity and external transport availability cannot prevent that transition.

### A fixed pool and fenced leases own retry

Four operational workers poll SQL every 250 ms. There is at most one leased
delivery per channel. Postgres serializes only the short claim transaction with
advisory key 780715 so two different pending rows cannot both observe an idle
channel. SQLite's single write connection gives the same ordering.

A lease lasts 90 seconds. A worker conditionally records a sending attempt before
network I/O, and can complete it only while its matching token is still live.
The attempt has a 30-second overall deadline; HTTP also has a 15-second client
deadline. Recovery runs every 15 seconds. An expired claim which never started
an attempt is pending again; an attempt that may have sent becomes `uncertain`.
Expired pending work becomes `expired`; default initial delivery expiry is
24 hours after occurrence. A stale worker cannot overwrite recovery or a new
owner's result.

Adapters make one network attempt. Confirmed acceptance is `accepted` even if
SMTP QUIT later fails. A lost final SMTP reply or a possibly transmitted HTTP
request is uncertain and is not automatically resent. Explicit transient
rejections may retry with exponential backoff, deterministic jitter and
Retry-After. Initial limits are five total attempts for new channels and three
for referenced legacy raw/Slack channels. Changing the legacy retry timing and
ambiguous-result policy is intentional: uncertainty is visible for review
instead of silently risking duplicate delivery.

No worker sleeps through backoff; SQL stores its due time. Paused channels wait
without adding attempts. Panic recovery leaves the lease for normal recovery.
Diagnostics use fixed error codes, not destination-bearing transport errors.

### Current authority is checked at use

Existing SMTP, forecast destinations and raw receivers remain the authoritative
configuration during adoption. Canonical references do not copy their credentials
into independently editable fields. A fingerprint includes effective legacy
settings, so old configuration APIs cannot silently redirect queued content.
Destination changes invalidate captured delivery revisions.

Routing grants select candidates and never grant permission to the resource.
Before sending, verify active/nonexpired account, current resource authority,
current route revision and grants, and preference. Mandatory rules may override
an operational preference, but never a revoked resource permission or audience.
Inbox APIs must repeat authorization on reads and mutations.

Deleting an account removes its inbox, preferences, consent and account grants,
and cancels active deliveries. Capture locks candidate account rows on Postgres
so deletion cannot be followed by orphan recipient insertion. Recreating a
username does not inherit the old account's notifications. Deleting an OU also
removes route grants referencing it.

### Restore requires review

All new tables are included in logical backups. An older supported dump can
restore with empty notification tables. Restore sets operational dispatch paused,
invalidates leased deliveries and classifies unfinished attempts uncertain. An
operator must review and explicitly resume; a backup cannot establish which
messages reached external recipients after its snapshot. Authentication email
does not use this restored operational queue.

Retention settings, the operator review API, source adoption beyond job
completion, and the management/inbox interface remain work packages tracked in
`docs/notification-consolidation-log.md`. No automatic deletion is enabled before
its policy and review behavior have tests.

## Consequences

SQLite and Postgres share one queue contract and the same protocol adapters.
State is inspectable across restarts, successful business actions remain
independent of external recipients, and replay has a separate identity. Operators
must distinguish confirmed acceptance from delivery to a person's inbox, and
uncertainty from a definite failure. The rollout still needs compatibility APIs,
all remaining producers, interface work, retention and full release acceptance.


### Implementation clarification: compatibility and configuration tests

All operational producers now use durable capture. Raw consumers additionally
receive `X-Report-Portal-Event-ID` and `X-Report-Portal-Delivery-ID`; payload bytes,
the existing event header and the signature base remain unchanged. Forecast
producers capture once for all configured outputs, rather than launching a
second sender beside raw delivery.

Explicit forecast tests freeze SMTP and Slack settings, send through one-attempt
adapters, and stamp readiness only if the captured configuration is still current.
SMTP/forecast save facades and the final test comparison use short transaction
exclusion with PostgreSQL advisory key 780716. Global and per-transport monotonic
revision counters live in `meta`, avoiding a schema change and detecting a change
back to earlier values. A stale successful test receives HTTP 409. Network I/O
never holds the configuration transaction.

Administrative channel/rule APIs, history/review/replay, generic authorized
extension publication, retention, management pages and the inbox interface remain
separate tracked implementation packages. This clarification is not their
completion claim.


### Implementation clarification: management and personal task ownership

The administrator session surface now exposes channel/rule CRUD, preview, bounded
single-attempt tests, cursor-based delivery history, evidence detail, uncertain
result review, idempotent replay and reviewed dispatch resume. The HTTP contract
is documented in `notification-management-api.md`. A replay is a new delivery
with a unique request key and a linked generation; accepted or uncertain evidence
requires duplicate confirmation. Network delivery already in progress cannot be
undone by channel/account deletion, so its unfinished attempt becomes uncertain
and is fenced. Closed records for a deleted account are non-replayable as well.

Submission stores the creator's notification-account generation in its source
pointer; terminal evidence carries it into the internal job resource reference.
Resource checks compare that generation before selecting or sending to a creator.
This prevents an explicit retry of an old job after username reuse from notifying
the replacement account. It also protects a completion snapshot captured only
after deletion. Historical jobs without ownership evidence gain no inferred
personal audience. No user column or additional table was introduced.

Administrative edit revision and delivery revision have separate purposes.
Renaming or pausing a channel preserves waiting messages; replacing a destination,
recipients or enabled state invalidates old transport intent. Forecast readiness
additionally includes enabled/pause state and rejects stale configuration tests.
Rule saves and identity deletion use the existing configuration lock so grants
cannot be inserted after deletion's sweep. Reads fetch route metadata and grants
from one snapshot to avoid mixing a newer grant with an older revision.

The first management page is now wired at `/manage/notifications`; visual review,
software presets, the generic extension publisher, inbox and retention remain
tracked work and are not declared complete by these backend decisions.


### Implementation clarification: personal inbox

Personal notification endpoints now expose authorized list/count/read/dismiss,
signed five-minute read-all snapshots and registered event-category preferences.
The contract is in `notification-inbox-api.md`. Original recipient membership is
intersected with current resource authority and current inbox rule selection.
The original task creator's consumed explicit consent remains an independent
personal route, bound to its submitting account generation. Revoked resources,
roles or OU membership disappear from content and counts.

Read-all snapshots name concrete recipient IDs and the account generation;
application rechecks current authority. Later arrivals remain unread and tokens
cannot cross accounts, survive username replacement or tolerate signature
changes. Metadata queries have a 10,000-candidate bound, per-request authority
caches and page-only body loading. UI state includes a header count and personal
page polling only while visible. Retention and visual acceptance remain separate
outstanding packages rather than assumptions established by these API tests.


### Implementation clarification: explicit retention policy

Retention is opt-in and managed in the UI/DB, with suggested initial windows of
30 days for closed delivery history and 90 days for inbox rows. It adds no DDL.
The bounded hourly pass pauses alongside dispatch, preserves every live or
uncertain delivery and retains a replay family until all members are old and
closed. Inbox-referenced bodies survive history deletion. Orphan old bodies can
then be compacted; their event identity metadata and a payload digest remain in
the source-state table so capture cannot recreate the source occurrence. This
policy intentionally does not erase deduplication tombstones or account-generation
cursors. The API contract and exact limits are in `notification-management-api.md`.


### Implementation clarification: authorized external publication

`POST /api/v1/notifications` is a narrowly scoped publication surface, separate
from administrator configuration and personal inbox access. Service credentials
require `notify:<publisher>`; ordinary `all`, `ingest` and `query` tokens do not
authorize publication. Installed apps declare `notify` and use the existing
sandbox bridge. Only an administrator issuer can receive that app capability;
each request rechecks installation, declared scope and current issuer identity,
session revision, activation, expiry and administrator role.

Publisher identity and event namespace derive from the credential. A bounded,
strict request contains only an occurrence ID, title, message and severity. It
cannot set recipients, resource authority, native event names, channels or URLs.
Administrator rules determine fan-out. Stable occurrence IDs deduplicate matching
content and reject conflicting content; a serialized rolling source quota bounds
new events. No background plugin code runs inside the portal. The full contract
and independent-service guidance are in `notification-publishing-api.md`.

### Final source boundaries and acceptance

All automatic operational producers now capture into shared durable delivery.
Authentication messages remain in the separate bounded executor and never put
recovery credentials into common history. Original SMTP/raw/forecast test
facades intentionally keep their synchronous transport-specific result contracts;
they use shared one-attempt adapters and the independent manual-test bound. New
channel tests additionally write common attempt history. Compatibility tests do
not trigger ordinary subscription fan-out or gain automatic retries.

The implementation log and `notification-operator-runbook.md` record final source
accounting, verification, restore review and downgrade procedures. Retention,
recipes, the administrator UI and authorized personal inbox are implemented.
Status probes, balances, public status pages and host-page plugin insertion
remain outside this decision and implementation.
