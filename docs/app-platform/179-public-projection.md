# Proposal: anonymous reviewed public projection

Status: proposal, 2026-11-17. Not owner-approved. No dependency on 174–178.
Index and order: doc [174](174-background-broker-egress.md).

## 1. Problem and evidence

- Every v2 viewer route is session-only: launch, backend, network, documents
  (`internal/app/routes.go:519-532`). The only unauthenticated app-platform
  routes are the two service-assertion routes (`routes.go:528-529`).
- App audience principals are `user` or `ou` only
  (`internal/app/app_platform_schema.go:44`); doc
  [54](54-static-activation-and-audience.md) states there is no implicit
  all-users grant.
- The backend relay requires a viewer assertion on every request
  (`internal/appplatform/backend_request.go:47-48`) and serves only `/v1/*`
  (`:17-46`).

Status's public status page and its JSON (`/public/v1/status`, `/history`,
`/incidents`, `Cache-Control: public, max-age=10`, a per-peer token bucket) are
parked in `servePhase2`, which the platform socket never calls
(Apps `apps/status/service/internal/status/server.go:343-361`, `public.go:13-68`,
`public_rate.go:20-45`).

Why not a `public` principal in `app_platform_access_grants`: an audience row
admits a principal to *launch the whole app*, including the frontend bridge,
backend relay and network routes, all of which assume a current account. A
public status page needs a few read-only responses, not an anonymous app user.

## 2. Threat model

| Threat | Control |
| --- | --- |
| App exposes private data | Only reviewed route IDs are reachable; the review states that the app decides content; the backend receives no identity, so it cannot personalize |
| App code on the Portal origin rides a visitor's session | Public documents carry `Content-Security-Policy: sandbox allow-scripts` without `allow-same-origin`, the precedent of `internal/app/apps_api.go:230-237`; public handlers never read cookies or `Authorization` |
| Anonymous traffic overloads the backend | Portal cache per route and query, per-client and per-installation rate limits, bounded concurrency, single-flight |
| Cache-busting with random queries | Declared query names only, bounded values, canonical ordering; unknown names refused |
| Probing for installed apps | Uniform 404 for unknown installation, disabled projection or unknown route |
| Response smuggling (HTML, scripts) | JSON only, `nosniff`, `default-src 'none'` on data responses, size bound |

## 3. Contract

### 3.1 Capability and descriptor

New capability `public.projection` (optional; requires a backend). New
descriptor block, part of the signed descriptor:

```json
"public": {
  "routes": [
    {"id":"status","path":"/v1/public/status","query":["limit","offset"],"cache_seconds":10,"response_bytes":262144},
    {"id":"history","path":"/v1/public/history","query":["component_id","from","to"],"cache_seconds":10,"response_bytes":262144},
    {"id":"incidents","path":"/v1/public/incidents","query":["limit","offset"],"cache_seconds":10,"response_bytes":262144}
  ],
  "document": "public/index.html"
}
```

- `routes`: 1..8, sorted by `id` (`^[a-z][a-z0-9-]{0,31}$`); `path` fixed,
  under `/v1/public/`, valid per `ValidBackendRequestRoute`; `query` 0..8 sorted
  names (`^[a-z][a-z0-9_]{0,31}$`); `cache_seconds` 1..300;
  `response_bytes` 1..1,048,576.
- `document`: optional path inside the signed frontend artifact under
  `public/`; every file the page loads must also be under `public/`.

### 3.2 Grant policy

