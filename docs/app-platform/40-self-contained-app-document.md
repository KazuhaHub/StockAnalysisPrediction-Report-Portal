# Self-contained application document transport

This development implementation addresses a browser delivery problem left by the
[verified file reader](39-verified-asset-read.md). It is a generic compiler plus a
current-reader repository, not a production route or an installer completion path.
The full phone-style application platform objective remains unchanged.

## Why this transport

The sandbox intentionally omits `allow-same-origin`. Subresources must not need a
Portal session cookie or a token handed to app code. A local browser probe showed
that parent-created blob URLs failed as script, stylesheet and image sources in
this opaque sandbox. A second probe loaded a private authenticated document whose
resources were embedded as data URLs: scripts, styles and images loaded while
parent DOM, cookie and localStorage access remained denied. Its test fetch was
also denied by the response CSP.

The chosen development profile compiles verified local package files into a
single private HTML response. The host browser makes the authenticated document
navigation; the app needs no further authenticated static resource requests.
Original signed archive bytes remain immutable. Derived HTML has its own digest
and retains the source archive digest. This profile is provisional, not the final
public SDK contract. Module splitting, workers, dynamic resource loading and
backend capabilities still require explicit compatible tooling and broker work.
Do not broaden the sandbox or make private archives public to support them.

## Compiler and limits

`appplatform.BuildFrontendDocument` first runs the complete archive inspector.
It uses the same exact member reader as `ReadFrontendFile`, with one inspected ZIP
index to avoid repeating whole-archive inspection per referenced resource.

- Tokenize bounded UTF-8 entry HTML before tree construction: at most 8192 tokens
  and 64 nested levels. The rendered tree also has a depth bound.
- Parse with the existing `golang.org/x/net/html` dependency. Refuse duplicate or
  namespaced attributes, foreign embedded namespaces, event-handler attributes,
  base/embedded-frame/object elements, HTTP-equivalent meta directives, non-UTF-8
  charset declarations and unsupported resource/link kinds.
- Replace classic script sources with base64 data URLs. Inline classic scripts
  become data resources too. Preserve ordering and ordinary defer/async attributes;
  do not execute or analyze JavaScript. Module script declarations are unavailable.
- Embed local stylesheets and rewrite CSS resource URLs to verified local image
  or font bytes. Resolve relative paths within the archive, not a filesystem.
  Already embedded image/CSS-resource data URLs remain local. Reject external,
  query-bearing, escaped/ambiguous or missing resource references.
- Tokenize CSS with the existing Gorilla CSS scanner. Refuse imports, escaped
  at-keywords/functions, unsupported URL spellings and malformed tokens. This is
  a bounded build profile, not a general CSS optimizer or arbitrary-web mirror.
- Embed local images and supported icons. Do not guess replacements for missing
  resources. Return a fixed unsupported-profile error with no partial document.
- Bound total encoded resource expansion and final rendered bytes to 24 MiB.
  Repeated references consume the expansion budget even when encoded bytes were
  cached. Existing 8 MiB/member and archive/total/entry limits still apply.

The compiler neither sanitizes a trusted app into trusted host code nor authorizes
execution. App JavaScript remains untrusted and runs only under the sandbox and
CSP. The source descriptor/signature and current audience must be checked by the
caller. Unsupported behavior must be made explicit during future installation
review rather than discovered only after an application has been activated.

## Response policy

The compiled result includes this required HTTP response policy:

```text
sandbox allow-scripts; default-src 'none'; script-src data:;
style-src data: 'unsafe-inline'; img-src data:; font-src data:;
connect-src 'none'; worker-src 'none'; frame-src 'none'; object-src 'none';
base-uri 'none'; form-action 'none'; frame-ancestors 'self'
```

