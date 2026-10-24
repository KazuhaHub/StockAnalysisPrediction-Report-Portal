package app

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestReadMembershipProjectionSurvivesAccessIntegration(t *testing.T) {
	s := newV1Server(t)
	report, member := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-01-06", Bench: "sh000852"}, "Current report")
	read := func(view forecastPredictionView) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/?include=payload", nil)
		r.SetPathValue("id", itoa64(report))
		s.serveReportPredictionsView(w, r, view)
		if w.Code != 200 {
			t.Fatalf("read = %d %s", w.Code, w.Body.String())
		}
		return append([]byte(nil), w.Body.Bytes()...)
	}
	for _, view := range []forecastPredictionView{{}, {reader: true}} {
		var out map[string]any
		if err := json.Unmarshal(read(view), &out); err != nil {
			t.Fatal(err)
		}
		if out["counted_set_id"] != float64(member) || out["display_set_id"] != float64(member) {
			t.Fatalf("unrestricted identity = %v", out)
		}
		if set := out["sets"].([]any)[0].(map[string]any); set["membership"] != membershipMember {
			t.Fatalf("unrestricted membership = %v", set)
		}
	}
	protected := []forecastPredictionView{{reader: true, noMembership: true}, {reader: true, protected: true}, {protected: true}}
	before := make([][]byte, len(protected))
	for i, view := range protected {
		before[i] = read(view)
	}
	seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-01-05", Bench: "sh000852"}, "Another earlier report")
	for i, view := range protected {
		after := read(view)
		if !bytes.Equal(before[i], after) {
			t.Fatalf("protected response changed with another report's earlier decision:\nbefore %s\nafter %s", before[i], after)
		}
		if bytes.Contains(after, []byte(`"membership"`)) || bytes.Contains(after, []byte(`"counted_set_id"`)) {
			t.Fatalf("protected membership disclosure: %s", after)
		}
	}
	var global map[string]any
	if err := json.Unmarshal(read(forecastPredictionView{}), &global); err != nil {
		t.Fatal(err)
	}
	if global["counted_set_id"] != float64(0) || global["sets"].([]any)[0].(map[string]any)["membership"] != membershipDuplicate {
		t.Fatalf("global positive control did not change: %v", global)
	}
}

func TestPurgedExternalReaderAliasesStillOmitVolume(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "external", scopeAll, true)
	if _, err := s.st.exec(`INSERT INTO prediction_purges(rtype,cohort,purged_at,sets_removed) VALUES(?,?,?,?)`, predTestRtype, "2025-06", "2026-10-01T00:00:00Z", 999); err != nil {
		t.Fatal(err)
	}
	for _, members := range []bool{false, true} {
		w := httptest.NewRecorder()
		if members {
			s.apiReaderForecastGroupMembers(w, httptest.NewRequest("GET", "/?label=2025-06&checkpoint=d5&population=all", nil), "external")
		} else {
			s.apiReaderForecastGroups(w, httptest.NewRequest("GET", "/", nil), "external")
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 {
			t.Fatalf("purged reader = %d %s", w.Code, w.Body.String())
		}
		forecastMatrixForbid(t, out, "$", []string{"coverage", "unrecorded", "n", "total", "resolved", "refused", "corp_action", "sets_removed"})
		if members {
			if out["state"] != "purged" || out["purged_at"] != "2026-10-01T00:00:00Z" {
				t.Fatalf("purge metadata lost: %v", out)
			}
		} else {
			found := false
			for _, raw := range out["groups"].([]any) {
				group := raw.(map[string]any)
				if group["label"] == "2025-06" {
					found = true
					if group["purged_at"] != "2026-10-01T00:00:00Z" {
						t.Fatal("group purge timestamp lost")
					}
				}
			}
			if !found {
				t.Fatal("retention-only group lost")
			}
		}
	}
}
