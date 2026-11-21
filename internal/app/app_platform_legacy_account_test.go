package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

// Accounts that predate users.created_at have NULL there. Their stored identity
// stays empty everywhere (sessions, storage owners, review plans); only viewer
// derivation uses appplatform.AccountCreatedMarker. Access is the OU policy (doc 190, D7), which
// never reads the creation time, so a legacy account opens an app like any other.

func platformMakeLegacyUser(t *testing.T, s *Server, name string) platformActorSession {
	t.Helper()
	if err := s.st.UpsertUser(User{Username: name, PasswordHash: "fixture", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.st.DeleteUser(name) })
	mustExec(t, s.st, `UPDATE users SET created_at=NULL WHERE username=?`, name)
	generation, err := notificationAccountGeneration(s.st.poolExec(), name)
	if err != nil {
		t.Fatal(err)
	}
	session := platformActorSession{username: name, created: s.st.GetUser(name).CreatedAt, sessionID: strings.Repeat("e", 63) + string(rune('a'+len(name)%20)), sessionRevision: s.st.GetUser(name).SessionRev, generation: generation, expires: time.Unix(time.Now().Add(time.Hour).Unix(), 0)}
	if session.created != "" {
		t.Fatal("legacy fixture kept a creation time")
	}
	platformSeedActorSession(t, s.st, session)
	return session
}

// platformLegacyGroup moves the user into a fresh OU and returns it.
func platformLegacyGroup(t *testing.T, s *Server, username, name string) int64 {
	t.Helper()
	group, err := s.st.CreateUserGroup(name, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.st.SetPrimaryGroups([]string{username}, 0)
		_, _, _ = s.st.DeleteUserGroup(group)
	})
	if _, err := s.st.SetPrimaryGroups([]string{username}, group); err != nil {
		t.Fatal(err)
	}
	return group
}

func testPlatformLegacyAccountViewer(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	mustExec(t, s.st, `UPDATE users SET created_at=NULL WHERE username=?`, reader.username)
	reader.created = ""
	platformCloseAccess(t, s, id)
	platformSetAccess(t, s, id, platformLegacyGroup(t, s, reader.username, "Legacy viewer group"), true)
	first, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id)
	if err != nil || len(first.Subject) != 64 {
		t.Fatal("legacy account launch refused", err)
	}
	second, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id)
	if err != nil || second.Subject != first.Subject {
		t.Fatal("legacy subject unstable", err)
	}
	stranger := platformMakeLegacyUser(t, s, "platform-legacy-stranger")
	if v, err := s.readPlatformLaunchViewerContext(context.Background(), stranger, id); err == nil || !reflect.DeepEqual(v, protocol.Viewer{}) {
		t.Fatal("legacy account outside the open OU launched")
	}
}
func TestPlatformLegacyAccountViewerSQLite(t *testing.T)   { testPlatformLegacyAccountViewer(t, false) }
func TestPlatformLegacyAccountViewerPostgres(t *testing.T) { testPlatformLegacyAccountViewer(t, true) }

// (ii) The OU row is written by the real install activation, from the reviewed open_to_ous.
func testPlatformLegacyAccountActivationGrant(t *testing.T, pg bool) {
	s, admin, release := platformInstallFixture(t, pg)
	legacy := platformMakeLegacyUser(t, s, "platform-legacy-audience")
	group := platformLegacyGroup(t, s, legacy.username, "Legacy audience group")
	review, err := s.createPlatformNativeInstallAudienceReview(admin, release.sha256, 1, []platformAudienceTarget{{kind: "ou", ouID: group}})
	if err != nil {
		t.Fatal(err)
	}
	s, _, lease := platformFinishActivationFixture(t, s, admin, review)
	if _, err := s.activatePlatformStaticInstall(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int](t, s.st, `SELECT enabled FROM app_ou_access WHERE app_key=? AND group_id=?`, platformAppKey(lease.installationID), group); got != 1 {
		t.Fatal("open row recorded", got)
	}
	if _, err := s.inspectPlatformLaunchAccess(legacy, lease.installationID); err != nil {
		t.Fatal("legacy audience user cannot open", err)
	}
	first, err := s.readPlatformLaunchViewerContext(context.Background(), legacy, lease.installationID)
	if err != nil || len(first.Subject) != 64 {
		t.Fatal("legacy audience viewer", err)
	}
	if _, err := s.inspectPlatformLaunchAccess(admin, lease.installationID); err == nil {
		t.Fatal("installer outside the opened OU can open")
	}
}
func TestPlatformLegacyAccountActivationGrantSQLite(t *testing.T) {
	testPlatformLegacyAccountActivationGrant(t, false)
}
func TestPlatformLegacyAccountActivationGrantPostgres(t *testing.T) {
	testPlatformLegacyAccountActivationGrant(t, true)
}

