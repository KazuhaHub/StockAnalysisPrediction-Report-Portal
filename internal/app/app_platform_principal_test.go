package app

import (
	"testing"
	"time"
)

func testPlatformPrincipalCleanup(t *testing.T, postgres bool) {
	st := platformTestStore(t, postgres)
	platformSeedPendingControl(t, st)
	user := "app-platform-fixture-reader"
	if err := st.UpsertUser(User{Username: user, PasswordHash: "h", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.DeleteUser(user); err != nil {
			t.Error(err)
		}
	})
	account := st.GetUser(user)
	generation, err := notificationAccountGeneration(st.poolExec(), user)
	if err != nil {
		t.Fatal(err)
	}
	platformTestInsert(t, st.poolExec(), "access_grants", map[string]any{
		"id": platformFixtureID(10), "installation_id": platformFixtureID(1), "principal_type": "user", "username": user, "account_created": account.CreatedAt, "account_generation": generation,
		"group_id": nil, "group_created": nil, "revision": 1, "created_ms": 1, "updated_ms": 1,
	})
	future := time.Now().UTC().Add(time.Hour).UnixMilli()
	// An install review may exist before its installation row. Deletion must
	// invalidate the actor's plan even when no installation can identify it.
	mustExec(t, st, `UPDATE app_platform_review_plans SET installation_id=NULL,username=?,account_created=?,account_generation=?,expires_ms=?`, user, account.CreatedAt, generation, future)
	if err := st.DeleteUser(user); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("deleted account audience survived")
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE username IS NOT NULL`) != 0 {
		t.Fatal("deleted actor label survived")
	}
	if scalar[int](t, st, `SELECT policy_revision FROM app_platform_installations`) != 2 {
		t.Fatal("audience deletion did not invalidate policy revision")
	}
	if scalar[int64](t, st, `SELECT expires_ms FROM app_platform_review_plans`) > time.Now().UTC().UnixMilli() {
		t.Fatal("deleted actor review remained valid")
	}
	if err := st.UpsertUser(User{Username: user, PasswordHash: "h", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("recreated username inherited app access")
	}
	group, err := st.CreateUserGroup("App platform fixture OU", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	created := scalar[string](t, st, `SELECT created_at FROM user_groups WHERE id=?`, group)
	platformTestInsert(t, st.poolExec(), "access_grants", map[string]any{
		"id": platformFixtureID(11), "installation_id": platformFixtureID(1), "principal_type": "ou", "username": nil, "account_created": nil, "account_generation": nil,
		"group_id": group, "group_created": created, "revision": 1, "created_ms": 1, "updated_ms": 1,
	})
	if _, _, err := st.DeleteUserGroup(group); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("deleted OU audience survived")
	}
	if scalar[int](t, st, `SELECT policy_revision FROM app_platform_installations`) != 3 {
		t.Fatal("OU deletion did not invalidate policy revision")
	}
}
func TestAppPlatformPrincipalCleanup(t *testing.T)         { testPlatformPrincipalCleanup(t, false) }
func TestPostgresAppPlatformPrincipalCleanup(t *testing.T) { testPlatformPrincipalCleanup(t, true) }
