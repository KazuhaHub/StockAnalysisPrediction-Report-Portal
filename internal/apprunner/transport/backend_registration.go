package transport

import (
	"bytes"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// BackendRegistrationObservation is an authenticated historical delivery reply.
// It contains no document/key and grants no application, broker or viewer access.
type BackendRegistrationObservation struct {
	receipt    journal.BackendRegistrationReceipt
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (BackendRegistrationObservation) MarshalJSON() ([]byte, error) {
	return nil, driver.ErrContainerUncertain
}
func (o *BackendRegistrationObservation) UnmarshalJSON([]byte) error {
	*o = BackendRegistrationObservation{}
	return driver.ErrContainerUncertain
}
func (o BackendRegistrationObservation) Receipt() journal.BackendRegistrationReceipt {
	return o.receipt
}
func (o BackendRegistrationObservation) Controller() control.Authority {
	a := o.authority
	a.PublicKey = bytes.Clone(a.PublicKey)
	return a
}
func (o BackendRegistrationObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o BackendRegistrationObservation) ReceivedAt() time.Time              { return o.receivedAt }
func (o BackendRegistrationObservation) CommandSHA256() string              { return o.commandSHA }
