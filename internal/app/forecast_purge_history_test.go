package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// The own-scoped purge history contract (ADR 0039, purge ownership evidence): an own reader is told
// about a purged month only when the evidence table says its current subtree owned sets in it. Every
// other reader keeps the global history, and a set removed by retention never reappears for anyone.

const purgeHistoryStamp = "2026-10-01T00:00:00Z"

// purgeHistoryDrivers runs one scenario on SQLite and, when configured, on Postgres.
func purgeHistoryDrivers(t *testing.T, run func(t *testing.T, env *purgeHistoryEnv)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { run(t, newPurgeHistoryEnv(t, newTestStore(t))) })
	t.Run("postgres", func(t *testing.T) {
		st := pgStore(t, "prediction_marks", "prediction_claims", "prediction_sets", "prediction_purges", "users", "user_groups")
		run(t, newPurgeHistoryEnv(t, st))
	})
}

type purgeHistoryEnv struct {
	s    *Server
	a, b int64 // two sibling OUs at own scope; neither contains the other
}

// ensurePurgeEvidenceTable gives a test the ownership evidence table an upgraded database has. An
// own-scoped record read refuses to answer without it, so every test that reads as an own reader
// states that it assumes the upgrade has run; the one test of the missing table drops it again.
func ensurePurgeEvidenceTable(t *testing.T, st *Store) {
	t.Helper()
	if err := createPredictionPurgeOwners(st.poolExec()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.exec(`DROP TABLE IF EXISTS prediction_purge_owners`) })
}

func newPurgeHistoryEnv(t *testing.T, st *Store) *purgeHistoryEnv {
	t.Helper()
	s := &Server{st: st}
	s.forecastEval.now = func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }
	ensurePurgeEvidenceTable(t, st)
	if _, err := st.exec(`DELETE FROM prediction_purge_owners`); err != nil {
		t.Fatal(err)
	}
	env := &purgeHistoryEnv{s: s}
	env.a = seedForecastReader(t, s, "reader-a", scopeOwn, false)
	env.b = seedForecastReader(t, s, "reader-b", scopeOwn, false)
	seedForecastReader(t, s, "reader-all", scopeAll, false)
	seedForecastReader(t, s, "reader-external", scopeAll, true)
	return env
}

// purge records what a retention pass leaves behind for one month: the global marker and, for each
// OU named, the ownership evidence snapshotted before the sets were deleted.
func (e *purgeHistoryEnv) purge(t *testing.T, month string, owners ...int64) {
	t.Helper()
	if _, err := e.s.st.exec(`INSERT INTO prediction_purges(rtype,cohort,purged_at,sets_removed) VALUES(?,?,?,1)`, predTestRtype, month, purgeHistoryStamp); err != nil {
		t.Fatal(err)
	}
	for _, ou := range owners {
		if _, err := e.s.st.exec(`INSERT INTO prediction_purge_owners(rtype,cohort,attributed_ou) VALUES(?,?,?)`, predTestRtype, month, ou); err != nil {
			t.Fatal(err)
		}
	}
}

// sets seeds n mature-capable sets in a month, attributed to ou.
func (e *purgeHistoryEnv) sets(t *testing.T, month string, n int, ou int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		id := seedGroupSet(t, e.s, gsSeed{
			Symbol: symbolFor(i), MadeOn: month + "-10",
			S0Fen: ip(10000), TerminalFen: ip(int64(11000 + i*10)), EntryFen: ip(10500),
			BearFen: ip(8000), BullFen: ip(14000), PProfitBp: bp(int32(6000 + i%10)),
			Marks: map[string]gsMark{
				"d5": {Status: markResolved, Close: ip(int64(10600 + i*10)), Low: ip(10300),
					RetBp: bp(int32(200 + i*5)), BenchRetBp: bp(100)},
			},
		})
		if _, err := e.s.st.exec(`UPDATE prediction_sets SET attributed_ou=? WHERE id=?`, ou, id); err != nil {
			t.Fatal(err)
		}
	}
}

