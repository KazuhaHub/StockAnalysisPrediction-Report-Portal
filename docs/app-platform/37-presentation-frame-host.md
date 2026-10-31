# Experimental v2 Portal presentation frame host

Status: P06 internal integration, not a registered application route, new install
API, stable SDK release or production migration. `AppFrameV2` hosts the existing
v2 presentation protocol; it does not grant backend authority or load unreviewed
packages on behalf of a browser request. No runtime/server/database changes here.

## Source and caller contract

`web/src/components/apps/AppFrameV2.tsx` supplies a reusable host component with:

- A caller-authorized immutable frontend asset URL under `/app-assets/`.
- Exact installation UUID, generation, policy revision and release digest binding.
- Validated current Portal presentation and capability **hints**, not grants.
- An accessible iframe title and a host-selected bounded integer frame height.

The initial source check rejects noncanonical/non-root-relative paths, other
origins, JavaScript/data URLs, queries/fragments, whitespace/control characters,
backslashes, invalid escapes and dot/double-slash paths. It bounds source length
at 2048 and height at 64..16384. It is **defense in depth**, not server-side asset
authorization or integrity verification. The caller must obtain the resource and
binding from the future current installation/activation access path and verify
immutable resource identity there. A component prop or same-origin URL alone
proves no entitlement. No app/browser body chooses these props through a new API.

The `/app-assets/` prefix is an existing namespace, not a new generation-aware
server handler introduced here. The future v2 asset-serving gate must bind actual
bytes and authorization to the reviewed release/generation. Do not route a v2
launch to mutable legacy assets merely because this prefix check passes. No
production page imports this component until that integration exists.

## Document and session lifecycle

The outer component keys the document by source, installation/generation/policy,
release digest and sorted capability hints. A change replaces the iframe and
closes its old host controller. Current presentation, title and allocated height
do not change that security identity; ordinary theme/language changes preserve
the same document and its local state.

Every document starts with:

- `sandbox="allow-scripts"`, without same-origin, top-navigation, form or popup
  permissions. No token or session cookie is sent in bridge metadata.
- `referrerPolicy="no-referrer"`, no app-selected navigation, and no `srcDoc`.
- `visibility:hidden`, `aria-hidden=true` and `tabIndex=-1` until ready.
- A borderless frame and outer region colored with the host's current safe theme,
  and an opaque themed cover containing localized status/spinner content.
- A ten-second asset-delivery deadline even if `load` never fires. Browser iframe
  error events are not relied on as a trustworthy asset-failure signal.

`load` is a **session reset**, never proof of application readiness. It closes the
prior controller, advances a local epoch, restores hidden/covered state, and starts
a new actual shared `createBridgeHost` against that exact contentWindow. The host
challenge forces an already initialized child to retire its previous connection.
The shared protocol has its own bounded handshake/presentation deadline after
asset delivery. Readiness reveals the frame only if its callback belongs to the
current live local epoch. Old ready **and failure** callbacks are fenced out.

Presentation changes update the existing controller; its existing coalescing rule
requires the latest pending snapshot before initial reveal. The cover tracks the
host's current theme while hidden. After first readiness, ordinary live updates
retain the document; they are not implemented by iframe navigation or a fresh
independent app language picker.

On protocol/application/transport deadline failure, remove the executable iframe,
close its session and show only fixed translated failure text and a Reopen action.
Raw exceptions, protocol diagnostics and backend details are not displayed.
Reopen creates a new covered iframe with its own asset timer/session. Unmount and
identity change close listeners/ports/timers and invalidate callback epochs. The
component owns no API RPC, generic request handler, token minting or v1 fallback.

## Layout and refresh limits

There is no outer app card border, radius or repeated visible app title here. The
accessible iframe title remains required; the surrounding shell owns its single
visible title/navigation. Height is supplied by the host layout, not by arbitrary
app messages. This component does not yet implement content-height messaging,
parent scrolling, document/viewport sizing policy, unsaved navigation or drafts.
Those remain required P06 work before the one-scroll product acceptance can pass.

The loading cover applies from component creation and controlled Reopen. On an
observed `load`, it also requires a new handshake. A sandbox document can initiate
its own navigation before the parent observes `load`; this component cannot claim
to detect every such navigation's start or eliminate all interim browser paints.
Real-browser dark/light refresh and navigation tests are still required. The
current DOM/protocol tests do not establish pixel-level no-flash acceptance, and
no deployed Status page is changed by this slice.

## Verification

Tests first failed on the missing component import. The targeted suite passes 43
cases across the new host, existing legacy AppView and shared v2 protocol:

- Hidden/covered initial frame with exact sandbox/referrer and accessibility state;
  `load` alone cannot reveal it; only current-session ready does.
- Reload closes old session and fences stale ready/failure callbacks.
- Live locale/theme updates preserve the frame; generation changes replace it.
- Failure removes executable content; Reopen restores a new covered document;
  cleanup closes the latest session. Asset delivery is bounded without a load.
- Unsafe sources refuse before iframe creation.
- Real shared host and child client communicate through transferable MessagePorts
  with explicit jsdom delivery adapters preserving source/origin/port inputs. A
  delayed child commit keeps the cover; a newer presentation delays reveal until
  its own acknowledgement; another load forces fresh negotiation. Exact counters
  above Number precision survive the path. No protocol factory is mocked in this
  integration test. It is not browser or server authorization evidence.

Local web typecheck, lint and production build pass. Existing unrelated lint
warnings are unchanged; none come from this component. Full local web regression passes 1626 cases across 144 files. Exact-source CI
must still be checked before accepting the whole slice. Fixed loading,
failure and retry strings are provided in en-US, zh-CN and zh-TW.

The prior SDK root source `101bdad9143937a4d8ce65e0c19167dc05d3e0ce` passed
[Apps CI 40256556012](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/actions/runs/40256556012)
and its push run, including 180 SDK cases, template/consumer builds and existing
Status browser regression. This is regression evidence for existing consumers,
not a real-browser test of the unregistered v2 host. Prior Portal evidence source
`50c22876885e708308a24a571edc9cdccfad5a31` passed
[CI 40256627542](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40256627542).

Next: current authorized activation/immutable asset delivery, sizing/navigation/
draft contracts, real host/root browser fixtures and the remaining installer,
physical runtime and capability broker. D5 visual review, stable SDK freeze,
production rollout and per-app adoption decisions remain separate.

Acceptance mapping: this slice supplies partial implementation evidence for
UX-01/02/03/10 and SEC-03/04. It does not close those complete scenario gates;
reference-app installation, real-browser rendering, full host navigation and
actual sandbox/CSP asset-serving verification remain required.
