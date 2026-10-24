package app

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNotificationInboxChecksAuthorityAndSnapshot(t *testing.T) {
	testNotificationInboxChecksAuthorityAndSnapshot(t, batchServer(t))
}

func testNotificationInboxChecksAuthorityAndSnapshot(t *testing.T, s *Server) {
	t.Helper()
	for _, name := range []string{"alice", "bob"} {
		if err := s.st.UpsertUser(User{Username: name, Role: "admin", PasswordHash: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.st.InsertNotificationChannel(NotificationChannel{ID: "inbox", Adapter: "inbox", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SaveNotificationRoute(NotificationRoute{ID: "rule", ChannelID: "inbox", Events: EventForecastEvaluatorUnhealthy, RecipientMode: "grant", Enabled: true, Grants: []string{userPrincipal("alice")}}, 0); err != nil {
		t.Fatal(err)
	}
	add := func(id string) {
		e := NotificationEvent{ID: id, SourceKey: id, Type: EventForecastEvaluatorUnhealthy, ResourceType: "operations", Payload: `{"reason":"stalled"}`, CreatedAt: time.Now().UnixMilli()}
		if err := s.st.CaptureNotification(e, nil, []string{"alice"}); err != nil {
			t.Fatal(err)
		}
	}
	call := func(h handler, method, body, user string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(method, "/api/notifications", strings.NewReader(body)), user)
		return w
	}
	add("first")
	w := call(s.apiNotificationInbox, "GET", "", "alice")
	if w.Code != 200 {
		t.Fatalf("inbox=%d %s", w.Code, w.Body.String())
	}
	var inbox struct {
		Items  []notificationInboxItem `json:"items"`
		Unread int                     `json:"unread"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &inbox); err != nil || len(inbox.Items) != 1 || inbox.Unread != 1 {
		t.Fatalf("inbox=%s", w.Body.String())
	}
	snap := call(s.apiNotificationReadSnapshot, "POST", `{}`, "alice")
	var snapshot struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(snap.Body.Bytes(), &snapshot); err != nil || snapshot.Token == "" {
		t.Fatalf("snapshot=%s", snap.Body.String())
	}
	if w := call(s.apiNotificationReadAll, "POST", `{"token":"`+snapshot.Token+`tampered"}`, "alice"); w.Code != 409 {
		t.Fatal("tampered read-all snapshot was accepted")
	}
	add("later")
	if w := call(s.apiNotificationReadAll, "POST", `{"token":"`+snapshot.Token+`"}`, "bob"); w.Code == 200 {
		t.Fatal("another account used a read-all snapshot")
	}
	if w := call(s.apiNotificationReadAll, "POST", `{"token":"`+snapshot.Token+`"}`, "alice"); w.Code != 200 {
		t.Fatalf("read-all=%d %s", w.Code, w.Body.String())
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_recipients WHERE read_at=0`); n != 1 {
		t.Fatal("read-all marked a later arrival as read")
	}
	if err := s.st.SetUserRole("alice", "user"); err != nil {
		t.Fatal(err)
	}
	w = call(s.apiNotificationInbox, "GET", "", "alice")
	if strings.Contains(w.Body.String(), "stalled") || strings.Contains(w.Body.String(), "first") {
		t.Fatal("revoked authority leaked old notification content")
	}
}

func TestNotificationInboxRuleRevocationHidesCountAndMutation(t *testing.T) {
	s := batchServer(t)
	if err := s.st.UpsertUser(User{Username: "operator", Role: "admin", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.InsertNotificationChannel(NotificationChannel{ID: "inbox", Adapter: "inbox", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SaveNotificationRoute(NotificationRoute{ID: "rule", ChannelID: "inbox", Events: EventForecastEvaluatorUnhealthy, RecipientMode: "grant", Enabled: true, Grants: []string{userPrincipal("operator")}}, 0); err != nil {
		t.Fatal(err)
	}
	s.fireEvent(EventForecastEvaluatorUnhealthy, map[string]string{"reason": "stalled"})
	items, err := s.notificationInboxItems("operator")
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if _, err := s.st.exec(`DELETE FROM notification_route_grants WHERE route_id='rule'`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.apiNotificationUnread(w, httptest.NewRequest("GET", "/", nil), "operator")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"unread":0`) {
		t.Fatalf("revoked count=%s", w.Body.String())
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"action":"read"}`))
	r.SetPathValue("id", items[0].ID)
	w = httptest.NewRecorder()
	s.apiNotificationInboxUpdate(w, r, "operator")
	if w.Code != 404 {
		t.Fatalf("revoked item mutation=%d %s", w.Code, w.Body.String())
	}
}
