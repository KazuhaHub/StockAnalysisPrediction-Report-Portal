# Shared experimental descriptor schema and generated products

Status: P02 in progress. This standardizes the existing declaration structure;
it does not freeze D3/D4, admit packages or deploy the new application platform.

## Source and products

The source of truth is
`internal/appplatform/contract/descriptor-v2.schema.json` in Portal. It uses JSON
Schema draft 2020-12 with closed objects and explicit nullable defaults. The
`x-go-*`, `x-typescript-name` and `x-portal-parser-limits` annotations describe
type spelling, serialization and byte-parser bounds; they grant no authority.

`scripts/generate_app_contract.py` derives:

| Product | Purpose |
| --- | --- |
| Portal `internal/appplatform/descriptor_contract_generated.go` | Existing Go DTOs, regular expressions, enums, protocol literals and limits |
| Portal `web/src/lib/descriptor-contract.generated.ts` | The same frontend domains for the isolated v2 host transport |
| Apps `packages/app-sdk/src/descriptor-contract.generated.ts` | Descriptor interfaces, field lists and the same primitive domains/limits |
| Apps `packages/app-sdk/schema/descriptor-v2.schema.json` | Exact source bytes for editors and standard JSON Schema tooling |
| Apps `scripts/generate_app_contract.py` | Exact generator copy for offline SDK product checks |

Go and TypeScript embed the SHA-256 of the source schema. The parser uses these
products instead of independently spelling its type/field/capability/locale/
platform/layout lists and resource limits. Existing public Go type names, JSON
tags, optional/null defaults, parser bounds and the shared 44-case byte corpus
are preserved. The SDK's experimental descriptor subpath stays separate from
its legacy React/bridge entry point.

Do not hand-edit generated products or maintain a second authored schema in Apps.
The Apps copy is a derived distribution artifact. Source changes need paired
Portal/Apps review and verification; local checks alone cannot prove that an
unrelated checkout has the same revision.

The generator additionally distributes the [presentation transport and its
source/test pins](22-presentation-bridge-implementation.md). Those are derived
from Portal's canonical TypeScript, not a general JSON Schema semantic compiler.

## Updating the contract, step by step

1. Check out clean development branches in Portal and Apps. Do not edit a release
   tag, published ZIP, production configuration or the legacy `app.json` format.
2. Edit the Portal source schema. Required fields, nullability, enums and bounds
   are contract behavior. Review D3 before freezing a public change.
3. From Portal root, run the following with the actual absolute Apps checkout:

   ```sh
   python3 scripts/generate_app_contract.py --apps-dir /absolute/path/to/Apps
   python3 scripts/generate_app_contract.py --check --apps-dir /absolute/path/to/Apps
   python3 -B -m unittest discover -s scripts -p 'generate_app_contract_test.py'
   GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 -race ./internal/appplatform
   ```

4. From Apps root, run `python3 scripts/generate_app_contract.py --check`, SDK
   tests, all-workspace typecheck, plugin-standard, demo/Status and template
   checks. Run full Portal source checks because its restore inspector consumes
   the same declaration types. Record paired source/CI evidence before merge.
5. If semantics changed, update the shared raw-byte corpus in both repositories
   and its pinned digest deliberately. Regenerating types is not a substitute for
   adding acceptance/refusal cases or implementing the semantic check.

`--check` refuses missing/stale products without writing. Apps mode requires
Python only; it does not invoke Go, fetch a schema or access the network. Portal
generation additionally uses local `gofmt` on stdin/stdout. The generator refuses
duplicate source keys, external references, unsupported schema keywords and
divergent domains that currently share a parser primitive. Extend the generator
and both parsers explicitly when introducing new shapes, rather than bypassing a
refusal. It is a deliberately bounded generator, not a general schema compiler.

CI includes the Portal generator tests and the Apps product check. Exact source,
generator and generated files are pinned to LF for byte comparisons on Windows.
Do not reformat generated TypeScript separately; rerun the generator instead.

## Editor checks are not admission

The SDK exposes `@portal-app/sdk/descriptor-v2.schema.json`. Configure editor
association outside the signed descriptor, for example in workspace settings:

```json
{
  "json.schemas": [{
    "fileMatch": ["descriptor.v2.json"],
    "url": "./node_modules/@portal-app/sdk/schema/descriptor-v2.schema.json"
  }]
}
```

Adjust that local URL for the package manager/workspace layout. Do not add a
`$schema` property to the descriptor itself: the strict descriptor parser does
not permit that field. Never apply this schema to legacy `app.json`.

Standard JSON Schema validates decoded structure. It cannot recover duplicate
keys, malformed UTF-8, raw byte size, integer lexical forms or exact signed bytes.
The retained strict parsers additionally enforce normalized paths, prerelease
numeric rules, unique route IDs/paths, home route, disjoint capabilities,
backend/background dependencies and inclusion of the current readable schema.
Actual semver range evaluation remains a separate admission step.

Tests intentionally demonstrate that editor validation accepts `../ui.zip`, a
missing background dependency, duplicate route identity and a missing current
data schema while the strict parser refuses. A decoded `64.0` also satisfies a
JSON Schema integer but the byte parser rejects it. Therefore the installer must
still parse exact bytes, verify trust/signatures/artifacts, evaluate compatibility,
obtain current review and enforce runtime policy. The schema itself must never
serve as an execution or capability grant.

## Verification scope

The SDK uses test-only Ajv 8.20.0 to compile/validate the draft schema, check
supported raw-corpus values, refuse malformed shapes and exercise those deliberate
semantic boundaries. Ajv is not imported by the app runtime or descriptor parser.
Go checks source digest and reflected serialization fields; both existing strict
parsers still run their unchanged 44-case corpus. Generator tests cover repeatable
output, read-only drift detection, offline Apps mode and refusal behavior.

Local verification passed: full Portal Go tests with disposable PostgreSQL, vet,
build, full staticcheck, targeted appplatform race tests and all 52 Python harness tests; SDK 82
tests, nine Status UI tests, two demo tests, all-workspace typecheck, foundation
checks and generated-template typecheck/test/build. Both checkouts pass the
cross-repository generator drift check. Source PR CI remains a separate gate.

These are declaration/tooling checks, not a full generated semantic validator,
trusted publisher service, new bridge, shell, runner or complete application
installer. Runtime acceptance, public freeze, visual and rollout gates remain open. No database shape,
installed application, market catalogue or server `beta` setting changes here.
