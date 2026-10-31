# Shared SDK own-backend channel

Status: experimental development implementation. Not a stable SDK freeze,
production rollout, Status migration or complete P00–P14 acceptance.

## Result and ownership

The Portal parent now has a fixed own-backend HTTP transport and passes it to
`AppFrameV2` only when authenticated launch metadata advertises
`backendAvailable: true`. The server derives this hint from the admitted manifest
and trusted runtime bootstrap. Static apps and unconfigured execution remain
false. The hint is neither a current-health assertion nor an authority grant:
each real request repeats the server admission and independent native checks.
Changing the availability flag retires the frame, like a generation change.

`web/src/lib/bridge-v2.ts` remains the sole protocol source. The contract generator
copies it and its common tests into the Apps SDK with SHA-256 pins. No app gets a
second handshake implementation. Apps uses the shared React/Ant root and exposes
`useAppBackend` from the experimental `@portal-app/sdk/root-v2` entry point. The
maintained legacy template and catalogue/releases are unchanged.

## Protocol and lifecycle

This closed development protocol is now `portal.app.bridge/2-dev.2`. Its init
requires a boolean `backend` hint; `2-dev.1` does not silently receive new dispatch
semantics. Rebuild the development Portal and SDK together. There is no v1/token
fallback, generic network grant or change to platform capability defaults.

The private MessagePort supports:

- `backend.request`: complete installation/generation/policy/release binding,
  document/channel nonces, canonical increasing `requestId`, exact
  `{method, path, body}` request.
- `backend.cancel`: the same envelope and ID, with no payload.
- `backend.result`: the same envelope and ID, with exactly a validated
  `{status, body}` response or one closed failure code.

Both peers reject transferred extra ports and malformed message shapes. Stale
bindings/nonces cannot dispatch. The host rejects reused request IDs; the client
ignores a late result for an already retired issued request and rejects a future
unissued ID. IDs correlate messages; they do not provide business idempotency.

Methods are GET/POST/PUT/DELETE with bounded relative `/v1/` routes; handshake is
reserved and server validation remains authoritative. Bodies are UTF-8 JSON
strings, max 64 KiB; app responses contain validated status and a JSON string,
max 1 MiB. No viewer, OU, authorization, Cookie, headers, destination, socket or
MAC field exists. App errors remain inner statuses; app 403 cannot sign out the
Portal user.

Each client and host tracks at most two pending calls, separate from the server's
two-request deployment budget. The host deadline is thirty seconds; the client
waits thirty-five. Timeout, cancellation and retirement abort owned work. Late
callback completion cannot release a response after deletion of that pending ID.
A submitted write rejected after cancellation/transport failure is uncertain,
not rolled back. There is no automatic retry.

`BridgeBackendError` exposes only `not_ready`, `unsupported`, `invalid_request`,
`busy`, `unavailable`, `uncertain`, with `retryable: false`. Arbitrary exception
messages are mapped to uncertainty and never sent to an iframe. The parent fetch
uses one immutable installation URL, same-origin credentials, no-store and
redirect-error. It bounds streamed response bytes before parsing, validates the
response envelope, and recognizes only explicit known Portal preflight refusals;
unknown/proxy/invalid outputs remain uncertain. Only the outer Portal
`session_expired` response invokes the existing host session-loss handler.

## React authoring rules

Mount one `AppRoot`. Use `useAppBackend()` rather than a second PortalClient or
fetch wrapper. Initial automatic reads must wait for `ready`; it becomes true
after the presentation commit/ready acknowledgement. The request function stays
stable during ordinary language/theme/layout updates, and those updates preserve
mounted state. A new document connection aborts old requests and replaces the
subtree. The hook combines caller and document cancellation and refuses new work
through an unmounted/retired hook reference.

For reads, use an effect depending on `ready` and `request`, abort on cleanup and
ignore results after abort. For writes, use an explicit user action, disable
duplicate submission, preserve the draft and handle business statuses. Show
localized uncertainty and offer a read-only state check; never resubmit a write
automatically after timeout or refresh. The Apps guide `docs/backend-v2.md` gives
a complete React/Ant example and these rules. App-specific deduplication/storage
still requires its own approved design.

