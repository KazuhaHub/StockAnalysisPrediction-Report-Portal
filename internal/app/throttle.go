package app

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// loginThrottle rate-limits failed logins per IP, per account, and per address+account pair, in
// memory (single binary). It blunts online password brute-force and the bcrypt-per-request
// CPU-exhaustion vector: after loginFailMax failures against a key within loginFailWindow, that key is
// refused until the window rolls over.
//
// On top of that window sits an optional LOCKOUT (security_policy.go): when the operator has turned it
// on, the key that reaches the ceiling stays refused for a duration of its own, which may outlive the
// window it was taken in. Two properties are deliberate and are tested as such:
//
//   - the caller checks the password BEFORE consulting the per-account key, so within the ceiling a
//     correct password always succeeds and clears the counters. A lockout at scope `account` breaks
//     that on purpose — it refuses a correct password for its duration — which is why it is opt-in.
//   - the IP key is checked BEFORE bcrypt and stays that way whatever the scope is: it is what prices
//     the CPU, and a lockout replaces it with nothing.
type loginThrottle struct {
	mu   sync.Mutex
	recs map[string]*failRec
	// limits, when set, is where the ceiling and the window come from — the admin's setting rather
	// than the constants below (limits.go). It is a function, not two numbers, so a change on the
	// settings page applies to the next attempt instead of at the next restart. Left nil the
	// constants stand, which is what the throttle did before it was configurable.
	limits func() (int, time.Duration)
	// lockout, when set, reports whether a key that reaches the ceiling is locked, and for how long.
	// Nil means no lockout, which is what every deployment had before the setting existed.
	lockout func() (bool, time.Duration)
}

// ceiling resolves the failure ceiling and the window in force right now.
func (l *loginThrottle) ceiling() (int, time.Duration) {
	if l.limits != nil {
		return l.limits()
	}
	return loginFailMax, loginFailWindow
}

// lockFor resolves the lockout in force right now: whether to lock, and for how long.
func (l *loginThrottle) lockFor() (bool, time.Duration) {
	if l.lockout == nil {
		return false, 0
	}
	return l.lockout()
}

type failRec struct {
	n       int
	resetAt time.Time
	// lockedUntil is when a lockout taken at the ceiling expires; the zero value means no lock. It is
	// deliberately independent of resetAt: a lock may outlive the window it was taken in, which is the
	// difference between this and the window counter.
	lockedUntil time.Time
}

// The shipped ceiling, still the fallback whenever no setting is wired in (tests, and any
// throttle constructed without a Server behind it).
const (
	loginFailWindow = defLoginFailWindow
	loginFailMax    = defLoginFailMax
)

func newLoginThrottle() *loginThrottle { return &loginThrottle{recs: map[string]*failRec{}} }

// blocked reports whether key is refused right now: either it has reached the failure ceiling within
// the current window, or it is serving a lockout.
func (l *loginThrottle) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.recs[key]
	if r == nil {
		return false
	}
	if now.Before(r.lockedUntil) {
		return true
	}
	max, _ := l.ceiling()
	return now.Before(r.resetAt) && r.n >= max
}

// record counts one failed attempt against key, (re)starting the window if it had lapsed, and takes a
// lockout when the attempt reaches the ceiling. It also opportunistically prunes lapsed entries so the
// map can't grow unbounded with distinct attacker IPs.
func (l *loginThrottle) record(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	max, window := l.ceiling()
	r := l.recs[key]
	if r == nil || (now.After(r.resetAt) && !now.Before(r.lockedUntil)) {
		r = &failRec{resetAt: now.Add(window)}
		l.recs[key] = r
	}
	// Counted here rather than in the branches above, so a first failure — the one that can reach a
	// ceiling of 1 — takes its lockout like any other.
	r.n++
	if lock, dur := l.lockFor(); lock && r.n >= max && !now.Before(r.lockedUntil) {
		r.lockedUntil = now.Add(dur)
	}
}

// prune bounds the map. The bound must never take a lock with it: a lock is a decision the operator
// asked for, and a flood of distinct sources — exactly when an attacker would want it gone — is the
// case that used to drop the whole table.
func (l *loginThrottle) prune(now time.Time) {
	const maxEntries = 4096
	if len(l.recs) <= maxEntries {
		return
	}
	for k, r := range l.recs {
		if now.After(r.resetAt) && !now.Before(r.lockedUntil) {
			delete(l.recs, k)
		}
	}
	if len(l.recs) <= maxEntries {
		return
	}
	// A burst of distinct live keys (nothing lapsed, nothing unlocked): keep the locked ones and drop
	// the rest. Losing partial window counters under a >4096-source flood is the lesser evil next to
	// running an O(n) scan under the lock on every subsequent insert — and the locks, which are the
	// part that is a policy rather than a counter, are exactly what is kept.
	kept := make(map[string]*failRec)
	for k, r := range l.recs {
		if now.Before(r.lockedUntil) {
			kept[k] = r
		}
	}
	l.recs = kept
}

