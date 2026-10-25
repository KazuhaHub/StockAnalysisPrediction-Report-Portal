# Package, capability and interface contracts

Status: proposed v2 contracts. Names in this document are reserved design names,
not callable endpoints. Freeze them at D3 after implementing parser fixtures.

## 1. Version and identity rules

Keep these separate: Portal release, runner protocol, package format, SDK version,
application version and application data schema. Compatibility declares ranges
for the dimensions actually used. Unknown required fields or capabilities fail
closed. Unknown optional metadata can be ignored only where the schema permits it.

An app ID identifies a product. A publisher binding prevents a different publisher
from taking over that ID. An installation ID survives a normal update. An
activation generation changes whenever code or effective capabilities change.
Data schema changes do not implicitly rotate the user/OU identity namespace.
Uninstall/reinstall and disaster restore are separate lifecycle events; see
[the lifecycle specification](03-lifecycle.md).

## 2. Logical package and descriptor example

The logical package is a signed release descriptor plus digest-addressed artifacts.
The `.portalapp` offline envelope contains the same descriptor/signature and all
required blobs. An online install can fetch those blobs individually. Both enter
the exact same validator and reviewed install plan.

```text
descriptor.json
descriptor.sig
blobs/<sha256>/...       # Frontend archive and optional OCI image layout
```

The following JSON is illustrative. Digest placeholders are deliberately invalid
and must not appear in accepted packages. It is not the legacy `app.json` format.

```json
{
  "format": "portal.app/v2",
  "id": "scheduled-digest",
  "version": "1.0.0",
  "publisher": "example-publisher",
  "compatibility": {
    "platform_api": ">=2.0.0 <3.0.0",
    "runner_protocol": ">=1.0.0 <2.0.0"
  },
  "presentation": {
    "name_key": "app.name",
    "description_key": "app.description",
    "locales": ["en-US", "zh-CN", "zh-TW"],
    "routes": [{"id": "home", "path": "/", "layout": "document"}],
    "entry": "index.html"
  },
  "frontend": {"artifact": "ui.zip", "sha256": "REQUIRED_SHA256"},
  "backend": {
    "kind": "oci",
    "image": "registry.example.invalid/apps/digest@sha256:REQUIRED_DIGEST",
    "platforms": ["linux/amd64", "linux/arm64"],
    "protocol": "portal.backend/1",
    "resources": {"memory_mib": 256, "cpu_millis": 500, "pids": 64}
  },
  "capabilities": {
    "required": ["storage.files", "tasks.schedule", "background.run"],
    "optional": ["notifications.publish"]
  },
  "data": {"schema": 1, "readable_schemas": [1], "writes_on_startup": false}
}
```

No backend object means a static app. It must not secretly launch server work.
Package limits and allowed file types are enforced before extraction, after
decompression and when serving assets. Reject duplicate names, traversal, encoded
path ambiguity, symlinks, devices, ZIP bombs, oversized archives and extra blobs
not in the descriptor. Use bounded JSON with duplicate-key detection. The app ID
is a bounded lowercase slug; route identifiers and paths are normalized separately.

The default backend contract is HTTP over the installation's provided Unix socket.
`GET /health/ready` is safe and credential-free; private app requests require the
platform assertion. Secrets and configuration never belong in image layers,
frontend archives, descriptor metadata or environment dumps.

## 3. Publisher trust and artifact verification

Proposed first-release trust: a deployment-managed allowlist of publisher public
keys bound to application ID namespaces. A valid signature is necessary but is
not a grant, compatibility proof or claim that code is safe.

The publishing tool signs the exact deterministic descriptor bytes. Artifact
digests inside those bytes bind frontend, backend index and platform images.
The descriptor cannot cover its own detached signature. Canonicalization rules,
algorithm/key-ID policy, test vectors and key-rotation procedure must be selected
at D4; do not improvise a cryptographic format in the installer.

Verification order:

1. Fetch a bounded catalogue; validate transport and schema.
2. Resolve an immutable descriptor and verify publisher/key/namespace trust.
3. Resolve all artifacts by signed digest; verify every downloaded byte.
4. Verify backend image platforms, minimum runtime and declared limits.
5. Produce an install plan containing package digest, permissions, data effects,
   expiry and a trust-policy revision.
6. Display that exact plan. At commit, recheck its digest, revision, current
   administrator authority and freshness. A changed plan requires new review.

