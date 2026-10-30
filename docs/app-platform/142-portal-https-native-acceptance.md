# Actual Portal/current-viewer HTTPS acceptance

Status: isolated Linux fixture implemented; Linux test compilation and vet pass.
Exact new-revision native execution remains required. This is development-only
acceptance of the private coordinator, not a public SDK/API or production claim.

## 1. Why this case is required

Document 141 proves actual current SQL signing after installation. Document 134
separately proves private root transport against standard TLS with synthetic
approval callbacks. Neither alone demonstrates that the actual Portal coordinator
performs HTTPS using the live viewer and its consumed installation grant. This
fixture joins those boundaries without injecting a grant, signer or HTTP client.

The existing quota acceptance still performs signed package installation,
physical writer/start/readiness/registration, atomic activation and a normal-user
backend request. Only then does it call `requestPlatformNetwork` with the current
page generation/policy/release binding. Existing installation assertions and all
eight SDK browser markers remain mandatory.

## 2. Disposable namespace and TLS

Before opening the root journal or daemon, the managed Portal fixture re-enters
its existing test through `unshare --mount --net --fork`. Mount propagation is
private. Only that namespace brings loopback up, assigns 8.8.8.8/32 and bind-mounts
an owned hosts fixture mapping api.example.com to that address. No host interface,
route, resolver or trust store is changed. The Docker daemon remains reachable
through its existing Unix socket; no daemon configuration changes are made.

The root fixture generates one temporary Ed25519 CA and server certificate valid
for api.example.com, serves HTTPS on namespace-local port 443 and configures the
process-standard CA file/directory before the first HTTP call. The broker uses
its real public-address validation, pinning, standard TLS verification and root
executor pool. There is no custom dialer, insecure TLS flag, injected transport,
proxy or real provider credential. The Portal child still runs as UID/GID 41001;
the protected runtime and actual HTTPS listener run as root.

The root endpoint counts actual HTTPS requests and validates method, host, path,
TLS, Accept, empty GET body and absence of Authorization/Cookie. Only the two
explicit fixture paths are served. Responses are bounded JSON with a Set-Cookie
header that is outside the broker's exposed response shape.

## 3. Three required observations

1. **Authorized normal user:** invoke the actual coordinator for `/v1/success`.
   Require status 200, exact JSON response and one server request. All signed
   root challenges use the actual current SQL approval callback.
2. **Before IO withdrawal:** set the current selected grant to denied in the
   disposable fixture and invoke the coordinator again. Require fixed unavailable
   error, status zero, empty body and unchanged server count. Restore the test row
   for the independent next case.
3. **After IO withdrawal:** invoke `/v1/revoke`. The real HTTPS handler records
   arrival and waits for an exact owned-file acknowledgement. A concurrent Portal
   child worker then withdraws the current grant in its own SQLite store and
   acknowledges only after exactly one row was updated. The handler returns its
   normal JSON response. Require fixed uncertain result, status zero, empty body
   and server count exactly two: the successful first request plus this request.
   This proves the coordinator did not release the post-withdrawal body or retry.

The synchronization is test-only and bounded by contexts. It is not a production
notification channel or durable coordination product. SQL is not held across
HTTPS. Cleanup closes the TLS server and removes only owned fixture files. The
row is restored only so the remaining unrelated acceptance cases can continue;
this does not implement or claim a production grant-management operation.

The root parent, namespace wrapper and Python report all require:

- `PORTAL_NETWORK_HTTPS_CURRENT_VIEWER`
- `PORTAL_NETWORK_HTTPS_PRE_IO_REVOKED`
- `PORTAL_NETWORK_HTTPS_POST_IO_REVOKED`

Corresponding report fields are
`native_portal_network_https_current_viewer`,
`native_portal_network_https_pre_io_revoked` and
`native_portal_network_https_post_io_revoked`. Missing output or nonzero process
status fails acceptance. Source markers alone are not executed evidence.

## 4. Remaining integration

A successful run proves this private foreground network capability against an
isolated standard-TLS service under actual installation and viewer authority.
It does not prove credential injection, reports/files/business-resource access,
service workloads, jobs/events, or application lifecycle/update completion.

Next add the host HTTP admission and typed shared SDK call using existing
origin/CSRF/enrollment/current-page binding, bounded request/response fields,
cancellation and fixed denial/uncertain errors with no retry. Demonstrate that
path through the real browser fixture. Credential-bearing HTTP remains denied
until exact binding/operation policy and private-use enforcement are implemented.
No Status migration, catalogue publication, production activation, SDK freeze,
storage layout or server beta configuration is part of this slice.
