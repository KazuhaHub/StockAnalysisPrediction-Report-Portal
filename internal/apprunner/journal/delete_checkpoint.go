package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// Deleting one retained checkpoint (doc 172, section 6).
//
// The Portal decides that a recovery set is beyond retention; the runner retires its checkpoint. The command runs
// under the data execution gate, the gate checkpoint, restore, import and migrate already take, so the claim is
// serialized with every reader of an archive. The claim, in one transaction, writes the command row as unknown and
// marks the checkpoint row deleted. From that commit on every consumer refuses the checkpoint (restore, migrate,
// update-start and read-back all require a verified or restoring row), so no consumer changes. Only then is the
// archive file removed, and the effect settles applied. The decision is irreversible once claimed: an interrupted
// deletion is finished by repeating the removal, never undone.

var (
	// ErrDeleteCheckpointUnavailable is a refusal before any claim: the runner cannot delete checkpoints now.
	ErrDeleteCheckpointUnavailable = errors.New("runner checkpoint deletion is unavailable")
	// ErrDeleteCheckpointInstallationBusy is the typed refusal of a claim while an unsettled command that is
	// installation work (a restore, migration, import, update start or any other start-bearing command) exists for
	// the installation. The journal stores only the payload digest of such a command, so it cannot tell whether it
	// names this checkpoint, and refuses for all of them. Nothing is claimed and nothing is touched.
	ErrDeleteCheckpointInstallationBusy = errors.New("the installation has unsettled work that may use a checkpoint (installation_busy)")
)

const deleteCheckpointClaimFormat = "portal.runner.delete-checkpoint-claim/1-dev.1"
const deleteCheckpointReceiptFormat = "portal.runner.delete-checkpoint-receipt/1-dev.1"
const deleteCheckpointStatusFormat = "portal.runner.delete-checkpoint-status/1-dev.1"

// The outcomes of a deletion. An applied effect carries DeleteOutcomeRemoved or DeleteOutcomeAbsent; a reverted one
// carries a refusal, and its checkpoint row and archive are untouched.
const (
	DeleteOutcomeRemoved = "removed"
	// DeleteOutcomeAbsent: the runner holds no live record of the checkpoint (no row and no archive, or a row already
	// deleted by another effect). The Portal may treat the set as gone.
	DeleteOutcomeAbsent = "absent"
	// DeleteOutcomeIdentityMismatch: the payload does not describe the runner's row. Not retried.
	DeleteOutcomeIdentityMismatch = "checkpoint_identity_mismatch"
	// DeleteOutcomeInUse: the checkpoint is being restored. Retried later.
	DeleteOutcomeInUse = "checkpoint_in_use"
	// DeleteOutcomeNotVerified: the checkpoint is not in a verified state. Not retried.
	DeleteOutcomeNotVerified = "checkpoint_not_verified"
	// DeleteOutcomeLatest: the checkpoint is the newest of a namespace that still exists. Retried later.
	DeleteOutcomeLatest = "checkpoint_latest"
	// DeleteOutcomeUnrecorded: an archive file exists for which the runner has no record. It never deletes such a
	// file; the operator handles it. Not retried.
	DeleteOutcomeUnrecorded = "checkpoint_unrecorded"
)

// DeleteCheckpointNative is the native side of a deletion: the retained archive of a checkpoint, observed and
// removed. It returns plain observations so the journal binds every field.
type DeleteCheckpointNative interface {
	// InspectCheckpointArchive observes the archive without changing anything.
	InspectCheckpointArchive(ctx context.Context, checkpointID string) (data.CheckpointArchive, error)
	// RemoveCheckpointArchive removes the archive, which must be a regular, singly linked file of exactly
	// archiveBytes, and confirms its absence. An archive that is already gone is reported with Found false.
	// manifestSHA256 is the SHA-256 of the archive file, used to identify a leftover of an interrupted removal.
	RemoveCheckpointArchive(ctx context.Context, checkpointID string, archiveBytes int64, manifestSHA256 string) (data.CheckpointArchive, error)
}

type rootDeleteCheckpointNative struct {
	root *data.Root
	pin  data.RootIdentity
}

// NewDeleteCheckpointNative binds the native side to the pinned checkpoint root.
func NewDeleteCheckpointNative(root *data.Root, pin data.RootIdentity) DeleteCheckpointNative {
	return rootDeleteCheckpointNative{root: root, pin: pin}
}

func (n rootDeleteCheckpointNative) InspectCheckpointArchive(ctx context.Context, id string) (data.CheckpointArchive, error) {
	if n.root == nil {
		return data.CheckpointArchive{}, ErrDeleteCheckpointUnavailable
	}
	return n.root.InspectCheckpointArchive(ctx, n.pin, id)
}

func (n rootDeleteCheckpointNative) RemoveCheckpointArchive(ctx context.Context, id string, archiveBytes int64, manifestSHA256 string) (data.CheckpointArchive, error) {
	if n.root == nil {
		return data.CheckpointArchive{}, ErrDeleteCheckpointUnavailable
	}
	return n.root.RemoveCheckpointArchive(ctx, n.pin, id, archiveBytes, manifestSHA256)
}