Transport follows existing safe-fetch rules and never sends a Portal cookie/token
to a catalogue, registry or publisher. Redirects and registry authentication need
an explicit bounded policy. An unavailable revocation list permits opening an
already trusted installed app under cached policy, but blocks new installs and
updates whose trust cannot be established. A revoked publisher blocks new code
activation. Quarantine running code on an applicable emergency package revocation;
retain data, record why, notify administrators and require deliberate recovery.

Repository/market switching does not add trusted execution publishers. The UI
distinguishes publisher identity, administrator trust and package verification.

## 4. Capability registry

Every capability definition has an identifier/version, human-readable locale keys,
required parameters, grant scope, resource restrictions, revocation behavior and
audit policy. A requested capability is not effective until granted.

| Proposed capability | Restrictions and authority |
| --- | --- |
| `reports.read` | Current viewer/report visibility; a separate approved service grant is needed for background reads |
| `storage.files` | Installation namespace, relative paths, quota, file count and safe content handling |
| `tasks.schedule` | Named registered handlers, minimum interval, concurrency, timeouts and retry ceiling |
| `events.subscribe` | Allowlisted event types and current resource authority; no raw global event stream |
| `notifications.publish` | Source-bound event namespace, rate limits and centrally selected recipients/routes |
| `network.http` | Approved origins, methods/path segments and data/time limits |
| `network.stream` | Explicit protocol/destination/port policy; disabled until adapter tests prove enforcement |
| `secrets.use` | Specific binding and broker operation; never arbitrary vault enumeration |
| `secrets.read` | Exceptional raw access; separately reviewed per binding with disclosure warning |
| `background.run` | Approved service work, leases and finite authority duration; no viewer impersonation |
| `files.export` | Approved file kind/size and deliberate user download |

Required capability refusal cancels installation. Optional refusal installs the
app with the corresponding feature visibly unavailable. Runtime checks enforce
denial even if the app ignores the UI hint. New required permissions during an
update leave the old version active until accepted.

Access to an application and access to its business resources are different
policies. Granting an OU access to an app does not grant all reports, balances or
credentials. Resolve current account/OU generation and ancestry at dispatch.
Cached presentation metadata is never authorization evidence.

## 5. Browser bridge v2

Preserve the legacy bridge for legacy packages. A v2 application cannot silently
fall back to broader v1 routes when negotiation fails.

Proposed handshake:

1. Host creates a sandboxed frame for a verified release and a fresh random
   document nonce. The host remains the only holder of Portal session/bridge tokens.
2. Child SDK installs its listener before announcing `app:hello`, with a fresh
   child-document nonce and supported protocol range.
3. Host validates `event.source`, app frame and generation, creates a dedicated
   MessageChannel, and sends `app:init` binding both nonces and the protocol.
4. Initial payload includes supported theme tokens, locale, business timezone,
   layout mode and effective capability names. It contains no session cookie,
   bearer token, signing key, raw secret or unrestricted principal directory.
5. SDK applies presentation before first visible content and replies `app:ready`.
   Host removes its loading surface only for the matching document/channel.
6. Navigation/reload invalidates the channel and pending callbacks. A previous
   document cannot send ready/resize/navigation/API results for its successor.

Message families: `presentation.changed`, `page.describe`, `layout.measure`,
`navigation.request`, `draft.changed`, `api.request`, `api.result`, `app.ready`,
`app.failed`. Every envelope includes protocol, request ID where applicable and
generation/channel binding. Validate discriminated schemas, payload sizes and
rate limits before dispatch. Reply errors have stable safe codes, not raw stack
traces or upstream response bodies. Cancellation/timeouts reject pending SDK work.

The host owns browser history. Routes must belong to the signed route registry;
query parameters have declared size/type rules. Escape or reject external URLs.
Back/forward updates the app route without a frame reload. Unsaved-draft guards
use a standard host prompt; a malicious app cannot block navigation indefinitely.

Initial provisional bounds, to be confirmed at D3:

| Boundary | Proposed default |
| --- | --- |
| Presentation handshake | 10 seconds; host error with explicit retry |
| Pending foreground API calls | 16 per installation/document; no automatic write replay |
| Control message | 64 KiB; file transfer uses an approved separate stream |
| Resize | Coalesce to at most 10/second; reject non-finite or negative sizes |
| Document content height | 0..100,000 CSS px; beyond this requires virtualized/viewport mode |
| API deadline | 15 seconds by default; narrowly registered long operations return a job |
| API JSON body | 1 MiB default; route-specific lower/explicit higher limits |

