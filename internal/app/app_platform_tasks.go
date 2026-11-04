package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

func decodePlatformServiceIdentityHeader(encoded string) ([]byte, error) {
	if len(encoded) < 1 || len(encoded) > 12000 {
		return nil, errAppPlatformServiceUnavailable
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		clear(raw)
		return nil, errAppPlatformServiceUnavailable
	}
	return raw, nil
}

func readPlatformTaskAdmission(raw []byte, now int64) (platformTaskAdmission, error) {
	var result platformTaskAdmission
	fields, err := platformManagementObject(raw,
		"occurrenceId", "handler", "payload", "runAt", "expiresAt")
	if err != nil {
		return result, errPlatformManagementInput
	}
	result.OccurrenceID, err = platformManagementString(fields, "occurrenceId")
	if err != nil {
		return result, errPlatformManagementInput
	}
	result.Handler, err = platformManagementString(fields, "handler")
	if err != nil || !notificationOccurrenceRe.MatchString(result.OccurrenceID) || len(result.Handler) < 1 || len(result.Handler) > 64 {
		return result, errPlatformManagementInput
	}
	result.Payload = bytes.Clone(fields["payload"])
	if len(result.Payload) == 0 || bytes.Equal(bytes.TrimSpace(result.Payload), []byte("null")) {
		return result, errPlatformManagementInput
	}
	result.RunAtMS = 0
	if rawRun, ok := fields["runAt"]; ok {
		var value string
		if json.Unmarshal(rawRun, &value) != nil {
			return result, errPlatformManagementInput
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || parsed.Before(time.UnixMilli(now)) {
			return result, errPlatformManagementInput
		}
		result.RunAtMS = parsed.UTC().UnixMilli()
	}
	result.ExpiresMS = 0
	if rawExpiry, ok := fields["expiresAt"]; ok {
		var value string
		if json.Unmarshal(rawExpiry, &value) != nil {
			return result, errPlatformManagementInput
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return result, errPlatformManagementInput
		}
		result.ExpiresMS = parsed.UTC().UnixMilli()
	}
	if result.RunAtMS != 0 && result.RunAtMS < now ||
		result.ExpiresMS != 0 && result.ExpiresMS <= now {
		return result, errPlatformManagementInput
	}
	return result, nil
}

func platformTaskFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPlatformManagementInput):
		jsonErrorCode(w, 400, "invalid_param", "provide a closed task occurrence, handler, payload and bounded schedule")
	case errors.Is(err, errAppPlatformServiceUnavailable), errors.Is(err, errAppPlatformTaskUnavailable):
		jsonErrorCode(w, 404, "app_platform_task_unavailable", "application task scheduling unavailable")
	case errors.Is(err, errAppPlatformTaskConflict):
		jsonErrorCode(w, 409, "task_occurrence_conflict", "task occurrence already has different immutable content")
	case errors.Is(err, errAppPlatformTaskQuota):
		jsonErrorCode(w, 429, "task_capacity", "application task capacity unavailable")
	default:
		jsonErrorCode(w, 500, "db_error", "application task scheduling failed")
	}
}

func (s *Server) apiPlatformServiceTaskSchedule(w http.ResponseWriter, r *http.Request) {
	if !platformHTTPPath(r) || !platformValidUUID(r.PathValue("installation")) {
		platformTaskFailure(w, errAppPlatformTaskUnavailable)
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		jsonErrorCode(w, 415, "invalid_content_type", "application/json required")
		return
	}
	rawIdentity, err := decodePlatformServiceIdentityHeader(r.Header.Get("X-Portal-Service-Identity"))
	if err != nil {
		platformTaskFailure(w, err)
		return
	}
	defer clear(rawIdentity)
	grant, err := s.verifyPlatformServiceIdentity(r.Context(), rawIdentity, "tasks.schedule")
	if err != nil || grant.InstallationID != r.PathValue("installation") {
		platformTaskFailure(w, errAppPlatformServiceUnavailable)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		platformTaskFailure(w, errPlatformManagementInput)
		return
	}
	defer clear(raw)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		platformTaskFailure(w, err)
		return
	}
	in, err := readPlatformTaskAdmission(raw, now)
	if err != nil {
		platformTaskFailure(w, err)
		return
	}
	id, created, err := s.admitPlatformTask(r.Context(), grant.InstallationID, in)
	if err != nil {
		platformTaskFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id, "created": created})
}