type deleteCheckpointClaim struct {
	Format            string `json:"format"`
	ClaimID           string `json:"claim_id"`
	EffectID          string `json:"effect_id"`
	RecoverySetID     string `json:"recovery_set_id"`
	CheckpointID      string `json:"checkpoint_id"`
	NamespaceID       string `json:"namespace_id"`
	SourceOperationID string `json:"source_operation_id"`
	ReleaseSHA256     string `json:"release_sha256"`
	ManifestSHA256    string `json:"manifest_sha256"`
	ArchiveBytes      string `json:"archive_bytes"`
	DataFormat        string `json:"data_format"`
	WriterFence       string `json:"writer_fence"`
	AdmittedFence     string `json:"admitted_fence"`
	ClaimedMS         string `json:"claimed_ms"`
}

// DeleteCheckpointClaim is the claim a receipt carries.
type DeleteCheckpointClaim = deleteCheckpointClaim

// DeleteCheckpointReceipt is the historical record of one deletion. Outcome is DeleteOutcomeRemoved or
// DeleteOutcomeAbsent for an applied effect and a refusal for a reverted one. ArchiveFound is whether the archive
// file existed when the runner removed it; it is false for every outcome but a removal, and may be false for a
// removal that an interrupted attempt had already finished.
type DeleteCheckpointReceipt struct {
	Format           string                `json:"format"`
	Claim            DeleteCheckpointClaim `json:"claim"`
	DeploymentID     string                `json:"deployment_id"`
	ControllerEpoch  string                `json:"controller_epoch"`
	InstallationID   string                `json:"installation_id"`
	OperationID      string                `json:"operation_id"`
	TargetGeneration string                `json:"target_generation"`
	Stage            string                `json:"stage"`
	EffectID         string                `json:"effect_id"`
	PayloadSHA256    string                `json:"payload_sha256"`
	RecoverySetID    string                `json:"recovery_set_id"`
	CheckpointID     string                `json:"checkpoint_id"`
	NamespaceID      string                `json:"namespace_id"`
	ManifestSHA256   string                `json:"manifest_sha256"`
	ArchiveBytes     string                `json:"archive_bytes"`
	Outcome          string                `json:"outcome"`
	ArchiveFound     bool                  `json:"archive_found"`
	ObservedMS       string                `json:"observed_ms"`
	SettledMS        string                `json:"settled_ms"`
}

// Applied reports whether the receipt is that of an applied effect: the checkpoint is retired.
func (r DeleteCheckpointReceipt) Applied() bool {
	return r.Outcome == DeleteOutcomeRemoved || r.Outcome == DeleteOutcomeAbsent
}

func deleteCheckpointRefusal(outcome string) bool {
	switch outcome {
	case DeleteOutcomeIdentityMismatch, DeleteOutcomeInUse, DeleteOutcomeNotVerified, DeleteOutcomeLatest, DeleteOutcomeUnrecorded:
		return true
	}
	return false
}

func validDeleteCheckpointClaim(c deleteCheckpointClaim) bool {
	if c.Format != deleteCheckpointClaimFormat || !importID(c.ClaimID) || !importID(c.EffectID) || !importID(c.RecoverySetID) || !importID(c.CheckpointID) ||
		!importID(c.NamespaceID) || !importID(c.SourceOperationID) || !dataSHA(c.ReleaseSHA256) || !dataSHA(c.ManifestSHA256) ||
		!dataNumber(c.AdmittedFence, true) || !dataNumber(c.ClaimedMS, false) {
		return false
	}
	size, ok := parseDataNumber(c.ArchiveBytes)
	return ok && size > 0 && size <= control.MaxDeleteCheckpointArchiveBytes && dataNumber(c.DataFormat, true) && dataNumber(c.WriterFence, true)
}

