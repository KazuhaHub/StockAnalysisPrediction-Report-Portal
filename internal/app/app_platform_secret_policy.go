package app

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const (
	platformSecretBindingPolicyFormat   = "portal.secret-binding-policy/1-dev.1"
	platformSecretBindingPolicyFormatV2 = "portal.secret-binding-policy/2-dev.1"
	platformSecretBindingPolicyFormatV3 = "portal.secret-binding-policy/3-dev.1"
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
// binding is the only thing that sets it. A binding is scoped either to the whole
// network policy (networkPolicySHA256: the v1 and 2-dev.1 formats) or to one
// target of it (networkTargetSHA256: the 3-dev.1 format); never both.
type platformSecretBindingPolicy struct {
	operation           string
	networkPolicySHA256 string
	networkTargetSHA256 string
	principals          string
	// acs3 is the reviewed list of API actions an acs3_hmac_sha256 binding may sign, as sorted,
	// unique "Action@Version" items joined by commas (doc 177); empty for every other operation.
	acs3 string
}
type platformSecretBindingPolicyWire struct {
	Format              string `json:"format"`
	Operation           string `json:"operation"`
	NetworkPolicySHA256 string `json:"network_policy_sha256,omitempty"`
}
type platformSecretBindingPolicyWireV2 struct {
	Format              string                  `json:"format"`
	Operation           string                  `json:"operation"`
	NetworkPolicySHA256 string                  `json:"network_policy_sha256"`
	Principals          []string                `json:"principals"`
	ACS3                *platformACS3PolicyWire `json:"acs3,omitempty"`
}

// platformACS3Action is one (action, version) pair of an ACS3 binding's reviewed policy.
type platformACS3Action struct {
	Action  string `json:"action"`
	Version string `json:"version"`
}

type platformACS3PolicyWire struct {
	Actions []platformACS3Action `json:"actions"`
}

const platformACS3MaxActions = 16

// platformACS3ActionList is the pairs of a policy's acs3 spelling.
func platformACS3ActionList(spelling string) []platformACS3Action {
	if spelling == "" {
		return nil
	}
	var list []platformACS3Action
	for _, item := range strings.Split(spelling, ",") {
		action, version, _ := strings.Cut(item, "@")
		list = append(list, platformACS3Action{action, version})
	}
	return list
}

// platformACS3Spelling is the policy spelling of a list of pairs, and whether the list is one a
// policy may carry: 1..16 valid pairs, sorted by action then version, no duplicate.
func platformACS3Spelling(list []platformACS3Action) (string, bool) {
	if len(list) < 1 || len(list) > platformACS3MaxActions {
		return "", false
	}
	items := make([]string, len(list))
	for i, a := range list {
		if !appplatform.ValidACS3Action(a.Action) || !appplatform.ValidACS3Version(a.Version) {
			return "", false
		}
		if i > 0 && (list[i-1].Action > a.Action || list[i-1].Action == a.Action && list[i-1].Version >= a.Version) {
			return "", false
		}
		items[i] = a.Action + "@" + a.Version
	}
	return strings.Join(items, ","), true
}

type platformSecretBindingPolicyWireV3 struct {
	Format              string   `json:"format"`
	Operation           string   `json:"operation"`
	NetworkTargetSHA256 string   `json:"network_target_sha256"`
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
		// Exactly one scope: the whole policy (older formats) or one target.
		if (p.networkTargetSHA256 == "") == (p.networkPolicySHA256 == "") || !platformValidSHA(p.networkPolicySHA256+p.networkTargetSHA256) || p.acs3 != "" {
			return nil, errAppPlatformCredentialUnavailable
		}
	case "acs3_hmac_sha256":
		// Always the v2 spelling with whole-policy scope: only v2 carries the action list, and a
		// viewer-only binding is then written with the viewer as its one principal.
		if p.networkTargetSHA256 != "" || !platformValidSHA(p.networkPolicySHA256) {
			return nil, errAppPlatformCredentialUnavailable
		}
		if spelling, ok := platformACS3Spelling(platformACS3ActionList(p.acs3)); !ok || spelling != p.acs3 {
			return nil, errAppPlatformCredentialUnavailable
		}
		return json.Marshal(platformSecretBindingPolicyWireV2{platformSecretBindingPolicyFormatV2, p.operation, p.networkPolicySHA256,
			platformSecretBindingPrincipalList(p.principals), &platformACS3PolicyWire{platformACS3ActionList(p.acs3)}})
	case "raw_read":
		if p.networkPolicySHA256 != "" || p.networkTargetSHA256 != "" || p.principals != "" || p.acs3 != "" {
			return nil, errAppPlatformCredentialUnavailable
		}
	default:
		return nil, errAppPlatformCredentialUnavailable
	}
	if p.networkTargetSHA256 != "" {
		return json.Marshal(platformSecretBindingPolicyWireV3{platformSecretBindingPolicyFormatV3, p.operation, p.networkTargetSHA256, platformSecretBindingPrincipalList(p.principals)})
	}
	if p.principals != "" {
		return json.Marshal(platformSecretBindingPolicyWireV2{platformSecretBindingPolicyFormatV2, p.operation, p.networkPolicySHA256, platformSecretBindingPrincipalList(p.principals), nil})
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
		if w.ACS3 != nil {
			// The block is only ever a list the canonical form of the operation carries; a list that
			// does not satisfy platformACS3Spelling cannot match the canonical bytes below.
			p.acs3, _ = platformACS3Spelling(w.ACS3.Actions)
			if p.acs3 == "" {
				return zero, errAppPlatformCredentialUnavailable
			}
		}
		if p.operation == "acs3_hmac_sha256" && p.principals == platformPrincipalViewer {
			p.principals = ""
		}
	case platformSecretBindingPolicyFormatV3:
		var w platformSecretBindingPolicyWireV3
		if d.Decode(&w) != nil {
			return zero, errAppPlatformCredentialUnavailable
		}
		// ["viewer"] is the viewer-only spelling, which the struct keeps as "".
		principals := strings.Join(w.Principals, ",")
		if principals == platformPrincipalViewer {
			principals = ""
		}
		p = platformSecretBindingPolicy{operation: w.Operation, networkTargetSHA256: w.NetworkTargetSHA256, principals: principals}
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
	// Only a whole-policy binding is judged against the policy digest; a target
	// binding is judged against the target (platformSecretBindingTargetAllowed).
	if err != nil || (operation != "http_bearer" && operation != "http_api_key" && operation != "acs3_hmac_sha256") || p.operation != operation || !platformValidSHA(networkPolicySHA256) || p.networkTargetSHA256 != "" || p.networkPolicySHA256 != networkPolicySHA256 {
		return errAppPlatformCredentialUnavailable
	}
	return nil
}
