# Initial writer execution and durable receipts

This development slice connects the signed intent and process-local one-shot
claim in [93](93-writer-intent-and-claim.md) to the physical permission transition
in [92](92-data-write-access.md). It does not enable managed activation, writable
container start, a public writer endpoint or production deployment.

## Entry points and order

1. The trusted controller supplies the separately signed `DataWriterCommand`,
   its current signed data reference and the exact release descriptor. Admit and
   claim it using the existing APIs from 93. The journal records `unknown` before
   returning its nonserializable, Store-owned claim.
2. Call `Store.ExecuteDataWriter(ctx, claim, descriptor, nativeRuntime)`. This
   consumes `RunDataWriterOnce`; JSON, a callback boolean or reconstructed rows
   cannot manufacture a live handle. Authority, source ownership, fences, command
   history and the signed deadline are rechecked before and after native IO.
3. The concrete driver verifies root privileges, the initial generation/writer
   fence, actual immutable image/platform, descriptor digest, original sealed
   allocation and the fixed seccomp policy. It refuses local launch pins and
   actual Docker references to the data path.
4. Pin every directory in the protected namespace path. Revalidate the held
   descriptors against fresh path resolution before and after the effect. Root
   filesystem/inode, device, namespace inode, project and hard limits must match
   the original allocation. The physical provider changes only the initial
   namespace from root:root `0700` to root:65532 `02770`; it preserves project
   inheritance, quotas and namespace identity.
5. Reinspect actual permissions and quota state and repeat Docker reference/path
   checks. Only the concrete driver constructs opaque `DataWriterObservation`.
   It binds the exact writer effect, claim ID, logical payload hash, source data
   receipt hash, candidate identity, image/platform/policy and physical identity.
6. Reacquire the Store execution gate, verify current signed authority/source and
   opaque evidence, then settle the volume and writer receipt in one transaction.
   Close cannot release journal ownership while this transaction is executing.

No added tables, columns, migrations or alternate journal are required. The
writer command keeps the existing `prepare_data/2` slot and `activate` kind;
backend registration's separate domain remains unchanged.

## Reference exclusion boundary

Use the bounded Docker inventory with `all=1`, including stopped containers.
Inspect bind and volume sources and resolve symlinks. A direct data mount, parent
mount, child mount or resolved alias conflicts. Unknown, malformed, missing or
unresolvable source evidence refuses the operation. Container labels do not
exempt a reference. The same helper protects private HTTP storage release.

This is a point-in-time check under admitted lifecycle exclusion, not a global
lock against host root or Docker administrators creating unapproved aliases or
resources. The closed deployment must retain its single owned runtime/journal
and protected roots. No claim here covers a compromised host administrator or
an externally exposed bind alias. Ordinary application containers have no such
host authority.

## Durable success and uncertainty

On success, the existing volume advances from revision 2 to 3. Its original
ownership hash, generation, control fence and writer fence remain unchanged.
`observed_ms` records the native permission observation; `updated_ms` records
settlement. In the same transaction, the writer command advances from `unknown`
to `applied`, storing the canonical typed `DataWriterReceipt`. Original source
preparation command and receipt remain unchanged.

The receipt includes original claim, grant control revision, native image and
seccomp identity, root/project/device/namespace inode, hard limits, bounded usage,
observation and settlement times, and a versioned binding digest. All decimal
values and identities are strictly validated. Observation and settlement must
fit inside the original claim deadline. The digest binds evidence; it is not a
substitute for a signature or opaque native proof.

Every failure after invoking the permission provider is uncertain. If permissions
changed but SQLite settlement fails, the actual namespace can be writable while
the journal remains volume revision 2 and writer `unknown`. The transaction must
not commit only one of its two updates. Do not retry, reconstruct a claim,
repair permissions, reset the row, delete data or treat this as success. Later
observation-only recovery is a separate unfinished protocol.

## Historical receipt reader

