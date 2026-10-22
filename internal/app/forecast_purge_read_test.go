package app

import (
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPurgedMonthBypassesCacheAcrossProcesses(t *testing.T) {
	writer := routeTableServer(t)
	path := filepath.Join(t.TempDir(), "shared.db")
	writerStore, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerStore.Close() })
	readerStore, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readerStore.Close() })
	writer.st = writerStore
	reader := &Server{st: readerStore}
	sampleMembers(t, writer, 35, nil)
	before := findGroup(t, doGroups(t, reader, "/?mode=month"), "2025-06")
	if before.Cells["d5"].State != "mature" {
		t.Fatal("positive control did not cache a mature month")
	}
	if n, err := writer.st.DeletePredictionsBefore(time.Now().AddDate(10, 0, 0)); err != nil || n != 35 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	after := findGroup(t, doGroups(t, reader, "/?mode=month"), "2025-06")
	if after.PurgedAt == "" || after.Cells["d5"].State != "purged" || after.Cells["d5"].N != nil {
		t.Fatalf("purged month reused statistics: %+v", after)
	}
	drawer := doMembers(t, reader, "/?mode=month&label=2025-06&checkpoint=d5")
	if drawer.State != "purged" || drawer.PurgedAt == "" || len(drawer.Members) != 0 {
		t.Fatalf("purged drawer = %+v", drawer)
	}
	custom := doGroups(t, reader, "/?mode=custom&from=2025-06-01&to=2025-06-30&group=month")
	if len(custom.ExcludedMonths) != 1 || custom.ExcludedMonths[0].Cohort != "2025-06" || custom.Groups[0].Cells["d5"].State == "mature" {
		t.Fatalf("custom range did not disclose exclusion: %+v", custom)
	}
	// A later write must not resurrect a marked month. A mixed range still computes and lists
	// its surviving population, including when both months contain the same symbols.
	sampleMembers(t, writer, 35, nil)
	sampleMembers(t, writer, 35, func(_ int, g *gsSeed) { g.MadeOn = "2025-07-10" })
	target := "/?mode=custom&from=2025-06-01&to=2025-07-31&group=all"
	mixed := doGroups(t, reader, target)
	if len(mixed.Groups) != 1 || mixed.Groups[0].Coverage.Reports != 35 || len(mixed.ExcludedMonths) != 1 {
		t.Fatalf("mixed population = %+v", mixed)
	}
	kept, err := reader.st.loadForecastGroupSets(predTestRtype, "2025-07-01", "2025-07-31")
	if err != nil {
		t.Fatal(err)
	}
	members, pending, _ := forecastMembership(kept, time.Time{}, "")
	expected := computeForecastCell(members, pending, predTestRtype, "2025-06-01", "2025-07-31", "d5", reader.forecastNow().In(chinaZone).Format("2006-01-02"))
	if !reflect.DeepEqual(mixed.Groups[0].Cells["d5"], expected) {
		t.Fatal("mixed range changed the surviving population's metrics")
	}
	mixedDrawer := doMembers(t, reader, target+"&checkpoint=d5&label="+url.QueryEscape(mixed.Groups[0].Label))
	if len(mixedDrawer.Members) != 35 || !reflect.DeepEqual(mixedDrawer.ExcludedMonths, mixed.ExcludedMonths) {
		t.Fatalf("mixed drawer population/exclusions = %+v", mixedDrawer)
	}
	for _, member := range mixedDrawer.Members {
		if member.MadeOn != "2025-07-10" {
			t.Fatalf("resurrected purged member: %+v", member)
		}
	}
}

func TestPurgeReadFailureNeverServesCachedStatistics(t *testing.T) {
	s := routeTableServer(t)
	sampleMembers(t, s, 35, nil)
	_ = doGroups(t, s, "/?mode=month")
	if _, err := s.st.exec(`DROP TABLE prediction_purges`); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/?mode=month", "/?mode=custom&from=2025-06-01&to=2025-06-30&group=month"} {
		r := httptest.NewRequest("GET", target, nil)
		w := httptest.NewRecorder()
		s.apiForecastGroups(w, r, "admin")
		if w.Code != 500 {
			t.Fatalf("groups returned %d after marker failure: %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.apiForecastGroupMembers(w, httptest.NewRequest("GET", "/?mode=month&label=2025-06&checkpoint=d5", nil), "admin")
	if w.Code != 500 {
		t.Fatalf("drawer returned %d after marker failure: %s", w.Code, w.Body.String())
	}
}
