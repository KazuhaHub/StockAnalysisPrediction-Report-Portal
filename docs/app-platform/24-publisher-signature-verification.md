# Experimental publisher verification

Status: P02 trust primitive under approved D1/D3/D4 candidate A. This is not a
complete package envelope, publisher enrollment endpoint, installer or stable SDK.
It introduces no database layout, production publisher key or server change.

## Verification sequence

`internal/appplatform/publisher.go` exports `VerifyPublisherDescriptor`. The Apps
SDK exposes the matching experimental `@portal-app/sdk/publisher-v2` subpath.
Both consume exact descriptor bytes, detached envelope bytes, a current
host-supplied typed deployment trust snapshot and explicit current UTC milliseconds.

1. Bound inputs and validate the supplied trust snapshot. An empty allowlist grants
   nothing; invalid, ambiguous or overlapping ownership fails closed.
2. Parse the closed detached envelope without discarding duplicate keys.
3. Run the existing strict descriptor parser; compare its publisher and exact-byte
   SHA-256 with the detached envelope.
4. Select only a currently enabled trusted publisher and its enrolled key ID.
   Match the app ID against that publisher's exact ID or explicit prefix grant.
5. Check `not_before_ms <= now_ms < not_after_ms` when expiry exists. Any non-null
   revocation denies the key, even if its recorded timestamp is in the future.
6. Verify Ed25519 using the selected 32-byte public key and 64-byte signature over
   `ASCII("portal.app/descriptor/v2") || 0x00 || uint64_be(len(raw)) || raw`.
7. Return the parsed descriptor, exact digest, selected key ID, publisher revision
   and key revision. Every failure returns the same safe verification error.

There is no parse-and-reserialize, BOM stripping, newline normalization, algorithm
fallback, catalogue-origin trust or package-supplied public key. The verifier uses
Go's standard Ed25519 implementation and SDK Web Crypto, with no custom curve
arithmetic. SDK verification is useful for authoring/preflight; an app's local
result cannot authorize the host or enroll a publisher.

The TypeScript implementation copies bytes and captures all selected policy
identity before its first asynchronous cryptographic operation. Its result
identifies that snapshot, not later policy. Go callers must keep supplied values
stable for the synchronous call. Neither function reads a database or the clock.

## Detached envelope

The experimental schema is
`internal/appplatform/contract/publisher-signature-v1.schema.json`, copied exactly
to the SDK's `schema/` directory. Its six required string fields are:

| Field | Rule |
| --- | --- |
| `format` | Exactly `portal.app.signature/1-dev.1` |
| `algorithm` | Exactly `Ed25519` |
| `publisher` | Canonical descriptor publisher slug, at most 64 ASCII bytes |
| `key_id` | 1–128 lowercase ASCII letters/digits/dot/underscore/hyphen; starts with a letter or digit |
| `descriptor_sha256` | 64 lowercase hexadecimal digits, hashing exact descriptor bytes |
| `signature` | Exactly 86 unpadded canonical base64url characters encoding 64 bytes |

Envelope bytes are UTF-8, at most 4,096 bytes. Reject unknown/missing fields,
nulls/nonstrings, duplicate or escaped duplicate fields, case aliases, malformed
UTF-8, BOMs and trailing JSON. JSON Schema alone does not detect duplicates or
verify cryptography; the strict byte parser remains mandatory. Public keys in the
host snapshot use canonical unpadded base64url encoding of exactly 32 bytes.

The detached metadata selects an existing trusted key; it never grants trust.
The signature authenticates the descriptor bytes, whose declaration binds the
publisher and artifact digests. Full archive/image integrity and publisher policy
at commit are independent mandatory gates.

## Trust snapshot rules

These are in-memory verifier inputs, not a new persisted store or public wire API.
The already approved publisher/key records remain the future storage source.

