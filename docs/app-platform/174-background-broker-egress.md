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
| Background flood starves viewers or the host | Per-installation concurrency and token-bucket budget; background may hold at most half of the pool's 8 slots (`network_pool.go:15`) |
| Notification spoofing | Namespace derived from the verified app; closed input, unchanged from doc [152](152-app-notification-publishing.md) |
| Audit flood | Aggregated audit, refusals rate-limited per installation |

Out of scope: a compromised runner (root) or Portal process; kernel escape.

## 3. Contract

### 3.1 App-facing broker protocol `portal.broker/1`

HTTP/1.1 over `$PORTAL_APP_BROKER_SOCKET`. JSON request and response bodies,
no keep-alive requirement, `Content-Type: application/json`. The app sends no
identity: the socket is the identity.

```text
POST /v1/network/http
POST /v1/notifications
GET  /v1/status
```

`POST /v1/network/http` request, closed object, request ≤ 96 KiB on the wire:

```json
{"requestId":"<uuid>","method":"GET","url":"https://api.example.com/v1/x",
 "accept":"application/json","contentType":"","bodyBase64":""}
```

Decoded body ≤ the policy's `request_bytes` (≤ 65,536). Success:

```json
{"status":200,"contentType":"application/json","bodyBase64":"...","retryAfterSeconds":null}
```

`status` is the upstream 2xx/4xx/5xx status, exactly as the executor releases
it today (`http.go:186`); 1xx and 3xx are `uncertain`. `retryAfterSeconds` is
new: an integer 0–86,400 parsed from a single `Retry-After` header on 429 or
503, otherwise `null`. No other upstream header is released. Status needs it
(Apps `.../status/probes.go:305`, `:333`).

`POST /v1/notifications` request is exactly the closed doc 152 input:
`occurrenceId`, `title`, `message`, `severity`. Success returns
`{"eventId":"...","created":true|false}`.

`GET /v1/status` returns `{"egress":"available"|"unavailable","resources":[...]}`
so an app can report `egress_unavailable` without spending a call.

Errors are `{"error":"<code>","retryable":false}` with HTTP status:

| Code | HTTP | Meaning |
| --- | --- | --- |
| `invalid` | 400 | Malformed or unknown field; never sent upstream |
| `denied` | 403 | Target, method, path, size or grant refused |
| `unavailable` | 503 | No current egress session, restore pause, runner shutdown |
| `busy` | 429 | Concurrency or rate budget exhausted; `Retry-After` set |
| `uncertain` | 502 | The request may have reached upstream; no body released |
| `conflict` | 409 | Notification occurrence reused with different bytes |
| `capacity` | 429 | Notification source capacity (existing doc 152 class) |

`retryable` is always `false` in the body; the app owns retry policy. The
broker never retries.

### 3.2 Portal-held egress session

The runner can execute background work only while Portal holds an *egress
session* for that installation open. A new Portal coordinator (recommended:
a loop in the existing `cmd/app-task-worker` process, which already issues H07
assertions, `internal/app/app_platform_task_store.go:564`) does, per eligible
installation:

1. In one read transaction, require: desired state enabled, phase ready, no
   restore pause, current controller epoch, active release and managed runtime,
   `background.run` granted, and at least one of `network.http` or
   `notifications.publish` granted.
2. Issue an H07 assertion for exactly the granted subset of
   `{network.http, notifications.publish}` with a 5-minute lifetime.
3. Open a new controller connection, family magic `PAEGRS01`, sending one
   signed `portal.service-egress-session/1-dev.1` command:

| Field | Rule |
| --- | --- |
| `format`, `deployment_id`, `controller_epoch`, `control_revision` | As in `NetworkHTTPRequestCommand` (`internal/apprunner/control/network_http.go:24-49`) |
| `session_id` | Fresh UUID |
| `installation_id`, `generation`, `policy_revision`, `release_sha256` | Current tuple |
| `service_identity_sha256` | SHA-256 of the canonical H07 assertion bytes |
| `resources` | Sorted subset of `network.http`, `notifications.publish` (later `secrets.use`, doc 176) |
| `concurrency` | 1..`policy.concurrency` (≤ 4, `internal/appplatform/http_policy.go:24`) |
| `rate_per_minute`, `burst` | Platform constants: 120 and 20 |
| `issued_ms`, `expires_ms` | `expires_ms` ≤ assertion expiry |

The runner verifies the enrolled signature, the installation/generation
against its own journal and running container, and opens
`/run/portal-app/broker/broker.sock` for that generation if not yet open
(root-owned, mode 0666 inside the root-owned 0755 directory; the directory
stays read-only in the container). A second session for the same installation
replaces the first only after it is verified; at most one is current.

On the open connection the frames are length-prefixed and bounded exactly like
doc 134 (lengths checked before allocation, 64 KiB command bound):

| Frame | Direction | Content |
| --- | --- | --- |
| `admit` | runner → Portal | request ID, kind (`http` or `notice`), method, URL, accept, content type, body length and SHA-256 |
| `command` | Portal → runner | signed `NetworkHTTPRequestCommand` and signature, or a refusal code |
| `challenge` / `proof` | both | the existing per-checkpoint confirmation (`internal/apprunner/broker/network_confirmation.go`) |
| `notice` | runner → Portal | the closed notification body |
| `notice-result` | Portal → runner | event ID and `created`, or a refusal code |
| `close` | either | reason code |

