package app

// P41: the two channels beside the webhook. Email is the sendEmail seam; Slack is a real POST to an
// httptest server registered through the test-only host allowance, so the wire format is exercised.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mailSink struct {
	mu   sync.Mutex
	to   []string
	subj string
	body string
	n    int
}

func (m *mailSink) send(to []string, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.n++
	m.to, m.subj, m.body = to, subject, body
	return nil
}

// TestAlertGoesToBothChannels: one watchdog event reaches the mailbox and the Slack webhook, and the
// text names the reason. De-duplication is the watchdog's, so exactly one of each arrives.
func TestAlertGoesToBothChannels(t *testing.T) {
	for _, reason := range []string{"stalled", "no_source"} {
		t.Run(reason, func(t *testing.T) { assertWatchdogAlertChannels(t, reason) })
	}
}

func assertWatchdogAlertChannels(t *testing.T, reason string) {
	t.Helper()
	s := newV1Server(t)
	mail := &mailSink{}
	s.mailFn = mail.send
	if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}

	type slackPost struct{ body string }
	posts := make(chan slackPost, 4)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posts <- slackPost{string(b)}
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()
	s.slackHostForTest = "127.0.0.1"
	if _, err := s.slackURLAllowed(stub.URL); err != nil {
		t.Fatalf("test URL refused: %v", err)
	}
	if err := s.st.SetSetting(setForecastAlertSlackURL, stub.URL); err != nil {
		t.Fatal(err)
	}

	if err := s.st.SetSetting(setForecastAuto, forecastModeOn); err != nil {
		t.Fatal(err)
	}
	heartbeatAt := time.Now().UTC().Add(-time.Hour)
	if reason == "no_source" {
		heartbeatAt = time.Now().UTC()
		if err := s.st.SetSetting(setQuoteSourceOrder, quoteSourceSina); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.st.saveForecastHeartbeat(forecastHeartbeat{
		Mode: forecastModeOn, LastTickAt: heartbeatAt.Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	startNotificationRuntimeForTest(t, s)
	s.forecastWatchdogOnce(time.Now())

	select {
	case p := <-posts:
		var msg struct{ Text string }
		if err := json.Unmarshal([]byte(p.body), &msg); err != nil {
			t.Fatalf("slack body: %v (%s)", err, p.body)
		}
		if !strings.Contains(msg.Text, reason) {
			t.Errorf("slack text does not name the reason: %q", msg.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no Slack delivery")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		mail.mu.Lock()
		n := mail.n
		mail.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no email delivery")
		}
		time.Sleep(20 * time.Millisecond)
	}
	mail.mu.Lock()
	defer mail.mu.Unlock()
	if len(mail.to) != 1 || mail.to[0] != "ops@example.com" {
		t.Errorf("recipients = %v", mail.to)
	}
	if !strings.Contains(mail.body, reason) {
		t.Errorf("email body does not name the reason: %q", mail.body)
	}
	// One deduplicated reason means one delivery per channel.
	select {
	case p := <-posts:
		t.Errorf("a second Slack POST arrived: %s", p.body)
	case <-time.After(300 * time.Millisecond):
	}
	if mail.n != 1 {
		t.Errorf("emails = %d, want 1", mail.n)
	}
}

// TestSlackURLIsWriteOnlyAndHostLocked: the GET never returns the secret, and only https://hooks.slack.com
// (plus the test loopback allowance) is accepted.
func TestSlackURLIsWriteOnlyAndHostLocked(t *testing.T) {
	s := newV1Server(t)
	secret := "https://hooks.slack.com/services/T000/B000/verysecret"
	for _, tc := range []struct {
		url  string
		want int
	}{
		{"http://hooks.slack.com/services/x", http.StatusBadRequest},
		{"https://evil.example/services/x", http.StatusBadRequest},
		{"https://127.0.0.1/services/x", http.StatusBadRequest},
		{"https://hooks.slack.com.evil.example/services/x", http.StatusBadRequest},
		{secret, http.StatusOK},
	} {
		rec := saveForecastSettings(t, s, `{"alert_slack_url":`+strconvQuote(tc.url)+`}`)
		if rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", tc.url, rec.Code, tc.want, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	s.apiForecastSettings(rec, httptest.NewRequest("GET", "/api/admin/forecast", nil), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "verysecret") || strings.Contains(body, "hooks.slack.com/services") {
		t.Errorf("GET returned the Slack URL: %s", body)
	}
	var got struct {
		Alerts struct {
			HasSlack bool `json:"has_slack"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Alerts.HasSlack {
		t.Error("has_slack = false after a successful save")
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestAlertTestOKOnlyWhenBothSucceed: email disabled is a failure, so the stamp is not written; when
// both channels answer, it is.
func TestAlertTestOKOnlyWhenBothSucceed(t *testing.T) {
	s := newV1Server(t)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()
	s.slackHostForTest = "127.0.0.1"
	if err := s.st.SetSetting(setForecastAlertSlackURL, stub.URL); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}

	call := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiForecastAlertTest(rec, httptest.NewRequest("POST", "/api/admin/forecast/alert-test", nil), "admin")
		return rec
	}
	rec := call()
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("without email: %d %s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting(setForecastAlertTestOKAt, ""); got != "" {
		t.Errorf("test_ok_at written by a failed test: %q", got)
	}

	s.mailFn = func([]string, string, string) error { return nil }
	rec = call()
	if rec.Code != http.StatusOK {
		t.Fatalf("with both: %d %s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting(setForecastAlertTestOKAt, ""); got == "" {
		t.Error("test_ok_at not written after both channels succeeded")
	}
}

// TestAlertAuditNamesFieldsNotValues: the log says which knob moved, never the secret or the address.
func TestAlertAuditNamesFieldsNotValues(t *testing.T) {
	s := newV1Server(t)
	s.slackHostForTest = "127.0.0.1"
	secret := "http://127.0.0.1:9999/services/leak"
	body := `{"alert_email_to":"ops@example.com","alert_slack_url":` + strconvQuote(secret) + `}`
	if rec := saveForecastSettings(t, s, body); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	rows := auditRows(t, s, AuditForecastAlert)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	d := rows[0].Detail
	if !strings.Contains(d, "AlertSlackURL") || !strings.Contains(d, "AlertEmailTo") {
		t.Errorf("detail does not name the fields: %s", d)
	}
	if strings.Contains(d, "leak") || strings.Contains(d, "ops@example.com") {
		t.Errorf("detail carries a value: %s", d)
	}
}

// TestStoreFailedAlertsBothChannelsWithoutDelayingTheIngest: the response does not wait for a slow
// Slack, and exactly one webhook and one alert per channel are produced.
func TestStoreFailedAlertsBothChannelsWithoutDelayingTheIngest(t *testing.T) {
	s := newV1Server(t)
	mail := &mailSink{}
	s.mailFn = mail.send
	if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	var slackHits int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&slackHits, 1)
		<-release // blocks until the test has seen the response
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()
	defer releaseNow() // LIFO: unblock the handler before Close waits on it
	s.slackHostForTest = "127.0.0.1"
	if err := s.st.SetSetting(setForecastAlertSlackURL, stub.URL); err != nil {
		t.Fatal(err)
	}

	// A webhook subscription, to prove the producer still emits exactly one event.
	events := make(chan string, 4)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		events <- r.Header.Get("X-Report-Portal-Event")
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()
	if _, err := s.st.CreateWebhook(recv.URL, []string{EventPredictionsStoreFailed}, ""); err != nil {
		t.Fatal(err)
	}

	if _, err := s.st.exec(`DROP TABLE prediction_claims`); err != nil {
		t.Fatal(err)
	}
	failing := strings.Replace(ingestBody(string(predWire(nil))), `"symbol":"600160"`, `"symbol":"600162"`, 1)
	start := time.Now()
	startNotificationRuntimeForTest(t, s)
	code, m := ingestPost(t, s, failing)
	elapsed := time.Since(start)
	if code != http.StatusOK || m["predictions_status"] != "store_failed" {
		t.Fatalf("ingest: %d %v", code, m)
	}
	if elapsed > time.Second {
		t.Errorf("ingest waited %v on the alert channels", elapsed)
	}
	select {
	case ev := <-events:
		if ev != EventPredictionsStoreFailed {
			t.Errorf("event = %q", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no store_failed webhook")
	}
	// The email is not blocked; the Slack POST is still held by the stub.
	deadline := time.Now().Add(3 * time.Second)
	for {
		mail.mu.Lock()
		n := mail.n
		mail.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no email for the store failure")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(mail.body, "store_failed") {
		t.Errorf("email body: %q", mail.body)
	}
	// A second event would page twice.
	select {
	case ev := <-events:
		t.Fatalf("a second webhook arrived: %s", ev)
	case <-time.After(300 * time.Millisecond):
	}
	// The Slack POST was started while the response was already sent; exactly one.
	deadline = time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&slackHits) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no Slack POST for the store failure")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&slackHits); got != 1 {
		t.Errorf("Slack POSTs = %d, want 1", got)
	}
	releaseNow()
	// And the delivery is recorded for the page.
	deadline = time.Now().Add(3 * time.Second)
	for {
		if rec := s.st.forecastAlertState(); rec.Channels["slack"].OK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the Slack outcome was not recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestStoreFailedAlertPanicDoesNotReachTheRequest: a channel sender that panics is confined to the
// alert goroutine; the ingest still answers store_failed.
func TestStoreFailedAlertPanicDoesNotReachTheRequest(t *testing.T) {
	s := newV1Server(t)
	s.mailFn = func([]string, string, string) error { panic("mail boom") }
	if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`DROP TABLE prediction_claims`); err != nil {
		t.Fatal(err)
	}
	failing := strings.Replace(ingestBody(string(predWire(nil))), `"symbol":"600160"`, `"symbol":"600163"`, 1)
	startNotificationRuntimeForTest(t, s)
	code, m := ingestPost(t, s, failing)
	if code != http.StatusOK || m["predictions_status"] != "store_failed" {
		t.Fatalf("ingest: %d %v", code, m)
	}
	// The goroutine's recover runs asynchronously; give it a moment so the race detector sees it.
	time.Sleep(100 * time.Millisecond)
}

// TestSlackTransportErrorDoesNotLeakTheURL: the URL is the secret, and net/http puts it in its
// error text. The recorded and displayed failure must not carry it.
func TestSlackTransportErrorDoesNotLeakTheURL(t *testing.T) {
	s := newV1Server(t)
	s.slackHostForTest = "127.0.0.1"
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	// An uppercase scheme is accepted and normalised by url.Parse, so the raw stored spelling is not
	// byte-identical to the URL in the error. The scrub must still remove it.
	raw := strings.Replace(stub.URL, "http://", "HTTP://", 1) + "/services/secret-path"
	stub.Close() // the port is now closed, so the POST fails at the transport layer
	if err := s.st.SetSetting(setForecastAlertSlackURL, raw); err != nil {
		t.Fatal(err)
	}
	res := s.forecastAlert("forecast.alert.test", "test", nil)
	if res["slack"] == nil {
		t.Fatal("a closed port did not fail the Slack POST")
	}
	if strings.Contains(res["slack"].Error(), "secret-path") {
		t.Errorf("the returned error leaks the webhook: %v", res["slack"])
	}
	rec := s.st.forecastAlertState()
	if strings.Contains(rec.Channels["slack"].Error, "secret-path") {
		t.Errorf("the recorded error leaks the webhook: %s", rec.Channels["slack"].Error)
	}
}

// TestAlertTestFailsWhenSlackFails is the other half of the contract: email succeeding alone must
// not write the stamp.
func TestAlertTestFailsWhenSlackFails(t *testing.T) {
	s := newV1Server(t)
	s.mailFn = func([]string, string, string) error { return nil }
	if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer stub.Close()
	s.slackHostForTest = "127.0.0.1"
	if err := s.st.SetSetting(setForecastAlertSlackURL, stub.URL); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.apiForecastAlertTest(rec, httptest.NewRequest("POST", "/api/admin/forecast/alert-test", nil), "admin")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting(setForecastAlertTestOKAt, ""); got != "" {
		t.Errorf("test_ok_at written while Slack failed: %q", got)
	}
}

// TestAlertTestNeedsOnlyTheConfiguredChannels: a deployment may alert by email alone or by Slack
// alone. The test reaches every configured channel, records nothing for the absent one, and the
// stamp it writes satisfies readiness item 5.
func TestAlertTestNeedsOnlyTheConfiguredChannels(t *testing.T) {
	s := newV1Server(t)
	s.mailFn = func([]string, string, string) error { return nil }
	if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.apiForecastAlertTest(rec, httptest.NewRequest("POST", "/api/admin/forecast/alert-test", nil), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("email only: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Results map[string]map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body.Results["email"]; !ok || len(body.Results) != 1 {
		t.Errorf("results = %v, want email only", body.Results)
	}
	if got := s.st.GetSetting(setForecastAlertTestOKAt, ""); got == "" {
		t.Error("test_ok_at not written after the only configured channel delivered")
	}
	if _, ok := s.st.forecastAlertState().Channels["slack"]; ok {
		t.Error("a delivery was recorded for Slack, which is not configured")
	}
	for _, it := range s.forecastReadiness(time.Now()) {
		if it.Key == "alerts" && !it.OK {
			t.Errorf("alerts item fails with one verified channel: %+v", it)
		}
	}
}

// TestAlertTestSlackOnly: the other single-channel setup, with no email recipients at all.
func TestAlertTestSlackOnly(t *testing.T) {
	s := newV1Server(t)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()
	s.slackHostForTest = "127.0.0.1"
	if err := s.st.SetSetting(setForecastAlertSlackURL, stub.URL); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.apiForecastAlertTest(rec, httptest.NewRequest("POST", "/api/admin/forecast/alert-test", nil), "admin")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"slack"`) || strings.Contains(rec.Body.String(), `"email"`) {
		t.Fatalf("Slack only: %d %s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting(setForecastAlertTestOKAt, ""); got == "" {
		t.Error("test_ok_at not written after the only configured channel delivered")
	}
}

// TestAlertTestRefusedWithoutAnyChannel: item 5 still needs somewhere to send an alert. With no
// channel configured the test is refused, nothing is stamped, and the item names the reason.
func TestAlertTestRefusedWithoutAnyChannel(t *testing.T) {
	s := newV1Server(t)
	rec := httptest.NewRecorder()
	s.apiForecastAlertTest(rec, httptest.NewRequest("POST", "/api/admin/forecast/alert-test", nil), "admin")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "forecast_alert_no_channel") {
		t.Fatalf("no channel: %d %s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting(setForecastAlertTestOKAt, ""); got != "" {
		t.Errorf("test_ok_at written with no channel: %q", got)
	}
	// A stamp alone, with nothing configured to deliver to, does not satisfy the item either.
	if err := s.st.SetSetting(setForecastAlertTestOKAt, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	for _, it := range s.forecastReadiness(time.Now()) {
		if it.Key == "alerts" && (it.OK || it.Reason != "alerts_no_channel") {
			t.Errorf("alerts item = %+v, want a failure with reason alerts_no_channel", it)
		}
	}
}

// TestAlertChangeInvalidatesTheVerification: a stamp only covers the configuration it was made
// with, and only for thirty days.
func TestAlertChangeInvalidatesTheVerification(t *testing.T) {
	s := newV1Server(t)
	now := time.Now().UTC()
	if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	item := func() readinessItem {
		for _, it := range s.forecastReadiness(now) {
			if it.Key == "alerts" {
				return it
			}
		}
		t.Fatal("no alerts item")
		return readinessItem{}
	}

	if err := s.st.SetSetting(setForecastAlertTestOKAt, now.Add(-24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if !item().OK {
		t.Fatal("a stamp from yesterday does not pass")
	}
	if err := s.st.SetSetting(setForecastAlertTestOKAt, now.Add(-31*24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if item().OK {
		t.Error("a 31-day-old stamp still passes")
	}
	if err := s.st.SetSetting(setForecastAlertTestOKAt, now.Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if item().OK {
		t.Error("a future stamp passes")
	}

	// Changing a channel deletes the stamp.
	if err := s.st.SetSetting(setForecastAlertTestOKAt, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	rec := saveForecastSettings(t, s, `{"alert_email_to":"new@example.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting(setForecastAlertTestOKAt, ""); got != "" {
		t.Errorf("stamp survived a channel change: %q", got)
	}
}

// TestStoreFailedAlertRunsOutsideTheInsertTransaction: the channel sender can use the connection
// pool (and meet a deadline) because it runs after the ledger transaction has returned.
func TestStoreFailedAlertRunsOutsideTheInsertTransaction(t *testing.T) {
	s := newV1Server(t)
	done := make(chan error, 1)
	s.mailFn = func([]string, string, string) error {
		deadline := time.Now().Add(2 * time.Second)
		for {
			var one int
			err := s.st.queryRow("SELECT 1").Scan(&one)
			if err == nil {
				done <- nil
				return nil
			}
			if time.Now().After(deadline) {
				done <- err
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`DROP TABLE prediction_claims`); err != nil {
		t.Fatal(err)
	}
	failing := strings.Replace(ingestBody(string(predWire(nil))), `"symbol":"600160"`, `"symbol":"600164"`, 1)
	startNotificationRuntimeForTest(t, s)
	if code, m := ingestPost(t, s, failing); code != http.StatusOK || m["predictions_status"] != "store_failed" {
		t.Fatalf("ingest: %d %v", code, m)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a pool call from the alert path could not run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the alert never ran")
	}
}

// TestSlackRedirectIsNotFollowed: following a redirect would leave the host list, and a 302 would
// turn the POST into a GET whose 2xx would look like a delivered message.
func TestSlackRedirectIsNotFollowed(t *testing.T) {
	s := newV1Server(t)
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer src.Close()
	s.slackHostForTest = "127.0.0.1"
	if accepted, _, _, _, _ := s.attemptNotification(context.Background(), NotificationChannel{Adapter: "slack"}, notificationChannelConfig{URL: src.URL}, NotificationEvent{Type: "forecast.alert.test", Payload: `{"reason":"test"}`}); accepted {
		t.Fatal("a redirect was treated as a successful delivery")
	}
	if n := atomic.LoadInt32(&targetHits); n != 0 {
		t.Errorf("the redirect was followed %d time(s)", n)
	}
}