- At most 128 publishers, 256 keys in total, 64 namespace entries per publisher
  and 256 namespace entries in total. Publisher IDs and per-publisher key IDs
  must be unique. Malformed unrelated records also refuse verification.
- Revisions are positive canonical decimal int64 strings. Clock/validity/revocation
  milliseconds are nonnegative canonical decimal int64 strings. Preserve values
  larger than JavaScript's safe integer limit; do not convert them to `Number`.
- Namespace kinds are `exact` and `prefix`. Exact values are canonical app IDs.
  Prefix values are at most 64 bytes, end in `-` and contain a valid nonempty slug
  before that delimiter. For example, `notes-` grants `notes-sample`, not `notes`.
  Loose `notes` prefix matching is invalid.
- Reject intersecting namespace grants, including redundant entries and grants
  owned by disabled publishers. Disabling a publisher does not transfer ownership.
  Empty namespace lists grant no app ID.
- Only Ed25519 keys are supported. Expiry must be strictly after not-before.
  No expired, revoked, disabled, unknown or malformed key can select a fallback.

The host must obtain a consistent current snapshot and bind these revisions into
its review. Under the activation transaction, recheck enabled state, key validity,
namespace ownership, key bytes and current revisions. A verified result must not
survive a conflicting trust revision merely because it was valid earlier.
Immutable key identities, durable rotation/revocation, restore revocation floors
and emergency quarantine still require the trust repository and lifecycle work.
The pure verifier does not claim to enforce those persistence rules.

## Reproducible verification

The shared corpus has 55 cases. Exact bytes in both repositories are pinned to
SHA-256 `f0553de5a701b0d8f665a2e997a525c8022ba5039ab6bce4c62da18f3118d641`.
The test key is the published [RFC 8032 section 7.1](https://www.rfc-editor.org/rfc/rfc8032.html#section-7.1)
test key, never a deployment credential. The Node fixture producer uses a separate
standard signing implementation; Go and SDK verify the same detached bytes and
framing digest. No verification test generates its expected signature at runtime.

From Portal root:

```sh
node scripts/generate_publisher_signature_vectors.mjs --check
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 -race ./internal/appplatform
```

From Apps root:

```sh
npm run test:sdk
npm run typecheck
```

Both suites were observed rejecting the positive vectors before implementation.
Deliberately bypassing each implementation's cryptographic rejection branch made
the wrong-domain/forged-signature cases fail; restoring it restored the passing
suites.
Cases cover exact/prefix grants, validity boundaries, int64 precision, byte/digest
changes, wrong signature domain, unframed signatures, signed malformed descriptors,
wrong publishers/keys, noncanonical signature encodings, Ed25519 high-S rejection,
revocation, disabled publishers, duplicate fields, oversized inputs and ownership
conflicts. SDK tests also mutate caller inputs while crypto is pending and verify
that the captured identity stays bound to the original bytes and revision.

To extend the corpus deliberately, update the producer, regenerate in Portal,
copy the exact corpus to `packages/app-sdk/tests/fixtures/` in Apps, and update both
suite hashes/counts after reviewing the changes. Do not silently accept fixture
changes. The schema copies are separately hash-pinned and positive envelopes are
checked against the SDK's schema validator.

Remaining: publisher persistence/admission, full package inspection, exact trust
rechecks in install/activation transactions, signed runner command DTOs/transport,
and actual Linux quota/sandbox/lifecycle acceptance. Successful verification here
is only one required input to installation; it does not start a process.

Local source checks pass: full Go tests (without the opt-in PostgreSQL service),
full Go vet/staticcheck, targeted platform/runner race tests, descriptor contract
product checks and the fixture producer's read-only reproducibility check. Apps
checks pass: 170 SDK tests, all-workspace typechecks, nine Status UI tests, two
Symbols demo tests, common frontend-standard validation and generated-template
create/typecheck/test/build. No existing frontend integration was changed.
Each implementation PR still needs its own exact-source CI; these local results
are not a production or complete-platform acceptance record.
