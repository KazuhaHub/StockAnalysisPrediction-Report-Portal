# Publisher trust management interface

This development change adds a Publishers tab to the existing application
management page. It uses Portal's React, Ant Design, authentication, translation
and theme providers. The existing platform installation and legacy application
tabs remain separate. No real publisher is enrolled and no production deployment,
server image channel, storage layout or public SDK contract changes here.

## Source map

| File | Responsibility |
| --- | --- |
| web/src/pages/manage/AppPlatformPublishersPanel.tsx | Read trust, edit drafts, explicitly review and confirm mutations |
| web/src/lib/publisherTrust.ts | Typed public trust records and canonical public-key fingerprinting |
| web/src/pages/manage/AppsAdminPage.tsx | Management navigation |
| web/src/pages/manage/AppPlatformPublishersPanel.test.tsx | Confirmation, initialization, registration, rotation, revocation and lifecycle tests |
| web/src/lib/publisherTrust.test.ts | Known fingerprint vector and invalid key representations |
| scripts/app-install-browser-fixture.py | Opt-in loopback fixture with a fresh-control mode |
| internal/app/app_platform_management_browser_test.go | Real HTTP routes with a disposable synthetic administrator/store |

The interface calls the [existing management HTTP endpoints](63-publisher-management-http.md).
It never imports trust from market metadata. HTTP and repository validation remain
authoritative; a successful local draft check does not bypass them.

## Administrator workflow

1. Open application management and select Publishers. A read alone creates no
   control identity or publisher. A fresh store displays an initialization action;
   a recovery-paused store remains inspectable but has no trust-write/resume action.
2. For a genuinely new deployment, select Initialize application platform. Read
   its limited effect, acknowledge the review and confirm. This only creates the
   approved control identity. It does not install an app or trust a publisher.
3. Obtain a publisher ID, allowed application IDs and public key through an
   independently verified channel. Never enter a private key, signing seed,
   service credential or market-provided key without independent verification.
4. Select Register publisher. Registration defaults to disabled. Enter an exact
   application ID (for example notes-sample) or a hyphen-terminated prefix (notes-).
   An empty scope list grants no scope. Disabled publishers still reserve scopes;
   the server refuses overlaps, including overlaps within the submitted list.
5. Enter the public key ID and 43-character unpadded base64url Ed25519 public key.
   IDs use lowercase letters/digits and non-leading dots, underscores or hyphens.
   Public keys decode to exactly 32 bytes and must have canonical trailing bits.
6. Validity fields currently accept canonical decimal Unix milliseconds. Zero
   start time is already valid; empty expiration means no expiry. Expiration must
   exceed start time. These are administrative development controls, not a final
   consumer-facing date-input design.
7. Select Review change. Compare publisher identity, enabled state, old/new
   scopes, key content, validity and SHA-256 fingerprint with the trusted source.
   The hash covers decoded raw key bytes, not the base64url text. No mutation is
   sent by previewing or cancelling. A separate checkbox enables final saving.
8. To rotate keys, add a new key under a new ID and verify its fingerprint. This
   preserves old keys and does not enable a disabled publisher. Revoke the old
   key separately when appropriate; revocation is permanent for that key ID.
9. Policy/rotation/revocation submissions bind the reviewed publisher revision;
   revocation additionally binds the key revision. If saving fails or the result
   is uncertain, writes lock until Refresh trust list. Inspect the newly loaded
   state and review a new action. The browser never guesses a revision or silently
   retries a mutation.

Current account lifecycle tokens discard old-account async previews/results.
Reads abort when the account changes or the tab unmounts. A synchronous in-flight
guard prevents duplicate confirmation while React state updates. An uncertain
POST is never automatically retried by a language change or read refresh.

## Repeatable local browser verification

From the repository root with existing development dependencies installed:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 python3 -B scripts/app-install-browser-fixture.py --fresh
```

Open only its printed loopback PROBE_URL. The fixture uses the real management
page and HTTP handlers with a temporary SQLite store and synthetic administrator.
Its cookie state is private to the temporary directory. `--fresh` and `--market`
are mutually exclusive because the latter requires its preseeded test publisher.
The script strips TEST_POSTGRES_DSN and neither reads production credentials nor
changes production routes. Stop the owned process with Ctrl-C and verify its
listener exits. Build output goes to its temporary directory; normal web builds
must finish before compiling/testing Go because production assets are embedded.

Browser verification exercised explicit initialization, registration as disabled,
raw-key fingerprint review, adding a second key at publisher revision 1 and seeing
both keys retained at revision 2. English, Simplified Chinese and generated
Traditional Chinese text and the shared theme switch were inspected through the
browser plugin. This proves those local interactions, not final D5 visual/mobile
or screen-reader acceptance, real trust enrollment or production readiness.

## Remaining platform work

Signed catalogue publication and acceptance hosting, complete managed backend
execution/broker/lifecycle, final shell design and full release acceptance remain
open. This interface does not authorize production trust enrollment or lift any
D5/D6 gate. Installation authorization remains a distinct subsequent step.

## Validation record

The full web suite passed 153 files / 1697 tests. Added initialization, rotation
and language-lock regressions and the final confirmation guard passed the focused
UI/key/locale suite (45 tests). Typecheck, lint and the final production web build
passed; existing lint warnings are outside this panel. Full local Go tests passed
(application package 105.444 seconds), with PostgreSQL cases skipped because this
UI-only change did not use a database DSN. Focused publisher HTTP/fixture tests
passed in 1.592 seconds. This evidence does not replace PostgreSQL or native
runtime release gates.
