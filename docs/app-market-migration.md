# Application market migration

The shared application repository is
[Report Portal Apps](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps).
Applications retain independent versions, frontend archives and optional backend
services. Changing the catalogue does not install an app or start its backend.

## Catalogue addresses

- Canonical: `https://raw.githubusercontent.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/main/index.json`
- Retained legacy: `https://raw.githubusercontent.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/main/apps/index.json`

The compiled default points to the canonical catalogue. The existing database
setting remains an explicit override; this change does not rewrite it or require
a schema migration.

| Deployment state | Behavior after upgrading |
| --- | --- |
| No saved override | Uses the new compiled default |
| Explicit legacy URL | Continues using the legacy URL |
| Custom URL | Continues using that custom URL |
| Installed applications | Versions, assets and grants remain unchanged |
| Catalogue unavailable | Fetch/install reports an error; installed apps remain available |

To move an explicitly configured deployment, an administrator opens application
management, enters the canonical URL in the market settings and fetches its list.
Review each app's version and requested permissions before confirming installation.
Canceling the permission dialog does not install the app. An independent backend
still requires separate deployment, pairing and authorization. Avoid a database
mass edit or an automatic app update during this transition.

## Keep older installations working

Keep this repository's `apps/index.json`, `apps/demo-symbols.zip` and published
commit-pinned archive addresses available. The shared repository is the source
for catalogue metadata. Refresh the retained legacy selection from a reviewed
Apps checkout using Python 3.11 or later:

```sh
python3 scripts/catalogue.py check --verify-downloads
python3 scripts/catalogue.py export-legacy --portal-root /absolute/path/to/StockAnalysisPrediction-Report-Portal
```

Run these commands from the Apps repository. Use a Portal checkout with no
unreviewed changes to its legacy catalogue. Review the resulting Portal diff and
run `go test ./internal/app -run '^TestPublishedAppMarketMatchesItsBundles$'` there
before committing it.

The exporter verifies the canonical downloads, preserves the legacy list of IDs
and each local ZIP path, and requires identical local and published archive
bytes. It refuses new notification/extension capabilities, unsafe paths, symlinks,
missing identities and concurrent catalogue changes. It writes only the legacy
JSON file. It does not update ZIPs, copy all newly published apps into the old
catalogue, or decide whether an old Portal binary supports a new app. A changed
archive requires an explicit source, packaging and compatibility review first.

## Verification and rollout

The public-market integration test exercises the real canonical URL and immutable
demo archive through the Portal listing/install handlers:

```sh
TEST_PUBLIC_APP_MARKET=1 go test ./internal/app -run '^TestCanonicalAppMarketPublishedInstall$' -count=1
```

The Apps browser suite also exercises listing, cancellation, permission confirmation,
installation and a sandbox bridge query using the published minimum Portal binary.
These checks establish the demo path, not a successful installation of every future
catalogue entry. Each new entry still needs its own immutable release, verified
assets, supported-host acceptance and permission review.

Status remains excluded until its release acceptance is complete. Moving the
default does not publish Status, activate `/status`, grant OU access, configure
provider credentials or enable notification delivery.

## Rollback

An administrator can save the retained legacy URL or a previously reviewed custom
URL in the market UI. Reverting the compiled default affects only deployments
without an override. Neither action downgrades or uninstalls an app. Application
and backend recovery follow their own versioned runbooks; never run an older
binary against an incompatible database as a catalogue rollback shortcut.
