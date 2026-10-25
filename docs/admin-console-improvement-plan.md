# Admin console improvement plan

## Objective

Make all administration pages usable as one console: discoverable navigation, clear policy scope,
shorter editing paths, predictable saves, and verified desktop and narrow-screen layouts. Keep
existing server contracts and permission boundaries. Database changes require a separate decision.

## Delivery sequence

### 1. Shared console foundation

- [x] Centralize the directory of all 22 administration pages.
- [x] Add an administration overview grouped by the task being performed.
- [x] Search translated page names, setting topics and descriptions from every administration page.
- [x] Give every page a consistent title, purpose and links to related settings.
- [x] Preserve existing URLs, permission gates, hover prefetch and the mobile navigation drawer.

### 2. Settings editing

- [x] Introduce reusable section navigation and a responsive settings workspace.
- [x] Migrate Site settings and Security from narrow continuous forms.
- [x] Migrate SSO from its narrow continuous form.
- [x] Standardize pending-change feedback, save progress, discard and navigation behavior.
- [x] Make report-version edits explicit; separate immediate actions from draft configuration.
- [x] Migrate Email, Run queue, Run defaults and Assistant settings to the shared editing pattern.

### 3. Connected workflows and remaining pages

- [x] Directory: connect Accounts, OUs, SSO provisioning, global security and effective policy sources.
- [x] Runs: distinguish execution targets, scheduler capacity, urgent tickets, presets and defaults.
- [x] Notifications: connect SMTP infrastructure, channels, rules, raw webhooks and delivery history.
- [x] Content: review Types, Versions, Links and Announcements for list/detail and preview workflows.
- [x] Operations: separate status, settings and history in Forecast, Quotes, Storage and Assistant.
- [x] Review Apps, Tokens, API reference and Audit for search, actions and narrow-screen consistency.
- [x] Keep all existing destructive-action confirmations and server-authoritative inheritance rules.

### 4. Verification and handoff

- [x] Test navigation/search, related links, save boundaries and preservation of pending changes.
- [x] Check locale keys and interpolation parity across Simplified Chinese, Traditional Chinese and English.
- [x] Inspect representative rendered pages in light/dark themes at desktop and mobile widths.
- [x] Exercise all administration routes; document remaining exceptions with evidence.
- [x] Run web typecheck, relevant tests, complete web tests/build and the embedded Go build.

### 5. Feature-owned organizational policy

The consolidated OU policy editor is superseded by this ownership model:

| Page | Responsibility |
| --- | --- |
| Accounts and organizational units | Names, hierarchy, descriptions and membership |
| Login protection | Per-OU second-factor enrolment and requirement |
| Run queue | Global urgent capacity/tickets and per-OU urgency, quota, concurrency and hours |
| Execution targets | Per-OU workflow and surface grants |
| Report versions | Reading grants, visibility and per-OU access isolation |
| Forecast operations | Per-OU forecast-reader scope |

- [x] Remove feature-policy cards and global urgent settings from the directory.
- [x] Add organizational scope to the owning settings pages, retaining inherited values and sources.
- [x] Add an atomic field-level update so one settings page cannot reset another page's policy.
- [x] Guard pending changes when switching organization or global/OU scope.
- [x] Retarget forecast access links and discovery topics to their owning pages.
- [x] Verify the revised ownership model in the browser and repeat the affected build/test gates.

## Page inventory

| Area | Pages |
| --- | --- |
| Site | Site settings, Announcements, Email, Links |
| Content | Types, Versions, Forecast verification operations |
| Directory and access | Accounts and OUs, SSO, Security, API tokens |
| Runs | Execution targets, Run queue, Run defaults, Assistant |
| Integrations | Apps, Webhooks, Notifications, API reference |
| System | Quote sources, Storage, Audit |

## Acceptance rules

The outer console uses available width; individual controls retain readable widths. Long settings
pages have a way to reach the relevant section without scrolling through unrelated fields. A draft
is visibly distinct from saved server state. A save action states its scope and does not silently
submit unrelated drafts. Configuration and live monitoring do not overwrite each other's state.
Search and navigation work by keyboard and on narrow screens. Structural changes retain the existing
administration URLs so bookmarks and cross-page links continue to work.

## Progress

- Planning: complete. The initial source audit covered the shared layout and all 22 pages.
- Console foundation: implemented. One directory supplies the rail, overview, search and related
  settings. Search matches translated topics, including SMTP, captcha and urgent tickets. Existing
  page URLs and permission gates are unchanged; `/manage` now opens the overview.
