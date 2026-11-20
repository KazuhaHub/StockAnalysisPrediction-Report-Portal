#!/bin/sh
# Tests for scripts/probe-proxy-pin-listener.sh. Run directly: sh scripts/proxy_pin_probe_test.sh
#
# The probe talks to a real nginx, so here curl, openssl and ss are shims that answer as a good or a broken
# listener would. What is tested is the probe's own decisions: every check fails closed, and one broken listener
# property fails the run with that check's name.
set -u

here=$(cd "$(dirname "$0")" && pwd)
probe="$here/probe-proxy-pin-listener.sh"
work=$(mktemp -d "${TMPDIR:-/tmp}/proxy-pin-probe.XXXXXX")
trap 'rm -rf "$work"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() {
    fail=$((fail + 1))
    printf 'FAIL: %s\n' "$1"
}

NAME=quotes.internal.anchan.kazuhahub.com

cat >"$work/curl" <<'SHIM'
#!/bin/sh
target=0
host=
prev=
for arg in "$@"; do
    [ "$prev" = --request-target ] && target=1
    [ "$prev" = -H ] && host=$arg
    prev=$arg
done
if [ "$target" = 1 ]; then printf '%s' "${FAKE_ABS_CODE:-421}"; exit 0; fi
case $host in
*portal*) printf '%s' "${FAKE_HOST_CODE:-421}"; exit 0 ;;
esac
[ -n "${FAKE_LOG:-}" ] && printf '%s\n' "${FAKE_LOG_LINE:-GET / quotes.internal.anchan.kazuhahub.com}" >>"$FAKE_LOG"
printf '%s' "${FAKE_OK_CODE:-200}"
exit "${FAKE_CURL_EXIT:-0}"
SHIM
cat >"$work/openssl" <<'SHIM'
#!/bin/sh
case ${FAKE_SNI:-reject} in
reject) printf 'CONNECTED(00000003)\n40E7:error:SSL alert number 112\nno peer certificate available\n' ;;
cert) printf 'CONNECTED(00000003)\n-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n' ;;
silent) printf 'CONNECTED(00000003)\n' ;;
esac
SHIM
cat >"$work/ss" <<'SHIM'
#!/bin/sh
printf '%s' "${FAKE_SS-LISTEN 0 511 127.0.0.1:20443 0.0.0.0:*
}"
SHIM
chmod +x "$work/curl" "$work/openssl" "$work/ss"

write_include() { # variant
    cat >"$work/include.conf" <<EOF
# Dedicated listener for $NAME (loopback only).
server {
    listen 127.0.0.1:20443 ssl default_server;
    ssl_reject_handshake on;
}
server {
    listen 127.0.0.1:20443 ssl;
    server_name $NAME;
    ssl_certificate /etc/letsencrypt/live/$NAME/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/$NAME/privkey.pem;
    if (\$host != "$NAME") { return 421; }
    location / {
        proxy_pass http://127.0.0.1:8000;
        proxy_set_header Host $NAME;
    }
}
EOF
    case $1 in
    good) ;;
    http_host) sed -i "s|proxy_set_header Host $NAME;|proxy_set_header Host \$http_host;|" "$work/include.conf" ;;
    no_host) sed -i '/proxy_set_header/d' "$work/include.conf" ;;
    commented_host) sed -i "s|proxy_set_header Host $NAME;|# proxy_set_header Host $NAME;|" "$work/include.conf" ;;
    wildcard_listen) sed -i 's|listen 127.0.0.1:20443 ssl;|listen [::]:20443 ssl;|' "$work/include.conf" ;;
    bare_listen) sed -i 's|listen 127.0.0.1:20443 ssl;|listen 20443 ssl;|' "$work/include.conf" ;;
    any_listen) sed -i 's|listen 127.0.0.1:20443 ssl default_server;|listen 0.0.0.0:20443 ssl default_server;|' "$work/include.conf" ;;
    no_reject) sed -i '/ssl_reject_handshake/d' "$work/include.conf" ;;
    no_421) sed -i 's|return 421;|return 200;|' "$work/include.conf" ;;
    no_listen) sed -i '/listen/d' "$work/include.conf" ;;
    esac
}

