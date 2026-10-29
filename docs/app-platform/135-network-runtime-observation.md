# Observe network support before permission admission

Status: internal read-only runtime observation implemented and locally verified;
exact Linux and combined native acceptance remain pending CI. The credential
storage extension is now owner-approved separately in document 131; no credential
table or persistent implementation is included in this change.

## Why enrollment alone is insufficient

A root endpoint may contain a valid controller key while its network executor is
absent. A managed backend endpoint may also lack the network family. Permission
admission must not infer provider compatibility from key enrollment or generic
runner availability. New internal discovery observes the enabled network family,
exact protocol versions and limits at the protected endpoint, without executing
a request, signing a fake grant or allocating an executor/session.

`ObserveControllerNetwork` uses the established root:GID socket and exact non-root
controller UID boundary. The server authenticates the kernel peer before reading
the family magic. The client authenticates root path/ownership/peer before sending
a fresh nonce, half-closes the fixed 40-byte request and requires an exact bounded
reply and EOF within the existing three-second observation ceiling.

The response echoes that fresh nonce and carries at most 4 KiB of canonical JSON.
It includes current enrollment, signed-request/check/policy format versions,
policy hard maxima, configured global active/cache capacities and whether the
candidate-backend control family is configured on the same listener. The latter
is enabled-control availability, not application health or a running writer.
Actual backend/platform/quota compatibility still requires the existing managed
runtime observation and physical lifecycle checks.

## One source for limits and interpretation

The existing HTTP policy maxima now have named platform constants; policy
validation and runtime reporting both use them. Pool active/cache hard maxima
likewise come from the actual pool constructor constants. This names existing
limits, without raising them or creating a second independently maintained set.

The profile reports both policy maxima (for example up to four per-installation
requests) and deployment configuration (for example only two global active slots).
The smaller effective scheduling capacity always applies. A report is not a
reservation, a promise of simultaneous work, a grant or permission to bypass
that lower global limit.

The decoder accepts only the known exact `portal.runner.network-runtime/1-dev.1`
profile and current request/check/policy formats/limits. Full canonical
re-encoding rejects aliases, unknown/duplicate/missing fields, whitespace,
unsupported versions, altered limits, invalid enrollment and oversized records.
Unsupported/malformed known replies report incompatible; a disabled/old/offline
family reports unavailable. The latter cannot distinguish an absent provider
from a lost connection and must not be presented as a precise health diagnosis.
Both outcomes block required capability admission; neither silently falls back.

`NetworkRuntimeObservation` contains copied current enrollment, scalar profile,
OS endpoint binding and local receipt instant. JSON serialization/reconstruction
refuse. Its caller must compare exact deployment/epoch/control revision/key and
endpoint to current SQL runtime binding, check age against the existing three-
second freshness window and repeat observation when consuming an expiring review.
Copying this value or displaying its metadata creates no lasting authority.

## Co-locating managed and network services

`ListenNetworkCandidateController` explicitly combines the existing checked
candidate runtime and one network pool on the same protected root endpoint.
It requires an enrolled journal, valid concrete native runtime and valid pool
bounds before listening, then reuses `ListenCandidateController`'s native config
checks. There is no automatic production startup or change to existing listener
constructors. Network-only listeners continue to report candidate control false;
ordinary candidate-only listeners do not gain network discovery or execution.

This is infrastructure bootstrap once per deployment, not a user/application
configuration form. No app supplies a socket path, controller identity, Docker
socket, key, CA, HTTP destination or pool factory through this probe.

## Tests and acceptance plan

Tests were written before the profile API existed and failed to compile. Local
canonical/bounded profile tests cover exact fields/enrollment, altered maxima,
unknown versions, invalid capacities, noncanonical/duplicate/trailing bytes,
serialization refusal and defensive key copies. Existing full transport/broker/
control/platform race tests, vet and Linux amd64 compilation pass locally.

Linux actual-socket tests require enabled success, unavailable old/disabled
providers and wrong peer refusal, with no new session or enrollment mutation.
The explicit pre-vulnerability transport CI group now includes those tests.

The disposable real TLS namespace acceptance now observes the actual exported
network-only root endpoint as UID41001, verifies current enrollment/capacities,
protected endpoint provenance and candidate control false, then runs its existing
success/revocation cases. This must pass in CI before the new observation is
counted as verified across UID boundaries.

The actual writable lifecycle native fixture now explicitly uses the combined
constructor for its service/recovery endpoints. Its successful non-root lifecycle
calls first observe network support on that same endpoint, require candidate
control true and verify the exact current controller identity and configured
limits. Existing container/writer/registration/recovery assertions remain; failure
in the added probe fails native acceptance rather than skipping it. Deliberately
invalid old lifecycle calls retain their original refusal assertions.

These are development fixtures. Native quota/container and namespace tests are
not runnable on local macOS; Linux cross-compilation is not a pass result for
them. Exact-source ordinary/PostgreSQL/general, native and daemon CI remains
required. No public SDK/protocol freeze or full P00–P14 completion is claimed.

## Required Portal integration

Use both fresh network and managed runtime observations where backend control is
required, and require them to bind the same operator endpoint/current enrollment.
Then construct an exact reviewed network policy, consumed required/optional grant,
current session/OU/resource authority and signed request. Reobserve at review
consumption and revalidate the actual current SQL grant for every online challenge
and before returning a response. A displayed network profile never substitutes
for these checks. The app-facing route remains closed until that chain exists.

No baseline, migration, credential value, new persistent side store, Status,
catalogue, production startup or server beta configuration is changed here.
