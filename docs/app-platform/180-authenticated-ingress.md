# Proposal: authenticated inbound ingress for heartbeats

Status: accepted 2026-11-17; implemented (see "Implementation notes"). No dependency on 174–178;
shares the anonymous-route machinery of [179](179-public-projection.md).
Index and order: doc [174](174-background-broker-egress.md).

## 1. Problem and evidence

- No route lets an external sender reach an app backend. Viewer routes require
  a session (`internal/app/routes.go:519-532`); the two public app-platform
  routes accept only an H07 service assertion, which only Portal's own workers
  hold (`routes.go:528-529`; `internal/app/app_platform_task_store.go:564`).
- The backend has no listener outside its Unix socket
  (`internal/apprunner/driver/container.go:384-388`) and the relay accepts only
  viewer-asserted requests (`internal/appplatform/backend_request.go:47-48`).

Status heartbeats are `POST /ingest/v1/heartbeats/{32-hex monitor id}` with a
≤ 1 KiB JSON body `{timestamp, nonce}`, `X-Status-Signature` = hex
HMAC-SHA256(secret, id + "\n" + body), a timestamp window of −30 s/+5 s, a
16–128 byte nonce stored as the observation key against replay, and at most 10
per monitor per minute (Apps `apps/status/service/internal/status/heartbeat.go:13-88`).
The handler decrypts the heartbeat secret inside the app; in v2 the app holds
no secret values, so verification must move to Portal custody. The route is
parked in `servePhase2` (`server.go:343-361`).

## 2. Threat model

| Threat | Control |
| --- | --- |
| Forged heartbeat | HMAC over installation, route, endpoint, timestamp and body digest, keyed by a credential in Portal custody |
| Replay | Timestamp window; Portal nonce cache for the window; the nonce digest forwarded so the app deduplicates durably |
| Secret exposure to the app | The app receives only the verified fact and the nonce digest, never the secret |
| Flood and amplification | Body ≤ 4 KiB; per-endpoint, per-client and per-installation limits; refusals cost no database write and no backend call |
| Endpoint enumeration | Every authentication failure is the same 401 with the same body and timing class |
| Sender confusion across apps | The signature binds installation and route ID, so a secret reused elsewhere cannot be replayed here |

## 3. Contract

### 3.1 Capability and descriptor

New capability `ingress.webhook` (optional; requires a backend). Descriptor
block:

```json
"ingress": {
  "routes": [{"id":"heartbeat","path":"/v1/ingress/heartbeat","body_bytes":1024,"per_endpoint_per_minute":10}]
}
```

1..4 routes; `path` under `/v1/ingress/`; `body_bytes` 1..4,096;
`per_endpoint_per_minute` 1..60.

### 3.2 Endpoints are credential bindings

One endpoint = one secret binding with the new operation
`ingress_hmac_sha256`. The binding name is the endpoint ID in the URL
(`^[a-z][a-z0-9-]{0,63}$`, `internal/app/app_platform_secret_bindings.go:21`).
Binding policy v2 (doc [176](176-background-credential-use.md)) for this
operation carries `{"ingress_route":"heartbeat"}` instead of a network target
and no principals. The credential value is the shared secret, 32..256 bytes.

### 3.3 Wire

```text
POST /ingress/apps/{installation}/{route}/{endpoint}
Content-Type: application/json
X-Portal-Ingress-Timestamp: 1763400000
X-Portal-Ingress-Nonce: 3f9c...            (16..128 chars of [A-Za-z0-9_-])
X-Portal-Ingress-Signature: v1=<64 hex>
```

Signature: HMAC-SHA256(secret, string) over

```text
portal-ingress/v1\n<installation>\n<route>\n<endpoint>\n<timestamp>\n<nonce>\n<hex SHA-256 of body>
```

Processing, in this order, stopping at the first failure:

1. Static checks: path shape, `Content-Type`, body ≤ the route's `body_bytes`,
   header shapes, timestamp within −30 s/+5 s of Portal time. Failure: 401
   (400 only for body too large: 413).
2. Per-client limit 120/min and per-installation limit 600/min (in memory).
   Failure: 429.
3. Load authority without a session: installation enabled and ready, no
   restore pause, `ingress.webhook` granted with this route, active binding of
   operation `ingress_hmac_sha256` for this route, credential active at the
   reviewed value revision.
