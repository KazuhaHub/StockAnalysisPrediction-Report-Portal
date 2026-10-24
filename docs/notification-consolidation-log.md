# Notification consolidation implementation log

## Scope and baseline

Consolidate the existing notification paths before starting a separate Status
project. Do not implement monitoring, provider balances, `/status`, plugin
publication, or changes to run admission, scheduling, or the run analysis hint.

Initial checkout: `1822f638d925e299ccf69b32bc31cbc9886869f1`.
The checkout was clean before W00. No production settings or database were opened
or modified by this inventory.

## Work packages

| Package | State | Evidence or dependency |
| --- | --- | --- |
| W00 inventory and behavior freeze | Complete | Sender inventory, baseline build/package tests and three new characterization tests passed |
| W01 structure and reliability | Complete | Approved migration 0007; exact storage and reliability contract in ADR 0038; baseline unchanged |
| W02 transport extraction | Complete | SMTP/HTTP single-attempt adapters; operational retries owned by durable workers |
| W03 transactional email adoption | Complete | Independent bounded executor; reset and verification paths adopted |
| W04 persistence and workers | Complete | Atomic capture, capacity limits, fixed pool, fenced leases, retries, expiry, paused restore and lifecycle tests |
| W05 job completion adoption | Complete | Durable opt-in, immutable completion evidence, per-retry occurrence, atomic consent consumption and restart recovery |
| W06 forecast and raw Webhook adoption | Complete | All business producers capture once; workers own retry; explicit tests use frozen settings and stale verification is refused |
| W07 configuration and compatibility APIs | Complete | Channel/rule APIs, compatibility settings, history/replay/recovery and scoped service/app publication |
| W08 management UI and presets | Complete | Channels, rules, history, preview and explicit review workflows at `/manage/notifications` |
| W09 inbox and OU routing | Complete | Authorized list/count/mutations, HMAC read-all snapshots, category preferences and visible-tab polling |
| W10 release acceptance | Complete for the worktree | Final automated gates, source accounting, operator runbook, release-note draft and isolated browser acceptance passed; no production deployment |

## W00 sender inventory

Paths are relative to the repository root. Tests and adapters are not independent
business producers. Repeat the search before declaring the consolidation complete:

```sh
GOWORK=off go build ./...
rg -n --glob '*.go' 'sendEmail\(|notifyJobDone\(|forecastAlert\(|fireEvent\(|webhook\.Deliver\(|slackPost\(' internal cmd
```

| Producer or entry point | Current path | Compatibility requirement |
| --- | --- | --- |
| Password reset | `password_reset.go`: `apiForgotPassword` -> `sendResetEmail` -> `sendEmail` | Keep CAPTCHA, eligibility, account throttling, public response invariance, asynchronous send and one-hour links |
| Signup verification | `register.go`: `apiRegister` -> `sendVerifyEmail` -> `sendEmail` | Keep disabled account until verification and 24-hour token validity |
| Job completion | `batch_api.go` captures `notify`; `batch_run.go`: `scheduleTick` and `finalizeJob` -> `notifyJobDone` | Both terminal paths publish after the scheduler lock; one-shot opt-in selects the submitter |
| Forecast watchdog | `forecast_ops.go`: `emitForecastAlert` -> `fireEvent` plus asynchronous `forecastAlert` | Keep reason suppression; one raw event and separate configured Email/Slack outputs |
| Prediction store failure | `apiv1.go` after the second failed prediction insert | Keep successful report ingest response; raw event plus asynchronous Email/Slack alerts |
| Prediction rejection | `apiv1.go` -> `fireEvent` | Keep validation detail fields and event name |
| Machine report ingest | `apiv1.go` -> `fireEvent` | Keep `report.ingested`, numeric legacy ID and existing field shape |
| Manual report create/edit | `report_edit_api.go`: `announceReport` -> `fireEvent` | Same event vocabulary as machine ingest; no invented delete event |
| SMTP test | `email.go`: `apiEmailTest` -> `sendEmail` | Explicit synchronous test using saved configuration |
| Raw receiver test | `webhook_api.go`: `apiWebhookTest` -> `webhook.Deliver` | Explicit signed `ping`; no normal subscription fan-out |
| Forecast test | `forecast_admin_api.go`: `apiForecastAlertTest` -> `forecastAlert` | Email and Slack both succeed before writing readiness stamp |

