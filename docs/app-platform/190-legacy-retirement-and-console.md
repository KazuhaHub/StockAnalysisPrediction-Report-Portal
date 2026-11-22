# Retiring the legacy app system and one apps console

Status: Accepted 2026-11-21 (owner decisions D1–D9 below). Supersedes the compatibility promises
for v1 apps in [08](08-source-inventory.md), [165](165-legacy-adoption-boundary.md),
[ADR 0003](../adr/0003-downloadable-apps.md), [ADR 0040](../adr/0040-external-application-backend-connections.md)
and [the market migration note](../app-market-migration.md). Extends [ADR 0048](../adr/0048-app-order-and-ou-access.md)
(per-OU app access) to platform installations and replaces the install-time audience of
[60](60-installation-confirmation-ui.md). Builds on [189](189-built-in-official-sources.md).

## 1. What is retired

The original app system ("v1"): ZIP packages uploaded or installed from the `index.json` market,
served as sandboxed iframes under `/app-assets/`, calling `/api/v1` with a short-lived bridge token,
plus viewer-bound external backend connections and `/api/v1/extensions/*`.

| Kind | Items |
|---|---|
| Routes | `GET /api/apps` (replaced, section 4), `POST /api/apps/{id}/token`, `POST /api/admin/apps/install`, `GET /api/admin/apps/market`, `POST /api/admin/apps/market/install`, `POST /api/admin/apps/market/index`, `DELETE /api/admin/apps/{id}`, `GET/PUT/DELETE /api/admin/apps/{id}/backend`, `GET /app-assets/{id}/{path...}`, `POST /api/v1/extensions/{id}/request`, `GET /api/v1/extensions/{id}/principals` |
| Code | `internal/app/apps_api.go`, `apps_market.go`, `apps_store.go`, `apps_token.go`, `extension_api.go`, `extension_store.go`, `extension_principals.go`, `extension_identity*.go`; the bridge branch of `tokenOK` and of `notificationPublisher` |
| Web | the legacy Apps tab, `AppInstall`, `AppBackendModal`, `LegacyAppsAdminPage` (already unused), `AppView` and `lib/appBridge.ts`, the `/apps/x/:id` route |
| Tables | `apps`, `app_files`, `app_backend_connections` (dropped in R2, section 5) |
| Settings | `meta.app_market_index_url` |
| Scopes | `query`, `ingest`, `notify`, `extension.read`, `extension.manage` as bridge scopes. Persistent API tokens keep their own scopes. |
| Apps repository | root `index.json`, `apps/demo-symbols`, the v1 Status builders (`scripts/build-status-*.py`, `release-status.yml`), `catalogue.py export-legacy`. `packages/extensionprotocol` stays: the platform imports it. |

Production on 2026-11-21 had one v1 app: `status` 0.1.0-beta.3, with one external backend
connection and a running container `report-portal-status`.

## 2. Decisions

| # | Decision |
|---|---|
| D1 | Batch, Scheduled tasks and Quotes stay core Portal features, not platform apps. Readers still see them as tiles on `/apps`. |
| D2 | Their per-OU switch is enforced by the server on the routes that belong to the feature alone. Only Scheduled tasks has such routes: every `/api/admin/batch/recurring*` route answers `403 {"code":"app_closed"}` when it is closed. Batch's routes (targets, presets, tickets, run quota, the queue and job actions) also serve the header run dialog and the shared queue page, and Quotes' routes serve the reading pages, so neither is gated: closing Batch or Quotes hides its entry. Closing Scheduled tasks does not pause schedules that already exist; they keep running. |
| D2a | Administrators are bound by the switches like everyone else (ADR 0048's administrator exemption ends). No switch can close the console pages where switches are edited: the OU policy pages and every app's access settings stay open to administrators. |
| D3 | `demo-symbols` is dropped. |
| D4 | R2 drops `apps`, `app_files`, `app_backend_connections` and `app_platform_access_grants`, after a read-only row check and a backup, in one transaction. Restoring an older backup skips those tables with one log line. |
| D5 | v1 Status is retired without waiting for Status v2. Status is unavailable until v2 is published and installed. No v1 data is migrated (no name-based adoption, as in 165). |
| D7 | Who can use a platform installation is an OU policy, the same one ADR 0048 gives built-in features: key `platform:<installation_id>` in `app_ou_access`, inherited down the tree, nearest explicit row wins. A platform key's baseline is **closed** (a built-in key's stays open); the baseline is chosen by key kind in code, with no synthetic rows. Grants are per OU only; per-user grants are dropped. It is an ordinary policy edit (immediate, audited as `app.access`), not part of the install review, which keeps capabilities, network, quota and storage. `app_platform_access_grants` is no longer read or written. |
| D8 | The "development preview" notice is removed. |
| D9 | Installing is a page of its own, `/manage/apps/install`. |

