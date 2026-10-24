package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestNotificationJobIntentSurvivesServerRestart(t *testing.T) {
	testNotificationJobIntentSurvivesServerRestart(t, newTestStore(t))
}

func testNotificationJobIntentSurvivesServerRestart(t *testing.T, st *Store) {
	t.Helper()
	if err := st.SetSetting("notification_dispatch_paused", "0"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUser(User{Username: "submitter", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserProfile("submitter", "Submitter", "submitter@example.com"); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateBatchJob(seedTarget(t, st), 1, 0, "submitter", []map[string]string{{"code": "a"}}, "normal", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishJob(id, false); err != nil {
		t.Fatal(err)
	}
	// A newly constructed server has no in-memory completion consent.
	s := &Server{st: st}
	sent := enableEmail(s)
	if err := s.recoverJobNotifications(); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverJobNotifications(); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int](t, st, `SELECT COUNT(*) FROM notification_events WHERE event_type=?`, EventBatchFinished); got != 1 {
		t.Fatalf("events=%d", got)
	}
	if got := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries`); got != 1 {
		t.Fatalf("deliveries=%d", got)
	}
	if err := s.processNotificationOnce(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := wantEmail(t, sent); got == "" {
		t.Fatal("missing completion email")
	}
	if err := s.recoverJobNotifications(); err != nil {
		t.Fatal(err)
	}
	if err := s.processNotificationOnce(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	wantNoEmail(t, sent)
}

func TestNotificationJobRecoveryDoesNotRenewExpiry(t *testing.T) {
	st := newTestStore(t)
	id, err := st.CreateBatchJob(seedTarget(t, st), 1, 0, "submitter", []map[string]string{{"code": "a"}}, "normal", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishJob(id, false); err != nil {
		t.Fatal(err)
	}
	var key, body string
	if err := st.queryRow(`SELECT source_key,signature FROM notification_source_cycles WHERE source_key LIKE 'job-completion:%' AND signature<>''`).Scan(&key, &body); err != nil {
		t.Fatal(err)
	}
	var snapshot jobNotificationSnapshot
	if err := json.Unmarshal([]byte(body), &snapshot); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-48 * time.Hour).UnixMilli()
	snapshot.OccurredAt = before
	bodyBytes, _ := json.Marshal(snapshot)
	if _, err := st.exec(`UPDATE notification_source_cycles SET signature=? WHERE source_key=?`, string(bodyBytes), key); err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st}
	if err := s.recoverJobNotifications(); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int64](t, st, `SELECT created_at FROM notification_events`); got != before {
		t.Fatalf("recovery changed occurrence time: %d != %d", got, before)
	}
}

func TestNotificationOldJobCannotNotifyReplacementAccountThroughCreatorRule(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	createUser := func() {
		if err := st.UpsertUser(User{Username: "reused", Role: "user", PasswordHash: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	createUser()
	if err := st.InsertNotificationChannel(NotificationChannel{ID: "creator-mail", Adapter: "email", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveNotificationRoute(NotificationRoute{ID: "creator-rule", ChannelID: "creator-mail", Events: EventBatchFinished, RecipientMode: "creator", Enabled: true}, 0); err != nil {
		t.Fatal(err)
	}
	target := seedTarget(t, st)
	createJob := func() int64 {
		id, err := st.CreateBatchJob(target, 1, 0, "reused", []map[string]string{{"code": "a"}}, "normal")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := createJob()
	if err := st.FinishJob(old, false); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser("reused"); err != nil {
		t.Fatal(err)
	}
	createUser()
	if _, err := st.RequeueItems(old, "queued"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishJob(old, false); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverJobNotifications(); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries WHERE username='reused'`); n != 0 {
		t.Fatalf("old jobs notified replacement account: %d", n)
	}
	fresh := createJob()
	if err := st.FinishJob(fresh, false); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverJobNotifications(); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries WHERE username='reused'`); n != 1 {
		t.Fatalf("fresh job did not notify its actual creator: %d", n)
	}
}

func TestNotificationJobRetryHasNewOccurrenceWithoutNewConsent(t *testing.T) {
	testNotificationJobRetryHasNewOccurrenceWithoutNewConsent(t, newTestStore(t))
}

func testNotificationJobRetryHasNewOccurrenceWithoutNewConsent(t *testing.T, st *Store) {
	t.Helper()
	if err := st.UpsertUser(User{Username: "admin", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateBatchJob(seedTarget(t, st), 1, 0, "admin", []map[string]string{{"code": "a"}}, "normal", true)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st}
	if err := st.FinishJob(id, false); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverJobNotifications(); err != nil {
		t.Fatal(err)
	}
	first := scalar[string](t, st, `SELECT source_key FROM notification_events`)
	if _, err := st.RequeueItems(id, "queued"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishJob(id, false); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverJobNotifications(); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int](t, st, `SELECT COUNT(*) FROM notification_events`); got != 2 {
		t.Fatalf("events=%d", got)
	}
	if got := scalar[int](t, st, `SELECT COUNT(*) FROM notification_events WHERE source_key=?`, first); got != 1 {
		t.Fatalf("first occurrence count=%d", got)
	}
	if got := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries WHERE username='admin'`); got != 1 {
		t.Fatalf("one-shot deliveries=%d", got)
	}
}

func TestNotificationDeletedJobCannotConsumeReusedIDConsent(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertUser(User{Username: "submitter", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	target := seedTarget(t, st)
	create := func() int64 {
		id, err := st.CreateBatchJob(target, 1, 0, "submitter", []map[string]string{{"code": "a"}}, "normal", true)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := create()
	if err := st.FinishJob(first, false); err != nil {
		t.Fatal(err)
	}
	key := scalar[string](t, st, `SELECT source_key FROM notification_source_cycles WHERE source_key LIKE 'job-completion:%' AND signature<>''`)
	if err := st.DeleteBatchJob(first); err != nil {
		t.Fatal(err)
	}
	// Normal AUTOINCREMENT allocation does not reuse IDs. Explicitly rewind the
	// disposable fixture to exercise replacement identity after database repair.
	if _, err := st.exec(`UPDATE sqlite_sequence SET seq=? WHERE name='batch_jobs'`, first-1); err != nil {
		t.Fatal(err)
	}
	second := create()
	if second != first {
		t.Fatalf("fixture did not reuse the deleted SQLite id: %d != %d", second, first)
	}
	s := &Server{st: st}
	if err := s.recoverJobNotifications(); err != nil {
		t.Fatal(err)
	}
	// Simulate a callback that read the old snapshot before deletion and now
	// reaches capture after the replacement submission has committed.
	e := NotificationEvent{ID: "old-callback", SourceKey: key, Type: EventBatchFinished, Payload: `{}`, CreatedAt: time.Now().UnixMilli()}
	if err := st.captureNotification(e, nil, nil, first, key); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int64](t, st, `SELECT consumed_at FROM job_notification_intents WHERE job_id=?`, second); got != 0 {
		t.Fatal("old callback consumed replacement consent")
	}
	if got := scalar[int](t, st, `SELECT COUNT(*) FROM notification_events`); got != 0 {
		t.Fatalf("old events=%d", got)
	}
	if err := st.CancelBatchJob(second); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverJobNotifications(); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int](t, st, `SELECT COUNT(*) FROM notification_events`); got != 1 {
		t.Fatalf("new cancellation events=%d", got)
	}
}
