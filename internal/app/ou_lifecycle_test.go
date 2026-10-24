package app

import "testing"

func TestUnifiedOUSSOWritersRejectMissingDestinations(t *testing.T) {
	s, org, _, _, _ := ouFixture(t)
	p := SSOProvider{Slug: "validated-destination", Kind: "oidc", DefaultGroup: org, Name: "original"}
	if _, err := s.st.SaveSSOProvider(p); err != nil {
		t.Fatal(err)
	}
	p.DefaultGroup, p.Name = 999999, "rejected"
	if _, err := s.st.SaveSSOProvider(p); err == nil {
		t.Fatal("provider accepted a missing default OU")
	}
	stored, ok := s.st.SSOProviderBySlug(p.Slug)
	if !ok || stored.DefaultGroup != org || stored.Name != "original" {
		t.Fatal("rejected provider save changed configuration")
	}
	if err := s.st.SaveSSORules([]storedRule{{TargetGroup: org, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SaveSSORules([]storedRule{{TargetGroup: 999999, Enabled: true}}); err == nil {
		t.Fatal("SSO rules accepted a missing OU")
	}
	if rules := s.st.SSORules(); len(rules) != 1 || rules[0].TargetGroup != org {
		t.Fatal("rejected rule save changed configuration")
	}
}

func TestUnifiedOUPopulatedDeleteRequiresExplicitMemberMove(t *testing.T) {
	s, _, team, _, _ := ouFixture(t)
	if _, _, err := s.st.DeleteUserGroup(team); err == nil {
		t.Fatal("populated deletion silently moved a restricted member to Default")
	}
	if got := s.st.PrimaryGroupOf("ext"); got != team {
		t.Fatal("refused deletion changed membership")
	}
	if err := s.st.SetPrimaryGroup("ext", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.st.DeleteUserGroup(team); err != nil {
		t.Fatal(err)
	}
}

func TestUnifiedOUDeleteRefusesSSOReferences(t *testing.T) {
	s, org, _, _, _ := ouFixture(t)
	if _, err := s.st.SaveSSOProvider(SSOProvider{Slug: "ou-lifecycle", Kind: "oidc", DefaultGroup: org}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.st.DeleteUserGroup(org); err == nil {
		t.Fatal("deleted SSO's destination OU")
	}
}
