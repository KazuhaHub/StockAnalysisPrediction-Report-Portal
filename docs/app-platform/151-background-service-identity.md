# Background application service identity

Status: H07 development implementation, 2026-10-30.

## Identity and lease

Background work uses a signed application service assertion derived from the
installation's sealed identity seed. It contains no username, account creation,
viewer subject, session, role or OU identity and never inherits the installer's
administrator authority.

The closed assertion binds:

- app and installation IDs;
- current controller epoch;
- installation generation;
- policy revision;
- active release SHA-256;
- one UUID lease ID;
- 1..16 sorted resource names; and
- issue/expiry instants.

The lifetime is 30 seconds to five minutes. The HMAC covers canonical assertion
bytes; changing any identity, resource or time field refuses. Reusing a valid
assertion is possible until its early-revocation conditions or expiry, so the
assertion is an authority lease, not a one-use password or an exactly-once
execution claim.

## Resources

`background.run` is a required install capability for the service principal. It
does not itself become workload authority. An assertion may request only
currently selected and granted resources:

- `reports.read`;
- `storage.files`;
- `network.http`;
- `notifications.publish`;
- `events.subscribe`;
- `tasks.schedule`; and
- `files.export`.

`secrets.use` is deliberately excluded. Background credential use needs a later
explicit service-bound credential decision rather than inheriting foreground
binding rights.

Issuance additionally verifies the active release descriptor, managed install
projection and exact selected capability set. It does not trust a standalone
capability row or synthesize authority from the presence of `background.run`.

## Current authority

Every dispatch or result checkpoint calls verification again in a fresh database
transaction. Verification reloads:

- active platform controller epoch and restore pause;
- installation desired state/phase/generation/policy revision and runtime;
- active release and descriptor;
- exact consumed install capability review;
- `background.run` and every resource in the lease; and
- the installation identity seed.

Disable, uninstall, policy change, release change, capability withdrawal, restore
pause/epoch rotation, missing key material or expired lease refuses without
releasing a result. A worker must repeat verification before submit; a successful
claim is never durable write authority.

## Verification

Tests cover canonical service assertions, no user fields, resource narrowing,
wrong seed, generation/policy/release/epoch/lease drift, expiry, restore pause,
capability withdrawal, install-capability selection and a background probe that
cannot submit after the session-free assertion loses its resource grant.

Durable queue claims, retry/backoff, attempt accounting, external-effect
reconciliation and the production worker belong to H09. This implementation does
not claim that service. Native browser/service acceptance and complete CI remain
evidence gates.
