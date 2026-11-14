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
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
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
	platformSettleStage        = "settle"
	platformSettledStage       = "settled"
	platformSettleStopEffect   = 1
	platformSettleRemoveEffect = 2
	platformSettleHeldEffect   = 3
	// Slots 4 and 5 are the removal of a target the runner reserved and never created. They are effects of their own,
	// under their own operation identity, so a start that turns out to be running opens the ordinary removal (slots
	// 1 and 2) as a new effect and never reuses or rewrites these.
	platformSettleReservedStopEffect   = 4
	platformSettleReservedRemoveEffect = 5
	// Slot 6 is the sweep of containers labeled for the namespace that nothing accounts for.
	platformSettleSweepEffect = 6
	platformSweepKind         = "sweep"
	// platformCreateGraceMS is how long after a start's last signed window a container create the runner gave up on
	// may still be carried out by the Docker daemon: the driver's request timeout.
	platformCreateGraceMS = int64(driver.DockerRequestTimeout / time.Millisecond)
	// platformSigningSlackMS is how far past the database clock a start command is allowed to be issued when it is
	// signed (the check in the signing transaction), so a command signed in the update's last moment can expire that
	// much after the window from its completion.
	platformSigningSlackMS = int64(1000)
	// platformBlockedSweepReceipt notes on the sweep effect that the runner reported a container that is not its own.
	platformBlockedSweepReceipt = `{"state":"blocked","reason":"foreign_container"}`
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

