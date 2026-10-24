package app

import (
	"testing"
)

func TestNotificationConfigRevisionsAndRollback(t *testing.T) {
	testNotificationConfigRevisionsAndRollback(t, newTestStore(t))
}

func testNotificationConfigRevisionsAndRollback(t *testing.T, st *Store) {
	t.Helper()
	initial, err := st.notificationSettings()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.saveNotificationSettings(map[string]string{setForecastAlertSlackURL: "https://hooks.slack.com/services/" + randToken()}, false); err != nil {
		t.Fatal(err)
	}
	changed, err := st.notificationSettings()
	if err != nil {
		t.Fatal(err)
	}
	if initial.channelFingerprint("smtp") != changed.channelFingerprint("smtp") {
		t.Fatal("Slack configuration invalidated unrelated completion email")
	}
	if initial.channelFingerprint("forecast-slack") == changed.channelFingerprint("forecast-slack") {
		t.Fatal("Slack revision did not change")
	}
	if _, err := st.saveNotificationSettings(map[string]string{setForecastAlertSlackURL: initial.values[setForecastAlertSlackURL]}, false); err != nil {
		t.Fatal(err)
	}
	reverted, err := st.notificationSettings()
	if err != nil {
		t.Fatal(err)
	}
	if reverted.channelFingerprint("forecast-slack") == initial.channelFingerprint("forecast-slack") {
		t.Fatal("round-trip settings reused an old channel revision")
	}
	if err := st.stampForecastNotificationTest(initial); err != errNotificationConflict {
		t.Fatalf("old stamp result=%v", err)
	}
	if err := st.SetSetting(notificationConfigRevision, "damaged"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.SetSetting(notificationConfigRevision, "0") })
	if err := st.SetSetting(setForecastAlertTestOKAt, "existing-stamp"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.saveNotificationSettings(map[string]string{"smtp_host": "must-rollback.example"}, true); err == nil {
		t.Fatal("invalid revision did not abort settings transaction")
	}
	if got := st.GetSetting("smtp_host", ""); got != initial.values["smtp_host"] {
		t.Fatalf("partial transport setting persisted: %s", got)
	}
	if got := st.GetSetting(setForecastAlertTestOKAt, ""); got != "existing-stamp" {
		t.Fatal("failed transaction removed existing test evidence")
	}
}
