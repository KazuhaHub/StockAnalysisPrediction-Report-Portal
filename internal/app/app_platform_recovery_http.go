package app

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// The HTTP surface of fresh-host recovery (doc 168). Three steps, all on the paused Portal and all
// administrator-only:
//
//   - replacement runtime enrollment, a prepare and a confirm on one route;
//   - restore_import and resume, each reviewed on the ordinary plans route and committed on the
//     ordinary operations route, so they share the plan, expiry, idempotency and audit machinery of
//     every other lifecycle action.
//
// Ending recovery is its own route (apiPlatformEndRecovery). None of these accepts runner evidence
// from a request: enrollment reads the runner's own report, and the resume target is read from the
// replacement runner, never from a body.

var errAppPlatformRecoveryInactive = errors.New("application recovery is not active")

// platformRecoveryError is platformManagementError for the recovery actions. On these routes a
// control pause that is not the recovery pause means the Portal is not in recovery at all, which is
// a different thing to tell an administrator than "paused pending recovery review". A runtime that
// cannot be enrolled or confirmed is likewise its own refusal rather than a transient outage.
func platformRecoveryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAppPlatformControlPaused):
		jsonErrorCode(w, 409, "app_platform_recovery_inactive", "application recovery is not active")
	default:
		platformManagementError(w, err)
	}
}

func platformReplacementResponse(r platformRuntimeRegistration, enabled bool) map[string]any {
	return map[string]any{
		"runtime_id": r.id, "deployment_id": r.deploymentID, "controller_epoch": r.controllerEpoch,
		"control_revision": strconv.FormatInt(r.controlRevision, 10), "revision": strconv.FormatInt(r.revision, 10),
		"verification_key_fingerprint": r.fingerprint, "public_key": hex.EncodeToString(r.publicKey), "enabled": enabled,
	}
}

// apiPlatformReplacementRuntime enrolls the runtime of a fresh host. "prepare" creates the pending
// binding and returns the controller public key the runner is configured with; "confirm" observes the
// runner's enrollment through the fixed endpoint and enables the binding. Both are idempotent while
// the binding is pending, and neither is admitted while any other binding is live.
func (s *Server) apiPlatformReplacementRuntime(w http.ResponseWriter, r *http.Request, user string) {
	fields, err := platformManagementRead(w, r, "action", "deployment_id")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	action, actionErr := platformManagementString(fields, "action")
	deployment, deploymentErr := platformManagementString(fields, "deployment_id")
	if actionErr != nil || deploymentErr != nil || (action != "prepare" && action != "confirm") || !platformValidUUID(deployment) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	registration, err := s.preparePlatformReplacementRuntime(actor, deployment)
	if err != nil {
		s.platformReplacementError(w, err)
		return
	}
	if action == "prepare" {
		writeJSON(w, platformReplacementResponse(registration, false))
		return
	}
	endpoint, ok := s.platformRuntimeEndpoint()
	if !ok {
		platformManagementError(w, errAppPlatformRuntimeConflict)
		return
	}
	confirmed, err := s.confirmPlatformReplacementRuntime(r.Context(), actor, registration, endpoint)
	if err != nil {
		s.platformReplacementError(w, err)
		return
	}
	writeJSON(w, platformReplacementResponse(confirmed, true))
}

func (s *Server) platformReplacementError(w http.ResponseWriter, err error) {
	if errors.Is(err, errAppPlatformRuntimeConflict) {
		jsonErrorCode(w, 409, "app_platform_replacement_unavailable", "replacement runtime cannot be enrolled")
		return
	}
	platformRecoveryError(w, err)
}

func platformRestoreImportResponse(review platformRemovalReview) (map[string]any, error) {
	var plan platformRestoreImportPlan
	if err := json.Unmarshal([]byte(review.JSON), &plan); err != nil || plan.Format != platformRestoreImportFormat {
		return nil, errAppPlatformPlanChanged
	}
	return map[string]any{
		"plan_id": review.ID, "plan_sha256": review.SHA256, "expires_ms": strconv.FormatInt(review.ExpiresMS, 10),
		"review": map[string]any{
			"action": "restore_import", "installation_id": plan.InstallationID, "recovery_set_id": plan.RecoverySetID,
			"checkpoint_id": plan.CheckpointID, "release_sha256": plan.ReleaseSHA256, "manifest_sha256": plan.ManifestSHA256,
			"archive_bytes": strconv.FormatInt(plan.ArchiveBytes, 10), "source_runtime_id": plan.SourceRuntimeID,
			"runtime_id": plan.RuntimeID, "deployment_id": plan.DeploymentID,
			"source_generation": strconv.FormatInt(plan.SourceGeneration, 10), "target_generation": strconv.FormatInt(plan.TargetGeneration, 10),
			"data_effect": "import_checkpoint",
		},
	}, nil
}

