# Live runtime enrollment observation and Portal confirmation

Status: internal Linux development integration under approved D1/D3/D4 candidate
A. This connects the prepared Portal signing identity to an explicitly enrolled
local runner through OS-authenticated transport. It does not enroll a runner over
the socket, admit managed applications, issue physical commands, provision accounts
or enable a production service. No database shape or beta configuration changes.

## Meaning of confirmed/enabled

The runtime binding's existing `enabled` flag represents **confirmed control
identity**. It does not represent a healthy Docker daemon, configured quota root,
capacity reservation, a running application or verified resource ownership.
Those are separate mandatory checks at runtime/app admission and physical dispatch.
The managed install gate still has no admitted runner compatibility profile and
refuses managed execution after this change; a regression test checks that refusal
with a confirmed binding present. Static apps retain their independent path.

This clarifies the earlier registration document's combined list of future gates:
confirming the enrollment channel can now enable its control binding, while actual
native compatibility evidence remains required before admitting/executing apps.
A database flag or public-key match must never replace that physical evidence.

## Closed read-only transport addition

The existing private and separate-UID profiles, inode checks, `SO_PEERCRED` checks,
three-second session budget and eight-session limit apply unchanged. The server
checks the configured controller UID before reading either request kind. The
client pins the server's file ownership/mode/inode and kernel UID before sending.

| Frame | Exact bytes |
| --- | --- |
| Request | ASCII `PRCQ0001` + 32 cryptographically random bytes + write-half EOF |
| Response | ASCII `PRCO0001` + the same 32 bytes + unsigned big-endian uint32 JSON length + canonical JSON + EOF |

JSON is 1..1024 bytes with exactly these ordered string fields:
`format`, `deployment_id`, `controller_epoch`, `control_revision`, `public_key`,
`fingerprint`. Format is `portal.runner.enrollment/1-dev.1`; revision is a positive
canonical decimal int64; UUIDs are canonical lowercase; the key is exact 32-byte
Ed25519 encoded in canonical unpadded base64url; fingerprint is its lowercase SHA-256.
The decoder verifies the identity and requires exact re-encoding equality, so
unknown/duplicate/alias fields, whitespace, noncanonical order/numbers/base64,
missing fields and trailing JSON are rejected. Wrong nonce, extra/truncated bytes
and oversized frames never produce an observation.

The server reads its actual durable `ControllerDeployment` on every query. An
empty/corrupt journal returns no identity and is never initialized by a query.
Only public enrollment data is returned; no filesystem path, secret, generic
method, SQL or physical receipt is exposed. Existing signed intent frames retain
their original format and signature verification. The two request kinds cannot
be combined into one stream.

`transport.ObserveControllerEnrollment` is the separate-UID client.
`ObserveEnrollment` is the private same-user development client. Their exported
`EnrollmentObservation` has private fields, defensive public-key copies and a
local monotonic receipt time. It can only be populated by a successful native
probe. Its zero value carries no evidence. This is a local observation on an
authenticated stream, not an independently signed or portable attestation.

## Portal entry point and transaction

`Server.confirmPlatformRuntimeBinding(ctx, session, initialRegistration, endpoint)`
is private. Its endpoint is operator bootstrap configuration with private path,
controller UID and GID fields; neither apps nor browser payloads choose it.

1. Clone caller-owned byte slices and preflight current administrator/control and
   the initial pending runtime snapshot under the shared actor transaction.
   Open the existing sealed seed with the binding-specific purpose and derive its
   public key again. Neither caller-provided public material nor a SQL pin alone
   is sufficient to choose the expected identity.
2. End that transaction before network I/O. Query the configured native endpoint
   through the separately provisioned UID/group profile. Non-Linux entry points
   return refusal, with no emulated OS identity fallback.
3. Begin a new actor transaction. Lock and reload the runtime row; check initial
   binding/deployment/epoch/control revision, sealed seed and public pin again.
   Require the single local binding profile and sane database timestamps.
