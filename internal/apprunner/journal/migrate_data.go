package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

var (
	ErrMigrateDataUnavailable = errors.New("runner data migration is unavailable")
	// ErrMigrationUnknown refuses to run a migration whose earlier attempt is unsettled. Such a row is
	// settled only by a discard-migration effect; it is never run a second time.
	ErrMigrationUnknown = errors.New("runner data migration is unknown and is never rerun")
)

const migrateClaimFormat = "portal.runner.migrate-data-claim/1-dev.1"
const migrateReceiptFormat = "portal.runner.migrate-data-receipt/1-dev.1"
const migrateStatusFormat = "portal.runner.migration-status/1-dev.1"

// MigrationNative is the native side of a migration: the job runner and the data read. Both
// return plain snapshots so the journal binds every field itself.
type MigrationNative interface {
	RunMigrationJob(context.Context, driver.MigrationJobRequest) (driver.MigrationJobSnapshot, error)
	ProveMigrationAbsent(context.Context, driver.MigrationJobRequest) error
	ReadSchemaMarker(context.Context, data.RootIdentity, string, uint32) (data.SchemaMarkerSnapshot, error)
}

type nativeMigration struct {
	runtime *driver.CandidateRuntime
	root    *data.Root
}

// NewMigrationNative adapts the concrete runtime and data root.
func NewMigrationNative(runtime *driver.CandidateRuntime, root *data.Root) MigrationNative {
	return nativeMigration{runtime: runtime, root: root}
}

func (n nativeMigration) RunMigrationJob(ctx context.Context, r driver.MigrationJobRequest) (driver.MigrationJobSnapshot, error) {
	if n.runtime == nil {
		return driver.MigrationJobSnapshot{}, ErrMigrateDataUnavailable
	}
	observation, err := n.runtime.RunMigrationJob(ctx, r)
	if err != nil {
		return driver.MigrationJobSnapshot{}, err
	}
	return observation.Snapshot()
}
func (n nativeMigration) ProveMigrationAbsent(ctx context.Context, r driver.MigrationJobRequest) error {
	if n.runtime == nil {
		return ErrMigrateDataUnavailable
	}
	return n.runtime.ProveMigrationAbsent(ctx, r)
}
func (n nativeMigration) ReadSchemaMarker(ctx context.Context, pin data.RootIdentity, id string, project uint32) (data.SchemaMarkerSnapshot, error) {
	if n.root == nil {
		return data.SchemaMarkerSnapshot{}, ErrMigrateDataUnavailable
	}
	observation, err := n.root.ReadSchemaMarker(ctx, pin, id, project)
	if err != nil {
		return data.SchemaMarkerSnapshot{}, err
	}
	return observation.Snapshot()
}

// migrateClaimMarker is the durable record of one uncertain migration attempt. It is committed with
// the job resource and the unknown state before any native call, and carries everything a receipt
// needs, so a discard can describe the effect without the original command.
type migrateClaimMarker struct {
	Format                  string `json:"format"`
	ClaimID                 string `json:"claim_id"`
	EffectID                string `json:"effect_id"`
	ResourceID              string `json:"resource_id"`
	OwnershipSHA256         string `json:"ownership_sha256"`
	AdmittedFence           string `json:"admitted_fence"`
	ClaimedMS               string `json:"claimed_ms"`
	NamespaceID             string `json:"namespace_id"`
	CheckpointID            string `json:"checkpoint_id"`
	CheckpointReceiptSHA256 string `json:"checkpoint_receipt_sha256"`
	RemoveCodeReceiptSHA256 string `json:"remove_code_receipt_sha256"`
	SourceReleaseSHA256     string `json:"source_release_sha256"`
	TargetReleaseSHA256     string `json:"target_release_sha256"`
	SourceSchema            string `json:"source_schema"`
	TargetSchema            string `json:"target_schema"`
	CheckpointWriterFence   string `json:"checkpoint_writer_fence"`
	WriterFence             string `json:"writer_fence"`
	StartWriterFence        string `json:"start_writer_fence"`
	ProjectID               string `json:"project_id"`
	QuotaBytes              string `json:"quota_bytes"`
	QuotaInodes             string `json:"quota_inodes"`
}

// MigrateDataClaim is the claim a receipt carries.
type MigrateDataClaim = migrateClaimMarker

// MigrateDataReceipt is a historical observation of one migration effect. State is "migrated" for an
// applied effect, "failed" for a known failure and "discarded" for an effect a discard-migration
// settled; the last two are reverted effects. It is not readiness or a Portal stage completion.
type MigrateDataReceipt struct {
	Format           string           `json:"format"`
	Claim            MigrateDataClaim `json:"claim"`
	DeploymentID     string           `json:"deployment_id"`
	ControllerEpoch  string           `json:"controller_epoch"`
	InstallationID   string           `json:"installation_id"`
	OperationID      string           `json:"operation_id"`
	TargetGeneration string           `json:"target_generation"`
	Stage            string           `json:"stage"`
	EffectID         string           `json:"effect_id"`
	PayloadSHA256    string           `json:"payload_sha256"`
	ExitCode         string           `json:"exit_code"`
	MarkerSchema     string           `json:"marker_schema"`
	UsedBytes        string           `json:"used_bytes"`
	UsedInodes       string           `json:"used_inodes"`
	State            string           `json:"state"`
	Outcome          string           `json:"outcome"`
	ObservedMS       string           `json:"observed_ms"`
	SettledMS        string           `json:"settled_ms"`
}

