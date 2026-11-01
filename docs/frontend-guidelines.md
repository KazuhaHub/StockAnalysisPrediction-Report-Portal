# Frontend guidelines

How the portal's web UI (`web/src`, React + Ant Design) is built and reviewed. These rules came out
of a full UI audit and the fixes that followed; most of them are enforced by a guard test, named
beside the rule. Read this before adding a page, a table, a dialog or a setting.

References we measure against: the Google Workspace admin console (settings, directories,
organisational units) and the Cloudflare dashboard (tables, filters, time ranges, analytics).
When in doubt, look at how they solve it before inventing a pattern.

## Principles

1. **One template per pattern.** A UI pattern is fixed once, in a shared component or CSS rule, and
   every instance uses it. When you fix one instance, sweep every route for the others. A
   hand-copied variant is a bug waiting to drift.
2. **Use the library before writing code.** antd, `@dnd-kit` and ECharts already provide disclosure
   motion (Collapse), context menus (Dropdown `trigger={['contextMenu']}`), date presets
   (RangePicker), drag reorder and charts. Wrap them; do not re-implement them.
3. **Demo before PR.** UI work is shown running on the integration branch `preview/all` (every
   in-flight branch merged) before a PR is opened. Verify at 320, 375, 768 and 1280 px, in light and
   dark, with side panels open.
4. **Tests first, review before commit.** Write the failing vitest first. Every diff gets an
   adversarial review before it is committed.

## Text and language

- All UI copy goes through `t()`; keys exist in `zh-CN`, `zh-TW` and `en-US` (locale parity test).
- Only closed, code-defined enums get translated labels. Server-written identifiers and open
  vocabularies are shown as they are.
- Say what a value is, not how the field is stored: "Never" for an empty expiry, never `∞` or
  "blank = never".
- A switch carries no text inside its track; its label and hint say what it does
  (`switchLabels.test.ts`).

## Layout and shape

- **Radii** come from the theme tokens only: `var(--ant-border-radius[-xs|-sm|-lg])`, `999px` or
  `50%` (`radiusScale.test.ts`).
- **No sideways page scroll on a phone.** Tables become stacked cards there (`CardTable`); rows that
  cannot wrap become one swipeable line (`.rp-scroll-row` + `useScrollEdges`) that fades at the side
  with more. A mouse can scroll such a row with its wheel or by dragging; a trackpad's vertical swipe
  still scrolls the page.
- **Card headers wrap.** When a title and its controls do not fit, the controls move to their own
  line (global `.ant-card-head` rule). A title is never squeezed to an ellipsis by its controls.
- **Segmented controls fold on phones.** Below 768 px (`useNarrow`) a segmented choice of more than
  a few options becomes a small Select, and a labelled toggle becomes icon-only (name kept in
  `aria-label` and `title`), so a card header stays on one line.
- **Forms keep a readable width** (`.rp-settings-panel > .ant-form-item`, 720 px); tables and cards
  use the full width.
- **Columns align.** Side-by-side panels start on the same line. A sticky rail's offset is measured
  from its scroll container: in the console the page body scrolls and has no top padding, so the
  rail sticks at `top: 0`; a page that scrolls the window sticks below the app header
  (`var(--rp-header-h)`).
- **Touch targets** are widened on touch screens (`touchTargets.test.ts`).

## Tables

- Every table is a `CardTable` (cards on phones, the antd table elsewhere). The one exception is
  the audit log, whose rows expand in place and which has its own phone card list; it should move
  onto `CardTable` rather than be copied.
- List columns are headlines: what the row is about. Identifiers and parameters go behind the
  row's full-record view.
- Column visibility, width-fit and order live in the header: a ⋮ at the end of the header row and a
  right-click on any header open the same menu (`TableView` / `TableColumnMenu`); headers are
  reordered by dragging (mouse, touch after a short press, or Space + arrow keys). Pinned columns
  stay put; action columns are pinned right.
- A filtered empty result says it is the filters and offers to clear them (`filtered` /
  `onClearFilters`, `NoMatches`); an empty list is a plain empty state.

## Loading

- A view's first load goes through `LoadGate` / `LoadingPlaceholder`: the shared loading ring with
  its status line, centred where the view will appear, **from the first frame**, fading in. No delay
  before it: a delay is a blank frame, repeated on every Suspense layer a page passes through.
- A table's first load shows skeleton rows in the table's own shape (`CardTable`); a refresh keeps
  the rows under antd's spinner.
- No hand-placed large spinners (`loadingStates.test.ts`). Never render defaults as if the server
  had sent them; a failed load says so and offers a retry.

## Feedback, status and confirmation

- Failures go through the notification stack (`notifyError` in `lib/notify.ts`), never
  `message.error` (`notify.guard.test.ts`).
- A status is told by colour **and** icon **and** words: `StatusTag`, never a coloured plain `Tag`.
  `statusTags.test.ts` rejects the status colours (`success` / `error` / `warning` / `processing`)
  on a plain `Tag`; colour names (`green`, `red`, `gold`) slip past it, so review for them.
- Every confirmation is a `Confirm`: the button names the action ("Delete token", not "OK"), Cancel
  is focused, and an irreversible action says so (`confirmations.test.ts`).
- Unsaved changes are guarded: settings pages register their draft (`AdminDraftBoundary`,
  `usePendingSettings`), dialogs use `useFormDraft` / `useLeaveGuard`, and the save bar
  (`SettingsSaveBar`) floats at the bottom of the page while there is something to save.

## Motion

- Everything that expands or collapses animates: antd `Collapse` for disclosures (no native
  `<details>`, `disclosures.test.ts`), `Expand` for content below a toggle (folds back from where it
  is if pressed again mid-fold).
- Every animation and transition has a `prefers-reduced-motion: reduce` path.

## Overlays and dark mode

- Overlays (dropdowns, popovers, dialogs, notifications, the column menu) are elevated surfaces;
  in dark mode they also get a 1 px border, since a shadow alone does not separate them from a
  black page.
- Overflowing tab rows never show antd's "…" menu; they scroll and fade (`tabsOverflow.test.ts`).
- Code and payload blocks share `.rp-code` (`codeBlocks.test.ts`).

## Checklist for a UI change

- [ ] Uses the existing template; every other instance of the pattern was checked.
- [ ] All copy through `t()`, in all three locales.
- [ ] Verified at 320 / 375 / 768 / 1280 px, light and dark, reduced motion.
- [ ] No sideways page scroll on a phone; nothing overlaps or is squeezed to an ellipsis.
- [ ] Loading, empty, filtered-empty and error states all shown.
- [ ] Tests written first; guard tests pass; reviewed; shown on `preview/all` before the PR.
