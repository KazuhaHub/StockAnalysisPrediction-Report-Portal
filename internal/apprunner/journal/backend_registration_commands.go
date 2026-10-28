package journal

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// AdmitBackendRegistrationCommand persists authenticated registration intent
// only. The private document is never passed to admission or stored in this row.
// No dispatcher/file writer/registration receipt is supplied by this method.
func (s *Store) AdmitBackendRegistrationCommand(ctx context.Context, raw, signature []byte) (CommandAdmission, error) {
	return s.admitSignedCommand(ctx, raw, signature, func(raw, signature []byte, a control.Authority, now int64) (verifiedAdmissionCommand, error) {
		verified, err := control.VerifyBackendRegistration(raw, signature, a, now)
		if err != nil {
			return verifiedAdmissionCommand{}, err
		}
		ref := verified.Candidate()
		index := registrationIndex(verified)
		return verifiedAdmissionCommand{identity: index, containerResourceID: ref.Payload.ResourceID, registrationReference: ref}, nil
	})
}

func validateBackendRegistrationSource(ctx context.Context, tx *sql.Tx, ref control.CandidateCommand) error {
	if ref.Format != control.CandidateFormat {
		return ErrCommandConflict
	}
	stored, err := loadAdmittedCommand(ctx, tx, ref.EffectID)
	if err != nil {
		return err
	}
	fence, _ := strconv.ParseInt(ref.InstallationFence, 10, 64)
	if stored.state != "applied" || stored.fence != fence || !sameLogicalCommand(stored, candidateIndex(ref)) {
		return ErrCommandConflict
	}
	var raw string
	var ack sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms FROM runner_commands WHERE effect_id=?`, ref.EffectID).Scan(&raw, &ack); err != nil {
		return err
	}
	r, err := ParseCandidateRunningReceipt([]byte(raw))
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	if err != nil || ack.Valid || !r.MatchesCommand(ref) || settled > time.Now().UnixMilli() {
		return ErrCommandConflict
	}
	var matches int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources WHERE id=? AND kind='container' AND native_id=? AND observed_state='running' AND desired_state='present' AND control_fence=? AND writer_fence=? AND revision=2`, ref.Payload.ResourceID, r.NativeID, fence, ref.Payload.WriterFence).Scan(&matches); err != nil {
		return err
	}
	if matches != 1 {
		return ErrCommandConflict
	}
	return nil
}
