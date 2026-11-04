package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The reader projection intentionally omits operation, generation, revision,
// lease, actor and endpoint identities. The installation ID is already in the
// requesting URL and never appears in the response body.
type platformInstallationStatus struct {
	State         string `json:"state"`
	Openable      bool   `json:"openable"`
	Action        string `json:"action,omitempty"`
	Stage         string `json:"stage,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	ControlPaused bool   `json:"control_paused"`
}

func (s *Server) readPlatformInstallationStatus(ctx context.Context, session platformActorSession, installation string) (platformInstallationStatus, error) {
	var result platformInstallationStatus
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
	account, err := platformReadSessionAccount(e, session, now, true, false)
	if err != nil {
		return result, err
	}
	if err = lockPlatformPublisherTrust(e); err != nil {
		return result, err
	}
	chain, err := platformAudienceChain(e, account)
	if err != nil {
		return result, err
	}
	if err = platformAudienceGrant(e, account, installation, chain); err != nil {
		return result, err
	}

	var appID, desired, phase, epoch string
	var runtime sql.NullString
	if err = e.queryRow(`SELECT app_id,desired_state,phase,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`, installation).Scan(
		&appID, &desired, &phase, &epoch, &runtime,
	); errors.Is(err, sql.ErrNoRows) {
		return result, errAppPlatformLaunchUnavailable
	} else if err != nil {
		return result, err
	}
	if !platformDirectoryAppID.MatchString(appID) {
		return result, errAppPlatformOperationConflict
	}

	var currentEpoch sql.NullString
	var paused sql.NullString
	if err = e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&currentEpoch); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if err = e.queryRow(`SELECT v FROM meta WHERE k='app_platform_restore_paused'`).Scan(&paused); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	result.ControlPaused = paused.String != "0" || !platformValidUUID(epoch) || !currentEpoch.Valid || epoch != currentEpoch.String
	result.State = "attention"
	switch {
	case desired == "disabled":
		result.State = "disabled"
	case phase == "pending" || phase == "setup_required":
		result.State = "pending"
	case desired == "enabled" && phase == "ready" && !result.ControlPaused:
		eligible := !runtime.Valid
		if runtime.Valid {
			eligible, err = platformWritableLaunchEligible(e, installation, runtime.String, epoch)
			if err != nil {
				return result, err
			}
		}
		if eligible {
			result.State = "installed"
			result.Openable = true
		}
	}

	var action appplatform.Action
	var stage appplatform.Stage
	var managed int
	var generation, revision int64
	var code sql.NullString
	err = e.queryRow(`SELECT action,managed,target_generation,state_revision,stage,error_code
		FROM app_platform_operations WHERE installation_id=?
		ORDER BY updated_ms DESC,id DESC LIMIT 1`, installation).Scan(
		&action, &managed, &generation, &revision, &stage, &code,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		err = nil
	case err != nil:
		return result, err
	default:
		// A corrupt historical row must not make the whole status route lie or
		// disclose internal evidence. Omit action/stage unless they reconstruct.
		if _, snapshotErr := appplatform.OperationFromSnapshot(appplatform.OperationSnapshot{
			Action: action, Managed: managed == 1, Generation: uint64(generation),
			Revision: uint64(revision), Stage: stage,
		}); snapshotErr == nil {
			result.Action = string(action)
			result.Stage = string(stage)
		}
		if code.Valid && code.String != "" {
			result.ErrorCode = "app_platform_operation_failed"
			if code.String == "install_lease_recovery_required" ||
				code.String == "install_authority_review_required" ||
				code.String == "install_execution_review_required" {
				result.ErrorCode = code.String
			}
		}
	}

	final, err := platformDatabaseNow(e)
	if err != nil {
		return platformInstallationStatus{}, err
	}
	current, err := platformReadSessionAccount(e, session, final, true, false)
	if err != nil || current != account {
		return platformInstallationStatus{}, errAppPlatformLaunchUnavailable
	}
	if err = tx.Commit(); err != nil {
		return platformInstallationStatus{}, err
	}
	return result, nil
}

func (s *Server) apiPlatformInstallationStatus(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformHTTPPath(r) || !platformValidUUID(installation) {
		platformDocumentFailure(w)
		return
	}
	session, release, ok := s.platformHTTPAdmission(w, r, user)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), platformDocumentTimeout)
	defer cancel()
	result, err := s.readPlatformInstallationStatus(ctx, session, installation)
	if err != nil || ctx.Err() != nil {
		platformDocumentFailure(w)
		return
	}
	writeJSON(w, result)
}
