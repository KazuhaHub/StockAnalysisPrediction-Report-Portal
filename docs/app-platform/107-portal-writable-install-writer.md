# Portal writable review and writer stage

This follows [106](106-writable-lifecycle-transport.md). The runner can execute
writable lifecycle commands, but the existing Portal review/source contract
covers readonly candidates. This change introduces explicit writable review and
connects Portal's typed writer intent/receipt transaction to the authenticated
root endpoint. Candidate start, readiness, registration, boot, activation and
automatic managed installation remain subsequent work.

## Runtime configuration and explicit review

The managed configuration wire has an optional `writable_sandbox_profile`. The
concrete shared data/candidate listener reports
`linux-docker-rootful-projectquota-writable/v1-dev` only after its existing shared
provider/root/current-daemon configuration checks. This reports a compiled adapter
available on that configured endpoint. It does **not** report app readiness,
image verification, permission enablement or completed per-app native preflight.
Those remain independently mandatory in the concrete writer/start operations.

Omitting the field preserves existing canonical readonly profile vectors. When
present, the only permitted value is the exact closed writable profile. Unknown
values, execution-ready claims and malformed canonical metadata refuse. The new
field is provisional development protocol, not a public SDK freeze. Existing
stored review targets pin the entire profile; any later configuration change
fails exact target comparison and requires a fresh review.

`portal.writable-install-review/1-dev.1` is a distinct confirmation format.
`createPlatformWritableInstallReview` and `consumePlatformWritableInstallReview`
perform fresh authenticated configuration observation and reuse existing current
administrator, publisher/release verification, audience, compatibility, encrypted
runtime control-key and idempotent review consumption checks. A writable target
requires the explicit writable adapter field. Backend presence and no required
provider capabilities remain mandatory; this does not grant arbitrary providers.

Generic HTTP admission and readonly review consumers do not consume writable
reviews. No new public route or worker is enabled. `platformManagedFormat` shares
only review/data/runtime infrastructure between the two managed formats. The
readonly candidate source now explicitly requires its original readonly format;
it cannot adopt a writable review, whether before or after writer completion.

## Exact durable writer intent

After native preparation is committed, the operation is initial managed install,
generation 1, at `start`. `preparePlatformInstallWriter` holds current OU/trust,
operation lease and runtime checks inside a transaction and accepts only the new
writable review format. The source requires:

- One exact active namespace owned/originated by the installation, original data
  schema/quota/fence, no retention/deletion, original revision 2 before writer.
- Exact applied preparation effect 1, original canonical receipt, physical root
  pin, current runtime/controller and original immutable receipt history. Renewed
  current control references preserve original receipt revision/time/hash.
- No access grants, prior activations or unrelated effects.
- For a fresh writer: allocate one stable future candidate resource identity and
  one writer effect identity. Store a candidate activation row with empty
  readiness and no observation; this reserves identity only.
- Persist writer at `prepare_data`, effect 2, kind `activate`, matching the
  separate runner writer index. There is no start effect or process yet.

The signed `DataWriterCommand` binds the exact preparation receipt hash,
namespace/release/operation/fences, future process resource ID, native platform
and closed writer profile. It contains no pathname, Unix identity, mode or secret.
The nested preparation reference and writer use current encrypted control
material. Their deadline is at most 30 seconds and ends at least one second
before the current DB lease expiry; local/DB clocks must agree within one second.
Retry preserves logical identities and digest while renewing only the envelope.
Private seed/key copies are cleared.

Existing intent must be a complete exact pair: candidate row plus writer effect,
with stable creation times, current source history and the exact logical payload.
Partial, unknown, foreign, extra or drifted rows refuse; nothing is repaired or
replaced. After writer completion this initial reader accepts revision 3 only
with the exact validated historical writer receipt and matching namespace/effect
update time. It still refuses later start/registration history; those need a
separate, deliberately extended writable source validator.

## RPC and atomic receipt commit

`dispatchPlatformInstallWriter` selects initial execution;
`completePlatformInstallWriter` selects historical read after a lost response;
`reconcilePlatformInstallWriter` selects the separately authorized runner recovery
operation. The last one does not wait for expiry or create a replacement; the
runner still requires fresh issuance after the original unknown claim expired.
An already applied Portal writer result is historical metadata only.

Before native IO, persist/revalidate the source and freshly compare the exact
root managed runtime target. IO stays outside Portal SQL transactions. Native
work is available only through the concrete endpoint from 106; no callback,
browser JSON, parsed receipt or zero value can create opaque provenance.

`commitPlatformObservedWriter` clones input and requires:

1. A fresh (at most three seconds) opaque writer execute/read/reconcile reply
   from the configured root endpoint and exact controller UID/GID.
2. Exact current authority/key, original request command SHA and signed logical
   command; correct effect/resource/preparation receipt/fence bindings.
3. Independently parsed canonical native writer receipt: exact closed profile,
   seccomp policy, platform, physical root/project/device/namespace inode/quota,
   current-or-historical allowed control revision and nonfuture observations.
   Host/native-to-database timestamp ordering uses the same explicit one-second
   bound as signing; database/native events can differ by milliseconds. Original
   row timestamps/identity stay exact across IO, and physical observations must
   never be in the host clock future. Deterministic 1ms/1001ms cases exercise the
   allowed/refused boundary in both database variants.
