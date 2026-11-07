package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Settlement of a failed migration.
//
// An update that ended recovery_required with a migrate-data effect holds a namespace the migration may have
// touched. Before either recovery choice is offered, an idle driver (it runs in normal and in paused mode,
// since it needs no lease) brings the namespace back to the verified pre-update checkpoint, in this order and
// one step at a time:
//
//  1. a started target is stopped and removed (a migrated target that failed readiness);
//  2. an unknown migrate-data is read back and, once the runner reports it orphaned, discarded;
//  3. the checkpoint is restored into the same namespace, unless the runner proved it never claimed the
//     migration;
//  4. a settled marker is recorded.
//
// Every effect is stored on the failed update and keeps its own identity: it is read back before it is ever
// sent again, an absence counts only after every command signed for it has expired, and nothing is sent once
// the installation's fence has moved past the update's.

const (
	platformSettleStage       = "settle"
	platformSettledStage      = "settled"
	platformSettleStopEffect  = 1
	platformSettleRemoveEffct = 2
)

type platformSettleEffect struct {
	exists                                 bool
	effectID, kind, digest, state, receipt string
	admitted, observed                     int64
}

func loadPlatformSettleEffect(e migExec, operation, stage string, number int) (platformSettleEffect, error) {
	var f platformSettleEffect
	err := e.queryRow(`SELECT effect_id,kind,payload_sha256,state,receipt_json,admitted_fence,observed_fence FROM app_platform_operation_effects
		WHERE operation_id=? AND stage=? AND effect_number=?`, operation, stage, number).
		Scan(&f.effectID, &f.kind, &f.digest, &f.state, &f.receipt, &f.admitted, &f.observed)
	if errors.Is(err, sql.ErrNoRows) {
		return platformSettleEffect{}, nil
	}
	f.exists = err == nil
	return f, err
}

// platformSettlementLease reads the failed update as an unleased lease: the identity every settlement command
// is bound to.
func platformSettlementLease(e migExec, operation string) (platformOperationLease, error) {
	var id, installation, epoch, stage string
	var fence, generation, stateRevision, rowRevision int64
	var completed sql.NullInt64
	var owner sql.NullString
	if err := e.queryRow(`SELECT id,installation_id,controller_epoch,fence,target_generation,state_revision,row_revision,stage,completed_ms,lease_owner
		FROM app_platform_operations WHERE id=? AND action='update' AND managed=1`, operation).
		Scan(&id, &installation, &epoch, &fence, &generation, &stateRevision, &rowRevision, &stage, &completed, &owner); err != nil ||
		stage != "recovery_required" || !completed.Valid || owner.Valid || generation < 2 {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	return platformOperationLease{
		id: id, installationID: installation, controllerEpoch: epoch, fence: fence, rowRevision: rowRevision,
		snapshot: appplatform.OperationSnapshot{Action: appplatform.ActionUpdate, Managed: true, Generation: uint64(generation),
			Revision: uint64(stateRevision), Stage: appplatform.StageRecoveryRequired},
	}, nil
}

// platformSettlementGuard checks, under the mutation lock, that the failed update is still what the settlement
// was derived from and that nothing has moved the installation's fence past it. It returns the database time.
func platformSettlementGuard(e migExec, lease platformOperationLease) (int64, error) {
	current, err := platformSettlementLease(e, lease.id)
	if err != nil || current.fence != lease.fence || current.controllerEpoch != lease.controllerEpoch || current.installationID != lease.installationID {
		return 0, errors.Join(errAppPlatformLeaseConflict, err)
	}
	var epoch string
	if err := e.queryRow(`SELECT COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),'')`).Scan(&epoch); err != nil || epoch != lease.controllerEpoch {
		return 0, errAppPlatformControlPaused
	}
	var phase, desired, installationEpoch string
	var fence int64
	if err := e.queryRow(`SELECT phase,desired_state,control_fence,controller_epoch FROM app_platform_installations WHERE id=?`, lease.installationID).
		Scan(&phase, &desired, &fence, &installationEpoch); err != nil {
		return 0, err
	}
	if phase != "recovery_required" || desired != "enabled" || installationEpoch != lease.controllerEpoch || fence != lease.fence {
		return 0, errAppPlatformLeaseConflict
	}
	return platformDatabaseNow(e)
}

// platformMigrationSettlementState is the projection of a failed migration: "" when the update has no
// migration to settle, then settling, settled or blocked.
func platformMigrationSettlementState(e migExec, operation string) (string, error) {
	var count int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, operation).Scan(&count); err != nil || count == 0 {
		return "", err
	}
	settled, err := loadPlatformSettleEffect(e, operation, platformSettledStage, 1)
	if err != nil {
		return "", err
	}
	if settled.exists && settled.state == "applied" {
		return "settled", nil
	}
	lease, err := platformSettlementLease(e, operation)
	if err != nil {
		return "blocked", nil
	}
	var fence int64
	var phase string
	if err := e.queryRow(`SELECT control_fence,phase FROM app_platform_installations WHERE id=?`, lease.installationID).Scan(&fence, &phase); err != nil || fence != lease.fence || phase != "recovery_required" {
		return "blocked", nil
	}
	start, err := loadPlatformSettleEffect(e, operation, "start", 1)
	if err != nil {
		return "", err
	}
	if start.exists && start.state != "applied" && start.state != "reverted" {
		return "blocked", nil
	}
	return "settling", nil
}

