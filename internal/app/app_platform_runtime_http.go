package app

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/service"
)

// The console surface of a fresh host's first runtime enrollment (handbook section 4.7, doc 187
// section 8.2). The status read shows where the deployment stands; "prepare" creates the pending
// binding and returns the public enrollment document host-setup reads; "confirm" observes the
// runner through Portal's fixed endpoint and enables the binding. Both actions run only in the
// ordinary, non-recovery transaction: a restored Portal enrolls its runtime through
// control/replacement-runtime instead. Neither accepts runner evidence from a request.

// platformRuntimeStatus is the status read. It makes no runner call.
func (s *Server) apiPlatformRuntimeStatus(w http.ResponseWriter, r *http.Request, user string) {
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	directory, err := s.readPlatformPublisherDirectory(r.Context(), actor)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	_, configured := s.platformRuntimeEndpoint()
	out := map[string]any{"endpoint_configured": configured, "runtime": nil}
	switch {
	case directory.Paused:
		out["state"] = "paused"
	case !directory.Initialized:
		out["state"] = "uninitialized"
	default:
		pool := migExec{s: s.st, ex: platformContextPool{DB: s.st.db, ctx: r.Context()}}
		row, found, retired, err := platformActiveRuntimeLocked(pool, "")
		if err != nil && !errors.Is(err, errAppPlatformRuntimeConflict) {
			platformManagementError(w, err)
			return
		}
		out["state"] = platformInitialRuntimeState(row, found, retired, err, s.st.GetSetting("app_platform_controller_epoch", ""))
		if err == nil && found {
			out["runtime"] = map[string]any{
				"runtime_id": row.id, "deployment_id": row.deploymentID, "controller_epoch": row.controllerEpoch,
				"verification_key_fingerprint": row.pin.String, "enabled": row.enabled == 1,
			}
		}
	}
	writeJSON(w, out)
}

// platformInitialRuntimeState classifies the runtime binding table for the first-enrollment surface.
// Retired rows mean a restore happened: that deployment's runtime is enrolled through the recovery
// procedure, never here, so they are a state for review rather than "none". A row this surface
// cannot carry forward (another epoch, a cleared key, a later revision) is likewise "conflict".
func platformInitialRuntimeState(row platformRuntimeRow, found bool, retired []string, err error, epoch string) string {
	switch {
	case err != nil || len(retired) > 0:
		return "conflict"
	case !found:
		return "none"
	case row.controllerEpoch != epoch || row.controlRevision != 1 || !row.pin.Valid || !platformValidSHA(row.pin.String):
		return "conflict"
	case row.enabled == 0 && row.revision == 1 && row.sealed.Valid:
		return "pending"
	case row.enabled == 1 && row.revision == 2:
		return "confirmed"
	}
	return "conflict"
}

// platformInitialRuntime reads the binding in force inside the ordinary trust transaction, so it is
// refused on an uninitialized or paused (recovering) Portal exactly as prepare and confirm are.
func (s *Server) platformInitialRuntime(actor platformActorSession) (platformRuntimeRow, string, error) {
	var row platformRuntimeRow
	state := ""
	err := s.withPlatformTrustTransaction(actor, func(e migExec, facts platformActorFacts, control platformActorControl) error {
		current, found, retired, err := platformActiveRuntime(e, false)
		if err != nil && !errors.Is(err, errAppPlatformRuntimeConflict) {
			return err
		}
		row, state = current, platformInitialRuntimeState(current, found, retired, err, control.epoch)
		_, err = platformRefreshActor(e, actor, facts, control)
		return err
	})
	return row, state, err
}

func platformRuntimeError(w http.ResponseWriter, err error, code string) {
	switch {
	case errors.Is(err, errAppPlatformRuntimeConflict):
		jsonErrorCode(w, 409, code, "application runtime binding is not in a state this action accepts")
	case errors.Is(err, errAppPlatformRuntimeNotObserved):
		jsonErrorCode(w, 409, "app_platform_runtime_not_observed", "application runner reported no matching enrollment")
	default:
		platformManagementError(w, err)
	}
}

