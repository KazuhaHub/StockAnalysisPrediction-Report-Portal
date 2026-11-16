package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const (
	testTaskID       = "10000000-0000-4000-8000-000000000001"
	testSubscription = "10000000-0000-4000-8000-000000000002"
)

func testIdentity(t *testing.T, resources []string, expires time.Time) string {
	t.Helper()
	raw, err := json.Marshal(appplatform.ServiceIdentityGrant{
		Format: appplatform.ServiceIdentityFormat, LeaseID: testTaskID, AppID: "notes-soak-m0", InstallationID: testSubscription,
		ControllerEpoch: testTaskID, Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64), Resources: resources,
		IssuedMS: "1", ExpiresMS: itoa(expires.UnixMilli()), Mac: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func serveTask(t *testing.T, path, identity string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	if identity != "" {
		request.Header.Set(appplatform.TaskServiceIdentityHeader, identity)
	}
	recorder := httptest.NewRecorder()
	if !soakTaskHandler(recorder, request) {
		t.Fatal("the task path was not handled")
	}
	return recorder
}

func decodeOutcome(t *testing.T, recorder *httptest.ResponseRecorder) (appplatform.TaskExecutionResult, string) {
	t.Helper()
	result, err := appplatform.DecodeTaskBackendResponse(recorder.Body.Bytes())
	if err != nil {
		t.Fatalf("the answer is not a task response: %v %q", err, recorder.Body.String())
	}
	return result, string(result.ResultJSON)
}

func TestSoakBackendAnswersABackendTaskWithAStableSuccess(t *testing.T) {
	body, err := appplatform.EncodeTaskBackendRequest(testTaskID, "soak:run:1", []byte(`{"soak":1}`))
	if err != nil {
		t.Fatal(err)
	}
	identity := testIdentity(t, []string{"tasks.schedule"}, time.Now().Add(time.Minute))
	recorder := serveTask(t, appplatform.TaskBackendPath, identity, body)
	result, raw := decodeOutcome(t, recorder)
	if recorder.Code != http.StatusOK || result.Outcome != appplatform.TaskResultSucceeded || !strings.Contains(raw, "soak:run:1") {
		t.Fatalf("answer = %d %+v %s", recorder.Code, result, raw)
	}
}

func TestSoakBackendAnswersAnEventDeliveryWithItsEventID(t *testing.T) {
	payload, _, err := appplatform.BuildEventDelivery(testSubscription, "10000000-0000-4000-8000-000000000003", appplatform.EventReportIngested,
		"report", "7", 1000, []byte(`{"id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	identity := testIdentity(t, []string{"events.subscribe", "reports.read", "tasks.schedule"}, time.Now().Add(time.Minute))
	recorder := serveTask(t, appplatform.TaskEventPath, identity, payload)
	result, raw := decodeOutcome(t, recorder)
	if recorder.Code != http.StatusOK || result.Outcome != appplatform.TaskResultSucceeded || !strings.Contains(raw, "10000000-0000-4000-8000-000000000003") {
		t.Fatalf("answer = %d %+v %s", recorder.Code, result, raw)
	}
}

func TestSoakBackendRefusesATaskWithoutTheServiceIdentityOrWithAnExpiredOrWrongResource(t *testing.T) {
	body, _ := appplatform.EncodeTaskBackendRequest(testTaskID, "soak:run:2", []byte(`{"soak":2}`))
	for name, identity := range map[string]string{
		"missing":        "",
		"expired":        testIdentity(t, []string{"tasks.schedule"}, time.Now().Add(-time.Minute)),
		"wrong resource": testIdentity(t, []string{"reports.read"}, time.Now().Add(time.Minute)),
		"not json":       "{",
	} {
		result, _ := decodeOutcome(t, serveTask(t, appplatform.TaskBackendPath, identity, body))
		if result.Outcome != appplatform.TaskResultFailed || result.ErrorCode != "soak_task_service_identity_invalid" {
			t.Errorf("%s: %+v", name, result)
		}
	}
}

func TestSoakBackendRefusesAMalformedTaskBody(t *testing.T) {
	identity := testIdentity(t, []string{"events.subscribe", "reports.read", "tasks.schedule"}, time.Now().Add(time.Minute))
	for _, path := range []string{appplatform.TaskBackendPath, appplatform.TaskEventPath} {
		result, _ := decodeOutcome(t, serveTask(t, path, identity, []byte(`{"format":"x"}`)))
		if result.Outcome != appplatform.TaskResultFailed || result.ErrorCode != "soak_task_request_invalid" {
			t.Errorf("%s: %+v", path, result)
		}
	}
}

func TestSoakBackendLeavesEveryOtherPathToTheMainHandler(t *testing.T) {
	for _, path := range []string{"/v1/echo", "/v1/soak/status", "/health/ready", "/v1/tasks/digest"} {
		if soakTaskHandler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Errorf("%s was handled as a task", path)
		}
	}
	if soakTaskHandler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, appplatform.TaskBackendPath, nil)) {
		t.Error("a GET was handled as a task")
	}
}
