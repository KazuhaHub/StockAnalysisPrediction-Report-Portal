# Shared executor ownership and network invocation budgets

Status: development implementation locally verified; exact revision CI pending.
No production broker listener, effective grant, persistent credential product or
installed-application network route is enabled. P07 and full P00–P14 remain open.
The credential storage decision in document 131 is still pending and is not
required for this in-memory runtime scheduling work.

## Why this owner is required

A bounded HTTP client does not establish an installation budget if a caller
constructs a new client for every request. Separately signed viewer requests
must share one executor when they belong to the same current installation grant,
and changing grant/epoch cache keys must not multiply active installation slots.
The runtime also needs finite deployment-wide work and cache capacity.

`broker.NetworkPool` is an internal owner of those shared executors. The future
trusted broker service must construct exactly one pool for its deployment and
retain it across requests. Neither package manifests nor app/browser payloads
can select its limits, create a pool or provide a factory. Creating one pool per
request would bypass these limits and is forbidden. There is no production
service constructor or listener wired in this slice.

## Resource rules

`NewNetworkPool(active, entries, current)` requires a real current-authority
callback. Infrastructure-selected active capacity is 1..8, cache capacity 1..32.
Invalid bounds and zero-value/nil authority refuse. This is in-memory scheduling
state only; it introduces no database, journal or secret storage product.

A request takes a global slot without waiting before parsing/copying its bounded
packet/body. Excess work returns unavailable immediately. Packet/signature/body
bounds are checked before copying; original caller mutation cannot substitute
bytes between validation, authority checks and dispatch. Temporary body copies
are cleared on return. A slot is released on every success/refusal/failure path.

Signed metadata is verified under trusted controller enrollment, and separate
body length/digest is checked. The initial online authority lookup receives a
context bounded by the caller, signed expiry and policy deadline, not an unbounded
background context. It must honor cancellation. Refused/expired/stale authority
cannot allocate a cache entry. The actual signed NetworkCall still performs all
three current-authority checks before DNS, before dispatch and after buffering.
The initial owner lookup is additional, not a replacement for these checks.

Executor keys contain deployment, controller epoch/revision, installation, review,
generation, capability policy revision, release digest and exact network policy
digest. Viewer scope and request ID are deliberately excluded: different viewers
and invocations share the same grant budget. Full signed packet/actor validation
and current authority remain per invocation, never cached in the executor.

Before acquiring an executor, count active references across every cache key for
the same deployment and installation. Refuse if that count reaches the current
request's reviewed policy concurrency. Old/new epochs or policy versions cannot
multiply available slots. If a newer policy lowers the limit below already active
work, deny new work until old references settle; online revocation still blocks
old response delivery. This counter does not undo an already dispatched remote
effect or forcibly kill an old request before its deadline.

Each executor retains its own policy slots as a second boundary. A new key may
create a client only within the bounded cache. On exhaustion, evict the least
recently idle entry; never evict an entry with active references. If every entry
is active, refuse instead of creating an untracked executor. A release is locally
idempotent. Cache entries contain only public policy and scheduling facts, never
credentials, session/assertion values, response bodies or cached authority grants.

The cache mutex covers bounded lookup/reference/creation/eviction only. Default
executor creation validates policy and allocates local objects; it does not perform
network or callback work. DNS/TLS/current-authority/IO never run under the mutex.
HTTP connections remain request-local and nonreused, as in the bounded executor.

## Errors, retries and restart

Use the executor's fixed denied/unavailable/uncertain errors. No pool path retries
an operation or releases a post-IO revoked response. Initial lookup cancellation
is unavailable because no HTTP dispatch has happened. Once dispatch is attempted,
NetworkCall/HTTP retain their uncertain/no-automatic-replay semantics.

Pool caching is not a replay ledger. Constructing a fresh invocation from a still
valid signed packet is not globally deduplicated by the pool. The authenticated
transport must keep packets away from apps, own invocation identity and never
recreate an uncertain operation. The existing single-use call object prevents
local concurrent reuse only. Crash recovery/durable deduplication requires its
separate protocol/storage proof; no hidden persistent map/file is introduced.

A new process creates an empty scheduling cache. It may not use that fact as
permission to execute: current controller epoch, installation, policy/review and
actor/resource checks are still mandatory. Restore pause and stale process
fencing must be proved by the actual current-authority/runner lifecycle adapter,
not by this in-memory owner.

## Tests and evidence

Pool tests were written before the API existed and failed to compile. They then
exercise real signed requests and local TLS through a private test-only factory.
The exported constructor has no factory/dialer/TLS override for application use.

- A blocked real request and a second request to the same grant produce one
  executor/one remote request; the second refuses the shared concurrency limit.
- The global active bound refuses excess work, with cleanup after completion.
- An active entry cannot be evicted. Once released, an idle entry is replaced
  within the fixed capacity; release twice does not lower the reference count.
- Changing policy revision and epoch cannot create extra installation slots.
- Modified body/signature and already-canceled contexts cannot allocate clients.
- Initial revocation refuses without caching/dispatch; a blocked current-authority
  lookup has a policy deadline and releases all resources when canceled.
- Zero-value pools, missing current authority and invalid capacities refuse.

Full local broker/control/platform race tests, vet, Linux amd64 broker compilation
and diff checks pass. Exact CI must run the explicit broker/contract checks,
ordinary PostgreSQL-backed full Go tests, web/race/Docker regressions and disposable
native/browser/daemon acceptance. A pool unit test is not a real Portal current
principal adapter or a granted marketplace feature.

## Remaining integration

The private authenticated transport still must own this pool once, select trusted
limits, bind current SQL authority to exact signed policy, enforce peer/enrollment
identity, and recheck authority before releasing the runner reply. Initial grant
review/admission and SDK/host calls remain disabled. The credential storage
proposal can be reviewed independently; no proposed table or stored credential
is implemented by this owner. Production, Status, publication, stable SDK and
server beta configuration are unchanged.
