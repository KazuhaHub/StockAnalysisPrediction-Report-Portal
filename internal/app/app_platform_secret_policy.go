package app

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

const platformSecretBindingPolicyFormat = "portal.secret-binding-policy/1-dev.1"

// Stored restrictions only. This object cannot select a credential, destination,
// header, principal or installation, and parsing it conveys no use authority.
type platformSecretBindingPolicy struct {
	operation           string
	networkPolicySHA256 string
}
type platformSecretBindingPolicyWire struct {
	Format              string `json:"format"`
	Operation           string `json:"operation"`
	NetworkPolicySHA256 string `json:"network_policy_sha256,omitempty"`
}

func canonicalPlatformSecretBindingPolicy(p platformSecretBindingPolicy) ([]byte, error) {
	switch p.operation {
	case "http_bearer", "http_api_key":
		if !platformValidSHA(p.networkPolicySHA256) {
			return nil, errAppPlatformCredentialUnavailable
		}
	case "raw_read":
		if p.networkPolicySHA256 != "" {
			return nil, errAppPlatformCredentialUnavailable
		}
	default:
		return nil, errAppPlatformCredentialUnavailable
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
	var w platformSecretBindingPolicyWire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&w) != nil || w.Format != platformSecretBindingPolicyFormat {
		return zero, errAppPlatformCredentialUnavailable
	}
	p := platformSecretBindingPolicy{w.Operation, w.NetworkPolicySHA256}
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
