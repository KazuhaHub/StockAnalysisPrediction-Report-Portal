package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Retention of the application platform's control records (DATA-08, doc 167 section 5, doc 172).
//
// The sweeper is bounded and conservative: it removes only rows no reader of the control tables still needs,
// and where it cannot tell it keeps the row. Every class is swept in its own transaction, under the lock the
// other platform mutations take, in batches of at most platformRetentionBatch rows.

const (
	// platformReviewPlanValidityMS is how long a review plan can be consumed after it was created.
	platformReviewPlanValidityMS = 15 * 60 * 1000
	// platformReviewPlanSweepGraceMS is how long an unconsumed plan stays after it expired before the sweep may remove it.
	platformReviewPlanSweepGraceMS = int64(24 * 60 * 60 * 1000)

	platformRetentionOperationDaysKey = "app_platform_retention_operation_days"
	platformRetentionRecoverySetsKey  = "app_platform_retention_recovery_sets"
	platformRetentionLastSweepKey     = "app_platform_retention_last_sweep_ms"
	platformRetentionBacklogKey       = "app_platform_retention_backlog"
	platformRetentionOperationDaysMin = 90
	platformRetentionOperationDaysMax = 365
	platformRetentionRecoverySetsMin  = 1
	platformRetentionRecoverySetsMax  = 2
	platformRetentionDayMS            = int64(24 * 60 * 60 * 1000)
	platformTaskRetentionDays         = 30
	platformTaskRowsPerInstallation   = 10000
	platformRetentionBatch            = 500
	platformRetentionInterval         = time.Hour
	platformRetentionClassTimeout     = 30 * time.Second
	platformRetentionSweepTimeout     = 5 * time.Minute
	platformRetentionRunnerLogMiB     = 10
)

// platformRetentionAuditActionName is the audit action of one sweep.
const platformRetentionAuditActionName = "app_platform.retention_sweep"

// platformRetentionConfig is the clamped, administrator-adjustable part of the policy.
type platformRetentionConfig struct {
	OperationDays int
	RecoverySets  int
}

// platformRetentionConfigLoad reads the two adjustable values and clamps them to their ranges on read, so a
// hand-edited meta value can never widen or shrink what the sweep removes beyond the documented bounds.
func (s *Server) platformRetentionConfigLoad() platformRetentionConfig {
	atoi := func(key string, def int) int {
		if n, err := strconv.Atoi(s.st.GetSetting(key, "")); err == nil {
			return n
		}
		return def
	}
	c := platformRetentionConfig{
		OperationDays: atoi(platformRetentionOperationDaysKey, platformRetentionOperationDaysMin),
		RecoverySets:  atoi(platformRetentionRecoverySetsKey, platformRetentionRecoverySetsMax),
	}
	c.OperationDays = min(max(c.OperationDays, platformRetentionOperationDaysMin), platformRetentionOperationDaysMax)
	c.RecoverySets = min(max(c.RecoverySets, platformRetentionRecoverySetsMin), platformRetentionRecoverySetsMax)
	return c
}

// errPlatformRetentionPaused is returned by a class that found the Portal in the restore pause inside its own transaction.
var errPlatformRetentionPaused = errors.New("application platform retention: restore pause")

// retentionClassTx runs one class in its own transaction with a deadline. lock takes the locks the writers of the
// records the class deletes take, in the order they take them, before the first read, so a decision to delete and
// the delete are made under the same locks and cannot be overtaken by an admission. The pause is re-read inside
// the transaction and fails closed, as the image reclaim does.
func (s *Server) retentionClassTx(ctx context.Context, lock func(migExec) error, fn func(migExec) error) error {
	for attempt := 0; ; attempt++ {
		err := s.retentionClassTxOnce(ctx, lock, fn)
		if err == nil || !isContentionErr(err) || attempt == 5 || ctx.Err() != nil {
			return err
		}
		time.Sleep(time.Duration(10<<attempt) * time.Millisecond)
	}
}

func (s *Server) retentionClassTxOnce(ctx context.Context, lock func(migExec) error, fn func(migExec) error) error {
	ctx, cancel := context.WithTimeout(ctx, platformRetentionClassTimeout)
	defer cancel()
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if lock != nil {
		if err := lock(e); err != nil {
			return err
		}
	}
	paused, err := platformRecoveryPaused(e)
	if err != nil {
		return err
	}
	if paused {
		return errPlatformRetentionPaused
	}
	if err := fn(e); err != nil {
		return err
	}
	return tx.Commit()
}

