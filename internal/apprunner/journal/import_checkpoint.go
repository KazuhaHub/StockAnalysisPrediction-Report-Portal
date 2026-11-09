package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

var ErrImportCheckpointUnavailable = errors.New("runner checkpoint import is unavailable")

const importClaimFormat = "portal.runner.import-checkpoint-claim/1-dev.1"
const importReceiptFormat = "portal.runner.import-checkpoint-receipt/1-dev.1"

// importClaimMarker is the durable record of one uncertain import attempt. It
// is committed together with the data root binding, the namespace reservation
// and the unknown state, so no native call can precede it. Grant, checkpoint
// and namespace identities live here because the command row stores only a
// payload digest and uniqueness must be enforceable from the journal alone.
type importClaimMarker struct {
	Format             string `json:"format"`
	ClaimID            string `json:"claim_id"`
	EffectID           string `json:"effect_id"`
	ResourceID         string `json:"resource_id"`
	OwnershipSHA256    string `json:"ownership_sha256"`
	AdmittedFence      string `json:"admitted_fence"`
	ClaimedMS          string `json:"claimed_ms"`
	GrantID            string `json:"grant_id"`
	RecoverySetID      string `json:"recovery_set_id"`
	CheckpointID       string `json:"checkpoint_id"`
	NamespaceID        string `json:"namespace_id"`
	ProjectID          string `json:"project_id"`
	RootFilesystemUUID string `json:"root_filesystem_uuid"`
	RootInode          string `json:"root_inode"`
}

// ImportCheckpointClaim is the claim a receipt carries.
type ImportCheckpointClaim = importClaimMarker

// ImportCheckpointReceipt is a historical native observation of an imported
// namespace. It is not writer access, readiness or a Portal stage completion.
type ImportCheckpointReceipt struct {
	Format           string                `json:"format"`
	Claim            ImportCheckpointClaim `json:"claim"`
	DeploymentID     string                `json:"deployment_id"`
	ControllerEpoch  string                `json:"controller_epoch"`
	InstallationID   string                `json:"installation_id"`
	OperationID      string                `json:"operation_id"`
	TargetGeneration string                `json:"target_generation"`
	Stage            string                `json:"stage"`
	EffectID         string                `json:"effect_id"`
	PayloadSHA256    string                `json:"payload_sha256"`
	RecoverySetID    string                `json:"recovery_set_id"`
	GrantID          string                `json:"grant_id"`
	CheckpointID     string                `json:"checkpoint_id"`
	NamespaceID      string                `json:"namespace_id"`
	ReleaseSHA256    string                `json:"release_sha256"`
	ManifestSHA256   string                `json:"manifest_sha256"`
	ArchiveBytes     string                `json:"archive_bytes"`
	DataFormat       string                `json:"data_format"`
	WriterFence      string                `json:"writer_fence"`
	ProjectID        string                `json:"project_id"`
	NamespaceInode   string                `json:"namespace_inode"`
	Device           string                `json:"device"`
	QuotaBytes       string                `json:"quota_bytes"`
	QuotaInodes      string                `json:"quota_inodes"`
	Entries          string                `json:"entries"`
	UsedBytes        string                `json:"used_bytes"`
	UsedInodes       string                `json:"used_inodes"`
	State            string                `json:"state"`
	ObservedMS       string                `json:"observed_ms"`
	SettledMS        string                `json:"settled_ms"`
}

func markerImportRoot(m importClaimMarker) data.RootIdentity {
	inode, _ := strconv.ParseUint(m.RootInode, 10, 64)
	return data.RootIdentity{FilesystemUUID: m.RootFilesystemUUID, Inode: inode}
}

func importID(id string) bool {
	return enrollmentUUID.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}

func validImportMarker(m importClaimMarker) bool {
	if m.Format != importClaimFormat || !dataSHA(m.OwnershipSHA256) ||
		!dataNumber(m.AdmittedFence, true) || !dataNumber(m.ClaimedMS, false) ||
		!nativeNumber(m.RootInode, true) {
		return false
	}
	for _, id := range []string{m.ClaimID, m.EffectID, m.ResourceID, m.GrantID, m.RecoverySetID, m.CheckpointID, m.NamespaceID} {
		if !importID(id) {
			return false
		}
	}
	project, err := strconv.ParseUint(m.ProjectID, 10, 32)
	if err != nil || project == 0 || project > uint64(maxDataProjectID) || strconv.FormatUint(project, 10) != m.ProjectID {
		return false
	}
	return m.ResourceID == m.NamespaceID && validDataRoot(markerImportRoot(m))
}