// platformMigrationSettled reports whether the failed update's migration has been settled.
func platformMigrationSettled(e migExec, operation string) (bool, error) {
	state, err := platformMigrationSettlementState(e, operation)
	return state == "settled", err
}

// platformMigrationSettlementCandidates lists the finished updates whose migration is not yet settled.
func (s *Server) platformMigrationSettlementCandidates(ctx context.Context) ([]string, error) {
	rows, err := s.st.db.QueryContext(ctx, s.st.bind(`SELECT o.id FROM app_platform_operations o
		WHERE o.action='update' AND o.managed=1 AND o.stage='recovery_required' AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL
		AND EXISTS (SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.stage='migrate' AND f.effect_number=2)
		AND NOT EXISTS (SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.stage='settled' AND f.state='applied')
		ORDER BY o.completed_ms,o.id LIMIT 8`))
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

// settlePlatformMigrations takes one settlement step for each failed migration that is waiting, and returns
// how many made progress.
func (s *Server) settlePlatformMigrations(ctx context.Context, endpoint platformRuntimeEndpoint) (int, error) {
	if endpoint.path == "" {
		return 0, nil
	}
	ids, err := s.platformMigrationSettlementCandidates(ctx)
	if err != nil {
		return 0, err
	}
	progressed := 0
	for _, id := range ids {
		done, err := s.settlePlatformMigration(ctx, endpoint, id)
		if err != nil {
			if ctx.Err() != nil {
				return progressed, ctx.Err()
			}
			log.Printf("app-platform migration settlement %s: %v", id, err)
			continue
		}
		if done {
			progressed++
		}
	}
	return progressed, nil
}

// settlePlatformMigration takes the next settlement step of one failed update, and reports whether it
// changed anything. A step that has to wait (the runner is busy, a signed window has not expired) is not an
// error: the same step is taken again later.
func (s *Server) settlePlatformMigration(ctx context.Context, endpoint platformRuntimeEndpoint, operation string) (bool, error) {
	lease, err := platformSettlementLease(s.st.poolExec(), operation)
	if err != nil {
		return false, err
	}
	pool := s.st.poolExec()
	if settled, err := loadPlatformSettleEffect(pool, operation, platformSettledStage, 1); err != nil || settled.exists {
		return false, err
	}
	m, err := loadPlatformSettleEffect(pool, operation, "migrate", 2)
	if err != nil || !m.exists {
		return false, err
	}
	start, err := loadPlatformSettleEffect(pool, operation, "start", 1)
	if err != nil {
		return false, err
	}
	// 1. A started target comes first: nothing may share the namespace with a restore.
	if start.exists && start.state == "applied" {
		stop, err := loadPlatformSettleEffect(pool, operation, platformSettleStage, platformSettleStopEffect)
		if err != nil {
			return false, err
		}
		if stop.state != "applied" {
			return s.removeSettlementTarget(ctx, endpoint, lease, "stop", platformSettleStopEffect)
		}
		remove, err := loadPlatformSettleEffect(pool, operation, platformSettleStage, platformSettleRemoveEffct)
		if err != nil {
			return false, err
		}
		if remove.state != "applied" {
			return s.removeSettlementTarget(ctx, endpoint, lease, "remove_code", platformSettleRemoveEffct)
		}
	} else if start.exists && start.state != "reverted" {
		return false, nil
	}
	// 2. An unfinished migration is read back, and discarded once nothing is running it.
	switch m.state {
	case "prepared", "unknown":
		discard, err := loadPlatformSettleEffect(pool, operation, "migrate", 3)
		if err != nil {
			return false, err
		}
		if discard.exists {
			return s.settleSettlementDiscard(ctx, endpoint, lease, m)
		}
		return s.readBackSettlementMigrate(ctx, endpoint, lease, m)
	}
	// 3. The checkpoint is restored, unless the runner never claimed the migration.
	needed, err := platformSettlementRestoreNeeded(m)
	if err != nil {
		return false, err
	}
	if needed {
		restore, err := loadPlatformSettleEffect(pool, operation, "restore_data", 1)
		if err != nil {
			return false, err
		}
		if restore.state != "applied" {
			return s.settleSettlementRestore(ctx, endpoint, lease, restore)
		}
	}
	// 4. Settled.
	return s.markPlatformMigrationSettled(ctx, lease)
}

// platformSettlementRestoreNeeded says whether the migration may have touched the namespace: any recorded
// outcome of a job (a receipt, applied or not, or a discard) does, only a proven absence does not.
func platformSettlementRestoreNeeded(m platformSettleEffect) (bool, error) {
	switch m.state {
	case "applied":
		return true, nil
	case "reverted":
		var probe struct {
			State string `json:"state"`
		}
		if json.Unmarshal([]byte(m.receipt), &probe) != nil {
			return false, errAppPlatformStageEvidence
		}
		return probe.State != "absent", nil
	}
	return false, errAppPlatformStageEvidence
}

const platformAbsentReceipt = `{"state":"absent"}`

// readBackSettlementMigrate reads the unfinished migrate-data under its own identity. A receipt settles it, an
// orphaned answer opens the discard, a pending one waits, and an absence settles it only once every command
// signed for it has expired.
func (s *Server) readBackSettlementMigrate(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease, m platformSettleEffect) (bool, error) {
	intent, err := s.signPlatformSettlementMigrate(ctx, lease)
	if err != nil {
		return false, err
	}
	runner := s.platformMigrationRunner()
	if err := runner.CheckRoot(ctx, lease.id, intent.runtime, intent.authority, endpoint); err != nil {
		return false, err
	}
	observation, err := runner.ReadMigrate(ctx, endpoint, intent)
	if errors.Is(err, errPlatformMigrationAbsent) {
		return s.revertAbsentMigration(ctx, lease, intent)
	}
	if err != nil {
		return false, nil
	}
	if receipt, ok := validPlatformMigrateObservation(observation, intent, endpoint); ok {
		return s.commitSettlementMigrateReceipt(ctx, lease, intent, receipt)
	}
	status, statusErr := observation.MigrationStatus()
	if statusErr != nil || !validPlatformMigrateStatus(observation, status, intent.command.CommandHeader, intent.raw, intent.authority, endpoint) {
		return false, errAppPlatformStageEvidence
	}
	if status.State != "orphaned" {
		return false, nil
	}
	return s.openSettlementDiscard(ctx, lease, intent)
}

// signPlatformSettlementMigrate re-signs the original migrate-data command, at the fence it was admitted under,
// for a read.
func (s *Server) signPlatformSettlementMigrate(ctx context.Context, lease platformOperationLease) (platformMigrateIntent, error) {
	var zero platformMigrateIntent
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
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	now, err := platformSettlementGuard(e, lease)
	if err != nil {
		return zero, err
	}
	ev, err := loadPlatformMigrateEvidence(e, lease, runtime, now, 0)
	if err != nil {
		return zero, err
	}
	intent, err := loadPlatformMigrateIntent(e, lease, runtime, ev, now, false)
	if err != nil {
		return zero, err
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, errAppPlatformLeaseConflict
	}
	if err := signPlatformMigrateCommand(e, lease.id, seed, &intent, issued, issued+25000, false); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return intent, nil
}

// commitSettlementMigrateReceipt records a receipt the read returned for an effect left unfinished: a
// migrated one applies it (the namespace holds migrated data, to be restored), a failed or discarded one
// reverts it.
func (s *Server) commitSettlementMigrateReceipt(ctx context.Context, lease platformOperationLease, intent platformMigrateIntent, receipt journal.MigrateDataReceipt) (bool, error) {
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return false, errAppPlatformStageEvidence
	}
	state := "reverted"
	if receipt.State == "migrated" {
		state = "applied"
	}
	return true, s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state=?,observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage='migrate' AND effect_number=2 AND effect_id=? AND kind='migrate-data' AND payload_sha256=?
			AND admitted_fence=? AND state IN ('prepared','unknown') AND observed_fence=0`,
			state, intent.admittedFence, string(encoded), now, lease.id, intent.effectID, intent.command.PayloadSHA256, intent.admittedFence)
		return platformLeaseCAS(changed, err)
	})
}

// revertAbsentMigration records the runner's typed absence. It is final only once every command signed for
// the effect has expired; until then a command may still be on its way to the runner.
func (s *Server) revertAbsentMigration(ctx context.Context, lease platformOperationLease, intent platformMigrateIntent) (bool, error) {
	// Only commands that could be executed count: the read just signed is never sent as an execute.
	if time.Now().UnixMilli() <= intent.priorSignedUntil+platformImportAbsenceMarginMS {
		return false, nil
	}
	return true, s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage='migrate' AND effect_number=2 AND effect_id=? AND kind='migrate-data' AND payload_sha256=?
			AND state IN ('prepared','unknown') AND observed_fence=0`,
			platformAbsentReceipt, now, lease.id, intent.effectID, intent.command.PayloadSHA256)
		return platformLeaseCAS(changed, err)
	})
}

