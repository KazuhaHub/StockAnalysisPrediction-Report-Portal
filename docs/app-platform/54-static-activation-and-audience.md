# Initial static activation and reviewed audience

This connects the existing static install operation's activate stage to the
existing protected launch reader. It is a repository implementation for the
initial generation, not the automatic installer, a market UI or a completed
platform release. Managed applications, capability grants, updates, takeover and
production deployment remain separate work.

## Review the audience before installation

`createPlatformInstallAudienceReview` takes explicit user and OU selections. The
existing no-audience entry point remains available and delegates with no targets.
An empty selection grants nobody, including the installer and administrators.
There is no implicit all-users, root-group or policy-fallback grant.

The audience is part of the existing canonical review plan JSON and therefore
its SHA-256 confirmation. It does not add a database table or migration. There
are at most 128 targets. Each target has exactly one type:

| Type | Input | Reviewed identity |
| --- | --- | --- |
| user | Existing username | Stored canonical username, creation timestamp, account generation |
| ou | Positive existing OU ID | Actual OU ID and creation timestamp |

User accounts must currently be active and unexpired under the Portal timezone
policy. Usernames must be valid UTF-8 and fit the existing 320-byte boundary.
Creation identities are bounded to 64 bytes. Numeric principal identities use
canonical decimal strings in the plan, avoiding browser integer precision loss.
Targets are sorted; duplicate, mixed, missing and unknown principal types refuse.
The whole plan remains bounded to 64 KiB.

Both initial review and consumption re-resolve these identities. Consumption
reconstructs the full canonical plan before admitting an operation. Every later
install stage validates it again, including a late check at activation commit.
Deletion/recreation, inactivity, expiry and OU identity changes refuse the old
review. User generation prevents same-second account recreation from inheriting
a grant. Changing the selected audience requires a new review; the worker cannot
supply another list at activation time. Review and operation admission create no
access grants.

An OU selection authorizes its current actual descendants. It does not freeze the
membership tree at review time. The existing launch reader traverses current
actual ancestry on every read. Moving a child outside the selected OU removes
that inherited access; policy fallback groups never supply audience ancestry.

## Activate only the exact prepared candidate

`activatePlatformStaticInstall(ctx, lease)` accepts only the initial unmanaged
install generation at activate. It opens a context-bound transaction and locks
OU mutation, publisher trust, the confirmed shared data runtime, installation and
operation in the existing order. It validates current controller epoch, pause
state, lease, consumed review, sponsor account authority and publisher/key trust.
The static installation keeps runtime_id NULL; the shared binding is only the
data-preparation provider, not an application backend.

The candidate must contain exactly one current namespace and one prepare-data
effect, with no activation or access grant already present:

1. Namespace origin/owner match this installation; state is active, quota is the
   reviewed 256 MiB, format is positive, writer fence is 1 and revision is 2.
   There are no retention/deletion markers or future database timestamps.
2. The effect is applied, belongs to the exact runtime/epoch/original lease fence
   and has the same observed fence. Its update time equals the namespace time.
3. Reconstructing the command from the signed descriptor and durable identities
   produces the exact stored logical digest. The canonical normal or recovered
   runner receipt matches that command, namespace format and observed bytes.
   Its observation cannot be in the future on the local clock.
4. The signed frontend remains current. The descriptor has no backend and its
   data schema equals the prepared namespace format. The actual archive compiles
   with `BuildFrontendDocument` into the supported self-contained document
   profile. A valid publisher signature alone cannot bypass this compilation.
5. The reviewed audience still resolves to the same eligible identities.

The receipt proves historical data preparation. It is not a running backend,
a live writer check or managed readiness evidence. The real receipt producer
remains the authenticated local transport; repository-only fixtures are not
physical acceptance evidence.

## One atomic visibility boundary

After a fresh lease-clock check, the transaction:

1. Inserts one active activation for generation/policy revision 1.
2. Records bounded readiness evidence (`portal.static-readiness/1-dev.1`) tying
   controller epoch, effect, namespace, canonical receipt digest, archive digest,
   compiled document digest and reviewed audience digest together.
3. Inserts exactly the reviewed user/OU access grants with creation identities,
   account generations and fresh grant UUIDs.
4. Uses compare-and-swap to move the exact pending enabled static installation
   to ready and increment its revision.
5. Uses compare-and-swap to move the exact operation from activate to complete,
   increment state/row revisions, record completion and clear the lease.
6. Writes an installation-scoped audit event, then repeats the captured lease,
   pause/controller, audience, sponsor and publisher checks before commit.

Failures, cancellation, expiry and failed writes roll back all these changes.
The prepared namespace/effect remain available for a valid retry; a successful
activation cannot replay under its old lease. The returned result contains the
activation identity and completed snapshot, not new worker authority.

The existing protected directory/document reader supplies current session,
account-generation, actual OU ancestry, signed release, installation generation,
policy, namespace and pending-operation checks. Administrators do not bypass app
access grants. No broadening of legacy app routes or public status access occurs.

## Verification and limits

Repository tests cover SQLite and PostgreSQL success, explicit self grants,
ordinary-user OU inheritance and later detachment, no implicit administrator
access, stale principals, duplicate/malformed targets, mutated preparation and
trust evidence, unsupported signed frontend, replay and per-write rollback/retry.
PostgreSQL delayed-audit tests additionally cover lease expiry and request
cancellation after activation writes, ensuring no partial ready/grant state.

The disposable Linux fixture now continues after actual non-root Portal data
preparation and authenticated receipt retrieval: it calls the real repository
activation method, verifies lease completion and explicit grant, then reads the
protected launch document. Its report adds
`native_portal_static_activation_and_launch` only after that fixture succeeds.
The preceding assertion still proves data-stage completion alone did not grant
access or activate the app. The workflow covers future audience, review and
activation source changes as well as data changes. Physical evidence must come
from a successful workflow run and its downloaded report, not a cross-compile.

This does not introduce an HTTP installer, polling worker, permission settings
UI, runtime-free/non-Linux static data provider, managed container lifecycle or
release gate approval. Those are still required by the full platform plan.
Sensitive capability-broker work must also resolve the existing sandbox
self-navigation disclosure concern before such capabilities are exposed. No
production runtime, real service credentials, Status migration, public SDK freeze
or server beta configuration is changed here.
