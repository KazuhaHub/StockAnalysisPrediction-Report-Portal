package app

import (
	"context"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A task submission takes the task admission lock and then the installation's row; an event capture that
// fans a delivery out to the same installation must take them in the same order, or the two deadlock when
// a backend submits while an event is captured (the soak saw "deadlock detected" on a capture).
func TestPlatformEventCaptureTakesTheTaskAdmissionLockBeforeAnyInstallationRow(t *testing.T) {
	t.Parallel()
	s, installation := platformServiceIdentityFixtureWithDatabase(t, true)
	if _, created, err := s.createPlatformEventSubscription(context.Background(), installation, appplatform.EventSubscriptionSpec{
		EventType: appplatform.EventReportIngested, ResourceType: "report", ResourceID: "7401", MaxInflight: 4,
	}); err != nil || !created {
		t.Fatalf("subscription: %t %v", created, err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	// The submission: it holds the admission lock first.
	tx, err := s.st.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	submission := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: context.Background()}}
	if err := lockPlatformTaskAdmission(submission); err != nil {
		t.Fatal(err)
	}
	captured := make(chan error, 1)
	go func() {
		captured <- s.st.CaptureNotification(NotificationEvent{
			ID: "10000000-0000-4000-8000-000000740100", SourceKey: "capture-lock:1", Type: appplatform.EventReportIngested,
			Payload: `{"id":1}`, ResourceType: "report", ResourceID: "7401", CreatedAt: now,
		}, nil, nil)
	}()
	// Let the capture reach whatever it takes first, then take the installation row as a submission does.
	time.Sleep(500 * time.Millisecond)
	if _, err := loadPlatformServiceFacts(submission, installation, nil, true); err != nil {
		t.Fatalf("the submission could not take the installation row: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-captured:
		if err != nil {
			t.Fatalf("capture: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the capture did not finish")
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND handler=?`, installation, appplatform.TaskHandlerEvent); got != 1 {
		t.Fatalf("%d delivery tasks", got)
	}
}
