# Proposal: reviewed multi-origin and private-network targets

Status: accepted 2026-11-17; implementation pending. Depends on
[174](174-background-broker-egress.md). Index and order: doc 174.

## 1. Problem and evidence

The reviewed network policy admits exactly one destination:

- `portal.http-policy/1-dev.1` has a single `origin`
  (`internal/appplatform/http_policy.go:19`, wire field `:42`); the origin must be
  `https`, with no path or query (`:64`), and its host must be a DNS name
  (IP literals refused, `:181-202`).
- Every resolved address must be public (`internal/apprunner/broker/http.go:136`,
  `:223-251`); private ranges, CGNAT and documentation ranges are denied.
- One grant row per installation and capability holds that policy
  (`internal/app/app_platform_schema.go:45`, primary key
  `installation_id,capability`), and the grant loader requires the stored
  policy to equal the policy inside the consumed install or update plan
  (`internal/app/app_platform_network_request.go:84-133`). A policy can change
  today only through a new install or update review.
- Secret bindings bind the full network policy digest and the installation
  policy revision (`internal/app/app_platform_secret_bindings.go:427`, `:447`),
  so any network policy change invalidates every binding.

Status monitors are configured in its own UI, one URL per monitor, with
optional private CIDRs and an explicit plain-HTTP switch
(Apps `apps/status/service/internal/status/model.go:78-79`, `:113-130`;
`probes.go:188-206`). Dify targets are operator-chosen hosts
(`probes.go:258-264`); DeepSeek and Qwen are fixed public origins
(`probes.go:265-272`). A one-origin policy changed only by release update
cannot carry this.

## 2. Threat model

| Threat | Control |
| --- | --- |
| An app adds destinations silently | Every target change is an administrator-consumed review; the app can only *request* |
| Administrator opens the host or LAN by mistake | Private targets need a deployment-level allowlist set by the host operator in the runner's root-owned configuration; Portal review can only select inside it |
| Reaching the host itself (Portal, runner, Docker API, Cockpit, metadata) | Loopback, link-local, unspecified, multicast and the runner's own addresses are refused even inside an allowlisted prefix |
| DNS rebinding into private space | A DNS-named target is either public-only or private-only; every resolved address must match that class and its allowed prefixes |
| Plain-text credentials | Plain HTTP only inside a private rule that sets `plain_http`, never with a credential binding |
| Friction drives administrators to over-broad rules | In-context host consent batches up to 16 targets per review; no wildcard hosts |
| Binding churn on every policy change | Bindings bind one target digest, not the whole policy (section 3.3) |

## 3. Contract

### 3.1 Policy format `portal.http-policy/2-dev.1`

Canonical JSON, keys in this order, ≤ 16,384 bytes (the
`policy_json` column bound, `app_platform_schema.go:45`):

```json
{"format":"portal.http-policy/2-dev.1",
 "targets":[{"id":"dify-prod","origin":"https://dify.example.com","paths":["/v1/info"],"methods":["GET"],"network":"public"},
            {"id":"lan-api","origin":"http://10.20.0.5:8080","paths":["/health"],"methods":["GET"],"network":"private"}],
 "request_bytes":65536,"response_bytes":1048576,"deadline_ms":15000,"concurrency":4}
```

- `targets`: 1..128, sorted by `id`; `id` matches `^[a-z][a-z0-9-]{0,47}$` and
  is unique; origins unique.
- `network: "public"`: rules of v1 apply unchanged (https, DNS name, public
  addresses only).
- `network: "private"`: host may be a DNS name or an IPv4/IPv6 literal; scheme
  `https`, or `http` only when the deployment allowlist entry that contains it
  sets `plain_http`; every resolved address must lie in one deployment
  allowlist prefix and outside the always-denied set.
- Shared bounds keep the v1 maxima (`http_policy.go:20-24`).
- A request names its target by URL; the executor selects the unique target
  whose origin matches and applies that target's paths and methods.

v1 policies remain valid and are read as a single public target with
`id: "default"`.

### 3.2 Deployment private-network allowlist

The runner's private service configuration (`app-runner serve -config`,
`cmd/app-runner/main.go:102-105`) gains:

```yaml
private_networks:
  - prefix: 10.20.0.0/24
    ports: [443, 8080]
    plain_http: false
```

At most 16 entries; each prefix at least /16 (IPv4) or /48 (IPv6) narrow;
loopback, link-local, `0.0.0.0/8` and multicast are refused at load, and so is
any prefix that contains an address assigned to a host interface (which covers
container bridges and VM NAT gateways). The runner advertises the
canonical allowlist SHA-256 and entries in its network runtime profile
(`internal/apprunner/transport/network_runtime.go:22-39`, new fields
`private_networks` and `private_networks_sha256`). An empty list means no
private target can be granted. Changing it requires a runner restart and makes
every installation whose policy uses a private target fail its next profile
check until re-reviewed.

### 3.3 Binding change

Binding policy v2 (doc [176](176-background-credential-use.md)) replaces
`network_policy_sha256` with `network_target_sha256`, the SHA-256 of the
canonical target object the binding may be sent to. Binding use checks that
this target is still present, byte-identical, in the current policy. Adding or
removing *other* targets leaves the binding valid. The installation
policy-revision equality check (`app_platform_secret_bindings.go:427`) is
replaced for v2 bindings by this target check plus the existing release,
generation, trust, epoch and credential-revision checks.

### 3.4 Capability policy review `portal.capability-policy-review/1-dev.1`

