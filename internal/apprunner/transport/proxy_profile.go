package transport

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform/proxyforwarder"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// The proxy capability query (doc 184 section 5.5): PRPX0001 plus a 32-byte nonce,
// answered with PRPY0001, the nonce, a u32 length and the closed JSON of a
// ProxyProfile. It is separate from the egress profile and the network runtime
// profile for the reason both of those are (doc 174 F1): a new field there would
// break every installation, while a runner that predates the query closes the
// connection at the unknown magic and Portal then puts no proxy policy into a
// session.
const (
	ProxyQueryMagic    = "PRPX0001"
	ProxyReplyMagic    = "PRPY0001"
	ProxyProfileFormat = "portal.runner.proxy-profile/1-dev.1"
	// ProxyStreamFormat names the stream type the broker socket serves.
	ProxyStreamFormat = "PRXY0001"

	maxProxyProfileBytes = 1024

	// The pinned-destination query (doc 186): PRPX0002 is answered with PRPY0002 and the closed JSON of a
	// ProxyPinsProfile. PRPX0001 stays frozen at the 1-dev.1 profile above whatever the runner's proxy_pins
	// hold, so a Portal that predates pins keeps accepting a runner that has them; a runner that predates pins
	// closes the connection at the unknown magic and a new Portal then asks PRPX0001. The largest valid
	// profile is about 5.6 KB (16 entries with 253-byte names and a full-length IPv6 address).
	ProxyPinsQueryMagic    = "PRPX0002"
	ProxyPinsReplyMagic    = "PRPY0002"
	ProxyPinsProfileFormat = "portal.runner.proxy-profile/1-dev.2"

	maxProxyPinsProfileBytes = 8192
)

// ErrProxyUnsupported means the runner does not serve the proxy: it predates the
// query, was built without egress, or answered something other than the closed
// format with this Portal's fixed limits.
var ErrProxyUnsupported = errors.New("runner does not support the forward proxy")

// ProxyProfile is what a runner reports: the stream and session formats it serves
// and the platform-fixed limits it enforces. Portal compares every value with its
// own, so a runner built for other limits is not used.
type ProxyProfile struct {
	Format          string `json:"format"`
	SessionFormat   string `json:"session_format"`
	StreamFormat    string `json:"stream_format"`
	Tunnels         int    `json:"tunnels"`
	RunnerTunnels   int    `json:"runner_tunnels"`
	NewPerMinute    int    `json:"new_per_minute"`
	Burst           int    `json:"burst"`
	IdleSeconds     int    `json:"idle_seconds"`
	LifetimeSeconds int    `json:"lifetime_seconds"`
	DailyBytes      int64  `json:"daily_bytes"`
	Mbit            int    `json:"mbit"`
}

// NewProxyProfile is the profile of a runner that enforces doc 184 section 6.
func NewProxyProfile() ProxyProfile {
	return ProxyProfile{
		Format: ProxyProfileFormat, SessionFormat: control.ServiceEgressSessionProxyFormat, StreamFormat: ProxyStreamFormat,
		Tunnels: appplatform.ProxyTunnelsPerInstallation, RunnerTunnels: appplatform.ProxyTunnelsPerRunner,
		NewPerMinute: appplatform.ProxyNewPerMinute, Burst: appplatform.ProxyNewBurst,
		IdleSeconds: appplatform.ProxyIdleSeconds, LifetimeSeconds: appplatform.ProxyLifetimeSeconds,
		DailyBytes: appplatform.ProxyDailyBytesMax, Mbit: appplatform.ProxyMbitPerSecond,
	}
}

func encodeProxyProfile(p ProxyProfile) ([]byte, error) {
	if p != NewProxyProfile() || proxyforwarder.OpenMagic != ProxyStreamFormat {
		return nil, ErrProxyUnsupported
	}
	return json.Marshal(p)
}

// ParseProxyProfile accepts only the exact canonical encoding of the profile this
// Portal expects.
func ParseProxyProfile(raw []byte) (ProxyProfile, error) {
	var p ProxyProfile
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > maxProxyProfileBytes || d.Decode(&p) != nil {
		return ProxyProfile{}, ErrProxyUnsupported
	}
	if canonical, err := encodeProxyProfile(p); err != nil || !bytes.Equal(canonical, raw) {
		return ProxyProfile{}, ErrProxyUnsupported
	}
	return p, nil
}

// ProxyPinsProfile is what a runner that serves pinned destinations reports on PRPX0002: the exact 1-dev.1
// profile, and the operator's allowlist of pins. Portal compares the fixed part with its own and offers the
// pins as the only choices of a review.
type ProxyPinsProfile struct {
	Format string                 `json:"format"`
	Fixed  ProxyProfile           `json:"fixed"`
	Pins   []appplatform.ProxyPin `json:"pins"`
}

// NewProxyPinsProfile is the profile of a runner holding the allowlist pins. The list must be a valid operator
// allowlist; a runner never encodes what Portal would refuse.
func NewProxyPinsProfile(pins []appplatform.ProxyPin) (ProxyPinsProfile, error) {
	if appplatform.ValidateProxyOperatorPins(pins) != nil {
		return ProxyPinsProfile{}, ErrProxyUnsupported
	}
	return ProxyPinsProfile{Format: ProxyPinsProfileFormat, Fixed: NewProxyProfile(), Pins: append([]appplatform.ProxyPin{}, pins...)}, nil
}

func encodeProxyPinsProfile(p ProxyPinsProfile) ([]byte, error) {
	if p.Format != ProxyPinsProfileFormat || p.Fixed != NewProxyProfile() || p.Pins == nil || appplatform.ValidateProxyOperatorPins(p.Pins) != nil {
		return nil, ErrProxyUnsupported
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > maxProxyPinsProfileBytes {
		return nil, ErrProxyUnsupported
	}
	return raw, nil
}

// ParseProxyPinsProfile accepts only the exact canonical encoding of a profile whose fixed part is this
// Portal's and whose allowlist is valid.
func ParseProxyPinsProfile(raw []byte) (ProxyPinsProfile, error) {
	var p ProxyPinsProfile
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > maxProxyPinsProfileBytes || d.Decode(&p) != nil {
		return ProxyPinsProfile{}, ErrProxyUnsupported
	}
	if canonical, err := encodeProxyPinsProfile(p); err != nil || !bytes.Equal(canonical, raw) {
		return ProxyPinsProfile{}, ErrProxyUnsupported
	}
	return p, nil
}

// ProxyRunnerState is what a runner reports about the proxy: the fixed profile, and whether it serves pinned
// destinations with the allowlist it holds. A runner that predates pins has PinsSupported false.
type ProxyRunnerState struct {
	Profile       ProxyProfile
	PinsSupported bool
	Pins          []appplatform.ProxyPin
}