// fails reports how many failures a key has accumulated inside the current window, which is what
// the captcha's after_failures trigger reads. A lapsed window counts as zero: the point is
// "recently suspicious", not "ever suspicious".
func (l *loginThrottle) fails(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.recs[key]
	if r == nil || time.Now().After(r.resetAt) {
		return 0
	}
	return r.n
}

// reset clears a key after a successful login.
func (l *loginThrottle) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.recs, key)
}

// parseTrustedProxies turns the configured list into the networks whose X-Forwarded-For the portal
// will believe.
//
// UNSET MEANS LOOPBACK, not "nobody". A portal behind nginx or Caddy on the same host is the
// ordinary deployment, and trusting nobody there records the proxy's address for every visitor
// while looking like it works — which is exactly the failure this default exists to prevent. An
// attacker cannot use it without already being on the host.
//
// Two tokens, neither combinable with a list, because a list plus a token is a contradiction and
// silently picking one reading would hand an operator a policy they did not write:
//
//	all / *  trust every upstream. Correct when the listener cannot be reached except through the
//	         proxy (a Docker network, a unix socket) and a hole anywhere else, so it is opt-in and
//	         the server logs a warning at boot.
//	none     trust nobody, not even loopback.
func parseTrustedProxies(entries []string) ([]*net.IPNet, error) {
	// Tokens first: they describe the whole policy, so they cannot share it.
	var tokens, nets []string
	for _, raw := range entries {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "":
		case "all", "*", "none":
			tokens = append(tokens, strings.ToLower(strings.TrimSpace(raw)))
		default:
			nets = append(nets, raw)
		}
	}
	if len(tokens) > 0 {
		if len(tokens) > 1 || len(nets) > 0 {
			return nil, fmt.Errorf("trusted_proxies: %q cannot be combined with anything else", tokens[0])
		}
		if tokens[0] == "none" {
			return nil, nil
		}
		_, v4, _ := net.ParseCIDR("0.0.0.0/0")
		_, v6, _ := net.ParseCIDR("::/0")
		return []*net.IPNet{v4, v6}, nil
	}
	if len(nets) == 0 {
		_, v4, _ := net.ParseCIDR("127.0.0.0/8")
		_, v6, _ := net.ParseCIDR("::1/128")
		return []*net.IPNet{v4, v6}, nil
	}

	out := make([]*net.IPNet, 0, len(nets))
	for _, raw := range nets {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if ip := net.ParseIP(raw); ip != nil {
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, block, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid IP/CIDR %q", raw)
		}
		out = append(out, block)
	}
	return out, nil
}

// trustsEverything reports the "all" policy, so the server can say so at boot. A configuration that
// believes any caller's claimed address is safe only behind an unreachable listener, and an
// operator who set it for a Docker network and later exposed the port should be reminded.
func trustsEverything(nets []*net.IPNet) bool {
	var v4, v6 bool
	for _, n := range nets {
		ones, bits := n.Mask.Size()
		if ones == 0 && bits == 32 {
			v4 = true
		}
		if ones == 0 && bits == 128 {
			v6 = true
		}
	}
	return v4 && v6
}

func ipTrusted(ip net.IP, trusted []*net.IPNet) bool {
	for _, block := range trusted {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP trusts X-Forwarded-For only when the immediate peer is explicitly configured as a
// trusted proxy. Walking the chain from right to left prevents a client-supplied leftmost value
// from bypassing the limiter when the trusted proxy appends the real address.
func clientIP(r *http.Request, trusted []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !ipTrusted(peer, trusted) {
		return host
	}
	current := peer
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(chain) - 1; i >= 0 && ipTrusted(current, trusted); i-- {
		next := net.ParseIP(strings.TrimSpace(chain[i]))
		if next == nil {
			// A hop that does not parse ends the chain of custody: nothing to its
			// left has been vouched for by a trusted proxy, so stop here rather
			// than stepping over it. Skipping instead would let anyone who can
			// reach the portal from inside trusted_proxies -- a neighbouring
			// container when the range is a whole Docker network, say -- park an
			// unparseable hop in front of an address of their choosing and have
			// it adopted as the client.
			break
		}
		current = next
	}
	return current.String()
}