func platformSignedUntilOf(c control.MigrateDataCommand) int64 {
	n, _ := strconv.ParseInt(c.ExpiresMS, 10, 64)
	return n
}

// settleEffectRow runs one settlement write under the mutation lock and the settlement guard.
func (s *Server) settleEffectRow(ctx context.Context, lease platformOperationLease, write func(e migExec, now int64) error) error {
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return err
	}
	now, err := platformSettlementGuard(e, lease)
	if err != nil {
		return err
	}
	if err := write(e, now); err != nil {
		return err
	}
	return tx.Commit()
}

// openSettlementDiscard records the discard effect (effect 3 of the migrate stage) for an orphaned migration.
// The migrate-data effect is untouched until the discard is applied.
func (s *Server) openSettlementDiscard(ctx context.Context, lease platformOperationLease, migrate platformMigrateIntent) (bool, error) {
	return true, s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		effectID, err := platformNewUUID()
		if err != nil {
			return err
		}
		command, err := platformDiscardCommand(lease, migrate, effectID, lease.fence)
		if err != nil {
			return err
		}
		_, err = e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			SELECT ?,'migrate',3,?,'discard-migration',?,runtime_id,controller_epoch,?,0,'prepared','{}',?,? FROM app_platform_operation_effects
			WHERE operation_id=? AND stage='migrate' AND effect_number=2`,
			lease.id, effectID, command.PayloadSHA256, lease.fence, now, now, lease.id)
		return err
	})
}

func platformDiscardCommand(lease platformOperationLease, migrate platformMigrateIntent, effectID string, fence int64) (control.DiscardMigrationCommand, error) {
	c := migrate.command
	command := control.DiscardMigrationCommand{
		CommandHeader: control.CommandHeader{
			Format: control.DiscardMigrationFormat, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
			ControlRevision: c.ControlRevision, InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration,
			Stage: "migrate", EffectNumber: control.DiscardMigrationEffectNumber, EffectID: effectID, Kind: "discard-migration",
			InstallationFence: strconv.FormatInt(fence, 10),
		},
		Payload: control.DiscardMigrationPayload{
			NamespaceID: c.Payload.NamespaceID, ResourceID: c.Payload.ResourceID, MigrateEffectID: c.EffectID,
			MigratePayloadSHA256: c.PayloadSHA256, TargetReleaseSHA256: c.Payload.TargetReleaseSHA256,
		},
	}
	digest, err := control.DiscardMigrationLogicalDigest(command)
	if err != nil {
		return command, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

// signPlatformSettlementDiscard signs the discard effect for an execute and a read. The command it signs is
// rebuilt from the migrate-data it settles, so it can only name that effect.
func (s *Server) signPlatformSettlementDiscard(ctx context.Context, lease platformOperationLease, record bool) (platformDiscardIntent, error) {
	var zero platformDiscardIntent
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
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	now, err := platformSettlementGuard(e, lease)
	if err != nil {
		return zero, err
	}
	ev, err := loadPlatformMigrateEvidence(e, lease, runtime, now, 0)
	if err != nil {
		return zero, err
	}
	migrate, err := loadPlatformMigrateIntent(e, lease, runtime, ev, now, false)
	if err != nil {
		return zero, err
	}
	d, err := loadPlatformSettleEffect(e, lease.id, "migrate", 3)
	if err != nil || !d.exists || d.kind != "discard-migration" || (d.state != "prepared") || d.observed != 0 {
		return zero, errors.Join(errAppPlatformStageEvidence, err)
	}
	command, err := platformDiscardCommand(lease, migrate, d.effectID, d.admitted)
	if err != nil || command.PayloadSHA256 != d.digest {
		return zero, errors.Join(errAppPlatformStageEvidence, err)
	}
	until, ok := platformImportSignedUntil(d.receipt)
	if !ok {
		return zero, errAppPlatformStageEvidence
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, errAppPlatformLeaseConflict
	}
	expires := issued + 25000
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	if record {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET receipt_json=?
			WHERE operation_id=? AND stage='migrate' AND effect_number=3 AND effect_id=? AND state='prepared' AND observed_fence=0 AND receipt_json=?`,
			platformImportPendingReceipt(max(until, expires)), lease.id, d.effectID, d.receipt)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	intent := platformDiscardIntent{
		effectID: d.effectID, admittedFence: d.admitted, runtime: runtime, authority: platformImportAuthority(runtime), command: command,
		descriptor: bytes.Clone(ev.descriptor), migrateEffectID: migrate.effectID, pendingReceipt: platformImportPendingReceipt(max(until, expires)),
		priorSignedUntil: until, effectState: d.state,
	}
	if intent.raw, intent.signature, err = control.SignDiscardMigration(command, key, intent.authority, issued); err != nil {
		return zero, err
	}
	if intent.readSignature, err = control.SignDiscardMigrationRead(intent.raw, key, intent.authority, issued); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return intent, nil
}

