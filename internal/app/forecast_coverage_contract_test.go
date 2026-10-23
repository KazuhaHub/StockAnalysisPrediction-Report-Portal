package app

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestCoverageOmitsRetiredRefusalButKeepsCheckpointRefusals(t *testing.T) {
	s := newV1Server(t)
	sampleMembers(t, s, 35, func(i int, g *gsSeed) {
		if i == 0 {
			mark := g.Marks["d5"]
			mark.Status = markRefusedFinal
			g.Marks["d5"] = mark
		}
	})
	w := httptest.NewRecorder()
	s.apiForecastGroups(w, httptest.NewRequest("GET", "/?mode=custom&from=2025-06-01&to=2025-06-30&group=month", nil), "admin")
	var out struct {
		Groups []struct {
			Coverage map[string]any
			Cells    map[string]map[string]any
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(out.Groups) != 1 {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
	group := out.Groups[0]
	if _, present := group.Coverage["refused"]; present {
		t.Fatal("coverage still exposes its never-populated refusal bucket")
	}
	if group.Coverage["reports"] != float64(35) || group.Coverage["member"] != float64(35) {
		t.Fatalf("coverage arithmetic changed: %v", group.Coverage)
	}
	if group.Cells["d5"]["state"] != "mature" || group.Cells["d5"]["refused"] != float64(1) {
		t.Fatalf("checkpoint refusal was removed or changed: %v", group.Cells["d5"])
	}
}
