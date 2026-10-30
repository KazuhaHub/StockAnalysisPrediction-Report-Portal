# Review and publish a restricted HTTP capability with installation

Status: internal managed installation review/consume, exact grant rows and atomic
activation implemented. Corrected revision
`3a5e5ea1d17158c17852758c2eaa250817ea92fe` passes general workflow
40984401099/job 123831927134 (including actual PostgreSQL), native quota/browser
40984401120/job 123831905094 and daemon 40984401330/job 123831906193. This is one P07 integration slice.
It does not complete the network SDK/HTTP dispatch adapter or the full platform.

## 1. Administrative selection and current runtime

An explicit private development entry point lets a current administrator select
one canonical HTTP restriction for a writable managed application that declares
`network.http`. It uses the existing administrator/session, publisher signature,
trust, package digest and app-use audience review. Generic frontend/readonly
management paths do not acquire this selection.

The selected restriction uses the shared HTTP policy format from document 128:
HTTPS origin, allowed methods/path segments, request/response byte bounds,
deadline and per-installation concurrency. The policy contains no key, credential,
proxy, TLS root or resolver override. There is no ambient network permission.

Both managed and network profiles must come from fresh protected-root observations
for the exact same current Portal runtime binding, endpoint, controller identity,
key, epoch and control revision. Network metadata must report the combined backend
family, and the managed profile must independently support the writable sandbox.
The common transport codec now exposes its exact profile metadata validation;
stored-target validation shares the same profile constructor and actual constants.
A valid metadata object alone cannot manufacture either OS observation.

The private review target stores canonical network policy and the known network
profile, alongside its existing managed runtime identity. These are immutable
review restrictions, not entities or secrets hidden in JSON. Empty new fields
are omitted, preserving existing review bytes. No baseline, table, column,
migration, protocol publication or public SDK freeze is introduced.

Consumption reads the stored policy as a routing hint, then repeats actual
current managed/network observation and the existing complete actor/hash/
signature/trust/runtime/audience transaction. The whole actual target must equal
the reviewed target. Missing/changed providers, capacities, formats or identity
refuse and require a fresh review. Expiry/freshness is rechecked after transactional
writes. A generic writable consumer without network observations cannot adopt
this review, including its idempotent replay.

## 2. Required, optional and unsupported capabilities

One shared selection function derives the complete expected capability set from
the parsed signed declaration and explicit host restrictions:

- Required `network.http` needs an explicitly selected valid policy and provider.
- Optional `network.http` defaults off and is selected only by that same explicit
  administrative path.
- Every other optional capability remains denied with `{}` restrictions.
- Unsupported required capabilities, duplicates, undeclared selections, ambiguous
  policies and incompatible/absent profiles refuse.

The derived set is sorted without modifying the publisher's declaration. Worker
and current-reader validation require exact name coverage, grant state, complete
policy bytes, installation policy revision and original consumed review identity.
A single `granted=1` flag cannot create permission or bypass signed declaration.
Missing, extra, changed or independently granted rows deny access.

Static frontend paths retain their original no-provider restriction. Managed
backend reads replace the old blanket ban on every grant with the exact shared
review/row validator; there is no generic allow-all capability path.

## 3. Pending selection versus effective permission

Consuming the install review stores all capability rows with `granted=0`. A
selected network row carries its exact canonical restriction; a declined optional
row carries `{}`. The consumed review distinguishes these cases. At this point
the application remains pending and has no effective app-use audience.

Only the existing writable activation transaction can publish the selected bit:

1. Validate the still-current installation authority, exact pending capability
   rows, physical writer/start/readiness/registration history and actual boot.
2. Revalidate package/frontend/audience metadata and the current lease/fence.
3. Publish activation, initial app-use audience and ready installation state.
4. Change each explicitly selected capability from zero to one with an exact
   installation/name/policy/review/revision predicate and one-row CAS.
5. Complete the operation and audit, repeat authority/time checks and verify the
   complete active row set before commit.

All five steps commit together. A fault, stale row or expired proof rolls back
ready state, audience and capability publication. No grant is enabled at preview,
queue admission, data preparation or container start. This also preserves the
existing queue's physical-effect accounting: a selected pending policy is not
mistaken for an already effective capability or external effect.

Current launch/backend readers validate the active set; worker paths validate
the pending set. Restored/disabled/removed installations still fail their existing
outer lifecycle gates. Secret, report, storage and service-workload authority is
not implied by a network selection.

## 4. Subsequent root checks

Installation worker dispatch rechecks the reviewed managed and network profiles
at the actual protected root endpoint before physical steps. A changed capability
provider cannot silently become a different reviewed runtime.

Completed own-backend business traffic validates the managed runtime and completed
execution source without requiring an unrelated network-family probe. Its wrapper
first requires a completed managed install. This keeps optional provider outages
from disabling unrelated backend traffic; it grants no network access. The future
network request adapter must independently validate live network support, current
capability and current user authority at every request checkpoint.

## 5. Evidence and limits

Tests were written first and failed without selection/target support. Local
focused capability/managed/launch/activation race tests pass, as does the complete
local internal/app suite (79.423s), vet and Linux amd64 compilation. Driver-paired
row tests distinguish pending/active state and reject changed bits, restrictions,
reviews, revisions, missing/extra names and unreviewed optional grants. Zero-fact
entry-point tests prove that policy/format metadata cannot manufacture a review.
The focused CI group requires PostgreSQL and both driver variants.

The existing actual Linux writable lifecycle fixture now signs a package requiring
`network.http`, creates/consumes its review through the new native-observation entry
points and requires the exact pending policy with bit zero. It rejects both old
consumers. Its existing background worker must still perform actual quota-backed
writer/start/readiness/registration/activation, rollback and reconciliation. An
activation audit fault must leave the bit zero. The resulting active current
viewer source must accept the exact grant and reject a temporarily withdrawn bit.

The quota harness requires `PORTAL_WRITABLE_NETWORK_REVIEW_CURRENT` and
`PORTAL_WRITABLE_NETWORK_GRANT_LAUNCHED` and records both booleans in its evidence.
These markers prove reviewed grant installation/current access only; they do not
prove HTTP execution, a frontend network SDK call, credential use or a public
market install. Their new exact revision run is pending; compile-only or skipped
local tests cannot satisfy the native acceptance.

There is deliberately no public review form, SDK method, automatic startup caller
or default market path enabling this development profile yet. Implement the actual
current-viewer network command/online confirmation, private exchange/result release
and typed SDK/HTTP adapter before exposing that path. Credential binding review,
shared rotation, private use and response projection remain separate next work.
No production service, real credential, Status adoption or beta setting changed.

## Initial native result and correction

Initial 5416055830521ab5f74d45fdbf9b5d5535a110bf passes the new actual PostgreSQL
review/publication group, but quota workflow 40982774476/job 123826562053 fails:
the Portal data-install fixture still bootstrapped a data/candidate-only listener,
so the required network observation correctly refused before admission. The
earlier combined candidate/network fixture was a different endpoint.

Add an explicit `ListenNetworkManagedController` composition that preserves the
checked shared data root/pin and candidate runtime while adding one bounded pool
to the same enrolled journal/endpoint. Existing constructors stay narrower. The
Portal fixture uses this complete composition. Linux compilation first failed
without the new constructor and then succeeds; exact corrected native/full CI is
still required. Do not count the initial failed quota run as passed.
