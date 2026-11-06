package transport

import (
	"bytes"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type UpdateStartBackendRequestObservation struct {
	result     journal.UpdateStartBackendRequestResult
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (UpdateStartBackendRequestObservation) MarshalJSON() ([]byte, error) {
	return nil, driver.ErrContainerUncertain
}

func (o *UpdateStartBackendRequestObservation) UnmarshalJSON([]byte) error {
	*o = UpdateStartBackendRequestObservation{}
	return driver.ErrContainerUncertain
}

func (o UpdateStartBackendRequestObservation) Result() journal.UpdateStartBackendRequestResult {
	result := o.result
	result.Response.Body = bytes.Clone(result.Response.Body)
	return result
}
func (o UpdateStartBackendRequestObservation) Controller() control.Authority {
	authority := o.authority
	authority.PublicKey = bytes.Clone(authority.PublicKey)
	return authority
}
func (o UpdateStartBackendRequestObservation) Endpoint() CandidateReceiptEndpoint {
	return o.endpoint
}
func (o UpdateStartBackendRequestObservation) ReceivedAt() time.Time {
	return o.receivedAt
}
func (o UpdateStartBackendRequestObservation) CommandSHA256() string {
	return o.commandSHA
}
