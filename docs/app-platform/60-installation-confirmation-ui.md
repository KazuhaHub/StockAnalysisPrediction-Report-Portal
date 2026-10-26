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
| web/src/pages/manage/AppPlatformInstallPanel.test.tsx | Confirmation, replay, refresh, identity and stale response tests |
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
8. Success places the operation UUID in `?operation=...`. Progress uses serialized
   two-second GET polling while nonterminal and unpaused. Refresh resumes this
   operation without POSTing another installation. Failure/pause stops polling
   and leaves an explicit refresh action. Completion links to the application hub;
   it does not bypass the actual app-use launch gate.

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

## Remaining platform scope

Remote v2 catalogue fetching, trust onboarding/publication, managed-backend
installation, broker capabilities, updates/uninstall/recovery UX, full sandbox
network enforcement, visual/mobile/accessibility acceptance and production rollout
remain open. Signed staging is still a trusted internal development repository
operation. This change does not pretend that a fresh production market can yet
download and install the complete first-release app set.
