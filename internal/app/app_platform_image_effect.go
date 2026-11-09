package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// The load-image effect row. Kind load_image, stage "stage", number 1, the id derived from the operation and
// release. The kind column has no enum check, so this needs no schema change. The row is written before the
// first command leaves Portal, and ends applied (receipt) or reverted (a definite rejection). A load the gate
// gives up on is left unknown: the operation ends recovery_required, an unsettled effect keeps it there (see
// settlePlatformRecoveryOperation), and settlePlatformLoadImages reads it back and sends it again under the same
// id until it settles.
const platformLoadImageKind = "load_image"

type platformImageRows struct {
	s         *Server
	lease     platformOperationLease
	runtimeID string
	// guard confirms, in the writing transaction, that the caller may still act for the operation, and returns
	// the database time.
	guard func(migExec) (int64, error)
}

func (r platformImageRows) with(ctx context.Context, fn func(e migExec, now int64) error) error {
	tx, err := r.s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: r.s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return err
	}
	now, err := r.guard(e)
	if err != nil {
		return err
	}
	if err := fn(e, now); err != nil {
		return err
	}
	if _, err := r.guard(e); err != nil {
		return err
	}
	return tx.Commit()
}

// prepare writes the row before the first command is sent. It reports applied when an earlier attempt already
// settled the load, and refuses a load an earlier attempt saw rejected.
func (r platformImageRows) prepare(ctx context.Context, c control.LoadImageCommand) (applied bool, err error) {
	err = r.with(ctx, func(e migExec, now int64) error {
		var id, kind, digest, state string
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		qerr := e.queryRow(`SELECT effect_id,kind,payload_sha256,state FROM app_platform_operation_effects WHERE operation_id=? AND stage=? AND effect_number=1`+lock, r.lease.id, control.LoadImageStage).
			Scan(&id, &kind, &digest, &state)
		if errors.Is(qerr, sql.ErrNoRows) {
			_, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
				VALUES(?,?,1,?,?,?,?,?,?,0,'prepared','{}',?,?)`, r.lease.id, control.LoadImageStage, c.EffectID, platformLoadImageKind, c.PayloadSHA256, r.runtimeID, r.lease.controllerEpoch, r.lease.fence, now, now)
			return err
		}
		if qerr != nil {
			return qerr
		}
		if kind != platformLoadImageKind || id != c.EffectID || digest != c.PayloadSHA256 {
			return errAppPlatformStageEvidence
		}
		switch state {
		case "applied":
			applied = true
		case "reverted":
			return fmt.Errorf("%w: an earlier attempt saw the runner reject the image", errPlatformImageRejected)
		case "prepared", "unknown":
		default:
			return errAppPlatformStageEvidence
		}
		return nil
	})
	return applied, err
}

func (r platformImageRows) settle(ctx context.Context, c control.LoadImageCommand, state, receipt string) error {
	if len(receipt) > 16384 {
		return errAppPlatformStageEvidence
	}
	return r.with(ctx, func(e migExec, now int64) error {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state=?,observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage=? AND effect_number=1 AND effect_id=? AND kind=? AND state IN ('prepared','unknown')`,
			state, r.lease.fence, receipt, now, r.lease.id, control.LoadImageStage, c.EffectID, platformLoadImageKind)
		return platformLeaseCAS(changed, err)
	})
}

// unknown marks a load that was sent and has no verdict. It is best effort: a failure leaves the row prepared,
// which the settlement driver treats the same way.
func (r platformImageRows) unknown(ctx context.Context, c control.LoadImageCommand) error {
	return r.with(ctx, func(e migExec, now int64) error {
		_, err := e.exec(`UPDATE app_platform_operation_effects SET state='unknown',updated_ms=?
			WHERE operation_id=? AND stage=? AND effect_number=1 AND effect_id=? AND kind=? AND state='prepared'`,
			now, r.lease.id, control.LoadImageStage, c.EffectID, platformLoadImageKind)
		return err
	})
}