Shared mechanisms currently remain separate:

- `email.go` builds SMTP configuration from `meta` and delegates to
  `internal/mail.Config.Send`. `mailFn` is a test seam.
- `internal/mail` sends MIME mail with implicit TLS, STARTTLS or plain SMTP.
- `forecast_alert.go` constructs Email/Slack text, runs retries for Slack and
  records outcomes in forecast alert state.
- `webhook_run.go` marshals the legacy payload once and starts a goroutine for
  each matching active receiver. It owns raw delivery retries.
- `internal/webhook.Deliver` performs one signed HTTP POST. It does not own retry.
- `webhook_store.go` owns subscriptions and latest-attempt state.

The event catalogue is `report.ingested`, `batch.job.finished`,
`predictions.rejected`, `predictions.store_failed`, and
`forecast.evaluator.unhealthy`. Test events are not normal subscriptions.

## Behaviors to protect before extraction

| Behavior | Existing evidence | Additional W00 coverage |
| --- | --- | --- |
| Recovery remains asynchronous and non-enumerating | `TestForgotPassword`, forged-host and eligibility tests | Block the mail sender and compare known/unknown HTTP responses |
| Completion mail requires one-shot intent | `TestNotifyJobDone` | Concurrent callbacks and sender failure consume only one intent |
| Legacy body and signing remain compatible | `TestFireEventDeliversToSubscribers`, Webhook primitive tests | Exact bytes including an ID above JavaScript's safe integer range; no inserted envelope |
| Raw 4xx response is terminal | Existing delivery policy | Confirm one request and recorded failed attempt |
| Forecast requires both channels | `TestAlertTestOKOnlyWhenBothSucceed`, `TestAlertTestFailsWhenSlackFails` | Retain existing tests; no duplicate replacement |
| Alert failure does not fail ingest | Forecast store-failure and panic tests | Retain existing tests |
| Credentials stay write-only | Email settings and Slack host/redaction tests | Retain existing tests |

W00 freezes correct behavior with passing characterization tests. Future bug fixes
must first add failing tests for their intended correction. This package does not
claim to fix SMTP timeouts, persistence, test/configuration races or worker recovery.

## Confirmed gaps and follow-up ownership

1. `jobNotify` is in-memory and uses `LoadAndDelete`; restart loses consent and a
   failed send consumes it. W05 must preserve one-shot consent while persisting
   future intents. It must not infer historical consent.
2. Explicit retry can reuse a job ID. A job ID alone cannot identify a completion
   occurrence. W01 must choose durable cycle evidence independently of mail intent.
3. SMTP currently bounds dial only; protocol I/O may stall. W02 adds cancellation
   and a complete-attempt deadline.
4. SMTP currently returns a QUIT error after successful DATA acceptance. W02 must
   retain evidence of accepted mail instead of encouraging a duplicate resend.
5. Raw Webhook `last_delivered_at` is updated on failure. It is a last-attempt time,
   not proof of success. W06/W07 must distinguish the two in diagnostics.
6. Raw delivery and Slack have separate retry loops. W02/W04 must establish one
   owner of retry per path before moving callers.
7. Forecast readiness relies on both channels and existing settings invalidation.
   W06 must also reject verification of a concurrently replaced configuration.
8. The generic receiver UI suggests a chat robot URL without a matching payload
   adapter. W08 replaces that example and makes adapter choice explicit.
9. Raw receiver URLs can include secrets and historical private destinations.
   W07 must not broaden visibility or silently break existing receivers.

## Local verification environment

