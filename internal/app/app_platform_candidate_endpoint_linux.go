//go:build linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformStartCandidate(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformCandidateIntent) (transport.CandidateReceiptObservation, error) {
	return transport.StartControllerCandidate(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, intent.raw, intent.signature, intent.authority, intent.descriptor)
}

func platformReadCandidate(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformCandidateIntent) (transport.CandidateReceiptObservation, error) {
	return transport.ReadControllerCandidateReceipt(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, intent.raw, intent.signature, intent.authority)
}
