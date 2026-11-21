#!/bin/sh
# Acceptance probe for the dedicated listener behind a pinned proxy destination (doc 186 section 14, P4).
#
# Run it on the host, as the operator, after the nginx include for the pinned name is in place and again after
# ANY panel edit of that site (aaPanel regenerates its vhost files). It changes nothing: it sends a handful of
# requests to the loopback listener, reads the nginx include and the socket table, and fails closed. Every check
# below fails the deploy if it does not hold; there is no "warning" level.
#
#   1. a request with certificate verification on succeeds (any status below 500 other than 421);
#   2. the same request with another Host header answers 421;
#   3. a TLS handshake with another SNI is rejected and no certificate is sent;
#   4. inside TLS with the right SNI, an absolute-URI request line for another site answers 421;
#   5. the include sets `proxy_set_header Host <name>` (never $http_host) and its only listen on the port is the
#      loopback address, with the default_server block rejecting handshakes and the 421 rule in place;
#   6. the upstream's log shows the fixed name for the request of check 1;
#   7. exactly one socket listens on the port, bound to the loopback address.
#
# Usage:
#   probe-proxy-pin-listener.sh --nginx-include FILE (--upstream-log FILE | --no-upstream-log)
#                               [--name HOST] [--other-host HOST] [--address ADDR] [--port PORT] [--path PATH]
#
# Exit status: 0 every check passed (a skipped upstream-log check is reported loudly), 1 a check failed, 2 usage.
#
# Tools are taken from CURL, OPENSSL and SS (default: curl, openssl, ss), which is how the test of this script
# stands in for them. Requires curl >= 7.55 (--request-target).
set -u

NAME=quotes.internal.anchan.kazuhahub.com
OTHER_HOST=portal.anchan.kazuhahub.com
OTHER_SNI=other.example
ADDRESS=127.0.0.1
PORT=20443
REQUEST_PATH=/
INCLUDE=
UPSTREAM_LOG=
NO_UPSTREAM_LOG=0
CURL=${CURL:-curl}
OPENSSL=${OPENSSL:-openssl}
SS=${SS:-ss}

usage() {
    sed -n '/^# Usage:/,/^# Exit status/p' "$0" | sed 's/^# \{0,1\}//' >&2
    exit 2
}

while [ $# -gt 0 ]; do
    case $1 in
    --name) [ $# -ge 2 ] || usage; NAME=$2; shift 2 ;;
    --other-host) [ $# -ge 2 ] || usage; OTHER_HOST=$2; shift 2 ;;
    --address) [ $# -ge 2 ] || usage; ADDRESS=$2; shift 2 ;;
    --port) [ $# -ge 2 ] || usage; PORT=$2; shift 2 ;;
    --path) [ $# -ge 2 ] || usage; REQUEST_PATH=$2; shift 2 ;;
    --nginx-include) [ $# -ge 2 ] || usage; INCLUDE=$2; shift 2 ;;
    --upstream-log) [ $# -ge 2 ] || usage; UPSTREAM_LOG=$2; shift 2 ;;
    --no-upstream-log) NO_UPSTREAM_LOG=1; shift ;;
    *) usage ;;
    esac
done

[ -n "$INCLUDE" ] || usage
if [ -z "$UPSTREAM_LOG" ] && [ "$NO_UPSTREAM_LOG" -ne 1 ]; then usage; fi
if [ -n "$UPSTREAM_LOG" ] && [ "$NO_UPSTREAM_LOG" -eq 1 ]; then usage; fi
case $PORT in '' | *[!0-9]*) usage ;; esac
[ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] || usage
if [ ! -r "$INCLUDE" ]; then
    printf 'cannot read the nginx include %s\n' "$INCLUDE" >&2
    exit 2
fi

failures=0
n=0
pass() {
    printf 'PASS %d: %s\n' "$n" "$1"
}
fail() {
    failures=$((failures + 1))
    printf 'FAIL %d: %s\n' "$n" "$1"
}
next() { n=$((n + 1)); }

URL="https://$NAME:$PORT$REQUEST_PATH"
RESOLVE="$NAME:$PORT:$ADDRESS"

# status_of prints the HTTP status of a request, or nothing when curl itself failed (a TLS or connection
# failure, including a certificate that does not verify: curl is never told to be insecure).
status_of() {
    "$CURL" --silent --show-error --max-time 15 --http1.1 --output /dev/null --write-out '%{http_code}' "$@" 2>/dev/null
}

# Check 6 needs where the upstream log ends before the request of check 1.
log_lines=0
if [ -n "$UPSTREAM_LOG" ]; then
    log_lines=$(wc -l <"$UPSTREAM_LOG" 2>/dev/null || echo 0)
fi

next
code=$(status_of --resolve "$RESOLVE" "$URL") && curl_ok=1 || curl_ok=0
case $code in
'' | 000 | 421 | 5??) fail "the listener did not serve $URL with certificate verification on (curl ok: $curl_ok, status: ${code:-none})" ;;
*) if [ "$curl_ok" -eq 1 ]; then pass "$URL answers $code with certificate verification on"; else fail "curl failed for $URL"; fi ;;
esac