func validImportReceipt(r ImportCheckpointReceipt) bool {
	if r.Format != importReceiptFormat || r.Stage != "restore_data" || r.State != "imported" ||
		!validImportMarker(r.Claim) || !dataSHA(r.PayloadSHA256) || !dataSHA(r.ReleaseSHA256) || !dataSHA(r.ManifestSHA256) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID, r.RecoverySetID, r.GrantID, r.CheckpointID, r.NamespaceID} {
		if !importID(id) {
			return false
		}
	}
	for _, value := range []string{r.TargetGeneration, r.ArchiveBytes, r.DataFormat, r.WriterFence, r.ProjectID, r.QuotaBytes, r.QuotaInodes, r.ObservedMS, r.SettledMS} {
		if !dataNumber(value, true) {
			return false
		}
	}
	for _, value := range []string{r.Entries, r.UsedBytes, r.UsedInodes} {
		if !dataNumber(value, false) {
			return false
		}
	}
	if !nativeNumber(r.NamespaceInode, true) || !nativeNumber(r.Device, true) {
		return false
	}
	archive, _ := strconv.ParseInt(r.ArchiveBytes, 10, 64)
	quotaBytes, _ := strconv.ParseInt(r.QuotaBytes, 10, 64)
	usedBytes, _ := strconv.ParseInt(r.UsedBytes, 10, 64)
	quotaInodes, _ := strconv.ParseInt(r.QuotaInodes, 10, 64)
	usedInodes, _ := strconv.ParseInt(r.UsedInodes, 10, 64)
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	return r.Claim.EffectID == r.EffectID && r.Claim.GrantID == r.GrantID &&
		r.Claim.RecoverySetID == r.RecoverySetID && r.Claim.CheckpointID == r.CheckpointID &&
		r.Claim.NamespaceID == r.NamespaceID && r.Claim.ProjectID == r.ProjectID &&
		archive <= maxCheckpointArchiveBytes && quotaBytes%512 == 0 &&
		usedBytes <= quotaBytes && usedInodes <= quotaInodes &&
		observed >= claimed && settled >= observed
}

