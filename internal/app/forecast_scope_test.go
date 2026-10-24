package app

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestForecastSubjectSnapshotIsConsistent(t *testing.T) {
	st := pgStore(t)
	s := &Server{st: st}
	mustAddUser(t, s, "forecast-snapshot-reader", false)
	defer st.exec("DELETE FROM users WHERE username=?", "forecast-snapshot-reader")
	a := seedOU(t, st, 0, scopeNone)
	b := seedOU(t, st, 0, scopeAll)
	defer st.exec("DELETE FROM user_groups WHERE id IN (?,?)", a, b)
	if _, err := st.exec("UPDATE users SET group_id=? WHERE username=?", a, "forecast-snapshot-reader"); err != nil {
		t.Fatal(err)
	}
	// Both committed states deny. Combining the old account with the new tree would allow all.
	sub, ix, err := s.loadForecastSubjectSnapshot("forecast-snapshot-reader", func() {
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(st.bind("UPDATE users SET group_id=? WHERE username=?"), b, "forecast-snapshot-reader"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(st.bind("UPDATE user_groups SET forecast_scope=? WHERE id=?"), scopeAll, a); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(st.bind("UPDATE user_groups SET forecast_scope=? WHERE id=?"), scopeNone, b); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := resolveForecastScope(sub, ix); d.Scope != scopeNone || d.Leaf != a {
		t.Fatalf("mixed snapshots widened access: %+v", d)
	}
	if d := s.forecastAccess("forecast-snapshot-reader"); d.Scope != scopeNone || d.Leaf != b {
		t.Fatalf("next request did not see committed move: %+v", d)
	}
}

func TestOwnMembershipIsComputedInsideThePopulation(t *testing.T) {
	s := newV1Server(t)
	a := seedOU(t, s.st, 0, scopeOwn)
	b := seedOU(t, s.st, 0, scopeOwn)
	seed := func(date string, ou int64) int64 {
		t.Helper()
		id := seedGroupSet(t, s, gsSeed{Symbol: "600519", MadeOn: date, Bench: "sh000852", S0Fen: ip(10000), HorizonStd: true})
		if _, err := s.st.exec("UPDATE prediction_sets SET attributed_ou=? WHERE id=?", ou, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	member := seed("2026-10-16", a)
	pop := populationFor(forecastDecision{Scope: scopeOwn, Subtree: []int64{a}})
	load := func() []*groupSet {
		t.Helper()
		sets, err := s.st.loadForecastGroupSets(predTestRtype, "2026-10-01", "2026-10-31", pop)
		if err != nil {
			t.Fatal(err)
		}
		return sets
	}
	before := load()
	seed("2026-10-15", b)
	after := load()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("another OU changed the own population")
	}
	members, pending, cov := forecastMembership(after, time.Time{}, "sh000852")
	if len(members) != 1 || members[0].ID != member || len(pending) != 0 || cov.Duplicate != 0 {
		t.Fatalf("own membership: %+v %+v %+v", members, pending, cov)
	}
}

func TestOwnPopulationIsTheSubtreeByAttribution(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			st := newTestStore(t)
			if driver == "postgres" {
				st = pgStore(t, "prediction_sets")
			}
			s := &Server{st: st}
			mustAddUser(t, s, "forecast-pop-reader", false)
			ix := scopeTree()
			ids := []int64{}
			for i, ou := range []any{int64(3), int64(4), int64(5), nil} {
				id := seedPredictionSet(t, st, sql.NullInt64{}, "scope-"+string(rune('a'+i)))
				ids = append(ids, id)
				if _, err := st.exec("UPDATE prediction_sets SET attributed_ou=? WHERE id=?", ou, id); err != nil {
					t.Fatal(err)
				}
			}
			loadIDs := func(pop forecastPopulation) []int64 {
				t.Helper()
				sets, err := st.loadForecastGroupSets(predTestRtype, "2026-10-01", "2026-10-31", pop)
				if err != nil {
					t.Fatal(err)
				}
				out := []int64{}
				for _, set := range sets {
					out = append(out, set.ID)
				}
				return out
			}
			d := resolveForecastScope(forecastSubject{User: "forecast-pop-reader", GroupID: 3}, ix)
			if got := loadIDs(populationFor(d)); !reflect.DeepEqual(got, ids[:2]) {
				t.Fatal(got, ids[:2])
			}
			// A current account move cannot rewrite historical attribution or its population.
			if _, err := st.exec("UPDATE users SET group_id=? WHERE username=?", int64(5), "forecast-pop-reader"); err != nil {
				t.Fatal(err)
			}
			if got := loadIDs(populationFor(d)); !reflect.DeepEqual(got, ids[:2]) {
				t.Fatal(got)
			}
			ix.ous[4] = ouRow{parent: 2}
			moved := resolveForecastScope(forecastSubject{User: "forecast-pop-reader", GroupID: 3}, ix)
			if got := loadIDs(populationFor(moved)); !reflect.DeepEqual(got, ids[:1]) {
				t.Fatal(got)
			}
			if populationFor(moved).Key == populationFor(d).Key {
				t.Fatal("tree change reused population key")
			}
			if got := loadIDs(populationFor(forecastDecision{Scope: scopeNone})); len(got) != 0 {
				t.Fatal(got)
			}
			if got := loadIDs(populationFor(forecastDecision{Scope: scopeAll})); !reflect.DeepEqual(got, ids) {
				t.Fatal(got, ids)
			}
			if _, err := st.exec("DELETE FROM users WHERE username=?", "forecast-pop-reader"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestForecastSubjectReadsLiveAccount(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			st := newTestStore(t)
			if driver == "postgres" {
				st = pgStore(t)
			}
			s := &Server{st: st}
			mustAddUser(t, s, "forecast-scope-reader", false)
			defer st.exec("DELETE FROM users WHERE username=?", "forecast-scope-reader")
			ou := seedOU(t, st, 0, scopeOwn)
			defer st.exec("DELETE FROM user_groups WHERE id=?", ou)
			if _, err := st.exec("UPDATE users SET group_id=?,restricted=1 WHERE username=?", ou, "forecast-scope-reader"); err != nil {
				t.Fatal(err)
			}
			d := s.forecastAccess("forecast-scope-reader")
			if d.Scope != scopeOwn || !d.External || d.Leaf != ou || d.Source != ou {
				t.Fatal(d)
			}
			if _, err := st.exec("UPDATE users SET role='admin' WHERE username=?", "forecast-scope-reader"); err != nil {
				t.Fatal(err)
			}
			if d := s.forecastAccess("forecast-scope-reader"); d.Scope != scopeAll || d.External || d.Reason != "admin" {
				t.Fatal(d)
			}
		})
	}
}

func scopeValue(v string) *string { return &v }

func scopeTree() ouIndex {
	return ouIndex{defaultID: 1, ous: map[int64]ouRow{
		1: {scope: scopeValue(scopeNone)},
		2: {scope: scopeValue(scopeAll)},
		3: {parent: 2, scope: scopeValue(scopeOwn)},
		4: {parent: 3},
		5: {parent: 2},
		6: {parent: 1},
		7: {parent: 2, restricted: true},
	}}
}

func TestForecastScopeNearestOUWins(t *testing.T) {
	ix := scopeTree()
	for _, tc := range []struct {
		leaf, source int64
		scope        string
	}{
		{3, 3, scopeOwn}, {4, 3, scopeOwn}, {5, 2, scopeAll}, {6, 1, scopeNone},
	} {
		d := resolveForecastScope(forecastSubject{User: "reader", GroupID: tc.leaf}, ix)
		if d.Scope != tc.scope || d.Source != tc.source || d.Reason != "ou" {
			t.Fatalf("leaf %d: %+v", tc.leaf, d)
		}
	}
}

func TestForecastScopeDefaultsToNone(t *testing.T) {
	ix := scopeTree()
	for id, row := range ix.ous {
		row.scope = nil
		ix.ous[id] = row
	}
	for id := range ix.ous {
		d := resolveForecastScope(forecastSubject{User: "reader", GroupID: id}, ix)
		if d.Scope != scopeNone || d.Reason != "default_null" {
			t.Fatalf("%d: %+v", id, d)
		}
	}
	if d := resolveForecastScope(forecastSubject{}, ix); d.Scope != scopeNone || d.Reason != "anonymous" {
		t.Fatal(d)
	}
}

func TestForecastScopeUnknownValueIsNone(t *testing.T) {
	ix := scopeTree()
	ix.ous[3] = ouRow{parent: 2, scope: scopeValue("everyone")}
	d := resolveForecastScope(forecastSubject{User: "reader", GroupID: 4}, ix)
	if d.Scope != scopeNone || d.Reason != "unknown_value" || d.Source != 0 {
		t.Fatal(d)
	}
}

func TestForecastAdminAlwaysAll(t *testing.T) {
	for _, role := range roleRegistry {
		d := resolveForecastScope(forecastSubject{User: "reader", Admin: can(role.Code, PermManage), RestrictedAccount: true, GroupID: 99}, scopeTree())
		if can(role.Code, PermManage) {
			if d.Scope != scopeAll || d.External || d.Source != 0 || d.Reason != "admin" {
				t.Fatalf("%s: %+v", role.Code, d)
			}
		} else if d.Scope != scopeNone {
			t.Fatalf("%s: %+v", role.Code, d)
		}
	}
}

func TestForecastScopeDanglingParentIsNone(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		rows         map[int64]ouRow
	}{
		{"dangling", "dangling_group", map[int64]ouRow{2: {parent: 99, scope: scopeValue(scopeAll)}}},
		{"cycle", "cyclic_chain", map[int64]ouRow{2: {parent: 3, scope: scopeValue(scopeAll)}, 3: {parent: 2, restricted: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := scopeTree()
			for id, row := range tc.rows {
				ix.ous[id] = row
			}
			d := resolveForecastScope(forecastSubject{User: "reader", GroupID: 2}, ix)
			if d.Scope != scopeNone || d.Reason != tc.reason || d.Source != 0 {
				t.Fatal(d)
			}
		})
	}
	ix := scopeTree()
	for id := int64(10); id < 76; id++ {
		ix.ous[id] = ouRow{parent: id + 1, scope: scopeValue(scopeAll)}
	}
	ix.ous[76] = ouRow{}
	if d := resolveForecastScope(forecastSubject{User: "reader", GroupID: 10}, ix); d.Scope != scopeNone || d.Reason != "cyclic_chain" {
		t.Fatal(d)
	}
}

func TestForecastScopeExternalIsSticky(t *testing.T) {
	ix := scopeTree()
	ix.ous[8] = ouRow{parent: 7, scope: scopeValue(scopeAll)}
	for _, sub := range []forecastSubject{
		{User: "reader", GroupID: 8},
		{User: "reader", GroupID: 2, RestrictedAccount: true},
	} {
		d := resolveForecastScope(sub, ix)
		if d.Scope != scopeAll || !d.External {
			t.Fatal(d)
		}
	}
}

func TestForecastScopeOwnOnDefaultCoversItsChildren(t *testing.T) {
	ix := scopeTree()
	ix.ous[1] = ouRow{scope: scopeValue(scopeOwn)}
	for _, tc := range []struct {
		leaf int64
		want []int64
	}{
		{0, []int64{1, 6}}, {6, []int64{6}}, {4, []int64{4}},
	} {
		d := resolveForecastScope(forecastSubject{User: "reader", GroupID: tc.leaf}, ix)
		if d.Scope != scopeOwn || !reflect.DeepEqual(d.Subtree, tc.want) {
			t.Fatalf("%d: %+v", tc.leaf, d)
		}
	}
}

func TestForecastPopulationKeyTracksSubtree(t *testing.T) {
	ix := scopeTree()
	sub := forecastSubject{User: "reader", GroupID: 3}
	a := populationFor(resolveForecastScope(sub, ix))
	if !reflect.DeepEqual(a.OUs, []int64{3, 4}) || !strings.HasPrefix(a.Key, "ou:") {
		t.Fatal(a)
	}
	ix.ous[4] = ouRow{parent: 2}
	b := populationFor(resolveForecastScope(sub, ix))
	if a.Key == b.Key || !reflect.DeepEqual(b.OUs, []int64{3}) {
		t.Fatal(a, b)
	}
	all := populationFor(forecastDecision{Scope: scopeAll})
	if all.Key != "all" || all.OUs != nil {
		t.Fatal(all)
	}
	deny := populationFor(forecastDecision{Scope: scopeNone})
	if deny.OUs == nil || len(deny.OUs) != 0 || deny.Key == all.Key {
		t.Fatal(deny)
	}
}

func TestForecastPopulationCacheAndSeedIsolation(t *testing.T) {
	all := populationFor(forecastDecision{Scope: scopeAll})
	own := populationFor(forecastDecision{Scope: scopeOwn, Subtree: []int64{2, 3}})
	other := populationFor(forecastDecision{Scope: scopeOwn, Subtree: []int64{4}})
	base := forecastGroupCellKey("type", "2026-01-01", "2026-01-31", "sh000852", "d5")
	if got := all.cellKey("type", "2026-01-01", "2026-01-31", "sh000852", "d5"); got != base {
		t.Fatalf("all cache identity changed: %q", got)
	}
	if got := all.seedParts("type", "2026-01-01", "2026-01-31"); !reflect.DeepEqual(got, []string{"type", "2026-01-01", "2026-01-31"}) {
		t.Fatal(got)
	}
	keys := map[string]bool{}
	seeds := map[int64]bool{}
	for _, pop := range []forecastPopulation{all, own, other} {
		key := pop.cellKey("type", "2026-01-01", "2026-01-31", "sh000852", "d5")
		seed := predSeed(pop.seedParts("type", "2026-01-01", "2026-01-31")...)
		if keys[key] || seeds[seed] {
			t.Fatal("different populations shared a cache or seed identity")
		}
		keys[key], seeds[seed] = true, true
	}
}

func TestForecastScopeDeniesOnStoreError(t *testing.T) {
	for _, table := range []string{"users", "user_groups"} {
		t.Run(table, func(t *testing.T) {
			s := newV1Server(t)
			mustAddUser(t, s, "reader", false)
			if _, err := s.st.exec("DROP TABLE " + table); err != nil {
				t.Fatal(err)
			}
			d := s.forecastAccess("reader")
			if d.Scope != scopeNone || d.Reason != "error" {
				t.Fatal(d)
			}
		})
	}
}

func TestForecastSubjectRejectsMissingAndInactiveAccounts(t *testing.T) {
	s := newV1Server(t)
	mustAddUser(t, s, "reader", true)
	if _, err := s.st.exec("UPDATE users SET active=0 WHERE username=?", "reader"); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"missing", "reader"} {
		if d := s.forecastAccess(user); d.Scope != scopeNone || d.Reason != "error" {
			t.Fatalf("%s: %+v", user, d)
		}
	}
}
