package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

var ErrUpdateStartUnavailable = errors.New("managed update start is unavailable")

const updateStartPendingPrefix = "update-start-pending/v1/"

type UpdateStartReservation struct {
	ResourceID          string `json:"resource_id"`
	InstallationID      string `json:"installation_id"`
	NamespaceID         string `json:"namespace_id"`
	EffectID            string `json:"effect_id"`
	OwnershipSHA256     string `json:"ownership_sha256"`
	SourceGeneration    int64  `json:"source_generation"`
	TargetGeneration    int64  `json:"target_generation"`
	SourceReleaseSHA256 string `json:"source_release_sha256"`
	TargetReleaseSHA256 string `json:"target_release_sha256"`
	ControlFence        int64  `json:"control_fence"`
	SourceWriterFence   int64  `json:"source_writer_fence"`
	TargetWriterFence   int64  `json:"target_writer_fence"`
	Replayed            bool   `json:"replayed"`
}

func ParseUpdateStartReservation(raw []byte) (UpdateStartReservation, error) {
	var reservation UpdateStartReservation
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &reservation) {
		return UpdateStartReservation{}, ErrFenceReconciliation
	}
	for _, id := range []string{reservation.ResourceID, reservation.InstallationID,
		reservation.NamespaceID, reservation.EffectID} {
		if !enrollmentUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return UpdateStartReservation{}, ErrFenceReconciliation
		}
	}
	if !dataSHA(reservation.OwnershipSHA256) || !dataSHA(reservation.SourceReleaseSHA256) ||
		!dataSHA(reservation.TargetReleaseSHA256) ||
		reservation.SourceGeneration < 1 ||
		reservation.TargetGeneration != reservation.SourceGeneration+1 ||
		reservation.ControlFence < 1 || reservation.SourceWriterFence < 1 ||
		reservation.TargetWriterFence <= reservation.SourceWriterFence {
		return UpdateStartReservation{}, ErrFenceReconciliation
	}
	return reservation, nil
}

type updateStartSource struct {
	checkpoint     CheckpointReceipt
	checkpointCmd  control.CheckpointCommand
	source         checkpointSource
	resource       managedRemovalResource
	checkpointRaw  []byte
	checkpointSHA  string
	checkpointData int64
}

type updateStartFacts struct {
	command        control.UpdateStartCommand
	stored         admittedCommand
	authority      control.Authority
	source         updateStartSource
	receipt        string
	created        int64
	updated        int64
	raw, signature []byte
	now            int64
}

func updateStartIndex(c control.UpdateStartCommand) control.Command {
	return control.Command{
		Format: c.Format, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		ControlRevision: c.ControlRevision, InstallationID: c.InstallationID,
		OperationID: c.OperationID, TargetGeneration: c.TargetGeneration,
		Stage: c.Stage, EffectNumber: c.EffectNumber, EffectID: c.EffectID,
		Kind: c.Kind, InstallationFence: c.InstallationFence,
		PayloadSHA256: c.PayloadSHA256, IssuedMS: c.IssuedMS, ExpiresMS: c.ExpiresMS,
		Payload: control.PrepareDataPayload{
			DataNamespaceID: c.Payload.NamespaceID,
			ReleaseSHA256:   c.Payload.TargetReleaseSHA256,
			DataFormat:      c.Payload.DataFormat,
			WriterFence:     c.Payload.TargetWriterFence,
		},
	}
}

func updateStartOwnership(c control.UpdateStartCommand, fence int64) string {
	p := c.Payload
	claim := struct {
		Format, Deployment, Epoch, Installation, Operation, Resource, Namespace string
		SourceGeneration, TargetGeneration, ControlFence                        int64
		SourceRelease, TargetRelease                                            string
		SourceStartReceipt, SourceDataReceipt, CheckpointReceipt                string
		Checkpoint, Manifest                                                    string
		SourceWriterFence, TargetWriterFence, DataFormat                        int64
	}{
		"portal.runner.update-start-ownership/1-dev.1", c.DeploymentID, c.ControllerEpoch,
		c.InstallationID, c.OperationID, p.ResourceID, p.NamespaceID,
		mustDataInt(p.SourceGeneration), mustDataInt(c.TargetGeneration), fence,
		p.SourceReleaseSHA256, p.TargetReleaseSHA256, p.SourceStartReceiptSHA256,
		p.SourceDataReceiptSHA256, p.SourceCheckpointReceiptSHA256, p.CheckpointID,
		p.CheckpointManifestSHA256, mustDataInt(p.SourceWriterFence),
		mustDataInt(p.TargetWriterFence), mustDataInt(p.DataFormat),
	}
	raw, _ := json.Marshal(claim)
	return candidateHash(raw)
}