// (iii) A legacy user reaches an app opened to its OU.
func testPlatformLegacyAccountOUGrant(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	mustExec(t, s.st, `UPDATE users SET created_at=NULL WHERE username=?`, reader.username)
	reader.created = ""
	platformCloseAccess(t, s, id)
	platformSetAccess(t, s, id, platformLegacyGroup(t, s, reader.username, "Legacy launch group"), true)
	v, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id)
	if err != nil || len(v.Subject) != 64 {
		t.Fatal("OU-granted legacy user refused", err)
	}
}
func TestPlatformLegacyAccountOUGrantSQLite(t *testing.T) { testPlatformLegacyAccountOUGrant(t, false) }
func TestPlatformLegacyAccountOUGrantPostgres(t *testing.T) {
	testPlatformLegacyAccountOUGrant(t, true)
}

// (i) Storage ownership keeps the raw empty marker, so objects stay reachable.
func testPlatformLegacyAccountObjects(t *testing.T, pg bool) {
	s, _, reader, installation := platformObjectsFixturePG(t, pg)
	mustExec(t, s.st, `UPDATE users SET created_at=NULL WHERE username=?`, reader.username)
	reader.created = ""
	binding := platformObjectBindingMap(t, s, reader, installation)
	create := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"first"}`))
	var created struct {
		Object platformObjectSummary `json:"object"`
	}
	if create.Code != http.StatusOK || json.Unmarshal(create.Body.Bytes(), &created) != nil {
		t.Fatalf("legacy object create: %d %s", create.Code, create.Body.String())
	}
	table := platformPluginTableForInstallation(t, s.st, installation)
	if got := scalar[string](t, s.st, `SELECT owner_account_created FROM `+table+` WHERE id=?`, created.Object.ID); got != "" {
		t.Fatal("owner marker rewritten", got)
	}
	get := platformObjectCall(t, s, reader, installation, map[string]any{"binding": binding, "action": "get", "id": created.Object.ID})
	if get.Code != http.StatusOK {
		t.Fatalf("legacy object get: %d", get.Code)
	}
	update := platformObjectCall(t, s, reader, installation, map[string]any{
		"binding": binding, "action": "update", "id": created.Object.ID, "expectedRevision": 1,
		"label": "Updated", "mediaType": "application/json", "contentBase64": base64.StdEncoding.EncodeToString([]byte(`{"body":"updated"}`)),
	})
	if update.Code != http.StatusOK {
		t.Fatalf("legacy object update: %d %s", update.Code, update.Body.String())
	}
}
func TestPlatformLegacyAccountObjectsSQLite(t *testing.T) { testPlatformLegacyAccountObjects(t, false) }
func TestPlatformLegacyAccountObjectsPostgres(t *testing.T) {
	testPlatformLegacyAccountObjects(t, true)
}

// (iv) A plan sponsored by a legacy admin is still consumable: sponsor checks compare raw values.
func testPlatformLegacyAdminSponsor(t *testing.T, pg bool) {
	s, admin, release := platformInstallFixture(t, pg)
	mustExec(t, s.st, `UPDATE users SET created_at=NULL WHERE username=?`, admin.username)
	admin.created = ""
	review, err := s.createPlatformNativeInstallReview(admin, release.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformInstallReview(admin, review.ID, review.SHA256, platformFixtureID(915)); err != nil {
		t.Fatal("legacy admin plan not consumable", err)
	}
}
func TestPlatformLegacyAdminSponsorSQLite(t *testing.T)   { testPlatformLegacyAdminSponsor(t, false) }
func TestPlatformLegacyAdminSponsorPostgres(t *testing.T) { testPlatformLegacyAdminSponsor(t, true) }

// The writable launch, its document and the repeat launch all go through the
// cookie reader, so a NULL created_at must work end to end over HTTP.
func testPlatformLegacyAccountWritableLaunch(t *testing.T, pg bool) {
	s, actor, lease := platformWritableAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE users SET role='user',created_at=NULL WHERE username=?`, actor.username)
	actor.created = ""
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	var source string
	for i := 0; i < 2; i++ {
		launch := sessionTestRequest(mux, "GET", "/api/app-platform/installations/"+lease.installationID+"/launch", cookie, "")
		var payload struct {
			Source string `json:"source"`
		}
		if launch.Code != 200 || json.Unmarshal(launch.Body.Bytes(), &payload) != nil || payload.Source == "" {
			t.Fatal("legacy writable launch", launch.Code, launch.Body.String())
		}
		source = payload.Source
	}
	if doc := sessionTestRequest(mux, "GET", source, cookie, ""); doc.Code != 200 {
		t.Fatal("legacy writable document", doc.Code)
	}
	a, err := s.readPlatformLaunchViewerContext(context.Background(), actor, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.readPlatformLaunchViewerContext(context.Background(), actor, lease.installationID)
	if err != nil || a.Subject != b.Subject || len(a.Subject) != 64 {
		t.Fatal("legacy writable subject unstable", err)
	}
}
func TestPlatformLegacyAccountWritableLaunchSQLite(t *testing.T) {
	testPlatformLegacyAccountWritableLaunch(t, false)
}
func TestPlatformLegacyAccountWritableLaunchPostgres(t *testing.T) {
	testPlatformLegacyAccountWritableLaunch(t, true)
}
