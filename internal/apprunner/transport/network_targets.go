package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The network targets query tells Portal which policy formats a runner serves
// and which private networks its operator allowed. It is a separate query, never
// a field of the network runtime profile: that profile is compared byte for byte
// with the plan Portal stored (doc 174 F1), so a new field there would break
// every installation's foreground calls at once. A runner that predates the query
// closes the connection at the unknown magic; Portal then grants no v2 policy.
const (
	// NetworkTargetsQueryMagic is followed by a 32-byte nonce: Portal -> runner.
	NetworkTargetsQueryMagic = "PRNT0001"
	// NetworkTargetsReplyMagic is followed by the nonce, a big-endian u32 length
	// and the closed JSON of NetworkTargets: runner -> Portal.
	NetworkTargetsReplyMagic = "PRNU0001"
	NetworkTargetsFormat     = "portal.runner.network-targets/1-dev.1"
	// maxNetworkTargetsBytes bounds the JSON of the reply.
	maxNetworkTargetsBytes = 4096
)

// ErrNetworkTargetsUnsupported means the runner does not answer the query: it
// predates it, or the reply was not the closed format. Portal must grant no v2
// policy against such a runner.
var ErrNetworkTargetsUnsupported = errors.New("runner does not support network targets")

// NetworkTargets is what a runner reports. PrivateNetworks is the operator's
// allowlist sorted by prefix (empty when no private target may be granted) and
// PrivateNetworksSHA256 its digest, the value a private-target command carries.
type NetworkTargets struct {
	Format                string                       `json:"format"`
	PolicyFormats         []string                     `json:"policy_formats"`
	PrivateNetworks       []appplatform.PrivateNetwork `json:"private_networks"`
	PrivateNetworksSHA256 string                       `json:"private_networks_sha256"`
}

func networkTargetsPolicyFormats() []string {
	return []string{appplatform.HTTPPolicyFormat, appplatform.HTTPPolicyV2Format}
}

// NewNetworkTargets is the advertisement of a runner with the given allowlist.
func NewNetworkTargets(networks []appplatform.PrivateNetwork) (NetworkTargets, error) {
	sorted, err := appplatform.SortPrivateNetworks(networks)
	if err != nil {
		return NetworkTargets{}, ErrNetworkTargetsUnsupported
	}
	sum, err := appplatform.PrivateNetworksSHA256(sorted)
	if err != nil {
		return NetworkTargets{}, ErrNetworkTargetsUnsupported
	}
	return NetworkTargets{Format: NetworkTargetsFormat, PolicyFormats: networkTargetsPolicyFormats(), PrivateNetworks: sorted, PrivateNetworksSHA256: sum}, nil
}

// EncodeNetworkTargets returns the canonical reply body of a valid advertisement.
func EncodeNetworkTargets(t NetworkTargets) ([]byte, error) {
	canonical, err := NewNetworkTargets(t.PrivateNetworks)
	if err != nil || t.Format != canonical.Format || !slices.Equal(t.PolicyFormats, canonical.PolicyFormats) || t.PrivateNetworksSHA256 != canonical.PrivateNetworksSHA256 {
		return nil, ErrNetworkTargetsUnsupported
	}
	raw, err := json.Marshal(canonical)
	if err != nil || len(raw) > maxNetworkTargetsBytes {
		return nil, ErrNetworkTargetsUnsupported
	}
	return raw, nil
}

// ParseNetworkTargets accepts only the exact canonical encoding of a valid
// advertisement: closed keys, sorted valid entries and a matching digest.
func ParseNetworkTargets(raw []byte) (NetworkTargets, error) {
	var t NetworkTargets
	if len(raw) == 0 || len(raw) > maxNetworkTargetsBytes || !utf8.Valid(raw) {
		return NetworkTargets{}, ErrNetworkTargetsUnsupported
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&t) != nil {
		return NetworkTargets{}, ErrNetworkTargetsUnsupported
	}
	canonical, err := EncodeNetworkTargets(t)
	if err != nil || !bytes.Equal(canonical, raw) {
		return NetworkTargets{}, ErrNetworkTargetsUnsupported
	}
	return t, nil
}
