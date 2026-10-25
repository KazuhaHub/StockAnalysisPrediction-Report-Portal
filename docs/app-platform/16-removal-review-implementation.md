# Actor-bound disable and uninstall review transactions

Status: P03 in progress. This is the first denial-only review/consumption slice,
not the installation/update admission endpoint or a completed lifecycle executor.
It uses the approved control tables and protected metadata; there is no migration
or extra database. Dependencies are control storage #193, operation leases #194
and the independently reviewed atomic keyring repair #195.

## Internal entry points

`app_platform_actor.go` supplies:

- `initializeAppPlatformControl(authenticatedSession)` for a fresh logical platform.
- `withAppPlatformActor(session, initialize, callback)` for the protected SQL boundary.

`app_platform_removal_review.go` supplies:

- `createPlatformRemovalReview(session, installationID, disable|uninstall)`.
- `consumePlatformRemovalReview(session, planID, planSHA256, idempotencyUUID)`.

These are private Go entry points. No route, public SDK or runnable worker is
registered. A future HTTP handler must construct the session proof from verified
server session state and apply the existing authentication, enrollment and CSRF
gates. It must not decode that proof, an actor scope, or a runtime command from
the caller's JSON. Legacy applications and their existing management routes do
not switch to these repositories implicitly.

## Current administrator identity

The repository verifies account creation identity, lifecycle generation, current
administrator role, active/expiry state, session revision and a live, unrevoked
tracked session with the exact signed expiry. A preliminary read filters invalid
proofs before lazy keyring initialization. The authoritative read repeats inside
the existing principal-lifecycle transaction lock.

On PostgreSQL the authoritative user/session reads also hold share locks. Direct
password, role and tracked-session revocation writes cannot pass the checked rows
while the control transaction commits. Current OU identities, ancestry and resolved
policy are included in the authority fingerprint. Account validity uses database
UTC time and the current valid panel timezone; malformed expiry/timezone refuses.
After resource/plan locks are acquired, the repository resamples database time and
rechecks session/account expiry, authority and controller markers before admission.
A plan that expires while waiting for an installation lock cannot use the earlier
timestamp to pass consumption.

The accepted schema limits review actor labels to 320 bytes and account creation
labels to 64 bytes. These private entry points enforce those limits without
truncation. They do not change the Portal's general username rules. Before public
admission, compatibility handling for any existing identity exceeding those limits
still needs to be resolved; no endpoint is exposed with that behavior hidden.

The opaque actor scope is HMAC-SHA256 over a versioned, framed account lifecycle
identity. A 32-byte random root seed is purpose-sealed under the existing keyring.
It is not a username hash, session ID or notification grant. Password/session or
OU changes preserve account scope while changing the authority fingerprint.
Deleting and recreating an account changes scope, even if its name is reused.

The seed, controller epoch and explicit unpaused marker are initialized in one
transaction. Partial/missing/tampered/foreign-purpose values refuse; initialization
cannot unpause restored state. It cannot generate a replacement identity over
existing installations, runtime bindings, plans or operations. Existing package
metadata alone does not imply an actor identity already existed.

A logically empty database is not proof that a former runtime has stopped. Fresh
metadata initialization does not enroll, contact or trust an existing runner.
The separate runner enrollment/reconciliation gates remain required after restore.

The keyring data key is loaded before obtaining the business transaction's single
SQLite connection. Only purpose-bound cryptography and SQL run inside the callback;
never perform HTTP, ZIP inspection, filesystem snapshots or runtime effects there.
See [atomic keyring initialization](../keyring-initialization.md).

## Exact review projection

The server generates canonical JSON containing the installation/product/publisher,
action, current and reserved generation, installation/policy/trust revisions,
controller epoch, actor authority fingerprint, runtime identity/revision, active
release digest, grant-set digest and owned data namespace/fence/revision.

Disable explicitly preserves data; uninstall explicitly retains it. There is no
delete-data mode, image, shell command, destination credential or new permission
in this projection. The review lasts 15 minutes using database time. Its exact
bytes and SHA-256 are persisted in the approved immutable review record.

At consumption the repository regenerates the entire current projection and
compares its exact canonical bytes, SHA-256 and duplicated relational columns.
This also rejects unknown fields, duplicate fields, alternate field spellings,
noncanonical encodings and rewritten data consequences; they cannot equal the
server-generated projection. It does not rely on permissive JSON decoding.

Publisher revision changes require new review. A disabled publisher or unavailable
runtime does not authorize new execution, but it does not prevent planning the
denial of its existing installation. Package signature admission and readiness
are intentionally not inferred from these cleanup fixtures or stored rows.

## Consumption transaction

1. Validate the plan/hash/key shapes and recheck current actor/control authority.
2. Look up the actor-lifecycle/idempotency identity before inspecting consumed or
   expired plans. Same request returns the current original operation; changed
   input conflicts. All future admission paths must use the same key namespace.
3. For a new request, confirm plan ownership, freshness and unused status. Lock
   runtime binding, installation and plan in the accepted order after the shared
   principal lock. Refuse another unfinished operation for the installation.
