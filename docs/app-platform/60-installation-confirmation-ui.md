# Installation confirmation and staged release directory

The existing `/manage/apps` page now defaults to the application platform tab.
The former page is preserved as `LegacyAppsAdminPage` behind a legacy applications
tab. This connects the review/admission/progress API from [59](59-installation-management-http.md)
to actual React/Ant Design controls. It is a development installation flow for
already staged signed releases, not a completed remote marketplace.

## Source map

| Source | Responsibility |
| --- | --- |
| internal/app/app_platform_catalogue_http.go | Bounded signed release metadata and minimal user/OU directories |
| internal/app/app_platform_catalogue_http_test.go | Trust/input/auth refusal and audience cursor pagination |
| web/src/pages/manage/AppsAdminPage.tsx | New/legacy tabs on the existing management route |
| web/src/pages/manage/LegacyAppsAdminPage.tsx | Existing upload/market/backend behavior, moved without redesign |
| web/src/pages/manage/AppPlatformInstallPanel.tsx | Explicit audience, reviewed confirmation, persistent request identity and progress observation |
| web/src/pages/manage/AppPlatformSettingsPage.tsx | Installation settings, audience/grants, backend, retained data, recovery and credential metadata, plus reviewed retained-data deletion |
| web/src/pages/manage/AppPlatformInstallPanel.test.tsx | Confirmation, replay, refresh, identity and stale response tests |
| internal/app/app_platform_settings_http.go | Bounded administrator-only installation settings projection |
| internal/app/app_platform_management_browser_test.go | Opt-in loopback server with temporary store and synthetic trust/session |
| scripts/app-install-browser-fixture.py | Real page/preferences/auth/Ant build and loopback relay for browser acceptance |

## Administrator workflow

1. Open application management. The development notice explains that versions
   come from signed staged packages; online marketplace downloading is pending.
2. Choose explicit users/OUs or deliberately check “Do not grant anyone access
   yet.” There is no implicit installer/admin audience. The review button stays
   disabled until this choice is made.
3. Select a compatible version. The host requests a canonical review containing
   the exact release digest, current trust revision and selected audience.
4. The confirmation dialog shows the localized app name, version, publisher,
   resolved audience, optional permissions that remain declined, and absence of
   persistent app storage. Capability labels use the host's three-locale copy.
   Required unavailable capabilities cannot be silently declined.
5. Cancel closes the dialog without creating an operation. The review expires
   after the server's deadline; an expired preview cannot be confirmed.
6. Confirm creates one client UUID and saves only plan ID, plan digest and that
   idempotency UUID in sessionStorage before sending. Storage failure prevents
   transmission. No credential, signing key or full audience list is saved there.
7. An uncertain response retains the exact confirmed request. “Retry same
   request” reuses it, including after reload or plan expiry, allowing server-side
   replay reconciliation. It never requests a second plan or silently changes
   permissions. Definitive 400/403/409 refusal permits explicit review again;
   server admission still independently rejects conflicts/stale authority.
8. Disable and uninstall are separate reviewed actions. Disable immediately
   rejects new live access and background work but retains code, grants,
   credentials and application data; a managed process may remain physically
   running until the runner confirms stop. Uninstall clears the selected
   installation's rows from the plugin's dedicated table. The table is dropped
   only when that was the last non-removed installation for the plugin; another
   installation of the same plugin and every other plugin keep their data.
   Reinstalling starts empty. Managed backend data deletion still requires the
   exact runner receipt.
9. Success places the operation UUID in `?operation=...`. Progress uses serialized
   two-second GET polling while nonterminal and unpaused. Refresh resumes this
   operation without POSTing another installation. Failure/pause stops polling
   and leaves an explicit refresh action. Completion links to the application hub;
   it does not bypass the actual app-use launch gate.
