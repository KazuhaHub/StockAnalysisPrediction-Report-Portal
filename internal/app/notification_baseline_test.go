package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/webhook"
)

// Recovery must answer without waiting for SMTP, even when the address exists.
// Comparing bodies also protects the response from becoming an account oracle.
func TestNotificationBaselineRecoveryDoesNotWaitForMail(t *testing.T) {
	s := batchServer(t)
	if err := s.st.UpsertUser(User{Username: "recipient", PasswordHash: "hash", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetUserProfile("recipient", "Recipient", "recipient@example.com"); err != nil {
		t.Fatal(err)
	}
	enableEmail(s)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	s.mailFn = func([]string, string, string) error {
		close(started)
		<-release
		close(finished)
		return errors.New("temporary SMTP failure")
	}
	t.Cleanup(func() {
		close(release)
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("mail sender did not finish")
		}
	})
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		s.apiForgotPassword(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{"account":"recipient"}`)))
		response <- rec
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("mail was not attempted")
	}
	var known *httptest.ResponseRecorder
	select {
	case known = <-response:
	case <-time.After(2 * time.Second):
		t.Fatal("password recovery waited for the blocked sender")
	}
	unknown := postJSON(t, s.apiForgotPassword, `{"account":"missing"}`)
	if known.Code != http.StatusOK || known.Code != unknown.Code || known.Body.String() != unknown.Body.String() {
		t.Fatalf("recovery responses differ: known=%d %s unknown=%d %s", known.Code, known.Body, unknown.Code, unknown.Body)
	}
}

func TestNotificationBaselineCompletionIntentIsSingleUse(t *testing.T) {
	s := batchServer(t)
	if err := s.st.UpsertUser(User{Username: "recipient", PasswordHash: "hash", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetUserProfile("recipient", "Recipient", "recipient@example.com"); err != nil {
		t.Fatal(err)
	}
	enableEmail(s)
	var attempts atomic.Int32
	s.mailFn = func([]string, string, string) error {
		attempts.Add(1)
		return errors.New("temporary SMTP failure")
	}
	id, err := s.st.CreateBatchJob(seedTarget(t, s.st), 1, 0, "recipient", []map[string]string{{"code": "a"}}, "normal", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.FinishJob(id, false); err != nil {
		t.Fatal(err)
	}
	job, _ := s.st.GetBatchJob(id)
	var callers sync.WaitGroup
	for range 16 {
		callers.Go(func() { s.notifyJobDone(job) })
	}
	callers.Wait()
	// A later completion under the same job ID must not recreate consumed consent.
	s.notifyJobDone(job)
	if err := s.processNotificationOnce(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("mail attempts = %d, want one despite concurrent callbacks and failure", got)
	}
}

// A shared event envelope must never be inserted into the legacy receiver body.
func TestNotificationBaselineRawWebhookWireContract(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	const body = `{"id":9007199254740993,"title":"A < B & C","created":false}`
	type request struct {
		body, signature, event, contentType string
	}
	requests := make(chan request, 4)
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		received, _ := io.ReadAll(r.Body)
		requests <- request{string(received), r.Header.Get(webhook.SignatureHeader), r.Header.Get(webhook.EventHeader), r.Header.Get("Content-Type")}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer receiver.Close()
	id, err := st.CreateWebhook(receiver.URL, []string{EventReportIngested}, "signing-secret")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.ensureLegacyNotificationChannel("webhook:"+itoa64(id), "webhook", "Wire contract")
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := s.effectiveNotificationChannel(c)
	if err != nil {
		t.Fatal(err)
	}
	e := NotificationEvent{ID: "wire-event", SourceKey: "wire-source", Type: EventReportIngested, Payload: body, CreatedAt: time.Now().UTC().UnixMilli()}
	if err := st.CaptureNotification(e, []NotificationDelivery{{ChannelID: c.ID, ChannelRevision: revision}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.processNotificationOnce(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var got request
	select {
	case got = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("receiver did not receive the request")
	}
	if calls.Load() != 1 || got.body != body {
		t.Fatalf("calls=%d body=%s, want one unmodified payload", calls.Load(), got.body)
	}
	if got.event != EventReportIngested || got.contentType != "application/json" || got.signature != "sha256="+webhook.Sign("signing-secret", []byte(body)) {
		t.Fatalf("wire headers differ: %+v", got)
	}
	stored, ok := st.GetWebhook(id)
	if !ok || stored.LastStatus != http.StatusBadRequest || stored.LastDelivered == "" {
		t.Fatalf("failed attempt was not recorded: %+v", stored)
	}
}