- Settings editing: Site settings, Security, SSO, SMTP, Run queue, Run defaults and Assistant now
  show one category at a time while keeping panels mounted. Shared save feedback distinguishes
  pending values from the acknowledged configuration. Drafts survive category and provider switches;
  failures retain inputs. Route links, browser Back and refresh respect pending changes. Saving one
  SSO provider does not submit or reset another provider. Blank stored secrets remain write-only.
- Save boundaries: report versions now use an explicit editor instead of blur-triggered writes.
  Types retain a whole-page field save, with independent colors and ordering clearly identified.
  Adding, deleting and reordering types wait for field edits to be saved or discarded. Queue and
  preset saves remain separate. SMTP tests explicitly use acknowledged settings. Form dialogs for
  announcements, links, execution targets, notifications, tokens and webhooks protect pending edits.
  Preset dialogs and OU selection have equivalent local guards.
- Connected workflows: related-page navigation supplies routes between account policy, provisioning,
  security, versions, queue capacity, defaults, SMTP, notification routing and delivery history.
  The account page retains its OU filter and the OU editor retains server-resolved policy sources,
  parent preview and descendant exclusion. Feature policy editors now live on their owning pages,
  rather than beside directory identity fields; stage 5 supersedes the earlier consolidated editor.
- Operations: Forecast separates status/backlog, source diagnostics, alert delivery and reader
  access. Quotes separates source health from caching/reader settings, and clearing its cache no
  longer replaces a configuration draft. Storage separates usage/manual cleanup, retention policy,
  sign-in retention and cleanup history. History failures are distinct from empty histories.
  Assistant separates configuration, live turns and conversation oversight; polling preserves drafts.
- Lists: Types, Versions, Tokens, Apps and raw Webhooks have local search. The API reference supports
  method/path/description/scope search and paginated endpoint cards, with a retry on failure. Tokens
  also distinguish failed loading from an empty directory. Audit retains its existing filters,
  pagination and narrow-screen cards. Announcements retain audience-aware previews and guarded
  destructive actions, with their unnecessary outer width cap removed.
- Browser checks: an isolated SQLite instance exercised the overview and all 22 administration
  routes at 1440px and 390px widths. The five feature-owned OU views were checked at both widths,
  with only their own policy cards present and no document overflow. Representative overview,
  settings and OU views were inspected in light and dark themes. Topic search, contextual links,
  mobile navigation, pending edits, browser Back, scope changes and drawer closure were exercised.
  A metadata-only directory save left all 14 sampled policy/parent fields unchanged in the database.
  The member shortcut selected the corresponding account-list filter. An unknown OU link exposed
  no editor or save button. Preview fixtures contain no production reports or external credentials.
- Final verification: all 150 web test files / 1,626 tests passed. The complete Go test suite,
  typecheck, production frontend build, Go vet, embedded Go build and whitespace checks passed.
  Lint completed with hook-dependency warnings. Focused regressions also verify field isolation,
  concurrent domain updates, explicit inheritance, unedited root null values and legacy root
  placement. The route pass uses a small seeded dataset; populated lists and policy edge cases are
  covered by the existing and added tests.
- Existing unrelated application-platform documentation is outside this change.

## Retained interaction boundaries

These are deliberate exceptions to a single page-wide draft, not new server behavior:

- Inline switches, list ordering, app installation and operational commands keep their existing
  immediate API actions and confirmations. Configuration forms use explicit saves.
- Quote saving can resubmit the acknowledged configuration because it also verifies the server's
  clamping and source-chain contract. Pending status and discard are still available.
- Live status and audit records remain server-owned. There is no invented aggregate health score
  on the administration overview, and no client reconstruction of effective authorization.
- Application installation permission review, one-time token display, retention previews, report
  deletion protection and notification replay review remain intact.
- The Apps market remains an explicitly fetched catalogue. Local search does not contact the
  catalogue, install an app or broaden permissions.

## Organizational-unit reference

The interaction follows Google Workspace's [organizational structure](https://knowledge.workspace.google.com/admin/users/advanced/how-the-organizational-structure-works)
and [per-user service settings](https://knowledge.workspace.google.com/admin/users/advanced/change-service-settings-for-different-users):
choose an OU, inspect its inherited value and source, and explicitly override or restore inheritance.
The portal's existing API remains authoritative for its own restrictions, quotas and access model.
