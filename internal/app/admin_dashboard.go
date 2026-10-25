package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

const dashboardSetting = "admin_dashboard_layout"

type dashboardBlock struct {
	ID      string          `json:"id"`
	Visible bool            `json:"visible"`
	Width   int             `json:"width"`
	Metrics []string        `json:"metrics,omitempty"`
	Chart   *dashboardChart `json:"chart,omitempty"`
}
type dashboardChart struct {
	Range      int    `json:"range"`
	Interval   string `json:"interval"`
	ShowValues bool   `json:"showValues"`
	Cumulative bool   `json:"cumulative"`
	TopN       int    `json:"topN"`
}

func validDashboardChart(b dashboardBlock) bool {
	if b.Chart == nil {
		return true
	}
	if b.ID != "trend" && b.ID != "report-bars" && b.ID != "kinds" && b.ID != "report-donut" {
		return false
	}
	c := b.Chart
	return (c.Range == 7 || c.Range == 30 || c.Range == 90 || c.Range == 365) &&
		(c.Interval == "auto" || c.Interval == "day" || c.Interval == "week" || c.Interval == "month") &&
		(c.TopN == 5 || c.TopN == 10 || c.TopN == 15)
}

type dashboardLayout struct {
	Blocks   []dashboardBlock `json:"blocks"`
	Revision string           `json:"revision"`
}

func dashboardDefaults() []dashboardBlock {
	return []dashboardBlock{
		{ID: "reports", Visible: true, Width: 2}, {ID: "queue", Visible: true, Width: 2}, {ID: "forecast", Visible: true, Width: 2},
		{ID: "trend", Visible: true, Width: 3}, {ID: "kinds", Visible: true, Width: 3}, {ID: "report-bars", Visible: false, Width: 3}, {ID: "report-donut", Visible: false, Width: 3}, {ID: "shortcuts", Visible: true, Width: 6}, {ID: "directory", Visible: true, Width: 6},
	}
}
func dashboardRevision(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

var dashboardShortcutID = regexp.MustCompile(`^shortcut:[a-z][a-z0-9-]{0,31}$`)

func dashboardMetricKeys(id string) []string {
	switch id {
	case "reports":
		return []string{"total", "today", "yesterday", "recent", "month", "year"}
	case "queue":
		return []string{"running_rows", "running", "waiting", "scheduled", "budget", "reserved", "done_today"}
	case "forecast":
		return []string{"due_sets", "pending", "refused", "unsettled_baseline"}
	}
	return nil
}
func validDashboardMetrics(b dashboardBlock) bool {
	if b.Metrics == nil {
		return true
	}
	allowed := dashboardMetricKeys(b.ID)
	if len(b.Metrics) == 0 || len(b.Metrics) > len(allowed) {
		return false
	}
	seen := map[string]bool{}
	for _, key := range b.Metrics {
		valid := false
		for _, candidate := range allowed {
			valid = valid || candidate == key
		}
		if !valid || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
func validDashboardBlock(b dashboardBlock) bool {
	known := dashboardShortcutID.MatchString(b.ID)
	for _, item := range dashboardDefaults() {
		known = known || item.ID == b.ID
	}
	return known && validDashboardChart(b) && validDashboardMetrics(b) && (b.Width == 2 || b.Width == 3 || b.Width == 6 || (b.Width == 1 && strings.HasPrefix(b.ID, "shortcut:")))
}
func normalizedDashboard(raw string) dashboardLayout {
	out := dashboardLayout{Revision: dashboardRevision(raw)}
	_ = json.Unmarshal([]byte(raw), &out)
	out.Revision = dashboardRevision(raw)
	seen := map[string]bool{}
	blocks := []dashboardBlock{}
	for _, b := range out.Blocks {
		if validDashboardBlock(b) && !seen[b.ID] {
			blocks = append(blocks, b)
			seen[b.ID] = true
		}
	}
	for _, b := range dashboardDefaults() {
		if !seen[b.ID] {
			blocks = append(blocks, b)
		}
	}
	out.Blocks = blocks
	return out
}
func (s *Server) apiDashboardLayout(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, normalizedDashboard(s.st.GetSetting(dashboardSetting, "")))
}
func (s *Server) apiDashboardLayoutSave(w http.ResponseWriter, r *http.Request, user string) {
	var in dashboardLayout
	if err := readJSONLimit(r, &in, 8192); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid dashboard layout")
		return
	}
	known := map[string]bool{}
	for _, b := range dashboardDefaults() {
		known[b.ID] = true
	}
	seen := map[string]bool{}
	visible := false
	for _, b := range in.Blocks {
		if !validDashboardBlock(b) || seen[b.ID] {
			jsonError(w, http.StatusBadRequest, "invalid dashboard block")
			return
		}
		seen[b.ID] = true
		visible = visible || b.Visible
	}
	complete := true
	for id := range known {
		complete = complete && seen[id]
	}
	if !complete || len(in.Blocks) > 64 || !visible {
		jsonError(w, http.StatusBadRequest, "a complete layout with at least one visible block is required")
		return
	}
	raw := s.st.GetSetting(dashboardSetting, "")
	if in.Revision != dashboardRevision(raw) {
		jsonErrorCode(w, http.StatusConflict, "dashboard_layout_changed", "dashboard layout changed")
		return
	}
	in.Revision = ""
	encoded, err := json.Marshal(in)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not encode dashboard layout")
		return
	}
	result, err := s.st.exec("INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v WHERE meta.v=?", dashboardSetting, string(encoded), raw)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not save dashboard layout")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not verify dashboard layout save")
		return
	}
	if changed == 0 {
		jsonErrorCode(w, http.StatusConflict, "dashboard_layout_changed", "dashboard layout changed")
		return
	}
	s.st.WriteAudit(AuditEntry{Actor: user, Action: "dashboard.layout.update", TargetType: "settings", TargetID: dashboardSetting})
	writeJSON(w, normalizedDashboard(string(encoded)))
}
