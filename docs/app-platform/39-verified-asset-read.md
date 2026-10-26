# Verified asset reading under current-reader authorization

This slice implements the actual buffered file read needed after
[current-reader inspection](38-launch-access-inspection.md). It remains an
internal development repository. It does not register an HTTP route, expose a
new bridge method, change the legacy assets handler or wire a production frame.

## Data path

`appplatform.ReadFrontendFile(manifest, archive, name)` performs exact,
case-sensitive lookup in a fully inspected signed-content archive:

1. Reject unsafe relative names before opening the archive. Do not decode URLs,
   strip prefixes, clean traversal or guess a file for an unknown name.
2. Use `InspectFrontend` on the entire archive, not just the selected member. Its
   existing digest, path/type, duplicate, ZIP structure, checksum, entry and byte/
   entry-count limits still apply. An invalid sibling invalidates the read.
3. Find the selected member in the inspector's accepted metadata. Directory names,
   unknown names and different case do not match.
4. Reopen that exact ZIP member into a new buffer using the existing 8 MiB file
   bound plus one sentinel byte. Check close/read errors, exact inspected length
   and SHA-256 before returning the buffer and fixed content-type metadata.

No file is extracted to disk. No manifest path reaches the filesystem. There is no
network fetch, symlink resolution, MIME sniffing, asset execution or persistent
cache. Returned bytes do not alias the caller's archive input. This helper proves
content integrity only; it does not confer publisher trust or access rights.

`Server.readPlatformLaunchAsset(session, expectedBinding, name)` supplies those
rights. It reuses the launch transaction and rechecks the current reader, user/OU
audience, installation, activation, namespace, operation/capability state and
current publisher trust. All six expected binding fields must exactly equal the
current binding: installation, generation, policy revision, entry, release digest
and frontend digest. A binding obtained earlier is not a cached authorization
permit. A changed generation/policy or revoked grant refuses the next file read.

The private `withPlatformLaunchRead` callback runs after locked release integrity
and manifest checks, but **before** the final database clock, session, signing-key
and epoch/pause checks. It may do bounded local reads only. It buffers the chosen
file while those checks run; no response writer, stream or external side effect
is available to it. Only after transaction commit does the wrapper return bytes.
Any callback, final-check or commit error returns a completely empty file result,
even if extraction had already populated the private buffer.

The same transaction already holds the stored release/frontend row locks. The
file read uses that immutable artifact digest, never a legacy app ID's replaceable
files. It reinspects bytes for defense in depth; this makes the work bounded but
potentially expensive (whole-archive inspection per read). A future serving layer
must add request/concurrency budgets and may cache verified content by digest.
It must never cache a positive account/grant decision as content-cache authority.

## Browser transport remains an integration requirement

Do not point `AppFrameV2` at a hypothetical authenticated file route and claim the
browser path works. The frame omits `allow-same-origin`; the
[HTML sandbox specification](https://html.spec.whatwg.org/multipage/browsers.html#sandboxed-origin-browsing-context-flag)
places its document in an opaque origin and blocks ordinary cookie/storage API
access. This repository does not change Portal's `SameSite=Lax` login cookie.
A delivery design must establish how actual subresource loads work with those
boundaries, rather than assume a sandbox is an ordinary same-origin host fetch.
This is an integration inference, not evidence from a browser acceptance test.

Do not relax the sandbox, hand a session/token to app code, make a release public
as a workaround, or turn on a route with unverified CSP/CORS/cache behavior. Before
connecting the production host, implement and test the chosen transport with
real multi-file SDK builds, direct navigation, subresources, logout/revocation,
refresh, failure and superseded generations. Current authorization applies on
any future host-controlled content fetch; this read repository supplies that
boundary without deciding an untested browser transport policy.

No stored record, migration, runtime privilege, secret, beta configuration or
production application is changed. Static and managed installer completion,
physical activation evidence, broker enforcement and P00–P14 acceptance remain
required; a fixture activation is not a physical readiness receipt.

## Verification

Tests first failed because both readers were absent. Pure file tests check exact
HTML/deflated JS/empty CSS bytes, metadata/digests, independent buffers, unsafe or
missing names, mutated archive digest and refusal of an invalid sibling even when
the selected HTML is valid.

SQLite/PostgreSQL tests use the existing explicit logical activation fixture,
then read actual signed fixture HTML. They check every binding field mismatch,
unsafe paths, grant revocation after obtaining a valid binding, completely empty
failure results and unchanged durable dumps.

For each driver, run the relevant tests against a disposable store. PostgreSQL
also exercises the actual file-read wrapper while a release row is held by
another transaction: the test observes its query waiting, expires the reader or
signing key, then releases the lock. The final error must discard the buffered
file. A temporary overlay that returns the populated buffer along with the
error makes both cases fail; the normal wrapper returns an empty result. These
tests share no external service credentials or production data.

Local full Go regression with disposable PostgreSQL, targeted race tests, Go vet,
staticcheck and build passed.

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -race -p 2 ./internal/appplatform ./internal/app \
  -run 'Frontend|PlatformAssetRead|PlatformLaunch|AppPlatformActor'
```

Set `TEST_POSTGRES_DSN` only to a disposable database to execute PostgreSQL cases;
without it those cases skip. Exact-source CI must be checked before treating this
slice as accepted. Tests of repository bytes do not prove sandbox browser loading,
HTTP policy, visual integration or a complete installed application.
