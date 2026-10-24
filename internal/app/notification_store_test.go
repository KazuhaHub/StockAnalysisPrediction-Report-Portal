package app

import (
	"bytes"
	"testing"
	"time"
)

func TestNotificationRestorePausesPreviouslyPendingWork(t *testing.T) {
	src := newTestStore(t)
	now := time.Now().UTC()
	event := NotificationEvent{ID: "restore-event", SourceKey: "restore-source", Type: EventBatchFinished, Payload: `{}`, CreatedAt: now.UnixMilli()}
	if err := src.CaptureNotification(event, []NotificationDelivery{{ID: "restore-delivery", ChannelID: "test"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := src.ClaimNotification(now); err != nil {
		t.Fatal(err)
	}
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dumpOf(t, src)), true); err != nil {
		t.Fatal(err)
	}
	if dst.GetSetting("notification_dispatch_paused", "0") != "1" {
		t.Fatal("restored dispatch is not paused")
	}
	var state string
	if err := dst.queryRow(`SELECT state FROM notification_deliveries WHERE id='restore-delivery'`).Scan(&state); err != nil || state != "uncertain" {
		t.Fatalf("state=%s error=%v", state, err)
	}
}

func TestNotificationSchemaIsOutsideBaselineAndBackedUp(t *testing.T) {
	st := newTestStore(t)
	for _, name := range notificationTables {
		if !st.tableExists(name) {
			t.Fatalf("missing %s", name)
		}
		found := false
		for _, table := range st.backupTables() {
			if table == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("backup omits %s", name)
		}
		for _, stmt := range st.baseSchemaStmts() {
			table, _, ok := parseCreateTable(stmt)
			if ok && table == name {
				t.Fatalf("%s moved baseline", name)
			}
		}
	}
}

func TestNotificationAccountDeletionDoesNotTransferInbox(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertUser(User{Username: "reused", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := NotificationEvent{ID: "private", SourceKey: "private-source", Type: EventBatchFinished, Payload: `{}`, CreatedAt: now.UnixMilli()}
	if err := st.CaptureNotification(e, []NotificationDelivery{{ChannelID: "private-channel", Username: "reused"}}, []string{"reused"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`INSERT INTO notification_preferences(username,event_type,enabled) VALUES('reused',?,0)`, EventBatchFinished); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser("reused"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUser(User{Username: "reused", PasswordHash: "new", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_recipients WHERE username='reused'`); n != 0 {
		t.Fatal("inbox inherited")
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_preferences WHERE username='reused'`); n != 0 {
		t.Fatal("preferences inherited")
	}
	if state := scalar[string](t, st, `SELECT state FROM notification_deliveries WHERE event_id='private'`); state != "canceled" {
		t.Fatalf("queued message state=%s", state)
	}
}

func TestNotificationCaptureAndLeaseFencing(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().UTC()
	event := NotificationEvent{ID: "event1", SourceKey: "source1", Type: EventBatchFinished, Payload: `{"job_id":42}`, CreatedAt: now.UnixMilli()}
	delivery := NotificationDelivery{ID: "delivery1", EventID: event.ID, ChannelID: "channel1", State: "pending", CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
	if err := st.CaptureNotification(event, []NotificationDelivery{delivery}, nil); err != nil {
		t.Fatal(err)
	}
	event.ID = "duplicate-event"
	if err := st.CaptureNotification(event, []NotificationDelivery{delivery}, nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.queryRow(`SELECT COUNT(*) FROM notification_events`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("capture count=%d err=%v", n, err)
	}
	claimed, err := st.ClaimNotification(now)
	if err != nil || claimed == nil {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	if again, err := st.ClaimNotification(now); err != nil || again != nil {
		t.Fatalf("second claim=%v err=%v", again, err)
	}
	if err := st.BeginNotificationAttempt(*claimed, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RecoverNotificationLeases(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.FinishNotification(*claimed, "accepted", "", 0, now); err != nil || ok {
		t.Fatalf("stale completion=%v err=%v", ok, err)
	}
	var state string
	if err := st.queryRow(`SELECT state FROM notification_deliveries WHERE id=?`, delivery.ID).Scan(&state); err != nil || state != "uncertain" {
		t.Fatalf("state=%s err=%v", state, err)
	}
}
