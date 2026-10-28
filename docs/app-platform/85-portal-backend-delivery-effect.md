# Portal durable backend delivery effect

This development phase connects Portal's existing operation ledger and sealed
keys to the protected registration RPC. It records exactly one logical delivery,
renews its signed envelope and transactionally stores the observed root receipt.
It does not complete the managed installation or enable its backend/user access.

## Entry points and state

The private development methods are:

- `preparePlatformBackendDelivery(ctx, lease)` captures/revalidates current
  lease, OU/admin/publisher authority, confirmed runtime, source history and keys;
- `dispatchPlatformBackendDelivery(ctx, lease, endpoint)` prepares, verifies the
  exact root runtime target, sends the protected RPC and settles its observation;
- `commitPlatformBackendDelivery(ctx, lease, intent, endpoint, proof)` revalidates
  current authority/history and atomically commits the public receipt.

The path supports only an initial managed install, generation/policy 1, at
`activate`, with an already applied data effect, actual candidate start receipt
and recorded readiness. Before creating a new delivery intent, readiness must be
at most three seconds old both at capture and final commit.

The existing `app_platform_operation_effects` table receives stage `activate`,
effect number 1, kind `activate`, a fresh effect UUID, stable logical payload
SHA-256, exact runtime/epoch/fence and prepared/empty-receipt state. These are
already approved columns and enum values; no migration or new table is added.
An audit record identifies the effect and candidate resource, with no private
material. Late audit failure rolls back the effect insertion.

## Keys, signing and stable renewal

Registration material is derived from the existing sealed installation identity
seed, under its existing AAD. Controller signatures use the existing sealed
runtime control seed. Neither seed is sent to the application. Temporary seed,
private signing-key and exported document buffers are cleared; live derived
registration handles must be closed by their owners.

The renewed signed candidate reference retains the original applied start effect,
resource, data namespace, release, generation and writer identity. The outer
registration command retains its original delivery effect UUID and document
hash. Only short-lived envelope issuance/expiry is renewed; expiry is bounded
by the current operation lease minus the existing one-second margin.

Every retry re-derives the document and recomputes the logical digest. A changed
sealed identity seed, source receipt, runtime binding, fence, target or stored
payload therefore fails closed rather than silently replacing delivery identity.
Prepared rows require the exact empty receipt and zero observed fence. Applied
rows require a canonical matching receipt, exact source receipt/native ID and
matching observed fence. Partial/unknown/unexpected states are refused.

## Historical readiness during reconciliation

The older candidate loaders still require exactly two effects and retain their
original freshness checks. Only the dedicated registration loader permits a
third effect, and only in the reserved activate slot with the same runtime,
epoch and admitted fence. The delivery loader then validates its complete state,
digest and receipt before returning material.

Once that intent is durably prepared, retries may inspect it after the original
readiness observation ages out. Its exact recorded source chain remains
mandatory; old readiness is not rewritten or called fresh. The runner still
checks the actual running container, pinned mounts and quota/data evidence for
file delivery/recovery. Later boot/health and activation must obtain their own
fresh evidence. This exception grants neither app access nor writer/provider
capabilities. Fence takeover and restore recovery remain separately gated; a
new owner cannot silently adopt an old admitted fence.

## Receipt settlement

A valid observation must be fresh transport provenance (at most three seconds),
from the exact configured root endpoint/controller UID/GID, under the current
controller key/epoch/revision. Its command digest and canonical root receipt
must match the prepared effect/document and original candidate receipt/native ID.
JSON or a zero observation cannot assert root provenance.

Settlement repeats current lease, restore-pause, OU/admin/publisher and runtime
checks; re-derives the key/document and logical intent; verifies both command
signature domains; and binds the exact source history. A transaction changes the
prepared effect to applied, records observed fence and canonical public receipt,
increments operation `row_revision`, and appends an audit receipt digest. It
returns the updated lease token. The stage and model state revision remain at
`activate`; installation stays pending, candidate stays candidate and no access
grant is created. A stale pre-settlement lease is refused.

If already applied, the same current lease may re-observe the exact historical
root receipt without another row-revision increment or duplicate audit record.
An old/different receipt or changed authority is refused. All SQL writes roll back
on a late audit failure. The root may already have completed the file effect;
retrying renews the same command and reads/reconciles that original effect.

## Verification

SQLite and independent PostgreSQL tests seed canonical repository metadata only.
They cover intent/signature/document agreement, stable renewal, unchanged grant
counts, old-health reconciliation for an existing intent, refusal of old loaders,
zero-observation refusal, stale first health, disabled runtime, restore pause,
malformed and changed valid sealed seeds, changed/partial/unexpected effects,
changed source receipt, and late audit rollback. These synthetic histories do
not prove an actual native backend or mint opaque root observations.

The Linux acceptance now uses Portal's actual preparation/signing and durable
effect row; the previous sample controller intent is removed. It first forces
root SQL settlement failure after publication. Then it forces Portal audit
failure after root recovery. Portal must retain its original prepared effect and
lease, renew and re-observe the same root receipt, commit exactly once and refuse
the stale lease. Historical replay leaves the new lease unchanged. The root
parent independently checks the same file inode throughout. Paused settlement
with an actual root observation is refused. Application/installation/grant states
must remain pending/candidate/no-grants. The script requires
`MANAGED_BACKEND_DELIVERY_COMMITTED` before emitting
`native_portal_backend_delivery_committed: true`.

Focused SQLite and PostgreSQL delivery race tests passed together (8.970 s).
Broader SQLite candidate/data/delivery regressions passed (21.771 s), independent
PostgreSQL candidate/data/delivery regressions passed (20.955 s), and runner/CLI
race passed (journal 84.606 s). Linux compilation, vet/staticcheck, actionlint
and 14 Python tests passed. Native workflow verification remains pending; SQL
fixtures and compilation are not substituted for physical execution evidence.

## Remaining platform scope

Connect real backend boot handshake, live viewer/OU-bound forwarding, default-deny
broker/provider and writer authorization, then managed activation with fresh
health. Normal managed worker/scheduler activation remains gated until that
complete path exists. Bounded writable socket storage, image fetching, startup
and restart/takeover reconciliation, cleanup, update/rollback/uninstall/reference
apps, cross-repository SDK publication and release approvals remain. No real
credentials, production deployment, server beta configuration, public SDK freeze,
database schema or module/toolchain versions change here.
