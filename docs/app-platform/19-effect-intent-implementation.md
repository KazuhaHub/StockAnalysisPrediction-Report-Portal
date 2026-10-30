# Durable operation effect intent

Status: P03 in progress. This uses the already approved
`app_platform_operation_effects` table and adds no storage products or public API.
The runtime/trust development approval is recorded in
[18](18-runtime-and-trust-proposal.md#9-approval-record). No container, signature
verifier or control transport is added by this storage work.

## Internal boundary

`Store.preparePlatformEffect` accepts an internal operation lease, a stage-local
effect number (1..32), a closed stage/action kind and an exact lowercase payload
SHA-256. It locks and validates persisted authority before inserting intent.
The provider that eventually calls this repository must construct the typed
payload from the reviewed plan and compute its digest. This method is not an
HTTP endpoint and must not be wired directly to caller-supplied JSON.

Effects are identified by operation + stage + number. The first preparation
generates one UUID and persists `prepared`, original admission fence, runtime,
controller epoch and digest. Repeating the same preparation returns the same
record. Changing the digest/kind/runtime/epoch conflicts. Concurrent preparations
serialize; exactly one creates the record.

Claiming the operation after lease expiry does not replace its effects. Existing
`prepared`, `unknown`, `applied` and `reverted` metadata remain unchanged, including
the original fence. The returned `created` flag describes an INSERT only; it is
not permission to execute. In particular, `created=false` must not trigger replay.

The shared lease loader now supplies the same persisted state checks to heartbeat
and effect preparation: valid lifecycle snapshot, source/target generations,
controller epoch, restore pause, phase, current installation fence, exact worker
identity and row/state revisions. SQLite obtains write intent before reads.
PostgreSQL lock order for effect preparation is runtime → installation → operation
→ effect, consistent with review admission. Cleanup intent can be recorded when
runtime admission is disabled, so denial does not depend on accepting new work.

Database time is sampled again after waiting for an existing effect row. A worker
whose lease expires during that wait is refused. Preparation neither renews the
lease nor advances operation stage/revisions. A refused transaction rolls back.

## Implemented kind scope

The private mapping permits stage/verify, prepare-data, start-candidate, probe,
activate, stop, snapshot, restore, remove-code and delete-data only at their
corresponding operation stages. Queued/validation/terminal states and arbitrary
shell actions have no effect preparation path. Download and migration providers
are not implemented; do not invent an action or bypass their evidence requirements.
Static installations have a null runtime; managed installations require a matching
current runtime binding and controller epoch.

## Verification

Source tests cover both SQLite and a disposable PostgreSQL database:

- One stable effect identity, changed payload refusal and eight simultaneous
  preparations producing one insertion and seven observations.
- Worker displacement, lease expiry, heartbeat preservation, pause/epoch/fence
  changes, invalid generation and removed/mismatched runtime binding.
- Unknown outcome survives worker reclaim without reset or a new execution fence.
- Invalid effect numbers, arbitrary/mismatched kinds and malformed digests refuse
  without modifying stored state. Disabled runtime admission still allows cleanup.
- SQLite close/reopen retains the original intent and identity.
- A real PostgreSQL transaction holds an effect row while the worker lease expires;
  the waiting preparation must refuse. Removing the final expiry check was
  observed to fail this regression with an incorrectly successful preparation.

Useful targeted command, with an isolated test DSN for PostgreSQL cases:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -p 2 -race ./internal/app \
  -run '^Test(Postgres)?AppPlatform(Effect|Lease)' -count=1
```

The fixtures exercise stop intent and common repository guards; they are not
evidence that each physical provider or the complete lifecycle works.

Targeted SQLite/PostgreSQL tests and the corresponding race run pass. Full-source
`go test -p 2 ./... -count=1` with the disposable PostgreSQL DSN,
`go vet -p 2 ./...` and `go build -p 2 ./...` pass. These are local source results;
the new PR requires its own exact-source CI before merge.

## Remaining work

No method accepts, verifies or commits a receipt. A stored state string, including
`applied`, is historical metadata rather than current resource evidence. Stage
advancement still needs a typed provider, fresh authenticated observations and an
atomic evidence-bound commit. Providers must assign stable slot meanings rather
than allocate a new effect number to retry an uncertain action.

Runtime admission, resource ownership, writer exclusion, rollback, actual process
reconciliation, trusted package install, SDK/shell and complete market flows remain
separate work. The runner journal still exposes only verified Open/Close. No
production service, credentials, installed app or server `beta` setting changes.