func validPlatformDiscardObservation(o platformDiscardObservation, intent platformDiscardIntent, endpoint platformRuntimeEndpoint) (journal.DiscardMigrationReceipt, bool) {
	receipt, err := o.DiscardMigrationReceipt()
	if err != nil || !journal.DiscardMigrationReceiptMatchesCommand(receipt, intent.command) ||
		!validPlatformLifecycleProof(o, intent.raw, intent.authority, endpoint, receipt.ObservedMS, receipt.SettledMS, intent.command.IssuedMS) {
		return journal.DiscardMigrationReceipt{}, false
	}
	return receipt, true
}

// settleSettlementDiscard sends the discard (reading it back first when an earlier attempt may have reached the
// runner) and, on its receipt, reverts the migrate-data effect it settled.
func (s *Server) settleSettlementDiscard(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease, m platformSettleEffect) (bool, error) {
	intent, err := s.signPlatformSettlementDiscard(ctx, lease, false)
	if err != nil {
		return false, err
	}
	runner := s.platformMigrationRunner()
	if err := runner.CheckRoot(ctx, lease.id, intent.runtime, intent.authority, endpoint); err != nil {
		return false, err
	}
	commit := func(o platformDiscardObservation) (bool, error) {
		receipt, ok := validPlatformDiscardObservation(o, intent, endpoint)
		if !ok {
			return false, errAppPlatformStageEvidence
		}
		return true, s.commitSettlementDiscard(ctx, lease, intent, receipt)
	}
	observation, err := runner.ReadDiscard(ctx, endpoint, intent)
	switch {
	case err == nil:
		if _, ok := validPlatformDiscardObservation(observation, intent, endpoint); ok {
			return commit(observation)
		}
		status, statusErr := observation.MigrationStatus()
		if statusErr != nil || !validPlatformMigrateStatus(observation, status, intent.command.CommandHeader, intent.raw, intent.authority, endpoint) {
			return false, errAppPlatformStageEvidence
		}
		if status.State == "pending" {
			return false, nil
		}
		// orphaned: a discard is convergent, so the same effect is sent again.
	case errors.Is(err, errPlatformMigrationAbsent):
		if time.Now().UnixMilli() <= intent.priorSignedUntil+platformImportAbsenceMarginMS {
			return false, nil
		}
	default:
		return false, nil
	}
	// Only now is a command that can be executed signed, and its window recorded.
	intent, err = s.signPlatformSettlementDiscard(ctx, lease, true)
	if err != nil {
		return false, err
	}
	executed, err := runner.ExecuteDiscard(ctx, endpoint, intent)
	if err != nil {
		return false, nil
	}
	return commit(executed)
}

