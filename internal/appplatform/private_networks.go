package appplatform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
)

// PrivateNetworksFormat names the canonical encoding of a runner's private
// network allowlist; its digest is what a reviewed command is bound to.
const PrivateNetworksFormat = "portal.runner.private-networks/1-dev.1"

// MaxPrivateNetworks and MaxPrivateNetworkPorts bound one allowlist.
const MaxPrivateNetworks = 16
const MaxPrivateNetworkPorts = 16

var ErrInvalidPrivateNetworks = errors.New("invalid private network allowlist")

// PrivateNetwork is one operator-chosen private prefix a reviewed private target
// may reach. It is deployment configuration, never a Portal or application field.
type PrivateNetwork struct {
	Prefix    string `json:"prefix"`
	Ports     []int  `json:"ports"`
	PlainHTTP bool   `json:"plain_http"`
}

var alwaysDeniedPrefixes = func() []netip.Prefix {
	raw := []string{
		"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "fe80::/10", "ff00::/8", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48",
		"2002::/16", "2001::/32", "fd00:ec2::254/128",
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var nonPublicPrefixes = func() []netip.Prefix {
	raw := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7"}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// Unmap returns the address a connection would actually reach: IPv4-mapped IPv6
// becomes IPv4. A zoned or invalid address yields the invalid address.
func Unmap(a netip.Addr) netip.Addr {
	if !a.IsValid() || a.Zone() != "" {
		return netip.Addr{}
	}
	return a.Unmap()
}

// AlwaysDenied reports addresses no allowlist may ever admit: this host and
// link, multicast, reserved, and every IPv6 form that embeds or translates an
// IPv4 address. Invalid and zoned addresses are denied.
func AlwaysDenied(a netip.Addr) bool {
	if !a.IsValid() || a.Zone() != "" {
		return true
	}
	for _, p := range alwaysDeniedPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func parsePrivateNetwork(n PrivateNetwork) (netip.Prefix, error) {
	var zero netip.Prefix
	p, err := netip.ParsePrefix(n.Prefix)
	if err != nil || p.String() != n.Prefix || p != p.Masked() || p.Addr().Zone() != "" || p.Addr().Is4In6() {
		return zero, ErrInvalidPrivateNetworks
	}
	if p.Addr().Is4() && (p.Bits() < 16 || p.Bits() > 32) || p.Addr().Is6() && (p.Bits() < 48 || p.Bits() > 128) {
		return zero, ErrInvalidPrivateNetworks
	}
	inside := false
	for _, c := range nonPublicPrefixes {
		if c.Addr().BitLen() == p.Addr().BitLen() && c.Bits() <= p.Bits() && c.Contains(p.Addr()) {
			inside = true
		}
	}
	if !inside {
		return zero, ErrInvalidPrivateNetworks
	}
	for _, d := range alwaysDeniedPrefixes {
		if d.Overlaps(p) {
			return zero, ErrInvalidPrivateNetworks
		}
	}
	if len(n.Ports) < 1 || len(n.Ports) > MaxPrivateNetworkPorts {
		return zero, ErrInvalidPrivateNetworks
	}
	for i, port := range n.Ports {
		if port < 1 || port > 65535 || i > 0 && port <= n.Ports[i-1] {
			return zero, ErrInvalidPrivateNetworks
		}
	}
	return p, nil
}

// ValidatePrivateNetworks checks an allowlist: at most 16 entries, canonical
// masked non-public prefixes (IPv4 /16../32, IPv6 /48../128) that do not overlap
// each other or the always-denied set, each with 1..16 sorted unique ports.
func ValidatePrivateNetworks(networks []PrivateNetwork) error {
	if len(networks) > MaxPrivateNetworks {
		return ErrInvalidPrivateNetworks
	}
	prefixes := make([]netip.Prefix, 0, len(networks))
	for _, n := range networks {
		p, err := parsePrivateNetwork(n)
		if err != nil {
			return err
		}
		for _, other := range prefixes {
			if other.Overlaps(p) {
				return ErrInvalidPrivateNetworks
			}
		}
		prefixes = append(prefixes, p)
	}
	return nil
}

type privateNetworksWire struct {
	Format  string           `json:"format"`
	Entries []PrivateNetwork `json:"entries"`
}

// CanonicalPrivateNetworks returns the digest input: entries sorted by prefix.
// The empty allowlist encodes with an empty entries array.
func CanonicalPrivateNetworks(networks []PrivateNetwork) ([]byte, error) {
	if ValidatePrivateNetworks(networks) != nil {
		return nil, ErrInvalidPrivateNetworks
	}
	entries := make([]PrivateNetwork, len(networks))
	for i, n := range networks {
		n.Ports = slices.Clone(n.Ports)
		entries[i] = n
	}
	slices.SortFunc(entries, func(a, b PrivateNetwork) int {
		pa, pb := netip.MustParsePrefix(a.Prefix), netip.MustParsePrefix(b.Prefix)
		if c := pa.Addr().Compare(pb.Addr()); c != 0 {
			return c
		}
		return pa.Bits() - pb.Bits()
	})
	raw, err := json.Marshal(privateNetworksWire{Format: PrivateNetworksFormat, Entries: entries})
	if err != nil {
		return nil, ErrInvalidPrivateNetworks
	}
	return raw, nil
}

// PrivateNetworksSHA256 is the lowercase hex SHA-256 of the canonical bytes.
func PrivateNetworksSHA256(networks []PrivateNetwork) (string, error) {
	raw, err := CanonicalPrivateNetworks(networks)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Admit reports whether the allowlist admits a connection to addr:port with the
// given scheme ("https", or "http" only where the entry sets plain_http). The
// address is unmapped first and always-denied addresses are never admitted. It
// assumes a validated allowlist; entries that do not parse admit nothing.
func Admit(networks []PrivateNetwork, addr netip.Addr, port uint16, scheme string) bool {
	addr = Unmap(addr)
	if AlwaysDenied(addr) || port == 0 || scheme != "https" && scheme != "http" {
		return false
	}
	for _, n := range networks {
		p, err := netip.ParsePrefix(n.Prefix)
		if err != nil || !p.Contains(addr) {
			continue
		}
		return slices.Contains(n.Ports, int(port)) && (scheme == "https" || n.PlainHTTP)
	}
	return false
}
