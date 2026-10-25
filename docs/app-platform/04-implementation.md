# Ordered implementation work packages

All packages below start **not started**. A proposed path is a suggestion for new
source, not a claim that a file/endpoint exists. Implement one vertical slice per
reviewable PR; do not combine a schema decision, runtime privilege, app migration
and production rollout in one change.

## 1. Before the first implementation PR

1. Read repository `AGENTS.md` and ADRs 0002, 0003, 0016, 0034, 0035, 0038 and 0040.
2. Confirm the working tree is clean or use an isolated worktree. Preserve other
   ongoing owner work. Use a work-specific `kazuha/` branch; no history rewriting.
3. Read [the contracts](02-contracts.md) and [lifecycle](03-lifecycle.md). Locate
   current implementations with the commands below.
4. Copy the relevant acceptance case IDs into the PR checklist. Write meaningful
   failing behavior tests before implementation.
5. Identify whether D1..D6 applies. Stop dependent implementation when its decision
   is absent; work on documentation, pure contracts or fixtures in the meantime.

Existing inspection commands, run from the named repository root:

```sh
# Portal repository
rg --files internal/app web/src docs/adr | rg 'apps_|AppView|AppsHub|appBridge|extension_|notification_|003[458]|0040'
rg -n 'InstallApp|DeleteApp|parseAppBundle|resetExtensionConnection' internal/app
rg -n 'apps|extensions' internal/app/routes.go

# Apps repository
rg --files packages templates scripts apps | rg 'app-sdk|create-app|catalogue|plugin-standard'
```

## 2. Dependency graph

```text
P00 inventory/decisions -> P01 prototypes -> P02 contracts
P02 -> P03 approved storage -> P04 runner -> P05 lifecycle installer
P02 + P01 -> P06 shell/SDK
P03 + P04 -> P07 capability broker
P05 + P06 + P07 -> P08 frontend and managed reference apps
P03 + P07 -> P09 jobs/events/notifications
P05 + P07 -> P10 update/recovery/uninstall
P08 + P09 + P10 -> P11 market/developer publication
P11 -> P12 adversarial/full acceptance -> P13 release -> P14 adoption
```

Some source work can proceed independently, but later gates cannot be skipped.
The first usable milestone is P08. The first **complete platform release** also
requires P09..P13. Do not call the early milestone the final delivery.

| Milestone | Work packages | Demonstration | Permission to proceed |
| --- | --- | --- | --- |
| Design frozen | P00..P02 | Approved flows, threat model, schema decision and validated contract fixtures | D1..D5 as applicable; no production changes |
| Installation works | P03..P08 | Both neutral apps install/open with automatic backend setup | Disposable development environments only |
| Lifecycle works | P09..P11 | Background tasks, update/failure recovery, uninstall and publication pipeline | Acceptance catalogue; no production adoption |
| Release accepted | P12..P13 | Full matrix and actual released-byte staging drill | Separate D6 production rollout decision |
| Existing apps adopted | P14 | Reviewed per-app continuity and rollback evidence | Separate app-specific adoption plan |

Do not estimate calendar completion from this table before P00 resolves the runtime
profile and physical schema. Track completed evidence and dependency gates first;
then estimate each PR against the verified integration work.

## P00. Inventory, decisions and authority map

**Inputs:** this plan; current source and release inventory. **No storage changes.**

Actions:

1. Record current legacy ZIP format, install behavior, app bridge and manually
   paired backend authority in a compatibility matrix.
2. Document which generic APIs can be reused and which require new contracts.
3. Produce proposed ADR amendments for 0003 and 0040: managed lifecycle, stable
   installation identity on update and generation-bound revocation.
4. Resolve D1/D3/D4: runtime topology, trust/signature policy, package limits,
   offline background lease and credential handling.
5. Obtain D2 separately before defining physical storage products in code.

**Files:** new design/ADR proposals; existing ADRs retain accepted decisions until
reviewed amendments are accepted. **Exit:** decision IDs, owner responses and
source references are linked; no misleading implemented flags.

## P01. Product interaction and visual prototypes

**Depends on:** P00 product scope. **Gate:** D5 before final UI acceptance.

Build non-production prototypes for My apps, market detail, permission review,
installation progress, running app, update review, app settings, disable,
uninstall/retained data and recovery failure. Use neutral sample content.