func parseDataNumber(value string) (int64, bool) {
	if !dataNumber(value, true) {
		return 0, false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil
}

func validDeleteCheckpointReceipt(r DeleteCheckpointReceipt) bool {
	if r.Format != deleteCheckpointReceiptFormat || r.Stage != control.DeleteCheckpointStage || !validDeleteCheckpointClaim(r.Claim) || !dataSHA(r.PayloadSHA256) ||
		!importID(r.RecoverySetID) || !importID(r.CheckpointID) || !importID(r.NamespaceID) || !dataSHA(r.ManifestSHA256) || !dataNumber(r.ArchiveBytes, true) {
		return false
	}
	switch {
	case r.Outcome == DeleteOutcomeRemoved:
	case r.Outcome == DeleteOutcomeAbsent || deleteCheckpointRefusal(r.Outcome):
		if r.ArchiveFound {
			return false
		}
	default:
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID} {
		if !importID(id) {
			return false
		}
	}
	for _, value := range []string{r.TargetGeneration, r.ObservedMS, r.SettledMS} {
		if !dataNumber(value, true) {
			return false
		}
	}
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	return r.Claim.EffectID == r.EffectID && r.RecoverySetID == r.Claim.RecoverySetID && r.CheckpointID == r.Claim.CheckpointID &&
		r.NamespaceID == r.Claim.NamespaceID && r.ManifestSHA256 == r.Claim.ManifestSHA256 && r.ArchiveBytes == r.Claim.ArchiveBytes &&
		observed >= claimed && settled >= observed
}

func ParseDeleteCheckpointReceipt(raw []byte) (DeleteCheckpointReceipt, error) {
	var receipt DeleteCheckpointReceipt
	if len(raw) == 0 || len(raw) > 16384 || !canonicalDataJSON(string(raw), &receipt) || !validDeleteCheckpointReceipt(receipt) {
		return DeleteCheckpointReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

func deleteCheckpointClaimMatchesCommand(m deleteCheckpointClaim, c control.DeleteCheckpointCommand) bool {
	p := c.Payload
	return m.EffectID == c.EffectID && m.RecoverySetID == p.RecoverySetID && m.CheckpointID == p.CheckpointID && m.NamespaceID == p.NamespaceID &&
		m.SourceOperationID == p.SourceOperationID && m.ReleaseSHA256 == p.ReleaseSHA256 && m.ManifestSHA256 == p.ManifestSHA256 &&
		m.ArchiveBytes == p.ArchiveBytes && m.DataFormat == p.DataFormat && m.WriterFence == p.WriterFence && m.AdmittedFence == c.InstallationFence
}

// DeleteCheckpointReceiptMatchesCommand reports whether the receipt answers exactly this signed command.
func DeleteCheckpointReceiptMatchesCommand(r DeleteCheckpointReceipt, c control.DeleteCheckpointCommand) bool {
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch && r.InstallationID == c.InstallationID &&
		r.OperationID == c.OperationID && r.TargetGeneration == c.TargetGeneration && r.Stage == c.Stage && r.EffectID == c.EffectID &&
		r.PayloadSHA256 == c.PayloadSHA256 && deleteCheckpointClaimMatchesCommand(r.Claim, c)
}

// DeleteCheckpointStatus is the runner's typed answer when there is no receipt: "pending" while the data gate is
// held, "orphaned" for an unknown effect nothing is running, and "absent" for an effect the journal proves it never
// claimed (given only under the gate). A read never writes a row.
type DeleteCheckpointStatus struct {
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

func validDeleteCheckpointStatus(s DeleteCheckpointStatus) bool {
	if s.Format != deleteCheckpointStatusFormat || s.Stage != control.DeleteCheckpointStage || !dataSHA(s.PayloadSHA256) || !dataNumber(s.TargetGeneration, true) ||
		!dataNumber(s.EffectNumber, true) || !dataNumber(s.ObservedMS, true) {
		return false
	}
	switch s.State {
	case "pending", "orphaned", "absent":
	default:
		return false
	}
	for _, id := range []string{s.DeploymentID, s.ControllerEpoch, s.InstallationID, s.OperationID, s.EffectID} {
		if !importID(id) {
			return false
		}
	}
	return true
}

func ParseDeleteCheckpointStatus(raw []byte) (DeleteCheckpointStatus, error) {
	var status DeleteCheckpointStatus
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &status) || !validDeleteCheckpointStatus(status) {
		return DeleteCheckpointStatus{}, ErrFenceReconciliation
	}
	return status, nil
}

func deleteCheckpointStatusFor(c control.DeleteCheckpointCommand, state string, now int64) DeleteCheckpointStatus {
	return DeleteCheckpointStatus{
		Format: deleteCheckpointStatusFormat, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID,
		OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectNumber: c.EffectNumber,
		EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, State: state, ObservedMS: strconv.FormatInt(now, 10),
	}
}

// DeleteCheckpointStatusMatchesCommand reports whether the status answers exactly this signed command.
func DeleteCheckpointStatusMatchesCommand(s DeleteCheckpointStatus, c control.DeleteCheckpointCommand) bool {
	return s.DeploymentID == c.DeploymentID && s.ControllerEpoch == c.ControllerEpoch && s.InstallationID == c.InstallationID &&
		s.OperationID == c.OperationID && s.TargetGeneration == c.TargetGeneration && s.Stage == c.Stage && s.EffectNumber == c.EffectNumber &&
		s.EffectID == c.EffectID && s.PayloadSHA256 == c.PayloadSHA256
}

type deleteCheckpointIntent struct {
	full           control.DeleteCheckpointCommand
	raw, signature []byte
	marker         deleteCheckpointClaim
	markerRaw      string
	// resumed is true for an effect an earlier attempt left unknown.
	resumed bool
	// settled is the receipt of a claim that ended in the claim transaction itself: a refusal, or an absence that
	// needed no native work.
	settled *DeleteCheckpointReceipt
}

func (s *Store) deleteCheckpointState(ctx context.Context, raw, signature []byte) (string, control.DeleteCheckpointCommand, *importRow, error) {
	var none control.DeleteCheckpointCommand
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", none, nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return "", none, nil, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return "", none, nil, err
	}
	authority := importAuthorityFrom(deployment)
	verified, err := control.VerifyDeleteCheckpointSettlement(raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return "", none, nil, err
	}
	c := verified.Command()
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return "", none, nil, err
	}
	if row == nil {
		if _, err := control.VerifyDeleteCheckpoint(raw, signature, authority, time.Now().UTC().UnixMilli()); err != nil {
			return "", none, nil, err
		}
		return "", c, nil, nil
	}
	if !sameMigrateCommand(row, c.CommandHeader) {
		return "", none, nil, ErrCommandConflict
	}
	if row.state != "applied" && row.state != "reverted" && row.state != "unknown" {
		return "", none, nil, ErrFenceReconciliation
	}
	return row.state, c, row, nil
}

// checkpointRow is the runner's record of the checkpoint a deletion names.
type checkpointRow struct {
	namespace, installation, operation, release string
	manifest                                    sql.NullString
	format, writer, bytes, revision, created    int64
	state                                       string
}

func loadCheckpointRow(ctx context.Context, tx *sql.Tx, id string) (*checkpointRow, error) {
	var r checkpointRow
	err := tx.QueryRowContext(ctx, `SELECT data_namespace_id,installation_id,operation_id,release_sha256,manifest_sha256,data_format,writer_fence,bytes,state,revision,created_ms
		FROM runner_checkpoints WHERE id=?`, id).
		Scan(&r.namespace, &r.installation, &r.operation, &r.release, &r.manifest, &r.format, &r.writer, &r.bytes, &r.state, &r.revision, &r.created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// beginDeleteCheckpoint verifies the signed command and, in one transaction, checks the floors and the installation
// and commits the effect: as unknown with the checkpoint row marked deleted (a claim), or settled at once (a refusal,
// or an absence that needs no native work). A lost commit acknowledgement leaves unknown work and never authorizes a
// native call. The caller holds the data execution gate.
func (s *Store) beginDeleteCheckpoint(ctx context.Context, raw, signature []byte, native DeleteCheckpointNative) (deleteCheckpointIntent, error) {
	var zero deleteCheckpointIntent
	if s.closing.Load() {
		return zero, ErrDeleteCheckpointUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	authority := importAuthorityFrom(deployment)
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyDeleteCheckpoint(raw, signature, authority, now)
	if err != nil {
		return zero, err
	}
	c := verified.Command()
	existing, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return zero, err
	}
	if existing != nil {
		return zero, ErrCommandConflict
	}
	var siblings int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE operation_id=? AND stage=? AND effect_number=?`,
		c.OperationID, c.Stage, mustDataInt(c.EffectNumber)).Scan(&siblings); err != nil {
		return zero, err
	}
	if siblings != 0 {
		return zero, fmt.Errorf("%w: effect number in use", ErrCommandConflict)
	}
	var foreign int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND controller_epoch<>?)+(SELECT COUNT(*) FROM runner_resources WHERE installation_id=? AND controller_epoch<>?)`,
		c.InstallationID, c.ControllerEpoch, c.InstallationID, c.ControllerEpoch).Scan(&foreign); err != nil || foreign != 0 {
		return zero, ErrFenceReconciliation
	}
	var fenceFloor, generationFloor int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(fence),0) FROM (SELECT admitted_fence AS fence FROM runner_commands WHERE installation_id=? UNION ALL SELECT control_fence AS fence FROM runner_resources WHERE installation_id=?)`,
		c.InstallationID, c.InstallationID).Scan(&fenceFloor); err != nil {
		return zero, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0) FROM (SELECT target_generation AS generation FROM runner_commands WHERE installation_id=? UNION ALL SELECT generation FROM runner_resources WHERE installation_id=?)`,
		c.InstallationID, c.InstallationID).Scan(&generationFloor); err != nil {
		return zero, err
	}
	fence, generation := mustDataInt(c.InstallationFence), mustDataInt(c.TargetGeneration)
	if fence < fenceFloor || generation < generationFloor {
		return zero, fmt.Errorf("%w: delete-checkpoint fence floors", ErrCommandConflict)
	}
	// Deliberately blunt: an unsettled command that is installation work may be a restore, migration, import or
	// update start naming this checkpoint, and the journal cannot tell which. Nothing is claimed.
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND kind NOT IN (`+installationNeutralCommandKinds+`) AND state IN ('prepared','unknown')`,
		c.InstallationID).Scan(&pending); err != nil {
		return zero, err
	}
	if pending != 0 {
		return zero, ErrDeleteCheckpointInstallationBusy
	}
	claimID, err := dataClaimID()
	if err != nil {
		return zero, err
	}
	p := c.Payload
	marker := deleteCheckpointClaim{
		Format: deleteCheckpointClaimFormat, ClaimID: claimID, EffectID: c.EffectID, RecoverySetID: p.RecoverySetID, CheckpointID: p.CheckpointID,
		NamespaceID: p.NamespaceID, SourceOperationID: p.SourceOperationID, ReleaseSHA256: p.ReleaseSHA256, ManifestSHA256: p.ManifestSHA256,
		ArchiveBytes: p.ArchiveBytes, DataFormat: p.DataFormat, WriterFence: p.WriterFence, AdmittedFence: c.InstallationFence,
		ClaimedMS: strconv.FormatInt(now, 10),
	}
	markerRaw, err := json.Marshal(marker)
	if err != nil || len(markerRaw) > 16384 || !validDeleteCheckpointClaim(marker) || !deleteCheckpointClaimMatchesCommand(marker, c) {
		return zero, ErrDeleteCheckpointUnavailable
	}
	intent := deleteCheckpointIntent{full: c, raw: bytes.Clone(raw), signature: bytes.Clone(signature), marker: marker, markerRaw: string(markerRaw)}
	outcome, finishEarlier, err := s.deleteCheckpointDecision(ctx, tx, c, native)
	if err != nil {
		return zero, err
	}
	insert := func(state, receipt string) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			c.EffectID, c.OperationID, c.InstallationID, c.Stage, mustDataInt(c.EffectNumber), c.Kind, c.PayloadSHA256, generation, c.ControllerEpoch, fence,
			state, receipt, now, now)
		return err
	}
	if outcome == "" {
		// The claim: the command row is unknown and the checkpoint is deleted in the same commit.
		if err := insert("unknown", string(markerRaw)); err != nil {
			return zero, err
		}
		if !finishEarlier {
			changed, err := tx.ExecContext(ctx, `UPDATE runner_checkpoints SET state='deleted',revision=revision+1,updated_ms=? WHERE id=? AND state='verified'`, now, p.CheckpointID)
			if err != nil {
				return zero, err
			}
			if n, err := changed.RowsAffected(); err != nil || n != 1 {
				return zero, fmt.Errorf("delete-checkpoint claim rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
			}
		}
	} else {
		// Settled in the claim transaction: nothing native is done, so the verdict is final.
		receipt := deleteCheckpointReceiptFor(intent, outcome, false, now, now)
		encoded, err := json.Marshal(receipt)
		if err != nil || len(encoded) > 16384 || !validDeleteCheckpointReceipt(receipt) || !DeleteCheckpointReceiptMatchesCommand(receipt, c) {
			return zero, fmt.Errorf("delete-checkpoint receipt: %w", ErrFenceReconciliation)
		}
		state := "applied"
		if deleteCheckpointRefusal(outcome) {
			state = "reverted"
		}
		if err := insert(state, string(encoded)); err != nil {
			return zero, err
		}
		intent.settled = &receipt
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if _, err := control.VerifyDeleteCheckpoint(raw, signature, authority, time.Now().UTC().UnixMilli()); err != nil {
		return zero, fmt.Errorf("delete-checkpoint claim command: %w", err)
	}
	if s.closing.Load() {
		return zero, ErrDeleteCheckpointUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return intent, nil
}

// deleteCheckpointDecision classifies the command against the runner's own record. It returns "" for a claim to
// proceed, or the outcome that settles the effect in the claim transaction. Identity is checked first, against the
// fields every state carries; the manifest and size, which only a verified checkpoint has, are checked once the state
// is known to be verified.
func (s *Store) deleteCheckpointDecision(ctx context.Context, tx *sql.Tx, c control.DeleteCheckpointCommand, native DeleteCheckpointNative) (outcome string, finishEarlier bool, err error) {
	p := c.Payload
	row, err := loadCheckpointRow(ctx, tx, p.CheckpointID)
	if err != nil {
		return "", false, err
	}
	if row == nil {
		// A fresh host: the old host's checkpoint was never recorded here. With no file there is nothing to retire.
		// A file with no record is never deleted by the runner.
		if native == nil {
			return "", false, ErrDeleteCheckpointUnavailable
		}
		archive, err := native.InspectCheckpointArchive(ctx, p.CheckpointID)
		if err != nil {
			return "", false, fmt.Errorf("inspect unrecorded archive: %w", err)
		}
		if archive.Found {
			return DeleteOutcomeUnrecorded, false, nil
		}
		return DeleteOutcomeAbsent, false, nil
	}
	if row.namespace != p.NamespaceID || row.installation != c.InstallationID || row.operation != p.SourceOperationID ||
		row.release != p.ReleaseSHA256 || row.format != mustDataInt(p.DataFormat) || row.writer != mustDataInt(p.WriterFence) {
		return DeleteOutcomeIdentityMismatch, false, nil
	}
	switch row.state {
	case "preparing", "review_required":
		return DeleteOutcomeNotVerified, false, nil
	case "restoring":
		return DeleteOutcomeInUse, false, nil
	case "deleted":
		// Deleted by another effect. When that effect's removal is proven (its row is settled) there is nothing left
		// to retire. When an earlier claim marked the row deleted and its removal was never proven (a claim whose
		// effect is still unknown, for instance after the installation's fence moved and the Portal signed a new
		// attempt), the archive, or its trash entry, may still be on disk: this effect finishes that removal under
		// its own claim and settles the earlier one with it, before it answers.
		var unproven int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE kind='delete-checkpoint' AND state='unknown' AND json_extract(receipt_json,'$.checkpoint_id')=?`, p.CheckpointID).Scan(&unproven); err != nil {
			return "", false, err
		}
		if unproven != 0 {
			return "", true, nil
		}
		return DeleteOutcomeAbsent, false, nil
	case "verified":
	default:
		return DeleteOutcomeNotVerified, false, nil
	}
	if !row.manifest.Valid || row.manifest.String != p.ManifestSHA256 || row.bytes != mustDataInt(p.ArchiveBytes) {
		return DeleteOutcomeIdentityMismatch, false, nil
	}
	if row.revision != 2 && row.revision != 4 {
		return DeleteOutcomeNotVerified, false, nil
	}
	// Defence in depth: the Portal never selects the newest verified checkpoint of a namespace that still exists. The
	// order is created_ms, newer first, then checkpoint id, smaller first (the order validateCheckpointRecord reads
	// the newest in). Only verified siblings count: a preparing, restoring or review_required newer row is not a
	// fallback anyone can restore, so it never makes the newest verified checkpoint deletable. A retained
	// (uninstalled, data kept) namespace keeps its last checkpoint until the namespace itself is deleted.
	var newer, live int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_checkpoints WHERE data_namespace_id=? AND state='verified' AND id<>? AND (created_ms>? OR (created_ms=? AND id<?))`,
		row.namespace, p.CheckpointID, row.created, row.created, p.CheckpointID).Scan(&newer); err != nil {
		return "", false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources WHERE data_namespace_id=? AND kind='volume' AND observed_state<>'absent'`, row.namespace).Scan(&live); err != nil {
		return "", false, err
	}
	if newer == 0 && live != 0 {
		return DeleteOutcomeLatest, false, nil
	}
	return "", false, nil
}

