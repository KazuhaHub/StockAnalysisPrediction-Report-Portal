# Signed application display metadata

The host can now use signed localized names, descriptions and shared Ant icons
on v2 directory cards and launch titles. This replaces the internal-ID fallback
for packages that supply metadata. It remains an experimental contract extension,
not D3/public SDK freeze, package publication or completed platform delivery.

## Package author instructions

Add `display` inside the existing descriptor's `presentation` object. Keep its
existing entry, route, locale and translation-key fields:

```json
{
  "icon": "file-text",
  "texts": [
    {"locale": "en-US", "name": "Notes", "description": "Personal notes"},
    {"locale": "zh-CN", "name": "笔记", "description": "个人笔记"},
    {"locale": "zh-TW", "name": "筆記", "description": "個人筆記"}
  ]
}
```

Use each of the three locales exactly once. Names contain 1–80 Unicode code
points; descriptions contain 1–320. Both fields must be nonempty, without edge
whitespace, control/format characters or malformed/replacement Unicode. They are
plain text, rendered by React, never HTML. Keys are closed and unknown fields are
refused. The icon vocabulary is `appstore`, `file-text`, `dashboard`, `calendar`,
`bar-chart`, `cloud`, `tool`, `experiment`; the host selects its own Ant component.
No external icon URL, SVG script, network request or arbitrary component name is
accepted. Custom branding needs a separately validated asset contract later.

Descriptor/signature identity covers these bytes. Changing copy requires a new
app-qualified release and a new signature; never edit an immutable signed release.
The existing `name_key`/`description_key` remain the app's internal translation
identifiers. Host copy is explicit in the descriptor and requires no app code or
translation bundle execution.

The field is optional/nullable only to preserve existing unpublished pre-freeze
fixtures. These use an ID/generic-icon fallback. Maintained v2 publishers should
provide it; requiring metadata in the eventual publishing tool and testing the
real catalogue/install path remain mandatory before public release. This slice
does not convert the existing legacy catalogue or migrate Status.

## Contract implementation

`internal/appplatform/contract/descriptor-v2.schema.json` remains the source.
`AppDisplay` and `AppDisplayText` are closed schema definitions; name/description
bounds and the icon vocabulary are generated for Go and TypeScript. Both strict
parsers additionally check exact locale coverage and Unicode/plain-text rules.
An absent field preserves the existing experimental descriptor corpus.

From Portal, regenerate both repositories with:

```sh
python3 scripts/generate_app_contract.py --apps-dir /absolute/path/to/Apps
python3 scripts/generate_app_contract.py --check --apps-dir /absolute/path/to/Apps
```

Do not edit generated schema/types independently. SDK consumer checks, the
schema/source generator suite and host checks must pass together. This extension
uses the existing descriptor blob; it introduces no table, migration, column,
side store or persistent cache.

## Server trust and cost boundary

Directory admission retains exact current session/user/OU filtering. It acquires
the publisher trust guard before installation rows, consistent with launch lock
ordering, and loads the bounded current trust snapshot. For each returned item
with an active activation it reads only the joined activation/release descriptor
and signature under shared row locks. Pending items without an activation retain
the fallback. It never reads or decompresses frontend content to render a card.

For each active record, checks bind:

- exact descriptor digest/signature to the current enabled publisher/key and app
  namespace;
- app ID, publisher, version, verification key, frontend digest and managed flag
  to stored release columns;
- activation generation and policy to the current installation.

At most 32 returned items require this lightweight verification. The descriptor
limit remains 128 KiB per release; signature parsing retains its existing bound.
After signature work and SQL waits, a fresh database clock/session check and
lightweight pinned-key time-window check precede commit. Locks prevent the trust
snapshot/release bytes from changing mid-read. No descriptor content leaves the
transaction before these final checks.

Invalid/revoked/expired metadata changes that card to the ID/generic-icon fallback
with `attention` and no open action. It does not hide other authorized cards.
Database/context errors still discard the page. The launch response gets its
optional display object from the fully verified manifest inside the existing
launch transaction, whose final authorization/trust recheck remains mandatory.

Display verification deliberately does **not** inspect the frontend ZIP or
establish executable integrity, physical readiness, provider completion or a
permission to run. Actual launch/document requests still perform those existing
checks. No previously loaded content is recalled by changing a list label.

## Host behavior

A shared display parser copies only known bounded values and uses the same
schema-derived vocabulary/bounds. Invalid supplied metadata refuses the response
at both the directory and launch boundaries. Absent/null experimental metadata
uses the fallback. Names and descriptions resolve from `usePrefs().lang`; icons
resolve only from the closed local Ant mapping. Launch title and frame accessible
label use the same localized name.

Changing panel language re-renders copy without another directory/launch request
or a new frame binding. Existing locale/theme propagation, app state, abort/stale
response protections and legacy routes remain in place. No app language picker,
new outer frame or independent design system is introduced.

## Verification and remaining work

- Go/SDK descriptor tests first refused complete display metadata; the generated
  extension and semantic validators now accept it and reject partial/duplicate
  locales, unsafe icon values, unknown fields, controls and excessive copy.
- Real directory tests cover verified labels and publisher revocation. A damaged
  ZIP still permits independent display verification but is refused by launch.
- Actual PostgreSQL release-lock waits cover signing-key expiry after admission;
  the page returns no trusted label/open action for that record. Existing
  session-expiry/cancellation and audience tests remain applicable.
- React tests cover invalid metadata at both boundaries and language changes on
  cards and launch/frame labels without additional fetches.
- Full local web regression passes 1663 tests across 149 files; typecheck, lint
  (existing unrelated warnings) and production build pass. SDK/consumer,
  generator, Go regression/race and source CI evidence must also be recorded.

Final local verification also passed: full Go regression with disposable
PostgreSQL (app package 132.597 seconds), final directory/display/HTTP race checks
(26.892 seconds), Go vet/staticcheck/build, schema generation plus 10 generator
tests, 181 SDK tests, demo (2) and Status UI (9) tests, all workspace typechecks,
plugin standard, template and experimental root checks, and demo/Status builds.
SDK changes are submitted in [Apps PR #27](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/pull/27).
Exact-source CI remains separate.

The local actual HTTP/SDK `--launch-page` browser fixture showed the signed host
title `Notes`, then `筆記` and `笔记` with the same app counter remaining at 1.
The child locale changed with the panel. This uses real launch/document handlers,
SDK and host page in the existing minimal fixture, not the complete AppLayout or
real market/install flow. The browser was controlled through the browser plugin;
no native screen control was used. It does not prove full browser/D5 visual
acceptance or pixel-level no-flash behavior.

Stopping the fixture initially delivered terminal SIGINT to its Go child as well
as Python. The Go child now has its own process group, allowing Python to signal
the existing stop marker and await normal test completion. A timeout terminates
that fixture's own group. A repeat startup/stop completed cleanly and removed its
temporary directory; no fixture process was left running.

These checks do not constitute full platform acceptance.
The directory predecessor `fa0182d94122dfd07a538825eb350fb1f0874deb` passed CI run
40274730211. Remaining work includes the actual publisher/market/install path,
physical resource activation, managed lifecycle, document sizing,
routing/history/drafts, sensitive broker policy and remaining P00–P14 acceptance.
No production enrollment/deployment, real credentials, SDK freeze or server beta
configuration changes are performed.
