package transport

import (
	"bytes"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// NetworkObservation can only be constructed by the authenticated OS endpoint
// client. It remains a point-in-time result: Portal must recheck current SQL
// authority and its exact binding before exposing response data to an app.
type NetworkObservation struct {
	response   broker.HTTPResponse
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	requestSHA string
	receivedAt time.Time
}

func (NetworkObservation) MarshalJSON() ([]byte, error) { return nil, broker.ErrUncertain }
func (o *NetworkObservation) UnmarshalJSON([]byte) error {
	*o = NetworkObservation{}
	return broker.ErrUncertain
}
func (o NetworkObservation) Response() broker.HTTPResponse {
	r := o.response
	r.Body = bytes.Clone(r.Body)
	return r
}
func (o NetworkObservation) Controller() control.Authority {
	a := o.authority
	a.PublicKey = bytes.Clone(a.PublicKey)
	return a
}
func (o NetworkObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o NetworkObservation) RequestSHA256() string              { return o.requestSHA }
func (o NetworkObservation) ReceivedAt() time.Time              { return o.receivedAt }
