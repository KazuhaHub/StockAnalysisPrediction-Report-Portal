# Reviewed network SDK and host transport

Status: development candidate for H02, 2026-10-29. This is not a released SDK,
production approval, or proof that the disposable Linux browser workflow has run
on a pushed commit.

## Product path

The launch endpoint now advertises `network.http` only when the current
installation has the matching granted row at its current policy revision. The
existing coherent launch snapshot still verifies every capability row against the
consumed install review, signed declaration, generation, epoch, and active
release. A revoked or stale grant makes the launch unavailable; it is never left
as a stale capability hint.

`AppFrameV2` creates `createAppNetworkTransport(binding)` only for an advertised
`network.http` capability. The transport fixes the route to the current
installation and accepts only the shared network wire request. The application
never supplies Portal identity, a review ID, controller, credential, internal
route, or arbitrary headers.

The parent fetch:

- sends same-origin cookies only to the Portal `/network` route;
- sets `redirect: error`, `cache: no-store`, and the bridge abort signal;
- bounds and strictly UTF-8 decodes the Portal JSON envelope;
- accepts only the closed `{status, contentType, bodyBase64}` response;
- preserves a remote 401/403/429/5xx inside the response envelope;
- maps Portal/session/runtime failures to a fixed `AppNetworkError`; and
- never retries a request whose execution state may be unknown.

The bridge uses separate monotonic IDs and pending maps for own-backend and
reviewed network calls. Both channels allow two concurrent calls and fail closed
on duplicate or stale envelope/channel identities. Host work expires after 30
seconds; the child fails after 35 seconds. Late results are ignored after timeout,
cancellation, channel replacement, or capability change.

## Shared SDK

The Apps SDK exposes `useAppNetwork()` beside the other v2 root hooks. It returns
stable `available`, `ready`, and `request` values during appearance updates. The
hook converts a `Uint8Array` body to bounded Base64, decodes the response bytes,
combines the caller signal with the document lifetime, and rejects old closures
after presentation retirement. It defaults `accept` to `application/json` and a
non-empty body to `application/octet-stream`.

The Portal source owns `web/src/lib/network-v2.ts` and its tests.
`scripts/generate_app_contract.py` copies those files into the Apps checkout and
pins both source and generated test bytes. The development bridge protocol is
`portal.app.bridge/2-dev.8`.

## Verification

Local checks completed on the candidate worktree:

- Portal web lint, typecheck, 1810 tests, and production build;
- Apps workspace typecheck, 279 SDK tests, root bundle, template generator,
  plugin standard, and script tests;
- Portal `go build`, `go vet`, full `go test ./...`, and focused race tests;
- Linux amd64 test-binary compilation for the native acceptance files; and
- generation and offline drift checks for both repositories.

The disposable browser fixture now contains a `useAppNetwork` path. When the
network SDK browser is requested, the fixture is installed as the real managed
network application and exercises iframe/MessagePort/host fetch/session HTTP,
the isolated HTTPS fixture, current viewer authority, revoked audience, and no
automatic retry. The Go test requires the `SDK_NATIVE_BROWSER_NETWORK` marker only
in that invocation. It still must run on a pushed commit in the disposable Linux
workflow.

## Remaining gates

- Run the pushed candidate through the Linux native/browser workflow and record
  exact run IDs and artifact/package digests.
- The current-main integration candidate renumbers the unpublished application
  control and credential migrations to `0015` and `0016`; verify its full CI
  before treating the paired SDK commit as stable.
- Do not treat the WIP chain or this local branch as a release, production
  approval, credential-use approval, or market publication.
