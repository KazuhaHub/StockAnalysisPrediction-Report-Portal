package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
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

// platformTaskSlotHook is a test seam called once a request holds its installation's slot; it is a no-op in production.
var platformTaskSlotHook = func() {}

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
	// Nothing below may let an unauthenticated caller make the Portal do database work, or hold what a
	// backend needs. A credential is first checked in memory (app_platform_service_material.go): one that
	// is malformed, or names another installation than the path, or carries a MAC that is wrong for an
	// installation whose verification material is warm, is refused 404 with no database access at all.
	installation := r.PathValue("installation")
	if named, ok := platformServiceCredentialInstallation(rawIdentity); !ok || named != installation {
		platformTaskFailure(w, errAppPlatformServiceUnavailable)
		return
	}
	known, authentic := s.platformServiceMaterial.authentic(installation, rawIdentity)
	if known && !authentic {
		platformTaskFailure(w, errAppPlatformServiceUnavailable)
		return
	}
	refusalKey := platformTaskRefusalKey(rawIdentity)
	var grant appplatform.ServiceIdentityGrant
	var release func()
	if known {
		// The MAC is valid: this is a holder of a real credential, and its database work for the
		// installation, the remembered refusal included, runs inside the installation's own slots, so one
		// tenant's parallel connections can never take more of the shared pool than that.
		var ok bool
		if release, ok = s.platformServiceTasks.acquire(installation); !ok {
			platformJSONBusy(w)
			return
		}
		defer release()
		platformTaskSlotHook()
		// A credential just refused for its installation's task quota is answered from a few live
		// statements (app_platform_task_refusal.go); anything but a confirmed refusal takes the full path.
		if entry, ok := s.platformTaskRefusals.lookup(refusalKey, installation); ok && bodyOK {
			if s.platformTaskBodyQuotaRefused(entry, raw) {
				platformTaskFailure(w, errAppPlatformTaskQuota)
				return
			}
			s.platformTaskRefusals.forget(refusalKey)
		}
		var seed []byte
		var err error
		if grant, seed, err = s.verifyPlatformServiceIdentityWithSeed(r.Context(), rawIdentity, "tasks.schedule"); err != nil || grant.InstallationID != installation {
			clear(seed)
			s.platformServiceMaterial.forget(installation)
			platformTaskFailure(w, errAppPlatformServiceUnavailable)
			return
		}
		s.platformServiceMaterial.remember(installation, seed)
		clear(seed)
	} else {
		// No material: the full verification is the only way to authenticate, and it runs under a small
		// Portal-wide limit that never waits, so an unauthenticated flood can delay other cold verifications
		// but never a backend whose material is warm or the rest of the Portal's connections.
		releaseUnverified, ok := s.platformUnverified.acquire()
		if !ok {
			platformJSONBusy(w)
			return
		}
		g, seed, err := s.verifyPlatformServiceIdentityWithSeed(r.Context(), rawIdentity, "tasks.schedule")
		releaseUnverified()
		if err != nil || g.InstallationID != installation {
			clear(seed)
			platformTaskFailure(w, errAppPlatformServiceUnavailable)
			return
		}
		grant = g
		s.platformServiceMaterial.remember(installation, seed)
		clear(seed)
		// The installation's slot is taken after verification, for the verified installation: an
		// unverified caller never holds one.
		if release, ok = s.platformServiceTasks.acquire(installation); !ok {
			platformJSONBusy(w)
			return
		}
		defer release()
		platformTaskSlotHook()
	}
	if !bodyOK {
		platformTaskFailure(w, errPlatformManagementInput)
		return
	}
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
