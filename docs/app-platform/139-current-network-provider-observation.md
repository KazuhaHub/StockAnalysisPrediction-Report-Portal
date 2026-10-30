# Bind network-provider observation to current Portal authority

Status: private administrator observation adapter implemented; exact Linux
acceptance and current revision CI remain required. This prerequisite is part of
P07's actual permission review path, not completion of that path or an enabled
network/credential provider.

## Why a socket profile alone is insufficient

The transport-level profile from document 135 proves what a protected root socket
answered. Grant review must also prove that Portal still selects that deployment,
epoch, key, control revision and endpoint under a current administrator session.
An old native observation cannot substitute for live database authority.

`observePlatformNetworkRuntime` captures the current administrator fingerprint and
runtime binding inside the existing principal/trust transaction. It reads the
existing DEK and runtime control source, clearing the temporary key buffers. A
missing, disabled, rotated or restore-paused runtime refuses before socket IO.
There is no automatic enrollment or creation of a replacement runtime.

The protected endpoint probe runs outside database locks. It sends no HTTP
request and grants no permission. A second transaction repeats current session,
account generation, actor authority and runtime source checks. It compares the
entire deployment/epoch/control revision/key/fingerprint/binding revision to the
first capture, then verifies the observation:

- authenticated root controller and exact fixed endpoint;
- exact non-root controller UID/GID;
- fresh receipt, neither future nor older than the existing three-second limit;
- exact deployment, controller epoch/revision, key and fingerprint;
- the known transport network profile format, already canonicalized and bounded
  by the transport decoder.

After this transaction commits, the adapter checks cancellation and freshness
again before returning private facts. Those facts refuse JSON serialization and
reconstruction and print only a redacted label. They are point-in-time review
input, not a bearer grant, application health, resource reservation or permanent
compatibility proof. Review consumption must repeat the actual probe and compare
the reviewed profile/policies; dispatch still needs current user/grant checks.

Non-Linux builds refuse this OS boundary. No browser, manifest or app request can
choose a socket path, controller identity or observation through this adapter.
There is no HTTP/SDK route or startup activation caller in this change.

## Tests and exact evidence needed

Driver-paired tests were written first and failed without the adapter. They
reject zero observations, serializable facts, absent current runtime and demoted
administrators, with unchanged persistent state on refusal. Local focused race
tests, vet and Linux amd64 source compilation pass.

The actual hosted Linux root-endpoint acceptance now enables a network family
on its already explicitly enrolled root journal. Its UID41001 Portal child uses
a real SQLite administrator/session/control binding and the exported observation
adapter to sample that root endpoint. It verifies exact capacities/provenance and
nonmutation, rejects a different controller identity/control revision, and then
rejects disabled current SQL state and a demoted administrator. Neither profile
read nor refusal installs an app, creates an effect or changes authority.

The child must emit `CURRENT_SQL_NETWORK_PROFILE_CROSS_UID_VERIFIED`; the parent
requires that exact marker and a successful child exit before emitting the same
marker into native CI logs. A compile-only or skipped local test cannot satisfy
this acceptance. The focused general CI group separately requires PostgreSQL and
runs both driver variants with race detection.

The earlier transport runtime observation revision 4bd40ba4 has already passed
its full general/native/quota workflows; see document 135. That proves its own
scope only. It does not count the new Portal-source adapter/native assertions as
passed before this revision's actual isolated run.

## Next integration

Select canonical required/optional capability restrictions during installation
review, pin the network profile and current runtime, consume with fresh authority
and profile evidence, and persist only the exact reviewed grants. Then validate
those grants in worker/launch/current-viewer paths and connect the bounded private
network exchange to the SDK. Current code continues to refuse effective grants
until those adapters exist. Credential use additionally needs exact binding
review, shared rotation/revocation and response projection against upstream echo.

No schema, production service, real credentials, Status adoption, public SDK
freeze, application catalogue or server beta setting is changed here.
