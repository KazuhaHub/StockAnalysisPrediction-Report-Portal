package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runForecastReadingStageMatrix(t *testing.T, s *Server, mux http.Handler, subjects []forecastMatrixSubject, ledger []forecastMatrixSet, readable, hidden int64) {
	t.Helper()
	type probe struct {
		name, path, kind       string
		body, hidden, prefetch bool
	}
	stock := "/api/stock/" + forecastMatrixSymbol(0) + "?r=" + itoa64(readable)
	probes := []probe{
		{name: "stock", path: stock, body: true},
		{name: "prefetch", path: stock, body: true, prefetch: true},
		{name: "run", path: "/api/run/" + itoa64(readable), body: true},
		{name: "hidden-stock", path: "/api/stock/" + forecastMatrixSymbol(1) + "?r=" + itoa64(hidden), body: true, hidden: true},
		{name: "hidden-run", path: "/api/run/" + itoa64(hidden), body: true, hidden: true},
		{name: "card", path: "/api/report/" + itoa64(readable) + "/predictions?include=payload", kind: "card"},
		{name: "history", path: "/api/predictions?symbol=" + forecastMatrixSymbol(0) + "&include=payload", kind: "list"},
		{name: "v1-card", path: "/api/v1/reports/" + itoa64(readable) + "/predictions?include=payload", kind: "card"},
		{name: "v1-history", path: "/api/v1/predictions?symbol=" + forecastMatrixSymbol(0) + "&include=payload", kind: "list"},
		{name: "record", path: "/api/forecast/groups", kind: "groups"},
		{name: "members", path: "/api/forecast/groups/members?label=2025-06&checkpoint=d5&limit=200", kind: "members"},
		{name: "me", path: "/api/me", kind: "me"},
	}
	for _, stage := range []string{"off", "card", "full", "unknown"} {
		t.Run("stage-"+stage, func(t *testing.T) {
			if err := s.st.SetSetting(setForecastReader, stage); err != nil {
				t.Fatal(err)
			}
			for _, who := range subjects {
				t.Run(who.name, func(t *testing.T) {
					for _, p := range probes {
						t.Run(p.name, func(t *testing.T) {
							r := httptest.NewRequest("GET", p.path, nil)
							if who.user != "" {
								r = sessionForPath(t, s, who.user, "", p.path)
							} else if who.token != "" {
								r.Header.Set("Authorization", "Bearer "+who.token)
							}
							if p.prefetch {
								r.Header.Set("X-Prefetch", "1")
							}
							w := httptest.NewRecorder()
							mux.ServeHTTP(w, r)
							want, code := 200, ""
							v1 := strings.HasPrefix(p.path, "/api/v1/")
							card := p.kind == "card" || p.kind == "list"
							switch {
							case who.user == "" && !((who.name == "machine" || who.bridge) && v1):
								want = 401
							case who.name == "Dangling" && p.kind != "me":
								want, code = 503, "enrolment_policy_unavailable"
							case who.walled && p.kind != "me":
								want, code = 403, "enrolment_required"
							case who.scope == scopeNone && !p.body && p.kind != "me":
								want, code = 403, "forecast_unavailable"
							case who.user != "" && !who.admin && card && (stage == "off" || stage == "unknown"):
								want, code = 403, "forecast_unavailable"
							case who.external && p.hidden:
								want, code = 404, "no_reports_for_symbol"
								if p.name == "hidden-run" {
									code = "run_not_found"
								}
							}
							if w.Code != want || code != "" && forecastMatrixErrorCode(t, w.Body.Bytes()) != code {
								t.Fatalf("got %d %s; want %d %s", w.Code, w.Body.String(), want, code)
							}
							if want != 200 {
								return
							}
							var out map[string]any
							if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
								t.Fatal(err)
							}
							if p.kind == "me" {
								wantStage := stage
								if stage == "unknown" || who.scope == scopeNone {
									wantStage = "off"
								}
								if who.admin {
									wantStage = "full"
								}
								if out["forecast_reader"] != wantStage || out["forecast"].(map[string]any)["scope"] != who.scope {
									t.Fatalf("incorrect reading/record hints: %v", out)
								}
								return
							}
							assertForecastMatrixShape(t, out, who, p.kind, ledger)
							if !p.body {
								return
							}
							id := readable
							if p.hidden {
								id = hidden
							}
							if out["rep"].(map[string]any)["id"] != float64(id) {
								t.Fatalf("reading stage changed the selected body: %v", out["rep"])
							}
							enabled := who.admin || who.scope != scopeNone && stage == "full"
							prediction, exists := out["prediction"]
							if exists != (enabled && !p.prefetch) {
								t.Fatalf("summary presence=%v, stage=%s subject=%s prefetch=%v", exists, stage, who.name, p.prefetch)
							}
							if exists {
								summary := prediction.(map[string]any)
								var expectedSet int64
								for _, set := range ledger {
									if set.report == id {
										expectedSet = set.id
										break
									}
								}
								if summary["has_prediction"] != true || summary["display_set_id"] != float64(expectedSet) {
									t.Fatalf("summary did not use this edition's accepted set: %v", summary)
								}
								internalAll := !who.external && who.scope == scopeAll
								_, membership := summary["membership"]
								_, counted := summary["counted_set_id"]
								if membership != internalAll || counted != internalAll {
									t.Fatalf("reading membership presence mismatch: %v", summary)
								}
							}
							if timeline, ok := out["timeline"].([]any); ok {
								if len(timeline) != 2 {
									t.Fatalf("stage changed timeline dates: %v", timeline)
								}
								for _, raw := range timeline {
									day := raw.(map[string]any)
									predicted, exists := day["predicted"]
									if exists != enabled || exists && predicted != true {
										t.Fatalf("stage=%s subject=%s timeline flag=%v", stage, who.name, day)
									}
								}
							}
						})
					}
				})
			}
		})
	}
}
