# Managed backend readiness observed through the root runner

This continues P04/P05 after document 79. A managed installation at `probe` can
now request a fresh application readiness observation and persist it before
advancing to `activate`. This stage does not activate the app, register its
backend, grant writer access or open user traffic. Public managed installation
and the remaining activation requirements are still incomplete.

## Source map

| Source | Responsibility |
| --- | --- |
| `internal/appplatform/backend_readiness.go` | Bounded provisional backend readiness response contract |
| `internal/apprunner/driver/candidate_readiness*.go` | Actual native/policy checks, protected Unix peer and fixed HTTP probe |
| `internal/apprunner/journal/candidate_readiness.go` | Current historical start/data authority before and after native IO |
| `internal/apprunner/transport/candidate_readiness*.go` | Fresh nonce-bound root/controller observation |
| `internal/app/app_platform_candidate_intent.go` | Lease-bounded signing of the already-applied candidate reference |
| `internal/app/app_platform_readiness*.go` | Exact current tuple validation and transactional readiness settlement |
| `scripts/acceptance/ready_app/` | Disposable source-built actual backend, local test registry and exact cleanup |

## Backend contract

The provisional credential-free request is `GET /health/ready` over the app's
provided Unix socket `/run/portal-app/http/app.sock`. Its success body is:

```json
{"protocol":"portal.backend/1","ready":true,"data_format":"1"}
```

The response must be HTTP 200 with `application/json`, at most 4 KiB, no content
encoding, exactly the three named fields and no duplicate keys/trailing document.
`ready` is a boolean; `data_format` is a canonical positive decimal string up to
1000000 and must equal the reviewed current data schema. Field order/whitespace
need not be canonical. False, incompatible protocol/schema, redirects, malformed
or slow responses cannot establish readiness. The probe sends no Portal cookie,
assertion, credential or app configuration. This is a development contract, not
a public SDK freeze; SDK/reference app adoption remains required.

## Native observation

1. Portal signs a fresh reference to the existing applied start effect. It does
   not create another start, probe effect, activation or resource ID. Current
   managed review, publisher/admin/runtime, lease/fence and canonical data/start
   receipts must all match. The candidate activation must remain unobserved with
   no readiness evidence and no app-use grant.
2. The explicit root endpoint authenticates the exact controller UID and signature.
   The request binds a fresh nonce, exact signed reference and descriptor; narrow
   endpoints without a concrete candidate provider refuse. The three-second
   read session is not promoted into an execution session.
3. The journal validates the current applied native receipt and original data
   history. It reconstructs the exact native request, obtains a new read-only
   preflight and requires its image/policy/mount/data snapshot to equal the
   original start preflight. It holds no SQL transaction during native IO.
4. The concrete runtime checks the data quota/pin and actual running container's
   complete existing ownership/security policy. The HTTP socket derives only
   from fixed infrastructure paths plus signed installation/generation IDs.
   Mount pins protect its parents; the socket must be non-symlink, mode 0600,
   owned by UID/GID 65532 and have one link. The connected kernel peer must match
   that UID/GID and the socket inode must remain the same after connection.
   Other apps cannot traverse this platform-owned parent hierarchy.
5. A fixed-path HTTP client has no proxy, redirect, compression or keepalive,
   bounded headers/body and a two-second native budget. After the response,
   runtime mount/data and actual running container/policy are checked again.
6. The journal rechecks current enrolled authority, resource/data history and
   durable fences after IO. An opaque native observation supplies the readiness
   result; stored JSON/native IDs alone cannot provide it. The wire result binds
   the original running receipt hash, logical payload, native ID, protocol/schema
   and root observation time. It changes no root journal/resource state.
7. Portal's opaque transport observation requires exact nonce/request framing,
   canonical bounded result, root peer, current signed authority and observation
   time within this request. It cannot be serialized into fresh provenance.

The claim is a bounded observation of this app's response and native identity,
not a guarantee that its business logic is correct or that it will stay healthy.

## Portal transaction

Settlement reloads the current managed runtime, operation lease, publisher/admin
authority, candidate row and applied start/data history. It compares the exact
signed command, descriptor and original running receipt to the fresh root proof.
One transaction stores the canonical readiness evidence and observed time in the
existing candidate activation row, advances `probe → activate`, updates revisions
and appends audit. Failed/stale checks roll back. No new schema or metadata-key
storage is used. The installation remains pending, the activation remains
`candidate`, and no use/writer/broker/registration grant is produced.

Future activation must recheck short-lived health and current authority; persisted
readiness JSON is not a reusable execution or access permit. Static activation
continues to refuse managed candidates. General takeover/unknown-effect recovery
and background probing remain separate work.

## Acceptance and evidence

Pure response tests cover false/true, reordering, duplicate/unknown/missing fields,
wrong types/protocol/schema, oversized bodies and trailing input. Linux socket
tests cover peer/permissions, redirect refusal, malformed/slow responses and fixed
credential-free requests. Metadata/opaque-JSON tests cannot prove native readiness.
Local SQLite and PostgreSQL candidate/data regressions exercise the shared intent
loader; actual positive readiness settlement currently uses the Linux SQLite
Portal fixture, so it is not full PostgreSQL end-to-end readiness acceptance.

The quota workflow builds a minimal Go HTTP-over-Unix backend from this source,
packages it in a scratch image and uses a newly owned localhost-only registry to
obtain the real immutable repository digest. It does not publish that image or
use account credentials. The registry is bounded, labelled with this workflow's
identity and deleted only after verifying its exact container ID and label.

The non-root Portal child installs/starts that signed backend through the actual
root runner, retains prior failed-start-settlement recovery assertions, and probes
the application until the bounded fixture deadline. It must persist readiness and
reach `activate` with candidate/pending/no use grants, reject static activation
and reject a stale repeat. The parent requires `MANAGED_INSTALL_READINESS`; only
a passing parent test permits `native_portal_managed_backend_readiness: true`.
Native workflow evidence is pending until this functional source passes.

This is partial RUN-04 and LIFE-02/03 coverage. It does not complete crash-loop
control, continuous health, full isolation or the entire installation lifecycle.
The existing general CI x/net vulnerability gate remains separately tracked.

## Remaining work

Complete managed registration/broker/writer authorization and actual activation,
automatic verified image fetching, bounded socket storage, managed scheduling and
runtime startup, native reconciliation and update/rollback/uninstall. Then verify
neutral reference apps, SDK/market distribution and the full acceptance matrix.
No production deployment, real credentials, server beta configuration, module/
toolchain changes, Status migration or public SDK freeze belongs to this phase.
