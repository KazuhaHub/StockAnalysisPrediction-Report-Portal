package journal

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

var ErrCommandConflict = errors.New("runner command identity, generation or fence conflict")
var ErrFenceReconciliation = errors.New("runner requires durable fence reconciliation")

// CommandAdmission describes persisted intent/historical state, never a physical
// effect receipt or dispatch permission. Replayed prepared/unknown work requires
// reconciliation; it must not be executed again just because this call succeeded.
type CommandAdmission struct {
	EffectID, State             string
	AdmittedFence, CurrentFence int64
	Replayed                    bool
}

type admittedCommand struct {
	effectID, operationID, installationID, stage, kind, payloadSHA, epoch, state string
	slot, generation, fence                                                      int64
}

func loadAdmittedCommand(ctx context.Context, tx *sql.Tx, effect string) (admittedCommand, error) {
	var c admittedCommand
	err := tx.QueryRowContext(ctx, `SELECT effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state FROM runner_commands WHERE effect_id=?`, effect).Scan(&c.effectID, &c.operationID, &c.installationID, &c.stage, &c.slot, &c.kind, &c.payloadSHA, &c.generation, &c.epoch, &c.fence, &c.state)
	return c, err
}
func sameLogicalCommand(stored admittedCommand, c control.Command) bool {
	return stored.effectID == c.EffectID && stored.operationID == c.OperationID && stored.installationID == c.InstallationID && stored.stage == c.Stage && stored.kind == c.Kind && stored.payloadSHA == c.PayloadSHA256 && stored.epoch == c.ControllerEpoch && strconv.FormatInt(stored.slot, 10) == c.EffectNumber && strconv.FormatInt(stored.generation, 10) == c.TargetGeneration
}

// AdmitControlCommand verifies against explicit durable controller trust after
// acquiring journal write intent. It only persists a prepare-data intent. No
// socket, resource mutation, receipt, or physical completion path is attached.
func (s *Store) AdmitControlCommand(ctx context.Context, raw, signature []byte) (CommandAdmission, error) {
	var zero CommandAdmission
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return zero, control.ErrInvalidCommand
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.Verify(raw, signature, authority, now)
	if err != nil {
		return zero, err
	}
	c := verified.Command()
	fence, _ := strconv.ParseInt(c.InstallationFence, 10, 64)
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
	var incompatible int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND controller_epoch<>?)+(SELECT COUNT(*) FROM runner_resources WHERE (installation_id=? OR data_namespace_id=?) AND controller_epoch<>?)`, c.InstallationID, c.ControllerEpoch, c.InstallationID, c.Payload.DataNamespaceID, c.ControllerEpoch).Scan(&incompatible); err != nil {
		return zero, err
	}
	if incompatible != 0 {
		return zero, ErrFenceReconciliation
	}
	var floor, generationFloor, writerFloor int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(fence),0),COALESCE(MAX(generation),0) FROM (SELECT admitted_fence AS fence,target_generation AS generation FROM runner_commands WHERE installation_id=? UNION ALL SELECT control_fence AS fence,generation FROM runner_resources WHERE installation_id=?)`, c.InstallationID, c.InstallationID).Scan(&floor, &generationFloor); err != nil {
		return zero, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(writer_fence),0) FROM runner_resources WHERE data_namespace_id=?`, c.Payload.DataNamespaceID).Scan(&writerFloor); err != nil {
		return zero, err
	}
	var foreignNamespace int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources WHERE data_namespace_id=? AND installation_id<>?`, c.Payload.DataNamespaceID, c.InstallationID).Scan(&foreignNamespace); err != nil {
		return zero, err
	}
	if fence < floor || generation < generationFloor || writer < writerFloor || foreignNamespace != 0 {
		return zero, ErrCommandConflict
	}
	stored, err := loadAdmittedCommand(ctx, tx, c.EffectID)
	var result CommandAdmission
	if err == nil {
		if !sameLogicalCommand(stored, c) {
			return zero, ErrCommandConflict
		}
		// Original admitted_fence is immutable. Without a newer durable command or
		// owned resource floor, a higher-fence retry cannot silently record takeover.
		if fence > floor {
			return zero, ErrFenceReconciliation
		}
		result = CommandAdmission{EffectID: stored.effectID, State: stored.state, AdmittedFence: stored.fence, CurrentFence: floor, Replayed: true}
	} else if errors.Is(err, sql.ErrNoRows) {
		var conflicts int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE (operation_id=? AND stage=? AND effect_number=?) OR (installation_id=? AND state IN ('prepared','unknown'))`, c.OperationID, c.Stage, c.EffectNumber, c.InstallationID).Scan(&conflicts); err != nil {
			return zero, err
		}
		if conflicts != 0 {
			return zero, ErrCommandConflict
		}
		slot, _ := strconv.ParseInt(c.EffectNumber, 10, 64)
		if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,?,?,'prepared','{}',NULL,?,?)`, c.EffectID, c.OperationID, c.InstallationID, c.Stage, slot, c.Kind, c.PayloadSHA256, generation, c.ControllerEpoch, fence, now, now); err != nil {
			return zero, err
		}
		result = CommandAdmission{EffectID: c.EffectID, State: "prepared", AdmittedFence: fence, CurrentFence: fence}
	} else {
		return zero, err
	}
	finalNow := time.Now().UTC().UnixMilli()
	if finalNow < now {
		return zero, control.ErrInvalidCommand
	}
	if _, err := control.Verify(raw, signature, authority, finalNow); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
