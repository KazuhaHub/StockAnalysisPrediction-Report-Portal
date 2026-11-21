#!/bin/sh
# Run one architecture of the app-runner image through its entry and check what the entry promises (doc 187
# section 12). Used by release.yml before the image's digest is recorded, and by hand against a local image.
#
#   probe-runner-image.sh IMAGE PLATFORM [WANT_VERSION]
#
# IMAGE is a reference (for the release, repo@sha256:... so the bytes that were pushed are the bytes that run).
# PLATFORM is linux/amd64 or linux/arm64 and must be this machine's. WANT_VERSION, when given, is the exact line
# `app-runner version` must print, "<tag without v> (<short sha>, <build date>)".
#
# It starts privileged containers in the host's PID and network namespaces, so run it only on a disposable
# machine. It checks that:
#   - the entry reaches the program in PID 1's mount namespace, with / as its root and umask 0077;
#   - `version` prints the stamped identity;
#   - the entry refuses without pid: host, and without network_mode: host, and starts nothing.
set -eu

[ "$#" -ge 2 ] && [ "$#" -le 3 ] || {
	printf 'usage: probe-runner-image.sh IMAGE PLATFORM [WANT_VERSION]\n' >&2
	exit 2
}
image=$1
platform=$2
want=${3:-}

fail() {
	printf 'FAIL: %s\n' "$1" >&2
	exit 1
}

# PID 1's mount namespace, read from a privileged container in the host's PID namespace with the entry bypassed:
# on a CI runner the probe runs as an unprivileged user, who cannot read /proc/1/ns/mnt directly (GNU readlink then
# fails without a word, and set -e ends the probe with no message).
host_mnt=$(docker run --rm --platform "$platform" --privileged --pid host --network none --entrypoint readlink \
	"$image" -v /proc/1/ns/mnt) || fail "could not read PID 1's mount namespace through $image"
[ -n "$host_mnt" ] || fail "PID 1's mount namespace read back empty"

# The test hook PORTAL_ENTRY_RUNNER swaps the program the entry opens on descriptor 3 for the image's shell, so
# the namespace the entry really lands in can be read from inside it. The compose file never sets it.
seen=$(docker run --rm --platform "$platform" --privileged --pid host --network host \
	-e PORTAL_ENTRY_RUNNER=/bin/sh "$image" \
	-c 'printf "%s|%s|%s" "$(readlink /proc/self/ns/mnt)" "$(readlink /proc/self/root)" "$(umask)"')
[ "$seen" = "$host_mnt|/|0077" ] || fail "the entry landed in '$seen', want '$host_mnt|/|0077' (PID 1's mount namespace, root /, umask 0077)"
printf 'ok   the entry reaches the program in PID 1'"'"'s mount namespace with root / and umask 0077\n'

got=$(docker run --rm --platform "$platform" --privileged --pid host --network host "$image" version)
printf '%s\n' "$got"
if [ -n "$want" ] && [ "$got" != "$want" ]; then
	fail "version printed '$got', want '$want'"
fi
printf 'ok   version prints the stamped identity\n'

out=$(docker run --rm --platform "$platform" --privileged --network host "$image" version 2>&1) && fail "the entry started without pid: host"
if [ -n "$want" ] && [ "$out" = "$want" ]; then
	fail "the entry ran the runner without pid: host"
fi
printf '%s\n' "$out" | grep -q 'portal-app-runner-entry:' || fail "the refusal without pid: host is not the entry's: $out"
printf 'ok   the entry refuses without pid: host\n'

out=$(docker run --rm --platform "$platform" --privileged --pid host "$image" version 2>&1) && fail "the entry started without network_mode: host"
printf '%s\n' "$out" | grep -q 'network_mode: host' || fail "the refusal without network_mode: host is not the entry's: $out"
printf 'ok   the entry refuses without network_mode: host\n'
