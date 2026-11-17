# Storage and authority decision proposal

Status: logical model; the initial physical subset is approved in
[the concrete specification](13-control-storage-spec.md). This document describes logical
ownership and invariants. It intentionally contains no DDL, migration ID, physical
table definition, side-store creation or baseline edit. A later concrete schema
must be reviewed with the owner before writing any database-shape code, including
the runner's local store or application metadata side tables.

The [initial physical specification](13-control-storage-spec.md) now makes the
first P03 control-plane/runner slice concrete. The owner approved that initial
scope on 2026-10-24 Pacific; later business/job/event products are still separate.

## 1. Recommended ownership: Option A

- Portal's existing database is the authority for desired installation state,
  user/OU access, capability grants, reviewed plans, operation intent and platform
  tasks/subscriptions. Support both SQLite and PostgreSQL.
- The runner owns a small local durable journal of admitted operations, observed
  resources, generations, data leases and recovery checkpoints. It does not copy
  the user directory or become another policy authority.
- Each application owns an isolated data namespace/volume. Platform metadata does
  not embed arbitrary app business tables in the Portal DB.
- The existing purpose-sealed keyring/vault boundary protects control-plane secrets.
  The runner receives only the material needed for admitted work. Recovery key
  ownership and escrow must be approved before implementing a new vault hierarchy.
- Artifact cache contains verified public code, never credentials or live app data.

Why: authorization remains with the system that owns accounts and OUs; the runner
can recover from a lost acknowledgement without asking apps to redeploy themselves;
one app's data migration cannot alter Portal's report schema.

## 2. Alternatives and costs

| Option | Advantage | Cost / reason not recommended initially |
| --- | --- | --- |
| A: Portal desired state plus runner journal | Current authority and durable reconciliation remain explicit | Requires checkpoint coordination and a controller-epoch protocol |
| B: Runner owns installation, policy and all application control state | More autonomous runtime | Duplicates authority, needs account synchronization, broader outage/revocation complexity |
| C: Portal DB is the only durable control store | Fewer logical stores | Runner cannot independently prove completed local effects after Portal outage or restore without another durable mechanism |

Do not implement C by silently adding unreviewed files as a hidden journal. Do not
implement B through raw database access from app containers.

## 3. Logical records to resolve in the physical proposal

These are record concepts, not an instruction to create one table per row.

| Logical record | Required facts | Invariant |
| --- | --- | --- |
| Publisher trust | Publisher/namespace, key IDs, verification policy, revocation revision | An app ID cannot be taken over through a catalogue override |
| Release descriptor | App/publisher/version, immutable digest, compatibility, artifacts and trust decision | One admitted descriptor digest binds all executable bytes |
| Installation | Durable ID, app identity, desired state, active generation, revision and controller epoch | At most one active writer/generation per installation |
| Activation | Frontend/backend digests, schema compatibility, effective grant revision and health evidence | A mixed frontend/backend tuple cannot be presented as ready |
| Review plan | Action, source/target, permission/data effects, expiry, content digest and reviewer authority revision | Commit never executes a different plan from the one reviewed |
| Operation | Stable ID, idempotency key/content hash, stage, prior/target generation, result and lease | Duplicate commit/reply loss cannot repeat an admitted mutation |
| App-use grants | Installation, current account/OU identity and permitted role/action | Deleted/recreated principals do not inherit grants |
| Capability grants | Capability/version, allowed resources, optional/required state and revision | A declaration alone is not effective permission |
| Secret bindings | Vault reference, installation purpose, approved use, revision and revocation | Ordinary reads/exports/logs never disclose secret values |
| Jobs/schedules | Registered handler, authority, due time, lease, deadline, attempt and idempotency | At-least-once delivery, with no stale worker commit after fencing |
| Event subscription | Allowed selector/resource scope, delivery/ack checkpoint and generation | No unrestricted global stream and no silent replay after restore |
| Recovery set | Control/data checkpoint, schema, artifact digests, protected key references and verification | A file existing is not proof of a compatible usable restore |
| Retained data | Installation origin, owner, schema/version, retention and deletion decision | Uninstalled data has no active runtime or authority |
| Audit reference | Actor, operation, policy/data revision, safe outcome and instant | Evidence excludes secret values, bearer links and sensitive payloads |

