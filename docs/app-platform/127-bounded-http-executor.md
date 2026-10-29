# Bounded HTTP capability executor

Status: internal development executor implemented; integration and exact-revision
CI evidence pending. This advances P07 but does not enable a capability for an
installed application. Required capabilities still refuse installation and
optional capabilities remain declined. P00–P14 and D5/D6 remain incomplete.

## Purpose and authority

`internal/apprunner/broker` implements real HTTPS execution under a host-selected,
immutable policy. It is separate from the SSO/market fetchers: those have distinct
redirect/private-host rules and are not an application network grant.

`NewHTTP(policy, authorize)` is an internal Go constructor, not an HTTP endpoint,
SDK method, durable grant or package declaration. There are no production callers.
Its mandatory authority callback must eventually bind current installation,
release/generation, controller epoch, policy revision and foreground actor/service
resource authority to the exact selected policy. The executor calls it before
DNS, after DNS immediately before dispatch, and after buffering the response.
Success is never cached. Returning an error discards the whole result. A closure
that always returns nil is only a test fixture, never an acceptable Portal adapter.

The constructor does not accept custom TLS roots, an insecure-TLS flag, DNS
resolver, proxy or replacement dialer. Unexported seams let package tests exercise
real local TLS while presenting a public DNS answer; installed apps cannot select
these seams. Production networking remains disabled in app containers.

## Development policy and request limits

A policy has one canonical HTTPS origin, one to sixteen path prefixes and one to
seven explicitly selected methods from GET, HEAD, POST, PUT, PATCH, DELETE and
OPTIONS. CONNECT/TRACE are unavailable. Origins use lowercase ASCII DNS names;
IP literals, numeric final labels, userinfo, fragments, trailing dots, paths,
queries, explicit default port and noncanonical ports refuse construction. A
nondefault port must be explicitly part of the reviewed origin.

Prefixes are ASCII paths with literal unambiguous segments. A prefix matches
itself or a child segment: `/v1` matches `/v1/item`, not `/v10`. `/` explicitly
means all paths on that one origin. Empty paths, repeated separators, dot
segments, percent-encoded paths, backslashes and Unicode paths are refused.
Queries are permitted within the bounded request URL, but cannot change the
origin or path. Request URLs are at most 4,096 bytes, paths at most 1,024.

| Resource | Configurable range / behavior |
| --- | --- |
| Request body | 1..65,536-byte policy maximum; empty body allowed |
| Buffered response body | 1..1,048,576-byte policy maximum |
| Deadline | Positive and at most 15 seconds, bounded further by caller cancellation |
| Concurrent requests | 1..4 per shared executor; excess refuses immediately |
| Request headers | Only Accept and Content-Type, each at most 256 printable ASCII bytes |
| Response headers | Transport bounds 8 KiB; only one bounded Content-Type is returned |
| DNS results | 1..16; every answer must pass policy before any dial |

The constructor clones policy slices; later caller mutation cannot expand the
policy. Body copying happens only after obtaining a concurrency slot. One
executor must be shared for a single concurrency allocation. A future global
admission owner must prevent creating a fresh executor per request or using
multiple executors to bypass an installation-wide limit. This package alone
does not enforce a deployment-wide budget or persist rate counters.

## Network enforcement

All returned DNS addresses are checked, including unused answers. A mixed
public/private answer set refuses. Loopback, private, link-local/metadata, CGNAT,
unspecified, multicast, documentation, benchmarking and reserved address ranges
are denied. IPv4-mapped addresses are checked after unmapping. IPv6 is limited to
current global unicast `2000::/3` with conservative special-range exclusions,
including Teredo/6to4 and documentation ranges. NAT64/local-use translation ranges
are outside that admitted range. Narrow private-service rules are not implemented.
This conservative deny list is versioned source policy, not a complete dynamic
IANA allocation service; future changes need tests and review.

