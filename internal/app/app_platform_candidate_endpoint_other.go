//go:build !linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformStartCandidate(context.Context, platformRuntimeEndpoint, platformCandidateIntent) (transport.CandidateReceiptObservation, error) {
	return transport.CandidateReceiptObservation{}, errAppPlatformRuntimeConflict
}
func platformReadCandidate(context.Context, platformRuntimeEndpoint, platformCandidateIntent) (transport.CandidateReceiptObservation, error) {
	return transport.CandidateReceiptObservation{}, errAppPlatformRuntimeConflict
}
