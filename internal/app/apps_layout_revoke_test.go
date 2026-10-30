package app

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Tokens already minted, and validation inside the write (ADR 0048).

// bridgeGet is a bridge call: a Bearer app token against the machine API, with no session.
func (e *appsEnv) bridgeGet(token string) int {
	e.t.Helper()
	return e.do("", "GET", "/api/v1/now", "", "Authorization", "Bearer "+token).Code
}

func (e *appsEnv) mint(user, app string) string {
	e.t.Helper()
	w := e.do(user, "POST", "/api/apps/"+app+"/token", "")
	if w.Code != http.StatusOK {
		e.t.Fatalf("mint %s: %d %s", app, w.Code, w.Body.String())
	}
	var out struct {
		Token string `json:"token"`
	}
	mustDecode(e.t, w, &out)
	return out.Token
}

func TestClosingAnAppRevokesItsOutstandingTokens(t *testing.T) {
	e := newAppsEnv(t)
	tok, other := e.mint("rita", "alpha"), e.mint("rita", "beta")
	if code := e.bridgeGet(tok); code != http.StatusOK {
		t.Fatalf("bridge call before closing = %d", code)
	}
	e.mustSetAccess("alpha", e.ou, "false")
	if code := e.bridgeGet(tok); code == http.StatusOK {
		t.Fatal("a token minted before the app was closed still works")
	}
	if code := e.bridgeGet(other); code != http.StatusOK {
		t.Fatalf("closing alpha revoked beta's token: %d", code)
	}
}

func TestEveryBridgeCallRechecksAccessForTheTokensUser(t *testing.T) {
	e := newAppsEnv(t)
	tok := e.mint("rita", "alpha")
	// The row is written behind the API's back, so nothing revoked the token: the per-request check
	// is what cuts it off.
	if _, err := e.s.st.exec(`INSERT INTO app_ou_access(app_key,group_id,enabled) VALUES('alpha',?,0)`, e.ou); err != nil {
		t.Fatal(err)
	}
	if code := e.bridgeGet(tok); code == http.StatusOK {
		t.Fatal("a closed app's token worked")
	}
	if _, err := e.s.st.exec(`DELETE FROM app_ou_access`); err != nil {
		t.Fatal(err)
	}
	if code := e.bridgeGet(tok); code != http.StatusOK {
		t.Fatalf("reopening did not restore the live token: %d", code)
	}
	// A user moved from an open OU into a closed one is cut off on the next call.
	closed := seedOU(t, e.s.st, 0, nil)
	if _, err := e.s.st.exec(`INSERT INTO app_ou_access(app_key,group_id,enabled) VALUES('alpha',?,0)`, closed); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.st.exec(`UPDATE users SET group_id=? WHERE username='rita'`, closed); err != nil {
		t.Fatal(err)
	}
	if code := e.bridgeGet(tok); code == http.StatusOK {
		t.Fatal("a user moved into a closed OU kept the app's token")
	}
	// A token with no recorded app or user is not subject to this.
	if code := e.bridgeGet(e.s.appTok.mint([]string{"query"}, time.Now())); code != http.StatusOK {
		t.Fatalf("an unbound token = %d", code)
	}
}

func TestAnUnchangedOrderIsNotAudited(t *testing.T) {
	e := newAppsEnv(t)
	put := func(body string) {
		t.Helper()
		if w := e.do("admin", "PUT", "/api/admin/apps/order", body); w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
	}
	count := func() int {
		return scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, AuditAppOrder)
	}
	put(`{"keys":["beta","alpha"]}`)
	if count() != 1 {
		t.Fatalf("a real change wrote %d rows", count())
	}
	put(`{"keys":["beta","alpha"]}`) // identical
	if count() != 1 {
		t.Fatal("repeating the same order wrote a row")
	}
	put(`{"keys":[]}`) // back to today's order: a change
	n := count()
	put(`{"keys":[]}`)
	if count() != n {
		t.Fatal("clearing an already-clear order wrote a row")
	}
	detail := scalar[string](t, e.s.st, `SELECT detail FROM audit_log WHERE action=? ORDER BY id LIMIT 1`, AuditAppOrder)
	for _, want := range []string{`"field":"order"`, `"from":[`, `"to":["beta","alpha","builtin:batch"`} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %s lacks %s", detail, want)
		}
	}
}

func TestWritesValidateTheAppInsideTheirTransaction(t *testing.T) {
	e := newAppsEnv(t)
	st := e.s.st
	// Well formed, but not installed: what a concurrent uninstall leaves behind a stale check.
	if err := st.ReplaceAppOrder([]string{"alpha", "ghost"}); !errors.Is(err, errUnknownApp) {
		t.Fatalf("ReplaceAppOrder = %v, want errUnknownApp", err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM app_order`); n != 0 {
		t.Fatalf("a refused order left %d rows", n)
	}
	if err := st.ReplaceAppOrder([]string{"builtin:nope"}); !errors.Is(err, errUnknownApp) {
		t.Fatalf("an unknown built-in key = %v", err)
	}
	f := false
	err := st.withOUMutation(func(m migExec) error { return st.setAppAccess(m, "ghost", e.ou, &f) })
	if !errors.Is(err, errUnknownApp) {
		t.Fatalf("setAppAccess = %v", err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM app_ou_access`); n != 0 {
		t.Fatalf("a refused access write left %d rows", n)
	}
	// Removing a row for a key that is gone is allowed: it is cleanup.
	if err := st.withOUMutation(func(m migExec) error { return st.setAppAccess(m, "ghost", e.ou, nil) }); err != nil {
		t.Fatalf("clearing a gone app's row = %v", err)
	}
	// The handlers answer an uninstalled app with a client error, not a 500.
	if _, err := st.exec(`DELETE FROM apps WHERE id='alpha'`); err != nil {
		t.Fatal(err)
	}
	if w := e.do("admin", "PUT", "/api/admin/apps/alpha/access/"+itoa64(e.ou), `{"enabled":false}`); w.Code != http.StatusNotFound {
		t.Fatalf("access for an uninstalled app = %d", w.Code)
	}
	if w := e.do("admin", "PUT", "/api/admin/apps/order", `{"keys":["alpha"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("order naming an uninstalled app = %d", w.Code)
	}
}