4. Require the observed deployment, epoch, control revision, key and fingerprint
   to match exactly. Local proof age must be nonnegative and at most three seconds.
   Refresh current administrator/session/authority and pause/epoch markers after
   obtaining row locks. Never treat an earlier login check as a lease extension.
5. CAS initial `enabled=0, revision=1` to `enabled=1, revision=2`; preserve the key,
   deployment, epoch and control revision. Append one
   `app_platform.runtime_confirmed` audit with public deployment/pin and evidence
   kind `local-enrollment/1-dev.1`. There is no resource or app creation.
6. After all blocking writes, recheck **both** current actor authority and proof
   age before committing. An audit lock can consume either lifetime. Roll back
   row and audit together; return an empty result on any failure.

An exact retry supplies the same initial revision-1 snapshot, obtains a **new**
live observation and accepts only the matching enabled revision-2 row. It does
not append another audit or change identity. A later disable/change/restore row
revision is not an initial retry and cannot be automatically enabled by this API.
Restore-cleared keys, changed epochs, multiple runtime rows and stale snapshots
require the separate reconciliation procedure. Registration remains unable to
reset/recreate an enabled or restored binding.

`confirmPlatformObservedRuntime` is a private repository seam used by the native
entry point and database tests. It must never be called with a deserialized
browser/app observation or introduced as an HTTP handler. Database unit fixtures
can construct its private input to exercise transaction fences; that is not OS
identity evidence. The Linux end-to-end test below uses the actual endpoint entry
point and cannot substitute those fixtures.

## Verification and source boundaries

Tests were first compiled against missing codecs/confirmation methods and failed.
Pure tests cover canonical identity JSON and strict rejection. Linux transport tests
cover an unenrolled journal, read-only observation of real enrollment, defensive
ownership, wrong nonce/replayed or malformed replies, plus the existing framing,
identity and signed-command tests. The distinct-UID acceptance also exercises
successful observation and refusal to disclose enrollment to a different same-group
UID.

SQLite and isolated PostgreSQL tests cover matching confirmation, fresh exact retry,
unchanged refused state, stale/future/zero proof, wrong identity/key/epoch/revision,
cleared keys, changed rows/control/role, audit failure rollback and the continuing
managed-admission refusal. PostgreSQL tests observe an actual audit-table lock wait
through `pg_stat_activity`, then expire either the session or the three-second peer
observation and verify no enabled row/audit/result commits.

The disposable Linux workflow additionally compiles the **Portal package** race-test
binary and invokes only `TestPlatformRuntimeEndpointAcceptance` with root, an empty
environment and explicit hosted-worker flags. In one exclusive temporary fixture:

1. A child process permanently drops to numeric UID/GID 41001 with cleared inherited
   groups, starts an isolated in-memory Portal database, initializes the fixture's
   administrator control and prepares a random sealed signing identity.
2. It sends only its public enrollment material over a dedicated pipe.
3. The root parent explicitly enrolls that public identity in a new root-only
   journal, starts the protected root endpoint in a root:41001 0750 directory, and
   signals readiness over another pipe.
4. The non-root child calls the real Portal endpoint entry point, performs the
   authenticated nonce probe and confirms the binding transactionally. It verifies
   that no installation or operation was created.
5. Both processes, listener, journal and copied executable are removed. No system
   account, existing directory, real credential or production state is modified.

The workflow must execute and pass this test against the final source before Linux
end-to-end acceptance is claimed. Local macOS cross-compilation cannot prove this
boundary. Prior registration source `c985af4bda3b61eba6eb2b17c09410fc58260342`
passed full repository CI
[40249657209](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40249657209).

Next: lease-bound command signing/delivery, actual configured native compatibility
and data/container providers, physical reconciliation/receipts, full installer and
React/Ant/SDK integration. Production enrollment/rollout, real credentials, Status
adoption and public SDK freeze remain separate approvals.
