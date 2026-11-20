package app

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	platformSecretBindingPolicyFormat   = "portal.secret-binding-policy/1-dev.1"
	platformSecretBindingPolicyFormatV2 = "portal.secret-binding-policy/2-dev.1"
)

// The principals that may use a binding (doc 176). A binding no review has
// opened to the service is usable by the viewer path only.
const (
	platformPrincipalViewer  = "viewer"
	platformPrincipalService = "service"
	// Spellings of principals in platformSecretBindingPolicy: the empty string is
	// viewer-only (the v1 policy), the others need the v2 policy.
	platformPrincipalsServiceOnly = "service"
	platformPrincipalsBoth        = "service,viewer"
)

// Stored restrictions only. This object cannot select a credential, destination,
// header or installation, and parsing it conveys no use authority. principals is
// "" (viewer only), "service" or "service,viewer"; the review that created the
// binding is the only thing that sets it.
type platformSecretBindingPolicy struct {
	operation           string
	networkPolicySHA256 string
	principals          string
}
type platformSecretBindingPolicyWire struct {
	Format              string `json:"format"`
	Operation           string `json:"operation"`
	NetworkPolicySHA256 string `json:"network_policy_sha256,omitempty"`
}
type platformSecretBindingPolicyWireV2 struct {
	Format              string   `json:"format"`
	Operation           string   `json:"operation"`
	NetworkPolicySHA256 string   `json:"network_policy_sha256"`
	Principals          []string `json:"principals"`
}

// platformSecretBindingPrincipalList is the sorted principal list of a spelling.
func platformSecretBindingPrincipalList(principals string) []string {
	switch principals {
	case platformPrincipalsServiceOnly:
		return []string{platformPrincipalService}
	case platformPrincipalsBoth:
		return []string{platformPrincipalService, platformPrincipalViewer}
	default:
		return []string{platformPrincipalViewer}
	}
}

func validPlatformSecretBindingPrincipals(principals string) bool {
	return principals == "" || principals == platformPrincipalsServiceOnly || principals == platformPrincipalsBoth
}

// allows says whether the binding may be used by principal (viewer or service).
func (p platformSecretBindingPolicy) allows(principal string) bool {
	return slices.Contains(platformSecretBindingPrincipalList(p.principals), principal)
}

func canonicalPlatformSecretBindingPolicy(p platformSecretBindingPolicy) ([]byte, error) {
	if !validPlatformSecretBindingPrincipals(p.principals) {
		return nil, errAppPlatformCredentialUnavailable
	}
	switch p.operation {
	case "http_bearer", "http_api_key":
		if !platformValidSHA(p.networkPolicySHA256) {
			return nil, errAppPlatformCredentialUnavailable
		}
	case "raw_read":
		if p.networkPolicySHA256 != "" || p.principals != "" {
			return nil, errAppPlatformCredentialUnavailable
		}
	default:
		return nil, errAppPlatformCredentialUnavailable
	}
	if p.principals != "" {
		return json.Marshal(platformSecretBindingPolicyWireV2{platformSecretBindingPolicyFormatV2, p.operation, p.networkPolicySHA256, platformSecretBindingPrincipalList(p.principals)})
	}
	return json.Marshal(platformSecretBindingPolicyWire{platformSecretBindingPolicyFormat, p.operation, p.networkPolicySHA256})
}

// Only complete canonical bytes are accepted. Re-encoding the bounded closed
// shape rejects duplicate/missing/aliased/unknown fields, whitespace, trailing
// data and escaped spellings. No values, ciphertext or mutable records belong
// in this JSON. raw_read is representable for future separate review, not usable.
func parsePlatformSecretBindingPolicy(raw []byte) (platformSecretBindingPolicy, error) {
	var zero platformSecretBindingPolicy
	if len(raw) == 0 || len(raw) > 8192 || !utf8.Valid(raw) {
		return zero, errAppPlatformCredentialUnavailable
	}
	var head struct {
		Format string `json:"format"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return zero, errAppPlatformCredentialUnavailable
	}
	var p platformSecretBindingPolicy
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	switch head.Format {
	case platformSecretBindingPolicyFormat:
		var w platformSecretBindingPolicyWire
		if d.Decode(&w) != nil {
			return zero, errAppPlatformCredentialUnavailable
		}
		p = platformSecretBindingPolicy{operation: w.Operation, networkPolicySHA256: w.NetworkPolicySHA256}
	case platformSecretBindingPolicyFormatV2:
		var w platformSecretBindingPolicyWireV2
		if d.Decode(&w) != nil {
			return zero, errAppPlatformCredentialUnavailable
		}
		p = platformSecretBindingPolicy{operation: w.Operation, networkPolicySHA256: w.NetworkPolicySHA256, principals: strings.Join(w.Principals, ",")}
	default:
		return zero, errAppPlatformCredentialUnavailable
	}
	canonical, err := canonicalPlatformSecretBindingPolicy(p)
	if err != nil || !bytes.Equal(raw, canonical) {
		return zero, errAppPlatformCredentialUnavailable
	}
	return p, nil
}

// Call only with the digest of the already validated current network grant,
// never a digest supplied by an application. This still proves no live grant.
func validatePlatformHTTPSecretBindingPolicy(raw []byte, operation, networkPolicySHA256 string) error {
	p, err := parsePlatformSecretBindingPolicy(raw)
	if err != nil || (operation != "http_bearer" && operation != "http_api_key") || p.operation != operation || !platformValidSHA(networkPolicySHA256) || p.networkPolicySHA256 != networkPolicySHA256 {
		return errAppPlatformCredentialUnavailable
	}
	return nil
}
