package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The admin API validates principals before it calls a writer, so a missing OU reaching a writer
// means the tree moved under the request. That must read as a conflict for the caller to retry
// against the reloaded list — not as a server fault, and never as a silent partial save.

func TestUnifiedOUGrantWriteConflictIsNotAServerError(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityGroup})
	st.SetVersionGrants("对外版", []string{groupPrincipal(st.EnsureDefaultGroup())})

	// The API's own principal check runs first, against the live tree.
	rec := httptest.NewRecorder()
	s.apiAdminVersionSave(rec, httptest.NewRequest(http.MethodPost, "/api/admin/versions",
		strings.NewReader(`{"name":"对外版","visibility":"group","grants":["g:999999"]}`)), "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("API accepted an unknown OU: %d %s", rec.Code, rec.Body.String())
	}
	if got := st.VersionGrants("对外版"); len(got) != 1 {
		t.Fatalf("refused API save changed grants: %v", got)
	}

	// A writer reached directly with the same principal answers a conflict, not a 500: this is the
	// race path, where the OU was valid when the request was validated and gone by the write.
	w := httptest.NewRecorder()
	writeGrantWriteError(w, errOUPrincipalMissing)
	if w.Code != http.StatusConflict {
		t.Fatalf("missing OU reported as %d, want %d", w.Code, http.StatusConflict)
	}
	if !strings.Contains(w.Body.String(), "ou_missing") {
		t.Fatalf("missing OU is not identified: %s", w.Body.String())
	}
}

// Sweeping a deleted OU must also clear the grants it was named by, so a writer racing the delete
// cannot resurrect the name on a report the deletion already pruned.
func TestUnifiedOUDeleteSweepsEveryGrantTable(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	root := st.EnsureDefaultGroup()
	ou, err := st.CreateUserGroup("doomed-grant-holder", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupParent(ou, root); err != nil {
		t.Fatal(err)
	}
	st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityGroup})
	if err := st.SetVersionGrants("对外版", []string{groupPrincipal(ou)}); err != nil {
		t.Fatal(err)
	}
	id, err := st.AddAnnouncement(Announcement{Title: "t", Scope: "grant", Audience: "grant", Level: "info", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAnnouncementGrants(id, []string{groupPrincipal(ou)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.DeleteUserGroup(ou); err != nil {
		t.Fatal(err)
	}
	if got := st.VersionGrants("对外版"); len(got) != 0 {
		t.Fatalf("version grants outlived the OU: %v", got)
	}
	if got := st.AnnouncementsGrantedTo(groupPrincipal(ou)); len(got) != 0 {
		t.Fatalf("announcement grants outlived the OU: %v", got)
	}
	if got := scalar[int](t, st, `SELECT COUNT(*) FROM report_viewers WHERE principal=?`, groupPrincipal(ou)); got != 0 {
		t.Fatal("report viewers outlived the OU")
	}
}
