package transport

import (
	"bytes"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// CandidateReadinessObservation is a fresh authenticated root observation. A
// stored JSON body or historical running receipt cannot mint its provenance.
type CandidateReadinessObservation struct {
	readiness  journal.CandidateReadiness
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (CandidateReadinessObservation) MarshalJSON() ([]byte, error) {
	return nil, driver.ErrCandidateNotReady
}
func (o *CandidateReadinessObservation) UnmarshalJSON([]byte) error {
	*o = CandidateReadinessObservation{}
	return driver.ErrCandidateNotReady
}
func (o CandidateReadinessObservation) Readiness() journal.CandidateReadiness { return o.readiness }
func (o CandidateReadinessObservation) Controller() control.Authority {
	a := o.authority
	a.PublicKey = bytes.Clone(a.PublicKey)
	return a
}
func (o CandidateReadinessObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o CandidateReadinessObservation) ReceivedAt() time.Time              { return o.receivedAt }
func (o CandidateReadinessObservation) CommandSHA256() string              { return o.commandSHA }
