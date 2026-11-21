# Built-in official catalogue and publisher

Status: Accepted 2026-11-21 (owner: "our official repository should be built in"; app-ID scope
decision A). Builds on [188](188-oci-catalogue-packages.md) (OCI packages),
[63](63-publisher-management-http.md) and [64](64-publisher-management-ui.md) (publisher trust),
[62](62-online-market-installation-ui.md) (catalogue browser).

## 1. What ships in Portal

| Item | Value |
|---|---|
| Official catalogue | `https://raw.githubusercontent.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/catalogue/catalogue.json`, shown as "Kazuha Hub" |
| Publisher ID | `kazuha-hub`, display name "Kazuha Hub" (a code constant; the trust model has no display name) |
| Namespace | prefix `kazuha-hub-` (official apps are `kazuha-hub-<name>`; the first is `kazuha-hub-market-monitor`) |
| Key | `kazuha-hub-2026-11`, Ed25519 `Z9uDuPmR0UIyQX3JIqgpKdFhQzqwJKMqHMBsNWvdIOk` (SHA-256 of the raw key `a9072bdb6b2911ebd09af406792abda71282a9eaa1e6ab94dfa5b8af721d0860`), not before 0, no expiry |

This is the apt default-sources / Homebrew core shape: a fresh Portal can browse and install official
apps with no manual trust step.

## 2. Trust model, stated honestly

Built-in trust means **whoever controls the Apps repository's release CI (which holds the signing
key in its Actions secrets) can ship code that installs on every Portal without a manual trust
step**. What still applies to each install is unchanged: the signed descriptor is verified, and the
per-install review shows capabilities, quota, network and secret bindings and needs an
administrator's confirmation. An administrator who does not accept the trade-off disables the
publisher and/or the catalogue (section 5).

## 3. App-ID scope: why a prefix

Publishers may never overlap (`ValidatePublisherTrust`), and namespaces are `exact` or `prefix`.

- **Prefix `kazuha-hub-` (chosen).** No schema or trust-model change; a new official app needs no
  Portal release; an administrator can still register any publisher outside the prefix.
- An exact list would need a Portal release per official app.
- An "everything unclaimed" kind would make every future official ID collide with administrator
  namespaces registered later, and would need the overlap rules redesigned.

## 4. Storage: real rows, no schema change

Many authority paths join `app_platform_publishers` / `app_platform_publisher_keys` directly, so the
built-in publisher is ordinary rows in those tables, written by Portal itself:

- **Fresh deployment:** `control/initialize` seeds it in the same transaction, after the existing
  "no history" check (seeding earlier would itself be history and block initialization).
- **Initialized deployment:** at startup, when control is initialized and not restore-paused, an
  idempotent ensure step runs under the trust lock. Every write is audited as
  `app_platform.publisher_trust_changed` by actor `system`, kind `builtin`.

The ensure step:

| State found | Action |
|---|---|
| No `kazuha-hub` row and the namespace overlaps nobody | insert publisher (enabled) and its keys |
| No row but an administrator publisher overlaps `kazuha-hub-` | write nothing; log; the directory reports a conflict |
| Row with exactly the built-in namespace whose every key is a built-in key with the same public key, other keys only if revoked (e.g. registered by hand before this release) | satisfied; add any built-in key IDs missing from the row |
| Row whose namespace differs, an unrevoked key of another ID, or a built-in key ID with another public key | conflict: write nothing, log, directory reports it, the row is not marked built-in and stays fully editable |

Ensure also runs after every publisher-trust change and when recovery ends, so resolving a conflict
(revoking a hand-registered row's own keys and setting its namespace to `kazuha-hub-`, or narrowing
an overlapping publisher) takes effect at once. The one conflict the console cannot resolve is a
row holding the built-in key ID with another public key: key IDs cannot be reused, so that needs an
operator. The enabled flag, revocations and revisions are never rewritten by ensure, so an
administrator's disable survives restarts and upgrades. A host restore that drops the row (backups
keep only disabled publishers and revoked keys) gets it back enabled when recovery ends.

## 5. What an administrator can do

- These limits apply while the row is the built-in one; a conflicting row with the same ID is the
  administrator's own and stays fully editable.
- **Disable / enable the publisher** with the existing policy change (audited; disabling quarantines
  running installations as for any publisher). Its namespaces cannot be changed, and keys cannot be
  added by hand: they come from Portal releases.
- **Revoke a built-in key** with the existing revoke; ensure never un-revokes it or re-adds it.
- **Not** register publisher ID `kazuha-hub`, nor any publisher whose namespace overlaps
  `kazuha-hub-` (the existing overlap rule refuses it once the row exists).
- **Disable / enable the official catalogue** (`POST /api/admin/app-platform/market/sources`,
  audited as `app_platform.market_source_changed` when the state changes, stored in `meta` key
  `app_platform_official_catalogue` = `disabled`). Disabling removes the preset from the browser;
  typing a catalogue URL by hand stays possible, as today. Catalogue choice never grants trust.

## 6. Key rotation

Key IDs follow `<publisher>-YYYY-MM`. A rotation is a Portal release that adds the new key to the
built-in list; ensure adds it to existing deployments at startup (publisher revision +1, open review
plans expire, as for a manual "add key"). The old key stays in the list until apps signed with it
have been re-released. Retiring it is an administrator's revoke in the Publishers panel, which the
release notes ask for: revocation quarantines running installations, which needs an administrator
actor, so Portal does not revoke a key by itself at startup. Release-driven revocation is a
follow-up.

## 7. Console

- Catalogue browser: a "Kazuha Hub" row with a built-in tag and a browse button, above the URL field,
  plus a switch to disable or enable it.
- Publishers panel: the built-in publisher shows its display name and a built-in tag; the namespace
  editor and "add key" are not offered for it; a conflict shows a warning that explains it.
- All strings through `t()` in zh-CN, zh-TW and en.
