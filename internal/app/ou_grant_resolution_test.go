package app

import "testing"

func TestUnifiedOUVersionGrantReadFailureCannotInheritBroaderAccess(t *testing.T) {
	s, org, _, _, _ := ouFixture(t)
	if err := s.st.SetVersionGrants("default", []string{groupPrincipal(org)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`DROP TABLE version_grants`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.GrantedVersionsErr("ext"); err == nil {
		t.Fatal("failed grant read became an empty inherit choice")
	}
	if got := s.st.GrantedVersions("ext"); len(got) != 0 {
		t.Fatalf("read failure granted versions: %v", got)
	}
}