// commitSettlementDiscard applies the discard effect and reverts the migrate-data effect it settled, in one
// transaction.
func (s *Server) commitSettlementDiscard(ctx context.Context, lease platformOperationLease, intent platformDiscardIntent, receipt journal.DiscardMigrationReceipt) error {
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return errAppPlatformStageEvidence
	}
	reverted, _ := json.Marshal(struct {
		State                string `json:"state"`
		DiscardEffectID      string `json:"discard_effect_id"`
		DiscardReceiptSHA256 string `json:"discard_receipt_sha256"`
		MigrateReceiptSHA256 string `json:"migrate_receipt_sha256"`
	}{"discarded", intent.effectID, platformDigest(encoded), receipt.MigrateReceiptSHA256})
	return s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage='migrate' AND effect_number=3 AND effect_id=? AND kind='discard-migration' AND payload_sha256=?
			AND admitted_fence=? AND state='prepared' AND observed_fence=0`,
			intent.admittedFence, string(encoded), now, lease.id, intent.effectID, intent.command.PayloadSHA256, intent.admittedFence)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		changed, err = e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage='migrate' AND effect_number=2 AND effect_id=? AND kind='migrate-data' AND state IN ('prepared','unknown')`,
			string(reverted), now, lease.id, intent.command.Payload.MigrateEffectID)
		return platformLeaseCAS(changed, err)
	})
}

