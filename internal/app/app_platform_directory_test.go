package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func testPlatformDirectory(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	result, err := s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("ungranted app leaked: %+v %v", result, err)
	}
	platformGrantLaunchUser(t, s, reader, id)
	result, err = s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(result.Items) != 1 || result.Items[0].InstallationID != id || result.Items[0].ApplicationID != "notes-sample" || result.Items[0].State != "installed" || !result.Items[0].Openable {
		t.Fatalf("current grant not listed: %+v %v", result, err)
	}
	mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, id)
	result, err = s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(result.Items) != 1 || result.Items[0].State != "disabled" || result.Items[0].Openable {
		t.Fatal("disabled state advertised an open action")
	}
	mustExec(t, s.st, `UPDATE app_platform_access_grants SET account_generation=account_generation+1 WHERE installation_id=?`, id)
	result, err = s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(result.Items) != 0 {
		t.Fatal("stale lifecycle inherited directory access")
	}
}
func TestPlatformDirectorySQLite(t *testing.T)   { testPlatformDirectory(t, false) }
func TestPlatformDirectoryPostgres(t *testing.T) { testPlatformDirectory(t, true) }
func TestPlatformDirectoryHTTP(t *testing.T) {
	s, mux, _, id, cookie := platformHTTPFixture(t)
	response := sessionTestRequest(mux, http.MethodGet, "/api/app-platform/apps", cookie, "")
	var result platformAppDirectory
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || len(result.Items) != 1 || result.Items[0].InstallationID != id {
		t.Fatalf("directory route: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("directory was cacheable")
	}
	if got := sessionTestRequest(mux, http.MethodGet, "/api/app-platform/apps", "", ""); got.Code != 401 {
		t.Fatal("anonymous directory read")
	}
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, id)
	response = sessionTestRequest(mux, http.MethodGet, "/api/app-platform/apps", cookie, "")
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Items) != 0 {
		t.Fatal("revoked audience stayed in the directory")
	}
}

func testPlatformDirectoryPagination(t *testing.T, pg bool) {
	s, reader, _ := platformLaunchFixture(t, pg)
	for i := 0; i < 40; i++ {
		if err := platformSeedInstallation(t, s.st.poolExec(), platformFixtureID(100+i), "fixture-publisher", fmt.Sprintf("notes-hidden-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 33; i++ {
		id := platformFixtureID(700 + i)
		if err := platformSeedInstallation(t, s.st.poolExec(), id, "fixture-publisher", fmt.Sprintf("notes-page-%d", i)); err != nil {
			t.Fatal(err)
		}
		platformTestInsert(t, s.st.poolExec(), "access_grants", map[string]any{"id": platformFixtureID(800 + i), "installation_id": id, "principal_type": "user", "username": reader.username, "account_created": reader.created, "account_generation": reader.generation, "group_id": nil, "group_created": nil, "revision": 1, "created_ms": 1, "updated_ms": 1})
	}
	first, err := s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(first.Items) != 32 || first.NextCursor != first.Items[31].InstallationID {
		t.Fatalf("bad authorized page: %+v %v", first, err)
	}
	second, err := s.listPlatformApps(context.Background(), reader, first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" || second.Items[0].InstallationID != platformFixtureID(732) {
		t.Fatalf("bad continuation: %+v %v", second, err)
	}
	for _, item := range append(first.Items, second.Items...) {
		if item.State != "pending" || item.Openable {
			t.Fatal("pending control row advertised execution readiness")
		}
	}
}
func TestPlatformDirectoryPaginationSQLite(t *testing.T)   { testPlatformDirectoryPagination(t, false) }
func TestPlatformDirectoryPaginationPostgres(t *testing.T) { testPlatformDirectoryPagination(t, true) }

func testPlatformDirectoryOU(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	parent, err := s.st.CreateUserGroup("Directory parent", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.st.CreateUserGroup("Directory child", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.st.SetPrimaryGroups([]string{reader.username}, 0); err != nil {
			t.Error(err)
		}
		if _, _, err := s.st.DeleteUserGroup(child); err != nil {
			t.Error(err)
		}
		if _, _, err := s.st.DeleteUserGroup(parent); err != nil {
			t.Error(err)
		}
	})
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, parent, child)
	if _, err := s.st.SetPrimaryGroups([]string{reader.username}, child); err != nil {
		t.Fatal(err)
	}
	var created string
	if err := s.st.queryRow(`SELECT created_at FROM user_groups WHERE id=?`, parent).Scan(&created); err != nil {
		t.Fatal(err)
	}
	platformTestInsert(t, s.st.poolExec(), "access_grants", map[string]any{"id": platformFixtureID(901), "installation_id": id, "principal_type": "ou", "username": nil, "account_created": nil, "account_generation": nil, "group_id": parent, "group_created": created, "revision": 1, "created_ms": 1, "updated_ms": 1})
	result, err := s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(result.Items) != 1 {
		t.Fatal("ancestor audience missing", err)
	}
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=NULL WHERE id=?`, child)
	result, err = s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(result.Items) != 0 {
		t.Fatal("old ancestor survived membership topology change", err)
	}
	var defaultID int64
	if err := s.st.queryRow(`SELECT id,created_at FROM user_groups WHERE is_default=1`).Scan(&defaultID, &created); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_access_grants SET group_id=?,group_created=?`, defaultID, created)
	result, err = s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(result.Items) != 0 {
		t.Fatal("Default policy fallback manufactured an audience", err)
	}
	if _, err := s.st.SetPrimaryGroups([]string{reader.username}, 0); err != nil {
		t.Fatal(err)
	}
	result, err = s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(result.Items) != 1 {
		t.Fatal("Default member audience missing", err)
	}
	mustExec(t, s.st, `UPDATE app_platform_access_grants SET group_created=?`, created+"old")
	result, err = s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(result.Items) != 0 {
		t.Fatal("stale OU identity inherited directory access", err)
	}
}
func TestPlatformDirectoryOUSQLite(t *testing.T)   { testPlatformDirectoryOU(t, false) }
func TestPlatformDirectoryOUPostgres(t *testing.T) { testPlatformDirectoryOU(t, true) }