## 3. Releases

- **R1 (next beta):** the console redesign (section 4) and the retirement (section 1) ship together.
  v1 tables are left untouched and unread, so returning to the previous image restores v1 apps.
- **R2 (next major baseline):** the three tables are dropped (D4).

No intermediate deprecation release: the only installation is the owner's.

### Operator checklist for R1 (production)

1. Before deploying, read-only: `apps` still holds only `status`.
2. After deploying: `/apps` no longer lists Status; `/apps/x/status` shows the retired-app page.
3. Stop and remove the v1 backend container `report-portal-status`
   (`/data/report-portal/status-release-0.1.0-beta.3/compose.json`). Portal never managed it.
4. Notification rules on `extension.app.status.notice` show their source as retired.
5. Install Status v2 from the official catalogue once it is published.
6. Platform installations are closed to everyone after R1 (D7: a platform key's baseline is closed and the
   old install-time audience is no longer read). Before deploying, list every existing installation
   (`SELECT id, app_id, desired_state FROM app_platform_installations WHERE desired_state<>'removed'`).
   After deploying, open each one per OU on its access page (`/manage/apps/platform:<installation id>/access`);
   until then nobody can use it, administrators included.
7. Deploy R1 only with no platform lifecycle work in flight: no unconsumed review plan and no incomplete
   operation (`SELECT id, action, stage FROM app_platform_operations WHERE completed_ms IS NULL` returns
   nothing). R1 changes the evidence digests those carry, so an in-flight plan or operation fails closed
   and needs a new review; a managed update caught after its runner start would land in recovery.

## 4. The apps console

`/manage/apps` has four tabs, by task:

1. **Apps** (default). One table of every app: the three built-in features and every platform
   installation. Columns: app, state (`StatusTag`), who can use it, version. Actions: Reorder, Install
   app. The order applies to both kinds and is the order readers see on `/apps`. A row with an
   operation in progress shows its progress.
   - **App page** (`/manage/apps/:key`). Built-in: who can use it (the inherited OU switch). Platform:
     summary; lifecycle actions (update, disable, enable, uninstall), each through its existing review;
     who can use it (the OU tree with one inherit field, D7); configuration and credentials; permissions and network panels;
     this installation's tasks, event subscriptions and recent operations.
2. **Sources and trust**: catalogues (the official one and custom URLs) and publishers with keys.
3. **Runtime**: the application runner, image storage status, capacity.
4. **Activity**: operations, tasks and event subscriptions across apps, filterable by app, each
   row linking to its app page.