// settleSettlementRestore restores the update's checkpoint into the namespace. An unfinished restore is read
// back first: a receipt settles it, a runner still working is waited for, and only an orphaned or finally
// absent one is sent again, under the same effect, so the runner completes or starts it under its own claim.
func (s *Server) settleSettlementRestore(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease, existing platformSettleEffect) (bool, error) {
	intent, fresh, err := s.signPlatformSettlementRestore(ctx, lease, true, false)
	if err != nil {
		return false, err
	}
	runner := s.platformMigrationRunner()
	if err := runner.CheckRoot(ctx, lease.id, intent.runtime, intent.authority, endpoint); err != nil {
		return false, err
	}
	commit := func(o platformRestoreObservation) (bool, error) {
		return true, s.commitPlatformSettlementRestore(ctx, lease, intent, endpoint, o)
	}
	if !fresh {
		observation, err := runner.ReadRestore(ctx, endpoint, intent)
		switch {
		case err == nil:
			if _, ok := validPlatformRestoreObservation(observation, intent, endpoint); ok {
				return commit(observation)
			}
			status, statusErr := observation.MigrationStatus()
			if statusErr != nil {
				return false, errAppPlatformStageEvidence
			}
			if status.State == "pending" {
				return false, nil
			}
		case errors.Is(err, errPlatformMigrationAbsent):
			if time.Now().UnixMilli() <= intent.signedUntilBefore+platformImportAbsenceMarginMS {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	// Only now is a command that can be executed signed, and its window recorded.
	prior := intent.signedUntilBefore
	if intent, _, err = s.signPlatformSettlementRestore(ctx, lease, false, true); err != nil {
		return false, err
	}
	intent.signedUntilBefore = prior
	executed, err := runner.ExecuteRestore(ctx, endpoint, intent)
	if err != nil {
		return true, nil
	}
	return commit(executed)
}

// removeSettlementTarget stops, then removes, the started target of the failed update under an operation
// identity of its own: the runner refuses a removal that names the operation of the start it removes.
func (s *Server) removeSettlementTarget(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease, stage string, number int) (bool, error) {
	intent, err := s.prepareSettlementTargetRemoval(ctx, lease, stage, number)
	if err != nil {
		return false, err
	}
	runner := s.platformMigrationRunner()
	if err := runner.CheckRoot(ctx, lease.id, intent.runtime, intent.authority, endpoint); err != nil {
		return false, err
	}
	observation, err := runner.RemoveTarget(ctx, endpoint, intent)
	if err != nil {
		return false, nil
	}
	if err := validatePlatformUpdateTargetRemovalObservation(observation, intent, endpoint); err != nil {
		return false, err
	}
	receipt, err := observation.UpdateStartRemovalReceipt()
	if err != nil {
		return false, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return false, errAppPlatformStageEvidence
	}
	return true, s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage=? AND effect_number=? AND effect_id=? AND kind=? AND payload_sha256=?
			AND admitted_fence=? AND state='prepared' AND observed_fence=0 AND receipt_json='{}'`,
			lease.fence, string(encoded), now, lease.id, platformSettleStage, number, intent.command.EffectID, stage,
			intent.command.PayloadSHA256, lease.fence)
		return platformLeaseCAS(changed, err)
	})
}

func (s *Server) prepareSettlementTargetRemoval(ctx context.Context, lease platformOperationLease, stage string, number int) (platformUpdateTargetRemovalIntent, error) {
	var zero platformUpdateTargetRemovalIntent
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
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	now, err := platformSettlementGuard(e, lease)
	if err != nil {
		return zero, err
	}
	_, _, checkpoint, _, err := loadPlatformRestoreCheckpoint(e, lease)
	if err != nil {
		return zero, err
	}
	start, _, _, _, err := loadPlatformUpdateTargetRemovalPlan(e, lease, runtime, checkpoint, now)
	if err != nil || start.running == nil {
		return zero, fmt.Errorf("settlement target removal source: %w", errors.Join(errAppPlatformStageEvidence, err))
	}
	issue := time.Now().UnixMilli()
	if issue < now-1000 || issue > now+1000 {
		return zero, errAppPlatformLeaseConflict
	}
	start.command.IssuedMS, start.command.ExpiresMS = strconv.FormatInt(issue, 10), strconv.FormatInt(issue+25000, 10)
	key := ed25519.NewKeyFromSeed(seed)
	start.raw, start.signature, err = control.SignUpdateStart(start.command, key, start.authority, issue)
	clear(key)
	if err != nil {
		return zero, err
	}
	existing, err := loadPlatformSettleEffect(e, lease.id, platformSettleStage, number)
	if err != nil {
		return zero, err
	}
	effectID := existing.effectID
	if !existing.exists {
		if effectID, err = platformNewUUID(); err != nil {
			return zero, err
		}
	} else if existing.kind != stage || existing.state != "prepared" || existing.admitted != lease.fence || existing.receipt != "{}" {
		return zero, errAppPlatformStageEvidence
	}
	removalLease := lease
	removalLease.id = platformDerivedUUID("settlement-removal", lease.id)
	command, err := platformUpdateTargetRemovalCommand(removalLease, start, effectID, stage)
	if err != nil {
		return zero, err
	}
	if existing.exists && existing.digest != command.PayloadSHA256 {
		return zero, errAppPlatformStageEvidence
	}
	if !existing.exists {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,0,'prepared','{}',?,?)`,
			lease.id, platformSettleStage, number, effectID, stage, command.PayloadSHA256, runtime.id, lease.controllerEpoch, lease.fence, now, now); err != nil {
			return zero, err
		}
	}
	key = ed25519.NewKeyFromSeed(seed)
	raw, signature, err := control.SignUpdateStartRemoval(command, key, start.authority, issue)
	clear(key)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return platformUpdateTargetRemovalIntent{
		command: command, raw: raw, signature: signature, authority: start.authority, start: start, runtime: runtime,
		sourceOperation: lease.id, nativeID: start.running.NativeID, effectStage: platformSettleStage, effectNumber: number,
		created: now, updated: now,
	}, nil
}

// markPlatformMigrationSettled records that the namespace was returned to the checkpoint and the target is gone.
func (s *Server) markPlatformMigrationSettled(ctx context.Context, lease platformOperationLease) (bool, error) {
	err := s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		var runtime string
		if err := e.queryRow(`SELECT runtime_id FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, lease.id).Scan(&runtime); err != nil {
			return err
		}
		_, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,?,1,?,'settled',?,?,?,?,?,'applied','{}',?,?)`,
			lease.id, platformSettledStage, platformDerivedUUID("settled", lease.id), platformDigest([]byte("settled/"+lease.id)),
			runtime, lease.controllerEpoch, lease.fence, lease.fence, now, now)
		if err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Operation string `json:"operation_id"`
		}{lease.id})
		_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.migration_settled','app_platform_operation',?,?,'')`,
			time.UnixMilli(now).UTC().Format(time.RFC3339), lease.id, string(detail))
		return err
	})
	return err == nil, err
}