// history builds the shared fixture. Months, oldest first:
//
//	2025-04 purged, evidence for B only        (before A's own range)
//	2025-05 purged, no evidence at all         (legacy marker, before A's own range)
//	2025-06 purged, evidence for A
//	2025-07 purged, evidence for B; A holds sets that were written after the purge
//	2025-08 live sets for A
//	2025-09 purged, no evidence at all         (legacy marker inside A's range)
func (e *purgeHistoryEnv) history(t *testing.T) {
	t.Helper()
	e.purge(t, "2025-04", e.b)
	e.purge(t, "2025-05")
	e.purge(t, "2025-06", e.a)
	e.purge(t, "2025-07", e.b)
	e.purge(t, "2025-09")
	e.sets(t, "2025-07", 35, e.a)
	e.sets(t, "2025-08", 35, e.a)
}

func (e *purgeHistoryEnv) get(t *testing.T, members bool, user, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", target, nil)
	if members {
		e.s.apiReaderForecastGroupMembers(w, r, user)
	} else {
		e.s.apiReaderForecastGroups(w, r, user)
	}
	return w
}

func (e *purgeHistoryEnv) groups(t *testing.T, user, query string) forecastReaderGroupsResponse {
	t.Helper()
	w := e.get(t, false, user, "/api/forecast/groups"+query)
	if w.Code != http.StatusOK {
		t.Fatalf("%s groups%s: %d %s", user, query, w.Code, w.Body.String())
	}
	var out forecastReaderGroupsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *purgeHistoryEnv) drawer(t *testing.T, user, query string) (int, forecastReaderMembersResponse, string) {
	t.Helper()
	w := e.get(t, true, user, "/api/forecast/groups/members"+query)
	var out forecastReaderMembersResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, out, w.Body.String()
}

func readerGroupLabels(resp forecastReaderGroupsResponse) map[string]forecastReaderGroup {
	out := map[string]forecastReaderGroup{}
	for _, g := range resp.Groups {
		out[g.Label] = g
	}
	return out
}

func purgedCohorts(months []forecastPurgedMonth) []string {
	out := []string{}
	for _, m := range months {
		out = append(out, m.Cohort)
	}
	return out
}

