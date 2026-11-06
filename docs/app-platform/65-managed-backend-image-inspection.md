# Managed backend immutable image inspection

This is the image-admission prerequisite for P04/P05 managed backend execution.
It adds a bounded read of an already-present local Docker image, bound to exact
reviewed descriptor bytes and the native image configuration ID. It does not
create a container, pull an image, register a backend, grant capabilities or change
runtime readiness. The static installation worker remains unchanged.

## Source and boundary

`internal/apprunner/driver/image.go` implements `InspectBackendImage`. Its arguments
are a caller context, explicitly configured Docker Unix socket, exact descriptor
bytes and their expected reviewed SHA-256. The result contains only release
SHA-256, reviewed immutable reference, native image ID and Linux platform.

The exported entry point refuses unsupported hosts and uses the existing checked
root-peer Docker Unix transport on Linux amd64/arm64. It has no HTTP URL, registry
credentials, arbitrary API path, Docker command or image-tag input. The total
inspection deadline is five seconds. The HTTP client disables redirects and
proxies; the response is bounded by the existing one-MiB reader. A not-found image
returns an unavailable result; no implicit pull or network fallback occurs.

The request uses the fixed [Docker Engine v1.47 image inspection API](https://docs.docker.com/reference/api/engine/version/v1.47/).
The daemon's reported metadata is an observation from the configured trusted
runtime, not independent content hashing or publisher signature verification.

## Validation sequence

1. Check cancellation before any daemon access.
2. Strictly parse the descriptor and compare its exact-byte SHA-256 with the
   reviewed release. Require a managed backend and an explicitly declared native
   Linux platform. Invalid/static/undeclared-platform requests make no request.
3. Inspect only the descriptor's digest-pinned reference through
   `/v1.47/images/<escaped-reference>/json`. No tag is resolved.
4. Require a canonical native `sha256:<64 lowercase hex>` image ID, `Os=linux`
   and the admitted OCI architecture. Do not use emulation or daemon-info aliases
   such as x86_64. For arm64, accept only the default/empty or v8 variant.
5. Require the requested repository digest in the bounded RepoDigests list.
   Docker Hub's familiar names are equivalent only by removing the exact
   `docker.io/` or `docker.io/library/` prefix of a reviewed Docker Hub reference.
   Other registry hosts, repositories, tags and changed digests never match.
6. Require an object Config and validate its projected Volumes field, including
   duplicate and case-alias rejection. Refuse all declared volumes: Docker can
   create anonymous volumes inherited from image configuration, which would
   otherwise bypass reviewed platform mounts/quota ownership. An empty create
   Volumes object is not treated as removing this inheritance.
7. Recheck cancellation and return the small observation. Never return daemon
   filesystem paths, image environment, entrypoint/command or raw error bodies.

Unknown nonprojected image configuration fields remain bounded and are discarded.
Their presence does not authorize inheriting executable settings in a future
container request. The later container adapter must use the native image ID from
this observation and explicitly override security, mounts, user, network, health
checks, restart and resource policy. It must recheck current authority and use
journalled effects. This observation alone is never an execution permit.

## Tests and disposable Linux check

The portable tests cover exact release/native-image binding, invalid/static
requests without daemon access, wrong digest/registry/architecture/variant,
missing Config, anonymous volume inheritance, case aliases/duplicates, malformed
or oversized responses, redirects, daemon errors, secret-free output and canceled
responses. HTTP fixtures assert the single read-only endpoint and absence of
cookies/authorization/request bodies.

`TestDockerAcceptanceBackendImage` is Linux-only and runs only with an explicit
`PORTAL_APP_RUNNER_ACCEPTANCE_SOCKET`. In that mode a missing digest-pinned
`PORTAL_APP_RUNNER_ACCEPTANCE_IMAGE` fails rather than silently skipping. The
existing daemon-probe workflow prepares the public official BusyBox 1.37.0 image
only on a GitHub-hosted disposable runner, resolves its repository digest, and
checks the real image through the production safe Unix-socket reader. No image
process is executed. The fixture descriptor is synthetic and gets its own newly
computed digest; production descriptors and trust records are untouched.

The workflow's image pull is test setup. The production helper remains strictly
read-only. A successful CI image inspection proves local metadata compatibility,
not container isolation, writer fencing or final platform acceptance. Its native
execution result must be checked on the corresponding commit before claiming
Linux evidence.

## Follow-on work

Implement the closed container create/start/inspect/stop/remove adapter, signed
managed commands and durable ownership reconciliation, controlled image fetching,
automatic backend registration, broker and lifecycle integration. Verify hard
quotas, network/socket isolation, restart behavior and failure recovery in a
supported disposable Linux environment. Keep `execution_ready=false` until the
full required admission and enforcement evidence exists. Production enrollment,
real credentials, Status migration, SDK freeze and server beta settings remain
outside this change.

## Local validation

Runner/control/data/journal/transport plus CLI race regression passed (journal
62.842 seconds). Following final reference-matching changes, driver race tests
passed in 1.368 seconds. Vet, staticcheck and Linux amd64/arm64 driver test
compilation passed. Native image inspection is an explicit CI case; cross-
compilation is not substituted for its actual execution result.