**Install** (`/manage/apps/install`) is a catalogue page, declared before `apps/:key` in the router. It has a
breadcrumb (Apps › Install app), a source choice (the official catalogue, or a custom catalogue URL or an offline
package from the top bar), and a grid of compact cards for the apps that are not installed. A card has a small
Install button; the card itself opens a detail dialog (version choice, Install, the version list, the publisher, and
a note that the publisher's signature is verified on install). Install downloads and verifies the chosen version,
then opens the install dialog, whose `Steps` are: open it now to some organisational units (optional, written in
the activation transaction; none leaves it closed to everyone), review (the existing review content, the confirm
button says "Install"), and progress (polled, stages shown; when complete "Open app settings" and "Back to apps").
The same dialog runs update, disable, enable and uninstall from the installed cards, without the first step. There
is no "unverified" warning: a catalogue is a list, and every package is verified against its publisher's signature
when chosen.

The Apps tab lists what is installed as compact cards (or a dense list, chosen with a toggle and remembered in the
browser) and has a plain Install app button that opens the catalogue page.

Labels lead with the verb ("Update", not "Review update").

### Unified list and order

- `app_order` keys gain the form `platform:<installation_id>` beside `builtin:<slug>`. The column is
  already text; no schema change.
- A reader directory endpoint returns built-in features and platform installations in that order,
  each with whether it is open to the caller. It replaces `GET /api/apps`.
- Every place that reads the install-time audience today reads the OU policy instead: the reader
  directory, launch, the status endpoint, the managed backend's viewer identity, notification
  recipients and the principal directory ([181](181-principal-directory.md)). Deleting an OU deletes
  its `app_ou_access` rows in the same transaction (ADR 0048), so a reused OU id never inherits a grant.
- Uninstalling deletes the installation's `app_order` and `app_ou_access` rows in the uninstall transaction.
- Production on 2026-11-21 had no completed platform installation, so no audience rows are converted.

## 5. Data

- R1 neither reads nor writes `apps`, `app_files`, `app_backend_connections` or
  `meta.app_market_index_url`. Rows keyed by a v1 app id in `app_order` and `app_ou_access` are
  ignored.
- R2 drops the three v1 tables and `app_platform_access_grants` (owner-approved 2026-11-21), and
  clears the orphaned v1 keys and the meta row, in the transaction of D4.
- Audit rows with `app.install`, `app.delete` and `app_backend.*` keep their labels; no new ones are
  written.

## 6. Order of work

| PR | Content |
|---|---|
| P0 | Shared alert spacing (frontend guidelines) |
| P1 | This document and the superseded-by pointers |
| P2 | Server: unified directory and order including platform installations; platform access as OU policy (D7); the Scheduled tasks switch on its routes and the administrator lock-out guard (D2, D2a) |
| P3 | Apps tab and app page, lifecycle actions moved there |
| P4 | Install page |
| P5 | Sources and trust, Runtime, Activity tabs; preview notice removed |
| P6 | Retirement: code, routes, web, i18n keys, retired-app page, OpenAPI, tests; Apps repository clean-up in its own PR |
| P7 | R2: drop the tables (owner confirms again before it runs) |

Each UI PR is shown running before it is opened.

## 7. Acceptance

Each becomes a test; the direction a guard fails is stated.

- **A1** After P6 the retired routes in section 1 answer 404. Fails closed.
- **A2** A bridge-shaped bearer token is refused; persistent API tokens and machine read tokens work as before.
- **A3** A database holding v1 rows starts under R1 unchanged; readers do not see those apps; `/apps/x/<id>` shows the retired-app page.
- **A4** Under R1 the v1 tables are neither read nor written (SQLite and PostgreSQL), so the previous image still serves them.
- **A5** The admin Apps list holds the three built-in features and every platform installation; after a reorder, `/apps` shows the same order.
- **A6** With Scheduled tasks switched off for an OU, every recurring route answers 403 to that OU's users, administrators included, and existing schedules still fire. No route the web code calls outside the Scheduled tasks console carries a feature gate (checked against the callers in `web/src`). Fails closed on the gated routes.
- **A6a** An administrator in an OU where every feature and every app is closed can still open the OU policy pages and every app's access settings, and re-open them.
- **A7** A new platform installation is closed to everyone until an OU row opens it; built-in features with no rows stay open. An OU opened at install time is open the moment activation commits.
- **A7a** Launch, directory, status, viewer identity, notifications and the principal directory all follow the OU policy and agree with each other for the same user; `app_platform_access_grants` is neither read nor written.
- **A8** Install: a later step cannot be used before the earlier one is done and says why; a version that failed verification cannot reach step 3; a reload resumes the operation in progress.
- **A9** Opening the install page shows no warning- or error-level notice.
- **A10** All new copy in zh-CN, zh-TW and en-US; no sideways scroll or overlap at 320/375/768/1280, light and dark, reduced motion.
- **A11** After R2, restoring a backup that holds the four dropped tables skips them with a log line and restores everything else. Skips, does not fail.
- **A12** A notification rule on a retired app's event shows its source as retired and is kept.
