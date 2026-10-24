package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestNotificationReplayIsIdempotentAndDoesNotRewriteEvidence(t *testing.T) {
	testNotificationReplayIsIdempotentAndDoesNotRewriteEvidence(t, newTestStore(t))
}

func TestNotificationHistoryPaginationKeepsSameTimestampRows(t *testing.T) {
	s := batchServer(t)
	now := time.Now().UTC()
	for _, id := range []string{"a", "b", "c"} {
		e := NotificationEvent{ID: id, SourceKey: id, Type: EventReportIngested, Payload: `{"id":42}`, CreatedAt: now.UnixMilli()}
		if err := s.st.CaptureNotification(e, []NotificationDelivery{{ID: id, ChannelID: "mail"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	cursor := ""
	seen := map[string]bool{}
	for range 3 {
		r := httptest.NewRequest("GET", "/api/admin/notifications/deliveries?limit=1&cursor="+url.QueryEscape(cursor), nil)
		w := httptest.NewRecorder()
		s.apiNotificationHistory(w, r, "admin")
		if w.Code != 200 {
			t.Fatalf("history=%d %s", w.Code, w.Body.String())
		}
		var page struct {
			Deliveries []notificationDeliveryView `json:"deliveries"`
			Next       string                     `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Deliveries) != 1 || seen[page.Deliveries[0].ID] {
			t.Fatalf("missing or repeated page: %s", w.Body.String())
		}
		seen[page.Deliveries[0].ID] = true
		if _, err := time.Parse(time.RFC3339Nano, page.Deliveries[0].OccurredAt); err != nil {
			t.Fatalf("timestamp not UTC wire instant: %v", err)
		}
		cursor = page.Next
	}
	if len(seen) != 3 || cursor != "" {
		t.Fatalf("seen=%v cursor=%s", seen, cursor)
	}
}

func TestNotificationDispatchResumeRequiresCurrentReview(t *testing.T) {
	s := batchServer(t)
	pause := notificationAdminCall(t, s, s.apiNotificationDispatch, "POST", "", `{"paused":true}`)
	if pause.Code != 200 {
		t.Fatalf("pause=%d %s", pause.Code, pause.Body.String())
	}
	var first struct {
		ReviewToken string `json:"review_token"`
	}
	if err := json.Unmarshal(pause.Body.Bytes(), &first); err != nil || first.ReviewToken == "" {
		t.Fatalf("review=%s", pause.Body.String())
	}
	e := NotificationEvent{ID: "new-after-review", SourceKey: "new-after-review", Type: EventBatchFinished, Payload: `{}`, CreatedAt: time.Now().UnixMilli()}
	if err := s.st.CaptureNotification(e, []NotificationDelivery{{ChannelID: "missing"}}, nil); err != nil {
		t.Fatal(err)
	}
	stale := notificationAdminCall(t, s, s.apiNotificationDispatch, "POST", "", `{"paused":false,"review_token":"`+first.ReviewToken+`"}`)
	if stale.Code != http.StatusConflict || s.st.GetSetting("notification_dispatch_paused", "") != "1" {
		t.Fatalf("stale review resumed queue: %d %s", stale.Code, stale.Body.String())
	}
	view := notificationAdminCall(t, s, s.apiNotifications, "GET", "", "")
	var current struct {
		ReviewToken string `json:"review_token"`
	}
	if err := json.Unmarshal(view.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	resume := notificationAdminCall(t, s, s.apiNotificationDispatch, "POST", "", `{"paused":false,"review_token":"`+current.ReviewToken+`"}`)
	if resume.Code != 200 || s.st.GetSetting("notification_dispatch_paused", "") != "0" {
		t.Fatalf("resume=%d %s", resume.Code, resume.Body.String())
	}
}

func testNotificationReplayIsIdempotentAndDoesNotRewriteEvidence(t *testing.T, st *Store) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.InsertNotificationChannel(NotificationChannel{ID: "replay-channel", Adapter: "webhook", Config: `{}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e := NotificationEvent{ID: "replay-event", SourceKey: "replay-source", Type: EventReportIngested, Payload: `{"id":42}`, CreatedAt: now.UnixMilli()}
	if err := st.CaptureNotification(e, []NotificationDelivery{{ID: "original", ChannelID: "replay-channel", ChannelRevision: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	d, err := st.ClaimNotification(now)
	if err != nil || d == nil {
		t.Fatalf("claim=%v %v", d, err)
	}
	if err := st.BeginNotificationAttempt(*d, now); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.FinishNotification(*d, "uncertain", "acceptance_unknown", 0, now); err != nil || !ok {
		t.Fatalf("finish=%v %v", ok, err)
	}
	if _, _, err := st.ReplayNotification(d.ID, "request-1", 1, 1, 0, false, now); !errors.Is(err, errNotificationReview) {
		t.Fatalf("unconfirmed uncertain replay=%v", err)
	}
	replay, created, err := st.ReplayNotification(d.ID, "request-1", 1, 1, 0, true, now)
	if err != nil || !created || replay.ID == d.ID || replay.Generation != 1 {
		t.Fatalf("replay=%+v created=%v err=%v", replay, created, err)
	}
	again, created, err := st.ReplayNotification(d.ID, "request-1", 1, 1, 0, true, now)
	if err != nil || created || again.ID != replay.ID {
		t.Fatalf("idempotent replay=%+v %v %v", again, created, err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_events`); n != 1 {
		t.Fatal("replay created another business event")
	}
	if outcome := scalar[string](t, st, `SELECT outcome FROM notification_attempts WHERE delivery_id=?`, d.ID); outcome != "uncertain" {
		t.Fatal("replay rewrote original acceptance evidence")
	}
	if _, _, err := st.ReplayNotification(replay.ID, "request-2", 1, 1, 0, true, now); !errors.Is(err, errNotificationConflict) {
		t.Fatalf("active replay duplicated: %v", err)
	}
}

func TestNotificationReplayCannotRestoreDeletedAccount(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertUser(User{Username: "reused", Role: "admin", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertNotificationChannel(NotificationChannel{ID: "mail", Adapter: "email", Config: `{}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := NotificationEvent{ID: "old", SourceKey: "old", Type: EventBatchFinished, Payload: `{}`, CreatedAt: now.UnixMilli()}
	if err := st.CaptureNotification(e, []NotificationDelivery{{ID: "old-mail", ChannelID: "mail", Username: "reused"}}, nil); err != nil {
		t.Fatal(err)
	}
	d, err := st.ClaimNotification(now)
	if err != nil || d == nil {
		t.Fatalf("claim=%v %v", d, err)
	}
	if ok, err := st.FinishNotification(*d, "accepted", "", 0, now); err != nil || !ok {
		t.Fatalf("finish=%v %v", ok, err)
	}
	if err := st.DeleteUser("reused"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUser(User{Username: "reused", Role: "admin", PasswordHash: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReplayNotification(d.ID, "request-new-account", 1, 1, 0, true, now); !errors.Is(err, errNotificationConflict) {
		t.Fatalf("deleted identity replay=%v", err)
	}
}

func TestNotificationAccountDeletionSettlesInFlightAttempt(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertUser(User{Username: "gone", Role: "admin", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := NotificationEvent{ID: "in-flight", SourceKey: "in-flight", Type: EventBatchFinished, Payload: `{}`, CreatedAt: now.UnixMilli()}
	if err := st.CaptureNotification(e, []NotificationDelivery{{ID: "in-flight", ChannelID: "mail", Username: "gone"}}, nil); err != nil {
		t.Fatal(err)
	}
	d, err := st.ClaimNotification(now)
	if err != nil || d == nil {
		t.Fatalf("claim=%v %v", d, err)
	}
	if err := st.BeginNotificationAttempt(*d, now); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser("gone"); err != nil {
		t.Fatal(err)
	}
	if outcome := scalar[string](t, st, `SELECT outcome FROM notification_attempts`); outcome != "uncertain" {
		t.Fatalf("unfinished revoked attempt=%s", outcome)
	}
	if state := scalar[string](t, st, `SELECT state FROM notification_deliveries`); state != "uncertain" {
		t.Fatalf("in-flight delivery=%s", state)
	}
}
