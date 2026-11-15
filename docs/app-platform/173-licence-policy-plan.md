# Licence policy gate: implementation plan

Status: implemented 2026-11-15 on `feat/app-platform-licence-gate` (Portal) and `feat/licence-gate` (Apps). This plan turns
section 8 of [doc 167](167-owner-decisions-remaining-acceptance.md) (`DEV-04`) into ordered, test-first steps and records
every resolution between the decision text and what the code and the real dependency graphs turned out to hold. **DEV-04
stays partial: the gate is in place and green on both branches, the owner decisions of 2026-11-15 are recorded, and the
final-artifact scan, the release-key approval and the branch-protection change are open.**

No Portal DDL was needed and none was added. Production behaviour changes in one respect only: the ELK diagram layout is
gone, and because Mermaid 12 uses ELK by default, diagrams are laid out with dagre (resolution 1).

## What the decision asks for

| Item (doc 167 §8) | Where it lives now |
| --- | --- |
| Allow, deny and unknown-fails policy | `scripts/licence-policy.json`, judged by `scripts/licence_scan.py` |
| `dompurify` under Apache-2.0 | election record in the policy |
| `golang/freetype` under the FreeType License | election record in the policy, `FTL` listed as election-only |
| `elkjs` aliased out, ELK layout unsupported, build test | `web/vite.config.ts`, `web/src/lib/elkDisabled.ts`, `web/scripts/check-no-elk.mjs` |
| CI gate, `elkjs` a "not shipped" exclusion valid only while the ELK test passes | `.github/workflows/licence.yml`, exclusion `requires: elk-build-test` |
| First-party apps stay AGPL-3.0, third-party apps are separate works | Apps repository: `license` fields, a test, and the `own` rule |

## Resolutions of conflicts between doc 167 §8 and the code

1. **Mermaid 12.1 lays every diagram out with ELK by default.** The decision describes ELK as "only used for the ELK layout
   option". In `mermaid@12.1.0` the default configuration has `layout: "elk"`, so a flowchart, state, class or ER diagram
   with no layout setting reaches the ELK loader. With `elkjs` aliased to a stub that throws, a plain `flowchart TD` failed.
   The portal now passes `layout: 'dagre'` to `mermaid.initialize` (`MermaidBlock.tsx`), and a Markdown test pins it. A diagram
   that explicitly asks for ELK (`layout: elk` in its frontmatter) still reaches the stub and fails with "The ELK layout is not
   supported in this build", which is the fail-closed behaviour the decision asks for. This is the one visible change: existing
   flowcharts move from ELK's layout to dagre's. They were checked in a real browser ("Verification").
2. **How Mermaid loads ELK.** `mermaid` registers an `elk` layout loader (`chunks/mermaid.core/chunk-*.mjs`,
   `elkLayoutLoaders`) that dynamically imports its own chunk `elk-*.mjs`, which has one import of the engine:
   `elkjs/lib/elk.bundled.js`, constructed with `new ELK()` when a diagram is laid out. The alias is a regular expression,
   `^elkjs(/.*)?$`, so any current or future elkjs path resolves to the stub. The loader chunk stays (34 kB, was 1.46 MB) and
   Mermaid's own option keys such as `elk.layered.spacing` are not ELK code.
3. **The stub throws on construction, not on import.** Importing it must succeed or the whole Mermaid chunk would fail to
   load; the throw happens at `new ELK()`, which only a diagram asking for ELK reaches.
4. **Markers for the ELK build test.** The test scans every file of the build output, source maps and HTML included, for the
   Java package names the ELK engine carries: `org.eclipse.elk.alg.` and `org.eclipse.elk.core.`. They are in the compiled
   engine (the 1.46 MB chunk) and appear nowhere in Mermaid's own code, which spells its option
   keys `elk.layered...`. Removing the alias makes the check fail (`assets/elk-*.js (org.eclipse.elk.alg.)`, exit 1); restoring
   it makes it pass. The detector itself is unit tested (`web/src/lib/elkGuard.test.ts`), including that Mermaid's option keys
   and an empty or missing output directory are handled (missing output fails closed, exit 2).
