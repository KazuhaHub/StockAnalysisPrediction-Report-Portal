# Signed foreground network requests and online execution checks

Status: development request boundary implemented; local checks pass and exact
revision CI remains required. No broker endpoint, grant issuance or installed
application call is enabled. Full P00–P14, online authority integration and
production acceptance remain incomplete.

## Problem and resulting behavior

A canonical policy by itself cannot bind an outgoing operation to the current
viewer, installation, release or request body. Forwarding arbitrary caller fields
to a correctly constrained HTTP client would still leave that authority boundary
unspecified. A signed request must also remain bound to the exact executor policy
and current online authority at dispatch and before returning data.

`internal/apprunner/control/network_http.go` now verifies one controller-signed
foreground network request. `internal/apprunner/broker/network_call.go` executes
that verified invocation through the existing shared bounded HTTPS executor.
It verifies current enrollment and the signed envelope again at each existing
online authorization checkpoint. This is an internal library path with real
cryptography and TLS tests, not a registered broker RPC or an effective grant.

## Signed envelope

Format: `portal.runner.viewer-http-request/1-dev.1`. Signature purpose:
`portal.app/runner-viewer-http/v1`, using the existing length-framed Ed25519
controller convention. It cannot reuse backend-request or registration signatures.
No new signing-key hierarchy, secret store or database product is introduced.

The signed canonical object contains:

- Current deployment/controller epoch/control revision.
- Request, installation and consumed review IDs; generation and policy revision;
  exact release SHA-256.
- An opaque viewer authority scope, with no username, session cookie or role.
- The complete canonical HTTP policy bytes and SHA-256.
- Exact method, URL, Accept, Content-Type, body length and SHA-256.
- Issued/expires instants in UTC Unix milliseconds.

UUIDs are nonzero canonical IDs. Revisions/generation/time/length use canonical
signed-64-bit decimal strings; values above JavaScript's safe integer limit are
preserved. The envelope is at most the existing 64 KiB command bound, with strict
duplicate/unknown-field and canonical-byte validation. Policy decoding and target/
header/resource checks use the shared platform contract, including its 8 KiB
policy limit. The request body travels separately, is at most 64 KiB, and must
match both signed length and digest. Raw upstream errors are never attached to
validation errors.

The trusted enrollment's public-key fingerprint, deployment, epoch and control
revision must match. The envelope must already be issued, not expired, and have
a positive lifetime at most 30 seconds and at least the declared HTTP deadline.
There is no clock-skew extension for this same-host development protocol. This
format does not provide service/background delegation or a renewable job lease.

A valid signature proves only that the enrolled controller signed these bytes.
It is not proof that a review was consumed, a grant remains active, a user still
has a session/OU membership, or the release can execute. Those facts require the
current authority adapter below; no such production adapter is supplied here.

## Bound executor and checkpoint behavior

`NewNetworkCall(sharedHTTP, raw, signature, body, authority, current)`:

1. Requires a nonnil shared executor and current-authority callback.
2. Verifies signature/enrollment/window and exact separate body binding.
3. Requires canonical policy bytes to equal the executor's selected policy,
   including response limits and concurrency. Neither a wider nor a different
   signed policy is silently substituted into an existing executor.
4. Copies packet, signature and body. Later mutation of the caller's buffers
   cannot change what is sent.

`NetworkCall.Do(ctx)` consumes that call object exactly once, even on denial,
unavailability or an uncertain outcome. Its context cannot outlive the signed
expiry or caller deadline. It uses the existing shared executor's slots and
limits; it does not allocate a new HTTP executor for each invocation.

Before DNS, after DNS immediately before dispatch, and after buffering:

1. Run the executor owner's existing current check.
2. Call `CurrentNetworkAuthority` with a defensive copy of the signed metadata.
3. Require the callback to check current viewer/session, installation state,
   release/generation, exact consumed review/grant, policy revision and controller
   enrollment. Returning only a cached Authority or checking only a signature is
   not an acceptable implementation.
4. Reverify the complete envelope using the returned current enrollment and the
   trusted clock. Rotated epochs/revisions/keys or an expired packet refuse.

Pre-dispatch refusal performs no remote request. Post-IO refusal returns the
existing uncertain error with zero status/content type/body. The remote effect
may already have happened; revocation cannot undo it. HTTP redirects, compression,
TLS checks, DNS pinning, request bounds and no automatic retries remain unchanged.
No packet/body/query or raw authority error is logged by these libraries.

## What single use does and does not guarantee

The atomic single-use flag prevents two goroutines or a local retry from dispatching
the same `NetworkCall` object twice. It is not a durable request-ID ledger and does
not reject constructing another call from the same valid envelope. It does not
survive a process restart or establish exactly-once external effects.

A future private authenticated transport must own invocation/connection identity,
keep these envelopes out of app/browser responses, and never recreate/replay an
uncertain request. An explicit new operation needs a fresh authorization/request
identity. If durable cross-process deduplication or job recovery is required, its
storage/protocol must be separately specified and approved; do not add a hidden
JSON/file ledger. No public listener or caller can use this path yet.

## Concrete integration sequence still required

1. Resolve current Portal reader/session and app-use OU access, then the reviewed
   required/optional capability row. Bind exact canonical policy to the consumed
   plan and active release/generation. A package request alone grants nothing.
2. Construct/sign the envelope from those trusted records and body, not from
   browser-supplied actor, policy, review or controller fields.
3. Add bounded private controller/root framing and OS peer authentication using
   the established endpoint boundary. Signature verification does not replace
   socket ownership, peer identity, active enrollment or response binding.
4. The runner selects an executor owned by installation/epoch/grant revision/policy
   identity, with installation and deployment budgets. Never bypass the budget
   by constructing a new executor per request.
5. Implement the real online current-authority exchange for every checkpoint.
   Prove account/OU deletion, grant/release/epoch revocation, controller loss,
   stale packets and restore pause on that actual exchange. A test callback
   returning a fixture Authority does not prove these properties.
6. Recheck current Portal authority before releasing a buffered reply to the
   application, then add the bounded SDK/host route and reviewed grant admission.
7. Demonstrate the neutral application's request after marketplace installation,
   with no manual backend setup, including uncertain/no-retry and revocation.
   Only then replace the currently declined-provider development profile.

There are no production callers to either the command signer or the new wrapper.
The existing installer, launch snapshots and effective network capabilities remain
unchanged. Secret injection, private-service exceptions, streams and background
service authority are separate work.

## Verification

Tests were written before the command and wrapper existed and failed to compile.
Control tests exercise real Ed25519 keys, exact body and policy binding, copied
buffers, precision above JavaScript's safe range, signature-purpose separation,
malformed/signed-invalid requests, scope/ID/epoch/revision, target/header/size
bounds, canonical JSON, fingerprint/signature and lifetime refusals.

Real TLS executor tests verify the actual received method/path/body, defensive
packet/body copies, three online checks, pre-DNS/pre-dispatch/post-IO revocation,
control revision change, expiry after IO, changed policy/body refusal, zero-value
and missing-current-authority refusal. Repeated and concurrent use of one call
object produce only one request. This is not a test of durable replay prevention
or a real Portal principal adapter.

Local control/broker/platform race tests, targeted signed-call race tests, vet,
Linux amd64 broker test compilation and diff checks pass. CI must run the same
packages plus complete general/PostgreSQL and disposable Linux/native/browser
acceptance at the exact functional revision. There is no schema, grant, SDK
freeze, production, Status migration, catalogue or server beta change.
