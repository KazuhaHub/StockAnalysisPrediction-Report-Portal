package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestExtensionIdentityUsesCurrentOUAndRole(t *testing.T) {
	testExtensionIdentityUsesCurrentOUAndRole(t, newTestStore(t))
}

func TestPostgresExtensionIdentityUsesCurrentOUAndRole(t *testing.T) {
	testExtensionIdentityUsesCurrentOUAndRole(t, pgStore(t, "notification_source_cycles", "users", "user_groups"))
}

func testExtensionIdentityUsesCurrentOUAndRole(t *testing.T, st *Store) {
	t.Helper()
	if err := st.UpsertUser(User{Username: "viewer", PasswordHash: "private-password-hash", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	root := st.EnsureDefaultGroup()
	a, err := st.CreateUserGroup("Team A", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateUserGroup("Team B", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupParent(a, root); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupParent(b, root); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPrimaryGroup("viewer", a); err != nil {
		t.Fatal(err)
	}
	user := st.GetUser("viewer")
	entry := appTokenEntry{appID: "status", username: user.Username, sessionRev: user.SessionRev, accountCreated: user.CreatedAt, expires: time.Now().Add(time.Minute)}
	binding := extensionIdentityBinding{Issuer: "portal", AppID: "status", InstanceID: "registration", Key: bytes.Repeat([]byte{42}, 32)}
	resolve := func() extensionViewerFacts {
		t.Helper()
		v, err := st.extensionViewerIdentity(context.Background(), entry, binding, time.Now(), time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := resolve()
	if len(before.OUs) != 2 || before.Subject == "" || before.Role != "user" {
		t.Fatalf("facts=%+v", before)
	}
	if strings.Contains(before.Subject, "viewer") || strings.Contains(before.OUs[0], "Team") {
		t.Fatal("opaque identities exposed private labels")
	}
	if err := st.SetPrimaryGroup("viewer", b); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserRole("viewer", "operator"); err != nil {
		t.Fatal(err)
	}
	after := resolve()
	if before.Subject != after.Subject || before.OUs[0] == after.OUs[0] || before.OUs[1] != after.OUs[1] || after.Role != "operator" {
		t.Fatalf("stale identity: before=%+v after=%+v", before, after)
	}
}

func TestExtensionIdentityRevokesAccountAndBinding(t *testing.T) {
	testExtensionIdentityRevokesAccountAndBinding(t, newTestStore)
}

func TestPostgresExtensionIdentityRevokesAccountAndBinding(t *testing.T) {
	testExtensionIdentityRevokesAccountAndBinding(t, func(t *testing.T) *Store { return pgStore(t, "notification_source_cycles", "users", "user_groups") })
}

func testExtensionIdentityRevokesAccountAndBinding(t *testing.T, makeStore func(*testing.T) *Store) {
	t.Helper()
	for _, mode := range []string{"inactive", "expired", "invalid-expiry", "password", "recreated", "app", "registration", "token-expired"} {
		t.Run(mode, func(t *testing.T) {
			st := makeStore(t)
			st.EnsureDefaultGroup()
			if err := st.UpsertUser(User{Username: "viewer", PasswordHash: "hash", Role: "user"}); err != nil {
				t.Fatal(err)
			}
			u := st.GetUser("viewer")
			e := appTokenEntry{appID: "status", username: u.Username, sessionRev: u.SessionRev, accountCreated: u.CreatedAt, expires: time.Now().Add(time.Minute)}
			b := extensionIdentityBinding{Issuer: "portal", AppID: "status", InstanceID: "registration", Key: bytes.Repeat([]byte{42}, 32)}
			original, err := st.extensionViewerIdentity(context.Background(), e, b, time.Now(), time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "inactive":
				err = st.SetUserActive("viewer", false)
			case "expired":
				err = st.SetUserExpiry("viewer", "2000-01-01")
			case "invalid-expiry":
				_, err = st.exec(`UPDATE users SET expires_at='invalid' WHERE username='viewer'`)
			case "password":
				err = st.SetUserPassword("viewer", "new-hash")
			case "recreated":
				if err = st.DeleteUser("viewer"); err == nil {
					err = st.UpsertUser(User{Username: "viewer", PasswordHash: "new", Role: "user"})
				}
				// Force identical legacy creation timestamp to prove the deletion cursor matters.
				if err == nil {
					_, err = st.exec(`UPDATE users SET created_at=?,session_rev=? WHERE username='viewer'`, u.CreatedAt, u.SessionRev)
				}
			case "app":
				b.AppID = "another"
			case "registration":
				b.InstanceID = "another"
			case "token-expired":
				e.expires = time.Now().Add(-time.Second)
			}
			if err != nil {
				t.Fatal(err)
			}
			v, err := st.extensionViewerIdentity(context.Background(), e, b, time.Now(), time.UTC)
			if mode == "registration" {
				if err != nil || v.Subject == original.Subject {
					t.Fatal("different registration inherited identity")
				}
				return
			}
			if err == nil {
				t.Fatalf("%s retained identity %+v", mode, v)
			}
		})
	}
}

func TestExtensionIdentityFailsClosedOnInvalidAncestry(t *testing.T) {
	testExtensionIdentityFailsClosedOnInvalidAncestry(t, newTestStore)
}

func TestPostgresExtensionIdentityFailsClosedOnInvalidAncestry(t *testing.T) {
	testExtensionIdentityFailsClosedOnInvalidAncestry(t, func(t *testing.T) *Store { return pgStore(t, "notification_source_cycles", "users", "user_groups") })
}

func testExtensionIdentityFailsClosedOnInvalidAncestry(t *testing.T, makeStore func(*testing.T) *Store) {
	t.Helper()
	for _, mode := range []string{"dangling", "cycle", "missing-identity", "duplicate-default"} {
		t.Run(mode, func(t *testing.T) {
			st := makeStore(t)
			root := st.EnsureDefaultGroup()
			if err := st.UpsertUser(User{Username: "viewer", PasswordHash: "hash", Role: "user"}); err != nil {
				t.Fatal(err)
			}
			u := st.GetUser("viewer")
			e := appTokenEntry{appID: "status", username: u.Username, sessionRev: u.SessionRev, accountCreated: u.CreatedAt, expires: time.Now().Add(time.Minute)}
			b := extensionIdentityBinding{Issuer: "portal", AppID: "status", InstanceID: "registration", Key: bytes.Repeat([]byte{42}, 32)}
			var err error
			switch mode {
			case "dangling":
				_, err = st.exec(`UPDATE users SET group_id=999999 WHERE username='viewer'`)
			case "cycle":
				_, err = st.exec(`UPDATE user_groups SET parent_id=id WHERE id=?`, root)
			case "missing-identity":
				_, err = st.exec(`UPDATE user_groups SET created_at='' WHERE id=?`, root)
			case "duplicate-default":
				id, e := st.CreateUserGroup("Other root", "", 1)
				if e != nil {
					t.Fatal(e)
				}
				_, err = st.exec(`UPDATE user_groups SET is_default=1 WHERE id=?`, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.extensionViewerIdentity(context.Background(), e, b, time.Now(), time.UTC); err == nil {
				t.Fatal("invalid ancestry obtained identity")
			}
		})
	}
}

func TestExtensionIdentityExpiryUsesPanelCivilDate(t *testing.T) {
	st := newTestStore(t)
	st.EnsureDefaultGroup()
	if err := st.UpsertUser(User{Username: "viewer", PasswordHash: "hash", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserExpiry("viewer", "2026-10-21"); err != nil {
		t.Fatal(err)
	}
	user := st.GetUser("viewer")
	now := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	e := appTokenEntry{appID: "status", username: user.Username, sessionRev: user.SessionRev, accountCreated: user.CreatedAt, expires: now.Add(time.Minute)}
	b := extensionIdentityBinding{Issuer: "portal", AppID: "status", InstanceID: "registration", Key: bytes.Repeat([]byte{42}, 32)}
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.extensionViewerIdentity(context.Background(), e, b, now, loc); err != nil {
		t.Fatal("account must remain valid through its panel civil day")
	}
	if _, err = st.extensionViewerIdentity(context.Background(), e, b, now, time.UTC); err == nil {
		t.Fatal("UTC-expired account remained valid")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = st.extensionViewerIdentity(ctx, e, b, now, loc); err == nil {
		t.Fatal("cancelled read accepted")
	}
}
