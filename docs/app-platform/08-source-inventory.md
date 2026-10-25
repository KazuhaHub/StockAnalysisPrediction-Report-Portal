# Source inventory and legacy compatibility map

Inspected against Portal commit
`d84f138e483034e12395359bb976dc1b9027ac51`. These are source observations, not
new runtime tests, security certification or approval to migrate existing apps.
Recheck functions and route gates before editing because source can advance.

This is the initial inventory deliverable of P00. P00 is still incomplete:
architecture/trust decisions, physical storage approval and protocol review remain.

## 1. Source map for the first developer

| Open this source | Find this behavior | v2 implementation instruction |
| --- | --- | --- |
| `internal/app/apps_api.go`: `appManifest` | Legacy ID/name/icon/version/entry/scopes manifest | Keep parser/version separate; do not accept v2 executable fields as ignored legacy metadata |
| Same file: `parseAppBundle` and bundle limits | Legacy upload/extraction guard, 8 MiB compressed, 16 MiB expanded, 200 files | Reuse tested safe-path ideas; v2 must define separate reviewed bounds and complete artifact validation |
| Same file: `apiAppInstall` | Preview parses without persisting; actual installation then revokes tokens and invalidates transport | New v2 review plan must bind exact descriptor/policy revision, not just parsed scopes |
| `internal/app/apps_store.go`: `InstallApp` | Replaces app/file records in one DB transaction and calls `resetExtensionConnection` | Leave unchanged for legacy; never call it as the complete managed-update algorithm |
| Same file: `DeleteApp` | Removes connection/files/app registration | v2 uninstall must first revoke work/authority and account for retained data and runner acknowledgement |
| `internal/app/apps_api.go`: `apiApps` | Returns the installed list for any logged-in user | Add a distinct v2 current-viewer access filter; hiding an app card alone is insufficient |
| Same file: `appAssets` | Public static assets, no secret payload, no-cache and response sandbox rules | Define v2 release/digest asset policy explicitly; never put credentials/private business DTOs in assets |
| Same file: `apiAppToken` | Grants known scopes and drops write scopes for non-admin users | v2 needs capability/resource/current-viewer authority, not a renamed broad scope |
| `internal/app/apps_token.go` | In-memory app-bound token entries, 30-minute legacy TTL and no `all` scope | Preserve host-only tokens; v2 generation/epoch fencing must be checked at actual dispatch |
| `internal/app/apps_market.go` | Fetches catalogue, compares reviewed manifest declarations, retains configured override | Extend through a versioned signed descriptor path; choosing an index is not publisher trust |
| `internal/app/extension_api.go` | Backend management DTOs hide key material and use revision checks | Reuse safe DTO/revision principles; managed registration is an internal lifecycle effect |
| `internal/app/extension_store.go` | Migration 0008 stores one sealed external connection per installed app | Do not overload this record into an unreviewed entire runtime database |
| `internal/app/extension_identity.go` | Current account and opaque OU/user identity derivation | Reuse verified identity semantics through explicit interfaces; preserve recreation refusal |
| `internal/app/extension_principals.go` | Administrator directory for an app namespace | Use normal authorized directory resolution; app-supplied numeric IDs are not grant evidence |
| `web/src/pages/AppView.tsx` | Scoped token host, parent-source check, theme/locale posting and fixed-height frame | Introduce negotiated v2 shell/document sizing; keep legacy hosting isolated |
| `web/src/lib/appBridge.ts` | Allowed bridge request validation and theme payload | Add v2 typed channel protocol without broadening legacy paths |
| `web/src/pages/AppsHub.tsx` | Built-in cards and installed app hub | Keep built-in permission behavior; add authorized v2 lifecycle states and links |
| `web/src/pages/manage/AppsAdminPage.tsx` | Upload/market permission review; installed badge rather than an upgrade lifecycle | Build plan/progress/update/retained-data views; do not require uninstall to update |
| `web/src/App.tsx` | `/apps/x/:id` route and management application route | Register v2 deep routes intentionally and preserve legacy bookmarks |
| `internal/app/routes.go` | Central route registration and authentication gates | Add management/current-viewer routes here with existing session/enrollment conventions |
| `internal/app/notification_publish.go` | Source-bound publication and occurrence deduplication | Add managed workload integration to this authority; no second notification service |
| `internal/app/migrate_steps.go`, backup tests | Append-only migration products and restore compatibility | Add only approved new steps and backup products; leave frozen baseline untouched |

## 2. Important authority differences

### Legacy query is not a v2 user-scoped report permission

`apiAppToken` explicitly drops legacy `query` for restricted external viewers
because that bearer scope is not an OU-bound report projection. The current
source comment calls this a fail-closed protection. Do not remove the protection
or map `reports.read` to the legacy token simply to make a sample work.

The v2 report capability needs a viewer-bound handler checking current report
visibility at read time. Background reports need a separate explicit resource
grant and service identity. A signed foreground identity does not create a
perpetual background read permission.

### Old app-use visibility is broader than the v2 promise

Legacy `apiApps` lists installed apps to logged-in users. It does not implement
the proposed per-OU app-use policy. V2 list/count/detail/deep-link/request gates
must use one current-viewer policy resolver. Existing business resource grants
cannot stand in for application-use permission, or vice versa.

Static code assets currently are public and contain no secrets. Code publication
does not authorize revealing installed state, user data or grants. At D3 specify
whether v2 executable assets are public immutable release artifacts or protected
assets and how the null-origin sandbox loads them safely. Do not describe existing
public asset delivery as a private-data boundary.

### Normal update identity is a new contract

Legacy `InstallApp` deliberately resets registration. V2 managed updates preserve
installation identity while rotating the active generation, as a reviewed
amendment to ADR 0040. A v2 implementation must prove both new continuity and old
legacy revocation. Silently changing the old function would change reinstall and
restore behavior for already deployed apps.

## 3. Reuse versus replace checklist

Reuse through tests and narrow adapters:

- Existing session/current-account/enrollment gates.
- Opaque account/OU creation-generation semantics.
- Purpose-sealed key handling and redacted administrative DTOs.
- Extension assertion/body/boot-binding principles.
- Existing notification capture/routing authority.
- Safe path/download validation concepts and legacy regression corpus.
- Shared React/TypeScript/Ant Design build foundation.

Add, rather than pretending existing code provides it:

- Signed complete packages and trusted publisher namespaces.
- Durable install/update operations and per-installation fencing.
- Runner enrollment, container isolation and capability broker.
- Stable managed update identity plus generation-bound revocation.
- App-use OU policy and viewer-bound report capability.
- Independent tasks/events/service authority and coordinated recovery.
- Seamless route-aware shell, foreground presentation handshake and lifecycle UI.

Do not replace the existing report/forecast/run queue or notification engines to
build these capabilities. Generalization should start at their documented API
boundaries, with independent executor capacity for app workloads.

## 4. First implementation PR checklist

Before writing runtime code, the developer should be able to answer:

1. Which operation is legacy and which is v2? Which version discriminator selects it?
2. Which source function makes the current authority decision for this request?
3. What exact artifact/policy revision did the administrator review?
4. What prevents a repeated POST or lost reply from repeating an external effect?
5. What fences an old app document, process, worker or restored controller epoch?
6. Which record commits this stage, and what happens if that commit fails?
7. Which migration/storage approval applies, if any durable layout is introduced?
8. Which acceptance IDs prove both success and refusal/recovery behavior?

If an answer is missing, finish the relevant design fixture/decision first. A
passing legacy test is not an answer about a v2 behavior it never exercises.
