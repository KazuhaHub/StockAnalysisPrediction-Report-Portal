# Application platform v2: delivery plan

Status: proposed implementation design, prepared 2026-10-24 Pacific. The owner
approved the product direction and writing this plan. Specific database layouts,
host privileges and deployment are not approved by this document.
Nothing in this directory is an implemented endpoint, command or accepted ADR.
The previous Status rollout remains paused. This is a platform project, not a
Status redesign. Production image/channel configuration is outside this change.

## The outcome

A user installs an application as they would install an application on a phone:
browse, review, install, authorize, open, update and uninstall. The installation
unit includes all required frontend and backend parts. An application owner never
has to create a container, copy a signing key, enter an internal service URL or
repair pairing manually. A deployment administrator provisions the platform
runtime once; this infrastructure prerequisite is visible before installation.

Applications share the Portal shell, language, theme and navigation. They retain
independent data and execution boundaries. A failed app must not take the report
reader, authentication or analysis queue down with it.

## Read in this order

| Document | What it answers |
| --- | --- |
| [01 Architecture and product](01-architecture.md) | What is installed, where code runs and which experience belongs to the platform |
| [02 Packages and interfaces](02-contracts.md) | Package format, identity, capabilities, bridge, routes and provisional API contracts |
| [03 Lifecycle and recovery](03-lifecycle.md) | Every operation, restart reconciliation, upgrade, rollback and retained data |
| [04 Implementation work packages](04-implementation.md) | Ordered tasks, source locations, dependencies, tests and completion criteria |
| [05 Storage decision proposal](05-storage-proposal.md) | Logical records, alternatives, backup/deletion rules and decisions requiring owner approval |
| [06 Acceptance specification](06-acceptance.md) | Reproducible tests, failure injection, evidence and release gates |
| [07 Developer and operator guide](07-handbook.md) | Concrete authoring, deployment, support and release workflows |
| [08 Source inventory](08-source-inventory.md) | Verified legacy code locations, reuse boundaries and compatibility traps |
| [09 Execution evidence](09-execution-log.md) | Actual implementation progress, verification and remaining work |
| [13 Initial physical storage](13-control-storage-spec.md) | Concrete control-plane/runner layout and incremental D2 decision |

Read the architecture and lifecycle before implementing an endpoint. Then take
the next work package whose dependencies and decision gates are satisfied. Do not
start from an application-specific adapter or a new market UI.

## Verified starting point

The inspection baseline is Portal commit
`d84f138e483034e12395359bb976dc1b9027ac51`. Recheck source before implementation.

| Existing area | Current behavior | Consequence for v2 |
| --- | --- | --- |
| `internal/app/apps_api.go`, `apps_store.go` | A ZIP contains a manifest and static assets; replacement resets its backend connection | Keep the legacy installer; implement a separate staged v2 lifecycle |
| `apps_market.go` | Catalogues describe downloadable frontend bundles | Add a versioned catalogue; bind reviewed metadata to verified release bytes |
| `web/src/pages/AppView.tsx` | A fixed-height sandboxed iframe hosts an installed app | Introduce one platform shell and a negotiated presentation protocol |
| `web/src/lib/appBridge.ts` | Parent mediates scoped machine API calls | Preserve the host-only token boundary; add explicit version negotiation |
| `internal/app/extension_*.go` | Viewer-bound, manually paired external backend connections | Reuse authentication rules; add automatic managed registration separately |
| ADR 0038 notification service | Capture, routing, delivery and inbox already exist | Integrate through this service; do not build a parallel notification system |
| Shared Apps repository | SDK, React/TypeScript/Ant Design, generator, per-app releases | Evolve these together; do not vendor app business code into Portal |

The Apps repository is
[StockAnalysisPrediction-Report-Portal-Apps](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps).
It remains the application source, SDK and distribution repository. Portal owns
the generic host and, initially, the separately built runner implementation.

## Boundaries and terminology

