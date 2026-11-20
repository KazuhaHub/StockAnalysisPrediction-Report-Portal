package appplatform

import "testing"

// An allowlist only ever holds non-public prefixes, so a private target naming a
// public IP literal could never be served; it is refused when parsed.
func TestPrivateTargetRefusesPublicIPLiterals(t *testing.T) {
	for _, origin := range []string{"https://8.8.8.8", "https://93.184.216.34:8443", "https://[2606:4700::1]", "http://1.1.1.1", "https://172.32.0.1", "https://100.128.0.1", "https://[fe00::1]", "https://100.100.100.200", "https://[fd20:ce::254]"} {
		if _, err := CanonicalHTTPTarget(priv("a", origin)); err == nil {
			t.Errorf("%s accepted", origin)
		}
	}
	for _, origin := range []string{"https://10.0.0.1", "https://172.16.0.1", "https://172.31.255.254", "https://192.168.1.1", "https://100.64.0.1", "https://100.127.255.254", "https://[fd00::1]", "https://[fc00::1]"} {
		if _, err := CanonicalHTTPTarget(priv("a", origin)); err != nil {
			t.Errorf("%s: %v", origin, err)
		}
	}
}