var migrateOutcomes = map[string]bool{
	"": true, "exit_nonzero": true, "wall_clock": true, "marker_missing": true,
	"marker_mismatch": true, "marker_invalid": true, "over_quota": true, "discarded": true,
}

func validMigrateMarker(m migrateClaimMarker) bool {
	if m.Format != migrateClaimFormat || !dataSHA(m.OwnershipSHA256) || !dataNumber(m.AdmittedFence, true) || !dataNumber(m.ClaimedMS, false) ||
		!dataSHA(m.CheckpointReceiptSHA256) || !dataSHA(m.RemoveCodeReceiptSHA256) || !dataSHA(m.SourceReleaseSHA256) || !dataSHA(m.TargetReleaseSHA256) {
		return false
	}
	for _, id := range []string{m.ClaimID, m.EffectID, m.ResourceID, m.NamespaceID, m.CheckpointID} {
		if !importID(id) {
			return false
		}
	}
	for _, value := range []string{m.SourceSchema, m.TargetSchema, m.CheckpointWriterFence, m.WriterFence, m.StartWriterFence, m.ProjectID, m.QuotaBytes, m.QuotaInodes} {
		if !dataNumber(value, true) {
			return false
		}
	}
	source, _ := strconv.ParseInt(m.SourceSchema, 10, 64)
	target, _ := strconv.ParseInt(m.TargetSchema, 10, 64)
	checkpoint, _ := strconv.ParseInt(m.CheckpointWriterFence, 10, 64)
	writer, _ := strconv.ParseInt(m.WriterFence, 10, 64)
	start, _ := strconv.ParseInt(m.StartWriterFence, 10, 64)
	return source < target && checkpoint < writer && writer < start && m.ResourceID != m.NamespaceID
}

