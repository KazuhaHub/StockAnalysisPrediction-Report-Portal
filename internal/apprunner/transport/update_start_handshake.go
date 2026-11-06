package transport

import (
	"bytes"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type UpdateStartBackendHandshakeObservation struct {
	evidence   journal.UpdateStartBackendHandshakeEvidence
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (UpdateStartBackendHandshakeObservation) MarshalJSON() ([]byte, error) {
	return nil, driver.ErrContainerUncertain
}

func (o *UpdateStartBackendHandshakeObservation) UnmarshalJSON([]byte) error {
	*o = UpdateStartBackendHandshakeObservation{}
	return driver.ErrContainerUncertain
}

func (o UpdateStartBackendHandshakeObservation) Evidence() journal.UpdateStartBackendHandshakeEvidence {
	return o.evidence
}

func (o UpdateStartBackendHandshakeObservation) Controller() control.Authority {
	authority := o.authority
	authority.PublicKey = bytes.Clone(authority.PublicKey)
	return authority
}

func (o UpdateStartBackendHandshakeObservation) Endpoint() CandidateReceiptEndpoint {
	return o.endpoint
}

func (o UpdateStartBackendHandshakeObservation) ReceivedAt() time.Time {
	return o.receivedAt
}

func (o UpdateStartBackendHandshakeObservation) CommandSHA256() string {
	return o.commandSHA
}