func loadUpdateStartSource(ctx context.Context, tx *sql.Tx, c control.UpdateStartCommand, now int64) (updateStartSource, error) {
	var zero updateStartSource
	p := c.Payload
	var operation, state, payload, raw, installation string
	if err := tx.QueryRowContext(ctx, `SELECT operation_id,installation_id,state,payload_sha256,receipt_json
		FROM runner_commands WHERE effect_id=? AND stage='snapshot' AND kind='snapshot'`,
		p.SourceCheckpointEffectID).Scan(&operation, &installation, &state, &payload, &raw); err != nil {
		return zero, ErrUpdateStartUnavailable
	}
	if operation != c.OperationID || operation != p.SourceCheckpointOperationID ||
		installation != c.InstallationID || state != "applied" ||
		candidateHash([]byte(raw)) != p.SourceCheckpointReceiptSHA256 {
		return zero, ErrFenceReconciliation
	}
	checkpoint, err := ParseCheckpointReceipt([]byte(raw))
	if err != nil || checkpoint.EffectID != p.SourceCheckpointEffectID ||
		checkpoint.PayloadSHA256 != payload || checkpoint.OperationID != c.OperationID ||
		checkpoint.TargetGeneration != c.TargetGeneration ||
		checkpoint.InstallationID != c.InstallationID ||
		checkpoint.CheckpointID != p.CheckpointID ||
		checkpoint.NamespaceID != p.NamespaceID ||
		checkpoint.ReleaseSHA256 != p.SourceReleaseSHA256 ||
		checkpoint.ManifestSHA256 != p.CheckpointManifestSHA256 ||
		checkpoint.ArchiveBytes != p.CheckpointArchiveBytes ||
		checkpoint.ProjectID != p.ProjectID ||
		checkpoint.NamespaceInode != p.NamespaceInode ||
		checkpoint.Device != p.Device ||
		checkpoint.DataFormat != p.DataFormat ||
		checkpoint.WriterFence != p.SourceWriterFence ||
		checkpoint.SourceOperationID != p.SourceOperationID ||
		checkpoint.SourceStartEffectID != p.SourceStartEffectID ||
		checkpoint.SourceReceiptSHA256 != p.SourceStartReceiptSHA256 ||
		checkpoint.SourceDataEffectID != p.SourceDataEffectID ||
		checkpoint.SourceDataReceiptSHA256 != p.SourceDataReceiptSHA256 {
		return zero, ErrFenceReconciliation
	}
	checkpointCommand := control.CheckpointCommand{
		Format: control.CheckpointFormat, DeploymentID: c.DeploymentID,
		ControllerEpoch: c.ControllerEpoch, ControlRevision: "1",
		InstallationID: c.InstallationID, OperationID: c.OperationID,
		TargetGeneration: c.TargetGeneration, Stage: "snapshot",
		EffectNumber: "1", EffectID: checkpoint.EffectID, Kind: "snapshot",
		InstallationFence: "1", PayloadSHA256: checkpoint.PayloadSHA256,
		Payload: control.CheckpointPayload{
			CheckpointID: checkpoint.CheckpointID, NamespaceID: checkpoint.NamespaceID,
			ResourceID: checkpoint.ResourceID, ReleaseSHA256: checkpoint.ReleaseSHA256,
			SourceOperationID:       checkpoint.SourceOperationID,
			SourceStartEffectID:     checkpoint.SourceStartEffectID,
			SourceReceiptSHA256:     checkpoint.SourceReceiptSHA256,
			SourceDataEffectID:      checkpoint.SourceDataEffectID,
			SourceDataReceiptSHA256: checkpoint.SourceDataReceiptSHA256,
			ProjectID:               checkpoint.ProjectID, NamespaceInode: checkpoint.NamespaceInode,
			Device: checkpoint.Device, DataFormat: checkpoint.DataFormat,
			WriterFence: checkpoint.WriterFence,
		},
	}
	digest, err := control.CheckpointLogicalDigest(checkpointCommand)
	if err != nil || digest != checkpoint.PayloadSHA256 {
		return zero, ErrFenceReconciliation
	}
	var checkpointState string
	var revision int64
	var manifest string
	var archiveBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT state,revision,manifest_sha256,bytes
		FROM runner_checkpoints WHERE id=? AND operation_id=? AND data_namespace_id=?`,
		checkpoint.CheckpointID, c.OperationID, checkpoint.NamespaceID).
		Scan(&checkpointState, &revision, &manifest, &archiveBytes); err != nil ||
		checkpointState != "verified" || (revision != 2 && revision != 4) ||
		manifest != checkpoint.ManifestSHA256 ||
		archiveBytes != mustDataInt(checkpoint.ArchiveBytes) {
		return zero, ErrFenceReconciliation
	}
	source, err := loadCheckpointSourceExcluding(ctx, tx, checkpointCommand, now, c.EffectID)
	if err != nil {
		return zero, ErrFenceReconciliation
	}
	if source.resource.observed != "stopped" || source.resource.desired != "stopped" ||
		source.source.Claim.ResourceID == p.ResourceID {
		return zero, ErrFenceReconciliation
	}
	var running int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources
		WHERE data_namespace_id=? AND kind='container' AND id NOT IN (?,?)
		AND observed_state NOT IN ('stopped','absent')`,
		p.NamespaceID, source.source.Claim.ResourceID, p.ResourceID).Scan(&running); err != nil || running != 0 {
		return zero, ErrFenceReconciliation
	}
	return updateStartSource{
		checkpoint: checkpoint, checkpointCmd: checkpointCommand, source: source,
		resource: source.resource, checkpointRaw: []byte(raw),
		checkpointSHA:  p.SourceCheckpointReceiptSHA256,
		checkpointData: mustDataInt(checkpoint.ArchiveBytes),
	}, nil
}