func TestOwnReadersAreToldOnlyAboutPurgedMonthsTheirOUsOwnedSetsIn(t *testing.T) {
	purgeHistoryDrivers(t, func(t *testing.T, env *purgeHistoryEnv) {
		env.history(t)

		t.Run("record of A", func(t *testing.T) {
			got := env.groups(t, "reader-a", "")
			if got.Range.From != "2025-06-01" {
				t.Fatalf("A's range starts at %s; only its evidenced June may extend it", got.Range.From)
			}
			groups := readerGroupLabels(got)
			if g, ok := groups["2025-06"]; !ok || g.PurgedAt != purgeHistoryStamp || g.Cells["d5"].State != "purged" {
				t.Fatalf("A's evidenced purge is not disclosed: %+v", g)
			}
			if g, ok := groups["2025-08"]; !ok || g.PurgedAt != "" || g.Cells["d5"].State != "mature" {
				t.Fatalf("A's live month changed: %+v", g)
			}
			for _, month := range []string{"2025-04", "2025-05", "2025-07", "2025-09"} {
				if g, ok := groups[month]; ok {
					t.Fatalf("A is shown purged-month %s it has no evidence for, or data from it: %+v", month, g)
				}
			}
			for _, g := range got.Groups {
				if g.PurgedAt != "" && g.Label != "2025-06" {
					t.Fatalf("A is told about purge of %s", g.Label)
				}
			}
		})

		t.Run("record of B", func(t *testing.T) {
			got := env.groups(t, "reader-b", "")
			// B owns no live set, so only its two evidenced months can form its range.
			if got.Range.From != "2025-04-01" {
				t.Fatalf("B's range starts at %s", got.Range.From)
			}
			groups := readerGroupLabels(got)
			for _, month := range []string{"2025-04", "2025-07"} {
				if g, ok := groups[month]; !ok || g.PurgedAt != purgeHistoryStamp {
					t.Fatalf("B's evidenced purge %s is not disclosed: %+v", month, g)
				}
			}
			for _, month := range []string{"2025-05", "2025-06", "2025-08", "2025-09"} {
				if g, ok := groups[month]; ok && (g.PurgedAt != "" || g.Cells["d5"].State == "purged") {
					t.Fatalf("B is told about purge of %s: %+v", month, g)
				}
			}
		})

		t.Run("all readers and the admin alias keep the global history", func(t *testing.T) {
			all := env.groups(t, "reader-all", "")
			if all.Range.From != "2025-04-01" {
				t.Fatalf("global range starts at %s", all.Range.From)
			}
			groups := readerGroupLabels(all)
			for _, month := range []string{"2025-04", "2025-05", "2025-06", "2025-07", "2025-09"} {
				if g, ok := groups[month]; !ok || g.PurgedAt != purgeHistoryStamp || g.Cells["d5"].State != "purged" {
					t.Fatalf("global purge of %s is not disclosed to an all reader: %+v", month, g)
				}
			}
			if g := groups["2025-08"]; g.PurgedAt != "" || g.Cells["d5"].State != "mature" {
				t.Fatalf("live month changed for all: %+v", g)
			}
			w := httptest.NewRecorder()
			env.s.apiForecastGroups(w, httptest.NewRequest("GET", "/api/admin/forecast/groups", nil), "admin")
			var admin map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &admin); err != nil {
				t.Fatal(err)
			}
			delete(admin, "unrecorded")
			var reader map[string]any
			if err := json.Unmarshal(env.get(t, false, "reader-all", "/api/forecast/groups").Body.Bytes(), &reader); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(admin, reader) {
				t.Fatal("the admin alias and an all reader diverged")
			}
			ext := env.groups(t, "reader-external", "")
			if ext.Range.From != "2025-04-01" || len(readerGroupLabels(ext)) != len(groups) {
				t.Fatalf("external-all history changed: %s %d", ext.Range.From, len(ext.Groups))
			}
		})

		const custom = "?mode=custom&from=2025-05-01&to=2025-09-30&group=month"
		t.Run("custom exclusions", func(t *testing.T) {
			for _, tc := range []struct {
				user string
				want []string
			}{
				{"reader-a", []string{"2025-06"}},
				{"reader-b", []string{"2025-07"}},
				{"reader-all", []string{"2025-05", "2025-06", "2025-07", "2025-09"}},
			} {
				got := env.groups(t, tc.user, custom)
				if names := purgedCohorts(got.ExcludedMonths); !reflect.DeepEqual(names, tc.want) {
					t.Fatalf("%s excluded months = %v; want %v", tc.user, names, tc.want)
				}
				groups := readerGroupLabels(got)
				// A set written after a purge stays out of every population, disclosed or not.
				if g := groups["2025-07"]; g.Coverage == nil || g.Coverage.Reports != 0 {
					t.Fatalf("%s sees data from a purged month: %+v", tc.user, g.Coverage)
				}
			}
			if g := readerGroupLabels(env.groups(t, "reader-a", custom))["2025-08"]; g.Coverage == nil || g.Coverage.Reports != 35 {
				t.Fatalf("A's live month changed in a custom range: %+v", g)
			}
		})

		t.Run("drawers", func(t *testing.T) {
			code, june, body := env.drawer(t, "reader-a", "?label=2025-06&checkpoint=d5")
			if code != http.StatusOK || june.State != "purged" || june.PurgedAt != purgeHistoryStamp || len(june.Members) != 0 {
				t.Fatalf("A's evidenced drawer: %d %s", code, body)
			}
			for _, label := range []string{"2025-05", "2025-07", "2025-09"} {
				code, _, body := env.drawer(t, "reader-a", "?label="+label+"&checkpoint=d5")
				if code != http.StatusNotFound || !hasCode([]byte(body), "label_not_found") {
					t.Fatalf("A's drawer for undisclosed purge %s: %d %s", label, code, body)
				}
			}
			code, aug, body := env.drawer(t, "reader-a", "?label=2025-08&checkpoint=d5&limit=200")
			if code != http.StatusOK || aug.State != "mature" || len(aug.Members) != 35 {
				t.Fatalf("A's live drawer: %d %s", code, body)
			}
			code, jul, body := env.drawer(t, "reader-all", "?label=2025-07&checkpoint=d5")
			if code != http.StatusOK || jul.State != "purged" || jul.PurgedAt != purgeHistoryStamp {
				t.Fatalf("all reader's drawer lost the global purge: %d %s", code, body)
			}
			// A custom drawer lists the exclusions inside the opened group only.
			q := "?mode=custom&from=2025-05-01&to=2025-09-30&group=month&label=2025-07&checkpoint=d5"
			for user, want := range map[string][]string{"reader-a": {}, "reader-b": {"2025-07"}, "reader-all": {"2025-07"}} {
				code, drawer, body := env.drawer(t, user, q)
				if code != http.StatusOK || len(drawer.Members) != 0 {
					t.Fatalf("%s custom drawer: %d %s", user, code, body)
				}
				if names := purgedCohorts(drawer.ExcludedMonths); !reflect.DeepEqual(names, want) {
					t.Fatalf("%s drawer exclusions = %v; want %v", user, names, want)
				}
			}
		})
	})
}

