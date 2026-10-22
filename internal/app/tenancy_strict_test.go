package app

// P31: an unreadable restriction scopes to NOTHING, never to everything (D3, V16).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

type p31Fixture struct {
	s        *Server
	tenant   int64
	reportID int64
	symbol   string
}

func newP31Fixture(t *testing.T) p31Fixture {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	f := p31Fixture{s: s, symbol: "600400"}
	root := st.EnsureDefaultGroup()
	f.tenant, _ = st.CreateUserGroup("tenant", "", 0)
	st.SetGroupParent(f.tenant, root)
	st.SetGroupRestricted(f.tenant, true)
	if _, err := st.exec("UPDATE user_groups SET forecast_scope=? WHERE id=?", scopeAll, f.tenant); err != nil {
		t.Fatal(err)
	}
	st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("ext", f.tenant)
	st.SaveVersion(ReportVersion{Name: "v", Ord: 1, Visibility: VisibilityGroup})
	st.SetVersionGrants("v", []string{groupPrincipal(f.tenant)})

	id, _, err := st.UpsertReport(Rep{Symbol: f.symbol, Date: "2026-10-19", RType: "val",
		Title: "t", Version: "v", MD: "x"})
	if err != nil {
		t.Fatal(err)
	}
	f.reportID = id
	if _, err := st.StampReportOwner(id, f.tenant); err != nil {
		t.Fatal(err)
	}
	if err := st.AddReportViewer(id, "2026-10-19", "ext", f.tenant); err != nil {
		t.Fatal(err)
	}
	// The list route reads the prediction ledger, so it needs a set to hide; without one every
	// scope, including the buggy unscoped one, would return an empty list.
	ps := &predSetRow{Symbol: f.symbol, Rtype: "投资决策建议", Version: "v", ReportDate: "2026-10-19",
		MadeAt: "2026-10-19T13:00:00Z", MadeOn: "2026-10-19", Cohort: "2026-10", Zone: "Asia/Shanghai",
		State: predStateAccepted, PayloadSHA: "p31-1", BodySHA: "p31-1"}
	if _, _, err := st.InsertPredictionSet(id, ps, nil); err != nil {
		t.Fatalf("seed prediction set: %v", err)
	}
	return f
}

// readReport runs the session route for one report and returns its status.
func (f p31Fixture) readReport(user string) int {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/report/x/predictions", nil)
	req.SetPathValue("id", fmt.Sprint(f.reportID))
	// The predictions route is the reading page's; it uses viewerScope through apiReportPredictions.
	f.s.apiReportPredictions(rec, req, user)
	return rec.Code
}

// readList runs the symbol list route; it must return no sets under a deny scope.
func (f p31Fixture) readList(user string) []any {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/predictions?symbol="+f.symbol, nil)
	f.s.apiQueryPredictions(rec, req, user)
	var out struct {
		Sets []any `json:"sets"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Sets
}

func TestScopeOnUnreadableRestrictedFlag(t *testing.T) {
	// Control: a readable restriction lets the tenant read its own report AND its list row.
	f := newP31Fixture(t)
	if code := f.readReport("ext"); code != http.StatusOK {
		t.Fatalf("control: restricted read = %d", code)
	}
	if sets := f.readList("ext"); len(sets) != 1 {
		t.Fatalf("control: list returned %d sets, want 1", len(sets))
	}

	// The account row cannot be read.
	if _, err := f.s.st.exec(`ALTER TABLE users RENAME TO users_away`); err != nil {
		t.Fatal(err)
	}
	if sc := f.s.viewerScope("ext"); sc == nil || !sc.denyAll {
		t.Fatalf("an unreadable account did not deny: %+v", sc)
	}
	if code := f.readReport("ext"); code != http.StatusForbidden {
		t.Errorf("report route with an unreadable account = %d, want 403", code)
	}
	if sets := f.readList("ext"); len(sets) != 0 {
		t.Errorf("list route with an unreadable account returned %d sets", len(sets))
	}
}

func TestScopeOnUnreadableOURow(t *testing.T) {
	f := newP31Fixture(t) // "ext" is not account-restricted; the OU carries the flag
	if _, err := f.s.st.exec(`ALTER TABLE user_groups RENAME TO user_groups_away`); err != nil {
		t.Fatal(err)
	}
	if sc := f.s.viewerScope("ext"); sc == nil || !sc.denyAll {
		t.Fatalf("an unreadable OU table did not deny: %+v", sc)
	}
	if code := f.readReport("ext"); code != http.StatusForbidden {
		t.Errorf("report route with an unreadable OU table = %d, want 403", code)
	}
}

func TestScopeOnABrokenOUChain(t *testing.T) {
	f := newP31Fixture(t)
	// Move the tenant under a parent that does not exist: the walk cannot be trusted.
	if _, err := f.s.st.exec(`UPDATE user_groups SET parent_id=999999 WHERE id=?`, f.tenant); err != nil {
		t.Fatal(err)
	}
	if sc := f.s.viewerScope("ext"); sc == nil || !sc.denyAll {
		t.Fatalf("a dangling chain did not deny: %+v", sc)
	}
	if code := f.readReport("ext"); code != http.StatusForbidden {
		t.Errorf("report route with a dangling chain = %d, want 403", code)
	}
}

// TestInternalUsersAreStillUnrestricted: the strict path must not scope an ordinary account.
func TestInternalUsersAreStillUnrestricted(t *testing.T) {
	f := newP31Fixture(t)
	f.s.st.UpsertUser(User{Username: "staff", PasswordHash: "h", Role: "user"})
	if sc := f.s.viewerScope("staff"); sc != nil {
		t.Fatalf("an internal user got a scope: %+v", sc)
	}
}
