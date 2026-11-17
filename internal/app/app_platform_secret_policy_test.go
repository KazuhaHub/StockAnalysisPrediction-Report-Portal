package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestPlatformSecretBindingPolicyCanonical(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, operation := range []string{"http_bearer", "http_api_key"} {
		policy := platformSecretBindingPolicy{operation: operation, networkPolicySHA256: digest}
		raw, err := canonicalPlatformSecretBindingPolicy(policy)
		if err != nil {
			t.Fatal(err)
		}
		expected := `{"format":"portal.secret-binding-policy/1-dev.1","operation":"` + operation + `","network_policy_sha256":"` + digest + `"}`
		if string(raw) != expected {
			t.Fatalf("unexpected canonical policy %s", raw)
		}
		parsed, err := parsePlatformSecretBindingPolicy(raw)
		if err != nil || parsed != policy {
			t.Fatal("policy roundtrip", err)
		}
		if validatePlatformHTTPSecretBindingPolicy(raw, operation, digest) != nil {
			t.Fatal("exact policy refused")
		}
		for _, pair := range [][2]string{{"raw_read", digest}, {operation, strings.Repeat("b", 64)}, {"arbitrary_header", digest}, {operation, strings.ToUpper(digest)}} {
			if validatePlatformHTTPSecretBindingPolicy(raw, pair[0], pair[1]) == nil {
				t.Fatal("operation/network identity mismatch accepted")
			}
		}
	}
	raw, err := canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: "raw_read"})
	if err != nil || string(raw) != `{"format":"portal.secret-binding-policy/1-dev.1","operation":"raw_read"}` {
		t.Fatal("raw shape", err)
	}
	if _, err := parsePlatformSecretBindingPolicy(raw); err != nil {
		t.Fatal(err)
	}
	if validatePlatformHTTPSecretBindingPolicy(raw, "raw_read", digest) == nil {
		t.Fatal("raw read callable through HTTP policy")
	}
}

func TestPlatformSecretBindingPolicyRefusals(t *testing.T) {
	raw, _ := canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: "http_bearer", networkPolicySHA256: strings.Repeat("a", 64)})
	inputs := [][]byte{nil, []byte("null"), []byte("[]"), append(raw, ' '), append([]byte(" "), raw...), append(raw, []byte(`{}`)...), bytes.Repeat([]byte("x"), 8193), {255}}
	for _, pair := range [][2]string{
		{`"operation":"http_bearer"`, `"operation":"http_bearer","operation":"http_bearer"`},
		{`"operation"`, `"Operation"`}, {`"operation"`, `"\u006fperation"`},
		{`"operation":"http_bearer"`, `"operation":"http_bearer","value":"secret"`},
		{`"operation":"http_bearer"`, `"operation":"http_bearer","header":"Authorization"`},
		{`"operation":"http_bearer"`, `"operation":"http_bearer","credential_id":"id"`},
		{`"operation":"http_bearer"`, `"operation":"http_bearer","origin":"https://example.com"`},
		{`"operation":"http_bearer"`, `"operation":"raw_read"`},
		{`"operation":"http_bearer"`, `"operation":null`},
		{`portal.secret-binding-policy/1-dev.1`, `portal.secret-binding-policy/1-dev.2`},
	} {
		inputs = append(inputs, bytes.Replace(raw, []byte(pair[0]), []byte(pair[1]), 1))
	}
	for i, input := range inputs {
		if _, err := parsePlatformSecretBindingPolicy(input); err == nil {
			t.Fatalf("ambiguous policy accepted case %d", i)
		}
	}
	for _, policy := range []platformSecretBindingPolicy{{operation: "http_bearer"}, {operation: "http_api_key", networkPolicySHA256: strings.Repeat("A", 64)}, {operation: "http_api_key", networkPolicySHA256: strings.Repeat("a", 63)}, {operation: "raw_read", networkPolicySHA256: strings.Repeat("a", 64)}, {operation: "arbitrary_header", networkPolicySHA256: strings.Repeat("a", 64)}} {
		if _, err := canonicalPlatformSecretBindingPolicy(policy); err == nil {
			t.Fatal("invalid declared policy accepted")
		}
	}
}

func FuzzPlatformSecretBindingPolicy(f *testing.F) {
	for _, operation := range []string{"http_bearer", "http_api_key", "raw_read"} {
		p := platformSecretBindingPolicy{operation: operation}
		if operation != "raw_read" {
			p.networkPolicySHA256 = strings.Repeat("a", 64)
		}
		raw, _ := canonicalPlatformSecretBindingPolicy(p)
		f.Add(raw)
	}
	f.Add([]byte(`{"operation":null}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		parsed, err := parsePlatformSecretBindingPolicy(raw)
		if err != nil {
			return
		}
		canonical, err := canonicalPlatformSecretBindingPolicy(parsed)
		if err != nil || !bytes.Equal(canonical, raw) {
			t.Fatal("accepted noncanonical policy")
		}
		if parsed.operation == "raw_read" && validatePlatformHTTPSecretBindingPolicy(raw, parsed.operation, strings.Repeat("a", 64)) == nil {
			t.Fatal("fuzz enabled raw read")
		}
	})
}