4. Decrypt inside the request, verify the HMAC in constant time, clear the
   secret. Failure: 401.
5. Nonce cache: reject a repeated `(endpoint, nonce)` seen within the last
   60 s (401). Bounded to 4,096 entries per installation, oldest evicted.
6. Per-endpoint limit from the descriptor. Failure: 429.
7. Forward to the backend through a signed `portal.app-ingress-request/1-dev.1`
   runner command: `POST <route path>` with the body unchanged and header
   `X-Portal-Ingress-Assertion`, signed with the registration key, binding
   installation, generation, route, endpoint, received time, nonce SHA-256 and
   body SHA-256.
8. Reply 202 `{"ok":true}` on backend 2xx; 503 with `Retry-After: 30` if the
   backend or runner is unavailable; 400 if the backend returns 4xx.

No step before 7 writes to the database.

## 4. Review and consent

Enabling `ingress.webhook` is an install selection or a doc 175 capability
policy review that shows each route and the URL pattern. Creating an endpoint
is a binding review: the administrator enters the secret (generated outside
Portal and given to the sender), picks the route, and confirms. Revoking the
binding disables the endpoint immediately. The app UI may show the endpoint URL
and the signing recipe, but never the secret.

## 5. Failure and audit

All authentication failures return the same 401 body `{"ok":false}`.
Audit: capability changes and binding lifecycle (existing). Traffic is
aggregated per installation per 10 minutes in
`app_platform.ingress_summary` (accepted, refused by class, forwarded,
backend failures); per-endpoint counts appear in the binding's console detail.
No source IP is written to audit.

## 6. Migration and compatibility

Additive. Heartbeat senders must be reconfigured for the new URL and the
signature string; the v2 line is a fresh install, so Beta.3 senders keep
pointing at Beta.3 during the comparison window. Status replaces its heartbeat
handler with one that verifies `X-Portal-Ingress-Assertion`, keeps the nonce
digest as the observation key, and maps the endpoint name to a monitor.

## 7. Test plan (these must fail first)

1. A POST to `/ingress/apps/...` matches no route today (only the `GET /`
   catch-all exists); it must return
   401 for an unsigned request and 202 for a correctly signed one.
2. Wrong secret, wrong installation or route in the signed string, timestamp
   outside the window, modified body: 401, no backend call, no database write
   (statement counter).
3. Same nonce twice within 60 s: second is 401; after a Portal restart the
   backend receives the same nonce digest and deduplicates.
4. 11th heartbeat in a minute for one endpoint: 429; other endpoints
   unaffected.
5. Revoke the binding, disable the app, restore pause: 401 or 503 as specified.
6. The backend never receives the secret (fixture inspects all bytes).
7. Body larger than the route bound: 413 before authentication work.
8. SQLite and PostgreSQL; native run with the Status fixture.

## 8. Schema change (approved by the owner on 2026-11-17)

Approved by the owner on 2026-11-17, together with the schema changes in docs
177, 178 and 180, as one migration, `0022`.

Yes: widen the `operation` CHECK of `app_platform_secret_bindings` with
`ingress_hmac_sha256` (19 bytes, within `operation` T24). Same mechanism,
constraint names and caveats as doc [177](177-provider-request-signing.md) §8
(PostgreSQL `_ck_15`, SQLite `_ck_29`); shipped in migration `0022`, one step with
`acs3_hmac_sha256` and `pg_password`.

PostgreSQL (shown alone):

```sql
ALTER TABLE app_platform_secret_bindings DROP CONSTRAINT app_platform_secret_bindings_ck_15;
ALTER TABLE app_platform_secret_bindings ADD CONSTRAINT app_platform_secret_bindings_ck_15
  CHECK ((operation='http_bearer') OR (operation='http_api_key') OR (operation='raw_read')
      OR (operation='ingress_hmac_sha256'));
```

SQLite:

```sql
PRAGMA writable_schema=ON;
UPDATE sqlite_master
   SET sql=replace(sql,
     'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read''))',
     'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read'') OR (operation=''ingress_hmac_sha256''))')
 WHERE type='table' AND name='app_platform_secret_bindings'
   AND instr(sql,'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read''))')>0;
PRAGMA writable_schema=OFF;
PRAGMA schema_version=<cookie+1>;
PRAGMA integrity_check(app_platform_secret_bindings);
PRAGMA foreign_key_check(app_platform_secret_bindings);
```

