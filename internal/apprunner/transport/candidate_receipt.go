package transport

import (
	"bytes"
	"errors"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

var ErrCandidateReceiptObservation = errors.New("authenticated runner candidate receipt unavailable")

const MaxCandidateReceiptAge = 3 * time.Second

// CandidateReceiptEndpoint records the same OS-authenticated local profile as
// other receipt channels. A private test endpoint is never a root-controller one.
type CandidateReceiptEndpoint = DataReceiptEndpoint

// CandidateReceiptObservation proves local receipt provenance for one signed
// request, not current native health, writer access or completed installation.
// Canonical bytes are held privately so nested receipt pointers never alias it.
type CandidateReceiptObservation struct {
	receiptJSON            string
	controller             control.Authority
	requestSHA, commandSHA string
	receivedAt             time.Time
	endpoint               CandidateReceiptEndpoint
}

func (CandidateReceiptObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrCandidateReceiptObservation
}
func (o *CandidateReceiptObservation) UnmarshalJSON([]byte) error {
	*o = CandidateReceiptObservation{}
	return ErrCandidateReceiptObservation
}
func (o CandidateReceiptObservation) Receipt() journal.CandidateRunningReceipt {
	r, _ := journal.ParseCandidateRunningReceipt([]byte(o.receiptJSON))
	return r
}
func (o CandidateReceiptObservation) Controller() control.Authority {
	r := o.controller
	r.PublicKey = bytes.Clone(r.PublicKey)
	return r
}
func (o CandidateReceiptObservation) RequestSHA256() string              { return o.requestSHA }
func (o CandidateReceiptObservation) CommandSHA256() string              { return o.commandSHA }
func (o CandidateReceiptObservation) ReceivedAt() time.Time              { return o.receivedAt }
func (o CandidateReceiptObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
