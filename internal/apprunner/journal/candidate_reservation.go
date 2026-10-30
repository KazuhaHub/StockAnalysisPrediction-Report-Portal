package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

var ErrCandidateClaimUnavailable = errors.New("runner candidate execution claim is unavailable")

const candidatePendingPrefix = "candidate-pending/v1/"

// CandidateReservation is durable intent, not native ownership or readiness.
type CandidateReservation struct {
	ResourceID, InstallationID, NamespaceID, EffectID, OwnershipSHA256 string
	Generation, ControlFence, WriterFence                              int64
	Replayed                                                           bool
}
type candidateFacts struct {
	command        control.CandidateCommand
	stored         admittedCommand
	authority      control.Authority
	raw, signature []byte
	now            int64
	started        time.Time
	data           DataPreparationReceipt
	dataSHA        string
	receipt        string
}

func candidateHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func finalCandidateCheck(ctx context.Context, f candidateFacts) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	if now < f.now {
		return control.ErrInvalidCommand
	}
	_, err := control.VerifyCandidate(f.raw, f.signature, f.authority, now)
	return err
}
func (s *Store) beginCandidateCommand(ctx context.Context, raw, signature []byte, state, receipt string) (*sql.Tx, candidateFacts, error) {
	return s.beginCandidateCommandReceipt(ctx, raw, signature, state, &receipt)
}

// A nil receipt allows an applied receipt read, without relaxing current signed
// authority, acknowledgement, logical identity, original fences or data history.
func (s *Store) beginCandidateCommandReceipt(ctx context.Context, raw, signature []byte, state string, receipt *string) (*sql.Tx, candidateFacts, error) {
	var zero candidateFacts
	if s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return nil, zero, ErrCandidateClaimUnavailable
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, zero, err
	}
	ok := false
	defer func() {
		if !ok {
			tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return nil, zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return nil, zero, err
	}
	authority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
	started := time.Now()
	now := started.UTC().UnixMilli()
	verified, err := control.VerifyCandidate(raw, signature, authority, now)
	if err != nil {
		return nil, zero, err
	}
	c := verified.Command()
	index := candidateIndex(c)
	stored, err := loadAdmittedCommand(ctx, tx, c.EffectID)
	if err != nil {
		return nil, zero, err
	}
	if !sameLogicalCommand(stored, index) {
		return nil, zero, ErrCandidateClaimUnavailable
	}
	if stored.state != state {
		return nil, zero, ErrFenceReconciliation
	}
	fence, _ := strconv.ParseInt(c.InstallationFence, 10, 64)
	floor, err := commandResourceFloor(ctx, tx, index)
	if err != nil {
		return nil, zero, err
	}
	if fence != stored.fence || floor != stored.fence {
		return nil, zero, ErrFenceReconciliation
	}
	var current string
	var ack sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&current, &ack); err != nil {
		return nil, zero, err
	}
	if (receipt != nil && current != *receipt) || ack.Valid {
		return nil, zero, ErrFenceReconciliation
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND effect_id<>? AND state IN ('prepared','unknown')`, c.InstallationID, c.EffectID).Scan(&pending); err != nil {
		return nil, zero, err
	}
	if pending != 0 {
		return nil, zero, ErrFenceReconciliation
	}
	proof, proofSHA, err := candidateDataEvidence(ctx, tx, c, now)
	if err != nil {
		return nil, zero, err
	}
	ok = true
	return tx, candidateFacts{command: c, stored: stored, authority: authority, raw: raw, signature: signature, now: now, started: started, data: proof, dataSHA: proofSHA, receipt: current}, nil
}

func candidateDataEvidence(ctx context.Context, tx *sql.Tx, c control.CandidateCommand, now int64) (DataPreparationReceipt, string, error) {
	var zero DataPreparationReceipt
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	rows, err := tx.QueryContext(ctx, `SELECT effect_id,effect_number,payload_sha256,admitted_fence,receipt_json FROM runner_commands WHERE installation_id=? AND operation_id=? AND stage='prepare_data' AND kind='prepare-data' AND state='applied' AND target_generation=? AND controller_epoch=? LIMIT 2`, c.InstallationID, c.OperationID, generation, c.ControllerEpoch)
	if err != nil {
		return zero, "", err
	}
	var effect, payload, receiptRaw string
	var slot, fence int64
	count := 0
	for rows.Next() {
		count++
		if count != 1 {
			rows.Close()
			return zero, "", ErrFenceReconciliation
		}
		if err := rows.Scan(&effect, &slot, &payload, &fence, &receiptRaw); err != nil {
			rows.Close()
			return zero, "", err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, "", err
	}
	if count != 1 {
		return zero, "", ErrFenceReconciliation
	}
	proof, err := ParseDataPreparationReceipt([]byte(receiptRaw))
	if err != nil {
		return zero, "", err
	}
	old := control.Command{Format: control.Format, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: proof.ControlRevision, InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: "prepare_data", EffectNumber: strconv.FormatInt(slot, 10), EffectID: effect, Kind: "prepare-data", InstallationFence: strconv.FormatInt(fence, 10), PayloadSHA256: payload, Payload: control.PrepareDataPayload{DataNamespaceID: c.Payload.DataNamespaceID, ReleaseSHA256: c.Payload.ReleaseSHA256, DataFormat: c.Payload.DataFormat, QuotaBytes: proof.QuotaBytes, QuotaInodes: proof.QuotaInodes, WriterFence: c.Payload.WriterFence}}
	if !proof.MatchesCommand(old) {
		return zero, "", ErrFenceReconciliation
	}
	resource, err := existingDataResource(ctx, tx, old)
	if err != nil {
		return zero, "", err
	}
	reservation, err := dataResourceIdentity(dataCommandFacts{command: old, stored: admittedCommand{fence: fence}, now: now}, resource)
	if err != nil {
		return zero, "", err
	}
	observed, _ := strconv.ParseInt(proof.ObservedMS, 10, 64)
	if resource.observed != "present" || resource.revision != 2 || !resource.observedMS.Valid || resource.observedMS.Int64 != observed || resource.updated != observed || observed > now || proof.ProjectID != strconv.FormatUint(uint64(reservation.ProjectID), 10) {
		return zero, "", ErrFenceReconciliation
	}
	if err := requireDataRootHistory(ctx, tx, markerRoot(proof.Claim)); err != nil {
		return zero, "", err
	}
	return proof, candidateHash([]byte(receiptRaw)), nil
}
func candidateOwnership(f candidateFacts) string {
	c := f.command
	claim := struct{ Format, Deployment, Epoch, Revision, Effect, Resource, Namespace, Release, Generation, Fence, Writer, Payload, DataReceipt string }{"portal.runner.candidate-ownership/1-dev.1", c.DeploymentID, c.ControllerEpoch, c.ControlRevision, c.EffectID, c.Payload.ResourceID, c.Payload.DataNamespaceID, c.Payload.ReleaseSHA256, c.TargetGeneration, strconv.FormatInt(f.stored.fence, 10), c.Payload.WriterFence, c.PayloadSHA256, f.dataSHA}
	raw, _ := json.Marshal(claim)
	return candidateHash(raw)
}
func existingCandidateResource(ctx context.Context, tx *sql.Tx, c control.CandidateCommand) (*dataResource, error) {
	return existingCandidateResourcePrefix(ctx, tx, c, candidatePendingPrefix)
}
func existingCandidateResourcePrefix(ctx context.Context, tx *sql.Tx, c control.CandidateCommand, prefix string) (*dataResource, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms FROM runner_resources WHERE id=? OR (kind='container' AND native_id=?) LIMIT 2`, c.Payload.ResourceID, prefix+c.Payload.ResourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result *dataResource
	for rows.Next() {
		if result != nil {
			return nil, ErrFenceReconciliation
		}
		r := new(dataResource)
		if err := rows.Scan(&r.id, &r.installation, &r.namespace, &r.kind, &r.native, &r.release, &r.generation, &r.epoch, &r.fence, &r.writer, &r.desired, &r.observed, &r.ownership, &r.revision, &r.observedMS, &r.created, &r.updated); err != nil {
			return nil, err
		}
		result = r
	}
	return result, rows.Err()
}
func reservedCandidate(f candidateFacts, r *dataResource) (CandidateReservation, error) {
	var zero CandidateReservation
	c := f.command
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
	if r == nil || r.id != c.Payload.ResourceID || r.installation != c.InstallationID || !r.namespace.Valid || r.namespace.String != c.Payload.DataNamespaceID || r.kind != "container" || r.native != candidatePendingPrefix+c.Payload.ResourceID || !r.release.Valid || r.release.String != c.Payload.ReleaseSHA256 || r.generation != generation || r.epoch != c.ControllerEpoch || r.fence != f.stored.fence || r.writer != writer || r.desired != "present" || r.observed != "unknown" || r.ownership != candidateOwnership(f) || r.revision != 1 || r.observedMS.Valid || r.created < 0 || r.updated != r.created || r.updated > f.now {
		return zero, ErrCandidateClaimUnavailable
	}
	return CandidateReservation{ResourceID: r.id, InstallationID: r.installation, NamespaceID: r.namespace.String, EffectID: c.EffectID, OwnershipSHA256: r.ownership, Generation: r.generation, ControlFence: r.fence, WriterFence: r.writer, Replayed: true}, nil
}

