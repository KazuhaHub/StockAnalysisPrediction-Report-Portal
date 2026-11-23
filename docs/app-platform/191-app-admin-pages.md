# Proposal: app-owned admin pages

Status: implemented with the owner's approval of the design and of the one table in section 4. Not deployed.
Related: doc [190](190-legacy-retirement-and-console.md) (the audience is the OU policy), doc
[117](117-backend-session-gateway.md) (the backend gateway), doc [41](41-authorized-document-http.md)
(authorized documents).

## 1. Problem

An app sometimes has operator pages (settings, "run now", delivery history) that must not be offered to its readers. Until
now the platform knew one surface: the reader page, open to the installation's audience. An app could only guess who is an
operator from the role in the viewer, and could not give a non-administrator the operator pages at all.

## 2. Manifest

`presentation.admin_routes` is optional and has the shape of `routes` (`id`, `path`, `layout`, optional `query`), with 1 to
8 entries when present (an empty array is refused). IDs and paths are unique across `routes` and `admin_routes` together.
Admin routes need no `/` path. The first admin route is the entry of the admin page. The source of truth is
`internal/appplatform/contract/descriptor-v2.schema.json`; `scripts/generate_app_contract.py` regenerates the Go and
TypeScript products. A release without `admin_routes` has no admin page, and every admin endpoint refuses it.

## 3. The surface claim

A request from the admin page is signed with `Signer.SignAdmin`, which adds `surface: "admin"` to the assertion;
`Claims.Admin()` reads it. Reader requests are signed with `Sign` and carry no `surface`.

What the claim says: the host verified, in the same transaction that admitted the request, that the viewer is an
administrator of this installation (section 5). What it does not say: that the viewer is in the audience, or that the
request came from an admin route. Therefore:

- A backend must require `claims.Admin()` on every admin route. Authorising by role alone is not enough: a Portal
  administrator outside the audience and a delegated user both arrive with `Admin()` true.
- A backend route that is only for readers must refuse an assertion for which `Admin()` is true, because that viewer may be
  outside the audience.
- The Portal does not map URLs to surfaces. The admin endpoint carries any backend route, as the reader endpoint does; the
  app owns the split.

Inside the Portal the surface is a field of the session value the HTTP layer hands to every gate, as the anonymous public,
ingress and machine reads are. Only the admin handlers set it, so no reader route can ask for it.

## 4. Storage

Step 0025 adds one table, exactly as approved:

```
CREATE TABLE IF NOT EXISTS app_platform_app_admins(
  installation_id TEXT NOT NULL,
  username TEXT NOT NULL,
  account_created TEXT NOT NULL,
  granted_by TEXT NOT NULL,
  granted_at BIGINT NOT NULL,
  PRIMARY KEY(installation_id, username))
```

It is a migration step and not a `baseSchemaStmts()` entry: the baseline is frozen (ADR 0035), and declaring a table there
would refuse every existing database. A dump from before the step restores with an empty table.

`account_created` is `users.created_at` at the grant. A row counts only while it equals the account's current value, so a
recreated account of the same name inherits nothing. Rows are deleted with the installation (`deletePlatformAppRows`, in the
removal transaction) and with the user (`Store.DeleteUser`). There is no foreign key.

## 5. Who is an app administrator

One decision, `platformAppAdmin`, replaces `platformLaunchAudience` on the admin surface: a Portal administrator (role holding
the manage permission), or an active user with a row whose `account_created` matches. Audience membership is not required, but
a delegate must still have a sound OU chain (`platformAccessChain`), as on the reader path: a broken chain refuses them.
Every other check of the launch, document and backend gates is unchanged: installation state, epoch, activation, release trust,
session revision, unresolved operations, and the final recheck after the backend IO. The admin surface additionally needs the
active release to declare `admin_routes`. Only Portal administrators grant and revoke; a delegated administrator cannot.

## 6. Endpoints

Portal administrators (`/api/admin/app-platform`, the management chain):

- `GET /installations/{installation}/app-admins` returns `{"admins":[{"username","displayName","grantedBy","grantedAt"}]}`,
  `grantedAt` in unix milliseconds, sorted by username, only rows that still match an active account.