func deleteCheckpointReceiptFor(intent deleteCheckpointIntent, outcome string, found bool, observed, settled int64) DeleteCheckpointReceipt {
	c := intent.full
	claimed := mustDataInt(intent.marker.ClaimedMS)
	observed = maxInt64(observed, claimed)
	return DeleteCheckpointReceipt{
		Format: deleteCheckpointReceiptFormat, Claim: intent.marker, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectID: c.EffectID,
		PayloadSHA256: c.PayloadSHA256, RecoverySetID: c.Payload.RecoverySetID, CheckpointID: c.Payload.CheckpointID, NamespaceID: c.Payload.NamespaceID,
		ManifestSHA256: c.Payload.ManifestSHA256, ArchiveBytes: c.Payload.ArchiveBytes, Outcome: outcome, ArchiveFound: found,
		ObservedMS: strconv.FormatInt(observed, 10), SettledMS: strconv.FormatInt(maxInt64(settled, observed), 10),
	}
}

func deleteCheckpointIntentFromRow(row *importRow, c control.DeleteCheckpointCommand, raw, signature []byte) (deleteCheckpointIntent, error) {
	var marker deleteCheckpointClaim
	if row.state != "unknown" || !canonicalDataJSON(row.receipt, &marker) || !validDeleteCheckpointClaim(marker) || !deleteCheckpointClaimMatchesCommand(marker, c) {
		return deleteCheckpointIntent{}, ErrFenceReconciliation
	}
	return deleteCheckpointIntent{full: c, raw: bytes.Clone(raw), signature: bytes.Clone(signature), marker: marker, markerRaw: row.receipt, resumed: true}, nil
}

