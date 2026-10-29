# Writable activation and current Portal viewer access

This follows [111](111-writable-backend-request.md). The writable backend can
already prove its boot and handle bounded signed requests. This continuation
adds the actual initial activation transaction and a current Portal-authorized
private request path. It does not yet expose managed apps through the generic
browser launch/asset routes or wire the writable automatic installation worker.
Production rollout, SDK freeze and Status adoption remain separate gates.

## Activation is one Portal transaction

`activatePlatformWritableInstall` captures the exact applied registration source,
then obtains the real independent host-verified writable boot through the existing
root endpoint. The commit cannot accept historical receipt JSON or a browser
readiness flag instead. The live boot must be no more than three seconds old,
match the exact lease/runtime, original native process, running receipt, delivered
registration and private document digest. Its type still refuses JSON import/export.

`commitPlatformWritableActivation` clones public source snapshots, takes existing
OU/publisher/runtime/lease locks, repeats current install authority checks and
reconstructs the complete four-effect pending source. It requires an identical
snapshot across IO and a fresh matching boot. Load and verify the actual frontend
archive/document and canonical reviewed audience; declined capabilities remain
declined. No arbitrary native callback or network operation runs in this transaction.

In the same transaction:

1. CAS the original candidate activation to active, retaining its resource ID,
   release, namespace, generation/policy 1, readiness JSON and observed timestamp.
   Pin original creation/update times and exact metadata. Only state and update
   time change; no replacement activation row or process is created.
2. Insert exactly the reviewed initial audience with existing user/OU lifecycle
   identity fields. No administrator bypass or implicit universal grant is added.
   An explicitly empty reviewed audience remains empty.
3. CAS the installation from enabled/pending to ready, retaining runtime, epoch,
   generation, policy and control fence; increment its revision.
4. Advance the exact operation to complete, increment its state/row revisions,
   record completion time and clear the worker owner/lease.
5. Write one activation audit containing hashes of boot/source/frontend/document/
   audience identity, not private keys or bearer material.

Reconstruct the resulting completed writable history before commit and require
precisely the expected activation timestamp update with original writer/start/
registration identities and receipts unchanged. Recheck audience lifecycle,
sponsor/publisher authority, signature/deadline and boot freshness. Any audit,
CAS, source or current-authority failure rolls the entire transaction back,
including audience, installation state and operation completion. This activation
performs no native write, registration rewrite, restart or namespace fence change.
A stale completed lease cannot repeat activation or regain worker authority.

## Shared current authorization, separate execution families

The readonly access transaction is generalized internally as
`platformBackendAccess[T]`; its original entry points remain typed aliases and
keep their readonly history/signature rules. The writable alias loads only the
writable family. Both run the existing current session/account, OU chain, audience,
restore pause/epoch, runtime, publisher/release and active-installation checks.
No readonly execution proof is converted to writable authority.

The private access snapshot owns a closable registration handle, exposes redacted
string formatting and rejects JSON import/export. Failed preparation closes all
private material and returns a zero value. Import attempts close an existing
handle before clearing it. Read-only DEK access does not initialize or repair a
keyring. Short-lived control references are signed inside the existing read
transaction and are returned only after final current account/trust/time checks.

`loadPlatformWritableBackendHistory` reconstructs completed history without a
fake live operation lease. It requires one completed initial install operation,
no owner/lease/error, the canonical consumed writable review and compatible exact
runtime, one active namespace at writer revision 3, one active resource and
exactly four applied effects:

| Source | Portal effect slot | Binding |
| --- | --- | --- |
| Data preparation | `prepare_data`, 1 | Original namespace/data receipt and current target |
| Writer | `prepare_data`, 2 | Original data receipt/resource/physical writer |
| Start | `start`, 1 | Exact writer receipt and original running process |
| Registration | `activate`, 1 | Exact running receipt, document and original file receipt |

Validate kind/runtime/epoch/fences, canonical bytes, all logical digests, original
creation/update relationships, source/current control revisions and native clock
bounds. Reuse historical writer/running matchers with an explicit source/fence
rather than constructing a lease. Check stored readiness only as canonical source
history, never as current health. Native observations cannot lie in the host clock
future; existing one-second database/native ordering tolerance is retained for
writable receipts. The readonly reader keeps its original stricter DB observation
condition in its own history loader.

The current audience policy may change independently of immutable execution
identity. It must agree on installation/activation/capability rows; bootstrap
policy remains 1 and its private file/key is not rotated for an audience-only edit.
A viewer still needs an actual current grant even when they are a Portal admin.
Installer demotion after completion does not invalidate an otherwise authorized
ordinary user's access. Pending work, unknown effects, extra history, retained
namespace, changed publisher/runtime/keyring/seed or restore pause refuse access.

## Current viewer business requests