- `POST /installations/{installation}/app-admins` with `{"username"}` returns `200 {"admin":{...}}`. An unknown or inactive
  user is `404 app_platform_user_not_found`; a missing or removed installation is `404 app_platform_not_found`. Granting again
  returns the same row; a row of a recreated account is replaced.
- `DELETE /installations/{installation}/app-admins/{username}` returns 204 and is idempotent.
- The settings projection gains `admin_page`, true when the current (or latest) release declares `admin_routes`.

Sessions (`/api/app-platform/installations/{installation}`, the chains of `launch` and `backend`):

- `GET /admin/launch` has the shape of `launch` with `routes` the admin routes, `layout` the layout of the first, `surface`
  `"admin"`, `entryPath` the path of the first, and `source` an admin document URL. The reader launch gains
  `"surface":"reader"` and `"manageable"`: true when the viewer passes the admin check and the release declares `admin_routes`,
computed in the launch's own transaction, so the reader page can offer the way to the admin page without scanning the
directory. The admin launch returns `capabilities: []` (section 9).
- `GET /app-documents/{installation}/admin/{generation}/{policy}/{release}/{frontend}/{entry...}` serves the same document
  bytes through the admin gate.
- `POST /admin/backend` has the contract of `backend`, admitted by the admin check, signed with `SignAdmin`, rechecked after
  the IO, and refused when the release declares no admin page.

A refusal looks like the existing launch refusals (404, same body), so it does not reveal whether the installation exists.

The app directory (`GET /api/app-platform/apps`) items gain `manageable`: the viewer administers the installation, its release
declares an admin page and it is installed. An administrator outside the audience gets the item with `openable: false` and
`manageable: true`. Ordering, paging and cursors are unchanged.

The directory decides which extra installations to list before paging, which needs the "release declares `admin_routes`" bit
for every installation the viewer administers. That bit is memoised by release SHA-256 in a small bounded in-process cache
(256 entries, reset when full): a release is immutable and its SHA-256 is the digest of its descriptor, so the bit never changes
for a key, and a page costs one query plus a descriptor read only for a release not seen before. The bit is only a prefilter;
the listing still verifies the signature of each item it shows and takes the final answer from the verified descriptor.

## 7. Audit

- `app_platform.app_admin_granted` and `app_platform.app_admin_revoked`: actor, installation, `app_id` and target `username`,
  written in the transaction of the change. A repeated grant and a revoke of a row that does not exist write nothing.
- `app_platform.admin_request`: one row for every non-GET admin backend request that passed the admin gate, with `method`,
  `path`, `status` (the Portal's answer), `outcome` and, when the app answered, `backend_status`. Outcomes: `ok` (the app
  answered), `refused` (the gate admitted the administrator, but the Portal refused the request before it reached the app,
  for example `503` when no runtime is available), `uncertain` (it may have reached the app and the answer is unknown, `502`).
  The row says "an administrator sent this", so a request refused at the gate (not an administrator, no admin page, no
  backend, a binding that does not match) writes no row and keeps only the server log (`logPlatformRefusal`). GET requests
  and malformed requests are not audited. Bodies are never recorded.

## 8. Run now

"Run now" is a route of the app's own backend, called through `POST /admin/backend` and audited like any non-GET admin
request. It is not a platform task: the platform neither schedules nor records it beyond the audit row.

## 9. Capabilities on the admin surface

Admin pages get the backend only in this version. Only the backend transport (`POST /admin/backend`) is admin-scoped. Network,
storage, reports, notifications and principals still go through reader endpoints, which are gated by the audience; an app
administrator outside the audience (a Portal administrator or a delegate with the audience closed) would be refused there, so
advertising them on the admin surface would offer a capability the frame cannot use.

- The admin launch returns `capabilities: []`; `backendAvailable` is unchanged. The web launch parser refuses an admin launch
  that lists any capability.
- The admin frame is told no capability and wires no transport but the backend, whatever its launch said, so the bridge
  refuses those requests locally, without a network request (fail closed). A reader frame is unchanged.
- Admin-scoped versions of those transports would each need their own gate decision (what an administrator outside the audience
  may read or write) and are left for a later version.