func ParseImportCheckpointReceipt(raw []byte) (ImportCheckpointReceipt, error) {
	var receipt ImportCheckpointReceipt
	if len(raw) == 0 || len(raw) > 16384 || !canonicalDataJSON(string(raw), &receipt) || !validImportReceipt(receipt) {
		return ImportCheckpointReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

func importReceiptMatchesCommand(r ImportCheckpointReceipt, c control.ImportCheckpointCommand) bool {
	p := c.Payload
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch &&
		r.InstallationID == c.InstallationID && r.OperationID == c.OperationID &&
		r.TargetGeneration == c.TargetGeneration && r.Stage == c.Stage && r.EffectID == c.EffectID &&
		r.PayloadSHA256 == c.PayloadSHA256 && r.RecoverySetID == p.RecoverySetID &&
		r.GrantID == p.GrantID && r.CheckpointID == p.CheckpointID && r.NamespaceID == p.NamespaceID &&
		r.ReleaseSHA256 == p.ReleaseSHA256 && r.ManifestSHA256 == p.ManifestSHA256 &&
		r.ArchiveBytes == p.ArchiveBytes && r.DataFormat == p.DataFormat &&
		r.WriterFence == p.WriterFence && r.QuotaBytes == p.QuotaBytes && r.QuotaInodes == p.QuotaInodes
}

func importOwnership(c control.ImportCheckpointCommand, project uint32) string {
	claim := struct {
		Format, Deployment, Epoch, Revision, Installation, Namespace, Native string
		Effect, Payload, Generation, Fence, Release, DataFormat              string
		Bytes, Inodes, Writer, Grant                                         string
	}{
		"portal.runner.import-ownership/1-dev.1", c.DeploymentID, c.ControllerEpoch, c.ControlRevision,
		c.InstallationID, c.Payload.NamespaceID, dataProjectPrefix + strconv.FormatUint(uint64(project), 10),
		c.EffectID, c.PayloadSHA256, c.TargetGeneration, c.InstallationFence, c.Payload.ReleaseSHA256,
		c.Payload.DataFormat, c.Payload.QuotaBytes, c.Payload.QuotaInodes, c.Payload.WriterFence, c.Payload.GrantID,
	}
	raw, _ := json.Marshal(claim)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// importIntent is the in-process handle for one claimed import. It carries the
// signed bytes the native call and the settlement must re-verify.
type importIntent struct {
	command                 control.ImportCheckpointPayload
	full                    control.ImportCheckpointCommand
	raw, signature          []byte
	pin                     data.RootIdentity
	project                 uint32
	quotaBytes, quotaInodes int64
	marker                  importClaimMarker
	markerRaw               string
}

type importRow struct {
	operation, installation, stage, kind, payload, epoch, state, receipt string
	slot, generation, fence                                              int64
}

func loadImportRow(ctx context.Context, tx *sql.Tx, effect string) (*importRow, error) {
	var r importRow
	err := tx.QueryRowContext(ctx, `SELECT operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json FROM runner_commands WHERE effect_id=?`, effect).
		Scan(&r.operation, &r.installation, &r.stage, &r.slot, &r.kind, &r.payload, &r.generation, &r.epoch, &r.fence, &r.state, &r.receipt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *importRow) sameCommand(c control.ImportCheckpointCommand) bool {
	slot, _ := strconv.ParseInt(c.EffectNumber, 10, 64)
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	return r.operation == c.OperationID && r.installation == c.InstallationID && r.stage == c.Stage &&
		r.kind == c.Kind && r.payload == c.PayloadSHA256 && r.epoch == c.ControllerEpoch &&
		r.slot == slot && r.generation == generation
}

func importAuthorityFrom(deployment Deployment) control.Authority {
	return control.Authority{
		DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey,
		Fingerprint: deployment.Fingerprint,
	}
}

// requireNoImportDuplicate refuses a second effect that repeats a grant,
// checkpoint or namespace already named by any other import, whatever its
// state. History is never recycled.
func requireNoImportDuplicate(ctx context.Context, tx *sql.Tx, c control.ImportCheckpointCommand) error {
	rows, err := tx.QueryContext(ctx, `SELECT effect_id,state,receipt_json FROM runner_commands WHERE kind='import-checkpoint' AND effect_id<>? LIMIT 65537`, c.EffectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > maxDataReservations {
			return ErrFenceReconciliation
		}
		var effect, state, raw string
		if err := rows.Scan(&effect, &state, &raw); err != nil {
			return err
		}
		marker, err := importHistoryMarker(effect, state, raw)
		if err != nil {
			return err
		}
		if marker.GrantID == c.Payload.GrantID || marker.CheckpointID == c.Payload.CheckpointID || marker.NamespaceID == c.Payload.NamespaceID {
			return ErrDataReservationConflict
		}
	}
	return rows.Err()
}

// importHistoryMarker decodes the claim an import row retains in its current
// state. A row in any other state or format is unreadable history and refuses.
func importHistoryMarker(effect, state, raw string) (importClaimMarker, error) {
	var marker importClaimMarker
	switch state {
	case "unknown":
		if !canonicalDataJSON(raw, &marker) || !validImportMarker(marker) {
			return marker, ErrFenceReconciliation
		}
	case "applied":
		var receipt ImportCheckpointReceipt
		if !canonicalDataJSON(raw, &receipt) || !validImportReceipt(receipt) {
			return marker, ErrFenceReconciliation
		}
		marker = receipt.Claim
	default:
		return marker, ErrFenceReconciliation
	}
	if marker.EffectID != effect {
		return marker, ErrFenceReconciliation
	}
	return marker, nil
}

// requireImportFloor applies the same fence, generation and writer floors that
// admission applies to other effects, using the rows this journal holds.
func requireImportFloor(ctx context.Context, tx *sql.Tx, c control.ImportCheckpointCommand) error {
	var foreign int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND kind<>'load-image' AND controller_epoch<>?)+(SELECT COUNT(*) FROM runner_resources WHERE (installation_id=? OR data_namespace_id=?) AND controller_epoch<>?)`,
		c.InstallationID, c.ControllerEpoch, c.InstallationID, c.Payload.NamespaceID, c.ControllerEpoch).Scan(&foreign); err != nil {
		return err
	}
	if foreign != 0 {
		return ErrFenceReconciliation
	}
	var fenceFloor, generationFloor, writerFloor int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(fence),0) FROM (SELECT admitted_fence AS fence FROM runner_commands WHERE installation_id=? AND kind<>'load-image' UNION ALL SELECT control_fence AS fence FROM runner_resources WHERE installation_id=?)`, c.InstallationID, c.InstallationID).Scan(&fenceFloor); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0) FROM (SELECT target_generation AS generation FROM runner_commands WHERE installation_id=? AND kind<>'load-image' UNION ALL SELECT generation FROM runner_resources WHERE installation_id=?)`, c.InstallationID, c.InstallationID).Scan(&generationFloor); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(writer_fence),0) FROM runner_resources WHERE data_namespace_id=?`, c.Payload.NamespaceID).Scan(&writerFloor); err != nil {
		return err
	}
	fence, _ := strconv.ParseInt(c.InstallationFence, 10, 64)
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
	if fence < fenceFloor || generation < generationFloor || writer < writerFloor {
		return ErrCommandConflict
	}
	return nil
}

