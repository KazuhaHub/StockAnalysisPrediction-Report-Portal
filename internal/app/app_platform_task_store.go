package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const (
	platformTaskLeaseDuration = 90 * time.Second
	platformTaskMaxLifetime   = 7 * 24 * time.Hour
	platformTaskMaxAttempts   = 3
)

var (
	errAppPlatformTaskConflict    = errors.New("application task state changed")
	errAppPlatformTaskUnavailable = errors.New("application task unavailable")
	errAppPlatformTaskQuota       = errors.New("application task quota exhausted")
)

type platformTaskAdmission struct {
	OccurrenceID string
	Handler      string
	Payload      []byte
	RunAtMS      int64
	ExpiresMS    int64
}

type platformTaskLease struct {
	ID              string
	InstallationID  string
	AppID           string
	PublisherID     string
	OccurrenceID    string
	Handler         string
	Payload         []byte
	PayloadSHA256   string
	State           appplatform.TaskState
	Generation      int64
	PolicyRevision  int64
	ReleaseSHA256   string
	ControllerEpoch string
	Attempts        uint64
	MaxAttempts     uint64
	Fence           uint64
	Revision        uint64
	WorkerID        string
	LeaseID         string
	LeaseUntilMS    int64
	NextAttemptMS   int64
	ExpiresMS       int64
	ExecutionSHA256 string
}

func platformTaskExecutionDigest(task platformTaskLease, attempt, fence uint64) string {
	raw, _ := json.Marshal(struct {
		ID              string `json:"id"`
		InstallationID  string `json:"installation_id"`
		AppID           string `json:"app_id"`
		PublisherID     string `json:"publisher_id"`
		OccurrenceID    string `json:"occurrence_id"`
		Handler         string `json:"handler"`
		PayloadSHA256   string `json:"payload_sha256"`
		Generation      int64  `json:"generation"`
		PolicyRevision  int64  `json:"policy_revision"`
		ReleaseSHA256   string `json:"release_sha256"`
		ControllerEpoch string `json:"controller_epoch"`
		Attempt         uint64 `json:"attempt"`
		Fence           uint64 `json:"fence"`
	}{
		task.ID, task.InstallationID, task.AppID, task.PublisherID,
		task.OccurrenceID, task.Handler, task.PayloadSHA256, task.Generation,
		task.PolicyRevision, task.ReleaseSHA256, task.ControllerEpoch, attempt,
		fence,
	})
	return platformDigest(raw)
}

func lockPlatformTaskAdmission(e migExec) error {
	if e.s.driver == "postgres" {
		if _, err := e.exec(`SELECT pg_advisory_xact_lock(780719)`); err != nil {
			return err
		}
	}
	return nil
}

func validatePlatformTaskAdmission(in platformTaskAdmission, now int64) (platformTaskAdmission, string, error) {
	payload, digest, err := appplatform.ValidateTaskPayload(in.Payload)
	if err != nil || !notificationOccurrenceRe.MatchString(in.OccurrenceID) || len(in.Handler) < 1 || len(in.Handler) > 64 ||
		in.RunAtMS < now || in.ExpiresMS <= in.RunAtMS ||
		in.ExpiresMS-now > platformTaskMaxLifetime.Milliseconds() ||
		in.ExpiresMS-in.RunAtMS > platformTaskMaxLifetime.Milliseconds() {
		return platformTaskAdmission{}, "", errAppPlatformTaskUnavailable
	}
	in.Payload = payload
	return in, digest, nil
}

