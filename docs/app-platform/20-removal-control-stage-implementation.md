# Persisted removal control stages

Status: P03 in progress. Uses approved control storage; no schema, public endpoint,
runner executor or production deployment changes. Runtime/trust candidate approval
is still tracked separately in [18](18-runtime-and-trust-proposal.md).

## What advances

`Store.advancePlatformRemovalControl` accepts the exact internal worker lease for
an already admitted disable/uninstall operation. It permits only:

```text
queued → validate → revoke
```

The current stage is work to perform, not completed work. Entering `revoke` means
revocation/drain must now be executed and proven. This repository cannot leave
`revoke`, stop a backend, remove code, release retained data or mark success. It
does not pretend that SQL denial flags prove broker admission has stopped or that
in-flight work has drained. Those require the still-missing providers.

Install, update, enable, repair, restore and data deletion are not admitted by this
method. It neither grants execution nor validates a package for new activation.

## Admission evidence checked again

Before either transition:

1. Lock the runtime binding, installation and operation; verify the complete lease,
   epoch, active control markers, generation/fence, lifecycle shape and row revision.
   This shared loader is also used by effect intent preparation.
2. Lock the referenced review. Require a consumed record, matching actor lifecycle
   scope, exact plan-byte digest and the operation's original
   request digest over plan ID/digest. Verify exact canonical projection bytes and
   all duplicated identity/action/revision/runtime/source-release fields.
3. Require the reviewed target generation to be current, desired state to remain
   denied and phase to match pending uninstall or disabled intent. Require no active
   route and no previously recorded effects for this operation's initial stages.
4. For a reviewed data namespace, verify the same owner-bound namespace and format,
   active state and advanced revision/writer fence. Never recreate absent data.
   A review with no data is valid only when no namespace has since been attached.
5. After potentially blocking review/effect locks, recheck pause and epoch, then
   resample database time and reject an expired lease. PostgreSQL holds those
   marker rows with `FOR SHARE` through the short final commit section.
6. Advance the pure model by exactly one stage; conditionally update one operation
   with matching old stage/state revision/row revision/fence/owner/expiry. Increment
   state and row revisions; retain lease expiry, owner, fence and target generation.
7. Insert one safe `system` stage audit in the same transaction. Any error rolls
   both writes back. The caller must keep the returned lease; the previous one is
   stale even though its worker ID and fence still match.

The current account and publisher do not receive new authority here. A consumed
cleanup decision remains usable after its review expires, the actor is deleted,
or the publisher is disabled/revoked. Principal deletion can clear the review's
username and advance policy revision; it must not resurrect access or cancel an
already committed denial. This exception does not apply to unconsumed reviews,
new installs or capability expansion.

## Control marker lock order

Resource-bound transactions acquire runtime → installation → operation →
review/effect → final marker locks. A marker-only pause/epoch writer must commit
without subsequently acquiring resource locks in that transaction. Otherwise it
can deadlock with a worker holding a resource and checking the marker.
Coordinated restore remains offline and must not be replaced with an online
setting edit followed by resource mutation. The new checks do not implement that
future restore coordinator or a general pause API.

Initial lease loading still performs unlocked marker checks before database-time
sampling. The final check is needed because later row locks can outlive that
sample. Effect intent preparation now uses this same final check, preserving its
existing stable IDs and uncertain-state semantics.

## Verification and limitations

Tests run against SQLite and disposable PostgreSQL and cover:

- Both transitions for all four static/managed × disable/uninstall combinations.
- Consumed review expiry, deleted actor and revoked publisher still permit denial
  cleanup; no access grant, data namespace or success result is recreated.
- A no-data/no-active-release application can enter uninstall revocation without
  the platform inventing storage.
- Eight callers sharing a token produce one stage commit/audit; seven stale-token
  conflicts. Old tokens cannot heartbeat/advance current work, and entering
  `revoke` cannot skip directly to stop or completion.
- Corrupt/unconsumed/mismatched reviews, request binding, active routes, changed
  app/generation/desired state/data format, missing data fence, expired lease,
  exhausted counters and paused control refuse without durable mutation.
- Injected stage audit failure rolls back the stage in both databases.
- A real PostgreSQL review-row lock delays a worker while control is paused.
  The initial implementation incorrectly advanced; the regression was observed
  failing before the final marker check was added.
- Existing effect identity/reclaim/restart, effect-row expiry and lease tests
  exercise the shared-loader refactor.

Local targeted tests and race tests pass with SQLite and disposable PostgreSQL.
The full Go suite with that PostgreSQL DSN, full vet/build, local documentation
links and whitespace checks also pass. This source change still requires its own
PR CI; previous PR results do not certify later source.

No test here proves physical revocation, external writer exclusion or container
shutdown. Existing externally managed apps and production `beta` configuration
remain untouched. Continue with evidence-bound provider commits, authenticated
runner commands and the full lifecycle/SDK/shell acceptance plan; do not expose
these private steps as a completed installer.