`ReadDataWriterReceipt` requires a newly verified current signed reference and
exact original source/history/resource evidence. It permits current key/revision
renewal while preserving the original grant revision and original data ownership.
It rejects changed timestamps, acknowledgements, policy, source ownership,
fences, identity, resource revision or receipt binding.

This initial reader is deliberately pre-start: extra installation commands or
namespace-associated resources still conflict. It proves historical permission
completion, not a current writer lease, running backend, readiness, stop fence or
later candidate handoff. The old sealed preparation receipt and normal read-only
candidate path refuse the newly writable resource. A distinct writable candidate
contract and history reader must be implemented before normal activation.

## Verification

Local tests cover actual SQLite transactions and authority verification using
explicitly synthetic reader fixtures. These fixtures do not create native proof.
Coverage includes renewal/current-key preservation, altered volume/command times,
acknowledgements, identity/policy/binding drift, zero identities and refusal to
settle from a zero/decoded native observation. Linux driver tests cover stopped
container inventory, direct/parent/child/symlink references and unresolved paths.
Existing bounded inventory and release tests remain applicable.

The disposable Linux quota workflow adds two actual native cases:

- The driver writer trial first refuses a local launch pin, then an actual
  stopped read-only container seen by a second runtime. Sealed data stays
  unchanged. After owned container removal, it executes the physical transition,
  validates opaque bindings, refuses replay and runs the existing unprivileged
  filtered byte/inode quota and attribute-protection trial.
- After existing remount/recovery/count assertions, a separate journal test uses
  that same XFS root and shared journal allocator. It prepares actual data,
  admits/claims/executes the writer, verifies the durable receipt and renewal,
  and checks volume revision 3. A second actual namespace uses a test-only SQLite
  trigger to fail the receipt update after physical permission success; the test
  requires writable physical state, volume revision 2, writer `unknown`, no
  returned receipt and no retry/reclaim.

The parent requires native marker strings before setting
`native_writer_adapter_and_exclusion_verified`,
`native_journal_data_writer_execution_verified` and
`native_writer_settlement_failure_preserves_unknown`. These cases are restricted
to explicitly configured disposable GitHub-hosted root/XFS/Docker fixtures. They
never access production, real credentials or server version configuration.

Local full control/data/driver/journal/transport race passed (control 2.019s,
driver 2.288s, journal 110.867s, transport 1.445s; data cache hit). Focused writer
race passed in 14.739s and platform regression in 10.757s. Vet/default Linux
staticcheck, Linux amd64/arm64 compilation and all 14 Python probe tests passed.

Functional source `6713550ddc88d20d415233261b7e9d16c1650167` passed fresh native
acceptance:

- Quota workflow 40684569991/job 122803511112 passed. All three new writer flags
  above are true, along with the existing filtered writer quota, current viewer
  request and owned-resource cleanup evidence. The fixture registry cleanup step
  also passed. Linux control/appplatform/transport/driver race passed in
  1.368/1.393/4.794/2.390s. This is actual amd64 execution; arm64 evidence is
  compilation only.
- Daemon workflow 40684569825/job 122803510721 passed the full runner race suite
  (journal 105.228s), native closed-container/IPC checks and Portal confirmation
  through the root endpoint. Its ordinary race invocation intentionally skips
  the XFS-only writer case; the separate quota workflow above executes it.
- General workflow 40684570082 passes web checks/tests, workflow lint and Docker
  smoke. Go job 122803511906 passed staticcheck but failed the existing x/net
  v0.60.0 vulnerability gate (GO-2026-6866/6860); its following non-race Test step
  was skipped. All three app race shards and their aggregate passed. This is
  not an all-green release.

## Remaining implementation

Implement observation-only recovery for uncertain writer effects, the signed
writable candidate protocol and physical writer lifecycle/fencing. Connect that
to actual managed installation/activation and normal workers. Complete capability
broker providers, SDK/generator/shared React and Ant shell, reference apps,
updates/uninstall/restore and full P00–P14 acceptance. D5 SDK freeze and D6
production approval remain separate; server beta configuration stays unchanged.
