# Proposal: background broker egress

Status: accepted 2026-11-17; implementation pending. No code, schema or
contract has changed yet. This is the first of eight proposals (174–181) that close the
platform gaps blocking the Status application on the v2 platform. Nothing here
authorizes production deployment, a stable SDK freeze or a Status release.

## The eight proposals

Implement in this order. A proposal may start once every proposal in its
"depends on" column has landed.

| Doc | Capability | Depends on | Schema change |
| --- | --- | --- | --- |
| 174 | Background broker egress (`network.http` and notifications from a backend) | — | None |
| [175](175-reviewed-multi-origin-and-private-network.md) | Reviewed multi-origin and private-network targets | 174 | None (policy format revision) |
| [176](176-background-credential-use.md) | `secrets.use` under the background identity | 174 | None (binding policy format revision) |
| [177](177-provider-request-signing.md) | Broker-side request signing, Alibaba ACS3 first | 176 | Yes: `app_platform_secret_bindings.operation` CHECK (migration `0022`) |
| [178](178-postgresql-liveness-probe.md) | PostgreSQL liveness probe as a restricted stream capability | 174, 175, 176 | Yes: `app_platform_secret_bindings.operation` CHECK (migration `0022`) |
| [179](179-public-projection.md) | Anonymous reviewed read-only public projection | — | None |
| [180](180-authenticated-ingress.md) | Authenticated inbound ingress for heartbeats | — | Yes: `app_platform_secret_bindings.operation` CHECK (migration `0022`) |
| [181](181-principal-directory.md) | Principal directory for app grant pickers | — | None |

The owner approved the schema changes in 177, 178 and 180 together on
2026-11-17. They widen the same CHECK and ship as one forward migration, `0022`
(the next free ID after `0021`), that adds `acs3_hmac_sha256`, `pg_password`
and `ingress_hmac_sha256` at once. Every proposal also revises contracts (policy formats,
descriptor capabilities, broker and runner command formats); those need owner
sign-off as contracts, but they are not database changes. 179, 180 and 181 do not depend on 174 and
can proceed in parallel with it; enabling their capabilities *after* install
uses the capability policy review defined in 175 §3.4, while selection at
install needs nothing from 175.

The owner decided on 2026-11-17 that no Status v2 release happens until
background probes work. By this plan, unauthenticated HTTP probes need 174 and
175; Dify and DeepSeek probes also need 176; Qwen needs 177; PostgreSQL needs
178; the public page needs 179; heartbeats need 180; per-OU grant editing needs
181.

## 1. Problem and evidence

Portal paths in 174–181 refer to `main` at `0d468eb0`. Paths prefixed "Apps"
refer to the Apps repository, branch `feat/status-platform-v2` at `9cabde0`.

A managed backend has no route to any network, by design:

- The container runs with `NetworkDisabled: true` and `NetworkMode = "none"`,
  and its only environment values are the two socket paths
  (`internal/apprunner/driver/container.go:384-388`).
- `PORTAL_APP_BROKER_SOCKET=/run/portal-app/broker/broker.sock` is advertised,
  and the per-installation/generation broker directory is bind-mounted
  read-only (`container.go:385`, `:410`). Nothing listens on that socket: the
  directory only receives `registration.json`
  (`internal/apprunner/driver/backend_registration_linux.go:17-21`, `:79`).
- The only outbound executor is the runner's `broker.HTTP`
  (`internal/apprunner/broker/http.go:84-208`), reached only through a
  Portal-signed `NetworkHTTPRequestCommand` whose `actor_scope` is a digest of
  the current *viewer* (`internal/app/app_platform_network_request.go:226`).
  The Portal entry point is session-only
  (`internal/app/routes.go:525-526`).
- Every Portal↔runner connection is opened by Portal (the non-root
  controller); the runner is the root listener and asks for online
  confirmation on the same connection (`internal/apprunner/transport/network_linux.go:22`, `:45`;
  doc [134](134-private-network-controller-transport.md)). The runner cannot
  call Portal on its own.
