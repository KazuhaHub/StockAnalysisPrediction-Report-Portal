# Publisher trust management HTTP

This exposes the existing deployment publisher trust repository through explicit
administrator management. It supplies the missing configuration path needed by
the online development market. A catalogue cannot invoke these routes, register
its own public key or broaden its namespace through download metadata.

The implementation is development source, not production trust enrollment,
release-channel configuration or a public protocol freeze. It adds no database
table, column, migration or new persistent configuration layout.

## Source map

| File | Responsibility |
| --- | --- |
| internal/app/app_platform_publisher_http.go | Closed typed inputs, redacted trust directory and explicit first initialization |
| internal/app/app_platform_trust.go | Existing atomic registration/policy/key mutations, now request-context aware |
| internal/app/app_platform_actor.go | Current actor authority and empty-history initialization guard |
| internal/app/app_platform_publisher_http_test.go | HTTP lifecycle, revision/refusal, restore/retained history and cancellation cases |
| internal/app/routes.go | Central administrator route registration |

## Endpoints and boundaries

| Method and path | Purpose |
| --- | --- |
| GET /api/admin/app-platform/publishers | Current public trust configuration plus initialized/paused hints |
| POST /api/admin/app-platform/control/initialize | Explicitly initialize an empty deployment control identity |
| POST /api/admin/app-platform/publishers | Register, change policy, add a rotation key or revoke a key |

All use the existing administrator/session/enrollment and management response
policy. Writes require JSON and the normal same-origin browser check. Bodies are
bounded at 32 KiB, with exact case-sensitive keys and duplicate/unknown/trailing
JSON refusal, including nested key and namespace objects. No URL, private key,
runner endpoint, command, broad app-use grant or arbitrary trust snapshot can be
supplied. Administrator authority is rechecked inside the transaction and again
at its final boundary, including idempotent revocation replay.

All counters and timestamps are canonical signed-int64-range decimal strings.
Request action determines a closed set of fields; fields belonging to another
action refuse rather than being silently ignored. Errors use the existing safe
management envelope/codes, without raw SQL, secret or authentication details.

## First initialization

```json
{"action":"initialize"}
```

This creates the already defined encrypted actor identity and controller epoch
only when all three control markers are absent and every approved platform table
is empty. The shared check uses the closed physical schema inventory, so retained
artifacts and other platform history also prevent treating an old deployment as
new. Initializing does not import a publisher, register a runner, stage a package,
grant a capability or install an application.

The response is initialized=true. Repeating the explicit request on a healthy
initialized controller preserves the same identity/epoch and writes an audit
confirmation. It cannot clear restore pause, replace a partial marker set or
reinitialize retained trust/package/data history. Those conditions require the
separate recovery workflow. Current administrator authority and the initialization
audit commit in the same transaction as the control identity.

GET publishers does not call keyring initialization or create control markers.
On a fresh database it returns initialized=false, paused=false and an empty
publisher list. Missing markers with retained history produce paused=true. On a
restored/partial controller, reads remain available so an administrator can inspect
public trust while writes refuse. These fields are metadata hints, not proof that
encryption, runtime readiness or recovery acceptance has completed.

## Register a publisher

```json
{
  "action": "register",
  "publisher_id": "example-publisher",
  "enabled": true,
  "namespaces": [{"kind":"prefix","value":"notes-"}],
  "key": {
    "id": "release-2026",
    "public_key": "<canonical unpadded base64url Ed25519 public key>",
    "not_before_ms": "0",
    "not_after_ms": null
  }
}
```

The bracketed key text is explanatory, not valid key material. Obtain the public
key from an independently verified publisher channel; never paste a signing seed
or private key. The repository accepts exactly a canonical 32-byte Ed25519 public
key. Algorithm=Ed25519, initial key revision=1 and initial publisher revision=1
are server controlled. Revoked status cannot be supplied on registration.

Namespaces support the existing exact or prefix kinds. Existing global limits,
slug rules, overlapping namespace refusal, nonreused publisher/key identity and
key validity rules remain enforced by ValidatePublisherTrust. A disabled
publisher retains its namespace reservation. Registration never authorizes
business-data access or changes the selected market source.

The response includes publisher_id, revision and, when applicable, key_id and
key_revision, all as strings. No actor seed/epoch or internal authority proof is
returned.

## Policy update

```json
{
  "action":"policy",
  "publisher_id":"example-publisher",
  "expected_revision":"1",
  "enabled":false,
  "namespaces":[{"kind":"prefix","value":"notes-"}]
}
```

The exact existing revision is required. A successful change increments it
atomically, expires unconsumed affected review plans and records the normal trust
audit. A stale revision is a conflict; callers must reload/review current policy,
not silently retry with a guessed revision. Enabling or broadening policy is an
explicit administrator security decision, not a marketplace suggestion.

## Rotation and revocation

Add a new key without replacing old history:

```json
{
  "action":"add_key",
  "publisher_id":"example-publisher",
  "expected_revision":"2",
  "key":{
    "id":"release-2027",
    "public_key":"<new canonical public key>",
    "not_before_ms":"0",
    "not_after_ms":null
  }
}
```

The key has a new ID and revision 1; the publisher revision increments. Rotation
does not automatically enable a disabled publisher or erase old revocation
history. Both keys may coexist during an administrator-approved overlap.

Revoke a specific current key revision:

```json
{
  "action":"revoke_key",
  "publisher_id":"example-publisher",
  "expected_revision":"3",
  "key_id":"release-2026",
  "expected_key_revision":"1"
}
```

The server supplies revocation time and increments key/publisher revisions. It
does not delete key history or accept an unrevocation field. Repeating against
the already revoked current revisions is a no-op with a final authority check;
reusing stale pre-revocation revisions conflicts. Existing launch/staging and
activation gates independently enforce current publisher/key trust. This does
not claim the still-pending managed-process quarantine/notification/recovery UX.

## Trust directory

GET returns initialized, paused and publishers. Each publisher uses the existing
public trust shape: ID, enabled state, revision, namespaces and public key records
(algorithm, validity, revocation, revision and public_key). There are no private
keys, controller seeds, session IDs, cookies, fingerprints of administrator
authority or runtime credentials. Existing trust limits bound the response to
128 publishers / 256 total keys / 256 total namespaces. Reads hold the established
principal/trust lock order and check the current administrator again before
committing the buffered result.

## Verification and remaining work

Tests cover fresh read without initialization, explicit initialization/retry,
registration, revision conflict, disable, revoke, rotation preserving disabled
state/history, paused inspection and refused reinitialization. Separate cases
refuse orphaned trust and retained standalone artifact history, malformed/action-
mixed input, private-key fields, anonymous/nonadministrator and cross-origin
requests. PostgreSQL tests hold and observe the principal advisory lock, cancel
real blocked read/write/initialize requests and require bounded refusal with
unchanged publisher policy. Broader context-aware repository tests remain in
place. Final full local Go/PostgreSQL regression passed (application package
162.080 s), as did final SQLite/PostgreSQL publisher race checks (19.132 s), vet,
staticcheck and Linux amd64 compilation. The cancellation cases also assert no
platform audit committed. This change has no new rendered UI, so no browser
acceptance is claimed.

The next management UI must make the trust decision concrete: show public key and
namespace, require explicit review for registration/enable/broadening/rotation,
and reload on revision conflict. No real publisher key has been enrolled by this
source change. Signed publication, acceptance catalogue hosting, managed lifecycle,
broker/recovery/security acceptance, visual approval and production deployment
remain open in the complete platform plan.
