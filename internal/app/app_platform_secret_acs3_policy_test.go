package app

import (
	"fmt"
	"strings"
	"testing"
)

const acs3TestDigest = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

func acs3TestPolicy(principals string, actions ...string) platformSecretBindingPolicy {
	return platformSecretBindingPolicy{operation: "acs3_hmac_sha256", networkPolicySHA256: acs3TestDigest, principals: principals, acs3: strings.Join(actions, ",")}
}

func TestPlatformSecretBindingPolicyACS3Canonical(t *testing.T) {
	t.Parallel()
	policy := acs3TestPolicy(platformPrincipalsBoth, "QueryAccountBalance@2017-12-14")
	raw, err := canonicalPlatformSecretBindingPolicy(policy)
	want := `{"format":"portal.secret-binding-policy/2-dev.1","operation":"acs3_hmac_sha256","network_policy_sha256":"` + acs3TestDigest + `","principals":["service","viewer"],"acs3":{"actions":[{"action":"QueryAccountBalance","version":"2017-12-14"}]}}`
	if err != nil || string(raw) != want {
		t.Fatalf("canonical policy\n got %s\nwant %s (%v)", raw, want, err)
	}
	parsed, err := parsePlatformSecretBindingPolicy(raw)
	if err != nil || parsed != policy {
		t.Fatalf("roundtrip %+v %v", parsed, err)
	}
	if !parsed.allows(platformPrincipalService) || !parsed.allows(platformPrincipalViewer) {
		t.Fatal("principals lost")
	}
	if validatePlatformHTTPSecretBindingPolicy(raw, "acs3_hmac_sha256", acs3TestDigest) != nil {
		t.Fatal("exact acs3 policy refused")
	}
	for _, pair := range [][2]string{{"http_bearer", acs3TestDigest}, {"acs3_hmac_sha256", strings.Repeat("b", 64)}} {
		if validatePlatformHTTPSecretBindingPolicy(raw, pair[0], pair[1]) == nil {
			t.Fatal("operation/network identity mismatch accepted")
		}
	}
	// Several actions, sorted by action then version.
	many := acs3TestPolicy(platformPrincipalsServiceOnly, "DescribeBill@2017-12-14", "QueryAccountBalance@2017-12-14", "QueryAccountBalance@2018-01-01")
	raw, err = canonicalPlatformSecretBindingPolicy(many)
	if err != nil || !strings.Contains(string(raw), `"principals":["service"],"acs3":{"actions":[{"action":"DescribeBill","version":"2017-12-14"},{"action":"QueryAccountBalance","version":"2017-12-14"},{"action":"QueryAccountBalance","version":"2018-01-01"}]}`) {
		t.Fatalf("%s %v", raw, err)
	}
	if got, err := parsePlatformSecretBindingPolicy(raw); err != nil || got != many {
		t.Fatal("roundtrip of several actions", err)
	}
}

// A viewer-only ACS3 binding has no v1 spelling (v1 carries no action block), so it is written as v2
// with the viewer as the only principal.
func TestPlatformSecretBindingPolicyACS3ViewerOnly(t *testing.T) {
	t.Parallel()
	policy := acs3TestPolicy("", "QueryAccountBalance@2017-12-14")
	raw, err := canonicalPlatformSecretBindingPolicy(policy)
	if err != nil || !strings.Contains(string(raw), `"principals":["viewer"],"acs3"`) {
		t.Fatalf("%s %v", raw, err)
	}
	parsed, err := parsePlatformSecretBindingPolicy(raw)
	if err != nil || parsed != policy || !parsed.allows(platformPrincipalViewer) || parsed.allows(platformPrincipalService) {
		t.Fatalf("%+v %v", parsed, err)
	}
	// A viewer-only HTTP policy is still the v1 spelling; its v2 spelling stays refused.
	httpRaw := `{"format":"portal.secret-binding-policy/2-dev.1","operation":"http_bearer","network_policy_sha256":"` + acs3TestDigest + `","principals":["viewer"]}`
	if _, err := parsePlatformSecretBindingPolicy([]byte(httpRaw)); err == nil {
		t.Fatal("a v2 viewer-only http policy was accepted")
	}
}

func TestPlatformSecretBindingPolicyACS3Refusals(t *testing.T) {
	t.Parallel()
	var seventeen []string
	for i := 0; i < 17; i++ {
		seventeen = append(seventeen, fmt.Sprintf("Action%02d@2017-12-14", i))
	}
	for name, p := range map[string]platformSecretBindingPolicy{
		"no actions":          acs3TestPolicy(platformPrincipalsBoth),
		"seventeen actions":   acs3TestPolicy(platformPrincipalsBoth, seventeen...),
		"unsorted":            acs3TestPolicy(platformPrincipalsBoth, "B@2017-12-14", "A@2017-12-14"),
		"duplicate":           acs3TestPolicy(platformPrincipalsBoth, "A@2017-12-14", "A@2017-12-14"),
		"bad action":          acs3TestPolicy(platformPrincipalsBoth, "1A@2017-12-14"),
		"action with a space": acs3TestPolicy(platformPrincipalsBoth, "A B@2017-12-14"),
		"bad version":         acs3TestPolicy(platformPrincipalsBoth, "A@2017-12"),
		"no version":          acs3TestPolicy(platformPrincipalsBoth, "A"),
		"no network digest":   {operation: "acs3_hmac_sha256", principals: platformPrincipalsBoth, acs3: "A@2017-12-14"},
		"actions on a bearer": {operation: "http_bearer", networkPolicySHA256: acs3TestDigest, principals: platformPrincipalsBoth, acs3: "A@2017-12-14"},
		"actions on raw_read": {operation: "raw_read", acs3: "A@2017-12-14"},
		"bad principals":      acs3TestPolicy("viewer", "A@2017-12-14"),
	} {
		if raw, err := canonicalPlatformSecretBindingPolicy(p); err == nil {
			t.Errorf("%s: accepted %s", name, raw)
		}
	}
	good, _ := canonicalPlatformSecretBindingPolicy(acs3TestPolicy(platformPrincipalsBoth, "A@2017-12-14"))
	s := string(good)
	for name, mutated := range map[string]string{
		"extra member in the block": strings.Replace(s, `"actions":`, `"extra":1,"actions":`, 1),
		"extra member in an action": strings.Replace(s, `"version":"2017-12-14"}`, `"version":"2017-12-14","x":1}`, 1),
		"duplicate action member":   strings.Replace(s, `"action":"A"`, `"action":"A","action":"A"`, 1),
		"v1 format with a block":    strings.Replace(s, `/2-dev.1`, `/1-dev.1`, 1),
		"block on a bearer":         strings.Replace(s, `acs3_hmac_sha256`, `http_bearer`, 1),
		"upper-case block name":     strings.Replace(s, `"acs3"`, `"ACS3"`, 1),
		"null block":                strings.Replace(s, `{"actions":[{"action":"A","version":"2017-12-14"}]}`, `null`, 1),
		"empty action list":         strings.Replace(s, `[{"action":"A","version":"2017-12-14"}]`, `[]`, 1),
		"whitespace":                strings.Replace(s, `"acs3":`, `"acs3": `, 1),
	} {
		if _, err := parsePlatformSecretBindingPolicy([]byte(mutated)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
