package app

import "testing"

func TestMeForecastHintMatchesStrictDecision(t *testing.T) {
	s := newV1Server(t)
	for _, tc := range []struct {
		user, scope string
		external    bool
	}{{"all-reader", scopeAll, false}, {"own-reader", scopeOwn, false}, {"external-reader", scopeAll, true}, {"none-reader", scopeNone, false}} {
		seedForecastReader(t, s, tc.user, tc.scope, tc.external)
		got, ok := s.meJSON(tc.user)["forecast"].(map[string]any)
		if !ok || got["scope"] != tc.scope || got["external"] != tc.external {
			t.Fatalf("%s: navigation hint does not match strict scope: %#v", tc.user, got)
		}
		// Only an own reader gets a population name: the other scopes have nothing to name.
		if _, named := got["ou"]; named != (tc.scope == scopeOwn) {
			t.Fatalf("%s: unexpected population name presence: %#v", tc.user, got)
		}
	}
	mustAddUser(t, s, "admin", true)
	got, ok := s.meJSON("admin")["forecast"].(map[string]any)
	if !ok || got["scope"] != scopeAll || got["external"] != false || len(got) != 2 {
		t.Fatalf("admin hint: %#v", got)
	}
}

// The population line names the OU the subtree is rooted at (the reader's own unit), not the
// ancestor that happened to decide the scope: naming the ancestor would claim a wider population
// than the one the statistics cover.
func TestMeForecastHintNamesTheOwnPopulationRoot(t *testing.T) {
	s := newV1Server(t)
	parent := seedOU(t, s.st, 0, scopeOwn)
	child := seedOU(t, s.st, parent, nil)
	mustAddUser(t, s, "child-reader", false)
	if _, err := s.st.exec("UPDATE users SET group_id=? WHERE username=?", child, "child-reader"); err != nil {
		t.Fatal(err)
	}
	var childName, parentName string
	if err := s.st.queryRow("SELECT name FROM user_groups WHERE id=?", child).Scan(&childName); err != nil {
		t.Fatal(err)
	}
	if err := s.st.queryRow("SELECT name FROM user_groups WHERE id=?", parent).Scan(&parentName); err != nil {
		t.Fatal(err)
	}
	got, ok := s.meJSON("child-reader")["forecast"].(map[string]any)
	if !ok || got["scope"] != scopeOwn {
		t.Fatalf("inherited own hint: %#v", got)
	}
	if got["ou"] != childName || got["ou"] == parentName {
		t.Fatalf("population root name = %#v, want %q (not the deciding ancestor %q)", got["ou"], childName, parentName)
	}
}

// An unassigned own reader sits in the Default OU, which is the root of their population.
func TestMeForecastHintNamesDefaultForUnassignedOwnReader(t *testing.T) {
	s := newV1Server(t)
	def := s.st.DefaultGroupID()
	if _, err := s.st.exec("UPDATE user_groups SET forecast_scope=? WHERE id=?", scopeOwn, def); err != nil {
		t.Fatal(err)
	}
	mustAddUser(t, s, "unassigned", false)
	var defName string
	if err := s.st.queryRow("SELECT name FROM user_groups WHERE id=?", def).Scan(&defName); err != nil {
		t.Fatal(err)
	}
	got, ok := s.meJSON("unassigned")["forecast"].(map[string]any)
	if !ok || got["scope"] != scopeOwn || got["ou"] != defName {
		t.Fatalf("unassigned own hint: %#v (want ou %q)", got, defName)
	}
}