5. **"Valid only while the ELK build test passes" is enforced by the caller and the evaluator together.** The evaluator accepts
   `--verified elk-build-test`; an exclusion whose `requires` is not in the verified set fails with
   `exclusion-unverified`. The workflow passes the flag only when `npm run check:no-elk` succeeded in the same run, against the
   SPA that run just built, and fails the job when it did not. Running the scan without the flag cannot excuse `elkjs`.
6. **SBOM scope.** The Go SBOM is `cyclonedx-gomod app -main cmd/report-portal` (the modules linked into the binary, not every
   module in `go.sum`), built with the release constraints (`CGO_ENABLED=0`, linux/amd64). `-assert-licenses` is required:
   without it the detected licences are recorded only as `evidence` and the component `licenses` are empty. The npm SBOM is
   `@cyclonedx/cyclonedx-npm@6.0.1 --omit dev --package-lock-only`. 6.0.1 is the newest release older than three months; 7.x
   is days old. `cyclonedx-gomod` is v1.9.0, the version the DEV-04 evidence used.
7. **Several licence entries on one component are ANDed**, and an SPDX `X WITH Y` is one opaque identifier, so an exception can
   never be satisfied by its base licence. A free-text licence name is taken literally and never guessed.
8. **Elections are pinned evidence, not free text.** An election records the exact declaration it was written against
   (`id:GPL-2.0-or-later`, `expression:(MPL-2.0 OR Apache-2.0)`: matched case-sensitively, by kind, with order and
   parentheses), the dual licence it chooses from (`offered`), the elected licence, and the SHA-256 of the licence files
   that state the choice. If upstream changes the declaration or any of those files the election is reported as stale and
   ignored, and the component is judged on its merits. The offer must be a pure top-level OR of identifiers, the elected
   licence one of them and allowed by the policy (on the allow list, or `election_only` for the FreeType License), the
   declaration another operand or the whole offer, and no operand of the declaration or the offer may be denied or be
   an unallowed licence other than the declared side. So an offer cannot launder `SSPL-1.0` or `GPL-3.0` through an
   `OR`, and an `AND` anywhere in it refuses the election. `FTL` is election-only: never a free choice in an `OR`.
   Evidence: for `github.com/golang/freetype` the `LICENSE` in the module cache (it states the choice of the FreeType
   License or the GPL, version 2 or later); for `dompurify` the `LICENSE` (Apache-2.0) and `LICENSE-MPL` it ships, with
   the `(MPL-2.0 OR Apache-2.0)` declaration pinned exactly. Both were found; if one had not been, the election would fail
   closed.
9. **The own-licence rule.** AGPL-3.0-only passes only on components the policy names as the project's own: a namespace
   prefix ending in `/` (`github.com/KazuhaHub/`, `@portal-app/`) or one package named exactly, and that carry proof of
   being the local package: a purl (a component with none is never own), for Go a `pkg:golang` purl, for npm an install
   path of exactly `node_modules/<name>` (a nested copy of the same name is not it), no registry distribution and
   membership of the repository's npm workspaces. A third party carrying AGPL fails.
10. **A dedicated workflow, not a job of `test.yml`.** The release guard names `test.yml`'s jobs one by one and requires each;
    a new job there would change what a release waits on. `licence.yml` runs on every pull request and every push to `main` and
    `integration/app-platform`. The licence scan **is** a release prerequisite in code: `release_guard.py wait-ci` waits for a
    green `licence.yml` run of the exact commit (job `licence scan`) as well as the full `test.yml` run, and Apps'
    `status-publish.py` lists the `licence` job among the `check.yml` jobs a status release requires. What stays a
    repository setting, pending owner approval, is adding the two checks (`licence scan` in the Portal, `licence` in Apps)
    to branch protection.

## Order of work

1. Evaluator tests (`scripts/licence_scan_test.py`: allowed, denied, unknown, missing licence, `OR`/`AND`, nesting, election,
   stale election, exclusion, exclusion invalid when the ELK test did not pass, own rule, CLI exit codes, the checked-in policy),
   written first and run to failure, then `scripts/licence_scan.py`.
2. ELK detector tests (`web/src/lib/elkGuard.test.ts`) and the Markdown test for `layout: 'dagre'`, written first, then
   `check-no-elk.mjs`, the stub, the alias and the `layout` option.
