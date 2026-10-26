# Platform implementation evidence

This register distinguishes implemented primitives from a delivered platform.
The implementation objective is the complete application framework in this plan,
not another Status-specific frontend or a manual backend deployment procedure.

## Work package state

| Work package | State | Evidence / next requirement |
| --- | --- | --- |
| P00 | In progress | Initial physical storage scope and [D1/D3/D4 candidate A for development/isolated acceptance](18-runtime-and-trust-proposal.md#9-approval-record) approved; runtime evidence, public freeze and later storage products remain |
| P01 | In progress | Isolated interaction prototype in [PR #189](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/189); owner visual review and broader browser acceptance remain |
| P02 | In progress | Go/SDK declaration parsers, shared 44-case corpus and pure lifecycle transition model implemented; [shared structural schema/types/domains](21-descriptor-schema-implementation.md) implemented; frontend integrity inspector implemented; compatibility evaluator in [PR #190](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/190); [exact-byte publisher verification](24-publisher-signature-verification.md) implemented in isolation; full semantic generation, [trust/staged-release transaction integration](25-trust-and-release-storage.md) implemented; full activation trust integration and complete envelope inspection remain |
| P03 | In progress | Control storage [#193](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/193), leases [#194](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/194), keyring [#195](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/195), [denial review #196](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/196) and [runner journal #198](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/pull/198) passed source CI; [runner journal storage/startup verification](17-runner-journal-implementation.md) implemented. [publisher trust/staged-release storage](25-trust-and-release-storage.md) implemented; [initial install intent](26-install-review-admission.md), full action admission, stage/effect commits, authenticated runner command/resource repositories and coordinated recovery remain |
| P04 | In progress | [Read-only native daemon inspection](23-runner-daemon-inspection.md), bounded CLI and disposable Linux probe workflow implemented; [disposable XFS hard-quota probe](28-project-quota-acceptance.md) has passing native XFS byte/inode/unlinked/remount/control-write evidence; [explicit controller enrollment primitive](29-runner-controller-enrollment.md) and [signed prepare-data intent admission](30-signed-runner-command-admission.md) implemented; runtime-integrated quota/sandbox enforcement, [private Linux development intent transport](31-runner-local-intent-transport.md) implemented; production identity provisioning and physical lifecycle remain |
| P05 | In progress | [Initial static install review/durable intent](26-install-review-admission.md) implemented; [initial fenced worker artifact stages](27-install-staging-worker.md) implemented; actual provider admission, data/execution/activation and complete managed/static installer remain |
| P06 | In progress | [Presentation transport and shared SDK contract](22-presentation-bridge-implementation.md) implemented; [shared experimental React root](36-react-presentation-root.md) adds commit-aware locale/theme/timezone integration; [covered frame host](37-presentation-frame-host.md) implemented without route registration; seamless shell, sizing/navigation/drafts and real-browser acceptance remain |
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

## Private Linux intent transport

[Local intent transport](31-runner-local-intent-transport.md) connects kernel peer
checks and bounded one-request streams to the existing signed journal admission.
The endpoint is opt-in internal code, with a same-user 0700/0600 development
profile. It does not register a service, enroll production trust, return physical
receipts or change server version configuration. The previous signed-command
source `a719efcd30f5bd11506e2110660703411245140e` passed full test run
[40245984411](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40245984411)
and Linux daemon run
[40245984401](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40245984401).
Current transport Linux execution evidence must be checked on its own source CI.

## Separate controller OS identity

[Separate-UID transport](32-runner-controller-identity.md) adds an explicit root
listener/non-root Portal profile with a dedicated group and exact 0750/0660
ownership, preserving kernel UID checks and signed journal admission. It does not
create system identities/directories or turn on a service. The disposable Linux
workflow exercises actual distinct-UID children, including a same-group attacker
holding valid signed bytes and an impostor with matching socket metadata. Verify
that source CI before claiming OS acceptance. Previous private transport source
`89c7c6d2b6624fc20d43460e9688c12c86886950` passed complete repository run
[40246747001](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40246747001)
and Linux race run
[40246747041](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40246747041).

## Portal pending runtime signing identity

[Initial runtime registration](33-portal-runtime-registration.md) fills the Portal
half of explicit public-key enrollment using the approved runtime table and
existing purpose-sealed DEK. Current administrator authority is checked before and
after locks/audit, the binding remains disabled, and exact pending retries preserve
one identity. Both-driver tests exercise audit rollback, actual restore refusal,
rewrap/reopen/concurrency and expiry after an observed PostgreSQL audit lock wait.
The public result explicitly enrolls into an isolated runner journal in a test;
there is no startup/network enrollment or command dispatch.

Separate-UID source `b32e50dfa5d2b1f5ec34058266161ea972b500b0` passed Linux runtime
[40247599811](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40247599811)
and full repository
[40247599835](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40247599835).
Portal lease-bound command production, enrollment confirmation and physical
lifecycle work remain open; initial registration does not enable those paths.

## Live enrollment observation and initial control binding confirmation

[Runtime confirmation](34-runtime-enrollment-confirmation.md) connects a bounded
read-only nonce query over the authenticated local endpoint to the Portal's
administrator/epoch/key checks and atomic initial binding/audit confirmation.
Enabled means confirmed control identity; it does not admit a managed runtime or
prove quota, isolation, capacity or resource readiness. Those gates remain required.
Local both-driver tests cover stale proof/session after observed audit lock waits
and rollback. Dedicated Linux acceptance exercises the actual non-root Portal
entry point against a root journal/endpoint; verify final source CI before claiming
that native result. Prior registration source `c985af4bda3b61eba6eb2b17c09410fc58260342`
passed full [CI 40249657209](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40249657209).

## Runtime control revocation

[Portal signing revocation](35-runtime-control-revocation.md) atomically disables
the binding, discards its sealed signing seed and advances revisions with a current
administrator audit. It preserves the public pin, does not claim physical stop or
recall already signed envelopes, and cannot be undone by initial registration or
confirmation. Both-driver tests cover audit rollback, concurrent/reopened exact
retry, overflow and session expiry after an observed audit lock wait. The normal
source retains the final actor refresh; an overlay removing it fails the expiry
test. Prior confirmation source `5e476e7675df9d8044aa7d25a122fa1ad7716008` passed
[full CI 40252177883](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40252177883)
and [Linux live endpoint acceptance 40252177832](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40252177832).

## Shared experimental React root

[React root integration](36-react-presentation-root.md) implements P06 settings
commit/acknowledgement in the Apps SDK. Local real shared MessagePort integration
checks host-ready ordering, state preservation and generation reset, alongside
180 SDK cases and affected consumer/template builds. It adds no independent locale
selector, frame border, published catalogue entry or production adoption. Actual
Portal host loading cover and full browser/UX acceptance still remain.

## Covered v2 presentation frame host

[Experimental frame host](37-presentation-frame-host.md) adds a borderless,
theme-covered sandbox that reveals only after current-session v2 acknowledgement.
Reload/reopen and identity changes retire old sessions; stale callbacks cannot
reveal or fail the current document. A real shared protocol integration checks
delayed/latest-presentation readiness without a factory mock. This is not a
production launch or immutable asset-serving gate; authorized activation/resource
integration, document sizing/navigation/drafts and actual browser acceptance still
remain. The prior Apps React root and Portal evidence source have passing CI links
in the implementation document.