func TestOwnReadsFailClosedWhenPurgeEvidenceStorageIsMissing(t *testing.T) {
	purgeHistoryDrivers(t, func(t *testing.T, env *purgeHistoryEnv) {
		env.history(t)
		if _, err := env.s.st.exec(`DROP TABLE prediction_purge_owners`); err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{"/api/forecast/groups", "/api/forecast/groups?mode=custom&from=2025-05-01&to=2025-09-30&group=month"} {
			w := env.get(t, false, "reader-a", target)
			if w.Code != http.StatusServiceUnavailable || !hasCode(w.Body.Bytes(), "forecast_purge_history_unavailable") {
				t.Fatalf("own groups %s without evidence storage: %d %s", target, w.Code, w.Body.String())
			}
		}
		code, _, body := env.drawer(t, "reader-a", "?label=2025-08&checkpoint=d5")
		if code != http.StatusServiceUnavailable || !hasCode([]byte(body), "forecast_purge_history_unavailable") {
			t.Fatalf("own drawer without evidence storage: %d %s", code, body)
		}
		// Reads that never needed ownership evidence must not start to depend on it.
		for _, user := range []string{"reader-all", "reader-external"} {
			got := env.groups(t, user, "")
			if got.Range.From != "2025-04-01" || readerGroupLabels(got)["2025-06"].PurgedAt != purgeHistoryStamp {
				t.Fatalf("%s lost the global history without evidence storage: %+v", user, got.Range)
			}
		}
		if code, drawer, body := env.drawer(t, "reader-all", "?label=2025-06&checkpoint=d5"); code != http.StatusOK || drawer.State != "purged" {
			t.Fatalf("all drawer without evidence storage: %d %s", code, body)
		}
		w := httptest.NewRecorder()
		env.s.apiForecastGroups(w, httptest.NewRequest("GET", "/api/admin/forecast/groups", nil), "admin")
		if w.Code != http.StatusOK {
			t.Fatalf("admin alias without evidence storage: %d %s", w.Code, w.Body.String())
		}
	})
}

// A cell cached before a purge by another process must not answer a custom range that now contains
// the purged month, even when the purge is not one the reader is told about: the cache identity
// follows what actually filters the sets, not what is disclosed.
func TestACellCachedBeforeAnUndisclosedPurgeIsNotReusedByACustomRange(t *testing.T) {
	purgeHistoryDrivers(t, func(t *testing.T, env *purgeHistoryEnv) {
		env.sets(t, "2025-06", 35, env.a)
		env.sets(t, "2025-08", 35, env.a)
		record := env.groups(t, "reader-a", "")
		if g := readerGroupLabels(record)["2025-06"]; g.Cells["d5"].State != "mature" {
			t.Fatalf("positive control did not cache A's June: %+v", g)
		}
		// Another process retires June. It leaves a global marker and no evidence for A, as every
		// marker written before the evidence table existed does.
		if _, err := env.s.st.exec(`DELETE FROM prediction_sets WHERE cohort='2025-06'`); err != nil {
			t.Fatal(err)
		}
		env.purge(t, "2025-06")
		custom := env.groups(t, "reader-a", "?mode=custom&from=2025-06-01&to=2025-06-30&group=month")
		if len(custom.ExcludedMonths) != 0 {
			t.Fatalf("the purge was disclosed: %+v", custom.ExcludedMonths)
		}
		cell := readerGroupLabels(custom)["2025-06"].Cells["d5"]
		if cell == nil || cell.State == "mature" || cell.N != nil && *cell.N != 0 {
			t.Fatalf("a cell cached before the purge answered the custom range: %+v", cell)
		}
		// The monthly drawer must not describe the retired month from the same stale cell.
		code, _, body := env.drawer(t, "reader-a", "?label=2025-06&checkpoint=d5")
		if code != http.StatusNotFound {
			t.Fatalf("drawer served a retired, undisclosed month: %d %s", code, body)
		}
	})
}
