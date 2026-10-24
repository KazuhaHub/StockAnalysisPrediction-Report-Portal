package app

const setForecastReader = "forecast_reader"

func validForecastReaderStage(stage string) bool {
	return stage == "off" || stage == "card" || stage == "full"
}

// Scope remains independent from the reading-stage switch.
func (s *Server) forecastReaderStage(d forecastDecision) string {
	if d.Scope != scopeAll && d.Scope != scopeOwn {
		return "off"
	}
	if d.Reason == "admin" {
		return "full"
	}
	stage := s.st.GetSetting(setForecastReader, "card")
	if !validForecastReaderStage(stage) {
		return "off"
	}
	return stage
}
