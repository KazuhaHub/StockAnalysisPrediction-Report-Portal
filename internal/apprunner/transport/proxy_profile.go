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