The dial uses exactly the first verified address plus the policy port; it performs
no second hostname lookup or address failover. The original hostname remains
HTTP Host and the TLS verification name. Production uses system certificate roots
with TLS 1.2 minimum and normal certificate/name verification. Environment proxies,
cookie jars, arbitrary headers and inherited Portal credentials are absent.

Redirects are never followed. Compression is disabled; multiple/unknown content
encodings refuse. The connection is not reused, so Go's retry of a broken reused
connection cannot replay an operation. There is no explicit retry or fallback.
The bounded body is fully read before final authorization and response delivery;
streaming is not supported. A raw upstream HTML body must remain data in any
future caller, never executable Portal markup.

## Outcomes and interruption semantics

Only three fixed errors leave the executor: denied, unavailable and uncertain.
No upstream URL, query, raw transport/TLS/resolver error, header or authority
callback diagnostic is attached to those errors. This package logs nothing.

- **Denied:** malformed/out-of-policy request, invalid DNS answer, missing or
  failed current authority before dispatch. No request was dispatched.
- **Unavailable:** already canceled, concurrency full, DNS failure/deadline before
  dispatch. No request was dispatched.
- **Uncertain:** HTTP dispatch was attempted and failed, redirected, timed out,
  disconnected, returned forbidden encoding/oversized data, or final authority
  refused. The remote server may already have performed an effect. No response
  status, content type or bytes are returned, and automatic replay is forbidden.

A valid bounded 4xx/5xx is returned as a response, not converted to a transport
error. A final authority failure cannot undo remote effects. This adapter does
not promise atomic remote writes or exactly-once semantics. Future service jobs
must preserve uncertain state rather than retry a write on lease expiry.

## Verification

Tests were added before implementation and initially failed to compile because
no executor existed. A later behavioral regression test demonstrated that looking
only at `Header.Get` accepted a second Content-Encoding; checking the complete
header value list fixed that failure.

The package tests use real local TLS sockets, certificate verification, POST and
buffered response handling, with test-only injected public DNS/pinned dial routing.
They verify one lookup/one dial, proxy bypass, exact HTTP Host, absent cookie/auth
headers, three current-authority checks and retained response status/body. Negative
cases cover origin/path/method/body/header policy, mixed unsafe DNS answers,
revocation before dispatch and after IO, redirects, fixed/chunked oversized bodies,
large headers, duplicate encodings, invalid TLS root/name, connection loss,
concurrency, caller cancellation/deadline and released concurrency slots.

Local package race/coverage checks pass (87.9% statement coverage at the first
complete run), `go vet` and Linux amd64 test compilation pass. Broader runner
regression and CI outcomes must be recorded separately before review. Tests do
not call an external provider or use real credentials. The general CI workflow
runs this package's race tests before the inherited vulnerability gate; disposable
Linux acceptance also includes it. No database shape, dependency, production,
Status, market catalogue or server beta change is included.

## Required continuation before granting network.http

1. Define/review a canonical durable network policy and exact plan projection;
   bind policy changes to required/optional review and activation. Do not encode
   a new business/secret store into arbitrary existing JSON to bypass D2.
2. Generalize the current exact denied snapshot checks into equally strict,
   provider-aware granted snapshots, preserving declaration/review/current-row
   agreement and stale-generation refusal.
3. Add current foreground and explicitly delegated service authority adapters,
   then bounded authenticated broker transport. An app cannot select the policy,
   callback, identity, destination dialer or trust roots.
4. Own executor lifetime and per-installation/deployment budgets; prove stale
   grant revocation, account/OU deletion, restore pause and generation fencing
   through the complete Portal/runner/SDK chain.
5. Add credential-specific use bindings, narrow private services and stream
   adapters only after their own authority/negative tests and necessary decisions.
6. Prove a neutral app performs this operation after marketplace installation,
   with no manual backend configuration. Keep all current providers declined
   until that integrated path has passing review/acceptance evidence.