10. The installation settings page derives repair eligibility from the bounded
    server projection. A disabled or recovery/review-required managed app can
    review repair only when its runtime is enabled, its data namespace is active
    and no operation is pending. The dialog binds the current release, generation
    transition, retained data and unchanged grants; confirmation uses the same
    reviewed plan/commit API and durable progress route.

The pending confirmed identity is scoped by username in the current browser tab.
It is not an authorization proof. Server-side account lifecycle/session checks
remain authoritative on every replay. The UI deliberately blocks another new
install while this tab has an uncertain confirmation; broader cross-tab operation
history/reconciliation remains future UX work.

## Directory APIs

Both routes inherit the current administrator/session/enrollment/origin response
policy from management. Unlike plan/commit/progress, they accept only their closed
query parameters, with single values and at most 2048 query bytes.

| Request | Result |
| --- | --- |
| GET /api/admin/app-platform/releases | First page of staged descriptor summaries |
| GET /api/admin/app-platform/releases?after=SHA256 | Next page ordered by exact release digest |
| GET /api/admin/app-platform/subjects?kind=user | Active-user IDs and display names only |
| GET /api/admin/app-platform/subjects?kind=ou | OU IDs and names only |
| GET /api/admin/app-platform/subjects?kind=user&after=USERNAME | Next user page |
| GET /api/admin/app-platform/subjects?kind=ou&after=DECIMAL_ID | Next OU page |

Each response contains items and next_cursor, with at most 32 items; the query
reads 33 candidates to determine the next page. Release signatures and current
publisher/key time are verified under stable trust and principal lifecycle locks.
The directory reads descriptor/signature bytes only, never frontend archives.
Unverifiable metadata is omitted; a page may consequently be empty with a next
cursor. Unsupported requirements and already installed/in-progress apps remain
visible with a disabled reason. Installability is a presentation hint: actual
review/admission rechecks complete signed bytes and all current authority.

The subjects response excludes passwords, email, sessions and security settings.
An active user's expiry or OU lifecycle can still change or refuse at actual
review; directory presentation is not a grant. Cursor parameters reject unknown,
duplicate, empty or malformed values. IDs/revisions retain canonical strings.

## Presentation and asynchronous ownership

The panel uses Portal's React, Ant Design, preferences and localized signed
display metadata. It adds no separate language switch, iframe shell or custom
theme. The development fixture's language/theme controls are test-only.

Initial directory and operation reads use AbortSignal. Account changes/unmounts
invalidate old previews/admissions by an explicit lifecycle counter, including
switching away and back to the same username. Directory reload increments a
separate counter so an older cursor response cannot append stale choices after
refresh/language change. A synchronous write guard prevents duplicate clicks;
UI busy controls alone are not relied on. In-flight POSTs are not automatically
replayed by effects or canceled as if their commit outcome were known.

## Verification

The full local Go suite with disposable PostgreSQL passed (application package
168.031 s). The earlier full web run passed 150 files / 1668 tests; subsequent
targeted checks include two additional async/storage cases. Focused UI/legacy/
locale tests pass 33 cases, with typecheck and lint passing. Focused SQLite/
PostgreSQL catalogue/management race checks passed (42.029 s), as did vet,
staticcheck, the production web build and Linux amd64 Portal compilation.

The browser plugin exercised the real page and HTTP handlers in an owned
loopback fixture: explicit test-user choice, signed app preview, confirmation,
background completion and reload preserving the exact operation UUID. English,
Simplified and Traditional strings followed Portal preferences. DOM computed
styles confirmed a black main background and Ant's dark table background after
theme change. A second fresh fixture checked friendly capability labels, version
display, cancel-before-admission and deliberate empty-audience completion.
No native screen control or production service was used. This is DOM/interaction
evidence, not pixel-perfect/mobile/assistive-technology acceptance or D5 approval.

