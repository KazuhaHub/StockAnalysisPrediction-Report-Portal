# Durable runner data reservations

This connects signed command admission to a durable namespace/project reservation
in the approved runner journal. It is P03/P04 preparation for the actual data
executor. It does not execute native data preparation, acknowledge a physical
receipt, grant a writer or activate an app.

## Current installation chain

| Step | Current implementation |
| --- | --- |
| Verify/enroll controller | Existing explicit controller trust and local transport |
| Admit signed prepare-data command | Existing durable `prepared` command intent |
| Reserve namespace/project | Implemented here in `runner_resources` |
| Claim physical execution | Still required; no dispatch permission exists yet |
| Prepare sealed native data | Native XFS primitive exists, not called by this repository |
| Reconcile observations/commit receipt | Still required, including crash and uncertain outcome handling |
| Grant writer/finish Portal stages | Still required |

The existing native XFS code at `48e44794088f81f08e7ddfb9418fe97dd9b7db80`
passed quota run 40278679625 and main CI run 40278679685. Those are predecessor
checks, not evidence that this reservation method performs a physical operation.

## API and prerequisites

`Store.ReservePrepareData(ctx, rawCommand, detachedSignature)` is an internal
library method. It is not registered on the control socket, Portal HTTP, worker
loop or command line. A caller must first successfully admit the same logical
command through `AdmitControlCommand`. A signature alone never manufactures the
missing admitted command.

Inside one context-bound transaction it:

1. Obtains the existing journal write guard and reads explicit controller trust.
2. Verifies bounded exact command/signature bytes, deployment, epoch, control
   revision, current time and the typed logical payload digest. Byte quota must
   additionally be divisible by the native 512-byte quota unit.
3. Loads the exact already-admitted effect and compares operation, stage/slot,
   installation, generation, kind, epoch and payload identity.
4. Requires `prepared`, empty receipt `{}` and no acknowledgement marker. Unknown,
   applied or reverted work must go to reconciliation instead.
5. Applies the same command/resource fence, generation, writer-floor, namespace
   owner and epoch rules used by admission. The requested fence and durable floor
   must equal the command's immutable original fence; no takeover is written here.
6. Refuses another prepared/unknown command for the installation, then either
   verifies the exact existing reservation or allocates one fresh target.
7. Rechecks wall-clock progression and the complete signed envelope after all
   reads/writes/waits, then commits. Cancellation, expiry or any error returns an
   empty result and rolls back this transaction.

Envelope renewal can change issue/expiry timing while preserving the logical
identity. It returns the same reservation, not a new project. This does not extend
an expired envelope: each call needs a currently valid signed renewal.

## Existing storage, no new schema

Only the approved `runner_resources` row is inserted. No table, column, migration,
journal version, JSON side entity, filesystem counter or fifth floor store is
added. The original four-table journal layout remains unchanged.

| Field | Initial reservation meaning |
| --- | --- |
| `id` | The signed data namespace UUID, also the stable resource identity |
| `installation_id`, `data_namespace_id` | Exact signed owner/namespace |
| `kind` | `volume` |
| `native_id` | Canonical `xfs-project/v1/<positive decimal ID>` target |
| `release_sha256`, `generation`, `controller_epoch` | Signed immutable tuple |
| `control_fence`, `writer_fence` | Original command fence and signed namespace writer fence |
| `desired_state` | `present` intent |
| `observed_state`, `observed_ms` | `unknown`, NULL; no physical observation is fabricated |
| `ownership_sha256` | Digest of the closed ownership claim described below |
| `revision`, timestamps | 1 and the initial creation time; replay does not rewrite them |

The admitted command remains `prepared`; its receipt and acknowledgement fields
stay untouched. The native target name reserves an identity. It does not assert
that an XFS project or directory exists.

The ownership digest uses the fixed `portal.runner.data-ownership/1-dev.1` claim
format and deterministic typed JSON. It binds deployment/epoch/control revision,
installation, namespace, canonical native project target, original effect/payload
digest, generation/admitted fence, release, data format, byte/inode limits and
writer fence. No issue/expiry timestamp or mutable observation enters that
identity. The original payload digest already binds operation/stage/slot and the
complete typed payload. The project itself is bound, so changing a stored project
ID cannot silently redirect a valid replay. This is an integrity/identity binding,
not an independent signature or native ownership proof.

