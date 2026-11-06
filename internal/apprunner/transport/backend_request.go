package transport

import (
	"bytes"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// A bounded reply from the exact root channel. This is not a viewer grant and
// cannot substitute for current Portal access checks before/after application IO.
type BackendRequestObservation struct {
	result     journal.BackendRequestResult
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (BackendRequestObservation) MarshalJSON() ([]byte, error) {
	return nil, driver.ErrContainerUncertain
}
func (o *BackendRequestObservation) UnmarshalJSON([]byte) error {
	*o = BackendRequestObservation{}
	return driver.ErrContainerUncertain
}
func (o BackendRequestObservation) Result() journal.BackendRequestResult {
	r := o.result
	r.Response.Body = bytes.Clone(r.Response.Body)
	return r
}
func (o BackendRequestObservation) Controller() control.Authority {
	a := o.authority
	a.PublicKey = bytes.Clone(a.PublicKey)
	return a
}
func (o BackendRequestObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o BackendRequestObservation) ReceivedAt() time.Time              { return o.receivedAt }
func (o BackendRequestObservation) CommandSHA256() string              { return o.commandSHA }