Exercise reader/admin states, 320 px and desktop, dark/light, all languages, long
labels, keyboard-only use, refresh, back/forward and unsaved navigation. Show both
document and viewport layouts. One outer title and one document scroll must be
visible in the prototype, with no outer app border or independent language picker.

**Files:** proposed `web/src/components/apps/*` and prototype fixtures isolated
from registered production routes. **Exit:** screenshots and interaction recordings
against UX-01..UX-10; owner approves the entire flow, not one screenshot.

## P02. Pure contracts, schemas and package tooling

**Depends on:** P00. **Gate:** D3/D4 for public freeze.

1. Create pure manifest/plan/state/capability types in `internal/appplatform`.
2. Write fixtures for accepted/rejected packages, version ranges, publisher binding,
   route normalization, capability changes and deterministic descriptor signatures.
3. Implement bounded parsers and validators without DB or runtime effects.
4. Add matching SDK TypeScript schemas and cross-language fixture tests in the
   Apps repository. Reserve a `portal.app/v2` envelope; keep legacy parsing separate.
5. Build a package inspector that prints only public declarations/digests and
   refuses unknown executable fields. Do not execute a package to inspect it.

**Tests:** PKG-01..PKG-10, SEC-01/02. **Exit:** the same corpus has the same result
in Portal, packaging tools and SDK; changed descriptors invalidate review plans.

## P03. Approved persistence and migration/recovery contracts

**Depends on:** P02 and D2 owner approval of a concrete physical layout.

1. Turn [the logical proposal](05-storage-proposal.md) into a concrete reviewed
   schema/indices/uniqueness/restore specification for both Portal and runner.
2. Add new ordered Portal migration steps through `migrate_steps.go` and dedicated
   `app_platform_*` schema/store files. Choose the next available step ID only at
   implementation time. Never edit a released step or the frozen baseline.
3. Implement transaction boundaries, revision comparisons, operation idempotency,
   claims/leases, deletion cleanup and checkpoint exports.
4. Add product verification, backup enumeration and old/new backup compatibility.
5. Implement the approved runner journal and lock ownership. Runner startup must
   not launch workloads when its journal is incompatible or damaged.

**Tests:** DATA-01..DATA-08 on SQLite and PostgreSQL for Portal; runner-store recovery
fixtures. **Exit:** fixtures prove retry after failure, concurrent writers, restart,
restore rejection and preserved preexisting Portal data.

## P04. Runner adapter and installation preflight

**Depends on:** P03; D1 runtime approval.

1. Implement typed control operations and peer/deployment authentication.
2. Implement artifact fetch/verification, staging, digest ownership labels and a
   read-only observed-state query. Do not expose arbitrary Docker operations.
3. Implement one-container Linux Docker adapter with safe mounts, Unix sockets,
   no network, non-root/capability/resource controls and bounded logs.
4. Verify rootless and supported rootful host profiles. If a quota/isolation
   requirement is unavailable, return an explicit incompatibility error.
5. Implement readiness/start/stop/deadline handling and a circuit breaker for a
   crash loop. Store sanitized diagnostic evidence without app secrets.
6. Add runtime availability/architecture/disk/resource preflight in management.

**Files:** `cmd/app-runner`, `internal/apprunner/{control,driver,artifacts,reconcile}`
are proposed ownership groups, not mandatory one-file-per-function directories.
**Tests:** RUN-01..RUN-08. **Exit:** an intentionally hostile fixture cannot access
host Docker, another app's data, direct network or undeclared mounts.

## P05. Static and managed installer vertical slice

**Depends on:** P02..P04.

1. Add plan/commit/progress handlers and API DTOs from contract section 6.
2. Produce the plan server-side; recheck its revision/trust at commit.
3. Implement durable per-installation operations and stage reconciliation.
4. Stage frontend/backend artifacts; automatically establish managed registration
   with installation/generation pins. Keep tokens/keys out of browser payloads.
5. Activate only verified tuples and display needs-configuration honestly.
6. Keep legacy installer/store behavior separate. Do not reuse `InstallApp` as an
   update transaction: it currently resets registration and replaces live assets.