The inherited Go workspace does not include this module. Initial unqualified
`go build` and `go test` commands failed before compiling source. Subsequent
commands use `GOWORK=off`, matching repository CI, without editing the external
workspace. PostgreSQL tests require a disposable database and
`TEST_POSTGRES_DSN`; an unset value means those cases skip.

## W02 transport extraction

`internal/mail.Config.Attempt` exposes confirmed acceptance and uncertainty while
`Send` remains a compatibility wrapper. A 30-second overall deadline covers SMTP
and TLS. Context cancellation closes the actual connection. Final DATA acceptance
is success even if QUIT cleanup fails. A lost final reply is uncertain; an explicit
SMTP rejection is not. A failed body write aborts the connection without closing
the DATA writer and accidentally submitting a truncated message.

`internal/notification.AttemptHTTP` sends once, records HTTP acceptance, transient
rejections and Retry-After, distinguishes possibly transmitted requests, bounds
response draining and scrubs destination-bearing error text. Existing Slack host
checks and redirect refusal remain in the Slack adapter. Existing raw receiver
and Slack retry profiles stay with their coordinators until worker adoption;
there is no new retry loop in the shared primitive.

Tests were added before the new APIs and initially failed because the methods
did not exist. Protocol fixtures cover cancellation, blocked greeting/EHLO/DATA
and TLS, final acceptance with failed cleanup, explicit rejection and a lost
reply. HTTP fixtures cover one-attempt response classification, rate-limit delay,
secret redaction and lost-response uncertainty. No real destinations are used.

## Verification results for the initial extraction

The following completed successfully on 2026-10-21:

```sh
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test ./...
GOWORK=off go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...
GOWORK=off go test -race ./internal/mail ./internal/notification ./internal/webhook ./internal/app -run 'TestAttempt|TestHTTPAttempt|TestNotificationBaseline|TestAlertGoesToBothChannels|TestSlackRedirectIsNotFollowed|TestFireEventDeliversToSubscribers' -count=1
git diff --check
```

The focused race command exercises the changed mail, HTTP primitive and selected
app paths; its Webhook package has no matching test names. The full package tests
above exercise the Webhook tests. This does not claim a full-repository race run.

No PostgreSQL integration result is claimed for this extraction. No frontend,
schema, production configuration, real recipient, queue admission or Status page
was changed. Database and frontend implementation remain later work packages.

The preceding verification describes the initial extraction only. The subsequent
implementation and its acceptance evidence are recorded below.


## W01, W03, W04 and W05 implementation

ADR 0038 records exact tables, portable constraints, queue bounds, claim exclusion,
lease fencing, protocol uncertainty and source occurrence identity. Migration
0007 leaves the accepted baseline untouched. All new tables participate in the
existing logical backup enumeration. Restore pauses operational dispatch, clears
leases and marks unfinished attempts uncertain. Existing older-dump fixtures now
remove the later ledger entry when simulating a pre-0006 database.

Password reset and registration verification now use an independent executor
with two active slots and 32 waiting slots. Operational delivery uses four fixed
workers and SQL due times, with at most one lease per channel. PostgreSQL capture
and claim transactions use separate short advisory locks; network I/O never holds
those locks. Shutdown cancels network activity and waits before closing the store,
with a separate five-second worker budget after HTTP drain.

Queued deliveries recheck destination/route revision, account eligibility,
resource access, route grants and preferences. Mandatory routes do not override
permission revocation. SMTP transport and raw signing configuration are frozen
for an attempt after effective configuration resolution. Authentication payloads
and bearer links never enter event or replay storage.

New job submissions persist explicit opt-in in the same transaction as the job
and items. Terminal transitions persist occurrence evidence, not a delivery.
Post-commit capture atomically creates the event and bounded raw/email/inbox
fan-out, consumes one-shot consent and clears pending evidence. A manual retry
rotates the occurrence token without restoring consumed consent. Recovery reads
only pending snapshots, and never executes a job or calls a workflow provider.
The snapshot includes an explicit UTC occurrence instant so recovery does not
renew the delivery lifetime by parsing legacy local timestamp strings.

