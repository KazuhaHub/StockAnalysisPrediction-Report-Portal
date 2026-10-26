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
| P06 | In progress | [Presentation transport and shared SDK contract](22-presentation-bridge-implementation.md) implemented; [shared experimental React root](36-react-presentation-root.md) adds commit-aware locale/theme/timezone integration; [covered frame host](37-presentation-frame-host.md), [authorized HTTP document delivery](41-authorized-document-http.md), [actual HTTP/SDK browser fixture](42-http-sdk-browser-acceptance.md), [protected launch page](43-app-launch-page.md) and [audience-filtered app directory](44-app-directory.md) implemented; [signed display metadata](45-signed-display-metadata.md) available for experimental packages; complete seamless shell, publisher metadata enforcement, sizing/navigation/drafts and full browser acceptance remain |
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

## Current-reader launch access

[Launch inspection](38-launch-access-inspection.md) checks the current ordinary
reader session, exact user/actual-ancestor OU grants, active generation/policy and
namespace, current publisher trust and actual stored frontend integrity. Admin
management authority does not imply audience access. Both-driver fixtures cover
identity/state refusal and OU reparenting; real observed PostgreSQL release-lock
waits exercise session/key expiry after waiting. A stale-clock negative-control
overlay makes both expiry cases fail; the normal source retains the final sample. The returned metadata is a
point-in-time logical binding, not a token, resource readiness proof or asset
serving route. Physical activation, immutable request-bound serving and full
installer/shell acceptance remain required. Prior covered-host source passed
[CI 40257791625](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40257791625).

## Verified asset bytes under current authorization

[Buffered file reading](39-verified-asset-read.md) checks the complete immutable
archive and extracts an exact bounded member into an independent buffer. The
Portal wrapper repeats current reader/OU/trust checks and compares all expected
binding fields in the read transaction; it returns bytes only after the final
session/key/epoch check and commit. Revocation invalidates earlier metadata.
Actual PostgreSQL lock-wait expiry cases exercise the file-read wrapper, including
discarding buffered content on failure. A leaky-error negative-control overlay
makes both file-expiry tests fail; the normal wrapper discards the buffer. Full
local Go regression, targeted race checks, vet, staticcheck and build pass. No HTTP asset route or browser transport
is registered; sandbox/cookie/subresource delivery needs real integration before
connecting the host. Installer completion, physical activation, broker enforcement
and full platform acceptance remain open.

## Self-contained sandbox document

[Document compiler and browser transport](40-self-contained-app-document.md)
embeds verified local resources into one private derived HTML document with an
opaque sandbox and CSP denying direct fetch/subresources. Self-navigation
exfiltration remains an explicit unresolved architecture/acceptance requirement. The current-reader wrapper buffers all
compilation before its final authorization/trust check. Actual Portal frame and
Apps React/Ant SDK builds run together in a loopback browser fixture; changing
host locale/timezone preserves app-local counter state. This is not a registered
production HTTP path, public SDK freeze, full UX acceptance or installer readiness.
Actual HTTP policy/budgets, compatibility review, physical activation and remaining
platform work stay open. Prior source CI passed for launch access at
`c271fb49711a7b8b664390cba2543592732d76ee` (run 40260967506) and verified assets at
`c8e34a8137989468a916264faa851e672d3ebcad` (run 40261213274).

## Authorized launch/document HTTP surface

[Session-backed document delivery](41-authorized-document-http.md) registers launch
metadata and exact-version document responses through the central session/enrolment
wall. A two-slot process budget and context-bound SQL executor bound compilation
admission and lock waits; current authorization/trust rechecks still precede any
content write. The experimental frame validates the new source against its bridge
binding. Actual HTTP mux, cancellation and existing reader/route regressions cover
the source; combining these exact handlers with the SDK in a browser and production
shell remains required. Physical installer activation, sensitive broker/navigation
policy, full shell UX and remaining platform acceptance are still open.

## Actual HTTP plus SDK browser fixture

