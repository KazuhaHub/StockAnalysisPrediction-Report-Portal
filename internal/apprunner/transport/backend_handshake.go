package transport

import (
	"bytes"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// BackendHandshakeObservation reports an authenticated fresh native exchange;
// the host must independently verify the challenge/answer before trusting boot.
type BackendHandshakeObservation struct {
	evidence   journal.BackendHandshakeEvidence
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (BackendHandshakeObservation) MarshalJSON() ([]byte, error) {
	return nil, driver.ErrContainerUncertain
}
func (o *BackendHandshakeObservation) UnmarshalJSON([]byte) error {
	*o = BackendHandshakeObservation{}
	return driver.ErrContainerUncertain
}
func (o BackendHandshakeObservation) Evidence() journal.BackendHandshakeEvidence { return o.evidence }
func (o BackendHandshakeObservation) Controller() control.Authority {
	a := o.authority
	a.PublicKey = bytes.Clone(a.PublicKey)
	return a
}
func (o BackendHandshakeObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o BackendHandshakeObservation) ReceivedAt() time.Time              { return o.receivedAt }
func (o BackendHandshakeObservation) CommandSHA256() string              { return o.commandSHA }
