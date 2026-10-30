# ADR 0048 — App order and per-OU app access

**Status: Proposed.** 2026-10-29. Builds on ADR 0003 (downloadable apps), ADR 0022 (OU tree), ADR 0035
(migrations after a frozen baseline) and ADR 0046 (the OU policy model this one reuses).

## Context

The Apps hub lists the compiled-in apps first and the installed apps after them, newest installed
first, and every signed-in user sees every app they hold the permission for. Administrators have two
requests the hub cannot meet: put the apps in an order that suits the organization, and open or close
an app for an organizational unit (a research desk should not see the batch console; one team should
get a purpose-built installed app that nobody else needs).

Both are per-installation settings, so they belong in the database and in the web UI, not in config.

## Decision

### Keys

An app is addressed by its **key**: `builtin:<slug>` for a compiled-in app (`batch`, `recurring`,
`quotes`), and the installed app's id otherwise. This is the entry-button pin convention
(`web/src/lib/builtinApps.ts`); it cannot collide, because an installed id never contains a colon.

### Schema (owner-approved 2026-10-29)

Migration step `0014`, `restoreRule{olderDumpOK: true}`, two new tables on both drivers. The frozen
baseline (`baseSchemaStmts()`) is untouched.

```sql
CREATE TABLE app_order(
  app_key  TEXT PRIMARY KEY,   -- 'builtin:batch' | installed app id
  position INTEGER NOT NULL);
CREATE TABLE app_ou_access(
  app_key  TEXT NOT NULL,
  group_id INTEGER NOT NULL,
  enabled  INTEGER NOT NULL,   -- 1 open, 0 closed; no row = inherit
  PRIMARY KEY(app_key, group_id));
```

No rows means every app is open and ordered as today, so applying the step changes nobody's view and
an older dump restores into two empty tables. Both tables are in the backup because the backup is
derived from the migration list (`backupTableNames`), so a backup round-trips them with no further
change.

### Semantics

- **Access** is resolved per user by walking the user's OU up the tree to the Default OU: the nearest
  explicit row wins, and no row anywhere means open. It uses the shared resolver
  (`nearestOUPolicy`, baseline `true`) so the console can show own value, effective value and source
  OU exactly as it does for the other OU policies. A user with no OU is governed by the Default OU.
  Like the export policy, it is not sticky: a sub-OU may open an app its parent closed.
- Administrators (`PermManage`) are exempt, always seeing and opening every app, so they can manage
  them. This is the same exemption as ADR 0046.
- Access is an **additional** gate. A built-in app's own permission (`run_batch` for batch and
  recurring) still applies on top of it.
- **Order** is one list shared by built-in and installed apps: ascending `position`; an app with no
  row comes after every ranked app, in today's order (built-in apps in registry order, then installed
  apps as `ListApps` returns them). The ordering endpoint replaces the whole list.
- **Cleanup.** Uninstalling an app deletes its rows in both tables, in the same transaction as the
  uninstall. Deleting an OU deletes its `app_ou_access` rows, in the transaction that deletes the OU.
  Re-installing an app (an upgrade of the same id) keeps its rows: it is the same app. Setting an
  override to `null` deletes the row; on the Default OU that means open.

### Enforcement, and what it cannot do

**Installed apps are enforced by the server.**

- The apps listing returns only the apps open for the caller, in order.
- `POST /api/apps/{id}/token` answers `403 {"error":"app_closed","code":"app_closed"}` for a closed
  app and mints nothing.
- **Tokens already minted are cut off too** (a token lives 30 minutes). Two mechanisms, because
  neither covers the other. (a) Any access change for an app revokes that app's outstanding tokens
  (`revokeApp`), so an open frame's next call fails and its re-mint answers `app_closed`. (b) Every
  request authenticated by an app token re-checks access for the token's recorded app and user: the
  bridge credential check (`tokenOK`, behind every `/api/v1` read) and the backend-connection proxy
  (`v1ExtensionRequest`). That is what cuts off a user moved into a closed OU, or an access row
  changed without going through the API. The bridge check refuses only on a definite "closed"
  answer, leaving an unreadable policy to the handler behind it, which needs the same store; the
  proxy, which forwards to a backend, fails closed.
