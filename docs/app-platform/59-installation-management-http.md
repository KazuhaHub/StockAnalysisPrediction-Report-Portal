# Installation review, admission and progress HTTP

This implements three administrator endpoints from the control-plane proposal.
It connects existing signed staged releases and canonical review/admission to the
process-owned frontend service. Catalogue downloading, package publication and
the installation confirmation UI are still separate pending work. No production
configuration, database structure or public SDK contract is changed here.

## Implemented development surface

| Method and path | Response | Meaning |
| --- | --- | --- |
| POST /api/admin/app-platform/plans | 200 | Create a 15-minute no-data frontend install review |
| POST /api/admin/app-platform/operations | 202 | Idempotently commit that exact review |
| GET /api/admin/app-platform/operations/{id} | 200 | Read durable operation progress as a current administrator |

These endpoints require a persisted signed browser session, current active
administrator role and normal enrollment policy. They are registered as
authAdmin in the central route table. Responses, including auth failures, have
no-store, nosniff, no-referrer and restrictive document CSP headers. The write
routes use Go's CrossOriginProtection check without trusted-origin bypasses and
require application/json. Fetch metadata or Origin indicating a foreign browser
origin refuses the request. Headerless non-browser requests follow the standard
library policy and still need a valid persisted administrator session.

Request paths must be canonical and have no query string, including an empty
trailing question mark. Bodies are capped at 32 KiB, use exact case-sensitive keys
and reject duplicate/unknown fields, null objects, trailing JSON and invalid
UTF-8. Audience contains at most 128 explicit user/OU targets; null or missing
audience is invalid, while [] deliberately grants no app-use access.

## Create a review

```json
{
  "action": "install",
  "release_sha256": "<exact staged descriptor SHA-256>",
  "trust_revision": "1",
  "audience": [
    {"type": "user", "username": "alice"},
    {"type": "ou", "group_id": "12"}
  ]
}
```

All integer counters/IDs in this HTTP protocol are canonical decimal strings.
Do not send JavaScript numbers for trust revisions or OU IDs. UUIDs and hashes
are validated before repository work. The action is currently exactly install;
other lifecycle actions require their actual implementation before admission.

The supplied digest names an already staged immutable signed descriptor. The
repository verifies its current allowlisted publisher/key, exact frontend archive,
compatibility, required/optional capability policy and current user/OU identities
under the established transaction lock order. No caller may supply a Docker
endpoint, URL, command, profile, key, runtime ID or arbitrary effective grants.

The response contains plan_id, plan_sha256 and expires_ms, followed by a review
object with app_id, publisher_id, release_sha256, trust_revision,
declined_capabilities, data_quota_bytes ("0") and the resolved audience's public
usernames/OU IDs. Internal actor fingerprints, controller epochs and principal
lifecycle markers remain server-side. The canonical stored plan still binds
them; the redacted public review is presentation, not an editable authority proof.
Creating a review does not create an installation, operation or app-use grant.

Only the no-data frontend format is produced by this route. Required unsupported
providers or backend requirements refuse; optional capabilities are explicitly
declined. This is not a substitute for managed-backend lifecycle or broker work.

## Confirm once, retry the same request

```json
{
  "plan_id": "<server review UUID>",
  "plan_sha256": "<server canonical review digest>",
  "idempotency_key": "<client-generated UUID>"
}
```

The response includes operation_id, installation_id, generation, revision, stage
and replayed. The first response normally has stage queued and replayed false;
a retry returns the existing operation and its current stage. A different body
under the same key conflicts. Preserve the same key after a timeout or lost
response; do not manufacture another install request. Replay still rechecks
current administrator/session authority and expiry at its final boundary.

The HTTP route accepts only the no-data frontend review format, including when
replaying. A native-data review produced by an internal development fixture cannot
be admitted or replayed through this route. Existing internal native acceptance
continues using its explicit repository entry points.

