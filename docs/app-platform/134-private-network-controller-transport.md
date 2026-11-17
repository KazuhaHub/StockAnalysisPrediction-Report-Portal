# Private network controller transport and isolated TLS acceptance

Status: development transport implemented; local common-stream tests pass and
Linux cross-compilation passes. Exact Linux unit, cross-UID and network-namespace
acceptance are pending CI. No production startup, app-accessible route, effective
network grant, credential store or real Portal SQL authorization adapter is enabled.
P07 and full P00–P14 remain incomplete.

## Resulting path

The previous signed request/check/pool libraries now have an explicitly enabled
Linux Unix-socket entry point. `ListenNetworkController` retains one shared pool
for its service lifetime and uses current enrollment from the existing runner
journal. `RequestControllerNetwork` is the non-root controller client. The
controller answers fresh challenges on the same authenticated connection instead
of giving the runner an offline permission lease.

This is separate from ordinary `ListenController`: existing endpoints do not
automatically gain network dispatch, and unknown/disabled request families close
without a legacy fallback. No app or browser can select a socket, control key,
private approval callback or pool settings. No production bootstrap calls these
constructors in this slice.

## OS endpoint boundary

The exported listener requires Linux/root, a pre-enrolled existing journal,
valid non-root controller UID and dedicated GID, and the established root:GID
0750 parent/root:GID 0660 socket profile. Reuse existing absolute/canonical path,
symlink/ancestor/ownership checks and exact-inode cleanup. Neither directory nor
system user/group/enrollment is created by listening.

The server checks kernel peer UID before reading even the request magic. Group
membership alone is insufficient. The non-root client must match its configured
UID, and verifies root endpoint ownership, protected directory/GID/socket metadata
and the actual root peer before sending signed data. The common duplex codec
cannot itself create an authenticated NetworkObservation; only the OS-bound
exported client does so after verified framing.

A server admits at most eight sessions, with a 20-second complete connection
ceiling, shorter caller cancellation, the signed request's expiry and the pool's
shorter policy deadline. Individual online confirmations are bounded to one
second. Cancel/connection loss closes the stream and settles local ownership;
there is no automatic request replay or retry.

## Request and confirmation framing

All lengths are unsigned big-endian bounded integers, checked before allocation.
The request contains eight-byte versioned family magic, a fresh 32-byte connection
nonce, a bounded signed-command length, exact command bytes, its 64-byte signature,
a bounded detached body length and body. Verify enrolled signature and complete
body digest/length before pool execution. Bodies are at most 64 KiB, command
bytes at most the existing 64 KiB bound; temporary copies are cleared on return.
Caller-owned buffers/key bytes are snapshotted before external work.

The root connection is registered under its exact request ID and command SHA-256.
At most eight registrations exist; an active duplicate request ID refuses rather
than overwriting a different connection. Confirmation lookup requires the complete
command digest, not just that ID. A request's registration is removed on every
return, including failed writes and cancellation.

One successful execution has exactly four confirmation rounds:

1. Pool admission before cache allocation.
2. Before DNS.
3. After DNS, immediately before HTTP dispatch.
4. After buffering the response, before releasing it.

Each root-to-controller check carries a monotonic 1..4 sequence and fresh random
challenge. The controller signs only after its configured current-authority
callback resolves the exact original request; metadata never comes from an
unsigned replacement sent by root. Responses contain bounded canonical check
bytes and a 64-byte signature. Both sides verify binding; the client also refuses
repeated challenges, excessive rounds and sequence jumps. The runner reloads
trusted local enrollment before/after each round and rejects rotation.

Changing this round contract requires an explicit protocol/version change, not
silently adding a callback to an existing frame. Local scheduling state is not a
durable request-ID ledger: a fresh session from a valid packet is not crash-safe
deduplication. The trusted controller must never recreate an uncertain operation.

## Response and authority observation

The final root response binds SHA-256 of the entire initial connection frame,
including its fresh nonce, signed packet, signature and detached body. The codec
permits one of four fixed outcomes: success, denied, unavailable, uncertain.
Non-success responses must have zero HTTP status, content type and body. No raw
SQL/provider/TLS/transport error text is transmitted.