- **Package:** one immutable application release, including referenced artifacts.
- **Installation:** the durable identity of one app in one Portal deployment.
- **Generation:** one activation of code/permissions under that installation.
- **Runner:** the privileged infrastructure service that controls app containers.
- **Broker:** the bounded gateway exposing only approved platform capabilities.
- **App administrator:** a current Portal administrator authorized to manage apps.
- **Runtime administrator:** the server operator who configures the runner once.
- **Desired state:** what Portal durably requested. **Observed state:** what the
  runner actually verified. They may differ and must never be silently conflated.

Additional implementation vocabulary:

| Term | Plain meaning |
| --- | --- |
| Digest | Hash identifying exact bytes; a different build has a different identity |
| Signature | Publisher proof covering a descriptor; trust still depends on an approved publisher key |
| Idempotency key | A request identity that makes a retry return the same operation instead of repeating it |
| Lease | A time-limited claim to perform work; expired workers must stop using it |
| Fencing | Rejecting work from an old owner/generation even if that process is still alive |
| Reconciliation | Comparing requested state with verified runtime state and safely completing interrupted work |
| Capability | A narrow named permission, usually restricted to specific resources |
| Checkpoint | A consistent point tying control records, app data, code and recovery keys together |
| Unix socket | A local filesystem-addressed communication endpoint, not an Internet-facing port |
| OCI image | The immutable container artifact consumed by the initial backend adapter |

When a task mentions a lease or generation, implement its refusal/recovery test
as well as its successful path. Renaming a field to one of these terms does not
provide the corresponding safety property.

Portal is initially a single-tenant installation. App installation is deployment
wide; access can be restricted to users/OUs. This is not a per-user container fleet
or a multi-tenant public hosting service.

## First release and later work

The first complete release includes static and managed-backend apps, a seamless
shell, automatic pairing, explicit permissions/OU access, independent task/event
execution, secret binding, quotas, install/update/disable/uninstall, verified
backup/recovery and a real marketplace path. Two neutral reference apps prove it.

The initial backend adapter targets a supported Linux Docker environment.
Non-Linux or runtime-free deployments continue to support static apps; managed
apps show an explicit compatibility reason before installation. Alternate
container engines, Kubernetes, arbitrary native binaries, WASM, paid billing,
third-party app-to-app dependencies, multi-node scheduling and automatic updates
are later projects. Do not claim support before a tested adapter exists.

There is no requirement to force every app into a declarative page language.
The default is authored React/TypeScript with the shared SDK inside an isolated
document. A restricted native widget renderer can be added later if needed.

## Decision gates

The concrete D1/D3/D4 development candidate is
[runtime and trust candidate A](18-runtime-and-trust-proposal.md). Its approval
record is separate from the accepted initial D2 storage scope and from production
rollout.

| Gate | Required decision or evidence | Blocks |
| --- | --- | --- |
| D1 | Approve architecture, runtime threat model and one-time runner deployment | Privileged runner integration and production setup |
| D2 | Approve concrete physical storage proposal for Portal and runner | Any new table, column, migration or persistent side-store layout |
| D3 | Approve protocol/package v2 contract and initial limits | Public SDK/API freeze and publisher implementation |
| D4 | Approve publisher trust, revocation and network/secret policy | Accepting packages for execution |
| D5 | Approve visual/interaction prototype | Final shell implementation acceptance |
| D6 | Complete all release acceptance evidence; authorize rollout | Production activation and legacy adoption |

Documentation and pure in-memory protocol tests can precede D2. A diagram or
logical record list is not permission to create storage. Do not hide new data in
the existing `meta` table or JSON files to avoid the schema decision.

## Definition of done

Every first-release requirement must link to a passing case in
[the acceptance specification](06-acceptance.md), a reviewed source change and
actual release evidence. Required demonstrations include an install from the real
market, no manual backend setup, restart recovery, an update with retained data,
rejected permissions, a failed migration and recovery, a stopped/uninstalled app,
and unaffected Portal business routes during app/runner outages.

No checkbox below is completed by this plan. See the
[execution register](09-execution-log.md) for subsequent work package state.
Source tests alone do not prove a production installation. Optional
credentials for a particular business app are separate from platform acceptance.