Job deletion and age-based cleanup clear old consent and pending snapshots in
their transaction. Tests deliberately simulate identifier reuse after database
repair and a callback holding a stale snapshot; neither can consume a replacement
job's consent. Account deletion also removes personal notification state and
cancels pending delivery; capture locks accounts to prevent reinsertion races.

The new completion path replaces the old in-memory map and direct completion
mailer. Both scheduler terminal callbacks use the durable path, and queued-job
cancellation records the same completion evidence. Job raw receivers are captured
alongside email, with unchanged legacy payload bytes and signing headers. Other
raw producers and forecast Email/Slack still use their legacy coordinators until
W06; they must not be enabled through a second path in parallel.

### Remaining work

1. Finish W04 retention policy, history review/replay and lifecycle acceptance.
2. W06 source adoption is complete; keep its compatibility and concurrency fixtures
   in the final release acceptance run.
3. W07: finish canonical channel management, old API facades, admin APIs, preview,
   explicit tests, paused restore review and idempotent replay. Provide the shared
   authorized extension publication contract, without implementing Status itself.
4. W08: management pages, safe presets and signed n8n integration example.
5. W09: authorized inbox endpoints, preferences, bell/page, snapshot read-all and
   visible-tab polling. Stored inbox rows alone are not a released inbox feature.
6. W10: full source-call accounting, dual-driver/frontend acceptance, operator
   runbook and reader-facing notes. Status remains a separate future project.

No production database, external recipient or real destination was used for this
work. PostgreSQL verification uses a private temporary cluster and Unix socket.
The work is uncommitted and is not a deployment or release.


### Verification after durable job adoption

Completed on 2026-10-21 with the approved schema and job adoption in place:

```sh
GOWORK=off go test ./...
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...
GOWORK=off go test -race ./internal/app ./internal/mail ./internal/notification -run 'TestNotification|TestAttempt|TestExecutor|TestNotifyJobDone' -count=1
GOWORK=off TEST_POSTGRES_DSN='<disposable local database>' go test ./internal/app -run TestPostgres -count=1
git diff --check
```

All passed. A later attempt-configuration snapshot adjustment was additionally
verified with the complete notification test subset, the app notification race
subset and staticcheck. This is not a full-repository race result. No frontend
source has changed yet; frontend acceptance belongs to the later interface work.

The PostgreSQL cases cover simultaneous capture, simultaneous claims of a single
row, competing different rows for one channel, stale-worker fencing, job consent
recovery and a retry with a new occurrence but no new consent. Existing PostgreSQL
and cross-driver restore cases also passed. A retained restore-pause setting in
the shared disposable database initially caused the new mail test to pause as
designed; the fixture now explicitly starts the dispatch setting it is testing.


## W06 adoption and W07 foundations

Every business producer now commits its operational event before network work.
Report ingest, manual report creation/editing, prediction rejection, prediction
store failure and the watchdog all use the durable path. The raw JSON body remains
the producer's JSON; internal metadata is not inserted into a legacy envelope.
Raw delivery adds opaque event and delivery ID headers for consumer idempotency
without changing existing event/signature headers or HMAC input bytes.

Forecast store failures and watchdog alerts capture one event with raw receiver,
Email and Slack fan-out. The producer no longer separately starts a forecast
sender. The old raw and Slack retry loops have been removed; automatic retry is
owned only by the worker. Forecast's compatibility view records the common
worker result. Explicit SMTP/raw/forecast test actions remain separate from
normal subscriptions, and forecast explicit tests use the same single-attempt
adapters as the worker.

