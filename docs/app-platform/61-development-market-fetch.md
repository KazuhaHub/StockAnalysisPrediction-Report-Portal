# Development catalogue download and verified release staging

This connects a remote development catalogue to the existing signed release
repository. It prepares the online market path without publishing unfinished
packages, changing the legacy catalogue, trusting a remote publisher automatically
or freezing the public SDK. The already implemented installation review/commit
remains a separate deliberate action after staging.

## Sources

| File | Responsibility |
| --- | --- |
| internal/appplatform/catalogue.go | Bounded development catalogue and URL syntax contracts |
| internal/appplatform/catalogue_test.go | Schema ambiguity, limits, duplicate identity and URL refusal |
| internal/app/app_platform_market_fetch.go | Credential-free public HTTPS transport and verify-before-artifact staging |
| internal/app/app_platform_market_fetch_test.go | Actual TLS downloads, tampering, current trust, repeat staging and transport bounds |
| internal/app/app_platform_market_http.go | Administrator catalogue discovery and stage endpoints |
| internal/app/app_platform_market_http_test.go | Auth/input/private-network refusal and in-flight request cancellation |
| internal/app/app_platform_release.go | Context-aware wrapper around the unchanged atomic release repository |

## Development catalogue contract

The temporary protocol identifier is `portal.catalogue/2-dev.1`. It is separate
from the legacy Apps repository index and is not a public version freeze. An
acceptance catalogue uses this shape:

```json
{
  "format": "portal.catalogue/2-dev.1",
  "releases": [
    {
      "app_id": "notes-sample",
      "publisher_id": "example-publisher",
      "version": "1.0.0-beta.1",
      "descriptor_sha256": "<64 lowercase hexadecimal characters>",
      "descriptor_url": "https://example.org/releases/notes/1.0.0-beta.1/app.json",
      "signature_url": "https://example.org/releases/notes/1.0.0-beta.1/app.sig"
    }
  ]
}
```

The bracketed digest is explanatory, not a usable fixture value. Publication must
hash the actual deterministic descriptor and sign those exact bytes with the
existing publisher signature protocol. The catalogue contains no trust keys,
effective capabilities, credentials, executable commands or OCI launch options.
The descriptor's signed `frontend.artifact` is a relative path resolved against
the original descriptor directory; `frontend.sha256` binds downloaded archive
bytes. A redirect does not change this base directory.

Parsing accepts at most 1 MiB and 256 releases. It rejects invalid UTF-8, unknown,
duplicate or case-folded keys, trailing JSON, null release lists, invalid package
IDs/versions/digests, repeated publisher/app/version identity and repeated
descriptor digest. An explicit empty releases array is allowed.

Initial catalogue, descriptor and signature URLs must be canonical HTTPS URLs
with a path and default port/443. Credentials, query strings, fragments, encoded
paths, backslashes, whitespace, dot segments and nondefault ports are refused.
These constraints currently exclude some legitimate encoded-tag/custom-port
hosting arrangements; do not loosen them through an unreviewed generic URL field.

## Discovery and staging API

Both endpoints are POST because they initiate an outbound fetch. They inherit
current administrator/session/enrollment, cross-origin write protection, exact
bounded JSON and no-store response policy from the management routes.

| Endpoint | Required JSON fields | Result |
| --- | --- | --- |
| /api/admin/app-platform/market/catalogue | catalogue_url | catalogue_sha256, releases, publisher_verified=false |
| /api/admin/app-platform/market/stage | catalogue_url, release_sha256 | Verified staged release identity, current trust_revision, reused |

Discovery returns explicitly unverified hints. It does not inspect each release,
grant app access, import trust, save the catalogue URL or admit an installation.
Clients must not present catalogue-provided publisher/version strings as verified
package identity. It uses the current platform administrator/control boundary
before and after network IO; a fresh uninitialized or paused controller refuses.

Staging re-fetches the catalogue and selects the exact requested descriptor
digest. It does not trust a caller-supplied descriptor URL or claimed permissions.
The selected release's app/publisher/version must match the verified signed
descriptor. The catalogue itself may change between discovery and staging;
selection remains bound to the requested immutable descriptor digest. This is not
a signed catalogue freshness/revocation-list protocol, which remains separate
release work.

Staging returns an immutable verified release, not an operation ID. The existing
staged-release directory then exposes it to the installation review UI. Repeating
staging for the same bytes returns reused=true and creates no additional release
or installation. A version/digest conflict is refused by the release repository.