3. The workflow, `actionlint`, and a full local replay of it from a clean clone.
4. The same gate in the Apps repository.

## Duplication

`scripts/licence_scan.py` and the allow, deny, election-only, `own.licences` and reviewed sections of
`scripts/licence-policy.json` are copied into the Apps repository (`scripts/licence_scan.py`, `scripts/licence-policy.json`,
`scripts/tests/test_licence_scan.py`). Each copy says so in its header, and the Portal evaluator, its policy and a test
(`AppsCopyNoticeTests`) name the Apps copy and how to update it. Both repositories are public, so Apps' `licence` job runs
`scripts/check_licence_copy.py` without credentials. It compares the evaluator (line for line, the module docstring
excluded) and those policy sections with:

- the Portal commit named in Apps' `scripts/licence-scan.pin`; and
- the live files on the Portal's `integration/app-platform` and `main`, each once the file exists there, failing on any
  difference.

Once a live branch carries the file, the pin must also be a commit that branch contains (GitHub's compare API, anonymously).
A pin on a feature-branch commit, which a squash merge orphans, therefore fails and has to be moved to the merge commit.
Until the Portal has merged the gate there is no live branch to compare with and only the pin is checked; the script says so.
To change the evaluator or the shared policy: merge in the Portal, copy the files from that commit into Apps, write the
commit's full SHA into `scripts/licence-scan.pin` and run the script. Elections and exclusions are Portal-only and are empty
in Apps.

## Resolutions after the independent review (2026-11-15)

The first implementation was reviewed and returned with fixes, all made test-first:

- **Nested components were never judged.** `cyclonedx-npm` nests a package that is not hoisted inside its parent's
  `components`; the Portal SBOM has 18 such packages (katex, tslib, parse5, `@babel/runtime`...) and Apps has 2. A nested
  denied package under an MIT parent passed. Components are now walked recursively and de-duplicated by `bom-ref`. The
  Portal scan is 397 components (50 Go across the six platforms, 347 npm, 329 of them top level), Apps 88 (73 npm, 15 Go).
- **A reviewed-as record replaced any declaration.** `BSD-Source-Code AND GPL-3.0-only` with an unchanged `LICENSE` passed.
  Each record now also pins the exact `declared` expression it was reviewed against (`BSD-Source-Code` for the Go modules,
  empty for `khroma`); anything else makes the record stale.
- **Elections.** The elected licence must be an operand of an `OR` in the pinned declaration. `golang/freetype` is declared
  as `GPL-2.0-or-later` only, so its election carries `offered: "GPL-2.0-or-later OR FTL"`, the upstream dual licence, and
  both the declared side and the elected side must be operands of it.
- **Licence files.** An npm licence file is read from the install path the SBOM records (`cdx:npm:package:path`, which tells a
  nested copy from the hoisted one); the installed `package.json` version must equal the component version; a root derived
  from a purl or path that escapes `node_modules` or the module cache is refused. A refused or missing file makes the record
  stale. A licence entry with only a `url` or `text` is unknown and fails; it used to be dropped.
- **Own components.** An npm component is own only if its name matches, it carries no registry distribution and it is a
  package of this repository's workspaces (the scan is given `--workspace-root`). A registry package cannot take the name.
- **Mind maps.** A global `layout: 'dagre'` made Mermaid treat the layout as the user's choice and drop the mind map's own
  layout. Probing every diagram type in a browser showed that only flowchart, state, class, ER and requirement diagrams reach
  ELK by default, so dagre is requested through `layoutForDiagram`, by detected type, and left unset for a mind map.
- **ELK guard.** `forbidElkjs`, a Rollup plugin, fails the build if any resolved module id lies under `node_modules/elkjs/`,
  which catches `lib/main.js` and `lib/elk-api.js` that the text markers miss (verified: the build fails with the alias
  removed). The marker scan stays as a second layer and gained markers for those wrappers.
- **Platforms.** The Go SBOM is built for all six release targets (the platforms differ: 48 components on linux, 50 on darwin
  and windows) and a test keeps the list equal to `release.yml`'s matrix. Apps builds it for the six status release targets.
- **Triggers.** `licence.yml` also runs on pushes to `integration/app-platform`.

## Resolutions after the second review (2026-11-15)

- **Mermaid configuration race.** `mermaid.initialize` is global and synchronous while parse and render are queued, so with a
  flowchart and a mind map on one page the last initialize won for both. Each diagram's initialize, parse and render now run as
  one turn on a module-level promise chain (`mermaidTurn.ts`), so its layout and theme are in force for its own render. A
  per-diagram `%%{init}%%` directive was rejected: it rewrites the source that is parsed, cached and exported, and the layout
  would depend on the author's own directives. A test renders both orders concurrently and asserts each diagram's layout at the
  start and the end of its render.
- **Elections** are evidence (resolution 8): hardened against laundering a denied or unallowed licence, pinned to licence files,
  a pure top-level OR.
- **Copy check** compares with the live Portal branches and requires a reachable pin (see Duplication).
- **Copies of one component.** Components sharing a `bom-ref` are judged once only when version and declared licences are
  identical; conflicting copies are each judged.
- **Own components** need a purl, and an npm one the exact install path (resolution 9).
- **Declarations** in reviewed-as records and elections are matched exactly and by kind (`id:`, `name:`, `expression:`).
- **Marker scan.** The generic markers (`ELKNode`, `elk-api.js`, `elk-worker.min.js`) are gone; the engine package names stay,
  and `forbidElkjs` covers elkjs's wrapper files by module id.
- **Apps platform list.** A test (`TheWorkflow` in `scripts/tests/test_licence_copy.py`, verified to fail when a target is
  removed from `check.yml`) keeps the Go SBOM targets equal to `build-status-release.py`'s.

## What the first scans flagged, and what the owner decided

The first scans failed on ten components. Nothing was added to the allow list; the owner decided on 2026-11-15:

| Component | Declared | Decision |
| --- | --- | --- |
| `golang.org/x/{crypto,image,net,oauth2,sync,sys,text}` | `BSD-Source-Code` (the licence detector's name for the standard Go BSD 3-clause text) | reviewed as BSD-3-Clause |
| `khroma` 2.1.0 (via mermaid) | none (no `license` field; an MIT `license` file) | reviewed as MIT |

Each decision is a reviewed-as record in `scripts/licence-policy.json`: the component names, the licence it is reviewed as
(which must be on the allow list), the declaration it was reviewed against, the file and the SHA-256 of that file
(`911f8f57...dad` for the seven Go modules, whose `LICENSE` is byte-identical, and `66b333b0...b49` for `khroma`'s `license`),
and it pins the declaration as `id:BSD-Source-Code` (`khroma`: none).
The scan re-reads the file in CI, from the Go module cache and `node_modules`; a changed hash, a changed declaration, a missing
file or a version mismatch makes the record stale, it is ignored, and the gate fails again. A record cannot override a
denied licence. The allow and deny lists did not change.

## Verification

- `python3 -B -m unittest discover -s scripts -p '*_test.py'` in the Portal, including 145 licence tests and the release guard
  tests (366 in all), and `python3 -B -m unittest discover -s scripts/tests` in Apps (211 tests);
- `cd web && npm run typecheck && npm run build && npm run check:no-elk` and `npx vitest run` (the whole suite);
- `actionlint` on both repositories' workflows;
- a real-browser probe of `mermaid` 12.1 with the production alias, over twenty-one diagram types with and without a layout
  option: only flowchart, state, class, ER and requirement diagrams need dagre; a diagram with `layout: elk` in its frontmatter
  fails with the stub's message;
- both jobs replayed locally from clean clones: all SBOMs, the SPA build, the ELK check, the copy check and the scan;
- green CI runs (Portal `licence` and `test`, Apps `check`): see `handoff/evidence/platform-licence-policy/acceptance.json`.

## Not done

- The scan covers source and lock inputs. It has not been run against a released binary or an OCI image digest; DEV-04's
  final-artifact scan stays open, and so does the release-key and environment approval.
- Adding `licence scan` (Portal) and `licence` (Apps) to branch protection is pending owner approval; no repository setting was
  changed.
- `apps/status/tests` (the browser acceptance harness) and `templates/react-app` are not scanned: the first is not shipped, the
  second has no lockfile and an app generated from it is a separate work.
- App `app.json` descriptors carry no licence field; the declaration is the package manifests and the repository `LICENSE`.
