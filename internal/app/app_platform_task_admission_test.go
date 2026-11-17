package app

import (
	"context"
	"testing"
	"time"
)

// Below the budget nothing changes, and every exit path gives the installation's slots back.
func TestPlatformServiceTaskUnderBudgetBehaviourIsUnchangedAndSlotsAreReleased(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	assertFree := func(step string) {
		t.Helper()
		var releases []func()
		for i := 0; i < platformServiceTaskSlots; i++ {
			release, ok := s.platformServiceTasks.acquire(installation)
			if !ok {
				t.Fatalf("%s: slot %d was not released", step, i)
			}
			releases = append(releases, release)
		}
		for _, release := range releases {
			release()
		}
	}
	body := `{"occurrenceId":"budget-task","handler":"app.backend.task","payload":{"value":1}}`
	if w := platformTaskHTTPFixture(t, s, installation, body); w.Code != 200 {
		t.Fatalf("admitted: %d %s", w.Code, w.Body.String())
	}
	assertFree("success")
	if w := platformTaskHTTPFixture(t, s, installation,
		`{"occurrenceId":"budget-task","handler":"app.backend.task","payload":{"value":2}}`); w.Code != 409 {
		t.Fatalf("conflict: %d %s", w.Code, w.Body.String())
	}
	assertFree("conflict")
	if w := platformTaskHTTPFixture(t, s, installation, `{"occurrenceId":""}`); w.Code != 400 {
		t.Fatalf("invalid body: %d %s", w.Code, w.Body.String())
	}
	assertFree("invalid body")
	revoked, _, err := s.issuePlatformServiceIdentity(context.Background(), installation,
		platformFixtureID(6301), []string{"tasks.schedule"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='tasks.schedule'`, installation)
	if w := platformTaskHTTPWithIdentity(t, s, installation, body, revoked); w.Code != 404 {
		t.Fatalf("revoked: %d %s", w.Code, w.Body.String())
	}
	assertFree("identity refused")
}
