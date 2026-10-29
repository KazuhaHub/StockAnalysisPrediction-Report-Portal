# Development v2 authoring template and candidate checks

Status: implementation and acceptance in progress. This advances P02/P06
and supplies a developer starting point for P08; it does not complete the two
reference apps, persistent storage, marketplace lifecycle, D5/D6 or P00–P14.
No database change, production activation, Status migration, catalogue publication,
stable SDK freeze or server beta change is included.

## What the developer gets

The Apps generator has an explicit `--template v2` option alongside the legacy
published-app template. A new workspace uses the same pinned React, TypeScript,
Ant Design and SDK dependencies. It contains:

- `app.v2.json`, a provisional v2 source declaration with signed-display texts in
  three locales, document routes `/` and `/editor`, no backend and zero requested
  capabilities. The frontend digest starts as a clearly documented build marker.
- One AppRoot with immutable trusted development-origin configuration, and an
  application using useAppPage/useAppPresentation/useAppNavigation/useAppDraft.
- Shared labelled form fields and normal Ant typography/content. No duplicate
  inner header, custom bridge, independent locale selector or fetch authority.
- A Preview action and an in-memory draft that survives route and locale commits.
  The UI states clearly that refresh/close discards it. Preview is not a save.
- A real MessageChannel/root integration test, three matching locale resources,
  strict TypeScript config and a local classic-script/CSS build.

The checked generator refuses invalid IDs/names/origins before creating an app,
existing directories, unsupported template modes and invalid publisher IDs.
No trust enrollment is inferred from the public publisher identifier.

## Reproducible developer commands

Run from the Apps repository root:

```sh
python3 scripts/create-app.py my-app --name "My app" --template v2 \
  --parent-origin https://portal.example.invalid --publisher development-publisher
npm install
python3 scripts/plugin-standard.py
npm run typecheck
npm test --workspace=apps/my-app/frontend
python3 scripts/build-app-v2.py my-app
```

Select the real trusted development origin instead of the example. Only canonical
HTTPS or exact loopback HTTP is accepted. No credentials, path, query or fragment
are allowed. Parent origin is infrastructure build configuration, not a user
installation form. It is never derived from a parent DOM/referrer/query/message.
The developer guide explains each file, checks, output and error recovery.

This per-development-deployment origin is provisional. Trusted cross-deployment
bootstrap/distribution must be accepted before a stable public template/default.
The default generator still preserves legacy source until that migration is
approved; this development option is not a public compatibility promise.

## Build and integrity

`build-app-v2.py` validates exact source bytes with the strict SDK descriptor
parser, builds the frontend, adds its entry document, packages local assets with
bounded member count/file/total bytes and fixed ZIP metadata, then validates a
new descriptor with the actual ZIP SHA-256. The source file is never rewritten.
Both `frontend.zip` and `descriptor.json` go into a new local directory, by default
`apps/my-app/.development/candidate`. Existing files/directories/symlink outputs
are refused. Use `--output apps/my-app/.development/check-2` for another build.

The packer sorts paths, fixes timestamps and modes, refuses symlinks/unsafe paths,
and detects changed member lengths. Its conservative authoring bounds are not a
replacement for the host's current bounds. The strict parser is bundled directly
from the same SDK source using installed build tools; no second descriptor schema,
remote compiler, fetched trust snapshot or app-supplied validator is used by CLI.
Original integer lexemes, duplicate keys and unknown fields remain visible to
strict preflight. Output validation happens before candidate files are written.

These are unsigned local candidates. The tool neither uploads nor signs them,
changes the catalogue, enrolls a publisher, provisions a backend or grants a
capability. It does not independently inspect remote OCI bytes or accept an image
reference as verified execution authority. Host artifact/HTML/CSP, signatures,
compatibility, current permissions and lifecycle checks remain mandatory.

## Standards enforcement

The standard checker discovers both manifest formats, including unlisted apps,
and refuses dual declarations or a frontend with no declaration. Legacy apps
retain their existing required foundation. Development v2 apps require AppRoot
and the shared page/presentation/navigation/draft hooks, a valid immutable origin
bootstrap, shared versions and locale parity. Legacy providers/pages/clients and
independent locale controls are refused in a v2 source. Direct browser authority,
copied handshakes and symlinked source remain refused.

This is a structural development check, not a JavaScript security proof. Host
runtime authorization remains independent even when a source passes the checker.

## Verification scope

Meaningful failing tests first demonstrated missing v2 creation and packaging.
Pure authoring tests exercise zero authority, unlisted output, current foundations,
dual-manifest and origin refusal, unsafe ZIP inputs, deterministic ZIP bytes,
real digest binding, unchanged sources and no candidate after validation failure.
The fresh-workspace integration checks actually generate both template kinds,
typecheck/test/build them and run strict descriptor preflight on the v2 output.

The Portal acceptance workflow receives only the generated public frontend and
descriptor. Go parses that exact descriptor, checks the ZIP checksum, compiles the
self-contained sandbox document and refuses changed ZIP/unknown descriptor fields.
A dedicated browser step installs the generated frontend with its signed
presentation under the existing disposable publisher/user fixture, opens the real
Portal shell, checks Preview, draft cancel/confirm, history without remount, zero
backend calls and the unchanged sandbox. It records a screenshot and fixed
JSON evidence only after the browser assertions pass. The identity/trust fixture
is explicit: this is not production publisher enrollment or a market release.

Local checks passed: 54 authoring/catalogue Python tests, SDK267 tests, demo2,
Status UI9, workspace types, both generated template type/test/builds, strict SDK
preflight, and Go parsing/digest/sandbox-document checks on the generated candidate.
The complete local Portal/HTTP/session/SQLite browser drill verified Preview,
draft cancel/confirm and retained text after browser-back navigation. It used only
the browser plugin, no native screen control. Linux CI repeats this flow using
Apps source 6abdafa1b21c8d62b48d39646962914b9a1b3d9f. Initial Linux runs
40942704891 and 40943549143 failed at the initial page: the disposable store had
no named business timezone, so launch metadata intentionally supplied an empty
zone and the frontend refused it. A bounded failure screenshot/status diagnostic
confirmed this. The fixture now sets UTC explicitly, as the local drill already
did, preserving the production fail-closed timezone rule. Final CI remains pending.
The broader P06/P07/P08, full accessibility/visual cases, persistent user data,
managed reference app, marketplace/update/uninstall/recovery and release gates
remain open.