**Files:** proposed `internal/app/app_platform_{api,store,lifecycle}.go`, route
wiring in `routes.go`, client/types in `web/src/api`, management progress components.
**Tests:** LIFE-01..LIFE-05. **Exit:** both neutral package kinds install with one
review, no app-specific host patch and no manual key/address/container step.

## P06. Seamless host shell and SDK v2

**Depends on:** P01/P02; v2 frontend fixture can precede managed runtime readiness.

1. Create an app-shell component owning title/actions/breadcrumb/history.
2. Implement nonce/channel-bound handshake and initial presentation before paint.
3. Implement document-height coalescing/limits and viewport-editor mode.
4. Implement route registry, browser back/forward, deep links and draft guard.
5. Add SDK root/hooks/state/error/actions and shared forms/typography/layout.
6. Prevent duplicate headers/theme/language controls in maintained templates.
7. Keep legacy AppView path functional. Do not relax iframe sandbox flags, CSP or
   host-only token handling to solve styling problems.

**Files:** existing `AppView.tsx`, `appBridge.ts`, `AppsHub.tsx`, `AppLayout` routing;
new shell/protocol components; Apps `packages/app-sdk`, generator and templates.
**Tests:** UX-01..UX-10, SEC-03/04. **Exit:** refresh and theme changes produce no
visible light frame or duplicate title; drafts/history/focus survive navigation.

## P07. Capability broker, app access and credentials

**Depends on:** P03/P04; agreed capability/trust policy.

1. Add required/optional permission review and effective-grant resolution.
2. Add app-use user/OU access separately from business-resource permissions.
3. Reuse current account/OU generation rules for foreground dispatch; add explicit
   service workload assertions with leases for background work.
4. Implement deny-by-default broker HTTP access and, before claiming stream
   support, the restricted stream adapter and negative tests.
5. Implement vault binding, redacted metadata, broker-side secret use, rotation,
   revocation and exceptional raw-read permission handling.
6. Fence all calls by installation/generation/capability revision and recheck
   current permission at admission and side-effect commit where applicable.

**Files:** extend generic `extension_*` authority through narrow adapters; new
`internal/appplatform` policy types and `internal/apprunner` broker modules.
**Tests:** AUTH-01..AUTH-08, SEC-05..SEC-10. **Exit:** denied optional features fail
on the server; revoked/deleted/recreated principals cannot retain prior access.

## P08. Two neutral reference applications

**Depends on:** P05..P07.

Implement in the Apps repository, not Portal business packages:

- **Notes sample:** static frontend, shared shell, user-scoped file storage,
  navigation and a draft. No external network or custom backend.
- **Scheduled Digest sample:** independent backend, fake feed/billing-free data,
  secret binding to a local fixture destination and a scheduled idempotent handler.

Use both as tutorials and complete-package test fixtures. A sample may not call
administrator endpoints with a session or rely on undocumented host imports.
**Exit:** a clean supported test deployment installs both from a test catalogue,
opens them, persists a draft/data and reports backend health without manual pairing.

## P09. Shared tasks, events and notification integration

**Depends on:** P03/P07; P08 supplies the handlers.

1. Implement dedicated app job workers, leases, admission quotas and timeout/retry.
2. Implement authorized event subscriptions with stable occurrence IDs and replay
   semantics. Keep raw global streams inaccessible to app callers.
3. Bind notification publication to existing ADR 0038 source/routing authority.
4. Add pause/review/resume interfaces and safe task/error diagnostics.
5. Test side-effect acknowledgement loss, cancellation and stale generation fencing.

**Tests:** JOB-01..JOB-08. **Exit:** closing the browser does not stop approved work;
disabling an app stops further admission; duplicate delivery does not duplicate the
fixture side effect; report queue and authentication mail retain capacity.

## P10. Update, disable, uninstall and verified recovery

**Depends on:** P05/P07/P09.

1. Implement the lifecycle algorithms, schema compatibility and recovery checkpoints.
2. Build fixture versions A/B/C: compatible update, migration update and intentionally
   failed migration/readiness. Supply explicit schema read/write compatibility.
3. Preserve installation identity on normal v2 update while revoking old generations.
4. Implement retained-data reinstall with explicit namespace/grant review.
5. Implement coordinated Portal/runner/app backup and isolated restore checks.
6. Implement disable/enable/repair, orphan fencing and separate irreversible cleanup.

