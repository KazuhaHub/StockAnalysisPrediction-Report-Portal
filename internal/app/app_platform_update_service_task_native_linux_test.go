//go:build linux && (amd64 || arm64)

package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func testNativeUpdatedEventTask(
	t *testing.T, s *Server, installation string,
	endpoint platformRuntimeEndpoint,
) {
	t.Helper()
	ctx := context.Background()
	subscription, created, err := s.createPlatformEventSubscription(
		ctx, installation, appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report",
			ResourceID: "41", MaxInflight: 4,
		})
	if err != nil || !created || !platformValidUUID(subscription) {
		t.Fatal("updated event subscription", subscription, created, err)
	}
	now := time.Now().UTC().UnixMilli()
	event := NotificationEvent{
		ID:        "10000000-0000-4000-8000-000000000231",
		SourceKey: "report:41:updated-generation", Type: appplatform.EventReportIngested,
		Payload: `{"updated":true}`, ResourceType: "report", ResourceID: "41",
		CreatedAt: now,
	}
	if err := s.st.CaptureNotification(event, nil, nil); err != nil {
		t.Fatal("updated event capture", err)
	}
	occurrence, _ := appplatform.EventOccurrence(event.ID, 1)
	mustExec(t, s.st, `UPDATE app_platform_tasks
		SET next_attempt_ms=1 WHERE installation_id=? AND occurrence_id=?`,
		installation, occurrence)
	previousEndpoint := s.platformBackendEndpoint.Swap(&endpoint)
	defer s.platformBackendEndpoint.Store(previousEndpoint)
	worker, err := newPlatformTaskScheduler(
		s, platformFixtureID(899), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.tick(ctx); err != nil || !worked {
		t.Fatal("updated event worker", worked, err)
	}
	var state, raw string
	if err := s.st.queryRow(`SELECT state,COALESCE(result_json,'')
		FROM app_platform_tasks WHERE installation_id=? AND occurrence_id=?`,
		installation, occurrence).Scan(&state, &raw); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Decision string `json:"decision"`
		EventID  string `json:"event_id"`
	}
	if state != "succeeded" || json.Unmarshal([]byte(raw), &result) != nil ||
		result.Decision != "applied" || result.EventID != event.ID {
		t.Fatal("updated event task", state, raw)
	}
	t.Log("MANAGED_UPDATE_EVENT_TASK_VERIFIED")
}

func testNativeUpdatedBackendTask(
	t *testing.T, s *Server, installation string,
	endpoint platformRuntimeEndpoint,
) {
	t.Helper()
	ctx := context.Background()
	var appID, publisherID, release, epoch string
	var generation, policy int64
	if err := s.st.queryRow(`SELECT i.app_id,i.publisher_id,
		i.controller_epoch,a.release_sha256,i.generation,i.policy_revision
		FROM app_platform_installations i
		JOIN app_platform_activations a
		  ON a.installation_id=i.id AND a.state='active'
		WHERE i.id=?`, installation).
		Scan(&appID, &publisherID, &epoch, &release,
			&generation, &policy); err != nil ||
		generation != 2 || policy != 2 {
		t.Fatal("updated task installation", generation, policy, err)
	}
	identity, _, err := s.issuePlatformServiceIdentity(
		ctx, installation, platformFixtureID(897),
		[]string{"tasks.schedule"}, time.Minute)
	if err != nil {
		t.Fatal("updated task identity", err)
	}
	defer clear(identity)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	taskID, created, err := s.admitPlatformTask(
		ctx, installation, platformTaskAdmission{
			OccurrenceID: "updated-backend:generation-2",
			Handler:      appplatform.TaskHandlerBackend,
			Payload:      []byte(`{"updated":true}`),
			ExpiresMS:    now + int64(time.Hour/time.Millisecond),
		})
	if err != nil || !created {
		t.Fatal("updated task admission", taskID, created, err)
	}
	previousEndpoint := s.platformBackendEndpoint.Swap(&endpoint)
	defer s.platformBackendEndpoint.Store(previousEndpoint)
	previousTaskBackend := s.platformTaskBackend
	defer func() { s.platformTaskBackend = previousTaskBackend }()
	s.platformTaskBackend = s.runPlatformBackendTask
	worker, err := newPlatformTaskScheduler(
		s, platformFixtureID(898), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var state, raw, errorCode string
	for attempt := 0; attempt < 32; attempt++ {
		worked, err := worker.tick(ctx)
		if err != nil {
			t.Fatal("updated task worker", err)
		}
		if err := s.st.queryRow(`SELECT state,COALESCE(result_json,''),
			COALESCE(error_code,'') FROM app_platform_tasks WHERE id=?`,
			taskID).Scan(&state, &raw, &errorCode); err != nil {
			t.Fatal(err)
		}
		if state == "succeeded" || state == "failed" ||
			state == "uncertain" || state == "review_required" {
			break
		}
		if !worked {
			break
		}
	}
	var result struct {
		OccurrenceID   string `json:"occurrenceId"`
		AppID          string `json:"appId"`
		Generation     string `json:"generation"`
		PolicyRevision string `json:"policyRevision"`
	}
	if state != "succeeded" ||
		json.Unmarshal([]byte(raw), &result) != nil ||
		result.OccurrenceID != "updated-backend:generation-2" ||
		result.AppID != appID || result.Generation != "2" ||
		result.PolicyRevision != "2" {
		t.Fatal("updated generation service task", state, errorCode, raw)
	}
	t.Log("MANAGED_UPDATE_SERVICE_TASK_VERIFIED")
}