# run VARIANT [ENV=VALUE ...] -- [probe args]: sets status and output.
status=0
output=
run() {
    variant=$1
    shift
    write_include "$variant"
    : >"$work/upstream.log"
    # The assignments before -- are exported in a subshell, so a value may hold spaces and newlines.
    output=$(
        while [ $# -gt 0 ] && [ "$1" != -- ]; do
            # shellcheck disable=SC2163 # "$1" is a NAME=value assignment, which is what export takes
            export "$1"
            shift
        done
        [ "${1:-}" = -- ] && shift
        if [ $# -eq 0 ]; then
            set -- --nginx-include "$work/include.conf" --upstream-log "$work/upstream.log"
        fi
        CURL="$work/curl" OPENSSL="$work/openssl" SS="$work/ss" FAKE_LOG="$work/upstream.log" sh "$probe" "$@" 2>&1
    )
    status=$?
}

expect_pass() { # description variant [env...]
    desc=$1
    shift
    run "$@"
    if [ "$status" -eq 0 ] && ! printf '%s\n' "$output" | grep -q '^FAIL'; then ok; else bad "$desc: expected the probe to pass (status $status): $output"; fi
}

expect_fail() { # description check-number variant [env...]
    desc=$1
    check=$2
    shift 2
    run "$@"
    if [ "$status" -eq 1 ] && printf '%s\n' "$output" | grep -q "^FAIL $check:"; then ok; else bad "$desc: expected FAIL $check with status 1, got status $status: $output"; fi
}

expect_usage() { # description args...
    desc=$1
    shift
    write_include good
    output=$(env CURL="$work/curl" OPENSSL="$work/openssl" SS="$work/ss" sh "$probe" "$@" 2>&1)
    status=$?
    if [ "$status" -eq 2 ]; then ok; else bad "$desc: expected a usage error (status 2), got $status"; fi
}

# A good listener passes every check, and the report names all seven.
expect_pass "a correct listener" good
passes=$(printf '%s\n' "$output" | grep -c '^PASS')
if [ "$passes" -eq 7 ]; then ok; else bad "expected 7 PASS lines, got $passes: $output"; fi

# Check 1: the request itself.
expect_fail "certificate does not verify" 1 good FAIL_X=1 FAKE_CURL_EXIT=60 FAKE_OK_CODE=
expect_fail "listener answers 421 to the right Host" 1 good FAKE_OK_CODE=421
expect_fail "upstream is down (502)" 1 good FAKE_OK_CODE=502
# A 404 from the upstream is fine: the listener and the certificate work.
expect_pass "an upstream 404 for the root path" good FAKE_OK_CODE=404

# Check 2: another Host header.
expect_fail "another Host is served" 2 good FAKE_HOST_CODE=200
expect_fail "another Host is refused another way" 2 good FAKE_HOST_CODE=404

# Check 3: another SNI.
expect_fail "another SNI is given a certificate" 3 good FAKE_SNI=cert
expect_fail "another SNI is not clearly rejected" 3 good FAKE_SNI=silent

# Check 4: absolute-URI request line.
expect_fail "absolute-URI request is served" 4 good FAKE_ABS_CODE=200

# Check 5: the include.
expect_fail "upstream Host is \$http_host" 5 http_host
expect_fail "upstream Host is not fixed" 5 no_host
expect_fail "the fixed Host is only a comment" 5 commented_host
expect_fail "listens on [::]" 5 wildcard_listen
expect_fail "listens on every address" 5 bare_listen
expect_fail "default server listens on 0.0.0.0" 5 any_listen
expect_fail "no ssl_reject_handshake" 5 no_reject
expect_fail "no 421 rule" 5 no_421
expect_fail "no listen on the port at all" 5 no_listen

# Check 6: the upstream log.
expect_fail "the upstream saw another name" 6 good FAKE_LOG_LINE="GET / portal.anchan.kazuhahub.com"
expect_pass "the upstream log check skipped on request" good -- --nginx-include "$work/include.conf" --no-upstream-log
case $output in *SKIPPED*) ok ;; *) bad "a skipped upstream-log check is not reported: $output" ;; esac

# Check 7: the socket table.
expect_fail "listening on every address" 7 good "FAKE_SS=LISTEN 0 511 0.0.0.0:20443 0.0.0.0:*"
expect_fail "listening on [::]" 7 good "FAKE_SS=LISTEN 0 511 [::]:20443 [::]:*"
expect_fail "two sockets" 7 good "FAKE_SS=LISTEN 0 511 127.0.0.1:20443 0.0.0.0:*
LISTEN 0 511 [::]:20443 [::]:*"
expect_fail "no socket" 7 good FAKE_SS=

# The shipped nginx example is itself a listener the probe accepts: it must never drift from the contract.
example="$here/../deploy/app-runner/proxy-pin-listener.nginx.example.conf"
output=$(env CURL="$work/curl" OPENSSL="$work/openssl" SS="$work/ss" sh "$probe" --nginx-include "$example" --no-upstream-log 2>&1)
status=$?
if [ "$status" -eq 0 ] && ! printf '%s\n' "$output" | grep -q '^FAIL'; then ok; else bad "the shipped nginx example does not pass the probe (status $status): $output"; fi

# Usage errors are not passes.
expect_usage "no include" --upstream-log "$work/upstream.log"
expect_usage "no log decision" --nginx-include "$work/include.conf"
expect_usage "both log decisions" --nginx-include "$work/include.conf" --upstream-log "$work/upstream.log" --no-upstream-log
expect_usage "bad port" --nginx-include "$work/include.conf" --no-upstream-log --port 70000
expect_usage "unknown flag" --nginx-include "$work/include.conf" --no-upstream-log --frobnicate
expect_usage "unreadable include" --nginx-include "$work/missing.conf" --no-upstream-log

printf '%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
