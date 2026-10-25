# Runtime and trust candidate A

Status: proposed; owner approval pending. This is the concrete development
decision for [D1/D3/D4](README.md#decision-gates), not a production deployment
instruction or a stable public SDK release. Initial storage D2 is already
approved; this document adds no tables, columns or persistent side-store layout.

## 1. Decision requested

Approve the following direction for implementation and isolated acceptance:

1. One Linux Docker runner per deployment; installing an application creates its
   constrained backend automatically. Administrators do not manually deploy a
   database, Compose stack or backend connection for every application.
2. First managed-runtime acceptance profile: a dedicated Linux environment with
   rootful Docker and verified filesystem project quotas. Rootless support is
   conditional on passing the same enforcement tests, not assumed equivalent.
3. Exact-byte Ed25519 descriptor signatures and deployment-managed publisher
   allowlists; authenticated, signed local runner commands with durable effects.
4. Default-deny broker access, explicit credential bindings and bounded background
   authority. Application visibility never substitutes for business permission.
5. Develop and test this candidate using disposable Linux environments and fake
   applications. Publish no stable protocol until common vectors and acceptance
   tests pass and the contract is reviewed for freeze.

Approval does **not** authorize production Docker/socket access, filesystem
reconfiguration, publisher trust changes, real service credentials, Status
migration, release deployment or a change from the server's `beta` configuration.
Production enrollment and rollout remain D6. D5 visual acceptance also remains.

## 2. Product behavior

The administrator installs the runtime once. An ordinary application installation
then follows Market → review → install → open. Portal owns downloads, integrity
checks, backend start, storage, progress, permission review, recovery and uninstall.
A backend application still needs a process, but that process is managed by the
platform rather than a second manual deployment task.

Static applications work without Docker. An incompatible managed runtime gives a
specific reason before commit. A failed runtime never blocks report, forecast,
account or other Portal business routes. These promises require full lifecycle
acceptance; the current storage primitives do not yet deliver them.

## 3. Runtime profile and privilege boundary — D1

| Component | Authority and boundary |
| --- | --- |
| Portal | User/session/OU authorization, reviewed intent and signing; no Docker socket |
| Runner | Narrow typed container/storage actions; sole holder of runtime control access |
| App backend | Fixed non-root identity; no runtime socket, host control key or Portal session |
| Capability broker | Separately authenticated application socket; no container-control dispatch |
| Application frontend | Sandboxed document and bounded bridge; no Portal token or direct external fetch |

Rootful Docker access is effectively host-level authority. A dedicated runtime
host or isolated VM is the initial recommended boundary; the runner process alone
does not neutralize that privilege. A compromised runner or host kernel is beyond
the application sandbox guarantee. Local macOS journal tests are development
evidence, not Linux runtime acceptance.

Every backend must have:

- An immutable, verified OCI digest for the admitted architecture, not a tag.
- Fixed non-root UID/GID, read-only root filesystem, all capabilities dropped,
  `no-new-privileges`, no privileged mode, no host namespaces or host ports.
- `network=none`; distinct platform-owned Unix sockets for app HTTP and broker
  access, with private parent directories and checked ownership.
- Enforced memory, CPU and PID limits; bounded temporary storage and persistent
  data bytes/inodes. Reject unsupported cgroup or quota enforcement at admission.
- Only its owned data mount and socket paths; no user-supplied bind mount, device,
  Compose file, sidecar, arbitrary command or Docker API passthrough.

Use a dedicated ext4/XFS project-quota data root whose byte and inode limits have
been tested against real writes. Directory-size polling is not a hard quota.
Rootless Docker is unsupported until the selected driver proves all required
limits, including writable data quota. No fallback to unlimited storage or bridge
networking is allowed. A quota-capable data root requires a reviewed deployment
procedure; do not change an existing production filesystem during development.

Runner startup probes the actual daemon, cgroups, mount/quota behavior and socket
isolation. Report safe compatibility findings, not an arbitrary host diagnostic
API. Derive resource paths from platform identities; before adopting, stopping or
deleting a resource, verify its native ID, ownership labels, deployment,
installation, generation and fences. A name match alone never proves ownership.

## 4. Signed package bytes and publisher trust — D3/D4

The candidate algorithm is Ed25519 only: a raw 32-byte public key and 64-byte
signature. Unknown algorithm strings in stored rows do not select an algorithm.
Use the standard cryptographic implementation and shared known-answer vectors.

Define the signature input as:

```text
ASCII("portal.app/descriptor/v2") || 0x00 || uint64_be(len(descriptor)) || descriptor
```

`descriptor` is the exact fetched UTF-8 byte sequence, at most 128 KiB. Its SHA-256
is also computed over those exact bytes. Do not parse and reserialize before
verification, normalize Unicode/newlines or silently strip a BOM. The existing
strict parser still rejects duplicate keys, malformed UTF-8, unknown executable
fields and invalid bounds even when a signature is valid.

The publishing tool emits deterministic descriptor bytes. Determinism is a
producer rule; the verifier checks the bytes actually signed. Descriptor identity
is immutable for a published release. Detached signature metadata binds a
publisher/key ID selected from deployment trust, never a key supplied as trusted
by the package itself. The detached envelope's exact JSON schema and encoding
receive refusal fixtures before freeze.

Trust rules:

1. A deployment administrator explicitly enrolls publisher keys and application
   namespaces. A catalogue URL, GitHub organization or TLS certificate is not an
   execution trust grant.
2. Namespace bindings are exact application IDs or explicit delimiter-terminated
   prefixes. Reject overlapping ownership between publishers; do not use loose
   string prefix checks. A publisher cannot take over another product on update.
3. A key ID is immutable. Rotation adds a new key under the same publisher after
   review; it never overwrites the bytes of an existing key ID.
4. Revocation advances policy revision and invalidates uncommitted reviews. New
   activation rechecks current trust. Emergency package revocation quarantines
   matching running code, fences broker work and retains data for recovery.
5. Restore cannot resurrect a revoked key. Existing monotonic revocations survive;
   restored control authority is paused until actual resources and current trust
   are reconciled. No automatic enrollment or key replacement on startup.

No real publisher private key is needed for development. Use labelled test keys
and deterministic public fixtures; never install a fixture key in production.

## 5. Portal-to-runner command contract — D3

Initial transport is a protected local Unix socket with OS peer-identity checks.
Remote TCP control is excluded. Socket access and signature verification are both
required; app/broker sockets cannot dispatch control commands.

Portal keeps its private control signing key purpose-sealed in the existing
keyring. The runner stores only the enrolled public verification key in the
approved deployment record. Enrollment is an explicit runtime-administrator
operation; an empty journal is not permission to create trust automatically.

Each strict, bounded command binds at least:

- Protocol version, deployment ID and controller epoch.
- Monotonic deployment control revision and installation control fence.
- Operation ID, installation ID, target generation, stage, effect number and
  stable effect ID.
- Closed action kind, logical payload SHA-256, issued/expiry times and typed
  action payload. Payload paths, images and ownership are resolved against the
  reviewed release; callers cannot supply arbitrary host paths.

Sign exact envelope bytes using the framing above with the distinct purpose
`portal.app/runner-command/v1`. The detached signature is outside the envelope.
The signed envelope is at most 64 KiB; strict JSON rejects duplicate/unknown
fields. Decimal strings carry 64-bit counters, revisions and UTC milliseconds in
public JSON to avoid JavaScript integer rounding. UUIDs/digests use canonical
lowercase forms. Internal SQL integers remain unchanged.

Commands expire after at most 30 seconds; a renewed transport envelope preserves
the logical effect ID and payload digest. Envelope timing/signature bytes are not
part of the logical payload identity. The exact DTO and domain-separated payload
digest framing require shared vectors before any executor accepts them.

Durability and fencing rules:

1. Persist the Portal effect intent before dispatch; persist runner admission
   before performing a physical effect. A signature alone proves neither.
2. The same effect ID and logical digest observes/reconciles existing work. A
   changed digest or changed logical identity conflicts. Never create a new
   migration effect merely because an acknowledgement was lost.
3. Serialize physical actions per installation. Record the highest observed fence
   before accepting a new action; lower-fence workers cannot dispatch new work.
   Observing a higher fence does not prove an old process stopped writing.
4. Keep the command's original admitted fence immutable. Determine the durable
   current floor from enrolled deployment revision and owned command/resource
   records under the same serialized transaction; no fifth journal table or
   ad-hoc side file. Reconcile resources before resuming after restart. If these
   records cannot establish a floor, refuse and require recovery.
5. A receipt binds the original effect/digest/execution fence and the fence at
   which resources were actually observed. An old receipt is historical evidence,
   not current proof that activation, stop or data-writer exclusion succeeded.
6. Parse receipts as a closed typed object, at most 16 KiB. SQL `state=applied`, a
   caller boolean or arbitrary JSON is insufficient evidence for stage completion.
   Unknown outcomes enter reconciliation/recovery, not blind command replay.

Runner replies use the authenticated local connection, peer identity and exact
request binding. Do not invent a runner signing-key file outside approved D2.
Portable independently verifiable receipts or remote transport need a later
explicit design. Hold per-installation execution serialization until a side
effect settles or is marked uncertain; database lease expiry alone cannot kill it.

## 6. Capability and background policy — D4

Backend networking remains disabled. Broker HTTP grants bind origin, method,
normalized path rules, bytes, concurrency and deadline. Validate all DNS answers,
pin the dial destination, verify TLS and refuse redirects. Metadata/link-local
targets are denied; private destinations need explicit narrow service rules.
`network.stream` stays unavailable until its separate protocol/enforcement tests
pass. A valid descriptor requesting an unavailable required capability cannot
install.

`secrets.use` binds one credential and one broker operation without returning raw
bytes. `secrets.read` is exceptional and separately reviewed for a specific
binding. Neither permits vault enumeration. `notifications.publish` emits a
source-owned event; Portal selects authorized recipients and delivery channels.
Apps do not supply arbitrary email, Slack or Webhook destinations through that
capability. Reuse the existing notification system rather than a second sender.

Foreground dispatch rechecks the current user/session, installation generation,
capability revision and OU/resource policy. Background work uses an explicit
service grant and registered handler, never an impersonated administrator.
Candidate background authority lasts at most five minutes and renews at most
once per minute. Loss of control connectivity stops new authority after expiry;
restored/paused control cannot renew it. Online broker revocation is immediate
at dispatch; disconnected process cessation is bounded by the lease plus tested
enforcement latency. Do not promise instantaneous offline revocation.

The existing operation-worker lease remains 30 seconds with a 10-second
heartbeat. It is separate from background application authority and is not
permission to replay an uncertain effect.

## 7. Presentation and SDK limits — D3

Retain the existing proposed parser and bridge limits in [02-contracts.md](02-contracts.md).
The host owns title/actions, navigation, theme, language, timezone and document
scroll. React/TypeScript and Ant Design are the common application foundation,
bundled within each isolated document. No second language picker or outer app
frame is part of the v2 application template.

This approval permits implementing the experimental contract and shared schema
tooling. It does not freeze an SDK or permit v2 to fall back to the legacy bridge.
Keep v1 behavior unchanged and label candidate interfaces accordingly.

## 8. Implementation order and evidence

| Step | Concrete change | Required evidence before advancing |
| --- | --- | --- |
| 1 | Pure signature framing, strict detached schema, trust matching and command DTO | Go/TypeScript identical positive and negative vectors; changed bytes, wrong domain/key/namespace, oversized and duplicate inputs rejected |
| 2 | Typed Portal effect and runner command/resource repositories | Both Portal DB drivers; contention, stale fences, epoch change, crash/reopen and idempotency tests; no executor yet |
| 3 | Authenticated Unix control transport and explicit test enrollment | Wrong peer/deployment/key/epoch refused; expired envelopes and replay conflicts; no app path reaches control dispatch |
| 4 | Linux Docker adapter with fake apps | Real hard CPU/memory/PID/tmp/data limits, network denial, ownership substitution refusal and no host mount/socket exposure |
| 5 | Durable executor and lifecycle reconciliation | Kill before/after intent, dispatch, effect and acknowledgement; lost replies do not repeat migration; writer exclusion precedes snapshot/migrate |
| 6 | Complete platform and SDK acceptance | Real market install/update/disable/uninstall/recovery, authorized browser flows and unaffected Portal routes; D5/D6 remain required |

Use disposable Linux VMs/CI workers with only synthetic apps and local fake
destinations. Any quota test filesystem must be newly created and owned by the
test job; never reformat an existing mount. Clean only resources bearing the
unique test deployment ownership labels. Record daemon/kernel/filesystem/driver
versions and observed enforcement, rather than inferring support from config.

Negative tests include cross-installation sockets/data, malicious symlinks,
resource-name collisions, unknown physical outcomes, exhausted disk, permission
revocation during work, rootful/rootless incompatibility and runner unavailability.
The [acceptance specification](06-acceptance.md) remains the full exit criterion.

## 9. Approval record

Pending owner response. Record the approved scope and any amendments here before
enabling privileged integration. Track source work separately in the
[execution register](09-execution-log.md). Existing storage test results and the
journal's four-table schema do not imply runtime or production approval.
