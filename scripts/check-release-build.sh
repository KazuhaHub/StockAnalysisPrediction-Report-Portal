#!/bin/sh
# Verify a release binary's embedded provenance without executing it: the compiler that built it,
# the platform it targets, the exact source commit, a clean worktree, CGO off, the expected main
# package, and the release note it carries. Guards what would otherwise ship silently — a wrong
# module path makes every ldflags -X a no-op (the binary keeps "dev"), a checkout of the wrong commit
# produces a binary with the right version string and the wrong code, and a skipped note step ships
# a release whose update prompt has nothing, or something stale, to show.
set -eu
[ "$#" = 6 ] || { printf '%s\n' 'usage: check-release-build.sh BINARY GOOS GOARCH COMMIT NOTE_FILE HISTORY_FILE' >&2; exit 1; }
binary=$1
goos=$2
goarch=$3
commit=$4
note=$5
history=$6
[ -s "$note" ] || { printf '%s\n' "release note $note is empty or missing" >&2; exit 1; }
[ -s "$history" ] || { printf '%s\n' "release history $history is empty or missing" >&2; exit 1; }
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

# The note is compiled in by go:embed, so the artifact either carries these exact bytes or it does
# not. Searching the binary for the note's longest line proves the RIGHT note was packaged: a
# binary still carrying the committed placeholder, or a previous build's note, does not contain it.
# The longest line is chosen because a heading or a blank is short enough to appear by accident,
# while a release note's prose is not.
probe=$(awk '{ if (length($0) > length(best)) best = $0 } END { print best }' "$note")
if [ -z "$probe" ] || ! grep -qaF -- "$probe" "$binary"; then
    printf '%s\n' "binary does not embed the release note at $note (looked for its longest line)" >&2
    exit 1
fi
printf '%s\n' "verified the release note at $note is embedded in the binary."

# Probe an older entry as well as the current note above. This catches a pipeline that generated the
# archive but failed to place it under go:embed before compilation.
history_probe=$(python3 - "$history" <<'PY'
import json
import sys

items = json.load(open(sys.argv[1], encoding="utf-8"))
older = items[-1] if len(items) > 1 else {}
print(older.get("title") or older.get("tag") or "")
PY
)
if [ -z "$history_probe" ] || ! grep -qaF -- "$history_probe" "$binary"; then
    printf '%s\n' "binary does not embed historical release notes from $history" >&2
    exit 1
fi
printf '%s\n' "verified historical release notes from $history are embedded in the binary."
