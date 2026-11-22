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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

const platformManagementBodyLimit = 32 << 10
const platformManagementTimeout = 15 * time.Second

var platformManagementOrigin = http.NewCrossOriginProtection()
var errPlatformManagementInput = errors.New("invalid application management input")
var errPlatformManagementBusy = errors.New("application management is busy")
var errPlatformOperationMissing = errors.New("application operation not found")

func platformNonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s *Server) platformAdminRoute(mux *http.ServeMux, pattern string, h handler) {
	s.route(mux, pattern, authAdmin, "", platformDocumentHeaders(s.requireAdminJSON(func(w http.ResponseWriter, r *http.Request, user string) {
		listPath := r.Method == "GET" && (r.URL.Path == "/api/admin/app-platform/releases" || r.URL.Path == "/api/admin/app-platform/subjects" || r.URL.Path == "/api/admin/app-platform/credentials" || r.URL.Path == "/api/admin/app-platform/tasks" || r.URL.Path == "/api/admin/app-platform/market/stage" || r.URL.Path == "/api/admin/app-platform/event-subscriptions" || strings.HasPrefix(r.URL.Path, "/api/admin/app-platform/installations/") && strings.HasSuffix(r.URL.Path, "/secret-bindings"))
		if !platformHTTPPath(r) && !(listPath && len(r.URL.RawQuery) <= 2048 && !r.URL.ForceQuery && r.URL.EscapedPath() == r.URL.Path) {
			jsonErrorCode(w, 400, "app_platform_invalid_request", "invalid application management request")
			return
		}
		if err := platformManagementOrigin.Check(r); err != nil {
			jsonErrorCode(w, 403, "app_platform_origin_denied", "cross-origin management request denied")
			return
		}
		if r.Method == http.MethodPost {
			kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			required := "application/json"
			if r.URL.Path == "/api/admin/app-platform/market/offline" {
				required = "application/zip"
			}
			if err != nil || kind != required {
				if required == "application/zip" {
					jsonErrorCode(w, 415, "app_platform_zip_required", "application/zip required")
				} else {
					jsonErrorCode(w, 415, "app_platform_json_required", "application/json required")
				}
				return
			}
		}
		timeout := platformManagementTimeout
		if r.Method == http.MethodPost && r.URL.Path == "/api/admin/app-platform/market/stage" {
			// The synchronous checks are capped at 45s; the wait for the background download comes on top
			// and the whole answer stays well under the 100s a proxy in front allows.
			timeout = platformMarketStageSyncLimit + s.platformStageWait()
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
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

// platformManagementOpenToOUs parses open_to_ous: a list of OU ids as decimal strings, the
// organisational units the installation opens to when it activates. Empty keeps it closed to everyone.
func platformManagementOpenToOUs(raw json.RawMessage) ([]platformAudienceTarget, error) {
	var items []string
	if json.Unmarshal(raw, &items) != nil || items == nil || len(items) > maxPlatformInstallAudience {
		return nil, errPlatformManagementInput
	}
	result := make([]platformAudienceTarget, 0, len(items))
	seen := map[int64]bool{}
	for _, value := range items {
		if !platformPositiveCounter(value) {
			return nil, errPlatformManagementInput
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seen[id] {
			return nil, errPlatformManagementInput
		}
		seen[id] = true
		result = append(result, platformAudienceTarget{kind: "ou", ouID: id})
	}
	return result, nil
}

func platformManagementError(w http.ResponseWriter, err error) {
	status, code := platformManagementStatus(err)
	jsonErrorCode(w, status, code, "application management request could not be completed")
}

// platformManagementStatus maps an error to the public status and code set, so a failure that is reported
// later (an asynchronous job) uses the same codes as one answered inline.
func platformManagementStatus(err error) (int, string) {
	status, code := 503, "app_platform_unavailable"
	if dataStatus, dataCode, ok := platformDataActionError(err); ok {
		return dataStatus, dataCode
	}
	switch {
	case errors.Is(err, errPlatformManagementInput):
		status, code = 400, "app_platform_invalid_request"
	case errors.Is(err, errPlatformResolveKeepDataUnavailable):
		status, code = 409, "app_platform_resolve_unavailable"
	case errors.Is(err, errPlatformUserAudience):
		status, code = 400, "app_platform_user_audience_unsupported"
	case errors.Is(err, errAppPlatformActorUnavailable), errors.Is(err, errAppPlatformLaunchUnavailable):
		status, code = 403, "app_platform_permission_required"
	case errors.Is(err, errAppPlatformPlanExpired):
		status, code = 409, "app_platform_plan_expired"
	case errors.Is(err, errAppPlatformPlanChanged):
		status, code = 409, "app_platform_plan_changed"
	case errors.Is(err, errAppPlatformOperationConflict):
		status, code = 409, "app_platform_operation_conflict"
	case errors.Is(err, errAppPlatformMigrationUnsettled):
		status, code = 409, "app_platform_migration_unsettled"
	case errors.Is(err, errAppPlatformQuotaRaiseUnsettled):
		status, code = 409, "app_platform_quota_raise_unsettled"
	case errors.Is(err, errAppPlatformRecoveryUnsettled):
		status, code = 409, "app_platform_recovery_unsettled"
	case errors.Is(err, errAppPlatformCredentialUnavailable):
		status, code = 409, "app_platform_credential_unavailable"
	case errors.Is(err, errAppPlatformReleaseConflict), errors.Is(err, errAppPlatformTrustConflict):
		status, code = 409, "app_platform_package_untrusted"
	case errors.Is(err, appplatform.ErrInvalidOfflineEnvelope):
		status, code = 400, "app_platform_invalid_request"
	case errors.Is(err, errPlatformImageStoreFull):
		status, code = 507, "app_platform_storage_full"
	case errors.Is(err, errPlatformInstallationCapacity):
		status, code = 429, "installation_capacity"
	case errors.Is(err, appplatform.ErrManagedOfflineNotReady):
		status, code = 409, "app_platform_requirements_unavailable"
	case errors.Is(err, errAppPlatformInstallUnavailable):
		status, code = 409, "app_platform_requirements_unavailable"
	case errors.Is(err, errAppPlatformRuntimeConflict):
		status, code = 503, "app_platform_runtime_unavailable"
	case errors.Is(err, errAppPlatformControlPaused):
		status, code = 503, "app_platform_control_paused"
	case errors.Is(err, errPlatformPrivateTargetNotAllowed):
		status, code = 409, "app_platform_private_target_not_allowed"
	case errors.Is(err, errPlatformCapabilityPolicyBusy):
		status, code = 409, "app_platform_installation_not_ready"
	case errors.Is(err, transport.ErrNetworkTargetsUnsupported), errors.Is(err, transport.ErrProxyUnsupported):
		status, code = 409, "app_platform_runner_unsupported"
	case errors.Is(err, errPlatformOperationMissing):
		status, code = 404, "app_platform_operation_missing"
	case errors.Is(err, errPlatformInstallationMissing):
		status, code = 404, "app_platform_installation_missing"
	case errors.Is(err, errPlatformManagementBusy):
		status, code = 429, "app_platform_busy"
	}
	return status, code
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

func platformRepairManagementProjection(
	review platformRemovalReview, installation string,
) (platformManagedUpdatePlan, error) {
	var projection platformManagedUpdatePlan
	if err := json.Unmarshal(
		[]byte(review.JSON), &projection); err != nil ||
		projection.Action != appplatform.ActionRepair ||
		projection.Format != platformManagedRepairFormat ||
		projection.InstallationID != installation {
		return projection, errAppPlatformPlanChanged
	}
	return projection, nil
}

func platformRepairManagementResponse(
	review platformRemovalReview,
	projection platformManagedUpdatePlan,
) map[string]any {
	return map[string]any{
		"plan_id": review.ID, "plan_sha256": review.SHA256,
		"expires_ms": strconv.FormatInt(review.ExpiresMS, 10),
		"review": map[string]any{
			"action":          "repair",
			"installation_id": projection.InstallationID,
			"app_id":          projection.AppID,
			"publisher_id":    projection.PublisherID,
			"release_sha256":  projection.TargetReleaseSHA256,
			"source_generation": strconv.FormatInt(
				projection.SourceGeneration, 10),
			"target_generation": strconv.FormatInt(
				projection.TargetGeneration, 10),
			"policy_revision": strconv.FormatInt(
				projection.TargetPolicyRevision, 10),
			"grants_sha256": projection.GrantsSHA256,
			"data_effect":   "checkpoint_and_retain",
		},
	}
}

func platformRestoreManagementProjection(
	review platformRemovalReview,
	installation, recoverySetID string,
) (platformManagedRestorePlan, error) {
	var projection platformManagedRestorePlan
	if err := json.Unmarshal(
		[]byte(review.JSON), &projection); err != nil ||
		projection.Format != platformManagedRestoreFormat ||
		projection.InstallationID != installation ||
		projection.RecoverySetID != recoverySetID {
		return projection, errAppPlatformPlanChanged
	}
	return projection, nil
}

func platformRestoreManagementResponse(
	review platformRemovalReview,
	projection platformManagedRestorePlan,
) map[string]any {
	return map[string]any{
		"plan_id": review.ID, "plan_sha256": review.SHA256,
		"expires_ms": strconv.FormatInt(review.ExpiresMS, 10),
		"review": map[string]any{
			"action":                  "restore",
			"installation_id":         projection.InstallationID,
			"recovery_set_id":         projection.RecoverySetID,
			"checkpoint_operation_id": projection.CheckpointOperationID,
			"checkpoint_effect_id":    projection.CheckpointEffectID,
			"checkpoint_id":           projection.CheckpointID,
			"release_sha256":          projection.ReleaseSHA256,
			"manifest_sha256":         projection.ManifestSHA256,
			"archive_bytes":           strconv.FormatInt(projection.ArchiveBytes, 10),
			"source_generation":       strconv.FormatInt(projection.SourceGeneration, 10),
			"target_generation":       strconv.FormatInt(projection.TargetGeneration, 10),
			"data_effect":             "restore_checkpoint",
		},
	}
}

func (s *Server) apiPlatformInstallPlan(w http.ResponseWriter, r *http.Request, user string) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, platformManagementBodyLimit))
	if err != nil {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	fields, err := platformManagementObject(raw, "action",
		"release_sha256", "trust_revision", "audience", "open_to_ous",
		"installation_id", "data_namespace_id", "recovery_set_id", "recovers_operation_id",
		"data_quota_bytes", "data_quota_inodes", "archive_sha256")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	action, actionErr := platformManagementString(fields, "action")
	release, releaseErr := platformManagementString(fields, "release_sha256")
	revision, revisionErr := platformManagementString(fields, "trust_revision")
	if actionErr != nil {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	if action == "install" || action == "update" {
		if releaseErr != nil || !platformValidSHA(release) || revisionErr != nil || !platformPositiveCounter(revision) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
	} else if action != "disable" && action != "uninstall" && action != "enable" &&
		action != "repair" && action != "restore" &&
		action != "set_data_quota" && action != "import_data" &&
		action != "delete_data" && action != "resolve_keep_data" && !platformRecoveryAction(action) {
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
	switch action {
	case "restore_import", "resume", "recovery_disable", "frontend_resume":
		installation, installationErr := platformManagementString(fields, "installation_id")
		recoverySetID, recoveryErr := platformManagementString(fields, "recovery_set_id")
		want := 2
		if action == "restore_import" {
			want = 3
		} else {
			recoverySetID, recoveryErr = "", nil
		}
		if len(fields) != want || installationErr != nil || recoveryErr != nil || !platformValidUUID(installation) ||
			(action == "restore_import" && !platformValidUUID(recoverySetID)) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		s.apiPlatformRecoveryPlan(w, r, actor, action, map[string]string{"installation_id": installation, "recovery_set_id": recoverySetID})
	case "install":
		// The administrator may approve a smaller data quota than the app requested (doc 185); both
		// fields are optional and decimal strings like every other count on this surface.
		approvedBytes, approvedInodes, optional, quotaErr := platformManagementQuotaApproval(fields)
		if fields["audience"] != nil {
			// The per-principal audience of earlier plans is gone (doc 190, D7): say so by code.
			platformManagementError(w, errPlatformUserAudience)
			return
		}
		if quotaErr != nil || len(fields) != 4+optional || fields["open_to_ous"] == nil || fields["installation_id"] != nil {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		reviewCtx := platformWithQuotaApproval(r.Context(), approvedBytes, approvedInodes)
		targets, audienceErr := platformManagementOpenToOUs(fields["open_to_ous"])
		if audienceErr != nil {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		staged, err := s.recheckPlatformStagedReleaseContext(
			r.Context(), actor, release, n)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		var review platformRemovalReview
		if staged.managed {
			endpoint, ok := s.platformRuntimeEndpoint()
			if !ok {
				platformManagementError(w, errAppPlatformRuntimeConflict)
				return
			}
			review, err = s.createPlatformRunnerInstallReview(
				reviewCtx, actor, release, n, targets, endpoint)
		} else {
			review, err = s.createPlatformInstallReviewForFormatContext(
				reviewCtx, actor, release, n, targets,
				platformFrontendInstallFormat)
		}
		if err != nil {
			platformManagementError(w, err)
			return
		}
		var projection platformInstallProjection
		if err := json.Unmarshal([]byte(review.JSON), &projection); err != nil {
			platformManagementError(w, err)
			return
		}
		openToOUs := make([]string, 0, len(projection.Audience))
		for _, target := range projection.Audience {
			openToOUs = append(openToOUs, target.GroupID)
		}
		writeJSON(w, map[string]any{"plan_id": review.ID, "plan_sha256": review.SHA256, "expires_ms": strconv.FormatInt(review.ExpiresMS, 10), "review": map[string]any{"action": "install", "app_id": projection.AppID, "publisher_id": projection.PublisherID, "release_sha256": projection.ReleaseSHA256, "trust_revision": revision, "declined_capabilities": projection.DeclinedCapabilities, "data_quota_bytes": strconv.FormatInt(projection.DataQuotaBytes, 10), "data_quota_inodes": strconv.FormatInt(projection.DataQuotaInodes, 10), "requested_data_quota_bytes": strconv.FormatInt(projection.RequestedDataQuotaBytes, 10), "requested_data_quota_inodes": strconv.FormatInt(projection.RequestedDataQuotaInodes, 10), "open_to_ous": openToOUs}})
	case "update":
		recovers := ""
		want := 4
		if fields["recovers_operation_id"] != nil {
			want = 5
			var recoversErr error
			if recovers, recoversErr = platformManagementString(fields, "recovers_operation_id"); recoversErr != nil || !platformValidUUID(recovers) {
				platformManagementError(w, errPlatformManagementInput)
				return
			}
		}
		if len(fields) != want || fields["installation_id"] == nil || fields["audience"] != nil || fields["open_to_ous"] != nil {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		installation, installationErr := platformManagementString(fields, "installation_id")
		if installationErr != nil || !platformValidUUID(installation) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		review, err := s.createPlatformUpdateReviewRecovering(
			r.Context(), actor, installation, release, n, recovers)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		var projection platformUpdateProjection
		if err := json.Unmarshal([]byte(review.JSON), &projection); err != nil {
			platformManagementError(w, err)
			return
		}
		reviewBody := map[string]any{
			"action": "update", "installation_id": projection.InstallationID, "app_id": projection.AppID, "publisher_id": projection.PublisherID,
			"source_release_sha256": projection.SourceReleaseSHA256, "release_sha256": projection.Target.ReleaseSHA256,
			"source_generation": strconv.FormatInt(projection.SourceGeneration, 10), "target_generation": strconv.FormatInt(projection.TargetGeneration, 10),
			"policy_revision": strconv.FormatInt(projection.PolicyRevision, 10), "target_policy_revision": strconv.FormatInt(projection.TargetPolicyRevision, 10),
			"trust_revision": revision, "declined_capabilities": projection.Target.DeclinedCapabilities,
			"previous_capabilities": platformNonNilStrings(projection.PreviousCapabilities), "target_capabilities": platformNonNilStrings(projection.TargetCapabilities), "declared_data_schema": strconv.FormatInt(projection.CurrentDataFormat, 10),
			"grants_sha256": projection.GrantsSHA256,
		}
		if projection.RecoversOperationID != "" {
			reviewBody["recovers_operation_id"] = projection.RecoversOperationID
		}
		if projection.Migration != nil {
			reviewBody["data_migration"] = map[string]any{
				"from": strconv.FormatInt(projection.Migration.From, 10), "to": strconv.FormatInt(projection.Migration.To, 10),
				"wall_clock_seconds": strconv.FormatInt(projection.Migration.WallClockSeconds, 10),
			}
		}
		if q := projection.DataQuota; q != nil {
			// The quota the update raises before the new release starts, and the runner's answer whether it can hold it.
			quota := map[string]any{
				"current_bytes": strconv.FormatInt(q.CurrentBytes, 10), "current_inodes": strconv.FormatInt(q.CurrentInodes, 10),
				"requested_bytes": strconv.FormatInt(q.RequestedBytes, 10), "requested_inodes": strconv.FormatInt(q.RequestedInodes, 10),
				"new_bytes": strconv.FormatInt(q.NewBytes, 10), "new_inodes": strconv.FormatInt(q.NewInodes, 10),
				"raise_applied": q.RaiseApplied, "capacity": q.Capacity,
			}
			if q.Capacity == "short" {
				quota["capacity_dimension"] = q.CapacityDimension
				quota["capacity_requested"] = strconv.FormatInt(q.CapacityRequested, 10)
				quota["capacity_remaining"] = strconv.FormatInt(q.CapacityRemaining, 10)
			}
			reviewBody["data_quota"] = quota
		}
		writeJSON(w, map[string]any{"plan_id": review.ID, "plan_sha256": review.SHA256, "expires_ms": strconv.FormatInt(review.ExpiresMS, 10), "review": reviewBody})
	case "disable", "uninstall":
		if len(fields) != 2 || fields["installation_id"] == nil {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		installation, installationErr := platformManagementString(fields, "installation_id")
		if installationErr != nil || !platformValidUUID(installation) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		removalAction := appplatform.ActionDisable
		if action == "uninstall" {
			removalAction = appplatform.ActionUninstall
		}
		review, err := s.createPlatformRemovalReviewContext(r.Context(), actor, installation, removalAction)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		var projection platformRemovalProjection
		if err := json.Unmarshal([]byte(review.JSON), &projection); err != nil {
			platformManagementError(w, err)
			return
		}
		// An installation with no active activation (a failed first install) is reviewed for the release it was
		// installed from.
		releaseSHA := projection.SourceReleaseSHA256
		if releaseSHA == "" && projection.NeverStarted != nil {
			releaseSHA = projection.NeverStarted.ReleaseSHA256
		}
		writeJSON(w, map[string]any{"plan_id": review.ID, "plan_sha256": review.SHA256, "expires_ms": strconv.FormatInt(review.ExpiresMS, 10), "review": map[string]any{
			"action": action, "installation_id": projection.InstallationID, "app_id": projection.AppID, "publisher_id": projection.PublisherID,
			"release_sha256": releaseSHA, "generation": strconv.FormatInt(projection.Generation, 10), "target_generation": strconv.FormatInt(projection.TargetGeneration, 10),
			"managed": projection.Managed, "data_effect": projection.DataEffect, "plugin_data_cleared": action == "uninstall", "managed_data_retained": action == "disable", "managed_data_cleared": action == "uninstall" && projection.Managed && projection.Data != nil,
		}})
	case "delete_data":
		if len(fields) != 3 || fields["installation_id"] == nil ||
			fields["data_namespace_id"] == nil {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		installation, installationErr := platformManagementString(
			fields, "installation_id")
		namespaceID, namespaceErr := platformManagementString(
			fields, "data_namespace_id")
		if installationErr != nil || namespaceErr != nil ||
			!platformValidUUID(installation) ||
			!platformValidUUID(namespaceID) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		review, err := s.createPlatformDeleteDataReview(
			r.Context(), actor, installation, namespaceID)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		var projection platformDeleteDataReviewProjection
		if err := json.Unmarshal(
			[]byte(review.JSON), &projection); err != nil {
			platformManagementError(w, err)
			return
		}
		writeJSON(w, map[string]any{
			"plan_id": review.ID, "plan_sha256": review.SHA256,
			"expires_ms": strconv.FormatInt(review.ExpiresMS, 10),
			"review": map[string]any{
				"action":            "delete_data",
				"installation_id":   projection.InstallationID,
				"app_id":            projection.AppID,
				"publisher_id":      projection.PublisherID,
				"data_namespace_id": projection.Data.ID,
				"release_sha256":    projection.ReleaseSHA256,
				"source_generation": strconv.FormatInt(
					projection.Generation, 10),
				"target_generation": strconv.FormatInt(
					projection.TargetGeneration, 10),
				"data_format": strconv.FormatInt(
					projection.Data.Format, 10),
				"writer_fence": strconv.FormatInt(
					projection.Data.WriterFence, 10),
				"scope": "exact namespace and recorded recovery sets",
			},
		})
	case "enable":
		if len(fields) != 2 || fields["installation_id"] == nil {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		installation, installationErr := platformManagementString(fields, "installation_id")
		if installationErr != nil || !platformValidUUID(installation) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		review, err := s.createPlatformFrontendEnableReviewContext(
			r.Context(), actor, installation)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		var projection platformFrontendEnablePlan
		if err := json.Unmarshal([]byte(review.JSON), &projection); err != nil {
			platformManagementError(w, err)
			return
		}
		writeJSON(w, map[string]any{
			"plan_id": review.ID, "plan_sha256": review.SHA256,
			"expires_ms": strconv.FormatInt(review.ExpiresMS, 10),
			"review": map[string]any{
				"action": "enable", "installation_id": projection.InstallationID,
				"app_id": projection.AppID, "publisher_id": projection.PublisherID,
				"release_sha256":    projection.SourceReleaseSHA256,
				"source_generation": strconv.FormatInt(projection.SourceGeneration, 10),
				"target_generation": strconv.FormatInt(projection.TargetGeneration, 10),
				"policy_revision":   strconv.FormatInt(projection.TargetPolicyRevision, 10),
				"grants_sha256":     projection.GrantsSHA256,
				"data_effect":       projection.DataEffect,
			},
		})
	case "repair":
		if len(fields) != 2 || fields["installation_id"] == nil {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		installation, installationErr := platformManagementString(
			fields, "installation_id")
		if installationErr != nil || !platformValidUUID(installation) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		endpoint, ok := s.platformRuntimeEndpoint()
		if !ok {
			platformManagementError(w, errAppPlatformRuntimeConflict)
			return
		}
		review, err := s.createPlatformManagedRepairReviewContext(
			r.Context(), actor, installation, endpoint)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		projection, projectionErr :=
			platformRepairManagementProjection(review, installation)
		if projectionErr != nil {
			platformManagementError(w, projectionErr)
			return
		}
		writeJSON(w, platformRepairManagementResponse(review, projection))
	case "resolve_keep_data":
		installation, installationErr := platformManagementString(fields, "installation_id")
		if len(fields) != 2 || installationErr != nil || !platformValidUUID(installation) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		endpoint, ok := s.platformRuntimeEndpoint()
		if !ok {
			platformManagementError(w, errAppPlatformRuntimeConflict)
			return
		}
		review, err := s.createPlatformResolveKeepDataReview(r.Context(), actor, installation, endpoint)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		var projection platformResolveKeepDataPlan
		if json.Unmarshal([]byte(review.JSON), &projection) != nil || projection.Format != platformResolveKeepDataFormat || projection.InstallationID != installation {
			platformManagementError(w, errAppPlatformPlanChanged)
			return
		}
		writeJSON(w, platformResolveKeepDataManagementResponse(review, projection))
	case "set_data_quota":
		installation, installationErr := platformManagementString(fields, "installation_id")
		bytesText, bytesErr := platformManagementString(fields, "data_quota_bytes")
		inodesText, inodesErr := platformManagementString(fields, "data_quota_inodes")
		if len(fields) != 4 || installationErr != nil || bytesErr != nil || inodesErr != nil || !platformValidUUID(installation) ||
			!platformPositiveCounter(bytesText) || !platformPositiveCounter(inodesText) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		newBytes, bytesParseErr := strconv.ParseInt(bytesText, 10, 64)
		newInodes, inodesParseErr := strconv.ParseInt(inodesText, 10, 64)
		if bytesParseErr != nil || inodesParseErr != nil {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		endpoint, ok := s.platformRuntimeEndpoint()
		if !ok {
			platformManagementError(w, errAppPlatformRuntimeConflict)
			return
		}
		review, err := s.createPlatformSetDataQuotaReview(r.Context(), actor, installation, newBytes, newInodes, endpoint)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		var projection platformDataQuotaPlan
		if json.Unmarshal([]byte(review.JSON), &projection) != nil || projection.Format != platformSetDataQuotaFormat || projection.InstallationID != installation {
			platformManagementError(w, errAppPlatformPlanChanged)
			return
		}
		writeJSON(w, platformDataQuotaManagementResponse(review, projection))
	case "import_data":
		installation, installationErr := platformManagementString(fields, "installation_id")
		archive, archiveErr := platformManagementString(fields, "archive_sha256")
		if len(fields) != 3 || installationErr != nil || archiveErr != nil || !platformValidUUID(installation) || !platformValidSHA(archive) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		endpoint, ok := s.platformRuntimeEndpoint()
		if !ok {
			platformManagementError(w, errAppPlatformRuntimeConflict)
			return
		}
		review, err := s.createPlatformManagedImportDataReview(r.Context(), actor, installation, archive, endpoint)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		var projection platformManagedUpdatePlan
		if json.Unmarshal([]byte(review.JSON), &projection) != nil || projection.Format != platformManagedImportDataFormat ||
			projection.InstallationID != installation || projection.Import == nil {
			platformManagementError(w, errAppPlatformPlanChanged)
			return
		}
		writeJSON(w, platformImportDataManagementResponse(review, projection))
	case "restore":
		if len(fields) != 3 || fields["installation_id"] == nil ||
			fields["recovery_set_id"] == nil {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		installation, installationErr := platformManagementString(
			fields, "installation_id")
		recoverySetID, recoveryErr := platformManagementString(
			fields, "recovery_set_id")
		if installationErr != nil || recoveryErr != nil ||
			!platformValidUUID(installation) ||
			!platformValidUUID(recoverySetID) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		review, err := s.createPlatformManagedRestoreReview(
			r.Context(), actor, installation, recoverySetID)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		projection, projectionErr := platformRestoreManagementProjection(
			review, installation, recoverySetID)
		if projectionErr != nil {
			platformManagementError(w, projectionErr)
			return
		}
		writeJSON(w, platformRestoreManagementResponse(review, projection))
	default:
		platformManagementError(w, errPlatformManagementInput)
	}
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
	var action, planRaw string
	if err := s.st.queryRowContext(r.Context(), `SELECT action,plan_json FROM app_platform_review_plans WHERE id=? AND username=? AND account_created=? AND account_generation=?`, id, actor.username, actor.created, actor.generation).Scan(&action, &planRaw); err != nil {
		platformManagementError(w, errAppPlatformPlanChanged)
		return
	}
	var result platformRemovalCommit
	var recovery bool
	if platformPlanIsResolveKeepData(planRaw) {
		// Accepting the data a failed import left is one database transition and no operation: it answers at once.
		endpoint, ok := s.platformRuntimeEndpoint()
		if !ok {
			platformManagementError(w, errAppPlatformRuntimeConflict)
			return
		}
		resolved, err := s.consumePlatformResolveKeepDataReview(r.Context(), actor, id, sha, key, endpoint)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		writeJSON(w, map[string]any{"resolved": true, "operation_id": resolved.OperationID, "installation_id": resolved.InstallationID,
			"abandoned_effects": strconv.Itoa(resolved.Abandoned)})
		return
	}
	switch action {
	case "install":
		format, formatErr := platformParseInstallProfile(planRaw)
		if formatErr != nil {
			err = formatErr
			break
		}
		if format == platformManagedInstallFormat {
			// No executor runs this format; admitting it would queue an install forever. Plan it again.
			err = errAppPlatformPlanChanged
		} else if format == platformWritableInstallFormat {
			endpoint, ok := s.platformRuntimeEndpoint()
			if !ok {
				err = errAppPlatformRuntimeConflict
				break
			}
			result, err = s.consumePlatformWritableInstallReview(
				r.Context(), actor, id, sha, key, endpoint)
		} else {
			result, err = s.consumePlatformInstallReviewForFormatContext(
				r.Context(), actor, id, sha, key,
				platformFrontendInstallFormat)
		}
	case "update":
		result, err = s.consumePlatformUpdateReviewContext(
			r.Context(), actor, id, sha, key)
	case "disable", "uninstall":
		if platformPlanIsRecoveryDisable(planRaw) {
			recovery = true
			result, err = s.consumePlatformRecoveryReview(r.Context(), actor, platformRecoveryDisableKind, id, sha, key)
			break
		}
		result, err = s.consumePlatformRemovalReviewContext(r.Context(), actor, id, sha, key)
	case "delete_data":
		result, err = s.consumePlatformDeleteDataReviewContext(
			r.Context(), actor, id, sha, key)
	case "enable":
		if platformPlanIsFrontendResume(planRaw) {
			recovery = true
			result, err = s.consumePlatformRecoveryReview(r.Context(), actor, platformFrontendResumeKind, id, sha, key)
			break
		}
		result, err = s.consumePlatformFrontendEnableReviewContext(
			r.Context(), actor, id, sha, key)
	case "set_data_quota":
		endpoint, ok := s.platformRuntimeEndpoint()
		if !ok {
			err = errAppPlatformRuntimeConflict
			break
		}
		result, err = s.consumePlatformSetDataQuotaReviewContext(
			r.Context(), actor, id, sha, key, endpoint)
	case "import_data":
		endpoint, ok := s.platformRuntimeEndpoint()
		if !ok {
			err = errAppPlatformRuntimeConflict
			break
		}
		result, err = s.consumePlatformManagedImportDataReviewContext(
			r.Context(), actor, id, sha, key, endpoint)
	case "repair":
		endpoint, ok := s.platformRuntimeEndpoint()
		if !ok {
			err = errAppPlatformRuntimeConflict
			break
		}
		result, err = s.consumePlatformManagedRepairReviewContext(
			r.Context(), actor, id, sha, key, endpoint)
	case "restore":
		if platformPlanIsResume(planRaw) {
			recovery = true
			result, err = s.consumePlatformRestoreResumeReview(
				r.Context(), actor, id, sha, key)
			break
		}
		result, err = s.consumePlatformManagedRestoreReview(
			r.Context(), actor, id, sha, key)
	case "restore_import":
		recovery = true
		result, err = s.consumePlatformRestoreImportReview(
			r.Context(), actor, id, sha, key)
	default:
		err = errAppPlatformPlanChanged
	}
	if err != nil {
		if recovery {
			platformRecoveryError(w, err)
			return
		}
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
		result.ErrorCode = platformPublicOperationErrorCode(code.String)
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

func platformDataQuotaManagementResponse(review platformRemovalReview, p platformDataQuotaPlan) map[string]any {
	item := map[string]any{
		"action": "set_data_quota", "installation_id": p.InstallationID, "app_id": p.AppID, "publisher_id": p.PublisherID,
		"current_data_quota_bytes":  strconv.FormatInt(p.CurrentQuotaBytes, 10),
		"current_data_quota_inodes": strconv.FormatInt(p.CurrentQuotaInodes, 10),
		"data_quota_bytes":          strconv.FormatInt(p.NewQuotaBytes, 10),
		"data_quota_inodes":         strconv.FormatInt(p.NewQuotaInodes, 10),
		"observed_bytes":            strconv.FormatInt(p.ObservedBytes, 10),
		"capacity":                  p.Capacity,
	}
	if p.Capacity != "fits" {
		item["capacity_dimension"] = p.CapacityDimension
		item["capacity_requested"] = strconv.FormatInt(p.CapacityRequested, 10)
		item["capacity_remaining"] = strconv.FormatInt(p.CapacityRemaining, 10)
	}
	return map[string]any{"plan_id": review.ID, "plan_sha256": review.SHA256, "expires_ms": strconv.FormatInt(review.ExpiresMS, 10), "review": item}
}

func platformResolveKeepDataManagementResponse(review platformRemovalReview, p platformResolveKeepDataPlan) map[string]any {
	effects := make([]map[string]any, 0, len(p.AbandonedEffects))
	for _, f := range p.AbandonedEffects {
		effects = append(effects, map[string]any{"operation_id": f.OperationID, "effect_id": f.EffectID, "stage": f.Stage, "kind": f.Kind, "recovery_set_id": f.RecoverySetID})
	}
	return map[string]any{
		"plan_id": review.ID, "plan_sha256": review.SHA256, "expires_ms": strconv.FormatInt(review.ExpiresMS, 10),
		"review": map[string]any{
			"action": "resolve_keep_data", "installation_id": p.InstallationID, "app_id": p.AppID, "publisher_id": p.PublisherID,
			"operation_id": p.OperationID, "operation_action": p.OperationAction, "operation_error_code": p.OperationErrorCode,
			"release_sha256": p.ReleaseSHA256, "observed_bytes": strconv.FormatInt(p.ObservedBytes, 10),
			"checkpoint_recovery_set_id": p.CheckpointRecoverySt, "abandoned_effects": effects,
			"keeps_data": true, "keeps_checkpoint": p.CheckpointRecoverySt != "", "runs_rollback": false,
		},
	}
}

func platformImportDataManagementResponse(review platformRemovalReview, p platformManagedUpdatePlan) map[string]any {
	step := p.Import
	return map[string]any{
		"plan_id": review.ID, "plan_sha256": review.SHA256, "expires_ms": strconv.FormatInt(review.ExpiresMS, 10),
		"review": map[string]any{
			"action": "import_data", "installation_id": p.InstallationID, "app_id": p.AppID, "publisher_id": p.PublisherID,
			"archive_sha256": step.ArchiveSHA256, "archive_bytes": strconv.FormatInt(step.ArchiveBytes, 10),
			"archive_entries": strconv.FormatInt(step.Entries, 10), "content_bytes": strconv.FormatInt(step.ContentBytes, 10),
			"archive_data_format": strconv.FormatInt(step.DataFormat, 10), "release_data_format": strconv.FormatInt(p.DataFormat, 10),
			"data_quota_bytes": strconv.FormatInt(step.QuotaBytes, 10), "data_quota_inodes": strconv.FormatInt(step.QuotaInodes, 10),
			"replaces_contents": true, "keeps_checkpoint": true, "requires_disabled": true,
		},
	}
}
