package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPlatformServiceNotificationUsesCentralRulesAndOccurrence(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	issued, _, err := s.issuePlatformServiceIdentity(context.Background(), installation, platformFixtureID(5201), []string{"notifications.publish"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	eventType := "extension.app." + scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, installation) + ".notice"
	if !slices.Contains(s.notificationEvents(), eventType) {
		t.Fatal("application event missing from administrator catalogue", eventType)
	}
	var hits atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "Fixture notice") || strings.Contains(string(body), "ops@example.com") {
			t.Error("unexpected notification body")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	if _, err := s.st.CreateWebhook(receiver.URL, []string{eventType}, "fixture-notify-secret"); err != nil {
		t.Fatal(err)
	}
	call := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/api/app-platform/installations/"+installation+"/notifications/background", strings.NewReader(string(raw)))
		r.SetPathValue("installation", installation)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Portal-Service-Identity", base64.RawURLEncoding.EncodeToString(issued))
		w := httptest.NewRecorder()
		s.apiPlatformServiceNotificationPublish(w, r)
		return w
	}
	body := map[string]any{"occurrenceId": "check-1", "title": "Fixture notice", "message": "Central routing only", "severity": "info"}
	first := call(body)
	if first.Code != 200 {
		t.Fatal("publish", first.Code, first.Body.String())
	}
	repeat := call(body)
	if repeat.Code != 200 || !strings.Contains(repeat.Body.String(), `"created":false`) {
		t.Fatal("idempotent app occurrence", repeat.Code, repeat.Body.String())
	}
	changed := call(map[string]any{"occurrenceId": "check-1", "title": "Changed", "message": "Different", "severity": "error"})
	if changed.Code != 409 {
		t.Fatal("changed occurrence", changed.Code, changed.Body.String())
	}
	injection := call(map[string]any{"occurrenceId": "check-2", "title": "Injected", "message": "x", "severity": "info", "recipients": []string{"ops@example.com"}})
	if injection.Code != 400 {
		t.Fatal("application selected recipients", injection.Code, injection.Body.String())
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events`) != 1 {
		t.Fatal("duplicate or rejected request captured an event")
	}
	if err := s.processNotificationOnce(context.Background(), time.Now().UTC()); err != nil || hits.Load() != 1 {
		t.Fatal("central webhook delivery", err, hits.Load())
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='notifications.publish'`, installation)
	revoked := call(map[string]any{"occurrenceId": "check-3", "title": "Revoked", "message": "x", "severity": "info"})
	if revoked.Code != 404 {
		t.Fatal("revoked application published", revoked.Code, revoked.Body.String())
	}
}

func TestPlatformNotificationForegroundRequiresSessionAndLaunchBinding(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	body := `{"binding":{"generation":"1","policyRevision":"1","releaseSha256":"` + strings.Repeat("a", 64) + `"},"occurrenceId":"foreground","title":"Fixture","message":"x","severity":"info"}`
	request := httptest.NewRequest(http.MethodPost, "/api/app-platform/installations/"+installation+"/notifications", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != 401 {
		t.Fatal("anonymous foreground notification", w.Code, w.Body.String())
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events`) != 0 {
		t.Fatal("anonymous request captured a notification")
	}
}
