# Online catalogue to installation review

Application management now connects the development catalogue/staging API from
[61](61-development-market-fetch.md) to the explicit install review from
[60](60-installation-confirmation-ui.md). The host uses React, Ant Design and its
existing theme/language providers. It does not embed a separate market page or
automatically install code discovered in a catalogue.

## Source map

| File | Responsibility |
| --- | --- |
| web/src/pages/manage/AppPlatformMarketPanel.tsx | Deliberate discovery, unverified hints, download/verification and result ownership |
| web/src/api/appPlatform.ts | Shared staged summary and bounded discovery response shapes |
| web/src/pages/manage/AppPlatformInstallPanel.tsx | Current verified release pin and existing explicit audience/review/admission |
| web/src/pages/manage/AppPlatformMarketPanel.test.tsx | Discovery/staging/refusal/retry and stale account/source response coverage |
| web/src/pages/manage/AppPlatformInstallPanel.test.tsx | End-to-end UI boundary from a remote version outside the first directory page |
| internal/app/app_platform_catalogue_http.go | Exact digest lookup using the current signed directory gate |
| internal/app/app_platform_market_http.go | Closed production client entry points plus private shared handler implementation |
| internal/app/app_platform_management_browser_test.go | Optional synthetic TLS market only in the temporary test listener |
| scripts/app-install-browser-fixture.py | Explicit --market browser acceptance mode |

## Administrator flow

1. Open `/manage/apps`. The new platform tab includes an online catalogue input
   and the verified packages/installation section below it.
2. Enter a new-format development catalogue URL and press Browse catalogue. No
   external fetch occurs merely by opening the page or entering text. The URL is
   not saved to server configuration or browser storage.
3. The host displays application ID, claimed publisher and version as plain text,
   visibly labeled unverified. It does not use a catalogue-supplied logo, HTML,
   external image, effective capabilities or trusted-publisher assertion.
4. Press Verify and download for an exact descriptor digest. The server performs
   the existing public HTTPS, signature, current trust and artifact checks. This
   action only stages immutable bytes; it does not create an installation.
5. The client checks the returned digest, then requests the current verified
   summary by that exact digest. A missing/mismatched summary is an error; the
   client never promotes catalogue text into verified package metadata.
6. A verified result is pinned at the top of the staged list, even if its digest
   would otherwise fall beyond the first cursor page. It uses the signed display
   metadata and server compatibility reason. The original discovery list keeps
   its unverified labeling because that directory itself was never signed/trusted.
7. Explicitly choose users/OUs or no audience, review the version/permissions/data
   policy, then confirm installation using the existing canonical plan and
   idempotent operation request. Download success does not skip that step.
8. Background completion and reload resume the same operation UUID. The ordinary
   application hub/launch gates still decide whether the current user may open it.

Staging retries reuse the same catalogue source and exact descriptor digest.
Immutable repository staging handles duplicate bytes without creating another
release or install. An uncertain staging response has no separate installation
identity to replay. Actual installation confirmation retains its stronger saved
plan/idempotency identity as before. Failed fetch/verification is visibly distinct
from a successful empty catalogue.

## Asynchronous ownership

The market panel owns a fresh object identity for every account lifecycle and
catalogue edit. Old discovery or staging responses cannot populate a different
account or a later source, even after switching away and back to the same user.
Unmount invalidates outstanding responses. Discovery POSTs are not automatically
replayed by effects; they remain bounded by the server request deadline/budget.

A synchronous active-request guard supplements disabled/loading buttons. Staging
reports a busy state to the installation panel so conflicting review/confirm,
audience changes and directory pagination cannot race it. Existing confirmation
or an uncertain admitted operation blocks new staging. Discovery can remain
independently editable because it grants/persists nothing; changing its URL clears
old entries and invalidates the old response. An account/host state change during
staging may leave verified bytes in the server but cannot silently install them
or publish a stale success into the replacement UI.

The downloaded-release pin is presentation only and is cleared after successful
admission/account change. Every install review still rechecks current signed bytes,
publisher trust, compatibility and audience; a displayed summary is never a grant.

## Exact staged release lookup

`GET /api/admin/app-platform/releases?release_sha256=SHA256` returns the existing
directory DTO filtered to one exact digest, with an empty next_cursor. It accepts
neither an after cursor at the same time nor malformed/duplicate/unknown query
fields. A missing or untrusted descriptor produces an empty items list under the
same authorization and signature/time checks, not a raw descriptor fallback.
This reads metadata only; it does not decompress artifacts or admit an operation.
The regular paged directory behavior is preserved.

## Isolated browser reproduction

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 python3 -B scripts/app-install-browser-fixture.py --market
```

The script prints a loopback management page and a synthetic catalogue URL.
Use those only in that fixture, via the browser plugin. The opt-in Go test mounts
the normal administrator/origin/body wrappers around the shared market handlers
with a test-only client mapped to an owned TLS server. That server contains one
synthetic signed `notes-market-sample` package. It does not expose production
credentials, alter deployment trust or publish a real Internet catalogue.

Production handlers always construct the closed public-network client. They do
not accept a client factory, loopback flag, TLS override, network policy or socket
from HTTP. The fixture overrides are compiled only into tests. The script excludes
inherited TEST_POSTGRES_DSN, uses temporary SQLite, writes private synthetic
session state, and joins/removes its owned listeners/processes on Ctrl-C.

Browser-plugin acceptance exercised actual directory discovery, unverified label,
TLS/signature/artifact staging, exact verified summary, explicit empty-audience
review, background completion and refresh preserving the operation UUID. The
confirmation showed the downloaded 1.0.1-beta.1 version rather than the already
staged 1.0.0-beta.1 package. A subsequent pass installed it, confirmed the
uninstall dialog's clear-on-uninstall disclosure, completed uninstall, verified
that the same release returned to the installable state, and reinstalled it.
English/Simplified/Traditional UI followed Portal preferences; theme switching
retained progress. The fixture now sets its synthetic session through a
`Set-Cookie` response header instead of relying on browser execution of an inline
cookie script. No production server was used. This is interaction/DOM evidence,
not mobile/pixel, assistive technology or production network acceptance.

## Verification and remaining work

Focused tests cover deliberate-only fetch, unverified discovery, exact digest
staging, separate installation confirmation, response mismatch, stale source and
account callbacks, uncertain retry, host blocking and explicit failures. The
final market/install/legacy/locale focused run passed 43 tests. The full front-end
regression passed 151 files / 1680 tests. An initial full run exposed a retry
assertion querying Ant's transient loading-button name; the test now waits for
the loading-exit state before clicking, and the full rerun passed. Typecheck,
lint and production web build passed, with existing unrelated warnings retained.
Full Go/PostgreSQL regression passed (application package 192.059 s); focused
SQLite/PostgreSQL directory/market race checks passed (18.253 s), as did vet,
staticcheck and Linux amd64 Portal compilation. The exact directory query is
exercised on both database engines. These results do not constitute all remaining
platform acceptance or production release approval.

An official v2 catalogue is not configured or published by this change. Trusted
publisher onboarding, exact-source signed release tooling, acceptance hosting and
real download re-verification remain necessary before a default market source is
appropriate. The legacy Apps root catalogue stays unchanged. Managed app lifecycle,
broker capabilities, updates/uninstall/recovery, full sandbox/network enforcement,
visual/mobile/accessibility approval and production rollout remain open.