To reproduce with installed web dependencies:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 python3 -B scripts/app-install-browser-fixture.py
```

Open the printed loopback URL through the browser plugin. The fixture builds in a
temporary folder, creates synthetic publisher/session data in temporary SQLite,
and excludes inherited TEST_POSTGRES_DSN. Ctrl-C asks the owned test process to
stop, joins it, closes the loopback listener and removes the temporary folder.
The session state file is private and is never printed. Do not adapt the fixture
to use production accounts, deployment credentials or an existing database.

The repeatable browser matrix starts and cleans up that fixture itself:

```sh
SDK_BROWSER_PACKAGE=/absolute/path/to/playwright/package.json \
  node scripts/app-install-browser-acceptance.cjs
```

It captures install reviews at 1440px English/light, 320px Simplified
Chinese/dark and 390px Traditional Chinese/light, plus update, re-enable, disable
and uninstall reviews against their real backend projections. It also renders
queued, controller-paused and recovery-required progress from durable operation
rows and captures the installation settings page at 1440px English/light and
320px Simplified Chinese/dark, plus a reviewed retained-data deletion against a
managed runtime and managed repair reviews at 1440px English/light and 320px
Simplified Chinese/light. Every case checks page bounds, overflow, visible focus,
Escape/focus restoration where a dialog exists, WCAG A/AA axe results, browser
errors and screenshots. After installation the install case completes the
no-audience installation and verifies the row changes from installable to
installed. The disable review
states that data is retained; the uninstall review states the single-plugin
clear boundary. CI uploads the screenshots and `acceptance.json`; this remains
installation-page evidence, not approval of every lifecycle page or a substitute
for D5 review.
Portal `ff331b52` passed quota/browser run `41382711308` with all three axe scans
clean and the background operation complete.
Portal `7ff23d12` extended the same job to update and re-enable projections;
quota/browser run `41388136069`, general test run `41388136067` and runner daemon
run `41388136083` pass. All five dialog scenarios have zero axe violations.
Portal `84c82eb2` adds the post-install uninstall review and passes
quota/browser run `41393438806`, general test run `41393438788` and runner daemon
run `41393438802`; the uninstall dialog confirms the single-plugin data boundary
and has zero axe violations.
Portal `ed04e66c` adds reviewed disable and queued/paused/recovery progress; the
browser report binds that code revision. Candidate `2693793e`, which adds the
report and screenshots, passed quota/browser run `41402057341`, general test run
`41402057322` and runner daemon run `41402057334`. The page-level and
dialog-level axe scans are clean across all eight captured scenarios; the full
local web run passed 269 files / 2832 tests, typecheck, lint and the production
build.
Portal `f1a4550f` adds the administrator installation settings page and terminal
operation refresh. Its browser report covers ten scenarios, including settings
at 1440px and 320px, with no overflow, clipped tags or serious/critical axe
violations. The full local web run passed 270 files / 2840 tests, typecheck,
lint and the production build. quota/browser run `41437808872`, general test run
`41437808855` and runner daemon run `41437808888` pass. See
[application settings](156-application-settings.md).
Portal `8d882a2a` adds reviewed permanent deletion for retained managed data.
The destructive dialog binds and displays the exact namespace and recovery
scope. quota/browser run `41445281563`, general test run `41445281473` and runner
daemon run `41445281626` pass with eleven captured scenarios.
Portal `49754cbb` adds management repair review/confirmation plumbing and browser
repair evidence. The matrix now covers thirteen scenarios, including repair at
desktop and 320px Chinese, with zero serious/critical axe violations, bounded
dialogs and no page overflow. Local Web passed 270 files / 2842 tests; full
`internal/app` passed in 444.062s. Exact hosted runs for this candidate are
recorded in [application settings](156-application-settings.md).

## Remaining platform scope

Remote v2 catalogue fetching, trust onboarding/publication, managed-backend
installation, broker capabilities, updates/uninstall/recovery UX, full sandbox
network enforcement, visual/mobile/accessibility acceptance and production rollout
remain open. Signed staging is still a trusted internal development repository
operation. This change does not pretend that a fresh production market can yet
download and install the complete first-release app set.