4. Current Portal lease/review/publisher/runtime/source revalidated after IO.
   Original source, timestamps, identities and receipt state must still match.

The transaction updates writer effect to applied and namespace revision 2 → 3,
records observed bytes and one audit event, then re-reads the full applied source
and rechecks current authority/freshness before commit. Exact state/fence/revision/
time CAS predicates prevent a stale result from settling another state. Audit
failure rolls back both records. Operation stage remains `start`; no process is
created, no generation becomes active and no viewer/broker grant is written.

## Verification

Repository tests exercise stable IDs/digests, bounded signed deadlines, current
runtime/key/admin/trust/lease/epoch/fence checks, namespace/source/resource/effect
integrity, partial/extra history refusal, readonly/writable separation and zero
proof refusal with dump equality. SQLite and PostgreSQL variants share the cases;
PostgreSQL execution belongs to CI, not a local database. A focused SQLite/
PostgreSQL workflow step runs before vulnerability scanning: these tests do not
match the existing name-based app race subset, and the ordinary test step can be
skipped when a dependency scan fails. The focused step requires its existing
service DSN and runs both database variants explicitly. A profile codec test
preserves old omitted-field vectors and rejects unknown writable adapters.

The existing disposable Linux Portal fixture now runs an additional actual
writable writer installation from its non-root controller identity. It stages a
separate neutral signed app, creates and consumes the distinct review, commits
actual native preparation and drives the new writer RPC. A Portal audit trigger
forces rollback **after** the runner has enabled physical permissions and settled
its receipt. Portal retains prepared effect/revision-2 metadata. The test reads
the exact historical runner result, removes the trigger and completes Portal
settlement without another permission attempt. Receipt bytes and future resource/
effect/digest identities stay unchanged. Final state has exactly two effects,
one candidate identity, namespace revision 3, one observed audit, pending install
and no access grant. Existing readonly native installation continues separately.

Required fresh markers/report flags:

- `PORTAL_WRITABLE_REVIEW_CONFIRMED` /
  `native_portal_writable_review_confirmed`.
- `PORTAL_WRITER_SETTLEMENT_RECONCILED` /
  `native_portal_writer_settlement_reconciled`.
- `PORTAL_WRITER_RETAINED_IDENTITY` /
  `native_portal_writer_retained_identity`.

This proves the Portal-rollback/runner-applied case only after fresh Linux
execution. A fake SQL fixture cannot stand in for physical writer acceptance.
Positive runner-unknown expiry recovery through Portal is not claimed by this
test.

Local focused race passed (Portal 8.101s, transport 1.934s); broader Portal
platform/managed race passed in 138.483s. Linux amd64/arm64 Portal test builds,
Linux vet/staticcheck and Python acceptance tests (14) passed. Workflow
40812304641/job 123245506889 separately confirmed that the SQLite and PostgreSQL
writer intent/refusal tests actually executed (12.215s combined), before the
existing x/net vulnerability scan failure. This is database evidence only;
final source results are recorded below. Clock-boundary writer tests passed
three consecutive local race runs in 27.810s; broader Portal platform/managed
race after history pinning passed in 133.492s.

Functional source `5a853cd1fa5926f0ae42043341220d47d2e1c032` passed disposable
quota workflow [40814032240](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40814032240),
job 123250796324. Checkout confirms that source merged into parent
`3211dfe05d62853e1c1f8df479c820a0a46ee6e5`. All three new Portal writable review,
settlement recovery and retained identity flags are true. Actual non-root Portal
requests completed native preparation/writer execution, survived forced Portal
rollback after the runner receipt settled, then completed the exact original
receipt/resource/effect via historical read. Existing native checks,
`owned_resources_cleaned` and the owned registry cleanup step passed. No writer
process/activation/access was inferred from the writer result.

The same source passed the focused SQLite/PostgreSQL storage workflow step in
19.496s, including both 1ms allowed and 1001ms refused clock-boundary cases.

Daemon workflow [40814032539](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40814032539),
job 123251062215, passed on the same source. Full Linux runner race passed
(control 1.870s, data 1.012s, driver 3.131s, journal 226.127s, transport 7.003s,
runner command 1.033s), plus closed container policy, distinct process identities
and Portal root-endpoint confirmation.

General workflow [40814032138](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40814032138)
passed all app race shards/aggregate, web tests/build checks, workflow lint and
Docker font/smoke checks. The new writer cases have separate explicit database
coverage as described above; the name-based race subset alone is not evidence
for those cases. Go job 123251068257 passed build/vet, focused SQLite/PostgreSQL
contracts and staticcheck, then failed existing `golang.org/x/net@v0.60.0`
vulnerabilities GO-2026-6866/GO-2026-6860 (reported fixed in v0.62.0), skipping
its ordinary full test step. The general workflow is therefore not green.
Dependencies/toolchain remain unchanged.

## Next dependency

Extend the separate writable source through start, current readiness, protected
registration and MAC/boot verification, retaining this exact future candidate ID
and eventually four exact effects. Connect current viewer/activation and automatic
install worker only after those checks. Physical stop/revocation/update/retained
storage/restore lifecycle, capability broker, SDK/shared shell, neutral reference
apps, real market installation and full P00–P14 acceptance remain unfinished.
No schema, dependency/toolchain, production/beta/version configuration, actual
credentials, Status adoption or public SDK freeze changes are included.
