package app

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Accounts that predate users.created_at have NULL there. They must still launch
// with a subject that is stable across launches, and grants must bind to them.
func testPlatformLegacyAccountViewer(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	mustExec(t, s.st, `UPDATE users SET created_at=NULL WHERE username=?`, reader.username)
	// The session carries what the cookie reader derives from the stored account.
	reader.created = appplatform.AccountCreatedMarker(s.st.GetUser(reader.username).CreatedAt)
	// The grant is written the way the audience resolver writes it for a legacy user.
	audience, err := platformAudienceUser(s.st.poolExec(), reader.username, 0)
	if err != nil || audience.AccountCreated != appplatform.LegacyAccountCreated {
		t.Fatal("legacy account audience", audience, err)
	}
	platformGrantLaunchUser(t, s, reader, id)
	first, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id)
	if err != nil || len(first.Subject) != 64 {
		t.Fatal("legacy account launch refused", err)
	}
	second, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id)
	if err != nil || second.Subject != first.Subject {
		t.Fatal("legacy subject unstable", err)
	}
	other := "platform-launch-other"
	if err := s.st.UpsertUser(User{Username: other, PasswordHash: "fixture", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.st.DeleteUser(other) })
	mustExec(t, s.st, `UPDATE users SET created_at=NULL WHERE username=?`, other)
	generation, err := notificationAccountGeneration(s.st.poolExec(), other)
	if err != nil {
		t.Fatal(err)
	}
	stranger := platformActorSession{username: other, created: appplatform.LegacyAccountCreated, sessionID: strings.Repeat("d", 64), sessionRevision: s.st.GetUser(other).SessionRev, generation: generation, expires: reader.expires}
	platformSeedActorSession(t, s.st, stranger)
	if v, err := s.readPlatformLaunchViewerContext(context.Background(), stranger, id); err == nil || !reflect.DeepEqual(v, protocol.Viewer{}) {
		t.Fatal("ungranted legacy account launched")
	}
}
func TestPlatformLegacyAccountViewerSQLite(t *testing.T)   { testPlatformLegacyAccountViewer(t, false) }
func TestPlatformLegacyAccountViewerPostgres(t *testing.T) { testPlatformLegacyAccountViewer(t, true) }

// The writable launch, its document and the repeat launch all go through the
// cookie reader, so a NULL created_at must work end to end over HTTP.
func testPlatformLegacyAccountWritableLaunch(t *testing.T, pg bool) {
	s, actor, lease := platformWritableAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE users SET role='user',created_at=NULL WHERE username=?`, actor.username)
	mustExec(t, s.st, `UPDATE app_platform_access_grants SET account_created=? WHERE installation_id=?`, appplatform.LegacyAccountCreated, lease.installationID)
	actor.created = appplatform.LegacyAccountCreated
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