SMTP and forecast destination saves now share an atomic settings service and a
short PostgreSQL transaction lock (780716). A manual forecast test freezes both
channel configurations, sends outside the transaction, then compares and stamps
inside another transaction using the same writer lock. A changed configuration
returns `notification_config_changed` rather than receiving a readiness stamp.
Monotonic per-transport counters detect a change followed by a change back; a
Slack-only change does not invalidate an unrelated queued completion email.
Failure while writing any setting or counter rolls back the entire settings
change and its verification invalidation. Legacy password-blank-means-keep and
forecast no-op save behavior remain intact.

Explicit rules can select granted account/OU candidates or the job creator for
personal Email/inbox, or a fixed administrator-configured external destination.
The routing foundation is active in capture but has no management HTTP API yet.
An empty grant set selects nobody; inbox candidates are stored only for original
eligible recipients. Selection intersects current resource authority, and worker
delivery repeats route, permission and preference checks. Inbox read checks still
belong to W09 and must not expose these rows directly.

A deletion invalidation cursor in `notification_source_cycles` fences candidates
selected before an account was deleted and recreated. Its source key hashes the
username, its generation advances inside the deleting transaction, and capture
compares it after locking the current account row. This closes the pre-capture
snapshot race without a new table or account-ID column. Persisted recipient and
preference cleanup remains necessary and tested as well.

Tests start the production-style bounded runtime explicitly when asserting actual
delivery; they no longer depend on the producer launching hidden goroutines.
Negative tests preserve exact raw body/signature bytes, one event/three forecast
outputs, blocked-network response independence, panic isolation, configuration
replacement and round-trip changes, permission revocation and reused-account
snapshot rejection. All network fixtures are local.


### W06 verification

Completed successfully after routing and deletion fencing were added:

- Full `GOWORK=off go test ./...`, `go build ./...`, `go vet ./...` and staticcheck.
- All `TestPostgres` cases against the private disposable PostgreSQL instance,
  including settings rollback/revision tests and stale-candidate rejection.
- Focused race tests for notification capture, routing, adapters, source adoption,
  legacy report events, forecast alerts and explicit test/configuration races.
- Frontend `npm run typecheck` and locale tests (25 passing cases), including the
  new translated configuration-change refusal.
- `git diff --check`.

The private PostgreSQL instance was stopped after verification. No management
page, notification bell or inbox endpoint is claimed in this stage. HTTP channel
management, history, replay and extension publication are the next package.


## W07 management API and W08 initial interface

`docs/notification-management-api.md` documents the implemented administrator
contract and novice-facing integration sequence. Channel/rule CRUD, explicit
single-attempt channel tests, preview, paginated history, attempt detail, uncertain
result dismissal, idempotent replay and reviewed dispatch resume are registered
through the standard administrator session route class. Ordinary accounts are
refused by a test that exercises every notification management route.

New destination URLs and signing keys are sealed with the existing keyring and
never returned. Canonical legacy references edit the original forecast settings
or raw receiver row. A blank secret input preserves the stored destination;
clearing requires an explicit field. SMTP/forecast implicit channels cannot be
deleted or duplicated by ordinary routing rules. Legacy raw deletion uses the
same transactional revocation path. Route writes revalidate their principals
inside the transaction and exclude concurrent identity deletion.

Pausing, resuming or renaming a channel preserves waiting messages. A delivery
revision separate from the administrative edit revision detects actual transport
changes. Forecast disable/pause invalidates readiness, and current configuration
snapshots include forecast channel state so a concurrent administrative change
cannot receive a stale test stamp.

Manual channel tests have a separate two-request bound, run once, and record
`notification.test` rather than pretending a report or task happened. Per-channel
lease exclusion is shared with normal workers on Postgres. Previewing a synthetic
example returns no real recipients. A real-event preview applies current rules
and permissions without consuming consent or writing inbox state.

Replay writes a new delivery generation under the original event. A stable
request ID returns the same new delivery after an HTTP retry. Accepted/uncertain
results require duplicate confirmation; current channel/rule revisions and
recipient authority are checked again. Replay does not invoke the source,
re-execute a workflow, recreate inbox state or rewrite original attempt evidence.
A reviewed uncertainty remains uncertainty in its attempt record.

