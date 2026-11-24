package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// A checkpoint only reads the namespace: it never writes a byte to the data. So when the walker refuses the tree
// (an entry it does not archive, such as a symlink, or an archive over its cap) the outcome is known and nothing
// needs reconciling: the command ends reverted with a typed failure record, the checkpoint row ends, and the
// answer to the sender is a typed failure it can tell apart from a lost reply.

// CheckpointCommandHeader is the header a checkpoint's typed failure status is bound to.
func CheckpointCommandHeader(c control.CheckpointCommand) control.CommandHeader {
	return control.CommandHeader{
		Format: c.Format, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: c.ControlRevision,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage,
		EffectNumber: c.EffectNumber, EffectID: c.EffectID, Kind: c.Kind, InstallationFence: c.InstallationFence,
		PayloadSHA256: c.PayloadSHA256, IssuedMS: c.IssuedMS, ExpiresMS: c.ExpiresMS,
	}
}

// classifyCheckpointRefusal reports the failure code of a native error that is a definite refusal. An error the
// native layer joined with ErrCheckpointIncomplete (the archive may be half written or unverified) is not one, and
// neither is any error that is not about the content of the namespace.
func classifyCheckpointRefusal(err error) (string, bool) {
	switch {
	case err == nil, errors.Is(err, data.ErrCheckpointIncomplete):
		return "", false
	case errors.Is(err, data.ErrCheckpointTooLarge):
		return dataFailureCheckpointTooLarge, true
	case errors.Is(err, data.ErrCheckpointUnsafe):
		return dataFailureCheckpointUnsafe, true
	}
	return "", false
}

// checkpointRefusedError is the typed answer for a checkpoint that ended refused.
func checkpointRefusedError(command control.CheckpointCommand, code string, cause error) *DataEffectFailedError {
	return &DataEffectFailedError{Header: CheckpointCommandHeader(command), Code: code, Cause: cause}
}

// refuseCheckpointTx settles the claimed snapshot as refused in the caller's transaction: the command reverted with
// the failure record, and the checkpoint row, still exactly as the claim left it, ended. The schema has no failed
// state for a checkpoint; deleted is its terminal state, and nothing is left of the archive to delete.
func refuseCheckpointTx(ctx context.Context, tx *sql.Tx, command control.CheckpointCommand, code string, now int64) error {
	encoded, err := json.Marshal(dataEffectFailure{Format: dataEffectFailureFormat, NamespaceID: command.Payload.NamespaceID, Code: code, Dirty: false, SettledMS: strconv.FormatInt(now, 10)})
	if err != nil {
		return err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='reverted',receipt_json=?,updated_ms=? WHERE effect_id=? AND operation_id=? AND installation_id=? AND kind='snapshot' AND state='unknown' AND receipt_json='{}' AND payload_sha256=?`,
		string(encoded), now, command.EffectID, command.OperationID, command.InstallationID, command.PayloadSHA256)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%w: checkpoint refusal command", ErrFenceReconciliation)
	}
	changed, err = tx.ExecContext(ctx, `UPDATE runner_checkpoints SET state='deleted',revision=revision+1,updated_ms=?
		WHERE id=? AND data_namespace_id=? AND installation_id=? AND operation_id=?
		AND state='preparing' AND revision=1 AND manifest_sha256 IS NULL AND bytes=0 AND verified_ms IS NULL`,
		now, command.Payload.CheckpointID, command.Payload.NamespaceID, command.InstallationID, command.OperationID)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%w: checkpoint refusal record", ErrFenceReconciliation)
	}
	return nil
}

// refuseCheckpoint records a refusal the native call just returned. The caller passes the context of the dispatch;
// the record is written on one detached from it, so a dispatch that was cancelled still settles what it learned.
func (s *Store) refuseCheckpoint(parent context.Context, raw, signature []byte, command control.CheckpointCommand, code string, cause error) error {
	ctx, cancel := failureRecordContext(parent)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	authority, err := s.readAuthority(ctx, tx)
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	// The signed window governs dispatch; a refusal after a long walk must still be recorded.
	if _, err := control.VerifyCheckpointSettlement(raw, signature, authority, now); err != nil {
		return err
	}
	if err := refuseCheckpointTx(ctx, tx, command, code, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// supersedeUnknownSnapshots settles the earlier snapshots of the installation that were claimed and never settled, when
// a new checkpoint arrives for it: without that, the pending-command check of the new one refuses it for ever, and
// the checkpoint a repair needs could never run. It runs in the transaction of the new claim and under the data
// execution gate every walk holds, so it cannot race one that is still running. A checkpoint only reads the data, so
// abandoning an unfinished one loses nothing. Only a snapshot whose checkpoint row is still exactly what its claim wrote
// is settled; any other stays for review. It returns the checkpoint ids whose archives are to be removed.
func supersedeUnknownSnapshots(ctx context.Context, tx *sql.Tx, next control.CheckpointCommand, now int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.effect_id,c.operation_id,c.payload_sha256,k.id,k.data_namespace_id
		FROM runner_commands c JOIN runner_checkpoints k ON k.operation_id=c.operation_id AND k.installation_id=c.installation_id
		WHERE c.installation_id=? AND c.kind='snapshot' AND c.state='unknown' AND c.receipt_json='{}' AND c.effect_id<>?
		AND k.state='preparing' AND k.revision=1 AND k.manifest_sha256 IS NULL AND k.bytes=0 AND k.verified_ms IS NULL`,
		next.InstallationID, next.EffectID)
	if err != nil {
		return nil, err
	}
	type earlier struct{ effect, operation, payload, checkpoint, namespace string }
	var found []earlier
	for rows.Next() {
		var e earlier
		if err := rows.Scan(&e.effect, &e.operation, &e.payload, &e.checkpoint, &e.namespace); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range found {
		encoded, err := json.Marshal(dataEffectFailure{Format: dataEffectFailureFormat, NamespaceID: e.namespace, Code: dataFailureCheckpointIncomplete, Dirty: false, SettledMS: strconv.FormatInt(now, 10)})
		if err != nil {
			return nil, err
		}
		changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='reverted',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json='{}' AND payload_sha256=?`,
			string(encoded), now, e.effect, e.payload)
		if err != nil {
			return nil, err
		}
		if n, err := changed.RowsAffected(); err != nil || n != 1 {
			return nil, fmt.Errorf("%w: superseded checkpoint command", ErrFenceReconciliation)
		}
		changed, err = tx.ExecContext(ctx, `UPDATE runner_checkpoints SET state='deleted',revision=revision+1,updated_ms=? WHERE id=? AND state='preparing' AND revision=1`, now, e.checkpoint)
		if err != nil {
			return nil, err
		}
		if n, err := changed.RowsAffected(); err != nil || n != 1 {
			return nil, fmt.Errorf("%w: superseded checkpoint record", ErrFenceReconciliation)
		}
		ids = append(ids, e.checkpoint)
	}
	return ids, nil
}