func (s *Store) beginUpdateStart(ctx context.Context, raw, signature []byte) (*sql.Tx, updateStartFacts, error) {
	var zero updateStartFacts
	if s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return nil, zero, ErrUpdateStartUnavailable
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
	authority := control.Authority{
		DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey,
		Fingerprint: deployment.Fingerprint,
	}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyUpdateStart(raw, signature, authority, now)
	if err != nil {
		return nil, zero, err
	}
	command := verified.Command()
	index := updateStartIndex(command)
	stored, err := loadAdmittedCommand(ctx, tx, command.EffectID)
	if err == nil {
		if !sameLogicalCommand(stored, index) || stored.state != "prepared" {
			return nil, zero, ErrFenceReconciliation
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, zero, err
	}
	fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
	generation, _ := strconv.ParseInt(command.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(command.Payload.TargetWriterFence, 10, 64)
	floor, err := commandResourceFloor(ctx, tx, index)
	if err != nil || fence < floor || generation != mustDataInt(command.Payload.SourceGeneration)+1 ||
		writer <= mustDataInt(command.Payload.SourceWriterFence) {
		return nil, zero, ErrFenceReconciliation
	}
	source, err := loadUpdateStartSource(ctx, tx, command, now)
	if err != nil {
		return nil, zero, err
	}
	if fence < source.resource.controlFence {
		return nil, zero, ErrFenceReconciliation
	}
	facts := updateStartFacts{
		command: command, stored: stored, authority: authority, source: source,
		raw: raw, signature: signature, now: now,
	}
	if err := finalUpdateStartCheck(ctx, facts); err != nil {
		return nil, zero, err
	}
	ok = true
	return tx, facts, nil
}

func finalUpdateStartCheck(ctx context.Context, f updateStartFacts) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	if now < f.now {
		return control.ErrInvalidCommand
	}
	_, err := control.VerifyUpdateStart(f.raw, f.signature, f.authority, now)
	return err
}

func reservedUpdateStart(f updateStartFacts, r *dataResource) (UpdateStartReservation, error) {
	var zero UpdateStartReservation
	c := f.command
	p := c.Payload
	ownership := updateStartOwnership(c, f.stored.fence)
	if r == nil || r.id != p.ResourceID || r.installation != c.InstallationID ||
		!r.namespace.Valid || r.namespace.String != p.NamespaceID || r.kind != "container" ||
		r.native != updateStartPendingPrefix+p.ResourceID || !r.release.Valid ||
		r.release.String != p.TargetReleaseSHA256 ||
		r.generation != mustDataInt(c.TargetGeneration) || r.epoch != c.ControllerEpoch ||
		r.fence != f.stored.fence || r.writer != mustDataInt(p.TargetWriterFence) ||
		r.desired != "present" || r.observed != "unknown" || r.ownership != ownership ||
		r.revision != 1 || r.observedMS.Valid || r.created < 0 ||
		r.updated != r.created || r.updated > f.now {
		return zero, ErrUpdateStartUnavailable
	}
	return UpdateStartReservation{
		ResourceID: r.id, InstallationID: r.installation, NamespaceID: r.namespace.String,
		EffectID: c.EffectID, OwnershipSHA256: ownership,
		SourceGeneration:    mustDataInt(p.SourceGeneration),
		TargetGeneration:    mustDataInt(c.TargetGeneration),
		SourceReleaseSHA256: p.SourceReleaseSHA256, TargetReleaseSHA256: p.TargetReleaseSHA256,
		ControlFence: r.fence, SourceWriterFence: mustDataInt(p.SourceWriterFence),
		TargetWriterFence: r.writer, Replayed: true,
	}, nil
}

// ReserveUpdateStart creates only one durable pending target resource. It never
// launches Docker or grants data writer access.
func (s *Store) ReserveUpdateStart(ctx context.Context, raw, signature []byte) (UpdateStartReservation, error) {
	var zero UpdateStartReservation
	tx, f, err := s.beginUpdateStart(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	c := f.command
	p := c.Payload
	existing, err := existingCandidateResourcePrefix(ctx, tx, updateStartIndex(c), updateStartPendingPrefix, p.ResourceID)
	if err != nil {
		return zero, err
	}
	if existing != nil {
		result, err := reservedUpdateStart(f, existing)
		if err != nil {
			return zero, err
		}
		if err := finalUpdateStartCheck(ctx, f); err != nil {
			return zero, err
		}
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		return result, nil
	}
	if f.stored.effectID != "" {
		return zero, ErrFenceReconciliation
	}
	fence, _ := strconv.ParseInt(c.InstallationFence, 10, 64)
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(p.TargetWriterFence, 10, 64)
	slot, _ := strconv.ParseInt(c.EffectNumber, 10, 64)
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,'prepared','{}',NULL,?,?)`,
		c.EffectID, c.OperationID, c.InstallationID, c.Stage, slot, c.Kind,
		c.PayloadSHA256, generation, c.ControllerEpoch, fence, f.now, f.now); err != nil {
		return zero, err
	}
	ownership := updateStartOwnership(c, fence)
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
		VALUES(?,?,?,'container',?,?,?,?,?,?,'present','unknown',?,1,NULL,?,?)`,
		p.ResourceID, c.InstallationID, p.NamespaceID, updateStartPendingPrefix+p.ResourceID,
		p.TargetReleaseSHA256, generation, c.ControllerEpoch, fence, writer, ownership,
		f.now, f.now); err != nil {
		return zero, err
	}
	result := UpdateStartReservation{
		ResourceID: p.ResourceID, InstallationID: c.InstallationID, NamespaceID: p.NamespaceID,
		EffectID: c.EffectID, OwnershipSHA256: ownership,
		SourceGeneration: mustDataInt(p.SourceGeneration), TargetGeneration: generation,
		SourceReleaseSHA256: p.SourceReleaseSHA256, TargetReleaseSHA256: p.TargetReleaseSHA256,
		ControlFence: fence, SourceWriterFence: mustDataInt(p.SourceWriterFence),
		TargetWriterFence: writer,
	}
	if err := finalUpdateStartCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrUpdateStartUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

func (s *Store) ReadUpdateStartReservation(ctx context.Context, raw, signature []byte) (UpdateStartReservation, error) {
	var zero UpdateStartReservation
	tx, f, err := s.beginUpdateStart(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	c := f.command
	resource, err := existingCandidateResourcePrefix(ctx, tx, updateStartIndex(c), updateStartPendingPrefix, c.Payload.ResourceID)
	if err != nil {
		return zero, err
	}
	result, err := reservedUpdateStart(f, resource)
	if err != nil {
		return zero, err
	}
	if err := finalUpdateStartCheck(ctx, f); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
