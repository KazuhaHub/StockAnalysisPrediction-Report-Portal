package app

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func TestPlatformReadinessRefusesUnstartedCandidate(t *testing.T) {
	s, lease := platformCandidateIntentFixture(t, false)
	if _, err := s.preparePlatformCandidateProbe(context.Background(), lease); err == nil {
		t.Fatal("unstarted candidate became readiness")
	}
	if _, err := s.probePlatformInstallCandidate(context.Background(), lease, platformRuntimeEndpoint{}); err == nil {
		t.Fatal("unstarted candidate probed")
	}
	if _, err := s.commitPlatformCandidateReadiness(context.Background(), lease, platformCandidateIntent{}, platformRuntimeEndpoint{}, transport.CandidateReadinessObservation{}); err == nil {
		t.Fatal("zero proof committed readiness")
	}
}
