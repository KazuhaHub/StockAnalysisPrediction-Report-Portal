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
- **Back leads the title.** A page reached from a list (a stock, a run, an editor, an app) puts its
  Back button at the top left, before the page or card title, never in the top-right corner.
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

### Editable grids

A table whose cells are inputs (the batch rows grid, shared by the batch console and the recurring-task
editor, `BatchRowsEditor`) follows the same table rules plus these:

- **Input columns fill, with a floor.** The input columns share the available width, each at least
  160 px, so a single input column never leaves an empty stretch on the right. When the minimums do
  not fit, the grid scrolls sideways inside itself: the row number and the delete column stay pinned,
  and the page never scrolls sideways. The table's `scroll.x` is the sum of the minimums, or of the
  widths the user gave (`fillScrollWidth`).
- **Long text wraps in the cell.** A text input is an `Input.TextArea` with `autoSize` (1 to 3 lines,
  then it scrolls inside) instead of a wider column. Enter does not insert a newline in a single-line
  input (an input method's Enter is left alone); only a paragraph input keeps it. Number and select
  inputs stay as they are.
- **Widths are draggable, and kept.** The grid reuses the table header's resize handle
  (`TableColumnHeader`). Widths are stored per workflow in `localStorage` as `rp.table.<name>`
  (`useColumnWidths`, the width half of `useTableView`: the same key prefix and `{ widths }` shape,
  without column hiding or reordering, which a grid of inputs must not offer). Storage access is
  wrapped in try/catch; a "reset column widths" action appears once any width was dragged.
- **The grid ends with one blank row**, spreadsheet-style. Typing or pasting into it makes the next
  blank row appear; blank rows are never counted, submitted or exported, and deleting rows still
  leaves one. There is no "add row" button. A paste of several rows expands the grid.
- **Paste is the spreadsheet clipboard format**, never CSV (`parseClipboardGrid`): tab-separated cells,
  rows ending at CRLF, LF or CR. A cell is quoted only when it opens with `"` and the matching closing
  quote (`""` is one quote) is followed by a tab, a line break or the end of the text; anything else
  (`"Big" deal`, an unterminated quote) is literal text, so a stray quote never swallows what follows.
  This is Excel's and Sheets' own quoting, odd cases included: hand-typed text such as
  `"Hello<tab>A<newline>B<tab>5"` followed by a newline is one quoted cell there too.
  One trailing row terminator makes no row; blank rows in the middle are kept so the rows after them
  stay aligned; trailing blank rows are dropped. A comma never splits a cell. In select mode every
  paste is a block from the active cell (text with a tab or line break; a lone value is a 1x1 block),
  each cell checked against its column: a number column takes a number (full-width digits, a leading plus, spaces and `1,234.50`-style thousands separators are normalised; `5%` is not one), a select column only one
  of its options, a single-line column turns line breaks into spaces; anything refused leaves the
  cell as it was, and one notice per paste says how many cells were skipped. In edit mode a paste is the browser's own insert at the caret, except that one trailing
  line terminator (Excel's `abc\r\n`) is dropped and a single-line cell gets its line breaks as spaces. Copy and cut write the Excel format (a value with a
  tab, line break or quote is quoted), through `navigator.clipboard` when it exists; a cut empties the cell only after the clipboard accepted
  the text, and a refusal says so and leaves the cell.
- **Keyboard follows Google Sheets, with a fine pointer on a wide viewport.** A touch screen of any
  width, or a window under 768 px, keeps plain tap-to-edit inputs (in the table layout from 576 px,
  cards below). One cell is active (2px primary outline, `aria-selected`); the grid is `role="grid"`
  on the body table, with `aria-rowindex` / `aria-colindex` and a roving tabindex, so Tab enters it
  once. *Select mode*: click selects (no caret); arrows move; Tab / Shift+Tab move along the row and
  wrap to the next / previous row, and leave the grid past the last / first cell; Delete or Backspace
  clears; Ctrl/Cmd+C, X and V act on the cell; Ctrl/Cmd+Z undoes and Ctrl/Cmd+Shift+Z or Ctrl+Y
  redoes (up to 50 steps of clears, cuts, pastes, row deletes and committed edits, restoring the
  active cell; edit mode leaves undo to the browser); Ctrl/Cmd+Alt+minus deletes the active row (AltGr characters are not shortcuts) (never
  the trailing blank one); Alt+Shift+Up / Down resize it; typing replaces the content and starts an
  edit (an input method composition too: the active editor keeps focus with its text selected, caret
  hidden, so native typing replaces it); Enter, F2 or double-click edit with the caret at the end.
  Escape in select mode moves focus to the grid itself, from where Tab / Shift+Tab leave to the next /
  previous control outside it (focus stays on the grid when there is none) and Enter or ArrowDown return to the active cell. *Edit mode*: Enter
  commits and moves down, Tab commits and moves, Escape restores the value from before the edit.
  Arrows move the caret after an explicit edit (Enter, F2, double-click) but commit and move after a
  typed one. Alt+Enter or Ctrl+Enter adds a line break in a paragraph input; a single-line input never
  gets one. A number cell starts editing only on a digit, minus or dot; a select opens its list, and a
  typed character filters it instead of replacing the text. Moving the active cell scrolls it into
  view and follows it across pages; a paste that changes page focuses its last row. Delete buttons
  and row-height handles are not tab stops (the shortcuts above replace them); the shortcuts are
  spelled out in the hint line and the grid's `aria-description`.
- **Row height is draggable.** The bottom edge of a row's number cell is a handle (pointer drag; a
  keyboard user has Alt+Shift+Up / Down on a cell) from one line to about ten. It reports the row's
  measured height until one is set. A resized row shows its text up to that height without the
  three-line clamp. Heights are not saved: they are kept by row index, shift with a row deletion,
  vanish with a trailing blank row that collapses, and are dropped when the rows are replaced
  wholesale. An editor being edited grows up to ten lines without changing the stored height.
- **On a phone** the grid is the usual stacked cards (`CardTable`); the trailing blank row is the last
  card.

## Loading

- A view's first load goes through `LoadGate` / `LoadingPlaceholder`: the shared loading ring with
  its status line, centred where the view will appear, **from the first frame**, with no delay and no
  fade (a delay is a blank frame, and a fade replays on every Suspense layer a page passes through).
  Every in-app ring's turn is pinned to one clock (`SpinRing`; the boot splash is plain CSS), so when a page's data wait takes over from
  its code wait the ring carries on instead of restarting.
- A table's first load shows skeleton rows in the table's own shape (`CardTable`); a refresh keeps
  the rows under antd's spinner.
- A refresh scopes its spinner to what is being replaced, not the page: switching a report tab spins
  the report card while the tabs and the quote stay usable, and the pressed tab is selected at once.
  A Spin over content taller than the screen adds `rp-spin-tall`, which keeps the ring in view
  (antd centres it in the whole block, far below the fold for a long report).
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
  (`SettingsSaveBar`) floats at the bottom of the page while there is something to save. An editor
  that is a page of its own (the recurring-task editor) registers its draft with
  `usePendingSettings` and leaves through the router (Back, a breadcrumb, the browser's back
  button), so `AdminDraftBoundary` is the only thing that asks; after a save it clears the draft in
  the same commit that navigates.

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