4. Recompute and compare the exact review and current authority. Counter exhaustion
   refuses before incrementing anything.
5. Consume the plan and advance installation generation/revision/control fence.
   Persist disabled/removed intent and retire the old active routing row. Keep the
   operation queued; these database changes do not prove physical shutdown.
6. Advance the owned namespace's logical writer fence/revision while retaining its
   ownership and data. A counter cannot stop an already running filesystem writer;
   real stop/reconciliation evidence is still mandatory before reuse or deletion.
7. Insert exactly one queued operation and its safe audit record in that transaction.
   Failure rolls back the consumed plan, denial state, routing and data fences.

A second key cannot consume the same plan. Eight concurrent identical commits
produce one initial operation and seven observations of it. An exact retry remains
an observation after the original plan expires; it never re-executes effects.

Uninstall sets removed **intent** with a pending phase. Its admitted operation may
still claim a lease for its restricted uninstall stages. Other removed-installation
work remains refused. No start/activate stage exists in the uninstall pure model.
Progress must derive from both intent and unfinished operation, not label removed
intent as completed removal.

Future fresh-install admission must also refuse unfinished operations for an older
installation of the same product, including removed-intent rows. The live-product
unique index alone excludes removed intent and is insufficient for that check.
Admission under the shared mutation boundary must enforce this before allocating
a replacement identity or adopting retained data.

## Verification evidence and limits

Actor contract tests were written and observed failing before implementation.
Removal integration tests exposed the wrong conflict classification when another
key reused an already consumed plan; that regression was observed failing and fixed.

Fixtures run on SQLite and disposable loopback PostgreSQL and cover:

- Stable actor scope across fresh Server caches and password/session changes;
  changed authority after password/session/OU changes and stable identity through
  a keyring rewrap.
- Deleted/recreated account refusal, role/active/session-revocation/expiry checks,
  missing/foreign seed, missing epoch, restore pause and refused identity regeneration.
- Atomic rollback of partially initialized control metadata.
- Disable and uninstall for both static and declared managed fixtures.
- Exact-request retries, changed-input conflicts, consumed-plan reuse conflicts,
  stale policy/trust/audience/data/JSON and expired-plan refusal without mutation.
- One operation under concurrent identical submission, and usable leases for
  admitted removal operations after intent is fenced.
- Failure of operation insertion rolling back every preceding control change.
- Expiry while blocked on a real PostgreSQL installation row lock, refusing after
  the lock becomes available. The regression was reproduced before the fresh-time check.

Full integration exposed an existing PostgreSQL restore allocation problem:
`setval` does not roll back when later artifact validation rejects the restore.
Rewinding to an older dump's IDs left the restored live rows intact but made the
next audit insert collide. `backup_identity.go` now captures live-row and sequence/
cache high-water floors before clearing any table, then advances allocation past
both those floors and incoming rows. It never lowers allocation to the older dump.
Non-unit/cycling policies and exhausted ranges refuse. A failed restore may leave
normal sequence gaps; it cannot lower allocation into the rollback target's rows.
The deterministic regression proves both row rollback and a successful later audit
insert. Restore still requires the documented offline procedure and stopped clients;
these changes do not make concurrent online restore safe.

Signatures and runtime declarations in fixtures are synthetic public test data.
No container starts, no model runs, no live credential is copied, and no notification
is delivered. These tests do not prove the actual broker, runner shutdown, job/event
revocation, package publishing or full LIFE-11/LIFE-13 acceptance cases. They prove
the internal control transaction portions of PKG-06, LIFE-01/LIFE-05 and DATA-08.

### Reproducible checks

Use a disposable database: the existing PostgreSQL integration fixtures replace
test rows and exercise forced restore. The local target was a fresh database in
an isolated PostgreSQL 18.6 cluster bound to loopback, not a production database.

```sh
TEST_POSTGRES_DSN='<disposable-loopback-database>' \
  GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 ./...
TEST_POSTGRES_DSN='<disposable-loopback-database>' \
  GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -race ./internal/app -run '^Test(Postgres)?AppPlatform' -count=1
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go vet -p 2 ./...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go build -p 2 ./...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
```

These local checks passed after the observed rollback/expiry regressions were
fixed. The PR race selector now includes AppPlatform, Keyring, Seal and secret-key
rotation tests; the normal Go job supplies PostgreSQL 16 integration. Local targeted
race checks also used real PostgreSQL connections. Neither a prior dependency's
green check nor a local result replaces this head's exact-source CI gate.

## Remaining work

- Install/update/enable/repair/restore/delete-data review and typed admission,
  including current publisher signature/compatibility/capability checks.
- Conditional stage commits, external effect intent/reconciliation and the runner
  journal; actual stop/remove evidence and retained-data finalization.
- HTTP/session-proof construction, localized DTOs and error mappings, enrollment/
  CSRF enforcement, visibility and the production host shell.
- Capability/broker/background/notification revocation and coordinated data/key
  recovery verification against real released fixtures.

No production routes, deployment, Status adoption or server beta settings change.
P03 and the overall application platform remain incomplete.