## Verification order and authority

1. Validate input and obtain current deployment trust under administrator/session,
   controller pause and principal lifecycle rules before any network request.
2. Fetch and parse the bounded catalogue. Select the exact requested digest.
3. Fetch the descriptor and require its exact SHA-256 match before fetching its
   detached signature.
4. Refresh administrator/session/trust and database time after network waits.
   Verify the descriptor using the existing Ed25519/key/namespace protocol.
5. Require catalogue identity to match that verified descriptor. This initial
   profile refuses managed backends, unavailable required providers and startup
   writes before downloading the frontend artifact.
6. Resolve only the signed relative frontend path and fetch the bounded archive.
7. Call the context-bound existing release staging repository. It verifies the
   archive, current publisher/key validity, current administrator and restore
   state again, then atomically persists bytes and its audit record.
8. Actual installation review/admission/activation repeat their own current trust,
   audience and permission rules. A successful download is never a launch grant.

Network IO is outside database transactions. A trust or administrator change
during a download cannot leave a partially staged release. Cancellation propagates
through downloads, preflight/commit SQL and contention backoff. Existing keyring
and browser-authentication helpers retain their own shared behavior; this does
not assert a new universal deadline for every Portal authentication operation.

## Transport policy and bounds

The dedicated market client sends fresh GET requests with no Cookie,
Authorization, Referer, client certificate or deployment token. Its transport has
no environment proxy and automatic decompression is disabled. Redirect requests
receive a fresh header map. It never forwards inbound browser headers.

Every concrete dial address is checked against the existing safe-fetch IP policy
plus refusal of nonglobal-unicast, IPv4 0/8 and 240/4 destinations. Private,
loopback, link-local/cloud metadata, CGNAT, multicast, unspecified and IPv6 ULA
addresses are refused. DNS resolution belongs to the context-bound dialer; there
is no additional uncancelable DNS lookup before dialing. Redirects repeat the URL
and concrete IP rules, retain HTTPS and are bounded by the existing three-request
redirect policy. Redirect-only queries are allowed for signed public release
assets, with an 8192-byte redirected URL bound. They do not become an approved
input URL for a subsequent artifact.

| Limit | Value |
| --- | --- |
| Initial URL | 2048 bytes, canonical HTTPS/443, no credentials/query/fragment |
| Each download | 10 seconds; successful status must be exactly 200 |
| Stage request context | 45 seconds total, inherited cancellation |
| Catalogue discovery request | Existing 15-second management context |
| Catalogue | 1 MiB / 256 releases |
| Descriptor | Existing MaxDescriptorBytes |
| Signature | Existing MaxDescriptorSignatureBytes |
| Frontend archive | Existing MaxFrontendArchiveBytes |
| Foreground discovery/stage concurrency | Existing shared two-slot management/document budget |

Bodies read limit+1 and refuse overflow, including chunked responses. Content
length above the limit, nonidentity content encoding, partial status and failed
TLS verification refuse. HTTP uses generic safe error envelopes, without raw
network targets, signed redirect queries or upstream response bodies. No TLS
validation, network policy or publisher trust escape hatch is exposed via HTTP.

## Evidence and next steps

Tests use actual owned TLS servers and temporary stores. Their test-only dial
mapping connects a canonical HTTPS/443 fixture URL to the ephemeral local port;
normal construction never permits that mapping or loopback override. Tests cover
normal signed staging and repeat staging, descriptor/signature/archive tampering,
trust changes before artifact request and before commit, already-canceled and
in-flight canceled requests, absent browser credentials, byte/status bounds,
private/reserved IP and redirect refusal, administrator role and malformed input.
SQLite and disposable PostgreSQL exercise the same release repository path.

Local full Go/PostgreSQL regression passed (application package 160.173 s).
Final market/catalogue race checks passed for SQLite/PostgreSQL (application
package 5.701 s; contract package 1.741 s). Vet, staticcheck and Linux amd64 Portal
compilation passed. This change adds no browser UI; no browser acceptance or real
Internet-hosted signed publication is claimed by these local TLS fixtures.

These tests do not publish a real remote catalogue or provide managed OCI image
verification. The next market UI must distinguish unverified discovery hints from
verified staged releases, then reuse the existing explicit install confirmation.
Trusted publisher onboarding, exact-source signed publication, acceptance
catalogue hosting, provenance/revocation policy, managed lifecycle, full security
and production release gates remain open. The Apps root index and production
image/channel configuration are unchanged.
