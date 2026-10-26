# Portal runtime signing identity preparation

Status: internal D2/D3/D4 integration for development and isolated testing.
This implements the initial Portal half of explicit runner identity enrollment.
It does not enable a binding, confirm a running process, issue a signed operation,
activate managed installation, register a route or deploy a service. The complete
application platform and its UI/lifecycle acceptance remain unfinished.

## Purpose and source

The runner's explicit enrollment repository can already persist an operator-chosen
controller public key. The separate-UID local transport can authenticate both OS
peers. Portal previously had only the approved `runtime_bindings` columns, without
a writer for its sealed control seed or reproducible public enrollment material.

`internal/app/app_platform_runtime.go` now provides the private administration
repository `Server.preparePlatformRuntimeBinding(session, deploymentUUID)`.
It reuses the existing actor/session/OU lifecycle boundary, existing DEK and the
already-approved runtime table. There is no schema, migration, master key,
bootstrap configuration or server version change.

The result contains only:

- Binding UUID and operator-supplied canonical deployment UUID.
- Current platform controller epoch, initial control revision 1 and row revision 1.
- Ed25519 public verification key and its lowercase SHA-256 fingerprint.

The seed, expanded private key and sealed ciphertext are never returned. The
result is not an enrollment acknowledgement or permission to dispatch work.

## Initial transaction

1. Reject invalid deployment identity before accessing the keyring.
2. Require an explicitly initialized, unpaused platform control identity and a
   current authenticated administrator session. Initialization is a separate
   existing action; registration cannot silently create a controller epoch.
3. Use the shared principal-lifecycle mutation boundary and bounded contention
   retry. Lock existing runtime rows before deciding whether this is an exact
   pending retry. The initial product supports one local runner binding; a second
   deployment is refused even if no app currently refers to the first.
4. Refresh the administrator/session/authority and database clock after waiting
   for runtime locks. A preliminary successful check cannot extend session life.
5. For a new binding, generate independent cryptographic random 32-byte Ed25519
   seed material. Derive its public key and exact SHA-256 pin. Seal the seed with
   the **existing DEK** and AAD `aad("app-platform", "runtime-control:" + bindingID)`.
   It is independent of the actor HMAC seed, installation identity seeds and
   publisher signing keys.
6. Insert the binding with `enabled=0`, current epoch, revision/control revision 1,
   encrypted seed and fingerprint. The deployment UUID uniqueness constraint
   remains unchanged. No runner/socket/filesystem call occurs in this transaction.
7. Append `app_platform.runtime_prepared` audit with current administrator/OU,
   binding ID, deployment ID and public fingerprint. No private seed/ciphertext
   appears in the audit.
8. Refresh the administrator/session/control markers again **after** the audit
   write, since a lock wait can consume its lifetime. Commit binding and audit
   together; return public material only after successful commit. All errors
   return an empty result, including transaction commit failures.

The actor repository loads the DEK before starting the transaction. Reading that
cached DEK in the callback cannot acquire a second connection while SQLite's only
connection is already held. This avoids nested keyring initialization/transaction
acquisition; it does not add a second key hierarchy.

## Exact retries and recovery refusal

A retry must find exactly one pending row with matching deployment, current epoch,
initial revisions, disabled state, valid pin/envelope and sane timestamps. It
opens the seed only with the binding-specific purpose, requires exact seed length,
derives the public key again and compares its fingerprint. It refreshes current
administrator authority before exporting the public result. It does not replace
or reseal the seed and does not append another audit for a read-only retry.

Refuse rather than repair when:

- Another deployment or multiple runtime rows already exist.
- A binding is enabled or its epoch/revisions no longer describe initial pending
  setup. An enabled runtime needs its own future inspection/management path.
- The envelope/pin is missing, corrupt, foreign-purpose or inconsistent.
- Control is paused/uninitialized, the administrator was demoted, the session
  expired/revoked, the account was recreated, or authority changed.

The existing offline restore flow clears control seed/pin, disables the runtime,
advances revisions/epoch and pauses control. Initial registration does not bypass
that recovery state, even if an operator later resumes the global pause marker
and signs in again. Re-enrollment against actual resources needs a separate
reconciliation procedure. There is no reset/delete/recreate shortcut in this API.

Existing master-secret rewrap preserves the DEK, so an exact pending retry after
rewrap exports the same public identity without rewriting the sealed seed.

## Operator and execution integration still required

The future authenticated management integration will invoke this repository with
server-derived current session facts, not a browser/app-supplied session or key.
The operator can then enroll **only the public result** in the runner's explicit
repository using the protected bootstrap procedure. No private key is transferred
to a runner or app. The initial transport does not expose an enrollment method.

Do not enable a row based on possession of public material. [The confirmation implementation](34-runtime-enrollment-confirmation.md) binds
the actual OS-authenticated endpoint, durable runner deployment/key/epoch/revision
and current control state. Its enabled flag confirms control identity only. Native
compatibility evidence is still required before admitting/executing managed apps.
Restored-resource reconciliation, Portal lease-bound command production, stable
intent delivery and physical provider execution remain separate work. Static
installations retain their no-runtime path; this does not require Docker for them.

## Verification

Tests first failed against missing registration types/methods. The implemented
suite exercises both SQLite and an isolated PostgreSQL instance:

- Disabled initial creation, sealed purpose/seed/public-pin agreement, exact retry,
  defensive result ownership, and single binding/audit identity.
- Eight concurrent callers and independent file-backed SQLite connections plus
  reopen, preserving one pending signing identity.
- Current-role/control/key/deployment refusals with unchanged durable state and
  empty result; no implicit platform control initialization.
- Audit failure injection rolls back binding/key/audit together in both drivers.
- A PostgreSQL audit-table lock is observed in `pg_stat_activity`; the database
  clock passes the session deadline while blocked; final refresh rejects and
  rolls back without exporting enrollment material. The test compares audit
  counts against the prior history, since prior valid audit events must survive.
- Actual offline restore in both drivers, deliberate marker resume/new session,
  and refusal to regenerate the cleared runtime identity.
- Existing keyring rewrap preserves public identity and sealed seed.
- The public result explicitly enrolls in a fresh isolated runner journal with
  the same fingerprint/epoch/revision. This test is an explicit in-process
  operator action, not an automatic production enrollment side effect.

A temporary Go source overlay removing only the post-audit authority refresh made
`TestPostgresRuntimeRegistrationExpiryAfterAuditWait` fail because an expired actor
obtained a committed public registration. The repository source retains the guard;
normal both-driver race tests pass. The overlay is not part of the repository or
runtime behavior.