Queue resume checks an opaque snapshot of pending, leased, retrying and uncertain
rows. A changed queue requires a new review. Revocation fences an active worker;
an already-started request is recorded uncertain rather than falsely unsent.
Closed account delivery records are also marked non-replayable, preventing a
recreated username from inheriting old external mail.

Task notification ownership is now bound to the submitting account's generation
in the approved source-state table. Completion snapshots retain that generation,
and personal delivery/resource checks compare it with the current account. An
old job explicitly retried after account deletion cannot select the replacement
account through a creator rule. Old submissions with no ownership evidence fail
closed for personal routing; raw administrator-configured outputs remain separate.
This uses existing approved tables and does not alter the database baseline.

Typed frontend bindings are in `web/src/api/notifications.ts`. The initial
management page is linked at `/manage/notifications`, with channel/rule editing,
secret-preserving inputs, test actions, history details, uncertainty review,
duplicate-confirmed replay and queue review/resume. A replay action retains its
request ID across retries. Interface testing and visual acceptance are still in
progress; do not claim W08 complete.

### Verification before management-page work

The full Go suite, all PostgreSQL integration cases and focused notification
race tests passed after task-owner fencing and its updated fixtures. The private
PostgreSQL test cluster remained running for this verification. An earlier fixture
selected every nonempty source signature, which started including owner cursors;
it now selects only the completion namespace it intends to manipulate.

Frontend typecheck passed after adding the page. The existing full frontend suite
passed 996 tests before this page was added. New page tests cover initial-load
failure, blank write-only values with revision-preserving save, and explicit queue
review; these must pass along with full frontend gates before release acceptance.

Remaining: finish W08 tests/visual verification and adapter/software presets,
shared authorized extension publication, retention policy and cleanup, W09 inbox
APIs/bell/page/preferences, signed workflow example, operator runbook and full W10
acceptance. Status itself remains outside this implementation.


### Management-page verification

The management-page tests now pass: load failure offers no editable defaults,
write-only URL fields remain blank while a save carries the loaded revision,
and queue resume requires a visible review and its exact token. These tests found
and fixed the initial adapter-field render when opening a prefilled form.
A slow jsdom accessible-name query originally timed out under the full suite;
locating the same labeled controls by visible text preserves the assertions and
reduces the focused test run to about one second without raising timeouts.

Verified after the current management-page changes:

- Frontend full suite: 117 files, 999 tests passed.
- Frontend typecheck and production build passed. The build still emits the
  existing Simplified Chinese locale static/dynamic import bundling warning.
- Full Go suite, all PostgreSQL integration cases and focused notification race
  checks passed after the task-owner-generation correction. A later added history
  pagination case verifies that equal-timestamp rows are neither skipped nor
  repeated and uses UTC wire instants; its focused run passed.
- Go build, vet and staticcheck passed; `git diff --check` passed.

The optional frontend lint command could not run because this checkout's installed
runtime has no `oxlint` executable. No lint result is claimed. The private local
PostgreSQL cluster was stopped after verification. Visual/browser acceptance is
still outstanding, and no production deployment, commit or release was made.


## W09 inbox implementation

The personal session API is documented in `notification-inbox-api.md`. It adds
resource-authorized list/count/read/dismiss endpoints, a signed snapshot-based
read-all action and registered-category preferences. Original recipient rows
intersect current resource authority and current inbox route membership; explicit
task opt-in remains personal to the submitting account generation. Revoking a
role or account/OU audience hides old content, count and mutation access.

Read-all records concrete recipient IDs, the caller and account generation, plus
an expiry protected with a purpose-separated HMAC. Later arrivals remain unread.
Application rechecks current authority, and another account or a recreated
username cannot use the old snapshot. Preferences cannot affect authentication
mail or grant a new event type. Metadata is scanned with a 10,000-candidate bound;
body loading is limited to the current 50-item page.