- `GET /app-assets/{id}/{path}` answers the same 403 when the request carries a session whose OU has
  the app closed. The route is public by design (the bundle holds no secrets) and the sandboxed
  frame's own sub-resource requests carry no cookie, so a request with **no session cannot be
  attributed to an OU and is still served**. The token mint is the gate that matters; the 403 on the
  asset route stops the navigation request, which does carry the cookie, and the SPA never builds
  the frame for a closed app because the mint is refused first.
- An unreadable policy refuses (503) rather than admits.

**Built-in apps cannot be gated by the server.** The compiled-in apps have no server-side object to
close: the quotes endpoints are shared with the reading pages and with the entry search, and the batch
and recurring APIs are already gated by `run_batch` and used by pages other than the app. Blocking
them per OU would break those pages. So for built-in apps the server **reports** which keys are open
and the SPA hides the closed ones: the hub card, the entry button pinned to it, every other link that
advertises it (the header run menu's batch item, the "open in app" quote link on the reading page, the
quote-only favourite card, which becomes a plain card), and the route, which shows a not-available
result instead of the console (`RequireAppOpen`). A reader who types the URL of
a closed built-in app, or calls its API directly, is not stopped by this feature. That is the stated
limit: closing a built-in app is a visibility control, not a security boundary, and the console says so
under the access card. A reader's real limits on those APIs remain their permissions and, for quotes,
nothing more than any signed-in reader already has.

The SPA's answer fails open: if the list cannot be read the built-in cards and buttons stay, because
the server still refuses what it can refuse.

**Intentional change to entry buttons.** The list is the whole set of apps the reader may open, so an
entry button pinned to an app that is not installed, or to a built-in slug that does not exist, is now
hidden for everyone. Before, it was shown and led to a not-found page. (`appAccess.test.tsx`)

Writes validate the key inside their own transaction (`requireApp`): a built-in key against the
compiled list, an installed key against the `apps` row, locked `FOR UPDATE` on Postgres, the lock
`DeleteApp` takes first. A concurrent uninstall therefore either completes before the write (the key
is refused, 404 or 400) or waits for it (and then deletes the rows it wrote); no orphan row is left.
Clearing a row never needs the app to exist.

### Audit

Two new actions, each recorded with `recordChange` in the `{"field","from","to"}` shape of ADR 0046:

- `app.access` — target `app/<key>`, detail `{"field":"access","ou":<id>,"from":null|bool,"to":null|bool}`,
  written only when the stored value actually changes.
- `app.order` — detail `{"field":"order","from":[keys...],"to":[keys...]}`, where both lists are the
  resolved order readers see (ranked apps, then the rest). Written only when that resolved order
  changes, so repeating the list already in effect is not an event.

## API

Reader-facing, cookie session. `GET /api/apps` keeps `apps` (installed apps, now filtered and
ordered, same fields) and adds `builtin_apps` (open built-in slugs, in order) and `order` (both kinds
as one key sequence; the hub interleaves with it). An older SPA that reads only `apps` keeps working.

Admin, `requireAdminJSON`:

- `GET /api/admin/apps/layout` → `{"apps":[{key,name,builtin,icon?,access:[{ou,own,effective,source_ou,inherited}]}]}`,
  all apps in order; `own` is `true`, `false` or `null`, and `source_ou` 0 is the open baseline. A
  built-in app's `name` is its slug; the SPA translates it.
- `PUT /api/admin/apps/order` body `{"keys":[...]}` → full replacement. An unknown or duplicate key,
  or a missing list, is 400 and writes nothing. Keys left out lose their position and fall to the end.
- `PUT /api/admin/apps/{key}/access/{ou}` body `{"enabled": true|false|null}` → tri-state. An invalid
  value is 400; an unknown app or OU is 404.

## Interface

The Apps administration page gains two sections above the install list. **App order** is a
drag-to-reorder list of every app (`@dnd-kit`, persisting on drop, like the entry buttons).
**Access by organizational unit** is modelled on the reader export policy: an OU tree on the left; on
the right, one `InheritField` per app showing its own value or "inherited from <OU>", the effective
value and the source, with an open/closed switch. The hub, entry buttons and routes read one
`AppAccessProvider` answer.

## Acceptance conditions (each becomes a test)