func validMigrateReceipt(r MigrateDataReceipt) bool {
	if r.Format != migrateReceiptFormat || r.Stage != "migrate" || !validMigrateMarker(r.Claim) || !dataSHA(r.PayloadSHA256) ||
		!migrateOutcomes[r.Outcome] {
		return false
	}
	switch r.State {
	case "migrated":
		if r.Outcome != "" {
			return false
		}
	case "failed":
		if r.Outcome == "" || r.Outcome == "discarded" {
			return false
		}
	case "discarded":
		if r.Outcome != "discarded" {
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
	for _, value := range []string{r.ExitCode, r.MarkerSchema, r.UsedBytes, r.UsedInodes} {
		if !dataNumber(value, false) {
			return false
		}
	}
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	return r.Claim.EffectID == r.EffectID && observed >= claimed && settled >= observed
}

func ParseMigrateDataReceipt(raw []byte) (MigrateDataReceipt, error) {
	var receipt MigrateDataReceipt
	if len(raw) == 0 || len(raw) > 16384 || !canonicalDataJSON(string(raw), &receipt) || !validMigrateReceipt(receipt) {
		return MigrateDataReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

func migrateMarkerMatchesCommand(m migrateClaimMarker, c control.MigrateDataCommand) bool {
	p := c.Payload
	return m.EffectID == c.EffectID && m.ResourceID == p.ResourceID && m.NamespaceID == p.NamespaceID && m.CheckpointID == p.CheckpointID &&
		m.CheckpointReceiptSHA256 == p.CheckpointReceiptSHA256 && m.RemoveCodeReceiptSHA256 == p.RemoveCodeReceiptSHA256 &&
		m.SourceReleaseSHA256 == p.SourceReleaseSHA256 && m.TargetReleaseSHA256 == p.TargetReleaseSHA256 &&
		m.SourceSchema == p.SourceSchema && m.TargetSchema == p.TargetSchema && m.CheckpointWriterFence == p.CheckpointWriterFence &&
		m.WriterFence == p.WriterFence && m.StartWriterFence == p.StartWriterFence && m.ProjectID == p.ProjectID &&
		m.QuotaBytes == p.QuotaBytes && m.QuotaInodes == p.QuotaInodes && m.AdmittedFence == c.InstallationFence
}

// MigrateDataReceiptMatchesCommand reports whether the receipt answers exactly this signed command.
func MigrateDataReceiptMatchesCommand(r MigrateDataReceipt, c control.MigrateDataCommand) bool {
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch && r.InstallationID == c.InstallationID &&
		r.OperationID == c.OperationID && r.TargetGeneration == c.TargetGeneration && r.Stage == c.Stage &&
		r.EffectID == c.EffectID && r.PayloadSHA256 == c.PayloadSHA256 && migrateMarkerMatchesCommand(r.Claim, c)
}

// MigrateDataStatus is the runner's typed answer to a read-back that has no receipt to return:
// "pending" while the data execution gate is held, "orphaned" for an unknown effect nothing is running,
// and "absent" for an effect the journal proves it never claimed (only with the gate free). Kind names
// the command it answers: migrate-data or discard-migration.
type MigrateDataStatus struct {
	Format           string `json:"format"`
	Kind             string `json:"kind"`
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

func validMigrateStatus(s MigrateDataStatus) bool {
	if s.Format != migrateStatusFormat || s.Stage != "migrate" || !dataSHA(s.PayloadSHA256) ||
		(s.Kind != "migrate-data" && s.Kind != "discard-migration") ||
		(s.State != "pending" && s.State != "orphaned" && s.State != "absent") ||
		!dataNumber(s.TargetGeneration, true) || !dataNumber(s.EffectNumber, true) || !dataNumber(s.ObservedMS, true) {
		return false
	}
	for _, id := range []string{s.DeploymentID, s.ControllerEpoch, s.InstallationID, s.OperationID, s.EffectID} {
		if !importID(id) {
			return false
		}
	}
	return true
}

// ParseMigrateDataStatus accepts only the canonical encoding of a valid status reply.
func ParseMigrateDataStatus(raw []byte) (MigrateDataStatus, error) {
	var status MigrateDataStatus
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &status) || !validMigrateStatus(status) {
		return MigrateDataStatus{}, ErrFenceReconciliation
	}
	return status, nil
}

func migrateStatusFor(h control.CommandHeader, state string, now int64) MigrateDataStatus {
	return MigrateDataStatus{
		Format: migrateStatusFormat, Kind: h.Kind, DeploymentID: h.DeploymentID, ControllerEpoch: h.ControllerEpoch,
		InstallationID: h.InstallationID, OperationID: h.OperationID, TargetGeneration: h.TargetGeneration, Stage: h.Stage,
		EffectNumber: h.EffectNumber, EffectID: h.EffectID, PayloadSHA256: h.PayloadSHA256, State: state,
		ObservedMS: strconv.FormatInt(now, 10),
	}
}

func migrateStatusMatchesHeader(s MigrateDataStatus, h control.CommandHeader) bool {
	return s.Kind == h.Kind && s.DeploymentID == h.DeploymentID && s.ControllerEpoch == h.ControllerEpoch &&
		s.InstallationID == h.InstallationID && s.OperationID == h.OperationID && s.TargetGeneration == h.TargetGeneration &&
		s.Stage == h.Stage && s.EffectNumber == h.EffectNumber && s.EffectID == h.EffectID && s.PayloadSHA256 == h.PayloadSHA256
}

// MigrateDataStatusMatchesCommand reports whether the status answers exactly this signed command.
func MigrateDataStatusMatchesCommand(s MigrateDataStatus, c control.MigrateDataCommand) bool {
	return migrateStatusMatchesHeader(s, c.CommandHeader)
}

// DiscardMigrationStatusMatchesCommand reports whether the status answers exactly this signed command.
func DiscardMigrationStatusMatchesCommand(s MigrateDataStatus, c control.DiscardMigrationCommand) bool {
	return migrateStatusMatchesHeader(s, c.CommandHeader)
}

func migrateOwnership(c control.MigrateDataCommand) string {
	claim := struct {
		Format, Deployment, Epoch, Revision, Installation, Operation, Namespace, Resource string
		Effect, Payload, Generation, Fence, Source, Target, Checkpoint, RemoveCode        string
		SourceSchema, TargetSchema, CheckpointWriter, Writer, StartWriter                 string
	}{
		"portal.runner.migrate-ownership/1-dev.1", c.DeploymentID, c.ControllerEpoch, c.ControlRevision, c.InstallationID,
		c.OperationID, c.Payload.NamespaceID, c.Payload.ResourceID, c.EffectID, c.PayloadSHA256, c.TargetGeneration,
		c.InstallationFence, c.Payload.SourceReleaseSHA256, c.Payload.TargetReleaseSHA256, c.Payload.CheckpointReceiptSHA256,
		c.Payload.RemoveCodeReceiptSHA256, c.Payload.SourceSchema, c.Payload.TargetSchema, c.Payload.CheckpointWriterFence,
		c.Payload.WriterFence, c.Payload.StartWriterFence,
	}
	raw, _ := json.Marshal(claim)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func migrateAuthority(deployment Deployment) control.Authority {
	return importAuthorityFrom(deployment)
}

func sameMigrateCommand(r *importRow, c control.CommandHeader) bool {
	slot, _ := strconv.ParseInt(c.EffectNumber, 10, 64)
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	return r.operation == c.OperationID && r.installation == c.InstallationID && r.stage == c.Stage && r.kind == c.Kind &&
		r.payload == c.PayloadSHA256 && r.epoch == c.ControllerEpoch && r.slot == slot && r.generation == generation
}

type migrateIntent struct {
	full           control.MigrateDataCommand
	raw, signature []byte
	marker         migrateClaimMarker
	markerRaw      string
}

// migrateJobNativeID is the placeholder identity of the job resource. The job's real container id is
// never stored: it is found by the name and labels derived from the resource id.
func migrateJobNativeID(resource string) string { return "migration-job/" + resource }

// requireMigrationEvidence checks the applied source removal and the verified checkpoint the
// migration follows, against the payload, inside the admission transaction.
func requireMigrationEvidence(ctx context.Context, tx *sql.Tx, c control.MigrateDataCommand) error {
	p := c.Payload
	removal, err := loadImportRow(ctx, tx, p.RemoveCodeEffectID)
	if err != nil || removal == nil || removal.state != "applied" || removal.kind != "remove-code" ||
		removal.installation != c.InstallationID || removal.operation != c.OperationID ||
		candidateHash([]byte(removal.receipt)) != p.RemoveCodeReceiptSHA256 {
		return fmt.Errorf("%w: migration source removal", ErrFenceReconciliation)
	}
	removed, err := ParseManagedRemovalReceipt([]byte(removal.receipt))
	if err != nil || removed.EffectID != p.RemoveCodeEffectID || removed.Stage != "remove_code" || removed.State != "absent" ||
		removed.InstallationID != c.InstallationID || removed.DataNamespaceID != p.NamespaceID ||
		removed.ReleaseSHA256 != p.SourceReleaseSHA256 || removed.DeploymentID != c.DeploymentID {
		return fmt.Errorf("%w: migration source removal receipt", ErrFenceReconciliation)
	}
	var observed, desired string
	if err := tx.QueryRowContext(ctx, `SELECT desired_state,observed_state FROM runner_resources WHERE id=? AND data_namespace_id=? AND kind='container'`,
		removed.ResourceID, p.NamespaceID).Scan(&desired, &observed); err != nil || observed != "absent" || desired != "absent" {
		return fmt.Errorf("%w: migration source container not absent", ErrFenceReconciliation)
	}
	checkpoint, err := loadImportRow(ctx, tx, p.CheckpointEffectID)
	if err != nil || checkpoint == nil || checkpoint.state != "applied" || checkpoint.kind != "snapshot" || checkpoint.stage != "snapshot" ||
		checkpoint.installation != c.InstallationID || checkpoint.operation != c.OperationID ||
		candidateHash([]byte(checkpoint.receipt)) != p.CheckpointReceiptSHA256 {
		return fmt.Errorf("%w: migration checkpoint", ErrFenceReconciliation)
	}
	receipt, err := ParseCheckpointReceipt([]byte(checkpoint.receipt))
	if err != nil || receipt.CheckpointID != p.CheckpointID || receipt.NamespaceID != p.NamespaceID ||
		receipt.ReleaseSHA256 != p.SourceReleaseSHA256 || receipt.ProjectID != p.ProjectID || receipt.NamespaceInode != p.NamespaceInode ||
		receipt.Device != p.Device || receipt.DataFormat != p.SourceSchema || receipt.WriterFence != p.CheckpointWriterFence ||
		receipt.InstallationID != c.InstallationID || receipt.TargetGeneration != c.TargetGeneration {
		return fmt.Errorf("%w: migration checkpoint receipt", ErrFenceReconciliation)
	}
	var state string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT state,revision FROM runner_checkpoints WHERE id=? AND data_namespace_id=? AND installation_id=? AND operation_id=?`,
		p.CheckpointID, p.NamespaceID, c.InstallationID, c.OperationID).Scan(&state, &revision); err != nil ||
		state != "verified" || (revision != 2 && revision != 4) {
		return fmt.Errorf("%w: migration checkpoint record", ErrFenceReconciliation)
	}
	return nil
}

// beginMigrateData verifies the signed command and, in one transaction, checks every precondition,
// reserves the job resource and commits the effect as unknown. A lost commit acknowledgement leaves
// unknown work and never authorizes a native call. The caller holds the data execution gate.
func (s *Store) beginMigrateData(ctx context.Context, raw, signature []byte) (migrateIntent, bool, error) {
	var zero migrateIntent
	if s.closing.Load() {
		return zero, false, ErrMigrateDataUnavailable
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
	authority := migrateAuthority(deployment)
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyMigrateData(raw, signature, authority, now)
	if err != nil {
		return zero, false, err
	}
	c := verified.Command()
	p := c.Payload
	existing, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return zero, false, err
	}
	if existing != nil {
		if !sameMigrateCommand(existing, c.CommandHeader) {
			return zero, false, ErrCommandConflict
		}
		switch existing.state {
		case "applied", "reverted":
			parsed, err := ParseMigrateDataReceipt([]byte(existing.receipt))
			if err != nil || !MigrateDataReceiptMatchesCommand(parsed, c) {
				return zero, false, ErrFenceReconciliation
			}
			return zero, true, nil
		}
		return zero, false, ErrMigrationUnknown
	}
	if err := requireMigrationEvidence(ctx, tx, c); err != nil {
		return zero, false, err
	}
	var siblings int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE operation_id=? AND (kind='migrate-data' OR (stage=? AND effect_number=?))`,
		c.OperationID, c.Stage, mustDataInt(c.EffectNumber)).Scan(&siblings); err != nil {
		return zero, false, err
	}
	if siblings != 0 {
		return zero, false, fmt.Errorf("%w: one migration per operation", ErrCommandConflict)
	}
	var resources int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources WHERE id=? OR native_id=?`, p.ResourceID, migrateJobNativeID(p.ResourceID)).Scan(&resources); err != nil {
		return zero, false, err
	}
	if resources != 0 {
		return zero, false, ErrDataReservationConflict
	}
	var live int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources WHERE data_namespace_id=? AND kind='container' AND observed_state NOT IN ('stopped','absent')`,
		p.NamespaceID).Scan(&live); err != nil || live != 0 {
		return zero, false, fmt.Errorf("%w: namespace container is live", ErrFenceReconciliation)
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND state IN ('prepared','unknown')`, c.InstallationID).Scan(&pending); err != nil || pending != 0 {
		return zero, false, fmt.Errorf("%w: installation has pending commands", ErrFenceReconciliation)
	}
	var foreign int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND controller_epoch<>?)+(SELECT COUNT(*) FROM runner_resources WHERE (installation_id=? OR data_namespace_id=?) AND controller_epoch<>?)`,
		c.InstallationID, c.ControllerEpoch, c.InstallationID, p.NamespaceID, c.ControllerEpoch).Scan(&foreign); err != nil || foreign != 0 {
		return zero, false, ErrFenceReconciliation
	}
	var fenceFloor, generationFloor, writerFloor int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(fence),0) FROM (SELECT admitted_fence AS fence FROM runner_commands WHERE installation_id=? UNION ALL SELECT control_fence AS fence FROM runner_resources WHERE installation_id=?)`, c.InstallationID, c.InstallationID).Scan(&fenceFloor); err != nil {
		return zero, false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0) FROM (SELECT target_generation AS generation FROM runner_commands WHERE installation_id=? UNION ALL SELECT generation FROM runner_resources WHERE installation_id=?)`, c.InstallationID, c.InstallationID).Scan(&generationFloor); err != nil {
		return zero, false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(writer_fence),0) FROM runner_resources WHERE data_namespace_id=? AND NOT (desired_state='absent' AND observed_state='absent')`, p.NamespaceID).Scan(&writerFloor); err != nil {
		return zero, false, err
	}
	fence := mustDataInt(c.InstallationFence)
	generation := mustDataInt(c.TargetGeneration)
	writer := mustDataInt(p.WriterFence)
	if fence < fenceFloor || generation < generationFloor || writer <= writerFloor {
		return zero, false, fmt.Errorf("%w: migration fence floors", ErrCommandConflict)
	}
	claimID, err := dataClaimID()
	if err != nil {
		return zero, false, err
	}
	ownership := migrateOwnership(c)
	marker := migrateClaimMarker{
		Format: migrateClaimFormat, ClaimID: claimID, EffectID: c.EffectID, ResourceID: p.ResourceID, OwnershipSHA256: ownership,
		AdmittedFence: c.InstallationFence, ClaimedMS: strconv.FormatInt(now, 10), NamespaceID: p.NamespaceID, CheckpointID: p.CheckpointID,
		CheckpointReceiptSHA256: p.CheckpointReceiptSHA256, RemoveCodeReceiptSHA256: p.RemoveCodeReceiptSHA256,
		SourceReleaseSHA256: p.SourceReleaseSHA256, TargetReleaseSHA256: p.TargetReleaseSHA256,
		SourceSchema: p.SourceSchema, TargetSchema: p.TargetSchema, CheckpointWriterFence: p.CheckpointWriterFence,
		WriterFence: p.WriterFence, StartWriterFence: p.StartWriterFence, ProjectID: p.ProjectID,
		QuotaBytes: p.QuotaBytes, QuotaInodes: p.QuotaInodes,
	}
	markerRaw, err := json.Marshal(marker)
	if err != nil || len(markerRaw) > 16384 || !validMigrateMarker(marker) || !migrateMarkerMatchesCommand(marker, c) {
		return zero, false, ErrMigrateDataUnavailable
	}
	slot := mustDataInt(c.EffectNumber)
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'container',?,?,?,?,?,?,'present','unknown',?,1,NULL,?,?)`,
		p.ResourceID, c.InstallationID, p.NamespaceID, migrateJobNativeID(p.ResourceID), p.TargetReleaseSHA256, generation,
		c.ControllerEpoch, fence, writer, ownership, now, now); err != nil {
		return zero, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,'unknown',?,NULL,?,?)`,
		c.EffectID, c.OperationID, c.InstallationID, c.Stage, slot, c.Kind, c.PayloadSHA256, generation, c.ControllerEpoch, fence,
		string(markerRaw), now, now); err != nil {
		return zero, false, err
	}
	if err := ctx.Err(); err != nil {
		return zero, false, err
	}
	if _, err := control.VerifyMigrateData(raw, signature, authority, time.Now().UTC().UnixMilli()); err != nil {
		return zero, false, fmt.Errorf("migration claim command: %w", err)
	}
	if s.closing.Load() {
		return zero, false, ErrMigrateDataUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, false, err
	}
	return migrateIntent{full: c, raw: bytes.Clone(raw), signature: bytes.Clone(signature), marker: marker, markerRaw: string(markerRaw)}, false, nil
}

// migrateObservation is the definite end of a job, before it is recorded.
type migrateObservation struct {
	exit                  int
	markerSchema          int
	usedBytes, usedInodes int64
	observedMS            int64
	state, outcome        string
}

// settleMigrateData records a definite end in one transaction: the receipt on the command row (applied
// or reverted) and the job resource absent. The container was already proven absent.
func (s *Store) settleMigrateData(ctx context.Context, intent migrateIntent, o migrateObservation) (MigrateDataReceipt, error) {
	var zero MigrateDataReceipt
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
	verified, err := control.VerifyMigrateDataSettlement(intent.raw, intent.signature, migrateAuthority(deployment), now)
	if err != nil || verified.Command() != intent.full {
		return zero, ErrFenceReconciliation
	}
	c := intent.full
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil || row == nil || !sameMigrateCommand(row, c.CommandHeader) || row.state != "unknown" || row.receipt != intent.markerRaw {
		return zero, fmt.Errorf("migration settlement state: %w", ErrFenceReconciliation)
	}
	if err := migrateResourceMatches(ctx, tx, c, intent.marker); err != nil {
		return zero, err
	}
	receipt := MigrateDataReceipt{
		Format: migrateReceiptFormat, Claim: intent.marker, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage,
		EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, ExitCode: strconv.Itoa(o.exit),
		MarkerSchema: strconv.Itoa(o.markerSchema), UsedBytes: strconv.FormatInt(o.usedBytes, 10),
		UsedInodes: strconv.FormatInt(o.usedInodes, 10), State: o.state, Outcome: o.outcome,
		ObservedMS: strconv.FormatInt(maxInt64(o.observedMS, mustDataInt(intent.marker.ClaimedMS)), 10), SettledMS: strconv.FormatInt(now, 10),
	}
	if !validMigrateReceipt(receipt) || !MigrateDataReceiptMatchesCommand(receipt, c) {
		return zero, fmt.Errorf("migration settlement receipt: %w", ErrFenceReconciliation)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, ErrFenceReconciliation
	}
	commandState := "reverted"
	if o.state == "migrated" {
		commandState = "applied"
	}
	if err := settleMigrateRows(ctx, tx, intent.marker, intent.markerRaw, commandState, string(encoded), now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// settleMigrateRows moves the unknown command row to its final state and the unknown job resource to
// absent, as one conditional pair.
func settleMigrateRows(ctx context.Context, tx *sql.Tx, marker migrateClaimMarker, markerRaw, commandState, receipt string, now int64) error {
	changed, err := tx.ExecContext(ctx, `UPDATE runner_resources SET desired_state='absent',observed_state='absent',revision=2,observed_ms=?,updated_ms=? WHERE id=? AND observed_state='unknown' AND revision=1 AND observed_ms IS NULL AND ownership_sha256=?`,
		now, now, marker.ResourceID, marker.OwnershipSHA256)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("migration settlement resource rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
	}
	changed, err = tx.ExecContext(ctx, `UPDATE runner_commands SET state=?,receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`,
		commandState, receipt, now, marker.EffectID, markerRaw)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("migration settlement command rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
	}
	return nil
}

// migrateResourceMatches checks the reserved job resource against the claim: unknown at revision one.
func migrateResourceMatches(ctx context.Context, tx *sql.Tx, c control.MigrateDataCommand, marker migrateClaimMarker) error {
	var installation, namespace, kind, native, epoch, desired, observed, ownership, release string
	var generation, fence, writer, revision int64
	var observedAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms FROM runner_resources WHERE id=?`,
		marker.ResourceID).Scan(&installation, &namespace, &kind, &native, &release, &generation, &epoch, &fence, &writer, &desired, &observed, &ownership, &revision, &observedAt); err != nil {
		return ErrFenceReconciliation
	}
	if installation != c.InstallationID || namespace != c.Payload.NamespaceID || kind != "container" || native != migrateJobNativeID(marker.ResourceID) ||
		release != c.Payload.TargetReleaseSHA256 || generation != mustDataInt(c.TargetGeneration) || epoch != c.ControllerEpoch ||
		fence != mustDataInt(c.InstallationFence) || writer != mustDataInt(c.Payload.WriterFence) || desired != "present" ||
		observed != "unknown" || ownership != marker.OwnershipSHA256 || revision != 1 || observedAt.Valid {
		return ErrFenceReconciliation
	}
	return nil
}