## Allocation, replay and retention

The initial profile uses one journal and one configured XFS data root/filesystem
per runtime. Allocation is serialized by the existing SQLite write transaction
and single owned journal connection, in addition to the process ownership guard.
It scans bounded existing volume targets and chooses maximum project ID plus one.
Zero and uint32 max are excluded; exhaustion refuses instead of wrapping.

All volume rows participate, including absent, unknown, retained or quarantined
historical resources. Gaps are not reused. Future cleanup must preserve these
resource tombstones while the deployment/journal identity remains in use. A new
reservation is refused when 65,536 volume reservations already exist. This is an
initial development bound, not a frozen public capacity/performance promise.
Malformed/unknown volume target formats require reconciliation; they are not
silently skipped as though their native IDs were free.

Replay requires the exact resource/owner/namespace/native-target/claim/release,
generation, epoch and fence tuple, initial revision/time consistency, unknown
observation and unchanged desired intent. Future timestamps or any observation/
revision change refuse. Existing resources with the same namespace/resource UUID
or another volume root for that installation cannot be replaced through this
method. Retained-data adoption, a new generation or a native adapter migration
requires its own reviewed reconciliation path.

The result's `Replayed` flag describes whether the same metadata already existed.
Neither flag value is a dispatch ticket. **Do not call the native primitive just
because `Replayed` is false.** A durable execution claim and settlement protocol
must first prevent duplicate dispatch, preserve uncertainty and serialize actual
native effects through restart and fence changes.

## Native and recovery boundary

This allocator does not inspect mounts or native project usage. The future
executor must bind the configured physical root/filesystem, reconcile native
ownership and detect IDs created outside this journal before mutation. The native
primitive independently refuses existing project limits/usage and namespace
paths, but this is not a shared-filesystem allocator for arbitrary runtimes or
administrators. Multiple independent journals cannot safely allocate project IDs
on one shared filesystem without additional coordination.

A failed reservation transaction can retry because no native provider is called
before commit. Once physical execution starts, a lost acknowledgement is different:
reconcile the original reserved ID/ownership/fence; do not allocate a replacement,
clear a quota, reset an unknown command or blindly replay. Restoring an old journal
must remain paused until current physical resources and durable authority have
been reconciled. This method does not implement restore/resume.

## Verification

The first positive reservation/replay test failed against an always-refusing
implementation. Tests now cover missing admission, durable reopen and exact
renewal, one insertion under eight duplicate requests, eight distinct concurrent
installation allocations, non-reuse after absent state, terminal/uncertain state
refusal, changed ownership/native ID/owner/release tuple metadata, stale fences,
invalid signatures/epochs/revisions, cancellation and byte-alignment refusal.

A real owned-connection wait expires the signed command before reservation;
no resource can persist. A failing SQLite INSERT trigger rolls back allocation,
and a valid retry gets the original first available ID. The capacity boundary
fixture verifies that the 65,536th reservation succeeds and the next refuses
without rewriting/recycling old rows. These fixtures are ledger tests, not native
resource observations.

A temporary Go build overlay removed only project-ID participation in the
ownership digest. The native-ID mutation refusal test then failed by accepting
the redirected reservation; normal code retains that binding. The source is not
modified by this negative-control build.

Full local Go regression/build passed (the app package was cached on the final
repeat; the preceding run took 57.353 seconds). Optional PostgreSQL tests were not
enabled; no Portal database code/schema changed. The runner race suite passed;
the final reservation race suite including the full capacity boundary passed in
79.767 seconds. Go vet/staticcheck and Linux amd64 test-binary cross-compilation
passed. Exact-source CI remains required. Physical execution, receipts, writer exclusion/admission, Portal
stage completion and full managed/static installation remain open. There is no
production enrollment/deployment, real credential use, Status migration, public
SDK freeze or server beta configuration change.