// beginImportCheckpoint verifies the signed command against the enrolled
// controller and, in one transaction, binds the data root, reserves the
// namespace with a project id this journal allocates, and commits the effect as
// unknown. A lost commit acknowledgement therefore leaves unknown work and never
// authorizes native creation. The caller holds the data execution gate.
func (s *Store) beginImportCheckpoint(ctx context.Context, raw, signature []byte, pin data.RootIdentity) (importIntent, bool, error) {
	var zero importIntent
	if s.closing.Load() || !validDataRoot(pin) {
		return zero, false, ErrImportCheckpointUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, false, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, false, err
	}
	authority := importAuthorityFrom(deployment)
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyImportCheckpoint(raw, signature, authority, now)
	if err != nil {
		return zero, false, err
	}
	c := verified.Command()
	existing, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return zero, false, err
	}
	if existing != nil {
		if !existing.sameCommand(c) {
			return zero, false, ErrCommandConflict
		}
		if existing.state == "applied" {
			parsed, err := ParseImportCheckpointReceipt([]byte(existing.receipt))
			if err != nil || !importReceiptMatchesCommand(parsed, c) {
				return zero, false, ErrFenceReconciliation
			}
			return zero, true, nil
		}
		// Unknown work is reconciled, never claimed a second time.
		return zero, false, ErrFenceReconciliation
	}
	if err := requireDataRootHistory(ctx, tx, pin); err != nil {
		return zero, false, err
	}
	if err := requireNoImportDuplicate(ctx, tx, c); err != nil {
		return zero, false, err
	}
	var resources int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources WHERE id=? OR data_namespace_id=? OR (kind='volume' AND installation_id=?)`,
		c.Payload.NamespaceID, c.Payload.NamespaceID, c.InstallationID).Scan(&resources); err != nil {
		return zero, false, err
	}
	if resources != 0 {
		return zero, false, ErrDataReservationConflict
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND kind<>'load-image' AND state IN ('prepared','unknown')`, c.InstallationID).Scan(&pending); err != nil {
		return zero, false, err
	}
	if pending != 0 {
		return zero, false, ErrFenceReconciliation
	}
	if err := requireImportFloor(ctx, tx, c); err != nil {
		return zero, false, err
	}
	project, err := allocateDataProject(ctx, tx)
	if err != nil {
		return zero, false, err
	}
	claimID, err := dataClaimID()
	if err != nil {
		return zero, false, err
	}
	ownership := importOwnership(c, project)
	marker := importClaimMarker{
		Format: importClaimFormat, ClaimID: claimID, EffectID: c.EffectID, ResourceID: c.Payload.NamespaceID,
		OwnershipSHA256: ownership, AdmittedFence: c.InstallationFence, ClaimedMS: strconv.FormatInt(now, 10),
		GrantID: c.Payload.GrantID, RecoverySetID: c.Payload.RecoverySetID, CheckpointID: c.Payload.CheckpointID,
		NamespaceID: c.Payload.NamespaceID, ProjectID: strconv.FormatUint(uint64(project), 10),
		RootFilesystemUUID: pin.FilesystemUUID, RootInode: strconv.FormatUint(pin.Inode, 10),
	}
	markerRaw, err := json.Marshal(marker)
	if err != nil || len(markerRaw) > 16384 || !validImportMarker(marker) {
		return zero, false, ErrImportCheckpointUnavailable
	}
	fence, _ := strconv.ParseInt(c.InstallationFence, 10, 64)
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
	slot, _ := strconv.ParseInt(c.EffectNumber, 10, 64)
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'volume',?,?,?,?,?,?,'present','unknown',?,1,NULL,?,?)`,
		c.Payload.NamespaceID, c.InstallationID, c.Payload.NamespaceID, dataProjectPrefix+strconv.FormatUint(uint64(project), 10),
		c.Payload.ReleaseSHA256, generation, c.ControllerEpoch, fence, writer, ownership, now, now); err != nil {
		return zero, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,'unknown',?,NULL,?,?)`,
		c.EffectID, c.OperationID, c.InstallationID, c.Stage, slot, c.Kind, c.PayloadSHA256, generation,
		c.ControllerEpoch, fence, string(markerRaw), now, now); err != nil {
		return zero, false, err
	}
	if err := ctx.Err(); err != nil {
		return zero, false, err
	}
	if _, err := control.VerifyImportCheckpoint(raw, signature, authority, time.Now().UTC().UnixMilli()); err != nil {
		return zero, false, fmt.Errorf("import claim command: %w", err)
	}
	if s.closing.Load() {
		return zero, false, ErrImportCheckpointUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, false, err
	}
	quotaBytes, _ := strconv.ParseInt(c.Payload.QuotaBytes, 10, 64)
	quotaInodes, _ := strconv.ParseInt(c.Payload.QuotaInodes, 10, 64)
	return importIntent{
		command: c.Payload, full: c, raw: raw, signature: signature, pin: pin, project: project,
		quotaBytes: quotaBytes, quotaInodes: quotaInodes, marker: marker, markerRaw: string(markerRaw),
	}, false, nil
}

