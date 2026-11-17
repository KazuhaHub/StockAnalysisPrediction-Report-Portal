package transport

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// ServiceEgressProfileFormat is the answer to the capability query a controller
// sends before it opens egress sessions. It is a separate query, never a field of
// the network runtime profile: that profile is compared byte for byte with the
// plan Portal stored, so a new field there would break every installation's
// foreground calls at once. A runner that predates the query closes the
// connection at the unknown magic; the controller treats that as no egress.
const ServiceEgressProfileFormat = "portal.runner.service-egress-profile/1-dev.1"

// ServiceEgressBrokerFormat names the application-facing protocol sessions serve.
const ServiceEgressBrokerFormat = broker.AppProtocol

// maxServiceEgressProfileBytes bounds the reply.
const maxServiceEgressProfileBytes = 1024

// ErrServiceEgressUnsupported means the runner does not host egress sessions: it
// predates them, or was built without them.
var ErrServiceEgressUnsupported = errors.New("runner does not support service egress")

// ServiceEgressProfile is what a runner reports about its egress capability.
// BackgroundActive is the number of pool slots background calls may hold (half
// of the network pool's active slots); zero means egress is disabled. A
// controller never opens a session on a runner that reports zero or a format it
// does not know.
type ServiceEgressProfile struct {
	Format           string `json:"format"`
	SessionFormat    string `json:"session_format"`
	BrokerFormat     string `json:"broker_format"`
	BackgroundActive int    `json:"background_active"`
	RatePerMinute    int    `json:"rate_per_minute"`
	Burst            int    `json:"burst"`
	// RetryAfterReply says the runner understands the versioned foreground frames
	// that carry Retry-After. It stays false until that lands.
	RetryAfterReply bool `json:"retry_after_reply"`
}

// NewServiceEgressProfile is the profile of a runner whose network pool has
// activeSlots slots.
func NewServiceEgressProfile(activeSlots int) ServiceEgressProfile {
	return ServiceEgressProfile{Format: ServiceEgressProfileFormat, SessionFormat: control.ServiceEgressSessionFormat, BrokerFormat: ServiceEgressBrokerFormat, BackgroundActive: activeSlots / 2, RatePerMinute: 120, Burst: 20}
}

func encodeServiceEgressProfile(p ServiceEgressProfile) ([]byte, error) {
	if _, err := ParseServiceEgressProfileValue(p); err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

// ParseServiceEgressProfileValue validates a profile's fields.
func ParseServiceEgressProfileValue(p ServiceEgressProfile) (ServiceEgressProfile, error) {
	if p.Format != ServiceEgressProfileFormat || p.SessionFormat != control.ServiceEgressSessionFormat || p.BrokerFormat != ServiceEgressBrokerFormat ||
		p.BackgroundActive < 0 || p.BackgroundActive > 4 || p.RatePerMinute != 120 || p.Burst != 20 {
		return ServiceEgressProfile{}, ErrServiceEgressUnsupported
	}
	return p, nil
}

// ParseServiceEgressProfile accepts only the exact canonical encoding of a
// valid profile.
func ParseServiceEgressProfile(raw []byte) (ServiceEgressProfile, error) {
	var p ServiceEgressProfile
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > maxServiceEgressProfileBytes || d.Decode(&p) != nil {
		return ServiceEgressProfile{}, ErrServiceEgressUnsupported
	}
	if canonical, err := json.Marshal(p); err != nil || !bytes.Equal(canonical, raw) {
		return ServiceEgressProfile{}, ErrServiceEgressUnsupported
	}
	return ParseServiceEgressProfileValue(p)
}
