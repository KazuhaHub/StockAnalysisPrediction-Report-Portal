# Fresh online confirmation for signed network authority

Status: internal development confirmation protocol/adapter locally verified;
exact revision CI remains required. Actual private OS-peer transport and Portal
SQL authorization are not implemented by this adapter. No network grant, broker
listener, credential storage or installed application call is enabled.

## Why another signed object is needed

A valid request packet remains valid until its finite expiry even if an account,
OU membership or grant changes. Runtime execution already requires online current
checks, but a cross-process response must bind each check to a fresh challenge
and the complete request. An old success response must not serve as a new online
permission decision or authorize another viewer/body/target.

`control.NetworkHTTPCheck` defines that response. `broker.NewConfirmedNetworkAuthority`
provides the current-check callback used by the existing signed NetworkCall and
shared NetworkPool. It loads current trusted enrollment locally and uses an
injected private controller exchange; neither an app nor a remote response may
select a public key, epoch or revision.

This is a protocol/library boundary with real signatures and HTTPS integration
tests. It does not provision a private listener, authenticate kernel peers, query
SQL, admit grants or complete the marketplace-to-broker path. Those requirements
remain explicit below; the credential D2 proposal is independent and still pending.

## Confirmation format and signature

Format: `portal.runner.viewer-http-check/1-dev.1`. Signature purpose:
`portal.app/runner-viewer-http-check/v1`, framed with the existing controller
Ed25519 convention. It cannot be used as an HTTP request, backend request or
registration command, and those signatures cannot serve as confirmations.

The exact canonical JSON object, in order, has:

| Field | Binding |
| --- | --- |
| format | Internal confirmation version above |
| request_sha256 | SHA-256 of the complete canonical NetworkHTTPRequestCommand |
| challenge | Fresh random 32-byte challenge encoded as 64 lowercase hex characters |
| issued_ms | Canonical nonnegative signed-64-bit decimal UTC milliseconds |
| expires_ms | Same convention; finite short lifetime |

The complete command digest covers actor scope, installation/review/release,
generation/policy revision, exact policy bytes/digest, target/headers/body digest
and timing. No body, plaintext credential or session cookie enters the proof.

Confirmation is at most 2,048 bytes. Typed decoding, canonical complete-byte
comparison and fixed-field checks reject duplicate/unknown/case-aliased fields,
noncanonical whitespace/trailing data, unsupported format, invalid UTF-8, bad
hash/challenge, malformed/overflowing numbers and invalid signatures. The same
bounded command metadata validator used by request verification checks the
referenced command; that validator alone is explicitly not signature or SQL proof.

Lifetime is positive and at most 1,000 ms. The proof must already be issued,
not expired and cannot outlive the original request. Signing clamps expiry to
the earlier of request expiry or now+1,000 ms without overflowing. Zero/malformed
challenges refuse. No successful proof is renewed or reused for another check.

A proof with a valid signature still depends on the controller actually checking
current authority before signing. The cryptographic signer deliberately does
not claim to query SQL. A signer invoked unconditionally by a transport callback
would be an invalid production adapter even though its signature verifies.

## Runtime check sequence

`NewConfirmedNetworkAuthority(enrollment, confirm)` requires both callbacks.
The returned CurrentNetworkAuthority callback:

1. Refuses nil/already canceled context and bounds the complete exchange to one
   second, or the shorter parent deadline. Providers must honor cancellation.
2. Loads trusted current controller enrollment locally, validates key length and
   copies key bytes. Validates bounded command metadata under that enrollment.
3. Copies the command policy and generates a cryptographically random challenge.
   Each invocation creates a new challenge; no cached nonce or success is reused.
4. Calls the private confirmation provider with a separate defensive copy and
   that challenge. A provider cannot mutate the retained command binding.
5. Loads trusted enrollment again after the exchange. Deployment, epoch, control
   revision, fingerprint and key bytes must match the pre-exchange observation.
   Rotation during the lookup refuses even if an old-key response is valid.
6. Verifies canonical proof, complete request hash, current challenge, signature
   domain, current trusted key and fresh timing. Returns only that current trusted
   enrollment. All errors are the fixed broker denied error, with no private
   provider/database/transport error attached or logged.

NetworkCall then repeats verification of the original request signature under
that returned current enrollment. A confirmation never substitutes a new request
or authorizes a packet signed by a superseded controller. NetworkPool performs an
initial check before allocating an executor, in addition to the three IO checks.
The overall request remains bounded by the caller, original expiry and policy
execution deadline. The callback itself holds no mutable per-request state, so
it can be shared; the future exchange provider must be concurrency-safe.

Before dispatch, a failed confirmation sends no upstream HTTP request. After
IO, it produces the existing uncertain outcome with no released response bytes
and no automatic retry. Confirmation cannot undo an effect the remote server
already performed. No stream/private-service/raw-secret capability is introduced.

## Tests and current evidence

Control tests were added before the protocol existed and failed to compile.
They use real Ed25519 keys and test exact request/challenge binding, another
viewer/body, one-second expiry, request-expiry clamping, signature-purpose
separation and signed-invalid/canonical-field/size/timing refusals.

Broker adapter tests were also added before implementation. A real TLS request
uses this callback at all three IO checks, observes three distinct challenges
and six local enrollment reads, and receives the bounded response. Negative
cases cover replay of a previously valid confirmation, another request's proof,
enrollment rotation between observations, private provider errors, cancellation/
deadline, invalid oversized metadata and missing enrollment/callbacks. Failed
preconditions never invoke the controller provider. No real provider credentials,
external endpoint or production service is used.

Full local broker/control/platform race tests, vet, Linux amd64 broker test
compilation and diff checks pass. Exact-source general/PostgreSQL/native/browser
CI remains required. The preceding executor owner has its own separate runtime
evidence; neither that evidence nor these tests establish a real SQL permission
adapter or private authenticated transport.

## Required concrete integration

1. Provide an explicitly configured private control exchange with bounded
   framing, kernel peer/socket identity checks, response-to-request binding,
   context cancellation, and finite in-flight capacity. A signature does not
   replace those endpoint checks. Confirmation material never enters an iframe.
2. On the controller, resolve the current session/account lifecycle/OU audience,
   installation/restore state, publisher/release/generation, exact consumed
   review, effective grant and canonical policy before signing each challenge.
   Use the existing lock order and safe outcomes. Never sign merely because
   the network request previously had a valid signature.
3. Select current enrollment from the trusted local runner journal/configuration,
   not from response JSON. Prove key/epoch rotation and local restore/revocation
   behavior across the actual process boundary with failure injection.
4. Wire one service-owned NetworkPool, with shared installation/global budgets,
   to that exchange; prove grant/OU/account revocation before DNS, before dispatch
   and after upstream IO and lost controller connectivity at each checkpoint.
5. Recheck current Portal authority before exposing the buffered runner result,
   then implement reviewed required/optional grants and SDK/host invocation.
   Keep providers declined until this real chain is accepted with a neutral app.

This slice changes no migration, baseline, persistent store, credential, Status,
public SDK freeze, catalogue, production deployment or server beta setting.
Full P00–P14 remains incomplete.