The header bell and `/notifications` page are wired through existing authenticated
routing. Both poll only while visible, and release listeners/timers on unmount.
The page renders summary text safely, exposes read/dismiss, confirms read-all
against its frozen count, and edits optional category preferences. Tests cover
safe text rendering, exact snapshot use, hidden-tab behavior and listener cleanup.
No schema change was needed for this package.


### W09 verification

The current inbox implementation passed the full Go suite, all PostgreSQL
integration cases, focused notification race checks, Go build/vet/staticcheck,
frontend typecheck/build and the full frontend suite (119 files, 1,001 tests).
The new browser route's title coverage test caught a missing tab title; both the
personal inbox title and the management page's specific title are now registered.
The personal inbox also has a home breadcrumb.

New backend checks prove cross-account snapshot refusal, signature tampering
refusal, later-arrival exclusion, operations-role revocation and route-grant
revocation for list/count/mutation. The same snapshot test runs on PostgreSQL.
Frontend checks prove safe text rendering, exact snapshot confirmation,
visible-tab polling and listener cleanup. The private test PostgreSQL cluster was
stopped after these checks. No production destination or database was used.

Still outstanding for the complete objective: software presets and signed
workflow example, generic authorized extension publication, configurable
retention/cleanup, visual/browser acceptance and final operator/release runbooks.
Existing old Email/Webhook test facades also need the final source inventory
review to ensure their intentional compatibility behavior is fully accounted for.
No goal completion or production release is claimed at this stage.


## Retention and software recipes

The optional retention service and management editor are implemented without new
schema. Automatic cleanup remains disabled until an administrator explicitly
enables it. Bounded hourly passes preserve live and uncertain work, recent replay
families and bodies still referenced by inbox rows. Global dispatch pause also
pauses deletion. When orphaned old bodies are compacted, source identity metadata
and a payload digest remain in approved records so idempotent source retries do
not recreate notifications. Details and bounds are in the management API runbook.

SQLite and PostgreSQL tests verify disabled-by-default behavior, restore-pause
protection, closed-history deletion, preservation of pending/leased/retrying/
uncertain work, linked replay-family retention, inbox-body references, and
idempotency after payload compaction. Policy saves are atomic, day ranges are
validated and the editor does not enable cleanup simply by opening it.

Setup recipes now offer Email, Slack, raw Webhook and portal inbox configuration
entry points. An inactive n8n signature-verification example and a detailed guide
are bundled as static credential-free downloads. The guide links official Slack
and n8n documentation and describes the exact-byte contract, trusted runtime
secret access, test/production URL distinction, durable deduplication and the
limits of unsigned correlation headers. The example performs no external action
and does not claim to be a durable production receiver.

Fixture tests execute the actual workflow Code-node source with a mocked n8n
binary-buffer helper and real HMAC implementation. They verify valid signatures,
tampering refusal, malformed-header refusal, missing-secret failure and retention
of a JSON integer beyond 2^53 as raw UTF-8 text. The workflow ships inactive with
empty pinned data. A live n8n installation is not included; runtime import and
activation remain a documented deployment verification, not a claimed test.

Full Go tests, all PostgreSQL cases and focused notification race tests passed
with retention enabled only in fixtures. Frontend typecheck/build and the focused
management/recipe/locale tests passed; the full updated frontend suite also passed (120 files, 1,004 tests). Visual/browser acceptance and generic extension publication
remain outstanding, together with final source accounting and release acceptance.


The bundled recipes and n8n workflow were confirmed present in the built embedded
SPA output. Full frontend acceptance for this stage passed 1,004 tests, typecheck
and build. Go build, vet, staticcheck and diff whitespace checks passed. The local
PostgreSQL fixture instance was stopped after verification. This remains an
uncommitted implementation, with no production configuration changes or release.


## External publication and final acceptance

