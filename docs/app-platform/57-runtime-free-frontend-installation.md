# Runtime-free frontend installation

This implements the no-application-data static installation profile. A compatible
frontend with no required providers can be installed, authorized, opened and
used without a runner binding, Unix socket, Docker service or data directory.
The actual Portal launch/document handlers and experimental React/Ant Design SDK
are exercised together. Market management HTTP/UI and general provider-backed or
managed applications remain separate work in the full platform plan.

## Source map

| Source | Responsibility |
| --- | --- |
| internal/app/app_platform_install_review.go | Generic frontend default, explicit native entry points, canonical confirmation/admission |
| internal/app/app_platform_install_profile.go | Closed profile/manifest/quota rules and current-plan lookup |
| internal/app/app_platform_frontend_activation.go | No-data candidate construction and strict readiness verification |
| internal/app/app_platform_install_stage.go | Context-bound local prepare transition |
| internal/app/app_platform_activation.go | Shared atomic ready/grant/completion transaction |
| internal/app/app_platform_install_queue.go / app_platform_install_scheduler.go | Profile-separated selection and portable empty-endpoint service |
| internal/app/app_platform_launch.go | Explicit namespace/no-data protected read branches |
| internal/app/app_platform_frontend_install_test.go | Repository, scheduler, HTTP, fencing and refusal coverage |
| internal/app/app_platform_http_browser_test.go | Real frontend installation for the loopback SDK/browser fixture |

## Choose the profile in the server-produced review

The generic `createPlatformInstallReview` and audience variant now choose the
frontend profile. `createPlatformFrontendInstallReview` names it explicitly.
The former native-data experimental profile has explicit
`createPlatformNativeInstallReview` / `createPlatformNativeInstallAudienceReview`
entry points, used by native storage/runtime acceptance. A browser cannot select a
profile by inventing a manifest field or editing a confirmation digest.

| Review property | Frontend profile | Native-data experimental profile |
| --- | --- | --- |
| format | portal.frontend-install-review/1-dev.1 | portal.install-review/1-dev.1 |
| data_quota_bytes | 0 | 268435456 |
| runtime preflight | None | Confirmed shared native data service |
| namespace/effect creation | None | Existing signed data intent and physical receipt |
| activation data_namespace_id | NULL | Exact prepared namespace |
| activation data_format | 0 (absent data) | Exact observed positive schema |

Both profiles use the existing closed canonical review structure, exact package
and trust identities, sponsor authority, explicit user/OU audience and consumed
review/idempotency contract. The profile and quota are part of the confirmation
SHA-256. Consumption and every stage reconstruct them from the verified signed
manifest. Unknown formats and mismatched quota/profile bytes refuse.

Frontend admission currently requires no backend, no required capability and no
startup writes. All optional capabilities are explicitly declined. A required
storage, task, background or other unavailable provider refuses admission; the
installer cannot silently discard it or manufacture provider readiness. This is
the current initial no-granted-capability slice. Further broker capabilities need
their actual reviewed enforcement adapters before acceptance is broadened.

The descriptor retains its declared positive data schema/readable-schema list.
With no persistence provider there is no current stored data schema. The
activation's zero means absence; it is not an empty filesystem using schema zero.
Future storage grants/updates need a reviewed provider transition. Never invent a
zero-byte file quota, writer fence, native receipt or empty namespace to make this
profile fit the native path.

## Existing storage, no additional product

The approved activation table already permits a NULL data_namespace_id and
nonnegative data_format. This implementation uses those existing fields and
bounded readiness_evidence JSON. It adds no table, migration, namespace, database,
file, background app process or SDK descriptor field. Installation identity still
uses the existing sealed identity record for future lifecycle continuity.

The new readiness evidence format, `portal.frontend-readiness/1-dev.1`, binds:

- installation/release, initial generation and policy revision;
- controller epoch and exact reviewed plan digest;
- descriptor's declared schema, archive digest and compiled document digest;
- original reviewed audience digest.

These are local contract/integrity facts. The format makes no physical allocation,
quota, writer or running-backend assertion. The initial implementation accepts
generation/policy revision 1; later audience-policy, update and restore workflows
must deliberately extend the evidence/reader contract rather than editing rows
behind the lifecycle repositories.

## Installation sequence

The shared bounded installer still follows:

```text
queued -> validate -> download -> stage -> prepare_data -> activate -> complete
```

At prepare_data, it reads the current reviewed profile. The frontend branch
runs a context-bound local transaction, rechecks the exact live lease, complete
review/sponsor/publisher/audience authority and absence of any resources, and
compiles the actual signed frontend with BuildFrontendDocument. It advances to
activate with an audited CAS. It does not sign or send a runner command.

