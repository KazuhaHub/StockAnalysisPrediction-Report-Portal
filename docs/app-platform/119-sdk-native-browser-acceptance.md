# SDK browser to native backend acceptance

Status: implemented fixture, awaiting its first disposable Linux CI result.
This document does not claim end-to-end success before inspecting that result.
No production startup, beta configuration, real credentials, schema, dependency
lockfile, published package or Status deployment changes.

## Gap being tested

The previous acceptance separately proved an SDK-built browser presentation and
actual native backend requests. This extension runs the complete request path:
a verified SDK frontend ZIP is signed and staged in a temporary Portal database,
the background install scheduler completes a real isolated backend installation,
and Chromium runs the real SDK React root and Portal iframe host against that
installation's actual protected launch/document/session-backend routes.

The acceptance frontend is not a marketplace release. It is compiled only for a
new disposable GitHub worker, with synthetic test identities and no production
configuration. The normal application installer source is unchanged. Existing
native quota, isolation, source, authority, reconciliation and owned-cleanup
assertions remain required alongside the browser assertions.

## Build and trust

The quota workflow checks out Apps commit
`8536fda3529470fbe366b3188fa060fb7517949f` into a separate hidden directory with
persisted Git credentials disabled. This is the reviewed SDK companion to Portal
PR #320. `generate_app_contract.py --check --apps-dir` must prove that its bridge
source/common-test pins exactly match the Portal checkout before building.
Updating the protocol requires an explicit new reviewed Apps pin; the fixture
never downloads a moving default branch as executable SDK input.

The fixture reuses the existing Node 24, Apps workspace lockfile and Apps Status
browser-test lockfile (Playwright 1.64.1). No new dependency/version is introduced.
Chromium and its system dependencies are installed only on the disposable CI
worker. `build.py` compiles real Portal AppFrameV2 and real SDK AppRoot as classic
self-contained bundles. The ZIP contains only the child entry, JS and CSS. A Go
build-fixture test verifies that the actual ZIP compiles into the opaque sandbox
document before the native tests start.

The parent origin is the fixed trusted bootstrap
`http://127.0.0.1:31935`, embedded before signing. The listener binds that exact
loopback endpoint exclusively and fails if occupied. It does not infer trust from
a query, referrer, arbitrary message or user input. The signed descriptor binds
the compiled ZIP hash and the existing owned readiness-backend image. The
background scheduler still starts from a new queued operation and zero effects.
It alone advances real data/writer/start/registration/activation stages.

## Native and browser execution

The real Portal controller runs as UID/GID 41001. The browser process is launched
by that controller with a minimal explicit environment, an exclusive temporary
HOME and browser storage, and no personal browser profile. The root controller,
backend container profile, quota, network-none isolation and peer checks remain
unchanged. The browser does not gain access to root transport credentials.

After installation, the test demotes the synthetic reader to an ordinary user.
A test-only loopback wrapper issues its synthetic HttpOnly session cookie and
serves the compiled parent assets. All application launch, document and backend
requests pass through the real central Portal routes and current authorization.
Two test-only endpoints disclose only the known launch URL and revoke only that
fixture installation's access; revocation requires the current synthetic session
and exact parent Origin. Neither endpoint exists in production binaries.

The compiled child uses `useAppBackend`, not a hand-written postMessage/fetch
client. The Chromium runner:

1. Waits for SDK backend readiness after the React presentation commit.
2. Reads actual backend state and verifies the viewer is the current ordinary
   user with an opaque subject, not an administrator claim in the app body.
3. Changes host locale to traditional Chinese and waits for the child update.
4. Performs one explicit write with forged body role/subject fields and verifies
   one added business call/write with the same real current viewer.
5. Performs a write whose backend deliberately disconnects after execution,
   observes SDK `uncertain`, then reads state. Both write count and total business
   call count must prove there was no retry or omitted write.
6. Revokes fixture access through the protected fixture control and verifies the
   still-loaded SDK app's next read is refused as `unavailable`.
7. Checks that the iframe still has exactly `sandbox="allow-scripts"`, then closes
   its context and owned Chromium process.

The runner's three success markers are required by the non-root Go child, root
parent, and Python artifact collector. Missing markers or a nonzero process exit
fail the test. The artifact fields are `sdk_native_browser_current_viewer`,
`sdk_native_browser_uncertain_no_retry`, and `sdk_native_browser_revoked`.
`execution_ready` remains false: these fixtures do not declare the full platform
or production rollout ready.

## Bounds and cleanup

Chromium startup has a ten-second bound; owned browser work closes after twenty
seconds. The Go command has a thirty-five-second context, requests SIGTERM on
cancellation and bounds exit waiting. Its temporary loopback server is closed and
joined on every return. The SDK adds at most thirty-five seconds to the existing
native test, so only the opted-in child/parent/outer acceptance deadlines expand
to 95/140/150 seconds; all ordinary non-browser deadlines stay unchanged. The CI
job ceiling grows to twenty minutes to include locked dependency/browser setup.
Individual boot/authority/proof and request deadlines are not relaxed.

The existing signed fixture-owner cleanup still stops/removes only this exact
application/container and releases its IPC storage; it preserves the neighboring
writable application and removes the owned test registry. Browser output reports
only fixed progress/failure stages, never a session cookie, MAC key or raw response
body. No synthetic credential is uploaded as an artifact.

## Local checks and remaining scope

On macOS, `build.py` compiled both real bundles from the exact SDK source;
`TestSDKBrowserArchiveDocument` passed (0.714s). Linux amd64 test compilation,
arm64 build, vet, configured staticcheck, workflow lint, Node/Python syntax and
14 collector tests passed. These checks do not execute Linux Docker or Chromium
against the real native installation; that outcome must come from CI.

Full marketplace discovery/review/install UX, update/uninstall/recovery,
capability broker, autosize/navigation/drafts/accessibility and D5/D6 remain
outstanding. Even a passing request-chain test does not complete P00–P14 or
approve production startup. The deployed Portal and server beta configuration
remain unchanged.
