# Catalogue packages as OCI artifacts

Status: Accepted 2026-11-21 (owner request: index + immutable artifact, packages in GHCR, no GitHub
Release per app version). Builds on [170](170-image-delivery-plan.md) (catalogue `2-dev.2`,
`package_url`) and [157](157-offline-envelope.md) (the `.portalapp` offline envelope v2).

## 1. Problem

A managed catalogue release names its `.portalapp` with `package_url`, a plain https file. The Apps
repository publishes from CI, which signs with the publisher key held only in Actions secrets. Using a
GitHub Release as a file bucket means one Release per app version and mutable assets under a name.
The mainstream shape is an index (the catalogue) pointing at immutable, content-addressed artifacts
in an OCI registry. GHCR is public and allows anonymous pull, so Portal needs no credentials.

## 2. Catalogue format `portal.catalogue/2-dev.3`

`2-dev.1` and `2-dev.2` stay accepted unchanged. In `2-dev.3` a release may carry `package_oci`
instead of `package_url`:

```json
{
  "format": "portal.catalogue/2-dev.3",
  "releases": [{
    "app_id": "market-monitor",
    "publisher_id": "kazuhahub",
    "version": "1.0.0",
    "descriptor_sha256": "<64 hex>",
    "descriptor_url": "https://raw.githubusercontent.com/<owner>/<repo>/catalogue/<...>/app.json",
    "signature_url": "https://raw.githubusercontent.com/<owner>/<repo>/catalogue/<...>/app.sig",
    "package_oci": {
      "repository": "ghcr.io/<owner>/<path>",
      "digest": "sha256:<64 hex of the manifest bytes>"
    }
  }]
}
```

Rules (each refusal fails the whole catalogue, as today):

- `package_url` and `package_oci` are mutually exclusive; `package_oci` is refused before `2-dev.3`.
  A managed release (one with `backend.images`) still needs exactly one of them; a frontend-only
  release has neither.
- `package_oci` has exactly the two fields; an explicit `null` or empty object is refused.
- `repository` is `host[:443]/path`: the host is lower-case DNS labels (or a literal IPv4 address,
  which the dial rules then judge), no userinfo, query, fragment or other port; the path follows the
  OCI distribution grammar `[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*`,
  at most 255 characters in all.
- `digest` is `sha256:` plus 64 lower-case hex. There is no tag: the reference is immutable.

## 3. The artifact

An OCI image manifest, and nothing else:

| Field | Required value |
|---|---|
| `schemaVersion` | `2` |
| `mediaType` | `application/vnd.oci.image.manifest.v1+json` |
| `artifactType` | `application/vnd.portal.app.package.v2` |
| `config` | the OCI empty descriptor: `application/vnd.oci.empty.v1+json`, `sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a`, size `2` (optional `data` must be `e30=`) |
| `layers` | exactly one: `application/vnd.portal.app.package.v2+zip`, a sha256 digest, size 1 to the managed envelope cap |

`annotations` are allowed on the manifest and on each descriptor (`oras push` writes
`org.opencontainers.image.created` and `org.opencontainers.image.title`). Any other field (`subject`,
`urls`, `platform`, an index) is refused, as are duplicate keys. This is what
`oras push --artifact-type application/vnd.portal.app.package.v2 ref app.portalapp:application/vnd.portal.app.package.v2+zip`
produces.

## 4. Fetch

1. `GET https://<host>/v2/<path>/manifests/<digest>` with `Accept: application/vnd.oci.image.manifest.v1+json`.
2. On `401`, read `WWW-Authenticate: Bearer realm="…",service="…"`. The realm must pass the
   catalogue URL policy (https, port 443, no query). Request
   `<realm>?scope=repository:<path>:pull&service=<service>` with no credentials; the scope is the one
   Portal computes, never the challenge's. The JSON answer's `token` (or `access_token`) is bounded
   and must be a plain token string. Retry the manifest with `Authorization: Bearer <token>`.
3. The response must be `200` with the manifest media type, at most 256 KiB, and
   `sha256(body) == digest`. Then parse it by section 3.
4. `GET https://<host>/v2/<path>/blobs/<layer digest>` with the same token, into the resumable staging
   file used for `package_url` (Range/If-Range, survives restart). The staging key is
   `catalogue:oci:<repository>@<manifest digest>`, so a held prefix always belongs to the same bytes.
   The download limit is the layer's declared size, which must not exceed the managed envelope cap.
5. Registries redirect blobs to a CDN with a signed query. Redirects follow the existing rule (at
   most 3, https, same dial policy). `Authorization` is carried only to hops on the host of the
   original request; any other host gets none.
6. The finished file must have exactly the layer size and digest; otherwise it is discarded and the
   download is tried once more from nothing. Then the
   bytes go to the unchanged offline install path: the envelope's descriptor and signature must be
   byte-equal to the catalogue's, which already passed publisher trust.

Every connection, including token and redirect hops, goes through the market client's dial-time
address check, so a private, loopback or link-local address is refused however it is reached.

## 5. Console

The catalogue browser lists releases by application, publisher and version and does not show any
URL; the new field changes nothing there (the browser treats entries as discovery hints and ignores
fields it does not use). The verified release shown after staging is the same for both sources. No
new strings, so no locale change.

## 6. Out of scope (follow-ups)

- Private registries and stored registry credentials.
- OCI image indexes, tags, referrers and cosign signatures: trust stays with the publisher-signed
  descriptor.
- Mirrors: one repository per release.
- A registry that serves the manifest anonymously but challenges the blob, or a token that expires
  during a long resumed download, fails closed with a fetch error; the next attempt takes a new
  token. Re-challenging on the blob is a follow-up if a registry needs it.
