package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const platformManagementBodyLimit = 32 << 10
const platformManagementTimeout = 15 * time.Second

var platformManagementOrigin = http.NewCrossOriginProtection()
var errPlatformManagementInput = errors.New("invalid application management input")
var errPlatformOperationMissing = errors.New("application operation not found")

func (s *Server) platformAdminRoute(mux *http.ServeMux, pattern string, h handler) {
	s.route(mux, pattern, authAdmin, "", platformDocumentHeaders(s.requireAdminJSON(func(w http.ResponseWriter, r *http.Request, user string) {
		if !platformHTTPPath(r) {
			jsonErrorCode(w, 400, "app_platform_invalid_request", "invalid application management request")
			return
		}
		if err := platformManagementOrigin.Check(r); err != nil {
			jsonErrorCode(w, 403, "app_platform_origin_denied", "cross-origin management request denied")
			return
		}
		if r.Method == http.MethodPost {
			kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || kind != "application/json" {
				jsonErrorCode(w, 415, "app_platform_json_required", "application/json required")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), platformManagementTimeout)
		defer cancel()
		h(w, r.WithContext(ctx), user)
	})))
}

// Exact, case-sensitive keys with no duplicates, null object, or trailing value.
// Decode nested objects separately so struct decoding cannot fold key casing.
func platformManagementObject(raw []byte, allowed ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, errPlatformManagementInput
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, errPlatformManagementInput
	}
	out := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, errPlatformManagementInput
		}
		known := false
		for _, name := range allowed {
			known = known || key == name
		}
		if _, duplicate := out[key]; duplicate || !known {
			return nil, errPlatformManagementInput
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, errPlatformManagementInput
		}
		out[key] = value
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return nil, errPlatformManagementInput
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errPlatformManagementInput
	}
	return out, nil
}

func platformManagementString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, exists := fields[key]
	var value string
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return "", errPlatformManagementInput
	}
	return value, nil
}

func platformManagementRead(w http.ResponseWriter, r *http.Request, keys ...string) (map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, platformManagementBodyLimit))
	if err != nil {
		return nil, errPlatformManagementInput
	}
	fields, err := platformManagementObject(raw, keys...)
	if err != nil || len(fields) != len(keys) {
		return nil, errPlatformManagementInput
	}
	return fields, nil
}

func platformManagementAudience(raw json.RawMessage) ([]platformAudienceTarget, error) {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil || len(items) > maxPlatformInstallAudience {
		return nil, errPlatformManagementInput
	}
	result := make([]platformAudienceTarget, 0, len(items))
	for _, item := range items {
		fields, err := platformManagementObject(item, "type", "username", "group_id")
		if err != nil || len(fields) != 2 {
			return nil, errPlatformManagementInput
		}
		kind, err := platformManagementString(fields, "type")
		if err != nil {
			return nil, err
		}
		target := platformAudienceTarget{kind: kind}
		switch kind {
		case "user":
			target.username, err = platformManagementString(fields, "username")
			if target.username == "" || len(target.username) > 320 {
				return nil, errPlatformManagementInput
			}
		case "ou":
			value, parseErr := platformManagementString(fields, "group_id")
			if parseErr != nil || !platformPositiveCounter(value) {
				return nil, errPlatformManagementInput
			}
			target.ouID, err = strconv.ParseInt(value, 10, 64)
		default:
			return nil, errPlatformManagementInput
		}
		if err != nil {
			return nil, errPlatformManagementInput
		}
		result = append(result, target)
	}
	return result, nil
}

