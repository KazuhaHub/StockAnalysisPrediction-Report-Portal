package transport

import (
	"bytes"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// NetworkStreamObservation binds a completed private stream to the exact
// controller, request frame and OS peer observed by the transport client.
type NetworkStreamObservation struct {
	result     broker.HTTPStreamResult
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	requestSHA string
	receivedAt time.Time
}

func (NetworkStreamObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrNetworkRuntimeUnavailable
}
func (o *NetworkStreamObservation) UnmarshalJSON([]byte) error {
	*o = NetworkStreamObservation{}
	return ErrNetworkRuntimeUnavailable
}
func (o NetworkStreamObservation) Result() broker.HTTPStreamResult { return o.result }
func (o NetworkStreamObservation) Controller() journal.ControllerEnrollment {
	c := o.authority
	c.PublicKey = bytes.Clone(c.PublicKey)
	return journal.ControllerEnrollment{
		DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		ControlRevision: c.ControlRevision, PublicKey: c.PublicKey, Fingerprint: c.Fingerprint,
	}
}
func (o NetworkStreamObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o NetworkStreamObservation) RequestSHA256() string              { return o.requestSHA }
func (o NetworkStreamObservation) ReceivedAt() time.Time              { return o.receivedAt }
