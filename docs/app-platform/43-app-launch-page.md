# Portal application launch page

This connects the authorized launch API to a real Portal page at
`/apps/v2/:installation`. It is inside the existing `Protected`/`AppLayout` route,
uses the normal API session-loss handling and preserves legacy `/apps/x/:id`.
It is a first host-page integration, not complete P06 shell/UX acceptance or a
production rollout. The market/list/installer must still supply actual ready
installations and links; no fixture entry is published in AppsHub.

## Server presentation facts

The launch response now also contains:

- `applicationId`: the verified signed manifest ID, used as a bounded plain-text
  fallback title. Publisher/localized market display names are still required
  before final app-center acceptance.
- `layout`: the first declared signed route's document/viewport layout. Arbitrary
  route navigation/history and route-registry selection remain later P06 work.
- `timeZone`: the configured Portal timezone, or a named process location when
  available. The process name `Local` is not an IANA identifier and is returned as
  explicitly unavailable (`""`), never replaced with a guessed browser timezone.

Facts are read inside the same release/current-reader transaction before the
final authorization check. No new setting or database product is created. Unknown
or invalid browser timezone identifiers refuse executable mounting. A deployment
whose system location cannot be expressed by name currently needs an explicit
valid panel timezone in its existing settings; complete automatic system-zone
resolution/bootstrap UX remains required before general release. This slice does
not change any actual server timezone or beta setting. The browser fixture sets
UTC only in its isolated temporary store.

## Client behavior

`parseAppLaunchV2` validates the expected installation, canonical positive decimal
int64 counters, exact digest-addressed source, local entry spelling, signed app ID,
known layout, browser-recognized timezone and currently empty capabilities. It
copies only the needed public fields. Unknown/invalid data never mounts a frame.
This is display validation, not an authorization permit; document requests still
repeat current authorization on the server.

The page loads metadata once per installation/retry through `api.get`. An optional
AbortSignal was added to that existing GET wrapper without changing other callers'
fetch options. Unmount/installation change aborts the pending request; a live
request guard also fences stale callbacks. Existing `session_expired` handling
still belongs to the shared API/AuthProvider. No raw server diagnostic appears in
this page's errors; loading/unavailable/reopen text uses all three Portal locales.

The business timezone is currently a launch snapshot; live server-timezone
change propagation still needs integration. The page gets language/dark mode
from the existing Portal preference provider and
colors/radius from Ant Design tokens. It supplies the verified timezone/layout to
the shared presentation bridge, with no independent app language/theme selector.
Preference changes keep the same frame/binding and reach the running app through
that bridge. The host renders one fallback app title and a Back to apps action;
there is no outer app card border or repeated version/header row.

The frame receives available host-panel height through a ResizeObserver bounded
by the existing frame limits. The v2 AppLayout route occupies the remaining
viewport and omits the ordinary footer/back-to-top action. Other routes retain
their existing layout. **This is not document-height messaging:** long document
content currently scrolls within its allocated frame, and true document-flow
measurement/coalescing, editor layout policy, focus and resize/scroll acceptance
remain unfinished. Do not mark the single-scroll/full P06 gate complete from this
height allocation. Historical navigation, draft guards, app actions and live
invalidation subscriptions also remain required.

## Verification

Tests first failed on the absent page and absent server presentation facts. A
new unsafe-entry case exposed `./index.html` normalization; the parser now refuses
that spelling before frame creation.

- Page tests cover current authorized metadata, exact large counters, inherited
  preferences, no independent selector, one fetch across appearance changes,
  invalid layout/timezone/source/principal/capabilities, safe refusals and aborted
  pending fetches.
- Parser tests cover int64 overflow/aliases and unsafe nested entry spelling.
- Existing AppLayout, legacy AppView, host/bridge and API regressions are retained.
- Full local web regression passed 1648 cases across 146 files; typecheck, lint
  and production build passed. Unrelated existing lint warnings remain.
- Server tests bind layout/app identity to the signed manifest and return an
  explicitly configured timezone. Full local Go regression with disposable PostgreSQL passed (app package
  129.658 seconds), and final targeted SQLite/PostgreSQL race checks passed.
  Exact-source CI must still be checked for this slice.

The local real HTTP/SDK browser fixture now supports:

```sh
python3 scripts/app-document-browser-fixture.py \
  --apps-dir /absolute/path/to/StockAnalysisPrediction-Report-Portal-Apps \
  --portal-http --launch-page
```

This uses the actual `AppViewV2`, Portal `PrefsProvider`, Ant provider/router,
real HTTP handlers and actual SDK root. The temporary store has explicit UTC.
Observed: initial English/UTC launch, interactive counter change to 1, Traditional
Chinese updating the host Back button and child locale while preserving 1, theme
change preserving that frame/state, Simplified Chinese updating both surfaces,
and host Back navigation removing the app frame. The fixture uses a minimal host
surface, **not the complete AppLayout with all production providers**; it does not
prove full shell geometry, real login ceremony, accessibility, cross-browser or
pixel-level no-flash acceptance. It does not provision physical app resources.

## Remaining delivery

Complete ready-app discovery and market/installer linking, display names, document
measurement, route/history/draft contracts, provider/data receipts and activation,
managed runtime lifecycle and the capability broker. Resolve frontend navigation/
exfiltration policy before sensitive broker output. Keep D5 visual and P00–P14
release gates open. No Status adoption or production deployment is performed.