The `public.projection` grant row holds
`portal.public-projection-policy/1-dev.1`: the reviewed route IDs (a subset of
the descriptor's), whether the document is enabled, and two limits chosen at
review: `client_per_minute` (default 60, max 600) and `backend_per_second`
(default 5, max 20).

### 3.3 Portal routes (anonymous)

```text
GET /public/apps/{installation}/data/{route}?{query}
GET /public/apps/{installation}/
GET /public/apps/{installation}/{asset...}
```

The `/public/apps/` prefix is unused today (`routes.go` registers only the SPA
catch-all `GET /` at `:590`). Data route processing:

1. Parse and canonicalize the query: only declared names, each at most once,
   value ≤ 128 bytes of printable ASCII, total ≤ 512 bytes, sorted.
2. Check the per-client bucket (client IP resolved with the existing trusted
   proxy rules, as `rateLimitV1` does, `internal/app/limits.go:164-175`).
3. Load current authority without a session: installation enabled and ready,
   no restore pause, current release, `public.projection` granted, route in the
   policy.
4. Serve from cache if fresh; else single-flight one backend fetch, within the
   installation's `backend_per_second` and at most 4 concurrent.
5. The backend fetch is a signed `portal.app-public-request/1-dev.1` command
   to the runner (same authority tuple and challenge as the backend relay,
   `actor_scope` = SHA-256 of `portal-app-public/v1:` + route ID). The runner
   forwards `GET <path>?<query>` with header `X-Portal-Public-Assertion`: a
   short-lived assertion signed by the same registration key as viewer
   assertions, binding installation, generation, route ID, path and query
   digest, with no subject, role or OU.
6. Accept only 200 with valid JSON within `response_bytes`; anything else
   becomes 503 (backend error) or 404 (route refused). Response headers:
   `Content-Type: application/json`, `Cache-Control: public, max-age=<cache_seconds>`,
   `X-Content-Type-Options: nosniff`,
   `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`,
   `Access-Control-Allow-Origin: *`, no `Set-Cookie`.

Document routes serve verified files of the active frontend artifact under
`public/` with `Content-Security-Policy: sandbox allow-scripts; default-src 'none';
script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self';
base-uri 'none'; form-action 'none'; frame-ancestors 'none'` and the same cache
and rate rules. The page fetches its data from the data route.

Cache: per installation at most 256 entries and 8 MiB; global 32 MiB; keyed by
installation, generation, policy revision, route and canonical query; cleared
on any change of those.

## 4. Review and consent

`public.projection` defaults off. The administrator enables it at install or
later through the doc 175 capability policy review. The review shows the public
URL, every route ID with its backend path and cache time, and the text "Anyone
who knows this address can read these responses without signing in." Disabling
is an immediate, audited revocation that needs no review (as binding
revocation, doc [150](150-secret-binding-use-and-rotation.md)) and empties the
cache.

## 5. Failure and audit

- 404 for unknown installation, projection off, route not granted; 429 with
  `Retry-After` on rate limits; 503 when the backend or runner is unavailable.
  Bodies carry only a fixed code.
- Audit: enable, policy change, disable. Traffic is aggregated: one
  `app_platform.public_projection_summary` per installation per 10 minutes
  (requests, cache hits, backend fetches, refusals by class). No client IP is
  stored in audit.

## 6. Migration and compatibility

Additive capability, descriptor block and routes. Apps without the block are
unaffected. Status moves its public handlers from `/public/v1/*` to
`/v1/public/*`, verifies `X-Portal-Public-Assertion`, and ships the parked
`public-src` page under `public/`. The old public URL is not preserved (the v2
line is a fresh install).

## 7. Test plan (these must fail first)

1. Descriptor parsing refuses a `public` block and the capability today.
2. Anonymous `GET /public/apps/{id}/data/status` returns the SPA today; it
   must return 404 until the grant exists, then JSON.
3. Undeclared query name, duplicate name, oversize value: 400 without a
   backend call.
4. A request carrying a valid session cookie: the backend sees no viewer and
   the response is identical to the anonymous one.
5. The public document cannot read `document.cookie` for the Portal origin and
   a `fetch('/api/me', {credentials:'include'})` from it is not authenticated
   (browser test).
6. 1,000 requests across 1,000 distinct allowed queries are held to
   `backend_per_second`; identical requests within `cache_seconds` reach the
   backend once.
7. Disable, uninstall, restore pause or release change: next request 404/503
   and the cache is empty.
8. Backend returns HTML, a 302, or 2 MiB: 503, nothing released.

## 8. Schema change (needs owner approval)

None. The grant and its policy fit `app_platform_capability_grants`
(`capability` T96, `policy_json` J16384, `app_platform_schema.go:45`); review
uses `app_platform_review_plans`; `app_platform_access_grants.principal_type`
stays `user`/`ou`. Contract changes needing sign-off: the capability, the
descriptor `public` block, the policy format, the public request command and
assertion, and the `/public/apps/` URL space.

## 9. Open questions

1. A friendly URL (`/status` instead of the installation UUID). Recommended:
   not now; it needs a unique slug column. Operators can map a path at the
   reverse proxy.
2. A separate public hostname. Recommended: allow as deployment configuration
   later; the CSP sandbox already removes same-origin authority.
3. Allow `POST` for public forms. Recommended: no; this capability is read-only.
