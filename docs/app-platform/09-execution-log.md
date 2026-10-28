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
