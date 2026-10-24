package app

import "time"

// Each publication replaces one complete fetch window; joining disjoint windows could invent
// contiguous session coverage. Readers receive copies and cannot mutate evaluator state.
func (e *forecastEvaluator) publishReadCalendar(code string, sessions []string) {
	if len(sessions) == 0 {
		return
	}
	for i, day := range sessions {
		if !validReportDate(day) || i > 0 && day <= sessions[i-1] {
			return
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.readCalendars == nil {
		e.readCalendars = map[string][]string{}
	}
	e.readCalendars[code] = append([]string(nil), sessions...)
}

func (e *forecastEvaluator) readCalendarSnapshot() map[string][]string {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string][]string, len(e.readCalendars))
	for code, sessions := range e.readCalendars {
		out[code] = append([]string(nil), sessions...)
	}
	return out
}

func (s *Server) annotateReadPhases(sets []PredictionSet, now time.Time) {
	annotateReadPhasesWithCalendars(sets, now, s.forecastEval.readCalendarSnapshot())
}
