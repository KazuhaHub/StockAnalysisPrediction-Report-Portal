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