// platformSettlementLease reads the failed update (or the failed enable, repair or restore whose start the
// runner may hold) as an unleased lease: the identity every settlement command is bound to.
func platformSettlementLease(e migExec, operation string) (platformOperationLease, error) {
	var id, installation, epoch, stage, action string
	var fence, generation, stateRevision, rowRevision int64
	var completed sql.NullInt64
	var owner sql.NullString
	if err := e.queryRow(`SELECT id,installation_id,controller_epoch,fence,target_generation,state_revision,row_revision,stage,completed_ms,lease_owner,action
		FROM app_platform_operations WHERE id=? AND action IN ('update','enable','repair','restore') AND managed=1`, operation).
		Scan(&id, &installation, &epoch, &fence, &generation, &stateRevision, &rowRevision, &stage, &completed, &owner, &action); err != nil ||
		stage != "recovery_required" || !completed.Valid || owner.Valid || generation < 2 {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	return platformOperationLease{
		id: id, installationID: installation, controllerEpoch: epoch, fence: fence, rowRevision: rowRevision,
		snapshot: appplatform.OperationSnapshot{Action: appplatform.Action(action), Managed: true, Generation: uint64(generation),
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
	// A failed enable or repair leaves the installation disabled; every other settled operation leaves it enabled.
	wantDesired := "enabled"
	if platformSameReleaseLifecycle(lease.snapshot.Action) {
		wantDesired = "disabled"
	}
	if phase != "recovery_required" || desired != wantDesired || installationEpoch != lease.controllerEpoch || fence != lease.fence {
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
	if start.exists && start.state != "applied" && start.state != "reverted" && start.state != "prepared" && start.state != "unknown" {
		return "blocked", nil
	}
	if reason, err := platformMigrationSettlementReason(e, operation); err != nil || reason != "" {
		return "blocked", err
	}
	return "settling", nil
}

// platformMigrationSettlementReason is why a settlement is stuck when the runner said so: "foreign_container" when the
// sweep found a container of the namespace that is not the runner's. It is empty otherwise.
func platformMigrationSettlementReason(e migExec, operation string) (string, error) {
	sweep, err := loadPlatformSettleEffect(e, operation, platformSettleStage, platformSettleSweepEffect)
	if err != nil {
		return "", err
	}
	if sweep.exists && sweep.state == "prepared" && sweep.receipt == platformBlockedSweepReceipt {
		return journal.SettleReasonForeignContainer, nil
	}
	return "", nil
}

// platformMigrationSettlementCandidates lists the finished updates whose migration is not yet settled.
func (s *Server) platformMigrationSettlementCandidates(ctx context.Context) ([]string, error) {
	rows, err := s.st.queryContext(ctx, `SELECT o.id FROM app_platform_operations o
		WHERE o.action='update' AND o.managed=1 AND o.stage='recovery_required' AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL
		AND EXISTS (SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.stage='migrate' AND f.effect_number=2)
		AND NOT EXISTS (SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.stage='settled' AND f.state='applied')
		ORDER BY o.completed_ms,o.id LIMIT 8`)
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
	// The enables and repairs whose start is open are settled by the same steps, from the same tick.
	held, err := s.settlePlatformHeldStarts(ctx, endpoint)
	return progressed + held, err
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
		// A removal of a reserved target that was begun before the start turned out to be running can never be
		// carried out: the runner refuses it for a start that is not reserved. It is closed as not applied, and the
		// target is then removed by the ordinary removal, which is a new effect with its own digest.
		if closed, err := s.closeReservedRemovals(ctx, lease); err != nil || closed {
			return closed, err
		}
		stop, err := loadPlatformSettleEffect(pool, operation, platformSettleStage, platformSettleStopEffect)
		if err != nil {
			return false, err
		}
		if stop.state != "applied" {
			return s.removeSettlementTarget(ctx, endpoint, lease, "stop", platformSettleStopEffect)
		}
		remove, err := loadPlatformSettleEffect(pool, operation, platformSettleStage, platformSettleRemoveEffect)
		if err != nil {
			return false, err
		}
		if remove.state != "applied" {
			return s.removeSettlementTarget(ctx, endpoint, lease, "remove_code", platformSettleRemoveEffect)
		}
		// Nothing may share the namespace with a restore, and a create the runner gave up on may have left a
		// container nothing accounts for. It is swept first, as an effect of its own.
		if swept, err := s.sweepSettlementNamespace(ctx, endpoint, lease); err != nil || !swept.done {
			return swept.progressed, err
		}
	} else if start.exists && start.state != "reverted" {
		// A start the update ended with still open: the runner may or may not have run it. It is read back under its
		// own identity and never sent; what comes back decides which of the paths here the update takes next.
		return s.settleSettlementStart(ctx, endpoint, lease, start)
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

// platformHeldStartKind marks the settlement effect (settle, slot 3) that records the runner holds the failed update's
// start with no running receipt: reserved and never created, or claimed with no executor. Its presence turns the target
// removal into the reserved-only one.
const platformHeldStartKind = "held-start"

// platformNeverRanReceipt closes the Portal's start effect once its target was removed without ever having run.
const platformNeverRanReceipt = `{"state":"never_ran"}`

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
		!validPlatformLifecycleProof(o, intent.raw, intent.authority, endpoint, receipt.ObservedMS, receipt.SettledMS, intent.command.IssuedMS, false) {
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
		// The discard was never admitted, and no command signed for it can still arrive. The runner refuses a
		// discard once the migration is no longer unknown, so before sending it again ask the migration.
		if done, handled, err := s.supersedeSettlementDiscard(ctx, endpoint, lease, intent); handled || err != nil {
			return done, err
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

// supersedeSettlementDiscard reads the migrate-data a discard was opened for. When the migration reached a
// definite end on its own (a receipt), the discard is closed as not applied and the migration is settled from
// that receipt, so settlement goes on from the migration's real state. It reports handled=false when the
// migration is still orphaned and the discard is still the way forward.
func (s *Server) supersedeSettlementDiscard(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease, discard platformDiscardIntent) (done, handled bool, err error) {
	migrate, err := s.signPlatformSettlementMigrate(ctx, lease)
	if err != nil {
		return false, true, err
	}
	runner := s.platformMigrationRunner()
	if err := runner.CheckRoot(ctx, lease.id, migrate.runtime, migrate.authority, endpoint); err != nil {
		return false, true, err
	}
	observation, readErr := runner.ReadMigrate(ctx, endpoint, migrate)
	if readErr != nil {
		return false, true, nil
	}
	receipt, ok := validPlatformMigrateObservation(observation, migrate, endpoint)
	if !ok {
		status, statusErr := observation.MigrationStatus()
		if statusErr != nil || !validPlatformMigrateStatus(observation, status, migrate.command.CommandHeader, migrate.raw, migrate.authority, endpoint) {
			return false, true, errAppPlatformStageEvidence
		}
		return false, status.State != "orphaned", nil
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return false, true, errAppPlatformStageEvidence
	}
	state := "reverted"
	if receipt.State == "migrated" {
		state = "applied"
	}
	return true, true, s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage='migrate' AND effect_number=3 AND effect_id=? AND kind='discard-migration' AND payload_sha256=?
			AND state='prepared' AND observed_fence=0`,
			platformAbsentReceipt, now, lease.id, discard.effectID, discard.command.PayloadSHA256)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		changed, err = e.exec(`UPDATE app_platform_operation_effects SET state=?,observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage='migrate' AND effect_number=2 AND effect_id=? AND kind='migrate-data' AND payload_sha256=?
			AND admitted_fence=? AND state IN ('prepared','unknown') AND observed_fence=0`,
			state, migrate.admittedFence, string(encoded), now, lease.id, migrate.effectID, migrate.command.PayloadSHA256, migrate.admittedFence)
		return platformLeaseCAS(changed, err)
	})
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
			if statusErr != nil || !validPlatformMigrateStatus(observation, status, platformRestoreHeader(intent.command), intent.raw, intent.authority, endpoint) {
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

// platformRestoreHeader is the identity a status for the restore must carry, in the shape the migrate and discard
// statuses are checked against.
func platformRestoreHeader(c control.RestoreCommand) control.CommandHeader {
	return control.CommandHeader{
		Format: c.Format, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: c.ControlRevision,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage,
		EffectNumber: c.EffectNumber, EffectID: c.EffectID, Kind: "restore", InstallationFence: c.InstallationFence,
		PayloadSHA256: c.PayloadSHA256, IssuedMS: c.IssuedMS, ExpiresMS: c.ExpiresMS,
	}
}

// platformSettlementStartRead is the original start effect of a finished update, rebuilt from the evidence it was
// built from and signed for a read.
type platformSettlementStartRead struct {
	intent platformUpdateStartIntent
	// reserved is set when the effect records the runner's reservation, which the runner cannot truthfully lack.
	reserved bool
	// definiteAfter is the time before which a command for the start may still be accepted, or a container create it
	// made may still be carried out. No command was signed after the update ended (the latest one was issued at most
	// the signing slack past it and expires one window later), the runner accepts a command until its own clock reaches
	// the expiry and may lag the Portal's by CommandClockSkewMS, and the Docker daemon may finish a request the runner
	// gave up on for the driver's request timeout. The start command's signed expiry itself is not recorded on its
	// effect, so the bound is derived from when the update ended.
	definiteAfter int64
	admitted      int64
}

// loadPlatformSettlementStart rebuilds the failed update's start command at the fence it was admitted under and
// proves it is the very command the effect row records. It signs and writes nothing.
func loadPlatformSettlementStart(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, now int64) (platformSettlementStartRead, error) {
	var zero platformSettlementStartRead
	fail := func(reason string) (platformSettlementStartRead, error) {
		return zero, errors.Join(errAppPlatformStageEvidence, errors.New("settlement start "+reason))
	}
	var effectID, kind, digest, epoch, state, receipt string
	var admitted, observed, completed int64
	if err := e.queryRow(`SELECT f.effect_id,f.kind,f.payload_sha256,f.controller_epoch,f.admitted_fence,f.observed_fence,f.state,f.receipt_json,o.completed_ms
		FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id
		WHERE f.operation_id=? AND f.stage='start' AND f.effect_number=1`, lease.id).
		Scan(&effectID, &kind, &digest, &epoch, &admitted, &observed, &state, &receipt, &completed); err != nil {
		return fail("effect")
	}
	// An applied start is loaded only for the sweep, which names the start it follows and needs nothing else of it.
	wantKind, sameRelease := "update-start", platformSameReleaseLifecycle(lease.snapshot.Action)
	if sameRelease {
		wantKind = platformSameReleaseStartKind(lease.snapshot.Action)
	}
	if lease.snapshot.Action == appplatform.ActionRestore {
		return fail("restore start")
	}
	if kind != wantKind || epoch != lease.controllerEpoch || admitted < 1 || admitted > lease.fence || (state != "prepared" && state != "unknown" && state != "applied") {
		return fail("effect row")
	}
	var reservation *journal.UpdateStartReservation
	if state == "applied" {
		if observed != admitted {
			return fail("effect row")
		}
	} else if receipt != "{}" {
		parsed, err := journal.ParseUpdateStartReservation([]byte(receipt))
		if err != nil || observed != admitted {
			return fail("reservation")
		}
		reservation = &parsed
	} else if observed != 0 {
		return fail("effect row")
	}
	// An enable or repair starts from the disabled installation's retained tuple, so its source is read in that mode.
	source, err := loadPlatformCheckpointSourceMode(e, lease, runtime, now, sameRelease, 0)
	if err != nil {
		return zero, fmt.Errorf("settlement start source: %w", err)
	}
	plan, descriptor, err := loadPlatformManagedUpdatePlan(e, lease, source)
	if err != nil {
		return zero, fmt.Errorf("settlement start plan: %w", err)
	}
	checkpoint, checkpointRaw, err := loadPlatformUpdateStartCheckpoint(e, lease, plan)
	if err != nil {
		return zero, fmt.Errorf("settlement start checkpoint: %w", err)
	}
	activation, err := platformResourceForUpdateStartEffect(e, lease, effectID)
	if err != nil {
		return zero, err
	}
	migration, err := loadPlatformAppliedMigration(e, lease.id, plan)
	if err != nil {
		return zero, err
	}
	effectLease := lease
	effectLease.fence = admitted
	command, err := platformUpdateStartCommand(effectLease, runtime, source, checkpoint, plan, activation, effectID, migration)
	if err != nil || command.PayloadSHA256 != digest || (reservation != nil && !platformUpdateStartReservationMatches(*reservation, command, activation, effectID)) {
		return fail("command")
	}
	intent := platformUpdateStartIntent{
		resourceID: activation, effectID: effectID, runtime: runtime, authority: platformImportAuthority(runtime),
		command: command, checkpoint: checkpoint, checkpointRaw: checkpointRaw, source: source, plan: plan,
		targetDescriptor: bytes.Clone(descriptor), reservation: reservation,
	}
	return platformSettlementStartRead{intent: intent, reserved: reservation != nil, admitted: admitted, definiteAfter: completed + platformSigningSlackMS + platformStartCommandWindowMS + platformImportAbsenceMarginMS + platformCreateGraceMS + control.CommandClockSkewMS}, nil
}

// signPlatformSettlementStartRead signs the failed update's start for a read.
func (s *Server) signPlatformSettlementStartRead(ctx context.Context, lease platformOperationLease) (platformSettlementStartRead, error) {
	var zero platformSettlementStartRead
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
	read, err := loadPlatformSettlementStart(e, lease, runtime, now)
	if err != nil {
		return zero, err
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 || issued > math.MaxInt64-platformStartCommandWindowMS {
		return zero, errAppPlatformLeaseConflict
	}
	read.intent.command.IssuedMS, read.intent.command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(issued+platformStartCommandWindowMS, 10)
	// A read is signed for a read and for nothing else: no execute-capable start command is made for a start that
	// finished, so intent.signature stays empty.
	if read.intent.raw, err = json.Marshal(read.intent.command); err != nil {
		return zero, err
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	if read.intent.readSignature, err = control.SignUpdateStartRead(read.intent.raw, key, read.intent.authority, issued); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return read, nil
}

// settleSettlementStart reads the open start effect of a failed update back from the runner and never sends it.
//
//   - A running receipt closes the effect as applied, and the target is then removed before anything else.
//   - A typed absence, once no command signed for the start can still arrive, closes it as not applied and drops
//     the candidate activation of a target that never ran. A start the effect records a reservation for cannot be
//     absent from a truthful journal, so that answer reverts nothing.
//   - Anything else changes nothing and is read again later.
func (s *Server) settleSettlementStart(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease, start platformSettleEffect) (bool, error) {
	if start.state != "prepared" && start.state != "unknown" {
		return false, nil
	}
	read, err := s.signPlatformSettlementStartRead(ctx, lease)
	if err != nil {
		return false, err
	}
	if err := s.platformMigrationRunner().CheckRoot(ctx, lease.id, read.intent.runtime, read.intent.authority, endpoint); err != nil {
		return false, err
	}
	observation, err := s.platformStartReceiptRead()(ctx, endpoint, read.intent)
	switch {
	case errors.Is(err, errPlatformStartAbsent):
		if read.reserved || time.Now().UnixMilli() <= read.definiteAfter {
			return false, nil
		}
		return true, s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
			changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
				WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND kind IN ('update-start','enable-start','restore-start') AND payload_sha256=?
				AND state IN ('prepared','unknown') AND receipt_json=?`,
				platformAbsentReceipt, now, lease.id, read.intent.effectID, read.intent.command.PayloadSHA256, start.receipt)
			if err := platformLeaseCAS(changed, err); err != nil {
				return err
			}
			_, err = e.exec(`DELETE FROM app_platform_activations WHERE id=? AND installation_id=? AND generation=? AND state='candidate'`,
				read.intent.resourceID, lease.installationID, lease.snapshot.Generation)
			return err
		})
	case err != nil && platformSettlementRefusal(err, read.intent):
		// The runner refused the start for lack of room: it never ran and never will, whatever the signed window.
		refusal, _ := platformCapacityRefusalOf(err)
		raw, _ := json.Marshal(refusal)
		return true, s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
			changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
				WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND kind IN ('update-start','enable-start','restore-start') AND payload_sha256=?
				AND state IN ('prepared','unknown') AND receipt_json=?`,
				string(raw), now, lease.id, read.intent.effectID, read.intent.command.PayloadSHA256, start.receipt)
			if err := platformLeaseCAS(changed, err); err != nil {
				return err
			}
			_, err = e.exec(`DELETE FROM app_platform_activations WHERE id=? AND installation_id=? AND generation=? AND state='candidate'`,
				read.intent.resourceID, lease.installationID, lease.snapshot.Generation)
			return err
		})
	case err != nil:
		var held *journal.UpdateStartHeldError
		if !errors.As(err, &held) {
			// Unreachable: read again later.
			return false, nil
		}
		return s.settleHeldStart(ctx, endpoint, lease, read, held.State)
	}
	running, ok := validPlatformUpdateStartRunningObservation(observation, read.intent, endpoint)
	if !ok {
		return false, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(running)
	if err != nil || len(encoded) > 16384 {
		return false, errAppPlatformStageEvidence
	}
	return true, s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND kind IN ('update-start','enable-start','restore-start') AND payload_sha256=?
			AND state IN ('prepared','unknown') AND receipt_json=?`,
			read.admitted, string(encoded), now, lease.id, read.intent.effectID, read.intent.command.PayloadSHA256, start.receipt)
		return platformLeaseCAS(changed, err)
	})
}

// Receipts of the held-start record (settle, slot 3): what the runner was last read to hold the start as.
const (
	platformHeldReservedReceipt = `{"state":"reserved"}`
	platformHeldOrphanedReceipt = `{"state":"orphaned"}`
	// platformNotAppliedReceipt closes a reserved-removal effect that the runner can no longer carry out.
	platformNotAppliedReceipt = `{"state":"not_applied"}`
)

// settleHeldStart acts on what the runner says it holds for the open start. It is called every tick with a fresh
// read.
//
// Nothing is done before the start's signed window has closed, plus the time a container create the runner gave up
// on can still be carried out: until then a claim may arrive, and a start read as reserved is no more than that. Once
// it has closed no claim can be admitted (the runner refuses an expired command), so a start read as reserved stays
// reserved.
//
//   - pending: the start may be executing; wait.
//   - orphaned: claimed with nothing executing it. A container may exist, so only the runner can say what happened:
//     it is asked to settle the start, and the next read shows the result (running, or never run). The record is
//     marked orphaned first, which is what tells a later never_ran that the runner's settlement, not a removal,
//     ended the start and that the target is already gone.
//   - reserved: the runner holds a pending target and no claim can still be made; the target is removed bound to the
//     reservation once the held-start record is in.
//   - never_ran: the start is settled as not run. If a reserved removal began, it is finished first.
func (s *Server) settleHeldStart(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease, read platformSettlementStartRead, state string) (bool, error) {
	if time.Now().UnixMilli() <= read.definiteAfter {
		return false, nil
	}
	pool := s.st.poolExec()
	marker, err := loadPlatformSettleEffect(pool, lease.id, platformSettleStage, platformSettleHeldEffect)
	if err != nil {
		return false, err
	}
	switch state {
	case "orphaned":
		if marker.exists && marker.kind != platformHeldStartKind {
			return false, errAppPlatformStageEvidence
		}
		if marker.receipt != platformHeldOrphanedReceipt {
			return true, s.recordHeldStart(ctx, lease, read, platformHeldOrphanedReceipt, marker)
		}
		return s.settleOrphanedStart(ctx, endpoint, lease)
	case "reserved":
		if platformSameReleaseLifecycle(lease.snapshot.Action) {
			// Nothing was claimed, so no capacity is held; the removal of its target is built from an update's own
			// checkpoint and is not taken for an enable.
			return false, nil
		}
		if !marker.exists {
			return true, s.recordHeldStart(ctx, lease, read, platformHeldReservedReceipt, marker)
		}
		if marker.receipt != platformHeldReservedReceipt {
			// A claimed start cannot become reserved again.
			return false, errAppPlatformStageEvidence
		}
		return s.reservedStartStep(ctx, endpoint, lease)
	case "never_ran":
		if marker.exists && marker.receipt == platformHeldReservedReceipt {
			return s.reservedStartStep(ctx, endpoint, lease)
		}
		// No removal of ours ended this start: the runner settled it, and its target is already gone. What is left to
		// make sure of is that no container of the namespace survives, which is swept before the start is closed.
		if swept, err := s.sweepSettlementNamespace(ctx, endpoint, lease); err != nil || !swept.done {
			return swept.progressed, err
		}
		return true, s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
			changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
				WHERE operation_id=? AND stage='start' AND effect_number=1 AND kind IN ('update-start','enable-start','restore-start') AND state IN ('prepared','unknown')`,
				platformNeverRanReceipt, now, lease.id)
			if err := platformLeaseCAS(changed, err); err != nil {
				return err
			}
			if err := closeReservedRemovalRows(e, lease.id, now); err != nil {
				return err
			}
			// Nothing ran and nothing was removed, so no settlement evidence stands for the candidate: drop it.
			_, err = e.exec(`DELETE FROM app_platform_activations WHERE id=? AND installation_id=? AND generation=? AND state='candidate'`,
				read.intent.resourceID, lease.installationID, lease.snapshot.Generation)
			return err
		})
	}
	return false, nil
}

// recordHeldStart records what the runner holds the start as. Recording a claimed start also closes any removal of a
// reserved target that was begun: a claimed start is not reserved, so the runner refuses it for good.
func (s *Server) recordHeldStart(ctx context.Context, lease platformOperationLease, read platformSettlementStartRead, receipt string, marker platformSettleEffect) error {
	return s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		if !marker.exists {
			if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
				SELECT operation_id,'settle',3,?,?,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,'applied',?,?,? FROM app_platform_operation_effects
				WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND state IN ('prepared','unknown')`,
				platformDerivedUUID("held-start", read.intent.effectID), platformHeldStartKind, receipt, now, now, lease.id, read.intent.effectID); err != nil {
				return err
			}
		} else {
			changed, err := e.exec(`UPDATE app_platform_operation_effects SET receipt_json=?,updated_ms=?
				WHERE operation_id=? AND stage='settle' AND effect_number=3 AND kind=? AND state='applied' AND receipt_json=?`,
				receipt, now, lease.id, platformHeldStartKind, marker.receipt)
			if err := platformLeaseCAS(changed, err); err != nil {
				return err
			}
		}
		if receipt == platformHeldOrphanedReceipt {
			return closeReservedRemovalRows(e, lease.id, now)
		}
		return nil
	})
}

