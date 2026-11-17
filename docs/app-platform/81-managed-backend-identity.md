# Managed backend identity and private registration preparation

This continues P05/P07 after document 80. It prepares the private registration
material needed for automatic backend registration while retaining ADR 0040's
existing handshake and request verifier. No root bootstrap file is delivered,
backend paired, request proxied or application activated by this phase.

## Source map

| Source | Responsibility |
| --- | --- |
| `internal/appplatform/backend_registration.go` | Managed tuple, independent derived key, explicit protected document export and redacted live handle |
| `internal/appplatform/backend_registration_test.go` | Existing protocol interoperability, tuple separation, replay/request binding, parsing and key lifetime |
| `internal/app/app_platform_backend_identity.go` | Current lease/admin/trust/runtime/data/start/readiness authority before deriving material |
| `internal/app/app_platform_candidate_intent.go` | Exact candidate/data/start history with bounded recorded readiness at `activate` |
| `internal/app/app_platform_managed_install_native_linux_test.go` | Real ready candidate, stable material retry and no extra grants/effects |

## One managed instance identity

The binding includes application ID, deployment ID, controller epoch,
installation ID, activation generation, capability policy revision and exact
release digest. IDs are canonical nonzero UUIDs; counters are canonical positive
decimal strings in the Portal storage range; app/digest rules match the package
contract. A canonical purpose-tagged tuple is hashed to produce the existing
protocol's opaque `InstanceID`. It does not expose a host address or principal.

The backend verification key is HMAC-SHA256 of a separately purpose-tagged instance
under the installation's existing sealed 32-byte identity seed. The backend gets
only this derived key, never the installation seed or runner control key.
Changing app/deployment/epoch/installation/generation/policy/release changes both
instance and key. Same-tuple retries preserve them. `KeyID` is the derived key's
SHA-256 fingerprint. The issuer binds deployment; audience binds installation.

This reuses the pinned shared extension protocol without changing that module:

- Its authenticated fresh challenge/answer binds the app's boot nonce.
- Its short-lived assertions bind issuer/audience/app/instance/boot plus exact
  method, `/v1/` path, body digest, request nonce and viewer claims.
- Existing replay bounds, request normalization and verifier checks remain.
- Old-generation/policy/release instances cannot validate under the new tuple.

A signing helper is not a viewer authority resolver. Before actual request
dispatch, Portal must resolve the current session/account/deletion generation,
role and OU ancestry itself; no browser/app-provided viewer becomes trusted here.
Stable account/OU identities must use a separate installation identity namespace,
not this generation-bound assertion instance. Broker admission must not accept an
app's possession of its own backend verification key as Portal business authority.

## Private document and lifetime

The provisional protected document has format
`portal.managed-backend-registration/1-dev.1`, managed metadata and a 32-byte
`signing_key` encoded using JSON's standard base64 byte representation. Parsing
is bounded to 4 KiB and requires exact canonical bytes, valid identity, matching
derived instance/fingerprint and no additional/duplicate/trailing fields. Parsing
does not authenticate a writer or filesystem path: future bootstrap delivery
and the app reader must establish their own root/path/ownership provenance.

The live registration handle refuses generic JSON serialization/deserialization.
Normal and Go-syntax string formatting are redacted. An explicit `Document()`
export is reserved for protected delivery; its returned key-bearing bytes must
be cleared after use. Copies share a mutex-protected close state; `Close()`
clears the retained key and invalidates all handle copies. Returned signer key
copies must be separately cleared by their callers. Closing a preparation handle
does not revoke previously copied verifier keys or already-issued assertions;
current generation/authority enforcement remains mandatory.

## Portal preparation

`preparePlatformBackendRegistration` is a private development method requiring
the current initial managed-install lease at `activate`. It obtains the current
confirmed runtime and clears the temporary runner seed, then validates publisher
signature/trust, current admitting administrator, lease/fence, reviewed target,
candidate identity and applied data/start receipts. The candidate must have the
exact canonical root-observed readiness hash/native ID/payload/schema and an
observed timestamp at most three seconds old. Stale work must be re-probed before
later delivery; this method cannot refresh or manufacture health.

It decrypts the existing installation identity envelope with its existing AAD,
derives the registration material, clears the temporary installation seed and
rechecks current authority/history/time before committing the read boundary.
Errors close the newly prepared key handle. The material is held only in memory:
no new table, migration, metadata setting, plaintext database value, delivery
effect, native file, app-use grant or backend registration is created.

## Acceptance and limits

Pure tests export/reparse fixture documents, perform the actual shared protocol's
challenge/answer and signed request verification, and refuse replay, changed
method/path/body/boot and every changed managed tuple dimension. They also cover
invalid identity/counters/documents, canonical parsing, accidental serialization,
redacted formatting and invalidation of copied handles after close. These are
cryptographic/contract checks, not a deployed backend handshake or live viewer
authorization test.

The Linux Portal fixture prepares material after its actual backend passes the
root readiness check. It requires the exact installation/release tuple, byte-
identical retry and unchanged effect/access counts. The parent/script requires
`MANAGED_BACKEND_REGISTRATION_MATERIAL` before emitting
`native_portal_managed_registration_material: true`. Functional source
`8dfd0a05f7f5e1c9ab20491c859fe64eb7afb9aa` passed quota workflow
[40617663109](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40617663109),
job 122583845402: actual ready-backend material preparation and owned cleanup are
true; exact registry cleanup passed. Linux transport/driver race passed in
4.654/1.621 s. These results prove preparation under the ready candidate, not
root key delivery or a deployed backend handshake.

Local SQLite race passed (6.731 s), full appplatform race (1.562 s), PostgreSQL
candidate/data race (13.277 s), Linux amd64/arm64 compilation, vet/staticcheck,
actionlint and 14 Python tests passed. General CI 40617663112 passed all three
app race shards, web checks/tests, workflow lint and Docker smoke. Its Go job
still failed the existing x/net v0.60.0 vulnerability gate (GO-2026-6866 and
GO-2026-6860, reported fixed in v0.62.0), skipping the following non-race test
step. The race aggregate was still active at capture; general CI is not all green. Positive Portal preparation currently uses
the SQLite native fixture; PostgreSQL candidate/data regressions cover shared
loader compatibility, not full end-to-end registration preparation.

## Next delivery

Add a signed durable root registration effect, protected bootstrap file handling,
actual app boot handshake and bounded authenticated request forwarding. Then
connect current viewer/OU authority, broker/provider denial and managed activation,
with short-lived health revalidation. Socket storage bounds, native cleanup/
reconciliation, writer authorization, image fetching and full lifecycle/reference
apps remain. Cross-repository SDK schema/corpus publication and a public freeze
are not implied. No production/beta configuration, real credentials or module/
toolchain versions change in this phase.
