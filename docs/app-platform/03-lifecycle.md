# Lifecycle, consistency and recovery

Status: proposed behavior. The storage gate applies before implementing durable
records. All mutating operations are asynchronous and have durable identities.

## 1. State model

Do not use a single `installed` boolean. Track installation intent, operation
stage and runtime health separately. A stopped process is not an uninstalled app.

```text
Not installed -> Installing -> Needs configuration -> Ready
                         \-> Failed / recovery required
Ready -> Updating -> Ready (new generation)
                \-> Ready (old generation) or recovery required
Ready -> Disabling -> Disabled -> Enabling -> Ready
Ready / Disabled -> Uninstalling -> Removed, data retained
Removed, data retained -> Reinstalling -> Needs review -> Ready
Removed, data retained -> Deleting data -> Removed, no data
Any uncertain restored state -> Recovery required
```

Every operation records stage, attempted effects, target generation, previous
generation, sanitized result and recovery choice. Progress displays a named stage;
download byte progress is not an invented whole-operation percentage.

## 2. Install algorithm

1. Preflight runtime, package trust, architecture, capacity and required services.
   Static apps skip managed-runtime preflight. No mutation occurs here.
2. Generate and display the digest-bound plan with required/optional capabilities,
   OU access, resource limits, initial data allocation and configuration needs.
3. On commit, revalidate authority, plan revision/expiry and trust. Reserve a
   durable operation ID and installation ID. Serialize operations per installation.
4. Download to a bounded staging location; verify digests before extraction or
   image activation. Cache blobs by digest; do not cache secrets with artifacts.
5. Prepare an independent data namespace and scoped broker sockets. Provision
   initial identities and broker bindings automatically. No manual pairing screen.
6. Start the managed candidate without user traffic, subscriptions or task claims.
   Check readiness using a bounded deadline. A static app validates served assets
   and supported bridge/layout without creating a backend.
7. Commit the active frontend/backend/generation pointer and effective grants.
   Open routes only after runtime evidence agrees. Partial control-plane failure
   must enter reconciliation, not expose an unverified candidate.
8. Mark `needs_configuration` when business setup is required. Setup routes can
   open for authorized administrators; unconfigured scheduled work remains paused.
9. After configuration review, enable approved work and mark ready. Do not request
   unnecessary credentials merely to satisfy an install wizard.

Failed staging can be retried/cleaned without deleting user data or an existing
version. An app failing readiness cannot replace healthy code. An image pull
failure never triggers a fallback to an unreviewed mutable image tag.

## 3. Normal update algorithm

1. Resolve one immutable target release and inspect its signed compatibility/data
   contract. Confirm new permission/resource differences. Keep the old version
   active while artifacts download and the plan is reviewed.
2. Acquire the installation operation lease and a fencing revision. Refuse a
   concurrent uninstall, restore or second update. Decide whether a brief app-only
   maintenance window is required; never stop Portal business services.
3. Fence new task/event claims and writes. Drain bounded in-flight work, then stop
   the old backend if the data engine cannot safely snapshot an active writer.
4. Create and verify a consistent recovery point covering app data/schema,
   configuration, vault references and matching protected key material. A volume
   copy made during arbitrary writes is not a consistent backup.
5. Run an explicit bounded migration job in the candidate sandbox with a data
   lease, no user traffic and no external side effects. Migrations are never
   hidden in the ordinary server startup path. Verify postconditions and journal
   completion before admitting candidate writes.
6. Start/probe the candidate. Atomically activate a single consistent release tuple:
   frontend descriptor digest, backend digest, data schema and generation.
7. Rotate generation-bound tokens and fence the old generation. Preserve the
   installation identity, user/OU namespace and granted policy unless the reviewed
   update explicitly changes them. Resume only previously enabled work.
8. Retain the old release and verified recovery point for the approved retention
   period. Record actual digests and health. Remove unused code only after success.

There is no universal transaction across a Portal DB, runner journal, Docker and
app data. Implement a recoverable staged operation with idempotent effects and
fencing, not a claim of one distributed ACID commit. The reconcile algorithm
must make every crash boundary safe.

If an update refuses permissions, times out downloading or fails before the data
migration, the old release remains active. If data may have changed, do not start
the old binary until its declared schema compatibility is proven. Offer either
an explicitly compatible code rollback or restore of the pre-update snapshot.

## 4. Identity continuity and existing ADR 0040

The current legacy installer resets backend registration on every replacement.
Do not change that behavior globally to make upgrades easier. The v2 lifecycle
requires a reviewed amendment: a normal managed update preserves installation
identity/OU seed but changes activation generation and token authority.

Uninstall revokes all live authority. Reinstall creates a new installation
identity by default. If retained business data includes identity-bound grants,
do not silently copy those grants into the new namespace. An explicit validated
adoption flow must remap authoritative principals and obtain review, or keep
those grants disabled. Account/OU deletion and recreation must never resurrect a
grant through a reused display name or database ID.