At activate, the profile is checked again under transaction locks. The frontend
branch checks all resource absence and recompiles the actual supported document.
The existing atomic activation boundary then inserts the explicit no-data
readiness evidence, exact reviewed grants, ready installation and completed
operation, clearing its lease. Its namespace remains NULL and format zero. Late
authority, audience, clock and cancellation checks still apply. Failed writes
roll back the entire activation; the same valid lease can retry.

Direct calls to the native preparation entry point reject frontend-profile work
before opening a runtime key/provider. The native command constructor also checks
the profile. The bounded installer does not fall back from unavailable native
storage to frontend mode, or from frontend failure to native execution.

## Portable background service and queue separation

`newPlatformInstallScheduler` accepts an entirely empty endpoint for the frontend
profile. Partial endpoint configuration still refuses. Its frontend service runs
on the Portal host without Linux/root/controller-UID prerequisites; native mode
retains the exact existing Linux non-root UID requirements. Existing production
startup remains unchanged.

Queue selection uses the fixed canonical format prefix before LIMIT, then
rechecks the actual closed reviewed profile under locks. Frontend/native jobs
cannot consume each other's page or be claimed by the wrong worker. Only the
native selector reads/decrypts the shared runtime binding. The frontend selector
and activation remain independent of a missing, disabled or unusable runtime.

An expired frontend operation with no resources can resume even at activate,
under a new fence: no external effect ever occurred. The previous owner cannot
commit afterward. Any unexpected namespace/effect/grant/activation residue still
requires retained review. Native activate/unknown intent retains its original
conservative recovery behavior. Review/retry/renewal limits from the background
scheduler continue to apply.

## Protected open and document delivery

The existing launch gate now has two explicit cases. A namespace-bearing
activation still requires its real matching active namespace/schema/writer.
A NULL namespace requires format zero, the frontend readiness format and the
initial generation/policy tuple. It cannot reuse a native row with its namespace
removed or an empty readiness object.

The frontend read gate repeats current session/account lifecycle, actual user/OU
access, publisher trust, signed package and control-epoch/pause checks. It verifies
canonical readiness/review hashes, matching completed initial operation and
idempotency frame, no persisted physical resources/effects, exact declined
capability declarations and the current compiled document digest. The declared
schema is checked against the descriptor while stored format remains zero.
Changed compiler output or proof requires revalidation; it cannot silently inherit
an old readiness assertion. Required provider grants remain unavailable.

The authorized directory can list the installed app, and the same authenticated
launch URL/document routes work. CSP stays opaque (`sandbox allow-scripts` without
allow-same-origin); response policy stays no-store/nosniff/no-referrer. Access
revocation invalidates an old document URL. Administrators do not bypass an empty
audience. Applications continue to use the shared host/SDK language and shell.

## Verification

Repository coverage includes SQLite/PostgreSQL installation and scheduling with
zero runner bindings, namespace/effect absence, real HTTP launch/document and
access revocation, directory visibility, empty-audience denial, required-storage
refusal, native-request rejection, proof/plan/document/epoch tampering, resource
injection, expired-owner fencing/resumption, atomic activation rollback/retry and
independence from an unusable configured runtime. The original database-wait
cancellation regression remains covered; new profile checks preserve raw context
errors instead of replacing them with a validation error.

The loopback browser fixture now performs the real generic frontend installation
for a normal user before exposing the actual Portal HTTP handlers. It no longer
seeds an activation manually. The fixture asserts zero runtime bindings,
namespaces and operation effects. The browser plugin observed:

1. Host Notes heading and real SDK button en-US / UTC / 0.
2. Simplified-Chinese host labels and zh-CN / UTC / 0 together.
3. A working in-memory click counter (1).
4. Traditional-Chinese host labels and zh-TW / UTC / 1, preserving page state.

This is interaction/language evidence, not pixel-level visual, accessibility or
full market installation acceptance. No desktop screenshot/control or production
site was used. The hidden temporary tab and owned loopback fixture were closed.
The Apps checkout was read without modification; its existing React/Ant Design
SDK and legacy no-scope template remain unchanged.

Reproduce repository tests with the pinned toolchain:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
go test -p 2 ./internal/app -run '^TestPlatformFrontend'
```

PostgreSQL cases additionally require TEST_POSTGRES_DSN pointing to an owned
disposable test database. Reproduce browser interaction using
`scripts/app-document-browser-fixture.py --apps-dir /absolute/path/to/Apps --portal-http --launch-page`
and the browser plugin, following the existing fixture cleanup instructions.
Do not use a production database or cookie in that fixture.

This advances the backend/launch portion of RUN-01. Full market click-through,
provider-backed storage, managed execution, fenced recovery approvals, lifecycle
updates/uninstall, SDK public freeze and release/adoption gates remain required.
No production deployment, beta configuration, Status migration or root market
catalogue is changed here.
