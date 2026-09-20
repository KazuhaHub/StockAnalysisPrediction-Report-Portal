#!/bin/sh
# Verify a release binary's embedded provenance without executing it: compiler, platform, exact
# source commit, clean worktree, CGO setting, and main package. Mutable release notes and maturity
# are deliberately absent because GitHub Release is their sole authority.
set -eu
[ "$#" = 4 ] || { printf '%s\n' 'usage: check-release-build.sh BINARY GOOS GOARCH COMMIT' >&2; exit 1; }
binary=$1
goos=$2
goarch=$3
commit=$4
compiler=$(go env GOVERSION)
root=$(unset CDPATH; cd -- "$(dirname -- "$0")/.." && pwd)
expected_path=$(cd "$root" && go list -m)/cmd/report-portal
info=$(go version -m "$binary")
printf '%s\n' "$info" | awk -v compiler="$compiler" -v goos="$goos" -v goarch="$goarch" \
    -v commit="$commit" -v path="$expected_path" '
    NR == 1 { version = ($NF == compiler) }
    $1 == "path" && $2 == path { main = 1 }
    $1 == "build" && $2 == "GOOS=" goos { os = 1 }
    $1 == "build" && $2 == "GOARCH=" goarch { arch = 1 }
    $1 == "build" && $2 == "CGO_ENABLED=0" { static = 1 }
    $1 == "build" && $2 == "vcs.revision=" commit { revision = 1 }
    $1 == "build" && $2 == "vcs.modified=true" { dirty = 1 }
    END { exit !(version && main && os && arch && static && revision && !dirty) }
' || {
    printf '%s\n' 'release binary provenance mismatch (compiler, path, platform, commit or clean worktree)' >&2
    printf '%s\n' "$info" >&2
    exit 1
}
printf '%s\n' "verified $goos/$goarch $commit built by $compiler with a clean worktree."