next
code=$(status_of --resolve "$RESOLVE" -H "Host: $OTHER_HOST" "$URL") || code=
if [ "$code" = 421 ]; then
    pass "Host: $OTHER_HOST answers 421"
else
    fail "Host: $OTHER_HOST must answer 421, got ${code:-no answer}"
fi

next
handshake=$("$OPENSSL" s_client -connect "$ADDRESS:$PORT" -servername "$OTHER_SNI" </dev/null 2>&1) || true
if printf '%s\n' "$handshake" | grep -q 'BEGIN CERTIFICATE'; then
    fail "a handshake with SNI $OTHER_SNI was given a certificate"
elif printf '%s\n' "$handshake" | grep -Eqi 'no peer certificate available|alert|handshake failure|unrecognized name'; then
    pass "a handshake with SNI $OTHER_SNI is rejected and no certificate is sent"
else
    fail "a handshake with SNI $OTHER_SNI was not clearly rejected (no alert, no 'no peer certificate' in the output)"
fi

next
code=$(status_of --resolve "$RESOLVE" --request-target "https://$OTHER_HOST/" -H "Host: $NAME" "$URL") || code=
if [ "$code" = 421 ]; then
    pass "an absolute-URI request line for $OTHER_HOST answers 421"
else
    fail "GET https://$OTHER_HOST/ inside TLS with SNI $NAME must answer 421, got ${code:-no answer}"
fi

next
include_ok=1
active=$(grep -v '^[[:space:]]*#' "$INCLUDE")
if ! printf '%s\n' "$active" | grep -Eq "^[[:space:]]*proxy_set_header[[:space:]]+Host[[:space:]]+$(printf '%s' "$NAME" | sed 's/\./\\./g')[[:space:]]*;"; then
    fail "the include does not set 'proxy_set_header Host $NAME;'"
    include_ok=0
fi
# shellcheck disable=SC2016 # the text searched for and reported is literally $http_host
if printf '%s\n' "$active" | grep -Eq '\$http_host'; then
    fail 'the include uses $http_host'
    include_ok=0
fi
if ! printf '%s\n' "$active" | grep -Eq '^[[:space:]]*ssl_reject_handshake[[:space:]]+on[[:space:]]*;'; then
    fail 'the include has no default_server block with ssl_reject_handshake on'
    include_ok=0
fi
if ! printf '%s\n' "$active" | grep -Eq 'return[[:space:]]+421'; then
    fail 'the include has no 421 rule for another Host'
    include_ok=0
fi
listens=$(printf '%s\n' "$active" | grep -E "^[[:space:]]*listen[[:space:]]" | grep -E "(^|[^0-9])$PORT([^0-9]|\$)" || true)
if [ -z "$listens" ]; then
    fail "the include has no listen on port $PORT"
    include_ok=0
else
    bad=$(printf '%s\n' "$listens" | grep -Ev "^[[:space:]]*listen[[:space:]]+$(printf '%s' "$ADDRESS" | sed 's/\./\\./g'):$PORT([[:space:];]|\$)" || true)
    if [ -n "$bad" ]; then
        fail "the include listens on $PORT somewhere other than $ADDRESS: $(printf '%s' "$bad" | tr '\n' ' ')"
        include_ok=0
    fi
fi
if [ "$include_ok" -eq 1 ]; then
    pass "the include fixes the upstream Host, listens on $ADDRESS:$PORT only, rejects other SNI and answers 421"
fi

next
if [ -n "$UPSTREAM_LOG" ]; then
    sleep 1
    if tail -n +"$((log_lines + 1))" "$UPSTREAM_LOG" 2>/dev/null | grep -Fq "$NAME"; then
        pass "the upstream log shows $NAME for the request of check 1"
    else
        fail "the upstream log shows no $NAME since check 1 ($UPSTREAM_LOG)"
    fi
else
    printf 'SKIPPED %d: upstream log not checked (--no-upstream-log); check by hand that the frp upstream saw Host: %s\n' "$n" "$NAME"
fi

next
sockets=$("$SS" -ltnH "sport = :$PORT" 2>/dev/null) || sockets=
count=$(printf '%s\n' "$sockets" | grep -c . || true)
local_addrs=$(printf '%s\n' "$sockets" | awk 'NF >= 4 { print $4 }')
if [ "$count" -eq 1 ] && [ "$local_addrs" = "$ADDRESS:$PORT" ]; then
    pass "exactly one socket listens on port $PORT, $ADDRESS:$PORT"
else
    fail "port $PORT must have exactly one listening socket, $ADDRESS:$PORT; found $count: $(printf '%s' "$local_addrs" | tr '\n' ' ')"
fi

if [ "$failures" -ne 0 ]; then
    printf '%d failure(s): do not add proxy_pins for %s.\n' "$failures" "$NAME" >&2
    exit 1
fi
if [ "$NO_UPSTREAM_LOG" -eq 1 ]; then
    printf 'All checks passed except the upstream log, which was skipped on request.\n'
else
    printf 'All checks passed.\n'
fi
exit 0