[Loopback HTTP/SDK acceptance](42-http-sdk-browser-acceptance.md) combines actual
Portal signed-session/route/database/release checks with the real frame and SDK
root through a minimal development reverse proxy. Browser checks demonstrate
startup, state-preserving locale change, refresh, grant revocation (old document
and new launch 404), restored access, session revocation (401), and no session
resurrection on refresh. Fixture controls and synthetic credentials exist only in
a test binary/private temporary directory; activation rows remain explicit logical
test state, not installer/provider readiness. Production shell, actual market
install/activation, broader browser/UX and sensitive broker/navigation policy remain
open. This evidence does not change server beta configuration or production data.

## Portal v2 launch page

[Authenticated launch page](43-app-launch-page.md) registers a protected v2 page,
validates real launch metadata and follows the existing Portal preferences/Ant
tokens. Pending metadata aborts on exit; invalid data never mounts content. The
layout allocates remaining viewport height without an outer app card or separate
language control. True document measurement, names/discovery, routing/drafts and
full shell acceptance remain open. A real-page HTTP/SDK fixture verifies language
and theme changes retaining child state and host Back navigation retiring the
frame. Full local web regression passes 1648 cases and Go regression with disposable
PostgreSQL passes; exact-source CI is still required. Existing legacy app routes and production/beta configuration are
unchanged.


## Audience-filtered app directory

[Application discovery](44-app-directory.md) projects audience-filtered control
records into the existing AppsHub. User/OU lifecycle and actual ancestry are
checked on every page; filtering precedes the bounded keyset page limit and
administrator role adds no audience bypass. Directory control-state hints do not
replace fresh launch/package/trust checks or prove provider readiness. The client
handles partial legacy/v2 failure, pagination, stale account responses and fixed
translated errors. SQLite/PostgreSQL and React tests cover these paths, including
real database lock-wait cancellation and session expiry. No actual market package,
physical activation, production configuration or Status adoption is added. The
launch-page predecessor passed exact-source CI run 40273040782; this slice needs
its own source CI. Full P00–P14 delivery remains in progress.


## Signed host display metadata

[Signed localized display metadata](45-signed-display-metadata.md) extends the
shared descriptor source and Go/SDK types with a bounded three-locale copy/icon
object. The directory checks lightweight signed metadata against current trust
and activation identity without ZIP reads; expired/revoked labels fall back and
lose the open action. The launch response obtains copy from its existing fully
verified manifest. Shared host parsing and Ant icons render localized card copy,
launch headings and accessible frame labels without refetching on language
changes. Old unpublished experimental descriptors retain an explicit fallback;
publisher enforcement/public contract freeze and full market/install acceptance
remain open. The directory predecessor passed source CI run 40274730211.


## Native XFS data preparation primitive

[Native data preparation](46-native-xfs-data-primitive.md) adds a pinned-FD Go
primitive for creating fresh sealed XFS namespaces and setting/observing project
hard limits. It refuses reuse and preserves partial failure as uncertain without
blind replay/deletion. The disposable quota workflow now includes independent C
UAPI checks and native Go-created directory workloads/reopen/enforcement refusal.
Native source `0ae7c6a7bf0fdc8e1a3eb98ec1adbaa19c6c6561` passed quota run
40278207342; its downloaded report verifies real byte/inode/open-unlinked denials,
fresh remount inspection, disabled-enforcement refusal and cleanup. The primitive
has no runtime/worker entry point; journal reservation,
execution claims/receipts, reconciliation and writer admission remain required.
The display-metadata predecessor passed source CI run 40276186871.


## Durable data resource reservations

[Runner data reservations](47-data-resource-reservations.md) bind an already
admitted current signed prepare-data command to one stable namespace/project
resource row. Exact replay/reopen preserve the mapping; allocation is serialized,
monotonic and retains absent/history IDs. Original command/resource floors and
ownership identity are rechecked; no command is executed, observed or completed.
Tests cover concurrency, renewal, corruption, expiry, rollback and capacity.
The native data primitive predecessor passed main run 40278679685 and quota run
40278679625. Connecting reservations to native work still requires durable
execution claims, physical root/ownership verification, receipts and restart
reconciliation before any writer or activation authority can be created.
