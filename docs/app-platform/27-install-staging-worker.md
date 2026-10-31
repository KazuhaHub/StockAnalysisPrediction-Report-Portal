# Initial installation worker staging

Status: P03/P05 internal worker repository. This connects the admitted static
install operation to durable, fenced artifact-staging stages. It is not an
activated application, a background polling service or the complete installer.
No database shape, production configuration, runtime privilege or SDK changes.

## Calling sequence and evidence

Source: `internal/app/app_platform_install_stage.go`.

1. Obtain an admitted operation from `consumePlatformInstallReview`.
2. Claim it with the existing `claimPlatformOperation` repository using the worker's
   UUID. The returned private token binds owner, deadline, operation/installation,
   controller epoch, generation, control fence, state revision and row revision.
3. Pass that exact token to `advancePlatformInstallStaging`.
4. Replace it with the returned token before any next call. A stale token refuses;
   lease heartbeat remains a separate operation and its returned token also replaces
   the previous one.
5. Stop at `prepare_data`. Do not call `Advance` on the pure operation model and
   write the result directly to SQL, skip provider evidence or mark the app ready.

Supported transitions are only:

```text
queued -> validate -> download -> stage -> prepare_data
```

The release/frontend bytes already reside in the approved database staging store.
Here `download` and `stage` validate those resident exact bytes; there is no network
fetch, archive extraction, filesystem deployment or container operation. Entering
`prepare_data` requests its future provider. It does not prove data exists or that
storage quota, application readiness or an activation has been established.

Every call re-verifies the complete signed descriptor/frontend bytes. No transition
accepts a caller-provided `verified`, `ready` or `applied` flag. No external effect
receipt is manufactured. The repository currently refuses managed installs,
non-install actions, generations other than the initial generation and stages after
artifact staging. These remain separate lifecycle work.

## Lock and validation order

Start a database transaction and acquire:

1. The existing principal/OU lifecycle mutation guard.
2. The publisher/key trust transaction guard.
3. Current installation and operation lease locks through the bound-lease loader.
4. The consumed review row and stored release/frontend evidence locks.

This order extends the installation admission order; it never locks a publisher
behind an installation while another path takes the reverse order. SQLite write
intent supplies cross-connection exclusion, not a process-local mutex.

Validation checks:

- The exact lease token is still current, unexpired and bound to the installation's
  present control fence/epoch. Runtime absence must match this static operation.
- Operation source generation is zero and its request digest binds the consumed
  review ID/digest using the same framing as admission.
- Review JSON is bounded, digest-matching and canonical under the closed projection.
  It is consumed, belongs to the same actor scope, app/publisher, target release,
  controller epoch and original zero installation/policy revisions.
- Reconstruct the reviewed identity from the current verified signed release,
  original authority fingerprint, current development protocol and exact sorted
  declined capabilities. It must equal the stored bytes; unknown or altered fields
  cannot select executable behavior.
- Current publisher/key revisions and namespace/validity/revocation still match.
  Inspect actual stored frontend ZIP bytes and their declared digest again.
- Current installation remains `enabled/pending`, generation 1, policy revision 1,
  matching app/publisher; it has no access grants, data namespace, activation or
  external effect records at these stages.
- Every optional capability has exactly the original declined row (`granted=0`,
  empty policy, policy revision 1 and this consumed review). Added, removed or
  broadened grants refuse; required providers remain unavailable.

The consumed review's former confirmation expiry does not cancel an admitted
operation. It is historical evidence. Current key validity, lease expiry, control
pause/fence and current sponsor lifecycle still apply on every step.

## Admitting account policy

Installation creates future authority, unlike denial-only cleanup. The worker
requires the admitting username still to identify the same account creation and
principal generation, with current admin role, active state and valid account
expiry in the configured Portal timezone. Deleted/recreated/demoted/inactive or
expired sponsors stop this path. It does not impersonate a user, recover a session
cookie or extend an expired session. A session expiring after successful admission
alone does not erase the durable decision.

This check is for the initial no-granted-capability static installation path.
It is not a complete future OU/resource/capability execution policy. Those providers
must supply their own current authorization and revocation checks before managed
execution or business-resource dispatch is enabled.

## Stage commit

Use the pure operation model only to compute the immediate next stage. A SQL
compare-and-swap binds the full expected token and advances both state and row
revisions. Owner, fence, generation and lease deadline do not change.

Insert `app_platform.operation_stage` in the same transaction. After this audit
write, recheck the lease/control markers and all admitted-install evidence. A
blocked audit can outlive a lease or signing key; an earlier successful check must
not let expired evidence commit. Before commit, sample database time again, check
the lease and verify key validity at that sample. Evidence locks are already held.

Any refusal, audit failure or final expiry rolls back stage, revisions and audit
together. There is no blind retry of uncertain external work; this repository
performs only transactional database work. No worker polling loop or startup hook
is added yet, so existing Portal business routes/startup behavior remain unchanged.

## Tests and continuation

`internal/app/app_platform_install_stage_test.go` covers both SQLite and disposable
PostgreSQL. Its success path was observed failing against the initial stub before
implementation. Cases cover all four transitions, exact token replacement, stale
worker rejection, heartbeat continuity, missing stage evidence and refusal without
side effects. It checks that no data/activation/effect record or completed operation
is fabricated.

Refusals include trust rotation, corrupted frontend bytes/review/request identity,
changed installation policy, broadened capabilities, demoted/inactive/expired/deleted sponsor, revoked signing key, expired lease
and paused control. Injected stage audit failures prove atomic rollback on both DB
engines. Real PostgreSQL tests hold the audit table lock, observe the worker blocked
in `pg_stat_activity`, then release it after lease or signing-key expiry; both must
leave the stage queued and insert no stage audit.

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
TEST_POSTGRES_DSN='postgres://portal@127.0.0.1:59409/reports_test?sslmode=disable' \
go test -p 2 -race ./internal/app \
  -run '^(TestPlatformInstallStag.*|TestPostgresPlatformInstallStageExpiryAfterAuditWait)$'
```

Next: actual owned data preparation/effect evidence, authenticated runtime control,
capability enforcement, activation/serving, the automatic worker service and full
installation UX. A stage label or consumed review is insufficient evidence for any
of these missing requirements. The complete application platform remains active work.


Development evidence: focused SQLite/PostgreSQL race tests pass on disposable
PostgreSQL 18.6. A separate Go build overlay removed only the post-audit guards;
both actual audit-wait tests then failed by committing an expired lease/key stage.
The normal source retains these guards and passes. The overlay does not modify
repository source. Full source CI remains a separate review requirement.