// loadUnknownMigrate rebuilds the intent of a migration left unknown, from the journal alone. It
// writes nothing. Expiry governs dispatch, not settlement.
func (s *Store) loadUnknownMigrate(ctx context.Context, raw, signature []byte) (migrateIntent, error) {
	var zero migrateIntent
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
	verified, err := control.VerifyMigrateDataSettlement(raw, signature, migrateAuthority(deployment), time.Now().UTC().UnixMilli())
	if err != nil {
		return zero, err
	}
	c := verified.Command()
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil || row == nil || !sameMigrateCommand(row, c.CommandHeader) || row.state != "unknown" {
		return zero, ErrFenceReconciliation
	}
	var marker migrateClaimMarker
	if !canonicalDataJSON(row.receipt, &marker) || !validMigrateMarker(marker) || !migrateMarkerMatchesCommand(marker, c) {
		return zero, ErrFenceReconciliation
	}
	if err := migrateResourceMatches(ctx, tx, c, marker); err != nil {
		return zero, err
	}
	return migrateIntent{full: c, raw: bytes.Clone(raw), signature: bytes.Clone(signature), marker: marker, markerRaw: row.receipt}, nil
}

// migrateWallClock reads the wall clock a payload carries.
func migrateWallClock(c control.MigrateDataCommand) time.Duration {
	return time.Duration(mustDataInt(c.Payload.WallClockSeconds)) * time.Second
}