// lockRetentionOperations takes the locks operation admission takes, in its order: the OU mutation lock, then the
// publisher trust lock. The operation, effect and plan classes run under them.
func lockRetentionOperations(e migExec) error {
	if err := lockOUMutation(e); err != nil {
		return err
	}
	return lockPlatformPublisherTrust(e)
}

// platformRetentionLimits are the bounds of one sweep. A test injects small ones.
type platformRetentionLimits struct {
	// Batch is the most rows each class removes or reduces in one sweep.
	Batch int
	// TaskCap is the most terminal, unreduced tasks one installation keeps.
	TaskCap int
}

func defaultPlatformRetentionLimits() platformRetentionLimits {
	return platformRetentionLimits{Batch: platformRetentionBatch, TaskCap: platformTaskRowsPerInstallation}
}

// platformRetentionResult is what one sweep did. Its JSON form is the audit row's detail.
type platformRetentionResult struct {
	At     string `json:"-"`
	Paused bool   `json:"paused,omitempty"`
	Error  string `json:"error,omitempty"`
	// Backlog is true when a class stopped at its batch bound, so a following sweep may find more.
	Backlog bool `json:"backlog"`

	Plans            int64 `json:"plans"`
	Operations       int64 `json:"operations"`
	Effects          int64 `json:"effects"`
	OperationPlans   int64 `json:"operation_plans"`
	TasksReduced     int64 `json:"tasks_reduced"`
	TaskAttempts     int64 `json:"task_attempts"`
	TasksCapped      int64 `json:"tasks_capped"`
	Tombstones       int64 `json:"tombstones"`
	OperationDays    int   `json:"operation_days"`
	RecoverySetsKept int   `json:"recovery_sets_kept"`
	// RecoverySetsBeyond counts the recovery sets past the latest-N policy that are still waiting: prunePlatformRecoverySets
	// deletes them through the runner, so a set drops out of this count once its deletion is decided.
	RecoverySetsBeyond int64 `json:"recovery_sets_beyond"`
}

func (r platformRetentionResult) changed() int64 {
	return r.Plans + r.Operations + r.Effects + r.OperationPlans + r.TasksReduced + r.TaskAttempts + r.TasksCapped + r.Tombstones
}

