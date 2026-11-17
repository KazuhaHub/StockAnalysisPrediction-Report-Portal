package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformTaskHTTPFixture(t *testing.T, s *Server, installation, body string) *httptest.ResponseRecorder {
	t.Helper()
	identity, _, err := s.issuePlatformServiceIdentity(
		context.Background(), installation, platformFixtureID(6201),
		[]string{"tasks.schedule"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return platformTaskHTTPWithIdentity(t, s, installation, body, identity)
}

func platformTaskHTTPWithIdentity(t *testing.T, s *Server, installation, body string, identity []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/app-platform/installations/"+installation+"/tasks/background",
		strings.NewReader(body))
	req.SetPathValue("installation", installation)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Portal-Service-Identity", base64.RawURLEncoding.EncodeToString(identity))
	rec := httptest.NewRecorder()
	s.apiPlatformServiceTaskSchedule(rec, req)
	return rec
}

func TestPlatformTaskSchedulingAPIUsesServiceIdentityAndOccurrenceIdentity(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	body := `{"occurrenceId":"api-task","handler":"app.backend.task","payload":{"value":1}}`
	first := platformTaskHTTPFixture(t, s, installation, body)
	if first.Code != http.StatusOK {
		t.Fatalf("schedule: %d %s", first.Code, first.Body.String())
	}
	var created struct {
		ID      string `json:"id"`
		Created bool   `json:"created"`
	}
	if json.Unmarshal(first.Body.Bytes(), &created) != nil || !created.Created || !platformValidUUID(created.ID) {
		t.Fatalf("schedule response: %s", first.Body.String())
	}
	replay := platformTaskHTTPFixture(t, s, installation, body)
	if replay.Code != http.StatusOK || !bytes.Contains(replay.Body.Bytes(), []byte(`"created":false`)) {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	changed := platformTaskHTTPFixture(t, s, installation,
		`{"occurrenceId":"api-task","handler":"app.backend.task","payload":{"value":2}}`)
	if changed.Code != http.StatusConflict {
		t.Fatalf("changed occurrence: %d %s", changed.Code, changed.Body.String())
	}
	unknown := platformTaskHTTPFixture(t, s, installation,
		`{"occurrenceId":"api-task-2","handler":"app.backend.task","payload":{"value":1},"recipient":"x"}`)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", unknown.Code, unknown.Body.String())
	}
	revokedIdentity, _, err := s.issuePlatformServiceIdentity(
		context.Background(), installation, platformFixtureID(6202),
		[]string{"tasks.schedule"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='tasks.schedule'`, installation)
	revoked := platformTaskHTTPWithIdentity(t, s, installation,
		`{"occurrenceId":"api-task-revoked","handler":"app.backend.task","payload":{"value":3}}`, revokedIdentity)
	if revoked.Code != http.StatusNotFound {
		t.Fatalf("revoked scheduling: %d %s", revoked.Code, revoked.Body.String())
	}
}

func TestPlatformAdminTaskAPIExposesSafeHistoryAndReview(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	admitTaskFixture(t, s, installation, "admin-pending",
		appplatform.TaskHandlerBackend, []byte(`{"fixture":true}`))
	list := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/app-platform/tasks?installation="+installation, nil)
	s.apiPlatformTaskList(list, req, "admin")
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	var listed struct {
		Tasks []platformAdminTask `json:"tasks"`
	}
	if json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed.Tasks) != 1 ||
		listed.Tasks[0].OccurrenceID != "admin-pending" ||
		len(listed.Tasks[0].AttemptHistory) != 0 {
		t.Fatalf("list response: %s", list.Body.String())
	}
	if bytes.Contains(list.Body.Bytes(), []byte(`"payload"`)) {
		t.Fatal("administrator list exposed immutable task payload")
	}
	id := listed.Tasks[0].ID
	cancel := httptest.NewRecorder()
	cancelReq := httptest.NewRequest(http.MethodPost,
		"/api/admin/app-platform/tasks/"+id+"/cancel", strings.NewReader(`{}`))
	cancelReq.SetPathValue("id", id)
	s.apiPlatformTaskCancel(cancel, cancelReq, "admin")
	if cancel.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", cancel.Code, cancel.Body.String())
	}
	if got := scalar[string](t, s.st, `SELECT state||':'||error_code FROM app_platform_tasks WHERE id=?`, id); got != "cancelled:admin_cancelled" {
		t.Fatalf("cancelled task = %q", got)
	}
}

func TestPlatformAdminTaskReviewClosesUncertainTaskWithoutReplay(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	admitTaskFixture(t, s, installation, "admin-uncertain",
		appplatform.TaskHandlerBackend, []byte(`{"fixture":true}`))
	claimed, err := s.claimNextPlatformTask(context.Background(), platformFixtureID(6211))
	if err != nil {
		t.Fatal(err)
	}
	started, identity, err := s.startPlatformTask(context.Background(), claimed)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='background.run'`, installation)
	if err := s.completePlatformTask(context.Background(), started, identity, appplatform.TaskExecutionResult{
		Outcome: appplatform.TaskResultSucceeded, ResultJSON: []byte(`{"private":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	review := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/admin/app-platform/tasks/"+started.ID+"/review",
		strings.NewReader(`{"decision":"failed"}`))
	req.SetPathValue("id", started.ID)
	s.apiPlatformTaskReview(review, req, "admin")
	if review.Code != http.StatusOK {
		t.Fatalf("review: %d %s", review.Code, review.Body.String())
	}
	if got := scalar[string](t, s.st, `SELECT state||':'||error_code FROM app_platform_tasks WHERE id=?`, started.ID); got != "failed:reviewed_failed" {
		t.Fatalf("reviewed task = %q", got)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE result_json IS NOT NULL`) != 0 {
		t.Fatal("review released uncertain result")
	}
	if worked, err := newPlatformTaskSchedulerMust(t, s, 6212).tick(context.Background()); err != nil || worked {
		t.Fatalf("reviewed task replayed: worked=%v err=%v", worked, err)
	}
}

func newPlatformTaskSchedulerMust(t *testing.T, s *Server, worker int) *platformTaskScheduler {
	t.Helper()
	scheduler, err := newPlatformTaskScheduler(s, platformFixtureID(worker), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}
