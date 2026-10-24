package app

import "log"

// forecastRefusalReason is the code for a decision that answered "none".
func forecastRefusalReason(d forecastDecision) string {
	if d.Reason == "ou" {
		return "ou_none"
	}
	return d.Reason
}

// noteForecastRefusal counts a refusal by reason code and, at most once a minute per reason, logs it.
// The line carries the code and a count: no account, no path, no query.
func (s *Server) noteForecastRefusal(reason string) {
	code, n, logIt := s.forecastSignals.refuse(reason, s.forecastEval.clock())
	if logIt {
		log.Printf("forecast access: refused reason=%s n=%d", code, n)
	}
}
