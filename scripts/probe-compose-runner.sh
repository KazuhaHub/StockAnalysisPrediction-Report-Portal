#!/bin/sh
# Native lifecycle probe of the compose runner (doc 187 section 11, "host-setup, new host" and "Lifecycle").
# It runs the REAL thing on a disposable Linux host: host-setup plan and --apply through `docker compose run`,
# `docker compose up -d`, the startup checks, health, restart, down, a killed runner and a simulated reboot.
#
#   probe-compose-runner.sh IMAGE [OBSERVER]
#
# IMAGE is a local app-runner image (the entry and the static binary; nothing is pulled or pushed). OBSERVER, when
# given, is the compiled test binary of ./internal/apprunner/service, which observes the managed profile as the
# controller UID the way the app-runner probe workflow does.
#
# DESTRUCTIVE: it creates loop-mounted XFS images, edits /etc/fstab, writes /etc/portal-app-runner, /srv/portal-runner
# and /var/lib/portal-app-runner, and runs a privileged container in the host namespaces. Run it only as root on a
# machine that can be thrown away (a CI worker or a test VM). It needs Docker with the compose plugin, xfsprogs,
# util-linux, jq and openssl on the host.
set -eu

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || { printf 'usage: probe-compose-runner.sh IMAGE [OBSERVER]\n' >&2; exit 2; }
[ "$(id -u)" = 0 ] || { printf 'run as root on a disposable host\n' >&2; exit 2; }
image=$1
observer=${2:-}

here=$(unset CDPATH; cd -- "$(dirname -- "$0")" && pwd)
repo=$(dirname "$here")
work=$(mktemp -d /tmp/compose-runner-probe.XXXXXXXX)
project=runner-probe
image_dir=/var/lib/portal-app-runner/volumes
uid=41001
gid=41001
control=/var/lib/portal-app-runner/control

# A copy of the compose file that never pulls (the image is local), under the tag PORTAL_VERSION selects.
sed 's/pull_policy: always/pull_policy: never/' "$repo/docker-compose.yml" >"$work/docker-compose.yml"
mkdir -p "$work/app-runner-control"
printf 'COMPOSE_PROFILES=app-runner\nPORTAL_VERSION=probe\n' >"$work/.env"
docker tag "$image" ghcr.io/kazuhahub/stockanalysisprediction-report-portal-app-runner:probe

compose() { docker compose -p "$project" --project-directory "$work" -f "$work/docker-compose.yml" "$@"; }

