package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type platformUpdateStartAdmission struct {
	intent      platformUpdateStartIntent
	reservation journal.UpdateStartReservation
}

// reservePlatformUpdateStart persists the signed generation-2 intent and asks
// the runner to reserve its distinct target resource. It does not start a
// process or advance the Portal operation.
func (s *Server) reservePlatformUpdateStart(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformUpdateStartAdmission, error) {
	intent, err := s.preparePlatformUpdateStart(ctx, expected)
	if err != nil {
		return platformUpdateStartAdmission{}, err
	}
	proof, err := platformReserveUpdateStart(ctx, endpoint, intent)
	if err != nil {
		return platformUpdateStartAdmission{}, err
	}
	reservation, ok := validPlatformUpdateStartObservation(proof, intent, endpoint)
	if !ok {
		return platformUpdateStartAdmission{}, errAppPlatformStageEvidence
	}
	return platformUpdateStartAdmission{intent: intent, reservation: reservation}, nil
}

func (s *Server) readPlatformUpdateStartReservation(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformUpdateStartAdmission, error) {
	intent, err := s.preparePlatformUpdateStart(ctx, expected)
	if err != nil {
		return platformUpdateStartAdmission{}, err
	}
	proof, err := platformReadUpdateStartReservation(ctx, endpoint, intent)
	if err != nil {
		return platformUpdateStartAdmission{}, err
	}
	reservation, ok := validPlatformUpdateStartObservation(proof, intent, endpoint)
	if !ok {
		return platformUpdateStartAdmission{}, errAppPlatformStageEvidence
	}
	return platformUpdateStartAdmission{intent: intent, reservation: reservation}, nil
}
