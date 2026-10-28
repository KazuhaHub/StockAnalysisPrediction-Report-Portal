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

func writableRegistrationIndex(v control.VerifiedWritableBackendRegistrationCommand) control.Command {
	index := candidateIndex(v.Candidate().Index())
	c := v.Command()
	index.Format, index.Stage, index.Kind, index.EffectID, index.EffectNumber, index.PayloadSHA256 = control.WritableBackendRegistrationFormat, "activate", "activate", c.EffectID, "3", c.PayloadSHA256
	return index
}

// This scope excludes only a separately authenticated prepared registration row
// which matches every logical index field. Normal start/readiness readers keep
// their original three-command history and cannot ignore later registration.
func checkWritableCandidateHistoryForRegistration(ctx context.Context, tx *sql.Tx, v control.VerifiedWritableCandidateCommand, allowedResource string, registration *control.VerifiedWritableBackendRegistrationCommand, now int64) error {
	if registration == nil {
		return checkWritableCandidateHistory(ctx, tx, v, allowedResource)
	}
	if registration.Candidate().Index() != v.Index() {
		return ErrFenceReconciliation
	}
	index := writableRegistrationIndex(*registration)
	own, err := loadAdmittedCommand(ctx, tx, index.EffectID)
	if err == nil {
		fence, _ := strconv.ParseInt(index.InstallationFence, 10, 64)
		if !sameLogicalCommand(own, index) || own.fence != fence || own.state != "prepared" {
			return ErrFenceReconciliation
		}
		var receipt string
		var ack sql.NullInt64
		var created, updated int64
		if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms,created_ms,updated_ms FROM runner_commands WHERE effect_id=?`, index.EffectID).Scan(&receipt, &ack, &created, &updated); err != nil {
			return err
		}
		if receipt != "{}" || ack.Valid || created < 0 || updated != created || updated > now {
			return ErrFenceReconciliation
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	c, w, ref := v.Command(), v.Writer(), v.Writer().Data()
	if allowedResource != "" && allowedResource != w.Command().ResourceID {
		return ErrFenceReconciliation
	}
	var conflicts int
	if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND effect_id<>? AND effect_id<>? AND effect_id<>? AND effect_id<>?)+
 (SELECT COUNT(*) FROM runner_resources WHERE (id=? AND id<>?) OR (data_namespace_id=? AND id<>? AND id<>?))`, ref.InstallationID, ref.EffectID, w.Command().EffectID, c.EffectID, index.EffectID, w.Command().ResourceID, allowedResource, ref.Payload.DataNamespaceID, ref.Payload.DataNamespaceID, allowedResource).Scan(&conflicts); err != nil {
		return err
	}
	if conflicts != 0 {
		return ErrFenceReconciliation
	}
	return nil
}
func loadWritableRegistrationSource(ctx context.Context, tx *sql.Tx, v control.VerifiedWritableBackendRegistrationCommand, a control.Authority, now int64, started time.Time) (writableCandidateFacts, WritableCandidateRunningReceipt, error) {
	var zero writableCandidateFacts
	c := v.Command()
	f, err := loadWritableCandidateFacts(ctx, tx, v.Candidate(), a, c.CandidateRaw, c.CandidateSignature, "applied", nil, false, now, started, &v)
	if err != nil {
		return zero, WritableCandidateRunningReceipt{}, err
	}
	running, err := ParseWritableCandidateRunningReceipt([]byte(f.receipt))
	if err != nil || candidateHash([]byte(f.receipt)) != c.RunningReceiptSHA256 {
		return zero, WritableCandidateRunningReceipt{}, ErrFenceReconciliation
	}
	var created int64
	err = tx.QueryRowContext(ctx, `SELECT created_ms FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&created)
	settled, _ := strconv.ParseInt(running.SettledMS, 10, 64)
	if err == nil && created < settled {
		return zero, WritableCandidateRunningReceipt{}, ErrFenceReconciliation
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return zero, WritableCandidateRunningReceipt{}, err
	}
	return f, running, nil
}

// Admission stores digest-only intent. It neither handles private document bytes
// nor writes a file, creates an executable claim, grants viewer access or pairs.
func (s *Store) AdmitWritableBackendRegistrationCommand(ctx context.Context, raw, signature []byte) (CommandAdmission, error) {
	return s.admitSignedCommand(ctx, raw, signature, func(raw, signature []byte, a control.Authority, now int64) (verifiedAdmissionCommand, error) {
		v, err := control.VerifyWritableBackendRegistration(raw, signature, a, now)
		if err != nil {
			return verifiedAdmissionCommand{}, err
		}
		return verifiedAdmissionCommand{identity: writableRegistrationIndex(v), containerResourceID: v.Candidate().Index().Payload.ResourceID, writableRegistration: &v}, nil
	})
}

// Public metadata only. This is prepared intent, not private file delivery,
// actual readiness, a registered connection or a valid application handshake.
type WritableBackendRegistrationIntent struct {
	EffectID, PayloadSHA256, RunningReceiptSHA256, DocumentSHA256              string
	CandidateEffectID, NativeID, ResourceID, InstallationID, OperationID       string
	DataNamespaceID, ReleaseSHA256, Generation, WriterFence, InstallationFence string
}

func (s *Store) ReadWritableBackendRegistrationIntent(ctx context.Context, raw, signature []byte) (WritableBackendRegistrationIntent, error) {
	var zero WritableBackendRegistrationIntent
	if s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return zero, ErrFenceReconciliation
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
	d, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	a := control.Authority{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, PublicKey: d.PublicKey, Fingerprint: d.Fingerprint}
	started := time.Now()
	now := started.UnixMilli()
	v, err := control.VerifyWritableBackendRegistration(raw, signature, a, now)
	if err != nil {
		return zero, err
	}
	f, running, err := loadWritableRegistrationSource(ctx, tx, v, a, now, started)
	if err != nil {
		return zero, err
	}
	index := writableRegistrationIndex(v)
	stored, err := loadAdmittedCommand(ctx, tx, index.EffectID)
	if err != nil {
		return zero, err
	}
	floor, err := commandResourceFloor(ctx, tx, index)
	fence, _ := strconv.ParseInt(index.InstallationFence, 10, 64)
	if err != nil || stored.state != "prepared" || stored.fence != fence || floor != fence || !sameLogicalCommand(stored, index) {
		return zero, ErrFenceReconciliation
	}
	var created int64
	if err := tx.QueryRowContext(ctx, `SELECT created_ms FROM runner_commands WHERE effect_id=?`, index.EffectID).Scan(&created); err != nil {
		return zero, err
	}
	settled, _ := strconv.ParseInt(running.SettledMS, 10, 64)
	if created < settled {
		return zero, ErrFenceReconciliation
	}
	check := func() error {
		if err := finalWritableCandidateCheck(ctx, f); err != nil {
			return err
		}
		if s.closing.Load() {
			return ErrFenceReconciliation
		}
		_, err := control.VerifyWritableBackendRegistration(raw, signature, a, time.Now().UnixMilli())
		return err
	}
	if err := check(); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := check(); err != nil {
		return zero, err
	}
	c, ref := v.Command(), v.Candidate().Index()
	return WritableBackendRegistrationIntent{EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, RunningReceiptSHA256: c.RunningReceiptSHA256, DocumentSHA256: c.DocumentSHA256, CandidateEffectID: ref.EffectID, NativeID: running.NativeID, ResourceID: ref.Payload.ResourceID, InstallationID: ref.InstallationID, OperationID: ref.OperationID, DataNamespaceID: ref.Payload.DataNamespaceID, ReleaseSHA256: ref.Payload.ReleaseSHA256, Generation: ref.TargetGeneration, WriterFence: ref.Payload.WriterFence, InstallationFence: ref.InstallationFence}, nil
}