- The background notification route exists
  (`routes.go:528`, handler `internal/app/app_platform_notifications.go:127`)
  and takes an H07 service assertion in `X-Portal-Service-Identity`
  (`:137`). A backend cannot reach Portal HTTP, so it cannot use it.
- The H07 resource set already includes `network.http` and
  `notifications.publish` (`internal/app/app_platform_service_identity.go:25-32`);
  assertions live 30 s to 5 min (`:17`, `:202`;
  `internal/appplatform/service_identity.go:65`).

Status runs its own probe loop (minimum interval 30 s,
Apps `apps/status/service/internal/status/model.go:98`) and today wires
`NoEgress`, reporting every probe `unknown/egress_unavailable`
(Apps `.../status/egress.go:1-26`, `probes.go:249-251`). Its outbox cannot
deliver either. Doc [01](01-architecture.md) §4 and §7 already require broker
egress and "explicitly granted background service calls"; this proposal
specifies them.

## 2. Threat model

Assets: the host and its private networks, other installations' quotas,
Portal's notification pipeline, administrator intent expressed in reviews.

| Threat | Control |
| --- | --- |
| Backend dials anything directly | Unchanged `network=none`; the broker socket is the only channel |
| Backend impersonates another installation | One listener per installation/generation directory; the runner binds identity from the socket path it created, never from request bytes |
| Backend escalates beyond its grants | Portal signs each call only after re-checking H07 authority, `background.run`, `network.http` and the exact reviewed policy |
| SSRF, DNS rebinding, redirects, metadata | The same `broker.HTTP` executor and `ValidateHTTPPolicyTarget`; all resolved addresses must pass `publicAddress` (`http.go:128-138`, `:232-251`); dial pinned to the checked address; redirects never followed |
| Offline authority after revocation | Online authority check before lookup, before dial and before release (`http.go:125`, `:140`, `:204`); session closed on any drift |
| Background flood starves viewers or the host | Per-installation concurrency and token-bucket budget; background may hold at most `active/2` of the pool's slots (`network_pool.go:15`) |
| Notification spoofing | Namespace derived from the verified app; closed input, unchanged from doc [152](152-app-notification-publishing.md) |
| Audit flood | Aggregated audit, refusals rate-limited per installation |

Out of scope: a compromised runner (root) or Portal process; kernel escape.

## 3. Contract

### 3.1 App-facing broker protocol `portal.broker/1`

HTTP/1.1 over `$PORTAL_APP_BROKER_SOCKET`. JSON request and response bodies,
no keep-alive requirement, `Content-Type: application/json`. The app sends no
identity: the socket is the identity. Any route other than the three below
answers 400 `invalid`.

```text
POST /v1/network/http
POST /v1/notifications
GET  /v1/status
```

`POST /v1/network/http` request, closed object, request ≤ 128 KiB on the wire.
The cap equals `platformNetworkHTTPBodyLimit`
(`internal/app/app_platform_network_http.go:21-23`), which already bounds the
canonical base64 body plus the JSON metadata:

```json
{"requestId":"<uuid>","method":"GET","url":"https://api.example.com/v1/x",
 "accept":"application/json","contentType":"","bodyBase64":""}
```

The app's `requestId` is for correlation only: the broker echoes it in logs and
never signs it. Portal mints the `RequestID` inside the signed
`NetworkHTTPRequestCommand`. The method set and the strict base64 decoder
move into `appplatform`, shared with the foreground decoder
(`app_platform_network_http.go:70-76`).

Decoded body ≤ the policy's `request_bytes` (≤ 65,536). Success:

```json
{"status":200,"contentType":"application/json","bodyBase64":"...","retryAfterSeconds":null}
```

`status` is the upstream 2xx/4xx/5xx status, exactly as the executor releases
it today (`http.go:186`); 1xx and 3xx are `uncertain`. `retryAfterSeconds` is
new: an integer 0–86,400 parsed from a single `Retry-After` header on 429 or
503, otherwise `null`. The executor reports it as
`HTTPResponse.RetryAfterSeconds` (−1 when absent); a malformed, repeated or
out-of-range header yields `null` and never turns a released response into
`uncertain`. No other upstream header is released. Status needs it
(Apps `.../status/probes.go:305`, `:333`).

