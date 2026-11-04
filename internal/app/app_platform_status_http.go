package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The reader projection intentionally omits operation, generation, revision,
// lease, actor and endpoint identities. The installation ID is already in the
// requesting URL and never appears in the response body.
type platformInstallationStatus struct {
	State         string                `json:"state"`
	Openable      bool                  `json:"openable"`
	Action        string                `json:"action,omitempty"`
	Stage         string                `json:"stage,omitempty"`
	ErrorCode     string                `json:"error_code,omitempty"`
	ControlPaused bool                  `json:"control_paused"`
	Timeline      []platformStatusEvent `json:"timeline"`
}

type platformStatusEvent struct {
	At        string `json:"at"`
	Event     string `json:"event"`
	Stage     string `json:"stage,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

func platformStatusStage(value string) bool {
	switch value {
	case "queued", "validate", "download", "stage", "prepare_data",
		"quiesce", "snapshot", "migrate", "start", "probe", "activate",
		"revoke", "stop", "remove_code", "remove_data", "restore_data",
		"complete", "failed", "recovery_required":
		return true
	default:
		return false
	}
}

func platformStatusEventName(action string) string {
	switch action {
	case "app_platform.operation_admitted":
		return "admitted"
	case "app_platform.operation_stage":
		return "stage"
	case "app_platform.install_activated", "app_platform.writable_install_activated",
		"app_platform.update_activated", "app_platform.managed_update_activated":
		return "activated"
	case "app_platform.restore_activated":
		return "restored"
	case "app_platform.update_failed", "app_platform.managed_update_failed",
		"app_platform.managed_uninstall_failed":
		return "failed"
	case "app_platform.managed_disable_pending":
		return "pending"
	case "app_platform.install_review_required":
		return "review_required"
	case "app_platform.data_deleted":
		return "data_deleted"
	default:
		return "activity"
	}
}

func platformStatusEventFromAudit(at, action, detail string) (platformStatusEvent, bool) {
	parsed, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return platformStatusEvent{}, false
	}
	result := platformStatusEvent{
		At: parsed.UTC().Format(time.RFC3339), Event: platformStatusEventName(action),
	}
	var raw map[string]any
	if result.Event != "admitted" && json.Unmarshal([]byte(detail), &raw) == nil {
		if stage, ok := raw["stage"].(string); ok && platformStatusStage(stage) {
			result.Stage = stage
		}
		if code, ok := raw["error_code"].(string); ok && taskErrorCode(code) {
			result.ErrorCode = code
		}
	}
	return result, true
}

func (s *Server) readPlatformInstallationStatus(ctx context.Context, session platformActorSession, installation string) (platformInstallationStatus, error) {
	result := platformInstallationStatus{Timeline: []platformStatusEvent{}}
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
		return platformInstallationStatus{}, err
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

	rows, err := e.query(`SELECT a.at,a.action,COALESCE(a.detail,'')
		FROM audit_log a
		WHERE a.action LIKE 'app_platform.%'
		AND (
			(a.target_type='app_platform_operation' AND a.target_id IN (
				SELECT id FROM app_platform_operations WHERE installation_id=?
			))
			OR
			(a.target_type='app_platform_installation' AND a.target_id=?)
		)
		ORDER BY a.at DESC,a.id DESC LIMIT 21`, installation, installation)
	if err != nil {
		return platformInstallationStatus{}, err
	}
	for rows.Next() {
		var at, action, detail string
		if err = rows.Scan(&at, &action, &detail); err != nil {
			rows.Close()
			return platformInstallationStatus{}, err
		}
		event, ok := platformStatusEventFromAudit(at, action, detail)
		if ok && len(result.Timeline) < 20 {
			result.Timeline = append(result.Timeline, event)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return platformInstallationStatus{}, err
	}
	rows.Close()

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
