//go:build !linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformProbeCandidate(context.Context, platformRuntimeEndpoint, platformCandidateIntent) (transport.CandidateReadinessObservation, error) {
	return transport.CandidateReadinessObservation{}, errAppPlatformRuntimeConflict
}
