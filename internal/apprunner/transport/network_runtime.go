package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const NetworkRuntimeFormat = "portal.runner.network-runtime/1-dev.1"
const maxNetworkRuntimeBytes = 4096

var ErrNetworkRuntimeUnavailable = errors.New("network runtime observation unavailable")
var ErrNetworkRuntimeIncompatible = errors.New("network runtime profile incompatible")

type NetworkRuntimeProfile struct {
	Format             string `json:"format"`
	RequestFormat      string `json:"request_format"`
	ConfirmationFormat string `json:"confirmation_format"`
	PolicyFormat       string `json:"policy_format"`
	MaxRequestBytes    int64  `json:"max_request_bytes"`
	MaxResponseBytes   int64  `json:"max_response_bytes"`
	MaxDeadlineMS      int    `json:"max_deadline_ms"`
	MaxConcurrency     int    `json:"max_concurrency"`
	ActiveRequests     int    `json:"active_requests"`
	CacheEntries       int    `json:"cache_entries"`
	WritableBackend    bool   `json:"writable_backend"`
}
type networkRuntimeWire struct {
	Controller json.RawMessage       `json:"controller"`
	Profile    NetworkRuntimeProfile `json:"profile"`
}

// This is a fresh OS endpoint observation, not installation compatibility by
// itself, backend readiness, a persistent grant or a permission to execute IO.
type NetworkRuntimeObservation struct {
	controller journal.ControllerEnrollment
	profile    NetworkRuntimeProfile
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
}

func (NetworkRuntimeObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrNetworkRuntimeUnavailable
}
func (o *NetworkRuntimeObservation) UnmarshalJSON([]byte) error {
	*o = NetworkRuntimeObservation{}
	return ErrNetworkRuntimeUnavailable
}
func (o NetworkRuntimeObservation) Controller() journal.ControllerEnrollment {
	c := o.controller
	c.PublicKey = bytes.Clone(c.PublicKey)
	return c
}
func (o NetworkRuntimeObservation) Profile() NetworkRuntimeProfile     { return o.profile }
func (o NetworkRuntimeObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o NetworkRuntimeObservation) ReceivedAt() time.Time              { return o.receivedAt }

func encodeNetworkRuntime(c journal.ControllerEnrollment, active, entries int, writable bool) ([]byte, error) {
	if active < 1 || active > broker.MaxNetworkPoolActive || entries < 1 || entries > broker.MaxNetworkPoolEntries {
		return nil, ErrNetworkRuntimeIncompatible
	}
	controller, err := encodeEnrollment(c)
	if err != nil {
		return nil, ErrNetworkRuntimeIncompatible
	}
	profile := NetworkRuntimeProfile{Format: NetworkRuntimeFormat, RequestFormat: control.NetworkHTTPRequestFormat, ConfirmationFormat: control.NetworkHTTPCheckFormat, PolicyFormat: appplatform.HTTPPolicyFormat, MaxRequestBytes: appplatform.MaxHTTPRequestBytes, MaxResponseBytes: appplatform.MaxHTTPResponseBytes, MaxDeadlineMS: appplatform.MaxHTTPDeadlineMS, MaxConcurrency: appplatform.MaxHTTPConcurrency, ActiveRequests: active, CacheEntries: entries, WritableBackend: writable}
	raw, err := json.Marshal(networkRuntimeWire{Controller: controller, Profile: profile})
	if err != nil || len(raw) > maxNetworkRuntimeBytes {
		return nil, ErrNetworkRuntimeIncompatible
	}
	return raw, nil
}
func decodeNetworkRuntime(raw []byte) (journal.ControllerEnrollment, NetworkRuntimeProfile, error) {
	var zero journal.ControllerEnrollment
	if len(raw) == 0 || len(raw) > maxNetworkRuntimeBytes || !utf8.Valid(raw) {
		return zero, NetworkRuntimeProfile{}, ErrNetworkRuntimeIncompatible
	}
	var wire networkRuntimeWire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil {
		return zero, NetworkRuntimeProfile{}, ErrNetworkRuntimeIncompatible
	}
	c, err := decodeEnrollment(wire.Controller)
	if err != nil {
		return zero, NetworkRuntimeProfile{}, ErrNetworkRuntimeIncompatible
	}
	canonical, err := encodeNetworkRuntime(c, wire.Profile.ActiveRequests, wire.Profile.CacheEntries, wire.Profile.WritableBackend)
	if err != nil || !bytes.Equal(canonical, raw) {
		return zero, NetworkRuntimeProfile{}, ErrNetworkRuntimeIncompatible
	}
	return c, wire.Profile, nil
}
