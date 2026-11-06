package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func fixtureDigestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != appplatform.TaskDigestPath ||
		r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		fixtureTaskFailure(w, "digest_request_invalid")
		return
	}
	body, err := io.ReadAll(io.LimitReader(
		r.Body, appplatform.MaxTaskBackendBody+1))
	if err != nil || len(body) == 0 ||
		len(body) > appplatform.MaxTaskBackendBody {
		fixtureTaskFailure(w, "digest_request_invalid")
		return
	}
	request, err := appplatform.DecodeTaskBackendRequest(body)
	if err != nil {
		fixtureTaskFailure(w, "digest_request_invalid")
		return
	}
	registration, err := fixtureRegistration()
	if err != nil {
		fixtureTaskFailure(w, "digest_registration_unavailable")
		return
	}
	defer registration.Close()
	metadata, err := registration.Metadata()
	if err != nil || metadata.Identity.AppID != "scheduled-digest" ||
		!fixtureDigestIdentity(
			r.Header.Get(appplatform.TaskServiceIdentityHeader),
			metadata.Identity) {
		fixtureTaskFailure(w, "digest_service_identity_invalid")
		return
	}
	payloadSum := sha256.Sum256(request.Payload)
	digestSum := sha256.Sum256(append(
		[]byte(request.OccurrenceID+"\x00"), request.Payload...))
	result, err := json.Marshal(struct {
		OccurrenceID  string `json:"occurrenceId"`
		PayloadSHA256 string `json:"payloadSha256"`
		Digest        string `json:"digest"`
	}{
		OccurrenceID:  request.OccurrenceID,
		PayloadSHA256: hex.EncodeToString(payloadSum[:]),
		Digest:        hex.EncodeToString(digestSum[:]),
	})
	if err != nil || len(result) > appplatform.MaxTaskResultBytes {
		fixtureTaskFailure(w, "digest_result_invalid")
		return
	}
	fixtureTaskSuccess(w, json.RawMessage(result))
}

func fixtureDigestIdentity(
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
		!fixtureTaskResource(grant.Resources, "reports.read") ||
		!fixtureTaskResource(grant.Resources, "tasks.schedule") {
		return false
	}
	now, issued, expires := time.Now().UnixMilli(),
		int64Value(grant.IssuedMS), int64Value(grant.ExpiresMS)
	return issued > 0 && expires > now &&
		expires-issued <= int64(5*time.Minute/time.Millisecond)
}
