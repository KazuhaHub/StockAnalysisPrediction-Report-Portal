package app

import (
	"testing"
	"time"
)

func TestNotificationRetentionPreservesLiveAndUncertainEvidence(t *testing.T) {
	testNotificationRetentionPreservesLiveAndUncertainEvidence(t, newTestStore(t))
}

func testNotificationRetentionPreservesLiveAndUncertainEvidence(t *testing.T, st *Store) {
	t.Helper()
	now := time.Now().UTC()
	old := now.Add(-100 * 24 * time.Hour).UnixMilli()
	for _, state := range []string{"accepted", "uncertain", "leased", "pending", "retry_wait"} {
		e := NotificationEvent{ID: state, SourceKey: state, Type: EventBatchFinished, Payload: `{"job_id":42}`, CreatedAt: old}
		if err := st.CaptureNotification(e, []NotificationDelivery{{ID: state, ChannelID: state, ExpiresAt: now.Add(time.Hour).UnixMilli()}}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := st.exec(`UPDATE notification_deliveries SET state=?,updated_at=? WHERE id=?`, state, old, state); err != nil {
			t.Fatal(err)
		}
		if _, err := st.exec(`INSERT INTO notification_attempts(id,delivery_id,lease_token,started_at,finished_at,outcome) VALUES(?,?,?, ?,?,?)`, state, state, "lease", old, old, state); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CleanupNotifications(now); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries`); n != 5 {
		t.Fatal("cleanup ran before being enabled")
	}
	if err := st.SetSetting("notification_retention_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("notification_dispatch_paused", "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.CleanupNotifications(now); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries`); n != 5 {
		t.Fatal("paused restore review lost historical evidence")
	}
	if err := st.SetSetting("notification_dispatch_paused", "0"); err != nil {
		t.Fatal(err)
	}
	if err := st.CleanupNotifications(now); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries`); n != 4 {
		t.Fatalf("remaining deliveries=%d", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_attempts WHERE delivery_id='accepted'`); n != 0 {
		t.Fatal("closed attempts were not purged")
	}
	if body := scalar[string](t, st, `SELECT payload FROM notification_events WHERE id='accepted'`); body != "" {
		t.Fatal("unreferenced old payload was retained")
	}
	// Compaction must preserve idempotency for a retried original occurrence.
	e := NotificationEvent{ID: "another-id", SourceKey: "accepted", Type: EventBatchFinished, Payload: `{"job_id":42}`, CreatedAt: old}
	if err := st.CaptureNotification(e, nil, nil); err != nil {
		t.Fatalf("same occurrence after compaction=%v", err)
	}
	e.Payload = `{"job_id":99}`
	if err := st.CaptureNotification(e, nil, nil); err != errNotificationConflict {
		t.Fatalf("changed occurrence after compaction=%v", err)
	}
}

func TestNotificationRetentionKeepsReplayFamilyAndInboxBody(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertUser(User{Username: "reader", Role: "user", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-100 * 24 * time.Hour).UnixMilli()
	e := NotificationEvent{ID: "family", SourceKey: "family", Type: EventReportIngested, Payload: `{"id":42}`, CreatedAt: old}
	if err := st.CaptureNotification(e, []NotificationDelivery{{ID: "original", ChannelID: "raw"}}, []string{"reader"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`UPDATE notification_deliveries SET state='accepted',updated_at=?`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`INSERT INTO notification_deliveries(id,event_id,channel_id,state,expires_at,generation,replay_of,created_at,updated_at) VALUES('replay','family','raw','accepted',?,1,'original',?,?)`, now.Add(time.Hour).UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("notification_retention_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("notification_inbox_days", "365"); err != nil {
		t.Fatal(err)
	}
	if err := st.CleanupNotifications(now); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries`); n != 2 {
		t.Fatal("cleanup broke a retained replay link")
	}
	if _, err := st.exec(`UPDATE notification_deliveries SET created_at=?,updated_at=?`, old, old); err != nil {
		t.Fatal(err)
	}
	if err := st.CleanupNotifications(now); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries`); n != 0 {
		t.Fatal("old closed family not removed")
	}
	if body := scalar[string](t, st, `SELECT payload FROM notification_events`); body == "" {
		t.Fatal("cleanup compacted a visible inbox body")
	}
	if err := st.SetSetting("notification_inbox_days", "90"); err != nil {
		t.Fatal(err)
	}
	if err := st.CleanupNotifications(now); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_recipients`); n != 0 {
		t.Fatal("expired inbox item remains")
	}
	if body := scalar[string](t, st, `SELECT payload FROM notification_events`); body != "" {
		t.Fatal("unreferenced old body not compacted")
	}
}