// closeReservedRemovalRows closes the reserved-removal effects that are still only prepared as not applied.
func closeReservedRemovalRows(e migExec, operation string, now int64) error {
	_, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='settle' AND effect_number IN (?,?) AND state='prepared'`,
		platformNotAppliedReceipt, now, operation, platformSettleReservedStopEffect, platformSettleReservedRemoveEffect)
	return err
}

// closeReservedRemovals closes the reserved-removal effects of an update whose start turned out to be running. The
// running receipt was just read from the runner, which refuses a reserved removal for a start that is not reserved,
// so such an effect is definitely not applied; one recorded as applied cannot coexist with a running start.
func (s *Server) closeReservedRemovals(ctx context.Context, lease platformOperationLease) (bool, error) {
	pool := s.st.poolExec()
	open := false
	for _, number := range []int{platformSettleReservedStopEffect, platformSettleReservedRemoveEffect} {
		effect, err := loadPlatformSettleEffect(pool, lease.id, platformSettleStage, number)
		if err != nil {
			return false, err
		}
		switch effect.state {
		case "prepared":
			open = true
		case "applied":
			return false, errAppPlatformStageEvidence
		}
	}
	if !open {
		return false, nil
	}
	return true, s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		return closeReservedRemovalRows(e, lease.id, now)
	})
}

// reservedStartStep takes the next step of removing a reserved target: stop, then remove, each bound to the
// reservation; once both are applied the start is closed as never run.
func (s *Server) reservedStartStep(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease) (bool, error) {
	pool := s.st.poolExec()
	for _, step := range []struct {
		stage  string
		number int
	}{{"stop", platformSettleReservedStopEffect}, {"remove_code", platformSettleReservedRemoveEffect}} {
		effect, err := loadPlatformSettleEffect(pool, lease.id, platformSettleStage, step.number)
		if err != nil {
			return false, err
		}
		if effect.state != "applied" {
			return s.removeSettlementTarget(ctx, endpoint, lease, step.stage, step.number)
		}
	}
	if swept, err := s.sweepSettlementNamespace(ctx, endpoint, lease); err != nil || !swept.done {
		return swept.progressed, err
	}
	return true, s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage='start' AND effect_number=1 AND kind IN ('update-start','enable-start','restore-start') AND state IN ('prepared','unknown')`,
			platformNeverRanReceipt, now, lease.id)
		return platformLeaseCAS(changed, err)
	})
}