// settleDeleteCheckpoint records the definite end of a claimed deletion in one transaction: applied, with the
// receipt replacing the claim.
func (s *Store) settleDeleteCheckpoint(ctx context.Context, intent deleteCheckpointIntent, archive data.CheckpointArchive, observedMS int64) (DeleteCheckpointReceipt, error) {
	var zero DeleteCheckpointReceipt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyDeleteCheckpointSettlement(intent.raw, intent.signature, importAuthorityFrom(deployment), now)
	if err != nil || verified.Command() != intent.full {
		return zero, ErrFenceReconciliation
	}
	c := intent.full
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil || row == nil || !sameMigrateCommand(row, c.CommandHeader) || row.state != "unknown" || row.receipt != intent.markerRaw {
		return zero, fmt.Errorf("delete-checkpoint settlement state: %w", ErrFenceReconciliation)
	}
	receipt := deleteCheckpointReceiptFor(intent, DeleteOutcomeRemoved, archive.Found, observedMS, now)
	if !validDeleteCheckpointReceipt(receipt) || !DeleteCheckpointReceiptMatchesCommand(receipt, c) {
		return zero, fmt.Errorf("delete-checkpoint settlement receipt: %w", ErrFenceReconciliation)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, ErrFenceReconciliation
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`,
		string(encoded), now, c.EffectID, intent.markerRaw)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf("delete-checkpoint settlement rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
	}
	// An earlier claim of the same checkpoint that never reached a verdict is finished by this removal: the archive
	// it was meant to remove is gone now, so it settles with the same observation.
	if err := settleEarlierDeleteCheckpointClaims(ctx, tx, deployment, c, archive, receipt, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

// settleEarlierDeleteCheckpointClaims settles every other unknown claim of the checkpoint the command names. The
// gate is held, so none of them is running. Their receipts are built from their own claim markers and rows.
func settleEarlierDeleteCheckpointClaims(ctx context.Context, tx *sql.Tx, deployment Deployment, c control.DeleteCheckpointCommand, archive data.CheckpointArchive, own DeleteCheckpointReceipt, now int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT effect_id FROM runner_commands WHERE kind='delete-checkpoint' AND state='unknown' AND effect_id<>? AND json_extract(receipt_json,'$.checkpoint_id')=?`,
		c.EffectID, c.Payload.CheckpointID)
	if err != nil {
		return err
	}
	var earlier []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		earlier = append(earlier, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range earlier {
		row, err := loadImportRow(ctx, tx, id)
		if err != nil || row == nil {
			return fmt.Errorf("earlier delete-checkpoint claim: %w", ErrFenceReconciliation)
		}
		var claim deleteCheckpointClaim
		if !canonicalDataJSON(row.receipt, &claim) || !validDeleteCheckpointClaim(claim) || claim.CheckpointID != c.Payload.CheckpointID {
			return fmt.Errorf("earlier delete-checkpoint claim marker: %w", ErrFenceReconciliation)
		}
		claimed := mustDataInt(claim.ClaimedMS)
		observed := maxInt64(mustDataInt(own.ObservedMS), claimed)
		receipt := DeleteCheckpointReceipt{
			Format: deleteCheckpointReceiptFormat, Claim: claim, DeploymentID: deployment.DeploymentID, ControllerEpoch: row.epoch,
			InstallationID: row.installation, OperationID: row.operation, TargetGeneration: strconv.FormatInt(row.generation, 10), Stage: row.stage,
			EffectID: id, PayloadSHA256: row.payload, RecoverySetID: claim.RecoverySetID, CheckpointID: claim.CheckpointID, NamespaceID: claim.NamespaceID,
			ManifestSHA256: claim.ManifestSHA256, ArchiveBytes: claim.ArchiveBytes, Outcome: DeleteOutcomeRemoved, ArchiveFound: archive.Found,
			ObservedMS: strconv.FormatInt(observed, 10), SettledMS: strconv.FormatInt(maxInt64(now, observed), 10),
		}
		encoded, err := json.Marshal(receipt)
		if err != nil || len(encoded) > 16384 || !validDeleteCheckpointReceipt(receipt) {
			return fmt.Errorf("earlier delete-checkpoint receipt: %w", ErrFenceReconciliation)
		}
		changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`,
			string(encoded), now, id, row.receipt)
		if err != nil {
			return err
		}
		if n, err := changed.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("earlier delete-checkpoint settlement rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
		}
	}
	return nil
}

// deleteCheckpointTimeout bounds the native removal of one claimed deletion: a stat, an unlink and a directory sync.
const deleteCheckpointTimeout = 5 * time.Minute

// DispatchDeleteCheckpoint is the trusted orchestrator for one signed deletion. Applied or reverted work replays its
// receipt without touching the archive. A deletion left unknown by an earlier attempt is finished by repeating the
// removal, which is safe at every point: the claim already marked the checkpoint deleted, an archive already gone
// settles as removed, and an archive that is not the claimed file is never touched and leaves the effect unknown.
// A claimed deletion runs detached from the connection, to a definite end.
func (s *Store) DispatchDeleteCheckpoint(ctx context.Context, raw, signature []byte, native DeleteCheckpointNative) (any, error) {
	if native == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return nil, ErrDeleteCheckpointUnavailable
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	if err := s.acquireDataExecution(ctx); err != nil {
		return nil, err
	}
	defer s.releaseDataExecution()
	state, command, row, err := s.deleteCheckpointState(ctx, raw, signature)
	if err != nil {
		return nil, fmt.Errorf("delete-checkpoint state: %w", err)
	}
	if state == "applied" || state == "reverted" {
		return s.readDeleteCheckpointReceiptHeld(ctx, raw, signature)
	}
	var intent deleteCheckpointIntent
	if state == "unknown" {
		if intent, err = deleteCheckpointIntentFromRow(row, command, raw, signature); err != nil {
			return nil, err
		}
	} else {
		if intent, err = s.beginDeleteCheckpoint(ctx, raw, signature, native); err != nil {
			return nil, fmt.Errorf("begin delete-checkpoint: %w", err)
		}
		if intent.settled != nil {
			return *intent.settled, nil
		}
	}
	// The claim is committed: the deletion runs to a definite end whatever happens to the connection.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deleteCheckpointTimeout)
	defer cancel()
	archive, err := native.RemoveCheckpointArchive(ctx, intent.full.Payload.CheckpointID, mustDataInt(intent.full.Payload.ArchiveBytes), intent.full.Payload.ManifestSHA256)
	if err != nil {
		// Nothing is settled: the effect stays unknown and the next attempt finishes it. A file that is not the claimed
		// archive is never touched.
		if errors.Is(err, data.ErrArchiveMismatch) {
			err = fmt.Errorf("%w: %v", ErrFenceReconciliation, err)
		}
		log.Printf("delete-checkpoint %s: native removal: %v", intent.full.EffectID, err)
		return nil, fmt.Errorf("remove checkpoint archive: %w", err)
	}
	receipt, err := s.settleDeleteCheckpoint(ctx, intent, archive, time.Now().UTC().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("settle delete-checkpoint: %w", err)
	}
	return receipt, nil
}

func (s *Store) readDeleteCheckpointReceiptHeld(ctx context.Context, raw, signature []byte) (DeleteCheckpointReceipt, error) {
	got, err := s.readDeleteCheckpoint(ctx, raw, signature, control.VerifyDeleteCheckpointSettlement)
	if err != nil {
		return DeleteCheckpointReceipt{}, err
	}
	if got.receipt == nil {
		return DeleteCheckpointReceipt{}, ErrDeleteCheckpointUnavailable
	}
	return *got.receipt, nil
}

type deleteCheckpointReadResult struct {
	receipt *DeleteCheckpointReceipt
	command control.DeleteCheckpointCommand
	missing bool
	unknown bool
}

type deleteCheckpointVerifier func(raw, signature []byte, authority control.Authority, nowMS int64) (control.VerifiedDeleteCheckpointCommand, error)

func (s *Store) readDeleteCheckpoint(ctx context.Context, raw, signature []byte, verify deleteCheckpointVerifier) (deleteCheckpointReadResult, error) {
	var zero deleteCheckpointReadResult
	if s.closing.Load() {
		return zero, ErrDeleteCheckpointUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	verified, err := verify(raw, signature, importAuthorityFrom(deployment), time.Now().UTC().UnixMilli())
	if err != nil {
		return zero, err
	}
	c := verified.Command()
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return zero, err
	}
	if row == nil {
		var siblings int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=? OR (operation_id=? AND stage=? AND effect_number=?)`,
			c.EffectID, c.OperationID, c.Stage, mustDataInt(c.EffectNumber)).Scan(&siblings); err != nil {
			return zero, err
		}
		if siblings != 0 {
			return zero, ErrCommandConflict
		}
		return deleteCheckpointReadResult{command: c, missing: true}, nil
	}
	if !sameMigrateCommand(row, c.CommandHeader) {
		return zero, ErrCommandConflict
	}
	if row.state == "unknown" {
		var claim deleteCheckpointClaim
		if !canonicalDataJSON(row.receipt, &claim) || !validDeleteCheckpointClaim(claim) || !deleteCheckpointClaimMatchesCommand(claim, c) {
			return zero, ErrFenceReconciliation
		}
		return deleteCheckpointReadResult{command: c, unknown: true}, nil
	}
	if row.state != "applied" && row.state != "reverted" {
		return zero, ErrDeleteCheckpointUnavailable
	}
	receipt, err := ParseDeleteCheckpointReceipt([]byte(row.receipt))
	if err != nil || !DeleteCheckpointReceiptMatchesCommand(receipt, c) || (row.state == "applied") != receipt.Applied() {
		return zero, ErrFenceReconciliation
	}
	return deleteCheckpointReadResult{command: c, receipt: &receipt}, nil
}

