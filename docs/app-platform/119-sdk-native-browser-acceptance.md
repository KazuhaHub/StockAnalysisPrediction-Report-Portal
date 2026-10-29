# SDK browser to native backend acceptance

Status: the SDK/browser/native request chain passed disposable Linux acceptance
at functional commit `93c26cf5b08e5df0bcd2b6903d58b75ae55b3bb9`. This is
request-chain acceptance, not complete platform or production approval.
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

The fixture reuses the existing Node 24, Portal web lockfile, Apps workspace lockfile and Apps Status
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
client. Its diagnostic result includes a local request sequence and pending
state; the runner waits for the exact button action's settled result rather than
reusing an earlier message. This sequence does not provide backend idempotency. The Chromium runner:

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
14 collector tests and two public-handoff tests passed. The corrected correlated
SDK package passed sandbox compilation (0.699s). Local macOS checks do not prove
Linux Docker/browser execution; the following CI evidence supplies that result.

Full marketplace discovery/review/install UX, update/uninstall/recovery,
capability broker, autosize/navigation/drafts/accessibility and D5/D6 remain
outstanding. Even a passing request-chain test does not complete P00–P14 or
approve production startup. The deployed Portal and server beta configuration
remain unchanged.

## Verified CI evidence

Run 40849595539 / job 123365455189 passed on functional source
`93c26cf5b08e5df0bcd2b6903d58b75ae55b3bb9`. Its uploaded artifact was inspected at
`/tmp/app-sdk-native-browser-correlated-evidence/project-quota-evidence.json`:

| Artifact field | Verified value |
| --- | --- |
| `sdk_native_browser_current_viewer` | true |
| `sdk_native_browser_uncertain_no_retry` | true |
| `sdk_native_browser_revoked` | true |
| `native_portal_writable_background_owned_cleanup` | true |
| `native_portal_writable_background_neighbor_preserved` | true |
| `execution_ready` | false |

Both explicit workflow cleanup steps, owned registry and owned public browser
fixture, completed successfully. These assertions came from a real Chromium
page, real shared SDK/Portal host, protected session gateway and actual isolated
backend after a fresh scheduler-driven install. No native/backend HTTP result
was replaced by a browser mock. The source remains a development acceptance
fixture; it does not prove a marketplace user can yet install/update/uninstall
through the complete product UI.

Earlier runs are not success evidence:

| Run | Observed failure and final correction |
| --- | --- |
| 40846011428 | Clean worker lacked Portal web dependencies; install from the existing lockfile before building the real host. |
| 40846254014 | UID 41001 could not traverse private worker directories; copy only public runtime files into an owned read-only `/tmp` tree, without changing worker-home permissions. |
| 40847285556 | Fixture UI read `viewer.subject`; the shared wire contract uses `sub`. Preserve the ordinary-role and opaque-subject checks. |
| 40848577626 | Runner read the previous error while a new request was pending; correlate each UI action with its own settled sequence. Preserve all business call/write counters. |

The public handoff excludes the repository, credentials and personal profiles.
Only copied public frontend, Playwright/core runtime and Chromium files are
readable by the non-root controller, and none are writable by that UID. Cleanup
requires exact owned path/UID plus a format marker and refuses foreign or
unmarked trees. Tests verify those boundaries.
