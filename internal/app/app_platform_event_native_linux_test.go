//go:build linux && (amd64 || arm64)

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func testNativePortalEventDelivery(
	t *testing.T, s *Server, activated platformOperationLease,
	endpoint platformRuntimeEndpoint,
) {
	t.Helper()
	ctx := context.Background()
	s.platformBackendEndpoint.Store(&endpoint)
	installation := activated.installationID
	activateNativeInstallation(t, s, activated)
	t.Log("NATIVE_EVENT_ACTIVATION_FIXTURE")
	const reportID = "41"
	subscriptionID, created, err := s.createPlatformEventSubscription(
		ctx, installation, appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report",
			ResourceID: reportID, MaxInflight: 4,
		})
	if err != nil || !created || !platformValidUUID(subscriptionID) {
		t.Fatal("native event subscription", subscriptionID, created, err,
			nativeEventInstallationState(t, s, installation))
	}
	t.Log("EVENT_SUBSCRIPTION_CREATED")
	mustExec(t, s.st, `UPDATE app_platform_capability_grants
		SET granted=0 WHERE installation_id=? AND capability='reports.read'`,
		installation)
	if _, _, err := s.createPlatformEventSubscription(
		ctx, installation, appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report",
			ResourceID: "42", MaxInflight: 1,
		}); !errors.Is(err, errAppPlatformEventUnavailable) {
		t.Fatal("event resource authority was not rechecked", err)
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants
		SET granted=1 WHERE installation_id=? AND capability='reports.read'`,
		installation)

	now := time.Now().UTC().UnixMilli()
	newer := NotificationEvent{
		ID:        "10000000-0000-4000-8000-000000000201",
		SourceKey: "report:41:native-newer", Type: appplatform.EventReportIngested,
		Payload: `{"newer":true}`, ResourceType: "report", ResourceID: reportID,
		CreatedAt: now + 1000,
	}
	older := NotificationEvent{
		ID:        "10000000-0000-4000-8000-000000000202",
		SourceKey: "report:41:native-older", Type: appplatform.EventReportIngested,
		Payload: `{"older":true}`, ResourceType: "report", ResourceID: reportID,
		CreatedAt: now,
	}
	for _, event := range []NotificationEvent{newer, older} {
		if err := s.st.CaptureNotification(event, nil, nil); err != nil {
			t.Fatal("capture native event", err)
		}
	}
	t.Log("EVENT_SOURCE_TASKS_CREATED")
	newerOccurrence, _ := appplatform.EventOccurrence(newer.ID, 1)
	olderOccurrence, _ := appplatform.EventOccurrence(older.ID, 1)
	mustExec(t, s.st, `UPDATE app_platform_tasks
		SET next_attempt_ms=1 WHERE installation_id=? AND occurrence_id=?`,
		installation, newerOccurrence)
	mustExec(t, s.st, `UPDATE app_platform_tasks
		SET next_attempt_ms=2 WHERE installation_id=? AND occurrence_id=?`,
		installation, olderOccurrence)
	worker, err := newPlatformTaskScheduler(
		s, platformFixtureID(891), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		worked, err := worker.tick(ctx)
		if err != nil || !worked {
			t.Fatalf("native event worker %d: worked=%v err=%v", i, worked, err)
		}
	}
	t.Log("EVENT_ORDERED_DELIVERIES_COMPLETE")
	if got := nativeTaskDecision(t, s, installation, newerOccurrence); got != "applied" {
		t.Fatalf("newer event decision = %q", got)
	}
	if got := nativeTaskDecision(t, s, installation, olderOccurrence); got != "ignored_older" {
		t.Fatalf("older event decision = %q", got)
	}

	lost := NotificationEvent{
		ID:        "10000000-0000-4000-8000-000000000203",
		SourceKey: "report:41:native-lost-ack", Type: appplatform.EventReportIngested,
		Payload: `{"drop_once":true}`, ResourceType: "report", ResourceID: reportID,
		CreatedAt: now + 2000,
	}
	if err := s.st.CaptureNotification(lost, nil, nil); err != nil {
		t.Fatal(err)
	}
	lostOccurrence, _ := appplatform.EventOccurrence(lost.ID, 1)
	if worked, err := worker.tick(ctx); err != nil || !worked {
		t.Fatalf("lost acknowledgement tick: worked=%v err=%v", worked, err)
	}
	if got := scalar[string](t, s.st,
		`SELECT state||':'||error_code FROM app_platform_tasks
		 WHERE installation_id=? AND occurrence_id=?`,
		installation, lostOccurrence); got != "uncertain:backend_response_uncertain" {
		t.Fatalf("lost acknowledgement task = %q", got)
	}
	if worked, err := worker.tick(ctx); err != nil || worked {
		t.Fatalf("uncertain event was retried automatically: worked=%v err=%v",
			worked, err)
	}
	t.Log("EVENT_LOST_ACK_UNCERTAIN")
	var taskID string
	var payload []byte
	if err := s.st.queryRow(`SELECT id,payload FROM app_platform_tasks
		WHERE installation_id=? AND occurrence_id=?`,
		installation, lostOccurrence).Scan(&taskID, &payload); err != nil {
		t.Fatal(err)
	}
	var appID, publisherID, release, epoch string
	if err := s.st.queryRow(`SELECT i.app_id,i.publisher_id,a.release_sha256,
		i.controller_epoch FROM app_platform_installations i
		JOIN app_platform_activations a
		  ON a.installation_id=i.id AND a.state='active'
		WHERE i.id=?`, installation).Scan(
		&appID, &publisherID, &release, &epoch); err != nil {
		t.Fatal(err)
	}
	retryLease := platformFixtureID(892)
	identity, _, err := s.issuePlatformServiceIdentity(
		ctx, installation, retryLease,
		[]string{"events.subscribe", "reports.read", "tasks.schedule"},
		time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	result := s.runPlatformEventDelivery(ctx, platformTaskLease{
		ID: taskID, InstallationID: installation, AppID: appID,
		PublisherID: publisherID, Handler: appplatform.TaskHandlerEvent,
		Payload: payload, Generation: 1, PolicyRevision: 1,
		ReleaseSHA256: release, ControllerEpoch: epoch, LeaseID: retryLease,
	}, string(identity))
	if result.Outcome != appplatform.TaskResultSucceeded {
		t.Fatalf("deduplicated retry = %+v", result)
	}
	t.Log("EVENT_MANUAL_REPLAY_COMPLETE")
	var retryResult struct {
		Decision string `json:"decision"`
	}
	if json.Unmarshal(result.ResultJSON, &retryResult) != nil ||
		retryResult.Decision != "duplicate" {
		t.Fatalf("deduplicated retry result = %s", result.ResultJSON)
	}
	t.Log("EVENT_DUPLICATE_OUT_OF_ORDER_LOST_ACK")
}

// The existing managed-install native fixture stops after verified delivery and
// boot. Task acceptance needs the same test-only final rows used by the viewer
// fixture before it can exercise active task authority.
func activateNativeInstallation(
	t *testing.T, s *Server, lease platformOperationLease,
) {
	t.Helper()
	stage := scalar[string](t, s.st, `SELECT stage
		FROM app_platform_operations WHERE id=?`, lease.id)
	activation := scalar[string](t, s.st, `SELECT state
		FROM app_platform_activations WHERE installation_id=?
		ORDER BY generation DESC LIMIT 1`, lease.installationID)
	phase := scalar[string](t, s.st, `SELECT phase
		FROM app_platform_installations WHERE id=?`, lease.installationID)
	if stage == "complete" && activation == "active" && phase == "ready" {
		return
	}
	tx, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: tx}
	now, err := platformDatabaseNow(e)
	if err != nil {
		t.Fatal(err)
	}
	model, err := appplatform.OperationFromSnapshot(lease.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := model.Advance(
		lease.snapshot.Revision, appplatform.StageComplete)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := e.exec(`UPDATE app_platform_operations
		SET stage='complete',state_revision=?,row_revision=row_revision+1,
		    completed_ms=?,lease_owner=NULL,lease_until_ms=NULL,updated_ms=?
		WHERE id=? AND installation_id=? AND stage='activate'
		  AND state_revision=? AND row_revision=? AND fence=?
		  AND completed_ms IS NULL`,
		int64(complete.Snapshot().Revision), now, now, lease.id,
		lease.installationID, int64(lease.snapshot.Revision),
		lease.rowRevision, lease.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		t.Fatal(err)
	}
	changed, err = e.exec(`UPDATE app_platform_activations
		SET state='active',updated_ms=?
		WHERE installation_id=? AND generation=? AND policy_revision=1
		  AND state='candidate'`,
		now, lease.installationID, lease.snapshot.Generation)
	if err := platformLeaseCAS(changed, err); err != nil {
		t.Fatal(err)
	}
	if err := activatePlatformInstallCapabilities(
		e, lease.installationID, lease.id, now); err != nil {
		t.Fatal(err)
	}
	readyPhase, err := platformReadyPhaseForInstallation(
		e, lease.installationID, 1)
	if err != nil {
		t.Fatal(err)
	}
	changed, err = e.exec(`UPDATE app_platform_installations
		SET phase=?,revision=revision+1,updated_ms=?
		WHERE id=? AND desired_state='enabled' AND phase='pending'`,
		readyPhase, now, lease.installationID)
	if err := platformLeaseCAS(changed, err); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func nativeEventInstallationState(
	t *testing.T, s *Server, installation string,
) string {
	t.Helper()
	state := scalar[string](t, s.st, `SELECT desired_state||':'||phase||':'||
		generation||':'||policy_revision||':'||controller_epoch
		FROM app_platform_installations WHERE id=?`, installation)
	activation := scalar[string](t, s.st, `SELECT generation||':'||policy_revision||':'||
		release_sha256||':'||data_format||':'||state
		FROM app_platform_activations WHERE installation_id=?
		ORDER BY generation DESC LIMIT 1`, installation)
	events := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants
		WHERE installation_id=? AND capability='events.subscribe' AND granted=1`,
		installation)
	reports := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants
		WHERE installation_id=? AND capability='reports.read' AND granted=1`,
		installation)
	background := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants
		WHERE installation_id=? AND capability='background.run' AND granted=1`,
		installation)
	return fmt.Sprintf(
		"installation=%s activation=%s grants(events=%d,reports=%d,background=%d)",
		state, activation, events, reports, background)
}

func nativeTaskDecision(
	t *testing.T, s *Server, installation, occurrence string,
) string {
	t.Helper()
	var result []byte
	if err := s.st.queryRow(`SELECT result_json FROM app_platform_tasks
		WHERE installation_id=? AND occurrence_id=?`,
		installation, occurrence).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		state := scalar[string](t, s.st, `SELECT state||':'||
			COALESCE(error_code,'')||':generation='||generation||
			':policy='||policy_revision||':'||COALESCE(result_json,'')
			FROM app_platform_tasks
			WHERE installation_id=? AND occurrence_id=?`,
			installation, occurrence)
		t.Fatalf("event task has no result: %s", state)
	}
	var decoded struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Decision
}