`POST /v1/notifications` request is exactly the closed doc 152 input:
`occurrenceId`, `title`, `message`, `severity`. Success returns
`{"eventId":"...","created":true|false}`.

`GET /v1/status` returns `{"egress":"available"|"unavailable","resources":[...]}`
so an app can report `egress_unavailable` without spending a call.

Errors are `{"error":"<code>","retryable":false}` with HTTP status:

| Code | HTTP | Meaning |
| --- | --- | --- |
| `invalid` | 400 | Malformed or unknown field, or unknown route; never sent upstream |
| `denied` | 403 | Target, method, path, size or grant refused |
| `unavailable` | 503 | No current egress session, session draining or closed, restore pause, runner shutdown |
| `busy` | 429 | Concurrency or rate budget exhausted; `Retry-After` set (`1` for concurrency) |
| `uncertain` | 502 | The request may have reached upstream; no body released |
| `conflict` | 409 | Notification occurrence reused with different bytes |
| `capacity` | 429 | Notification source capacity (existing doc 152 class) |

The runner maps conditions to codes as follows: `broker.ErrDenied` to
`denied`, `broker.ErrUnavailable` to `unavailable`, the new `broker.ErrBusy`
(which wraps `ErrUnavailable`, so existing callers keep treating it as
unavailable) to `busy`, `broker.ErrUncertain` to `uncertain`; no session, or a
session that is draining or closed, is `unavailable`.

`retryable` is always `false` in the body; the app owns retry policy. The
broker never retries.

### 3.2 Portal-held egress session

The runner can execute background work only while Portal holds an *egress
session* for that installation open. A new Portal coordinator (decided: a
goroutine in `RunPlatformTaskWorker`,
`internal/app/app_platform_task_worker.go:152-181`, which already issues H07
assertions, `internal/app/app_platform_task_store.go:564`; it is not started
under `--once`, and only one coordinator may run) does, per eligible
installation:

1. In one read transaction, require: desired state enabled, phase ready, no
   restore pause, current controller epoch, active release and managed runtime,
   `background.run` granted, and at least one of `network.http` or
   `notifications.publish` granted.
2. Issue an H07 assertion for exactly the granted subset of
   `{network.http, notifications.publish}` with a 5-minute lifetime. The
   assertion's `lease_id` is the session ID.
3. Query the runner's egress capability (below), then open a new controller
   connection, family magic `PAEGRS01`, carrying one signed
   `portal.service-egress-session/1-dev.1` command.

**Capability query.** The runner network profile gains no field: Portal
compares the live profile to the stored plan with `==`
(`app_platform_network_request.go:380`, `:444`;
`internal/apprunner/transport/network_runtime.go:101`), so any new profile
field would break foreground for every installation. Egress capability is a
separate query instead. Portal sends `PAEGRV01` plus a 32-byte nonce; the
runner replies `PAEGRW01`, the nonce, a u32 length and closed canonical JSON:

| Field | Value |
| --- | --- |
| `format` | `portal.runner.service-egress-profile/1-dev.1` |
| `session_format` | `portal.service-egress-session/1-dev.1` |
| `broker_format` | `portal.broker/1` |
| `background_active` | `active/2` of the runner's network pool; `0` means egress is disabled |
| `rate_per_minute`, `burst` | 120 and 20 |
| `retry_after_reply` | `true` when the runner understands the foreground `PRNQ0002`/`PRNR0002` frames (§6) |

A runner that predates this query closes the connection at the unknown-magic
`default` case (`internal/apprunner/transport/transport_linux.go:398`); Portal
treats that EOF as "no egress" and opens no session. The coordinator skips such
runners.

**Session command.** Defined in `internal/apprunner/control/service_egress.go`,
framed like `network_http.go`: strict object, canonical re-marshal,
`frame(purpose, raw)`, purpose `portal.app/runner-service-egress-session/v1`.

