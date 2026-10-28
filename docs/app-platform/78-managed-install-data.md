# Managed installation review and native data preparation

This continues P04/P05 after document 77. A separate development entry point now
reviews and admits a managed release against an authenticated runtime, stages its
verified frontend and prepares its data through the root runner. Managed data
completion advances to `start`; it does not activate the application. Generic
HTTP installation stays frontend-only while remaining managed stages are built.

## Source map

| File | Responsibility |
| --- | --- |
| `internal/app/app_platform_managed_install.go` | Exact runtime target, explicit review/admission, binding/root checks |
| `internal/app/app_platform_install_review.go` | Canonical review, consumption/replay and atomic managed admission |
| `internal/app/app_platform_install_profile.go` | Managed quota and format |
| `internal/app/app_platform_install_stage.go` | Current publisher/admin/lease/target authority before staging |
| `internal/app/app_platform_data_intent.go` | Signed preparation under the reviewed runtime identity |
| `internal/app/app_platform_data_stage.go` | Fresh root probe, receipt settlement and transition to `start` |
| `internal/app/app_platform_managed_install*_test.go` | Refusals and actual Linux installation acceptance |
| `scripts/acceptance/project_quota_probe.py` | Mandatory real Portal managed data acceptance marker |

## Review and admission

1. `createPlatformManagedInstallReview` obtains current opaque runtime facts from
   the existing administrator session and confirmed enabled runtime binding.
   Native IO happens outside SQL transactions. Caller-created metadata cannot
   provide this authority.
2. The canonical review fixes runtime/deployment/controller identity, binding and
   control revisions, public fingerprint, native architecture, runner/backend
   protocols, sandbox and root UUID/inode. Existing release signature, publisher/
   key/trust revisions, actor authority and audience remain bound to the review.
   Neither decrypted seeds nor private host paths enter this document.
3. The backend must match the observed platform/protocol ranges. Required
   capabilities refuse until their enforcement providers exist; optional ones
   remain declined. Compatibility does not establish readiness:
   `execution_ready` stays false.
4. `consumePlatformManagedInstallReview` probes again, requires the exact target
   and current binding, then applies existing actor/trust/expiry/canonical-plan/
   idempotency checks. One transaction creates the installation's `runtime_id`
   and the operation's `managed=1`. Temporary identity seed bytes are cleared.
5. Exact replay requires the managed entry point and fresh matching runtime
   facts. Generic static consumption/replay refuses managed plans. No public
   managed management route is exposed by this phase.

## Staging and data

With a current operation lease, staging follows:

`queued → validate → download → stage → prepare_data`

Every transition rechecks current actor authority, publisher signature/trust,
reviewed target, installation generation/policy/runtime and operation fence. The
existing release/blob checks are reused. The frontend shortcut that skips native
data preparation refuses managed operations.

Portal prepares a durable signed data intent and obtains a fresh runtime profile
before dispatch. That profile must equal the reviewed target. The root runner
prepares data under the same runtime/controller and stable namespace/effect
identity in the shared journal. Settlement validates authenticated native evidence,
current lease and authority, command/effect identity, runtime binding and receipt
root UUID/inode. Namespace metadata, applied effect, operation revision and audit
commit together.

Static native data completion still advances to `activate`. Managed completion
advances to `start`, with installation `pending`, no activation and no access
grant. Active namespace metadata is not a writer lease; native directories remain
root-owned and protected. No managed scheduler is enabled here: the development
caller explicitly drives the operation, which remains leased at `start` awaiting
the candidate integration. Existing static scheduling is unchanged.

## Verification

Common tests refuse missing opaque proof, invalid or changed runtime identity/
epoch/revisions/fingerprint, disabled binding, invalid platform/root/inode/sandbox
and missing required capability providers. Metadata validation runs on SQLite and
PostgreSQL; it does not claim native success.

The Linux fixture runs a real root service and a separate non-root Portal child.
After all existing static installation assertions, the child signs/stages a
managed release, reviews the exact runtime, refuses legacy consumption and a
disabled binding, admits/replays the managed operation, advances staging and
dispatches native data preparation. It checks managed/runtime persistence,
applied data effect, `start`, pending/no grants/no activation, static activation
refusal and stale-lease refusal. The parent requires `MANAGED_INSTALL_DATA_READY`.
Only a passing parent test permits the script to emit
`native_portal_managed_install_data_to_start: true`.

The sample backend digest is syntactic fixture input: this test does not pull or
start that image. Native workflow evidence is pending until this functional
source passes. Local results are recorded in the execution log. Passing this
phase cannot establish candidate startup, health or usable application access.

## Remaining integration

Connect signed candidate intent, artifact/socket staging, actual native start and
receipt settlement to `probe`, then real readiness, registration and writer/broker
authorization before activation. Managed scheduling/startup, unknown-effect
reconciliation, update/rollback/uninstall and reference-app acceptance remain.
No schema, real credentials, production/beta configuration, Status migration or
SDK freeze changes in this phase.
