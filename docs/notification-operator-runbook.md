# Notification consolidation: operator and release runbook

This runbook covers the implemented notification foundation. It does not deploy a
Status app, query provider balances, publish an app-market package or change run
queue admission. The external notification API is available for future sources.

Related contracts:

- [Storage decision and reliability](adr/0038-notification-consolidation.md)
- [Management API](notification-management-api.md)
- [Inbox API](notification-inbox-api.md)
- [Service and sandboxed-app publication](notification-publishing-api.md)
- [Email, Slack, raw Webhook and n8n recipes](../web/public/notification-presets/README.md)
- [Implementation and verification record](notification-consolidation-log.md)

## 1. Prepare an upgrade

1. Finish CI for the exact commit to be deployed. Use the repository's normal
   release workflow; this implementation does not create a release automatically.
2. Export a portal backup using the existing backup page. Keep it with the exact
   previous binary/image reference and the infrastructure configuration/keyring
   material required to decrypt saved credentials. Treat the backup as secret.
3. Restore that backup into a separate instance first. Give it a different port
   and database. Disable or isolate its external egress before startup; a backup
   contains real destinations and credentials. Do not point a review instance at
   the production SQLite path or PostgreSQL DSN.
4. Confirm that the source database already satisfies the v0.4.72 baseline. A
   refused baseline must first pass through v0.4.72; do not edit schema products
   or migration markers by hand to bypass startup checks.
5. Start the isolated instance with the new binary. Startup applies every
   pending step in its own transaction, and nothing has to be run first
   ([ADR 0035](adr/0035-migrations-after-a-frozen-baseline.md), amended
   2026-10-24). To review the pending steps beforehand, the optional
   `schema-upgrade` command lists them without changing anything
   ([upgrade notes](forecast-purge-ownership-upgrade.md)). Migration order
   preserves the released prefix `0001` through `0006`, then `0010`, `0009`,
   and appends notification step `0007`. Step IDs are labels, not sort keys.
   Step `0007` creates eleven notification tables under the ADR 0035
   transaction and ledger without changing the frozen baseline.
6. Open Manage > Notifications. Existing SMTP, forecast Email/Slack and raw
   receivers appear as referenced channels. Compare their enabled states and
   destination hosts with the original settings pages. Existing configuration
   remains authoritative; do not create duplicate rules to reproduce automatic
   legacy deliveries.
7. Verify the acceptance checklist below before upgrading production. Stop all
   production writers, retain and verify a backup made with the old image, then
   repeat the offline inspection/apply/inspection sequence before startup.
   A normal upgrade preserves operational dispatch state; a *backup restore*
   pauses it. Rollback restores the old backup into a clean target using the
   retained old image; never remove ledger entries or open the upgraded database
   with the old image.

There is no backfill of historical completion consent or old notifications.
Only persisted future consent is recoverable. Historical business records are
not treated as permission to send a new message.

## 2. Configure one new source safely

1. Keep the existing Email page for SMTP transport setup. A notification Email
   channel references that transport; it does not define a second SMTP server.
2. In Manage > Notifications > Channels, add the desired destination. Use Portal
   inbox for personal on-site messages; Email for fixed or account addresses;
   Slack for a supported incoming Webhook; raw Webhook for signed machine events.
3. For an external channel, use Send test with a deliberate test recipient. An
   explicit test can exercise a paused channel. It does not resume restored work
   or fan out through normal event subscriptions. Inspect the recorded result.
4. In Routing rules, select an exact event and the channel. For personal delivery,
   select accounts and/or OUs. OU grants include descendants. An empty audience
   notifies nobody. Creator mode selects the source's creator where defined.
   Fixed external destinations are administrator-controlled egress; select them
   only when the receiver may receive that event's contents.
5. A grant selects recipients; it does not grant access to a report/job or other
   protected resource. Capture and current authority both constrain personal
   delivery. Moving a user out of an OU can hide old inbox items and their counts.
   Adding a user later does not reveal old events they never originally received.
6. Optional preferences affect future optional notifications. Mandatory rules
   may override a preference, but cannot override resource access. Reset and
   signup verification mail never depend on these preferences or routing rules.
7. Use Message preview before enabling production delivery. A sample preview
   does not send and does not invent real recipients. A stored-event preview
   evaluates current rule candidates; it is not evidence of past delivery.