// sweepPlatformRetention applies the retention policy as of nowMS. It does nothing while the Portal is in the
// restore pause and fails closed when the pause cannot be read. It writes one audit row per sweep that ran.
func (s *Server) sweepPlatformRetention(ctx context.Context, nowMS int64, limits platformRetentionLimits) (platformRetentionResult, error) {
	var result platformRetentionResult
	result.At = time.UnixMilli(nowMS).UTC().Format(time.RFC3339)
	paused, err := platformRecoveryPaused(s.st.poolExec())
	if err != nil {
		return result, err
	}
	if paused {
		result.Paused = true
		return result, nil
	}
	if limits.Batch < 1 {
		limits.Batch = platformRetentionBatch
	}
	if limits.TaskCap < 1 {
		limits.TaskCap = platformTaskRowsPerInstallation
	}
	cfg := s.platformRetentionConfigLoad()
	result.OperationDays, result.RecoverySetsKept = cfg.OperationDays, cfg.RecoverySets

	// Each class runs on its own: a failure is recorded and the others still run.
	steps := []func() error{
		func() (err error) { result.Plans, err = s.sweepRetentionPlans(ctx, nowMS, limits); return },
		func() error { return s.sweepRetentionOperations(ctx, nowMS, cfg, limits, &result) },
		func() error { return s.sweepRetentionTasks(ctx, nowMS, cfg, limits, &result) },
		func() error {
			return s.retentionClassTx(ctx, nil, func(e migExec) error {
				beyond, err := platformRecoverySetsBeyondRetention(e, cfg.RecoverySets)
				result.RecoverySetsBeyond = int64(len(beyond))
				return err
			})
		},
	}
	var failures []error
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		err := step()
		if errors.Is(err, errPlatformRetentionPaused) {
			result.Paused = true
			break
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	sweepErr := errors.Join(failures...)
	if result.Paused && sweepErr == nil && result.changed() == 0 {
		return result, nil
	}
	if result.Plans >= int64(limits.Batch) {
		result.Backlog = true
	}
	if sweepErr != nil {
		result.Error = sweepErr.Error()
	}
	detail, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if _, err := s.st.execContext(ctx, `INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform','retention',?,'')`,
		result.At, platformRetentionAuditActionName, string(detail)); err != nil && sweepErr == nil {
		sweepErr = err
	}
	return result, sweepErr
}

// sweepRetentionPlans removes unconsumed plans that expired more than the grace period ago and that nothing
// references. A consumed plan is never removed on its own: it goes with its operation.
func (s *Server) sweepRetentionPlans(ctx context.Context, nowMS int64, limits platformRetentionLimits) (int64, error) {
	var removed int64
	err := s.retentionClassTx(ctx, lockRetentionOperations, func(e migExec) error {
		res, err := e.exec(`DELETE FROM app_platform_review_plans WHERE id IN (
			SELECT p.id FROM app_platform_review_plans p
			WHERE p.consumed_ms IS NULL AND p.expires_ms<?
			AND NOT EXISTS (SELECT 1 FROM app_platform_operations o WHERE o.review_plan_id=p.id)
			AND NOT EXISTS (SELECT 1 FROM app_platform_capability_grants g WHERE g.review_plan_id=p.id)
			AND NOT EXISTS (SELECT 1 FROM app_platform_secret_bindings b WHERE b.review_plan_id=p.id)
			ORDER BY p.expires_ms,p.id LIMIT ?)`, nowMS-platformReviewPlanSweepGraceMS, limits.Batch)
		if err != nil {
			return err
		}
		removed, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// platformOperationSettled is when an operation was last resolved: the later of its completion and its last update.
// A recovery reconcile that resolves an operation later writes updated_ms and leaves completed_ms where it was, so
// the retention period counts from the actual resolution.
const platformOperationSettled = `(CASE WHEN o.updated_ms>o.completed_ms THEN o.updated_ms ELSE o.completed_ms END)`

// platformRetentionEligibleOperations selects, in completion order, the completed operations the SQL-expressible
// rules allow the sweep to remove. The rules are the pin rule of doc 172; the two that need parsed receipts (the
// image ledger and the checkpoint source operations) are applied by the caller from platformRetentionPins.
//
// An operation is removable only when it completed (stage complete or failed, never recovery_required) before
// the cutoff and none of these holds:
//   - one of its effects has no verdict (prepared or unknown);
//   - its installation has an unfinished operation or one in recovery_required, whose recovery readers walk the
//     installation's whole history;
//   - a recovery set row names it (the foreign key keeps it, and a retained set is read through it);
//   - a capability grant or secret binding names its review plan (the grant is read through plan and operation);
//   - its installation was removed but still has data that is not deleted (delete-data reads the install
//     operation and its effects), or the installation is live and the operation belongs to the chain the launch,
//     history and status readers use: a completed install, a completed operation of the installation's current
//     generation or later, the latest completed operation of its action, or the installation's latest
//     operation by update time.
const platformRetentionEligibleOperations = `SELECT o.id,o.installation_id,o.review_plan_id,` + platformOperationSettled + `
	FROM app_platform_operations o
	JOIN app_platform_installations i ON i.id=o.installation_id
	WHERE o.completed_ms IS NOT NULL AND ` + platformOperationSettled + `<?
	AND (` + platformOperationSettled + `>? OR (` + platformOperationSettled + `=? AND o.id>?))
	AND o.stage IN ('complete','failed') AND o.lease_owner IS NULL
	AND NOT EXISTS (SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.state IN ('prepared','unknown'))
	AND NOT EXISTS (SELECT 1 FROM app_platform_operations x WHERE x.installation_id=o.installation_id AND (x.completed_ms IS NULL OR x.stage='recovery_required'))
	AND NOT EXISTS (SELECT 1 FROM app_platform_recovery_sets r WHERE r.operation_id=o.id)
	AND NOT EXISTS (SELECT 1 FROM app_platform_capability_grants g WHERE g.review_plan_id=o.review_plan_id)
	AND NOT EXISTS (SELECT 1 FROM app_platform_secret_bindings b WHERE b.review_plan_id=o.review_plan_id)
	AND (
		(i.desired_state='removed' AND i.phase='removed' AND NOT EXISTS (
			SELECT 1 FROM app_platform_data_namespaces n
			WHERE (n.origin_installation_id=i.id OR n.owner_installation_id=i.id) AND n.state<>'deleted'))
		OR (
			(i.desired_state<>'removed' OR i.phase<>'removed')
			AND NOT (o.stage='complete' AND o.action='install')
			AND NOT (o.stage='complete' AND o.target_generation>=i.generation)
			AND (o.stage<>'complete' OR EXISTS (
				SELECT 1 FROM app_platform_operations n2
				WHERE n2.installation_id=o.installation_id AND n2.action=o.action AND n2.stage='complete'
				AND (n2.completed_ms>o.completed_ms OR (n2.completed_ms=o.completed_ms AND n2.id>o.id))))
			AND EXISTS (
				SELECT 1 FROM app_platform_operations n3
				WHERE n3.installation_id=o.installation_id
				AND (n3.updated_ms>o.updated_ms OR (n3.updated_ms=o.updated_ms AND n3.id>o.id)))
		)
	)
	ORDER BY ` + platformOperationSettled + `,o.id LIMIT ?`

type platformRetentionCandidate struct {
	id, installation, plan string
	completed              int64
}

// sweepRetentionOperations removes eligible completed operations together with their effects and plans. The pins,
// the candidates and the deletes are all read and written in one transaction under the admission locks.
func (s *Server) sweepRetentionOperations(ctx context.Context, nowMS int64, cfg platformRetentionConfig, limits platformRetentionLimits, result *platformRetentionResult) error {
	cutoff := nowMS - int64(cfg.OperationDays)*platformRetentionDayMS
	var effects, operations, plans int64
	backlog := false
	err := s.retentionClassTx(ctx, lockRetentionOperations, func(e migExec) error {
		effects, operations, plans, backlog = 0, 0, 0, false
		pins, err := loadPlatformRetentionPins(e)
		if err != nil {
			return err
		}
		var chosen []platformRetentionCandidate
		afterSettled, afterID := int64(-1), ""
		for len(chosen) < limits.Batch {
			page, err := loadRetentionCandidates(e, cutoff, afterSettled, afterID, limits.Batch)
			if err != nil {
				return err
			}
			for _, c := range page {
				if !pins.ops[c.id] && !pins.installations[c.installation] && len(chosen) < limits.Batch {
					chosen = append(chosen, c)
				}
			}
			if len(page) < limits.Batch {
				break
			}
			afterSettled, afterID = page[len(page)-1].completed, page[len(page)-1].id
		}
		chosen = withholdUnclosedImageRemovals(chosen, pins.closedImages)
		for _, c := range chosen {
			res, err := e.exec(`DELETE FROM app_platform_operation_effects WHERE operation_id=?`, c.id)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			effects += n
			if _, err := e.exec(`DELETE FROM app_platform_operations WHERE id=?`, c.id); err != nil {
				return err
			}
			operations++
			// Its plan goes with it unless a grant or binding still names the plan.
			res, err = e.exec(`DELETE FROM app_platform_review_plans WHERE id=? AND consumed_ms IS NOT NULL
				AND NOT EXISTS (SELECT 1 FROM app_platform_operations o WHERE o.review_plan_id=?)
				AND NOT EXISTS (SELECT 1 FROM app_platform_capability_grants g WHERE g.review_plan_id=?)
				AND NOT EXISTS (SELECT 1 FROM app_platform_secret_bindings b WHERE b.review_plan_id=?)`, c.plan, c.plan, c.plan, c.plan)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
			plans += n
		}
		backlog = len(chosen) >= limits.Batch
		return nil
	})
	if err != nil {
		return err
	}
	result.Effects, result.Operations, result.OperationPlans = effects, operations, plans
	result.Backlog = result.Backlog || backlog
	return nil
}

// withholdUnclosedImageRemovals keeps a closed image record whole or not at all. The load side of a record is
// the operations holding its applied loads and the removal side those holding its applied removals. While any
// load-side operation is not being deleted (pinned by the live chain, too young, or past the batch), the removals
// stay, because a load without its removal reads as an image still loaded and reclaim would remove it again. A
// removal withheld may in turn be the load side of another record, so this repeats until nothing changes.
func withholdUnclosedImageRemovals(chosen []platformRetentionCandidate, records []platformClosedImageRecord) []platformRetentionCandidate {
	for {
		deleting := map[string]bool{}
		for _, c := range chosen {
			deleting[c.id] = true
		}
		withheld := map[string]bool{}
		for _, r := range records {
			whole := true
			for op := range r.loads {
				if !deleting[op] {
					whole = false
					break
				}
			}
			if whole {
				continue
			}
			for op := range r.removals {
				if deleting[op] {
					withheld[op] = true
				}
			}
		}
		if len(withheld) == 0 {
			return chosen
		}
		kept := chosen[:0:0]
		for _, c := range chosen {
			if !withheld[c.id] {
				kept = append(kept, c)
			}
		}
		chosen = kept
	}
}

func loadRetentionCandidates(e migExec, cutoff, afterCompleted int64, afterID string, limit int) ([]platformRetentionCandidate, error) {
	rows, err := e.query(platformRetentionEligibleOperations, cutoff, afterCompleted, afterCompleted, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []platformRetentionCandidate
	for rows.Next() {
		var c platformRetentionCandidate
		if err := rows.Scan(&c.id, &c.installation, &c.plan, &c.completed); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// platformRetentionPins are the operations the SQL rules cannot see a reason to keep: the image ledger and the
// source operations of retained checkpoints. installations holds the installations whose evidence could not be
// read, which keep every operation.
type platformRetentionPins struct {
	ops           map[string]bool
	installations map[string]bool
	// closedImages are the image records whose latest load a removal has followed. They pin nothing themselves;
	// they are deleted whole or not at all (withholdUnclosedImageRemovals).
	closedImages []platformClosedImageRecord
}

type platformClosedImageRecord struct {
	loads, removals map[string]bool
}

func loadPlatformRetentionPins(e migExec) (platformRetentionPins, error) {
	pins := platformRetentionPins{ops: map[string]bool{}, installations: map[string]bool{}}
	if err := pinImageLedger(e, &pins); err != nil {
		return pins, err
	}
	if err := pinRetainedCheckpointSources(e, pins); err != nil {
		return pins, err
	}
	if err := pinCheckpointDeletions(e, pins); err != nil {
		return pins, err
	}
	return pins, nil
}

// pinCheckpointDeletions keeps the operation that hosts a recorded checkpoint deletion (pin 9). The effect row is the
// Portal's only record that the runner retired a checkpoint. Recovery set rows are never deleted (foreign keys name
// them), so a set that was deleted stays in the table for ever, and the sweeper that finds sets of a deleted namespace
// (platformDeletedNamespaceSets) would select it again the day the effect row went, and send a deletion the runner can
// only answer "absent" to, every retention period. So an operation holding an applied deletion stays while its set row
// exists. A refused deletion is kept while its set is not deleted: its row is the attempt count, and an attempt number
// that was forgotten would sign again an effect id the runner already settled as a refusal. A row whose receipt names
// no set is kept, because nothing can say what it was for.
func pinCheckpointDeletions(e migExec, pins platformRetentionPins) error {
	rows, err := e.query(`SELECT id,state FROM app_platform_recovery_sets`)
	if err != nil {
		return err
	}
	sets := map[string]string{}
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			rows.Close()
			return err
		}
		sets[id] = state
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	effects, err := e.query(`SELECT f.operation_id,f.state,f.receipt_json FROM app_platform_operation_effects f WHERE f.kind=? AND f.state IN ('applied','reverted')`, platformDeleteCheckpointKind)
	if err != nil {
		return err
	}
	defer effects.Close()
	for effects.Next() {
		var op, state, raw string
		if err := effects.Scan(&op, &state, &raw); err != nil {
			return err
		}
		var named struct {
			RecoverySetID string `json:"recovery_set_id"`
		}
		if json.Unmarshal([]byte(raw), &named) != nil {
			pins.ops[op] = true
			continue
		}
		setState, exists := sets[named.RecoverySetID]
		switch {
		case !exists && named.RecoverySetID != "":
			// Its set row is gone: nothing can select the set again.
		case state == "applied", setState != "deleted":
			pins.ops[op] = true
		}
	}
	return effects.Err()
}

// pinImageLedger keeps the operations whose load and removal effects are the Portal's record of what is on a
// runner (platformLoadedImages). The record of an image is closed once an applied removal is at least as recent
// as its latest applied load, and then it is no longer read: those effects may go. An image still loaded, an
// effect whose receipt cannot be read, and every image of a runtime that holds an effect with no verdict keep
// all of their effects, because the removal sends the known loads to the runner and the runtime is left out of
// reclaim as a whole while a verdict is missing.
func pinImageLedger(e migExec, pins *platformRetentionPins) error {
	rows, err := e.query(`SELECT f.kind,f.state,COALESCE(f.runtime_id,''),f.operation_id,f.updated_ms,f.receipt_json
		FROM app_platform_operation_effects f WHERE f.kind IN ('load_image','remove_image') ORDER BY f.updated_ms,f.effect_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type key struct{ runtime, digest string }
	type record struct {
		loads, removals       map[string]bool
		hasLoad               bool
		latestLoad, removedAt int64
	}
	records := map[key]*record{}
	runtimeOps := map[string]map[string]bool{}
	busy := map[string]bool{}
	touch := func(runtime, op string) {
		if runtimeOps[runtime] == nil {
			runtimeOps[runtime] = map[string]bool{}
		}
		runtimeOps[runtime][op] = true
	}
	for rows.Next() {
		var kind, state, runtime, op, receipt string
		var updated int64
		if err := rows.Scan(&kind, &state, &runtime, &op, &updated, &receipt); err != nil {
			return err
		}
		switch {
		case state == "prepared" || state == "unknown":
			busy[runtime] = true
			touch(runtime, op)
		case state != "applied":
			// A reverted effect is read by nothing.
		case kind == platformLoadImageKind:
			touch(runtime, op)
			parsed, err := journal.ParseLoadImageReceipt([]byte(receipt))
			if err != nil {
				pins.ops[op] = true
				continue
			}
			k := key{runtime, parsed.Claim.ConfigDigest}
			if records[k] == nil {
				records[k] = &record{loads: map[string]bool{}, removals: map[string]bool{}}
			}
			records[k].loads[op], records[k].hasLoad = true, true
			records[k].latestLoad = max(records[k].latestLoad, updated)
		case kind == platformRemoveImageKind:
			touch(runtime, op)
			var settled journal.RemoveImageReceipt
			if json.Unmarshal([]byte(receipt), &settled) != nil || settled.Claim.ConfigDigest == "" {
				pins.ops[op] = true
				continue
			}
			k := key{runtime, settled.Claim.ConfigDigest}
			if records[k] == nil {
				records[k] = &record{loads: map[string]bool{}, removals: map[string]bool{}}
			}
			records[k].removals[op] = true
			records[k].removedAt = max(records[k].removedAt, updated)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range records {
		switch {
		case !r.hasLoad:
			// A removal with no load is read by nothing.
		case r.removedAt < r.latestLoad:
			for op := range r.loads {
				pins.ops[op] = true
			}
			for op := range r.removals {
				pins.ops[op] = true
			}
		default:
			pins.closedImages = append(pins.closedImages, platformClosedImageRecord{loads: r.loads, removals: r.removals})
		}
	}
	for runtime := range busy {
		for op := range runtimeOps[runtime] {
			pins.ops[op] = true
		}
	}
	return nil
}

// pinRetainedCheckpointSources keeps what a retained recovery set is restored from: the operation that cut it
// (the foreign key), and the source operation and source data operation its snapshot receipt names, which the
// restore and update-history readers load by id. A retained set whose receipt cannot be read pins its whole
// installation.
func pinRetainedCheckpointSources(e migExec, pins platformRetentionPins) error {
	rows, err := e.query(`SELECT id,installation_id,operation_id FROM app_platform_recovery_sets WHERE state<>'deleted'`)
	if err != nil {
		return err
	}
	type set struct{ id, installation, operation string }
	var sets []set
	for rows.Next() {
		var s set
		if err := rows.Scan(&s.id, &s.installation, &s.operation); err != nil {
			rows.Close()
			return err
		}
		sets = append(sets, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, s := range sets {
		pins.ops[s.operation] = true
		var raw string
		err := e.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot' AND effect_number=1 AND state='applied'`, s.operation).Scan(&raw)
		var receipt journal.CheckpointReceipt
		if err != nil || json.Unmarshal([]byte(raw), &receipt) != nil || !platformValidUUID(receipt.SourceOperationID) || !platformValidUUID(receipt.SourceDataEffectID) {
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			pins.installations[s.installation] = true
			continue
		}
		pins.ops[receipt.SourceOperationID] = true
		var dataOperation string
		if err := e.queryRow(`SELECT operation_id FROM app_platform_operation_effects WHERE effect_id=?`, receipt.SourceDataEffectID).Scan(&dataOperation); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			pins.installations[s.installation] = true
			continue
		}
		pins.ops[dataOperation] = true
		if platformValidUUID(receipt.SourceStartEffectID) {
			var startOperation string
			if err := e.queryRow(`SELECT operation_id FROM app_platform_operation_effects WHERE effect_id=?`, receipt.SourceStartEffectID).Scan(&startOperation); err == nil {
				pins.ops[startOperation] = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
	}
	return nil
}

const platformTaskTerminal = `('succeeded','failed','cancelled')`

func platformTaskUnreduced(driver string) string {
	return `(` + platformByteLength("t.payload", driver) + `>0 OR t.result_json IS NOT NULL OR EXISTS (SELECT 1 FROM app_platform_task_attempts a WHERE a.task_id=t.id))`
}

func platformEmptyBlob(driver string) string {
	if driver == "postgres" {
		return `decode('','hex')`
	}
	return `X''`
}

// sweepRetentionTasks reduces old and surplus terminal tasks to occurrence tombstones and removes tombstones past
// the operations period. A tombstone keeps occurrence_id, payload_sha256 and handler, the three columns the
// admission dedup reads, so a replayed occurrence is still recognised and a different payload still conflicts.
// Tasks that have not finished, or are uncertain or in review, are never touched.
func (s *Server) sweepRetentionTasks(ctx context.Context, nowMS int64, cfg platformRetentionConfig, limits platformRetentionLimits, result *platformRetentionResult) error {
	driver := s.st.driver
	unreduced := platformTaskUnreduced(driver)
	var reduced, attempts, capped, tombstones int64
	backlog := false
	// The task admission lock is the only one task admission takes, so it is the only one taken here.
	err := s.retentionClassTx(ctx, lockPlatformTaskAdmission, func(e migExec) error {
		reduced, attempts, capped, tombstones, backlog = 0, 0, 0, 0, false
		reduce := func(ids []string) (int64, error) {
			var done int64
			for _, id := range ids {
				res, err := e.exec(`DELETE FROM app_platform_task_attempts WHERE task_id=?`, id)
				if err != nil {
					return done, err
				}
				n, _ := res.RowsAffected()
				attempts += n
				if _, err := e.exec(`UPDATE app_platform_tasks SET payload=`+platformEmptyBlob(driver)+`,result_json=NULL WHERE id=? AND state IN `+platformTaskTerminal, id); err != nil {
					return done, err
				}
				done++
			}
			return done, nil
		}
		ids, err := queryRetentionIDs(e, `SELECT t.id FROM app_platform_tasks t WHERE t.state IN `+platformTaskTerminal+` AND t.completed_ms<? AND `+unreduced+` ORDER BY t.completed_ms,t.id LIMIT ?`,
			nowMS-platformTaskRetentionDays*platformRetentionDayMS, limits.Batch)
		if err != nil {
			return err
		}
		if reduced, err = reduce(ids); err != nil {
			return err
		}
		if len(ids) >= limits.Batch {
			backlog = true
		}

		type surplus struct {
			installation string
			count        int64
		}
		rows, err := e.query(`SELECT t.installation_id,COUNT(*) FROM app_platform_tasks t WHERE t.state IN `+platformTaskTerminal+` AND `+unreduced+` GROUP BY t.installation_id HAVING COUNT(*)>? ORDER BY t.installation_id`, limits.TaskCap)
		if err != nil {
			return err
		}
		var crowded []surplus
		for rows.Next() {
			var c surplus
			if err := rows.Scan(&c.installation, &c.count); err != nil {
				rows.Close()
				return err
			}
			crowded = append(crowded, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		remaining := int64(limits.Batch)
		for _, c := range crowded {
			take := min(c.count-int64(limits.TaskCap), remaining)
			if take <= 0 {
				backlog = true
				break
			}
			ids, err := queryRetentionIDs(e, `SELECT t.id FROM app_platform_tasks t WHERE t.installation_id=? AND t.state IN `+platformTaskTerminal+` AND `+unreduced+` ORDER BY t.completed_ms,t.id LIMIT ?`, c.installation, take)
			if err != nil {
				return err
			}
			n, err := reduce(ids)
			if err != nil {
				return err
			}
			capped += n
			remaining -= n
			if take < c.count-int64(limits.TaskCap) {
				backlog = true
			}
		}

		ids, err = queryRetentionIDs(e, `SELECT t.id FROM app_platform_tasks t WHERE t.state IN `+platformTaskTerminal+` AND t.completed_ms<? AND NOT `+unreduced+` ORDER BY t.completed_ms,t.id LIMIT ?`,
			nowMS-int64(cfg.OperationDays)*platformRetentionDayMS, limits.Batch)
		if err != nil {
			return err
		}
		for _, id := range ids {
			res, err := e.exec(`DELETE FROM app_platform_tasks WHERE id=? AND state IN `+platformTaskTerminal, id)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			tombstones += n
		}
		if len(ids) >= limits.Batch {
			backlog = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	result.TasksReduced, result.TaskAttempts, result.TasksCapped, result.Tombstones = reduced, attempts, capped, tombstones
	result.Backlog = result.Backlog || backlog
	return nil
}

func queryRetentionIDs(e migExec, query string, args ...any) ([]string, error) {
	rows, err := e.query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// platformBeyondSet is a retained recovery set past the latest keep of its installation.
type platformBeyondSet struct {
	id, installation, state string
	created                 int64
}

// platformBeyondRetention lists the retained recovery sets past the latest keep of their installation, leaving out
// any that is held: by an import grant that is issued or consumed, or by a restore in progress. The count of "latest
// keep" includes every retained state, so a review_required set takes a place; the caller decides which states it
// may act on. A set whose deletion is decided is not retained and is not listed (platformRetainedRecoverySetsOf).
func platformBeyondRetention(e migExec, keep int) ([]platformBeyondSet, error) {
	rows, err := e.query(`SELECT r.id,r.installation_id,r.state,r.created_ms,
		EXISTS (SELECT 1 FROM app_platform_import_grants g WHERE g.recovery_set_id=r.id AND g.state IN ('issued','consumed'))
		FROM app_platform_recovery_sets r WHERE ` + platformRetainedRecoverySetsOf("r") + ` ORDER BY r.installation_id,r.created_ms DESC,r.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []platformBeyondSet
	var current string
	position := 0
	for rows.Next() {
		var b platformBeyondSet
		var granted bool
		if err := rows.Scan(&b.id, &b.installation, &b.state, &b.created, &granted); err != nil {
			return nil, err
		}
		if b.installation != current {
			current, position = b.installation, 0
		}
		position++
		if position > keep && b.state != "restoring" && !granted {
			out = append(out, b)
		}
	}
	return out, rows.Err()
}

// platformRecoverySetsBeyondRetention lists the ids of platformBeyondRetention. The retention sweep reports them;
// prunePlatformRecoverySets acts on the verified ones (doc 172 section 6).
func platformRecoverySetsBeyondRetention(e migExec, keep int) ([]string, error) {
	beyond, err := platformBeyondRetention(e, keep)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, b := range beyond {
		out = append(out, b.id)
	}
	return out, nil
}

// platformRetainedRecoverySetsOf is the retained-set predicate of the recovery bundle, qualified by an alias. A set
// whose deletion is decided (a delete_checkpoint effect that is prepared, unknown or applied) is not retained: the
// runner's claim cannot be undone, so the bundle must not require an archive that is going away.
func platformRetainedRecoverySetsOf(alias string) string {
	return alias + `.state IN ('verified','restoring','review_required') AND ` + alias + `.deleted_ms IS NULL AND ` + alias + `.manifest_sha256 IS NOT NULL AND NOT ` + platformDeletionDecidedSQL(alias)
}

// platformRetentionTick runs the sweep from the cleanup ticker. It waits platformRetentionInterval between sweeps
// unless the last one stopped at a batch bound, in which case the next tick continues. A Portal that has no
// application platform records has nothing to sweep and writes no audit row.
func (s *Server) platformRetentionTick(now time.Time) {
	last, _ := strconv.ParseInt(s.st.GetSetting(platformRetentionLastSweepKey, ""), 10, 64)
	backlog := s.st.GetSetting(platformRetentionBacklogKey, "") == "1"
	if last != 0 && !backlog && now.Sub(time.UnixMilli(last)) < platformRetentionInterval {
		return
	}
	var present int
	if err := s.st.queryRow(`SELECT (SELECT COUNT(*) FROM (SELECT 1 FROM app_platform_review_plans LIMIT 1) p)+(SELECT COUNT(*) FROM (SELECT 1 FROM app_platform_tasks LIMIT 1) t)`).Scan(&present); err != nil || present == 0 {
		if err != nil {
			log.Printf("app-platform retention: %v", err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), platformRetentionSweepTimeout)
	defer cancel()
	result, err := s.sweepPlatformRetention(ctx, now.UnixMilli(), defaultPlatformRetentionLimits())
	if result.Paused {
		return
	}
	if err != nil {
		log.Printf("app-platform retention: %v", err)
	}
	s.st.SetSetting(platformRetentionLastSweepKey, strconv.FormatInt(now.UnixMilli(), 10))
	if result.Backlog {
		s.st.SetSetting(platformRetentionBacklogKey, "1")
	} else {
		s.st.SetSetting(platformRetentionBacklogKey, "0")
	}
}
