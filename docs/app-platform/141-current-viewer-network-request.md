# Current-viewer network request authority

Status: private foreground coordinator verified at corrected revision
`b6c3cbd7015ddefa62df6e71e5a88303546fa60c`: general 40989047942/job
123846758444 and native quota/browser 40989047714/job 123846757999 pass.
General includes actual PostgreSQL, staticcheck, zero reachable vulnerabilities
and full internal/app tests (637.127s). This slice itself does not expose a public
HTTP/SDK route; later documents 142/143 record the actual HTTPS and HTTP adapters.

## 1. Inputs and trust boundaries

`preparePlatformNetworkRequest` takes a current host session, installation ID,
current page generation/policy/release binding and bounded HTTP request fields.
These arguments come from host admission; the application cannot provide a
controller key, actor assertion, review ID or policy. Request body is at most
65,536 bytes, target at most 4,096 bytes, method at most 16 bytes and header values
use the shared strict validator. Bodies are copied into owned memory and cleared.

The existing current-viewer backend reader validates the live account/session,
OU audience, installation lifecycle, runtime/trust/restore state and complete
signed installation history. Normal authorized users can request access; there
is no substitution of the original installing administrator. The network-specific
lookup requires the exact active `network.http` row and consumed install review,
canonical projection bytes/hash, reviewed runtime/profile and current policy
revision. A grant bit alone is insufficient.

Signing uses the host controller seed already resolved by that current reader.
The request binds the exact installation, release, generation, current policy and
controller revision, review, canonical network policy, method/URL/header values,
body length/hash and a domain-separated digest of current viewer facts. No raw
username/OU list is placed in the network command. Its signed lifetime is 30
seconds; the foreground coordinator has a 20-second context deadline. Existing
executor policy deadlines remain independently enforced.

## 2. Dispatch and repeated authorization

`requestPlatformNetwork` performs these operations in order:

1. Prepare/sign under the existing bounded current-reader transaction and verify
   the resulting signed bytes again after commit.
2. Outside SQL, observe the actual protected root endpoint. Its peer, controller,
   epoch/revision, freshness and complete network profile must match the current
   source and consumed review.
3. Send one request through `RequestControllerNetwork`. Linux uses the protected
   transport; unsupported platforms refuse. There is no automatic retry.
4. For each transport challenge, compare the whole received command to the pinned
   signed request. Rebuild current viewer/history/grant facts in SQL, compare them
   to preparation, then sign the exact request/challenge proof. Recheck proof and
   original command expiry after commit. No network IO occurs inside SQL.
5. Accept a result only from the private transport observation with the exact
   root/controller peer, request hash and controller identity, received no more
   than three seconds earlier. Repeat current SQL authority and observation
   freshness before cloning the response for the caller.

The initial runtime probe is not reused as a freshness timer for a long HTTP
operation. Current SQL is checked on each challenge, while the protected runtime
independently checks current enrollment. Otherwise a legitimate 15-second policy
operation would always fail a three-second initial-probe timer.

Preparation/permission failures return one fixed unavailable/denied error. A
transport-uncertain outcome or failed post-IO release check returns one fixed
uncertain error and an empty response. Callers must not automatically retry an
uncertain operation. No remote response/error body becomes a permission error.
Owned request intent refuses JSON encoding/decoding, redacts formatting and has
an explicit close operation that clears owned buffers.

## 3. Verification and honest scope

Local `TestPlatformNetworkRequestPayloadBoundary` rejects oversized/invalid input
before authority lookup and refuses serialized intents or zero observations.
Driver-paired `TestPlatformNetworkRequestRequiresGrantSQLite/Postgres` checks
that denied optional capability fixtures produce no command and no DB mutation.
Local PostgreSQL cases skip without a driver DSN; the focused CI step explicitly
requires the PostgreSQL DSN and runs the group with the race detector.

The actual Linux writable-install fixture, after signed package installation,
boot/activation and a successful normal-user backend request, now prepares and
independently verifies a network command and current SQL challenge proof. It
rejects a changed request and withdraws the current grant before the next
challenge, requiring empty proof/signature. It also refuses a missing native
result. Parent acceptance and quota reporting require these markers:

- `PORTAL_NETWORK_CURRENT_VIEWER_SIGNED`
- `PORTAL_NETWORK_CURRENT_GRANT_RECHECKED`

Those new assertions require new-revision Linux execution. They do not execute
HTTPS. The earlier independent transport TLS fixture does not stand in for a
Portal/current-SQL HTTPS exchange. The next integration must demonstrate the
whole coordinator against an isolated TLS endpoint, including before-IO refusal
and after-IO withdrawal with no response release. Then add host HTTP admission,
origin/CSRF/enrollment checks and a typed SDK adapter using the current page
binding. Never expose a route directly around this private coordinator.

This change creates no storage product, migration, credential binding/use,
production permission, catalogue publication, SDK freeze or Status adoption.
Credential-bearing requests need their own current binding/operation policy and
private secret-use enforcement before implementation can claim that capability.

Local regression note: the first combined writable/current-request race run
failed while constructing an existing writable-history fixture in
`TestPlatformWritableAccessRefusalsSQLite/plan`, with operation stage evidence
mismatch. That exact subcase then passed three consecutive race runs. A repeated combined
run failed in the same fixture setup for the `grant` subcase. The combined race
regression remains unresolved; passing focused new cases is not a passing
combined run or a diagnosed/fixed root cause.

Initial revision 7e69b4ab8ed1749c94c6ee5ad0534455513979bc passed native quota/
browser workflow 40987519817/job 123841897884, including the two actual current-
SQL signing/withdrawal markers. General workflow 40987519779/job 123841898193
passed the focused PG request group but failed staticcheck U1000 because the
complete private dispatch path had not been called by tests. The paired denied-
grant fixture now also calls the real coordinator and requires fixed denial,
empty response and no SQL mutation. A corrected revision must pass general CI;
the original failed run is not a pass. Actual HTTPS remains a separate next slice.