For new admission, the repository rechecks the whole canonical plan, expiry,
release/trust, actor authority and audience snapshot, and atomically consumes it
with the operation/installation records. Changing an OU lifecycle or losing role
after preview cannot transfer the old approval to a new principal. The installer
is not automatically added to the app-use audience. The HTTP handler does not
execute the scheduler inline; the owned background service discovers durable work.

## Observe after refresh

Store the returned operation UUID in the management page's route and GET that
operation after reload. Progress includes operation_id, installation_id, action,
stage, generation, revision, terminal, control_paused and an optional safe
error_code. It comes from SQL rather than the scheduler's last in-memory status.

Complete, failed and recovery_required are terminal for this observed operation.
Terminal does not authorize a new retry, recovery action or app launch. The
existing launch endpoint independently checks current app-use grants and actual
readiness; operation progress does not return a launch credential or bypass it.
No next-action buttons are authorized by this initial progress DTO.

Any current administrator may inspect the operation. Ordinary users and revoked
sessions cannot. A restore-paused or mismatched controller epoch is reported by
control_paused without clearing the pause. Observation remains possible during
recovery. It never claims, resumes, renews or mutates the operation. Lease owners,
fences, identity seeds, raw stored errors, endpoints and credentials are omitted.
Unknown stored error text is replaced with app_platform_operation_failed.

## Bounds and error handling

Requests carry a 15-second derived context through review/admission transactions,
preflight pool reads, SQL lock acquisition and contention backoff. Cancellation
rolls back without admitting partial work. Existing browser authentication and
keyring loading retain their shared Portal implementations; this change does not
claim a new process-wide deadline for those services or slow request-body reads.
Foreground plan/commit archive verification shares the existing two-slot document
budget. Busy requests get 429 plus Retry-After: 1; progress reads do not compile
archives. The separate background installation worker is already serial/bounded.

The stable JSON error codes are translated in all three Portal locales:

| Code suffix (app_platform_ prefix) | Status |
| --- | --- |
| invalid_request | 400 |
| origin_denied / permission_required | 403 |
| json_required | 415 |
| plan_expired / plan_changed / operation_conflict | 409 |
| package_untrusted / requirements_unavailable | 409 |
| operation_missing | 404 |
| busy | 429 |
| control_paused / unavailable | 503 |

Existing session/enrollment wrappers retain their normal refusal responses.
Safe progress error codes install_lease_recovery_required,
install_authority_review_required and install_execution_review_required also have
three-locale messages. Never translate by matching fallback English error text.

## Verification and remaining work

The HTTP tests cover actual review -> confirmation -> autonomous background
activation -> protected launch, identical replay and conflicting payload,
malformed/duplicate/case-folded inputs, CSRF and content-type refusal, enrollment,
role changes, expiry, native review/replay refusal, empty audience, replaced OU,
paused progress, error redaction, revoked session and bounded verification.
PostgreSQL tests hold the principal lifecycle advisory lock, observe the request
actually waiting, cancel it while the holder remains locked, and require a safe
response plus unchanged durable state for plan, commit and progress. The fixture
first performs the ordinary browser session activity refresh so that the dump
comparison isolates installation work from that legitimate authentication write.

Run focused tests with the pinned Go toolchain:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -race -p 2 ./internal/app -run '^TestPlatformManagement' -count=1
```

Set TEST_POSTGRES_DSN only to an owned disposable database to include PostgreSQL
cases. Those fixtures reset their database; they must never use production.

Local full Go/PostgreSQL regression passed (application package 158.082 s), as did
focused SQLite/PostgreSQL management race checks (35.005 s), vet, staticcheck
and Linux amd64 compilation.
The three-locale test file passed all 25 tests; web typecheck, lint and production
build passed. Existing unrelated hook-dependency/build warnings remain. This
change has no new rendered management UI to claim browser acceptance for.

Next, provide a verified staged-release directory and explicit user/OU choices
for the administrator UI, preserve confirmation identity across uncertain
submissions, resume progress through a URL, and exercise the real browser flow.
Remote catalogue/publication, managed apps, full lifecycle/recovery, broader
sandbox/network isolation, visual approval and production rollout remain open.
