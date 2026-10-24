package app

import (
	"context"
	"testing"
	"time"
)

func startNotificationRuntimeForTest(t *testing.T, s *Server) {
	t.Helper()
	stop := s.startNotificationRuntime()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		stop(ctx)
	})
}

func TestNotificationRuntimeDeliversAndStops(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	enableEmail(s)
	called := make(chan struct{}, 1)
	s.mailFn = func([]string, string, string) error { called <- struct{}{}; return nil }
	c := NotificationChannel{ID: "runtime", Label: "Runtime", Adapter: "email", Config: `{"recipients":["ops@example.com"]}`, Enabled: true, Revision: 1}
	if err := st.InsertNotificationChannel(c); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := NotificationEvent{ID: "runtime", SourceKey: "runtime", Type: EventBatchFinished, Payload: `{}`, CreatedAt: now.UnixMilli()}
	if err := st.CaptureNotification(e, []NotificationDelivery{{ChannelID: c.ID, ChannelRevision: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	stop := s.startNotificationRuntime()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		stop(ctx)
	})
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("persisted notification was not delivered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stop(ctx)
	if got := scalar[string](t, st, `SELECT state FROM notification_deliveries`); got != "accepted" {
		t.Fatalf("state=%s", got)
	}
}

func TestNotificationRuntimeHonorsRestorePause(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	st.SetSetting("notification_dispatch_paused", "1")
	now := time.Now().UTC()
	e := NotificationEvent{ID: "paused", SourceKey: "paused", Type: EventBatchFinished, Payload: `{}`, CreatedAt: now.UnixMilli()}
	if err := st.CaptureNotification(e, []NotificationDelivery{{ChannelID: "missing"}}, nil); err != nil {
		t.Fatal(err)
	}
	stop := s.startNotificationRuntime()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stop(ctx)
	if got := scalar[string](t, st, `SELECT state FROM notification_deliveries`); got != "pending" {
		t.Fatalf("state=%s", got)
	}
}
