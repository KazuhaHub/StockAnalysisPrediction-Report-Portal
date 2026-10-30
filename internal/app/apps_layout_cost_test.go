package app

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// Revocation only when something closes, lock order, a missing user and the cost of the per-request
// check (ADR 0048).

func TestOnlyAClosingChangeRevokesTokens(t *testing.T) {
	e := newAppsEnv(t)
	tok := e.mint("rita", "alpha")
	live := func(what string) {
		t.Helper()
		if code := e.bridgeGet(tok); code != http.StatusOK {
			t.Fatalf("%s revoked a token: %d", what, code)
		}
	}
	e.mustSetAccess("alpha", e.ou, "true") // opening an app that was open
	live("an opening")
	e.mustSetAccess("alpha", e.ou, "null") // clearing a row that opened it
	live("clearing an opening")
	e.mustSetAccess("alpha", e.ou, "null") // nothing there
	live("a no-op")
	other := seedOU(t, e.s.st, 0, nil)
	e.mustSetAccess("alpha", other, "false")
	if code := e.bridgeGet(tok); code == http.StatusOK {
		t.Fatal("closing the app for an OU must revoke: the revocation is per app, not per subject")
	}
}

func TestReopeningNeverRevokes(t *testing.T) {
	e := newAppsEnv(t)
	e.mustSetAccess("alpha", e.ou, "false")
	tok := e.mint("admin", "alpha") // an administrator is exempt, so can hold a token for a closed app
	e.mustSetAccess("alpha", e.ou, "null")
	if code := e.bridgeGet(tok); code != http.StatusOK {
		t.Fatalf("reopening revoked a token: %d", code)
	}
	e.mustSetAccess("alpha", e.ou, "false") // closing again, even though nothing is open there yet
	e.mustSetAccess("alpha", e.ou, "false") // a repeat is a no-op
}

func TestAMissingAppTokenIsA401AtTheProxyAndAClosedAppIsA403(t *testing.T) {
	s := extensionFixture(t)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	if w := extensionAdminCall(s, "PUT", extensionSaveBody(0, key)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	token := extensionMint(t, s, "viewer")
	s.appTok.revokeApp("status")
	if w := extensionRequest(s, token, "GET", "status"); w.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked token = %d, want 401 so the frame re-mints", w.Code)
	}
	if w := extensionRequest(s, "no-such-token", "GET", "status"); w.Code != http.StatusUnauthorized {
		t.Fatalf("an unknown token = %d", w.Code)
	}
	token = extensionMint(t, s, "viewer")
	if _, err := s.st.exec(`INSERT INTO app_ou_access(app_key,group_id,enabled) VALUES('status',?,0)`, s.st.DefaultGroupID()); err != nil {
		t.Fatal(err)
	}
	if w := extensionRequest(s, token, "GET", "status"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "app_closed") {
		t.Fatalf("a definitely closed app = %d %s, want 403 app_closed", w.Code, w.Body.String())
	}
}

func TestAppLockOrderIsSortedAndDeduplicated(t *testing.T) {
	got := appLockOrder([]string{"zeta", "builtin:quotes", "alpha", "zeta"})
	if !slices.Equal(got, []string{"alpha", "builtin:quotes", "zeta"}) {
		t.Fatalf("lock order = %v: concurrent reorders must take row locks in one order", got)
	}
}

func TestADeletedUsersTokenIsClosedAtTheBridge(t *testing.T) {
	e := newAppsEnv(t)
	tok := e.mint("rita", "alpha")
	if _, err := e.s.st.exec(`DELETE FROM users WHERE username='rita'`); err != nil {
		t.Fatal(err)
	}
	if code := e.bridgeGet(tok); code == http.StatusOK {
		t.Fatal("a token whose user no longer exists was admitted")
	}
}

func TestAnAppWithNoAccessRowsCostsNoSnapshot(t *testing.T) {
	e := newAppsEnv(t)
	tok := e.mint("rita", "alpha")
	before := appAccessSnapshotLoads.Load()
	for i := 0; i < 3; i++ {
		if code := e.bridgeGet(tok); code != http.StatusOK {
			t.Fatalf("bridge = %d", code)
		}
	}
	if n := appAccessSnapshotLoads.Load() - before; n != 0 {
		t.Fatalf("%d OU snapshot loads for an app with no rows", n)
	}
	// A row for ANOTHER app does not make this one pay either.
	e.mustSetAccess("beta", e.ou, "false")
	tok = e.mint("rita", "alpha")
	before = appAccessSnapshotLoads.Load()
	if code := e.bridgeGet(tok); code != http.StatusOK {
		t.Fatalf("bridge = %d", code)
	}
	if appAccessSnapshotLoads.Load() != before {
		t.Fatal("rows for another app made this one load a snapshot")
	}
	// Once it has rows, it does resolve.
	e.mustSetAccess("alpha", e.ou, "true")
	tok = e.mint("rita", "alpha")
	before = appAccessSnapshotLoads.Load()
	if code := e.bridgeGet(tok); code != http.StatusOK || appAccessSnapshotLoads.Load() == before {
		t.Fatalf("an app with rows must resolve: %d", code)
	}
	// Administrators never need the snapshot.
	atok := e.mint("admin", "alpha")
	before = appAccessSnapshotLoads.Load()
	if code := e.bridgeGet(atok); code != http.StatusOK || appAccessSnapshotLoads.Load() != before {
		t.Fatalf("admin bridge = %d, loads %d", code, appAccessSnapshotLoads.Load()-before)
	}
}
