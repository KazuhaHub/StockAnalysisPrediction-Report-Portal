# ADR 0036 — Revocable account sessions and personal login history

**Status: Accepted.** 2026-10-04. Extends ADR 0019 (session revisions), ADR 0017
(audit retention) and ADR 0035 (migration ledger).

The login-activity page previously projected successful sign-ins directly from
`audit_log`. These events could not identify a currently valid session or revoke
one device. Its ten-row display limit did not bound stored history.

## Sessions

All password, TOTP, passkey and SSO sign-ins now issue a signed `v2` cookie naming
a random session id, username, account revision and expiry. `user_sessions`
persists the id, revision, expiry, creation/activity times, initial IP, client
summary, authentication method and revocation flag. Neither a displayed id nor
an unsigned cookie authenticates. Every authenticated request checks the signed
cookie, persisted session, account revision, enabled state and account expiry.
Activity timestamps update at most once per five minutes per session.

The account page lists **valid sessions**, not proof that devices are online.
Its owner can revoke one session, including the current session. Revocations
persist until expiry; retaining a tombstone prevents a migrated legacy cookie
from recreating a session that was revoked. Ordinary logout revokes the server
session too, rather than only removing the browser cookie.

Bulk sign-out increments the account revision in a transaction. "Other sessions"
retains the current id at the new revision, and reissues its cookie with the same
expiry. "All sessions" retains none and clears the caller's cookie. Both cover
unmigrated legacy cookies. New concurrent sign-ins use the revision that was
current when issued: a cookie signed with an earlier revision cannot be revived
by a delayed migration. Session issuance refuses database failures.

Legacy cookies migrate on `/api/me` or the sessions page without extending their
signed lifetime. A deterministic hash of their complete cookie identifies their
migrated session. Previously issued cookies that are identical cannot be told
apart; they represent one legacy session. Legacy devices that have not returned
are not enumerable and are covered by bulk sign-out. SSO sign-out affects this
portal, not the identity provider's own session.

The existing fifteen-minute authentication sweep removes expired session rows
and sessions whose account or revision no longer exists. Account deletion removes
all its sessions. New account revisions use a random positive 62-bit seed so
deletion/recreation within a coarse clock tick cannot reuse a cookie's revision.

Routes (session authentication, owner scope, normal enrolment gate):

- `GET /api/me/sessions`
- `DELETE /api/me/sessions/{id}`
- `POST /api/me/sessions/revoke` with `scope: "others" | "all"`

## Personal history

`login_history` is a separate account-facing copy of successful authentication
events, keyed by audit id. It keeps the newest **100** rows per account by default.
An administrator with management permission sets the global **1–10000** cap in
Storage management through `GET/PUT /api/admin/login-activity/retention` with
`keep`. Account owners can only view history. Saving prunes every account immediately,
and every subsequent successful sign-in prunes in the same transaction that
writes its audit and history rows. Equal timestamps use the audit id as the
stable newest-first ordering. PostgreSQL writers take a shared policy-row lock
before the account-row lock; a policy change takes the exclusive policy lock
before pruning all accounts in one transaction. SQLite uses the existing
single-connection pool.

Changing this cap neither ends sessions nor removes administrator audit events.
The audit log retains its existing, separately configured day-based policy.
Increasing the personal cap cannot recover deleted personal history. UI display
remains the latest ten records (API maximum twenty), independently of storage.

The global cap is stored in `meta.login_history_keep` and is included in backups.
The former per-user field from v2026.40.6 is retained for migration compatibility
but no longer read. Upgrading to v2026.40.7 starts with the global default of 100
unless a global policy already exists; prior personal caps are not promoted.

On first access, existing accounts copy up to their cap from the existing audit
trail and persist an initialization flag. This happens once, including after a
restore of an older backup. Newly created accounts start initialized with empty
history, so a reused username cannot inherit a prior account holder's history.
Deleting the account removes its personal history while preserving audit events.

Migration `0003` adds both tables and the two defaulted user fields without moving
the frozen baseline. Its declared tables participate in backups and restore;
older backups may omit them and default to lazy history initialization. Builds
that do not know `0003` follow the existing ledger rule and refuse the migrated
database; restore a pre-upgrade backup when rolling back across this boundary.