| Field | Rule |
| --- | --- |
| `format`, `deployment_id`, `controller_epoch`, `control_revision` | As in `NetworkHTTPRequestCommand` (`internal/apprunner/control/network_http.go:24-49`) |
| `session_id` | Fresh UUID, equal to the H07 `lease_id` |
| `installation_id`, `generation`, `policy_revision`, `release_sha256` | Current tuple |
| `service_identity_sha256` | SHA-256 of the canonical H07 assertion bytes |
| `resources` | Sorted subset of `network.http`, `notifications.publish` (later `secrets.use`, doc 176) |
| `concurrency` | `"1"`..`"4"`, ≤ `policy.concurrency` (`internal/appplatform/http_policy.go:24`) |
| `rate_per_minute`, `burst` | Exactly `"120"` and `"20"` |
| `registration_raw`, `registration_signature` | The backend registration proof, as in `ServiceBackendTaskCommand` (`internal/apprunner/control/service_backend_task.go:20-35`) |
| `issued_ms`, `expires_ms` | `expires_ms` ≤ assertion expiry; 60,000 ≤ `expires_ms` − `issued_ms` ≤ 300,000 |

The runner verifies the enrolled signature and the registration proof, and
checks the installation/generation against its own journal and running
container. The open frame is `PAEGRS01` | nonce[32] | u32 length | command |
signature[64] | the candidate descriptor frame; every length is checked before
allocation. The runner answers `PAEGRA01` | SHA-256 of the open frame[32] |
status u8 (1 open, 2 refused). A second session for the same installation
replaces the first only after it is verified, under the registry mutex; the
old session then drains, and at most one is current.

**Session lifetime.** A `PAEGRS01` session is detached from the runner's Serve
loop. Serve allows 8 concurrent connections
(`transport_linux.go:223`); an egress session would hold one of those slots for
up to five minutes, so Serve hands the connection to the runner's workers and
frees its slot. Detached sessions have their own bound of 64. The broker
socket (`/run/portal-app/broker/broker.sock`, root-owned, mode 0666 inside the
root-owned 0755 directory, bound inside the pinned directory fd; the directory
stays read-only in the container) is created by the first session and then
persists until a newer generation, a terminal close or runner shutdown. Between
sessions it answers 503 `unavailable`; before the first session a connect fails
and the SDK maps that to `unavailable`. A stale socket is unlinked only if it
is root-owned with a link count of 1, and the runner re-checks the path
identity and the peer uid (65532) on every command as defence in depth.

**Managed installs.** A generation-1 managed install that is not writable gets
notification-only sessions: the foreground grant loader already refuses
network grants for the initial install target
(`app_platform_network_request.go:84-115`), so `resources` is limited to
`notifications.publish` and `network.http` is never signed.

**Frames.** After the acknowledgement, every frame is
`type u8 | 0,0,0 | call_id[16] | u32 length | payload`, bounded exactly like
doc 134 (lengths checked before allocation; payload ≤ `MaxCommandBytes` + 1,024).

| Type | Frame | Direction | Content |
| --- | --- | --- | --- |
| 0x01 | `admit` | runner → Portal | closed JSON `{method,url,accept,contentType,bodyBytes,bodySha256}`, ≤ 8 KiB |
| 0x02 | `command` | Portal → runner | u32 length, signed `NetworkHTTPRequestCommand`, signature[64] |
| 0x03 | `refusal` | Portal → runner | u8: 1 invalid, 2 denied, 3 unavailable, 5 busy, 6 conflict, 7 capacity |
| 0x04 | `challenge` | runner → Portal | seq u8, nonce[32] (the existing per-checkpoint confirmation, `internal/apprunner/broker/network_confirmation.go`) |
| 0x05 | `proof` | Portal → runner | u32 length, proof, signature[64]; ≤ `MaxNetworkHTTPCheckBytes` |
| 0x06 | `notice` | runner → Portal | one frame carrying the closed doc 152 JSON, ≤ 16 KiB |
| 0x07 | `notice-result` | Portal → runner | `{"eventId","created"}`, ≤ 256 B |
| 0x08 | `outcome` | runner → Portal | class u8, status u16, response bytes u32, retryAfter i32 (−1 = none), one per finished call |
| 0x09 | `close` | either | reason u8 |

