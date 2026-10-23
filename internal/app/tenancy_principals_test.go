package app

// P30: an OU is a group principal only when it is effectively restricted (D4, V16). The bug this
// pins is an OPEN one on main: an account-restricted user in an unrestricted OU wrote a g:<OU> row,
// and every restricted reader whose chain passed through that OU could match it.

import (
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

type p30Fixture struct {
	s         *Server
	root      int64
	container int64
	tenant    int64
	version   string
}

func newP30Fixture(t *testing.T) p30Fixture {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	f := p30Fixture{s: s, root: st.EnsureDefaultGroup()}
	f.container, _ = st.CreateUserGroup("container", "", 0)
	st.SetGroupParent(f.container, f.root)
	f.tenant, _ = st.CreateUserGroup("tenant", "", 0)
	st.SetGroupParent(f.tenant, f.container)
	st.SetGroupRestricted(f.tenant, true)

	// alice and bob are account-restricted members of an UNRESTRICTED container; carol and dave are
	// members of the restricted tenant below it.
	for _, u := range []struct {
		name string
		ou   int64
		flag bool
	}{
		{"alice", f.container, true},
		{"bob", f.container, true},
		{"carol", f.tenant, false},
		{"dave", f.tenant, false},
	} {
		st.UpsertUser(User{Username: u.name, PasswordHash: "h", Role: "user"})
		st.SetPrimaryGroup(u.name, u.ou)
		if u.flag {
			st.SetUserRestricted(u.name, true)
		}
	}
	f.version = "grouped"
	st.SaveVersion(ReportVersion{Name: f.version, Ord: 1, Visibility: VisibilityGroup})
	st.SetVersionGrants(f.version, []string{groupPrincipal(f.container), groupPrincipal(f.tenant)})
	return f
}

// report publishes one report on the group-visibility version, attributed to `ou` and requested by
// `who`, exactly as ingest does.
func (f p30Fixture) report(t *testing.T, symbol, who string, ou int64) int64 {
	t.Helper()
	id, _, err := f.s.st.UpsertReport(Rep{Symbol: symbol, Date: "2026-10-19", RType: "val",
		Title: "t", Version: f.version, MD: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.st.StampReportOwner(id, ou); err != nil {
		t.Fatal(err)
	}
	if err := f.s.st.AddReportViewer(id, "2026-10-19", who, ou); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f p30Fixture) canRead(user string, reportID int64) bool {
	rep, _ := f.s.st.GetNew(reportID, f.s.viewerScope(user))
	return rep != nil
}

func TestOnlyRestrictedOUsShare(t *testing.T) {
	f := newP30Fixture(t)
	containerReport := f.report(t, "600300", "alice", f.container)
	tenantReport := f.report(t, "600301", "carol", f.tenant)

	// (a) Two account-restricted users in one UNRESTRICTED OU do not see each other's reports.
	if f.canRead("bob", containerReport) {
		t.Error("bob read a report requested by alice through an unrestricted OU")
	}
	// The requester can read their own.
	if !f.canRead("alice", containerReport) {
		t.Error("alice cannot read her own report")
	}
	// (b) A tenant below the unrestricted container cannot read the container's report.
	if f.canRead("carol", containerReport) {
		t.Error("a restricted tenant read a report requested in the unrestricted container above it")
	}
	// (c) Two members of one restricted tenant still share.
	if !f.canRead("dave", tenantReport) {
		t.Error("two members of a restricted tenant do not share")
	}

	// A LEGACY row written for the unrestricted container (as every build before P30 did) must not
	// match either: this is the part that protects existing data, and the part a writer-only fix
	// leaves broken.
	legacy := f.report(t, "600302", "alice", f.container)
	if err := f.s.st.SetReportViewers(legacy, "2026-10-19",
		[]string{userPrincipal("alice"), groupPrincipal(f.container)}); err != nil {
		t.Fatal(err)
	}
	if f.canRead("bob", legacy) {
		t.Error("an account-restricted user matched a legacy g-row for an unrestricted OU")
	}
	if f.canRead("carol", legacy) {
		t.Error("a tenant matched a legacy g-row for its unrestricted container")
	}
	// And the scope itself carries the restricted tenant but not the unrestricted container.
	var hasTenant bool
	for _, p := range f.s.viewerScope("carol").principals {
		if p == groupPrincipal(f.container) {
			t.Errorf("the reader scope contains the unrestricted container: %v", p)
		}
		hasTenant = hasTenant || p == groupPrincipal(f.tenant)
	}
	if !hasTenant {
		t.Error("the reader scope lost its restricted tenant")
	}
}

func TestIngestWritesGroupPrincipalOnlyForRestrictedOUs(t *testing.T) {
	s := tokenServer(t)
	internal := seedOU(t, s.st, 0, scopeAll)
	external := seedOU(t, s.st, 0, scopeAll)
	if err := s.st.SetGroupRestricted(external, true); err != nil {
		t.Fatal(err)
	}
	// The token names a person, and the ingest only records a person who is an account (a deleted
	// submitter's late ingest writes nothing), so the submitters exist and sit in their OUs.
	for user, ou := range map[string]int64{"staff": internal, "ext": external} {
		s.st.UpsertUser(User{Username: user, PasswordHash: "h", Role: "user"})
		s.st.SetPrimaryGroup(user, ou)
	}
	principals := func(reportID int64) []string {
		t.Helper()
		rows, err := s.st.query(`SELECT principal FROM report_viewers WHERE report_id=? ORDER BY principal`, reportID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			out = append(out, p)
		}
		return out
	}

	internalReport, _ := ingestWithToken(t, s, "600310", s.mintOwnerToken(internal, "staff"))
	got := principals(internalReport)
	if len(got) != 1 || !strings.HasPrefix(got[0], "u:") {
		t.Errorf("internal OU principals = %v, want the user row only", got)
	}
	externalReport, _ := ingestWithToken(t, s, "600311", s.mintOwnerToken(external, "ext"))
	got = principals(externalReport)
	if len(got) != 2 {
		t.Fatalf("restricted OU principals = %v, want a user and a group row", got)
	}
	var hasUser, hasGroup bool
	for _, p := range got {
		hasUser = hasUser || strings.HasPrefix(p, "u:")
		hasGroup = hasGroup || p == groupPrincipal(external)
	}
	if !hasUser || !hasGroup {
		t.Errorf("restricted OU principals = %v, want %s and %s", got, userPrincipal("ext"), groupPrincipal(external))
	}
}

func TestOUIndexErrorWritesNoGroupPrincipal(t *testing.T) {
	s := tokenServer(t)
	// A dangling chain: the OU names a parent that is not in the table.
	if _, err := s.st.exec(`INSERT INTO user_groups(id,name,description,created_at,weight,urgent_unlimited,is_default,parent_id,restricted)
		VALUES(9101,'dangling','',?,0,0,0,987654,1)`, nowStr()); err != nil {
		t.Fatal(err)
	}
	reportID, _, err := s.st.UpsertReport(Rep{Symbol: "600320", Date: "2026-10-19", RType: "val", Title: "t", MD: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.AddReportViewer(reportID, "2026-10-19", "who", 9101); err != nil {
		t.Fatal(err)
	}
	if n := viewerRows(t, s.st, "principal=?", groupPrincipal(9101)); n != 0 {
		t.Error("a dangling chain still wrote a group principal")
	}
	if n := viewerRows(t, s.st, "principal=?", userPrincipal("who")); n != 1 {
		t.Error("the user principal was not written")
	}

	// An index that cannot be read at all: rename the table out from under the loader.
	report2, _, err := s.st.UpsertReport(Rep{Symbol: "600321", Date: "2026-10-19", RType: "val", Title: "t2", MD: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`ALTER TABLE user_groups RENAME TO user_groups_away`); err != nil {
		t.Fatal(err)
	}
	if err := s.st.AddReportViewer(report2, "2026-10-19", "who2", 1); err != nil {
		t.Fatalf("AddReportViewer: %v", err)
	}
	if n := viewerRows(t, s.st, "principal=?", groupPrincipal(1)); n != 0 {
		t.Error("an unreadable index wrote a group principal")
	}
	if n := viewerRows(t, s.st, "principal=?", userPrincipal("who2")); n != 1 {
		t.Error("the user principal was not written when the index failed")
	}
}

// TestEveryoneAudienceStillReachesAScopedReader: the "all" audience is stored as the Default OU
// principal, so a restricted reader must keep that one principal even though unrestricted OUs are
// no longer principals. Without this, P30 would silently break "everyone".
func TestEveryoneAudienceStillReachesAScopedReader(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	st := s.st
	root := st.EnsureDefaultGroup()
	tenant, _ := st.CreateUserGroup("tenant", "", 0)
	st.SetGroupParent(tenant, root)
	st.SetGroupRestricted(tenant, true)
	st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("ext", tenant)
	st.SaveVersion(ReportVersion{Name: "v", Ord: 1, Visibility: VisibilityGroup})
	st.SetVersionGrants("v", []string{groupPrincipal(tenant)})

	id, _, err := st.UpsertReport(Rep{Symbol: "600330", Date: "2026-10-19", RType: "val", Title: "t", Version: "v", MD: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetReportViewers(id, "2026-10-19", []string{groupPrincipal(root)}); err != nil {
		t.Fatal(err)
	}
	if rep, _ := st.GetNew(id, s.viewerScope("ext")); rep == nil {
		t.Fatal(`a report addressed to "everyone" is unreadable by a scoped reader`)
	}
}
