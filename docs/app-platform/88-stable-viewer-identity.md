# Stable application viewer identity

This development phase supplies the host's current-viewer identity foundation.
It does not enable managed launch or the browser-facing backend request route.
The existing static launch admission gate remains complete and unchanged in scope.
The independently signed runner relay from phase 87 still needs a managed active
authorization adapter before it can serve authenticated browser requests.

## Identity and authority have different lifetimes

`appplatform.DeriveViewerIdentity` accepts only host-resolved account/OU facts and
an installation's private 32-byte identity seed. It returns the pinned shared
protocol's `Viewer` shape: opaque subject, current session revision, current role
and opaque actual OU ancestors. It creates no bearer token or access grant.

The HMAC-SHA256 frame is the standard Go JSON encoding of a string array:

```text
["portal.app-viewer-identity/1-dev.1", appID, installationID, purpose, ...fields]
```

Account purpose `account` binds username, account creation identity and canonical
decimal account generation. OU purpose `ou` binds canonical decimal OU ID and OU
creation identity. Outputs are lowercase 64-character hex. Distinct purposes and
array framing separate principal types and field boundaries. The seed and raw
principal identifiers never enter the returned viewer.

Generation, release, data schema, mutable audience-policy revision, controller
epoch, backend boot and signing-key ID are deliberately absent from this frame.
Normal updates retain installation ID/seed and therefore retain subjects/OUs.
Role, session revision and membership remain current facts; they are not cached
or used as stable identity inputs. Backend assertion keys continue rotating under
their separate existing derivation and full managed execution identity.

Reinstall creates a new installation ID/seed, as approved in document 13. It does
not silently adopt old subjects or retained data. Account or OU recreation also
changes the corresponding identity. Explicit retained-data adoption remains a
separate lifecycle task. This provisional internal implementation is not D5 SDK
publication/freeze.

Malformed app/installation IDs, invalid seed length, negative revisions, missing
creation identities, invalid UTF-8/control characters, duplicate/nonpositive OU
IDs, empty ancestry and ancestry longer than 64 are refused with an empty viewer.

## Host read transaction

`readPlatformLaunchViewerContext` is a private host adapter, not an HTTP endpoint.
It uses `withPlatformLaunchAccountReadContext`, which shares the existing launch
transaction and passes the account already authenticated inside that transaction.
Existing asset/document handlers keep their previous callback interface.

The transaction holds principal, publisher and installation authority, validates
the actual live session/account generation, resolves the actual OU chain, checks
the explicit user/OU audience grant and verifies the signed release/activation.
There is no administrator bypass or Default-policy ancestry fallback. A missing
creation identity anywhere in the actual OU chain now rejects the chain rather
than silently omitting that ancestor, including when a direct user grant exists.

Only after admission does the adapter read the durable keyring pair in this same
transaction and unwrap the purpose-sealed installation seed. It does not call
lazy keyring initialization or use the cached DEK. Missing/corrupt keyring material
is refused without repair. The normal keyring opener can rewrap with a configured
previous secret key; this read deliberately uses only the current key and performs
no rewrap. Existing explicit keyring recovery must finish before this read works
after a secret-key rotation. Temporary unwrapped keys and seed buffers are cleared.

All derived facts remain private until the launch gate's final live account/
session, publisher-time and controller-epoch/restore checks and transaction commit
succeed. Any failure returns an empty viewer. No username, role, OU list, seed,
destination or backend identity is accepted from an app request body.

## Verification scope

Pure tests include a fixed independently calculated HMAC vector, namespace and
principal-recreation separation, malformed/duplicate/oversized inputs and stable
identity across an actual backend registration/key change. The new registration
signs a request with the derived viewer and the pinned shared verifier authenticates
its exact facts under the new boot/key.

SQLite/PostgreSQL tests exercise ungranted and revoked sessions, live role changes,
OU moves, recreated OUs, actual ancestor grants and immediate loss of access after
reparenting/recreation. They check missing/corrupt keyring and seed/AAD failures,
previous-key-only unwrap refusal, restore pause, cancellation and managed-launch
refusal. Database dumps before/after reads verify no durable mutation, including
on missing keyring despite a previously populated DEK cache. A final-check fixture
revokes the session after derivation inside the transaction: the gate returns an
empty access result and rolls back, so the buffered facts cannot authorize a read.

The generation-stability database fixture seeds coherent rows to test this read
boundary. It is not evidence of an implemented normal update. Protocol signing is
a pure test; there is no new Linux container behavior in this phase.

Local verification used Go 1.26.10 with `GOWORK=off`, `GOMAXPROCS=2` and `-p 2`:

- Shared protocol/identity plus related SQLite launch/frontend/audience race tests:
  appplatform 1.223s, app 21.259s. The final added exact-64-OU boundary also passed
  a focused identity race run (1.281s).
- Related PostgreSQL viewer/launch/frontend/audience race tests: 14.467s, using one
  disposable isolated PostgreSQL cluster. The cluster was stopped after completion.
- Broader `TestPlatform|TestViewerIdentity` local regression: appplatform 1.244s,
  app 8.382s; PostgreSQL-dependent cases in that command have no configured DSN
  and are covered separately by the explicit PostgreSQL run above.
- `go vet` for appplatform/app, Linux/default-check-set staticcheck for those
  packages, workflow actionlint and `git diff --check`: passed. An exploratory
  macOS all-checks staticcheck run reports pre-existing style/platform-specific
  unused-code diagnostics; it is not a clean all-checks acceptance claim.

The quota-probe workflow now includes viewer identity and audience source paths
in its pull-request/main triggers. General CI's existing dependency vulnerability
gate is still a separate release requirement; no module pins are changed here.

## Next integration

1. Reconstruct active managed authority from committed activation/effect evidence,
   independent of the completed installation worker lease. Keep immutable backend
   bootstrap policy identity separate from current mutable audience policy.
2. Resolve the current viewer within that admission snapshot. Sign only a bounded
   method/path/body after the explicit current audience and capability checks.
3. Bind the separately authorized native request to that activation/boot, recheck
   current account/OU/grants and execution authority after IO, and discard a response
   if authority changed. Lost writes remain outcome-uncertain; do not auto-retry.
4. Complete broker/provider and writer gates, bounded socket storage, activation/
   scheduling and the remaining normal update/rollback/uninstall recovery paths.

There are no schema, dependency/toolchain, production, real-credential, Status or
server beta configuration changes in this phase.
