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
| P05 | In progress | [Initial static install review/durable intent](26-install-review-admission.md) implemented; [initial fenced worker artifact stages](27-install-staging-worker.md), [native data-stage commit](53-portal-install-data-stage.md) and [initial static activation with reviewed audience](54-static-activation-and-audience.md) and [signed data dispatch/bounded install driver](55-signed-data-dispatch-and-install-driver.md) and [opt-in background static scheduling](56-background-static-install-scheduler.md) and [runtime-free no-data frontend installation](57-runtime-free-frontend-installation.md) implemented; startup wiring, explicit fenced recovery/takeover, provider-backed static data, complete managed installer and browser market install flow remain |
| P06 | In progress | [Presentation transport and shared SDK contract](22-presentation-bridge-implementation.md) implemented; [shared experimental React root](36-react-presentation-root.md) adds commit-aware locale/theme/timezone integration; [covered frame host](37-presentation-frame-host.md), [authorized HTTP document delivery](41-authorized-document-http.md), [actual HTTP/SDK browser fixture](42-http-sdk-browser-acceptance.md), [protected launch page](43-app-launch-page.md) and [audience-filtered app directory](44-app-directory.md) implemented; [signed display metadata](45-signed-display-metadata.md) available for experimental packages; [document sizing](120-document-layout.md), [dirty navigation](121-unsaved-change-guard.md), [signed history](122-signed-route-navigation.md), [typed queries](123-typed-route-query.md) and [shared page actions](124-shared-page-actions.md) now have development/browser evidence; maintained v2 templates, persistent data/drafts, complete shell/focus/accessibility and full browser acceptance remain |
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


## One-shot data execution claims

[Durable execution claims](48-data-execution-claims.md) persist unknown state and a
bounded claim marker before returning a private one-shot handle. Invocation
rechecks current signed authority/resources and consumes shared process-local
state; copied handles, expiry, callback error/panic and process reopen cannot
create a second invocation. A cancelable execution gate holds journal ownership
through synchronous work while allowing separate short SQL transactions. Close
waits for active callbacks. This is an unwired infrastructure seam with synthetic
callback tests, not a native receipt/recovery/activation implementation. The
reservation predecessor passed main run 40289609568 and runner run 40289609569.
Physical root binding, native execution integration, settlement/reconciliation,
writer admission and complete installation remain open.


## Physical data-root identity

[Native root identity](49-data-root-identity.md) makes the filesystem UUID and
root inode mandatory expectations for native prepare/inspect. Pinned-FD checks
also revalidate private permissions, mount options and project enforcement.
The disposable probe independently supplies the pin, measures the geometry ABI,
checks path replacement and remount, and changes only its owned image UUID to
exercise old-pin refusal. Local Go/race/vet, cross-compilation and twelve Python
refusal tests pass. Source `0473cecac83120b3e402e196434a2d4b96e4fe73` passed
native run 40296737513; its downloaded report confirms changed-UUID refusal,
remount, enforcement and cleanup. The execution-claim
predecessor passed main run 40294014358 and runner run 40294014396. Durable root
binding, concrete executor/receipts, reconciliation and full installation remain
open; no production or server beta configuration changes.


## Native data execution and receipts

[Concrete native execution](50-native-data-execution-receipts.md) joins signed
admission/reservation/one-shot claims to the pinned XFS provider. Physical root
history is committed before mutation; a typed native receipt and resource
observation settle atomically afterward. Partial native or persistence failure
retains unknown state and cannot reconstruct/replay the live handle. The
extended disposable workflow exercises actual journal/native preparation,
alternate-root refusal, failure after physical creation and remount history.
Source eb3f52d64b291def1de82b68c853bae2dbf1f145 passed native run
40298739199; the downloaded report confirms physical receipts, root binding,
uncertain settlement, remount history and changed-UUID refusal with cleanup.
Full local Go tests and full journal race checks passed; subsequent clock-guard
coverage passes targeted race tests. Main/runner CI have independent status.
Root-identity predecessor source
8198a047cb6d50899da6ea94789be5155d2b9706 passed main 40297033484, runner
40297033450 and native 40297033502. Recovery, controller receipt delivery, Portal
stage completion, writer admission and the complete installer remain open.


## Completed-data recovery under fresh authority

[Inspection-only data recovery](51-data-preparation-reconciliation.md) checks a
freshly signed envelope against retained unknown intent, observes the original
sealed namespace and atomically records a recovered receipt. Original claim/root
identity remains intact; expired original authority is not resurrected, and
absent/partial namespaces are not recreated or repaired. The disposable native
fixture now covers actual original-envelope expiry, concurrent settlement,
unchanged physical identity and incomplete/absent refusal. Source
1f4eb273f4ae821bdb7763b6f202980766bc5acf passed native run 40300992906; its
downloaded report confirms all new recovery flags and owned cleanup. Full local
Go/race checks and the freshness negative control pass; main/runner CI are
independent. Receipt predecessor source
6b359965de8b5cbd780985400a3328a0aedd7f1d passed main 40299103478, runner
40299103353 and native 40299103357. Partial/absence recovery, takeover, controller
receipt delivery, Portal stage completion and the full installer remain open.


## Authenticated historical data receipt retrieval

[The local receipt channel](52-authenticated-data-receipt-channel.md) adds an
applied-only journal read and a separate nonce/request-bound message under the
existing UID-authenticated endpoint. Canonical receipts must match current signed
intent and unchanged observed resource identity. The opaque client observation
retains controller and endpoint profile and refuses JSON authority. Admission
acknowledgements remain distinct. The disposable workflow now tests actual normal
and recovered receipts across root/non-root identities using the same journal,
with group-only, signature and direct-journal denials. Source
f3e77b540565d6fc678ec1be29f184bd4440d0ba passed native run 40304423554, including
Linux transport race tests. Its downloaded report confirms all three new transport
flags and existing native/cleanup evidence; main/daemon CI are independent.
Recovery predecessor source
a9a716b2a742720b503a0249118cdd5e3bf5f6f7 passed main 40301418154, runner
40301418166 and native 40301418160. Portal command construction, receipt/stage CAS,
writer/activation and the full market installation experience remain open.


## Portal signed data intent and stage commit

[Portal data preparation](53-portal-install-data-stage.md) creates a stable pending
namespace/effect pair and signs the exact reviewed data intent with the existing
sealed runtime key under the current lease. Static applications keep NULL backend
runtime identity. The actual endpoint entry point reads opaque root-profile
receipt evidence; the repository atomically applies namespace, effect and
prepare_data-to-activate transition with late context/authority/proof checks.
No application activation or access grant is implied. SQLite/PostgreSQL source
checks and delayed-audit/cancellation cases cover transaction boundaries; the
extended native workflow tests the real non-root Portal through XFS execution
and receipt retrieval. Source daf3f154fa1b65bc8d7fa2f89e7211783f8e87c8 passed
native run 40311415337; its downloaded report confirms the actual Portal data
stage chain without activation and owned cleanup. Full local Go/PostgreSQL and
targeted race/delayed-audit checks pass; main/daemon CI are independent.
Receipt-channel predecessor source 5244eb1d48d801577fe1336ce08b57879221a325 passed
main 40305195824, runner 40305195850 and native 40305195814. Activation,
automatic dispatch and complete market install experience remain open.


## Initial static activation and reviewed audience

