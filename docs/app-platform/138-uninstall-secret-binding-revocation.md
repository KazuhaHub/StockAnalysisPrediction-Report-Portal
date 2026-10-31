# Revoke credential bindings with reviewed uninstall admission

Status: lifecycle integration built on documents 136–137 and completed by 150.
Uninstall still grants no use itself; current reviewed bindings may use the
private credential broker path documented in 150. The complete application-platform
plan remains incomplete.

## Trigger and resulting behavior

An administrator previews and consumes an uninstall review using the existing
current account/session, principal, runtime/installation and exact plan checks.
At the same commit that records the removed installation intent and advances its
generation/fence, revoke every nonrevoked binding for that installation. Increment
each binding revision and record its revocation instant before the queued cleanup
operation and audit can commit. Existing tombstones retain their revisions and
instants. No binding can be inherited by a future new installation identity.

The operation admission audit records the number of newly revoked bindings with
its existing operation/plan/action/generation metadata. No secret, ciphertext or
upstream data is read into audit. Replaying the same committed idempotency key
returns the existing operation without another revocation or revision increment.

Shared credential custody is unchanged: another installation can still own its
separate binding to that credential. Retained app data is unchanged by this
admission step; physical drain/stop/code/data cleanup continues through the
existing worker and must provide its own evidence. SQL denial is not proof that
a process has already stopped.

Disable continues to deny at installation state/generation without permanently
revoking bindings. A later enabling operation must still pass all current grant
and binding checks. This change adds no enable shortcut.

## Exact review and atomic failure

The removal grant digest now includes the ordered binding restriction metadata:
identity/name, credential identity/value revision, operation/state, complete policy,
policy/review/binding revisions and revocation instant. It contains no credential
ciphertext or value. A binding change after preview stales the review rather than
silently consuming a different affected set.

The digest extension can stale an older pending review; regenerate that preview.
Already consumed operations retain their existing idempotent replay semantics and
durable cleanup intent. The extension does not re-admit execution or reinterpret
an old consumed plan as a new permission approval.

Within the current transaction, installation intent is locked before the binding
rows, which are locked in ID order on PostgreSQL. SQLite uses the existing
serialized mutation boundary. Refuse more than 32 nonrevoked bindings, invalid or
exhausted revisions, and changed update counts. No increment is attempted over
MaxInt64. These checks, plan consumption, generation/fence denial, binding updates,
operation insertion and audit belong to one transaction. Failure rolls everything
back; no partially consumed review or partially revoked set remains.

## Verification and remaining work

Tests first demonstrated the defects: uninstall retained active bindings and a
binding edit after preview did not stale the removal review. Driver-paired cases
now exercise the actual removal review/consume repository, covering active and
review-required revocation, unchanged prior tombstones, shared custody and a
neighbor installation, retained data, unchanged disable behavior, non-secret audit,
idempotent replay, stale review, exhaustion and rollback after operation failure.
Synthetic fixture bindings prove denial only; they do not prove an executable
credential grant or replace signed-package/current-viewer use acceptance.

Local focused removal/secret race tests pass. The focused CI credential group
includes both SQLite and PostgreSQL variants; exact revision CI is still required.
No real credential, production deployment, Status adoption, public SDK or beta
configuration is changed. Exact binding review/consume, current use resolution,
reviewed shared rotation/revocation, the private fixed-header exchange and response
projection/echo denial are implemented in 150; native browser/service acceptance
and complete CI remain evidence gates.
