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
	future := time.Now().UTC().Add(time.Hour).UnixMilli()
	// An install review may exist before its installation row. Deletion must
	// invalidate the actor's plan even when no installation can identify it.
	mustExec(t, st, `UPDATE app_platform_review_plans SET installation_id=NULL,username=?,account_created=?,account_generation=?,expires_ms=?`, user, account.CreatedAt, generation, future)
	if err := st.DeleteUser(user); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE username IS NOT NULL`) != 0 {
		t.Fatal("deleted actor label survived")
	}
	// Access is an OU policy (doc 190, D7): a user holds no access row, so deleting one changes no
	// installation's policy revision; only the plans they reviewed are invalidated.
	if scalar[int](t, st, `SELECT policy_revision FROM app_platform_installations`) != 1 {
		t.Fatal("deleting a user changed an installation policy revision")
	}
	if scalar[int64](t, st, `SELECT expires_ms FROM app_platform_review_plans`) > time.Now().UTC().UnixMilli() {
		t.Fatal("deleted actor review remained valid")
	}
	if err := st.UpsertUser(User{Username: user, PasswordHash: "h", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	group, err := st.CreateUserGroup("App platform fixture OU", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, st, `INSERT INTO app_ou_access(app_key,group_id,enabled) VALUES(?,?,1)`, platformAppKey(platformFixtureID(1)), group)
	if _, _, err := st.DeleteUserGroup(group); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key LIKE 'platform:%'`) != 0 {
		t.Fatal("deleted OU's access row survived")
	}
	if scalar[int](t, st, `SELECT policy_revision FROM app_platform_installations`) != 1 {
		t.Fatal("OU deletion moved the policy revision of an installation that only had an access row")
	}
}
func TestAppPlatformPrincipalCleanup(t *testing.T) {
	t.Parallel()
	testPlatformPrincipalCleanup(t, false)
}
func TestPostgresAppPlatformPrincipalCleanup(t *testing.T) {
	t.Parallel()
	testPlatformPrincipalCleanup(t, true)
}