For `admit`, Portal reloads current H07 authority (doc 151 "Current
authority"), loads the grant with the existing `loadPlatformNetworkGrant`
(`app_platform_network_request.go:61-143`), validates the target, and signs a
`NetworkHTTPRequestCommand` whose `actor_scope` is
`SHA-256("portal-network-service/v1:" + lease_id)` instead of the viewer
digest. The runner executes it through the shared `NetworkPool`; every
existing executor check applies unchanged. A `notice` runs the same capture
function as `apiPlatformServiceNotificationPublish` in-process, using the
session's assertion; no HTTP hop.

The coordinator renews 60 s before expiry by opening the replacement session;
overlap is at most 60 s. It closes the session on any drift it observes;
the runner closes it on expiry, epoch change, container stop or connection
loss. No session means every broker call returns `unavailable` immediately.

### 3.3 Limits

| Limit | Value |
| --- | --- |
| Request body / response body / deadline | From the reviewed policy; ≤ 64 KiB / 1 MiB / 15 s (`http_policy.go:21-23`) |
| Concurrent background calls per installation | Session `concurrency` |
| Rate per installation | 120 calls per minute, burst 20, token bucket in the runner |
| Background share of the network pool | At most 4 of 8 active slots |
| Broker socket connections per installation | 16 open, 5 s header read deadline |
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
- Session open, replacement and close write one `audit_log` row each:
  `app_platform.service_egress_opened` / `_closed`, with installation,
  session ID, resources and close reason.
- Calls are aggregated: one `app_platform.service_egress_summary` row per
  installation per 10 minutes with counts by outcome, bytes in/out and the
  sorted set of origins used. Refusals by policy (`denied`) also produce
  individual rows, at most 10 per installation per 10 minutes, then counted.
- The existing per-installation diagnostic view gains the last session state
  and the last summary.

## 6. Migration and compatibility

Additive. Apps that never open the broker socket see no change. The foreground
network route is unchanged. Apps call the broker through a new SDK helper
(`packages/app-sdk` Go and TypeScript service helpers); Status replaces
`NoEgress` with a broker-backed `Egress` whose `Available()` reads
`GET /v1/status`, and replaces `probeTransport` dial/lookup with broker calls.
The runner network profile advertises `service_egress_format` so Portal never
opens a session against a runner that lacks it.

## 7. Test plan (these must fail first)

1. A backend fixture connecting to `broker.sock` gets `ECONNREFUSED` today;
   the test asserts a `503 unavailable` JSON reply when no session exists.
2. Session opened with an assertion whose installation, generation, epoch or
   release differs from the runner journal: refused, no socket created.
3. A request to a reviewed origin succeeds; the same request after revoking
   `network.http`, disabling, uninstalling or rotating the epoch returns
   `denied` or `unavailable`, and a call in flight returns `uncertain` with no
   body.
4. DNS answers containing a private, loopback, link-local or denied-prefix
   address: `denied` before dial (reuse the `http_test.go` resolver fixtures).
5. Redirect, compressed body, oversize body and credential echo: `uncertain`.
6. A 121st call within a minute returns `429 busy`; five concurrent calls with
   `concurrency=4` yield exactly one `busy`; a foreground call still gets a
   pool slot while background holds 4.
7. Two installations: a request on installation A's socket can never be
   signed with installation B's tuple (path-derived identity).
8. Notification through the broker: idempotent replay, conflict, namespace
   fixed to the app, unknown-field refusal, no HTTP handler involved.
9. Restore pause: sessions close, new ones refuse, broker returns
   `unavailable`.
10. Native: the Scheduled Digest fixture gains a broker probe to a disposable
    TLS upstream on SQLite and PostgreSQL.

## 8. Schema change

None. Sessions and budgets are in-memory runner state bound to a signed,
expiring command; authority is re-derived from existing rows on every call.
Audit uses the existing `audit_log`. The new formats are contract additions
(`portal.broker/1`, `portal.service-egress-session/1-dev.1`, network profile
field `service_egress_format`) and need owner sign-off as contracts, not as
schema.

## 9. Owner decisions (2026-11-17)

The owner decided every question below on 2026-11-17.

1. Where the coordinator runs. Decided: `cmd/app-task-worker`, which already
   owns H07 issuance and runner connections; the Portal web process stays free
   of long-lived controller sessions.
2. Fixed or reviewed rate budget. Decided: fixed 120/min, burst 20 for this
   release; Status at 50 monitors x 30 s needs 100/min. Revisit with the
   capacity envelope (doc [171](171-capacity-envelope-plan.md)).
3. Whether to release `retryAfterSeconds` on the foreground route too. Decided:
   yes, same parser, same bound.
4. Whether `tasks/background` is also reachable through the broker so a backend
   can schedule its own tasks. Decided: yes, as a follow-up using the same
   session frames; not needed by Status.