// MigrateDataBound is the longest a claimed migration may run: its wall clock plus the grace for the
// kill, removal and absence proofs. The Portal polls by read-back for this long.
func MigrateDataBound(wallClockSeconds int64) time.Duration {
	if wallClockSeconds < 0 {
		wallClockSeconds = 0
	}
	return time.Duration(wallClockSeconds)*time.Second + driver.MigrationGrace
}

func migrateJobRequest(c control.MigrateDataCommand, marker migrateClaimMarker, descriptor []byte, step appplatform.DataMigration, pin data.RootIdentity) driver.MigrationJobRequest {
	p := c.Payload
	return driver.MigrationJobRequest{
		Binding: driver.MigrationBinding{EffectID: c.EffectID, ClaimID: marker.ClaimID, PayloadSHA256: c.PayloadSHA256,
			OwnershipSHA256: marker.OwnershipSHA256, CheckpointReceiptSHA256: p.CheckpointReceiptSHA256},
		Identity: driver.CandidateIdentity{
			DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID,
			ResourceID: p.ResourceID, NamespaceID: p.NamespaceID, ReleaseSHA256: p.TargetReleaseSHA256,
			Generation: c.TargetGeneration, ControlFence: c.InstallationFence, WriterFence: p.WriterFence,
		},
		Descriptor: bytes.Clone(descriptor), Platform: "linux/" + runtime.GOARCH,
		Data: driver.CandidateDataEvidence{
			Root: pin, ProjectID: uint32(mustDataUint(p.ProjectID)), Device: mustDataUint(p.Device),
			NamespaceInode: mustDataUint(p.NamespaceInode), QuotaBytes: mustDataInt(p.QuotaBytes), QuotaInodes: mustDataInt(p.QuotaInodes),
		},
		Step: step,
	}
}

