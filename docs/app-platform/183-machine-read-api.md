# Proposal: authenticated machine read API for apps (e.g. Dify)

Status: Accepted 2026-11-20; implemented (section 12). Owner decisions are in
section 11; the schema change (section 10) is migration 0023. Builds on [179](179-public-projection.md) (route list, limits,
consent, `pub.` assertion) and [180](180-authenticated-ingress.md) (Portal-side
verification of an external caller). Related: [175](175-reviewed-multi-origin-and-private-network.md),
[182](182-cross-platform-backend-runtime.md).

Evidence is against `main` at `f3ac2120`.

## 1. Problem and evidence

market-monitor's plan is a v2 backend that "serves read endpoints"
(Apps PR #45, `apps/market-monitor/README.md:36-40`). The owner wants Dify, a
server-side workflow engine with no Portal session, to call those query
endpoints with a token that Portal issues. Today nothing admits it:

- **Viewer routes are session-only.** Launch, backend, network, documents and the
  other v2 viewer routes are registered `authSession`
  (`internal/app/routes.go:524-537`). The backend relay requires a viewer
  assertion, or since 179 a `pub.` assertion confined to bodiless GETs under `/v1/public/` (`internal/appplatform/backend_request.go:15`, `:51-58`).
- **AUTH-05 forbids machine tokens on app routes.** "Call as wrong app, wrong
  generation, machine token or forged viewer: refused before platform/business
  data read or side effect" (`docs/app-platform/06-acceptance.md:107`). The
  extension proxy refuses every bearer that is not an app-bound token, including
  `all`-scoped API tokens (`internal/app/extension_api.go:292-305`), and so does
  the principals route (`extension_principals.go:14-18`).
- **The existing machine credential is the wrong shape.** `api_tokens`
  (`internal/app/store.go:689-696`) is Portal's own `/api/v1` credential, and
  Dify already uses it for report reads (`store.go:1728`). It has no
  installation or route binding. Its scope check lets `all` pass any requested
  scope (`store.go:1718-1720`). Its lookup still matches legacy plaintext values
  (`WHERE token_hash=? OR token=?`, `store.go:1709`). The `authBearer` class that
  serves it also admits a browser cookie on reads (`routes.go:33-37`).
- **The app token is ephemeral and viewer-bound.** `appTokens` live only in
  memory, are minted when a user opens an app, carry that user and session, and
  never touch the database (`internal/app/apps_token.go:42-63`).
- **179 and 180 do not fit.** 179 is anonymous: there is no caller identity, a
  response is shared through a public cache, and it sends
  `Access-Control-Allow-Origin: *` (doc 179 section 3.3). Data that is fine for
  Dify may not be fine for the internet. 180 is inbound `POST` of events
  authenticated by an HMAC the sender computes per request (doc 180 sections 1
  and 3). The owner rejected a plain bearer variant there because it is
  replayable (doc 180 decision 1). For a read API, a bearer is the format Dify
  supports natively (section 8). Section 3 bounds its replay risk instead.

## 2. Proposal in one paragraph

A new optional capability `machine.read`, a new descriptor block `machine` that
declares reviewed GET routes under `/v1/machine/`, and a new Portal table of
**machine tokens**. A Portal administrator creates each token for one
installation and a subset of its reviewed routes, with an expiry and rate
limits. The secret is shown once and only its hash is stored. A machine client
presents the token as `Authorization: Bearer` to
`GET /machine/apps/{installation}/data/{route}`. Portal authenticates it,
applies limits, and relays a bodiless GET through the existing signed relay
with a **machine assertion**: prefix `mch.`, its own MAC domain and its own
header, naming the token's ID and label, never its secret. The app sees a
distinct audience: not a viewer, not public. No session, cookie or CORS is
involved.

## 3. Threat model

| Threat | Control |
| --- | --- |
| Token theft (Dify config, logs) | Read-only; bound to one installation and listed routes; expiry required; per-token rate limit; optional source CIDR list; immediate revocation; last-used time shown to the administrator |
| Replay of a captured request | Bearer tokens are replayable for their lifetime by design. The routes are GET-only and side-effect-free by contract (as 179), so a replay reveals only what the holder can already read. TLS is required by deployment (doc 142). Expiry bounds the lifetime. |
| Machine token used as a viewer, or the reverse (AUTH-05) | Distinct token prefix, Portal route, assertion prefix, MAC domain, header and backend path space. The runner refuses every cross-combination (section 5.4). |
| Machine token used against another app | The token row names one installation. Any mismatch gets the same 401 as an unknown token. |
| App exposes more than reviewed | Only routes in both the reviewed policy and the token's list are relayed; the path is fixed by the descriptor; only declared query names are passed |
| Enumeration of installations or tokens | Uniform 401 for a missing, malformed, unknown, revoked, expired or foreign token or an unknown installation, with the same body and timing class, before any per-installation work |
| Flood or amplification | Per-source pre-auth ceiling; per-token per-minute limit; per-installation per-second limit and concurrency; response size bound; no fan-out |
| Browser abuse (CSRF, a page calling with ambient credentials) | No cookie is read, no CORS headers are sent, `OPTIONS` is not served; a browser cannot attach a bearer cross-origin without a preflight that fails |
| Secret disclosure at rest | Only SHA-256 of a 256-bit random secret is stored; the secret is shown once; it is never forwarded to the runner or the app |
| Stale authority after a change | Every request re-reads the token row, the installation state, the grant and the policy, as 179 does |
| Restore resurrecting a revoked token | Revocation floors survive restore (section 6.5) |

## 4. Contract

### 4.1 Capability and descriptor

New capability `machine.read` (optional; requires a backend). New descriptor
block, part of the signed descriptor, parallel to `public`
(`internal/appplatform/descriptor_contract_generated.go:99-112`):

```json
"machine": {
  "routes": [
    {"id":"margin-latest","path":"/v1/machine/margin/latest","query":["date"],"response_bytes":262144,"timeout_ms":5000},
    {"id":"sentiment","path":"/v1/machine/sentiment","query":["from","to"],"response_bytes":1048576,"timeout_ms":10000},
    {"id":"symbol-bars","path":"/v1/machine/bars","query":["from","limit","symbol","to"],"response_bytes":1048576,"timeout_ms":10000}
  ]
}
```

- `routes`: 1..32, sorted by `id` (`^[a-z][a-z0-9-]{0,31}$`). `path` is fixed,
  under `/v1/machine/`, and valid per `ValidBackendRequestRoute`
  (`backend_request.go:21-50`). `query` is 0..16 sorted names
  (`^[a-z][a-z0-9_]{0,31}$`, within the descriptor's existing
  `maxRouteQueryFields` of 16, `descriptor_contract_generated.go:115`).
  `response_bytes` is 1..1,048,576, which is the relay's `MaxBackendResponseBody`
  (`backend_request.go:19`). `timeout_ms` is 100..30,000.
- An optional `description` per route (up to 200 characters) feeds the OpenAPI
  document (section 4.5) so Dify can show it.
- The descriptor schema is the shared contract (the generated file says "edit
  the source contract instead", `descriptor_contract_generated.go:1`), so the
  block lands in the Apps contract first.

### 4.2 Grant policy

The `machine.read` grant row in `app_platform_capability_grants` holds
`portal.machine-read-policy/1-dev.1`: the reviewed route IDs (a subset of the
descriptor's), `installation_per_second` (default 5, max 50) and
`installation_concurrency` (default 4, max 16). It fits the existing table
(`capability` T96, `policy_json` J16384, `app_platform_schema.go:45`), as 179's
policy does (doc 179 section 8).

### 4.3 Tokens

Format: `pmr_` + handle (12 characters, lowercase base32) + `_` + secret
(43 characters, base64url of 32 random bytes). The total length is 60
characters. The `pmr_` prefix makes the token recognisable to secret scanners
and distinct from `api_tokens` values (48 hex digits, `internal/app/server.go:617-621`).

Each token row binds: installation, label (up to 64 characters, chosen by the
administrator, for example "Dify production"), route IDs (a non-empty subset of
the policy's), `per_minute` (1..600, default 60), an optional list of up to 8
source CIDRs, expiry (required, at most 365 days, default 90), creator and the
policy revision at creation. At most 16 active tokens per installation.

Lifecycle:

- **Create:** admin only (section 11, Q7). Returns the token once; stores the
  handle, `sha256(secret)` and the metadata. Audited
  `app_platform.machine_token_created` (installation, handle, label, routes,
  expiry; never the secret).
- **Rotate:** creates a successor with the same binding and sets the
  predecessor's expiry to `min(current, now + grace)`, with a grace of 0 to 7
  days. Audited `_rotated`.
- **Revoke:** immediate; state `revoked`. Audited `_revoked`. Disabling the
  capability, uninstalling, or deleting the installation revokes every token
  of it in the same transaction.
- **Policy shrink:** a token's effective routes are the intersection of its list
  and the current policy. A route dropped from the policy stops working at once
  without rewriting the token.
- **Last used:** `last_used_ms` is written at most once a minute per token, the
  rule `api_tokens` already uses (`store.go:1698-1723`).

Storage is hash-only. SHA-256 is enough for a 256-bit random secret; a slow
password hash adds latency without security here, and the house precedent is
the same digest (`store.go:1654-1657`). Lookup is by `handle` (unique index),
then a constant-time comparison of the digest.

### 4.4 Portal route (machine)

```text
GET /machine/apps/{installation}/data/{route}?{query}
GET /machine/apps/{installation}/openapi.json
```

The `/machine/` prefix is unused today. They are registered in a new route class
`authMachine`, distinct from `authPublic` and `authBearer` (`routes.go:23-41`),
so the route inventory tests can tell them apart. Processing order (cost
control, as `publicAdmit`, `app_platform_public_http.go:140-201`):

1. A per-source ceiling before anything is looked up, keyed by the client IP
   resolved with the existing trusted-proxy rules (`clientIP`,
   `internal/app/throttle.go:255`).
2. Parse `Authorization`: exactly `Bearer pmr_<handle>_<secret>`, at most
   128 bytes. Anything else gets 401. The `Cookie` header is never read.
3. One indexed read of the token by handle; constant-time digest comparison;
   state `active`; `expires_ms` in the future; installation equal to the path's;
   source CIDR match if a list is set. Every failure gets the same 401
   `{"code":"unauthorized"}`.
4. Per-token bucket (`per_minute`): 429 with `Retry-After`.
5. Session-less authority read (the 179 relay variant): installation enabled and
   ready, no restore pause, current release, `machine.read` granted, route in
   policy and token. An undeclared route gets 404 `not_found`. A declared route
   that is not granted to this token gets 403 `route_not_permitted`, which is
   safe because the caller has already proven possession of a valid token.
6. Canonicalise the query: declared names only, each at most once, value at
   most 256 bytes of printable ASCII, total at most 1,024 bytes, sorted. Anything
   else gets 400 `bad_query` without a backend call.
7. Per-installation rate and concurrency: 429 or 503.
8. Relay `GET <path>?<query>` through `requestPlatformWritableBackendAs` with
   a machine assertion function, exactly as 179 relays with
   `platformPublicAssertion` (`app_platform_public_http.go:367-390`), under the
   route's `timeout_ms`.
9. Response: 200 with valid JSON within `response_bytes` is passed through.
   400, 404 and 422 JSON bodies up to 64 KiB are passed through, so a Dify tool
   gets the app's validation message. 429 from the app becomes 429. Anything
   else, or an invalid body, becomes 502 `backend_error`; a runner or backend
   that is unavailable becomes 503.

Response headers: `Content-Type: application/json`, `Cache-Control: no-store`,
`X-Content-Type-Options: nosniff`,
`Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`. There is
no `Access-Control-*` header and no `Set-Cookie`. There is no Portal cache in
version 1 (section 11, Q4).

### 4.5 OpenAPI document

`GET /machine/apps/{installation}/openapi.json` with the same token returns an
OpenAPI 3.0 document generated by Portal from the descriptor and the token's
effective routes: one GET operation per route, the declared query parameters as
strings, and the bearer security scheme. It costs no backend call. Dify's
custom-tool import reads this format directly (section 8).

### 4.6 Machine assertion

The format follows the `pub.` assertion (`internal/appplatform/public.go:25-28`,
`:175-213`):

- Token: `mch.` + `<claims>.<mac>`, base64url.
- MAC: HMAC-SHA256 with the same registration key as viewer and public
  assertions, over the domain `rp-extension-machine-v1\n` plus the encoded
  claims. The domain differs from `rp-extension-public-v1\n` (`public.go:28`)
  and from the viewer's, so no kind verifies as another.
- Header: `X-Portal-Machine-Assertion`.
- Lifetime: 30 seconds, as `PublicAssertionLifetime` (`public.go:27`).
- Claims: `v`, `kid`, `iss`, `aud`, `app`, `instance`, `boot`, `rid` (random
  request ID), `iat`, `exp`, `method` (`GET`), `installation`, `generation`,
  `route`, `path`, `query_sha256`, `client_id` (the token handle) and
  `client_label` (the token label). There is no subject, role, OU or secret.

What the app sees: which reviewed route, for which installation generation, and
which named machine client. It may log or vary by `client_id` (for example a
per-client quota inside the app), but it must not treat it as a user.

### 4.7 Runner changes

- `ValidBackendRequestData` accepts a `mch.` assertion only on a bodiless GET of
  a path under `/v1/machine/`, as it already does for `pub.` and `/v1/public/`
  (`backend_request.go:51-58`). The assertion pattern widens from
  `^(pub\.)?...` to `^((pub|mch)\.)?...` (`backend_request.go:15`).
- The driver picks `X-Portal-Machine-Assertion` for `mch.` tokens
  (`internal/apprunner/driver/backend_request_linux.go:68-75`).
- New rule: a viewer assertion is refused on `/v1/machine/` and `/v1/public/`,
  so each path space has exactly one audience (section 11, Q11).
- An older runner already refuses `mch.` tokens: `mch.<claims>.<mac>` does not
  match the current two-part pattern, so the upgrade fails closed. Portal must
  still learn whether the runner supports machine assertions before it offers
  the capability. Use a separate runner capability query, not new fields in the
  runtime profile, for the reason in doc 175 amendment 1
  (`175-reviewed-multi-origin-and-private-network.md:243-248`).

### 4.8 App side (Apps repository)

`packages/extensionprotocol` gains `VerifyMachineAssertion` (the reference check,
as `VerifyPublicAssertion`, `public.go:248`). An app serves `/v1/machine/*`
only when this header verifies, and never on a viewer or public assertion.
market-monitor's planned Go wrapper (`README.md:36-40`) exposes its query
endpoints there. Its current FastMCP server is not relayed (section 11, Q10).

## 5. Interaction with existing authority

1. **AUTH-05 is unchanged.** Machine tokens are still refused on every viewer
   route: those routes are `authSession` and never read `Authorization`. The
   extension proxy and principals routes keep refusing non-app bearers
   (`extension_api.go:292-305`). This proposal adds a separate audience and adds
   refusal tests to AUTH-05 (section 9).
2. **`api_tokens` is not reused** (section 10.1). A `pmr_` token presented to
   `/api/v1` fails `TokenValid` (no matching hash) and gets 401. An `api_tokens`
   value presented to `/machine/` fails the prefix check and gets 401.
3. **`authBearer` is not reused.** Its read handlers admit cookies via
   `canQuery` (`routes.go:33-37`). The machine class admits only its own token.
4. **179 public projection** is unaffected and stays the right tool for
   anonymous data. An app may declare both blocks. The path spaces
   `/v1/public/` and `/v1/machine/` stay separate.
5. **Review and consent.** Enabling `machine.read` goes through the doc 175
   capability policy review when that exists. Until then it uses an interim
   administrator route modelled on 179 amendment C3:
   `GET/POST /api/admin/app-platform/installations/{installation}/machine-read`.
   It shows the route list with backend paths and this disclosure: "Holders of a
   machine token for this app can read these responses without signing in, until
   the token expires or is revoked." The POST must echo the preview digest.
   Token management is
   `GET/POST /api/admin/app-platform/installations/{installation}/machine-tokens`
   and `POST .../machine-tokens/{handle}/revoke` and `.../rotate`.

## 6. Limits, audit and operations

### 6.1 Rate and size limits

| Limit | Default | Max | Scope |
| --- | --- | --- | --- |
| Pre-auth per source | 120/min | fixed | client IP |
| Per token | 60/min | 600/min | token |
| Per installation | 5/s | 50/s | installation |
| Concurrent backend calls | 4 | 16 | installation |
| Query | 16 names, 256 bytes per value, 1,024 total | fixed | request |
| Response | per route, at most 1 MiB | 1 MiB | route |
| Timeout | per route | 30 s | route |

### 6.2 Audit

- Events: `app_platform.machine_read_enabled`, `_policy_changed`, `_disabled`,
  `app_platform.machine_token_created`, `_rotated`, `_revoked`, `_expired`
  (written by the sweep when an expiry passes).
- Traffic: one `app_platform.machine_read_summary` per token per 10 minutes when
  there was traffic: requests, refusals by class, backend calls, bytes out.
  Same aggregation as 179's summary (`app_platform_public_http.go:72-122`).
- Source address: recorded once per token per day as
  `app_platform.machine_token_source` (the address, in audit only), so an
  administrator can spot a leaked token used from a new place (section 11, Q9).
- Never logged: the secret, the `Authorization` header or response bodies.

### 6.3 Expiry sweep

A sweep marks expired tokens in audit; requests check `expires_ms` themselves,
so the sweep is only for reporting.

### 6.4 Admin UI

Per installation: enable or disable the capability (consent preview), a token
list (label, handle, routes, created by, created, expires, last used, state),
create (secret shown once with a copy button and an OpenAPI URL), rotate and
revoke.

### 6.5 Backup and restore

The table joins the app-platform backup set (`internal/app/app_platform_backup.go:46`).
On restore, any token that is revoked in the live database before the restore
stays revoked, and the revocation time keeps the earlier value. This is the
same floor rule as credentials (`app_platform_credential_restore.go:52-60`).
Tokens created after the backup are lost and must be recreated; the restore
summary says how many.

## 7. Failure codes

| Status | Code | When |
| --- | --- | --- |
| 400 | `bad_query` | Undeclared, duplicate or oversize query |
| 401 | `unauthorized` | Any authentication failure (uniform) |
| 403 | `route_not_permitted` | Authenticated, route declared but not in the token or policy |
| 404 | `not_found` | Route ID not declared |
| 405 | (none) | Any method other than GET |
| 429 | `rate_limited` | Any limit, with `Retry-After` |
| 502 | `backend_error` | Backend answered outside the contract |
| 503 | `unavailable` | Installation not ready, restore pause, runner unavailable or runner without machine support |

## 8. Dify integration notes

- **Custom tool (recommended):** import from the OpenAPI URL (section 4.5) and
  set authentication to API key, header `Authorization`, type Bearer, value
  the `pmr_` token. Each route becomes a tool. This uses only GET.
- **HTTP Request node:** in a workflow, use GET, the data route URL and Bearer
  authorization. Good for one fixed call.
- **API-based extension:** Dify POSTs a fixed JSON envelope to a single
  endpoint. This proposal is GET-only, so it is not supported (section 11, Q3).
- **Network path:** self-hosted Dify sends tool HTTP calls through its SSRF
  proxy, which by default may refuse private addresses. If Portal is on a
  private network, the Dify operator must allow Portal's address. Verify
  against the deployed Dify version. Dify Cloud calls come from Dify's egress
  addresses, so a source CIDR list on the token needs those addresses and they
  can change.
- **MCP:** Dify can also consume MCP servers. Relaying MCP (streamable HTTP,
  POST plus server-sent events) is out of scope for this proposal.

## 9. Test plan (these must fail first)

1. Descriptor parsing refuses a `machine` block and the capability today.
2. `GET /machine/apps/{id}/data/{route}` returns the SPA today; it must return
   401 without a token, and 404 or 401 (never the SPA) once routes exist.
3. AUTH-05 additions:
   - a `pmr_` token on every viewer route and on `/api/v1/extensions/*` is refused;
   - an `api_tokens` value (`all` scope) and an app token on `/machine/` get 401;
   - a valid token for installation A on installation B gets 401, with the
     same body as an unknown token;
   - a valid session cookie on `/machine/` with no bearer gets 401.
4. Assertion separation: a `mch.` assertion verifies with neither the viewer
   nor the public verifier, and the reverse. The runner refuses `mch.` with a
   body, with POST, or outside `/v1/machine/`. It refuses a viewer token under
   `/v1/machine/` and `/v1/public/`. An older runner refuses `mch.`.
5. Revocation, expiry, capability disable, uninstall and restore pause: the next
   request is refused (401 or 503) within the same second, with no backend call.
6. Policy shrink: a route removed from the policy gets 403 on a token that still
   lists it.
7. Limits: the 61st request in a minute for one token gets 429 while another
   token on the same installation succeeds; the installation limit holds across
   tokens.
8. Response: HTML, a 302, a 2 MiB body or a timeout gets 502 or 503 and nothing
   is passed through. A 422 JSON body is passed through.
9. Storage: the database holds only the digest. The token appears in no log,
   audit row or error (fixture scans all output). The secret is shown exactly
   once.
10. No CORS: a preflight `OPTIONS` gets 405 with no `Access-Control-*` header.
11. Restore: a token revoked after the backup stays revoked after restoring that
    backup.
12. SQLite and PostgreSQL; native run with a fixture app that verifies
    `X-Portal-Machine-Assertion`.

## 10. Schema change — Approved by the owner on 2026-11-20 (migration 0023)

### 10.1 Why not reuse an existing table

| Candidate | Why not |
| --- | --- |
| `api_tokens` (`store.go:689-696`) | No installation or route binding; `all` passes every scope check (`store.go:1718-1720`), so adding a scope would let every existing `all` token read every app; it still matches legacy plaintext values (`store.go:1709`); text timestamps instead of the platform's `MS` columns; managed by `/api/admin/tokens` (`routes.go:358-360`) outside the app-platform review and audit |
| `appTokens` (`apps_token.go`) | In memory only, viewer- and session-bound, lost on restart |
| `app_platform_secret_bindings` | Holds credentials Portal uses outbound (`operation` http_bearer, http_api_key, raw_read; doc 180 adds `ingress_hmac_sha256`) whose sealed values Portal must recover; a machine token must never be recoverable, and its routes, limits and expiry do not fit `policy_json` without a lookup by digest |
| `app_platform_capability_grants.policy_json` | Holds the reviewed policy, one row per capability; it cannot index tokens by digest or hold many revocable rows |

### 10.2 The table

One append-only migration adds one table. It is numbered after `0022` (the
approved 177/178/180 migration) and is the next free number when it is
implemented.

| Column | Type | Rule |
| --- | --- | --- |
| `id` | ID | UUID primary key |
| `installation_id` | ID | FK to `app_platform_installations(id)`, `ON DELETE CASCADE` (as `secret_bindings`) |
| `handle` | T16 | Public lookup handle, unique |
| `label` | T64 | Administrator label, sent to the app |
| `secret_sha256` | SHA | Digest of the secret part, unique |
| `routes_json` | J2048 | Sorted, non-empty route ID array |
| `per_minute` | R | 1..600 |
| `source_cidrs_json` | J1024 | Sorted CIDR array, `[]` for any source |
| `state` | T16 | `active`, `revoked` |
| `policy_revision` | R | Grant policy revision at creation |
| `created_by` | T320 | Creator username (as `access_grants.username`) |
| `revision` | R | Monotonic |
| `expires_ms` | MS | Greater than `created_ms` |
| `created_ms`, `updated_ms` | MS | `updated_ms>=created_ms` |
| `revoked_ms` | MS? | Set exactly when revoked |
| `last_used_ms` | MS? | Written at most once a minute |

The DDL below is what the house generator (`platformTable.ddl`,
`internal/app/app_platform_schema.go:106-205`) produces for this declaration:

```text
{name: "machine_tokens", fields: "id:ID installation_id:ID handle:T16 label:T64 secret_sha256:SHA routes_json:J2048 per_minute:R source_cidrs_json:J1024 state:T16 policy_revision:R created_by:T320 revision:R expires_ms:MS created_ms:MS updated_ms:MS revoked_ms:MS? last_used_ms:MS?",
 primary: "id", references: installation_id -> installations(id) CASCADE,
 checks: state enum; revoked consistency; per_minute<=600; expires_ms>created_ms; updated_ms>=created_ms}
```

PostgreSQL:

```sql
CREATE TABLE IF NOT EXISTS app_platform_machine_tokens (
  id TEXT NOT NULL,
  installation_id TEXT NOT NULL,
  handle TEXT NOT NULL,
  label TEXT NOT NULL,
  secret_sha256 TEXT NOT NULL,
  routes_json TEXT NOT NULL,
  per_minute BIGINT NOT NULL,
  source_cidrs_json TEXT NOT NULL,
  state TEXT NOT NULL,
  policy_revision BIGINT NOT NULL,
  created_by TEXT NOT NULL,
  revision BIGINT NOT NULL,
  expires_ms BIGINT NOT NULL,
  created_ms BIGINT NOT NULL,
  updated_ms BIGINT NOT NULL,
  revoked_ms BIGINT,
  last_used_ms BIGINT,
  CONSTRAINT app_platform_machine_tokens_pk PRIMARY KEY (id),
  CONSTRAINT app_platform_machine_tokens_fk_1 FOREIGN KEY (installation_id) REFERENCES app_platform_installations(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT app_platform_machine_tokens_ck_1 CHECK (octet_length(id)=36),
  CONSTRAINT app_platform_machine_tokens_ck_2 CHECK (octet_length(installation_id)=36),
  CONSTRAINT app_platform_machine_tokens_ck_3 CHECK (octet_length(handle)<=16),
  CONSTRAINT app_platform_machine_tokens_ck_4 CHECK (octet_length(label)<=64),
  CONSTRAINT app_platform_machine_tokens_ck_5 CHECK (octet_length(secret_sha256)=64),
  CONSTRAINT app_platform_machine_tokens_ck_6 CHECK (octet_length(routes_json)<=2048),
  CONSTRAINT app_platform_machine_tokens_ck_7 CHECK (per_minute>=1),
  CONSTRAINT app_platform_machine_tokens_ck_8 CHECK (octet_length(source_cidrs_json)<=1024),
  CONSTRAINT app_platform_machine_tokens_ck_9 CHECK (octet_length(state)<=16),
  CONSTRAINT app_platform_machine_tokens_ck_10 CHECK (policy_revision>=1),
  CONSTRAINT app_platform_machine_tokens_ck_11 CHECK (octet_length(created_by)<=320),
  CONSTRAINT app_platform_machine_tokens_ck_12 CHECK (revision>=1),
  CONSTRAINT app_platform_machine_tokens_ck_13 CHECK (expires_ms>=0),
  CONSTRAINT app_platform_machine_tokens_ck_14 CHECK (created_ms>=0),
  CONSTRAINT app_platform_machine_tokens_ck_15 CHECK (updated_ms>=0),
  CONSTRAINT app_platform_machine_tokens_ck_16 CHECK (revoked_ms>=0),
  CONSTRAINT app_platform_machine_tokens_ck_17 CHECK (last_used_ms>=0),
  CONSTRAINT app_platform_machine_tokens_ck_18 CHECK ((state='active') OR (state='revoked')),
  CONSTRAINT app_platform_machine_tokens_ck_19 CHECK (((state='revoked') AND (revoked_ms IS NOT NULL)) OR ((state<>'revoked') AND (revoked_ms IS NULL))),
  CONSTRAINT app_platform_machine_tokens_ck_20 CHECK (per_minute<=600),
  CONSTRAINT app_platform_machine_tokens_ck_21 CHECK (expires_ms>created_ms),
  CONSTRAINT app_platform_machine_tokens_ck_22 CHECK (updated_ms>=created_ms));
```

SQLite (the generator adds a byte-length and a `typeof` check per column):

```sql
CREATE TABLE IF NOT EXISTS app_platform_machine_tokens (
  id TEXT NOT NULL,
  installation_id TEXT NOT NULL,
  handle TEXT NOT NULL,
  label TEXT NOT NULL,
  secret_sha256 TEXT NOT NULL,
  routes_json TEXT NOT NULL,
  per_minute INTEGER NOT NULL,
  source_cidrs_json TEXT NOT NULL,
  state TEXT NOT NULL,
  policy_revision INTEGER NOT NULL,
  created_by TEXT NOT NULL,
  revision INTEGER NOT NULL,
  expires_ms INTEGER NOT NULL,
  created_ms INTEGER NOT NULL,
  updated_ms INTEGER NOT NULL,
  revoked_ms INTEGER,
  last_used_ms INTEGER,
  CONSTRAINT app_platform_machine_tokens_pk PRIMARY KEY (id),
  CONSTRAINT app_platform_machine_tokens_fk_1 FOREIGN KEY (installation_id) REFERENCES app_platform_installations(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT app_platform_machine_tokens_ck_1 CHECK (length(CAST(id AS BLOB))=36),
  CONSTRAINT app_platform_machine_tokens_ck_2 CHECK (typeof(id)='text'),
  CONSTRAINT app_platform_machine_tokens_ck_3 CHECK (length(CAST(installation_id AS BLOB))=36),
  CONSTRAINT app_platform_machine_tokens_ck_4 CHECK (typeof(installation_id)='text'),
  CONSTRAINT app_platform_machine_tokens_ck_5 CHECK (length(CAST(handle AS BLOB))<=16),
  CONSTRAINT app_platform_machine_tokens_ck_6 CHECK (typeof(handle)='text'),
  CONSTRAINT app_platform_machine_tokens_ck_7 CHECK (length(CAST(label AS BLOB))<=64),
  CONSTRAINT app_platform_machine_tokens_ck_8 CHECK (typeof(label)='text'),
  CONSTRAINT app_platform_machine_tokens_ck_9 CHECK (length(CAST(secret_sha256 AS BLOB))=64),
  CONSTRAINT app_platform_machine_tokens_ck_10 CHECK (typeof(secret_sha256)='text'),
  CONSTRAINT app_platform_machine_tokens_ck_11 CHECK (length(CAST(routes_json AS BLOB))<=2048),
  CONSTRAINT app_platform_machine_tokens_ck_12 CHECK (typeof(routes_json)='text'),
  CONSTRAINT app_platform_machine_tokens_ck_13 CHECK (per_minute>=1),
  CONSTRAINT app_platform_machine_tokens_ck_14 CHECK (typeof(per_minute)='integer'),
  CONSTRAINT app_platform_machine_tokens_ck_15 CHECK (length(CAST(source_cidrs_json AS BLOB))<=1024),
  CONSTRAINT app_platform_machine_tokens_ck_16 CHECK (typeof(source_cidrs_json)='text'),
  CONSTRAINT app_platform_machine_tokens_ck_17 CHECK (length(CAST(state AS BLOB))<=16),
  CONSTRAINT app_platform_machine_tokens_ck_18 CHECK (typeof(state)='text'),
  CONSTRAINT app_platform_machine_tokens_ck_19 CHECK (policy_revision>=1),
  CONSTRAINT app_platform_machine_tokens_ck_20 CHECK (typeof(policy_revision)='integer'),
  CONSTRAINT app_platform_machine_tokens_ck_21 CHECK (length(CAST(created_by AS BLOB))<=320),
  CONSTRAINT app_platform_machine_tokens_ck_22 CHECK (typeof(created_by)='text'),
  CONSTRAINT app_platform_machine_tokens_ck_23 CHECK (revision>=1),
  CONSTRAINT app_platform_machine_tokens_ck_24 CHECK (typeof(revision)='integer'),
  CONSTRAINT app_platform_machine_tokens_ck_25 CHECK (expires_ms>=0),
  CONSTRAINT app_platform_machine_tokens_ck_26 CHECK (typeof(expires_ms)='integer'),
  CONSTRAINT app_platform_machine_tokens_ck_27 CHECK (created_ms>=0),
  CONSTRAINT app_platform_machine_tokens_ck_28 CHECK (typeof(created_ms)='integer'),
  CONSTRAINT app_platform_machine_tokens_ck_29 CHECK (updated_ms>=0),
  CONSTRAINT app_platform_machine_tokens_ck_30 CHECK (typeof(updated_ms)='integer'),
  CONSTRAINT app_platform_machine_tokens_ck_31 CHECK (revoked_ms>=0),
  CONSTRAINT app_platform_machine_tokens_ck_32 CHECK ((revoked_ms IS NULL) OR (typeof(revoked_ms)='integer')),
  CONSTRAINT app_platform_machine_tokens_ck_33 CHECK (last_used_ms>=0),
  CONSTRAINT app_platform_machine_tokens_ck_34 CHECK ((last_used_ms IS NULL) OR (typeof(last_used_ms)='integer')),
  CONSTRAINT app_platform_machine_tokens_ck_35 CHECK ((state='active') OR (state='revoked')),
  CONSTRAINT app_platform_machine_tokens_ck_36 CHECK (((state='revoked') AND (revoked_ms IS NOT NULL)) OR ((state<>'revoked') AND (revoked_ms IS NULL))),
  CONSTRAINT app_platform_machine_tokens_ck_37 CHECK (per_minute<=600),
  CONSTRAINT app_platform_machine_tokens_ck_38 CHECK (expires_ms>created_ms),
  CONSTRAINT app_platform_machine_tokens_ck_39 CHECK (updated_ms>=created_ms));
```

Indexes (both dialects):

```sql
CREATE UNIQUE INDEX IF NOT EXISTS uq_ap_machine_token_handle ON app_platform_machine_tokens (handle);
CREATE UNIQUE INDEX IF NOT EXISTS uq_ap_machine_token_secret ON app_platform_machine_tokens (secret_sha256);
CREATE INDEX IF NOT EXISTS idx_ap_machine_token_installation ON app_platform_machine_tokens (installation_id,state,id);
CREATE INDEX IF NOT EXISTS idx_ap_machine_token_expiry ON app_platform_machine_tokens (state,expires_ms);
```

Migration properties: additive. Restore of an older dump is allowed (the table
is empty in it, as `migration0016`'s `olderDumpOK`,
`app_platform_credential_schema.go:32`). The table is added to the backup table
list and to the restore floor of section 6.5. The token count limit (16 active
per installation), the route subset rule and the 365-day expiry maximum are
enforced in code, not in CHECKs, because they depend on other rows or on
policy.

Contract changes needing sign-off besides the schema: the capability
`machine.read`, the descriptor `machine` block, the policy format
`portal.machine-read-policy/1-dev.1`, the `mch.` assertion and its header, the
runner's path-space rule, and the `/machine/apps/` URL space.

## 11. Owner decisions (2026-11-20)

The owner decided every question below on 2026-11-20 and accepted the recommended answer for each (Q1 to Q14). Q14: the table `app_platform_machine_tokens` of section 10 is approved as one additive migration after `0022`, numbered `0023`.


Q1. New table or reuse `api_tokens`. Decided: new table `app_platform_machine_tokens` (section 10.1).

Q2. Token format and hashing. Decided: `pmr_<handle>_<secret>`, 256-bit secret, SHA-256 digest, shown once.

Q3. GET only, or also POST for queries (needed by Dify's API-based extension). Decided: GET only in version 1; revisit if a real workflow needs a JSON body.

Q4. Portal response cache for machine routes. Decided: none in version 1 (`no-store`); the app can cache internally.

Q5. Optional source CIDR list per token. Decided: yes, optional, default empty.

Q6. Expiry. Decided: required; default 90 days, maximum 365; rotation with up to 7 days of grace.

Q7. Who creates and revokes tokens. Decided: Portal administrators who may manage the installation (as doc 175 decision 1); never an app role.

Q8. Does the app learn the token's identity. Decided: yes; handle and label in the assertion; never the secret.

Q9. Record source addresses. Decided: once per token per day in audit only; no per-request IP storage.

Q10. Relay MCP for market-monitor's FastMCP server. Decided: no; the app exposes JSON GET routes under `/v1/machine/` through its planned Go wrapper.

Q11. Make `/v1/machine/` and `/v1/public/` exclusive (runner refuses viewer assertions there). Decided: yes.

Q12. Serve a generated OpenAPI document. Decided: yes, authenticated with the same token, no backend call.

Q13. Default limits. Decided: 60/min per token (max 600), 5/s and 4 concurrent per installation (max 50 and 16).

Q14. Approve the schema in section 10. Decided: approve as one additive migration after `0022`.

## 12. Implementation notes

Implemented on `feat/app-platform-machine-read-api` (on top of the ACS3 signing
branch, with the docs branch merged). Where the code is, and where it differs from
the text above:

**What exists**

- Contract: `machine.read` (optional, needs a backend, never required) and the
  descriptor `machine` block, in the source schema and the generated Go and
  TypeScript products (`internal/appplatform/descriptor_machine.go` validates the
  block; the optional capability list now has 14 entries).
- Formats (`internal/appplatform/machine.go`): the policy
  `portal.machine-read-policy/1-dev.1`, the query canonicalisation shared with 179
  (bounds 256 bytes per value, 1,024 in total, 16 names), the `pmr_` token
  (`NewMachineToken`, `ParseMachineAuthorization`), and the `mch.` assertion with
  its own MAC domain `rp-extension-machine-v1\n`, header
  `X-Portal-Machine-Assertion`, 30 second lifetime and the claims of section 4.6
  (`rid` for the request ID, as written there; 179's public claims call it `jti`).
- Reserved route space: `ValidBackendRequestData` judges the DECODED path
  (`backendRequestSpace`: decoded twice, no `;` in either spelling, case-insensitive,
  no dot or empty segment; these refusals apply to a path that is in, or after
  normalisation would reach, the public, machine or handshake space, and an ordinary
  viewer path keeps its spelling). A `mch.` token is valid only on a bodiless GET in the
  exact lower-case `/v1/machine/` spelling; a `pub.` token only in `/v1/public/`; a
  viewer token in neither (Q11). The runner checks claims against the request
  (`validateMachineAssertionData`) and the driver picks the header from the prefix.
- Runner capability query (section 4.7): `PRMA0001`/`PRMB0001`
  (`portal.runner.machine-assertions/1-dev.1`), modelled on the network targets
  query, with one retry before a runner is concluded unsupported. Enabling is
  refused (409) and requests are 503 against a runner that does not answer; the
  definite answer is remembered for 60 seconds (10 when it is "unsupported"); a
  failure to ask, such as a timeout, is not remembered.
- Table `app_platform_machine_tokens` as migration 0023, DDL exactly as section 10
  (a test compares the generated DDL with the text of that section). Restore keeps
  every live revocation with the earlier revocation time, and the shorter of the live
  and the restored expiry (a rotation retires the old token by shortening it); a revoked token whose
  installation is not in the backup is skipped rather than refusing the restore;
  the restore report and the `restore` command say how many unexpired active
  tokens the backup does not hold.
- Tokens (`app_platform_machine_token.go`): create, rotate (grace 0 to 7 days,
  never lengthening the predecessor), revoke, list, last-used (at most once a
  minute), sixteen active per installation, expiry 1 to 365 days (the console
  defaults to 90), source list of at most eight addresses or prefixes. Disabling
  the capability and uninstalling revoke every token in the same transaction.
- Machine routes (`app_platform_machine_http.go`): the order of section 4.4, a new
  route class `authMachine` (never a session route), a global cap of 32
  concurrent pre-authentication database reads (the token lookup and the authority
  read, answered 503 beyond it), a sharded LRU limiter per source, per token and
  per installation (the per-source ceiling counts requests that fail to authenticate;
  a valid token is held only to its own limit, so its configured rate is reachable
  from one address), an installation concurrency counter, exclusion from the
  request log, no CORS, no cookie read, `no-store`.
- OpenAPI 3.0.3 document (`app_platform_machine_openapi.go`), generated from the
  descriptor and the token's effective routes, with no backend call.
- Audit: `machine_read_enabled`, `_policy_changed`, `_disabled`,
  `machine_token_created`, `_rotated`, `_revoked`, `_expired` (a sweep every ten
  minutes; the audit row is the record, so a restart repeats nothing; a token
  a rotation retired is reported by the rotation, not as expired),
  `machine_token_source` (once per token per day, the address in `detail`), and
  `machine_read_summary` per token per 10 minutes. Requests that do not
  authenticate are counted in one shared, address-free summary row.
- Console: a panel on the application settings page (consent preview with digest,
  route and limit choice, token table with last use, create, rotate, revoke, recent
  use, secret shown once with the OpenAPI address), in en-US, zh-CN and zh-TW.

**Decisions where the text left room**

- A valid token for an installation whose `machine.read` grant is off answers 401
  (the token no longer works); 503 is for an installation that is not ready, a
  restore pause, a trust failure, a runner without support and a busy cap.
- Switching the capability on (off to on) revokes any token an earlier period left
  behind, because an update resets the grant row without a withdrawal. A policy
  change while it stays on keeps tokens, as the intersection rule requires.
- The access transaction of the relay re-reads the token as well as the grant, before and after
  the backend call, so a revocation during a call withholds the answer (503) and
  the assertion is signed only for the token that was admitted.
- 405 for every method other than GET comes from the mux (`Allow: GET, HEAD`) and
  from the handlers for HEAD; there is no body and no CORS header.
- `machine.read` is not in the capability list the launch page gives the app's
  frontend: the frontend has no use for it, and the Apps SDK treats that list as
  closed. It is in the settings capability list and in the launch aggregate check.
- The interim consent route of section 5.5 is used; the doc 175 capability policy
  review does not carry `machine.read` yet.

**Not as the proposal assumed**

- `backendRequestSpace` is not on main: it exists only on
  `feat/app-platform-heartbeat-ingress`. It is added here in the same shape with
  the spaces `public` and `machine`. Merging the two branches needs
  `((pub|ing|mch)\.)?` in `backendRequestAssertion`, the `machine` case beside
  `ingress` in `ValidBackendRequestData`, and `"machine"` in the space list. That
  branch's `platformAuthoritySlots` is added here identically so the file merges
  cleanly; `anonymousMachine` is a second field beside its `anonymousIngress`, and
  `platformReadMachineGrant` is called from the same two places in
  `platformBackendReadTx` as its `platformReadAnonymousGrant`.

**Apps side (not part of this change)**

1. Take the source schema `internal/appplatform/contract/descriptor-v2.schema.json`
   and regenerate with `python3 scripts/generate_app_contract.py --apps-dir <Apps>`
   (adds `Machine`, `MachineRoute`, the `machine.read` capability, the three
   machine patterns and limits; the contract digest changes).
2. `packages/extensionprotocol`: `VerifyMachineAssertion` and `MachineClaims`
   (prefix `mch.`, domain `rp-extension-machine-v1\n`, header
   `X-Portal-Machine-Assertion`, claims of section 4.6, 30 second lifetime). An
   app serves `/v1/machine/*` only on that header, never on a viewer or public
   assertion.
3. market-monitor's planned Go wrapper exposes its query endpoints under
   `/v1/machine/` and may vary by `client_id`.
4. Pin the Apps checkout of `.github/workflows/app-project-quota-probe.yml`
   (`ref:` of the Apps repository) to the Apps commit that carries 1 and 2.

**Tests** are the ones named in section 9, except item 12's native fixture run,
which needs the runner and is covered by the signing test that goes through the
real relay signer and the runner's validation: `appplatform/machine_test.go`,
`descriptor_machine_test.go`, `apprunner/control/backend_request_machine_test.go`,
`apprunner/driver/backend_request_machine_linux_test.go`,
`apprunner/transport/machine_support_linux_test.go`,
`app/app_platform_machine_token_schema_test.go`,
`app_platform_machine_token_restore_test.go`, `app_platform_machine_test.go`,
`app_platform_machine_support_test.go`, `app_platform_machine_slots_test.go`,
`app_platform_machine_log_test.go` and
`web/src/pages/manage/AppMachineReadPanel.test.tsx`.
