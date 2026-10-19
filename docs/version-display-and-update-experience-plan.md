# Version display and update experience implementation guide

Status: implementation handoff. UI direction agreed; technical proposals below require implementation review.
Prepared: 2026-09-19.
Scope: version labels, update notifications, release-note modal, and administrator update policy.
This document does not implement, publish, or deploy a change.

## 1. Agreed product direction

- Add a localized Version label to the product version in the portal footer and management sidebar. Use Version, not Software. Keep the existing subdued typography and single-line layout.
- Display CalVer without its leading v; retain the raw tag for identity and release lookup. Accept both two- and three-component tags.
- Name the target version in the update notification when it is known.
- Show release notes in a modal everywhere, never in a drawer. Follow the existing Run Analysis modal's appearance and responsive behavior.
- Administrators can choose whether users may defer the prompt, receive a persistent banner, or must refresh through a non-dismissible modal.
- A required refresh remains an explicit user action. Do not automatically reload an active page.

## 2. Existing implementation and gaps

Verified entry points:

| Area | Current file / behavior | Required change |
| --- | --- | --- |
| Update detection | `web/src/lib/useVersionCheck.ts` returns a boolean; captures the first API response as its baseline and stops polling after a change | Return current and target identity; keep target and policy fresh while an update is pending |
| Worker activation | `web/src/lib/swUpdate.ts`, `applyUpdate()` | Preserve worker handover and reload-once behavior |
| Portal footer and banner | `web/src/components/AppLayout.tsx` | Localized label, target-aware banner, shared modal entry |
| Management version display | `web/src/pages/manage/ManageLayout.tsx` | Same label and release-note entry; shared update policy coverage |
| Modal reference | `web/src/components/RunAnalysisModal.tsx` | Uses Ant Design Modal, width 980, class `rp-run-analysis-modal`; inspect its associated CSS before implementing |
| Product label | `web/src/lib/productVersionLabel.ts` | Reuse formatting; do not change API identity values |
| Build endpoint | `internal/app/server.go`, session-gated `GET /api/version` | Preserve existing identity fields and authentication |
| Site settings | `internal/app/apiui.go`, `web/src/api/types.ts`, `web/src/pages/manage/SiteSettingsPage.tsx` | Add policy using existing settings conventions |

The first response from /api/version describes the server, not necessarily the cached SPA that the browser loaded. Do not label that response as the current page build without establishing its provenance.

## 3. Version labels and entry points

Examples of localized UI copy:

| Surface | English | Simplified Chinese |
| --- | --- | --- |
| Footer / sidebar | Version 2026.38 | 版本 2026.38 |
| Tooltip fields | Version / Commit / Built | 版本 / 构建提交 / 构建时间 |
| Release-note entry | View release notes | 查看更新内容 |

The version is a keyboard-accessible button styled as secondary text. Clicking it opens notes for the displayed build. Hover and keyboard focus reveal build details, including a clearly labeled timezone for the timestamp. Do not put required actions only in a tooltip.

Preserve `footerShowVersion`: hiding the footer version hides that entry point, not update detection or the administrator's update policy. Management sidebar collapse behavior stays consistent with the existing layout.

The displayed current version remains the loaded page's version until reload succeeds. The target version belongs to the update notification. If the current page identity cannot be established, say it is unavailable rather than substituting the server version.

## 4. Administrator policy

Place a single radio group under Site settings > Update notifications. Do not use independent booleans for deferral and enforcement.

Proposed setting: `update_prompt_policy`, values `dismissible`, `persistent`, `required`; default `dismissible`. Proposed API field: `updatePromptPolicy`.

| Policy | Presentation | Dismissal | Continue using page |
| --- | --- | --- | --- |
| dismissible | Banner with Refresh, View release notes, Later | Later hides this target's prompt for this tab session | Yes |
| persistent | Banner with Refresh and View release notes | No close or Later action | Yes |
| required | Open the shared release-note modal automatically | No close button, cancel action, Escape, or mask dismissal | No, while the modal is active |

