package app

import (
	"fmt"
	"os"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

const forecastDemoClockEnv = "RP_FORECAST_DEMO_NOW"

func parseForecastDemoClock(value string, present bool, build string) (time.Time, error) {
	if !present {
		return time.Time{}, nil
	}
	if build != "dev" {
		return time.Time{}, fmt.Errorf("%s is allowed only in a dev build", forecastDemoClockEnv)
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 instant: %w", forecastDemoClockEnv, err)
	}
	return t.UTC(), nil
}

func validateForecastDemoClock() error {
	value, present := os.LookupEnv(forecastDemoClockEnv)
	_, err := parseForecastDemoClock(value, present, version.Version)
	return err
}

// forecastNow is a read-only preview clock. It never controls receipt, authentication, token
// validity, evaluation, retention or any other writer. Startup rejects invalid or release use.
func (s *Server) forecastNow() time.Time {
	value, present := os.LookupEnv(forecastDemoClockEnv)
	if t, err := parseForecastDemoClock(value, present, version.Version); err == nil && present {
		return t
	}
	// Preserve the existing per-server test clock without wiring the demo environment to writers.
	return s.forecastEval.clock()
}
