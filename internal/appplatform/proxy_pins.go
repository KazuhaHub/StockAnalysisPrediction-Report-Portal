package appplatform

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Pinned destinations of the reviewed forward proxy (doc 186). A pin maps one exact host name, which the
// application always reaches on port 443, to one address and port the broker dials without resolving the
// name. A Portal administrator chooses pins in the proxy review; the host operator lists the destinations a
// pin may name in the runner's root-owned configuration (proxy_pins). A pin is granted only when both agree
// on the exact host, address and port.

// ProxyPolicyFormatPins is the canonical encoding of a policy that holds at least one pin. A policy without
// pins keeps ProxyPolicyFormat and its exact encoding.
const ProxyPolicyFormatPins = "portal.proxy-policy/1-dev.2"

const (
	// MaxProxyPins bounds the pins of one installation; they count within MaxProxyHosts.
	MaxProxyPins = 8
	// MaxProxyOperatorPins bounds the operator's allowlist.
	MaxProxyOperatorPins = 16
)

// ProxyPin is one pinned destination: the exact name the application connects to, and where the broker dials.
type ProxyPin struct {
	Host    string `json:"host"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// Label is how a pin is written in audit rows and the console: "host → address:port".
func (p ProxyPin) Label() string { return p.Host + " → " + p.Endpoint() }

// Endpoint is "address:port", with an IPv6 address in brackets.
func (p ProxyPin) Endpoint() string {
	if strings.Contains(p.Address, ":") {
		return "[" + p.Address + "]:" + strconv.Itoa(p.Port)
	}
	return p.Address + ":" + strconv.Itoa(p.Port)
}

// PinAddr is the pin's address as a netip.Addr, and whether it is one.
func (p ProxyPin) PinAddr() (netip.Addr, bool) {
	a, err := netip.ParseAddr(p.Address)
	return a, err == nil
}

var pinDeniedPrefixes = func() []netip.Prefix {
	raw := []string{
		// IPv4: this host and link-local (cloud metadata), multicast, reserved, and the IETF protocol
		// assignments (Oracle's metadata is 192.0.0.192).
		"0.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4", "192.0.0.0/24",
		// IPv6: unspecified, link-local, multicast, IPv4-mapped, the translation and tunnelling forms and the
		// metadata addresses.
		"::/128", "fe80::/10", "ff00::/8", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "2001::/32",
		"fd00:ec2::254/128", "fd20:ce::254/128",
		// Metadata of Aliyun and Azure.
		"100.100.100.200/32", "168.63.129.16/32",
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var (
	pinULA      = netip.MustParsePrefix("fc00::/7")
	pinGlobal6  = netip.MustParsePrefix("2000::/3")
	pinLoopback = netip.MustParseAddr("::1")
)

// PinDenied reports addresses a pin may never name (doc 186 section 6). An address is admitted only when it is
// positively an IPv4 unicast address, ::1, or inside fc00::/7 or 2000::/3, and lies outside the denied set. That
// set is AlwaysDenied with exactly loopback released (127.0.0.0/8 and ::1), plus 192.0.0.0/24 and the Azure
// WireServer address. Ordinary private space (RFC 1918, 100.64.0.0/10, bridges) is not denied here: each such
// destination needs an explicit entry in the operator's allowlist. An invalid, zoned or IPv4-mapped address is
// denied.
func PinDenied(a netip.Addr) bool {
	if !a.IsValid() || a.Zone() != "" || a.Is4In6() {
		return true
	}
	if a.Is4() {
		if a.IsLoopback() {
			return false
		}
	} else if a == pinLoopback {
		return false
	} else if !pinULA.Contains(a) && !pinGlobal6.Contains(a) {
		return true
	}
	if slices.ContainsFunc(pinDeniedPrefixes, func(p netip.Prefix) bool { return p.Contains(a) }) {
		return true
	}
	return AlwaysDenied(a)
}

// ValidProxyPin checks one pin by itself: an exact valid host name, a canonical address that PinDenied admits,
// and a port from 1 to 65535.
func ValidProxyPin(p ProxyPin) bool {
	if !validProxyHostName(p.Host) || p.Port < 1 || p.Port > 65535 {
		return false
	}
	a, err := netip.ParseAddr(p.Address)
	if err != nil || a.String() != p.Address || a.Zone() != "" || a.Is4In6() {
		return false
	}
	return !PinDenied(a)
}

// ValidProxyPins checks the pins of a policy: at most MaxProxyPins valid entries, sorted by host, each host once.
func ValidProxyPins(pins []ProxyPin) bool {
	if len(pins) > MaxProxyPins {
		return false
	}
	for i, p := range pins {
		if !ValidProxyPin(p) || i > 0 && pins[i-1].Host >= p.Host {
			return false
		}
	}
	return true
}

// ValidateProxyOperatorPins checks the operator's allowlist: at most MaxProxyOperatorPins valid entries, sorted
// by (host, address, port) and unique. A name may appear with more than one destination.
func ValidateProxyOperatorPins(pins []ProxyPin) error {
	if len(pins) > MaxProxyOperatorPins {
		return ErrInvalidProxyPolicy
	}
	for i, p := range pins {
		if !ValidProxyPin(p) || i > 0 && compareProxyPins(pins[i-1], p) >= 0 {
			return ErrInvalidProxyPolicy
		}
	}
	return nil
}

func compareProxyPins(a, b ProxyPin) int {
	if c := strings.Compare(a.Host, b.Host); c != 0 {
		return c
	}
	if c := strings.Compare(a.Address, b.Address); c != 0 {
		return c
	}
	return a.Port - b.Port
}

// SortProxyPins returns a sorted copy, by (host, address, port).
func SortProxyPins(pins []ProxyPin) []ProxyPin {
	out := slices.Clone(pins)
	slices.SortFunc(out, compareProxyPins)
	return out
}

// ProxyPinPermitted reports whether the operator's allowlist holds exactly this destination and the destination
// is not in the denied set. It is judged again at every dial, so a corrupt list never opens a denied address.
func ProxyPinPermitted(allowlist []ProxyPin, p ProxyPin) bool {
	a, ok := p.PinAddr()
	if !ok || PinDenied(a) {
		return false
	}
	return slices.Contains(allowlist, p)
}

// Pin returns the pinned entry for an exact CONNECT host. Wildcards never match a pin.
func (p ProxyPolicy) Pin(host string) (ProxyPin, bool) {
	for _, pin := range p.Pins {
		if pin.Host == host {
			return pin, true
		}
	}
	return ProxyPin{}, false
}