Recommended deferral semantics: sessionStorage keyed by full target identity. A different target prompts again; a policy change to persistent or required overrides deferral. Closing an optional release-note modal alone does not mean Later. Do not add a reminder timer in this scope.

Use the existing DB-backed settings mechanism, not config.yaml. No new table, column, schema generation, or migration is proposed. If implementation needs a DB shape change, obtain owner approval before writing it. Validate writes server-side; reject unknown policy values. Treat an absent or unreadable value as the default rather than unexpectedly forcing a refresh.

Publish the effective policy through an authenticated response usable by already-open pages. Continue checking policy after detection: required can be relaxed without trapping a stale page. Apply the policy to portal and management routes through one shared coordinator, with no duplicate overlays.

Required mode is a browser interaction policy, not a security guarantee or a server-side block on old API clients. It neither installs software nor upgrades a database.

## 5. Banner copy and target semantics

For a known newer version:

- Title: New version 2026.38.1 is ready.
- Description: Refresh this page to use the new version.
- Actions: Refresh page, View release notes, and Later only in dismissible mode.

For the same product version with a different build: Page update available. For a rollback: Server version changed; refresh to switch to 2026.38. Do not describe a rollback as a newer release.

For a service-worker-only signal with no confirmed server target, use generic page-update copy. Never infer a version from a worker notification alone. Keep refresh usable even when release-note retrieval fails.

Compare full build identity (version, commit, build date), not just the displayed label. Numeric CalVer comparison is only for wording such as newer versus rollback, not for deciding whether a changed build needs refresh.

## 6. Shared release-note modal

Use one reusable component, proposed `ReleaseNotesModal`, and one update-state coordinator. Match Run Analysis modal spacing, header, mask, button styles, theme tokens, and responsive rules. Its current desktop width is 980px: start from that shared convention, constrain to the viewport, and verify readability rather than introducing a separate fixed-width design.

Structure:

1. Title: Release notes · 2026.38.1.
2. Secondary context: Current page version: 2026.38, when viewing an update target.
3. Scrollable Markdown body. Preserve upgrade and rollback instructions and original section ordering.
4. Fixed footer: View on GitHub on the left; Close and Refresh to 2026.38.1 on the right.

When browsing the current version without an update, omit Refresh. When required, omit Close and the header X, disable Escape and mask dismissal, and retain Refresh plus the external notes link. Use the same modal directly; do not stack a blocking prompt and a second notes dialog.

On narrow screens follow Run Analysis's existing responsive behavior, wrap actions without horizontal overflow, and keep the primary action visible. Verify at 390px, a 13-inch laptop viewport, and 1440px desktop, in both themes. Keyboard focus must stay inside the modal and return to its trigger when optional dismissal occurs. Release-note loading must not prevent the refresh action from appearing.

States: loading, loaded, notes unavailable, request failure with Retry, and diagnostic build without a release. Do not display an empty success state for missing notes. Do not invent a GitHub release link for dev/ci or an invalid tag.

## 7. Release-note delivery proposal

Recommended first implementation: serve the deployed build's committed release note from the application itself. Include the exact `docs/releases/<tag>.md` content during release builds; expose it through a session-gated endpoint, proposed `GET /api/release-notes`. Return raw tag, commit identity, Markdown, and a trusted GitHub release URL. Keep existing /api/version fields compatible.

This avoids browser GitHub credentials, rate limits, and third-party availability for the primary reading experience. The View on GitHub link provides the complete published body, including GitHub's generated PR list and publication-time edits. The in-app content is the committed note; do not claim these two bodies are always identical.

Support viewing the loaded version after the server has advanced: either package that build's note with the SPA or provide immutable, identity-keyed cached content. Decide the packaging before implementation; never show the new server note under the old page's version label. If old notes are unavailable, show that state and the exact release link.

