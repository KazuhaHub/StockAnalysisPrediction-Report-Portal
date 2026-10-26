package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// OU defaults are optional form preferences in existing metadata, not queue policy or permissions.
func runDefaultsOUKey(id int64) string { return "run_defaults_ou:" + strconv.FormatInt(id, 10) }

func (s *Server) validRunDefaultsOverride(key string, value any) bool {
	if value == nil {
		return true
	}
	switch key {
	case "run_default_mode":
		return value == "now" || value == "preset" || value == "scheduled"
	case "run_default_idle", "run_default_notify", "run_show_preset_rule":
		_, ok := value.(bool)
		return ok
	case "run_default_target_id", "run_default_preset_id", "run_default_retries":
		n, ok := value.(float64)
		if !ok || n < 0 || n > 1<<53 || n != float64(int64(n)) {
			return false
		}
		if key == "run_default_retries" {
			return n <= runDefaultRetriesMax
		}
		if n == 0 {
			return true
		}
		if key == "run_default_target_id" {
			_, ok = s.st.GetTarget(int64(n))
			return ok
		}
		_, ok = s.st.GetRunPreset(int64(n))
		return ok
	}
	return false
}

func (s *Server) resolveRunDefaultsOU(graph ouGraph, id int64) (map[string]any, map[string]any, map[string]string, map[string]any, error) {
	chain, err := graph.policyChain(id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	effective := s.runFormDefaultsJSON("run_default_")
	effective["run_show_preset_rule"] = s.runShowPresetRule()
	inherited := make(map[string]any)
	sources := make(map[string]string)
	own := make(map[string]any)
	for i := len(chain) - 1; i >= 0; i-- {
		ou := chain[i]
		if ou == id {
			for k, v := range effective {
				inherited[k] = v
			}
		}
		raw := s.st.GetSetting(runDefaultsOUKey(ou), "{}")
		var values map[string]any
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("invalid run defaults for OU %d", ou)
		}
		if ou == id {
			own = values
		}
		for k, v := range values {
			if v == nil {
				continue
			}
			if _, known := effective[k]; !known {
				continue
			}
			// Deleted workflows/windows resolve to no preference, matching global defaults.
			if (k == "run_default_target_id" || k == "run_default_preset_id") && !s.validRunDefaultsOverride(k, v) {
				v = float64(0)
			}
			if !s.validRunDefaultsOverride(k, v) {
				return nil, nil, nil, nil, fmt.Errorf("invalid run default %s", k)
			}
			effective[k] = v
			if ou != id {
				sources[k] = graph.nodes[ou].name
			}
		}
	}
	return effective, inherited, sources, own, nil
}

func (s *Server) apiRunDefaultsOU(w http.ResponseWriter, r *http.Request, user string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, 400, "invalid organizational unit")
		return
	}
	snap, err := s.st.loadOUPolicySnapshotFrom(s.st.db)
	if err != nil {
		jsonError(w, 500, "could not read organizational units")
		return
	}
	if _, ok := snap.graph.nodes[id]; !ok {
		jsonError(w, 404, "organizational unit not found")
		return
	}
	if r.Method == http.MethodPut {
		var values map[string]any
		if err := readJSONLimit(r, &values, 4096); err != nil || values == nil {
			jsonError(w, 400, "invalid run defaults")
			return
		}
		for k, v := range values {
			if !s.validRunDefaultsOverride(k, v) {
				jsonError(w, 400, "invalid run default: "+k)
				return
			}
			switch k {
			case "run_default_target_id", "run_default_preset_id", "run_default_retries", "run_default_mode", "run_default_idle", "run_default_notify", "run_show_preset_rule":
			default:
				jsonError(w, 400, "unknown run default")
				return
			}
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			jsonError(w, 500, "could not encode run defaults")
			return
		}
		// Lock the OU while writing so deletion and metadata cleanup serialize together.
		tx, err := s.st.db.BeginTx(r.Context(), nil)
		if err != nil {
			jsonError(w, 500, "could not save run defaults")
			return
		}
		defer tx.Rollback()
		if s.st.driver == "postgres" {
			if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(780716)`); err != nil {
				jsonError(w, 500, "could not save run defaults")
				return
			}
		}
		query := "SELECT id FROM user_groups WHERE id=?"
		if s.st.driver == "postgres" {
			query += " FOR UPDATE"
		}
		var present int64
		if err := tx.QueryRow(s.st.bind(query), id).Scan(&present); err != nil {
			if err == sql.ErrNoRows {
				jsonError(w, 404, "organizational unit not found")
			} else {
				jsonError(w, 500, "could not read organizational unit")
			}
			return
		}
		if _, err := tx.Exec(s.st.bind("INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v"), runDefaultsOUKey(id), string(encoded)); err != nil {
			jsonError(w, 500, "could not save run defaults")
			return
		}
		if err := tx.Commit(); err != nil {
			jsonError(w, 500, "could not commit run defaults")
			return
		}
		s.recordChange(r, user, AuditGroupChange, "group", strconv.FormatInt(id, 10), map[string]any{"run_defaults": values})
	}
	effective, inherited, sources, overrides, err := s.resolveRunDefaultsOU(snap.graph, id)
	if err != nil {
		jsonError(w, 500, "could not resolve run defaults")
		return
	}
	writeJSON(w, map[string]any{"effective": effective, "inherited": inherited, "sources": sources, "overrides": overrides})
}
