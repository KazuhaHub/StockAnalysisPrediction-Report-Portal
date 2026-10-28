package journal

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func dataWriterIndex(v control.VerifiedDataWriterCommand) control.Command {
	index, c := v.Data(), v.Command()
	index.Format, index.Stage, index.Kind = control.DataWriterFormat, "prepare_data", "activate"
	index.EffectNumber, index.EffectID, index.PayloadSHA256 = "2", c.EffectID, c.PayloadSHA256
	return index
}

// AdmitDataWriterCommand persists a separately authenticated initial-writer
// intent in the approved journal. It does not change data permissions, reserve a
// native container, stop a process or grant a writable launch/capability.
func (s *Store) AdmitDataWriterCommand(ctx context.Context, raw, signature []byte) (CommandAdmission, error) {
	return s.admitSignedCommand(ctx, raw, signature, func(raw, signature []byte, a control.Authority, now int64) (verifiedAdmissionCommand, error) {
		v, err := control.VerifyDataWriter(raw, signature, a, now)
		if err != nil {
			return verifiedAdmissionCommand{}, err
		}
		return verifiedAdmissionCommand{identity: dataWriterIndex(v), dataWriter: &v}, nil
	})
}

// This initial pre-start contract requires exactly the applied data source and
// this writer effect, with no other installation command/resource history. It
// checks journal facts only; actual Docker/process exclusion is still mandatory
// at a later physical adapter. Do not use it as a historical active-writer read.
func validateDataWriterSource(ctx context.Context, tx *sql.Tx, v control.VerifiedDataWriterCommand, now int64) (DataPreparationReceipt, error) {
	var zero DataPreparationReceipt
	c, ref := v.Command(), v.Data()
	stored, err := loadAdmittedCommand(ctx, tx, ref.EffectID)
	if err != nil {
		return zero, err
	}
	fence, _ := strconv.ParseInt(ref.InstallationFence, 10, 64)
	if stored.state != "applied" || stored.fence != fence || !sameLogicalCommand(stored, ref) {
		return zero, ErrFenceReconciliation
	}
	var raw string
	var ack sql.NullInt64
	var created, updated int64
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms,created_ms,updated_ms FROM runner_commands WHERE effect_id=?`, ref.EffectID).Scan(&raw, &ack, &created, &updated); err != nil {
		return zero, err
	}
	proof, err := ParseDataPreparationReceipt([]byte(raw))
	if err != nil || ack.Valid || candidateHash([]byte(raw)) != c.DataReceiptSHA256 {
		return zero, ErrFenceReconciliation
	}
	// A current key/revision signs the renewable reference; original physical
	// ownership is checked with the revision recorded in the immutable receipt.
	original := ref
	original.ControlRevision = proof.ControlRevision
	originalRevision, _ := strconv.ParseInt(proof.ControlRevision, 10, 64)
	currentRevision, _ := strconv.ParseInt(ref.ControlRevision, 10, 64)
	if originalRevision > currentRevision || !proof.MatchesCommand(original) {
		return zero, ErrFenceReconciliation
	}
	resource, err := existingDataResource(ctx, tx, original)
	if err != nil {
		return zero, err
	}
	reservation, err := dataResourceIdentity(dataCommandFacts{command: original, stored: stored, now: now}, resource)
	if err != nil {
		return zero, err
	}
	observed, _ := strconv.ParseInt(proof.ObservedMS, 10, 64)
	if resource.observed != "present" || resource.revision != 2 || !resource.observedMS.Valid || resource.observedMS.Int64 != observed || resource.updated != observed || created < 0 || created > observed || updated != observed || observed > now || proof.ProjectID != strconv.FormatUint(uint64(reservation.ProjectID), 10) {
		return zero, ErrFenceReconciliation
	}
	if err := requireDataRootHistory(ctx, tx, markerRoot(proof.Claim)); err != nil {
		return zero, err
	}
	var conflicts int
	if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND effect_id<>? AND effect_id<>?)+
 (SELECT COUNT(*) FROM runner_resources WHERE id=? OR (data_namespace_id=? AND id<>?))`, ref.InstallationID, ref.EffectID, c.EffectID, c.ResourceID, ref.Payload.DataNamespaceID, ref.Payload.DataNamespaceID).Scan(&conflicts); err != nil {
		return zero, err
	}
	if conflicts != 0 {
		return zero, ErrFenceReconciliation
	}
	return proof, nil
}