func platformRestoreResumeResponse(review platformRemovalReview) (map[string]any, error) {
	var plan platformRestoreResumePlan
	if err := json.Unmarshal([]byte(review.JSON), &plan); err != nil || plan.Format != platformRestoreResumeFormat {
		return nil, errAppPlatformPlanChanged
	}
	return map[string]any{
		"plan_id": review.ID, "plan_sha256": review.SHA256, "expires_ms": strconv.FormatInt(review.ExpiresMS, 10),
		"review": map[string]any{
			"action": "resume", "installation_id": plan.InstallationID, "recovery_set_id": plan.RecoverySetID,
			"import_operation_id": plan.ImportOperationID, "release_sha256": plan.ReleaseSHA256,
			"runtime_id": plan.RuntimeID, "deployment_id": plan.DeploymentID,
			"source_generation": strconv.FormatInt(plan.SourceGeneration, 10), "target_generation": strconv.FormatInt(plan.TargetGeneration, 10),
			"data_effect": "none",
		},
	}, nil
}

// platformPlanIsResume reports whether a stored review plan is a resume. A resume is reviewed and
// committed as a restore, so the plan's own format is what tells the two apart.
func platformPlanIsResume(planRaw string) bool {
	return strings.HasPrefix(planRaw, `{"format":"`+platformRestoreResumeFormat+`"`)
}

// platformRecoveryAction reports whether a plan action belongs to the recovery surface.
func platformRecoveryAction(action string) bool {
	return action == "restore_import" || action == "resume" || action == "recovery_disable" || action == "frontend_resume"
}

func platformRecoveryActionResponse(action string, review platformRemovalReview) (map[string]any, error) {
	var plan struct {
		Format              string `json:"format"`
		InstallationID      string `json:"installation_id"`
		RuntimeID           string `json:"runtime_id"`
		SourceReleaseSHA256 string `json:"source_release_sha256"`
		SourceGeneration    int64  `json:"source_generation"`
		TargetGeneration    int64  `json:"target_generation"`
		DataEffect          string `json:"data_effect"`
	}
	want := platformRecoveryDisableFormat
	if action == "frontend_resume" {
		want = platformFrontendResumeFormat
	}
	if err := json.Unmarshal([]byte(review.JSON), &plan); err != nil || plan.Format != want {
		return nil, errAppPlatformPlanChanged
	}
	reviewed := map[string]any{
		"action": action, "installation_id": plan.InstallationID,
		"source_generation": strconv.FormatInt(plan.SourceGeneration, 10), "target_generation": strconv.FormatInt(plan.TargetGeneration, 10),
		"data_effect": plan.DataEffect,
	}
	if action == "frontend_resume" {
		reviewed["release_sha256"] = plan.SourceReleaseSHA256
	} else {
		reviewed["runtime_id"] = plan.RuntimeID
	}
	return map[string]any{
		"plan_id": review.ID, "plan_sha256": review.SHA256, "expires_ms": strconv.FormatInt(review.ExpiresMS, 10), "review": reviewed,
	}, nil
}

func (s *Server) apiPlatformRecoveryPlan(w http.ResponseWriter, r *http.Request, actor platformActorSession, action string, fields map[string]string) {
	var (
		review   platformRemovalReview
		response map[string]any
		err      error
	)
	switch action {
	case "restore_import":
		review, err = s.createPlatformRestoreImportReview(r.Context(), actor, fields["installation_id"], fields["recovery_set_id"])
		if err == nil {
			response, err = platformRestoreImportResponse(review)
		}
	case "recovery_disable", "frontend_resume":
		kind := platformRecoveryDisableKind
		if action == "frontend_resume" {
			kind = platformFrontendResumeKind
		}
		review, err = s.createPlatformRecoveryReview(r.Context(), actor, kind, fields["installation_id"])
		if err == nil {
			response, err = platformRecoveryActionResponse(action, review)
		}
	case "resume":
		endpoint, ok := s.platformRuntimeEndpoint()
		if !ok {
			platformManagementError(w, errAppPlatformRuntimeConflict)
			return
		}
		var target *platformManagedInstallTarget
		if target, err = s.platformRecoveryResumeTarget(r.Context(), actor, endpoint); err == nil {
			review, err = s.createPlatformRestoreResumeReview(r.Context(), actor, fields["installation_id"], target)
		}
		if err == nil {
			response, err = platformRestoreResumeResponse(review)
		}
	}
	if err != nil {
		platformRecoveryError(w, err)
		return
	}
	writeJSON(w, response)
}
