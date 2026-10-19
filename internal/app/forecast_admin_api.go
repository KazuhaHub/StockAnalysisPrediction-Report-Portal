package app

// The forecast evaluator's admin surface (ADR 0029 §6, plan §6.1).
//
// A switch with no writer is a constant with a misleading name, so the reader, the writer and the UI
// ship together. Both routes are PermManage, like every other settings page.

import (
	"net/http"
	"strings"
	"time"
)

// apiForecastSettings is the GET half: what the switch is, and what the evaluator has done lately.
func (s *Server) apiForecastSettings(w http.ResponseWriter, r *http.Request, user string) {
	enabled := s.st.GetSetting(setForecastAuto, "0") == "1"
	// The ledger start (P5): its value, the bound a new value must respect, and the sets that a value
	// at or before the bound would exclude, so an admin can see the effect before saving it.
	population := map[string]any{}
	if from, ok := s.st.ForecastPopulationFrom(); ok {
		population["set"] = true
		population["from"] = from.UTC().Format(time.RFC3339)
	} else if s.st.forecastPopulationIsSet() {
		// The key exists but its value is unreadable: keep "set" true (write-once still holds) but do
		// not hand the UI an instant nobody can trust.
		population["set"] = true
		population["damaged"] = true
	} else {
		population["set"] = false
	}
	if actor, at := s.st.ForecastPopulationSetBy(); actor != "" || at != "" {
		population["set_by"] = actor
		population["set_at"] = at
	}
	// The bound is derived from the ledger's oldest set; an empty ledger has no bound to offer, but
	// the field is present so the UI does not have to special-case a missing key.
	if earliest, has, err := s.st.forecastEarliestMadeAt(); err == nil && has {
		b := populationBound(earliest)
		population["bound"] = b.UTC().Format(time.RFC3339)
		cands, cerr := s.st.forecastPopulationCandidates(b)
		if cerr != nil {
			jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
			return
		}
		population["candidates"] = cands
	} else {
		population["candidates"] = []populationCandidate{}
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"enabled": enabled,
		// The benchmark new sets are written with. Existing sets keep theirs, and the UI says so:
		// changing this does not re-score anything already on the ledger.
		"bench": s.st.ForecastBench(),
		// The closed list of benchmarks a new set may be frozen with, so the settings Select cannot
		// offer one the validator would refuse.
		"bench_codes": forecastBenchCodes(),
		"population":  population,
		// What the evaluator would find if it ran now. Read-only and cheap — a count, not a fetch —
		// so an admin can tell "on and caught up" from "on and stuck" without opening the log.
		"due_sets":           s.st.CountForecastSetsDue(),
		"pending":            s.st.CountForecastMarksByStatus(markPending),
		"refused":            s.st.CountForecastMarksByStatus(markRefused),
		"unsettled_baseline": s.st.CountForecastBaselinePending(),
	})
}

// apiForecastSettingsSave is the POST half. A pointer per field, so "omitted" and "set to false" are
// different — the same convention every other settings form uses.
func (s *Server) apiForecastSettingsSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Enabled        *bool   `json:"enabled"`
		Bench          *string `json:"bench"`
		PopulationFrom *string `json:"population_from"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	// Validate EVERY field before writing any: a request that changes the benchmark and is then
	// refused for its ledger start must not have persisted the benchmark. "A refusal writes nothing"
	// is about the request, not about the one field that happened to be checked last.
	var bench string
	if in.Bench != nil {
		bench = strings.TrimSpace(*in.Bench)
		if _, ok := benchSpecFor(bench); !ok {
			jsonErrorCode(w, http.StatusBadRequest, "forecast_bad_bench",
				"bench must be an index code the portal is allowed to fetch, for example sh000852")
			return
		}
	}
	var populationFrom time.Time
	if in.PopulationFrom != nil {
		from, err := time.Parse(time.RFC3339, strings.TrimSpace(*in.PopulationFrom))
		if err != nil {
			jsonErrorCode(w, http.StatusBadRequest, "invalid_param", "population_from must be an RFC3339 instant")
			return
		}
		populationFrom = from
	}

	// Apply the ledger start FIRST, because it is the field that can be refused on a rule the caller
	// cannot see in the request (write-once, the bound, a mature cohort). Doing it after the other
	// writes is what would leave a partially applied request behind.
	if in.PopulationFrom != nil {
		earliest, hasSets, _ := s.st.forecastEarliestMadeAt()
		var bound string
		if hasSets {
			bound = populationBound(earliest).UTC().Format(time.RFC3339)
		}
		excluded, byState, code, err := s.st.SaveForecastPopulationFrom(populationFrom, time.Now().UTC())
		if err != nil {
			jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
			return
		}
		if code != "" {
			jsonErrorCode(w, populationRefusalStatus(code), code, forecastPopulationMessage(code))
			return
		}
		s.recordChange(r, user, AuditForecastPopulation, "settings", setForecastPopulationFrom,
			map[string]any{
				"from": populationFrom.UTC().Format(time.RFC3339), "bound": bound,
				"excluded_set_ids": excluded, "counts_by_state": byState,
			})
	}
	if in.Bench != nil {
		s.st.SetSetting(setForecastBench, bench)
	}
	if in.Enabled != nil {
		s.st.SetSetting(setForecastAuto, boolSetting(*in.Enabled))
		// Flipping a background loop that reaches a third party is exactly the kind of change an
		// operator wants a record of who made and when.
		s.recordChange(r, user, AuditForecastAuto, "settings", setForecastAuto,
			map[string]any{"enabled": *in.Enabled})
	}
	writeJSON(w, map[string]any{"ok": true})
}

// populationRefusalStatus maps a store refusal code to its HTTP status: the two "already decided"
// refusals are conflicts (409), the rest are the caller's to fix (400).
func populationRefusalStatus(code string) int {
	switch code {
	case "forecast_population_set", "forecast_population_too_late", "forecast_population_month_mature":
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

// forecastPopulationMessage is the English message behind a population refusal code. The SPA shows
// the localized err.forecast_population_* string; this is for a direct API caller.
func forecastPopulationMessage(code string) string {
	switch code {
	case "forecast_population_set":
		return "the ledger start is already set and is write-once"
	case "forecast_population_too_late":
		return "the ledger start is later than the bound for this ledger"
	case "forecast_population_future":
		return "the ledger start is in the future"
	case "forecast_population_month_mature":
		return "the first cohort already has a mature result"
	default:
		return code
	}
}

// The counts above. Each is one cheap aggregate; none of them fetches anything.

func (s *Store) CountForecastSetsDue() int64 {
	var n int64
	s.queryRow(`SELECT COUNT(*) FROM prediction_sets ps WHERE ps.state = ?
		AND (ps.base_check IN ('pending','refused')
		  OR EXISTS (SELECT 1 FROM prediction_marks m WHERE m.set_id = ps.id
		       AND m.status IN (?,?)))`,
		predStateAccepted, markPending, markRefused).Scan(&n)
	return n
}

func (s *Store) CountForecastMarksByStatus(status string) int64 {
	var n int64
	s.queryRow(`SELECT COUNT(*) FROM prediction_marks WHERE status = ?`, status).Scan(&n)
	return n
}

func (s *Store) CountForecastBaselinePending() int64 {
	var n int64
	s.queryRow(`SELECT COUNT(*) FROM prediction_sets WHERE state = ? AND base_check IN ('pending','refused')`,
		predStateAccepted).Scan(&n)
	return n
}