// ReadDeleteCheckpointOrStatus is the runner's read-back answer: the receipt (applied or reverted), or a status. A
// status never waits for a running deletion: "pending" while the data gate is held, "orphaned" for an unknown effect
// with the gate free, and "absent" only for no row at all, proven with the gate held by this read. The command is
// verified under the read purpose and is not bound by the signed window. A read never writes a row.
func (s *Store) ReadDeleteCheckpointOrStatus(ctx context.Context, raw, signature []byte) (any, error) {
	got, err := s.readDeleteCheckpoint(ctx, raw, signature, control.VerifyDeleteCheckpointRead)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().UnixMilli()
	if got.receipt != nil {
		return *got.receipt, nil
	}
	if !s.tryAcquireDataExecution() {
		return deleteCheckpointStatusFor(got.command, "pending", now), nil
	}
	defer s.releaseDataExecution()
	// The deletion that held the gate may have settled the effect since the first look.
	again, err := s.readDeleteCheckpoint(ctx, raw, signature, control.VerifyDeleteCheckpointRead)
	if err != nil {
		return nil, err
	}
	switch {
	case again.receipt != nil:
		return *again.receipt, nil
	case again.missing:
		return deleteCheckpointStatusFor(again.command, "absent", time.Now().UTC().UnixMilli()), nil
	}
	return deleteCheckpointStatusFor(again.command, "orphaned", time.Now().UTC().UnixMilli()), nil
}