// apiPlatformRuntimeControl prepares or confirms the first runtime binding. Prepare takes no
// deployment: Portal mints one when there is no binding, and reuses the pending one otherwise, so a
// repeated prepare hands out the same document. Confirm names the deployment it confirms.
func (s *Server) apiPlatformRuntimeControl(w http.ResponseWriter, r *http.Request, user string) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, platformManagementBodyLimit))
	if err != nil {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	// Prepare carries only its action; confirm also names the deployment.
	fields, err := platformManagementObject(raw, "action", "deployment_id")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	action, actionErr := platformManagementString(fields, "action")
	_, hasDeployment := fields["deployment_id"]
	deployment, deploymentErr := "", error(nil)
	if hasDeployment {
		deployment, deploymentErr = platformManagementString(fields, "deployment_id")
	}
	valid := actionErr == nil && deploymentErr == nil &&
		(action == "prepare" && !hasDeployment || action == "confirm" && platformValidUUID(deployment))
	if !valid {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	if action == "prepare" {
		s.platformRuntimePrepare(w, r.Context(), actor)
		return
	}
	s.platformRuntimeConfirm(w, r.Context(), actor, deployment)
}

func (s *Server) platformRuntimePrepare(w http.ResponseWriter, ctx context.Context, actor platformActorSession) {
	row, state, err := s.platformInitialRuntime(actor)
	if err != nil {
		platformRuntimeError(w, err, "app_platform_runtime_binding_unavailable")
		return
	}
	// The read above only chooses the deployment; prepare re-checks every row under its own lock and
	// refuses an enabled, changed or recovered binding.
	var deployment string
	switch state {
	case "pending":
		deployment = row.deploymentID
	case "none":
		if deployment, err = platformNewUUID(); err != nil {
			platformManagementError(w, err)
			return
		}
	default:
		platformRuntimeError(w, errAppPlatformRuntimeConflict, "app_platform_runtime_binding_unavailable")
		return
	}
	registration, err := s.preparePlatformRuntimeBinding(actor, deployment)
	if err != nil {
		platformRuntimeError(w, err, "app_platform_runtime_binding_unavailable")
		return
	}
	document, err := service.MarshalEnrollment(service.Controller{
		DeploymentID: registration.deploymentID, ControllerEpoch: registration.controllerEpoch,
		ControlRevision: registration.controlRevision, Fingerprint: registration.fingerprint,
	}, registration.publicKey)
	if err != nil {
		platformRuntimeError(w, errAppPlatformRuntimeConflict, "app_platform_runtime_binding_unavailable")
		return
	}
	out := platformReplacementResponse(registration, false)
	out["enrollment"] = string(document)
	writeJSON(w, out)
}

func (s *Server) platformRuntimeConfirm(w http.ResponseWriter, ctx context.Context, actor platformActorSession, deployment string) {
	row, state, err := s.platformInitialRuntime(actor)
	if err != nil {
		platformRuntimeError(w, err, "app_platform_runtime_binding_unavailable")
		return
	}
	if row.deploymentID != deployment || (state != "pending" && state != "confirmed") {
		platformRuntimeError(w, errAppPlatformRuntimeConflict, "app_platform_runtime_binding_unavailable")
		return
	}
	// An already confirmed initial binding is answered as it stands, without another observation or audit.
	if state == "confirmed" {
		writeJSON(w, platformReplacementResponse(row.platformRuntimeRegistration, true))
		return
	}
	endpoint, ok := s.platformRuntimeEndpoint()
	if !ok {
		jsonErrorCode(w, 409, "app_platform_runtime_endpoint_missing", "Portal has no application runner endpoint configured")
		return
	}
	// Prepare is idempotent while pending and derives the expected identity from the sealed key, never
	// from the stored pin alone.
	expected, err := s.preparePlatformRuntimeBinding(actor, deployment)
	if err != nil {
		platformRuntimeError(w, err, "app_platform_runtime_binding_unavailable")
		return
	}
	confirmed, err := s.confirmPlatformRuntimeBinding(ctx, actor, expected, endpoint)
	if err != nil {
		platformRuntimeError(w, err, "app_platform_runtime_not_observed")
		return
	}
	out := platformReplacementResponse(confirmed, true)
	out["observed"] = map[string]any{
		"deployment_id": confirmed.deploymentID, "controller_epoch": confirmed.controllerEpoch,
		"control_revision": strconv.FormatInt(confirmed.controlRevision, 10), "verification_key_fingerprint": confirmed.fingerprint,
		"public_key": hex.EncodeToString(confirmed.publicKey),
	}
	writeJSON(w, out)
}