func (s *Server) admitPlatformTask(ctx context.Context, installation string, in platformTaskAdmission) (string, bool, error) {
	if s == nil || s.st == nil || !platformValidUUID(installation) {
		return "", false, errAppPlatformTaskUnavailable
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	id, created, err := s.st.admitPlatformTaskTx(e, installation, in)
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return id, created, nil
}

func (s *Store) admitPlatformTaskTx(e migExec, installation string, in platformTaskAdmission) (string, bool, error) {
	if err := lockPlatformTaskAdmission(e); err != nil {
		return "", false, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return "", false, err
	}
	if in.RunAtMS == 0 {
		in.RunAtMS = now
	}
	if in.ExpiresMS == 0 {
		in.ExpiresMS = now + (24 * time.Hour).Milliseconds()
	}
	validated, digest, err := validatePlatformTaskAdmission(in, now)
	if err != nil {
		return "", false, fmt.Errorf("%w: admission shape", errAppPlatformTaskUnavailable)
	}
	in = validated
	resources := []string{"tasks.schedule"}
	if in.Handler == appplatform.TaskHandlerEvent {
		delivery, parseErr := appplatform.ParseEventDelivery(in.Payload)
		if parseErr != nil {
			return "", false, fmt.Errorf("%w: event delivery", errAppPlatformTaskUnavailable)
		}
		resources = append(resources, "events.subscribe")
		if delivery.EventType == appplatform.EventReportIngested {
			resources = append(resources, "reports.read")
		}
	}
	facts, err := loadPlatformServiceFacts(e, installation, resources, true)
	if err != nil {
		return "", false, fmt.Errorf("%w: current authority", errAppPlatformTaskUnavailable)
	}
	granted, err := platformTaskGrantedCapabilities(e, installation, facts.policyRevision)
	if err != nil {
		return "", false, err
	}
	if _, err := appplatform.ResolveTaskHandler(facts.appID, in.Handler, granted); err != nil {
		return "", false, fmt.Errorf("%w: handler resolution", errAppPlatformTaskUnavailable)
	}
	var existingID, existingDigest, existingHandler string
	err = e.queryRow(`SELECT id,payload_sha256,handler FROM app_platform_tasks WHERE installation_id=? AND occurrence_id=?`, installation, in.OccurrenceID).
		Scan(&existingID, &existingDigest, &existingHandler)
	if err == nil {
		if existingDigest != digest || existingHandler != in.Handler {
			return "", false, errAppPlatformTaskConflict
		}
		return existingID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	var global, application, installationActive int64
	for _, count := range []struct {
		query string
		args  []any
		dest  *int64
	}{
		{`SELECT COUNT(*) FROM app_platform_tasks WHERE state NOT IN ('succeeded','failed','cancelled')`, nil, &global},
		{`SELECT COUNT(*) FROM app_platform_tasks WHERE app_id=? AND state NOT IN ('succeeded','failed','cancelled')`, []any{facts.appID}, &application},
		{`SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, []any{installation}, &installationActive},
	} {
		if err := e.queryRow(count.query, count.args...).Scan(count.dest); err != nil {
			return "", false, err
		}
	}
	if err := appplatform.AdmitTask(appplatform.TaskQuota{
		GlobalActive: uint64(global), ApplicationActive: uint64(application),
		InstallationActive: uint64(installationActive),
	}); err != nil {
		return "", false, errAppPlatformTaskQuota
	}
	id, err := platformNewUUID()
	if err != nil {
		return "", false, err
	}
	_, err = e.exec(`INSERT INTO app_platform_tasks(
		id,installation_id,app_id,publisher_id,occurrence_id,handler,payload,payload_sha256,
		state,generation,policy_revision,release_sha256,controller_epoch,attempts,max_attempts,
		fence,revision,worker_id,lease_id,lease_until_ms,next_attempt_ms,expires_ms,
		error_code,result_json,completed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,'waiting',?,?,?,?,0,?,1,1,NULL,NULL,NULL,?,?,NULL,NULL,NULL,?,?)`,
		id, installation, facts.appID, facts.publisherID, in.OccurrenceID, in.Handler,
		in.Payload, digest, facts.generation, facts.policyRevision, facts.release,
		facts.epoch, platformTaskMaxAttempts, in.RunAtMS, in.ExpiresMS, now, now)
	if err != nil {
		return "", false, err
	}
	detail, _ := json.Marshal(struct {
		AppID       string `json:"app_id"`
		Handler     string `json:"handler"`
		Occurrence  string `json:"occurrence_sha256"`
		RunAtMS     int64  `json:"run_at_ms"`
		ExpiresAtMS int64  `json:"expires_at_ms"`
	}{
		facts.appID, in.Handler, platformDigest([]byte(in.OccurrenceID)),
		in.RunAtMS, in.ExpiresMS,
	})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.task_admitted','app_platform_task',?,?,'')`,
		timeUnixMilliUTC(now), id, string(detail)); err != nil {
		return "", false, err
	}
	return id, true, nil
}

func platformTaskGrantedCapabilities(e migExec, installation, policyRevision string) (map[string]bool, error) {
	rows, err := e.query(`SELECT capability,granted FROM app_platform_capability_grants WHERE installation_id=? AND policy_revision=? ORDER BY capability`, installation, policyRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	granted := map[string]bool{}
	for rows.Next() {
		var capability string
		var value int
		if err := rows.Scan(&capability, &value); err != nil {
			return nil, err
		}
		granted[capability] = value == 1
	}
	return granted, rows.Err()
}

func (s *Server) claimNextPlatformTask(ctx context.Context, worker string) (platformTaskLease, error) {
	var zero platformTaskLease
	if s == nil || s.st == nil || !platformValidUUID(worker) {
		return zero, errAppPlatformTaskUnavailable
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	if err := expirePlatformTasks(e, now); err != nil {
		return zero, err
	}
	var epoch, paused string
	if err := e.queryRow(`SELECT COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),''),COALESCE((SELECT v FROM meta WHERE k='app_platform_restore_paused'),'')`).Scan(&epoch, &paused); err != nil || paused != "0" || !platformValidUUID(epoch) {
		return zero, errAppPlatformTaskUnavailable
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE OF t SKIP LOCKED"
	}
	rows, err := e.query(`SELECT t.id FROM app_platform_tasks t
		JOIN app_platform_installations i ON i.id=t.installation_id
		WHERE t.state='waiting' AND t.next_attempt_ms<=? AND t.expires_ms>?
		AND t.attempts<t.max_attempts AND t.controller_epoch=?
		AND i.desired_state='enabled' AND i.phase='ready'
		AND i.app_id=t.app_id AND i.publisher_id=t.publisher_id
		AND i.generation=t.generation AND i.policy_revision=t.policy_revision
		AND i.controller_epoch=t.controller_epoch
		ORDER BY t.next_attempt_ms,t.created_ms,t.id LIMIT 32`+lock, now, now, epoch)
	if err != nil {
		return zero, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return zero, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}
	for _, id := range ids {
		task, err := readPlatformTaskForWorker(e, id, false)
		if err != nil {
			if errors.Is(err, errAppPlatformTaskConflict) {
				continue
			}
			return zero, err
		}
		if task.State != appplatform.TaskWaiting || task.NextAttemptMS > now || task.ExpiresMS <= now ||
			task.Attempts >= task.MaxAttempts || task.Revision == math.MaxUint64 || task.Fence == math.MaxUint64 {
			continue
		}
		if _, err := platformTaskCurrentAuthority(e, task); err != nil {
			_, updateErr := e.exec(`UPDATE app_platform_tasks
				SET state='review_required',error_code='task_authority_lost',
				    revision=revision+1,updated_ms=?
				WHERE id=? AND state='waiting' AND revision=?`,
				now, task.ID, task.Revision)
			if updateErr != nil {
				return zero, updateErr
			}
			continue
		}
		leaseID, err := platformNewUUID()
		if err != nil {
			return zero, err
		}
		attempt, fence, revision := task.Attempts+1, task.Fence+1, task.Revision+1
		leaseUntil := now + platformTaskLeaseDuration.Milliseconds()
		if leaseUntil > task.ExpiresMS {
			continue
		}
		executionSHA := platformTaskExecutionDigest(task, attempt, fence)
		changed, err := e.exec(`UPDATE app_platform_tasks
			SET state='leased',attempts=?,fence=?,revision=?,worker_id=?,lease_id=?,
			    lease_until_ms=?,updated_ms=?
			WHERE id=? AND state='waiting' AND attempts=? AND fence=? AND revision=?
			AND next_attempt_ms<=? AND expires_ms>?`,
			attempt, fence, revision, worker, leaseID, leaseUntil, now, id,
			task.Attempts, task.Fence, task.Revision, now, now)
		if err != nil {
			return zero, err
		}
		affected, err := changed.RowsAffected()
		if err != nil {
			return zero, err
		}
		if affected == 0 {
			continue
		}
		if _, err := e.exec(`INSERT INTO app_platform_task_attempts(
			task_id,attempt,state,worker_id,lease_id,fence,execution_sha256,
			started_ms,lease_until_ms,finished_ms,error_code,result_sha256,
			evidence_json,created_ms,updated_ms)
			VALUES(?,?,'leased',?,?,?,?,?,?,NULL,NULL,NULL,NULL,?,?)`,
			id, attempt, worker, leaseID, fence, executionSHA, now, leaseUntil, now, now); err != nil {
			return zero, err
		}
		task.State = appplatform.TaskLeased
		task.Attempts, task.Fence, task.Revision = attempt, fence, revision
		task.WorkerID, task.LeaseID, task.LeaseUntilMS = worker, leaseID, leaseUntil
		task.ExecutionSHA256 = executionSHA
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		return task, nil
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return zero, nil
}

func readPlatformTaskForWorker(e migExec, id string, lock bool) (platformTaskLease, error) {
	var task platformTaskLease
	lockSQL := ""
	if lock && e.s.driver == "postgres" {
		lockSQL = " FOR UPDATE"
	}
	var state string
	var leaseID, worker sql.NullString
	var leaseUntil sql.NullInt64
	err := e.queryRow(`SELECT id,installation_id,app_id,publisher_id,occurrence_id,handler,payload,
		payload_sha256,state,generation,policy_revision,release_sha256,controller_epoch,
		attempts,max_attempts,fence,revision,worker_id,lease_id,lease_until_ms,
		next_attempt_ms,expires_ms FROM app_platform_tasks WHERE id=?`+lockSQL, id).
		Scan(&task.ID, &task.InstallationID, &task.AppID, &task.PublisherID,
			&task.OccurrenceID, &task.Handler, &task.Payload, &task.PayloadSHA256,
			&state, &task.Generation, &task.PolicyRevision, &task.ReleaseSHA256,
			&task.ControllerEpoch, &task.Attempts, &task.MaxAttempts, &task.Fence,
			&task.Revision, &worker, &leaseID, &leaseUntil, &task.NextAttemptMS,
			&task.ExpiresMS)
	if errors.Is(err, sql.ErrNoRows) {
		return task, errAppPlatformTaskConflict
	}
	if err != nil {
		return task, err
	}
	task.State = appplatform.TaskState(state)
	task.WorkerID, task.LeaseID = worker.String, leaseID.String
	task.LeaseUntilMS = leaseUntil.Int64
	if !platformValidUUID(task.ID) || !platformValidUUID(task.InstallationID) ||
		!platformDirectoryAppID.MatchString(task.AppID) || !platformDirectoryAppID.MatchString(task.PublisherID) ||
		!notificationOccurrenceRe.MatchString(task.OccurrenceID) || len(task.Handler) > 64 ||
		len(task.Payload) == 0 || len(task.Payload) > appplatform.MaxTaskPayloadBytes ||
		platformDigest(task.Payload) != task.PayloadSHA256 || !platformValidSHA(task.ReleaseSHA256) ||
		!platformValidUUID(task.ControllerEpoch) || task.Generation < 1 || task.PolicyRevision < 1 ||
		task.MaxAttempts < 1 || task.MaxAttempts > appplatform.MaxTaskAttempts ||
		task.Attempts > task.MaxAttempts || task.Fence < 1 || task.Revision < 1 ||
		task.NextAttemptMS > task.ExpiresMS || !validPlatformTaskState(task.State) {
		return task, errAppPlatformTaskConflict
	}
	return task, nil
}

func validPlatformTaskState(state appplatform.TaskState) bool {
	switch state {
	case appplatform.TaskWaiting, appplatform.TaskLeased, appplatform.TaskRunning,
		appplatform.TaskSucceeded, appplatform.TaskFailed, appplatform.TaskCancelled,
		appplatform.TaskUncertain, appplatform.TaskReviewRequired:
		return true
	default:
		return false
	}
}

func expirePlatformTasks(e migExec, now int64) error {
	// A dead worker cannot prove whether its external effect committed. Close the
	// stale attempt as uncertain and revoke its lease; never put it back in the
	// automatic retry queue.
	if _, err := e.exec(`UPDATE app_platform_task_attempts
		SET state='uncertain',finished_ms=?,error_code='task_lease_expired',updated_ms=?
		WHERE state IN ('leased','running') AND task_id IN (
			SELECT id FROM app_platform_tasks
			WHERE state IN ('leased','running') AND lease_until_ms<=?)`,
		now, now, now); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_tasks
		SET state='uncertain',worker_id=NULL,lease_id=NULL,lease_until_ms=NULL,
		    error_code='task_lease_expired',result_json=NULL,
		    revision=revision+1,updated_ms=?
		WHERE state IN ('leased','running') AND lease_until_ms<=?`,
		now, now); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_task_attempts
		SET state='uncertain',finished_ms=?,error_code='task_expired',updated_ms=?
		WHERE state IN ('leased','running') AND task_id IN (
			SELECT id FROM app_platform_tasks
			WHERE state IN ('leased','running') AND expires_ms<=?)`, now, now, now); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_tasks
		SET state='review_required',worker_id=NULL,lease_id=NULL,lease_until_ms=NULL,
		    revision=revision+1,updated_ms=?
		WHERE state IN ('leased','running') AND expires_ms<=?`, now, now); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_tasks
		SET state='failed',error_code='task_expired',completed_ms=?,
		    revision=revision+1,updated_ms=?
		WHERE state='waiting' AND expires_ms<=?`, now, now, now); err != nil {
		return err
	}
	return nil
}

func invalidatePlatformTasksForInstallation(e migExec, installation, code string, now int64) error {
	if !platformValidUUID(installation) || !taskErrorCode(code) {
		return errAppPlatformTaskConflict
	}
	if _, err := e.exec(`UPDATE app_platform_task_attempts
		SET state='uncertain',finished_ms=?,error_code=?,updated_ms=?
		WHERE state IN ('leased','running') AND task_id IN (
			SELECT id FROM app_platform_tasks
			WHERE installation_id=? AND state IN ('leased','running')
		)`, now, code, now, installation); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_tasks
		SET state='uncertain',worker_id=NULL,lease_id=NULL,lease_until_ms=NULL,
		    error_code=?,result_json=NULL,revision=revision+1,updated_ms=?
		WHERE installation_id=? AND state IN ('leased','running')`,
		code, now, installation); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_tasks
		SET state='cancelled',error_code=?,completed_ms=?,revision=revision+1,updated_ms=?
		WHERE installation_id=? AND state='waiting'`,
		code, now, now, installation); err != nil {
		return err
	}
	return nil
}

func (s *Server) startPlatformTask(ctx context.Context, task platformTaskLease) (platformTaskLease, string, error) {
	var zero platformTaskLease
	if s == nil || s.st == nil || !platformValidUUID(task.ID) || !platformValidUUID(task.WorkerID) || !platformValidUUID(task.LeaseID) {
		return zero, "", errAppPlatformTaskConflict
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, "", err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	current, err := readPlatformTaskForWorker(e, task.ID, true)
	if err != nil {
		return zero, "", fmt.Errorf("%w: initial read", errAppPlatformTaskConflict)
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, "", err
	}
	if current.State != appplatform.TaskLeased || current.Revision != task.Revision ||
		current.WorkerID != task.WorkerID || current.LeaseID != task.LeaseID ||
		current.Fence != task.Fence || current.LeaseUntilMS <= now ||
		current.LeaseUntilMS < now+appplatform.MinTaskLease.Milliseconds() {
		return zero, "", fmt.Errorf("%w: initial lease", errAppPlatformTaskConflict)
	}
	granted, err := platformTaskCurrentAuthority(e, current)
	if err != nil {
		return zero, "", fmt.Errorf("%w: initial authority", errAppPlatformTaskUnavailable)
	}
	handler, err := appplatform.ResolveTaskHandler(current.AppID, current.Handler, granted)
	if err != nil {
		return zero, "", errAppPlatformTaskUnavailable
	}
	resources := taskServiceResources(handler)
	if current.Handler == appplatform.TaskHandlerEvent {
		delivery, err := appplatform.ParseEventDelivery(current.Payload)
		if err != nil {
			return zero, "", errAppPlatformTaskUnavailable
		}
		if delivery.EventType == appplatform.EventReportIngested {
			resources = append(resources, "reports.read")
		}
		resources = signedTaskResourceUnion(resources)
	}
	if err := tx.Commit(); err != nil {
		return zero, "", err
	}
	raw, _, err := s.issuePlatformServiceIdentity(ctx, current.InstallationID, current.LeaseID, resources, platformTaskLeaseDuration)
	if err != nil {
		return zero, "", err
	}
	tx, err = s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, "", err
	}
	defer tx.Rollback()
	e = migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	current, err = readPlatformTaskForWorker(e, task.ID, true)
	if err != nil {
		return zero, "", fmt.Errorf("%w: second read", errAppPlatformTaskConflict)
	}
	now, err = platformDatabaseNow(e)
	if err != nil {
		return zero, "", err
	}
	if current.State != appplatform.TaskLeased || current.Revision != task.Revision ||
		current.WorkerID != task.WorkerID || current.LeaseID != task.LeaseID ||
		current.Fence != task.Fence || current.LeaseUntilMS <= now ||
		current.LeaseUntilMS < now+appplatform.MinTaskLease.Milliseconds() {
		return zero, "", fmt.Errorf("%w: second lease", errAppPlatformTaskConflict)
	}
	if _, err := platformTaskCurrentAuthority(e, current); err != nil {
		return zero, "", fmt.Errorf("%w: second authority", errAppPlatformTaskUnavailable)
	}
	current.ExecutionSHA256 = platformTaskExecutionDigest(current, current.Attempts, current.Fence)
	result, err := e.exec(`UPDATE app_platform_tasks
		SET state='running',revision=revision+1,updated_ms=?
		WHERE id=? AND state='leased' AND revision=? AND fence=? AND worker_id=? AND lease_id=?
		AND lease_until_ms=? AND lease_until_ms>?`, now, current.ID, current.Revision,
		current.Fence, current.WorkerID, current.LeaseID, current.LeaseUntilMS, now)
	if err != nil {
		return zero, "", err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return zero, "", err
	}
	if affected != 1 {
		return zero, "", fmt.Errorf("%w: task running update", errAppPlatformTaskConflict)
	}
	result, err = e.exec(`UPDATE app_platform_task_attempts
		SET state='running',updated_ms=?
		WHERE task_id=? AND attempt=? AND state='leased' AND worker_id=? AND lease_id=?
		AND fence=? AND execution_sha256=? AND finished_ms IS NULL`,
		now, current.ID, current.Attempts, current.WorkerID, current.LeaseID,
		current.Fence, current.ExecutionSHA256)
	if err != nil {
		return zero, "", err
	}
	affected, err = result.RowsAffected()
	if err != nil {
		return zero, "", err
	}
	if affected != 1 {
		return zero, "", fmt.Errorf("%w: attempt running update", errAppPlatformTaskConflict)
	}
	current.State = appplatform.TaskRunning
	current.Revision++
	current.Payload = bytes.Clone(current.Payload)
	if err := tx.Commit(); err != nil {
		return zero, "", err
	}
	return current, string(raw), nil
}

func taskServiceResources(handler appplatform.TaskHandlerSpec) []string {
	resources := []string{"tasks.schedule"}
	for _, resource := range handler.RequiredCapabilities {
		if resource != "background.run" {
			resources = append(resources, resource)
		}
	}
	return signedTaskResourceUnion(resources)
}

func signedTaskResourceUnion(resources []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(resources))
	for _, resource := range resources {
		if !seen[resource] {
			seen[resource] = true
			out = append(out, resource)
		}
	}
	return appplatform.SortedTaskResources(out)
}

func platformTaskCurrentAuthority(e migExec, task platformTaskLease) (map[string]bool, error) {
	handlerGranted, err := platformTaskGrantedCapabilities(e, task.InstallationID, strconv.FormatInt(task.PolicyRevision, 10))
	if err != nil {
		return nil, err
	}
	handler, err := appplatform.ResolveTaskHandler(task.AppID, task.Handler, handlerGranted)
	if err != nil {
		return nil, errAppPlatformTaskUnavailable
	}
	required := taskServiceResources(handler)
	if task.Handler == appplatform.TaskHandlerEvent {
		delivery, err := appplatform.ParseEventDelivery(task.Payload)
		if err != nil {
			return nil, errAppPlatformTaskUnavailable
		}
		var subscription platformEventSubscription
		if err := readPlatformEventSubscription(e,
			`id=? AND installation_id=?`,
			[]any{delivery.Subscription, task.InstallationID},
			&subscription, true); err != nil ||
			subscription.State != "active" ||
			subscription.AppID != task.AppID ||
			subscription.PublisherID != task.PublisherID ||
			subscription.Generation != task.Generation ||
			subscription.PolicyRevision != task.PolicyRevision ||
			subscription.ReleaseSHA256 != task.ReleaseSHA256 ||
			subscription.ControllerEpoch != task.ControllerEpoch ||
			subscription.EventType != delivery.EventType ||
			subscription.ResourceType != delivery.ResourceType ||
			subscription.ResourceID != delivery.ResourceID ||
			!strings.HasSuffix(task.OccurrenceID,
				":"+strconv.FormatInt(subscription.Revision, 10)) {
			return nil, errAppPlatformTaskUnavailable
		}
		if delivery.EventType == appplatform.EventReportIngested {
			required = append(required, "reports.read")
		}
	}
	required = signedTaskResourceUnion(required)
	facts, err := loadPlatformServiceFacts(e, task.InstallationID, required, true)
	if err != nil || facts.appID != task.AppID || facts.publisherID != task.PublisherID ||
		facts.generation != strconv.FormatInt(task.Generation, 10) ||
		facts.policyRevision != strconv.FormatInt(task.PolicyRevision, 10) ||
		facts.release != task.ReleaseSHA256 || facts.epoch != task.ControllerEpoch {
		return nil, errAppPlatformTaskUnavailable
	}
	return handlerGranted, nil
}

func (s *Server) completePlatformTask(ctx context.Context, task platformTaskLease, identity string, result appplatform.TaskExecutionResult) error {
	if _, err := s.verifyPlatformServiceIdentity(ctx, []byte(identity), "tasks.schedule"); err != nil {
		return s.markPlatformTaskUncertain(task.ID, task.WorkerID, task.LeaseID, task.Fence, task.Revision, "authority_revoked")
	}
	nowMS := time.Now().UTC().UnixMilli()
	decision, err := appplatform.ClassifyTaskResult(result, task.Attempts, task.MaxAttempts, time.UnixMilli(nowMS).UTC())
	if err != nil {
		return err
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	current, err := readPlatformTaskForWorker(e, task.ID, true)
	if err != nil {
		return err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return err
	}
	if current.State != appplatform.TaskRunning || current.Revision != task.Revision ||
		current.WorkerID != task.WorkerID || current.LeaseID != task.LeaseID ||
		current.Fence != task.Fence || current.LeaseUntilMS <= now {
		return errAppPlatformTaskConflict
	}
	current.ExecutionSHA256 = platformTaskExecutionDigest(current, current.Attempts, current.Fence)
	if _, err := platformTaskCurrentAuthority(e, current); err != nil {
		if _, updateErr := e.exec(`UPDATE app_platform_task_attempts
			SET state='uncertain',finished_ms=?,error_code='authority_revoked',updated_ms=?
			WHERE task_id=? AND attempt=? AND state='running' AND worker_id=? AND lease_id=? AND fence=?`,
			now, now, current.ID, current.Attempts, current.WorkerID, current.LeaseID, current.Fence); updateErr != nil {
			return updateErr
		}
		if _, updateErr := e.exec(`UPDATE app_platform_tasks
			SET state='uncertain',revision=revision+1,worker_id=NULL,lease_id=NULL,lease_until_ms=NULL,
			    error_code='authority_revoked',result_json=NULL,updated_ms=?
			WHERE id=? AND state='running' AND revision=? AND fence=? AND worker_id=? AND lease_id=?`,
			now, current.ID, current.Revision, current.Fence, current.WorkerID, current.LeaseID); updateErr != nil {
			return updateErr
		}
		return tx.Commit()
	}
	attemptState := decision.State
	if decision.State == appplatform.TaskWaiting {
		attemptState = appplatform.TaskFailed
	}
	var resultSHA any
	if len(decision.ResultJSON) > 0 {
		resultSHA = platformDigest(decision.ResultJSON)
	}
	var evidence any
	if decision.EvidenceSHA != "" {
		evidence = `{"evidence_sha256":"` + decision.EvidenceSHA + `"}`
	}
	finished := any(now)
	if decision.State == appplatform.TaskWaiting {
		finished = now
	}
	update, err := e.exec(`UPDATE app_platform_task_attempts
		SET state=?,finished_ms=?,error_code=?,result_sha256=?,evidence_json=?,updated_ms=?
		WHERE task_id=? AND attempt=? AND state='running' AND worker_id=? AND lease_id=?
		AND fence=? AND execution_sha256=?`,
		attemptState, finished, nullIfEmpty(decision.ErrorCode), resultSHA, evidence,
		now, current.ID, current.Attempts, current.WorkerID, current.LeaseID,
		current.Fence, current.ExecutionSHA256)
	if err != nil {
		return err
	}
	affected, err := update.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errAppPlatformTaskConflict
	}
	completed := any(nil)
	if decision.State == appplatform.TaskSucceeded || decision.State == appplatform.TaskFailed ||
		decision.State == appplatform.TaskCancelled {
		completed = now
	}
	nextAttempt := current.NextAttemptMS
	if !decision.RetryAt.IsZero() {
		nextAttempt = decision.RetryAt.UnixMilli()
		if nextAttempt > current.ExpiresMS {
			decision.State = appplatform.TaskFailed
			completed = now
			nextAttempt = current.NextAttemptMS
		}
	}
	resultJSON := any(nil)
	if decision.State == appplatform.TaskSucceeded {
		resultJSON = string(decision.ResultJSON)
	}
	worker, leaseID, leaseUntil := any(nil), any(nil), any(nil)
	if decision.State == appplatform.TaskWaiting {
		worker, leaseID, leaseUntil = nil, nil, nil
	}
	update, err = e.exec(`UPDATE app_platform_tasks
		SET state=?,revision=revision+1,worker_id=?,lease_id=?,lease_until_ms=?,
		    next_attempt_ms=?,error_code=?,result_json=?,completed_ms=?,updated_ms=?
		WHERE id=? AND state='running' AND revision=? AND fence=? AND worker_id=? AND lease_id=?
		AND lease_until_ms>?`,
		decision.State, worker, leaseID, leaseUntil, nextAttempt,
		nullIfEmpty(decision.ErrorCode), resultJSON, completed, now, current.ID,
		current.Revision, current.Fence, current.WorkerID, current.LeaseID, now)
	if err != nil {
		return err
	}
	affected, err = update.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errAppPlatformTaskConflict
	}
	return tx.Commit()
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Server) markPlatformTaskUncertain(id, worker, leaseID string, fence, revision uint64, code string) error {
	if s == nil || s.st == nil || !platformValidUUID(id) || !platformValidUUID(worker) || !platformValidUUID(leaseID) || !taskErrorCode(code) {
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
	var state string
	if err := e.queryRow(`SELECT state FROM app_platform_tasks WHERE id=?`, id).Scan(&state); err != nil {
		return err
	}
	if state == "uncertain" || state == "review_required" {
		return tx.Commit()
	}
	if state != "leased" && state != "running" {
		return errAppPlatformTaskConflict
	}
	update, err := e.exec(`UPDATE app_platform_task_attempts
		SET state='uncertain',finished_ms=?,error_code=?,result_sha256=NULL,updated_ms=?
		WHERE task_id=? AND state IN ('leased','running') AND worker_id=? AND lease_id=? AND fence=?`,
		now, code, now, id, worker, leaseID, fence)
	if err != nil {
		return err
	}
	affected, err := update.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errAppPlatformTaskConflict
	}
	update, err = e.exec(`UPDATE app_platform_tasks
		SET state='uncertain',revision=revision+1,worker_id=NULL,lease_id=NULL,
		    lease_until_ms=NULL,error_code=?,result_json=NULL,updated_ms=?
		WHERE id=? AND state IN ('leased','running') AND revision=? AND fence=? AND worker_id=? AND lease_id=?`,
		code, now, id, revision, fence, worker, leaseID)
	if err != nil {
		return err
	}
	affected, err = update.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errAppPlatformTaskConflict
	}
	return tx.Commit()
}

func taskErrorCode(value string) bool {
	if len(value) < 1 || len(value) > 96 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, c := range []byte(value) {
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

func platformTaskDuration(attempt uint64) time.Duration {
	delay, err := appplatform.TaskBackoff(attempt)
	if err != nil {
		return 0
	}
	return delay
}
