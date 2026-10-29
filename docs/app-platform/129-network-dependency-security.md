# Restore the ordinary Go test gate after network security updates

Status: dependency repair in development. Local reachable-vulnerability scanning
passes; ordinary full Go tests and exact revision CI remain required. This does
not complete the application platform, enable a broker grant or authorize release.

## Observed failure and resulting change

The preceding policy/executor slices passed their explicit contract, SQL,
web/race/Docker and native acceptance jobs, but general Go CI stopped at
`govulncheck`: `GO-2026-6866` and `GO-2026-6860` in `golang.org/x/net@v0.60.0`.
The ordinary broad test step after that gate was skipped. The latest direct
example is run40960961922/job123753278653 at functional fc418ed4; the logs report
v0.62.0 as fixed. A selected pre-gate test suite was never full ordinary Go CI.

This separate maintenance change selects `golang.org/x/net@v0.62.0` and runs
`go mod tidy`. Keep the existing vulnerability gate and its ordering. Do not
suppress findings, move tests around to disguise the gate, or claim older PRs
passed the tests that they skipped.

The module graph's required companion versions are:

| Module | Previous | Selected | Why |
| --- | --- | --- | --- |
| golang.org/x/net | v0.60.0 | v0.62.0 | Fixed version reported by the reachable-vulnerability gate |
| golang.org/x/crypto | v0.57.0 | v0.58.0 | Required by x/net v0.62.0 |
| golang.org/x/sys | v0.48.0 | v0.49.0 | Required by the new net/crypto graph |
| golang.org/x/text | v0.42.0 | v0.43.0 | Required by the new net/crypto graph |
| golang.org/x/sync | v0.23.0 | v0.24.0 | Required by x/text v0.43.0 |

`x/net` also requires `x/term@v0.47.0` in its module graph; the application does
not gain a new direct term dependency. Tidy removes obsolete selected-version
sums and includes checksums needed by the resulting graph. Go remains 1.26.10;
there is no toolchain/channel/configuration upgrade, replace directive, fork,
vendored patch or new application permission.

## Verification and interpretation

Commands use the repository toolchain and limited local parallelism:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go mod tidy
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 -show verbose ./...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go vet -p 2 ./...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 -count=1 ./...
```

Both local vulnerability scans exit zero: affected symbols zero and affected
imported packages zero. Vet passes. The verbose scan still reports two module
findings outside imported vulnerable packages:

- `GO-2026-6222`, x/image v0.43.0 VP8L decoding, fixed in v0.45.0. This change
  does not select an image decoder update; the scan reports no affected imported
  package or called symbol in this application.
- `GO-2026-5932`, unmaintained x/crypto/openpgp, with no fixed version. The
  application does not import that affected package in the scanned build.

Therefore the evidence is that the existing reachable-vulnerability gate passes,
not that every package in all required modules is free of findings. Future
imports/features can change reachability and must run the same gate. The tool
and database are external inputs; repeat the check at release rather than treating
this result as a permanent certificate.

The full local ordinary Go run is pending. Local PostgreSQL is unavailable;
its skipped cases must not be counted as passed. General Linux CI must run its
PostgreSQL-backed ordinary full Go test step after govulncheck, in addition to
existing pre-gate groups. All selected race/web/Docker checks and disposable
native quota/browser/controller acceptance must be checked at the exact revision,
since crypto, Unicode and syscall companion modules changed too.

No version-string assertions or mirrored dependency tests are added: the failing
vulnerability gate is the reproducer, and real consumer/whole-project tests are
the regression evidence. No baseline, migration, storage product, production,
Status, marketplace catalogue, SDK freeze or server beta setting changes.

## Remaining platform work

This removes a verification obstruction; it is not the P07 broker authorization
adapter. Reviewed required/optional grants, current actor/service dispatch,
executor ownership/budgets, revoke/restore behavior, later storage decisions and
all remaining P00–P14 requirements are still required. The preceding records
continue to describe their actual old revisions and inherited failures; they are
not retroactively relabeled green.
