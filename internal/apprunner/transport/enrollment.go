package transport

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const maxEnrollmentBytes = 1024
const MaxEnrollmentAge = 3 * time.Second

var errEnrollmentObservation = errors.New("runner enrollment observation unavailable")
var observationUUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type enrollmentWire struct {
	Format          string `json:"format"`
	DeploymentID    string `json:"deployment_id"`
	ControllerEpoch string `json:"controller_epoch"`
	ControlRevision string `json:"control_revision"`
	PublicKey       string `json:"public_key"`
	Fingerprint     string `json:"fingerprint"`
}

// EnrollmentObservation can only be populated by an OS-authenticated transport
// probe. It observes enrolled public identity, never hardware compatibility,
// resource ownership, execution readiness or an independently signed receipt.
type EnrollmentObservation struct {
	controller journal.ControllerEnrollment
	receivedAt time.Time
}

func (o EnrollmentObservation) Controller() journal.ControllerEnrollment {
	result := o.controller
	result.PublicKey = bytes.Clone(result.PublicKey)
	return result
}
func (o EnrollmentObservation) ReceivedAt() time.Time { return o.receivedAt }
func encodeEnrollment(c journal.ControllerEnrollment) ([]byte, error) {
	if !observationUUID.MatchString(c.DeploymentID) || !observationUUID.MatchString(c.ControllerEpoch) || c.ControlRevision < 1 || len(c.PublicKey) != ed25519.PublicKeySize {
		return nil, errEnrollmentObservation
	}
	pin := sha256.Sum256(c.PublicKey)
	if hex.EncodeToString(pin[:]) != c.Fingerprint {
		return nil, errEnrollmentObservation
	}
	return json.Marshal(enrollmentWire{Format: "portal.runner.enrollment/1-dev.1", DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: strconv.FormatInt(c.ControlRevision, 10), PublicKey: base64.RawURLEncoding.EncodeToString(c.PublicKey), Fingerprint: c.Fingerprint})
}
func decodeEnrollment(raw []byte) (journal.ControllerEnrollment, error) {
	var zero journal.ControllerEnrollment
	if len(raw) == 0 || len(raw) > maxEnrollmentBytes {
		return zero, errEnrollmentObservation
	}
	var wire enrollmentWire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || wire.Format != "portal.runner.enrollment/1-dev.1" {
		return zero, errEnrollmentObservation
	}
	revision, err := strconv.ParseInt(wire.ControlRevision, 10, 64)
	if err != nil || strconv.FormatInt(revision, 10) != wire.ControlRevision {
		return zero, errEnrollmentObservation
	}
	public, err := base64.RawURLEncoding.DecodeString(wire.PublicKey)
	if err != nil || base64.RawURLEncoding.EncodeToString(public) != wire.PublicKey {
		return zero, errEnrollmentObservation
	}
	result := journal.ControllerEnrollment{DeploymentID: wire.DeploymentID, ControllerEpoch: wire.ControllerEpoch, ControlRevision: revision, PublicKey: public, Fingerprint: wire.Fingerprint}
	canonical, err := encodeEnrollment(result)
	if err != nil || !bytes.Equal(canonical, raw) {
		return zero, errEnrollmentObservation
	}
	return result, nil
}
