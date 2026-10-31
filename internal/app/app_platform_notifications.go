package app

import (
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type platformNotificationInput struct {
	binding                                platformObjectBinding
	occurrenceID, title, message, severity string
}

func readPlatformNotificationInput(raw []byte, foreground bool) (platformNotificationInput, error) {
	var result platformNotificationInput
	if len(raw) == 0 || len(raw) > 16384 {
		return result, errPlatformManagementInput
	}
	keys := []string{"occurrenceId", "title", "message", "severity"}
	if foreground {
		keys = append(keys, "binding")
	}
	fields, err := platformManagementObject(raw, keys...)
	if err != nil || len(fields) != len(keys) {
		return result, errPlatformManagementInput
	}
	if foreground {
		binding, err := platformManagementObject(fields["binding"], "generation", "policyRevision", "releaseSha256")
		if err != nil || len(binding) != 3 {
			return result, errPlatformManagementInput
		}
		for key, dst := range map[string]*string{"generation": &result.binding.Generation, "policyRevision": &result.binding.PolicyRevision, "releaseSha256": &result.binding.ReleaseSHA256} {
			*dst, err = platformManagementString(binding, key)
			if err != nil {
				return result, errPlatformManagementInput
			}
		}
		if !platformPositiveCounter(result.binding.Generation) || !platformPositiveCounter(result.binding.PolicyRevision) || !platformValidSHA(result.binding.ReleaseSHA256) {
			return result, errPlatformManagementInput
		}
	}
	for key, dst := range map[string]*string{"occurrenceId": &result.occurrenceID, "title": &result.title, "message": &result.message, "severity": &result.severity} {
		*dst, err = platformManagementString(fields, key)
		if err != nil {
			return result, errPlatformManagementInput
		}
	}
	if !notificationOccurrenceRe.MatchString(result.occurrenceID) || strings.TrimSpace(result.title) == "" || len(result.title) > 240 || len(result.message) > 8192 || !notificationSelects("info,warning,error,resolved", result.severity) {
		return result, errPlatformManagementInput
	}
	return result, nil
}

func platformNotificationFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAppPlatformLaunchUnavailable), errors.Is(err, errAppPlatformServiceUnavailable):
		jsonErrorCode(w, 404, "app_platform_notification_unavailable", "application notification publication unavailable")
	case errors.Is(err, errNotificationConflict):
		jsonErrorCode(w, 409, "notification_conflict", "source occurrence was already used with different content")
	case errors.Is(err, errNotificationCapacity):
		jsonErrorCode(w, 429, "notification_capacity", "notification publication capacity unavailable")
	default:
		jsonErrorCode(w, 500, "db_error", "notification capture failed")
	}
}

func (s *Server) apiPlatformNotificationPublish(w http.ResponseWriter, r *http.Request, user string) {
	if !platformHTTPPath(r) || !platformValidUUID(r.PathValue("installation")) || platformManagementOrigin.Check(r) != nil {
		platformDocumentFailure(w)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	if err != nil {
		jsonErrorCode(w, 400, "app_platform_invalid_request", "invalid application request")
		return
	}
	defer clear(raw)
	input, err := readPlatformNotificationInput(raw, true)
	if err != nil {
		jsonErrorCode(w, 400, "app_platform_invalid_request", "invalid application request")
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		jsonErrorCode(w, 401, "session_expired", "session unavailable")
		return
	}
	installation := r.PathValue("installation")
	var publisher, eventID string
	_, err = s.withPlatformLaunchAccountReadContext(r.Context(), session, installation, func(e migExec, current platformLaunchAccess, manifest appplatform.Manifest, _ platformActorAccount) error {
		if current.generation != input.binding.Generation || current.policyRevision != input.binding.PolicyRevision || current.releaseSHA != input.binding.ReleaseSHA256 {
			return errAppPlatformLaunchUnavailable
		}
		var granted int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND capability='notifications.publish' AND granted=1 AND policy_revision=?`, installation, current.policyRevision).Scan(&granted); err != nil || granted != 1 || !platformManifestAllowsNotification(manifest) {
			return errAppPlatformLaunchUnavailable
		}
		publisher = "app." + manifest.ID
		return nil
	})
	if err != nil {
		platformNotificationFailure(w, err)
		return
	}
	eventID, _, err = s.capturePublishedNotification(publisher, input.occurrenceID, input.title, input.message, input.severity)
	if err != nil {
		platformNotificationFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": eventID})
}

func platformManifestAllowsNotification(m appplatform.Manifest) bool {
	for _, capability := range append(append([]string(nil), m.Capabilities.Required...), m.Capabilities.Optional...) {
		if capability == "notifications.publish" {
			return true
		}
	}
	return false
}

func (s *Server) apiPlatformServiceNotificationPublish(w http.ResponseWriter, r *http.Request) {
	if !platformHTTPPath(r) || !platformValidUUID(r.PathValue("installation")) {
		jsonErrorCode(w, 404, "app_platform_notification_unavailable", "application notification publication unavailable")
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		jsonErrorCode(w, 415, "invalid_content_type", "application/json required")
		return
	}
	encoded := r.Header.Get("X-Portal-Service-Identity")
	if len(encoded) < 1 || len(encoded) > 12000 {
		platformNotificationFailure(w, errAppPlatformServiceUnavailable)
		return
	}
	rawIdentity, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(rawIdentity) != encoded {
		clear(rawIdentity)
		platformNotificationFailure(w, errAppPlatformServiceUnavailable)
		return
	}
	defer clear(rawIdentity)
	grant, err := s.verifyPlatformServiceIdentity(r.Context(), rawIdentity, "notifications.publish")
	if err != nil || grant.InstallationID != r.PathValue("installation") {
		platformNotificationFailure(w, errAppPlatformServiceUnavailable)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid notification request")
		return
	}
	defer clear(raw)
	input, err := readPlatformNotificationInput(raw, false)
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid notification request")
		return
	}
	publisher := "app." + grant.AppID
	id, created, err := s.capturePublishedNotification(publisher, input.occurrenceID, input.title, input.message, input.severity)
	if err != nil {
		platformNotificationFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id, "created": created})
}
