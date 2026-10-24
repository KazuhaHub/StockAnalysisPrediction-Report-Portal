# Notification storage decision proposal

## Approval boundary

The owner approved Option A and its principles on 2026-10-21: dedicated records,
references to existing configuration, ADR 0035 additions without a baseline
change, deletion cleanup for reused account identities, post-commit capture and
paused restored delivery. Exact fields, migration 0007 and the reliability
contract are now recorded in ADR 0038 and implemented under that approval. No
production database was converted; no additional approval is needed for the same
scope.
The standing `AGENTS.md` instruction requires an owner structure decision before
any table, column or migration change. The baseline instruction and ADR 0035 must
also be reconciled explicitly before choosing how additions are declared.

## Choices

**Option A, recommended: dedicated notification records with references to
existing settings and receivers.** Separate operational events, rules, recipients,
deliveries and attempts from business records. Reuse the existing SMTP settings
and raw receiver configuration initially through one configuration service.
This limits credential movement and preserves old API behavior during adoption.

**Option B: extend the Webhook records into the canonical channel model.** Add
adapter type and structured destination configuration to existing receivers, then
add event, recipient and delivery storage around them. This reduces duplicated
receiver concepts but gives a historically raw-Webhooks table unrelated mail and
inbox responsibilities and increases legacy read/write compatibility work.

Neither option stores a durable queue as a large JSON value in `meta`, modifies
report identity, adds feature settings to `config.yaml`, or implements Status.

## Option A object layout for review

Names below are proposed and can change with the owner's decision. Exact SQL
types, foreign keys and index declarations follow approval and dual-driver tests.

| Proposed object | Fields and responsibility | Key integrity rule |
| --- | --- | --- |
| `notification_events` | Opaque event ID, source, event type, payload version, UTC occurrence time, resource reference, audience ceiling, permitted payload and optional raw bytes | Unique stable source occurrence; distinct report updates remain distinct |
| `notification_channels` | Opaque channel ID, adapter, label, configuration reference, enabled/paused state, revision | Reference current SMTP settings or an existing raw receiver; new Slack destinations use write-only secret storage |
| `notification_routes` | Route ID, event selector, channel ID, recipient strategy, enabled state and revision | No destination address supplied by an event payload |
| `notification_route_grants` | Route ID and explicit user/OU principal | Empty explicit scope grants nobody; deleted principals are swept |
| `notification_deliveries` | Delivery ID, event/route/channel IDs, recipient identity or destination reference, state, expiry, attempt count, retry time, lease token and revision snapshots | Unique event/route/recipient/generation; compare-and-swap lease completion |
| `notification_attempts` | Attempt ID, delivery ID, start/end, classified result, protocol code, acceptance evidence and sanitized diagnostic | Preserve possible upstream acceptance after local failure; never store secrets or auth links |
| `notification_recipients` | Event ID, account identity, read/dismiss times | Original candidates intersected with current resource permission; unique per event/account |
| `notification_preferences` | Account identity, registered event category, optional channel choice and enabled flag | Operational preferences cannot suppress authentication mail |
| `job_notification_intents` | Job ID, explicit submitting account, intent creation and consumed/canceled state | Future opt-in is durable and one-shot; no historical inference |
| `notification_source_cycles` | Source kind/resource, generation, cycle state and stable occurrence reference | A retry under the same job ID can complete again without regenerating consumed mail intent |
| `notification_config_mappings` | Legacy source key or receiver ID, canonical reference and adoption revision | One configuration source of truth; no independently editable copies |

The implementation review must decide whether completion cycles and job intent
can share one source-state object. Do not create a second event bus or use report
ID alone as the occurrence key. Source bookkeeping cannot cause a paid job to be
executed again.

Usernames are currently reusable. Recipient and preference references must be
deleted with an account before its username can be reused; queued deliveries
must be canceled rather than inherited. The owner may instead select a dedicated
immutable notification identity. Do not add an account-ID column unilaterally.

## Configuration mapping

- Site SMTP settings remain the one shared SMTP transport. Channel records
  reference them instead of duplicating the password per recipient.
- Existing raw receivers keep their IDs, URL, subscriptions, signing secret and
  active state during the initial adoption. A canonical channel references them.
- Forecast recipient settings and Slack URL remain authoritative until a tested
  atomic mapping switches both old and new APIs to the same canonical service.
- Credential encryption uses an approved purpose binding in the existing secret
  facility; plaintext legacy credentials are not automatically rewritten without
  an explicit adoption decision.
- No settings save emits messages. Manual tests invoke the selected adapter and
  record the tested configuration revision.

## Persistence guarantees and lifecycle

The initial recommendation is post-business-commit capture. A successful capture
commits the event and resumable fan-out intent. A failure does not turn report
ingest or completed execution into a business failure. There remains a documented
crash gap before capture; selected job reconciliation may recover notification
intent without executing work. An atomic business outbox needs its own explicit
decision because capture failure could otherwise roll back business state.

Network work happens outside transactions. Due claims use a lease with a fencing
token; an expired token cannot overwrite a newer claim. A possibly sent attempt
becomes uncertain on recovery and is not blindly treated as unsent. Legacy retry
profiles remain separately identified until their compatibility policy is chosen.

Proposed initial retention is 30 days for closed operational attempts/deliveries
and 90 days for inbox items. Pending work expires under a selected source policy;
cleanup never deletes live leases or deduplication evidence still needed by them.
These retention values require owner confirmation.

Backups must include all approved notification objects and configuration mappings.
Restore starts operational dispatch paused, invalidates leases, and requires
explicit review before resuming old pending work, which may already have reached
external recipients after the backup. Authentication tokens are excluded from
notification payload storage.

## Recorded decision and remaining implementation details

1. Option A is selected. Keep job completion occurrence separate from one-shot
   consent; decide whether their source state shares a physical table during the
   exact-schema design.
2. Use the ADR 0035 migration ledger and leave the v0.4.72 baseline unchanged. The
   owner explicitly selected this resolution of the older instruction wording.
3. Use deletion cleanup for reusable account references, without adding a user ID
   column. Test queued deliveries as well as inbox and preference deletion.
4. Post-commit capture, uncertain-delivery review and paused restore are accepted
   principles. Retention defaults remain proposed tuning values to expose and
   test under the selected policy.

Implementation evidence and remaining acceptance work are tracked in
`notification-consolidation-log.md`; ADR 0038 is the exact-schema decision record.
