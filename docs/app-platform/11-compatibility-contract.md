# Provisional protocol and runtime compatibility evaluator

Status: P02 implementation, not D3 contract approval or an installation endpoint.
Source: `internal/appplatform/compatibility.go`.

## What it decides

`EvaluateCompatibility` compares a parsed application descriptor with a host-supplied protocol/runtime snapshot. It checks platform API range for every app. Managed apps additionally require a runner, a matching runner protocol range, a declared OS/architecture and an exact supported backend protocol.

A static app does not require a runner. Its declared runner range must still be syntactically valid, but an absent or unrelated runtime does not block static execution. This preserves the single-binary/static-only deployment path.

The target comes from trusted host runtime observation, never an app's own readiness claim or browser-supplied version. The current helper performs no observation itself. A future admission coordinator must obtain current observations and recheck before activation/dispatch as appropriate.

## Separate version dimensions

- `PlatformAPI` is the semantic version of the v2 platform API.
- Runner `Protocol` is its independently versioned control protocol.
- Runner `BackendProtocol` is the exact app-service contract, currently declared as `portal.backend/1` by descriptors.
- Runner `Platform` is the observed runtime OS/architecture, such as `linux/amd64`.
- Portal's product CalVer and stable/beta channel are not inputs to these protocol comparisons.

This implementation does not assign or advertise a released platform API version. Test targets are fixtures. It does not change production's `:beta` configuration.

## Range semantics and bounds

Constraint evaluation uses the pinned direct dependency
[Masterminds/semver v3.5.0](https://github.com/Masterminds/semver/tree/v3.5.0).
The wrapper retains the descriptor's 96-character ASCII range bound. Supported
syntax is the dependency's syntax within that existing character allowlist:
space-separated conjunctions, `||` alternatives, comparisons, caret, tilde,
wildcards, shorthand versions and spaced hyphen ranges. Commas and `!` are not in
the descriptor alphabet and therefore are not accepted, even if supported by the
dependency. This is not a promise of npm-semver equivalence.

Examples covered by behavior tests:

| Range | Target | Result |
| --- | --- | --- |
| `>=2.0.0 <3.0.0` | `2.0.0` | Match |
| `>=2.0.0 <3.0.0` | `3.0.0` | Mismatch |
| `>=2.9.0 <3.0.0` | `2.10.0` | Match, numeric ordering |
| `^0.2.3` | `0.3.0` | Mismatch |
| `~2.3.0` | `2.4.0` | Mismatch |
| `2.*` | `2.99.0` | Match |
| `2.0.0 - 2.3.1` | `2.3.1` | Match |
| `>=1.0.0 <2.0.0 || >=3.0.0 <4.0.0` | `2.1.0` | Mismatch |
| `=2.3.1` | `2.3.1+build.9` | Match, build metadata ignored |
| `>=2.0.0 <3.0.0` | `2.4.0-beta.1` | Mismatch, no prerelease opt-in |
| `>=2.0.0-0 <3.0.0` | `2.4.0-beta.1` | Match, explicit opt-in |
| `>=3.0.0-beta.1 || >=2.0.0 <3.0.0` | `2.4.0-beta.1` | Mismatch, no opt-in leakage between alternatives |

Empty expressions and empty OR branches are rejected explicitly rather than
inheriting the dependency's wildcard interpretation. Literal `*` is accepted as
an explicit unconstrained range. Overlong expressions, unsupported characters,
trailing control characters and malformed comparisons fail closed.

Actual protocol versions use strict three-component SemVer, maximum 128 bytes,
without `v` prefixes, omitted components, whitespace, leading numeric zeroes or
leading-zero numeric prerelease identifiers. Numeric major/minor/patch components
must fit the dependency's unsigned 64-bit domain. Descriptor parsing remains a
separate lexical declaration check; the evaluator also refuses an application
release version outside this comparison domain before returning compatibility.
No package-global semver flags are modified.

## Results and errors

A valid, incompatible release returns a result with `Compatible=false` and an
ordered list of safe reason codes. It does not return raw descriptor values as
error text. Reasons are platform API mismatch, runner required, runner protocol
mismatch, runtime platform mismatch and backend protocol mismatch, in that order.

Malformed compatibility declarations or malformed relevant target data return
`ErrInvalidCompatibility` and a zero result. An otherwise structurally invalid
manifest returns `ErrInvalidDescriptor` and a zero result. Both mean refusal;
callers must never treat an error as an ordinary mismatch that can be ignored.
The zero result is not compatible. A static app ignores irrelevant runner target
information because it does not use that service.

## What compatibility does not establish

A true result does not verify a publisher, signature, package hash, verified cache,
revocation/expiry, capabilities, current viewer/OU, resource admission, secret
availability, application data compatibility, migration safety or readiness. It
must not be used as a stand-alone execution or install authorization.

There are no routes, persistent records, network/process calls or installation
side effects in this helper. No existing legacy app parser/bridge changes.

## Verification

Tests were added and observed failing before the helper existed. They cover the
examples above, managed/static behavior, both declared architectures, combined
mismatch ordering, strict target/version refusal, empty alternatives, invalid
manifest/runner information, numeric overflow and input/result independence.
A bounded fuzz target checks refusal consistency and input size bounds; this is
not exhaustive input verification.

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 go test -race -cover ./internal/appplatform
GOWORK=off GOTOOLCHAIN=go1.26.10 go vet ./internal/appplatform
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test ./internal/appplatform -run '^$' -fuzz FuzzCompatibilityRefusesInvalidInputs -fuzztime=4s -parallel=1
GOWORK=off GOTOOLCHAIN=go1.26.10 go build ./...
```

Generated schemas, SDK-side compatibility parity, publisher trust, artifact
inspection and actual install admission remain P02/later work. Changing the pinned
semver dependency requires rerunning semantic vectors and reviewing behavior,
not merely accepting a dependency version bump.
