package transport

import (
	"bytes"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// WritableBackendHandshakeObservation reports an authenticated fresh native exchange;
// the host must independently verify the challenge/answer before trusting boot.
type WritableBackendHandshakeObservation struct {
	evidence   journal.WritableBackendHandshakeEvidence
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (WritableBackendHandshakeObservation) MarshalJSON() ([]byte, error) {
	return nil, driver.ErrContainerUncertain
}
func (o *WritableBackendHandshakeObservation) UnmarshalJSON([]byte) error {
	*o = WritableBackendHandshakeObservation{}
	return driver.ErrContainerUncertain
}
func (o WritableBackendHandshakeObservation) Evidence() journal.WritableBackendHandshakeEvidence {
	return o.evidence
}
func (o WritableBackendHandshakeObservation) Controller() control.Authority {
	a := o.authority
	a.PublicKey = bytes.Clone(a.PublicKey)
	return a
}
func (o WritableBackendHandshakeObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o WritableBackendHandshakeObservation) ReceivedAt() time.Time              { return o.receivedAt }
func (o WritableBackendHandshakeObservation) CommandSHA256() string              { return o.commandSHA }
