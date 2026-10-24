package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotificationFireEventCapturesBeforeNetwork(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	var calls atomic.Int32
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer recv.Close()
	if _, err := st.CreateWebhook(recv.URL, []string{EventReportIngested}, "secret"); err != nil {
		t.Fatal(err)
	}
	s.fireEvent(EventReportIngested, map[string]any{"id": int64(9007199254740993)})
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_events`); n != 1 {
		t.Fatalf("durable events=%d", n)
	}
	if calls.Load() != 0 {
		t.Fatal("producer performed network delivery")
	}
	restarted := &Server{st: st}
	if err := restarted.processNotificationOnce(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("restarted delivery calls=%d", calls.Load())
	}
}

func TestNotificationForecastHasOneEventAndThreeDeliveries(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st, slackHostForTest: "127.0.0.1"}
	mail := enableEmail(s)
	if err := st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	var rawHits, slackHits atomic.Int32
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rawHits.Add(1); w.WriteHeader(http.StatusOK) }))
	defer recv.Close()
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackHits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 {
			t.Error("missing Slack body")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer slack.Close()
	if err := st.SetSetting(setForecastAlertSlackURL, slack.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateWebhook(recv.URL, []string{EventForecastEvaluatorUnhealthy}, "secret"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.emitForecastAlert("stalled", "stale heartbeat", now, time.Hour)
	s.emitForecastAlert("stalled", "stale heartbeat", now.Add(time.Minute), time.Hour)
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_events`); n != 1 {
		t.Fatalf("events=%d", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries`); n != 3 {
		t.Fatalf("deliveries=%d", n)
	}
	for range 4 {
		if err := s.processNotificationOnce(context.Background(), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	_ = wantEmail(t, mail)
	wantNoEmail(t, mail)
	if rawHits.Load() != 1 || slackHits.Load() != 1 {
		t.Fatalf("raw=%d slack=%d", rawHits.Load(), slackHits.Load())
	}
	if !st.forecastAlertState().Channels["slack"].OK {
		t.Fatal("forecast compatibility view has no accepted Slack result")
	}
}

func TestNotificationForecastTestRejectsConcurrentConfigChange(t *testing.T) {
	for _, change := range []string{"recipients", "smtp", "slack", "round-trip"} {
		t.Run(change, func(t *testing.T) {
			s := newV1Server(t)
			s.slackHostForTest = "127.0.0.1"
			var oldHits, newHits atomic.Int32
			old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { oldHits.Add(1); w.WriteHeader(http.StatusOK) }))
			defer old.Close()
			newDest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { newHits.Add(1); w.WriteHeader(http.StatusOK) }))
			defer newDest.Close()
			if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
				t.Fatal(err)
			}
			if err := s.st.SetSetting(setForecastAlertSlackURL, old.URL); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			s.mailFn = func([]string, string, string) error { close(entered); <-release; return nil }
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rec := httptest.NewRecorder()
				s.apiForecastAlertTest(rec, httptest.NewRequest("POST", "/test", nil), "admin")
				done <- rec
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				close(release)
				t.Fatal("test did not reach email adapter")
			}
			switch change {
			case "smtp":
				rec := httptest.NewRecorder()
				s.apiEmailSave(rec, httptest.NewRequest("POST", "/save", strings.NewReader(`{"host":"replacement.example.com"}`)), "admin")
				if rec.Code != http.StatusOK {
					close(release)
					t.Fatalf("save=%d", rec.Code)
				}
			case "recipients":
				if rec := saveForecastSettings(t, s, `{"alert_email_to":"new@example.com"}`); rec.Code != http.StatusOK {
					close(release)
					t.Fatalf("save=%d", rec.Code)
				}
			case "slack":
				if rec := saveForecastSettings(t, s, `{"alert_slack_url":`+strconvQuote(newDest.URL)+`}`); rec.Code != http.StatusOK {
					close(release)
					t.Fatalf("save=%d", rec.Code)
				}
			case "round-trip":
				if rec := saveForecastSettings(t, s, `{"alert_email_to":"new@example.com"}`); rec.Code != http.StatusOK {
					close(release)
					t.Fatalf("save=%d", rec.Code)
				}
				if rec := saveForecastSettings(t, s, `{"alert_email_to":"ops@example.com"}`); rec.Code != http.StatusOK {
					close(release)
					t.Fatalf("save=%d", rec.Code)
				}
			}
			close(release)
			select {
			case rec := <-done:
				if rec.Code != http.StatusConflict {
					t.Fatalf("test status=%d body=%s", rec.Code, rec.Body.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("test did not finish")
			}
			if got := s.st.GetSetting(setForecastAlertTestOKAt, ""); got != "" {
				t.Fatal("changed configuration received a readiness stamp")
			}
			if oldHits.Load() != 1 || newHits.Load() != 0 {
				t.Fatalf("test followed replacement destination: old=%d new=%d", oldHits.Load(), newHits.Load())
			}
		})
	}
}
