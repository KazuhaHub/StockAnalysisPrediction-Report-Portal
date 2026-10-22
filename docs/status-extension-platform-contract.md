# Generic external application connection contract v1

Status: proposed for S01, storage approval pending. This is a reusable Portal
platform feature, not an API that currently exists in the deployed Portal.

## API ownership

Administrator session endpoints under `/api/admin/apps/{id}/backend` read/save the
one connection for an installed app. GET returns URL host, label, audience,
registration instance, revision, enabled flag, key-presence and redacted policy.
It never returns the shared key or full credential-bearing upstream response.
PUT requires a current revision (zero for create), validates the complete policy,
seals supplied keys, and defaults new registrations to disabled until explicitly
reviewed. DELETE removes the registration and invalidates its traffic. Existing
forced-enrollment and administrator session gates apply to every method.

Installed frontends use the existing `rp:api` message. The only new v1 bridge
endpoint is `POST /api/v1/extensions/{app_id}/request` with this bounded object:

```json
{"method":"GET","path":"/v1/components?limit=50"}
```

An optional `body` is a JSON value for an allowed management request. The iframe
cannot provide headers, URL host, audience, identity facts or a signing key.
Outer POST does not mean this is a management action: the inner GET requires
`extension.read`; inner POST/PUT/DELETE require `extension.manage` and current
administrator identity. No query/ingest/notify substitution is permitted.

The existing host bridge must recognize this exact endpoint shape and permit it
only for the relevant extension capability. Server-side authentication requires
an app-bound ephemeral token for the same app, never a machine API token or an
app A token naming app B. Current installation/scopes/account/session/registration
are repeated checks even if the frontend previously passed a permission preview.
Uninstall/reinstall must change token generation, including a same-version reinstall.

Return the normal v1 error envelope with safe codes. Propagate only allowed
bounded upstream JSON responses, without cookies, locations, credentials, internal
HTTP headers or HTML. Set private response `Cache-Control: no-store`. Cancellation
and a finite deadline cover connection, response headers and body reading. A bad
backend response cannot consume unbounded memory or keep a Portal handler alive.

## Viewer identity facts

The host loads account activation, expiry, session revision, account creation
identity and deletion generation for every request. It derives an opaque subject
from issuer/account identity. It loads the current primary OU and its verified
ancestor chain, refusing cycles, dangling parents, duplicate identity evidence or
excessive depth. OU references must bind to the current group creation identity
and app registration, never just a display name or numeric ID. Confirm stable
identity behavior for rapid deletion/recreation and restore in fixtures before
claiming the boundary complete.

The backend receives only opaque current OU identities and the effective role,
not email, password hash, notification preferences or report data. Opaque identity
derivation uses a separate stable seed from the sealed connection record; signing
key rotation preserves this seed, while registration reset changes the namespace. Management
options may get administrator-authorized principal labels through a separate
explicitly allowlisted platform identity-list endpoint; ordinary readers never
receive the directory. Do not let a frontend invent an OU ID to obtain access.

## Request assertion

Use the shared `packages/extensionprotocol` v1 library in the application monorepo.
The assertion binds method, exact normalized path/query and body SHA-256, issuer,
audience, app ID, registration instance, key ID, random request nonce, 30-second
validity and current viewer claims. It is a server-to-server header, never a
browser token. An assertion authorizes only its signed request.

The backend pins registration/audience/issuer and verifies before dispatch. Nonces
are accepted once with a fixed capacity and no eviction of live entries. The
current library is single-process and its nonce cache is ephemeral. Backend
startup generates a fresh unpredictable boot ID and pins it in the assertion
verifier. A signed host challenge/backend answer binds that boot to the fixed
registration, issuer/audience, existing shared key and exact fresh challenge.
The host verifies that answer before signing a private request for the boot. Old
boot assertions are refused even after the in-memory nonce cache was reset.
A boot is not a new registration or an access grant; restored registrations
remain disabled for operator pairing/review. The shared library implements the
cryptographic handshake, while HTTP endpoints/proxy wiring remain pending.

## Transport policy

Fixed absolute backend root with no userinfo, fragments or query credentials.
Normalize and validate a relative `/v1/` path; reject dot segments, encoded/double
encoded traversal, backslashes, absolute/protocol-relative URLs and ambiguous
slashes. Backend policy consists of explicit method plus segment-prefix rules;
`/v1/components` does not grant `/v1/components-secret`.

Network allowances are scoped to the registration, with explicit host/CIDR/port
constraints for private services. DNS is resolved and validated at dial time;
validate every resolved address, pin the allowed address for that connection and
never follow an upstream redirect. Link-local/metadata/multicast/unspecified
addresses are refused regardless of a broad private CIDR rule. Plain HTTP is
allowed only on a specifically approved private-network connection; TLS validation
remains enabled otherwise. A changed policy/revision invalidates work awaiting
forwarding. Restrict outbound concurrency globally/per registration and separate
it from Portal business queues and notification/authentication executors.

## Required negative and lifecycle cases

Tests must prove wrong app/machine scope cannot forward; non-admin cannot manage;
demoted/disabled/expired/deleted/recreated account loses access; changed OU/ancestor
applies on next request; reinstall/uninstall revokes old token; connection disable
and restore refuse forwarding; cross-app replay/signature/body/path changes fail;
DNS rebinding/mixed DNS/redirect/local metadata/private-network widening fail;
body/header timeouts and bounds apply; backend errors expose no secrets.

The schema proposal is `status-storage-and-platform-proposal.md`. No schema or
route is considered implemented by the contract or shared library alone.


## Pre-storage implementation evidence (2026-10-22)

`internal/app/extension_identity.go` reads existing account/current-OU authority in
one read-only snapshot (repeatable-read on PostgreSQL). The app-bound token expiry,
account activation, civil-date expiry, current session/creation identity and
account deletion generation are checked. Opaque account/OU references are bound to
the application registration and a stable identity seed, with bounded ancestry.

This is an internal projection primitive, not a registered HTTP endpoint. It does
not expose a directory or enable new capabilities. Future token issuance must
capture the current deletion generation into its internal entry; default/unknown
values cannot be used to grant a recreated account. Future dispatch still must
check installation generation, current scopes and connection revision. Storage
and route wiring remain behind the unanswered schema approval gate.
