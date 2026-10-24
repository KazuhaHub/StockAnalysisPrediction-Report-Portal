package app

import (
	"fmt"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresNotificationPublisher(t *testing.T) {
	st := pgStore(t, append(append([]string{}, notificationTables...), "api_tokens", "apps", "app_files")...)
	testNotificationPublisherIsNamespacedAndIdempotent(t, &Server{st: st, cfg: &config.Config{SecretKey: "publisher-test-key"}})
}

func TestPostgresNotificationRetention(t *testing.T) {
	st := pgStore(t, notificationTables...)
	if err := st.SetSetting("notification_dispatch_paused", "0"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("notification_retention_enabled", "0"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("notification_history_days", "30"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("notification_inbox_days", "90"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.SetSetting("notification_retention_enabled", "0") })
	testNotificationRetentionPreservesLiveAndUncertainEvidence(t, st)
}

func TestPostgresNotificationInbox(t *testing.T) {
	st := pgStore(t, append(append([]string{}, notificationTables...), "users")...)
	testNotificationInboxChecksAuthorityAndSnapshot(t, &Server{st: st, cfg: &config.Config{SecretKey: "inbox-test-key"}})
}

func TestPostgresNotificationConfig(t *testing.T) {
	testNotificationConfigRevisionsAndRollback(t, pgStore(t, notificationTables...))
}

func TestPostgresNotificationReplay(t *testing.T) {
	testNotificationReplayIsIdempotentAndDoesNotRewriteEvidence(t, pgStore(t, notificationTables...))
}

func TestPostgresNotificationManualAttempt(t *testing.T) {
	st := pgStore(t, notificationTables...)
	c := NotificationChannel{ID: "manual-channel", Adapter: "webhook", Enabled: true, Revision: 1}
	if err := st.InsertNotificationChannel(c); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := NotificationEvent{ID: "manual-event", SourceKey: "manual-source", Type: "notification.test", ResourceType: "operations", Payload: `{}`, CreatedAt: now.UnixMilli()}
	d, err := st.beginNotificationTest(e, c, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	e.ID, e.SourceKey = "another-manual", "another-manual"
	if _, err := st.beginNotificationTest(e, c, 1, now); err != errNotificationCapacity {
		t.Fatalf("parallel manual attempt=%v", err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_events`); n != 1 {
		t.Fatal("rejected test left a partial event")
	}
	if ok, err := st.FinishNotification(d, "accepted", "", 200, now); err != nil || !ok {
		t.Fatalf("finish=%v %v", ok, err)
	}
	if got := scalar[string](t, st, `SELECT outcome FROM notification_attempts`); got != "accepted" {
		t.Fatalf("attempt=%s", got)
	}
}

func TestPostgresNotificationManualAndWorkerShareChannelLimit(t *testing.T) {
	st := pgStore(t, notificationTables...)
	c := NotificationChannel{ID: "shared-channel", Adapter: "webhook", Enabled: true, Revision: 1}
	if err := st.InsertNotificationChannel(c); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		for _, table := range []string{"notification_attempts", "notification_deliveries", "notification_events"} {
			if _, err := st.exec("DELETE FROM " + table); err != nil {
				t.Fatal(err)
			}
		}
		now := time.Now().UTC()
		e := NotificationEvent{ID: fmt.Sprintf("normal-%d", i), SourceKey: fmt.Sprintf("normal-%d", i), Type: EventReportIngested, Payload: `{}`, CreatedAt: now.UnixMilli()}
		if err := st.CaptureNotification(e, []NotificationDelivery{{ChannelID: c.ID}}, nil); err != nil {
			t.Fatal(err)
		}
		e.ID, e.SourceKey, e.Type = fmt.Sprintf("manual-%d", i), fmt.Sprintf("manual-%d", i), "notification.test"
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		wg.Go(func() { <-start; _, err := st.ClaimNotification(now); errs <- err })
		wg.Go(func() {
			<-start
			_, err := st.beginNotificationTest(e, c, 1, now)
			if err == errNotificationCapacity {
				err = nil
			}
			errs <- err
		})
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries WHERE state='leased'`); n != 1 {
			t.Fatalf("iteration %d leased=%d", i, n)
		}
	}
}

func TestPostgresNotificationCandidateDeletionFence(t *testing.T) {
	testNotificationRoutingSnapshotCannotFollowReusedUsername(t, pgStore(t, append(append([]string{}, notificationTables...), "users")...))
}

func TestPostgresNotificationJobRecovery(t *testing.T) {
	testNotificationJobIntentSurvivesServerRestart(t, pgStore(t, append(append([]string{}, notificationTables...), "batch_jobs", "batch_items", "users")...))
}

func TestPostgresNotificationJobRetry(t *testing.T) {
	testNotificationJobRetryHasNewOccurrenceWithoutNewConsent(t, pgStore(t, append(append([]string{}, notificationTables...), "batch_jobs", "batch_items", "users")...))
}

func TestPostgresNotificationCaptureAndClaims(t *testing.T) {
	st := pgStore(t, notificationTables...)
	now := time.Now().UTC()
	e := NotificationEvent{ID: "pg-event", SourceKey: "pg-source", Type: EventBatchFinished, Payload: `{}`, CreatedAt: now.UnixMilli()}
	d := NotificationDelivery{ID: "pg-delivery", ChannelID: "pg-channel"}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- st.CaptureNotification(e, []NotificationDelivery{d}, nil) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_events`); n != 1 {
		t.Fatalf("events=%d", n)
	}
	claims := make(chan *NotificationDelivery, 8)
	claimErrors := make(chan error, 8)
	for range 8 {
		wg.Go(func() { d, err := st.ClaimNotification(now); claims <- d; claimErrors <- err })
	}
	wg.Wait()
	close(claims)
	close(claimErrors)
	for err := range claimErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var winner *NotificationDelivery
	for d := range claims {
		if d != nil {
			if winner != nil {
				t.Fatal("two claims for one delivery")
			}
			winner = d
		}
	}
	if winner == nil {
		t.Fatal("no claim")
	}
	if err := st.BeginNotificationAttempt(*winner, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RecoverNotificationLeases(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.FinishNotification(*winner, "accepted", "", 200, now); err != nil || ok {
		t.Fatalf("stale completion=%v err=%v", ok, err)
	}
	if state := scalar[string](t, st, `SELECT state FROM notification_deliveries`); state != "uncertain" {
		t.Fatalf("state=%s", state)
	}
}

func TestPostgresNotificationChannelConcurrency(t *testing.T) {
	st := pgStore(t, notificationTables...)
	now := time.Now().UTC()
	for i := range 8 {
		id := fmt.Sprintf("event-%d", i)
		e := NotificationEvent{ID: id, SourceKey: id, Type: EventBatchFinished, Payload: `{}`, CreatedAt: now.UnixMilli()}
		if err := st.CaptureNotification(e, []NotificationDelivery{{ChannelID: "one-destination"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	var claimed atomic.Int32
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			d, err := st.ClaimNotification(now)
			errs <- err
			if d != nil {
				claimed.Add(1)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := claimed.Load(); got != 1 {
		t.Fatalf("concurrent claims for one channel=%d", got)
	}
}
