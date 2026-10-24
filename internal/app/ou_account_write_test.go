package app

import "testing"

func TestPostgresOUAccountAndSSOAtomicWrites(t *testing.T) {
	st := pgStore(t, "sso_providers", "users", "user_groups")
	root := st.EnsureDefaultGroup()
	if err := st.createOUAccount("atomic-pg", "h", "user", "", "", "", root); err != nil {
		t.Fatal(err)
	}
	operator, missing := "operator", int64(999999)
	before := st.GetUser("atomic-pg").SessionRev
	if _, err := st.saveOUAccount("atomic-pg", "actor", "2026-10-24", ouAccountPatch{Role: &operator, PrimaryGroup: &missing}); err == nil {
		t.Fatal("account accepted missing OU")
	}
	s := &Server{st: st}
	if err := s.applyAssignment("atomic-pg", operator, missing); err == nil {
		t.Fatal("SSO assignment accepted missing OU")
	}
	if u := st.GetUser("atomic-pg"); u.Role != "user" || u.SessionRev != before || st.PrimaryGroupOf(u.Username) != root {
		t.Fatal("rejected edit changed account")
	}
	if _, err := st.SaveSSOProvider(SSOProvider{Slug: "atomic-pg", DefaultGroup: missing}); err == nil {
		t.Fatal("provider accepted missing OU")
	}
	if err := st.SaveSSORules([]storedRule{{TargetGroup: missing}}); err == nil {
		t.Fatal("rules accepted missing OU")
	}
	provider := SSOProvider{Slug: "atomic-pg", Kind: "oidc", DefaultGroup: root, Name: "created"}
	id, err := st.SaveSSOProvider(provider)
	if err != nil || id == 0 {
		t.Fatalf("provider create: id=%d err=%v", id, err)
	}
	provider.Name = "updated"
	if updated, err := st.SaveSSOProvider(provider); err != nil || updated != id {
		t.Fatalf("provider update: id=%d err=%v", updated, err)
	}
	if err := s.applyAssignment("atomic-pg", operator, root); err != nil {
		t.Fatal(err)
	}
	if u := st.GetUser("atomic-pg"); u.Role != operator || u.SessionRev != before+1 {
		t.Fatal("committed SSO assignment did not update role and revoke sessions together")
	}
}
