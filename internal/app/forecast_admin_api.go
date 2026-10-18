package app

// The forecast evaluator's admin surface (ADR 0029 §6, plan §6.1).
//
// A switch with no writer is a constant with a misleading name, so the reader, the writer and the UI
// ship together. Both routes are PermManage, like every other settings page.

import (
	"net/http"
	"strings"
)

// apiForecastSettings is the GET half: what the switch is, and what the evaluator has done lately.
func (s *Server) apiForecastSettings(w http.ResponseWriter, r *http.Request, user string) {
	enabled := s.st.GetSetting(setForecastAuto, "0") == "1"
	writeJSON(w, map[string]any{
		"ok":      true,
		"enabled": enabled,
		// The benchmark new sets are written with. Existing sets keep theirs, and the UI says so:
		// changing this does not re-score anything already on the ledger.
		"bench": s.st.ForecastBench(),
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
		Enabled *bool   `json:"enabled"`
		Bench   *string `json:"bench"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.Bench != nil {
		bench := strings.TrimSpace(*in.Bench)
		if market, _ := benchMarketCode(bench); market == "" {
			jsonErrorCode(w, http.StatusBadRequest, "forecast_bad_bench",
				"基准必须是一个能取到日线的六位指数代码，例如 sh000852")
			return
		}
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