step() { printf '\n== %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
state() { # a digest of everything host-setup may touch
	{
		find /etc/portal-app-runner /var/lib/portal-app-runner /srv/portal-runner -exec stat -c '%n %F %a %U:%G %s' {} + 2>/dev/null || true
		sha256sum /etc/fstab
		mount | grep -c portal-runner || true
	} | sort | sha256sum
}

cleanup() {
	status=$?
	if [ -z "${PROBE_KEEP:-}" ]; then
		compose down --remove-orphans >/dev/null 2>&1 || true
		for mnt in /srv/portal-runner/data /srv/portal-runner/checkpoint; do
			umount "$mnt" 2>/dev/null || true
		done
		for img in "$image_dir/data.img" "$image_dir/checkpoint.img"; do
			for dev in $(losetup -j "$img" -O NAME -n 2>/dev/null || true); do losetup --detach "$dev" 2>/dev/null || true; done
		done
		if [ -f /etc/fstab.before-portal-app-runner ]; then cp /etc/fstab.before-portal-app-runner /etc/fstab; fi
		rm -rf /etc/portal-app-runner /var/lib/portal-app-runner /srv/portal-runner /run/portal-app-runner /etc/fstab.before-portal-app-runner
		systemctl daemon-reload 2>/dev/null || true
	fi
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT

wait_ready() { # wait for the ready line of the CURRENT container, then for `healthy`
	n=0
	until compose logs app-runner 2>&1 | grep -q 'app-runner ready'; do
		n=$((n + 1))
		[ "$n" -le 120 ] || { compose logs app-runner >&2; fail "the runner did not become ready"; }
		sleep 1
	done
	n=0
	until [ "$(docker inspect -f '{{.State.Health.Status}}' report-portal-app-runner)" = healthy ]; do
		n=$((n + 1))
		[ "$n" -le 120 ] || { docker inspect -f '{{json .State.Health}}' report-portal-app-runner >&2; fail "the runner did not become healthy"; }
		sleep 1
	done
}

pid_of_runner() { docker inspect -f '{{.State.Pid}}' report-portal-app-runner; }

observe() {
	[ -n "$observer" ] || return 0
	env -i PATH=/usr/bin:/bin GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=github-hosted GOARCH="$(dpkg --print-architecture)" \
		PORTAL_APP_RUNNER_SERVICE_ACCEPTANCE=1 PORTAL_APP_RUNNER_SERVICE_UID=$uid PORTAL_APP_RUNNER_SERVICE_GID=$gid \
		PORTAL_APP_RUNNER_SERVICE_SOCKET="$control/runner.sock" PORTAL_APP_RUNNER_SERVICE_PUBLIC_KEY="$public" \
		PORTAL_APP_RUNNER_SERVICE_DEPLOYMENT_ID="$deployment" PORTAL_APP_RUNNER_SERVICE_CONTROLLER_EPOCH="$epoch" \
		PORTAL_APP_RUNNER_SERVICE_FINGERPRINT="$fingerprint" \
		setpriv --reuid=$uid --regid=$gid --clear-groups --no-new-privs \
		"$observer" -test.v -test.timeout=30s -test.run='^TestNativeManagedServiceProfile$' | tee "$work/observer.log"
	grep -q NATIVE_MANAGED_SERVICE_PROFILE_VERIFIED "$work/observer.log" || fail "the controller UID did not observe the managed profile"
}

# ---- an enrollment document, as the Portal console would produce it -----------------------------------------
openssl genpkey -algorithm ED25519 -out "$work/key.pem"
openssl pkey -in "$work/key.pem" -pubout -outform DER | tail -c 32 >"$work/public.raw"
public=$(base64 -w0 "$work/public.raw" | tr '+/' '-_' | tr -d '=')
fingerprint=$(sha256sum "$work/public.raw" | awk '{print $1}')
deployment=10000000-0000-4000-8000-000000000001
epoch=20000000-0000-4000-8000-000000000002
jq -n --arg public "$public" --arg fingerprint "$fingerprint" --arg deployment "$deployment" --arg epoch "$epoch" \
	'{format: "portal.app-runner.bootstrap/1-dev.1", controller_public_key: $public,
	  controller: {deployment_id: $deployment, controller_epoch: $epoch, control_revision: 1, fingerprint: $fingerprint}}' \
	>"$work/enrollment.json"

HOST_SETUP="docker compose -p $project --project-directory $work -f $work/docker-compose.yml run --rm app-runner host-setup"
SIZES="--image-dir $image_dir --data-size 4G --checkpoint-size 4G --checkpoint-size-below-production --controller-uid $uid --controller-gid $gid"

step "entry: the image refuses without the host namespaces"
if docker run --rm "$image" version >"$work/refused.log" 2>&1; then fail "the entry started without privileged, pid: host and network_mode: host"; fi
grep -q 'portal-app-runner-entry:' "$work/refused.log" || fail "the refusal is not the entry's"

step "host-setup: the plan changes nothing"
before=$(state)
# shellcheck disable=SC2086
$HOST_SETUP $SIZES --enrollment "$work/enrollment.json" >"$work/plan.log" 2>&1 || { cat "$work/plan.log" >&2; fail "plan failed"; }
cat "$work/plan.log"
grep -q 'plan only: nothing was changed' "$work/plan.log" || fail "the plan does not say it changed nothing"
[ "$(state)" = "$before" ] || fail "the plan changed the host"

step "host-setup --apply on a fresh host"
# shellcheck disable=SC2086
$HOST_SETUP --apply $SIZES --enrollment "$work/enrollment.json" >"$work/apply.log" 2>&1 || { cat "$work/apply.log" >&2; fail "apply failed"; }
cat "$work/apply.log"
for line in COMPOSE_PROFILES=app-runner PORTAL_UID=$uid PORTAL_GID=$gid RP_APP_RUNNER_CONTROL_DIR=$control \
	RP_APP_RUNNER_SOCKET=$control/runner.sock RP_APP_RUNNER_CONTROLLER_UID=$uid RP_APP_RUNNER_CONTROLLER_GID=$gid; do
	grep -qx "$line" "$work/apply.log" || fail "the .env report lacks $line"
done
[ "$(stat -c '%a %u:%g' "$control")" = "750 0:$gid" ] || fail "the control root is not root:$gid 0750"
[ "$(stat -c '%a' /etc/portal-app-runner/service.json)" = 600 ] || fail "service.json is not 0600"
grep -q "$image_dir/data.img /srv/portal-runner/data xfs loop,prjquota,nodev,nosuid,noexec" /etc/fstab || fail "fstab lacks the data volume"
[ "$(findmnt -n -o FSTYPE /srv/portal-runner/data)" = xfs ] || fail "the data volume is not mounted as XFS"

step "a second --apply changes nothing"
after=$(state)
# shellcheck disable=SC2086
$HOST_SETUP --apply $SIZES >"$work/second.log" 2>&1 || { cat "$work/second.log" >&2; fail "second apply failed"; }
grep -q 'no changes' "$work/second.log" || { cat "$work/second.log" >&2; fail "the second apply does not say no changes"; }
[ "$(state)" = "$after" ] || fail "the second apply changed the host"

step "conflicts refuse and change nothing: a journal directory of another mode"
chmod 0755 /var/lib/portal-app-runner/journal
guarded=$(state)
# shellcheck disable=SC2086
if $HOST_SETUP --apply $SIZES >"$work/conflict.log" 2>&1; then fail "a 0755 journal directory was accepted"; fi
grep -q 'refused' "$work/conflict.log" || fail "the refusal is not reported"
[ "$(state)" = "$guarded" ] || fail "a refused run changed the host"
chmod 0700 /var/lib/portal-app-runner/journal

step "up -d: the startup checks pass and the runner is ready and healthy"
compose up -d app-runner
wait_ready
compose logs app-runner 2>&1 | grep 'app-runner ready' | tail -1
pid=$(pid_of_runner)
[ "$(readlink "/proc/$pid/ns/mnt")" = "$(readlink /proc/1/ns/mnt)" ] || fail "the runner is not in the host mount namespace"
[ "$(readlink "/proc/$pid/root")" = / ] || fail "the runner's root is not /"
[ "$(readlink "/proc/$pid/ns/net")" = "$(readlink /proc/1/ns/net)" ] || fail "the runner is not in the host network namespace"
[ "$(awk '/^Umask:/ {print $2}' "/proc/$pid/status")" = 0077 ] || fail "the runner's umask is not 0077"
[ "$(awk '/^Max open files/ {print $4}' "/proc/$pid/limits")" = 65536 ] || fail "the runner's open files limit is not 65536"
exe=$(readlink "/proc/$pid/exe" || true)
case "$exe" in *app-runner*) ;; *) fail "the process is not the runner: $exe" ;; esac
# The descriptor the entry opened is close-on-exec in the runner: no child can hold it (fd 3 exists, flags 02000000).
if [ -e "/proc/$pid/fdinfo/3" ]; then
	flags=$(awk '/^flags:/ {print $2}' "/proc/$pid/fdinfo/3")
	[ $((flags & 02000000)) -ne 0 ] || fail "descriptor 3 is not close-on-exec in the runner (flags $flags)"