// ReserveCandidate reserves a fresh global resource identity after matching the
// prior applied data receipt. No Docker resource or writer access is created.
func (s *Store) ReserveCandidate(ctx context.Context, raw, signature []byte) (CandidateReservation, error) {
	var zero CandidateReservation
	tx, f, err := s.beginCandidateCommand(ctx, raw, signature, "prepared", "{}")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	c := f.command
	r, err := existingCandidateResource(ctx, tx, c)
	if err != nil {
		return zero, err
	}
	var result CandidateReservation
	if r != nil {
		result, err = reservedCandidate(f, r)
		if err != nil {
			return zero, err
		}
	} else {
		generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
		writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
		result = CandidateReservation{ResourceID: c.Payload.ResourceID, InstallationID: c.InstallationID, NamespaceID: c.Payload.DataNamespaceID, EffectID: c.EffectID, OwnershipSHA256: candidateOwnership(f), Generation: generation, ControlFence: f.stored.fence, WriterFence: writer}
		_, err = tx.ExecContext(ctx, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'container',?,?,?,?,?,?,'present','unknown',?,1,NULL,?,?)`, result.ResourceID, c.InstallationID, result.NamespaceID, candidatePendingPrefix+result.ResourceID, c.Payload.ReleaseSHA256, generation, c.ControllerEpoch, f.stored.fence, writer, result.OwnershipSHA256, f.now, f.now)
		if err != nil {
			return zero, err
		}
	}
	if err := finalCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
