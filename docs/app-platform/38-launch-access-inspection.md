# Current-reader launch access inspection

This is an internal repository prerequisite for P06 launch integration. It does
not register a route, finish an installation, activate code, mint a token or serve
frontend bytes. Production and legacy Status behavior are unchanged.

## Inputs and output

`Server.inspectPlatformLaunchAccess` accepts a trusted current login snapshot
(`platformActorSession`) and an installation UUID. A future authenticated handler
must construct that snapshot from server session state, never a JSON request or
an app message. This repository is not an authentication endpoint.

On success it returns only installation ID, generation, policy revision, entry
path, signed release SHA-256 and frontend archive SHA-256. Counters are decimal
strings, including values above JavaScript's exact integer range. It returns no
signing key, identity seed, credential, grant policy, scoped token, backend URL or
resource bytes. Every error returns an empty result.

The returned value is a point-in-time logical binding, **not** a reusable access
permit or physical readiness proof. A future asset handler must reauthorize in
its serving boundary and bind the requested installation/generation/release to
that result. Do not authorize once, cache this result indefinitely, then serve
mutable legacy `/app-assets/` content. No such handler exists in this slice.

## Checks in transaction order

1. Reject malformed installation IDs. Begin a transaction and acquire the same
   principal lifecycle guard used by management admissions. SQLite obtains write
   intent; PostgreSQL takes the existing advisory lock. Nothing is initialized.
2. Sample database time. Check the current account is active, its creation and
   lifecycle generation match, its session revision matches, the session is
   present and not revoked/expired, and the account's date expiry has not passed
   in the configured timezone. These are the same session checks as management,
   but an application reader need not be an administrator. The management wrapper
   still explicitly requires the current admin role.
3. Acquire the existing publisher trust guard and load current trust. Require a
   valid existing controller epoch and an explicit unpaused marker.
4. Lock the installation. Require enabled/ready state, current epoch and positive
   generation/policy revision. Runtime-bound installations are unavailable until
   actual execution and broker integration exists.
5. Lock current OU rows before loading topology, including protection against old
   row-update helpers that do not take the advisory guard. Resolve the reader's
   actual primary OU chain. Check an exact user lifecycle grant or an exact
   lifecycle grant on an actual ancestor OU. Lock a matching grant through the
   final check. Current management writers must retain the principal-first lock
   order; future operation/effect/grant writers must also lock the installation.
6. Lock the active activation and its namespace. Generation/policy must match the
   installation, observation must be present, namespace must still be owned by
   this installation and active, format must match, and its logical writer fence
   must be positive. The namespace fence and controller operation fence are
   separate counters; comparing them would manufacture an unsupported guarantee.
7. Refuse incomplete or recovery-required operations, uncertain prepared/unknown
   effects, granted capabilities or capability rows at another policy revision.
   This deliberately matches the current install admission: static apps with no
   required provider and optional capabilities declined. It does not implement a
   capability broker or silently grant an optional provider.
8. Reverify the stored descriptor/signature against current publisher policy and
   key validity; compare signed app/publisher/release identity to the installation.
   Reinspect actual stored ZIP bytes and their digest/declared entry, using the
   existing bounded frontend inspector. Cached verification columns are not
   sufficient. Check current platform compatibility and the declared data schema.
9. After all potentially blocking row locks and bounded archive inspection,
   sample database time again. Recheck the reader account/session, signing-key
   validity and epoch/pause markers. Refuse a future observation timestamp.
   Commit the read transaction, then return only the public binding.

The repository does not create or lazy-open the DEK, write an audit row, change
state, or claim that a `ready` database string proves actual resources exist.
SQLite's existing no-op lock statement leaves the logical dump unchanged.

## Audience behavior

A direct user grant pins username, account creation and account generation.
Recreated accounts do not inherit it. An OU grant pins ID and creation identity;
reused IDs do not inherit it. An absent OU creation identity cannot pin a grant. Current membership and parent links are read during
each inspection, so moving a member or subtree changes access immediately on the
next inspection.

An administrator has no implicit audience bypass. Managing an installation and
opening it are separate permissions. A direct or actual-ancestor grant can allow
an administrator to open it normally.

A user with no explicit primary group belongs to Default. An actual descendant
of Default can inherit its audience grant. A historical independent root may
inherit Default's **policies**, but it is not in Default's audience subtree.
`ouGraph.actualChain` supplies this distinction; using `policyChain` here would
incorrectly expose applications. Cyclic, missing or otherwise invalid topology
refuses inspection, including when a direct user grant exists.

## Reproducible verification

Use only a disposable database with the approved migration. Never point these
fixture tests at Portal data. From the Portal checkout:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -p 2 ./internal/app -run 'PlatformLaunch|AppPlatformActor'
```

Set `TEST_POSTGRES_DSN` to a disposable PostgreSQL database to execute the second
driver's cases; absent that variable those cases skip. The activation fixture
explicitly seeds coherent logical records and completes its fixture operation;
it is not a provider, resource receipt or installer completion path.

Both-driver tests cover normal user access, no grant, user and OU lifecycle
mismatch, actual ancestry/reparenting, independent-root Default fallback,
Default membership, cyclic topology, invalid current state, data ownership state,
missing/future observations, stale epochs, paused restore, damaged descriptor or
frontend content, publisher/key revocation, revoked/expired accounts/sessions,
uncertain effects, incomplete/recovery operations and unsupported capability
state. Additional cases check admin isolation and exact large decimal counters.
Success and refusal cases compare logical dumps to detect durable mutation.

PostgreSQL tests hold a real release row lock, observe the launch query waiting
in `pg_stat_activity`, let either the reader session or signing key expire, then
release the lock. Both must return empty failure. This exercises the final time
sample after a demonstrated wait, rather than only an already-expired input. An overlay that deliberately retains
the initial clock sample makes both cases fail by returning a binding after
expiry; the normal source retains the final sample.

Local verification passed the full Go suite with disposable PostgreSQL, followed
by the final targeted race suite (launch and existing administrator checks) after
missing-OU-identity hardening. `go vet`, staticcheck and the Go build also passed.
There are no web changes or production/server configuration changes in this slice.
Exact-source CI must still be checked for the new PR.

The prior covered-host source `df2b40cf3622c7875db2b49091bd41f1da84f274` passed
[full CI 40257791625](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40257791625).
This is regression evidence for that source; this slice requires its own checks.

## Remaining integration

Implement authenticated immutable asset delivery with exact request binding,
normal-user authorization on each request, bounded extraction and explicit
sandbox CSP/cache behavior. Then connect the experimental frame to real launch
metadata. Physical data/execution receipts and actual activation commits remain
installer requirements; this read gate cannot replace them. Sizing, navigation,
drafts, real-browser UX acceptance, D5 review and the rest of P00–P14 remain open.
No public API/SDK freeze, production deployment, server channel change or Status
migration is authorized by this implementation.
