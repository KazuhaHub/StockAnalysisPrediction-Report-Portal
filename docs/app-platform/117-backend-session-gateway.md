# Current-page backend requests and session gateway

Status: development implementation; no production runtime binding, SDK dispatch,
public SDK freeze, Status adoption or server configuration change.

## Purpose

A page that remains open after a release or access-policy change must not submit
work to a different current backend. The host must preserve the page binding and
must derive the viewer from the current Portal session. An application supplies
only its own API method, relative route and JSON data.

PR #318 adds the bound private execution entry point. This continuation adds the
session HTTP boundary that the parent MessagePort host will call. These are
prerequisites; the SDK and interactive browser acceptance are still outstanding.

## HTTP contract

`POST /api/app-platform/installations/{installation}/backend` accepts only a
managed Portal session and `application/json`. Existing account activity and
mandatory enrolment checks apply. Cross-origin requests, including an opaque
iframe origin, are refused. The parent host performs the HTTP request; iframe
code must never receive the session cookie or a backend credential.

The exact request object has four required, case-sensitive keys:

```json
{
  "binding": {
    "generation": "1",
    "policyRevision": "1",
    "releaseSha256": "<64 lowercase hexadecimal characters>"
  },
  "method": "POST",
  "path": "/v1/echo",
  "body": "{\"value\":\"example\"}"
}
```

The installation identity comes solely from the validated route. Generation and
policy counters are canonical positive decimal strings. Unknown or duplicate
keys, missing fields, nulls, alternate casing and trailing JSON are refused.
`body` is a string preserving the exact UTF-8 JSON bytes used by the downstream
MAC; it is not re-serialized as a JSON object. GET requires an empty body.
Existing route validation refuses external URLs, traversal and the reserved
handshake operation. No headers, credentials, destination or viewer fields exist.

The body is at most 64 KiB, and the outer request at most 400 KiB to accommodate
JSON escaping. The request-body read deadline is five seconds; production log
and gzip writer wrappers expose `Unwrap` so ResponseController can reach the
underlying connection. Native execution has a separate twenty-second context.
Two backend requests share one deployment-wide admission budget independent of
document compilation. A saturated budget responds 429 before dispatch.

Current account, audience, publisher, runtime and completed writable history are
read transactionally before checking whether a trusted endpoint is configured.
A valid page binding is only a precondition; it never grants authority. The
bound execution path repeats current source and binding checks before signing,
then repeats authority/source checks before releasing a native response.

The response is HTTP 200 with `{ "status": 200, "body": "<exact JSON bytes>" }`.
The inner status is the validated application status. Application headers,
cookies, redirects and authentication challenges are not forwarded. All paths
use no-store, nosniff and no-referrer response policy.

A pre-dispatch access/source failure returns a generic 404 without a backend
body. An authorized app with no configured execution endpoint returns 503.
After a business dispatch, a missing/unverifiable response or authority change
returns 502, code `app_platform_request_uncertain`, and `retryable: false`.
There is no Retry-After or automatic retry for uncertain writes. The app can
have written successfully even when the result must be withheld.

## Trusted runtime binding

`bindPlatformBackendHTTP` is a private, explicit development bootstrap. It
accepts one canonical absolute Unix socket and a non-root controller UID/GID;
the current process must be Linux and have that UID. An atomic set-once pointer
prevents replacement during in-flight requests. The existing root transport
independently authenticates peer identity and the current reviewed execution
profile on each request. Browser input and app manifests never set this value.

The production startup does not call this binder. Enabling it for production
remains part of D6 and requires the separately approved rollout. Current
writable execution still supports completed generation-one installations only;
this does not claim update/rollback completion.

## Verification and remaining work

The PR #318 functional commit is
`4f0682a0cf7e4c88a5e1a393f735e13e0f052d49`. Local focused tests passed (0.787s),
expanded writable request/access/launch race tests passed (50.786s), Linux amd64
and arm64 builds, amd64 test compilation, vet and configured staticcheck passed.
Fourteen quota collector tests passed. The additional non-default
`staticcheck -checks=all` run reported existing style issues in unrelated files;
those files and the configured check set were not changed.

Disposable Linux run 40839676986 / job 123332738266 passed, including
`native_portal_writable_page_binding_native=true` and owned cleanup. Its evidence
still says `execution_ready=false`. Current launch bindings reached the real
backend; stale generation, policy and release writes changed neither its call
counter nor its write counter, and audience revocation refused a formerly valid
binding.

General run 40839676991 passed the affected SQLite/PostgreSQL steps, build, vet,
configured staticcheck, frontend checks, Docker smoke checks and race jobs.
The Go job failed at the existing `x/net` v0.60.0 vulnerability gate
(GO-2026-6866 and GO-2026-6860); ordinary full Go tests after that gate were skipped.
This is not a fully green CI claim. No dependency or vulnerability suppression
was introduced.

Gateway local admission, closed decoder, bootstrap refusal, enrolment and real
connection read-deadline tests passed (6.306s). The final focused race run passed
(22.948s), including the real-connection read deadline; Linux amd64 test
compilation, vet and configured staticcheck also passed. Gateway Linux native/SQL
CI remain pending at this commit. The native fixture now exercises an ordinary user's session HTTP write,
rejects body-spoofed identity, interrupts a write response and verifies exactly
one write after the interruption, then revokes access. Required evidence markers
are `PORTAL_WRITABLE_BACKEND_SESSION_HTTP` and
`PORTAL_WRITABLE_BACKEND_HTTP_NO_RETRY`; adding these checks is not evidence that
they have already passed.

Next work connects the nonce-bound private MessagePort protocol, shared SDK and
React host to this endpoint, with cancellation, response limits and explicit
uncertain-write UI behavior. No token bridge or arbitrary HTTP fallback is
allowed. Marketplace lifecycle, capabilities/broker, update/uninstall/recovery,
D5 visual acceptance and D6 production approval remain open across P00–P14.