`requestPlatformWritableBackend` is a private host path, not a browser HTTP route.
It accepts an authenticated host session and bounded route/body; it never accepts
an app-supplied subject, role, OU, assertion or destination. It:

1. Admits the current viewer through the writable access gate and verifies the
   exact configured managed root endpoint.
2. Performs a real writable challenge/answer and independently verifies its MAC
   and boot identity with the derived registration key.
3. Repeats current access under the existing transaction. Require unchanged
   account, OU pseudonyms, policy/fence, active resource and exact writable source
   before signing the request/body/boot and current viewer assertion.
4. Sends exactly one bounded writable request through the concrete root transport.
   Any error after dispatch is an uncertain outcome; it is never automatically
   retried or reported as an application rollback.
5. Checks exact endpoint/authority/request/native/delivery/boot bindings and fresh
   opaque response provenance, then repeats current viewer admission after IO.
   Only release the buffered JSON body if current authorization and source still
   match. Session/OU/policy/grant/runtime/publisher changes withhold the result.

A permission revocation during a request cannot undo an app write already
performed. Return the existing explicit uncertain-outcome error with no buffered
body; refuse subsequent requests before business dispatch. Applications need their
own recovery/idempotency semantics where appropriate. Transport errors and
revocation cannot cause the platform to repeat a write.

## Verification

Synthetic SQL fixtures construct historical applied registration/complete/active
metadata only, never an opaque boot or native success. SQLite/PostgreSQL tests
check writable source reconstruction, readonly-family refusal, policy-only edits,
current role and identity, zero-proof response refusal and unchanged storage.
Activation metadata tests validate the archive/review/audience and refuse zero
boot without changing state. Bad archive, plan, audience, runtime, stage and
unapplied source refuse. Access cases exercise pending/candidate/unfinished/lease/
fence/grant/session/publisher/runtime/policy/namespace/effect/receipt/review/keyring/
seed/time changes. Legacy readonly access cases run explicitly alongside them.

The dedicated storage CI step runs these SQLite/PostgreSQL cases before dependency
scanning, rather than relying on an ordinary full-test step that may be skipped
by an existing dependency finding. Payload and signing boundary cases run there
as well. No local PostgreSQL service is started.

Actual disposable Linux acceptance extends the existing non-root Portal/root
runner fixture after real data, writer, start, readiness and registration:

- Refuse a pending app's viewer request before dispatch. Force activation audit
  failure after all transition writes and prove candidate/pending/operation/audience
  roll back together. Remove the trigger and call the real activator; no completed
  or active row is fabricated for this success path.
- Require the original native/writer/file identity, exactly four effects, one
  activation audit, ready state and the explicitly reviewed user grant. Refuse
  stale activation and completed-operation heartbeat.
- Demote the disposable installer to user and send a real business request through
  the production private method. The app verifies the actual current viewer MAC;
  body fields claiming admin/another subject cannot change it.
- Exercise an audience-only policy edit and ancestor OU grant. Move the child's
  OU out of that ancestor and refuse a new request before dispatch; restore the
  relationship and retain the same pseudonymous user/execution identity.
- Produce a real write result, move the account out of its authorized OU before
  consumption and call the exact response-completion boundary. Withhold the body
  with an uncertain result, refuse another write, restore membership and use a
  fresh GET to prove no automatic retry. Return only the shared disposable account
  to its original role/group before independent legacy fixture paths continue.
- Read completed history without mutation. Generic managed browser launch remains
  denied until its distinct launch/asset integration is implemented. Existing root
  witness still verifies actual process/start time/quota and owned cleanup.

Required markers/report flags are:

| Marker | Report flag |
| --- | --- |
| `PORTAL_WRITABLE_INSTALL_ACTIVATED` | `native_portal_writable_install_activated` |
| `PORTAL_WRITABLE_CURRENT_OU_REQUEST_VERIFIED` | `native_portal_writable_current_ou_request_verified` |
| `PORTAL_WRITABLE_POST_IO_OU_REVOKED` | `native_portal_writable_post_io_ou_revoked` |

Fresh actual Linux evidence is pending. Synthetic source/metadata tests do not
prove activation, native business execution or post-IO revocation behavior.

## Remaining delivery work

Connect managed frontend launch/assets/bridge admission and the automatic writable
install worker/scheduler to these exact gates; install from the actual marketplace
with neutral static/managed reference apps and the shared SDK. Complete positive
expired-unknown recovery through Portal, physical stop/fencing/revocation, update/
migration/rollback/retained uninstall/restore/checkpoint lifecycle and broker/
secret/task capabilities. D5 visual/SDK and D6 production gates and full P00–P14
acceptance remain outstanding. No schema, dependencies/toolchain, production/real
credentials, Status migration, public SDK freeze or beta configuration changes
are included.
