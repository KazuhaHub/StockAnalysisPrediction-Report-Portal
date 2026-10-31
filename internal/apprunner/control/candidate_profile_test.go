package control_test

import (
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func TestCandidateSandboxMatchesRuntimeProfile(t *testing.T) {
	if control.CandidateSandboxProfile != driver.Profile {
		t.Fatal("signed candidate profile differs from runtime adapter")
	}
}