These are candidate defaults, not measured capacity guarantees. Test burst and
slow-client behavior before freezing them. Existing extension limits remain in
force until a separate reviewed change replaces them.

## 6. Control-plane API proposal

All management requests require current administrator permission, session checks,
normal enrollment gates and applicable CSRF defenses. List/detail reads enforce
app visibility. Use the existing JSON error-envelope conventions.

| Proposed endpoint | Result and rule |
| --- | --- |
| `GET /api/admin/app-platform/runtime` | Redacted runtime compatibility/health; no Docker or secret inspection API |
| `POST /api/admin/app-platform/plans` | Validate install/update/uninstall intent and produce an expiring reviewed plan |
| `POST /api/admin/app-platform/operations` | Commit a plan using an idempotency key; return 202 and operation ID |
| `GET /api/admin/app-platform/operations/{id}` | Authorized progress, stage, safe failure and recovery choices |
| `GET /api/admin/app-platform/installations/{id}` | Desired/observed state, generations, granted capabilities and resource health |
| `PUT /api/admin/app-platform/installations/{id}/access` | Revision-checked user/OU policy; immediately fence revoked requests |
| `POST /api/admin/app-platform/installations/{id}/actions` | Typed disable/enable/backup/restore actions, not arbitrary commands |
| `GET /api/app-platform/apps` | Current user's visible installed apps only |
| `POST /api/app-platform/apps/{id}/request` | Typed, viewer-bound request via broker; identity/destination cannot be supplied by the app |

Plan responses include an opaque ID, expiry, revision, descriptor digest, effective
permission diff, storage consequences and compatibility findings. Commit accepts
the plan ID/revision plus idempotency key, not a caller-supplied image or command.
Reusing a key with different content returns conflict. Reloading the management
page resumes observation of the same operation, not another installation.

Illustrative commit exchange; this is a proposed DTO fixture, not a live API:

```json
{
  "plan_id": "plan_fixture_1",
  "plan_revision": 3,
  "idempotency_key": "client-generated-operation-uuid"
}
```

```json
{
  "operation_id": "operation_fixture_1",
  "installation_id": "installation_fixture_1",
  "state": "installing",
  "stage": "download",
  "revision": 1,
  "retryable": false
}
```

The operation read supplies safe stage/result, source/target version, observed
runtime state and the permitted next actions. Errors use stable codes such as
`plan_expired`, `plan_changed`, `operation_conflict`, `runtime_unavailable`,
`package_untrusted`, `permission_required`, `quota_exceeded`,
`data_incompatible` and `recovery_required`. A retryable failure does not authorize
replaying a write under a new operation ID. Add all codes to typed locale maps;
do not use an error message's English text as the application protocol.

## 7. Runner and backend protocol

Portal control messages bind deployment ID, operation ID, installation ID, target
generation, monotonic control revision, desired state and verified package digest.
Authenticate the local transport and authorize each typed action. Reject an old
controller epoch following restore. A lost acknowledgement is retried with the
same ID; the runner returns its durable result rather than repeating a migration.

Runner actions are stage, verify, prepare-data, start-candidate, probe, activate,
stop, snapshot, restore, remove-code and delete-data. No shell, exec, arbitrary
mount or user-selected container operation is exposed through Portal's API.

Backend calls retain the request/body/boot binding of ADR 0040. Managed updates
add installation/generation and capability revision. A local socket does not
remove the need for authentication. Old-generation workers are fenced at the
broker, scheduler, events, data writer and notification publication boundaries.
Background service assertions bind a registered workload; they are not synthetic
administrator sessions. Acknowledge jobs/events only after durable app handling.

## 8. SDK contract

The new SDK must provide one application root, shell metadata/actions, route hooks,
theme/locale/timezone subscription, capability checks, bounded API calls,
draft/navigation guards, jobs/events, secret binding UI, export and safe errors.
Every method documents foreground versus service authority and retry behavior.
Generate TypeScript and Go validators from the approved schemas where practical;
check fixtures in both implementations. Do not maintain divergent handwritten
permission enums in several repositories.

Application copy belongs in en-US/zh-CN/zh-TW resources. No embedded app language
selector is needed. Versions are independently negotiated; a newer SDK must
display an incompatible-platform explanation before attempting business calls.
