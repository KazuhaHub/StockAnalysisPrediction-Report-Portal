# Actual Portal/current-viewer HTTPS acceptance

Status: corrected revision `4ee32ad20ad2a979a781088dfd9c13b703554af2`
passes native quota 40990619226/job 123851844667 with all three actual HTTPS
markers, both current SQL markers and all eight existing SDK/browser markers.
Its general workflow 40990619214 also passes. The initial failed revision remains
recorded below. This is development-only acceptance, not production approval.

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
its existing test through `unshare --net --fork`. Only that network namespace
brings loopback up and assigns 8.8.8.8/32 plus the validated IPv4 nameserver
addresses from the unchanged system resolver file. A namespace-local bounded
UDP/TCP DNS fixture answers only api.example.com with 8.8.8.8 and returns no
external data. The actual default Go resolver still reads the system configuration;
there is no broker resolver injection. No host interface, route, hosts/resolver
file or trust store is changed.

The runtime retains its existing mount namespace with the Docker daemon. Its
normal owned IPC tmpfs mounts must remain visible to the daemon's bind mounts.
An initial attempt to isolate mounts too made that requirement false and caused
readiness refusal before HTTPS. Preserving mount visibility is not a relaxed
sandbox policy; workload/data/IPC hard quotas and all native ownership checks
remain mandatory. Docker stays on its existing Unix socket/configuration.

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

## 5. Initial native failure and correction

Initial f3dc0f9c8e4569abe08cd4e4441880ab98a15e50 failed quota workflow
40989307256/job 123847581442 before HTTPS: the writable backend never reached
readiness and the existing bounded readiness wait expired. The new private mount
namespace hid IPC tmpfs mounts from the existing Docker daemon. The correction
keeps the original mount relationship and isolates only networking, using the
namespace-local DNS service described above. Corrected-revision native execution now passes as recorded at the top;
the initial failure is not a passed HTTPS test.