Make production release builds fail if their committed note is absent. Diagnostic local builds can return notes unavailable. Fetch responses without a stale cache; cache rendered content only by identity. If the server changes again during a request, discard mismatched content and refetch the new target.

Render through the existing safe Markdown conventions, disable raw executable HTML, and restrict link protocols. Do not fetch arbitrary URLs provided by users. No GitHub token belongs in browser code or endpoint responses.

## 8. Build identity and refresh reliability

Establish the loaded SPA identity at build time, aligned with binary identity in the release pipeline. Include version and commit, with one coordinated build identifier/date if used by both surfaces. Inspect release, CI, Docker, and local build paths so ordinary builds remain supported. Do not stamp cached HTML with a fresh server identity and assume the cached JS matches it.

Extend the detector to expose loaded identity, latest observed server identity, worker readiness, effective policy, and request status. Poll at the existing cadence and on visibility changes, deduplicate in-flight checks, and protect against out-of-order responses. Re-evaluate newer targets and policy changes while the prompt is open.

Use the existing applyUpdate path; disable duplicate refresh clicks and preserve reload-once behavior. Network errors or 401 responses do not manufacture an update. Do not cause an endless forced-refresh loop when offline or when an old service worker remains active; show a recoverable failure state and retry controls.

Never silently discard edits by auto-refreshing. Before explicit refresh, integrate with existing unsaved-change handling where available. In required mode, warn that unsaved changes may be lost. Automatic draft preservation is not promised in this scope; do not introduce storage of sensitive form content as incidental work. Background server jobs continue independently of a browser refresh.

## 9. Implementation sequence

1. Confirm shared modal CSS, settings permission flow, Markdown renderer, and all build identity paths. Resolve note packaging and SPA identity together.
2. Add authenticated metadata/note delivery and validated policy read/write using existing settings storage.
3. Refactor update detection into structured shared state; cover rollover, rollback, worker-only signals, deferral, and policy changes.
4. Add localized version labels, banner copy, and the shared modal across portal and management layouts.
5. Add the administrator radio group and translated explanations in all supported locales.
6. Verify real browser behavior with two controlled builds and isolated settings/data. Record evidence and limitations before PR creation.

This guide authorizes no production deployment or new release by itself. Keep the work separate from release-number derivation fixes.

## 10. Acceptance and regression tests

- Both two- and three-component CalVer labels have a localized Version prefix; raw API tags remain unchanged.
- A loaded A page detects server B, displays A as current and B as target, and remains correct through server C or a rollback.
- Same-version rebuilds and worker-only updates use honest generic wording.
- Later suppresses only its target in the same tab session; new targets and policy escalation reappear.
- Persistent banners cannot be hidden; required modals resist all dismissal paths on portal and management routes.
- Policy relaxation unblocks an already-open required modal appropriately.
- Optional current-version notes have no refresh action; target notes have one; no modal stacking occurs.
- Notes are matched to identity; slow responses, missing notes, offline mode, diagnostic tags, unsafe Markdown, and authentication failures behave correctly.
- Refresh activates the waiting worker before reloading, does not double reload, and does not loop indefinitely on a stale cache.
- Settings writes enforce existing management permissions and enum validation; defaults work on existing databases without schema changes.
- Browser checks cover desktop/mobile, light/dark themes, keyboard navigation, long notes, and an unsaved form warning.
- Run relevant Go tests, frontend typecheck and component/hook tests, locale parity, production build, and workflow lint if build workflows change. Use a real browser for layout and service-worker lifecycle checks; unit tests alone are insufficient.

## 11. Completion evidence

The implementation PR should include screenshots of both version labels, the three policy presentations, and desktop/mobile notes modals; test results; the exact release-note source and fallback behavior; and a two-build browser update/refresh demonstration. State any remaining limitations explicitly. Do not report a banner-only test as full service-worker validation.
