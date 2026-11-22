package app

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// Accepting the data a failed import left (doc 185, recovery).
//
// An import, enable or repair whose start did not run ends recovery_required, and an import also leaves the rollback it
// was about to take as a prepared effect the runner never admitted. Nothing retries that rollback: it runs only from
// the live operation, which is over. The operation stays unresolved all the same, because the only things the Portal
// counts as resolving a recovery_required operation are a completed restore of its checkpoint (which would put the
// pre-import data back) and, for an update, a completed retry. This action is the third way: an administrator
// reviews the state and accepts the data as it now is.
//
// It issues no runner command. What it rests on:
//
//   - the Portal's own rows say the installation is disabled in recovery_required, that exactly one operation of it is
//     unresolved, that the operation is a managed import, enable or repair of the release the installation runs, and
//     that every effect of it is settled except the prepared, never-observed rollback restore;
//   - the runner says it never admitted that rollback: a signed read of the very restore command comes back as the
//     typed absence, once no command signed for it can still arrive. The runner's other answers (a receipt, pending,
//     orphaned, unreachable) refuse the action.
//
// Its effect is two durable facts in rows that already exist: each abandoned effect is closed reverted with a receipt that
// says so and why, and the operation gets the same "recovered" marker effect a completed update retry writes, which is
// what the launch and settings predicates read as resolved. The installation keeps the phase it is in, recovery_required:
// the way forward is a reviewed repair, which is why the review says so. The data namespace, its recovery sets and its
// checkpoints are not touched, so the pre-import checkpoint stays available for a later reviewed restore.
//
// No operation is recorded: an operation row carries an action the source loaders would read as a lifecycle step.
// The action is a review plan, consumed once, and one audit entry.
const (
	platformResolveKeepDataFormat = "portal.recovery-resolve-keep-data-review/1-dev.1"
	platformResolveKeepDataAudit  = "app_platform.recovery_resolved_keep_data"
	platformResolveKeepDataMeta   = "app_platform_resolve_keep_data:"
	platformAbandonedReceipt      = `{"state":"abandoned","reason":"resolve_keep_data"}`
)

var errPlatformResolveKeepDataUnavailable = errors.New("no failed operation of this installation can be resolved by keeping its data")

type platformResolveKeepDataEffect struct {
	OperationID string `json:"operation_id"`
	Action      string `json:"operation_action"`
	CompletedMS int64  `json:"operation_completed_ms"`
	EffectID    string `json:"effect_id"`
	Stage       string `json:"stage"`
	Number      int64  `json:"effect_number"`
	Kind        string `json:"kind"`
	// RecoverySetID is the 'preparing' recovery set a never-admitted snapshot left, closed with the effect.
	RecoverySetID string `json:"recovery_set_id,omitempty"`
}

type platformResolveKeepDataPlan struct {
	Format               string                          `json:"format"`
	InstallationID       string                          `json:"installation_id"`
	AppID                string                          `json:"app_id"`
	PublisherID          string                          `json:"publisher_id"`
	OperationID          string                          `json:"operation_id"`
	OperationAction      string                          `json:"operation_action"`
	OperationErrorCode   string                          `json:"operation_error_code"`
	ReleaseSHA256        string                          `json:"release_sha256"`
	DataNamespaceID      string                          `json:"data_namespace_id"`
	ObservedBytes        int64                           `json:"observed_bytes"`
	CheckpointRecoverySt string                          `json:"checkpoint_recovery_set_id,omitempty"`
	AbandonedEffects     []platformResolveKeepDataEffect `json:"abandoned_effects"`
	InstallationRevision int64                           `json:"installation_revision"`
	PolicyRevision       int64                           `json:"policy_revision"`
	TrustRevision        int64                           `json:"trust_revision"`
	Generation           int64                           `json:"generation"`
	ControllerEpoch      string                          `json:"controller_epoch"`
	OperationFence       int64                           `json:"operation_fence"`
	CompletedMS          int64                           `json:"operation_completed_ms"`
}

