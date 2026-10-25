# Experimental signed runner command admission

Status: internal P02/P03/P04 development integration under approved initial D2
journal and D1/D3/D4 candidate A. This adds a pure signed-command verifier and
persistent prepared intent. It opens no socket, invokes no Docker/filesystem
provider and grants no physical dispatch permission. No database shape, private
runner key, production enrollment or server configuration is changed.

## Source ownership

| Source | Responsibility |
| --- | --- |
| `internal/apprunner/control/command.go` | Bounded strict envelope, exact-byte signature, closed prepare-data payload, logical digest and trusted clock/identity checks |
| `internal/apprunner/control/testdata/commands.json` | Hash-pinned deterministic command/refusal vectors |
| `scripts/generate_runner_command_vectors.mjs` | Independent Node standard-crypto fixture signer; `--check` refuses drift |
| `internal/apprunner/journal/commands.go` | Explicit controller lookup, transactional prepared intent, stable identity/floors and replay observation |
| `internal/apprunner/journal/commands_test.go` | Persistence/reopen, concurrency, stale ownership/fences, clock wait and rollback tests |

The controller key comes only from the explicitly enrolled journal singleton.
An app, catalogue, envelope or API parameter cannot supply its own trusted key or
algorithm. The verifier's standalone authority argument is a trusted host snapshot;
the journal entry point constructs it itself from stored enrollment.

## Closed development envelope

Format is exactly `portal.runner.command/1-dev.1`. The only supported stage/kind
pair is `prepare_data` / `prepare-data`; new executable actions need separate typed
contracts/tests. Every field is required. No arbitrary host path, command line,
image, mount, SQL or generic provider options are accepted.

| Root field | Rule |
| --- | --- |
| `format` | Exact experimental format above |
| `deployment_id`, `controller_epoch` | Canonical lowercase UUIDs equal to current enrolled identity |
| `control_revision` | Positive canonical decimal int64 equal to enrolled control revision |
| `installation_id`, `operation_id`, `effect_id` | Canonical lowercase UUIDs |
| `target_generation`, `installation_fence` | Positive canonical decimal int64 |
| `stage`, `kind` | Exact supported pair |
| `effect_number` | Canonical decimal integer 1 through 32 |
| `payload_sha256` | Exact lowercase SHA-256 of the typed logical identity below |
| `issued_ms`, `expires_ms` | Nonnegative canonical decimal int64 UTC milliseconds |
| `payload` | Exactly the prepare-data object below |

Payload has six required string fields: canonical UUID `data_namespace_id`,
64-lowercase-hex `release_sha256`, decimal `data_format` in 1..1,000,000, and positive
decimal int64 `quota_bytes`, `quota_inodes`, `writer_fence`. Numeric bounds here are
wire bounds, not allocation authorization. The future provider must enforce the
reviewed actual quota/profile/capacity before any resource write.

Envelope bytes are bounded to 65,536 UTF-8 bytes, root object required, maximum
object depth 8, maximum 32 fields per object and bounded ASCII lowercase field
names. Scan before typed decoding to reject duplicate/escaped duplicate fields,
case aliases, unknown fields, missing/null/type-invalid fields, arrays, invalid
UTF-8, BOM, excessive nesting and trailing JSON. Counters never pass through a
floating-point number, and leading zeros, signs and exponent forms refuse.

Only raw Ed25519 public keys/signatures of 32/64 bytes are accepted. Verification
uses Go's standard cryptography over exact received bytes framed as:

```text
ASCII("portal.app/runner-command/v1") || 0x00 || uint64_be(len(raw)) || raw
```

Do not parse and reserialize the signed envelope. Descriptor signatures, another
domain, altered whitespace/bytes or unframed signatures cannot authenticate it.
`Verify` returns fixed safe errors and an immutable value snapshot; it does not
admit replay/fences or a workload.

Require `issued_ms <= now_ms < expires_ms`, with strictly positive validity of at
most 30,000 milliseconds. Future-issued and expired-at-the-boundary messages
refuse. The standalone verifier receives a trusted clock sample. The journal
samples its own UTC time after acquiring the database connection/write intent,
then checks again before commit and refuses observed backward movement. Command
expiry must also be checked before future physical dispatch; persisting intent is
not a perpetual execution lease.

## Logical effect digest

A renewed transport envelope preserves the same effect ID and logical digest.
The digest covers canonical compact JSON with exactly this ordered structure:

```text
format = "portal.runner.effect/1-dev.1"
deployment_id, controller_epoch, operation_id, installation_id,
target_generation, stage, effect_number, kind,
payload = { data_namespace_id, release_sha256, data_format,
            quota_bytes, quota_inodes, writer_fence }
```

All values in this structure are the already validated ASCII strings. Marshal in
that order, without optional/unknown members. Hash:

```text
SHA256(ASCII("portal.app/runner-effect/v1") || 0x00 ||
       uint64_be(len(logical_json)) || logical_json)
```

Exclude envelope issue/expiry, signature, control revision and installation fence.
They renew transport/current authority, not the original logical payload. Exclude
the effect UUID so Portal can persist its logical digest before allocating the
stable effect record; the signed envelope and journal uniqueness still bind it.
`LogicalDigest` validates the logical fields before returning a hash. It never
normalizes quota/counter text to another representation.

## Journal admission and original fences

`Store.AdmitControlCommand(ctx, raw, signature)`:

1. Bound/copy bytes before opening a transaction. Acquire SQLite write intent
   before looking up explicit enrollment; empty or unsupported enrollment refuses.
2. Load the pinned key/deployment/epoch/control revision and verify signature,
   logical digest and timing using a fresh runtime-clock sample.
3. Reject relevant command/resource records from another controller epoch. They
   require recovery, not an automatic reassignment.
4. Derive the highest known installation control fence and generation from
   original command records plus owned resource records. Derive the namespace's
   known writer fence from resource records. Refuse lower fence/generation/writer
   or a namespace associated with another installation.
5. For an existing effect ID, require identical operation, installation, generation,
   stage, slot, kind, digest and epoch. Return a replay observation, never re-execute.
6. For a new effect, refuse an occupied operation/stage/slot, or another prepared/
   unknown command for that installation. Insert one `prepared` row with original
   admitted fence, empty receipt, null acknowledgement time and current timestamps.
7. Recheck time/signature before commit. SQL/context/verification failure rolls
   back intent; no resource or effect outcome is written.

The original command's `admitted_fence` is immutable. A replay carrying a higher
fence than every durable command/resource floor returns `ErrFenceReconciliation`.
It cannot update that original field or infer takeover from a signed counter.
A later typed observational reconciliation command/resource repository must be
able to establish that floor without pretending old work stopped. It is not
implemented by this admission helper. Once another owned record durably establishes the higher floor, a matching replay
at that floor returns both original admitted fence and current floor separately.
A lower-fence replay refuses. Advancing real ownership/writer exclusion still
requires the future typed reconciliation provider.

Unknown resources are conservative fence inputs, not physical success evidence.
This repository does not inspect native ownership labels or prove that an old
writer stopped. A resource/command SQL state alone cannot complete a stage.
`CommandAdmission` reports only effect ID, journal state, original/current fence
and replay status; it contains no physical receipt or `ready`/dispatch flag.
Prepared/unknown replay must enter reconciliation, not another execution attempt.
Applied/reverted historical rows likewise require native/current proof before any
new action depends on their physical consequences.

No command-completion/receipt mutation API exists here. There is no fifth floor
store, rewritten original fence, automatic controller re-enrollment or caller
permission to mutate the journal directly. Actual execution serialization, receipt
proof and restart/resource reconciliation remain mandatory integration work.

## Verification and remaining gates

The Go suite uses 48 hash-pinned Node-signed positive/negative vectors.
A separate test build overlay bypassing only signature rejection made the wrong-
purpose, unframed, invalid-signature and changed-byte cases fail; normal source
retains the rejection branch and passes. The signer
uses only the published RFC 8032 test key and never enrolls it in a deployment.
The exact corpus identity is pinned in `command_test.go`; CI regenerates in
read-only `--check` mode. Cases cover original/renewed exact bytes, wrong domains,
changed payload/digest, malformed/ambiguous/oversized JSON, time boundaries and
int64 lexical/precision bounds. An initially refusing verifier and admission stub
were observed failing their meaningful positive tests before implementation.

Journal tests cover missing trust, idempotent renewal, immutable original fence,
reopen, one intent under eight concurrent requests, changed logical identity/slot,
prepared-work exclusion, resource fence/generation/writer/owner/epoch conflicts,
SQL insert rollback and expiry after an actually observed connection wait.

```sh
node scripts/generate_runner_command_vectors.mjs --check
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -p 2 -race ./internal/apprunner/control ./internal/apprunner/journal
```

Independent signing vectors are not a stable public SDK or complete Go/TypeScript
operator-tool parity. That remaining contract work, authenticated Unix transport,
explicit administrator setup, Portal effect/command binding, native-resource
reconciliation, quota-backed data provider, container isolation and physical effect
receipts must precede execution. Application/browser SDKs receive no controller key
or control dispatch interface through this implementation. The full platform is
still in progress; production rollout remains separately authorized.


Local evidence: the full Go test/build/vet/staticcheck checks pass without the
optional PostgreSQL service (no Portal database-driver code changed), as do the
control/journal race suites and workflow lint. The native runner probe now runs
all runner package race suites on its disposable Linux worker as well as the
read-only Docker inspection. That exact-source CI remains pending on the PR.