// ErrNoSuchEffect is returned by a read only when the journal proves it holds no
// command row for the effect identity (an import-checkpoint or a restore-start).
// It is the one definite "never claimed" answer; any other read failure leaves
// the effect unknown.
var ErrNoSuchEffect = errors.New("runner holds no such effect")

const importAbsenceFormat = "portal.runner.import-checkpoint-absence/1-dev.1"

// ImportCheckpointAbsence is the runner's reply to a read-receipt for an effect it
// never claimed. It binds the effect identity of the signed command it answers.
type ImportCheckpointAbsence struct {
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

func importAbsenceFor(c control.ImportCheckpointCommand, now int64) ImportCheckpointAbsence {
	return ImportCheckpointAbsence{
		Format: importAbsenceFormat, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration,
		Stage: c.Stage, EffectNumber: c.EffectNumber, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256,
		State: "absent", ObservedMS: strconv.FormatInt(now, 10),
	}
}

func validImportAbsence(a ImportCheckpointAbsence) bool {
	if a.Format != importAbsenceFormat || a.Stage != "restore_data" || a.State != "absent" || !dataSHA(a.PayloadSHA256) ||
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

// ParseImportCheckpointAbsence accepts only the canonical encoding of a valid absence reply.
func ParseImportCheckpointAbsence(raw []byte) (ImportCheckpointAbsence, error) {
	var absence ImportCheckpointAbsence
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &absence) || !validImportAbsence(absence) {
		return ImportCheckpointAbsence{}, ErrFenceReconciliation
	}
	return absence, nil
}

// ImportAbsenceMatchesCommand reports whether the reply answers exactly this signed command.
func ImportAbsenceMatchesCommand(a ImportCheckpointAbsence, c control.ImportCheckpointCommand) bool {
	return importAbsenceMatchesCommand(a, c)
}

func importAbsenceMatchesCommand(a ImportCheckpointAbsence, c control.ImportCheckpointCommand) bool {
	return a.DeploymentID == c.DeploymentID && a.ControllerEpoch == c.ControllerEpoch &&
		a.InstallationID == c.InstallationID && a.OperationID == c.OperationID &&
		a.TargetGeneration == c.TargetGeneration && a.Stage == c.Stage && a.EffectNumber == c.EffectNumber &&
		a.EffectID == c.EffectID && a.PayloadSHA256 == c.PayloadSHA256
}