Runner records must bind operation/deployment/installation IDs, control epoch,
resource ownership labels, stage result, writer fencing token and local checkpoint.
The journal must distinguish a command received, an effect committed and an
acknowledgement sent. A network retry cannot erase that distinction.

## 4. Transaction boundaries to specify

The concrete proposal must show what happens under concurrency for each boundary:

1. Plan commit: idempotency reservation, authority recheck and desired operation
   creation occur in one Portal transaction.
2. Operation claim: lease/fencing revision compare-and-swap; obsolete claims cannot
   advance a newer target. Per-installation serialization survives restart.
3. Runner effect: durable admission/result surrounds the external effect. Recovery
   observes actual labeled resources when an effect and journal commit can split.
4. Activation: one Portal transaction selects a consistent release tuple and grant
   revision; runner routing and data fencing converge through the same operation.
5. Disable/uninstall: access denial is durable before code cleanup. Cleanup failure
   cannot leave an installation authorized while the UI claims it is removed.
6. Account/OU deletion: revoke current references and task delegation; retain only
   policy-approved nonidentifying audit facts. Names/IDs must not recreate access.
7. Job/event acknowledgement: bind current lease/generation; ambiguous external
   delivery is separately classified, never converted into safe automatic replay.

The proposal must specify uniqueness, nullable/absent values, indices, time units,
lock order, maximum object sizes and per-record cleanup. Avoid a single arbitrary
JSON blob that hides structured entity lifecycle and uniqueness requirements.

## 5. Migration, baseline and backup rules

Keep the accepted v0.4.72 baseline unchanged. Follow
[ADR 0034](../adr/0034-calver-baseline-and-database-compatibility-reset.md) and
[ADR 0035](../adr/0035-migrations-after-a-frozen-baseline.md): append ordered
steps, verify declared products and refuse incompatible restore claims. Do not
pick a future step number in advance or edit already released migration code.

The review must include old-backup behavior, new-backup products, missing-product
rejection, newer-schema rejection, corruption behavior, key loss and coordinated
Portal/runner/app checkpoint restoration. All managed work restored from backup
starts paused/review-required; no automatic public exposure or outgoing replay.

App business schema upgrades use the app's explicit migration job and its own
schema contract. They never run DDL against Portal. Backend startup is not an
untracked migration command. An app may not claim rollback compatibility without
tests proving the old binary reads the target data schema safely.

## 6. Retention and quota decisions

Candidate defaults to review, not hidden production settings:

| Item | Proposed default / rule |
| --- | --- |
| Expiring review plan | 15 minutes; invalidate on relevant policy/package change |
| Completed operation metadata | 90 days; retain nonterminal/recovery-needed records |
| Bounded app logs | 10 MiB per installation, rotated; 7-day target maximum |
| Verified artifact cache | Quota/LRU; never evict an active or required recovery artifact |
| Retained uninstalled data | Keep until explicit administrator decision; show storage usage |
| App data allocation | 256 MiB initial reference quota, adjustable within host limits |
| Recovery snapshots | One pre-update recovery set minimum; retention/disk policy must be explicit |
| Task/event history | Bounded by age/count without dropping uncertain or unacknowledged work |

Define disk-full admission and snapshot capacity reservation. A pre-update backup
cannot be the first time the operator discovers that free disk is insufficient.
Retention must respect privacy deletion policy; retain operation identity without
retaining unnecessary user or business payloads. Secrets have no public log TTL
because they must never enter logs in the first place.

## 7. What the owner must decide before D2 passes

Prepare the physical specification and request one concrete review covering:

- Option A or another ownership model, including the runner journal.
- Exact logical-to-physical mapping, uniqueness, indices and transaction model.
- User/OU deletion, reinstall identity and retained-data adoption rules.
- Vault/key ownership, backup recovery and key-loss behavior.
- Portal/runner/app checkpoint consistency and restore pause rules.
- Retention/quota defaults and which settings administrators may change.

Record the answer and accepted document revision. An approval of the earlier
Status storage proposal is not approval of this larger platform schema. Until
then, implementation may use in-memory fake repositories for pure protocol and
state-machine tests, not durable "temporary" production stores.
