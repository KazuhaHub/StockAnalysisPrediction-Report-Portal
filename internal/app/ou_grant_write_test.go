package app

import "testing"

// Reference writers that name an OU must validate it inside the same serialized writer boundary as
// OU deletion. Validating outside it lets a delete land between the check and the write, leaving a
// grant row that names an OU which no longer exists — a row that grants nothing and that no later
// operation will ever clean up.

func TestUnifiedOUGrantWritersRejectMissingOU(t *testing.T) {
	s, org, _, _, _ := ouFixture(t)
	st := s.st
	st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityGroup})

	if err := st.SetVersionGrants("对外版", []string{groupPrincipal(999999)}); err == nil {
		t.Fatal("version grants accepted a missing OU")
	}
	if got := st.VersionGrants("对外版"); len(got) != 0 {
		t.Fatalf("rejected save wrote grants: %v", got)
	}
	if err := st.SetVersionGrants("对外版", []string{groupPrincipal(org)}); err != nil {
		t.Fatal(err)
	}
	if got := st.VersionGrants("对外版"); len(got) != 1 || got[0] != groupPrincipal(org) {
		t.Fatalf("valid grant not stored: %v", got)
	}

	a, err := st.AddAnnouncement(Announcement{Title: "t", Scope: "grant", Audience: "grant", Level: "info", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAnnouncementGrants(a, []string{groupPrincipal(999999)}); err == nil {
		t.Fatal("announcement grants accepted a missing OU")
	}
	if got := st.AnnouncementsGrantedTo(groupPrincipal(999999)); len(got) != 0 {
		t.Fatalf("rejected save wrote announcement grants: %v", got)
	}
}

// A grant row must not survive the OU it names. Deleting the OU sweeps the grants, and a writer that
// runs afterwards re-validates against the tree rather than resurrecting the name.
func TestUnifiedOUGrantWritersFailAfterDeletion(t *testing.T) {
	s, _, team, _, _ := ouFixture(t)
	st := s.st
	st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityGroup})
	if err := st.SetPrimaryGroup("ext", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.DeleteUserGroup(team); err != nil {
		t.Fatal(err)
	}
	if err := st.SetVersionGrants("对外版", []string{groupPrincipal(team)}); err == nil {
		t.Fatal("version grant accepted a deleted OU")
	}
	if got := st.VersionGrants("对外版"); len(got) != 0 {
		t.Fatalf("grant written for a deleted OU: %v", got)
	}
}

// Rejected and no-op saves write nothing, so a refusal cannot be mistaken for a grant of access.
func TestUnifiedOUGrantWritersLeaveStateOnRefusal(t *testing.T) {
	s, org, _, _, _ := ouFixture(t)
	st := s.st
	st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityGroup})
	if err := st.SetVersionGrants("对外版", []string{groupPrincipal(org)}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetVersionGrants("对外版", []string{groupPrincipal(org), groupPrincipal(999999)}); err == nil {
		t.Fatal("mixed valid/invalid list accepted")
	}
	if got := st.VersionGrants("对外版"); len(got) != 1 || got[0] != groupPrincipal(org) {
		t.Fatalf("refused save changed grants: %v", got)
	}
}
