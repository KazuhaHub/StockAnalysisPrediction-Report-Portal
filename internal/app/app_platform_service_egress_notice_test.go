package app

import (
	"encoding/json"
	"errors"
	"testing"
)

// The background notice frame and the background HTTP route share one capture
// function: same closed input, same namespace (the verified app), same
// idempotency and conflict rules (doc 174 section 3.2, doc 152).
func testPlatformServiceEgressNoticeIdempotentConflictNamespace(t *testing.T, pg bool) {
	s, installation := platformServiceIdentityFixtureWithDatabase(t, pg)
	resetPlatformServiceEgressNotices(t, s)
	app := scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, installation)
	notice := func(v map[string]any) []byte {
		raw, _ := json.Marshal(v)
		return raw
	}
	body := notice(map[string]any{"occurrenceId": "egress-1", "title": "Fixture notice", "message": "Through the session", "severity": "info"})
	id, created, err := s.publishPlatformServiceNotification(app, body)
	if err != nil || !created || id == "" {
		t.Fatal("first notice", id, created, err)
	}
	again, created, err := s.publishPlatformServiceNotification(app, body)
	if err != nil || created || again != id {
		t.Fatal("idempotent replay", again, created, err)
	}
	changed := notice(map[string]any{"occurrenceId": "egress-1", "title": "Changed", "message": "Different", "severity": "error"})
	if _, _, err := s.publishPlatformServiceNotification(app, changed); !errors.Is(err, errNotificationConflict) {
		t.Fatal("changed occurrence is not a conflict", err)
	}
	// The namespace is the app the session was verified for: the same
	// occurrence of another app is a different event and never a conflict.
	other, created, err := s.publishPlatformServiceNotification("other-app", body)
	if err != nil || !created || other == id {
		t.Fatal("namespace is not the app", other, created, err)
	}
	for name, raw := range map[string][]byte{
		"unknown field": notice(map[string]any{"occurrenceId": "egress-2", "title": "x", "message": "x", "severity": "info", "recipients": []string{"ops@example.com"}}),
		"missing field": notice(map[string]any{"occurrenceId": "egress-2", "title": "x", "severity": "info"}),
		"severity":      notice(map[string]any{"occurrenceId": "egress-2", "title": "x", "message": "x", "severity": "loud"}),
		"not json":      []byte("{"),
	} {
		if _, _, err := s.publishPlatformServiceNotification(app, raw); !errors.Is(err, errPlatformManagementInput) {
			t.Fatal(name, "was not refused as invalid input", err)
		}
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events`) != 2 {
		t.Fatal("a replay or a refused notice captured an event")
	}
}

func TestPlatformServiceEgressNoticeIdempotentConflictNamespaceSQLite(t *testing.T) {
	t.Parallel()
	testPlatformServiceEgressNoticeIdempotentConflictNamespace(t, false)
}
func TestPlatformServiceEgressNoticeIdempotentConflictNamespacePostgres(t *testing.T) {
	t.Parallel()
	testPlatformServiceEgressNoticeIdempotentConflictNamespace(t, true)
}

// resetPlatformServiceEgressNotices empties the event table around a test that counts events: the
// PostgreSQL database is shared between runs, and an occurrence a previous run captured would
// otherwise read as a replay.
func resetPlatformServiceEgressNotices(t *testing.T, s *Server) {
	t.Helper()
	clean := func() {
		for _, table := range []string{"notification_attempts", "notification_deliveries", "notification_events"} {
			if _, err := s.st.exec("DELETE FROM " + table); err != nil {
				t.Fatalf("reset %s: %v", table, err)
			}
		}
	}
	clean()
	t.Cleanup(clean)
}
