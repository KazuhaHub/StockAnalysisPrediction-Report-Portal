#!/usr/bin/env bash
# Scan github.com/crewjam/saml as released, not as patched.
#
# go.mod replaces crewjam/saml with third_party/crewjam-saml, an unversioned directory that no
# advisory can ever match, so the main govulncheck gate is blind to crewjam from then on. This
# script re-runs govulncheck against a temporary copy of go.mod without that replace and fails when:
#
#   - any advisory other than the one the patch carries reaches our code through crewjam/saml, or
#   - the carried advisory now lists a fixed version, meaning the copy should be removed in favour
#     of the release (see third_party/crewjam-saml/PATCHED.md).
set -euo pipefail

carried="GO-2026-6909"
module="github.com/crewjam/saml"
govulncheck="golang.org/x/vuln/cmd/govulncheck@v1.8.0"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cp go.mod "$tmp/released.mod"
cp go.sum "$tmp/released.sum"
go mod edit -dropreplace="$module" "$tmp/released.mod"
export GOFLAGS="-modfile=$tmp/released.mod -mod=mod"
go mod download "$module"

# -format json exits 0 whatever it finds; the verdict is computed below.
go run "$govulncheck" -format json ./... >"$tmp/scan.json"

reached="$(jq -r --arg m "$module" \
	'select(.finding) | select(.finding.trace[0].module == $m and .finding.trace[0].function != null) | .finding.osv' \
	"$tmp/scan.json" | sort -u)"
fixed="$(jq -r --arg id "$carried" \
	'select(.osv.id == $id) | .osv.affected[].ranges[]?.events[]? | .fixed // empty' \
	"$tmp/scan.json" | sort -u)"

status=0
# While the carried advisory is unfixed it must reach our code through the released module. If it
# does not, the scan did not see crewjam as released (the modfile override stopped applying, or the
# JSON shape changed), and a silent pass would hide every other advisory too.
if [ -z "$fixed" ] && ! grep -qx "$carried" <<<"$reached"; then
	echo "::error::the scan did not report $carried through $module as released; the scan itself is broken"
	status=1
fi
for id in $reached; do
	if [ "$id" != "$carried" ]; then
		echo "::error::$id reaches our code through $module as released; the patched copy does not carry a fix for it"
		status=1
	fi
done
if [ -n "$fixed" ]; then
	echo "::error::$carried is fixed in $module $fixed; drop third_party/crewjam-saml and the replace in go.mod"
	status=1
fi
if [ "$status" -eq 0 ]; then
	echo "$module as released: only $carried reaches our code, and it is still unfixed upstream"
fi
exit "$status"