The versioned API now accepts narrowly authorized publication from dedicated
`notify:<publisher>` service credentials and installed-app `notify` capability.
The namespace is credential-derived, request fields cannot select recipients,
and publication commits through the same durable capture and administrator rules.
Stable source occurrences deduplicate; conflicting content is rejected; new
occurrences have a serialized rolling quota. App publication rechecks installation
and current administrator issuer identity/session state. The app iframe never
receives its ephemeral credential or administrator API access. OpenAPI and the
separate publishing guide document setup, bridge usage, bounds and revocation.

Full SQLite/backend tests, actual PostgreSQL tests and focused notification/app
race checks passed after publication adoption. The frontend suite passed 1,005
tests across 120 files, typecheck and build. Final browser acceptance then found
a narrow-screen title clipping issue and a legacy test-capacity bypass. The
inbox title now wraps, with the timestamp in the body. Original SMTP/raw/forecast
test endpoints now share the separate two-call manual-test bound and SMTP tests
honor request cancellation. Both new backend regressions failed before fixes and
passed afterwards. Extension sample tests now render the selected source content.

An isolated SQLite instance on loopback port 18793 was created with disposable
credentials. Browser acceptance exercised real login, channel creation, event
catalogue refresh, rule creation with an explicit account grant, external service
publication, inbox/unread display, snapshot-confirmed read-all and preferences.
Light/dark views were checked at the actual narrow in-app viewport; the complete
title remained readable after the fix. This is not a claim of exhaustive desktop
viewport or assistive-technology certification. No production database, provider,
SMTP recipient or Slack destination was used.

The final producer search accounts for every automatic sender and the deliberate
legacy test-only facades in `notification-operator-runbook.md`. Those old tests
retain synchronous transport-specific responses/readiness evidence; new canonical
channel tests record common attempt history. No automatic producer keeps an old
per-receiver goroutine or independent Slack retry loop. The runbook includes
upgrade/restore/downgrade, uncertain delivery review, replay, retention, setup,
troubleshooting and reader-facing draft notes. No release is published by this work.


### Final gates (2026-10-21)

- Full Go suite: passed (`GOWORK=off go test ./...`).
- PostgreSQL: all `TestPostgres` cases passed against a private Unix-socket cluster.
- Focused race checks: notification, app-token and adapter cases passed; this is
  not a claim that the entire unrelated repository was run under the race detector.
- Go build, vet and installed staticcheck: passed. Staticcheck found an unused
  compatibility wrapper after the SMTP test adopted request context; it was removed
  and all three static gates passed again.
- Frontend: typecheck/build passed; 120 test files and 1,005 tests passed.
- New source-sample and legacy test-capacity regression tests passed after their
  intended pre-fix failures.
- Diff whitespace check passed. No optional frontend lint result is claimed; the
  local optional linter executable is unavailable.
- Actual browser acceptance: login, channel/rule creation, source publication,
  inbox display, snapshot read-all, preferences, light/dark rendering and retention
  editor with cleanup still disabled passed. No live external receiver was used.

No further implementation package remains in this notification-consolidation
scope. Production SMTP/Slack/n8n activation and release/deployment require the
operator's normal environment-specific acceptance. Status remains future work.
Implementation was verified locally before pull request preparation. No release
or production configuration was changed.


## Main-line upgrade integration, 2026-10-23

Synchronized main commit `65c18abb` before deployment preparation. Released
migration order remains `0001` through `0006`, `0010`, `0009`; notification
`0007` appends afterward. The upgrade fixture now removes the complete later
migration suffix when constructing a historical pre-0009 backup, including its
ledger rows and tables. PostgreSQL's older-dump fixture uses the current migration
list instead of assuming 0007 is the only later step. No approved schema product
or frozen baseline changed.

The operator runbook now requires offline schema inspection/apply/inspection for
the explicit step inherited from main, with old-image backup and clean-target
rollback. Validation passed: full Go tests, vet and build; all PostgreSQL tests
in a separate `notification_merge_1023` test database; frontend typecheck, 1209
tests across 129 files and production build. No live Portal upgrade, release,
notification activation or production database write was performed.
