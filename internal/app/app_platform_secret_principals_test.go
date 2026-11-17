package app

import (
	"strings"
	"testing"
)

func TestPlatformSecretBindingPolicyV2Principals(t *testing.T) {
	digest := strings.Repeat("a", 64)
	head := `{"format":"portal.secret-binding-policy/2-dev.1","operation":"http_bearer","network_policy_sha256":"` + digest + `","principals":`
	for _, c := range []struct {
		principals string
		raw        string
		viewer     bool
		service    bool
	}{
		{"service", head + `["service"]}`, false, true},
		{"service,viewer", head + `["service","viewer"]}`, true, true},
	} {
		p := platformSecretBindingPolicy{operation: "http_bearer", networkPolicySHA256: digest, principals: c.principals}
		raw, err := canonicalPlatformSecretBindingPolicy(p)
		if err != nil || string(raw) != c.raw {
			t.Fatalf("canonical %q: %s %v", c.principals, raw, err)
		}
		parsed, err := parsePlatformSecretBindingPolicy(raw)
		if err != nil || parsed != p {
			t.Fatal("v2 roundtrip", err)
		}
		if parsed.allows("viewer") != c.viewer || parsed.allows("service") != c.service || parsed.allows("other") {
			t.Fatal("principal membership", c.principals)
		}
		if validatePlatformHTTPSecretBindingPolicy(raw, "http_bearer", digest) != nil {
			t.Fatal("v2 policy refused for its own network digest")
		}
	}
	// A viewer-only binding keeps the v1 spelling, and v1 parses as viewer-only.
	v1, err := canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: "http_bearer", networkPolicySHA256: digest})
	if err != nil || !strings.Contains(string(v1), "/1-dev.1") || strings.Contains(string(v1), "principals") {
		t.Fatal("viewer-only policy is not v1", string(v1), err)
	}
	if p, err := parsePlatformSecretBindingPolicy(v1); err != nil || !p.allows("viewer") || p.allows("service") {
		t.Fatal("v1 is not viewer-only", err)
	}
}

func TestPlatformSecretBindingPolicyV2Refusals(t *testing.T) {
	digest := strings.Repeat("a", 64)
	head := `{"format":"portal.secret-binding-policy/2-dev.1","operation":"http_bearer","network_policy_sha256":"` + digest + `","principals":`
	for _, raw := range []string{
		head + `[]}`, head + `null}`, head + `["viewer"]}`, head + `["viewer","service"]}`,
		head + `["service","service"]}`, head + `["admin"]}`, head + `["Service"]}`, head + `"service"}`,
		head + `["service"],"principals":["service"]}`,
		strings.Replace(head, `,"principals":`, ``, 1) + `}`,
		`{"format":"portal.secret-binding-policy/2-dev.1","operation":"raw_read","principals":["service"]}`,
		`{"format":"portal.secret-binding-policy/1-dev.1","operation":"http_bearer","network_policy_sha256":"` + digest + `","principals":["service"]}`,
	} {
		if _, err := parsePlatformSecretBindingPolicy([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	for _, p := range []platformSecretBindingPolicy{
		{operation: "raw_read", principals: "service"},
		{operation: "http_bearer", networkPolicySHA256: digest, principals: "viewer"},
		{operation: "http_bearer", networkPolicySHA256: digest, principals: "viewer,service"},
		{operation: "http_bearer", networkPolicySHA256: digest, principals: "admin"},
	} {
		if _, err := canonicalPlatformSecretBindingPolicy(p); err == nil {
			t.Fatal("invalid principals accepted", p.principals)
		}
	}
}
