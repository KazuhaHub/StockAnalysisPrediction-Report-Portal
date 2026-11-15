# Licence policy gate: implementation plan

Status: implemented 2026-11-15 on `feat/app-platform-licence-gate` (Portal) and `feat/licence-gate` (Apps). This plan turns
section 8 of [doc 167](167-owner-decisions-remaining-acceptance.md) (`DEV-04`) into ordered, test-first steps and records
every resolution between the decision text and what the code and the real dependency graphs turned out to hold. **DEV-04
stays partial: the gate is in place and fails on ten components that need an owner decision ("What the real scans flagged").**

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
8. **Elections are pinned to what upstream declares.** An election records the declaration it was written against
   (`dompurify`: `MPL-2.0 OR Apache-2.0`; `freetype`: `GPL-2.0-or-later`, the only side the detector reports). If upstream
   changes the declaration the election is reported as stale and ignored, and the component is judged on its merits, so a
   licence change cannot slip through an old election. `FTL` is election-only: it is never a free choice in an `OR`.
9. **The own-licence rule.** AGPL-3.0-only passes only on components the policy names as the project's own: a namespace
   prefix ending in `/` (`github.com/KazuhaHub/`, `@portal-app/`) or one package named exactly. A third party carrying AGPL
   fails.
10. **A dedicated workflow, not a job of `test.yml`.** The release guard names `test.yml`'s jobs one by one and requires each;
    a new job there would change what a release waits on. `licence.yml` runs on every pull request and every push to `main`.
    Making it a required check, and a release prerequisite, is a repository setting for the owner.

## Order of work

1. Evaluator tests (`scripts/licence_scan_test.py`: allowed, denied, unknown, missing licence, `OR`/`AND`, nesting, election,
   stale election, exclusion, exclusion invalid when the ELK test did not pass, own rule, CLI exit codes, the checked-in policy),
   written first and run to failure, then `scripts/licence_scan.py`.
2. ELK detector tests (`web/src/lib/elkGuard.test.ts`) and the Markdown test for `layout: 'dagre'`, written first, then
   `check-no-elk.mjs`, the stub, the alias and the `layout` option.
3. The workflow, `actionlint`, and a full local replay of it from a clean clone.
4. The same gate in the Apps repository.

## Duplication

`scripts/licence_scan.py` and the allow, deny and `own.licences` sections of `scripts/licence-policy.json` are copied into the
Apps repository (`scripts/licence_scan.py`, `scripts/licence-policy.json`, `scripts/tests/test_licence_scan.py`). Each copy
says so in its header. There is no automatic check that they agree; change both in one sitting. Elections and exclusions are
Portal-only and are empty in Apps.

## What the real scans flagged

The gate was run against the real repositories (acceptance record:
`handoff/evidence/platform-licence-policy/acceptance.json`). Nothing was added to the allow list to make it pass.

| Component | Where | Declared | Why it fails |
| --- | --- | --- | --- |
| `golang.org/x/crypto` v0.58.0, `x/image` v0.43.0, `x/net` v0.62.0, `x/oauth2` v0.37.0, `x/sync` v0.24.0, `x/sys` v0.49.0, `x/text` v0.43.0 | Portal Go | `BSD-Source-Code` | not on the allow list |
| `golang.org/x/sys` v0.48.0, `x/text` v0.42.0 | Apps status service | `BSD-Source-Code` | not on the allow list |
| `khroma` 2.1.0 | Portal web (via mermaid) | none | its `package.json` has no `license` field |

`BSD-Source-Code` is the identifier the licence detector in `cyclonedx-gomod` gives these modules. Their `LICENSE` files in the
module cache are byte-identical and are the standard Go BSD 3-clause text, so this looks like a detector misclassification of
BSD-3-Clause, but that is a reading, and the gate does not make it. `khroma` ships a `license` file with the MIT text and no
`license` field in its manifest, so no SBOM tool can see it. Both need an owner decision (a reviewed-as record per component, or
a change to the policy); see the report that accompanied this branch.

## Verification

- `python3 -B -m unittest discover -s scripts -p '*_test.py'` (61 licence tests, all repository script tests green);
- `cd web && npm run typecheck && npm run lint && npm run build && npm run check:no-elk`, and `npx vitest run` for the Markdown,
  Mermaid cache and ELK guard tests;
- `actionlint` on both workflows;
- a real-browser probe of `mermaid` 12.1 built with the production alias and the portal's initialisation: flowchart, flowchart
  with a subgraph, sequence, state, class, ER, mind map, pie and xychart diagrams all render; a diagram with `layout: elk` in its
  frontmatter fails with the stub's message;
- the whole workflow replayed locally from a depth-1 clone of the branch: SBOMs, SPA build, ELK check, scan.

## Not done

- The scan covers source and lock inputs. It has not been run against a released binary or an OCI image digest; DEV-04's
  final-artifact scan stays open.
- No CI run of either workflow is cited yet; the branches are pushed and the first runs are the evidence.
- `apps/status/tests` (the browser acceptance harness) and `templates/react-app` are not scanned: the first is not shipped, the
  second has no lockfile and an app generated from it is a separate work.
- App `app.json` descriptors carry no licence field; the declaration is the package manifests and the repository `LICENSE`.
