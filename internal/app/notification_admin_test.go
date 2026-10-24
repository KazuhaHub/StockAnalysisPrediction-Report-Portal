package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/webhook"
)

func notificationAdminCall(t *testing.T, s *Server, h handler, method, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/admin/notifications", strings.NewReader(body))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h(w, r, "admin")
	return w
}

func TestNotificationLegacyTestsShareManualCapacity(t *testing.T) {
	s := batchServer(t)
	enableEmail(s)
	id, err := s.st.CreateWebhook("http://127.0.0.1:1/test", []string{EventReportIngested}, "")
	if err != nil {
		t.Fatal(err)
	}
	s.notificationTestSlots = make(chan struct{}, 2)
	s.notificationTestSlots <- struct{}{}
	s.notificationTestSlots <- struct{}{}
	for name, h := range map[string]handler{"email": s.apiEmailTest, "webhook": s.apiWebhookTest, "forecast": s.apiForecastAlertTest} {
		t.Run(name, func(t *testing.T) {
			w := notificationAdminCall(t, s, h, "POST", itoa64(id), `{"to":"review@example.com"}`)
			if w.Code != http.StatusTooManyRequests {
				t.Fatalf("legacy test bypassed capacity: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestNotificationAdminLegacyFacadesShareDestinations(t *testing.T) {
	s := batchServer(t)
	if err := s.adoptNotificationChannels(); err != nil {
		t.Fatal(err)
	}
	saved := notificationAdminCall(t, s, s.apiNotificationChannelSave, "PUT", "legacy:forecast-email", `{"label":"Forecast email","revision":1,"recipients":["new@example.com"]}`)
	if saved.Code != 200 || s.st.GetSetting(setForecastAlertEmailTo, "") != "new@example.com" {
		t.Fatalf("forecast alias=%d %s", saved.Code, saved.Body.String())
	}
	id, err := s.st.CreateWebhook("https://old.example/hook", []string{EventReportIngested}, "old-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.adoptNotificationChannels(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE webhooks SET active=0 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	channel := "legacy:webhook:" + itoa64(id)
	updated := notificationAdminCall(t, s, s.apiNotificationChannelSave, "PUT", channel, `{"label":"Receiver","revision":1,"url":"https://new.example/secret-path","signing_secret":"new-secret","enabled":true}`)
	if updated.Code != 200 {
		t.Fatalf("reactivation=%d %s", updated.Code, updated.Body.String())
	}
	hook, ok := s.st.GetWebhook(id)
	if !ok || hook.URL != "https://new.example/secret-path" || hook.Secret != "new-secret" || !hook.Active {
		t.Fatal("legacy receiver did not reflect canonical save")
	}
	if strings.Contains(updated.Body.String(), "secret-path") || strings.Contains(updated.Body.String(), "new-secret") {
		t.Fatal("canonical response exposed legacy credentials")
	}
	if err := s.st.DeleteWebhook(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.NotificationChannel(channel); err == nil {
		t.Fatal("legacy deletion left its canonical destination active")
	}
}

func TestNotificationAdminPausedForecastCannotKeepReadiness(t *testing.T) {
	s := batchServer(t)
	if err := s.adoptNotificationChannels(); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetSetting(setForecastAlertTestOKAt, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	w := notificationAdminCall(t, s, s.apiNotificationChannelSave, "PUT", "legacy:forecast-slack", `{"label":"Forecast Slack","revision":1,"enabled":false,"paused":true}`)
	if w.Code != 200 {
		t.Fatalf("pause=%d %s", w.Code, w.Body.String())
	}
	if stamp := s.st.GetSetting(setForecastAlertTestOKAt, ""); stamp != "" {
		t.Fatal("paused forecast retained a readiness stamp")
	}
	test := notificationAdminCall(t, s, s.apiForecastAlertTest, "POST", "", `{}`)
	if test.Code != http.StatusConflict {
		t.Fatalf("paused forecast verified: %d %s", test.Code, test.Body.String())
	}
}

func TestNotificationAdminManualTestIsSingleAndRecorded(t *testing.T) {
	s := batchServer(t)
	bodies := make(chan string, 2)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if got := r.Header.Get(webhook.SignatureHeader); got != "sha256="+webhook.Sign("signing-secret", body) {
			t.Errorf("incorrect new-channel signature: %s", got)
		}
		if kind := r.Header.Get(webhook.EventHeader); kind != "notification.test" {
			t.Errorf("test masqueraded as business event: %s", kind)
		}
		bodies <- string(body)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer recv.Close()
	create := notificationAdminCall(t, s, s.apiNotificationChannelSave, "POST", "", `{"label":"Raw","adapter":"webhook","url":"`+recv.URL+`/private","signing_secret":"signing-secret","enabled":true}`)
	if create.Code != 200 {
		t.Fatalf("create=%d %s", create.Code, create.Body.String())
	}
	var result struct {
		Channel notificationChannelView `json:"channel"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	// No other subscriber may receive an explicit channel test.
	if _, err := s.st.CreateWebhook("http://unreachable.invalid", []string{EventReportIngested}, ""); err != nil {
		t.Fatal(err)
	}
	test := notificationAdminCall(t, s, s.apiNotificationChannelTest, "POST", result.Channel.ID, `{"revision":1,"event_type":"report.ingested"}`)
	var seen string
	select {
	case seen = <-bodies:
	default:
		t.Fatal("test did not reach receiver")
	}
	if test.Code != http.StatusBadGateway || seen == "" {
		t.Fatalf("test=%d %s body=%s", test.Code, test.Body.String(), seen)
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_attempts`); n != 1 {
		t.Fatalf("test attempts=%d", n)
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_deliveries`); n != 1 {
		t.Fatalf("test fan-out=%d", n)
	}
	if state := scalar[string](t, s.st, `SELECT state FROM notification_deliveries`); state != "failed" {
		t.Fatalf("test entered automatic retry: %s", state)
	}
	if strings.Contains(test.Body.String(), "/private") || strings.Contains(test.Body.String(), "signing-secret") {
		t.Fatal("test result exposed destination credentials")
	}
}

func TestNotificationAdminRoutesRefuseOrdinaryAccounts(t *testing.T) {
	s := routeTableServer(t)
	if err := s.st.UpsertUser(User{Username: "reader", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	u := s.st.GetUser("reader")
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	n := 0
	for _, row := range s.routeTable {
		if !strings.Contains(row.pattern, "/api/admin/notifications") {
			continue
		}
		n++
		parts := strings.SplitN(row.pattern, " ", 2)
		path := strings.ReplaceAll(parts[1], "{id}", "missing")
		r := httptest.NewRequest(parts[0], path, strings.NewReader(`{}`))
		r.AddCookie(&http.Cookie{Name: cookieName, Value: s.signUserFor(*u, 0)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("ordinary account reached %s: %d %s", row.pattern, w.Code, w.Body.String())
		}
	}
	if n < 10 {
		t.Fatalf("notification routes missing: %d", n)
	}
}

func TestNotificationAdminPauseDoesNotInvalidatePendingMessage(t *testing.T) {
	s := batchServer(t)
	sent := enableEmail(s)
	w := notificationAdminCall(t, s, s.apiNotificationChannelSave, "POST", "", `{"label":"Mail","adapter":"email","recipients":["ops@example.com"],"enabled":true}`)
	if w.Code != 200 {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	var result struct {
		Channel notificationChannelView `json:"channel"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	c, err := s.st.NotificationChannel(result.Channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := s.effectiveNotificationChannel(c)
	if err != nil {
		t.Fatal(err)
	}
	e := NotificationEvent{ID: "pause-event", SourceKey: "pause-source", Type: EventBatchFinished, Payload: `{}`, CreatedAt: time.Now().UnixMilli()}
	if err := s.st.CaptureNotification(e, []NotificationDelivery{{ChannelID: c.ID, ChannelRevision: revision}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"label":"Mail","revision":1,"paused":true}`, `{"label":"Mail","revision":2,"paused":false}`} {
		w := notificationAdminCall(t, s, s.apiNotificationChannelSave, "PUT", c.ID, body)
		if w.Code != 200 {
			t.Fatalf("pause toggle=%d %s", w.Code, w.Body.String())
		}
	}
	if err := s.processNotificationOnce(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_ = wantEmail(t, sent)
	if state := scalar[string](t, s.st, `SELECT state FROM notification_deliveries`); state != "accepted" {
		t.Fatalf("state=%s", state)
	}
}

func TestNotificationAdminChannelSecretsAndRevision(t *testing.T) {
	s := batchServer(t)
	create := notificationAdminCall(t, s, s.apiNotificationChannelSave, "POST", "", `{"label":"Slack","adapter":"slack","url":"https://hooks.slack.com/services/private-token","enabled":true}`)
	if create.Code != http.StatusOK {
		t.Fatalf("create=%d %s", create.Code, create.Body.String())
	}
	if strings.Contains(create.Body.String(), "private-token") {
		t.Fatal("create exposed a destination secret")
	}
	var result struct {
		Channel struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	c, err := s.st.NotificationChannel(result.Channel.ID)
	if err != nil || c.Secret == "" || strings.Contains(c.Secret, "private-token") {
		t.Fatalf("destination not sealed: %v", err)
	}
	list := notificationAdminCall(t, s, s.apiNotifications, "GET", "", "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "private-token") {
		t.Fatalf("list=%d %s", list.Code, list.Body.String())
	}
	update := notificationAdminCall(t, s, s.apiNotificationChannelSave, "PUT", c.ID, `{"label":"Renamed","adapter":"slack","revision":1,"url":"","enabled":true}`)
	if update.Code != http.StatusOK {
		t.Fatalf("update=%d %s", update.Code, update.Body.String())
	}
	after, err := s.st.NotificationChannel(c.ID)
	if err != nil || after.Secret != c.Secret {
		t.Fatal("blank secret input did not preserve stored destination")
	}
	stale := notificationAdminCall(t, s, s.apiNotificationChannelSave, "PUT", c.ID, `{"label":"Stale","adapter":"slack","revision":1,"url":"https://hooks.slack.com/services/replacement","enabled":true}`)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update=%d %s", stale.Code, stale.Body.String())
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events`); n != 0 {
		t.Fatal("configuration save published an event")
	}
}

func TestNotificationAdminRejectsInvalidAdapterAndRoutes(t *testing.T) {
	s := batchServer(t)
	for _, body := range []string{
		`{"label":"Bad","adapter":"slack","url":"https://example.com/secret","enabled":true}`,
		`{"label":"Bad","adapter":"webhook","url":"file:///etc/passwd","enabled":true}`,
		`{"label":"Bad","adapter":"code","enabled":true}`,
	} {
		w := notificationAdminCall(t, s, s.apiNotificationChannelSave, "POST", "", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid create=%d %s", w.Code, w.Body.String())
		}
	}
	if err := s.st.InsertNotificationChannel(NotificationChannel{ID: "inbox", Adapter: "inbox", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"label":"Bad","channel_id":"inbox","events":["not.registered"],"recipient_mode":"grant","enabled":true,"grants":[]}`,
		`{"label":"Bad","channel_id":"inbox","events":["report.ingested"],"recipient_mode":"destination","enabled":true,"grants":[]}`,
		`{"label":"Bad","channel_id":"inbox","events":["report.ingested"],"recipient_mode":"grant","enabled":true,"grants":["u:missing"]}`,
	} {
		w := notificationAdminCall(t, s, s.apiNotificationRouteSave, "POST", "", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid route=%d %s", w.Code, w.Body.String())
		}
	}
}
