# Dependency and publication review

This is development evidence for DEV-04. It is not a license grant, a release
approval or proof that a published artifact matches these inputs.

## Inputs

- Portal source: `d5b6537edc36ce9052883a49ce122f89d6501f81`
- Apps source: `917651d6bfc2dbc508d0d96f391031e806f5b99d`
- Go toolchain: `go1.26.10`
- SBOM format: CycloneDX 1.6
- Portal Go: 50 components
- Portal Web production graph: 313 components
- Apps production graph, lockfile-only: 73 components

The exact input and compressed SBOM hashes are in `acceptance.json`.

## Vulnerability gates

- `govulncheck` passes on the exact Portal CI candidate.
- Portal production `npm audit --omit=dev --audit-level=high` exits 0 with five
  low findings and no moderate, high or critical findings.
- Apps production audit exits 0 with no findings.
- Apps code check `41803523453` passes at `917651d`.

## License flags

Most components are MIT, ISC, Apache-2.0 or BSD-family licenses. Two Go
components need explicit review before distribution:

- The repository-owned
  `StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol`
  module is detected as `AGPL-3.0-only`.
- The transitive `github.com/golang/freetype` module is detected as
  `GPL-2.0-or-later`.

The Web graph also contains one `EPL-2.0` component and one
`MPL-2.0 OR Apache-2.0` component. No final allow/deny policy exists in the
repository, so these are review findings rather than silently accepted
licenses.

## Reproducibility note

The clean Apps commit completes `npm ci` under the repository CI toolchain
(Node 24.15 / npm 11.6). Local Node 26.9 / npm 11.19.1 rejects the same
versionless workspace metadata as out of sync, so declaring package versions
would improve cross-tool reproducibility.

The built-in `npm sbom` also rejects the versionless root with
`EINVALIDPURLTYPE`. The Apps SBOM therefore uses the standard CycloneDX
lockfile-only mode with `--ignore-npm-errors`. The existing uncommitted
package-version edits in the Apps worktree were not used and must not be
treated as released source.

## Still required

- Decide whether to declare workspace package versions so `npm sbom` and newer
  npm clients consume the Apps graph without compatibility flags.
- Adopt an explicit dependency/license policy and record owner or legal
  approval for the AGPL and GPL findings.
- Rebuild the actual release artifacts and regenerate SBOMs from their
  published bytes and OCI digests.
- Complete the release-key/environment approval.
