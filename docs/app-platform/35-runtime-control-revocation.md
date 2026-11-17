# Portal runtime control revocation

Status: internal development integration, using the approved runtime table,
current administrator boundary and existing audit log. No schema change,
production enrollment, server configuration or physical runtime action is included.

## What this action revokes

`Server.revokePlatformRuntimeControl(session, bindingID, expectedRevision)` disables
the Portal control binding, clears its sealed Ed25519 seed, and increments its
control and row revisions in one transaction. It preserves the public verification
fingerprint and deployment/epoch identity for later reconciliation. It accepts a
pending or confirmed initial binding and can discard a damaged signing envelope
without attempting to decrypt it. It never creates another identity.

The action is **Portal-side signing revocation**, not remote execution revocation:

- It does not contact a socket, Docker daemon, container, broker or data writer.
- It does not stop an app, withdraw routes/grants, complete an operation or claim
  native cleanup. Existing operation records remain available for reconciliation.
- The runner's enrolled public key/control revision is not silently rewritten.
  Already signed/in-flight envelopes can remain valid under the runner's original
  identity until their expiry (at most the existing 30-second command lifetime).
  A higher Portal revision alone cannot recall those bytes.
- Future command producers must check enabled state/current revision and load the
  sealed seed transactionally. They cannot keep an unfenced cached private key or
  treat an old confirmed registration as permission to sign. Those command paths
  are not connected yet.
- No automatic re-enable or reset is introduced. Fresh operator reconciliation
  against actual runner resources/identity is required before recovery can grant
  signing authority again.

This action closes the control binding lifecycle before command production is
connected. It does not substitute for the platform's application disable/uninstall
workflow, which separately revokes access and proves that physical effects stopped.

## Transaction and retry contract

1. Reject malformed UUIDs and nonpositive/overflowing expected row revisions.
2. Use the existing current-administrator session checks, explicitly initialized
   unpaused control identity and shared principal-lifecycle lock. Registration,
   confirmation and revocation acquire that same boundary before runtime rows.
3. Lock the matching runtime row; verify canonical deployment identity, current
   controller epoch and valid revision domains. Refresh current administrator and
   authority after the row-lock wait. A caller's old role/session does not persist
   through revocation or expiry.
4. For new work require the exact expected row revision, a still-present signing
   envelope or enabled binding, and incrementable control revision. CAS the row to
   `enabled=0`, `sealed_control_key=NULL`, `control_revision+1`, `revision+1` with
   the database clock. The signing key is not opened or exported.
5. Append `app_platform.runtime_control_revoked` audit in the same transaction,
   identifying current actor/OU, target binding, deployment/epoch, source row
   revision, resulting row revision and resulting control revision. Public int64
   counters are canonical decimal strings in the closed audit projection
   `portal.runtime-revocation/1-dev.1`. No seed, ciphertext or endpoint is logged.
6. Refresh current administrator/session/authority and control markers again after
   all blocking audit writes. Commit atomically and return only binding ID and
   the two resulting revisions. Every failure returns an empty result.

A lost-response retry supplies the **same original expected revision**. It is
accepted only if the current row is exactly one revision newer, disabled, has no
sealed seed, and exactly one matching revocation audit exists for that binding,
epoch, deployment and all source/result counters. It does not append another audit,
change time, increment a counter or invoke any external service.

A disabled/manual/restored row without that exact audit is not proof that this
request committed. Missing or altered audit evidence refuses rather than repairs
itself. A caller supplying the already revoked current revision also refuses;
there is nothing left to revoke and no reason to keep advancing revisions.
The final valid counter transition to MaxInt64 is allowed and retryable, but no
counter wraps and no later increment is accepted. Audit retention can make a very
old retry unverifiable; that is a refusal, never permission to regenerate a key.

Current global pause/epoch mismatch remains a recovery gate. This repository does
not bypass paused restoration or create a separate emergency production interface.
The existing actor wrapper still requires a functioning platform keyring/control
identity even though the **runtime signing envelope** itself need not be readable.

## Old confirmation results cannot undo revocation

Initial preparation only accepts a disabled revision-1 binding with its original
sealed seed. Initial endpoint confirmation only accepts its corresponding
revision-1/2 state and matching purpose-sealed key. A revoked row has a later
revision/control revision and no seed. Therefore neither the old registration nor
a new live observation of the unchanged runner public key can automatically
re-enable it. This remains true across a file-backed SQLite reopen. Restore has
its own additional cleared-key, changed-epoch, pause and revision fences.

## Validation

Tests first failed against missing revocation method/type. The suite exercises
SQLite and an isolated PostgreSQL instance:

- Pending and confirmed binding revocation, retained public pin, exact retry with
  one audit, and refusal to prepare/confirm from stale initial material afterward.
- Eight concurrent callers preserve one outcome. A file-backed SQLite reopen
  preserves revocation and exact retry; altered audit evidence causes refusal.
- Invalid UUID/revision, stale/foreign/manual state, changed epoch, paused control,
  demoted account and counter overflow refuse without durable mutation or result.
- A corrupt runtime signing envelope can still be discarded without decryption.
- Final valid MaxInt64 transition and its exact retry do not overflow.
- Audit trigger failure rolls back signer clearing, disable flag, revisions and
  audit together in both database drivers.
- A PostgreSQL audit-table lock is observed through `pg_stat_activity`; the
  administrator session expires while blocked, and the final authority refresh
  rejects with no committed revocation. A temporary source overlay removing that
  final refresh makes the expiry test fail; the normal source retains it.

Prior live-confirmation source `5e476e7675df9d8044aa7d25a122fa1ad7716008` passed
[full repository CI 40252177883](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40252177883)
and [Linux endpoint acceptance 40252177832](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40252177832).
The latter executed the real non-root Portal -> root runner -> nonce observation
-> transactional binding confirmation test, which passed in 2.14 seconds. This
prior evidence does not prove resource execution or production compatibility.

Remaining work includes lease-bound command production/delivery, native resource
admission and physical execution/reconciliation, full application lifecycle,
management/UI/SDK integration and the separate production rollout decision.
