package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func fixtureBackendTaskHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != appplatform.TaskBackendPath ||
		r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		fixtureTaskFailure(w, "backend_task_request_invalid")
		return
	}
	body, err := io.ReadAll(io.LimitReader(
		r.Body, appplatform.MaxTaskBackendBody+1))
	if err != nil || len(body) == 0 ||
		len(body) > appplatform.MaxTaskBackendBody {
		fixtureTaskFailure(w, "backend_task_request_invalid")
		return
	}
	request, err := appplatform.DecodeTaskBackendRequest(body)
	if err != nil {
		fixtureTaskFailure(w, "backend_task_request_invalid")
		return
	}
	registration, err := fixtureRegistration()
	if err != nil {
		fixtureTaskFailure(w, "backend_task_registration_unavailable")
		return
	}
	defer registration.Close()
	metadata, err := registration.Metadata()
	if err != nil || metadata.Identity.AppID == "" ||
		!fixtureBackendTaskIdentity(
			r.Header.Get(appplatform.TaskServiceIdentityHeader),
			metadata.Identity) {
		fixtureTaskFailure(w, "backend_task_service_identity_invalid")
		return
	}
	result, err := json.Marshal(struct {
		OccurrenceID   string `json:"occurrenceId"`
		AppID          string `json:"appId"`
		Generation     string `json:"generation"`
		PolicyRevision string `json:"policyRevision"`
	}{
		OccurrenceID: request.OccurrenceID,
		AppID:        metadata.Identity.AppID, Generation: metadata.Identity.Generation,
		PolicyRevision: metadata.Identity.PolicyRevision,
	})
	if err != nil || len(result) > appplatform.MaxTaskResultBytes {
		fixtureTaskFailure(w, "backend_task_result_invalid")
		return
	}
	fixtureTaskSuccess(w, json.RawMessage(result))
}

func fixtureBackendTaskIdentity(
	raw string, identity appplatform.BackendIdentity,
) bool {
	if len(raw) == 0 || len(raw) > 8192 {
		return false
	}
	var grant appplatform.ServiceIdentityGrant
	if json.Unmarshal([]byte(raw), &grant) != nil ||
		grant.Format != appplatform.ServiceIdentityFormat ||
		grant.AppID != identity.AppID ||
		grant.InstallationID != identity.InstallationID ||
		grant.Generation != identity.Generation ||
		grant.PolicyRevision != identity.PolicyRevision ||
		grant.ControllerEpoch != identity.ControllerEpoch ||
		grant.ReleaseSHA256 != identity.ReleaseSHA256 ||
		len(grant.Mac) != 64 ||
		!fixtureTaskResource(grant.Resources, "tasks.schedule") {
		return false
	}
	now, issued, expires := time.Now().UnixMilli(),
		int64Value(grant.IssuedMS), int64Value(grant.ExpiresMS)
	generation, generationErr := strconv.ParseInt(
		identity.Generation, 10, 64)
	policy, policyErr := strconv.ParseInt(identity.PolicyRevision, 10, 64)
	return generationErr == nil && generation >= 1 &&
		policyErr == nil && policy >= 1 &&
		issued > 0 && expires > now &&
		expires-issued <= int64(5*time.Minute/time.Millisecond)
}
