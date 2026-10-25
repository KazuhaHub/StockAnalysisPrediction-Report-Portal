# Platform implementation evidence

This register distinguishes implemented primitives from a delivered platform.
The implementation objective is the complete application framework in this plan,
not another Status-specific frontend or a manual backend deployment procedure.

## Work package state

| Work package | State | Evidence / next requirement |
| --- | --- | --- |
| P00 | In progress | Initial physical storage scope and [D1/D3/D4 candidate A for development/isolated acceptance](18-runtime-and-trust-proposal.md#9-approval-record) approved; runtime evidence, public freeze and later storage products remain |
| P01 | In progress | Isolated interaction prototype in [PR #189](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/189); owner visual review and broader browser acceptance remain |
| P02 | In progress | Go/SDK declaration parsers, shared 44-case corpus and pure lifecycle transition model implemented; [shared structural schema/types/domains](21-descriptor-schema-implementation.md) implemented; frontend integrity inspector implemented; compatibility evaluator in [PR #190](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/190); [exact-byte publisher verification](24-publisher-signature-verification.md) implemented in isolation; full semantic generation, durable trust enforcement and complete envelope inspection remain |
| P03 | In progress | Control storage [#193](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/193), leases [#194](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/194), keyring [#195](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/195), [denial review #196](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/196) and [runner journal #198](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/198) passed source CI; [runner journal storage/startup verification](17-runner-journal-implementation.md) implemented. Other action admission, stage/effect commits, authenticated runner command/resource repositories and coordinated recovery remain |
| P04 | In progress | [Read-only native daemon inspection](23-runner-daemon-inspection.md), bounded CLI and disposable Linux probe workflow implemented; actual quota/sandbox enforcement, authenticated control and container lifecycle remain |
| P05 | Not started | No complete installer is delivered by the control schema or daemon metadata probe |
| P06 | In progress | [Presentation transport and shared SDK contract](22-presentation-bridge-implementation.md) implemented in isolation; React root, seamless shell, sizing/navigation/drafts and real-browser acceptance remain |
| P07..P14 | Not started | Follow dependencies and remaining public-freeze, visual and production rollout gates |

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
separate PRs still need their own exact-source CI. Shared corpus agreement is
bounded evidence and does not freeze or fully prove every possible input.

No current installed app, legacy parser/bridge, database baseline, release or
server `beta` configuration is changed by this implementation. The planning PR
passed required checks and merged before this implementation branch was based
on main. Implementation still requires its own review and exact-source CI.

## Frontend artifact inspection

The new `InspectFrontend` helper and `cmd/app-inspect` command validate a bounded
frontend ZIP against the declared digest and return public metadata without
extracting or executing it. See [the inspection contract](12-frontend-inspection.md)
for provisional limits, refusal behavior, tests and remaining envelope/trust work.
This is frontend integrity only; it is not a trusted package or install admission.

## Next steps

Private [effect intent persistence](19-effect-intent-implementation.md) now binds
stable effect identities to current worker leases and preserves uncertain results
across reclaim. It does not dispatch work or complete a stage; typed evidence and
authenticated runner integration remain pending.

The private [removal control stage repository](20-removal-control-stage-implementation.md)
connects consumed disable/uninstall admission to `queued → validate → revoke`
with an atomic stage audit. It cannot complete revocation or claim physical stop;
final provider evidence and the complete installer remain unfinished.

Effect storage [PR #201](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/201)
at `be914ddfd463869109cc13ae8a8a655aedea840a` and control stages
[PR #203](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/203)
at `cda39ae1d1c2d874708e1a84d29a5aeb10f2a8ea` passed their source CI. This does
not establish completed physical stages or runtime acceptance.

1. Add the v2 generated JSON Schema and expand shared Go/TypeScript refusal cases.
2. Add the bounded compatibility evaluator and select/review publisher signature
   verification with common vectors; keep parsing separate from admission.
3. Build the P01 whole-flow prototype against fake typed installation states.
4. Continue the approved initial D2 implementation through durable operation
   repositories and runner journal; later storage products still need their own
   physical proposal. See [the accepted initial scope](13-control-storage-spec.md).
5. Continue through P03..P14; do not release these internal primitives as a complete
   app platform or install an app-specific production backend as a shortcut.