## Evidence and limits

Previous gateway commit `ad482b7970f3105cf29b8fdbe20c5258e263bab4` passed disposable
Linux run 40841328334 / job 123338152478: session HTTP, stale bindings, viewer
identity, business call/write counters, interrupted write/no retry and cleanup.
Artifact inspected at `/tmp/app-backend-http-followup-evidence/`.
General run 40841328354 / Go job 123338281960 passed the affected SQL steps and
failed the existing `x/net` v0.60.0 vulnerability gate (GO-2026-6866 / 6860).
Ordinary full Go tests after that gate were skipped. No dependency or gate change.

Current local validation:

- Portal affected frontend tests: 79 passed; typecheck passed. Existing unrelated
  hook-dependency lint warnings remain; SPA build passed.
- Apps SDK: 191 passed, all workspace typechecks passed. Status UI 9, demo 2 and
  generated template 1 passed; template/root/demo/Status builds passed. Generated
  Status public files have no diff. Frontend standard and source/test pin checks
  passed.
- Portal current document/writable launch/gateway race checks passed (56.646s).
  Linux amd64 test compilation, arm64 build, vet and configured staticcheck passed.
  Ten contract-generator tests passed. New CI remains pending until inspected.

Node transferable-MessagePort and jsdom tests prove protocol/React commit behavior,
not rendered browser pixels, true browser opaque-origin behavior or production
readiness. The real backend fixture checks server authority separately; it has
not yet rendered an SDK-built business app in a browser. Full interactive browser
acceptance, autosize/navigation/drafts/accessibility, complete marketplace
install/update/uninstall/recovery, capability broker and D5/D6 remain outstanding.
Server beta configuration, real credentials and deployed Status are unchanged.

## Functional CI and browser follow-up

Portal functional commit `80d5ecede6d51b016d6276f7fc274f4873772eb3` passed
Linux quota run 40843143291 / job 123344119512, including the new authenticated
availability-hint assertion before the required backend session/no-retry markers.
The artifact at `/tmp/app-sdk-channel-evidence/project-quota-evidence.json`
records both backend flags true and `execution_ready=false`.
Apps functional commit `fef8ef7a4efd2da1aca96a4960dffaf6985bea85` passed every job
in check run 40843217337, including shared frontend/root/template standards,
maintained Status browser/backend/protocol checks and release-artifact builds.
The release build jobs validate artifacts only; nothing was published.

The existing local fixture was run with `--portal-http --launch-page` against
these sources, using the real protected Portal launch/document endpoints,
AppViewV2, AppFrameV2, preferences and SDK AppRoot. It uses disposable synthetic
SQLite installation/session facts described in document 42; it does not create
native managed storage or exercise a backend request in the browser.
Only the browser plugin controlled an in-app localhost tab; no native screen
control, real account, production server or credentials were accessed.

Observed sequence:

1. Fresh app rendered `en-US / UTC / 0` in the opaque sandbox.
2. Child button changed to `en-US / UTC / 1`.
3. Host simplified Chinese changed the title/back action and child to
   `zh-CN / UTC / 1`; traditional Chinese changed them to `zh-TW / UTC / 1`.
4. Host theme toggle preserved the same child counter and source. The iframe
   retained `sandbox="allow-scripts"`; no same-origin exception was added.
5. Refresh reauthorized/reloaded the app and negotiated `2-dev.2` again;
   persisted host language remained traditional Chinese, while local child
   counter restarted at zero.

A browser screenshot was saved as `app-sdk-channel-browser.jpg` in the local
visualization output folder. This is a single rendered-state record, not proof
of every paint or absence of all white flashes. The temporary tab was closed;
the fixture exited successfully through its stop path and its temporary directory
was removed. There was no production UI change.

An additional Apps integration test composes the real React root and real shared
MessagePort channel under StrictMode. It proves the initial read waits for ready,
a locale update does not repeat that read, and a button performs one explicit
write through the supplied test dispatcher. SDK tests now pass 192 locally with
workspace typechecks. The test dispatcher is not a native/backend authority
attestation. The complete SDK-browser-native-backend chain remains unverified.