func platformManagementError(w http.ResponseWriter, err error) {
	status, code := 503, "app_platform_unavailable"
	switch {
	case errors.Is(err, errPlatformManagementInput):
		status, code = 400, "app_platform_invalid_request"
	case errors.Is(err, errAppPlatformActorUnavailable), errors.Is(err, errAppPlatformLaunchUnavailable):
		status, code = 403, "app_platform_permission_required"
	case errors.Is(err, errAppPlatformPlanExpired):
		status, code = 409, "app_platform_plan_expired"
	case errors.Is(err, errAppPlatformPlanChanged):
		status, code = 409, "app_platform_plan_changed"
	case errors.Is(err, errAppPlatformOperationConflict):
		status, code = 409, "app_platform_operation_conflict"
	case errors.Is(err, errAppPlatformReleaseConflict), errors.Is(err, errAppPlatformTrustConflict):
		status, code = 409, "app_platform_package_untrusted"
	case errors.Is(err, errAppPlatformInstallUnavailable):
		status, code = 409, "app_platform_requirements_unavailable"
	case errors.Is(err, errAppPlatformControlPaused):
		status, code = 503, "app_platform_control_paused"
	case errors.Is(err, errPlatformOperationMissing):
		status, code = 404, "app_platform_operation_missing"
	}
	jsonErrorCode(w, status, code, "application management request could not be completed")
}

// Plan verification/admission inspects the signed archive. Share the document
// compiler's bounded process budget instead of allowing unbounded admin work.
func (s *Server) platformManagementSlot(w http.ResponseWriter) (func(), bool) {
	release, ok := s.platformDocuments.acquire()
	if !ok {
		w.Header().Set("Retry-After", "1")
		jsonErrorCode(w, 429, "app_platform_busy", "application verification is busy")
	}
	return release, ok
}

func (s *Server) apiPlatformInstallPlan(w http.ResponseWriter, r *http.Request, user string) {
	fields, err := platformManagementRead(w, r, "action", "release_sha256", "trust_revision", "audience")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	action, actionErr := platformManagementString(fields, "action")
	release, releaseErr := platformManagementString(fields, "release_sha256")
	revision, revisionErr := platformManagementString(fields, "trust_revision")
	targets, audienceErr := platformManagementAudience(fields["audience"])
	if actionErr != nil || action != "install" || releaseErr != nil || !platformValidSHA(release) || revisionErr != nil || !platformPositiveCounter(revision) || audienceErr != nil {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	releaseSlot, ok := s.platformManagementSlot(w)
	if !ok {
		return
	}
	defer releaseSlot()
	n, _ := strconv.ParseInt(revision, 10, 64)
	review, err := s.createPlatformInstallReviewForFormatContext(r.Context(), actor, release, n, targets, platformFrontendInstallFormat)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var projection platformInstallProjection
	if err := json.Unmarshal([]byte(review.JSON), &projection); err != nil {
		platformManagementError(w, err)
		return
	}
	audience := make([]map[string]string, 0, len(projection.Audience))
	for _, target := range projection.Audience {
		item := map[string]string{"type": target.Type}
		if target.Type == "user" {
			item["username"] = target.Username
		} else {
			item["group_id"] = target.GroupID
		}
		audience = append(audience, item)
	}
	writeJSON(w, map[string]any{"plan_id": review.ID, "plan_sha256": review.SHA256, "expires_ms": strconv.FormatInt(review.ExpiresMS, 10), "review": map[string]any{"app_id": projection.AppID, "publisher_id": projection.PublisherID, "release_sha256": projection.ReleaseSHA256, "trust_revision": revision, "declined_capabilities": projection.DeclinedCapabilities, "data_quota_bytes": "0", "audience": audience}})
}

func (s *Server) apiPlatformInstallCommit(w http.ResponseWriter, r *http.Request, user string) {
	fields, err := platformManagementRead(w, r, "plan_id", "plan_sha256", "idempotency_key")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	id, idErr := platformManagementString(fields, "plan_id")
	sha, shaErr := platformManagementString(fields, "plan_sha256")
	key, keyErr := platformManagementString(fields, "idempotency_key")
	if idErr != nil || shaErr != nil || keyErr != nil || !platformValidUUID(id) || !platformValidSHA(sha) || !platformValidUUID(key) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	releaseSlot, ok := s.platformManagementSlot(w)
	if !ok {
		return
	}
	defer releaseSlot()
	result, err := s.consumePlatformInstallReviewForFormatContext(r.Context(), actor, id, sha, key, platformFrontendInstallFormat)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"operation_id": result.OperationID, "installation_id": result.InstallationID, "generation": strconv.FormatInt(result.Generation, 10), "revision": strconv.FormatInt(result.Revision, 10), "stage": result.Stage, "replayed": result.Replayed})
}

