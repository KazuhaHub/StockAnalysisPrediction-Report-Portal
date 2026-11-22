# Proposal: principal directory for app grant pickers

Status: accepted 2026-11-17; implemented on branch `feat/app-platform-principal-directory`. No dependency on 174–180.
Index and order: doc [174](174-background-broker-egress.md).

## 1. Problem and evidence

- A v2 backend sees only the current viewer: an opaque subject, opaque OU
  ancestors and the role, derived with HMAC from the installation's identity
  seed (`internal/appplatform/viewer_identity.go:29-52`,
  `internal/app/app_platform_viewer.go:54`; doc
  [88](88-stable-viewer-identity.md)). An app therefore cannot name any other
  user or OU, so an administrator cannot grant "OU Research can see component
  X" before someone from that OU has opened the app.
- The legacy extension platform solved this with an administrator-only
  directory, `GET /api/v1/extensions/{id}/principals`
  (`internal/app/routes.go:193`, `internal/app/extension_principals.go:12-152`):
  `kind=ou|user`, 100 per page by offset, `principal` = `ou:`/`user:` plus an
  opaque identity, `label` = OU name or username, all OUs and all active users.
  It is bound to legacy app tokens and the legacy identity binding, not to v2
  installations.

Status stores grant principals as `user:<64 hex>` and `ou:<64 hex>` and matches
them against the viewer (Apps `apps/status/service/internal/status/identity.go:28-44`,
`model.go:201-203`). Its grant editor needs to list and search these principals
with labels.

## 2. Threat model

| Threat | Control |
| --- | --- |
| App harvests the user list | Capability-gated, reviewed, and answered only for a current Portal administrator viewer; results limited to the installation's audience |
| Raw identifiers leak (username as key, numeric OU ID) | Principals are the same seed-derived HMAC values the viewer carries; raw IDs never cross the bridge |
| Cross-installation correlation | Values are per installation seed, so two apps cannot join their principals |
| Profile data leakage | Only a display label and, for OUs, the opaque parent; no email, role, login time or status |
| Enumeration by brute-force resolve | Resolve accepts only IDs in the response space, at most 100 per call, rate-limited |
| Cost amplification (HMAC per account) | Audience-bounded scan, page size 50, per-installation in-memory index rebuilt on change |

## 3. Contract

### 3.1 Capability

New capability `principals.read` (descriptor contract revision; optional; no
backend required). It has no policy (`{}`).

### 3.2 Routes (session-only, bridge only)

```text
POST /api/app-platform/installations/{installation}/principals/search
POST /api/app-platform/installations/{installation}/principals/resolve
```

Both are registered like the other viewer routes
(`authSession`, document headers, budget precheck; `routes.go:521-532`) and
require the exact launch binding, as the network route does.

Search request:

```json
{"kind":"ou","query":"research","after":"","limit":50}
```

`kind` `user` or `ou`; `query` 0..64 bytes, case-insensitive substring of the
label; `after` an opaque cursor ≤ 128 bytes; `limit` 1..50.

Search response:

```json
{"items":[{"principal":"ou:<64 hex>","label":"Research","parent":"ou:<64 hex>"}],"next":""}
```

`parent` is present for OUs (the nearest ancestor inside the result space, or
`null`) and absent for users.

Resolve request `{"principals":["user:<hex>","ou:<hex>"]}` (1..100, unique);
response `{"items":[{"principal":"...","label":"..."}],"unknown":["..."]}`.

### 3.3 Result space

- Users: active, unexpired accounts that pass the installation's launch
  audience check today (`internal/app/app_platform_audience.go:56-63`): users
  granted directly, or whose current OU chain includes a granted OU.
- OUs: granted OUs and all their current descendants.
- Labels: user `display_name`, else username; OU `name` (`user_groups.name`).
  A label is ≤ 160 bytes, truncated on a UTF-8 boundary.
- Principal values: exactly `"user:" + derive("account", username, created, generation)`
  and `"ou:" + derive("ou", id, created)` from `DeriveViewerIdentity`'s frame,
  so they equal what the viewer later presents.

### 3.4 Authority

Each call, in one read transaction: current session and launch binding;
installation enabled and ready; `principals.read` granted; the viewer is a
Portal administrator (`role == "admin"`, the legacy rule,
`extension_principals.go:55`) and passes the app audience. Otherwise 403
`not_permitted`. Rate: 30 calls per minute per viewer per installation.

### 3.5 SDK

```ts
const directory = useAppPrincipals();
const page = await directory.search({ kind: "ou", query: "res" });
const labels = await directory.resolve(["ou:...", "user:..."]);
```

The frontend sends chosen principal strings to its own backend; the backend
stores them as today. The backend has no directory access.

## 4. Review and consent

`principals.read` defaults off. The install or capability policy review (doc
[175](175-reviewed-multi-origin-and-private-network.md) §3.4) states: "The
app's administrators can list the names of users and OUs that can use this
app." Withdrawal is immediate.

