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

Local checks passed: 55 authoring/catalogue Python tests, SDK267 tests, demo2,
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
did, preserving the production fail-closed timezone rule. Portal15e289f2 passed
run40944298514/job123695464741. Its generated-template JSON confirms the browser
flow, source/ZIP hashes, test publisher identity and no backend/production
provisioning. The full hardened quota report retains all eight SDK browser flags,
owned cleanup and neighbor preservation true; execution_ready remains false.

The generator then received a failing-then-passing literal-name regression fix:
marker-like text inside a quoted application name must remain literal instead of
being substituted twice and damaging JSON. Apps eea3e9afeb1896513def64edefb2cbac3ff3e80e
passed all jobs in run40944731609. The workflow now pins that exact source and
repeats acceptance at the paired revision. Portal c9b67807 passed native run
40945555000/job123699847538 with the same generated ZIP/descriptor hashes and
all prior native/browser flags. General run40945554919 passed front-end, Docker
and race checks but failed the inherited x/net vulnerability gate after its pure
contract and affected storage suites passed.

A final name-validation correction reads the maximum directly from the shared
schema and refuses surrounding whitespace, format controls and replacement
characters before writing a workspace. The regression test first demonstrated
four accepted invalid names. Apps e7d12a609b1e872f607069e1b5b418e4751fd1ee passed
all CI in run40947104413; the workflow now pins that exact source. Final paired
Portal b9dd81af3a09167a016366ba877b724cfd8e4b10 passed native run40954734818 /
job123731758604. Its browser-evidence JSON again binds the exact default ZIP hash
5200fc74f29808009fe22da2e1900d82e925fa66094641c3ff66d543bfb7c876 and descriptor
b9bbbec9be9e9d61f7300945d6066f1121a1a935182f2deeed94fc6f43e37524. All eight
prior SDK browser flags, owned cleanup and neighbor preservation remain true;
execution_ready remains false. General run40954734940 passed every front-end/race/workflow check.
Its first Docker job123731760387 stopped on an HTTP/2 INTERNAL_ERROR from the
module proxy before tests. A targeted rerun job123735340512 passed image/font and
end-to-end smoke checks. Go-test still fails only the inherited GO-2026-6866 and
GO-2026-6860 in x/net v0.60.0 after its pure contract and six affected storage
suites pass; the ordinary broad test step is skipped. This is not all-green
Go CI. Exact results are linked in PR333. Full platform acceptance remains open.
The broader P06/P07/P08, full accessibility/visual cases, persistent user data,
managed reference app, marketplace/update/uninstall/recovery and release gates
remain open.
