package journal

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// AdmitCandidateCommand persists authenticated candidate intent only. It creates
// no native container/resource, writer grant or execution claim. Domain-specific
// verification remains mandatory before the shared durable floor/replay checks.
func (s *Store) AdmitCandidateCommand(ctx context.Context, raw, signature []byte) (CommandAdmission, error) {
	return s.admitSignedCommand(ctx, raw, signature, func(raw, signature []byte, authority control.Authority, now int64) (verifiedAdmissionCommand, error) {
		verified, err := control.VerifyCandidate(raw, signature, authority, now)
		if err != nil {
			return verifiedAdmissionCommand{}, err
		}
		c := verified.Command()
		// Only fields consumed by journal indexing/floors are projected. The signed
		// logical digest binds the complete candidate payload, including resource,
		// platform and sandbox profile. This is never passed to prepare-data dispatch.
		identity := control.Command{Format: c.Format, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: c.ControlRevision, InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectNumber: c.EffectNumber, EffectID: c.EffectID, Kind: c.Kind, InstallationFence: c.InstallationFence, PayloadSHA256: c.PayloadSHA256, IssuedMS: c.IssuedMS, ExpiresMS: c.ExpiresMS, Payload: control.PrepareDataPayload{DataNamespaceID: c.Payload.DataNamespaceID, ReleaseSHA256: c.Payload.ReleaseSHA256, DataFormat: c.Payload.DataFormat, WriterFence: c.Payload.WriterFence}}
		return verifiedAdmissionCommand{identity: identity, containerResourceID: c.Payload.ResourceID}, nil
	})
}

func validateCandidateAdmissionResource(ctx context.Context, tx *sql.Tx, verified verifiedAdmissionCommand) (bool, error) {
	if verified.containerResourceID == "" {
		return false, nil
	}
	c := verified.identity
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	var count, conflicts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources WHERE id=?`, verified.containerResourceID).Scan(&count); err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources WHERE id=? AND (kind<>'container' OR installation_id<>? OR controller_epoch<>? OR data_namespace_id IS NULL OR data_namespace_id<>? OR release_sha256 IS NULL OR release_sha256<>? OR generation<>?)`, verified.containerResourceID, c.InstallationID, c.ControllerEpoch, c.Payload.DataNamespaceID, c.Payload.ReleaseSHA256, generation).Scan(&conflicts); err != nil {
		return false, err
	}
	if conflicts != 0 {
		return false, ErrCommandConflict
	}
	return true, nil
}
