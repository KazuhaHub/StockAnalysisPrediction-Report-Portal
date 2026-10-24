package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotificationWorkerAcceptedAndPaused(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "paused"}[paused], func(t *testing.T) {
			st := newTestStore(t)
			s := &Server{st: st}
			enableEmail(s)
			var calls atomic.Int32
			s.mailFn = func([]string, string, string) error { calls.Add(1); return nil }
			c := NotificationChannel{ID: "mail", Label: "Operations", Adapter: "email", LegacyRef: "smtp", Config: `{"recipients":["ops@example.com"]}`, Enabled: true, Paused: paused}
			if err := st.InsertNotificationChannel(c); err != nil {
				t.Fatal(err)
			}
			c.Revision = 1
			_, revision, err := s.effectiveNotificationChannel(c)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			e := NotificationEvent{ID: "event", SourceKey: "source", Type: EventBatchFinished, Payload: `{"job_id":42,"status":"finished"}`, CreatedAt: now.UnixMilli()}
			if err := st.CaptureNotification(e, []NotificationDelivery{{ChannelID: c.ID, ChannelRevision: revision}}, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.processNotificationOnce(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			state := scalar[string](t, st, `SELECT state FROM notification_deliveries`)
			if paused {
				if calls.Load() != 0 || state != "retry_wait" {
					t.Fatalf("calls=%d state=%s", calls.Load(), state)
				}
			} else {
				if calls.Load() != 1 || state != "accepted" {
					t.Fatalf("calls=%d state=%s", calls.Load(), state)
				}
			}
			if err := s.processNotificationOnce(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			if calls.Load() > 1 {
				t.Fatal("accepted mail was resent")
			}
		})
	}
}

func TestNotificationWorkerCancelsReplacedDestination(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	enableEmail(s)
	var calls atomic.Int32
	s.mailFn = func([]string, string, string) error { calls.Add(1); return nil }
	c := NotificationChannel{ID: "mail", Label: "Operations", Adapter: "email", Config: `{"recipients":["old@example.com"]}`, Enabled: true, Revision: 1}
	if err := st.InsertNotificationChannel(c); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := NotificationEvent{ID: "event", SourceKey: "source", Type: EventBatchFinished, Payload: `{}`, CreatedAt: now.UnixMilli()}
	if err := st.CaptureNotification(e, []NotificationDelivery{{ChannelID: c.ID, ChannelRevision: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	c.Config = `{"recipients":["new@example.com"]}`
	if err := st.UpdateNotificationChannel(c, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.processNotificationOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("old message was sent to replacement destination")
	}
	if reason := scalar[string](t, st, `SELECT last_error FROM notification_deliveries`); reason != "config_changed" {
		t.Fatalf("reason=%s", reason)
	}
}

func TestNotificationWorkerRechecksAudienceAndPreferences(t *testing.T) {
	for _, tc := range []struct {
		name              string
		mandatory, revoke bool
		want              string
	}{
		{"optional-muted", false, false, "recipient_muted"},
		{"mandatory-muted", true, false, ""},
		{"mandatory-revoked", true, true, "audience_revoked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			s := &Server{st: st}
			enableEmail(s)
			if err := st.UpsertUser(User{Username: "operator", PasswordHash: "h", Role: "admin"}); err != nil {
				t.Fatal(err)
			}
			if err := st.SetUserProfile("operator", "Operator", "operator@example.com"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.exec(`INSERT INTO notification_preferences(username,event_type,enabled) VALUES(?,?,0)`, "operator", EventForecastEvaluatorUnhealthy); err != nil {
				t.Fatal(err)
			}
			c := NotificationChannel{ID: "mail", Adapter: "email", Config: `{}`, Enabled: true, Revision: 1}
			if err := st.InsertNotificationChannel(c); err != nil {
				t.Fatal(err)
			}
			r := NotificationRoute{ID: "ops", ChannelID: c.ID, Events: EventForecastEvaluatorUnhealthy, RecipientMode: "grant", Enabled: true, Mandatory: tc.mandatory, Grants: []string{userPrincipal("operator")}}
			if err := st.SaveNotificationRoute(r, 0); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			e := NotificationEvent{ID: "ops-event", SourceKey: "ops-source", Type: EventForecastEvaluatorUnhealthy, ResourceType: "operations", Payload: `{}`, CreatedAt: now.UnixMilli()}
			if err := st.CaptureNotification(e, []NotificationDelivery{{ChannelID: c.ID, ChannelRevision: 1, RouteID: r.ID, RouteRevision: 1, Username: "operator"}}, nil); err != nil {
				t.Fatal(err)
			}
			if tc.revoke {
				if _, err := st.exec(`DELETE FROM notification_route_grants WHERE route_id=?`, r.ID); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			s.mailFn = func([]string, string, string) error { calls.Add(1); return nil }
			if err := s.processNotificationOnce(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			if reason := scalar[string](t, st, `SELECT last_error FROM notification_deliveries`); reason != tc.want {
				t.Fatalf("reason=%s want=%s", reason, tc.want)
			}
			wantCalls := int32(0)
			if tc.want == "" {
				wantCalls = 1
			}
			if calls.Load() != wantCalls {
				t.Fatalf("calls=%d want=%d", calls.Load(), wantCalls)
			}
		})
	}
}