This policy blocks fetch/WebSocket and undeclared subresource families. It does
not establish comprehensive network isolation: an opaque frame may navigate its
own document, and ordinary CSP does not reliably prevent every such navigation.
This is an implementation inference from the separation of fetch and navigation
checks in the [CSP specification](https://www.w3.org/TR/CSP/), not a claim of
completed exfiltration testing. The host observes a later load and renegotiates,
but that cannot undo a request
already sent. Do not expose sensitive broker output or claim the frontend
network-policy acceptance gate closed until navigation/exfiltration policy is
explicitly resolved and tested. This remains an open architecture/acceptance
requirement, not permission to weaken the current sandbox.

A future handler must set it on **all** document responses, including direct
navigation. Also require `Cache-Control: no-store`, `X-Content-Type-Options:
nosniff`, `Referrer-Policy: no-referrer` and fixed HTML content type. Do not return
these bytes through the legacy public file endpoint. Never cache authorization
with the content, emit a partial response before verification or expose a request
parameter that can weaken this policy. This slice registers no handler.

`Server.readPlatformLaunchDocument` compiles only after current session, user/OU
grant, installation/generation/policy, namespace, release signature and archive
checks. It compares the complete expected binding in the same transaction, holds
all output privately, and returns only after the final current session/key/epoch
check and transaction commit. Errors discard content, CSP and digests. No new
persistent records or credentials are introduced.

## Reproduce the real host/SDK browser fixture

Use Portal plus the Apps checkout containing the experimental root, with their
normal dependencies already installed. No production DB or service key is needed:

```sh
python3 scripts/app-document-browser-fixture.py --apps-dir /absolute/path/to/Apps
```

The script creates only a temporary directory and loopback server. It builds the
actual Portal `AppFrameV2` and actual Apps React/Ant `AppRoot` as classic bundles,
uses a known fixture-only HttpOnly/SameSite=Lax cookie, invokes the Go compiler
through the explicitly requested fixture-export test, and prints `PROBE_URL`.
The fixture app's trusted parent origin is the exact selected loopback origin.
It does not read the viewer's real Portal session or derive trust from a message.
Open that URL through the browser plugin. Stop the script after acceptance.

Verified in the local in-app browser:

1. The real frame/bridge/root loads and displays `zh-CN / Asia/Shanghai / 0`.
2. Clicking the app's Ant button changes its local counter to `1`.
3. The host's Traditional Chinese button changes the same app to
   `zh-TW / Asia/Taipei / 1`, preserving its state.
4. The host's English/light button changes it to `en-US / UTC / 1`.

This is browser integration evidence for this temporary fixture, not a pixel-level
no-flash claim, cross-browser acceptance, production auth/CSRF evidence, visual D5
approval or proof of real install/provider readiness. The fixture intentionally
uses no database; the repository's current-user checks are independently tested
with SQLite and disposable PostgreSQL. The future actual HTTP route must combine
these paths and prove direct-navigation, logout/revocation, refresh/failure and
superseded-generation behavior before the product host is connected.

## Source verification and remaining work

Tests first failed because the compiler was absent. Unit cases cover local
HTML/JS/CSS/image/font embedding, precise integrity, independent input, inline
scripts, unsupported HTML/CSS, invalid archives, depth and repeated-expansion
limits. The actual React/Ant SDK build also compiles through this path.

Compiler race checks, vet, staticcheck, build and the real SDK fixture export
passed locally. The full Go suite with disposable PostgreSQL is still running;
exact-source CI is required for this new slice.

Both database drivers check document reading, complete binding mismatch, grant
revocation and unchanged durable dumps. PostgreSQL holds an actual release lock,
observes the document query waiting, expires the reader or signing key, then
releases it. The derived document must be discarded on final failure.

Next implement actual authenticated HTTP response and request budgets, bind the
host source to authorized generation/release, and integrate package compatibility
review. Sizing/navigation/drafts, physical activation/provider receipts, automatic
managed backend lifecycle, broker capabilities and the rest of P00–P14 remain.
No Status migration, production deployment, public SDK freeze or server beta
configuration change is part of this slice.
