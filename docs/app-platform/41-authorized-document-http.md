# Authorized application launch and document HTTP responses

This development slice registers the current-reader launch and compiled document
repositories as session-backed routes. It is not a complete installed application
or a production rollout. The ordinary AppsHub/AppView flow is unchanged; the v2
installer must still produce actual activation evidence before the product host
can use these routes. Existing public legacy assets keep their own behavior.

## Routes and public binding

Both registrations go through the central route table with `authSession` and
`requireUserJSON`. The existing account-active/session-revision and forced
second-factor enrolment wall run first. A current administrator still needs an
application audience grant; management role does not bypass the launch repository.
Machine bearer tokens are not credentials for these routes.

```text
GET /api/app-platform/installations/{installation}/launch
GET /app-documents/{installation}/{generation}/{policy}/{release}/{frontend}/{entry...}
```

The first route reconstructs a trusted session snapshot from the signed cookie,
current account, persisted session record and account lifecycle generation. It preserves the signed cookie expiry, matching it to the session row rather than
allowing a later mutable row lifetime to extend that cookie. It
never accepts a username, principal generation or session proof from JSON/query
parameters. Unmigrated legacy cookies without a persisted session record are
unavailable on this new surface; the handler does not create/upgrade credentials.
Existing legacy routes and normal session migration remain unchanged.

The launch transaction rechecks the full user/OU/state/trust boundary and compiles
the document profile before advertising a URL. It returns:

```json
{
  "source": "/app-documents/<installation>/<generation>/<policy>/<release-sha>/<frontend-sha>/index.html",
  "binding": {
    "installationId": "<installation>",
    "generation": "1",
    "policyRevision": "1",
    "releaseSha256": "<release-sha>"
  },
  "capabilities": []
}
```

Placeholders above are explanatory, not accepted identities. Generation and policy
are canonical positive decimal int64 strings. Release/frontend digests are exact
lowercase SHA-256. Entry is the actual signed entry, which can include local
subdirectories. No app backend credential, signing key, session cookie, account
identity or authorization token is returned. Current supported launches have no
granted capabilities; the empty array is a presentation hint, not broker authority.

The document route compares **all** expected binding fields in the same transaction
that reads and compiles current bytes. The source is a version identity, not a
bearer permit. Reusing it after grant revocation, disable, generation/policy change,
restore pause/epoch change, session expiry or publisher/key revocation refuses.
It never serves mutable legacy `AppFile` content.

Query strings, an explicit empty query, percent-encoded path aliases, noncanonical
counters, invalid identity/digest syntax and paths beyond 1024 bytes are refused.
The handler does not clean an arbitrary resource name or choose a default entry
for a mismatched source. Standard ServeMux canonical redirects can occur before
handler dispatch for malformed path structure; those redirects contain no app
content and do not establish authorization.

## Response policy

A policy wrapper runs outside the normal session/enrolment middleware so handled
401/403/404/429 responses also carry `Cache-Control: no-store`, `nosniff`,
`Referrer-Policy: no-referrer` and a restrictive nonexecuting sandbox CSP. Failure
bodies are fixed safe messages; no raw database/trust/compiler error is exposed.
Business-state or binding failures use the same 404 body. Capacity refusal uses
429 and `Retry-After: 1`; missing authentication uses the existing 401 behavior.