// bindMigrateDescriptor checks the descriptor the dispatch was given against the signed payload: the
// target release, a backend, and the one declared step with the signed entrypoint and wall clock.
func bindMigrateDescriptor(c control.MigrateDataCommand, descriptor []byte) (appplatform.DataMigration, error) {
	var zero appplatform.DataMigration
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != c.Payload.TargetReleaseSHA256 ||
		strconv.Itoa(parsed.Manifest.Data.Schema) != c.Payload.TargetSchema {
		return zero, ErrFenceReconciliation
	}
	step, err := appplatform.PlanDataUpgrade(int(mustDataInt(c.Payload.SourceSchema)), parsed.Manifest.Data)
	if err != nil || step == nil || strconv.Itoa(step.To) != c.Payload.TargetSchema ||
		control.MigrationEntrypointSHA256(step.Entrypoint) != c.Payload.EntrypointSHA256 ||
		time.Duration(driver.MigrationWallClock(*step).Seconds()) != time.Duration(mustDataInt(c.Payload.WallClockSeconds)) {
		return zero, ErrFenceReconciliation
	}
	return *step, nil
}

// DispatchMigrateData is the trusted orchestrator for one signed migration. Applied or reverted work
// replays its receipt without touching a native root. Unknown work is never run again: only a
// discard-migration effect settles it. A claimed job runs detached from the connection, to a definite
// end bounded by its wall clock plus MigrationGrace.
func (s *Store) DispatchMigrateData(ctx context.Context, raw, signature, descriptor []byte, native MigrationNative, pin data.RootIdentity) (MigrateDataReceipt, error) {
	var zero MigrateDataReceipt
	if native == nil || s.closing.Load() || !validDataRoot(pin) || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 ||
		len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrMigrateDataUnavailable
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	state, command, err := s.migrateState(ctx, raw, signature)
	if err != nil {
		return zero, fmt.Errorf("migration state: %w", err)
	}
	switch state {
	case "applied", "reverted":
		return s.readMigrateReceiptHeld(ctx, raw, signature)
	case "unknown":
		return zero, ErrMigrationUnknown
	}
	step, err := bindMigrateDescriptor(command, descriptor)
	if err != nil {
		return zero, err
	}
	// The marker is checked before any claim: a namespace already past the source schema is not migrated.
	before, err := native.ReadSchemaMarker(ctx, pin, command.Payload.NamespaceID, uint32(mustDataUint(command.Payload.ProjectID)))
	if err != nil {
		return zero, fmt.Errorf("read schema marker: %w", err)
	}
	if before.Present && before.Schema != int(mustDataInt(command.Payload.SourceSchema)) {
		return zero, fmt.Errorf("%w: namespace schema marker %d is not the source schema", ErrFenceReconciliation, before.Schema)
	}
	intent, replay, err := s.beginMigrateData(ctx, raw, signature)
	if err != nil {
		return zero, fmt.Errorf("begin migration: %w", err)
	}
	if replay {
		return s.readMigrateReceiptHeld(ctx, raw, signature)
	}
	// The claim is committed: the job runs to a definite end whatever happens to the connection.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), MigrateDataBound(mustDataInt(command.Payload.WallClockSeconds)))
	defer cancel()
	request := migrateJobRequest(intent.full, intent.marker, descriptor, step, pin)
	job, err := native.RunMigrationJob(ctx, request)
	if err != nil {
		return zero, fmt.Errorf("run migration job: %w", err)
	}
	if job.Binding != request.Binding || job.Identity != request.Identity || job.ExitCode < 0 || job.Platform != request.Platform {
		return zero, fmt.Errorf("migration job binding: %w", ErrFenceReconciliation)
	}
	outcome, err := observeMigrationOutcome(ctx, native, pin, intent.full, job)
	if err != nil {
		return zero, err
	}
	return s.settleMigrateData(ctx, intent, outcome)
}

