# Platform implementation evidence

This register distinguishes implemented primitives from a delivered platform.
The implementation objective is the complete application framework in this plan,
not another Status-specific frontend or a manual backend deployment procedure.

## Work package state

| Work package | State | Evidence / next requirement |
| --- | --- | --- |
| P00 | In progress | Source inventory and compatibility map complete; detailed runtime/trust decisions and physical storage approval remain |
| P01 | In progress | Local whole-flow prototype, 17 focused tests and browser sample flow/320 px review; see [prototype guide](10-prototype-review.md). Owner visual review and broader acceptance remain |
| P02 | In progress | Go/SDK declaration parsers, shared 44-case corpus and pure lifecycle transition model implemented; generated schemas, compatibility evaluator, signature policy and package inspector remain |
| P03..P14 | Not started | Follow dependencies and approval gates; no durable schema, runner or production operation is implied by P02 |

## First contract implementation

Source lives in `internal/appplatform`. It has no app/server initialization, DB,
Docker, filesystem mutation, HTTP route registration or network activity.

Descriptor parsing currently provides:

- Exact-byte SHA-256 identity for later binding to a reviewed plan/signature.
- Bounded UTF-8 JSON with duplicate/escaped-duplicate keys, mixed-case field
  aliases, unknown fields, trailing values and excessive nesting refused.
- Separate static and managed declarations, normalized-safe artifact/entry/route
  paths, required locale coverage, unique routes and immutable image references.
- A provisional known capability set and declaration dependency checks: scheduled
  or event-driven handlers require a backend and explicit background permission;
  a required handler cannot depend on an optional background permission.
- Bounded backend resource declarations and explicit readable data schemas;
  implicit schema migration through ordinary server startup is refused.

Parsing does **not** verify publishers/signatures/artifact bytes, evaluate semantic
compatibility ranges, extract archives, enforce network/host policy or grant any
capability. A descriptor being parsed is not permission to install or execute it.
These missing gates must be implemented and tested before an admission endpoint.

The operation model provides ordered stages, revision checks, stable generation,
separate retained-data deletion and explicit uncertain-effect recovery. Static
apps skip backend start/probe stages. All installs still prepare their data
namespace; managed updates cannot skip snapshot/migration/readiness stages.

The model is a value transformation, not a durable compare-and-swap, lease,
executor or idempotency store. It cannot prove a stage's effects committed. The
future persistence/executor must supply verified evidence and atomically save the
transition. Unknown external effects enter recovery-required; failures cannot
silently restart the same completed or uncertain operation.

## Verification performed

Tests were written and observed failing before implementation of the parser,
operation transitions and explicit install data-preparation stage.

From Portal root, using its declared Go toolchain:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 go test -race -cover ./internal/appplatform
GOWORK=off GOTOOLCHAIN=go1.26.10 go vet ./internal/appplatform
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test ./internal/appplatform -run '^$' -fuzz FuzzDescriptorParser -fuzztime=3s -parallel=1
```

Targeted tests/race/vet pass; statement coverage is 92.8% for these primitives.
A short single-worker fuzz run passes. This is bounded development evidence,
not exhaustive fuzzing or a claim that the 90 platform acceptance cases pass.
The initial sample fixtures covered the proposed Go contract only. The subsequent
Go/TypeScript parity corpus covers 44 exact-byte inputs, including malformed UTF-8,
BOM, escaped duplicate keys, integer lexical forms, null defaults, capability
dependencies and invalid route layout types. Both repositories pin the corpus
SHA-256 to `16128f031fd71d83429db6a4209f8cfb02e5209b7c4f76a202c5dda1a00d929e`
and require LF checkout bytes. The SDK validator is an experimental separate
subpath, without changes to the legacy bridge/provider or installed apps.

The current Apps-source verification passes 62 SDK tests, nine Status UI tests,
two demo tests, all-workspace typechecks and generated-template typecheck/test/build.
Go shared-corpus and targeted race/vet checks pass. These are source checks; the
SDK [Apps #22](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/pull/22)
and host [Portal #188](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/188)
changes passed exact-source CI and merged. Shared corpus agreement is
bounded evidence and does not freeze or fully prove every possible input.

No current installed app, legacy parser/bridge, database baseline, release or
server `beta` configuration is changed by this implementation. The planning PR
passed required checks and merged before this implementation branch was based
on main. Implementation still requires its own review and exact-source CI.

## Next steps

1. Add the v2 generated JSON Schema and expand shared Go/TypeScript refusal cases.
2. Add the bounded compatibility evaluator and select/review publisher signature
   verification with common vectors; keep parsing separate from admission.
3. Review and expand the P01 whole-flow prototype against fake typed installation states.
4. Prepare the concrete D2 storage specification for owner review before any DDL
   or runner journal implementation. Earlier Status schema approval does not apply.
5. Continue through P03..P14; do not release these internal primitives as a complete
   app platform or install an app-specific production backend as a shortcut.