[Static activation and audience](54-static-activation-and-audience.md) binds
explicit user/OU identity snapshots into the existing canonical install review.
Review and admission grant nobody. The initial activate stage reconstructs the
exact prepared namespace/effect and receipt, compiles the current frontend, then
atomically commits activation, only the reviewed grants, ready state, completed
operation and audit. Late lease/context/authority failures roll back the whole
visibility boundary. Current actual OU ancestry remains the launch-time policy;
there is no installer/admin bypass. Existing storage is reused without migration.
The disposable Linux fixture is extended to perform actual activation and a
protected document read after actual native preparation and receipt retrieval.
Source 282ce732c45371967fb4085cb39462d36e89904c passed native run 40317472960;
the downloaded report confirms actual activation/launch, preceding data-stage
separation, existing recovery/quota checks and owned cleanup, while retaining
execution_ready=false. Full local Go/PostgreSQL and targeted race checks pass;
main source CI 40317472965 also passed with all configured race/web/image checks.
Data-stage predecessor source 18892f88a18fdc44ee9036bd52b152f12994f4a8 passed
main 40312440453, daemon 40312440434 and native 40312440473 (checked directly from
PR #248). Initial static activation is one internal lifecycle step; market UI,
automatic worker/dispatch, runtime-free static data, managed lifecycle and the
complete application platform remain open.


## Signed data execution request and bounded static installation

[Signed data dispatch and install driver](55-signed-data-dispatch-and-install-driver.md)
connect current journal state to concrete native execution, read-only recovery or
unchanged applied receipt retrieval. An explicit opt-in root endpoint accepts a
separate signed execution protocol; admission and read-only queries remain
non-executing. Portal persists/revalidates intent before dispatch and uses the
existing opaque-evidence commit boundary afterward. A bounded driver advances
already-claimed initial static installs, replacing each exact lease token; stage
SQL now supports context cancellation. The native fixtures now cover actual
non-root Portal dispatch rather than manual root-side execution, unknown recovery,
partial/absent refusal, applied replay and concurrent first requests. Native
run 40320510850 passed on source 6e13be8466430603a515006d13dea5a8d6914756;
the downloaded report confirms actual signed dispatch/recovery, activation/launch,
partial/absent refusal and owned cleanup, with execution_ready=false. Final local
full Go/PostgreSQL tests and focused worker/staging/dispatch race tests pass; main
CI 40320510835 and daemon CI 40320510816 also passed. Static activation
predecessor 819ce451ace66174b8cea60645214a33fed1713b passed main 40318413527 and
native 40318413393. Automatic polling/startup, market HTTP/UI, managed lifecycle
and the complete platform remain open.


The additional full-driver fixture source
589b5aabec33c07c3ae663de54909288c27096e8 passed native run 40321680162 and daemon
run 40321680182. Its downloaded report confirms actual queued-to-complete native
installation and protected document access for a second independently signed
application, as well as preceding dispatch/recovery/cleanup checks. Main run
40321680160 has independent status; the follow-up changes only native acceptance
and documentation, preserving the preceding production source.


## Opt-in background static installation service

[Background static scheduling](56-background-static-install-scheduler.md) discovers
eligible queued/expired effect-free initial static installs, revalidates reviewed
authority and claims them atomically under the shared lock order. The explicitly
owned service renews at serial boundaries, drives bounded attempts and retains
uncertain progress; retry exhaustion or expired external-work intent becomes an
audited review/recovery state with cleared leases and preserved data/evidence.
Lease writes now respect context and cannot renew an already expired original
lease after blocking. Existing startup/configuration remains unchanged. Native
acceptance now starts the actual service after admitting a second queued package,
with no manual claim/driver call for it. Native acceptance for this source
passed in run 40326048071 on source 616003ab97bf77e9e98e2599aa041c12454501c2.
The downloaded report confirms actual background scheduling, protected document
access, preceding dispatch/recovery and owned cleanup, retaining execution_ready=false.
Full local Go/PostgreSQL tests and focused race checks pass; main source CI
40326048047 also passed all configured checks. Dispatch predecessor
68cb91971a472a99475cccaaa7f5c0a3ae326e35 passed main 40322270790, daemon 40322270597
and native 40322270932. Fenced recovery approval/takeover, startup wiring, market
HTTP/UI, managed lifecycle and the complete platform remain open.


## Runtime-free frontend installation profile

[Frontend installation without a runtime](57-runtime-free-frontend-installation.md)
is now the generic static review default. An exact reviewed no-data format binds
zero quota, no required provider capabilities and explicit app-use audience. Local
prepare/activate transactions create no namespace/effect/runtime and use the
approved nullable activation fields with typed integrity evidence. Protected reads
verify this profile separately from real native namespace readiness. A portable
empty-endpoint worker selects only these jobs; native acceptance uses explicitly
named native review entry points with unchanged canonical bytes. Effect-free
frontend activate stages can resume after expiry with a new fence. The loopback
HTTP/SDK browser fixture now installs the actual frontend before serving it;
English/Simplified/Traditional presentation and in-memory interaction were checked
through the browser plugin. New profile checks preserve database cancellation
errors after the existing wait regression exposed an incorrect error conversion.
Source b7733590f8265eae26d8ae32106dd2b755f52e57 passed main CI 40334056417 and
native regression 40334056329. Local full Go/PostgreSQL and focused race/context
checks passed; the downloaded native report confirms retained native behavior
and owned cleanup with execution_ready=false. The no-data path is separately
verified by repository/service/HTTP tests and the actual loopback browser sequence.
Scheduler predecessor
804fdc2a46ca095677c711f43dddbf7b2c173235 passed main 40327545213 and native 40327545246.
Market HTTP/UI, provider-backed data/managed lifecycle and the complete platform
remain open.

## Process-owned frontend installation service

[Frontend service lifecycle](58-frontend-install-service-lifecycle.md) wires the
portable no-data scheduler into Portal startup and bounded shutdown. One owner
per Server supplies a random worker identity, concurrent start-once/stop/join
and a fixed-message panic boundary. Startup does not initialize missing platform
control, unpause a restored controller, claim native-data jobs, create native
resources or change deployment configuration. Focused service tests exercise
actual autonomous installation/launch, repeated concurrent ownership and stop,
fresh/paused/native-only database immutability, new admission after idle and panic
containment. Local full Go/PostgreSQL tests passed (application package 155.990 s),
as did focused SQLite/PostgreSQL frontend race checks (4.882 s), vet, staticcheck
and Linux amd64 compilation.
Administrator plan/commit/
progress HTTP/UI, remote marketplace publication, managed lifecycle and the
complete platform remain open. This source work is not production deployment
approval.

## Administrator installation review/admission/progress HTTP

[Installation management HTTP](59-installation-management-http.md) exposes the
existing no-data review and idempotent admission repositories through three
administrator/session/enrollment-protected routes. Exact bounded JSON, origin
checks, shared foreground verification slots, request-bound SQL/backoff and
format-specific admission prevent caller-supplied runtime instructions or native
review bypass. Progress is read from durable SQL, remains inspectable during
restore pause and exposes safe codes with three-locale messages. HTTP tests cover
actual review/admission/background completion/launch, replay/conflict, role and
enrollment gates, malformed inputs, stale audience, empty audience, native review
refusal, pause/redaction and real canceled PostgreSQL advisory-lock waits.
Local full Go/PostgreSQL regression passed (application package 158.082 s), as did
focused SQLite/PostgreSQL management race checks (35.005 s), vet, staticcheck,
Linux amd64 compilation and web
locale tests/typecheck/lint/build. The exact dump cancellation assertion accounts
for the shared authentication layer's legitimate initial session activity refresh.
Predecessor service source 8e784be889fb11feb606667e46e2e31e5532fc31 passed main
CI 40339290369 and native regression 40339290645. Catalogue management UI,
publication, managed lifecycle and full platform acceptance remain open.

## Installation confirmation UI and staged directories

[Installation confirmation](60-installation-confirmation-ui.md) adds the new
platform tab to existing application management, preserving the old page behind
the legacy tab. Signed staged-release summaries and minimal paged user/OU choices
support explicit audience selection, canonical review, a localized permission/data
confirmation and durable operation observation after reload. A confirmed request
identity is saved before transmission and retained for exact replay after an
uncertain response. Account and directory lifecycle counters reject stale async
responses; storage failure prevents transmission. Tests cover those behaviors
and preserve legacy permission confirmation. Local full Go/PostgreSQL regression
passed (application package 168.031 s), the full web run passed 1668 tests, and
subsequent focused UI/locale checks passed 33 cases. Browser-plugin loopback checks
exercised actual preview/confirm/background completion/refresh, cancel, empty
audience and three-locale presentation. Native desktop control and production
configuration were not used. Focused SQLite/PostgreSQL catalogue/management race
checks passed (42.029 s), as did vet, staticcheck, web build and Linux amd64
compilation. Owned browser fixture processes/listeners and disposable PostgreSQL
were stopped after verification.
Management API predecessor 96591a12068151c5916d8388739d3c1b46d18b27 passed main
40341986266, daemon 40341986230 and native 40341986307. Remote marketplace
publication, managed/full lifecycle and complete platform acceptance remain open.

## Development catalogue and verified remote staging

[Market fetch](61-development-market-fetch.md) adds a bounded provisional v2
catalogue and administrator discovery/staging endpoints. The dedicated public
HTTPS client strips deployment credentials, checks actual dial addresses and
redirects, bounds bytes/time/status and respects cancellation. Staging selects an
exact descriptor digest, refreshes current trust before artifact download and
uses the existing context-bound atomic release repository after the full archive
download. It adds no trust key, installation or app-use grant. Actual TLS tests
cover tampering, trust changes, repeat staging, cancellation and transport/input/
authority refusal. Full local Go/PostgreSQL regression passed (application package
160.173 s); final market/catalogue race checks passed (application package 5.701 s,
contract package 1.741 s), along with vet, staticcheck and Linux amd64 compilation.
Installation UI predecessor bd93e252e0e73a06d0f8c3d67d64633f1f441987 passed main
CI 40392383575. Online UI,
publisher onboarding/publication, managed/full lifecycle and full platform release
remain open; production configuration and the legacy Apps catalogue are unchanged.

## Online market interface and exact verified release handoff

[Online market UI](62-online-market-installation-ui.md) connects explicit remote
discovery and verification/download to the existing audience/installation review.
Catalogue hints stay visibly unverified; successful staging is followed by current
signed metadata lookup by exact descriptor digest. A verified summary is pinned
for immediate review even beyond the initial directory page. The market never
POSTs installation plans/operations itself. Account/source object identities and
busy ownership reject stale responses or conflicting confirmation work. Tests
cover remote-to-review integration, failure, malformed verification claims,
uncertain retry and account/source changes. An opt-in synthetic TLS browser fixture
exercised actual discovery/staging/review/background completion/reload with three
locales, using only the browser plugin. Full Go/PostgreSQL regression passed
(application package 192.059 s), focused directory/market SQLite/PostgreSQL race
checks passed (18.253 s), and full front-end regression passed 151 files / 1680
tests after correcting the loading-exit timing in a retry test. Focused UI/locale
checks passed 43 tests; typecheck, lint, web build, vet, staticcheck and Linux
amd64 compilation passed. Market-fetch predecessor
574473bf41f2ed12166530b5c506807dd8009289 passed main 40393799973 and native
regression 40393799881. Publisher onboarding and signed publication/acceptance hosting,
managed/full lifecycle and complete platform release remain open.

## Publisher trust management HTTP

[Publisher management](63-publisher-management-http.md) exposes explicit empty
control initialization, public trust inspection and typed register/policy/add-key/
revoke-key operations through current administrator/session/enrollment/origin
boundaries. Revisions, exact nested inputs and final authority checks preserve
the existing trust repository guarantees. Mutation/backoff now follow request
context. Fresh reads create no keyring/control identity; initialization refuses
partial/paused markers and any retained platform-table history, including a
standalone artifact. HTTP tests cover rotation/revocation, stale policy, fresh/
paused/orphaned state and refused unauthorized/malformed requests; blocked
PostgreSQL read/write/initialize cancellation is exercised separately. Full local
Go/PostgreSQL regression passed (application package 162.080 s), as did final
SQLite/PostgreSQL publisher race checks (19.132 s), vet, staticcheck and Linux
amd64 compilation. Owned disposable PostgreSQL was stopped after validation.
Online market UI predecessor
9840ebbce37194b2b52940233db2b370fcf954e2 passed main CI 40395583598.
No real trust enrollment, production
configuration or public API/SDK freeze is included. Trust UI, signed publication,
managed/full lifecycle and full platform acceptance remain open.

## Publisher trust management interface

[Publisher management UI](64-publisher-management-ui.md) adds the shared React/
Ant Design management tab for explicit empty-platform initialization, default-
disabled publisher registration, scope policy, key rotation and revocation. Each
mutation has a separate review and acknowledgement; raw-key SHA-256 fingerprints
and current publisher/key revisions bind the reviewed change. Local draft errors
remain correctable, while uncertain saves require an explicit fresh trust read.
Language changes cannot silently clear that lock. Account lifecycle checks discard
late reads, previews and save results.

A disposable loopback fixture with no initial control identity exercised the real
page and HTTP handlers: initialization, disabled registration, fingerprint review,
adding a second key and retaining the first at publisher revision 2. Three locales
and shared theme switching were inspected through the browser plugin. The owned
fixture listener and process were stopped. This is local functional evidence,
not final D5 shell/mobile/screen-reader acceptance or production trust enrollment.

Full local front-end regression passed 153 files / 1697 tests; subsequent added
initialization, rotation and language-lock cases plus final guard changes passed
45 focused UI/key/locale tests. Typecheck, lint and production web build passed;
lint reports pre-existing warnings outside the new panel. Full local Go regression
passed (application package 105.444 s, PostgreSQL integration cases skipped without
a DSN); focused publisher HTTP/fixture checks passed (1.592 s). The unchanged
publisher HTTP predecessor c9d9c404c55bce9c3f990f62b18f5c185da81db4 passed main
CI 40401915455, native quota 40401915452 and daemon 40401915687. Real publisher
onboarding, signed publication/acceptance hosting, managed backend execution,
broker and full lifecycle/release acceptance remain open. No production channel,
real credentials, Status migration or public SDK freeze is included.

## Managed backend image inspection prerequisite

[Immutable image inspection](65-managed-backend-image-inspection.md) binds a
read-only safe Unix-socket Docker image observation to exact descriptor bytes,
reviewed repository digest, native configuration ID and declared Linux platform.
It rejects image-declared anonymous volumes, unsafe aliases/variants, oversized or
malformed responses and canceled observations. Output excludes image environment,
commands and daemon host paths. No pull, container creation/start or implicit
execution authorization is added; runtime readiness remains unchanged.

Local runner/control/data/journal/transport and CLI race regression passed
(journal 62.842 s). After the final Docker Hub familiar-name matching change,
driver race regression passed (1.368 s), as did vet, staticcheck and Linux
amd64/arm64 test compilation. Real-daemon image inspection is added to the
existing GitHub-hosted disposable daemon workflow; its execution result remains
to be checked on this source revision. Local macOS checks do not prove Linux
runtime behavior. The next integration still requires closed container actions,
signed managed effects, ownership reconciliation, broker and full lifecycle.
Production trust/runtime configuration, credentials, Status and beta settings
are untouched.

## Closed candidate container policy and adapter

[Candidate container policy](66-candidate-container-policy.md) adds private typed
create, native-ID observation and ownership-checked non-forced removal primitives.
The policy fixes native image, non-root user, read-only root/data, private socket
mounts, network denial, resource/log limits and disabled inherited health checks.
Identity/fence/policy substitutions refuse, and uncertain create/delete effects
are never automatically retried or adopted by name. These primitives are not
wired into a production runner command dispatcher; signed resource admission,
physical mount/quota pins, start/readiness and the active-writer transition remain.

Local driver race tests passed (1.445 s), with vet, staticcheck and Linux amd64
compilation passing. A new root-only GitHub-hosted disposable test creates an
owned stopped fixture, inspects policy and removes it without forcing or deleting
volumes; its actual CI result must still be checked. The image-inspection
predecessor 446f8c369531e82121a7239579fdcbfffe8ebbe8 passed real daemon workflow
40405086955 and full main CI 40405087018. No production configuration, schema,
credential, Status migration, SDK freeze or runtime-ready claim is included.
The final private-plan check additionally validates canonical policy bytes and
binds owner fields to labels before all adapter actions. Driver race regression
passed in 1.452 s after this change; vet, staticcheck and Linux compilation passed.
Native run 40406868559 failed before any container effect: the separately compiled
acceptance binary ran from the repository root, while shared test descriptors use
the normal Go package working directory. The workflow now enters the driver
package before invocation. This is fixture wiring, not native policy evidence;
the corrected revision still needs the real-daemon acceptance run.

## Signed candidate command and journal admission

[Candidate command admission](67-candidate-command-admission.md) defines a separate
bounded Ed25519 signature/effect domain for start-candidate intent. It binds exact
release, resource/namespace, generation, writer identity, platform and the closed
sandbox profile without accepting paths, arbitrary Docker options or credentials.
Stable framing and every immutable input have explicit tests; existing prepare-
data wire bytes and its shared corpus remain unchanged.

Candidate admission uses a private verified index projection with the existing
atomic command/floor/replay path. It refuses stale/foreign ownership, changed
logical effects, pending command conflicts and reuse/rebinding of resource IDs.
Fresh admission records only intent, not a container or execution/write claim.
No schema, production route or dispatcher changes are included.

Full runner/CLI race regression passed (journal 64.403 s), focused final command/
floor cases passed (journal 5.187 s), full control/profile race tests passed
(1.360 s), and full local Go regression passed (application 66.808 s). Vet,
staticcheck and Linux amd64 journal compilation passed. Container predecessor
c1ac06701b4ccf60dfd5ed0c33612d7d72a7fa48 passed real daemon workflow 40407582383,
including stopped candidate create/inspect/ownership-checked removal after the
fixture working-directory correction. Durable candidate execution claims,
physical pins/receipts, start/readiness/stop, automatic backend registration,
broker and full platform lifecycle/release acceptance remain open.

## Candidate reservation and one-shot execution claims

[Candidate execution claims](68-candidate-execution-claims.md) reserve a global
container resource identity only after matching the exact prior applied data
receipt and current candidate authority. Claiming durably changes prepared to
unknown before returning a process-owned, unserializable handle. Copies share
one atomic consumption bit and an envelope-bounded monotonic deadline. The
trusted synchronous invocation gate rechecks authority, floors, resource, marker
and data proof; Close waits for the provider to settle. Restart retains unknown
intent but cannot recreate a first execution handle or replay it automatically.

Volume lookup now permits a container row to share its data namespace while
preserving volume/resource-ID collision checks. Added tests cover rollback,
coexistence, mismatched data tuples, stale authority/resources, copied/concurrent
handles, cancellation/expiry, owner restart and Close. No schema, production
route/native dispatcher, physical mount pin, execution receipt, writer grant or
readiness claim is introduced.

Runner/CLI race regression passed (journal 66.113 s) before final added cases;
focused final candidate race cases passed (4.888 s). Full local Go regression
passed (application 65.049 s, journal 15.116 s); vet, staticcheck and Linux
amd64/arm64 compilation passed. No local PostgreSQL DSN was used. Predecessor
836c1ae5090e626367aa47b1f041dd76113df8f0 passed full main CI 40408711973,
disposable daemon 40408711894 and hard quota 40408711972. Native claims/receipt/
reconciliation, start/readiness/stop, backend registration, broker and complete
lifecycle/release acceptance remain open. Production/beta settings, credentials,
Status migration and SDK freeze remain untouched.
Final-source full journal race regression, including all added cases, passed in
73.029 s.

## Native candidate directory identity pins

[Native mount pins](69-native-candidate-mount-pins.md) retain Linux directory
identities for the planned data/HTTP/broker sources and their root-owned ancestors.
Component-wise no-symlink traversal and held/fresh identity comparisons refuse
substitution, changed permissions or unexpected ownership. The private create
wrapper validates before and after its single Docker request and holds pin
lifetime across it. Post-effect substitution remains uncertain, not an automatic
retry or readiness result. No native journal dispatcher, directory repair,
quota claim, writer grant or data access transition is introduced.

The existing disposable stopped-container acceptance uses these pins, and native
negative cases cover source/parent/owner/mode/symlink substitution, closed pins,
cancellation and before/after-create refusal. Local runner/CLI race regression
passed (journal 68.584 s), along with Linux amd64/arm64 compilation and Linux-target
vet/staticcheck. Actual native execution remains pending the current revision's
CI; macOS checks do not substitute for it. Managed start/readiness, native claim
receipts/reconciliation, backend registration, broker and full lifecycle remain
open. Production credentials/configuration, beta settings and Status are untouched.
Functional mount-pin source 025da0abe51cbb2cc896b6d478fc3616bb3fee27 subsequently
passed disposable Linux workflow 40447371928. Its explicit root step actually ran
and passed the real stopped-container fixture and all mount pin/negative/boundary
cases; ordinary-phase gated skips were not counted as evidence. Identity isolation
and Portal endpoint confirmation also passed. Full main CI was still running at
this observation; native mount validation does not complete managed startup.

## Candidate native start and stop adapters

[Candidate start/stop](70-candidate-start-stop.md) adds closed native-ID lifecycle
actions with current owner/policy validation before and after possible effects.
Initial start requires created state and live mount pins; exited processes are
not silently restarted. Stop is fixed/bounded and returns an observation only
when the owned process is confirmed exited. Lost acknowledgements and post-effect
identity/state changes remain uncertain. Nested state aliases and already-
canceled transport access now refuse explicitly.

Portable lifecycle/refusal/uncertainty tests passed in the runner/CLI race suite
(journal 70.173 s, driver 1.472 s); Linux-target vet/staticcheck and amd64/arm64
compilation passed. A root-only disposable CI case uses the official digest-
resolved Kubernetes pause fixture to verify actual running/stopped transitions,
running-removal refusal and automatic-restart refusal. Its actual execution
result is still required. No production dispatcher, native claim receipt,
application health, data-access/writer grant or activation is included. Broker,
registration and complete lifecycle/release acceptance remain open; production
credentials, beta settings, Status and SDK freeze are untouched.
Native lifecycle functional source 71105b28a04aebafe7965043661988aa00a7adb3 passed
Linux workflow 40448345362. Actual root-step logs confirmed the inert process ran,
refused running deletion, stopped, refused automatic restart and cleaned up;
the mount-start refusal case, identity isolation and Portal endpoint checks also
passed. Main CI was still running. This does not complete native claim receipts,
application readiness or managed installation acceptance.

## Concrete native candidate runtime and opaque observations

[Native candidate launch](71-native-candidate-launch.md) combines current daemon
profile, actual shared XFS quota/root inspection, immutable image verification,
native mount pins and one-shot create/start into an infrastructure-only API.
Its private live launch and running observation cannot be reconstructed through
JSON; returned snapshots omit host paths and are historical metadata rather than
current authority. The shared quota provider remains owned by its caller, with
runtime close invalidating only owned launch pins. No production route, journal
settlement, readiness, data-access/writer grant or activation is introduced.

The disposable quota workflow now compiles the driver fixture, uses a public
fixed repository digest, and runs native preparation/start/owned cleanup on a
separate private root of its newly owned XFS loopback image. Opt-in, root/host and
owned loop/mount boundaries remain; partial/relative/floating fixture input
refuses. Actual native evidence remains pending the current revision's workflow.
Local runner/CLI race regression passed (journal 69.319 s), final driver race
passed (1.642 s), Python refusal/configuration tests passed (13 tests), and Linux
vet/staticcheck plus amd64/arm64 compilation passed. Persistent preflight/receipt
binding, uncertain-effect recovery, readiness, registration, broker and full
lifecycle/release remain open. Production/beta settings, credentials, Status and
SDK freeze are untouched.
Combined native runtime source 8ae37acf67ec813ab49c1f03926c91de807c8e30 passed
quota workflow 40455635442, with native_candidate_quota_mount_start_once=true
and owned_resources_cleaned=true in checked logs. Main workflow lint separately
reported SC2129 on repeated GITHUB_ENV redirects; grouping the writes fixes that
script style without changing driver behavior. Native success is not recorded as
full main-CI, persisted receipt or application-readiness acceptance.
The corrected workflow passed local actionlint v1.7.12 with ShellCheck, and native
daemon workflow 40455635317 passed on the functional source as well.

### Native candidate journal execution and receipt binding

- Added the concrete `ExecuteCandidate` composition: verified one-shot claim,
  read-only native preflight, durable path-free binding before Docker mutation,
  current authority/data recheck, opaque running observation and atomic native
  resource/command receipt settlement. No success can be supplied through JSON.
- Added versioned string-valued native evidence/receipt metadata, refusal tests
  for changed authority/fence/data/mounts and SQL failures, and a real Linux
  journal/XFS/Docker fixture in the existing owned quota workflow. The fixture
  verifies a successful actual native ID and an injected post-effect SQL failure;
  it never authorizes production cleanup or retries uncertain candidates.
- See [execution and receipt details](72-native-candidate-journal-receipt.md).
  Native acceptance passed on functional source
  `cc799bf79c6f7be0e4ef0555509839ee75116212`, quota workflow 40457215632: both
  journal receipt/failure evidence keys and owned-resource cleanup are true.
  Local full runner/CLI race checks passed (journal 74.425 s), focused candidate
  race checks passed (9.730 s), 13 Python tests and Linux vet/staticcheck plus
  amd64/arm64 compilation passed. Historical process
  running evidence does not complete managed installation, readiness, writable
  data, broker, receipt transport/reconciliation or rollout. No schema or
  production/beta configuration changed.

### Candidate historical receipt read and replay dispatch

- Added current signed-authority historical reads with exact applied command,
  resource, preflight, original fence, data receipt/root history and pending-work
  checks. Renewed envelopes preserve the original native observation/receipt.
- Added trusted first-attempt/applied dispatch. Applied retries return that
  historical receipt without native IO; unknown work never reenters create/start.
- Extended the owned Linux native fixture to dispatch the initial process,
  reopen the journal, renew/read/replay its actual receipt and refuse unknown
  dispatch after an actual effect and injected SQL failure. Functional source
  `3845330aaf787949683f73520dd122286e71ca2f` passed quota workflow 40458125760:
  restart/renewal/replay, unknown no-restart and owned-resource cleanup keys are
  true. Full runner/CLI race checks passed (journal 77.673 s), focused race
  checks passed (15.778 s), 13 Python tests and Linux vet/staticcheck plus both
  architecture compilations passed. Synthetic rows validate journal reads only.
- See [historical read/replay details](73-candidate-receipt-read-and-replay.md).
  Receipt transport, unknown native reconciliation, readiness/writer access,
  managed registration and full application installation remain. No schema or
  production/beta configuration changed.

### OS-authenticated candidate receipt channel

- Added a candidate-specific read protocol on the existing provisioned local
  endpoint, with kernel peer identity, current signed request, exact nonce/frame
  binding and bounded canonical response checks. The server calls historical
  journal reads only; it never dispatches native execution.
- Added an opaque observation holding immutable canonical bytes to prevent
  nested preflight pointer mutation; authority and receipt getters return copies.
- Added public-vector/framing/refusal tests and an owned Linux fixture reading an
  actual shared-journal candidate receipt from a distinct controller UID, with
  unsigned/unknown/UID/group refusal and direct-journal DAC denial. Functional
  source `9bc26d7e059e254e22a3cb16241038f8cc23c07b` passed quota workflow
  40459053491: cross-UID/DAC, prior replay and owned-cleanup evidence are true;
  Linux transport race checks passed in 4.388 s. Local full runner/CLI race
  checks passed (journal 78.377 s), vector/copy and 13 Python tests passed, and
  Linux vet/staticcheck plus both architecture compilations passed.
  See [channel details](74-candidate-receipt-channel.md).
- Candidate execution transport, Portal stage settlement, native reconciliation,
  readiness/writer access and managed registration/lifecycle remain. No schema or
  production/beta configuration changed.

### Signed native candidate execution channel

- Added an explicitly enabled root/controller execution endpoint backed only by
  a configured concrete runtime/shared journal. The request carries exact managed
  descriptor bytes bound by the current signed release digest; read-only endpoints
  continue to refuse execution and no CLI listener starts automatically.
- Added separate execution magic/full-frame binding and bounded 30-second execution
  sessions after the existing authenticated header budget. Signed authority keeps
  its own expiry; uncertain effects cannot be retried under replacement identities.
- Added unit refusals and a real owned cross-UID fixture for actual first launch,
  same-receipt/native-ID replay, read, unsigned/UID/group refusal, unknown no-create
  and journal DAC denial. Functional source
  `cdaebf713498b5137a6a11907054dc1cc8ae4b50` passed quota workflow 40460414731:
  actual cross-UID start, same-native-ID replay and owned-resource cleanup keys
  are true; Linux transport race checks passed in 4.418 s. Local full runner/CLI
  race checks passed (journal 80.599 s), 13 Python tests and Linux vet/staticcheck
  plus both architecture compilations passed.
  See [execution channel details](75-candidate-execution-channel.md).
- Portal stage integration, combined provisioning, native reconciliation, readiness,
  writer/broker access and managed registration/lifecycle remain. No schema or
  production/beta configuration changed.

### Shared managed data/start endpoint

- Added one explicit root/controller constructor for data preparation and candidate
  execution with one shared journal and the exact same borrowed concrete provider
  and root pin. Narrow read/data/candidate constructors retain their capability sets.
- Added object/pin mismatch refusal before socket creation and extended the actual
  Linux fixture to prepare a fresh namespace from a separate controller process,
  inspect/replay that native evidence, then launch/read/replay a candidate through
  the same endpoint. No in-process data dispatch shortcut remains in that fixture.
- Functional source `265b9fdb00e5eb15b4900abb175eaf5c5b966e99` passed quota
  workflow 40607439801, job 122550353436: combined cross-UID data/start, exact
  provider refusal and owned-resource cleanup keys are true. Linux transport race
  checks passed in 4.810 s. Local full runner/CLI race checks passed (journal
  78.726 s), 13 Python tests and Linux vet/staticcheck plus both architecture
  compilations passed. See [shared managed endpoint](76-managed-runtime-endpoint.md).
  Portal stage delivery,
  automatic startup/staging, reconciliation, readiness/writer/broker access and
  registration/lifecycle remain. No schema or production/beta configuration changed.

### Authenticated runtime profile consumed by Portal

- Added a combined-endpoint-only configuration probe that binds actual native
  platform/sandbox/root metadata to stable current enrollment over the existing
  OS-authenticated read profile. Opaque, canonical, fresh metadata explicitly
  keeps execution_ready=false and cannot be supplied by a browser or JSON proof.
- Added Portal consumption that rechecks current admin authority/runtime binding
  before and after network IO outside SQL transactions, then supplies the observed
  target to compatibility matching. Existing managed-install refusal remains;
  compatibility is not execution, readiness or an application grant.
- Added metadata/framing/zero-proof refusals and extended the actual non-root Portal
  acceptance to observe the configured root runtime, match a sample managed target,
  retain the old refusal and continue existing installation regression checks.
  Functional source `71cadb7f17c992052977ce9a343f904d0864bda6` passed quota
  workflow 40609387623 (job 122556907621): actual Portal profile and owned-cleanup
  keys are true, execution_ready stays false, and Linux transport race passed in
  4.469 s. Local full runner/CLI race passed (journal 79.429 s), focused Portal
  race passed (2.462 s), 13 Python tests and Linux vet/staticcheck plus architecture
  compilations passed. See [profile and Portal compatibility](77-managed-runtime-profile.md).
- Managed review/data/start/probe lifecycle integration, staging/startup, native
  reconciliation and broker/registration/reference apps remain. No schema or
  production/beta configuration changed.

### Managed installation review and data preparation

- Added explicit development managed review/admission with fresh authenticated
  runtime targets, exact canonical plan/replay binding and persistent managed
  installation/runtime identity. Generic HTTP installation remains static.
- Reused current publisher/admin/lease/fence staging authority and signed native
  data preparation. Managed receipt settlement advances to `start`, retaining
  pending installation/no activation/no access; static completion stays unchanged.
- SQLite focused race regression passed (19.656 s); Linux amd64/arm64 Portal test
  compilation, vet/staticcheck and all 13 Python acceptance-script tests passed.
  PostgreSQL focused race regression passed (19.663 s). Corrected the signed
  test package to use the authorized publisher namespace and added common release
  staging regression (managed tests 2.711 s).
- Functional source `2ffa6ef7e11933863623611e5f31ae2973d92c3b` passed native
  quota workflow 40612418406 (job 122566896200): authenticated Portal profile,
  managed data-to-start and owned-cleanup keys are true. Linux transport race
  passed in 4.475 s. General CI still fails its existing x/net vulnerability gate;
  it is not all green. See [managed data details](78-managed-install-data.md).
- Signed candidate intent/start/probe, staging/startup, managed scheduling,
  reconciliation, writer/broker/registration and complete lifecycle/reference-app
  acceptance remain. No schema, production/beta or real credentials changed.

### Managed candidate startup and receipt recovery

- Added a stable candidate activation/effect intent using existing approved tables,
  validated applied data history and current reviewed runtime/admin/publisher/
  lease authority. Initial managed start signatures remain lease-bounded.
- Added concrete dispatch, opaque native-receipt settlement to `probe` and a
  receipt-only recovery path. Candidate readiness remains unobserved and no app
  access is granted by a running process.
- The native runner provisions fixed communication directories after durable
  claiming using protected descriptor-relative paths, exact ownership and no
  symlink repair. Added directory refusals and real root/non-root Portal start,
  failed-settlement recovery, exact native replay and owned cleanup acceptance.
- Local focused SQLite race passed (20.119 s), PostgreSQL race passed (15.318 s),
  Linux amd64/arm64 compilation, vet/staticcheck and 13 Python tests passed.
  Runner/CLI race also passed (journal 81.723 s).
- Functional source `441a26083fee4c7e30999015ece5bb024bf2c8c5` passed native
  quota workflow 40613886646 (job 122572074822): actual non-root Portal candidate
  start-to-probe, data-to-start and owned cleanup keys are true. Linux transport/
  driver race passed in 4.540/1.492 s. General CI still fails the existing x/net
  vulnerability gate. See [managed candidate details](79-managed-candidate-start.md).
- Actual readiness/writer/broker/registration, image fetching, managed scheduling,
  bounded socket storage, reconciliation and full lifecycle/reference apps remain.
  No schema, production/beta, module versions or real credentials changed.

### Fresh managed backend readiness

- Added the provisional bounded credential-free HTTP-over-Unix readiness contract,
  actual container/data/mount/policy checks before and after the app response,
  private socket owner/inode/peer verification and bounded no-redirect probing.
- Added current journal authority/history checks around native IO, a separate
  nonce-bound root readiness channel and opaque observation, plus Portal signing
  and transactional candidate evidence/probe-to-activate settlement. It does not
  activate the app or create app-use/writer/broker/registration grants.
- Added a source-built actual backend and an owned localhost-only test registry
  for native acceptance. The fixture must pass the prior startup recovery checks
  and observe readiness from the actual running app before emitting its marker.
- Local SQLite race regression passed (17.293 s), PostgreSQL candidate/data race
  passed (13.184 s), runner/CLI race passed (journal 80.724 s), Linux compilation,
  vet/staticcheck, shellcheck/actionlint and 14 Python tests passed.
- Functional source `f6a617e981a013c84da45e2f4d2f0c0284ed41ab` passed quota
  workflow 40615945463 (job 122578517361): actual managed backend readiness,
  candidate start and owned cleanup keys are true; exact registry cleanup passed.
  Linux transport/driver race passed in 4.551/1.546 s, including private framing
  and Unix HTTP checks. General CI retains its existing x/net vulnerability gate
  failure. See [managed readiness details](80-managed-readiness.md).
- Managed activation/registration/writer/broker, image fetching, bounded socket
  storage, scheduling/startup, reconciliation and full lifecycle/reference apps
  remain. No schema, production/beta, module versions or real credentials changed.

### Managed backend instance identity and registration preparation

- Bound the existing backend handshake/request protocol's opaque instance to
  deployment/epoch/installation/generation/policy/release. Derived a separate
  per-instance backend key from the existing sealed installation seed; no runner
  key or stable identity seed enters the protected bootstrap document.
- Added a redacted, non-serializable live registration handle with explicit bounded
  document export/parser and shared close/clear semantics. Shared protocol tests
  prove handshake/request interoperability, tuple separation and replay/refusal.
- Added current private Portal preparation after exact recent root readiness,
  current lease/admin/publisher/runtime/data/start validation. It creates no file,
  delivery effect, app-use grant or actual backend registration. Native fixture
  now requires stable material preparation after actual backend readiness.
- Focused SQLite race passed (6.731 s), pure backend protocol/registration tests
  passed (1.562 s); PostgreSQL candidate/data race passed (13.277 s), Linux
  amd64/arm64 compilation, vet/staticcheck, actionlint and 14 Python tests passed.
- Functional source `8dfd0a05f7f5e1c9ab20491c859fe64eb7afb9aa` passed quota
  workflow 40617663109 (job 122583845402): actual ready-backend material and owned
  cleanup keys are true; exact registry cleanup passed. Linux transport/driver
  race passed in 4.654/1.621 s. General CI retains its existing x/net vulnerability
  gate failure. See [managed backend identity](81-managed-backend-identity.md).
- Signed root delivery/boot handshake, request forwarding/current viewer authority,
  broker/writer and activation plus remaining full lifecycle/reference apps remain.
  No schema, production/beta, module versions or real credentials changed.

### Backend registration delivery authorization and intent

- Added a separate signed registration command bound to the existing candidate
  effect and private document digest. Private document validation checks the
  exact managed tuple and descriptor without storing a key in journal commands.
- Added transactional prepared intent admission against matching applied source
  history and present/running native resource metadata. Exact replay is accepted;
  source changes, uncertain history and effect-slot conflicts are refused.
- Added native fixture admission/replay after actual ready-backend material
  preparation. This is sample authorization by the root test controller, not
  automatic Portal key delivery or completed backend registration.
- Local focused control/journal race and full runner/CLI race passed (journal
  79.356 s); Linux amd64/arm64 compilation, vet/staticcheck and 14 Python tests
  passed. Functional source `c011d25e58d0ab28ec745f3375a3d79cdc66189f`
  passed native quota workflow 40619597258 (job 122590125633): actual candidate
  registration intent, backend readiness and owned cleanup keys are true; exact
  registry cleanup passed. Linux transport/driver race passed in 4.482/1.481 s.
  General CI still fails the existing x/net vulnerability gate, skipping the
  subsequent non-race Test step.
- See [registration command details](82-backend-registration-command.md).
  Protected file delivery, boot handshake, forwarding/current viewer authority,
  broker/writer and activation plus the remaining platform scope still remain.
  No schema, production/beta, real credentials or module versions changed.

### Protected backend registration file delivery

- Added durable unknown claims before native publication, exact current source/
  data/resource/fence checks around IO, opaque observation settlement and
  historical delivery receipts. Unknown retries only observe an existing file.
- Added anonymous atomic no-overwrite publication into the candidate's fixed
  read-only broker mount. Exact claim xattr, bytes, mode/owner/link/inode checks
  prevent accepting a preexisting file for a new claim. No private document is
  stored in the journal or a named temporary file.
- Extended Linux acceptance to carry Portal material through an inherited private
  fixture pipe, fail SQL settlement after publication, recover without replacing
  the file, and require historical replay. Production RPC and boot handshake are
  still separate remaining work.
- Local focused journal race, full runner/CLI race (journal 85.947 s), focused
  Portal regressions (15.951 s), Linux amd64/arm64 compilation, vet/staticcheck
  and 14 Python tests passed. Functional source
  `fbbaadb617726438f74aed016894bc4f23122377` passed quota workflow 40620897099
  (job 122594632732): actual protected delivery recovery, backend readiness and
  owned cleanup keys are true; exact registry cleanup passed. Linux transport/
  driver race passed in 4.630/1.602 s; the root fixture executes atomic no-overwrite
  publication checks. Daemon probe 40620896826 also passed. General CI passed all
  app race shards/aggregate, web checks/test shards and Docker smoke, but retains
  the existing x/net vulnerability gate failure and skipped non-race Test step.
- See [protected registration delivery](83-backend-registration-delivery.md).
  Full managed activation, forwarding, broker/writer and remaining lifecycle/
  reference apps remain. No schema, production/beta, real credentials or module
  versions changed.

### Protected controller registration RPC

- Added a nonce/request-digest-bound private-document channel over the existing
  protected root/non-root Unix endpoint. Public authority and size checks precede
  private reads; only a concrete candidate provider can invoke the dispatcher.
- Added opaque authenticated historical delivery observations and exact public
  receipt/command matching. Private buffers are cleared; ordinary listeners
  cannot dispatch registration or mint root provenance.
- Replaced the native fixture's key-bearing pipe with real UID/GID-41001 RPC.
  Parent coordination now contains public intent/events only. The fixture still
  requires failed-settlement recovery, same inode and historical replay.
- Linux framing tests and pure journal regressions cover malformed replies,
  mismatched effects/documents, refusal, historical metadata and provenance.
  Full local runner/CLI race passed (journal 84.888 s), focused journal race
  passed (5.657 s); Linux amd64/arm64 compilation, vet/staticcheck and 14 Python
  tests passed. Functional source `91ce4859c8dbe9d2a13ebb98c581f49bfa388f58`
  passed native quota workflow 40622256803 (job 122598829955): actual protected
  RPC recovery, same-file delivery recovery and owned cleanup keys are true;
  exact registry cleanup passed. Linux transport/driver race passed in
  4.530/1.438 s. General CI retains the existing x/net vulnerability gate failure,
  skipping the subsequent non-race Test step.
- See [registration channel](84-backend-registration-channel.md). Portal durable
  effect/scheduler integration, boot handshake, forwarding, broker/writer,
  activation and the remaining full platform scope are still outstanding.
  No schema, production/beta, real credentials or module versions changed.

### Portal durable backend registration delivery

- Added the third initial managed effect in the existing activate slot. Portal
  derives material from its sealed installation seed, signs with its sealed
  runtime controller key and retains the same effect/digest on envelope renewal.
- Added fresh first-intent readiness and exact historical source checks, a narrow
  old-health reconciliation path for already persisted delivery, root endpoint/
  observation validation and transactional public receipt settlement. Operation
  row revision advances; activation/grants remain unchanged.
- Native fixture now uses actual Portal preparation/signing instead of sample
  authorization. It requires both root settlement failure and Portal late audit
  failure recovery, same inode, exact receipt, stale-lease refusal and replay.
- SQLite/PostgreSQL delivery race passed (8.970 s together); broader SQLite
  regressions passed (21.771 s), PostgreSQL regressions passed (20.955 s), runner/
  CLI race passed (journal 84.606 s). Linux compilation, vet/staticcheck,
  actionlint and 14 Python tests passed. Functional source
  `2070308728c1f2512b6ff3695a8975b7ba7522a5` passed quota workflow 40623890666
  (job 122604109031): actual Portal delivery effect settlement, RPC recovery and
  owned cleanup keys are true; exact registry cleanup passed. Linux transport/
  driver race passed in 4.816/1.604 s. General CI passed all app race shards/
  aggregate, web checks/tests and Docker smoke, but retains the existing x/net
  vulnerability gate failure and skipped non-race Test step.
- See [Portal delivery effect](85-portal-backend-delivery-effect.md). Boot
  handshake, forwarding, broker/writer, managed activation/scheduling and the
  remaining full lifecycle/reference apps remain. No schema, production/beta,
  real credentials or module versions changed.

### Actual managed backend boot handshake

- Added strict bounded challenge/answer bodies, read-only native handshake under
  exact delivered-file/container/socket evidence, current journal history checks
  and a nonce-bound protected controller exchange. Keys are not retransmitted.
- Added Portal's independent shared-protocol MAC/challenge/boot verification and
  post-IO authority/material revalidation. Boot evidence is live/non-serializable;
  it creates no activation or viewer/provider/writer grant.
- Disposable backend reads the protected file and answers with one random boot
  identity per process. Native acceptance requires actual repeated boot proof,
  wrong-key refusal, different-challenge replay refusal and unchanged SQL state.
- Local appplatform/runner/CLI race passed (journal 88.439 s); SQLite regressions
  passed (20.251 s), PostgreSQL regressions passed (16.922 s); Linux compilation,
  vet/staticcheck, actionlint and 14 Python tests passed. Fixed a synthetic
  fixture clock boundary and passed 30 repeat cases. Functional source
  `107d944e7296d1bc4336ead25b6f1f4d671fbb3a` passed quota workflow 40632718683
  (job 122632590592): actual backend boot, durable delivery and owned cleanup
  keys are true; exact registry cleanup passed. Linux transport/driver race
  passed in 4.551/1.500 s. General CI retains its existing x/net vulnerability
  gate failure and skipped following non-race Test step.
- See [managed boot handshake](86-managed-backend-boot-handshake.md). Forwarding,
  broker/writer, activation/scheduling and remaining full lifecycle/reference apps
  remain. No schema, production/beta, real credentials or module versions changed.

### Independently signed native backend request relay

- Added per-request controller signature binding the exact registration/boot,
  method/path/body and assertion. Registration signatures cannot authorize a
  request; backend MAC/nonce verification remains independently mandatory.
- Added bounded fixed-socket HTTP forwarding under the shared delivered-file/
  native-policy boundary, exact journal checks around IO and protected nonce-bound
  response framing. No retries, arbitrary destinations, ambient headers, durable
  request effects or user/provider/writer grants are created.
- Native acceptance uses explicit sample control authorization and synthetic
  viewer claims after real boot. It requires correct viewer binding, one write,
  replay/body/forged-MAC/old-boot refusal and counter verification. This does not
  enable browser access to pending applications.
- Local runner/CLI race passed (journal 85.091 s), final contract/control race
  passed (1.749/1.876 s), Portal SQLite regressions passed (28.615 s); Linux
  compilation, vet/staticcheck, actionlint and 14 Python tests passed. Native
  control/appplatform race gates now run directly and trigger on control edits.
  Functional source `2224c1898da5b1f0afb2b7e840bc49fa35f94d21` passed quota
  workflow 40635903047 (job 122642689300): actual signed relay, backend boot and
  owned cleanup keys are true; exact registry cleanup passed. Forged-MAC refusal
  uses an unseen nonce. Linux control/appplatform/transport/driver race passed in
  1.342/1.476/5.011/1.827 s. General CI retains the existing x/net vulnerability
  gate failure and skipped following non-race Test step. See [signed request relay](87-signed-backend-request-relay.md).
  Current session/OU/grant gating and stable identities, broker/writer, activation/
  scheduling and remaining full lifecycle/reference apps remain. No schema,
  production/beta, real credentials or module versions changed.

- Added stable app-scoped viewer identity and a private current-viewer read
  adapter using the existing full launch transaction. Installation ID/identity
  seed define the namespace; backend generation/key/boot and audience-policy
  changes do not rotate subjects/OUs. Live roles/sessions and actual OU ancestry
  remain current; recreated principals get different identities. Incomplete OU
  creation identities now fail closed rather than being silently omitted.
  Reads use the durable keyring in the same transaction without lazy creation,
  previous-key rewrap or cached-key fallback, and return no facts on final gate
  failure. No managed admission or browser request endpoint is enabled.
  Related SQLite race tests passed (appplatform 1.223s/app 21.259s), PostgreSQL
  race tests passed (14.467s), exact-64-OU contract race passed (1.281s), and the
  broader platform regression passed (appplatform 1.244s/app 8.382s). Vet,
  Linux/default staticcheck and actionlint passed; the isolated PG cluster was
  stopped. See [stable viewer identity](88-stable-viewer-identity.md) for exact
  scope and next integration. Active managed authority reconstruction, broker/
  writer, bounded socket storage, activation/scheduling and full lifecycle/SDK/
  reference-app acceptance remain. Production, beta, dependencies and schemas
  are unchanged.

- Connected a private current-viewer managed request path to the native relay.
  A completed operation selects immutable source history through an explicit
  source tuple, without manufacturing or renewing a worker lease. Admission
  validates current sessions, actual OU/user audience, current policy/trust and
  runtime plus the exact three applied source effects. It keeps mutable audience
  policy separate from the original bootstrap policy/key/document. The host
  obtains a fresh actual boot challenge, repeats current admission before signing,
  forwards once, and repeats admission before releasing the buffered response.
  Post-dispatch failure is outcome-uncertain with no automatic retry. Public
  managed launch/activation remain closed. Related SQLite race passed (20.211s),
  PostgreSQL race passed (13.384s), broader local platform regression passed
  (8.125s); Linux amd64 compilation, vet/default Linux staticcheck and 14 Python
  tests passed. The dedicated PG cluster is stopped. Native acceptance now
  requires actual current-viewer writes, audience-only policy change, post-IO
  revocation/result suppression and a physical execution counter; its complete/
  active Portal rows are explicitly seeded, not a normal activator. See
  [current managed viewer requests](89-current-managed-viewer-requests.md).
  Normal activation/root acknowledgement lifetime, broker/writer, bounded socket
  storage, full lifecycle, SDK/reference-app and production acceptance remain.
  No schema, dependency, production/beta or real-credential changes.

- Functional source `a23114a9f4dc5d6eb6cafbad77c0a6980b0bedee` passed native
  workflow 40640341083/job 122656764332: current-viewer and independently signed
  request relay verification plus owned-resource/registry cleanup succeeded.
  Linux control/appplatform/transport/driver race passed in
  1.430/1.675/5.420/2.025s. The first native run correctly failed a missing child
  evidence forwarding marker; parent and outer probe now both require it. The
  final envelope binding check also passed local request race (2.685s), and
  Linux arm64 compilation passed. General workflow 40640341091 retains the
  existing x/net GO-2026-6866/6860 vulnerability failure, not an all-green release.
  The real native fixture's write is an in-memory execution counter; filesystem
  writer authorization, normal managed activation and remaining release gates
  are not claimed complete.

- Added independently bounded tmpfs storage for the private HTTP socket directory:
  16 MiB data blocks, 1024 inodes, nodev/nosuid/noexec, private propagation and
  exact trusted base/mount ownership records. Candidate provisioning, all mount
  pin checks and the Docker policy label enforce this boundary; existing plain
  or foreign directories are refused without repair. Explicit quiescent release
  requires no local pins and no Docker mount references, then uses ordinary
  unmount and removes only the matching empty owned underlay. Native fixtures
  now exercise this lifecycle and require actual unprivileged byte/inode refusal,
  trusted-marker protection, limit/identity drift rejection, live-container
  cleanup exclusion and owned teardown. Local driver/transport/journal race
  passed (1.663s/cache hit/85.263s), platform regression passed (8.097s), Linux
  amd64 compilation and vet/default Linux staticcheck passed. These checks do
  not establish native enforcement; that remains pending its own CI evidence.
  See [bounded HTTP storage](90-bounded-http-storage.md). Normal lifecycle,
  broker/writer, activation, SDK and full acceptance remain. No production/beta,
  schema, dependency/toolchain or credential changes.

- Bounded IPC functional source `7cc47936761fabc2d733704e8cdb040f4caec655`
  passed daemon workflow 40664832166/job 122736587767 and quota workflow
  40664832058/job 122736587721. Actual UID 65532 byte/inode exhaustion,
  trusted-marker protection, capacity/identity drift refusal, live Docker
  reference exclusion and complete owned-resource/registry cleanup succeeded.
  Existing native current-viewer/relay checks remain true. Linux control/
  appplatform/transport/driver race passed in 1.323/1.450/5.223/1.894s; arm64
  compilation passed. The initial array-decoder/probe-execution and fixture
  shutdown-order failures were corrected without relaxing ownership, limit or
  unsafe-endpoint checks. General workflow 40664832078 passed separate app
  race/web/Docker jobs but retains the existing x/net vulnerability gate failure.
  The overall application framework, normal lifecycle, broker/writer, activation
  and SDK/reference-app acceptance remain incomplete.

- Added an explicit frozen Moby-based seccomp profile to the closed candidate
  policy, narrowing unrestricted ioctl access to five exact terminal/socket
  requests while preserving the other upstream rules. Container observation
  refuses missing/unconfined/broadened options. The actual ready fixture must
  verify entrypoint identity, NoNewPrivs, zero effective capabilities, seccomp
  mode, filesystem ioctl refusal and io_uring refusal before health is served;
  parent/native probes require this evidence. Driver/transport local race passed
  (2.244s/1.738s), and Linux amd64/arm64 fixture builds passed. Native acceptance
  remains pending. See [candidate syscall boundary](91-candidate-seccomp.md).
  Writable data, broker/writer authority, normal activation/lifecycle, SDK and
  overall acceptance remain incomplete. No production/beta, schema, dependency,
  toolchain or credential changes.

- Candidate syscall functional source `f494b754103eb98708ddaf45065331ea95165b37`
  passed daemon workflow 40668996531/job 122750687854 and quota workflow
  40668996631/job 122750688073. Actual entrypoint filesystem/compat ioctl refusal,
  the five admitted terminal/socket requests, NoNewPrivs/capability checks,
  io_uring refusal and existing readiness/registration/boot/current-viewer/owned
  teardown all passed. The initial policy omitted the exact TIOCSPGRP request
  that container init needs on non-TTY stdin; adding that request fixed startup
  without opening filesystem setters. Linux control/appplatform/transport/driver
  race passed in 1.440/1.607/5.384/2.928s, local final driver/transport race in
  2.091/1.568s, and final arm64 compilation and Linux vet/staticcheck passed.
  General CI passed all app race shards/aggregate and web/Docker/workflow checks,
  but retains the existing x/net vulnerability gate failure. This closes
  this runtime boundary only; writer/broker, automatic activation/lifecycle,
  SDK/reference apps and complete P00–P14 acceptance remain outstanding.

- Added a native one-way write-access permission primitive that keeps namespace
  UID 0, fixes GID 65532/mode 02770, verifies exact allocation/root pins and quota
  inheritance, and refuses partial/already writable states without repair. The
  sealed observer now also verifies GID 0. New disposable native tests cover
  concurrent transition attempts, refusal/reopen/drift and an actual filtered
  app attempting filesystem attribute changes and byte/inode quota exhaustion.
  The ordinary candidate route must still reject the changed namespace and keep
  data read-only. Parent probes require both native evidence markers. See
  [data write-access primitive](92-data-write-access.md). Native acceptance is
  pending. This is not writer authorization or revocation; persistent admission,
  native writer exclusion, normal activation, broker and complete platform
  acceptance remain outstanding. No schema, production/beta, dependency/toolchain
  or credential change.

- Data write-access functional source
  `095983669d4ba177663a50e33832bef7f338ad5d` passed quota workflow
  40673687092/job 122766559793 and daemon workflow 40673687027/job 122766559138.
  Both `native_go_data_write_access_transition` and
  `native_filtered_data_writer_verified` are true: physical transition/reopen/
  refusal, actual app-owned attribute attacks, byte/inode refusal, independent
  host project/inheritance checks, healthy sibling and owned teardown passed.
  Existing entrypoint/current-viewer checks remain true. Linux control/
  appplatform/transport/driver race passed in 1.455/1.638/5.348/2.889s; local
  platform regression passed in 8.501s, dual-architecture compilation, Linux
  vet/staticcheck and 14 Python probe tests passed. General CI passed app race
  shards/aggregate and web/Docker/workflow jobs, retaining the existing x/net
  vulnerability gate failure. This closes the physical primitive/trial, not
  persistent writer admission/exclusion/revocation, normal activation/lifecycle,
  SDK/reference apps or complete P00–P14 acceptance.

- Added a separately signed initial writer intent bound to the exact applied
  data receipt and future resource, persisted in existing prepare_data/2 activate
  records. Current trust/source/floor/history checks refuse takeover, rebound
  receipts/resources and recorded old containers. Durable prepared->unknown
  claims produce nonserializable one-shot owner handles, bounded to signed
  expiry; post-IO rechecks, restart/copy refusal and Close serialization preserve
  uncertainty. Callback success grants no permission or applied receipt. Real
  SQLite/crypto tests passed; the disposable native test now requires the actual
  XFS source and unchanged sealed data through admission/claim. See
  [writer intent and claim](93-writer-intent-and-claim.md). Local full control/
  journal/transport race passed (1.880/88.397/1.318s), focused writer race passed
  in 10.621s, Linux dual-architecture compilation, vet/staticcheck and 14 Python
  probe tests passed. Native acceptance remains pending. Actual writer exclusion,
  physical dispatch/settlement, writable candidate/recovery, broker/activation,
  SDK/reference apps and full acceptance remain. No schema or production/beta,
  dependency/toolchain or credential changes.

- Writer intent functional source `a612799f0c064e0d024684632b1fbe896fd3fa4a`
  passed quota workflow 40679110373/job 122784962851 and daemon workflow
  40679110347/job 122784962964. Actual XFS source admission/claim/one-shot
  verification passed with sealed data unchanged and unknown intent preserved;
  `native_writer_intent_and_claim_verified` is true. Existing physical writer
  trial/current-viewer/owned-cleanup flags remain true. Linux control/appplatform/
  transport/driver race passed in 1.542/1.582/5.365/2.946s; the full daemon runner
  suite passed (journal 143.827s), including deadline and Close checks. Local
  platform regression passed in 8.208s. General app race aggregate, web/Docker/
  workflow checks passed; the existing x/net vulnerability gate still fails.
  This closes intent/claim guarding only. Native process exclusion, physical
  dispatch/opaque settlement, writable candidate/recovery, broker/activation and
  complete SDK/reference-app/P00–P14 acceptance remain incomplete.

- Connected the initial signed one-shot writer claim to a concrete native driver
  and atomic historical receipt settlement. Actual stopped/live Docker mount
  references and local launch pins exclude permission changes; protected path,
  allocation/image/platform/seccomp identities are rechecked around native IO.
  Success advances volume revision 2->3 and writer unknown->applied together,
  preserving original ownership. A current-authority historical reader supports
  renewal without rewriting the original grant; it does not permit candidate
  start. Added actual XFS/journal success and post-permission SQLite-failure
  acceptance, plus the existing filtered writer trial through the native adapter.
  See [writer execution and receipts](94-writer-execution-and-receipts.md).
  Native acceptance is pending. Writable candidate/recovery/fencing, managed
  activation, broker, SDK/reference apps and full platform acceptance remain.
  No schema, production, dependency/toolchain, credential or beta changes.

- Writer execution functional source `6713550ddc88d20d415233261b7e9d16c1650167`
  passed quota workflow 40684569991/job 122803511112 and daemon workflow
  40684569825/job 122803510721. Actual stopped-container exclusion, native
  journal writer settlement and post-permission commit-failure preservation are
  verified; filtered quota/current-viewer/owned-cleanup evidence remains true.
  Linux control/appplatform/transport/driver race passed in
  1.368/1.393/4.794/2.390s; full daemon journal race passed in 105.228s.
  Local full runner race, focused writer race (14.739s), platform regression
  (10.757s), Linux dual-architecture compilation, vet/staticcheck and 14 Python
  probe tests passed. General workflow 40684570082 passed all app race shards
  and aggregate, web/Docker/workflow checks; its Go job 122803511906 retains the
  existing x/net vulnerability gate failure after passing staticcheck.
  This closes initial physical dispatch and historical receipt settlement only;
  unknown writer recovery, writable candidate/fencing, managed activation,
  broker, SDK/reference apps and full P00–P14 acceptance remain incomplete.

- Added observation-only recovery for an expired unknown initial writer effect.
  A fresh current signed reference authorizes only a concrete native observer;
  it cannot repeat permissions, reconstruct claims or repair partial data.
  Recovery uses separate opaque proof and a distinct receipt format/window,
  preserves the original marker, rechecks authority/source after native IO and
  atomically settles both journal records. Current-key historical reads preserve
  recovery evidence. Extended disposable tests cover restart/expiry, partial
  permission refusal and retry after an injected recovery commit failure with
  unchanged native inode/mode/group/ctime. See
  [writer observation recovery](95-writer-observation-recovery.md). Native
  acceptance remains pending; writable candidate/start/fencing, managed activation,
  broker, SDK/reference apps and full platform acceptance are still incomplete.
  No new schema, production, real credentials or server beta changes.

- Writer recovery functional source `a6f5ab4f986f5082d02117f49ccb70c6001dda23`
  passed quota workflow 40686824124/job 122811213128 and daemon workflow
  40686824149/job 122811213023. Actual expired-claim recovery, partial permission
  refusal and unchanged namespace inode/mode/group/ctime across recovery failure
  and read-only retry are verified; all four new flags and existing filtered
  writer/current-viewer/owned-cleanup evidence are true. Linux control/appplatform/
  transport/driver race passed in 1.585/1.608/5.422/2.973s; full daemon journal
  race passed in 154.869s. The recovery test reopens Store/runtime objects inside
  its process, not a daemon kill/relaunch; full process lifecycle acceptance is
  still outstanding. Local full runner race, expanded recovery race, platform
  regression, dual-architecture compilation, vet/staticcheck and Python14 passed.
  General workflow 40686824170 passed all app race shards/aggregate, web/Docker/
  workflow checks, retaining the existing x/net vulnerability gate failure in
  Go job 122811213418 after staticcheck passed. Initial recovery is implemented;
  writable candidate/start/fencing, managed activation, broker, SDK/shared shell,
  reference apps and full P00–P14 acceptance remain incomplete.

- Added separately signed writable-candidate intent bound to the exact completed
  writer receipt and its future container identity. Current nested authority,
  original data ownership, revision-3 volume/time/policy evidence, durable floors
  and exact three-command/no-container history are checked before prepared
  admission and current metadata reads. Original execution and recovered writer
  receipts are supported without relaxing legacy sealed/pre-start readers.
  No execution handle, native container or readiness is created. Added fixed
  independently reproduced digest/domain/renewal tests, SQLite drift/reopen/
  current-key tests and actual-source disposable acceptance after both native
  writer paths. See [writable candidate intent](96-writable-candidate-intent.md).
  Native acceptance is pending. Reservation/one-shot execution, writable native
  start/fencing, managed activation, broker, SDK/reference apps and full platform
  acceptance remain. No schema, production, credential or beta changes.

- Writable intent functional source `0699ce7357931adeca0c24e720309357701c74b9`
  passed quota workflow 40690209401/job 122822784136 and daemon workflow
  40690209510/job 122822950227. Actual direct/recovered writer sources admit
  exact prepared start intent; wrong hashes and legacy execution paths refuse,
  and source bytes remain unchanged with no container reservation. The new
  `native_writable_candidate_intent_actual_sources_verified` flag and existing
  writer/recovery/filtered-quota/current-viewer/owned-cleanup evidence are true.
  Linux control/appplatform/transport/driver race passed in
  1.733/1.654/5.394/3.004s; full daemon journal race passed in 166.235s. Added
  chronology checks reject writer creation before data completion and start
  creation before writer completion; targeted race passed in 5.326s. Local full
  runner race, platform regression, dual-architecture compilation, vet/staticcheck
  and Python14 passed. General workflow 40690209482 passed all app race shards/
  aggregate, web/Docker/workflow checks, retaining the existing x/net gate failure
  after staticcheck in Go job 122822991559. This closes initial intent/source
  binding only, not reservation, writable native execution, readiness/activation,
  SDK/reference apps or full P00–P14 acceptance. Production and beta unchanged.

- Added exact writable candidate resource reservation, current prepared-resource
  reads and process-local one-shot claims. Stable ownership binds original data/
  writer receipt bytes and source tuple while allowing current-key prepared
  renewal; higher fences and unknown restart/copy/JSON takeover refuse. Only the
  independently validated pending resource extends the strict source history.
  Unknown claim state commits before invocation, current authority/source/resource
  is rechecked after IO, signed expiry/cancellation is bounded and Close waits
  before releasing journal ownership. Callback success creates no running/ready
  evidence. Added real SQLite/crypto concurrency/drift/renewal/deadline/Close
  tests and actual XFS-source read-only invocation acceptance for both writer
  receipt kinds. See [writable reservation and claims](97-writable-candidate-reservation-and-claims.md).
  Native acceptance is pending. Concrete writable native preflight/start/opaque
  settlement, readiness/registration/activation, broker, SDK/reference apps and
  full platform acceptance remain incomplete. No schema or production/beta changes.

- Writable reservation/claim functional source
  `62eb67278c259b39359ea6867b6d0765bc044890` passed quota workflow
  40694331237/job 122836964588 and daemon workflow 40694331553/job 122836965353.
  Both actual writer receipt kinds pass exact pending reservation and one-shot
  invocation with unchanged XFS allocation; command remains unknown and resource
  pending revision 1 without native observation. The new actual-source claim flag
  and existing intent/writer/recovery/filtered-quota/current-viewer/owned-cleanup
  evidence are true. Linux control/appplatform/transport/driver race passed in
  1.448/1.396/4.854/2.461s; full daemon journal race passed in 179.838s. Local full
  runner race, expanded claim/renewal/Close tests, platform regression, dual-arch
  compilation, vet/staticcheck and Python14 passed. General workflow 40694331560
  passed all app race shards/aggregate, web/Docker/workflow checks, retaining
  the existing x/net vulnerability gate failure after staticcheck in Go job
  122836966930. Concrete writable native preflight/start/opaque settlement,
  readiness/registration/activation, SDK/reference apps and complete P00–P14
  acceptance remain incomplete. No production or beta changes.

- Added a separately typed concrete writable candidate driver: closed RW-data
  builder with exact claim/source labels, protected writable data and IPC pins,
  immutable image/policy checks, local/actual stopped/running reference exclusion,
  fresh pre-create checks and one-shot native start returning opaque process
  evidence. Legacy sealed builder/checks and old handle entry remain independent.
  Replaced the disposable writer trial's JSON/Env mutation with the real typed
  provision/preflight/start, retaining all actual filtered quota/attribute/child
  project checks and owned cleanup. See [writable native driver](98-writable-native-driver.md).
  Native acceptance is pending. Journal preflight binding/process settlement and
  current running history/recovery, readiness/registration/activation, broker,
  SDK/reference apps and complete platform acceptance remain incomplete. No
  production, schema, credentials, dependency/toolchain or beta changes.

- Writable native driver functional source
  `32101df7fc7414212cbadf302ef9b178d76c95b2` passed quota workflow
  40757095906/job 123058818615 and daemon workflow 40757095659/job 123058818067.
  Actual typed preflight/start and opaque process evidence passed, including a
  late stopped reference, running-reference and repeated-start refusal. The real
  unprivileged filtered writer/attribute/byte/inode quota and child-project checks
  pass without test JSON or Env mutation, with owned resource/IPC/registry cleanup.
  `native_writable_candidate_driver_start_verified` and existing source-claim/
  writer/recovery/current-viewer flags are true. Linux control/appplatform/
  transport/driver race passed in 1.757/1.628/5.424/3.027s; full daemon journal
  race passed in 178.210s. Local full runner race, platform regression, dual-arch
  driver/fixture builds, vet/staticcheck and Python14 passed. General workflow
  40757095413 passed all app race shards/aggregate, web/Docker/workflow checks,
  retaining the existing x/net vulnerability gate failure after staticcheck in
  Go job 123058818707. This proves the concrete adapter under synthetic trusted
  fixture bindings, not current journal authorization or a full app installation.
  Journal preflight binding/process settlement/history/recovery, activation,
  SDK/reference apps and full acceptance remain. Production and beta unchanged.

- Connected current writable claims to concrete native execution: opaque live
  preflight is durably bound before create/start, current authorization is
  rechecked immediately before IO, guarded postflight preserves immutable
  source/claim/resource facts, and opaque actual process evidence atomically
  settles running resource plus applied receipt. Added current historical reads
  preserving original claim/key/times across current-key renewal, with strict
  exact running-resource history. Added disposable actual process success,
  post-start receipt failure and preflight-commit-before-process cases. See
  [writable running receipts](99-writable-running-receipts.md). Native acceptance
  is pending. Unknown-start observation recovery, readiness/registration/
  activation, broker, SDK/reference apps and full acceptance remain incomplete.
  No schema, production, credential, dependency/toolchain or beta changes.

- Writable running receipt source `deaa01f19666e769dfba983d48bd956b5410d9d3`
  passed quota workflow 40761567921/job 123074574861 and daemon workflow
  40761567968/job 123074574054. Actual native preflight-before-create, successful
  process settlement and post-start atomic commit failure preservation all
  passed, with exact owned cleanup and existing source/writer/viewer checks.
  Full Linux journal race passed in 184.912s; local full runner race and platform
  regression, dual-architecture journal builds, vet/staticcheck and Python14
  passed. General workflow 40761567937 passed all app race shards, web and Docker
  checks; Go job 123074574375 retained the existing x/net vulnerability gate
  failure after staticcheck. Historical owner reopen/reference renewal passed;
  full daemon crash/relaunch and unknown-process observation recovery remain
  unfinished, as do writable readiness/registration/activation, broker, SDK,
  reference apps and full release acceptance. No production or beta changes.

- Added observation-only writable start recovery under fresh signed authority
  after original claim expiry. The separate opaque native path rebuilds/pins
  exact original preflight, requires one full-policy-matching running container,
  repeats all-container reference checks and never provisions/starts/repairs.
  Current settlement preserves original claim while atomically recording a
  separate-window recovered process receipt/resource. Added strict historical
  parser/state/refusal tests and actual owner reopen/metadata-failure retry/
  unchanged native start identity acceptance. See
  [writable start recovery](100-writable-start-recovery.md). Native acceptance
  is pending; readiness/registration/activation, SDK/reference apps, lifecycle
  and complete release acceptance remain incomplete. No production/beta changes.

- Writable start recovery source `f836718daa5107c7fe692d7442ab0f38a5d3c221`
  passed quota workflow 40767469240/job 123095373863 and daemon workflow
  40767469270/job 123095504935. Actual post-start unknown recovery after owner
  reopen/expiry, metadata-failure atomic rollback and observation retry all
  passed with unchanged native ID/creation/start/restart identity and original
  claim/preflight. Full Linux journal race passed in 164.928s; local full runner
  race journal 159.193s, expanded writable race, platform regression, dual-arch
  builds, vet/staticcheck and Python14 passed. General workflow 40767469054
  passed app race shards, web and Docker checks, retaining existing x/net gate
  failures in Go job 123095584809. The inventory fixture's overly long temporary
  socket path was shortened without changing production limits. This is owner
  object reopen, not full daemon crash/relaunch. Writable readiness/registration/
  activation, broker, SDK/reference apps, physical lifecycle and full P00–P14
  acceptance remain incomplete. Production, Status adoption and beta unchanged.

- Added current writable backend readiness: strict applied source preparation,
  original/recovered receipt binding, opaque actual native/private socket probe
  under repeated full policy/quota/reference checks, owner gate and current
  postflight before transient result. No process/storage/journal mutation or
  activation. Added parser and postflight drift/refusal tests plus actual normal/
  recovered readiness, unknown/stopped refusal, mismatched process/schema and
  exact durable-row preservation checks. See
  [writable readiness](101-writable-readiness.md). Native acceptance pending;
  automatic registration/handshake/viewer, activation/worker, SDK/reference apps,
  lifecycle and full acceptance remain. Production and beta unchanged.

- Writable readiness source `57788e7976e5739e16505b11f80b63f38e6141ba` passed
  quota workflow 40769452465/job 123102370045 and daemon workflow
  40769452355/job 123102370043. Actual normal/recovered private-socket readiness,
  exact durable deployment/command/resource preservation, unknown/stopped refusal
  and process/schema mismatch checks passed alongside writer/start/recovery and
  owned cleanup. Full Linux journal race passed in 202.103s; local journal race
  148.650s, expanded writable race, platform regression, dual-arch builds,
  vet/staticcheck and Python14 passed. General workflow 40769452406 passed app
  race shards/aggregate, web and Docker checks, retaining existing x/net gate
  failures in Go job 123102370952. This is current point-in-time readiness only;
  registration/handshake/viewer, activation/worker, physical lifecycle, broker,
  SDK/reference apps and full P00–P14 acceptance remain incomplete. No production,
  Status adoption, SDK freeze, dependency/toolchain or beta changes.

- Added separately signed writable backend registration intent, binding the exact
  normal/reconciled running receipt and private document digest. Admission and
  current prepared view authenticate complete source/resource/fence history and
  only their own exact prepared history extension; old readers stay strict.
  Secret bytes are validated separately in memory and never reach admission.
  Added protocol/vector/domain/renewal tests, source/own-row drift and current-key
  reopen/renewal tests, plus actual native source intent checks. See
  [writable registration intent](102-writable-registration-intent.md). Native
  acceptance pending. Physical private-file delivery/recovery, handshake/viewer,
  activation/worker, SDK/reference apps and full acceptance remain incomplete.
  No production, credential, schema, dependency/toolchain or beta changes.

- Writable registration intent source `c88ea3d0f675491014bec06e09d032524137ab43`
  passed quota workflow 40771960021/job 123111204192 and daemon workflow
  40771959992/job 123111205032. Exact original/reconciled native source intent,
  in-memory synthetic document validation, replay and changed receipt refusal
  passed alongside existing writer/start/recovery/readiness and owned cleanup.
  Full Linux journal race passed in 205.284s; local journal race 140.336s,
  focused protocol/source/current-key tests, platform regression, dual-arch
  builds, vet/staticcheck and Python14 passed. General workflow 40771960020
  passed app race shards/aggregate, web and Docker checks, retaining existing
  x/net gate failures in Go job 123111206787. No private registration file was
  delivered; physical delivery/recovery, handshake/viewer, activation/worker,
  broker, SDK/reference apps, lifecycle and full P00–P14 acceptance remain.
  Production, real credentials, Status adoption, SDK freeze and beta unchanged.

- Added once-only writable registration delivery and observation-only uncertain
  recovery: current private-material validation/readiness, durable unknown claim,
  fixed protected atomic file publication, opaque physical proof and current
  receipt CAS. Fresh expired-claim recovery reads only the original claim-bound
  file; missing/changed files are never created or repaired. Added normal and
  recovered historical reads, strict state/history/window validators and actual
  normal/commit-failure/reopen-recovery/missing/stopped acceptance cases. See
  [writable registration delivery](103-writable-registration-delivery.md).
  Native acceptance pending. Handshake/viewer, activation/worker, lifecycle,
  broker, SDK/reference apps and full acceptance remain. Production/beta unchanged.

- Writable registration delivery source
  `92250b151b31e7105a7aee89b6bc00bb49a0db2a` passed quota workflow
  40785510608/job 123157574157 and daemon workflow 40785510581/job 123157573982.
  Real protected publication/replay, post-file receipt failure, expired fresh
  observation recovery with metadata rollback/retry and unchanged file identity/
  timestamps, missing-file non-creation and stopped preflight refusal passed.
  All new delivery flags and existing source/start/readiness/cleanup flags are
  true. Full Linux journal race passed in 147.957s; local full race journal
  145.827s, expanded registration race 21.877s, platform regression, dual-arch
  builds, vet/staticcheck and Python14 passed. General workflow 40785510624
  passed app race shards/aggregate, web and Docker checks; Go job 123157574208
  retained existing x/net vulnerability failures after staticcheck. No handshake,
  viewer access or activation follows from file delivery. Controller transport,
  activation/worker, physical lifecycle, broker, SDK/reference apps and full
  P00–P14 acceptance remain. Production, credentials, Status and beta unchanged.

- Added current writable backend handshake: exact applied original/recovered
  delivery/source verification, fresh protected-file/native policy checks around
  fixed private socket challenge exchange, opaque native evidence and current
  postflight under owner exclusion. No durable mutation or activation. Added
  parser/current-drift/refusal tests plus actual host MAC/challenge/boot checks,
  same-content inode substitution refusal, row/file preservation and stopped
  handshake refusal. See [writable backend handshake](104-writable-backend-handshake.md).
  Native acceptance pending. Host/controller integration, viewer/activation/worker,
  lifecycle, broker, SDK/reference apps and full acceptance remain incomplete.
  Production, credentials, Status adoption and beta unchanged.

- Writable handshake source `9543e04253038fd8c1fdd21de081cffad01877ef` passed
  quota workflow 40786578360/job 123161172419 and daemon workflow
  40786578356/job 123161172391. Actual normal/recovered delivered-key possession,
  host MAC/challenge/key/boot verification, same-content inode substitution
  refusal, unchanged durable rows/file metadata and stopped handshake refusal
  passed, with all prior writable checks and owned cleanup. Full Linux journal
  race passed in 223.745s; local full race journal 153.907s, focused handshake
  race, platform regression, dual-arch builds, vet/staticcheck and Python14 passed.
  General workflow 40786578414 passed app race shards/aggregate, web and Docker
  checks; Go job 123161173409 retained existing x/net vulnerabilities after
  staticcheck. Physical exchange still needs host/controller integration before
  pairing/access. Viewer forwarding, activation/worker, physical lifecycle,
  broker, SDK/reference apps and full P00–P14 acceptance remain incomplete.
  Production, real credentials, Status, SDK freeze and beta unchanged.

- Added separately framed authenticated writable controller handshake transport,
  exact expected delivery/native/challenge correlation, bounded current signature
  checks and opaque endpoint/authority/receipt-time proof. Readonly listeners and
  old protocol remain separate. Added portable nested fixture/proof tests and
  Linux frame/freshness/binding/readonly refusal cases. See
  [writable handshake transport](105-writable-handshake-transport.md).
  Added disposable actual new writable backend plus non-root controller/root
  listener handshake, independent host MAC/boot verification and UID/group
  denial acceptance. Fresh Linux/native results pending; full lifecycle transport
  and current Portal boot consumer/viewer/activation remain unfinished. No
  production/beta changes or SDK freeze.

- Writable handshake transport source `898ea6907be1a6de7a8396e537b13ec84924b846`
  passed quota workflow 40788152465/job 123166422651 and daemon workflow
  40788152469/job 123166422265. Actual new writable backend, configured non-root
  controller/root listener handshake, independent MAC/boot verification, wrong
  UID/group denial and owned cleanup passed. Both new cross-UID/peer-denial
  evidence flags and prior native writable checks are true. Full runner race
  passed (journal 210.813s; transport 6.039s), plus local dual-arch builds,
  vet/staticcheck and Python14. General workflow 40788152521 passed app race
  shards/aggregate, web and Docker checks; Go job 123166423307 retained existing
  x/net vulnerabilities GO-2026-6866/6860 after staticcheck, skipping ordinary tests.
  See [transport acceptance](105-writable-handshake-transport.md). Full writable
  lifecycle transport, Portal boot/viewer/activation/worker, physical lifecycle,
  broker, SDK/reference apps and full P00–P14 acceptance remain unfinished.
  Production, real credentials, Status, SDK freeze and beta unchanged.

- Added closed writable lifecycle controller RPC for writer execution/read/recovery,
  candidate start/read/recovery/readiness and protected registration delivery/read.
  Added first-attempt journal dispatch that refuses unknown writer/start work,
  independent command-domain validation, bounded correlated canonical replies and
  opaque typed provenance. See [writable lifecycle transport](106-writable-lifecycle-transport.md).
  New disposable non-root child/root listener test drives actual writer/start/
  readiness/delivery, historical reads and identical applied replays before host
  handshake verification. Fresh Linux acceptance pending; Portal integration,
  activation/worker and remaining full platform acceptance are not yet complete.
  Production, credentials, Status, SDK freeze and beta unchanged.

- Writable lifecycle transport source `2f304d36818d18a9b3a8e3934697e3c3185a72a8`
  passed quota workflow 40790818016/job 123175301435 and daemon workflow
  40790818046/job 123175301720. Actual non-root controller/root listener drove
  writer/start/readiness/protected delivery, strict historical reads and identical
  applied replays; process start/restart metadata stayed unchanged, applied
  recovery refused and independent host MAC/boot verification passed. Both new
  lifecycle flags, prior native checks and owned cleanup passed. First-attempt
  admission retains its original strict source rules; completed replay uses the
  independent historical reader, with a 6.283s regression test. Exact prepared
  reservation resumes through its dedicated validator. Full Linux runner race
  passed (journal 226.732s; transport 7.457s), full local race journal 164.939s,
  platform regression 8.103s, dual-arch builds, vet/staticcheck and Python14 passed.
  General workflow 40790817988 passed app race shards/aggregate, web and Docker
  checks; Go job 123175301782 retained existing x/net GO-2026-6866/6860 findings
  after staticcheck and skipped ordinary tests. See [lifecycle acceptance and
  Portal integration sequence](106-writable-lifecycle-transport.md). Positive
  expired-unknown recovery over this channel, Portal writable source/boot/viewer/
  activation/worker, physical lifecycle, broker, SDK/reference apps and full
  P00–P14 acceptance remain unfinished. Production, credentials, Status, public
  SDK freeze and beta unchanged.

- Added distinct writable install review, authenticated optional writable adapter
  configuration and Portal typed writer intent/RPC/atomic receipt commit. Shared
  review/data infrastructure recognizes the new format; readonly candidate source
  explicitly refuses it. Stable future candidate identity and writer effect are
  persisted before native IO; successful proof updates only writer/namespace,
  leaving install pending with no process/activation/access. See
  [Portal writable writer](107-portal-writable-install-writer.md). Added SQL
  authority/source/refusal tests and disposable non-root Portal/root endpoint
  acceptance for Portal rollback after actual runner permission settlement,
  historical-read completion and exact retained receipt/resource/effect identity.
  Fresh Linux acceptance pending. Writable start/readiness/registration/boot,
  activation/worker and remaining full acceptance are still required. Production,
  credentials, Status, SDK freeze and beta unchanged.

- Portal writable review/writer source `5a853cd1fa5926f0ae42043341220d47d2e1c032`
  passed quota workflow 40814032240/job 123250796324 and daemon workflow
  40814032539/job 123251062215. Actual non-root Portal/root endpoint exercised
  distinct writable review, native data/writer, forced Portal rollback after
  runner settlement and exact historical completion without replacing writer
  receipt/resource/effect identity. All three new Portal writer flags, prior
  native checks and owned cleanup passed. Full Linux runner race passed (journal
  226.127s; transport 7.003s). Explicit SQLite/PostgreSQL contracts passed in
  19.496s, including pinned source creation times and 1ms allowed/1001ms refused
  database/native clock-boundary cases. Original row identity/timestamps remain
  exact; the existing one-second signer skew bound applies to clock ordering,
  with physical observations never in the host clock future. Local writer race
  repeated three times passed in 27.810s; broader local platform/managed race
  133.492s, dual-arch builds, vet/staticcheck and Python14 passed. General workflow
  40814032138 passed app race shards/aggregate, web and Docker checks; Go job
  123251068257 retained existing x/net GO-2026-6866/6860 findings after focused
  database tests/staticcheck, skipping ordinary full tests. See [Portal writable
  acceptance](107-portal-writable-install-writer.md). Writable start/readiness/
  registration/boot/viewer/activation/worker, physical lifecycle, broker,
  SDK/reference apps and full P00–P14 acceptance remain incomplete. Production,
  real credentials, Status, SDK freeze and beta unchanged.

- Added Portal writable backend start intent, authenticated lifecycle dispatch/
  historical-read/reconciliation selection and atomic start-to-probe settlement.
  Reuses the confirmed writer's future resource ID and exact original receipts;
  the extended source permits only its independently validated third start
  effect while the original writer reader keeps its strict no-start contract.
  Added SQL source/authority/partial-history refusals and disposable non-root
  Portal/root runner rollback recovery plus native identity/start-time witness
  and owned cleanup. See [Portal writable start](108-portal-writable-start.md).
  Fresh Linux evidence pending. Readiness/registration/boot/viewer/activation/
  worker, physical lifecycle, broker, SDK/reference apps and full acceptance
  remain incomplete. Production, credentials, Status, SDK freeze and beta unchanged.

- Portal writable start source `797e1ace7b9e841dd8f408fb370fde1ef7d8c4a9`
  passed quota workflow 40816295673/job 123258102372. Actual non-root Portal/root
  runner startup, forced Portal rollback, exact original result completion and
  retained writer/resource/effect/native/start-time identity passed. All four new
  start recovery/retained-identity/native-witness/full-quota flags are true. The
  root witness waited for actual 256MiB/65536 inode denial verification and
  reinspected the running process; original 8MiB/128 native fixture checks remain.
  Fixed profile refusal tests, prior native checks and owned cleanup passed.
  Explicit SQLite/PostgreSQL writer/start contracts passed in 25.957s. Local
  focused race 15.893s, standalone start race 8.381s, broader platform/managed
  race 138.433s, Linux dual-arch Portal/fixture builds, vet/staticcheck and Python14
  passed. General workflow 40816295709 passed race shards/aggregate, web and
  Docker checks; shard 1/job 123258136904 ran full journal race in 223.364s and its
  app subset in 112.071s. Go job 123258136783 retained existing x/net
  GO-2026-6866/6860 findings after explicit database tests/staticcheck, skipping
  ordinary full tests. No standalone daemon workflow was triggered by this
  Portal/fixture-only diff. See [writable start acceptance](108-portal-writable-start.md).
  Readiness/registration/boot/viewer/activation/worker, positive expired-unknown
  runner recovery through Portal, physical lifecycle, broker, SDK/reference apps
  and full P00–P14 acceptance remain incomplete. Production, credentials, Status,
  SDK freeze and beta unchanged.

- Added Portal writable readiness and activate-stage fresh refresh using the
  existing authenticated concrete runner probe. Separate continuation validates
  canonical applied process/history while initial writer/start readers keep
  empty-readiness scope. Pin candidate metadata across IO; require exact current
  source/authority and a fresh opaque proof; atomically update readiness and
  probe-to-activate transition, or refresh only the observed candidate. Added SQL
  source/refusal cases and actual non-root Portal/root fixture coverage for audit
  rollback, expired-proof refusal, historical-source/fresh-probe refresh and
  runtime disablement after native IO. See [writable readiness](109-portal-writable-readiness.md).
  Fresh Linux results pending. Registration/boot/viewer/activation/worker and
  remaining full platform acceptance are incomplete. Production, credentials,
  Status, public SDK freeze and beta unchanged.

- Portal writable readiness source `f4d8ebd661a5eb5496d225c92167a6d63bde6b4f`
  passed quota workflow 40818206784/job 123264206750. All three new readiness
  advancement/refresh/current-source refusal flags, prior native checks, full
  Portal quota witness and owned cleanup passed; registry cleanup also passed.
  Explicit SQLite/PostgreSQL writer/start/readiness contracts passed in 47.781s.
  Local focused race 28.534s and platform/managed race 156.795s, dual-arch Linux
  builds, vet/staticcheck and Python14 passed. General workflow 40818206640 passed
  all race shards/aggregate, web and Docker checks. Shard 1/job 123264206743 ran
  full journal race 233.690s and Portal subset 151.251s. Go job 123264206965 still
  failed existing x/net GO-2026-6866/6860 dependency findings after focused DB
  tests/staticcheck; ordinary full test step was skipped. No standalone daemon
  workflow was triggered by this Portal/fixture-only diff. See [readiness
  acceptance](109-portal-writable-readiness.md). Registration/boot/viewer/activation/
  worker, full recovery/physical lifecycle, broker, SDK/reference apps and full
  P00–P14 acceptance remain incomplete. Production, real credentials, Status,
  SDK freeze and beta configuration remain unchanged.

- Added Portal writable registration preparation, protected lifecycle delivery and
  exact original-result settlement after a Portal transaction failure. The sole
  registration-specific source permits and verifies four effects; old writer/
  start/readiness/readonly readers retain their strict source contracts. Keys
  remain derived from the existing encrypted identity and only digests enter
  public commands/control effects. Added SQL source/refusal/rollback tests and
  actual non-root Portal/root delivery rollback/replay/current-authority fixture.
  See [writable registration](110-portal-writable-registration.md). Fresh Linux
  evidence pending. Host boot/possession, viewer/activation/worker and remaining
  full platform acceptance remain incomplete; production, real credentials,
  Status, SDK freeze and beta configuration unchanged.

- Extended the writable registration continuation with independent host MAC/key
  possession and current boot verification over the existing protected writable
  handshake. Recheck all four effects and current encrypted identity after IO;
  return a distinct non-serializable live writable boot observation and no grants.
  Added applied-delivery/zero-proof/serialization refusals plus actual native
  same-boot exchanges, foreign challenge and post-IO runtime-disablement refusal.
  Fresh composed Linux evidence pending; viewer/activation/worker and the full
  lifecycle/SDK/reference-app requirements remain incomplete.

- Writable registration + host boot functional source
  `ae03c03467a04dc213bb1013e15041ef9bce00a8` passed quota workflow
  40820212731/job 123270673202. All five new actual Portal registration/boot
  flags, prior native checks, full quota witness and owned cleanup are true;
  registry cleanup passed. General workflow 40820213195 explicitly passed both
  SQLite/PostgreSQL registration/refusal/applied-delivery boot cases and existing
  writer/start/readiness contracts in 61.810s, plus build/vet/staticcheck. All app
  race shards/aggregate, web checks and Docker checks passed. Shard 1/job
  123270877958 ran full journal race 228.876s and Portal subset 146.731s. Go job
  123270877856 still failed the existing x/net GO-2026-6866/6860 findings and
  skipped its ordinary full test step. Local focused registration/boot race
  18.679s, preceding registration platform/managed race 185.591s, final dual-arch
  Linux test builds/vet/staticcheck and Python14 passed. No standalone daemon
  workflow was triggered by this Portal/fixture-only diff. See [registration and
  host boot acceptance](110-portal-writable-registration.md). Writable viewer/
  activation/worker, positive expired-unknown recovery through Portal, physical
  lifecycle, broker, SDK/reference apps and full P00–P14 acceptance remain
  incomplete. Production, real credentials, Status, SDK freeze and beta unchanged.

- Added the distinct signed writable backend-request family, physical fixed-socket
  relay, current journal pre/postflight and protected root/controller frames.
  Reuse existing bounded HTTP/structural-assertion checks without converting
  writable authority into readonly values. Added refusal/copy/provenance tests
  plus actual normal/recovered file-inode/stopped-process and cross-UID relay
  acceptance. The disposable backend now counts route entries and can close
  after one accepted write to prove the relay does not retry uncertain requests.
  See [writable request protocol](111-writable-backend-request.md). Fresh Linux
  results pending. Current Portal viewer/OU access, activation/worker and full
  lifecycle/broker/SDK/reference-app acceptance remain incomplete; production,
  credentials, Status, SDK freeze and beta configuration unchanged.

- Writable request functional source `8e8381cbfc1254499da79da57cc4abeba84a7f86`
  passed quota workflow 40822546730/job 123278068950. All six new actual normal/
  recovered request, replaced-file/stopped-process refusal, cross-UID relay and
  uncertain-write/no-retry flags, prior native checks, full quota witness and
  owned cleanup are true. Registry cleanup passed. Linux control/transport/driver
  race passed in 2.722s/8.428s/3.016s. Runner workflow 40822546716/job 123278070756
  passed full runner race (journal 239.921s, transport 8.961s), closed policy,
  identity isolation and Portal confirmation. General workflow 40822546671 passed
  app race shards/aggregate, web and Docker checks; shard 1/job 123278071591 ran
  full journal race 158.203s and Portal subset 96.450s. Go job 123278068627 passed
  explicit SQLite/PostgreSQL contracts in 78.391s plus build/vet/staticcheck,
  then failed existing x/net GO-2026-6866/6860 findings and skipped ordinary full
  tests. Local control/journal/driver focused race, dual-arch test builds,
  Linux vet/staticcheck and Python14 passed. See [writable request
  acceptance](111-writable-backend-request.md). Current Portal writable viewer/OU
  access and post-IO authorization, activation/worker, recovery/physical lifecycle,
  broker, SDK/reference apps and full P00–P14 acceptance remain incomplete.
  Production, real credentials, Status, SDK freeze and beta unchanged.

- Added actual writable initial activation with fresh host boot, exact source/
  frontend/audience validation, atomic candidate/audience/ready/completion and
  audit, plus resulting-history reconstruction before commit. Added separate
  completed writable history and current viewer/OU access using the shared
  readonly authorization transaction without constructing a live lease. Current
  writable requests recheck access before/after real native IO and withhold a
  revoked viewer's buffered result without retrying an uncertain write. Added
  SQL metadata/source/refusal cases and actual activation rollback, user/ancestor
  OU business requests and post-IO membership-revocation acceptance. See
  [writable activation/access](112-writable-activation-access.md). Fresh Linux
  evidence pending. Managed browser launch/assets, worker/scheduler and remaining
  full lifecycle/broker/SDK/reference-app acceptance are incomplete. Production,
  credentials, Status, SDK freeze and beta configuration unchanged.

- Writable activation/current viewer functional source
  `d404241292776f114d277c2c825861753581ca67` passed quota workflow
  40826884869/job 123291856129. Actual atomic activation/audit rollback, retained
  native/writer/file identity, ordinary-user/ancestor-OU requests, reparent refusal
  and post-IO membership revocation/no retry passed; all three new report flags,
  prior native checks, full quota witness and owned cleanup are true. Registry
  cleanup passed. Fixed a PostgreSQL registration audit fault trigger leaking
  between test processes; cleanup now removes both trigger and function. General
  workflow 40826884881/job 123291972286 passed both explicit database steps:
  existing writable lifecycle contracts 57.776s; new activation/access and legacy
  access contracts 24.764s. Build/vet/staticcheck, all app race shards/aggregate,
  web and Docker checks passed. Shard 1/job 123291972471 ran full journal race
  162.297s and Portal subset 98.342s. Go job still failed existing x/net
  GO-2026-6866/6860 findings and skipped ordinary full tests. Local focused race
  24.308s, cleanup-focused race 15.161s, dual-arch Linux builds, Linux
  vet/staticcheck and Python14 passed. No standalone daemon workflow was triggered
  by this Portal/fixture-only diff. See [activation/access
  acceptance](112-writable-activation-access.md). Managed browser launch/assets,
  writable worker/scheduler, recovery/physical lifecycle, broker, SDK/reference
  apps and full P00–P14 acceptance remain incomplete. Production, real credentials,
  Status, SDK freeze and beta unchanged.

- Added a bounded claimed writable installation driver connecting immutable
  artifact staging, actual data/writer/start/readiness, registration and current
  boot activation. Failed attempts retain the last confirmed lease, including a
  registration revision committed before activation rollback. Prepared-start
  routing retains strict full writer/start source checks. Added pending/refusal
  tests and a dedicated SQLite/PostgreSQL CI step, plus real disposable native
  driver entry/failure/resumption checks on the original reviewed installation.
  See [claimed writable driver](113-writable-install-worker.md). Local SQLite,
  race, dual-arch Linux builds, Linux vet/staticcheck and Python14 passed.
  Functional source `5f32c95095cfe11e48ac9e00daa43c964e5754e7` passed native
  quota workflow 40829332469/job 123299658093: seven worker flags, prior native
  checks, full quota witness and owned cleanup true; registry cleanup succeeded.
  Fixed the parent fixture's marker relay; the initial missing-relay failure and
  first relay edit's compile failure are not final acceptance evidence. General
  workflow 40829332473/job 123300058040 passed lifecycle SQL 62.589s, current/legacy
  access SQL 29.017s and worker/static SQL 10.856s on SQLite/PostgreSQL. Build,
  vet, Linux staticcheck, published market, arm64 numeric, web, Docker and race
  shards/aggregate passed. Shard 1/job 123300058092 passed full journal race
  232.159s and Portal subset 141.991s. Go failed the existing x/net
  GO-2026-6866/6860 gate and skipped ordinary full tests. Background
  writable queue/scheduler, managed browser shell and the remaining full platform
  acceptance are incomplete. Production, credentials, Status, SDK freeze and beta
  configuration unchanged.

- Added an explicit writable installation queue/service mode, preserving static/
  frontend selectors. It claims only current canonical writable initial work;
  expired work with residue or post-start stages is fenced into retained review/
  recovery instead of redispatched. Failure settlement pins the consumed review,
  request digest and exact runtime, including safe denial of revoked runtime
  residue. Writable probe waits poll only actual native observations under a
  bounded deadline and commit once; SQL/source errors return immediately. Added
  SQL claim/recovery/authority/binding/retry/cancellation tests and a dedicated
  SQLite/PostgreSQL CI step. Native acceptance now uses actual background serve
  for queued-to-data progress and actual readiness waiting/audit-error return.
  See [writable scheduling](114-writable-install-scheduling.md). Local tests,
  focused race, dual-arch Linux builds, vet/staticcheck and Python14 passed.
  Functional source `966f478d72c844598be610f0aa2bef49a5b63b09` passed native
  quota workflow 40831373753/job 123306128129: actual background claimed-progress
  and readiness-wait flags, prior native checks, full quota witness and owned
  cleanup true; registry cleanup succeeded. General workflow 40831373762/job
  123306128447 passed lifecycle SQL 74.213s, current/legacy access SQL 37.602s,
  worker/static SQL 14.164s and queue/scheduling/legacy SQL 14.940s on SQLite/
  PostgreSQL. Build/vet/staticcheck, published market, arm64 numeric, all app
  race shards/aggregate, web and Docker passed. Shard 1/job 123306128452 ran
  full journal race 239.214s and Portal subset 71.878s. Go failed existing x/net
  GO-2026-6866/6860 findings and skipped ordinary full tests. No standalone daemon
  workflow triggered. Updated the plan index to distinguish requirements,
  approved development scope, implemented evidence and outstanding delivery.
  Uninterrupted background completion/browser launch,
  physical lifecycle, broker/SDK/reference apps and full release acceptance
  remain incomplete. No production/credential/Status/SDK-freeze/beta change.

- Added a fresh writable native fixture driven from queued to ready solely by
  the actual non-root background service, with no manual lease/stage/candidate
  calls or injected install failure. It checks exact effects/claim/fence/cleared
  ownership and actual ordinary-user POST/GET with body spoof refusal. A separate
  pre-start signed test ownership record enables bounded cleanup on interruption
  without staging a start by hand. Root independently verifies completed native
  identity/full quota evidence, then exercises missing-snapshot fallback cleanup
  and re-witnesses the neighboring app alive. Added ownership refusal tests and
  six mandatory native report flags. See [background completion](115-writable-background-completion.md).
  Local ownership race, dual-arch Linux builds, vet/staticcheck and Python14
  passed. Functional source `ecc1134fb177eba871b1c7570818226a28f3d47d` passed
  quota workflow 40833806816/job 123313800713: all six background-completion,
  current-viewer, native-witness, fallback/owned-cleanup and neighbor-preservation
  flags true, prior native/full-quota/owned-cleanup checks true; registry cleanup
  succeeded. General workflow 40833806834/job 123313800959 passed lifecycle SQL
  77.011s, current/legacy access SQL 39.915s, worker/static SQL 15.208s and queue/
  scheduling/ownership SQL 16.098s on SQLite/PostgreSQL. Build/vet/staticcheck,
  published market, arm64 numeric, all app race shards/aggregate, web and Docker
  passed. Shard 1/job 123313800910 ran full journal race 211.815s and Portal
  subset 60.478s. Go failed existing x/net GO-2026-6866/6860 findings and skipped
  ordinary full tests. No standalone daemon workflow triggered.
  No production code, schema, dependency, deployment, credential, Status,
  SDK-freeze or beta change. Generic managed browser launch and
  the remaining complete lifecycle/platform acceptance are still unfinished.

- Added completed writable frontend admission using its full typed native history
  and shared current account/OU/audience/runtime/publisher transaction. Runtime
  routing is a hint only; static restrictions remain and failures do not fall
  back across families. Launch/document/viewer reads stay buffered through final
  checks and close private registration material. Directory eligibility now
  projects supported completed writable reviews without claiming launch authority
  or native health. Existing session-only URLs, CSP/no-store/budgets and empty
  platform capability metadata remain. Added current HTTP/refusal/OU-reparent/
  callback/cancellation tests and expanded pre-gate SQLite/PostgreSQL static and
  access regressions. Real background-installed apps now exercise these HTTP
  document/directory/revocation paths. See [writable launch documents](116-writable-launch-documents.md).
  Local focused race 60.177s, expanded local tests 5.363s, dual-arch Linux builds,
  Linux vet/staticcheck and Python14 passed. Functional source
  `9ab08744748d1a3dd052466dbbb39fbfa538e893` passed quota workflow 40837291395/
  job 123324962078: all three current document/directory/revocation flags,
  previous native/full-quota/owned-cleanup checks true; registry cleanup passed.
  General workflow 40837291397/job 123324962656 passed lifecycle SQL 76.436s,
  current/legacy access SQL 39.245s, worker/static SQL 14.821s, queue SQL 15.627s
  and expanded frontend/static/access/PostgreSQL wait SQL 106.754s. Build/vet/
  staticcheck, published market, arm64 numeric, all app race shards/aggregate,
  web and Docker passed. Shard 1/job 123324962575 ran full journal race 238.387s
  and Portal subset 70.707s. Go failed existing x/net GO-2026-6866/6860 findings
  and skipped ordinary full tests; no standalone daemon workflow triggered. Iframe
  backend protocol/gateway, interactive SDK/reference-app browser acceptance and
  the remaining full lifecycle/platform release are unfinished. No schema,
  dependency, production, credential, Status, SDK-freeze or beta change.


- Added the shared boolean unsaved-change guard in the experimental SDK and app
  host. Mounted form owners aggregate with OR; presentation changes preserve
  edits, retirement clears stale owners, and the host owns localized router/POP
  leave confirmation plus native refresh warnings. Development protocol dev.4 is
  matched and pinned across repositories. No draft body, destination, app veto,
  persistence, schema, dependency, publication or production access is added.
  See [unsaved-change guard](121-unsaved-change-guard.md) for implementation,
  tests and bounded behavior. Local affected Portal108 and SDK200 tests,
  typechecks/consumer builds, Python26 and the built document handler passed;
  the actual AppViewV2 browser fixture verified Chinese host confirmation,
  cancel/state retention, theme preservation and confirmed iframe retirement.
  Companion Apps #30 passed all checks at its pinned commit. Linux runtime
  acceptance passed at functional b58bf494 in run 40860210842/job 123401233721;
  draft/current-viewer/layout/no-retry/revoke and owned-cleanup/neighbor flags
  true, execution_ready false. General Go still fails inherited x/net findings.
  Web shard3 exposed an older missing-height fixture, repaired without weakening
  the cover requirement (526 local shard tests pass). P06/full platform remain incomplete.


- Added signed static route registries to current launch metadata, selecting home
  layout by path rather than array order. Matched development bridge dev.5 and
  `useAppNavigation` follow only host-committed path/layout. Deep links, POP and
  dirty-route confirmation preserve one installation document and mounted state;
  undeclared/query/fragment/encoded paths never mount content. Requests cannot
  write parent history or bypass current backend authorization. See
  [signed routes and host history](122-signed-route-navigation.md). Local Portal147,
  SDK217, frontend consumers, Python26 and actual document/HTTP tests pass. A local
  actual-page browser check verifies commit/cancel/state retention; functional
  df293dad passed Linux run40863529181/job123412436309 with navigation, draft,
  current-viewer/layout/no-retry/revoke and owned-cleanup/neighbor flags true.
  execution_ready remains false. Apps dab705d passed all jobs in run40863149243.
  General Go still fails inherited x/net findings after affected SQL suites pass. No descriptor/storage schema,
  dependency, publication, production, Status, stable SDK or beta change.
  Typed queries and remaining P06/full platform requirements stay open.


- Added optional signed typed query rules (bounded text/integer/boolean/enum), a
  pinned pure validator shared by descriptor tools/host/SDK, and dev.6 canonical
  search with host-committed typed query state. Pointer-preserved zero bounds,
  strict duplicate/encoding/size/Unicode checks and destination-rule admission
  retain current authority and channel guards. Invalid links have localized
  host-owned home recovery. See [typed queries](123-typed-route-query.md).
  Local Portal165/SDK249, shared corpus56, Python12/14/2, frontend consumers,
  descriptor/HTTP/signed-document tests and Linux compilation pass. Actual-page
  browser URL/back/forward/reload/type restoration and error recovery passed;
  initial a328ee5c passed native run40868846704/job123430411682 (typed-query and
  prior browser/native/cleanup flags true, execution_ready false). General Go
  passed new pure contracts/SQL suites then failed inherited x/net findings.
  A query-object identity regression was then found and fixed in Apps fa34a900;
  final functional a1a19259 passed native run40870583604/job123436285697 with
  unchanged query effects across locale updates and all prior flags true.
  Apps fa34a900 passed all checks; Portal web/race/Docker pass, and general Go
  still fails the inherited vulnerability gate. execution_ready remains false. Descriptor schema is extended;
  database schema, dependencies, publication, production, Status, SDK freeze and
  beta settings are unchanged. Remaining full P06/platform work stays open.


- Added shared page metadata/actions for P06: bounded immutable plain-text
  descriptors, a common Ant Design header, experimental dev.7 action dispatch,
  one pending operation, deadlines/cancellation and route/document ownership.
  React StrictMode and late old-route cleanup defects were caught and repaired.
  An actual Portal-shell drill caught and repaired the inherited full-path
  Suspense remount; a failing-then-passing test covers same-installation state
  retention and cross-installation retirement (31 AppLayout tests pass).
  The browser/native fixture now requires page-action evidence; Linux results
  at initial 23de4d76 passed run40935591598/job123665149793: page-action and
  prior browser/native/cleanup flags true, execution_ready false. Apps a4c69386
  passed all CI. The later outer-layout fix has local browser/test evidence and
  also passed native run40936495688/job123668295562. A further 320px long-label
  and Enter-action assertion passed at final functional b9bb5146 in
  run40937099808/job123670423183, with all prior browser/native/cleanup flags
  true and execution_ready false. See [shared page actions](124-shared-page-actions.md).
  P06, UX-01/06/08/09 and full platform acceptance remain incomplete. Production,
  Status, publication, stable SDK freeze, storage and beta settings are unchanged.