func TestPlatformDirectoryUninitializedEmpty(t *testing.T) {
	s, reader := platformActorTestServer(t, false)
	before := dumpOf(t, s.st)
	page, err := s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("empty platform needs no bootstrap: %+v %v", page, err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("directory initialized persistent control state")
	}
}

func TestPlatformDirectoryHTTPInvalidQuery(t *testing.T) {
	_, mux, _, _, cookie := platformHTTPFixture(t)
	for _, suffix := range []string{"?", "?after=", "?after=bad", "?unknown=1", "?after=" + platformFixtureID(1) + "&after=" + platformFixtureID(2), "?%zz=1"} {
		response := sessionTestRequest(mux, http.MethodGet, "/api/app-platform/apps"+suffix, cookie, "")
		if response.Code != 404 || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("invalid query %q: %d", suffix, response.Code)
		}
	}
}

func TestPlatformDirectoryAdminHasNoAudienceBypass(t *testing.T) {
	s, reader, _ := platformLaunchFixture(t, false)
	mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, reader.username)
	result, err := s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("admin role disclosed ungranted app: %+v %v", result, err)
	}
}

func TestPostgresPlatformDirectoryWaitRefusal(t *testing.T) {
	for _, kind := range []string{"expiry", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			s, reader, id := platformLaunchFixture(t, true)
			platformGrantLaunchUser(t, s, reader, id)
			now, err := platformDatabaseNow(s.st.poolExec())
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.UnixMilli(now + 3000).Truncate(time.Second)
			if kind == "expiry" {
				reader.expires = deadline
				platformSeedActorSession(t, s.st, reader)
			}
			before := dumpOf(t, s.st)
			holder, err := s.st.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback()
			if _, err := holder.Exec(`SELECT id FROM app_platform_installations FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				page platformAppDirectory
				err  error
			}
			done := make(chan outcome, 1)
			go func() { page, err := s.listPlatformApps(ctx, reader, ""); done <- outcome{page, err} }()
			blocked := false
			limit := time.Now().Add(5 * time.Second)
			for time.Now().Before(limit) {
				var n int
				if err := s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%FOR SHARE OF i%'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					blocked = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !blocked {
				t.Fatal("directory never reached installation lock")
			}
			if kind == "expiry" {
				for time.Now().Before(deadline) {
					time.Sleep(5 * time.Millisecond)
				}
				if err := holder.Commit(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case got := <-done:
				if got.err == nil || got.page.Items != nil || got.page.NextCursor != "" {
					t.Fatalf("failed read leaked page: %+v", got)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("directory did not refuse expired/cancelled wait")
			}
			holder.Rollback()
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("failed directory read changed durable state")
			}
		})
	}
}

func TestPlatformDirectorySignedDisplay(t *testing.T) {
	s, reader, id := platformLaunchFixture(t, false)
	platformGrantLaunchUser(t, s, reader, id)
	page, err := s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Display == nil || page.Items[0].Display.Texts[1].Name != "笔记" {
		t.Fatalf("signed display unavailable: %+v %v", page, err)
	}
	mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET revoked_ms=1`)
	page, err = s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Display != nil || page.Items[0].Openable || page.Items[0].State != "attention" {
		t.Fatalf("revoked publisher retained display: %+v %v", page, err)
	}
}

func TestPlatformDirectoryDisplayDoesNotInspectExecutable(t *testing.T) {
	s, reader, id := platformLaunchFixture(t, false)
	platformGrantLaunchUser(t, s, reader, id)
	var content []byte
	if err := s.st.queryRow(`SELECT content FROM app_platform_frontend_artifacts`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_frontend_artifacts SET content=?`, bytes.Repeat([]byte{'x'}, len(content)))
	page, err := s.listPlatformApps(context.Background(), reader, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Display == nil {
		t.Fatalf("lightweight display read depended on executable archive: %+v %v", page, err)
	}
	if _, err := s.inspectPlatformLaunchAccess(reader, id); err == nil {
		t.Fatal("display metadata bypassed executable integrity")
	}
}

func TestPostgresPlatformDisplayKeyExpiryAfterWait(t *testing.T) {
	s, reader, id := platformLaunchFixture(t, true)
	platformGrantLaunchUser(t, s, reader, id)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.UnixMilli(now + 3000).Truncate(time.Second)
	mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET not_after_ms=?`, deadline.UnixMilli())
	holder, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.Exec(`SELECT sha256 FROM app_platform_releases FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		page platformAppDirectory
		err  error
	}
	done := make(chan outcome, 1)
	go func() { page, err := s.listPlatformApps(context.Background(), reader, ""); done <- outcome{page, err} }()
	blocked := false
	limit := time.Now().Add(5 * time.Second)
	for time.Now().Before(limit) {
		var n int
		if err := s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%FOR SHARE OF a,r%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			blocked = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("display did not wait on release lock")
	}
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || len(got.page.Items) != 1 || got.page.Items[0].Display != nil || got.page.Items[0].Openable || got.page.Items[0].State != "attention" {
			t.Fatalf("expired key retained signed display: %+v %v", got.page, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("display read did not return")
	}
}
