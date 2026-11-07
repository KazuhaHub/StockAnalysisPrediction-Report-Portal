package journal

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const updateStartAbsenceFormat = "portal.runner.update-start-absence/1-dev.1"

// UpdateStartAbsence is the runner's reply to a restore-start or update-start read-receipt for an effect it never
// admitted. It binds the effect identity of the signed command it answers.
type UpdateStartAbsence struct {
	Format           string `json:"format"`
	DeploymentID     string `json:"deployment_id"`
	ControllerEpoch  string `json:"controller_epoch"`
	InstallationID   string `json:"installation_id"`
	OperationID      string `json:"operation_id"`
	TargetGeneration string `json:"target_generation"`
	Stage            string `json:"stage"`
	EffectNumber     string `json:"effect_number"`
	EffectID         string `json:"effect_id"`
	PayloadSHA256    string `json:"payload_sha256"`
	State            string `json:"state"`
	ObservedMS       string `json:"observed_ms"`
}

func updateStartAbsenceFor(c control.UpdateStartCommand, now int64) UpdateStartAbsence {
	return UpdateStartAbsence{
		Format: updateStartAbsenceFormat, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration,
		Stage: c.Stage, EffectNumber: c.EffectNumber, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256,
		State: "absent", ObservedMS: strconv.FormatInt(now, 10),
	}
}

func validUpdateStartAbsence(a UpdateStartAbsence) bool {
	if a.Format != updateStartAbsenceFormat || a.Stage != "start" || a.State != "absent" || !dataSHA(a.PayloadSHA256) ||
		!dataNumber(a.TargetGeneration, true) || !dataNumber(a.EffectNumber, true) || !dataNumber(a.ObservedMS, true) {
		return false
	}
	for _, id := range []string{a.DeploymentID, a.ControllerEpoch, a.InstallationID, a.OperationID, a.EffectID} {
		if !importID(id) {
			return false
		}
	}
	return true
}

// ParseUpdateStartAbsence accepts only the canonical encoding of a valid absence reply.
func ParseUpdateStartAbsence(raw []byte) (UpdateStartAbsence, error) {
	var absence UpdateStartAbsence
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &absence) || !validUpdateStartAbsence(absence) {
		return UpdateStartAbsence{}, ErrFenceReconciliation
	}
	return absence, nil
}

// UpdateStartAbsenceMatchesCommand reports whether the reply answers exactly this signed restore-start.
func UpdateStartAbsenceMatchesCommand(a UpdateStartAbsence, c control.UpdateStartCommand) bool {
	return (c.Kind == "restore-start" || c.Kind == "update-start") && a.DeploymentID == c.DeploymentID && a.ControllerEpoch == c.ControllerEpoch &&
		a.InstallationID == c.InstallationID && a.OperationID == c.OperationID &&
		a.TargetGeneration == c.TargetGeneration && a.Stage == c.Stage && a.EffectNumber == c.EffectNumber &&
		a.EffectID == c.EffectID && a.PayloadSHA256 == c.PayloadSHA256
}

// ReadUpdateStartRunningOrAbsence is the runner's read-receipt answer for a start effect: the applied
// running receipt, or, for a restore-start or an update-start, an UpdateStartAbsence when the journal holds no command
// row for the effect identity. Every other outcome is the ordinary error of the running-receipt read.
// The command is verified under the read purpose: a signature made for an execute is not a read.
func (s *Store) ReadUpdateStartRunningOrAbsence(ctx context.Context, raw, signature []byte) (any, error) {
	receipt, err := s.readUpdateStartRunningReceipt(ctx, raw, signature, control.VerifyUpdateStartRead)
	if err == nil {
		return receipt, nil
	}
	absence, absenceErr := s.readRestoreStartAbsence(ctx, raw, signature, control.VerifyUpdateStartRead)
	if absenceErr == nil && absence != nil {
		return *absence, nil
	}
	return nil, err
}

// readRestoreStartAbsence finds a restore-start effect absent. A claim does not hold the data execution
// gate the way an import does, but a read that finds no row still takes the gate and looks again, so it
// cannot overtake work that holds it. It answers nil, nil when the effect is not provably absent.
func (s *Store) readRestoreStartAbsence(ctx context.Context, raw, signature []byte, verify updateStartVerifier) (*UpdateStartAbsence, error) {
	absence, err := s.restoreStartAbsent(ctx, raw, signature, verify)
	if err != nil || absence == nil {
		return nil, err
	}
	if err := s.acquireDataExecution(ctx); err != nil {
		return nil, err
	}
	defer s.releaseDataExecution()
	return s.restoreStartAbsent(ctx, raw, signature, verify)
}

func (s *Store) restoreStartAbsent(ctx context.Context, raw, signature []byte, verify updateStartVerifier) (*UpdateStartAbsence, error) {
	if s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return nil, ErrUpdateStartUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return nil, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return nil, err
	}
	authority := control.Authority{
		DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey,
		Fingerprint: deployment.Fingerprint,
	}
	now := time.Now().UTC().UnixMilli()
	verified, err := verify(raw, signature, authority, now)
	if err != nil {
		return nil, err
	}
	c := verified.Command()
	if c.Kind != "restore-start" && c.Kind != "update-start" {
		return nil, nil
	}
	if _, err := loadAdmittedCommand(ctx, tx, c.EffectID); err == nil {
		return nil, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// Absent means no row under the effect id and none holding the same operation, stage and effect
	// number under another id; a sibling row is a conflict, not an absence.
	var siblings int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=? OR (operation_id=? AND stage=? AND effect_number=?)`,
		c.EffectID, c.OperationID, c.Stage, mustDataInt(c.EffectNumber)).Scan(&siblings); err != nil {
		return nil, err
	}
	if siblings != 0 {
		return nil, ErrCommandConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	absence := updateStartAbsenceFor(c, now)
	return &absence, nil
}
