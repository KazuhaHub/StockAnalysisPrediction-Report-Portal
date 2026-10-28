package journal

import (
	"bytes"
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

type writableCandidateSource struct {
	data   DataPreparationReceipt
	writer DataWriterReceipt
}

// The initial intent admits only the exact completed preparation + writer +
// this start command, with the future container identity still unreserved.
// Later reservation/execution must define their own exact extended history;
// this does not relax the old sealed candidate or pre-start writer readers.
func loadWritableCandidateSource(ctx context.Context, tx *sql.Tx, v control.VerifiedWritableCandidateCommand, now int64) (writableCandidateSource, error) {
	var zero writableCandidateSource
	c, w, ref := v.Command(), v.Writer(), v.Writer().Data()
	index := dataWriterIndex(w)
	stored, err := loadAdmittedCommand(ctx, tx, index.EffectID)
	if err != nil {
		return zero, err
	}
	fence, _ := strconv.ParseInt(ref.InstallationFence, 10, 64)
	if stored.state != "applied" || stored.fence != fence || !sameLogicalCommand(stored, index) {
		return zero, ErrFenceReconciliation
	}
	var raw string
	var ack sql.NullInt64
	var created, updated int64
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms,created_ms,updated_ms FROM runner_commands WHERE effect_id=?`, index.EffectID).Scan(&raw, &ack, &created, &updated); err != nil {
		return zero, err
	}
	receipt, err := ParseDataWriterReceipt([]byte(raw))
	if err != nil || ack.Valid || candidateHash([]byte(raw)) != c.WriterReceiptSHA256 {
		return zero, ErrFenceReconciliation
	}
	settled, _ := strconv.ParseInt(receipt.SettledMS, 10, 64)
	claimed, _ := strconv.ParseInt(receipt.Claim.ClaimedMS, 10, 64)
	if created < 0 || created > claimed || updated != settled || settled > now {
		return zero, ErrFenceReconciliation
	}
	source, err := loadDataWriterSourceCore(ctx, tx, w, now, &receipt)
	if err != nil {
		return zero, err
	}
	if !dataWriterReceiptMatches(receipt, dataWriterFacts{verified: w, source: source, now: now, fence: fence}) {
		return zero, ErrFenceReconciliation
	}
	var conflicts int
	if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND effect_id<>? AND effect_id<>? AND effect_id<>?)+
 (SELECT COUNT(*) FROM runner_resources WHERE id=? OR (data_namespace_id=? AND id<>?))`, ref.InstallationID, ref.EffectID, index.EffectID, c.EffectID, w.Command().ResourceID, ref.Payload.DataNamespaceID, ref.Payload.DataNamespaceID).Scan(&conflicts); err != nil {
		return zero, err
	}
	if conflicts != 0 {
		return zero, ErrFenceReconciliation
	}
	return writableCandidateSource{data: source, writer: receipt}, nil
}

// AdmitWritableCandidateCommand persists separately authenticated start intent.
// It creates no reservation, live handle, container, permissions or readiness.
func (s *Store) AdmitWritableCandidateCommand(ctx context.Context, raw, signature []byte) (CommandAdmission, error) {
	return s.admitSignedCommand(ctx, raw, signature, func(raw, signature []byte, a control.Authority, now int64) (verifiedAdmissionCommand, error) {
		v, err := control.VerifyWritableCandidate(raw, signature, a, now)
		if err != nil {
			return verifiedAdmissionCommand{}, err
		}
		c := v.Index()
		return verifiedAdmissionCommand{identity: candidateIndex(c), containerResourceID: c.Payload.ResourceID, writableCandidate: &v}, nil
	})
}

// WritableCandidateIntent is a current, path-free metadata view of prepared
// start intent. It is not an executable handle or native process observation.
type WritableCandidateIntent struct {
	EffectID, WriterEffectID, WriterReceiptSHA256, ResourceID             string
	InstallationID, OperationID, DataNamespaceID, ReleaseSHA256, Platform string
	ControlRevision, InstallationFence, Generation, WriterFence           string
}

func (s *Store) ReadWritableCandidateIntent(ctx context.Context, raw, signature []byte) (WritableCandidateIntent, error) {
	var zero WritableCandidateIntent
	if s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return zero, ErrCandidateClaimUnavailable
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
	now := time.Now().UnixMilli()
	v, err := control.VerifyWritableCandidate(raw, signature, a, now)
	if err != nil {
		return zero, err
	}
	c := v.Index()
	stored, err := loadAdmittedCommand(ctx, tx, c.EffectID)
	if err != nil {
		return zero, err
	}
	fence, _ := strconv.ParseInt(c.InstallationFence, 10, 64)
	floor, err := commandResourceFloor(ctx, tx, candidateIndex(c))
	if err != nil || stored.state != "prepared" || stored.fence != fence || floor != fence || !sameLogicalCommand(stored, candidateIndex(c)) {
		return zero, ErrFenceReconciliation
	}
	var receipt string
	var ack sql.NullInt64
	var created, updated int64
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms,created_ms,updated_ms FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&receipt, &ack, &created, &updated); err != nil {
		return zero, err
	}
	if ack.Valid || receipt != "{}" || created < 0 || updated != created || updated > now {
		return zero, ErrFenceReconciliation
	}
	source, err := loadWritableCandidateSource(ctx, tx, v, now)
	if err != nil {
		return zero, err
	}
	final := time.Now().UnixMilli()
	if final < now || s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if _, err := control.VerifyWritableCandidate(raw, signature, a, final); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrCandidateClaimUnavailable
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	after := time.Now().UnixMilli()
	if after < final {
		return zero, ErrFenceReconciliation
	}
	if _, err := control.VerifyWritableCandidate(raw, signature, a, after); err != nil {
		return zero, err
	}
	return WritableCandidateIntent{EffectID: c.EffectID, WriterEffectID: source.writer.Claim.EffectID, WriterReceiptSHA256: v.Command().WriterReceiptSHA256, ResourceID: c.Payload.ResourceID, InstallationID: c.InstallationID, OperationID: c.OperationID, DataNamespaceID: source.data.Claim.ResourceID, ReleaseSHA256: source.data.ReleaseSHA256, Platform: c.Payload.Platform, ControlRevision: c.ControlRevision, InstallationFence: c.InstallationFence, Generation: c.TargetGeneration, WriterFence: c.Payload.WriterFence}, nil
}