func platformPlanIsResolveKeepData(planRaw string) bool {
	var probe struct {
		Format string `json:"format"`
	}
	return json.Unmarshal([]byte(planRaw), &probe) == nil && probe.Format == platformResolveKeepDataFormat
}

// loadPlatformResolveKeepDataPlan rebuilds the review's facts from the Portal's rows. It changes nothing and asks nobody.
func loadPlatformResolveKeepDataPlan(e migExec, installation string, control platformActorControl) (platformResolveKeepDataPlan, error) {
	var p platformResolveKeepDataPlan
	if !platformValidUUID(installation) {
		return p, errAppPlatformPlanChanged
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var desired, phase string
	var runtime sql.NullString
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,generation,revision,policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`+lock, installation).
		Scan(&p.AppID, &p.PublisherID, &desired, &phase, &p.Generation, &p.InstallationRevision, &p.PolicyRevision, &p.ControllerEpoch, &runtime); err != nil {
		return p, errAppPlatformPlanChanged
	}
	if desired != "disabled" || phase != "recovery_required" || p.ControllerEpoch != control.epoch || !runtime.Valid ||
		p.InstallationRevision == math.MaxInt64 {
		return p, errPlatformResolveKeepDataUnavailable
	}
	var paused string
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_restore_paused'`).Scan(&paused); err != nil || paused != "0" {
		return p, errAppPlatformControlPaused
	}
	var enabled int
	if err := e.queryRow(`SELECT enabled FROM app_platform_runtime_bindings WHERE id=?`, runtime.String).Scan(&enabled); err != nil || enabled == 0 {
		return p, errPlatformResolveKeepDataUnavailable
	}
	// Nothing of the installation is in flight, and exactly one operation of it is unresolved.
	var inFlight int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, installation).Scan(&inFlight); err != nil {
		return p, err
	}
	if inFlight != 0 {
		return p, errAppPlatformOperationConflict
	}
	unresolved, err := platformUnresolvedOperationCount(e, installation)
	if err != nil {
		return p, err
	}
	if unresolved != 1 {
		return p, errPlatformResolveKeepDataUnavailable
	}
	var stage, errorCode, plan string
	var managed int64
	var completed sql.NullInt64
	var owner sql.NullString
	var planRelease, planTarget sql.NullString
	if err := e.queryRow(`SELECT o.id,o.action,o.stage,o.managed,o.fence,o.error_code,o.completed_ms,o.lease_owner,p.source_release_sha256,p.target_release_sha256,p.plan_json
		FROM app_platform_operations o
		JOIN app_platform_installations i ON i.id=o.installation_id
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND `+platformUnresolvedOperationPredicate(e), installation).
		Scan(&p.OperationID, &p.OperationAction, &stage, &managed, &p.OperationFence, &errorCode, &completed, &owner, &planRelease, &planTarget, &plan); err != nil {
		return p, errPlatformResolveKeepDataUnavailable
	}
	action := appplatform.Action(p.OperationAction)
	if stage != string(appplatform.StageRecoveryRequired) || managed != 1 || !completed.Valid || owner.Valid ||
		!platformSameReleaseLifecycle(action) || !planRelease.Valid || !planTarget.Valid || planRelease.String != planTarget.String {
		return p, errPlatformResolveKeepDataUnavailable
	}
	p.OperationErrorCode, p.CompletedMS, p.ReleaseSHA256 = errorCode, completed.Int64, planRelease.String
	// The operation is of the release the installation runs: its retired activation, since it is disabled.
	var active int
	var retired string
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state<>'retired'`, installation).Scan(&active); err != nil {
		return p, err
	}
	if err := e.queryRow(`SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='retired' ORDER BY generation DESC,created_ms DESC LIMIT 1`, installation).Scan(&retired); err != nil {
		return p, errPlatformResolveKeepDataUnavailable
	}
	if active != 0 || retired != p.ReleaseSHA256 {
		return p, errPlatformResolveKeepDataUnavailable
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers WHERE id=?`, p.PublisherID).Scan(&p.TrustRevision); err != nil || p.TrustRevision < 1 {
		return p, errAppPlatformPlanChanged
	}
	// Every effect of the operation is settled, except a rollback restore the Portal prepared and never observed.
	rows, err := e.query(`SELECT stage,effect_number,effect_id,kind,state,observed_fence,receipt_json FROM app_platform_operation_effects WHERE operation_id=? ORDER BY stage,effect_number`, p.OperationID)
	if err != nil {
		return p, err
	}
	p.AbandonedEffects = []platformResolveKeepDataEffect{}
	for rows.Next() {
		f := platformResolveKeepDataEffect{OperationID: p.OperationID, Action: p.OperationAction, CompletedMS: p.CompletedMS}
		var state, receipt string
		var observed int64
		if err := rows.Scan(&f.Stage, &f.Number, &f.EffectID, &f.Kind, &state, &observed, &receipt); err != nil {
			rows.Close()
			return p, err
		}
		switch state {
		case "applied", "reverted":
		case "prepared", "unknown":
			if state != "prepared" || f.Stage != "import" || f.Number != platformImportRollbackEffectNumber || f.Kind != "restore" || observed != 0 || receipt != "{}" {
				rows.Close()
				return p, errAppPlatformOperationConflict
			}
			p.AbandonedEffects = append(p.AbandonedEffects, f)
		default:
			rows.Close()
			return p, errAppPlatformOperationConflict
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return p, err
	}
	rows.Close()
	// An earlier attempt that failed before it did anything may have left its snapshot prepared and never admitted,
	// with the recovery set it opened for it. Nothing else of such an operation may be unsettled.
	failed, err := e.query(`SELECT o.id,o.action,o.completed_ms,p.source_release_sha256,p.target_release_sha256
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.stage='failed' AND o.managed=1 AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL
		AND o.action IN ('import_data','enable','repair') ORDER BY o.completed_ms,o.id`, installation)
	if err != nil {
		return p, err
	}
	type earlier struct {
		id, action, source, target string
		completed                  int64
	}
	var earlierOperations []earlier
	for failed.Next() {
		var o earlier
		var source, target sql.NullString
		if err := failed.Scan(&o.id, &o.action, &o.completed, &source, &target); err != nil {
			failed.Close()
			return p, err
		}
		o.source, o.target = source.String, target.String
		earlierOperations = append(earlierOperations, o)
	}
	if err := failed.Err(); err != nil {
		failed.Close()
		return p, err
	}
	failed.Close()
	for _, o := range earlierOperations {
		effects, err := e.query(`SELECT stage,effect_number,effect_id,kind,state,observed_fence,receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND state IN ('prepared','unknown')`, o.id)
		if err != nil {
			return p, err
		}
		var found []platformResolveKeepDataEffect
		for effects.Next() {
			f := platformResolveKeepDataEffect{OperationID: o.id, Action: o.action, CompletedMS: o.completed}
			var state, receipt string
			var observed int64
			if err := effects.Scan(&f.Stage, &f.Number, &f.EffectID, &f.Kind, &state, &observed, &receipt); err != nil {
				effects.Close()
				return p, err
			}
			if state != "prepared" || f.Kind != "snapshot" || f.Number != 1 || f.Stage != platformSameReleaseCheckpointEffectStage(appplatform.Action(o.action)) ||
				observed != 0 || receipt != "{}" || o.source != p.ReleaseSHA256 || o.target != p.ReleaseSHA256 {
				effects.Close()
				return p, errAppPlatformOperationConflict
			}
			found = append(found, f)
		}
		if err := effects.Err(); err != nil {
			effects.Close()
			return p, err
		}
		effects.Close()
		for _, f := range found {
			var set sql.NullString
			if err := e.queryRow(`SELECT id FROM app_platform_recovery_sets WHERE operation_id=? AND state='preparing' AND manifest_sha256 IS NULL AND bytes=0`, o.id).Scan(&set); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return p, err
			}
			f.RecoverySetID = set.String
			p.AbandonedEffects = append(p.AbandonedEffects, f)
		}
	}
	// No other unsettled effect of the installation is counted: they would keep it unsafe after this action.
	unsafe, err := platformUnresolvedOperationEffectCount(e, installation)
	if err != nil {
		return p, err
	}
	if unsafe != len(p.AbandonedEffects) {
		return p, errAppPlatformOperationConflict
	}
	var namespaceState string
	if err := e.queryRow(`SELECT id,state,observed_bytes FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation).Scan(&p.DataNamespaceID, &namespaceState, &p.ObservedBytes); err != nil || namespaceState != "active" {
		return p, errPlatformResolveKeepDataUnavailable
	}
	var set sql.NullString
	if err := e.queryRow(`SELECT id FROM app_platform_recovery_sets WHERE operation_id=? AND state='verified'`, p.OperationID).Scan(&set); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.CheckpointRecoverySt = set.String
	p.Format, p.InstallationID = platformResolveKeepDataFormat, installation
	return p, nil
}

// platformResolveKeepDataQuiet is the time after which no command the Portal signed for the failed operation can still
// arrive at the runner.
func platformResolveKeepDataQuiet(completed int64) int64 {
	return completed + platformSigningSlackMS + platformStartCommandWindowMS + platformImportAbsenceMarginMS + control.CommandClockSkewMS
}

// signPlatformImportRollbackRead rebuilds the rollback restore of a failed import, the very command the effect row
// records, and signs it for a read and for nothing else.
func (s *Server) signPlatformImportRollbackRead(ctx context.Context, operation string) (platformRestoreIntent, error) {
	var zero platformRestoreIntent
	dek, err := s.dek()
	if err != nil {
		return zero, err
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return zero, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	lease, err := platformSettlementLease(e, operation)
	if err != nil {
		return zero, err
	}
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	now, err := platformSettlementGuard(e, lease)
	if err != nil {
		return zero, err
	}
	source, err := loadPlatformCheckpointSourceMode(e, lease, runtime, now, true, 0)
	if err != nil {
		return zero, err
	}
	plan, _, err := loadPlatformManagedUpdatePlan(e, lease, source)
	if err != nil || plan.Import == nil || plan.RecoverySetID == "" {
		return zero, errors.Join(errAppPlatformStageEvidence, err)
	}
	checkpoint, err := loadPlatformImportCheckpoint(e, lease, plan)
	if err != nil {
		return zero, err
	}
	var effectID, kind, digest, state, receipt string
	var admitted, observed int64
	if err := e.queryRow(`SELECT effect_id,kind,payload_sha256,admitted_fence,observed_fence,state,receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='import' AND effect_number=?`, operation, platformImportRollbackEffectNumber).
		Scan(&effectID, &kind, &digest, &admitted, &observed, &state, &receipt); err != nil ||
		kind != "restore" || state != "prepared" || observed != 0 || receipt != "{}" {
		return zero, errAppPlatformStageEvidence
	}
	effectLease := lease
	effectLease.fence = admitted
	command, err := platformImportRollbackCommand(effectLease, runtime, source, checkpoint, plan.RecoverySetID, effectID)
	if err != nil || command.PayloadSHA256 != digest {
		return zero, errors.Join(errAppPlatformStageEvidence, err)
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, errAppPlatformLeaseConflict
	}
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(issued+platformStartCommandWindowMS, 10)
	authority := platformRuntimeAuthority(runtime)
	intent := platformRestoreIntent{recoverySetID: plan.RecoverySetID, effectID: effectID, runtime: runtime, authority: authority, command: command, checkpoint: checkpoint, source: source}
	if intent.raw, err = json.Marshal(command); err != nil {
		return zero, err
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	if intent.readSignature, err = control.SignRestoreRead(intent.raw, key, authority, issued); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return intent, nil
}

// proveRollbackNotAdmitted asks the runner about each abandoned effect and accepts only what proves it never took it.
func (s *Server) proveRollbackNotAdmitted(ctx context.Context, endpoint platformRuntimeEndpoint, plan platformResolveKeepDataPlan) error {
	for _, f := range plan.AbandonedEffects {
		if time.Now().UnixMilli() <= platformResolveKeepDataQuiet(f.CompletedMS) {
			return errAppPlatformOperationConflict
		}
		var err error
		switch f.Kind {
		case "restore":
			err = s.proveRestoreNotAdmitted(ctx, endpoint, plan, f)
		case "snapshot":
			err = s.proveSnapshotNotAdmitted(ctx, endpoint, plan, f)
		default:
			err = errAppPlatformOperationConflict
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) proveRestoreNotAdmitted(ctx context.Context, endpoint platformRuntimeEndpoint, plan platformResolveKeepDataPlan, f platformResolveKeepDataEffect) error {
	intent, err := s.signPlatformImportRollbackRead(ctx, f.OperationID)
	if err != nil {
		return err
	}
	runner := s.platformMigrationRunner()
	if err := runner.CheckRoot(ctx, f.OperationID, intent.runtime, intent.authority, endpoint); err != nil {
		return err
	}
	_, err = runner.ReadRestore(ctx, endpoint, intent)
	if errors.Is(err, errPlatformMigrationAbsent) {
		return nil
	}
	if err != nil {
		return errors.Join(errAppPlatformRuntimeConflict, err)
	}
	// A receipt, a pending or an orphaned status: the runner holds the restore.
	return errAppPlatformOperationConflict
}

// platformCheckpointReceiptRead reads a checkpoint effect back from the runner; tests replace it.
var platformCheckpointReceiptRead = platformReadCheckpointReceipt

// proveSnapshotNotAdmitted shows that the runner never took the snapshot an earlier failed attempt prepared.
//
// The runner has no typed absence for a checkpoint read: it answers a receipt or fails, whatever the reason. So the
// proof is composed of what can be shown, each part of which the action requires:
//
//   - the effect was never observed by the Portal, and the operation ended before the read (the command expired);
//   - the very command is rebuilt from the effect row and its digest equals the effect's, so the read below asks about
//     exactly the effect the row records;
//   - the runner admitted and settled a later snapshot of the same installation (the operation being resolved): it
//     refuses any checkpoint while a prepared or unknown command of the installation exists, so none was unsettled then;
//   - a read of the rebuilt command, with the runner reachable (its root answers just before), returns no receipt,
//     twice: it was not applied either.
func (s *Server) proveSnapshotNotAdmitted(ctx context.Context, endpoint platformRuntimeEndpoint, plan platformResolveKeepDataPlan, f platformResolveKeepDataEffect) error {
	var later int
	if err := s.st.queryRowContext(ctx, `SELECT COUNT(*) FROM app_platform_operation_effects
		WHERE operation_id=? AND state='applied' AND kind='snapshot' AND created_ms>?`, plan.OperationID, platformResolveKeepDataQuiet(f.CompletedMS)).Scan(&later); err != nil {
		return err
	}
	if later == 0 {
		return errAppPlatformOperationConflict
	}
	intent, err := s.signPlatformFailedSnapshotRead(ctx, f)
	if err != nil {
		return err
	}
	runner := s.platformMigrationRunner()
	for attempt := 0; attempt < 2; attempt++ {
		if err := runner.CheckRoot(ctx, f.OperationID, intent.runtime, intent.authority, endpoint); err != nil {
			return err
		}
		if observation, readErr := platformCheckpointReceiptRead(ctx, endpoint, intent); readErr == nil {
			_ = observation
			return errAppPlatformOperationConflict
		}
	}
	return nil
}

// signPlatformFailedSnapshotRead rebuilds the snapshot command of an earlier failed attempt, as the Portal that sent it
// built it, and proves it is the command the effect row records. The command was built before the writer fence of a
// disabled install source was corrected, so both fences it may have carried are tried; the digest decides.
func (s *Server) signPlatformFailedSnapshotRead(ctx context.Context, f platformResolveKeepDataEffect) (platformCheckpointIntent, error) {
	var zero platformCheckpointIntent
	dek, err := s.dek()
	if err != nil {
		return zero, err
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return zero, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	var installation, epoch, digest, checkpointID string
	var admitted, generation int64
	if err := e.queryRow(`SELECT o.installation_id,o.controller_epoch,o.target_generation,fe.admitted_fence,fe.payload_sha256
		FROM app_platform_operations o JOIN app_platform_operation_effects fe ON fe.operation_id=o.id
		WHERE o.id=? AND fe.effect_id=? AND fe.state='prepared' AND fe.observed_fence=0 AND fe.receipt_json='{}'`, f.OperationID, f.EffectID).
		Scan(&installation, &epoch, &generation, &admitted, &digest); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if err := e.queryRow(`SELECT checkpoint_id FROM app_platform_recovery_sets WHERE operation_id=? AND state='preparing'`, f.OperationID).Scan(&checkpointID); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	runtime, seed, err := s.platformDataRuntime(e, epoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	lease := platformOperationLease{id: f.OperationID, installationID: installation, controllerEpoch: epoch, fence: admitted,
		snapshot: appplatform.OperationSnapshot{Action: appplatform.Action(f.Action), Managed: true, Generation: uint64(generation), Revision: 1, Stage: appplatform.StageRecoveryRequired}}
	source, err := loadPlatformCheckpointSourceMode(e, lease, runtime, now, true, 0)
	if err != nil {
		return zero, err
	}
	fences := []string{source.writerFence}
	if source.currentWriterFence != "" {
		fences = append(fences, source.currentWriterFence)
	}
	for _, fence := range fences {
		candidate := source
		candidate.writerFence = fence
		command, err := platformCheckpointCommand(lease, runtime, candidate, checkpointID, f.EffectID)
		if err != nil || command.PayloadSHA256 != digest {
			continue
		}
		issued := time.Now().UnixMilli()
		command.IssuedMS, command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(issued+platformStartCommandWindowMS, 10)
		intent := platformCheckpointIntent{effectID: f.EffectID, runtime: runtime, authority: platformRuntimeAuthority(runtime), command: command, source: candidate}
		key := ed25519.NewKeyFromSeed(seed)
		defer clear(key)
		if intent.raw, intent.signature, err = control.SignCheckpoint(command, key, intent.authority, issued); err != nil {
			return zero, err
		}
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		return intent, nil
	}
	return zero, errors.Join(errAppPlatformStageEvidence, errors.New("the snapshot command is not the one the effect records"))
}

func (s *Server) createPlatformResolveKeepDataReview(ctx context.Context, session platformActorSession, installation string, endpoint platformRuntimeEndpoint) (platformRemovalReview, error) {
	var plan platformResolveKeepDataPlan
	if err := s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl, _ appplatform.PublisherTrust) error {
		var err error
		plan, err = loadPlatformResolveKeepDataPlan(e, installation, control)
		return err
	}); err != nil {
		return platformRemovalReview{}, err
	}
	if err := s.proveRollbackNotAdmitted(ctx, endpoint, plan); err != nil {
		return platformRemovalReview{}, err
	}
	var result platformRemovalReview
	err := s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl, _ appplatform.PublisherTrust) error {
		fresh, err := loadPlatformResolveKeepDataPlan(e, installation, control)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(fresh)
		want, _ := json.Marshal(plan)
		if err != nil || len(raw) > 65536 || string(raw) != string(want) {
			return errAppPlatformPlanChanged
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if control.nowMS > math.MaxInt64-platformReviewPlanValidityMS {
			return errAppPlatformPlanChanged
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		digest, expires := platformDigest(raw), control.nowMS+platformReviewPlanValidityMS
		// The review plan table keeps an action from the closed set; the format tells this plan apart, as a recovery
		// resume's does.
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,
			installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,
			plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			id, installation, fresh.AppID, fresh.PublisherID, string(appplatform.ActionImportData), fresh.ReleaseSHA256, fresh.ReleaseSHA256,
			fresh.InstallationRevision, fresh.PolicyRevision, fresh.TrustRevision, control.epoch, actor.scope, actor.username,
			actor.created, actor.generation, actor.authorityFingerprint, digest, string(raw), expires, control.nowMS, control.nowMS); err != nil {
			return err
		}
		result = platformRemovalReview{ID: id, SHA256: digest, JSON: string(raw), ExpiresMS: expires}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

// platformResolveKeepDataResult is what a confirmed resolution answers, and what an idempotent repeat answers again.
type platformResolveKeepDataResult struct {
	OperationID    string `json:"operation_id"`
	InstallationID string `json:"installation_id"`
	Abandoned      int    `json:"abandoned_effects"`
}

func (s *Server) consumePlatformResolveKeepDataReview(ctx context.Context, session platformActorSession, planID, planSHA, key string, endpoint platformRuntimeEndpoint) (platformResolveKeepDataResult, error) {
	var result platformResolveKeepDataResult
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) || !platformValidUUID(key) {
		return result, errAppPlatformPlanChanged
	}
	// An idempotent repeat answers what the first confirmation recorded and does nothing else.
	var recorded string
	err := s.st.queryRowContext(ctx, `SELECT v FROM meta WHERE k=?`, platformResolveKeepDataMeta+platformIdempotencySHA(key)).Scan(&recorded)
	if err == nil {
		var previous struct {
			Plan   string                        `json:"plan_id"`
			Result platformResolveKeepDataResult `json:"result"`
		}
		if json.Unmarshal([]byte(recorded), &previous) != nil || previous.Plan != planID {
			return result, errAppPlatformOperationConflict
		}
		return previous.Result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var plan platformResolveKeepDataPlan
	var raw string
	if err := s.st.queryRowContext(ctx, `SELECT plan_json FROM app_platform_review_plans WHERE id=?`, planID).Scan(&raw); err != nil ||
		json.Unmarshal([]byte(raw), &plan) != nil || plan.Format != platformResolveKeepDataFormat || platformDigest([]byte(raw)) != planSHA {
		return result, errAppPlatformPlanChanged
	}
	if err := s.proveRollbackNotAdmitted(ctx, endpoint, plan); err != nil {
		return result, err
	}
	err = s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl, _ appplatform.PublisherTrust) error {
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var digest, scope, username, created, authority, action, epoch string
		var installation sql.NullString
		var expires, accountGeneration int64
		var consumed sql.NullInt64
		var stored string
		if err := e.queryRow(`SELECT plan_json,plan_sha256,actor_scope,username,account_created,account_generation,authority_fingerprint,expires_ms,consumed_ms,
			controller_epoch,action,installation_id FROM app_platform_review_plans WHERE id=?`+lock, planID).
			Scan(&stored, &digest, &scope, &username, &created, &accountGeneration, &authority, &expires, &consumed, &epoch, &action, &installation); err != nil {
			return errAppPlatformPlanChanged
		}
		if scope != actor.scope {
			return errAppPlatformActorUnavailable
		}
		if consumed.Valid {
			return errAppPlatformOperationConflict
		}
		control, err := platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if expires <= control.nowMS {
			return errAppPlatformPlanExpired
		}
		if !installation.Valid || action != string(appplatform.ActionImportData) || stored != raw || digest != planSHA ||
			username != actor.username || created != actor.created || accountGeneration != actor.generation || authority != actor.authorityFingerprint ||
			epoch != control.epoch || platformDigest([]byte(stored)) != planSHA {
			return errAppPlatformPlanChanged
		}
		fresh, err := loadPlatformResolveKeepDataPlan(e, installation.String, control)
		freshRaw, _ := json.Marshal(fresh)
		if err != nil || string(freshRaw) != stored {
			return errAppPlatformPlanChanged
		}
		now := control.nowMS
		changed, err := e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND expires_ms>? AND plan_sha256=?`,
			now, now, planID, now, planSHA)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		for _, f := range fresh.AbandonedEffects {
			changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
				WHERE operation_id=? AND stage=? AND effect_number=? AND effect_id=? AND kind=? AND state='prepared' AND observed_fence=0 AND receipt_json='{}'`,
				platformAbandonedReceipt, now, f.OperationID, f.Stage, f.Number, f.EffectID, f.Kind)
			if err := platformLeaseCAS(changed, err); err != nil {
				return err
			}
			if f.RecoverySetID != "" {
				// The set a never-admitted snapshot opened holds no checkpoint at the runner: it is closed as a deleted one,
				// the state every other settled-away set takes, so it no longer reads as a recovery point.
				changed, err := e.exec(`UPDATE app_platform_recovery_sets SET state='deleted',deleted_ms=?,updated_ms=?
					WHERE id=? AND operation_id=? AND state='preparing' AND manifest_sha256 IS NULL AND bytes=0`, now, now, f.RecoverySetID, f.OperationID)
				if err := platformLeaseCAS(changed, err); err != nil {
					return err
				}
			}
		}
		// The marker a completed update retry writes: launch and settings read it as the operation being resolved.
		var runtimeID string
		if err := e.queryRow(`SELECT runtime_id FROM app_platform_installations WHERE id=?`, installation.String).Scan(&runtimeID); err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,'recovered',1,?,'recovered',?,?,?,?,?,'applied','{}',?,?)`,
			fresh.OperationID, platformDerivedUUID("recovered", fresh.OperationID), platformDigest([]byte("resolve-keep-data/"+fresh.OperationID+"/"+planID)),
			runtimeID, fresh.ControllerEpoch, fresh.OperationFence, fresh.OperationFence, now, now); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Plan          string                          `json:"plan_id"`
			Operation     string                          `json:"operation_id"`
			Action        string                          `json:"operation_action"`
			ErrorCode     string                          `json:"operation_error_code"`
			ObservedBytes int64                           `json:"observed_bytes"`
			Abandoned     []platformResolveKeepDataEffect `json:"abandoned_effects"`
			Checkpoint    string                          `json:"checkpoint_recovery_set_id,omitempty"`
		}{planID, fresh.OperationID, fresh.OperationAction, fresh.OperationErrorCode, fresh.ObservedBytes, fresh.AbandonedEffects, fresh.CheckpointRecoverySt})
		at := time.UnixMilli(now).UTC().Format(time.RFC3339)
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_operation',?,?,'')`,
			at, actor.username, actor.ouID, platformResolveKeepDataAudit, fresh.OperationID, string(detail)); err != nil {
			return err
		}
		result = platformResolveKeepDataResult{OperationID: fresh.OperationID, InstallationID: installation.String, Abandoned: len(fresh.AbandonedEffects)}
		recordedRaw, _ := json.Marshal(struct {
			Plan   string                        `json:"plan_id"`
			Result platformResolveKeepDataResult `json:"result"`
		}{planID, result})
		if _, err := e.exec(`INSERT INTO meta(k,v) VALUES(?,?)`, platformResolveKeepDataMeta+platformIdempotencySHA(key), string(recordedRaw)); err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformResolveKeepDataResult{}, err
	}
	return result, nil
}
