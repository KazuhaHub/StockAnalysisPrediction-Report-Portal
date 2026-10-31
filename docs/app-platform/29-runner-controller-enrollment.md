# Explicit controller enrollment journal primitive

Status: internal P03/P04 development implementation within approved initial D2
journal and D1/D3/D4 candidate A. It adds no table, column, migration, side-store,
production privilege, control socket or startup enrollment. It is a prerequisite
for authenticated runtime control, not authentication or execution by itself.

## API and authority boundary

Source: `internal/apprunner/journal/enrollment.go`.

`ControllerDeployment(ctx)` reads the one explicitly enrolled deployment. An empty
journal returns `ErrNotEnrolled`. Opening the journal verifies filesystem/storage
integrity; it never generates a controller key or calls enrollment automatically.

`EnrollController(ctx, request)` accepts:

| Field | Meaning |
| --- | --- |
| `DeploymentID` | Canonical lowercase UUID of the intended deployment |
| `ControllerEpoch` | Canonical lowercase UUID of the reviewed current control epoch |
| `ControlRevision` | Positive signed int64 control revision supplied by the trusted controller setup |
| `PublicKey` | Exactly 32 raw bytes of the Ed25519 controller verification key |
| `Fingerprint` | Independently pinned lowercase SHA-256 of those exact public bytes |

The fingerprint must be obtained through trusted runtime-administrator setup. A
package, app, broker message or catalogue cannot enroll its own key. Merely hashing
an untrusted incoming key and passing that hash is not an independent trust grant.
The journal primitive itself has no network caller. H03 adds the separate explicit
`app-runner bootstrap --config` administrator command, which reads only a private
public-enrollment document. Command/control endpoints must never expose enrollment.

Store only the public key. The Portal control private key still belongs in the
approved purpose-sealed Portal keyring and is not added to runner storage. The
algorithm is fixed to `Ed25519`; an arbitrary string in a stored row cannot select
a different verifier. The method validates canonical identity, key length and pin;
it does not prove a signature, current Linux peer identity or a physical resource.

## First enrollment transaction

1. Copy and validate supplied public identity; refuse malformed UUIDs, zero/negative
   control revision, incorrect key length or mismatched/noncanonical pin.
2. Begin a context-bound transaction on the exclusive journal owner's SQLite pool.
3. Acquire write intent before reading the singleton, including when it is empty.
4. If a valid deployment already exists, require exact deployment, epoch, control
   revision, public bytes and pin. An identical retry returns the unchanged row
   with `created=false`; changed identity or revision conflicts without rewriting.
5. If no deployment exists, require zero rows in commands, resources and checkpoints.
   Orphan/unknown work cannot be adopted by filling in a new controller identity.
   An empty journal is not proof that the host has no old physical resources; the
   future setup/reconciliation adapter must still establish that separate boundary.
6. Insert the singleton at journal revision 1 and record current UTC milliseconds
   as metadata, then read/validate it and commit. Return `created=true` only for
   the successful first write.

The metadata clock is not a command validity proof. The future signed command
verifier must check its own bounded issue/expiry window at admission/dispatch.
A context cancellation, SQL failure or failed commit returns an error and does not
supply a grant; retry must observe the durable exact identity before proceeding.

Concurrent identical requests have one creator and unchanged subsequent readers.
Concurrent conflicting requests permit one identity; the loser cannot replace it.
Returned public-key byte slices are not a live handle into database authority.

## What is intentionally absent

There is no key replacement, controller rotation, epoch advancement, revision
reset, automatic recovery acceptance or erase-and-reenroll method. Those operations
need explicit reconciliation of original command fences, owned physical resources
and restore floors. The current enrollment method must not become a shortcut around
that work. Recovery cannot delete uncertain commands/resources merely to satisfy
its empty-journal requirement.

A valid enrolled record is only a trust input for later signature verification.
It does not admit a workload, authenticate a Unix connection, verify a command,
create a data directory or grant Docker access. Runtime execution readiness stays
false; the already completed disposable quota probe is independent native evidence.
No existing Portal startup or server `beta` configuration is changed.

## Verification

`enrollment_test.go` runs on Linux/macOS journal development profiles. It covers
empty-journal denial, explicit success and exact retries, changed key/epoch/
deployment/control revision, malformed pin/key/UUID/revision, unsupported stored
algorithm, pre-existing orphan work and canceled context. It checks immutable
returned snapshots, concurrent identical/conflicting requests, SQL insert failure
rollback and exact identity after close/reopen.

A subprocess test commits enrollment then deliberately exits without SQLite Close
or Go defers. The parent verifies committed WAL exists and reopens the journal,
requiring the same explicit controller identity rather than a replacement.

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -p 2 -race ./internal/apprunner/journal ./internal/apprunner/driver ./cmd/app-runner
```

Actual test/CI results are recorded separately from this test specification.
Remaining integration: strict signed command DTOs/shared vectors, authenticated
Unix transport and explicit operator setup, durable command admission and resource
repositories, restore/fence reconciliation, typed effect receipts and the actual
data/container lifecycle. The complete platform goal remains unfinished.


Development evidence: journal/driver/CLI race checks pass using the declared Go
1.26.10 toolchain. A test-only Go build overlay replacing successful enrollment
with the initial refusal stub makes the explicit-enrollment success test fail as
expected. The source implementation passes that test. This is storage/enrollment
behavior only, not signed command, native peer or physical runtime acceptance.

Local full Go tests (without the opt-in PostgreSQL service), build, vet and
staticcheck pass. The final focused enrollment race suite also passes, including
the abrupt-exit/WAL case. This change does not alter Portal database-driver code;
Linux exact-source CI remains a separate required check on the implementation PR.