// platformSettleStartIntent is the signed settle-update-start command for an orphaned start.
type platformSettleStartIntent struct {
	raw, signature []byte
	authority      control.Authority
	runtime        platformRuntimeRegistration
	descriptor     []byte
	// The identity the runner's receipt must carry.
	mode                                         string
	effectID, operationID, digest, startEffectID string
	resourceID, namespaceID                      string
}

// platformSettleStartRunner sends the command to the runner and answers its receipt.
type platformSettleStartRunner func(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformSettleStartIntent) (journal.SettleUpdateStartReceipt, error)

func (s *Server) platformSettleStartRun() platformSettleStartRunner {
	if s.platformSettleStart != nil {
		return s.platformSettleStart
	}
	return platformRunSettleUpdateStart
}

// signPlatformSettlementOrphan signs the command that settles the orphaned start. Its start reference is signed for
// a read, so nothing execute-capable is made for the start; its own effect and operation identities are derived from
// the start's, so a command signed again names the same settlement.
func (s *Server) signPlatformSettlementOrphan(ctx context.Context, lease platformOperationLease) (platformSettleStartIntent, error) {
	return s.signPlatformSettlementStartCommand(ctx, lease, control.SettleModeSettle)
}

// signPlatformSettlementStartCommand signs the settle-update-start command of a mode. The sweep is an effect of the
// update's own (slot 6), recorded as prepared in the transaction that signs it, so what is sent again is the same
// effect, and its receipt is read back under its own identity.
func (s *Server) signPlatformSettlementStartCommand(ctx context.Context, lease platformOperationLease, mode string) (platformSettleStartIntent, error) {
	var zero platformSettleStartIntent
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
	read, err := loadPlatformSettlementStart(e, lease, runtime, now)
	if err != nil {
		return zero, err
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 || issued > math.MaxInt64-platformStartCommandWindowMS {
		return zero, errAppPlatformLeaseConflict
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	command, intent, err := buildPlatformSettleStartCommand(read.intent, key, lease.id, lease.fence, mode, issued)
	if err != nil {
		return zero, err
	}
	if mode == control.SettleModeSweep {
		existing, err := loadPlatformSettleEffect(e, lease.id, platformSettleStage, platformSettleSweepEffect)
		if err != nil {
			return zero, err
		}
		switch {
		case !existing.exists:
			if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
				VALUES(?,?,?,?,?,?,?,?,?,0,'prepared','{}',?,?)`,
				lease.id, platformSettleStage, platformSettleSweepEffect, command.EffectID, platformSweepKind, command.PayloadSHA256, runtime.id, lease.controllerEpoch, lease.fence, now, now); err != nil {
				return zero, err
			}
		case existing.kind != platformSweepKind || existing.state != "prepared" || existing.effectID != command.EffectID || existing.digest != command.PayloadSHA256 || existing.admitted != lease.fence:
			return zero, errAppPlatformStageEvidence
		}
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return intent, nil
}

// buildPlatformSettleStartCommand signs the settle-update-start command of a mode for the start an intent describes.
// The start reference is signed for a read, so nothing execute-capable is made for the start; the command's own
// effect and operation identities are derived from the start's and from the operation's, so a command signed again
// names the same settlement.
func buildPlatformSettleStartCommand(read platformUpdateStartIntent, key ed25519.PrivateKey, operationID string, fence int64, mode string, issued int64) (control.SettleUpdateStartCommand, platformSettleStartIntent, error) {
	var zeroCommand control.SettleUpdateStartCommand
	var zero platformSettleStartIntent
	start := read.command
	start.IssuedMS, start.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(issued+platformStartCommandWindowMS, 10)
	startRaw, err := json.Marshal(start)
	if err != nil {
		return zeroCommand, zero, err
	}
	startRead, err := control.SignUpdateStartRead(startRaw, key, read.authority, issued)
	if err != nil {
		return zeroCommand, zero, err
	}
	label := "settle-update-start"
	if mode == control.SettleModeSweep {
		label = "sweep-update-start"
	}
	command := control.SettleUpdateStartCommand{
		Format: control.SettleUpdateStartFormat, Mode: mode, EffectID: platformDerivedUUID(label, read.effectID),
		OperationID: platformDerivedUUID(label+"-operation", operationID), InstallationFence: strconv.FormatInt(fence, 10),
		IssuedMS: strconv.FormatInt(issued, 10), ExpiresMS: strconv.FormatInt(issued+platformStartCommandWindowMS, 10),
		StartRaw: startRaw, StartReadSignature: startRead,
	}
	if command.PayloadSHA256, err = control.SettleUpdateStartLogicalDigest(command); err != nil {
		return zeroCommand, zero, err
	}
	raw, signature, err := control.SignSettleUpdateStart(command, key, read.authority, issued)
	if err != nil {
		return zeroCommand, zero, err
	}
	return command, platformSettleStartIntent{
		raw: raw, signature: signature, authority: read.authority, runtime: read.runtime, descriptor: bytes.Clone(read.targetDescriptor),
		mode: mode, effectID: command.EffectID, operationID: command.OperationID, digest: command.PayloadSHA256, startEffectID: start.EffectID,
		resourceID: start.Payload.ResourceID, namespaceID: start.Payload.NamespaceID,
	}, nil
}

// settleOrphanedStart asks the runner to settle the orphaned start. Whatever it answers, the next tick reads the start
// itself: the settled start reads as running or as never run, and a refusal (the start moved on meanwhile) reads as
// whatever it has become.
func (s *Server) settleOrphanedStart(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease) (bool, error) {
	intent, err := s.signPlatformSettlementOrphan(ctx, lease)
	if err != nil {
		return false, err
	}
	if err := s.platformMigrationRunner().CheckRoot(ctx, lease.id, intent.runtime, intent.authority, endpoint); err != nil {
		return false, err
	}
	if _, err := s.platformSettleStartRun()(ctx, endpoint, intent); err != nil {
		return false, nil
	}
	return true, nil
}

// platformSweepStatus is the outcome of a sweep step: whether the sweep is not needed or finished, and whether the
// step changed anything.
type platformSweepStatus struct{ done, progressed bool }

// sweepSettlementNamespace makes sure no container labeled for the namespace survives that nothing accounts for,
// before the checkpoint is restored into it. A create the runner gave up on can still be carried out by the Docker
// daemon after the start was settled, and the start's own removal cannot know of it. It runs once the start's own
// target is gone and before the start is closed (or, for a start that ran, before anything else is done), so the
// start it names is still the one the effect records.
func (s *Server) sweepSettlementNamespace(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease) (platformSweepStatus, error) {
	sweep, err := loadPlatformSettleEffect(s.st.poolExec(), lease.id, platformSettleStage, platformSettleSweepEffect)
	if err != nil {
		return platformSweepStatus{}, err
	}
	if sweep.state == "applied" {
		return platformSweepStatus{done: true}, nil
	}
	intent, err := s.signPlatformSettlementStartCommand(ctx, lease, control.SettleModeSweep)
	if err != nil {
		return platformSweepStatus{}, err
	}
	if err := s.platformMigrationRunner().CheckRoot(ctx, lease.id, intent.runtime, intent.authority, endpoint); err != nil {
		return platformSweepStatus{}, err
	}
	receipt, err := s.platformSettleStartRun()(ctx, endpoint, intent)
	if err != nil {
		return platformSweepStatus{}, nil
	}
	if receipt.Outcome == "blocked" && receipt.Reason == journal.SettleReasonForeignContainer && receipt.EffectID == intent.effectID && receipt.PayloadSHA256 == intent.digest {
		// Failing closed, visibly: the reason is kept on the sweep effect for the settlement projection, and the sweep
		// is sent again on later ticks, so it clears once the container is dealt with.
		err := s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
			_, err := e.exec(`UPDATE app_platform_operation_effects SET receipt_json=?,updated_ms=?
				WHERE operation_id=? AND stage=? AND effect_number=? AND effect_id=? AND kind=? AND state='prepared'`,
				platformBlockedSweepReceipt, now, lease.id, platformSettleStage, platformSettleSweepEffect, intent.effectID, platformSweepKind)
			return err
		})
		return platformSweepStatus{}, err
	}
	if receipt.Outcome != "swept" || receipt.EffectID != intent.effectID || receipt.OperationID != intent.operationID || receipt.PayloadSHA256 != intent.digest ||
		receipt.StartEffectID != intent.startEffectID || receipt.ResourceID != intent.resourceID || receipt.NamespaceID != intent.namespaceID {
		return platformSweepStatus{}, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return platformSweepStatus{}, errAppPlatformStageEvidence
	}
	err = s.settleEffectRow(ctx, lease, func(e migExec, now int64) error {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage=? AND effect_number=? AND effect_id=? AND kind=? AND payload_sha256=? AND state='prepared' AND receipt_json IN ('{}',?)`,
			lease.fence, string(encoded), now, lease.id, platformSettleStage, platformSettleSweepEffect, intent.effectID, platformSweepKind, intent.digest, platformBlockedSweepReceipt)
		return platformLeaseCAS(changed, err)
	})
	return platformSweepStatus{progressed: err == nil}, err
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
	var start platformUpdateStartIntent
	// The reserved-only removal has slots of its own and is for a start that is still open. One that got its running
	// receipt is removed like any running target, by an effect of the ordinary slots.
	reservedOnly := number == platformSettleReservedStopEffect || number == platformSettleReservedRemoveEffect
	if reservedOnly {
		held, err := loadPlatformSettleEffect(e, lease.id, platformSettleStage, platformSettleHeldEffect)
		if err != nil {
			return zero, err
		}
		startEffect, err := loadPlatformSettleEffect(e, lease.id, "start", 1)
		if err != nil {
			return zero, err
		}
		if !held.exists || held.kind != platformHeldStartKind || held.receipt != platformHeldReservedReceipt || held.state != "applied" ||
			(startEffect.state != "prepared" && startEffect.state != "unknown") {
			return zero, errAppPlatformStageEvidence
		}
		// The target was reserved and never created: there is no running receipt, and the start is the one the
		// effect row records, rebuilt from the evidence it was built from.
		read, err := loadPlatformSettlementStart(e, lease, runtime, now)
		if err != nil {
			return zero, fmt.Errorf("settlement reserved removal source: %w", err)
		}
		start = read.intent
	} else {
		start, _, _, _, err = loadPlatformUpdateTargetRemovalPlan(e, lease, runtime, checkpoint, now)
		if err != nil || start.running == nil {
			return zero, fmt.Errorf("settlement target removal source: %w", errors.Join(errAppPlatformStageEvidence, err))
		}
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
	if reservedOnly {
		removalLease.id = platformDerivedUUID("settlement-reserved-removal", lease.id)
	}
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
		sourceOperation: lease.id, nativeID: platformRemovalNativeID(start), effectStage: platformSettleStage, effectNumber: number,
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

// platformSettlementRefusal reports whether err is the runner's capacity refusal of exactly this start.
func platformSettlementRefusal(err error, intent platformUpdateStartIntent) bool {
	refusal, ok := platformCapacityRefusalOf(err)
	return ok && refusal.Matches(journal.CapacityIdentityOfUpdateStart(intent.command))
}
