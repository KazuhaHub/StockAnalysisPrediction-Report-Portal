package transport

import (
	"bytes"
	"errors"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

var ErrDataReceiptObservation = errors.New("authenticated runner data receipt unavailable")

const MaxDataReceiptAge = 3 * time.Second

// DataReceiptEndpoint records the authenticated local profile. A Portal stage
// writer must require the configured root/controller profile, not a private test socket.
type DataReceiptEndpoint struct {
	Path                                    string
	RunnerUID, ControllerUID, ControllerGID uint32
	RootController                          bool
}

// DataReceiptObservation is minted only by the OS-authenticated receipt channel.
// It proves receipt provenance for one request, not fresh native readiness or a
// completed Portal stage. JSON/caller-created zero values carry no authority.
type DataReceiptObservation struct {
	receipt                journal.DataPreparationReceipt
	controller             control.Authority
	requestSHA, commandSHA string
	receivedAt             time.Time
	endpoint               DataReceiptEndpoint
}

func (DataReceiptObservation) MarshalJSON() ([]byte, error) { return nil, ErrDataReceiptObservation }
func (o *DataReceiptObservation) UnmarshalJSON([]byte) error {
	*o = DataReceiptObservation{}
	return ErrDataReceiptObservation
}
func (o DataReceiptObservation) Receipt() journal.DataPreparationReceipt { return o.receipt }
func (o DataReceiptObservation) Controller() control.Authority {
	result := o.controller
	result.PublicKey = bytes.Clone(result.PublicKey)
	return result
}
func (o DataReceiptObservation) RequestSHA256() string { return o.requestSHA }
func (o DataReceiptObservation) CommandSHA256() string { return o.commandSHA }
func (o DataReceiptObservation) ReceivedAt() time.Time { return o.receivedAt }

func (o DataReceiptObservation) Endpoint() DataReceiptEndpoint { return o.endpoint }