One generic review for changing a capability's policy after install, reused by
docs 179–181. It follows the secret-binding precedent: a row in
`app_platform_review_plans` with `action='update'`
(`app_platform_secret_bindings.go:235-238`), a closed canonical plan, a
5-minute preview expiry and consume-time re-validation.

Plan fields: installation, app, publisher, generation, release, installation
revision, policy revision, trust revision, controller epoch, capability name,
previous grant policy SHA-256, new canonical policy bytes and SHA-256, the
runtime network profile SHA-256 (for network capabilities), and the reviewing
administrator authority.

Consume, in one transaction: lock the installation and grant row; re-check
every field; for a network policy, re-observe the runtime profile and verify
each private target against the advertised allowlist; write the grant row
(`granted=1`, new `policy_json`, `review_plan_id` = this plan); increment
`installations.policy_revision` and the active activation's revision, as
principal invalidation already does
(`internal/app/app_platform_principal.go:70-75`); re-stamp every other grant
row of the installation to the new revision, because grant readers require
`policy_revision` equality (`app_platform_network_request.go:70`); audit
`app_platform.capability_policy_changed` with old and new policy SHA-256.

`loadPlatformNetworkGrant` gains a third plan kind: when the grant's review is
a consumed `portal.capability-policy-review` plan for this capability, the
stored policy must equal that plan's policy, and the plan's runtime facts must
match the current runtime as the install branch requires today
(`app_platform_network_request.go:84-122`).

### 3.5 In-context target requests

The app proposes; the host decides. New bridge message and SDK call, foreground
only:

```ts
const result = await network.requestTargets([
  { id: "dify-prod", origin: "https://dify.example.com", paths: ["/v1/info"], methods: ["GET"], network: "public" },
]);
// result: { state: "applied", policyRevision } | { state: "declined" } | { state: "not_permitted" }
```

At most 16 targets per call. Portal answers `not_permitted` unless the current
viewer is a Portal administrator who may manage this installation. Otherwise
the host (never the app document) shows a trusted dialog listing the exact
targets, marking private ones and plain HTTP prominently, and the resulting
policy diff. Confirm creates and consumes the review plan in one step;
cancel discards it. Status calls this when an administrator saves a monitor
whose URL is not yet covered, so the common case is one confirmation per new
host, in context. The administrator console offers the same review with
removal.

Routes (session-only, administrator):

```text
POST /api/app-platform/installations/{installation}/capability-policy/plan
POST /api/app-platform/capability-policy-reviews/{id}
```

## 4. Review and consent

- Install: v2 policies can be selected at install exactly like v1 (doc 140).
- Later: section 3.4, from the console or the in-context dialog.
- Private targets show the matching allowlist entry; a target outside it
  cannot be submitted.
- Removing a target is also a review; it takes effect at consume.

## 5. Failure and audit

Executor outcomes are unchanged. A request whose URL matches no target is
`denied` before lookup. A private-rule address mismatch is `denied`. A policy
change during an in-flight call yields `uncertain` at the release check.
Audit: each consumed review (old/new SHA-256, targets added and removed);
refusals by unknown target are counted in the doc 174 summary.

## 6. Migration and compatibility

v1 policies keep working. Installed apps move to v2 by a capability policy
review; no data migration. v1 bindings keep their whole-policy digest and
therefore still pause on any policy change; re-approving them as v2 moves them
to target scope. The SDK `requestTargets` is new and capability-gated.

## 7. Test plan (these must fail first)

1. `ParseHTTPPolicy` refuses a two-target document today; the v2 parser must
   accept it, refuse unsorted/duplicate targets and IP literals in public
   targets, and keep the exact-bytes round trip.
2. A private target is refused at review when the runner advertises no
   allowlist, and when the address is loopback, link-local or a host
   interface address, even if an allowlist prefix covers it.
3. A DNS private target resolving to one public and one private address:
   `denied`.
4. Plain HTTP refused without `plain_http`; refused with any credential binding.
5. Consume after a concurrent policy change, release change or profile drift:
   refused, nothing written.
6. Adding a target leaves a v2 binding on another target usable; removing its
   target makes it unavailable; a v1 binding pauses either way.
7. `requestTargets` from a non-administrator viewer: `not_permitted`, no plan
   row created.
8. Grant loader accepts a capability-policy plan and refuses one for another
   capability or installation.
9. SQLite and PostgreSQL, plus a native run against a disposable private
   upstream on a test-only allowlisted prefix.

## 8. Schema change

None. The v2 policy fits `app_platform_capability_grants.policy_json`
(J16384); the review uses `app_platform_review_plans` with the existing
`'update'` action and a ≤ 64 KiB plan; the allowlist lives in runner
configuration. Contract changes needing sign-off: `portal.http-policy/2-dev.1`,
`portal.capability-policy-review/1-dev.1`, the network runtime profile fields,
the binding policy target digest, and the `network.requestTargets` bridge call.

## 9. Owner decisions (2026-11-17)

The owner decided every question below on 2026-11-17.

1. Who may approve in context. Decided: Portal administrators only; an app
   role never suffices.
2. Wildcard hosts (`*.example.com`). Decided: not supported; 128 explicit
   targets cover Status, and wildcards defeat per-target review.
3. Allowlist owner. Decided: the host operator via runner configuration, not
   the Portal UI, so a Portal administrator alone cannot open the LAN.
4. Whether a policy change bumps `installations.policy_revision` (reloading
   open app documents). Decided: yes, for one authority revision; the binding
   target scope removes the main cost.
