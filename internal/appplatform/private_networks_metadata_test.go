package appplatform

import (
	"net/netip"
	"testing"
)

// Cloud metadata endpoints that sit inside ranges an allowlist may legitimately
// cover (CGNAT and ULA). AWS and Azure use link-local addresses, already denied.
var metadataEndpoints = []string{"100.100.100.200", "fd20:ce::254", "fd00:ec2::254"}

func TestMetadataEndpointsAreAlwaysDenied(t *testing.T) {
	for _, s := range metadataEndpoints {
		if !AlwaysDenied(netip.MustParseAddr(s)) {
			t.Errorf("%s not denied", s)
		}
	}
	// Neighbours stay allowed.
	for _, s := range []string{"100.100.100.199", "100.100.100.201", "fd20:ce::253", "fd20:ce::255"} {
		if AlwaysDenied(netip.MustParseAddr(s)) {
			t.Errorf("%s denied", s)
		}
	}
}

func TestMetadataEndpointsRefusedAtValidationWhenExact(t *testing.T) {
	for _, p := range []string{"100.100.100.200/32", "fd20:ce::254/128"} {
		if err := ValidatePrivateNetworks([]PrivateNetwork{pn(p, false, 80)}); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
}

func TestMetadataEndpointsDeniedByAdmitInsideAContainingPrefix(t *testing.T) {
	n := []PrivateNetwork{pn("100.100.0.0/16", true, 80), pn("fd20:ce::/48", true, 80)}
	if err := ValidatePrivateNetworks(n); err != nil {
		t.Fatalf("a wide prefix that merely contains the endpoint must validate: %v", err)
	}
	for _, s := range metadataEndpoints[:2] {
		if Admit(n, netip.MustParseAddr(s), 80, "http") {
			t.Errorf("%s admitted", s)
		}
	}
	for _, s := range []string{"100.100.0.5", "fd20:ce::1"} {
		if !Admit(n, netip.MustParseAddr(s), 80, "http") {
			t.Errorf("%s refused", s)
		}
	}
}