Restore invalidates pairing/epochs and pauses external effects as described below.
Key rotation preserves stable identity only when the protected identity seed and
installation relationship are proven. Leaked keys need immediate revocation,
not merely the next planned update.

## 5. Disable, enable, repair and uninstall

**Disable:** revoke/fence new foreground calls first; stop job/event admission;
cancel/drain bounded in-flight work; stop the app backend; retain data and package
identity. An unreachable runner yields `disable_pending`, not a false success.
The broker still denies new access while shutdown is pending.

**Enable:** revalidate trust, permissions, capacity and compatibility; start and
verify the same reviewed release; resume only explicitly retained allowed work.
A revoked package cannot be restarted through this action.

**Repair:** re-fetch the exact approved artifacts, verify and reconcile runtime
resources without changing user configuration or schema. Repair is not an alias
for an unreviewed update or automatic data deletion.

**Uninstall:** show an exact plan, including background effects and retained data.
Default to retaining data. Revoke broker access, secrets bindings, schedules,
subscriptions and notification publication before stopping/removing app code.
Wait for runner confirmation or report pending. Do not delete unrelated app
volumes, shared registry layers or Portal's data. Retained data has no active code,
tasks, subscriptions or service credentials.

**Delete retained data:** separate destructive operation with current authority,
explicit confirmation and bounded cleanup evidence. Describe exactly which
snapshots/backups are included. Off-host copies need their own retention/deletion
policy; do not claim global erasure from deleting one local volume.

## 6. Reconcile after restart or acknowledgement loss

For each nonterminal operation, read the desired revision and runtime evidence:

1. Check deployment/installation/operation IDs and control epoch.
2. Enumerate only runner-owned resources with matching immutable ownership labels.
3. Compare expected digests, data lease owner, generation and observed readiness.
4. Continue a safe idempotent unfinished stage or complete an already verified
   stage. Never rerun a completed migration merely because a reply was lost.
5. Fence an orphan/duplicate writer before admitting work. Stop unknown candidates
   but preserve ambiguous data for review; do not delete by guessed name prefixes.
6. Surface a human recovery requirement when evidence cannot prove which schema
   or external side effects committed.

The runner reconciles its admitted workload when Portal is offline. Existing
explicitly granted background work may continue until its authority lease expires;
the proposed default lease is five minutes, renewed every minute. Local disable
or revocation takes effect immediately when received. A Portal outage therefore
has a bounded stale-authority window; do not promise both indefinite offline work
and instantaneous remote revocation. This policy needs D3/D4 approval.

After runner restart, do not restart unjournaled workloads. Portal restart must
not trigger a second installation. After disk-full, failed writes are failures;
do not report a lifecycle stage durable before its record is committed.

## 7. Background jobs and events

Use platform-owned dedicated app executors, not analysis queue slots or mail
authentication workers. Each workload is bound to installation/generation,
registered handler, schedule or event ID and capability revision. Persist the
claim/lease, deadline, attempt and safe result.

At-least-once execution is the default contract. A timeout after an external side
effect is ambiguous, not proof of non-delivery. Supply stable occurrence/idempotency
keys; require application handlers and external integrations to deduplicate.
Never promise exactly-once delivery to an arbitrary third-party API.

Pause/revoke prevents new claims and fences replies from obsolete workers.
Cancelled work cannot silently become a new task. Separate retryable transport
failures, permanent permission/configuration failures and uncertain outcomes.

## 8. Backup and restore

A recovery set binds installation identity, descriptor/artifact digests, schema,
consistent data snapshot, control-plane export, vault envelopes/key references
and operation checkpoint. Protect credentials and key recovery material outside
public evidence. Hash verification alone does not prove a usable restore.

Portal and runner backups must be coordinated by checkpoint, not independently
copied at unrelated times. Test restoration in an isolated network-free fixture,
check data/config identities and decryption, then start compatible code in paused
mode. Recovery of Portal alone must not cause a still-running runner to trust
stale desired state; use a new controller epoch and deliberate reconciliation.

Restored apps have tasks, subscriptions, publication and public endpoints paused.
Review destinations, principals, queued work and possible previous external
delivery before resuming. No replay of paid work, outgoing messages or migrations
is implied by restore. A local backup is not an off-host disaster-recovery copy.

## 9. Legacy compatibility and adoption

Keep the old ZIP/bridge path and installed assets functional during v2 rollout.
Mark legacy external backends as externally managed; the runner must not discover
and take over arbitrary containers. Merely naming a matching app ID is insufficient.

An eventual adoption plan requires verified package/version mapping, an offline
recovery point, deliberate ownership proof, a no-duplicate-writer cutover,
credential/OU namespace review and rollback evidence. It must not recreate keys
or discard existing data. Business app adoption is a later operation after the
two neutral platform fixtures pass. Existing Status is not changed by this plan.
