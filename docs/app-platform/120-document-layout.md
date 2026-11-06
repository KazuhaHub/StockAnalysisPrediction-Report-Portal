# Natural document sizing and one Portal scroll region

Status: implemented and passed real Linux browser/native layout acceptance at
functional commit `6016b735989ee884bd1ce2f074553e1148ce0f67`. No stable SDK freeze, production activation, Status
migration, dependency/schema change or server beta configuration change.

## User-visible behavior

Document apps grow and shrink with their natural content. Portal's bounded
content region owns vertical document scrolling; the app iframe has no separate
document scrollbar. Viewport apps still fill the available content height and
own intentional internal editor/list scroll regions. The outer Portal shell
continues to own title, gutters, navigation and loading/failure UI.

The initial document cover remains until both presentation acknowledgment and a
valid document height arrive. A missing first height fails after ten seconds
with explicit retry. The host applies at least 64 CSS pixels for an empty app.
Reload clears the previous height and retires its channel; old callbacks cannot
reveal or resize the successor. Ordinary growth, shrinkage and presentation
updates do not reload the iframe or replay backend writes.

## Shared protocol and SDK

Development protocol is `portal.app.bridge/2-dev.3`, rebuilt together in Portal
and the shared Apps SDK. Earlier protocols do not silently gain layout control.
The message is exactly `layout.measure` plus the existing complete binding and
nonces, `revision`, `presentationRevision`, and `height`. Revisions are canonical
positive decimal strings; heights are finite integer CSS pixels from zero to
100000. The host ignores old document/presentation revisions, does not apply
measurements during a superseded presentation or in viewport mode, and rejects
invalid shapes, extra transfers or oversized heights.

The SDK coalesces size changes to at most ten reports per second. The host
independently coalesces applied sizes to at most ten per second and fails a
channel exceeding twenty increasing valid-envelope messages in a receive-second
window. Replayed measurement revisions cannot change host height. Timers and
queued values are disposed with their connection. App failures cannot create an
unbounded host rerender or ever-growing measurement cache.

`AppRoot` observes its own natural `flow-root` border-box height after React
commit and channel readiness, using ResizeObserver (with bounded mutation/resize
fallback). It observes only child-owned DOM, never the parent. The document root
and body do not derive height from the iframe viewport; they hide their own
outer overflow. Natural measurement can therefore shrink after a taller host
frame rather than locking onto the previous iframe height. SDK content taller
than 100000 pixels fails locally; use virtualization or a signed viewport layout.

Do not put viewport min-height/100vh or size-to-parent loops in document content.
Out-of-flow modal/overlay hosting remains part of the unfinished shared shell
contract and is not solved by expanding natural document content. This change
does not claim complete navigation/draft/dialog/accessibility support.

The shared bridge source/tests remain Portal-owned and generator-pinned into
Apps. The native browser workflow pins Apps source
`aa0eeec12791176ad6daaee20d2183e9d7bcf808` and verifies the exact shared bytes
before building; no default branch code is executed implicitly.

## Host integration and acceptance

`AppViewV2` measures available height only for viewport apps. Document panels and
their page flex item remain natural-sized; `AppLayout` gives v2 routes an outer
scrollable content region while retaining existing bounded chat behavior.
`AppFrameV2` chooses natural measured height for document mode and host-provided
height for viewport mode. Theme covers, strict source validation and opaque
`sandbox="allow-scripts"` remain intact.

The disposable real SDK/native browser fixture now expands a 3000px content block,
waits for the actual iframe to exceed 3000px, checks its document does not have
inner scroll overflow, collapses the block and waits for height below 1000px.
The new required marker `SDK_NATIVE_BROWSER_DOCUMENT_LAYOUT` must pass the child,
root parent and Python collector, producing `sdk_native_browser_document_layout`.
Existing real-viewer, no-retry, revoke, quota/isolation and owned cleanup assertions
remain mandatory. These are implemented checks, not passing evidence until CI
has actually executed them.

## Local evidence

Portal affected host/frame/page/bridge tests: 93 passed. SDK tests: 196 passed,
including natural height observation/shrink/retirement, message coalescing and
malicious/stale height refusal. Portal and Apps workspace typechecks passed;
Portal SPA, SDK root, template and Status builds passed. Generated Status public
files have no diff. Existing unrelated Portal lint hook warnings remain.
One existing Status UI form test timed out twice locally at five seconds (8 of 9
passed); the source and built output were unchanged. Its clean CI result remains
to be inspected; no timeout increase or test suppression was introduced.
Generator/collector/public-fixture checks and native compilation are recorded
when complete. No local macOS test is substituted for Linux Chromium acceptance.

The earlier SDK/browser/native functional request chain passed Linux acceptance
in document 119; that does not prove this new sizing extension. Full P00–P14,
D5/D6 and production authorization remain outstanding.

## Local browser and companion CI follow-up

Apps commit `aa0eeec12791176ad6daaee20d2183e9d7bcf808` passed every job in check
run 40852409804. Its frontend-standard job 123374900935 passed all 196 SDK tests,
all nine maintained Status UI tests, template/root/demo checks, typechecks and
builds. The local Status timeout is retained above as an observed local failure,
not hidden with a timeout increase or used to claim a clean local result.

The browser plugin exercised the real local AppViewV2/AppFrameV2/SDK root through
protected launch/document routes using disposable synthetic SQLite facts. The
child expanded from 64px to 3032px; its HTML clientHeight and scrollHeight were
both 3032px with overflow hidden. Collapsing returned the same iframe to 64px.
Traditional Chinese presentation continued to apply, and the sandbox remained
exactly allow-scripts. This local presentation fixture does not attest native
backend authority or every rendered paint. Its temporary tab/server exited and
its temporary directory was removed. A screenshot is preserved in the local
visualization output as `app-document-layout.jpg`.

The reusable local browser fixture now provides the same expand/collapse button.
An additional AppLayout test confirms the v2 content region scrolls while chat
retains its prior overflow behavior; all 30 AppLayout tests passed. Twenty-six
contract-generator/collector/public-fixture tests and Linux amd64 test compilation
also passed. The full Linux native/browser layout extension still requires its
own artifact inspection below.

## Verified Linux result

Quota run 40852667490 / job 123375762394 passed on functional source
`6016b735989ee884bd1ce2f074553e1148ce0f67`. The inspected artifact at
`/tmp/app-document-layout-evidence/project-quota-evidence.json` records:

| Artifact field | Result |
| --- | --- |
| `sdk_native_browser_document_layout` | true |
| `sdk_native_browser_current_viewer` | true |
| `sdk_native_browser_uncertain_no_retry` | true |
| `sdk_native_browser_revoked` | true |
| `native_portal_writable_background_owned_cleanup` | true |
| `native_portal_writable_background_neighbor_preserved` | true |
| `execution_ready` | false |

This result runs the actual SDK root/Portal frame and protected backend chain,
not a mock iframe height. The browser expanded long content above 3000px, checked
inner HTML scroll/client heights, collapsed below 1000px, then completed the real
viewer/write/uncertainty/revoke checks. All existing native quota/isolation and
owned cleanup assertions remained required. The single local rendered screenshot
is not a substitute for this runtime evidence and does not prove every paint.
General test run 40852667466 and later documentation/test-only follow-ups must be
reported by their own statuses; this native result is tied to its functional SHA.
