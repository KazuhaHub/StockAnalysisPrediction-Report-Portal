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

// platformTaskBodyTimeout is how long the route waits for a submission's body, as the backend and network relays do.
var platformTaskBodyTimeout = 5 * time.Second

// platformTaskReadBody reads the closed 16 KiB body under the read deadline.
func platformTaskReadBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(platformTaskBodyTimeout)); err == nil {
		defer controller.SetReadDeadline(time.Time{})
	}
	return io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
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
	// The body is read first, under a read deadline and a size cap, and no slot is held while it is read:
	// a stalled or slow sender costs a connection, never a slot. A body the transport could not deliver
	// (a deadline, a cut connection, over the cap) is answered 400 at once, before any database work and
	// whatever the credential: the answer says nothing about the credential, and the request context is
	// already cancelled by then. An empty body that did arrive is answered after the credential is
	// verified, so a stranger and a retired credential get the same unauthenticated answer whether or not
	// the credential was just remembered.
	raw, readErr := platformTaskReadBody(w, r)
	defer clear(raw)
	if readErr != nil {
		platformTaskFailure(w, errPlatformManagementInput)
		return
	}
	bodyOK := strings.TrimSpace(string(raw)) != ""
	// A credential just refused for its installation's task quota is answered from one live statement
	// (app_platform_task_refusal.go). The lookup is keyed on the digest of the exact credential bytes, so
	// an anonymous caller never finds an entry; anything but a confirmed refusal takes the full path.
	refusalKey := platformTaskRefusalKey(rawIdentity)
	if entry, ok := s.platformTaskRefusals.lookup(refusalKey, r.PathValue("installation")); ok && bodyOK {
		if s.platformTaskBodyQuotaRefused(entry, raw) {
			platformTaskFailure(w, errAppPlatformTaskQuota)
			return
		}
		s.platformTaskRefusals.forget(refusalKey)
	}
	grant, err := s.verifyPlatformServiceIdentity(r.Context(), rawIdentity, "tasks.schedule")
	if err != nil || grant.InstallationID != r.PathValue("installation") {
		platformTaskFailure(w, errAppPlatformServiceUnavailable)
		return
	}
	if !bodyOK {
		platformTaskFailure(w, errPlatformManagementInput)
		return
	}
	// Admission slots are taken only now, for the verified installation: see platformInstallationBudgets.
	// The deferred release covers every exit path below.
	release, ok := s.platformServiceTasks.acquire(grant.InstallationID)
	if !ok {
		platformJSONBusy(w)
		return
	}
	defer release()
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
	if errors.Is(err, errAppPlatformTaskQuota) {
		s.platformTaskRefusals.remember(refusalKey, grant)
	}
	if err != nil {
		platformTaskFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id, "created": created})
}

// platformTaskBodyQuotaRefused applies the cheap quota refusal to a well-formed submission only: a
// body the full path would refuse for its shape is left to it, so the answer for a malformed request
// does not depend on the memory.
func (s *Server) platformTaskBodyQuotaRefused(entry platformTaskRefusalEntry, raw []byte) bool {
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		return false
	}
	in, err := readPlatformTaskAdmission(raw, now)
	if err != nil {
		return false
	}
	// The same defaults the admission applies before it validates.
	if in.RunAtMS == 0 {
		in.RunAtMS = now
	}
	if in.ExpiresMS == 0 {
		in.ExpiresMS = now + (24 * time.Hour).Milliseconds()
	}
	validated, _, err := validatePlatformTaskAdmission(in, now)
	if err != nil {
		return false
	}
	in = validated
	if entry.expiresMS <= now {
		return false // the credential's own expiry, in the clock that stamped it
	}
	return s.platformTaskQuotaRefused(entry, in)
}
