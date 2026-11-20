package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/publicsuffix"
)

// The reviewed forward proxy (doc 184). A network.proxy grant is a sorted list of
// host entries the backend's CONNECT tunnels may reach, on port 443 only. Parsing
// a policy grants nothing: the runner's broker and Portal's review decide.

// ProxyPolicyFormat names the canonical encoding of a reviewed proxy policy.
const ProxyPolicyFormat = "portal.proxy-policy/1-dev.1"

// ProxyCapability is the capability name of the grant.
const ProxyCapability = "network.proxy"

// Platform-fixed limits of the proxy (doc 184 section 6). They are carried in the
// signed session command and compared exactly, so a change is a format revision.
const (
	MaxProxyHosts = 64
	// MaxProxyPolicyBytes bounds the canonical policy: 64 entries of at most
	// 255 bytes, the quotes and commas, and the fixed members.
	MaxProxyPolicyBytes = 20480
	// ProxyDailyBytesMax is the daily budget per installation, both directions.
	// A review may lower it, never raise it.
	ProxyDailyBytesMax int64 = 2 << 30
	// ProxyDailyBytesMin keeps a reviewed budget from being a typo for zero.
	ProxyDailyBytesMin int64 = 1 << 20

	ProxyTunnelsPerInstallation = 16
	ProxyNewPerMinute           = 300
	ProxyNewBurst               = 50
	ProxyIdleSeconds            = 60
	ProxyLifetimeSeconds        = 600
	ProxyTunnelsPerRunner       = 64
	ProxyConnectTimeoutSeconds  = 10
	ProxyFirstRecordSeconds     = 10
	ProxyMbitPerSecond          = 50
	// ProxyPort is the only port a tunnel may reach.
	ProxyPort = 443
)

// ErrInvalidProxyPolicy is every refusal of a host entry or a policy.
var ErrInvalidProxyPolicy = errors.New("invalid proxy policy")

// ProxyPolicy is the reviewed content of the network.proxy grant row.
type ProxyPolicy struct {
	Hosts      []string
	DailyBytes int64
}

type proxyPolicyWire struct {
	Format     string   `json:"format"`
	Hosts      []string `json:"hosts"`
	DailyBytes int64    `json:"daily_bytes"`
}

// ParsedProxyPolicy is a policy with the SHA-256 of its canonical bytes, the value
// a session command carries.
type ParsedProxyPolicy struct {
	Policy ProxyPolicy
	SHA256 string
	Raw    []byte
}

// ValidProxyHostEntry reports whether entry is an exact host or a one-label
// wildcard (`*.base`) whose base is a registrable domain or deeper.
func ValidProxyHostEntry(entry string) bool {
	if base, wildcard := strings.CutPrefix(entry, "*."); wildcard {
		if !validProxyHostName(base) {
			return false
		}
		// EffectiveTLDPlusOne fails for a name that is itself a public suffix:
		// *.com, *.com.cn and *.github.io are all refused here.
		registrable, err := publicsuffix.EffectiveTLDPlusOne(base)
		return err == nil && registrable != ""
	}
	return validProxyHostName(entry)
}

// validProxyHostName is a lower-case ASCII (or punycode) DNS name of at least two
// labels with no trailing dot, whose last label is not numeric, so no IP literal
// and no single-label name passes.
func validProxyHostName(name string) bool {
	if len(name) < 3 || len(name) > 253 || !utf8.ValidString(name) {
		return false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
				return false
			}
		}
	}
	last := labels[len(labels)-1]
	if last[0] >= '0' && last[0] <= '9' {
		return false
	}
	_, err := netip.ParseAddr(name)
	return err != nil
}

// ValidProxyHosts checks a host list: at most MaxProxyHosts valid entries, sorted
// byte-wise and unique.
func ValidProxyHosts(hosts []string) bool {
	if len(hosts) > MaxProxyHosts {
		return false
	}
	for i, entry := range hosts {
		if !ValidProxyHostEntry(entry) || i > 0 && hosts[i-1] >= entry {
			return false
		}
	}
	return true
}

// NormalizeProxyPolicy validates a policy and returns a copy.
func NormalizeProxyPolicy(p ProxyPolicy) (ProxyPolicy, error) {
	if len(p.Hosts) < 1 || !ValidProxyHosts(p.Hosts) || p.DailyBytes < ProxyDailyBytesMin || p.DailyBytes > ProxyDailyBytesMax {
		return ProxyPolicy{}, ErrInvalidProxyPolicy
	}
	return ProxyPolicy{Hosts: slices.Clone(p.Hosts), DailyBytes: p.DailyBytes}, nil
}

// CanonicalProxyPolicy is the one encoding of a valid policy.
func CanonicalProxyPolicy(p ProxyPolicy) ([]byte, error) {
	p, err := NormalizeProxyPolicy(p)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(proxyPolicyWire{Format: ProxyPolicyFormat, Hosts: p.Hosts, DailyBytes: p.DailyBytes})
	if err != nil || len(raw) > MaxProxyPolicyBytes {
		return nil, ErrInvalidProxyPolicy
	}
	return raw, nil
}

// ParseProxyPolicy accepts only the exact canonical encoding of a valid policy.
func ParseProxyPolicy(raw []byte) (ParsedProxyPolicy, error) {
	var zero ParsedProxyPolicy
	if len(raw) == 0 || len(raw) > MaxProxyPolicyBytes || !utf8.Valid(raw) {
		return zero, ErrInvalidProxyPolicy
	}
	var wire proxyPolicyWire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil || wire.Format != ProxyPolicyFormat || wire.Hosts == nil {
		return zero, ErrInvalidProxyPolicy
	}
	canonical, err := CanonicalProxyPolicy(ProxyPolicy{Hosts: wire.Hosts, DailyBytes: wire.DailyBytes})
	if err != nil || !bytes.Equal(canonical, raw) {
		return zero, ErrInvalidProxyPolicy
	}
	sum := sha256.Sum256(raw)
	return ParsedProxyPolicy{Policy: ProxyPolicy{Hosts: slices.Clone(wire.Hosts), DailyBytes: wire.DailyBytes}, SHA256: hex.EncodeToString(sum[:]), Raw: bytes.Clone(raw)}, nil
}

// Match reports whether a CONNECT host is covered by an entry of the policy. The
// host is compared as written, in lower case: an IP literal, a trailing dot, an
// upper-case or non-ASCII name matches nothing. A wildcard matches exactly one
// label in front of its base.
func (p ProxyPolicy) Match(host string) bool {
	if !validProxyHostName(host) {
		return false
	}
	for _, entry := range p.Hosts {
		if base, wildcard := strings.CutPrefix(entry, "*."); wildcard {
			label, rest, ok := strings.Cut(host, ".")
			if ok && label != "" && rest == base {
				return true
			}
			continue
		}
		if entry == host {
			return true
		}
	}
	return false
}

// EmptyProxyPolicyJSON is the policy of a grant row that is not granted.
const EmptyProxyPolicyJSON = "{}"

// ValidProxyPolicyShape reports whether a stored policy of a granted row parses.
func ValidProxyPolicyShape(stored string) bool {
	_, err := ParseProxyPolicy([]byte(stored))
	return err == nil
}
