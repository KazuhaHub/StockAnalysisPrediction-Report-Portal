# Shared unsaved-change guard

Status: development implementation. Local React/bridge and browser checks are
recorded below. Linux browser/native acceptance is pending at this document's
first commit. No production activation, catalogue publication, Status migration,
stable SDK freeze, database/dependency change or server beta configuration change.

## Behavior and ownership

Every mounted form can call `useAppDraft(dirty)` from the experimental shared
React root. Each hook owns one entry in a document-local registry. The aggregate
is logical OR: saving the first of two edited forms cannot clear the second.
Unmount removes only that hook's entry. StrictMode remounts do not strand entries.
Language/theme updates preserve the registry and mounted form state. A fresh
connection retires the old registry. This is a boolean hint, never draft content.

Portal receives the hint through the admitted iframe's current private channel.
Its app page renders one host-owned localized Ant modal when the router attempts
to leave a dirty document. Links, programmatic return and history POP use the
same blocker. Cancel resets the pending navigation and retains the document;
confirm proceeds immediately without waiting for an application callback.
If the document becomes clean while blocked, cancel the pending departure and
keep editing; the next departure proceeds normally. The app cannot choose prompt
text, inject markup, veto confirmation or supply a navigation destination.

Refresh and tab/window close register the browser-native beforeunload warning
only while dirty. Browser activation rules can suppress this warning and its text
is browser controlled. This does not make an app durable: crashes, forced closes,
direct iframe reloads, access loss and unreported edits can still discard data.
Draft persistence/autosave and in-app form transitions need their own contracts.
Do not describe this change as saving or restoring draft text.

## Router integration

The SPA entry uses the existing router package's `createBrowserRouter` and
`RouterProvider`, with one stable wildcard route containing the existing `App`
route/provider tree. This enables its supported `useBlocker` implementation;
there is no new dependency, custom history monkey patch or second navigation
stack. Existing authentication/layout and nested route definitions are retained.
The guard test deliberately uses that same wildcard plus nested `Routes` shape.
All existing non-app pages remain outside the app-specific draft blocker.

`AppViewV2` owns the current document hint. `AppFrameV2` clears it on reload,
failure, binding replacement and unmount. Session callbacks are epoch fenced:
a callback retained by a retired channel cannot dirty its successor. The host
observer uses a ref, so changing a callback identity does not reload the app.
The local launch-page fixture now also uses a data router; a legacy MemoryRouter
cannot exercise useBlocker and would produce a false fixture failure.

## Shared development protocol

The protocol advances to `portal.app.bridge/2-dev.4`; both host and child must
be rebuilt together. The exact message is `draft.changed`, all existing binding
and channel envelope fields, canonical increasing positive int64 `revision`, and
boolean `dirty`. Extra fields, draft bodies, URLs and callbacks are refused.
Stale envelopes/revisions cannot clear the current document's hint.

Client updates coalesce to at most ten per second, suppress duplicate values and
cancel queued reports on retirement. Before readiness or after close the low-level
setter returns false; the React registry emits its current aggregate on readiness.
The host rejects a channel exceeding twenty increasing reports in a second.
No draft authority, secret access or backend operation is granted by this hint.

Portal remains the owner of the generated bridge source/tests. Regeneration must
update the companion Apps mirror and its pinned source digest. The disposable
native browser workflow pins Apps commit
`f7eff236ba61725e953c7b207aef32f95b5ef76f`, verifies shared bytes before building,
and exercises actual AppRoot, AppFrameV2 and AppDraftGuard code. No implicit
execution of a changing default branch is allowed.

## Implementation and verification

1. SDK meaningful failing integration case: missing `useAppDraft` caused the
   real host/client React fixture to fail. Implement the registry and re-run.
2. Real transferable MessagePort/React StrictMode test edits two forms, saves
   one, changes locale, verifies the other stays dirty, removes that owner and
   verifies clean. Unmount cannot send later queued reports.
3. Shared bridge tests exercise ready/close, boolean-only payloads, stale hints,
   unknown content fields and channel flooding. Frame tests verify stale callback
   rejection and clearing after reload/failure/unmount.
4. Router tests cover clean leave, cancel then confirm, history POP, native
   beforeunload cleanup and cleaning the document during a blocked departure.
5. Run Portal typecheck, affected frame/page/layout/backend/bridge tests and SPA
   build. Run SDK tests and every maintained frontend consumer check.
6. Build the native browser fixture, verify its signed-document archive through
   `TestSDKBrowserArchiveDocument`, then run Linux quota/native workflow. Its new
   required `SDK_NATIVE_BROWSER_DRAFT_GUARD` marker must survive child, parent and
   collector, producing `sdk_native_browser_draft_guard` in the evidence artifact.
   A marker string in source is not passing runtime evidence.

Local evidence: 200 SDK tests and workspace typechecks passed. SDK root, template,
demo and maintained Status tests/builds passed; generated Status public assets
have no diff. Python generator/collector/public-fixture tests passed (10/14/2).
The actual built signed browser ZIP passed the Portal document handler test.
Portal SPA build and lint passed with existing unrelated hook warnings. The final combined affected host/frame/page/layout/backend/bridge suite passed
108 tests, and the Portal typecheck passed.

The local disposable HTTP/session/SQLite fixture used actual AppViewV2 and the
SDK in a background browser tab. Editing changed the child's button to Save;
changing Portal to Simplified Chinese preserved that state. Return opened the
Chinese host modal, Cancel retained the document, a host theme change preserved
the dirty state, and confirmed Leave removed the iframe and showed Apps home.
Screenshot `app-draft-guard.jpg` is a local visual artifact, not a production UI
change or completed D5 accessibility/visual acceptance.

Companion Apps PR #30 is stacked on #29. At exact commit
`f7eff236ba61725e953c7b207aef32f95b5ef76f`, all jobs in Apps run 40855391245
passed, including frontend standard, protocol, catalogue and maintained Status
browser/container/artifact checks. Portal Linux runtime evidence remains separate.

## Remaining scope

This completes neither P06 nor the full application platform. Signed in-app
route/history synchronization, persisted drafts, shared dialogs/overlays,
accessibility and D5 acceptance, marketplace lifecycle, capability brokers,
recovery/disable fencing and D6 rollout evidence remain open. Development-only
runtime setup does not authorize production privileges or deployment.
