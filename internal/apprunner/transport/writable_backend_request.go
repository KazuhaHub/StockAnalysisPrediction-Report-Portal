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
type WritableBackendRequestObservation struct {
	result     journal.WritableBackendRequestResult
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (WritableBackendRequestObservation) MarshalJSON() ([]byte, error) {
	return nil, driver.ErrContainerUncertain
}
func (o *WritableBackendRequestObservation) UnmarshalJSON([]byte) error {
	*o = WritableBackendRequestObservation{}
	return driver.ErrContainerUncertain
}
func (o WritableBackendRequestObservation) Result() journal.WritableBackendRequestResult {
	r := o.result
	r.Response.Body = bytes.Clone(r.Response.Body)
	return r
}
func (o WritableBackendRequestObservation) Controller() control.Authority {
	a := o.authority
	a.PublicKey = bytes.Clone(a.PublicKey)
	return a
}
func (o WritableBackendRequestObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o WritableBackendRequestObservation) ReceivedAt() time.Time              { return o.receivedAt }
func (o WritableBackendRequestObservation) CommandSHA256() string              { return o.commandSHA }
