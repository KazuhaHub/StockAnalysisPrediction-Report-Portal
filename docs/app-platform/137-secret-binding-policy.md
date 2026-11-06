# Canonical secret-binding restrictions

Status: internal policy codec and exact HTTP-policy matching only. This slice
builds on document 136 without activating bindings or credential use. The full
application-platform plan and P07 remain incomplete.

The policy JSON in the approved binding table is a closed versioned restriction,
not a configuration bag. The internal format is
`portal.secret-binding-policy/1-dev.1`; it is not a public SDK freeze.

HTTP bearer and API-key bindings contain exactly `format`, `operation` and
`network_policy_sha256`, in that canonical order. The SHA must be the exact
lowercase digest of the current validated `network.http` grant. A binding cannot
supply a destination, arbitrary header, credential ID, plaintext, ciphertext,
account snapshot or independent entity through this JSON. The separate binding
columns retain identity and lifecycle authority.

The representable `raw_read` policy contains only format and operation. The HTTP
matching helper rejects it even when a caller asks for that operation. There is
no raw-read route, SDK method or permission activation in this change.

Parsing bounds complete UTF-8 input at 8192 bytes, decodes the closed shape and
requires exact canonical re-encoding. Unknown/duplicate/missing/aliased/escaped
fields, nulls, alternate versions, trailing bytes and whitespace refuse. Encoder
validation rejects unknown operations and malformed digests; raw read cannot
carry a network digest. Errors use the fixed custody refusal, not input bytes.

The HTTP matching helper requires the exact operation and current network-policy
digest. Its caller must obtain that digest from a separately validated current
network grant, never from an app request. Parsing/matching grants no authority,
selects no credential, performs no decrypt and runs no request. Current session,
OU, installation, signed release, consumed review, capability row, binding and
credential revisions remain mandatory in the future transaction.

Behavioral tests were written first and failed to compile without these helpers.
They pin canonical bytes and roundtrips, reject ambiguous/injected policy shapes,
reject operation/network mismatches and verify raw read stays uncallable through
HTTP. Fuzzing asserts every accepted byte sequence roundtrips exactly and never
enables the raw operation. Local race tests and vet pass; a three-second fuzz
run completed 6198 executions without a failure. The focused CI credential group
includes these tests. Exact revision CI remains required.

Next: implement exact permission review and consumption, current SQL binding
resolution, affected-installation rotation/revocation and uninstall invalidation.
Then prove a private credential-use exchange and response projection against
intentional secret echoes before exposing any app callable operation. This codec
alone does not fulfill those requirements. Real credentials, production, Status,
server beta settings and the Apps catalogue remain untouched.