Success requires all four confirmation rounds, a nonredirect bounded HTTP status,
one bounded printable Content-Type and a body within the exact signed policy's
response limit (also within the executor's 1 MiB ceiling). Both ends enforce size
before copying/allocating the result. Reject unknown codes, malformed lengths,
truncation, extra bytes, wrong frame digest, redirects and incomplete rounds.
The client requires EOF, current context and an unexpired original signed packet.
No upstream cookies, auth headers or redirects are forwarded.

Any missing/malformed reply after sending the request is uncertain: a remote HTTP
effect may already have happened. Refusing an online approval closes the session;
pre-dispatch denial performs no HTTP operation, while post-IO denial withholds all
response bytes. A fresh request ID is not permission to retry an uncertain write.

`NetworkObservation` is constructed only after OS and stream checks. It carries
copied response/controller data, pinned endpoint facts, exact signed request digest
and receipt time. JSON marshal/unmarshal refuse; a serialized blob cannot be
reconstituted as current endpoint evidence. Getters return copies. This remains
point-in-time evidence, not an app/viewer bearer grant. Portal must recheck current
SQL/session/grant/restore authority and exact observation binding before exposing
any body to an application.

## Local tests

Common duplex tests run over real synchronous streams with real request/proof
signatures. The controllable test executor intentionally simulates the HTTP effect
boundary so frame/proof failures can be isolated; this is not described as actual
HTTPS execution. Cases cover all four approvals, bounded reply/request digests,
revocation before dispatch and after IO, zero released data, malformed/truncated/
oversized requests, forged/unbounded/repeated-nonce replies, sequence/missing-check
refusal, extra bytes and authority-observation copy/serialization rules.

Local whole transport/broker/control/platform race tests and vet pass. Linux amd64
transport test compilation passes. Actual Linux socket/UID tests and namespace
acceptance below are not runnable on the local macOS host and remain CI gates.

## Linux and disposable root acceptance

Ordinary Linux unit tests use actual Unix sockets and the existing private-profile
fixture. They cover same-peer success with an interactive test executor, wrong
controller UID, wrong runner ownership, disabled provider and invalid root bootstrap.
Their selected race suite runs explicitly before other long SQL/regression steps.

The existing disposable root `TestControllerIdentityAcceptance` now additionally
runs two network cases. It remains opt-in only on a hosted Linux worker; ordinary
runs skip privileged acceptance.

### Real cross-UID denial before DNS

Use the actual exported root listener/shared pool and an enrolled new owned
journal. UID41002 in the endpoint GID cannot reach approval; UID41001 without that
GID cannot traverse the endpoint. UID41001 with the correct GID receives the first
real challenge and deliberately denies its current fixture grant. No DNS/HTTP is
performed, no result observation is returned, enrollment is unchanged and all
session registrations are removed on shutdown.

### Actual HTTPS in a private network/mount namespace

Spawn one disposable child through `unshare --mount --net --fork`. Only that
namespace brings up loopback, owns a synthetic public-looking `8.8.8.8/32` route
and bind-mounts its own hosts fixture. It has no external network interface or
Internet route. Host routes, resolver, certificate store and accounts are untouched;
namespace teardown removes its mount/network effects.

Inside the child, create an ephemeral CA/leaf certificate for api.example.com,
a TLS server on its own synthetic address/high port, an enrolled owned journal,
and the actual exported root listener with the real broker pool/client. Use only
that process's normal system-root configuration pointing to the public fixture CA;
do not set InsecureSkipVerify or inject a broker resolver/dialer/TLS override.
The broker's usual DNS all-answer checks, public-address filtering, pinned dial,
hostname/chain validation, request/response bounds and no-retry path remain active.

A distinct UID41001/GID41001 child uses the real exported controller client and
signs public development fixture approvals. Check three paths against the actual
TLS server's observed request count:

- Success: four approvals, one real HTTPS request, exact response and authenticated
  endpoint/request observation.
- Denial at pre-dispatch round three: zero HTTPS requests and no response data.
- Denial after IO at round four: exactly one HTTPS request, uncertain result and
  no response data. No retry or replacement request is sent.

The handler verifies actual POST path/body, TLS and absent cookie/auth headers;
it sends a cookie that the transport cannot expose. Every call releases session
ownership. The fixture uses temporary CA/controller keys only, never provider or
production credentials. Approval decisions are fixture callbacks, not real Portal
SQL/session/OU lookups. A passing case proves the private runtime/signature/TLS
chain, not the missing real grant-admission/current-principal adapter.

Namespace setup or any assertion failure must fail the existing root identity
job, not silently skip. Logs require the success marker
`NETWORK_REAL_TLS_CROSS_UID_REVOCATION_VERIFIED`. Exact-source CI result and
native evidence still need to be recorded before claiming these gates passed.

## Required continuation

Implement Portal current SQL/session/OU/installation/release/grant checks and its
private packet/approval signer under current control authority, then final reply
revalidation. Connect required/optional permission review and current grant rows,
one managed runtime binding, and bounded SDK/host calls. Prove restore pause,
revoked/deleted/recreated principals, key/epoch changes and controller loss on that
real path with a neutral marketplace-installed app. Keep all current providers
declined until it exists and passes acceptance.

The credential storage proposal remains pending; this transport creates no new
schema/journal tables or persistent side store. There is no production startup,
Status migration, catalogue publication, stable SDK freeze or server beta change.
