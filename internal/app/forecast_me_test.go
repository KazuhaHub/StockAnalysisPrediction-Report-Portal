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
		if !ok || len(got) != 2 || got["scope"] != tc.scope || got["external"] != tc.external {
			t.Fatalf("%s: navigation hint does not match strict scope: %#v", tc.user, got)
		}
	}
	mustAddUser(t, s, "admin", true)
	got, ok := s.meJSON("admin")["forecast"].(map[string]any)
	if !ok || got["scope"] != scopeAll || got["external"] != false {
		t.Fatalf("admin hint: %#v", got)
	}
}