**Tests:** LIFE-06..LIFE-15, DATA-01..DATA-08, OPS-01..OPS-08. **Exit:** every crash
boundary has a deterministic tested recovery or an honest review-required state.
No app-originated migration can bypass the platform's data/egress restrictions.

## P11. Marketplace, publication and developer tooling

**Depends on:** P08..P10 and trust/package contract freeze.

1. Add v2 catalogue entries without removing legacy catalogue compatibility.
2. Implement app detail, compatibility, update permission diff, operation resume,
   retained data and runtime diagnostics using P01-approved UI.
3. Add authoring generator, fake platform/OU/capability fixtures, lint/validator,
   pack/inspect commands and SDK compatibility tests.
4. Build a guarded publisher with exact-source CI, signed descriptors, immutable
   assets, image-digest verification, provenance/dependency inventory and public
   download re-verification. Signing credentials are absent from ordinary PR jobs.
5. Publish the neutral apps to an acceptance catalogue only after actual artifacts
   exist. Promote to the real market after final release gates.

**Tests:** PKG-01..PKG-10, DEV-01..DEV-05. **Exit:** someone following only the
handbook can create, test, package, review and install a new sample app.

## P12. Full acceptance, review and capacity evidence

**Depends on:** P11.

Run every case in the acceptance matrix on supported Linux runtime profiles,
both Portal DB drivers and released-version fixtures. Review host privilege,
publisher trust, network policy and restore behavior. Record capacity defaults
against a named machine/workload; do not extrapolate arbitrary scale.

Include malicious packages, DNS rebinding, forged frame messages, lost replies,
disk full, duplicate runners/writers, revoked users/publishers, task saturation,
Portal/runner/app outages and coordinated restore. Recheck unrelated Portal health,
login/report reads/run-queue reads throughout, not only after tests finish.

**Exit:** all required cases have authoritative evidence and known limits; no
pending required tests are described as optional deployment work.

## P13. Release and rollout

**Depends on:** P12 and D6 rollout authorization.

1. Finalize accepted ADRs, API/schema artifacts, upgrade/recovery guide and notes.
2. Release platform/runner/SDK with an explicit compatibility matrix.
3. Provision the runtime in a staging deployment once; install both public released
   reference packages through the marketplace and verify actual served bytes.
4. Perform real release A-to-B-to-recovery installation drills against those bytes.
5. Back up production using its verified driver/config, validate restore in isolation,
   then deploy through the existing beta channel without rewriting server version
   configuration to fixed tags/digests. Digests remain verification evidence.
6. Enable managed apps deliberately; existing legacy apps remain untouched.

**Exit:** signed release artifacts, channel evidence, operator receipts and a full
go/no-go record. Do not use development binaries as released-version evidence.

## P14. Optional legacy adoption after platform delivery

**Depends on:** P13 and an app-specific reviewed adoption plan.

Inventory each externally managed app; prove ownership/config/data/key compatibility;
test backup and recovery; prepare a no-duplicate-writer cutover; review identities
and grants; then perform supported adoption. Status is one later consumer. Do not
turn this work package into an exception permitting arbitrary container takeover.

## 3. Checks and PR size

Existing Portal checks, from Portal root:

```sh
go build ./...
go vet ./...
go test ./...
```

From `web/`:

```sh
npm ci
npm run lint
npm run typecheck
npm run test
npm run build
```

Use focused tests during development; full required CI at integration/release.
Run PostgreSQL integration with a dedicated disposable `TEST_POSTGRES_DSN` using
the repository's existing CI service. Never point test commands at production.

Existing Apps checks, from Apps root:

```sh
npm ci
python3 scripts/plugin-standard.py
npm run typecheck
npm run test:sdk
python3 scripts/test-app-template.py
python3 -m unittest discover -s scripts/tests -v
```

New runner/contract packages will join `go test ./...`; add dedicated Docker
integration and browser jobs to CI. The future CLI/tool names in the handbook are
requirements to implement, not commands available today. Pin supported toolchains
in repository manifests and use those versions rather than a guessed latest tool.

Each PR states problem/result, affected contracts, storage decision reference,
tests/evidence and recovery impact. A protocol change must update SDK, schema,
fixtures and docs together. A reviewed main commit and green CI are prerequisites
for release; neither is authorization to mutate production configuration.