// platformLoadSettlementLease reads a finished operation as the unleased identity a settlement command is bound
// to.
func platformLoadSettlementLease(e migExec, operation string) (platformOperationLease, error) {
	var id, installation, epoch, stage, action string
	var fence, generation, stateRevision, rowRevision int64
	var completed sql.NullInt64
	var owner sql.NullString
	if err := e.queryRow(`SELECT id,installation_id,controller_epoch,fence,target_generation,state_revision,row_revision,stage,action,completed_ms,lease_owner
		FROM app_platform_operations WHERE id=? AND managed=1`, operation).
		Scan(&id, &installation, &epoch, &fence, &generation, &stateRevision, &rowRevision, &stage, &action, &completed, &owner); err != nil ||
		stage != "recovery_required" || !completed.Valid || owner.Valid || generation < 1 {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	return platformOperationLease{
		id: id, installationID: installation, controllerEpoch: epoch, fence: fence, rowRevision: rowRevision,
		leaseUntilMS: time.Now().Add(5 * time.Minute).UnixMilli(),
		snapshot: appplatform.OperationSnapshot{Action: appplatform.Action(action), Managed: true, Generation: uint64(generation),
			Revision: uint64(stateRevision), Stage: appplatform.StageRecoveryRequired},
	}, nil
}

// platformLoadSettlementGuard checks under the mutation lock that the finished operation is what the settlement
// was derived from, and that nothing has moved the installation's fence past it.
func platformLoadSettlementGuard(e migExec, lease platformOperationLease) (int64, error) {
	current, err := platformLoadSettlementLease(e, lease.id)
	if err != nil || current.fence != lease.fence || current.controllerEpoch != lease.controllerEpoch || current.installationID != lease.installationID {
		return 0, errors.Join(errAppPlatformLeaseConflict, err)
	}
	var epoch string
	if err := e.queryRow(`SELECT COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),'')`).Scan(&epoch); err != nil || epoch != lease.controllerEpoch {
		return 0, errAppPlatformControlPaused
	}
	var fence int64
	var installationEpoch string
	if err := e.queryRow(`SELECT control_fence,controller_epoch FROM app_platform_installations WHERE id=?`, lease.installationID).Scan(&fence, &installationEpoch); err != nil ||
		fence != lease.fence || installationEpoch != lease.controllerEpoch {
		return 0, errAppPlatformLeaseConflict
	}
	return platformDatabaseNow(e)
}

// platformLoadSettlementCandidates lists the finished operations that hold a load with no verdict.
func (s *Server) platformLoadSettlementCandidates(ctx context.Context) ([]string, error) {
	rows, err := s.st.db.QueryContext(ctx, s.st.bind(`SELECT o.id FROM app_platform_operations o
		JOIN app_platform_operation_effects f ON f.operation_id=o.id AND f.stage='stage' AND f.effect_number=1 AND f.kind='load_image'
		WHERE o.managed=1 AND o.stage='recovery_required' AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL
		AND f.state IN ('prepared','unknown') ORDER BY o.completed_ms,o.id LIMIT 8`))
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

// settlePlatformLoadImages takes one settlement step for each finished operation that holds an unknown load:
// the same effect is read back and, while the runner reports nothing running it, sent again under the same id,
// with its blobs uploaded when the runner names them. It returns how many loads settled.
func (s *Server) settlePlatformLoadImages(ctx context.Context, endpoint platformRuntimeEndpoint) (int, error) {
	if endpoint.path == "" {
		return 0, nil
	}
	ids, err := s.platformLoadSettlementCandidates(ctx)
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, id := range ids {
		lease, err := platformLoadSettlementLease(s.st.poolExec(), id)
		if err == nil {
			err = s.settlePlatformLoadImage(ctx, endpoint, lease)
		}
		switch {
		case err == nil:
			settled++
		case ctx.Err() != nil:
			return settled, ctx.Err()
		case errors.Is(err, errPlatformImageBusy):
			// The runner still holds it: the same effect is read again on the next tick.
		default:
			log.Printf("app-platform load-image settlement %s: %v", id, err)
		}
	}
	return settled, nil
}

func (s *Server) settlePlatformLoadImage(ctx context.Context, endpoint platformRuntimeEndpoint, lease platformOperationLease) error {
	target, err := s.loadPlatformImageTarget(ctx, lease)
	if err != nil {
		return err
	}
	if target == nil {
		return errAppPlatformStageEvidence
	}
	guard := func(e migExec) (int64, error) { return platformLoadSettlementGuard(e, lease) }
	signer, err := s.newPlatformImageSignerGuarded(ctx, lease, func(e migExec) error { _, err := guard(e); return err })
	if err != nil {
		return err
	}
	defer signer.close()
	rows := platformImageRows{s: s, lease: lease, runtimeID: signer.runtime.id, guard: guard}
	return s.runPlatformImageLoad(ctx, endpoint, signer, target, rows)
}

// platformRequireSettledLoad refuses a recovery choice while the failed operation holds a load with no verdict:
// the runner may still be loading, or may load later, under that id. It is lifted when the settlement driver
// has read the load back (or sent it again) and it is applied or reverted.
func platformRequireSettledLoad(e migExec, operation string) error {
	var open int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind='load_image' AND state IN ('prepared','unknown')`, operation).Scan(&open); err != nil {
		return err
	}
	if open != 0 {
		return errAppPlatformMigrationUnsettled
	}
	return nil
}
