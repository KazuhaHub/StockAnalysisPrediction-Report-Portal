package app

import (
	"context"
	"testing"
	"time"
)

func TestNotificationRoutingIntersectsOUWithResourceAuthority(t *testing.T) {
	s := batchServer(t)
	st := s.st
	ou, err := st.CreateUserGroup("Operations", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []User{{Username: "operator", PasswordHash: "h", Role: "admin"}, {Username: "reader", PasswordHash: "h", Role: "user"}} {
		if err := st.UpsertUser(u); err != nil {
			t.Fatal(err)
		}
		if err := st.SetPrimaryGroup(u.Username, ou); err != nil {
			t.Fatal(err)
		}
		if err := st.SetUserProfile(u.Username, u.Username, u.Username+"@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.InsertNotificationChannel(NotificationChannel{ID: "inbox", Adapter: "inbox", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertNotificationChannel(NotificationChannel{ID: "mail", Adapter: "email", Config: `{}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"inbox", "mail"} {
		if err := st.SaveNotificationRoute(NotificationRoute{ID: channel, ChannelID: channel, Events: EventForecastEvaluatorUnhealthy, RecipientMode: "grant", Enabled: true, Grants: []string{groupPrincipal(ou)}}, 0); err != nil {
			t.Fatal(err)
		}
	}
	s.fireEvent(EventForecastEvaluatorUnhealthy, map[string]string{"reason": "stalled"})
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_recipients WHERE username='operator'`); n != 1 {
		t.Fatalf("operator inbox=%d", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_recipients WHERE username='reader'`); n != 0 {
		t.Fatal("OU routing granted operations authority to a reader")
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries WHERE username='operator'`); n != 1 {
		t.Fatalf("operator deliveries=%d", n)
	}
	if err := st.SetPrimaryGroup("operator", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.processNotificationOnce(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if reason := scalar[string](t, st, `SELECT last_error FROM notification_deliveries WHERE username='operator'`); reason != "audience_revoked" {
		t.Fatalf("reason=%s", reason)
	}
}

func TestNotificationRoutingSnapshotCannotFollowReusedUsername(t *testing.T) {
	testNotificationRoutingSnapshotCannotFollowReusedUsername(t, newTestStore(t))
}

func testNotificationRoutingSnapshotCannotFollowReusedUsername(t *testing.T, st *Store) {
	t.Helper()
	s := &Server{st: st}
	create := func() {
		if err := st.UpsertUser(User{Username: "reused", PasswordHash: "h", Role: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	create()
	if err := st.InsertNotificationChannel(NotificationChannel{ID: "inbox", Adapter: "inbox", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveNotificationRoute(NotificationRoute{ID: "inbox", ChannelID: "inbox", Events: EventForecastEvaluatorUnhealthy, RecipientMode: "grant", Enabled: true, Grants: []string{userPrincipal("reused")}}, 0); err != nil {
		t.Fatal(err)
	}
	e := NotificationEvent{ID: "old-account-event", SourceKey: "old-account-source", Type: EventForecastEvaluatorUnhealthy, ResourceType: "operations", Payload: `{}`, CreatedAt: time.Now().UnixMilli()}
	ds, recipients, err := s.routeNotification(&e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 {
		t.Fatal("fixture has no original candidate")
	}
	if err := st.DeleteUser("reused"); err != nil {
		t.Fatal(err)
	}
	create()
	if err := st.CaptureNotification(e, ds, recipients); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM notification_recipients`); n != 0 {
		t.Fatal("replacement account inherited a candidate snapshot from the old account")
	}
}
