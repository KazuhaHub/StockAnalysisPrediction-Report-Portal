package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The platform task worker runs service tasks against the backend (doc 171, SEC-09): a backend task at POST /v1/tasks/run and an event
// delivery at POST /v1/events/deliver. The fixture answers both at once with a stable success, so that what the soak measures is the Portal's
// worker, the runner's forwarding and the queue, not an application. It checks the request is a well-formed task and carries a current service
// identity for the resource the task needs; the runner has already verified the registration and the identity's signature before it forwards.

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// soakTaskHandler answers a task request and reports whether the request was one.
func soakTaskHandler(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost || (r.URL.Path != appplatform.TaskBackendPath && r.URL.Path != appplatform.TaskEventPath) {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, appplatform.MaxTaskBackendBody+1))
	defer clear(body)
	if err != nil || len(body) == 0 || len(body) > appplatform.MaxTaskBackendBody || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		soakTaskFailure(w, "soak_task_request_invalid")
		return true
	}
	var result map[string]string
	resource := "tasks.schedule"
	if r.URL.Path == appplatform.TaskBackendPath {
		request, err := appplatform.DecodeTaskBackendRequest(body)
		if err != nil {
			soakTaskFailure(w, "soak_task_request_invalid")
			return true
		}
		result = map[string]string{"handled": "backend", "occurrence_id": request.OccurrenceID}
	} else {
		delivery, err := appplatform.ParseEventDelivery(body)
		if err != nil {
			soakTaskFailure(w, "soak_task_request_invalid")
			return true
		}
		resource = "events.subscribe"
		result = map[string]string{"decision": "applied", "event_id": delivery.EventID}
	}
	if !soakTaskIdentity(r.Header.Get(appplatform.TaskServiceIdentityHeader), resource) {
		soakTaskFailure(w, "soak_task_service_identity_invalid")
		return true
	}
	raw, err := json.Marshal(result)
	if err != nil {
		soakTaskFailure(w, "soak_task_result_invalid")
		return true
	}
	encoded, err := appplatform.EncodeTaskBackendResponse(appplatform.TaskExecutionResult{Outcome: appplatform.TaskResultSucceeded, ResultJSON: raw})
	if err != nil {
		soakTaskFailure(w, "soak_task_result_invalid")
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
	return true
}

// soakTaskIdentity is whether the header is a service identity of this format that is still current and names the resource.
func soakTaskIdentity(raw, resource string) bool {
	if len(raw) == 0 || len(raw) > 8192 {
		return false
	}
	var grant appplatform.ServiceIdentityGrant
	if json.Unmarshal([]byte(raw), &grant) != nil || grant.Format != appplatform.ServiceIdentityFormat {
		return false
	}
	expires, err := strconv.ParseInt(grant.ExpiresMS, 10, 64)
	if err != nil || expires <= time.Now().UnixMilli() {
		return false
	}
	for _, have := range grant.Resources {
		if have == resource {
			return true
		}
	}
	return false
}

func soakTaskFailure(w http.ResponseWriter, code string) {
	encoded, err := appplatform.EncodeTaskBackendResponse(appplatform.TaskExecutionResult{Outcome: appplatform.TaskResultFailed, ErrorCode: code})
	if err != nil {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}