Implementation: the capability is declared optional only; a descriptor that
lists it as required is refused. Install leaves its grant row off. A Portal
administrator enables or withdraws it with
`POST /api/admin/app-platform/installations/{installation}/principals-read`
(`{"enabled":bool,"policyRevision":"N"}`, audited as
`app_platform.capability_granted` / `app_platform.capability_withdrawn`) from
the installation settings page, which shows the sentence above before enabling.
The directory reads the row on every call, so withdrawal needs no cache
invalidation. An update replaces the grant rows, so a new release starts with
the capability off again and is reviewed again. There is no way to carry the
grant through an update review: update reviews of frontend apps select no
optional capability at all (network.http is selectable only in a managed
target's reviewed policy), so an administrator enables it again from the
settings page after the update.

Enabling and withdrawing are refused with 409 `app_platform_installation_not_ready`
unless the installation is enabled and ready and has no unfinished or
unresolved operation, checked in the same transaction that writes the row.
Install, enable and update read the grant rows as evidence (the install
resource count, the frontend activation checks and the grants digest), so
changing one mid-operation would turn a clean operation into a review. The
settings page disables both controls with an explanation in those states.
In the schema, `principals.read` appears only in the optional capability list. The per viewer rate is applied
only after the caller has passed the installation, audience and administrator
checks.

## 5. Failure and audit

403 for missing authority, 400 for malformed input, 429 with `Retry-After`.
Audit: capability grant/withdrawal (existing review audit) and one
`app_platform.principal_directory_used` row per viewer per installation per
hour with call counts; queries are not logged.

## 6. Migration and compatibility

Additive. The legacy endpoint is unchanged and keeps serving legacy apps.
Principals remain installation-scoped: a reinstall changes them (doc 88).
Status's v2 migration plan already re-enters grants on the fresh install; the
directory is what makes that re-entry possible without each user first opening
the app.

## 7. Test plan (these must fail first)

1. Search route returns 404 today; with `principals.read` granted and an
   administrator viewer it returns principals that equal the `Viewer` values
   the same users and OUs receive at launch (byte-for-byte).
2. Non-administrator viewer, revoked capability, disabled app, wrong launch
   binding: 403 and no query of `users`.
3. A user outside the audience and an OU outside the granted subtrees never
   appear, including through `resolve`.
4. Moving a user out of a granted OU removes them from the next search.
5. Two installations of the same app return different principals for the same
   user.
6. 31st call in a minute: 429. A 10,000-user fixture answers within the
   document budget.
7. Response contains no username when a display name exists, and no email,
   role, numeric OU ID or creation time.

## 8. Schema change

None. Results are computed from `users`, `user_groups`, the installation's
audience rows and its sealed identity seed; the capability row fits
`app_platform_capability_grants` with `{}` policy.

## 9. Owner decisions (2026-11-17)

The owner decided every question below on 2026-11-17.

1. Who may call it. Decided: Portal administrators only, matching the legacy
   endpoint; delegating to an app-defined manager role would let any app grant
   itself a directory.
2. Username fallback in labels. Decided: yes when no display name exists;
   administrators already see usernames in Portal.
3. Scope beyond the audience (all OUs, to pre-grant before widening the
   audience). Decided: no; widen the audience first.


## Amendment 2026-11-21: result space follows the OU policy (doc 190, D7)

Who can use an installation is now its OU policy, not install-time grants, so section 3.3 reads:

- OUs: every OU whose effective `app_ou_access` value for `platform:<installation_id>` is open
  (an OU inherits its parent's row unless it has its own; no row anywhere is closed), that has a usable
  creation identity chain.
- Users: active, unexpired accounts whose OU (the Default OU when they have none) resolves open, with
  the same usable-chain rule.

Unchanged: the principal values, labels, paging (`after` cursors are the principal values), the
administrator-only authority, the rate limit and the audit rule. There are no per-user grants, so a
user is never listed on their own account. The principals stay pinned to the OU id and creation
identity and to the account name, creation identity and generation, so a recreated OU or account is a
different principal. Launch, the directory, status, the viewer identity and this directory all decide
from the same rows (`TestPlatformPrincipalDirectoryAgreesWithLaunch`).

Evidence is not compared with access. Readiness evidence, enable plans and update checks written before
this change carry the digest of the install-time audience, and the grants digest was computed over a row
set that included the audience table. Neither can match again, and access is an OU policy edited at any
time rather than install evidence, so verification no longer compares the audience digest or the enable
plan's audience and grants digests (`verifyPlatformFrontendActivation`, the enable and update-start
checks). Everything else in the proof stays checked, and an installation activated or enabled before the
upgrade keeps launching and keeps verifying as an update source (`TestPlatformLaunchAndUpdateVerifyEvidence...`).
Deleting an OU deletes its access rows and does not move any installation's policy revision.
