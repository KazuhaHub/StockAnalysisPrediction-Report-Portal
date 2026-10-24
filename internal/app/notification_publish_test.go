package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNotificationPublisherTestUsesSourceSample(t *testing.T) {
	sample := notificationSample("extension.service.example.notice")
	body, err := json.Marshal(map[string]any{"sample_type": sample.Type, "sample": json.RawMessage(sample.Payload)})
	if err != nil {
		t.Fatal(err)
	}
	subject, message := notificationMessage(NotificationEvent{Type: "notification.test", Payload: string(body)})
	if subject != "Notification channel test" || !strings.Contains(message, "Notification channel test") || !strings.Contains(message, "info") {
		t.Fatalf("source sample lost: %q %q", subject, message)
	}
}

func TestNotificationPublisherIsNamespacedAndIdempotent(t *testing.T) {
	testNotificationPublisherIsNamespacedAndIdempotent(t, batchServer(t))
}

func testNotificationPublisherIsNamespacedAndIdempotent(t *testing.T, s *Server) {
	t.Helper()
	if err := s.st.CreateToken("publisher-secret", "Example publisher", "notify:example", ""); err != nil {
		t.Fatal(err)
	}
	call := func(token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/notifications", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.v1PublishNotification(w, r)
		return w
	}
	body := `{"occurrence_id":"outage-1","title":"Example unavailable","message":"Provider has no credit","severity":"warning"}`
	for _, scope := range []string{"all", "ingest", "query"} {
		if err := s.st.CreateToken(scope, "wrong scope", scope, ""); err != nil {
			t.Fatal(err)
		}
		if w := call(scope, body); w.Code != 401 {
			t.Fatalf("%s obtained publication: %d", scope, w.Code)
		}
	}
	first := call("publisher-secret", body)
	if first.Code != 200 {
		t.Fatalf("publish=%d %s", first.Code, first.Body.String())
	}
	var a, b map[string]any
	_ = json.Unmarshal(first.Body.Bytes(), &a)
	second := call("publisher-secret", body)
	_ = json.Unmarshal(second.Body.Bytes(), &b)
	if a["id"] != b["id"] || b["created"] != false {
		t.Fatalf("duplicate=%s", second.Body.String())
	}
	if kind := scalar[string](t, s.st, `SELECT event_type FROM notification_events`); kind != "extension.service.example.notice" {
		t.Fatalf("kind=%s", kind)
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events`); n != 1 {
		t.Fatal("same occurrence created multiple events")
	}
	if w := call("publisher-secret", strings.Replace(body, "no credit", "a changed body", 1)); w.Code != 409 {
		t.Fatalf("changed occurrence=%d %s", w.Code, w.Body.String())
	}
	if w := call("publisher-secret", `{"occurrence_id":"outage-2","title":"Injected","message":"x","severity":"warning","recipients":["someone@example.com"]}`); w.Code != 400 {
		t.Fatal("publisher accepted destination injection")
	}
}

func TestNotificationPublisherQuotaDoesNotLeavePartialCapture(t *testing.T) {
	s := batchServer(t)
	if err := s.st.CreateToken("publisher-secret", "Example", "notify:example", ""); err != nil {
		t.Fatal(err)
	}
	for i := range 120 {
		id := fmt.Sprintf("earlier-%d", i)
		if _, err := s.st.exec(`INSERT INTO notification_events(id,source_key,event_type,payload,resource_type,resource_id,created_at) VALUES(?,?,?,'{}','extension','service.example',?)`, id, id, "extension.service.example.notice", time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/notifications", strings.NewReader(`{"occurrence_id":"too-many","title":"Notice","message":"Example body","severity":"info"}`))
	r.Header.Set("Authorization", "Bearer publisher-secret")
	w := httptest.NewRecorder()
	s.v1PublishNotification(w, r)
	if w.Code != 429 {
		t.Fatalf("quota=%d %s", w.Code, w.Body.String())
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events`); n != 120 {
		t.Fatalf("partial quota event=%d", n)
	}
}

func TestNotificationAppPublisherRechecksIssuerAndInstallation(t *testing.T) {
	s := batchServer(t)
	s.appTok = newAppTokens(time.Minute)
	if err := s.st.UpsertUser(User{Username: "admin", Role: "admin", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}
	app := App{ID: "demo", Name: "Demo", Version: "1", Entry: "index.html", Scopes: []string{"notify"}}
	if err := s.st.InstallApp(app, nil); err != nil {
		t.Fatal(err)
	}
	token := s.appTok.mintForApp([]string{"notify"}, time.Now(), app.ID, s.st.GetUser("admin"))
	r := httptest.NewRequest("POST", "/api/v1/notifications", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if name, ok := s.notificationPublisher(r); !ok || name != "app.demo" {
		t.Fatalf("publisher=%s %v", name, ok)
	}
	if err := s.st.SetUserRole("admin", "user"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.notificationPublisher(r); ok {
		t.Fatal("demoted issuer retained publish permission")
	}
	if err := s.st.SetUserRole("admin", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.st.DeleteApp(app.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.notificationPublisher(r); ok {
		t.Fatal("uninstalled app retained publish permission")
	}
}

func TestNotificationPublisherUsesAdministratorRulesForInbox(t *testing.T) {
	s := batchServer(t)
	if err := s.st.CreateToken("service", "Sender", "notify:example", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertUser(User{Username: "reader", Role: "user", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.InsertNotificationChannel(NotificationChannel{ID: "inbox", Adapter: "inbox", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SaveNotificationRoute(NotificationRoute{ID: "source-inbox", ChannelID: "inbox", Events: "extension.service.example.notice", RecipientMode: "grant", Enabled: true, Grants: []string{userPrincipal("reader")}}, 0); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/notifications", strings.NewReader(`{"occurrence_id":"1","title":"Notice","message":"Example body","severity":"info"}`))
	r.Header.Set("Authorization", "Bearer service")
	w := httptest.NewRecorder()
	s.v1PublishNotification(w, r)
	if w.Code != 200 {
		t.Fatalf("publish=%d %s", w.Code, w.Body.String())
	}
	items, err := s.notificationInboxItems("reader")
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox=%v %v", items, err)
	}
	if _, err := s.st.exec(`DELETE FROM notification_route_grants`); err != nil {
		t.Fatal(err)
	}
	items, err = s.notificationInboxItems("reader")
	if err != nil || len(items) != 0 {
		t.Fatal("source event survived audience revocation")
	}
}

func TestNotificationPublisherIdentityReadIsDedicatedAndReadOnly(t *testing.T) {
	s := batchServer(t)
	for _, scope := range []string{"all", "query", "ingest", "notify:status-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		token := "identity-" + scope
		if err := s.st.CreateToken(token, "Publisher identity", scope, ""); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", "/api/v1/notifications/publisher", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.v1NotificationPublisherIdentity(w, r)
		if strings.HasPrefix(scope, "notify:") {
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"publisher":"service.status-`) {
				t.Fatalf("identity=%d %s", w.Code, w.Body.String())
			}
		} else if w.Code != 401 {
			t.Fatal("non-publication token introspected publisher")
		}
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events`); n != 0 {
		t.Fatal("identity check published a notification")
	}
}