// observeMigrationOutcome turns a finished, removed job into the settlement it earns. The marker is
// read only after a clean exit; an error reading it that is not a verdict on its content leaves the
// effect unknown.
func observeMigrationOutcome(ctx context.Context, native MigrationNative, pin data.RootIdentity, c control.MigrateDataCommand, job driver.MigrationJobSnapshot) (migrateObservation, error) {
	p := c.Payload
	o := migrateObservation{exit: job.ExitCode, observedMS: job.ObservedMS, state: "failed"}
	switch {
	case job.Killed:
		o.outcome = "wall_clock"
		return o, nil
	case job.ExitCode != 0:
		o.outcome = "exit_nonzero"
		return o, nil
	}
	marker, err := native.ReadSchemaMarker(ctx, pin, p.NamespaceID, uint32(mustDataUint(p.ProjectID)))
	if errors.Is(err, data.ErrMigrationUnsafe) {
		o.outcome = "marker_invalid"
		return o, nil
	}
	if err != nil {
		return o, fmt.Errorf("read schema marker after job: %w", err)
	}
	if marker.NamespaceID != p.NamespaceID || marker.ProjectID != uint32(mustDataUint(p.ProjectID)) ||
		marker.NamespaceInode != mustDataUint(p.NamespaceInode) || marker.NamespaceDevice != mustDataUint(p.Device) ||
		marker.ByteLimit != mustDataInt(p.QuotaBytes) || marker.InodeLimit != mustDataInt(p.QuotaInodes) {
		return o, fmt.Errorf("schema marker binding: %w", ErrFenceReconciliation)
	}
	o.markerSchema, o.usedBytes, o.usedInodes = marker.Schema, marker.UsedBytes, marker.UsedInodes
	if marker.ObservedMS > o.observedMS {
		o.observedMS = marker.ObservedMS
	}
	switch {
	case !marker.Present:
		o.outcome = "marker_missing"
	case marker.Schema != int(mustDataInt(p.TargetSchema)):
		o.outcome = "marker_mismatch"
	case marker.UsedBytes > mustDataInt(p.QuotaBytes) || marker.UsedInodes > mustDataInt(p.QuotaInodes):
		o.outcome = "over_quota"
	default:
		o.state = "migrated"
	}
	return o, nil
}