8. Generate a real disposable occurrence in the isolated instance and verify
   history or the intended account's inbox. For external publication, follow the
   dedicated credential procedure in the publishing API guide. Ordinary report
   ingestion tokens cannot publish notifications.

Legacy raw Webhook subscriptions already produce their original payloads. Do not
add another managed rule to the same physical receiver unless duplicate external
messages are deliberate. Raw receiver signing covers the exact body bytes.
Receivers must durably deduplicate; the bundled n8n example only verifies a
signature and requires deployment-specific durable deduplication before actions.

## 3. Interpret delivery evidence

| State | Meaning and next action |
| --- | --- |
| Pending | Captured durably, not yet attempted; inspect pause and capacity |
| Sending | Leased to a worker; inspect after the bounded attempt/lease window |
| Waiting to retry | A known transient failure; workers own the next attempt |
| Accepted | SMTP/HTTP peer accepted the request; this does not prove a human read it or a downstream workflow finished |
| Uncertain | A request may have been accepted; automatic resend stops to avoid a duplicate |
| Failed / expired / cancelled | Closed delivery; inspect its reason and current configuration before considering replay |

Automatic attempts are bounded: four operational workers, one active attempt per
channel, a 90-second lease, a 30-second full attempt deadline and a 24-hour delivery
lifetime. New deliveries allow five attempts; referenced raw deliveries retain
three. Transient retries use backoff/jitter. Authentication mail uses a separate
two-worker, 32-waiting-item executor. Manual tests have a separate two-call bound per server process.
Queue limits are 10,000 active deliveries globally and 1,000 per channel. Capture
also limits one event's payload/fan-out and rolls back rather than keeping a
partial set. External sources allow 120 new occurrences per rolling hour.

A notification failure does not reverse a completed business transaction. Report
and forecast producers capture after business commit and log a capture failure;
they do not promise a universal transactional outbox for all historical sources.
Job completion additionally persists immutable completion evidence in its business
transaction so recovery can capture its notification without rerunning analysis.
Do not claim exactly-once external delivery. Replay and network uncertainty can
produce duplicates, so downstream actions still require idempotency.

### Resolve uncertainty or replay

1. Inspect the delivery detail, original event, channel and attempt timestamps.
2. Check the receiver's own durable evidence using its event/delivery correlation
   identifiers. Do not infer success from a timeout or from a raw receiver's
   historical `last_delivered_at`, which actually means last attempt.
3. If no further delivery is needed, dismiss the uncertain result. The original
   uncertain attempt remains as evidence.
4. If resending is deliberate, choose Replay. Accepted/uncertain history requires
   explicit duplicate-risk confirmation and an explanation. The request key is
   idempotent; replay creates a linked new delivery generation with a new lifetime.
5. Replay sends the stored notification. It does not restart an analysis, call a
   provider, regenerate a report, re-check a balance or create another source event.
   Current destination, rule and resource checks still apply. Deleted-account
   deliveries cannot be replayed to a replacement account with the same username.

## 4. Restore, resume and roll back

1. Restore only with a compatible binary following the existing backup runbook.
   Approved older dumps without `0007` are supported; dumps claiming the step must
   contain its declared products. Malformed/newer migration claims are refused
   before destructive restore work.
2. A successful restore pauses operational dispatch and retention cleanup. Old
   worker leases are invalidated. Started attempts without a confirmed result are
   classified uncertain, rather than automatically sent again.
3. Review destinations, recipients, waiting work, uncertain attempts and current
   rule grants. Explicit tests remain available while dispatch is paused. Login,
   password reset and signup verification remain independent of this pause.
4. Refresh the overview, then resume against that queue review snapshot. If the
   queue changed, refresh and review again. Resume does not re-send uncertain work.
5. A schema rollback is not an in-place downgrade. An older binary refuses an
   unknown migration marker. Stop writers and restore the pre-upgrade backup into
   a database compatible with the previous release, then run that release. Preserve
   the upgraded database for investigation. Do not delete migration markers or
   notification tables to make an older binary accept newer data.

Backups contain destination secrets and notification content. Use the existing
protected backup handling; exports and recipe downloads must not include actual
tokens, SMTP passwords or signing secrets.

## 5. Retention and troubleshooting