An unknown type, an over-length frame, a duplicate or unknown `call_id`, or an
out-of-order `seq` closes the session. The runner writes through one write
mutex, with per-write deadlines only: it never sets a deadline on the shared
connection.

For `admit`, Portal reloads current H07 authority (doc 151 "Current
authority"), loads the grant with the existing `loadPlatformNetworkGrant`
(`app_platform_network_request.go:61-143`, split so the intent-independent part
is reusable), validates the target, and signs a `NetworkHTTPRequestCommand`
with a fresh Portal-minted `RequestID`, no `Credential*` fields, an
`ExpiresMS` no later than the assertion's expiry, and `actor_scope` equal to
`SHA-256("portal-network-service/v1:" + session_id)` instead of the viewer
digest. Portal refuses an admit when the assertion has under 31 s left. Before
executing, the runner checks that the command's tuple and actor scope match the
session and that method, URL, accept, content type, body length and SHA-256
match the `admit`. The call then runs through the shared `NetworkPool`; every
existing executor check applies unchanged, and each of the four per-checkpoint
confirmations re-runs the Portal authority load. The `outcome` frame feeds the
summary audit (§5). A `notice` runs the same capture function as
`apiPlatformServiceNotificationPublish` in-process, using the session's
assertion; no HTTP hop.

The coordinator ticks every 5 s and renews 60 s before expiry by opening the
replacement session; overlap is at most 60 s. It closes the session on any
drift it observes; the runner closes it on expiry, epoch change, container
stop or connection loss. A call in flight when its session closes returns
`uncertain`. No session means every broker call returns `unavailable`
immediately.

### 3.3 Limits

| Limit | Value |
| --- | --- |
| Request body / response body / deadline | From the reviewed policy; ≤ 64 KiB / 1 MiB / 15 s (`http_policy.go:21-23`) |
| Concurrent calls per installation | Session `concurrency` (1–4), one semaphore acquired without blocking, shared with foreground (`network_pool.go:146-154`); refusal is `busy` with `Retry-After: 1` |
| Rate per installation | 120 per minute, burst 20; one token bucket keyed by installation, not session, shared by HTTP calls and notices |
| Background share of the network pool | `active/2`, where the runner's active slots are 1–8 (`network_pool.go:15`); a share of 0 disables egress |
| Detached egress sessions per runner | 64, separate from Serve's 8 connections |
| Broker socket connections per installation | 16 open, `ReadHeaderTimeout` 5 s, `MaxHeaderBytes` 8192, `WriteTimeout` 25 s |
| Notifications | Existing doc 152 global and per-source capacity |

## 4. Review and consent

No new review. The administrator already reviews `background.run`,
`network.http` (with its exact policy) and `notifications.publish` at install
(doc [140](140-reviewed-network-installation.md)). The install review page must
add one sentence when both `background.run` and `network.http` are selected:
"This app can call the reviewed destinations while no user is signed in."
Declining optional `network.http` keeps background egress off.

## 5. Failure and audit

- Executor outcomes keep their classes: `denied` before any dial, `unavailable`
  before dial, `uncertain` after a possible upstream effect. Nothing is retried
  and an uncertain call never yields a body.
- All rows use actor `system` and target type `app_platform_installation`.
  Session open, replacement and close write one row each:
  `app_platform.service_egress_opened` (session ID, replaced session ID,
  resources, generation, policy revision, release SHA-256, concurrency) and
  `app_platform.service_egress_closed` (session ID and a reason: expired,
  renewed, drift, restore_pause, epoch, disabled, connection_lost, refused,
  shutdown).
- Calls are aggregated from the `outcome` frames: one
  `app_platform.service_egress_summary` row per installation per 10 minutes
  with counts by class, bytes out, bytes in and the sorted set of origins
  used. Refusals by policy (`denied`) also produce individual
  `app_platform.service_egress_denied` rows, at most 10 per installation per
  10 minutes, then counted; they carry method, origin and session ID, never the
  path or query.
- Diagnostics: the admin installation settings response
  (`internal/app/routes.go:491`) gains `serviceEgress {lastSession,
  lastSummary}`, read from `audit_log`.

## 6. Migration and compatibility

Additive. Apps that never open the broker socket see no change. The foreground
network route is unchanged until the last step below. Apps call the broker
through a new SDK helper (`packages/app-sdk` Go and TypeScript service
helpers); Status replaces `NoEgress` with a broker-backed `Egress` whose
`Available()` reads `GET /v1/status`, and replaces `probeTransport`
dial/lookup with broker calls. Portal never opens a session against a runner
that fails the `PAEGRV01` query (§3.2).

Foreground `retryAfterSeconds` (owner decision 3) ships last, after the
background path works. The runner network profile and the existing
`PRNQ0001`/`PRNR0001` frames stay byte-for-byte unchanged. A runner whose
capability reply says `retry_after_reply` also accepts the versioned frames
`PRNQ0002` and `PRNR0002`, which add one i32 (−1 = none); Portal sends
`PRNQ0002` only to such a runner. On the web side, `parseNetworkResponse`
(`web/src/lib/network-v2.ts:77-78`) now requires exactly three keys and must
accept three or four, and the Apps SDK changes in lockstep.

## 7. Test plan (these must fail first)

1. Before the first session, a backend fixture connecting to `broker.sock`
   fails to connect today and the SDK maps that to `unavailable`; after a
   session closes the same connection gets `503 unavailable` JSON.
2. Session opened with a session tuple (installation, generation, epoch,
   release or registration proof) that differs from the runner journal:
   refused, no socket created. A command signed for another installation is
   refused by the runner.
3. A request to a reviewed origin succeeds; the same request after revoking
   `network.http`, disabling, uninstalling or rotating the epoch returns
   `denied` or `unavailable`, and a call in flight returns `uncertain` with no
   body. Portal refuses an admit when the assertion has under 31 s left.
4. DNS answers containing a private, loopback, link-local or denied-prefix
   address: `denied` before dial (reuse the `http_test.go` resolver fixtures).
5. Redirect, compressed body, oversize body and credential echo: `uncertain`.
   `Retry-After` is released only on 429 and 503 and never makes an outcome
   `uncertain`.
6. Rate and concurrency on installation A: a 121st call within a minute
   returns `429 busy`, with notices counting against the same bucket; five
   concurrent calls with `concurrency=4` yield exactly one `busy` with
   `Retry-After: 1`. Background holds at most `active/2` pool slots; with
   `active=1` the share is 0 and the capability reply says egress is disabled.
   Foreground and background share installation A's concurrency, so "a
   foreground call still gets a slot while background holds its share" is
   asserted on installation B.
7. Two installations: a request on installation A's socket can never be
   signed with installation B's tuple (path-derived identity); the peer uid is
   enforced.
8. Notification through the broker: one `notice` frame, idempotent replay,
   conflict, namespace fixed to the app, unknown-field refusal, no HTTP handler
   involved.
9. Restore pause: sessions close, new ones refuse, broker returns
   `unavailable`.
10. Runner wiring: a detached session does not hold a Serve slot; the 65th
    concurrent session is refused; `PAEGRV01` returns the profile, an old
    runner's EOF is treated as no egress, and narrow constructors leave egress
    disabled.
11. A generation-1 managed install that is not writable gets a
    notification-only session and no `network.http` resource.
12. The `outcome` frame produces the summary audit row; the settings response
    carries `serviceEgress {lastSession, lastSummary}`.
13. Foreground `retryAfterSeconds`: `PRNQ0002` is used only when the query says
    `retry_after_reply`; existing `PRNQ0001`/`PRNR0001` and the three-key web
    check keep passing; the web parser accepts three or four keys.
14. Native: the Scheduled Digest fixture gains a broker probe to a disposable
    TLS upstream on SQLite and PostgreSQL.

## 8. Schema change

None. Sessions and budgets are in-memory runner state bound to a signed,
expiring command; authority is re-derived from existing rows on every call.
Audit uses the existing `audit_log`. The new formats are contract additions
(`portal.broker/1`, `portal.service-egress-session/1-dev.1`, the `PAEGRV01`
query with the `portal.runner.service-egress-profile/1-dev.1` reply, and later
the `PRNQ0002`/`PRNR0002` foreground frames) and need owner sign-off as
contracts, not as schema.

## 9. Owner decisions (2026-11-17)

The owner decided every question below on 2026-11-17.

1. Where the coordinator runs. Decided: `cmd/app-task-worker`, which already
   owns H07 issuance and runner connections; the Portal web process stays free
   of long-lived controller sessions.
2. Fixed or reviewed rate budget. Decided: fixed 120/min, burst 20 for this
   release; Status at 50 monitors x 30 s needs 100/min. Revisit with the
   capacity envelope (doc [171](171-capacity-envelope-plan.md)).
3. Whether to release `retryAfterSeconds` on the foreground route too. Decided:
   yes, same parser, same bound; shipped last, as versioned frames (see the
   amendments below).
4. Whether `tasks/background` is also reachable through the broker so a backend
   can schedule its own tasks. Decided: yes, as a follow-up using the same
   session frames; not needed by Status.

### Amendments adopted 2026-11-17

The owner adopted these corrections to the first draft on 2026-11-17. §§3, 5, 6
and 7 above already describe the corrected design. There is still no schema
change.

1. F1: capability is the separate `PAEGRV01` query, not a network-profile
   field, because the live profile is compared to the stored plan with `==`
   (`app_platform_network_request.go:380`, `:444`; `network_runtime.go:101`)
   and a new field would break foreground for every installation; an old runner
   hits EOF at `transport_linux.go:398` and Portal treats that as no egress.
2. F2: egress sessions are detached with their own bound of 64, because Serve
   allows only 8 concurrent connections (`transport_linux.go:223`) and a
   five-minute session would otherwise occupy a slot.
3. F3: the listener lives from the first session until a newer generation, a
   terminal close or shutdown, answering 503 between sessions, so the app sees
   `unavailable` rather than a flapping socket.
4. F4: the session command carries `registration_raw` and
   `registration_signature`, as `control/service_backend_task.go:20-35` does,
   so the runner verifies the backend registration itself; the mismatch test
   becomes a session-tuple mismatch.
5. F5: foreground and background share the per-installation concurrency
   (`network_pool.go:146-154`), so the "foreground still gets a slot" test uses
   installation B.
6. F6: the background share is `active/2` with `NetworkActive` between 1 and 8,
   and a share of 0 disables egress, so `active=1` cannot starve foreground.
7. F7: a new `broker.ErrBusy` wraps `ErrUnavailable`, so existing callers
   treat it as unavailable while the broker can answer `busy`.
8. F8: a per-call `outcome` frame (class, status, response bytes, retryAfter)
   goes runner to Portal, because Portal otherwise cannot build the summary
   audit.
9. F9: diagnostics are `serviceEgress {lastSession, lastSummary}` in the admin
   installation settings response (`routes.go:491`), read from `audit_log`,
   rather than a new view.
10. F10: foreground `retryAfterSeconds` ships last, as versioned
    `PRNQ0002`/`PRNR0002` frames gated on the capability query, because the web
    check at `web/src/lib/network-v2.ts:78` accepts exactly three keys and the
    Apps SDK must change in lockstep.
11. F11: the request cap is 128 KiB, equal to `platformNetworkHTTPBodyLimit`
    (`app_platform_network_http.go:21-23`), not 96 KiB, which would reject
    requests the foreground route accepts.
12. F12: the app's `requestId` is correlation only and Portal mints the signed
    `RequestID`; notices share the rate bucket; `session_id` equals the H07
    `lease_id`; a notice is one `notice` frame. Each keeps one identity and one
    budget per installation.
13. F13: a generation-1 managed install that is not writable gets
    notification-only sessions (`app_platform_network_request.go:84-115`),
    because no network grant exists for the initial install target.
