//go:build linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformProbeCandidate(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformCandidateIntent) (transport.CandidateReadinessObservation, error) {
	return transport.ProbeControllerCandidate(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, intent.raw, intent.signature, intent.authority, intent.descriptor)
}