fi
if ss -H -lntup 2>/dev/null | grep -q "pid=$pid,"; then fail "the runner owns a TCP or UDP listening socket"; fi
[ -S "$control/runner.sock" ] && [ "$(stat -c '%a %u:%g' "$control/runner.sock")" = "660 0:$gid" ] || fail "the control socket is not root:$gid 0660"
[ "$(stat -c '%a %u:%g' /run/portal-app-runner/ipc)" = "700 0:0" ] || fail "serve did not create ipc_root as root:root 0700"
observe

step "version through the entry matches the ready line"
compose run --rm app-runner version
compose logs app-runner 2>&1 | grep -q 'app-runner ready .* version=' || fail "the ready line does not carry the version"

step "restart keeps the journal and the enrollment"
compose restart app-runner
wait_ready
observe

step "a killed runner leaves a stale socket, and the restart removes it"
# No automatic restart, so the socket the killed runner left is still there to look at.
docker update --restart=no report-portal-app-runner >/dev/null
docker kill -s KILL report-portal-app-runner >/dev/null
[ -S "$control/runner.sock" ] || fail "no stale socket was left by SIGKILL"
compose up -d app-runner
wait_ready
observe

step "down removes the control socket and health fails with the runner stopped"
compose down
[ ! -e "$control/runner.sock" ] || fail "down left the control socket"
if compose run --rm app-runner health --config /etc/portal-app-runner/service.json; then fail "health passed with the runner stopped"; fi

step "a simulated reboot: unmount, detach, clear /run, then up -d with no manual step"
for mnt in /srv/portal-runner/data /srv/portal-runner/checkpoint; do umount "$mnt"; done
for img in "$image_dir/data.img" "$image_dir/checkpoint.img"; do
	for dev in $(losetup -j "$img" -O NAME -n); do losetup --detach "$dev"; done
done
rm -rf /run/portal-app-runner
# What the boot does with the fstab lines.
mount /srv/portal-runner/data
mount /srv/portal-runner/checkpoint
compose up -d app-runner
wait_ready
observe

printf '\nall compose runner probes passed\n'