On successful document delivery, replace only the error CSP with the compiled
[document CSP](40-self-contained-app-document.md#response-policy), use fixed HTML
content type and exact Content-Length, and write only after transaction commit.
There are no ETags, 304 shortcuts, range responses, CORS allowance or public cache.
`If-None-Match` and `Range` do not bypass authorization or return partial content.
HEAD follows the same checks/compilation and headers but writes no body.
Unsupported methods/unmatched paths are ordinary ServeMux responses, not app
content responses; this wrapper does not claim to change the global router.

The CSP blocks fetch and undeclared subresource families but is not comprehensive
network isolation against document self-navigation. The unresolved navigation/
exfiltration requirement recorded in document 40 remains open. No sensitive
broker output or permission is enabled by these registrations.

## Request budgets and cancellation

A lazy, race-safe per-Server budget admits at most two concurrent v2 launch or
document compilations. There is no waiting request queue and no durable setting.
Slots release exactly once on success or any failure, including cancellation.
Multiple Portal processes each have their own budget; this is not a distributed
rate limiter or a per-user fairness guarantee. Those remain scaling requirements.
The existing signed-session/enrolment middleware runs before this app budget.

After session reconstruction/admission, the request uses a five-second context
bounded by the caller's context. The launch transaction uses `BeginTx(ctx)` plus
an executor adapter forwarding **every** Exec/Query/QueryRow to its Context
variant. BeginTx alone does not make a later background Query cancelable. This
covers principal/trust/resource lock acquisition as well as final checks, on the
same transaction/connection. Any cancellation returns an empty repository result
and the fixed failure response, without exposing an already compiled buffer.

The context does not preempt arbitrary CPU work in the compiler. Archive/member,
HTML token/depth and document expansion limits still bound that local work; the
next context-bound statement/final commit refuses if the deadline elapsed. Normal
Portal authentication/account activity bookkeeping precedes this transaction and
retains its existing behavior. Do not describe this as a five-second whole-request
or global Portal database availability guarantee.

## Host compatibility

`AppFrameV2` now accepts `/app-documents/` sources in addition to its pre-existing
experimental source profile. For the new prefix, it checks installation,
generation, policy and release against the bridge binding, validates the frontend
digest spelling and refuses percent-encoded aliases before creating an iframe.
This is defense in depth; the server remains the authority. It does not register
an AppsHub/AppView page, authorize a user or change sandbox flags. Old AppView and
its legacy bridge remain unchanged. No separate app language picker is added.

## Verification

The initial Go tests failed on the absent HTTP budget/repositories. The frontend
source test failed before `/app-documents/` support existed.

Actual central-mux tests cover signed current ordinary-user launch metadata and
HTML, response policy, HEAD, conditional/range behavior, invalid counters/digests,
wrong generation, query/encoding aliases, anonymous reads, revoked grants/sessions,
disabled installations, existing enrolment mandates and exhausted slots. Existing
route-table structural/anonymous/enrolment tests cover the new registrations.

SQLite and disposable PostgreSQL run existing reader/OU/activation/trust/expiry
cases with the new context-backed executor. A PostgreSQL HTTP cancellation test
holds a real release row lock, observes the HTTP query waiting in
`pg_stat_activity`, cancels the request and requires a fixed empty-content failure
**before** releasing the holder. This distinguishes actual SQL interruption from
merely checking a cancellation flag after an unbounded wait. A temporary overlay
using background Exec/Query/QueryRow makes that cancellation test fail; the normal
source forwards context to every statement.

Frontend tests check the new binding/source match and retain legacy AppView plus
real bridge integration regression. The targeted frontend suite passes 44 cases. Web typecheck/lint/build pass; existing
unrelated lint warnings are not evidence of a new component failure. Full Go
regression with disposable PostgreSQL passed; exact-source CI must also be
checked for this slice.

Prior source `adca13d5c3b4343c984a4f2ff0a2392fc0d97730` passed full CI 40269034312
and Linux endpoint acceptance 40269034339. Its browser fixture proved real
React/Ant/bridge presentation over the compiled document; that fixture used its
own loopback serving layer, not this newly registered Portal HTTP path. Do not
claim real-browser end-to-end authentication acceptance for this route until a
browser fixture combines the actual handlers with the SDK/host.

## Remaining work

Connect real launch metadata to the shell and verify the actual HTTP path in a
browser, including direct navigation, refresh, logout/revocation and failures.
Add document compatibility validation to installation review, actual provider/data
receipts and activation commits, then complete managed lifecycle and the broker.
P06 sizing/navigation/drafts, D5 visuals and P00–P14 release acceptance remain open.
No production deployment, real credential, Status adoption, public SDK freeze,
new storage layout or server beta configuration change is part of this slice.
