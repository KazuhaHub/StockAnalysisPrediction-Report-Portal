# One canonical network policy for review and execution

Status: internal development contract implemented and locally verified. Exact
revision CI remains required. This advances P07 network authorization, without
creating a grant, changing database products, enabling an app route, publishing
an SDK contract or completing the application platform.

## Problem and resulting behavior

The HTTP executor initially owned a private in-memory policy type and target
validator. A later review/transport adapter could independently interpret or
serialize the same limits differently, for example rounding a duration or losing
a method/path restriction. Reviewed bytes must bind the exact policy that the
executor accepts, before a capability grant can be enabled.

`internal/appplatform/http_policy.go` now owns the policy, validation, canonical
encoding, strict parsing and target checks. The runner's `broker.HTTPPolicy` is
an alias, and both constructor and dispatch use the shared validator. There is
no independent runner path/origin/method validator left. The DNS, TLS, response,
current-authority and uncertain/no-retry boundaries from the executor remain.

## Encoding rules

The internal format is `portal.http-policy/1-dev.1`. Fields, in canonical order:
`format`, `origin`, `paths`, `methods`, `request_bytes`, `response_bytes`,
`deadline_ms`, `concurrency`. No principal, actor/session assertion, installation,
credential, proxy, dialer, resolver or TLS override belongs in this object.

Example canonical bytes (one line, without trailing newline):

```json
{"format":"portal.http-policy/1-dev.1","origin":"https://api.example.com","paths":["/v1","/v2"],"methods":["GET","POST"],"request_bytes":65536,"response_bytes":1048576,"deadline_ms":1500,"concurrency":2}
```

- Normalize a host-selected Go policy using `NormalizeHTTPPolicy`. It validates,
  deep-copies and sorts path/method sets without modifying the caller. Exact
  duplicate members refuse; redundant overlapping prefixes are not silently
  removed or expanded.
- `CanonicalHTTPPolicy` encodes only the validated typed wire record. The full
  record is at most 8,192 bytes, including structure, not merely each field.
- `ParseHTTPPolicy` accepts exactly these canonical bytes and returns their
  SHA-256 plus a decoded policy. Unknown/missing/duplicate/case-aliased/escaped
  keys, trailing data, reordered keys/sets, whitespace, invalid UTF-8 and an
  unsupported format refuse. It uses the existing depth-24 token scanner before
  strict typed decoding and compares a complete canonical re-encoding.
- All numeric wire limits are integer lexical values. Deadline is 1..15,000
  whole milliseconds. Reject overflow and nonintegral Go durations before any
  conversion; `1ms + 1ns` cannot silently become an approved `1ms` policy.
- The existing public-HTTPS origin, ASCII segment-prefix, explicit method,
  byte/deadline/concurrency and header/network limits remain as described in
  [the HTTP executor](127-bounded-http-executor.md). The constructor now also
  rejects policies whose complete canonical record cannot fit the byte budget.

`HTTPPolicy.MarshalJSON` uses this canonical representation, preventing default
Go field names or duration nanoseconds from becoming a second wire format.
Nested `UnmarshalJSON` validates its complete object. A containing review still
must validate its own exact bytes: `encoding/json` may discard whitespace outside
a nested object's range. Complete stored/transport records must go through
`ParseHTTPPolicy`, not an unvalidated generic decoder. Encoding a larger object
with indentation is not a canonical policy record.

A parsed digest identifies bytes; it does not verify a publisher, grant a
capability or authenticate its caller. Parsed values are ordinary Go values.
Do not mutate a parsed policy and continue treating its old digest as evidence.
The executor copies validated policy on construction. Future dispatch must still
bind it to the exact current stored review/grant revision.

## Persistence boundary and continuation

The approved control storage specification already reserves typed quotas and
network-origin bounds in `app_platform_capability_grants.policy_json` (16 KiB).
This 8 KiB policy fits inside that product, but this slice writes no rows and
changes no review projection or activation. It introduces no independent entity,
secret binding, business data, principal snapshot or hidden side store.

The next integrated review must bind required/optional choices and these exact
policy bytes into the consumed plan and current grant rows. Current installation
requirements and exact denied-snapshot checks still refuse providers. Do not
weaken those checks or grant an unsupported required capability to test the new
parser. Complete authority/transport ownership, executor lifetime/budgets,
revocation, restore pause and real neutral-app acceptance remain required before
making the capability callable. Private services, streams and secret use remain
separate adapters; no public protocol/SDK freeze is implied by this dev format.

## Tests and evidence

Tests were added before implementation and initially failed because no shared
policy API existed. A fixed exact-byte fixture/digest, order-independent input
normalization, defensive copying, policy-change digest binding, malformed and
ambiguous JSON, integer overflow/fractions, complete record budget, target path
boundaries and invalid declarations are covered. The existing real TLS executor
suite runs unchanged against the shared contract. A short single-worker fuzz run
completed 99,501 executions without failure; this is bounded development evidence,
not exhaustive fuzzing or authorization proof.

Local full appplatform and broker race tests, go vet, Linux amd64 broker test
compilation and diff checks pass. CI must rerun the explicit pre-vulnerability
executor/contract steps, general regressions and disposable Linux acceptance at
the exact new functional head. Full P00–P14, stable SDK, actual grants and
production rollout remain incomplete. No production, Status or server beta
configuration changes are included.