No nonce table: replay inside the window is held by memory, and after a restart
by the timestamp window plus the app's durable deduplication on the forwarded
nonce digest. If the owner wants Portal-side durable replay protection, the
alternative is one table
`app_platform_ingress_nonces(installation_id ID, endpoint T64, nonce_sha256 SHA, expires_ms MS, PRIMARY KEY(installation_id, endpoint, nonce_sha256))`
with a sweep; it costs one write per accepted heartbeat. The owner
rejected it on 2026-11-17 (question 2).

## 9. Owner decisions (2026-11-17)

The owner decided every question below on 2026-11-17.

1. Also accept a plain bearer token for senders that cannot compute an HMAC.
   Decided: no; a bearer token is replayable for its whole life, and cron with
   `openssl` can sign.
2. Durable nonce table. Decided: no (above).
3. Portal-generated secrets shown once. Decided: not now; it would be the
   first value-reveal path in custody and needs its own review.

## 10. Implementation notes (2026-11-19)

What shipped, and where it differs from sections 3 to 8. There is no schema change beyond migration
`0022`, which already carries `ingress_hmac_sha256`.

- **No new runner command.** Section 3.3 step 7 names a `portal.app-ingress-request/1-dev.1` runner
  command. As for doc 179 (amendment C1), the relay commands already bind method, path, body digest
  and assertion digest, so Portal signs a separate assertion instead: `ing.` + `<claims>.<mac>`, MAC
  domain `rp-extension-ingress-v1`, claims `installation`, `generation`, `route`, `endpoint`, `path`,
  `received_ms`, `nonce_sha256` and `body_sha256`, no secret and no address. The runner picks
  `X-Portal-Ingress-Assertion` from the prefix and refuses an `ing.` token on anything but a POST of
  `/v1/ingress/` with the exact body; it also refuses a viewer or public token on that path space.
- **Descriptor.** The `ingress` block and the optional `ingress.webhook` capability are in the signed
  descriptor schema; `route.id` is `^[a-z][a-z0-9-]{0,31}$`. The Apps SDK schema and generated types are
  regenerated from it (`scripts/generate_app_contract.py`).
- **Endpoint binding.** The policy is `{"format":"portal.secret-binding-policy/2-dev.1","operation":
  "ingress_hmac_sha256","ingress_route":"<route>"}`: no network target, no principals. The review needs
  the capability granted (not `secrets.use`) and refuses a credential that is not 32 to 256 bytes or a
  route the active release does not declare. The administrator enters the secret as any credential; Portal
  generates and shows nothing (owner decision 3). An endpoint is never usable as an outbound credential.
- **Granting.** Until the doc 175 capability policy review exists, an interim administrator route,
  modelled on `public.projection`, shows the routes, the address pattern and a digest, and enables or
  withdraws the grant row (`GET`/`POST .../installations/{installation}/ingress`; audit
  `app_platform.ingress_enabled` and `_disabled`). Withdrawal is immediate and allowed in any state.
- **Step 1 and the route's body bound.** The route's `body_bytes` is known only once the installation
  and its descriptor are read, so step 1 refuses anything over the largest allowed bound (4,096) with 413,
  and the route's own bound is applied right after the authority read, before any decryption, for a
  granted installation and a declared route whether or not the endpoint exists.
- **Statuses.** Unknown installation, route or endpoint, capability off, disabled app, revoked or changed
  binding, bad signature, replay and malformed requests are the one 401 `{"ok":false}`; an unknown
  endpoint still spends the work of a signature check. A restore pause, an unfinished operation, a trust
  failure or a custody failure of a real endpoint is 503 with `Retry-After: 30`. Backend 2xx is 202, any
  4xx is 400, anything else 503. The nonce is claimed at step 5 and is not given back on a later 429 or
  503, so a sender retries with a fresh nonce and timestamp.
- **Nothing is written by a request.** The summary (`app_platform.ingress_summary`, per installation per
  10 minutes: requests, accepted, forwarded, backend failures, refusals by class, per-endpoint accepted and
  refused) is written by the timer only. The open window's per-endpoint counts are shown in the binding list
  (console detail). Counters before an installation is known share one global bucket.
- **Console.** The credentials page offers the operation with a route field and shows the endpoint address
  and the signing recipe in the approval step; the settings page has the grant panel.