// migrateState classifies the effect without writing: "" when no row exists.
func (s *Store) migrateState(ctx context.Context, raw, signature []byte) (string, control.MigrateDataCommand, error) {
	var none control.MigrateDataCommand
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", none, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return "", none, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return "", none, err
	}
	verified, err := control.VerifyMigrateDataSettlement(raw, signature, migrateAuthority(deployment), time.Now().UTC().UnixMilli())
	if err != nil {
		return "", none, err
	}
	c := verified.Command()
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return "", none, err
	}
	if row == nil {
		// An unclaimed command must still be inside its signed window to be dispatched.
		if _, err := control.VerifyMigrateData(raw, signature, migrateAuthority(deployment), time.Now().UTC().UnixMilli()); err != nil {
			return "", none, err
		}
		return "", c, nil
	}
	if !sameMigrateCommand(row, c.CommandHeader) {
		return "", none, ErrCommandConflict
	}
	if row.state != "applied" && row.state != "reverted" && row.state != "unknown" {
		return "", none, ErrFenceReconciliation
	}
	return row.state, c, nil
}

func (s *Store) readMigrateReceiptHeld(ctx context.Context, raw, signature []byte) (MigrateDataReceipt, error) {
	got, err := s.readMigrateData(ctx, raw, signature, control.VerifyMigrateDataSettlement)
	if err != nil {
		return MigrateDataReceipt{}, err
	}
	receipt, ok := got.row.(MigrateDataReceipt)
	if !ok {
		return MigrateDataReceipt{}, ErrMigrateDataUnavailable
	}
	return receipt, nil
}

type migrateReadResult struct {
	row     any
	command control.MigrateDataCommand
	missing bool
	unknown bool
}

type migrateVerifier func(raw, signature []byte, authority control.Authority, nowMS int64) (control.VerifiedMigrateDataCommand, error)

func (s *Store) readMigrateData(ctx context.Context, raw, signature []byte, verify migrateVerifier) (migrateReadResult, error) {
	var zero migrateReadResult
	if s.closing.Load() {
		return zero, ErrMigrateDataUnavailable
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
	verified, err := verify(raw, signature, migrateAuthority(deployment), time.Now().UTC().UnixMilli())
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
		return migrateReadResult{command: c, missing: true}, nil
	}
	if !sameMigrateCommand(row, c.CommandHeader) {
		return zero, ErrCommandConflict
	}
	if row.state == "unknown" {
		return migrateReadResult{command: c, unknown: true}, nil
	}
	if row.state != "applied" && row.state != "reverted" {
		return zero, ErrMigrateDataUnavailable
	}
	receipt, err := ParseMigrateDataReceipt([]byte(row.receipt))
	if err != nil || !MigrateDataReceiptMatchesCommand(receipt, c) || (row.state == "applied") != (receipt.State == "migrated") {
		return zero, ErrFenceReconciliation
	}
	var observed string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT observed_state,revision FROM runner_resources WHERE id=?`, receipt.Claim.ResourceID).Scan(&observed, &revision); err != nil ||
		observed != "absent" || revision < 2 {
		return zero, ErrFenceReconciliation
	}
	return migrateReadResult{command: c, row: receipt}, nil
}

// tryAcquireDataExecution takes the gate only if it is free.
func (s *Store) tryAcquireDataExecution() bool {
	s.executionOnce.Do(func() { s.executionGate = make(chan struct{}, 1) })
	select {
	case s.executionGate <- struct{}{}:
		return true
	default:
		return false
	}
}

// ReadMigrateDataOrStatus is the runner's read-back answer: the receipt (applied or reverted), or a
// MigrateDataStatus. A status never waits for a running dispatch: "pending" while the gate is held,
// "orphaned" for an unknown effect with the gate free, and "absent" only for no row at all, proven
// with the gate held by this read. The command is verified under the read purpose.
func (s *Store) ReadMigrateDataOrStatus(ctx context.Context, raw, signature []byte) (any, error) {
	got, err := s.readMigrateData(ctx, raw, signature, control.VerifyMigrateDataRead)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().UnixMilli()
	switch {
	case got.unknown:
		if !s.tryAcquireDataExecution() {
			return migrateStatusFor(got.command.CommandHeader, "pending", now), nil
		}
		s.releaseDataExecution()
		return migrateStatusFor(got.command.CommandHeader, "orphaned", now), nil
	case got.missing:
		if !s.tryAcquireDataExecution() {
			return migrateStatusFor(got.command.CommandHeader, "pending", now), nil
		}
		defer s.releaseDataExecution()
		again, err := s.readMigrateData(ctx, raw, signature, control.VerifyMigrateDataRead)
		if err != nil {
			return nil, err
		}
		if again.missing {
			return migrateStatusFor(again.command.CommandHeader, "absent", time.Now().UTC().UnixMilli()), nil
		}
		if again.unknown {
			return migrateStatusFor(again.command.CommandHeader, "orphaned", time.Now().UTC().UnixMilli()), nil
		}
		return again.row, nil
	}
	return got.row, nil
}
