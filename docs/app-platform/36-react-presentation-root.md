# Shared React presentation root integration

Status: P06 internal development, not v2 host rollout, stable SDK freeze or a
Status adoption. Implementation is in [Apps PR #26](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/pull/26), source
`101bdad`, under the experimental
`@portal-app/sdk/root-v2` entry point; Portal's shared transport source remains
unchanged. No server config, database, catalogue or published package changes.

## Scope

The bridge in document 22 previously delegated presentation application to a
callback. A callback that merely schedules React state cannot truthfully tell the
host that the app has applied its settings. The SDK now supplies `AppRoot` to own
that callback and `useAppPresentation` to expose its validated public context.

The root waits for initialization before mounting business content, applies the
host's locale/theme/timezone/layout through the existing React/Ant foundation, and
resolves the bridge callback only from a commit marker's layout effect inside the
application error boundary. The marker commits document language, color scheme,
background and text before ready can reach the host. It does not create a second
language selector, outer title, card border, application-specific theme engine or
legacy bridge listener.

Ordinary settings updates preserve component state. A fresh host challenge clears
old context and remounts for the new binding/generation, including values beyond
JavaScript Number precision. Abort/reset/failure/unmount disposes pending work and
owned channel/listeners. Errors remove app content and show fixed localized text;
raw exceptions are not rendered or sent. The hook exposes no resolver, credential,
request/directory adapter or backend authority. Capability fields remain hints.

The existing SDK provider is reused through a private appearance-only adapter;
its source and legacy consumers are unchanged. No `PortalClient` is instantiated.
The new entry grants no API/data/navigation/job capability. Document layout does
not force an inner viewport scroll; viewport layout uses the sandbox viewport.
Automatic sizing, guarded navigation and drafts remain separate work.

## Host loading-cover obligation

Root readiness means committed presentation, not a promise that every browser
paint was hidden. Before app JavaScript runs, only the Portal host can cover the
browser's initial blank document. The future host integration must keep the frame
hidden/covered with current host theme from creation, on `load` and after every
reset until the **latest** valid acknowledgement. Revealing on `load` alone remains
incorrect. No sandbox or CSP weakening is introduced to hide the initial paint.

This slice has React/jsdom/real MessagePort integration evidence, not rendered
browser evidence or D5 approval. Production legacy Status is not switched to v2,
and no claim is made that this changes its deployed appearance. Full dark/light
refresh, scrollbars, mobile and screen-reader/keyboard acceptance still require
the actual host shell. The Apps documentation `docs/root-v2.md` includes authoring
composition, ownership, failure behavior and remaining acceptance gates.

## Verification

Tests first failed against the missing root module. Current local SDK suite has
180 passing cases, including the new root cases:

- No business render before init; settings applied before the Promise settles.
- Ant locale and color tokens track the host without another language selector.
- Theme/locale/timezone/layout updates preserve state and exact int64 strings.
- Reset/abort/failure/unmount removes old work and listeners; render failures reject
  pending acknowledgement and hide private exception text; failed roots do not
  revive or repeatedly notify.
- Real shared host/client with transferable MessagePorts: the host ready observer
  checks committed React DOM, language and background, live updates retain a
  stateful button, StrictMode cleanup/remount is exercised, and a new host
  generation clears old application state. Simulated window source/origin fixtures
  are explicit; this is not a live browser or server permission test.

A new isolated build fixture bundles the root into one classic IIFE and one local
stylesheet. It has a reserved `.invalid` bootstrap origin and is not in the app
catalogue. The Apps CI runs this check. Local shared contract/standard checks,
SDK/all-workspace typechecks, generated template checks, demo and Status tests and
builds pass. Rebuilding Status produces no tracked public-asset diff; old released
bytes/catalogue entries are not rewritten for this experiment.

The same-source Apps CI must be checked before claiming remote acceptance. Portal
has only the implementation evidence update in this slice. The full P03..P14 goal
remains active: lease-bound command generation still needs its reviewed resource
preparation records and actual physical providers; this frontend work does not
replace those dependencies or the release/adoption decisions.

Prior runtime revocation source `2dfa04e2ca441fb96543a40f8ba490c6645e18f1`
passed full [CI 40255168716](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40255168716)
and [native endpoint run 40255168720](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40255168720).
