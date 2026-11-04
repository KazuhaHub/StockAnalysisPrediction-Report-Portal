//go:build linux && (amd64 || arm64)

package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func testNativeScheduledDigest(
	t *testing.T, s *Server, lease platformOperationLease,
) {
	t.Helper()
	ctx := context.Background()
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	occurrence := "digest:2026-11-03"
	payload := []byte(`{"scheduleId":"daily","reportIds":[41,42]}`)
	admission := platformTaskAdmission{
		OccurrenceID: occurrence,
		Handler:      appplatform.TaskHandlerDigest,
		Payload:      payload,
		ExpiresMS:    now + int64(time.Hour/time.Millisecond),
	}
	id, created, err := s.admitPlatformTask(
		ctx, lease.installationID, admission)
	if err != nil || !created || !platformValidUUID(id) {
		t.Fatal("native digest admission", id, created, err,
			nativeEventInstallationState(t, s, lease.installationID))
	}
	replayed, created, err := s.admitPlatformTask(
		ctx, lease.installationID, admission)
	if err != nil || created || replayed != id {
		t.Fatal("native digest occurrence replay", replayed, created, err)
	}
	changed := admission
	changed.Payload = []byte(`{"scheduleId":"daily","reportIds":[41]}`)
	if _, created, err := s.admitPlatformTask(
		ctx, lease.installationID, changed); created ||
		!errors.Is(err, errAppPlatformTaskConflict) {
		t.Fatal("changed native digest occurrence", created, err)
	}
	worker, err := newPlatformTaskScheduler(
		s, platformFixtureID(894), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var state, raw string
	for attempt := 0; attempt < 32; attempt++ {
		worked, err := worker.tick(ctx)
		if err != nil {
			t.Fatal("native digest worker", err)
		}
		if err := s.st.queryRow(`SELECT state,COALESCE(result_json,'')
			FROM app_platform_tasks WHERE id=?`, id).Scan(&state, &raw); err != nil {
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
		OccurrenceID  string `json:"occurrenceId"`
		PayloadSHA256 string `json:"payloadSha256"`
		Digest        string `json:"digest"`
	}
	if state != "succeeded" || json.Unmarshal([]byte(raw), &result) != nil ||
		result.OccurrenceID != occurrence ||
		!platformValidSHA(result.PayloadSHA256) ||
		!platformValidSHA(result.Digest) ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks
			WHERE installation_id=? AND handler=?`,
			lease.installationID,
			appplatform.TaskHandlerDigest) != 1 {
		t.Fatal("native digest result", state, raw,
			nativeEventInstallationState(t, s, lease.installationID))
	}
	t.Log("SCHEDULED_DIGEST_HANDLER_VERIFIED")
}