type platformManagementProgress struct {
	OperationID    string             `json:"operation_id"`
	InstallationID string             `json:"installation_id"`
	Action         appplatform.Action `json:"action"`
	Stage          appplatform.Stage  `json:"stage"`
	Generation     string             `json:"generation"`
	Revision       string             `json:"revision"`
	ErrorCode      string             `json:"error_code,omitempty"`
	Terminal       bool               `json:"terminal"`
	ControlPaused  bool               `json:"control_paused"`
}

func (s *Server) readPlatformManagementProgress(ctx context.Context, actor platformActorSession, id string) (platformManagementProgress, error) {
	var result platformManagementProgress
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err = lockOUMutation(e); err != nil {
		return result, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return result, err
	}
	account, err := platformReadActorAccount(e, actor, now, true)
	if err != nil {
		return result, err
	}
	var managed int
	var generation, revision int64
	var code sql.NullString
	var epoch, currentEpoch, paused string
	err = e.queryRow(`SELECT id,installation_id,action,managed,target_generation,state_revision,stage,error_code,controller_epoch,COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),''),COALESCE((SELECT v FROM meta WHERE k='app_platform_restore_paused'),'') FROM app_platform_operations WHERE id=?`, id).Scan(&result.OperationID, &result.InstallationID, &result.Action, &managed, &generation, &revision, &result.Stage, &code, &epoch, &currentEpoch, &paused)
	if errors.Is(err, sql.ErrNoRows) {
		return result, errPlatformOperationMissing
	}
	if err != nil {
		return result, err
	}
	if !platformValidUUID(result.OperationID) || !platformValidUUID(result.InstallationID) || generation < 1 || revision < 1 || (managed != 0 && managed != 1) {
		return result, errAppPlatformOperationConflict
	}
	if _, err = appplatform.OperationFromSnapshot(appplatform.OperationSnapshot{Action: result.Action, Managed: managed == 1, Generation: uint64(generation), Revision: uint64(revision), Stage: result.Stage}); err != nil {
		return result, errAppPlatformOperationConflict
	}
	result.Generation, result.Revision = strconv.FormatInt(generation, 10), strconv.FormatInt(revision, 10)
	result.ControlPaused = paused != "0" || !platformValidUUID(epoch) || epoch != currentEpoch
	result.Terminal = result.Stage == appplatform.StageComplete || result.Stage == appplatform.StageFailed || result.Stage == appplatform.StageRecoveryRequired
	if code.Valid && code.String != "" {
		switch code.String {
		case "install_lease_recovery_required", "install_authority_review_required", "install_execution_review_required":
			result.ErrorCode = code.String
		default:
			result.ErrorCode = "app_platform_operation_failed"
		}
	}
	final, err := platformDatabaseNow(e)
	if err != nil {
		return result, err
	}
	current, err := platformReadActorAccount(e, actor, final, true)
	if err != nil {
		return result, err
	}
	if final < now || current != account {
		return result, errAppPlatformActorUnavailable
	}
	if err = tx.Commit(); err != nil {
		return platformManagementProgress{}, err
	}
	return result, nil
}

func (s *Server) apiPlatformOperationProgress(w http.ResponseWriter, r *http.Request, user string) {
	id := r.PathValue("id")
	if !platformValidUUID(id) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	result, err := s.readPlatformManagementProgress(r.Context(), actor, id)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, result)
}
