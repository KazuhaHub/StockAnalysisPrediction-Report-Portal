# Current viewer requests after a completed managed install

This development phase connects the private Portal host to the native backend
relay using current Portal session/audience authority. It adds no HTTP endpoint
or bridge method. Public managed launch, normal managed activation and production
execution readiness remain closed. This is a platform path, not Status migration.

## Completed history is not a worker lease

`preparePlatformManagedBackendAccess` admits an internal read only when the
installation is enabled/ready in the current controller epoch, its active
activation matches generation and current audience-policy revision, the runtime
is enabled/pinned, and the current session/account has an explicit audience grant.
Administration does not bypass that grant, and the original installer does not
have to remain an administrator for other authorized users to use the app.

This initial reconstruction supports generation 1, one completed managed install
and three exact applied effects: prepare data, start candidate and registration
delivery. It rejects pending/recovery operations, unknown/prepared effects,
additional activations, granted provider capabilities and inconsistent policy
revisions. Later generations/operations require their own lifecycle integration;
there is no fallback to first-install records after an update.

The read validates the canonical consumed review projection/hash, trusted signed
release, reviewed runtime profile, data namespace ownership/format/quota/writer
fence, and the exact original data/start/delivery receipt chain. It compares
effect IDs, native ID, command digests, receipt hashes and temporal bounds.
An incomplete chain is refused; a read does not repair it. The operation must be
complete with no lease owner/expiry. A separate `platformManagedSource` tuple
selects historical bytes; no live `platformOperationLease` is fabricated, renewed
or used to bypass the existing install-stage guards.

The old readiness record is checked for structural binding only. It is never
called fresh health or sufficient execution authority. The completed operation
and all receipts remain unchanged. A request gets newly signed, short-lived
references to the original logical candidate/registration identities under the
current sealed control key; this creates no root effect and no new registration
file. The read-only DEK opener is shared with phase 88 and never creates/rewraps
keyring rows or falls back to a cached key.

## Current policy and immutable bootstrap identity

The active activation's mutable policy revision must equal the installation's
current audience-policy revision. That revision is captured separately in the
private snapshot, alongside the current complete account facts and pseudonymous
viewer. Audience-only changes do not rewrite the bootstrap identity's original
policy revision 1, its derived signing key, document hash or applied delivery.
Re-deriving a different document after such an edit would make the protected
native file disagree; this path deliberately refuses that design.

The shared data-command and registration-material builders now take an explicit
source tuple internally. Existing install-lease wrappers preserve their behavior.
Temporary seeds/keys/documents are cleared; registration handles must be closed.
Private access snapshots reject JSON export and redact string formatting.

## One native request and response consumption

`requestPlatformManagedBackend` accepts only method, canonical `/v1/` request URI
and bounded JSON body, plus a trusted host session/configured root endpoint.
Reserved handshake aliases, unsupported methods, invalid/oversized JSON and GET
bodies fail before database/native access. The app supplies neither destination,
viewer, role, OUs, assertion, control signature nor a bootstrap file.

The host performs these steps under a 20-second overall context:

1. Read current access and complete source history in a protected transaction.
2. Re-observe the exact reviewed root runtime profile and perform the existing
   independent key-possession handshake over the protected native channel.
3. Verify the actual answer's MAC/challenge and fresh native provenance, obtain
   the current process boot ID, and read current access again. Any account, role,
   OU, audience-policy, source/runtime/activation/fence or document change refuses
   dispatch.
4. Inside that second transaction, sign the bounded request with the current
   viewer/boot and then its separate controller authorization. The signatures
   remain buffered until final session/trust/epoch checks and commit succeed.
5. Invoke the native relay exactly once. It keeps all phase-87 root journal,
   container/data/file/socket/peer, nonce, request/body and response bounds.
6. Validate the opaque reply's exact endpoint/controller, request ID/raw hash,
   delivery receipt hash, native/boot IDs and freshness. Re-read current access
   after IO and compare the complete snapshot again before releasing its JSON
   status/body. Upstream headers/cookies never enter this result.

The final response boundary is factored into
`completePlatformManagedBackendRequest`, used by the production path and native
revocation acceptance. Empty/JSON-made observations cannot authorize a response.
No snapshot can be cached to skip subsequent current-state checks.

Any error after business-request dispatch returns an empty response plus an
outcome-uncertain error. This includes loss, cancellation, invalid/stale native
proof, expired signature or changed/revoked access after execution. A write may
already have happened; no rollback is claimed and there is no automatic retry.
App-level idempotency is still required for deliberate business retries.

## Verification and limits

SQLite/PostgreSQL repository fixtures seed final complete/active metadata around
canonical synthetic history. They verify absent worker leases, read-only state,
normal-user access after installer demotion, independent current policy/bootstrap
identity, malformed/missing/revoked source authority, live session/audience checks
and signature construction. They do not prove native execution or a working
normal managed activator.

The Linux native fixture starts with real Portal data/start/delivery receipts and
the actual app process. Only final complete/active Portal rows and an explicit
audience grant are seeded. It calls the private host path as a normal user, checks
that body-supplied admin/subject values do not become viewer claims, changes only
audience policy without replacing the actual registration, then revokes access
after a real write and before consuming its opaque result. The response must be
withheld as uncertain; a newly dispatched write must be refused, and the actual
app execution counter must prove neither was retried. The probe requires the
`MANAGED_CURRENT_VIEWER_REQUEST_VERIFIED` marker. This remains a development
fixture, not a managed activation implementation or browser acceptance.

Local verification with Go 1.26.10, `GOWORK=off`, `GOMAXPROCS=2`, `-p 2`:

- Related SQLite access/request/viewer/delivery/candidate race: 20.211s.
- Related PostgreSQL access/viewer/delivery/candidate race: 13.384s. The dedicated
  disposable cluster was stopped after the test process completed.
- Broader local `TestPlatform` regression: 8.125s; PostgreSQL-dependent cases have
  no DSN in that command and are covered separately above.
- Linux amd64 test-binary compilation, vet, Linux/default staticcheck, 14 Python
  probe tests and diff checks passed. Staticcheck first caught a test cleanup
  deferred before its expected error check; that cleanup order was corrected.

Native run 40639878051/job 122655297068 initially failed its outer verification
gate: the controller child's new marker was not explicitly checked/forwarded by
the Portal root-parent fixture. The Python probe correctly refused the missing
evidence. The parent now independently requires the marker from its captured child
output before forwarding it. A fresh complete native run is required; that first
failed run is not recorded as native acceptance.

Native acceptance for this phase must be read from its own CI run, not inferred
from local compilation or the prior phase. Phase 88's functional source
`28e11d251ccb9e82cf05773dea29668d79e5acf7` separately passed native workflow
40637673431/job 122648283789, including owned-resource and registry cleanup.
Its general CI finished with only the existing x/net govulncheck failure
(GO-2026-6866/6860 at v0.60.0); the following non-race Test step was skipped.

Normal activation must still establish actual current boot/health/writer and
broker authority and resolve the root command acknowledgement lifetime deliberately:
the current relay uses the original, unacknowledged applied registration chain.
Do not simply acknowledge/retire that chain and assume requests will continue.
Bounded writable socket storage, normal scheduling/restart recovery, provider/
writer gates, updates/rollback/uninstall and the SDK/reference apps also remain.
There are no schema, dependency/toolchain, production, real-credential, Status,
SDK-freeze or server beta changes in this phase.