Automatic retention is disabled by default. Opening the retention editor does not
enable it. If desired, explicitly enable a reviewed policy (suggested starting
values: 30 days closed delivery history, 90 days inbox; allowed range 1–3650 days).
Hourly bounded cleanup preserves active/uncertain work, recent replay families
and bodies still referenced by inbox rows. Source identity metadata/digests remain
so a late source retry cannot recreate a compacted event. This is not a promise
to remove every source identifier from storage after a fixed number of days.

| Symptom | Check |
| --- | --- |
| No notification | Original opt-in, exact event selector, enabled channel/rule, audience, resource permission, optional preference and source capture logs |
| Waiting work | Global/channel pause, expiry, invalidated destination revision and worker processing logs |
| Configuration conflict | Reload current revision; do not overwrite another administrator's edits |
| Restore will not resume | Refresh and review the changed queue fingerprint |
| HTTP publication 401 | Dedicated `notify:<publisher>` credential, expiry/revocation; installed app scope and current administrator issuer |
| HTTP publication 409 | Same occurrence ID was reused with different content; retry the original body or use a new real occurrence |
| HTTP publication 429 | Source quota or global/channel capacity; inspect queue, then retry the same occurrence ID |
| Inbox count differs after OU change | Current authorization hides revoked items, including counts and mutations |
| Forecast test not ready | Both configured channels must succeed against an unchanged settings snapshot |
| n8n rejects signature | Raw body binary property, exact UTF-8 bytes, header format and trusted secret environment access; do not reserialize JSON |

Transport errors recorded in common history use bounded reason codes. Do not add
secrets, destination paths or response bodies to logs to diagnose a failure.

## 6. Acceptance and source accounting

Release gates: Go build/vet/staticcheck and full tests; real PostgreSQL tests;
notification/app-token race tests; frontend typecheck/full tests/build; diff checks;
backup/migration/restore fixtures; actual management and inbox browser acceptance.
The implementation log records actual results and test limitations. A live n8n
server, production SMTP and production Slack are deployment checks, not claims
made by local fixture tests.

| Source | Final path / intentional compatibility |
| --- | --- |
| Password reset | Bounded authentication executor -> shared SMTP one-attempt adapter; no durable secret-bearing inbox/history |
| Signup verification | Same independent authentication path and existing token lifetime |
| Job terminal/cancel | Business-transaction completion evidence -> durable event and opt-in consumption -> workers/inbox; recovery never reruns the provider |
| Forecast watchdog | Postcommit `fireEvent` -> one durable event with Email/Slack/raw/rule fan-out |
| Prediction store failure | Same durable capture; successful ingest response is preserved |
| Prediction rejection | Postcommit durable raw/rule event with existing vocabulary |
| Machine/manual report ingest/edit | Postcommit durable raw/rule event with compatible original raw body |
| Service/app publication | Credential-derived namespace -> atomic event/fan-out capture -> same workers/inbox |
| New channel test | Separately bounded synchronous single attempt; common event/attempt history; no ordinary subscription fan-out |
| Original SMTP test | Compatibility facade retains synchronous SMTP-specific message/response; shared SMTP adapter and manual-test bound; does not create common operational history |
| Original raw receiver test | Compatibility facade retains signed `ping` body/response and latest-attempt status; shared HTTP primitive and manual-test bound; does not create a normal subscription event |
| Original forecast test | Compatibility facade retains both-channel readiness result and existing forecast audit/state; frozen settings, shared adapters and manual-test bound; no operational event fan-out |

The old test facades intentionally retain their transport-specific response and
readiness contracts. Use the new channel test when common attempt history is
required. No automatic producer retains the old per-receiver goroutine or Slack
retry loop; durable workers own operational retries.

## Reader-facing release-note draft

- Added unified notification channels, routing rules and delivery history.
- Added account/OU-aware personal inbox, unread indicator and optional preferences.
- Existing completion, prediction and Webhook notifications use durable delivery;
  password reset and signup mail remain independent.
- Added deliberate uncertainty review, linked replay and paused restore recovery.
- Added Slack/Email/Webhook setup recipes and an inactive signed n8n example.
- Added scoped service and installed-app notification publication for future sources.
- Status monitoring, provider balance checks and a public status page are separate
  future work.

This is a draft in the worktree, not published release metadata. Publish final
reader notes through the normal GitHub Release workflow after acceptance.
