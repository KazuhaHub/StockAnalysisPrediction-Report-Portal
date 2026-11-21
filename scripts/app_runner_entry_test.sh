#!/bin/sh
# Tests of deploy/app-runner/portal-app-runner-entry against a fake /proc and a fake nsenter, so they need no
# privilege (doc 187 section 11, "Entry"). The real namespace switch is exercised by the compose lifecycle probe.
set -u

here=$(unset CDPATH; cd -- "$(dirname -- "$0")" && pwd)
entry="$here/../deploy/app-runner/portal-app-runner-entry"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# A fake runner and a fake nsenter that reports what the entry handed it.
printf 'runner-binary' >"$work/runner"
chmod 0755 "$work/runner"
cat >"$work/nsenter" <<'EOF'
#!/bin/sh
printf 'args:%s\n' "$*"
printf 'umask:%s\n' "$(umask)"
printf 'env:%s\n' "${PORTAL_APP_RUNNER_EXEC_FD:-unset}"
printf 'fd3:%s\n' "$(cat <&3)"
EOF
chmod 0755 "$work/nsenter"

failures=0
check() { # description, condition result (0 = pass)
	if [ "$2" -eq 0 ]; then
		printf 'ok   %s\n' "$1"
	else
		printf 'FAIL %s\n' "$1"
		failures=$((failures + 1))
	fi
}

# fake_proc DIR PID_SHARED NET_SHARED MNT_SHARED
fake_proc() {
	dir=$1
	mkdir -p "$dir/1/ns" "$dir/self/ns"
	ln -s 'pid:[1]' "$dir/1/ns/pid"
	ln -s 'net:[1]' "$dir/1/ns/net"
	ln -s 'mnt:[1]' "$dir/1/ns/mnt"
	if [ "$2" = yes ]; then ln -s 'pid:[1]' "$dir/self/ns/pid"; else ln -s 'pid:[2]' "$dir/self/ns/pid"; fi
	if [ "$3" = yes ]; then ln -s 'net:[1]' "$dir/self/ns/net"; else ln -s 'net:[2]' "$dir/self/ns/net"; fi
	if [ "$4" = yes ]; then ln -s 'mnt:[1]' "$dir/self/ns/mnt"; else ln -s 'mnt:[2]' "$dir/self/ns/mnt"; fi
}

run_entry() { # proc dir, then the arguments of the entry; sets $out and $status
	proc_dir=$1
	shift
	status=0
	out=$(PORTAL_ENTRY_PROC="$proc_dir" PORTAL_ENTRY_RUNNER="$work/runner" PORTAL_ENTRY_NSENTER="$work/nsenter" \
		sh "$entry" "$@" 2>&1) || status=$?
}

contains() { printf '%s\n' "$out" | grep -qF -- "$1"; }

# Both host namespaces shared, own mount namespace: the entry reaches nsenter.
fake_proc "$work/ok" yes yes no
run_entry "$work/ok" serve --config /etc/portal-app-runner/service.json
check "with pid: host and network_mode: host the entry reaches nsenter" "$status"
contains "args:--mount=$work/ok/1/ns/mnt -- /proc/self/fd/3 serve --config /etc/portal-app-runner/service.json"
check "nsenter joins PID 1's mount namespace and runs the runner from descriptor 3 with the arguments" "$?"
contains "umask:0077"
check "the umask is 0077 before the runner starts" "$?"
contains "env:3"
check "PORTAL_APP_RUNNER_EXEC_FD names descriptor 3" "$?"
contains "fd3:runner-binary"
check "descriptor 3 is the runner binary, opened before the switch" "$?"

# Each missing condition refuses before nsenter runs. Fails closed.
refused() { # description, proc dir, expected message
	run_entry "$2"
	if [ "$status" -ne 0 ] && contains "$3" && ! contains "args:"; then
		check "$1" 0
	else
		check "$1" 1
		printf '%s\n' "$out"
	fi
}
fake_proc "$work/nopid" no yes no
refused "without pid: host the entry refuses" "$work/nopid" "pid: host"
fake_proc "$work/nonet" yes no no
refused "without network_mode: host the entry refuses" "$work/nonet" "network_mode: host"
fake_proc "$work/samemnt" yes yes yes
refused "already in PID 1's mount namespace the entry refuses" "$work/samemnt" "mount namespace"
mkdir -p "$work/unreadable"
refused "with PID 1's namespaces unreadable the entry refuses" "$work/unreadable" "cannot read PID 1"

# The runner binary must be there.
status=0
out=$(PORTAL_ENTRY_PROC="$work/ok" PORTAL_ENTRY_RUNNER="$work/missing" PORTAL_ENTRY_NSENTER="$work/nsenter" sh "$entry" serve 2>&1) || status=$?
[ "$status" -ne 0 ] && contains "runner binary is missing" && ! contains "args:"
check "a missing runner binary refuses" "$?"

# Arguments are passed through unchanged, including empty and spaced ones.
run_entry "$work/ok" host-setup --enrollment "/root/a b.json" ""
contains "args:--mount=$work/ok/1/ns/mnt -- /proc/self/fd/3 host-setup --enrollment /root/a b.json "
check "arguments reach the runner unchanged" "$?"

if [ "$failures" -ne 0 ]; then
	printf '%s entry test(s) failed\n' "$failures" >&2
	exit 1
fi
printf 'all entry tests passed\n'