1. Step `0014` adds exactly the two tables and five columns on both drivers; re-running it is a no-op;
   a database missing a table the ledger records is refused; the frozen baseline does not declare
   them. (`apps_schema_test.go`; Postgres store test gated on `TEST_POSTGRES_DSN`.)
2. An older dump restores into empty tables and records the step; a backup of rows round-trips.
   (`TestMigration0014OlderDumpRestoresWithEmptyTables`, `TestBackupRoundTripCarriesAppOrderAndAccess`.)
3. With no rows every app is open for everyone, built-in apps first. (`TestAppsWithNoRowsEverything...`)
4. Resolution: inherit from a parent, override at a child, nearest row wins, `null` returns to
   inheriting, the Default OU row reaches OUs with none, no rows is open, administrators are exempt.
   (`TestAppAccessResolution`, `TestAppAccessRowsAreNotRequiredForAnUnassignedUser`.)
5. A closed installed app leaves the listing and its token mint and asset are 403 `app_closed`;
   reopening restores them; administrators are unaffected. (`TestClosedInstalledApp...`)
6. An anonymous asset request is still served, as documented. (`TestAnAnonymousAssetRequestIsStillServed`)
7. A closed built-in key is absent from `builtin_apps` and `order` for the OU and present for an
   administrator. (`TestClosedBuiltinKeyIsReportedNotServerGated`)
8. The order endpoint validates keys, replaces the whole list, and unranked apps follow in today's
   order. (`TestAppOrderPutValidatesAndReplaces`, `TestOrderAppKeys`)
9. The access endpoint validates its input, is tri-state, keeps one row per (app, OU), and `null`
   deletes. (`TestAppAccessPutValidation`, `TestAppAccessPutIsTriStateAndReplacesTheRow`)
10. The layout reports own, effective and source OU. (`TestAdminLayoutIsOrderedAndShows...`)
11. Uninstalling deletes the app's rows, a re-install keeps them, deleting an OU deletes its rows.
    (`TestUninstallingAnAppDeletesItsRows`, `TestReinstallingAnAppKeepsItsRows`,
    `TestDeletingAnOUDeletesItsAccessRows`)
12. Both changes are audited, once, and only when something changed. (`TestAppChangesAreAudited`)
13. Hub: built-in and installed apps interleave in server order, closed built-ins are hidden, the
    built-in permission still applies, and an older server's answer shows today's list.
    (`AppsHub.test.tsx`)
14. Admin: the order persists on drop as the whole key sequence; the access control shows own value,
    inherited value and source OU, sets an override, and clears it with `null`.
    (`AppLayoutAdmin.test.tsx`)
15. A closed built-in app's route shows a not-available result, never the console, and does not flash
    it while the answer loads; the answer fails open. Entry buttons pinned to a closed app are hidden.
    (`appAccess.test.tsx`)

16. Closing an app revokes its outstanding tokens and leaves other apps' tokens alone; every bridge
    call re-checks the token's user, so a closed row or a move into a closed OU cuts it off, a
    reopened app works again with the same token, and the backend proxy refuses likewise.
    (`TestClosingAnAppRevokesItsOutstandingTokens`, `TestEveryBridgeCallRechecksAccess...`,
    `TestExtensionProxyEnforcesCurrentAuthority/app-closed`)
17. An unchanged order writes no audit row. (`TestAnUnchangedOrderIsNotAudited`)
18. Order and access writes refuse a key that is not an app inside their transaction and leave no
    rows. (`TestWritesValidateTheAppInsideTheirTransaction`)
19. The header run menu, the reading page's quote link and the quote-only favourite card do not
    advertise a closed built-in app. (`AppLayout.test.tsx`, `StockPage.test.tsx`,
    `FavoritesGrid.test.tsx`)
20. An older dump with no sections for the two tables restores into empty tables.
    (`TestMigration0014OlderDumpRestoresWithEmptyTables`)

## Consequences

Two tables join the ledger; the baseline is unchanged. Installed apps gain a real per-OU boundary.
Built-in apps gain only a visibility control, and this ADR says so rather than implying otherwise.
Asset serving is the one place where a closed installed app is not refused for every possible caller;
the token mint, which is what an app needs to do anything, is.
