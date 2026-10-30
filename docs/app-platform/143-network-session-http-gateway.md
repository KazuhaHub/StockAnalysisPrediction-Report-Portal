# Session-only HTTP gateway for reviewed application networking

Status: revision `8a11f1d40c883e1b134fbf5ab9706c6321342a35` passes full
general workflow 40991159862 and native quota/HTTP/browser workflow 40991159933.
Actual PostgreSQL and the new native session-HTTP assertions execute in CI.
The typed shared SDK, launch availability and browser network path remain
incomplete; see the separate WIP and remaining-work handoff in document 144.

## 1. Request and response

`POST /api/app-platform/installations/{installation}/network` is a session-only
route registered in the central table with the same enrolment wall and no-store
response wrapper as the own-backend gateway. Bearer credentials do not substitute
for a current Portal session. The same-origin JSON host performs this call;
an opaque-origin application document cannot call it directly.

The JSON object has exactly six required, case-sensitive fields:

```json
{
  "binding": {
    "generation": "1",
    "policyRevision": "1",
    "releaseSha256": "<current release SHA-256>"
  },
  "method": "GET",
  "url": "https://approved.example/v1/resource",
  "accept": "application/json",
  "contentType": "",
  "bodyBase64": ""
}
```

The nested binding also has exactly those three fields. Duplicate/unknown keys,
aliases, null/non-string values, trailing JSON and missing fields refuse. The
body uses canonical padded standard Base64; the empty body is an empty string.
Noncanonical padding/bits/newlines and decoded bodies above 65,536 bytes refuse.
Binary bytes, including zero and invalid UTF-8 octets, survive without string
replacement. Request method belongs to the shared supported HTTP method set,
URL is at most 4,096 bytes and the two supported header values are each bounded
ASCII with no control characters. The current reviewed policy still checks the
exact origin/method/path/body restrictions; shape validation grants nothing.

The complete JSON read is at most 128 KiB with a five-second connection read
deadline. This includes the largest Base64 body plus bounded escaped metadata.
There is no arbitrary headers, credential ID, actor assertion, runtime address,
proxy, resolver or TLS field.

Successful transport returns Portal HTTP 200 containing only:

```json
{"status":200,"contentType":"application/json","bodyBase64":"eyJvayI6dHJ1ZX0="}
```

`status` is the remote status, not a change to the Portal authentication status.
The remote body is bounded by the reviewed policy and shared one-MiB maximum.
Base64 preserves exact bytes. Remote cookies, redirects, authentication headers
and arbitrary headers are never forwarded. A remote 401 therefore remains an
explicit remote status inside the envelope. The SDK must decode this bounded
shape rather than interpreting it as a Portal session failure.

## 2. Admission, authority and outcomes

The handler checks path/installation shape, same-origin policy, JSON media type
and the current managed session. It has its own two-slot budget, independent of
own-backend requests and the original Portal analysis queue. Busy admission is
429 with Retry-After: 1; it occurs before dispatch. The bounded root executor
continues to enforce its separate shared/per-installation limits.

After decoding, the actual current-viewer network preparation validates the
page binding, complete current SQL/history, live grant and reviewed request
policy before runtime configuration is revealed. This preflight signs no durable
record and performs no IO. An authorized request with no trusted endpoint gets
503; denied/stale/current-policy failures return the fixed unavailable document.
The endpoint is the same immutable trusted development bootstrap already used
for own-backend HTTP. Payloads cannot install or replace it, and production does
not bind it in this change.

Actual dispatch invokes the complete coordinator from document 141, which repeats
current authority, observes the actual root provider, signs the real request,
checks each root challenge and rechecks before releasing the response. The
preflight is not a cached execution grant. The foreground context is 20 seconds;
response backpressure has a 30-second write deadline. Owned body buffers are
cleared on completion/refusal.

A possibly executed request with an uncertain result returns Portal HTTP 502:

```json
{"code":"app_platform_request_uncertain","message":"application request outcome uncertain","retryable":false}
```

It contains no remote body and no Retry-After. Neither host nor SDK may retry
that result automatically. Other execution denials use the fixed unavailable
response. No credential injection or browser capability token is introduced.

## 3. Verification and remaining work

`TestPlatformNetworkHTTPDecoder` covers exact binary round-trip at the maximum,
malformed/open schemas, aliases/duplicates, unsupported methods, header injection,
invalid binding, oversized body/wire input and noncanonical Base64.
Driver-paired admission tests cover anonymous/cross-origin/opaque requests, JSON
and query refusal, missing grants without runtime disclosure, revoked sessions,
independent budgets and no-store. The enrolment test exercises the actual route
wrapper. The focused CI group requires PostgreSQL rather than counting skipped
local cases as PostgreSQL proof.

The native fixture extends actual installed normal-user HTTPS acceptance with a
fresh host sharing the real SQL/configuration and the actual protected root
endpoint. It requires authorized-but-unconfigured 503 with zero IO, stale-page
refusal with zero IO, exact binary HTTP envelope with no forwarded cookies and
post-IO grant withdrawal returning 502/retryable:false without a body or retry.
The root parent and Python report require:

- `PORTAL_NETWORK_SESSION_HTTP`
- `PORTAL_NETWORK_SESSION_HTTP_UNCERTAIN`

The exact revision recorded at the top now passes these native assertions.
Earlier private coordinator success alone would not prove them. The direct and
HTTP cases together expect exactly four HTTPS requests, with the before-IO cases
adding none.

Next implement the typed bridge/SDK method, current launch availability and
browser acceptance with cancellation, stale-channel/current-page refusal and
uncertain-no-retry behavior. No public SDK freeze, catalogue publication, Status
migration, production deployment, new storage layout or beta setting is included.
