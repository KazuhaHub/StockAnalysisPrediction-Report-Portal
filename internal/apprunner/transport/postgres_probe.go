package transport

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// This file is the wire of the PostgreSQL liveness probe (doc 178), shared by Portal's controller
// side and the runner: the admit codec, the outcome status codes and the capability query. It has
// no build tag and decides no authority.

// EgressPostgresAdmit is what the runner tells Portal when an application asks for a check: the
// reviewed target and the binding to use, both only names. Portal resolves them from its own rows.
type EgressPostgresAdmit struct {
	Target     string `json:"target"`
	Credential string `json:"credential"`
}

func validEgressPostgresAdmit(a EgressPostgresAdmit) bool {
	return appplatform.ValidPostgresTargetID(a.Target) && appplatform.ValidCredentialBindingName(a.Credential)
}

// EncodeEgressPostgresAdmit encodes the closed admit JSON.
func EncodeEgressPostgresAdmit(a EgressPostgresAdmit) ([]byte, error) {
	if !validEgressPostgresAdmit(a) {
		return nil, ErrEgressProtocol
	}
	out, err := json.Marshal(a)
	if err != nil || len(out) > egressAdmitMaxBytes {
		return nil, ErrEgressProtocol
	}
	return out, nil
}

// DecodeEgressPostgresAdmit accepts only the exact canonical encoding of a valid admit.
func DecodeEgressPostgresAdmit(payload []byte) (EgressPostgresAdmit, error) {
	var a EgressPostgresAdmit
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if len(payload) == 0 || len(payload) > egressAdmitMaxBytes || d.Decode(&a) != nil || !validEgressPostgresAdmit(a) {
		return EgressPostgresAdmit{}, ErrEgressProtocol
	}
	if canonical, err := json.Marshal(a); err != nil || !bytes.Equal(canonical, payload) {
		return EgressPostgresAdmit{}, ErrEgressProtocol
	}
	return a, nil
}

// EgressPostgresStatus is the status code a probe outcome travels as in an EgressOutcome (1..6, in
// the order of the doc), or 0 for a name that is not an outcome.
func EgressPostgresStatus(outcome string) uint16 {
	for i, name := range EgressPostgresOutcomes {
		if name == outcome {
			return uint16(i + 1)
		}
	}
	return 0
}

// EgressPostgresOutcome is the inverse of EgressPostgresStatus, or "" for an unknown code.
func EgressPostgresOutcome(status uint16) string {
	if status < 1 || int(status) > len(EgressPostgresOutcomes) {
		return ""
	}
	return EgressPostgresOutcomes[status-1]
}

// EgressPostgresOutcomes are the outcome names a check can end with, by status code minus one.
var EgressPostgresOutcomes = []string{broker.PostgresReachable, broker.PostgresConnectFailed, broker.PostgresTLSFailed, broker.PostgresAuthFailed, broker.PostgresQueryFailed, broker.PostgresTimeout}

// The postgres probe capability query. Like the network targets query it is separate from the
// network runtime profile (which is compared byte for byte with Portal's stored plan) and from the
// egress profile; a runner that predates it closes the connection at the unknown magic, and Portal
// then adds no network.postgres to an egress session, because a runner that does not know the
// resource would refuse the whole session.
const (
	// PostgresProbeQueryMagic is followed by a 32-byte nonce: Portal -> runner.
	PostgresProbeQueryMagic = "PRPQ0001"
	// PostgresProbeReplyMagic is followed by the nonce, a big-endian u32 length and the closed JSON
	// of PostgresProbeProfile: runner -> Portal.
	PostgresProbeReplyMagic = "PRPR0001"
	PostgresProbeFormat     = "portal.runner.postgres-probe/1-dev.1"
	maxPostgresProbeProfile = 1024
)

// ErrPostgresProbeUnsupported means the runner does not answer the query, or answered something
// other than the closed format.
var ErrPostgresProbeUnsupported = errors.New("runner does not support the PostgreSQL probe")

// PostgresProbeProfile is what a runner reports: the formats of the command it verifies and of the
// policy it serves.
type PostgresProbeProfile struct {
	Format        string `json:"format"`
	CommandFormat string `json:"command_format"`
	PolicyFormat  string `json:"policy_format"`
}

// NewPostgresProbeProfile is the answer of a runner that serves the probe.
func NewPostgresProbeProfile() PostgresProbeProfile {
	return PostgresProbeProfile{Format: PostgresProbeFormat, CommandFormat: control.PostgresProbeCommandFormat, PolicyFormat: appplatform.PostgresPolicyFormat}
}

// EncodePostgresProbeProfile returns the canonical reply body of a valid profile.
func EncodePostgresProbeProfile(p PostgresProbeProfile) ([]byte, error) {
	if p != NewPostgresProbeProfile() {
		return nil, ErrPostgresProbeUnsupported
	}
	return json.Marshal(p)
}

// ParsePostgresProbeProfile accepts only the exact canonical encoding of the profile this build
// understands: a runner that answers any other format is unsupported, never partly used.
func ParsePostgresProbeProfile(raw []byte) (PostgresProbeProfile, error) {
	var p PostgresProbeProfile
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > maxPostgresProbeProfile || d.Decode(&p) != nil {
		return PostgresProbeProfile{}, ErrPostgresProbeUnsupported
	}
	if canonical, err := EncodePostgresProbeProfile(p); err != nil || !bytes.Equal(canonical, raw) {
		return PostgresProbeProfile{}, ErrPostgresProbeUnsupported
	}
	return p, nil
}
