package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type platformAdminTaskAttempt struct {
	Attempt     int64  `json:"attempt"`
	State       string `json:"state"`
	StartedMS   int64  `json:"startedMs"`
	FinishedMS  *int64 `json:"finishedMs,omitempty"`
	ErrorCode   string `json:"errorCode,omitempty"`
	ResultSHA   string `json:"resultSha256,omitempty"`
	EvidenceSHA string `json:"evidenceSha256,omitempty"`
}

type platformAdminTask struct {
	ID             string                     `json:"id"`
	InstallationID string                     `json:"installationId"`
	AppID          string                     `json:"appId"`
	OccurrenceID   string                     `json:"occurrenceId"`
	Handler        string                     `json:"handler"`
	State          string                     `json:"state"`
	Generation     int64                      `json:"generation"`
	PolicyRevision int64                      `json:"policyRevision"`
	Attempts       int64                      `json:"attempts"`
	MaxAttempts    int64                      `json:"maxAttempts"`
	NextAttemptMS  int64                      `json:"nextAttemptMs"`
	ExpiresMS      int64                      `json:"expiresMs"`
	ErrorCode      string                     `json:"errorCode,omitempty"`
	ResultJSON     string                     `json:"result,omitempty"`
	CompletedMS    *int64                     `json:"completedMs,omitempty"`
	UpdatedMS      int64                      `json:"updatedMs"`
	AttemptHistory []platformAdminTaskAttempt `json:"attemptHistory"`
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

func scanPlatformAdminTask(scanner interface{ Scan(...any) error }) (platformAdminTask, error) {
	var task platformAdminTask
	var errorCode, result sql.NullString
	var completed sql.NullInt64
	err := scanner.Scan(
		&task.ID, &task.InstallationID, &task.AppID, &task.OccurrenceID,
		&task.Handler, &task.State, &task.Generation, &task.PolicyRevision,
		&task.Attempts, &task.MaxAttempts, &task.NextAttemptMS, &task.ExpiresMS,
		&errorCode, &result, &completed, &task.UpdatedMS,
	)
	if err != nil {
		return task, err
	}
	task.ErrorCode = errorCode.String
	task.ResultJSON = result.String
	task.CompletedMS = nullableInt64(completed)
	return task, nil
}

func (s *Server) platformAdminTaskAttempts(taskID string) ([]platformAdminTaskAttempt, error) {
	rows, err := s.st.query(`SELECT attempt,state,started_ms,finished_ms,error_code,
		result_sha256,evidence_json FROM app_platform_task_attempts
		WHERE task_id=? ORDER BY attempt`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []platformAdminTaskAttempt
	for rows.Next() {
		var attempt platformAdminTaskAttempt
		var finished sql.NullInt64
		var errorCode, resultSHA, evidence sql.NullString
		if err := rows.Scan(&attempt.Attempt, &attempt.State, &attempt.StartedMS,
			&finished, &errorCode, &resultSHA, &evidence); err != nil {
			return nil, err
		}
		attempt.FinishedMS = nullableInt64(finished)
		attempt.ErrorCode = errorCode.String
		attempt.ResultSHA = resultSHA.String
		if evidence.Valid {
			var value struct {
				EvidenceSHA string `json:"evidence_sha256"`
			}
			if json.Unmarshal([]byte(evidence.String), &value) == nil {
				attempt.EvidenceSHA = value.EvidenceSHA
			}
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

func (s *Server) loadPlatformAdminTask(id string) (platformAdminTask, error) {
	task, err := scanPlatformAdminTask(s.st.queryRow(`SELECT id,installation_id,
		app_id,occurrence_id,handler,state,generation,policy_revision,attempts,
		max_attempts,next_attempt_ms,expires_ms,error_code,result_json,
		completed_ms,updated_ms FROM app_platform_tasks WHERE id=?`, id))
	if err != nil {
		return task, err
	}
	task.AttemptHistory, err = s.platformAdminTaskAttempts(task.ID)
	return task, err
}

func (s *Server) apiPlatformTaskList(w http.ResponseWriter, r *http.Request, _ string) {
	limit := int64(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 || value > 100 {
			jsonErrorCode(w, 400, "invalid_param", "limit must be 1..100")
			return
		}
		limit = value
	}
	installation := r.URL.Query().Get("installation")
	state := r.URL.Query().Get("state")
	if installation != "" && !platformValidUUID(installation) {
		jsonErrorCode(w, 400, "invalid_param", "invalid installation")
		return
	}
	if state != "" && !validPlatformTaskState(appplatformTaskState(state)) {
		jsonErrorCode(w, 400, "invalid_param", "invalid task state")
		return
	}
	query := `SELECT id,installation_id,app_id,occurrence_id,handler,state,
		generation,policy_revision,attempts,max_attempts,next_attempt_ms,expires_ms,
		error_code,result_json,completed_ms,updated_ms FROM app_platform_tasks`
	var where []string
	var args []any
	if installation != "" {
		where = append(where, "installation_id=?")
		args = append(args, installation)
	}
	if state != "" {
		where = append(where, "state=?")
		args = append(args, state)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_ms DESC,id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.st.query(query, args...)
	if err != nil {
		jsonErrorCode(w, 500, "db_error", "task list unavailable")
		return
	}
	defer rows.Close()
	tasks := []platformAdminTask{}
	for rows.Next() {
		task, err := scanPlatformAdminTask(rows)
		if err != nil {
			jsonErrorCode(w, 500, "db_error", "task list unavailable")
			return
		}
		task.AttemptHistory = []platformAdminTaskAttempt{}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		jsonErrorCode(w, 500, "db_error", "task list unavailable")
		return
	}
	if err := rows.Close(); err != nil {
		jsonErrorCode(w, 500, "db_error", "task list unavailable")
		return
	}
	for i := range tasks {
		attempts, err := s.platformAdminTaskAttempts(tasks[i].ID)
		if err != nil {
			jsonErrorCode(w, 500, "db_error", "task attempts unavailable")
			return
		}
		tasks[i].AttemptHistory = attempts
	}
	writeJSON(w, map[string]any{"tasks": tasks})
}

func appplatformTaskState(value string) appplatform.TaskState {
	return appplatform.TaskState(value)
}

func (s *Server) apiPlatformTaskDetail(w http.ResponseWriter, r *http.Request, _ string) {
	id := r.PathValue("id")
	if !platformValidUUID(id) {
		jsonErrorCode(w, 404, "not_found", "task not found")
		return
	}
	task, err := s.loadPlatformAdminTask(id)
	if errors.Is(err, sql.ErrNoRows) {
		jsonErrorCode(w, 404, "not_found", "task not found")
		return
	}
	if err != nil {
		jsonErrorCode(w, 500, "db_error", "task unavailable")
		return
	}
	writeJSON(w, task)
}

func (s *Server) transitionPlatformAdminTask(id, actor, action, decision string) error {
	if !platformValidUUID(id) || actor == "" {
		return errAppPlatformTaskConflict
	}
	tx, err := s.st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: tx}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return err
	}
	var state, installation string
	lock := ""
	if s.st.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	if err := e.queryRow(`SELECT state,installation_id FROM app_platform_tasks WHERE id=?`+lock, id).Scan(&state, &installation); err != nil {
		return err
	}
	requireOne := func(result sql.Result, err error) error {
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return errAppPlatformTaskConflict
		}
		return nil
	}
	switch action {
	case "cancel":
		switch state {
		case "waiting":
			result, updateErr := e.exec(`UPDATE app_platform_tasks
				SET state='cancelled',error_code='admin_cancelled',
				    completed_ms=?,revision=revision+1,updated_ms=?
				WHERE id=? AND state='waiting'`, now, now, id)
			err = requireOne(result, updateErr)
		case "leased", "running":
			result, updateErr := e.exec(`UPDATE app_platform_task_attempts
				SET state='uncertain',finished_ms=?,error_code='admin_cancel_review',updated_ms=?
				WHERE task_id=? AND state IN ('leased','running')`,
				now, now, id)
			if err = requireOne(result, updateErr); err == nil {
				result, updateErr = e.exec(`UPDATE app_platform_tasks
					SET state='review_required',error_code='admin_cancel_review',
					    worker_id=NULL,lease_id=NULL,lease_until_ms=NULL,
					    revision=revision+1,updated_ms=?
					WHERE id=? AND state=?`, now, id, state)
				err = requireOne(result, updateErr)
			}
		default:
			return errAppPlatformTaskConflict
		}
	case "review":
		if state != "uncertain" && state != "review_required" {
			return errAppPlatformTaskConflict
		}
		nextState := "failed"
		code := "reviewed_failed"
		if decision == "cancelled" {
			nextState, code = "cancelled", "reviewed_cancelled"
		} else if decision != "failed" {
			return errPlatformManagementInput
		}
		result, updateErr := e.exec(`UPDATE app_platform_tasks
			SET state=?,error_code=?,completed_ms=?,revision=revision+1,updated_ms=?
			WHERE id=? AND state=?`, nextState, code, now, now, id, state)
		err = requireOne(result, updateErr)
	default:
		return errAppPlatformTaskConflict
	}
	if err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]string{"action": action, "decision": decision})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,?,0,'app_platform.task_'||?, 'app_platform_task',?,?,'')`,
		timeUnixMilliUTC(now), actor, action, id, string(detail)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) apiPlatformTaskCancel(w http.ResponseWriter, r *http.Request, user string) {
	if err := s.transitionPlatformAdminTask(r.PathValue("id"), user, "cancel", ""); err != nil {
		if errors.Is(err, errAppPlatformTaskConflict) {
			jsonErrorCode(w, 409, "task_state_conflict", "task cannot be cancelled in its current state")
		} else {
			jsonErrorCode(w, 500, "db_error", "task cancellation failed")
		}
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) apiPlatformTaskReview(w http.ResponseWriter, r *http.Request, user string) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid review decision")
		return
	}
	fields, err := platformManagementObject(raw, "decision")
	if err != nil || len(fields) != 1 {
		jsonErrorCode(w, 400, "invalid_param", "invalid review decision")
		return
	}
	decision, err := platformManagementString(fields, "decision")
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid review decision")
		return
	}
	if err := s.transitionPlatformAdminTask(r.PathValue("id"), user, "review", decision); err != nil {
		if errors.Is(err, errAppPlatformTaskConflict) {
			jsonErrorCode(w, 409, "task_state_conflict", "task cannot be reviewed in its current state")
		} else if errors.Is(err, errPlatformManagementInput) {
			jsonErrorCode(w, 400, "invalid_param", "decision must be failed or cancelled")
		} else {
			jsonErrorCode(w, 500, "db_error", "task review failed")
		}
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
