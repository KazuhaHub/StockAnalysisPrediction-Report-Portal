package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const discardClaimFormat = "portal.runner.discard-migration-claim/1-dev.1"
const discardReceiptFormat = "portal.runner.discard-migration-receipt/1-dev.1"

type discardClaimMarker struct {
	Format          string `json:"format"`
	ClaimID         string `json:"claim_id"`
	EffectID        string `json:"effect_id"`
	MigrateEffectID string `json:"migrate_effect_id"`
	AdmittedFence   string `json:"admitted_fence"`
	ClaimedMS       string `json:"claimed_ms"`
}

// DiscardMigrationReceipt records that a migration job was proven absent and its migrate-data effect
// reverted, in one transaction. MigrateReceiptSHA256 is the digest of the discarded migrate-data receipt.
type DiscardMigrationReceipt struct {
	Format               string `json:"format"`
	DeploymentID         string `json:"deployment_id"`
	ControllerEpoch      string `json:"controller_epoch"`
	InstallationID       string `json:"installation_id"`
	OperationID          string `json:"operation_id"`
	TargetGeneration     string `json:"target_generation"`
	Stage                string `json:"stage"`
	EffectID             string `json:"effect_id"`
	PayloadSHA256        string `json:"payload_sha256"`
	NamespaceID          string `json:"namespace_id"`
	ResourceID           string `json:"resource_id"`
	MigrateEffectID      string `json:"migrate_effect_id"`
	MigratePayloadSHA256 string `json:"migrate_payload_sha256"`
	MigrateReceiptSHA256 string `json:"migrate_receipt_sha256"`
	State                string `json:"state"`
	ObservedMS           string `json:"observed_ms"`
	SettledMS            string `json:"settled_ms"`
}

func validDiscardReceipt(r DiscardMigrationReceipt) bool {
	if r.Format != discardReceiptFormat || r.Stage != "migrate" || r.State != "discarded" ||
		!dataSHA(r.PayloadSHA256) || !dataSHA(r.MigratePayloadSHA256) || !dataSHA(r.MigrateReceiptSHA256) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID, r.NamespaceID, r.ResourceID, r.MigrateEffectID} {
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
	return r.EffectID != r.MigrateEffectID && settled >= observed
}

func ParseDiscardMigrationReceipt(raw []byte) (DiscardMigrationReceipt, error) {
	var receipt DiscardMigrationReceipt
	if len(raw) == 0 || len(raw) > 16384 || !canonicalDataJSON(string(raw), &receipt) || !validDiscardReceipt(receipt) {
		return DiscardMigrationReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

// DiscardMigrationReceiptMatchesCommand reports whether the receipt answers exactly this signed command.
func DiscardMigrationReceiptMatchesCommand(r DiscardMigrationReceipt, c control.DiscardMigrationCommand) bool {
	p := c.Payload
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch && r.InstallationID == c.InstallationID &&
		r.OperationID == c.OperationID && r.TargetGeneration == c.TargetGeneration && r.Stage == c.Stage &&
		r.EffectID == c.EffectID && r.PayloadSHA256 == c.PayloadSHA256 && r.NamespaceID == p.NamespaceID &&
		r.ResourceID == p.ResourceID && r.MigrateEffectID == p.MigrateEffectID && r.MigratePayloadSHA256 == p.MigratePayloadSHA256
}

type discardIntent struct {
	full           control.DiscardMigrationCommand
	raw, signature []byte
	marker         discardClaimMarker
	markerRaw      string
	migrate        migrateClaimMarker
	migrateRaw     string
}

// loadDiscardTarget checks the unknown migrate-data effect the discard names against the signed payload.
func loadDiscardTarget(ctx context.Context, tx *sql.Tx, c control.DiscardMigrationCommand) (migrateClaimMarker, string, error) {
	var zero migrateClaimMarker
	p := c.Payload
	row, err := loadImportRow(ctx, tx, p.MigrateEffectID)
	if err != nil || row == nil || row.kind != "migrate-data" || row.stage != "migrate" || row.installation != c.InstallationID ||
		row.operation != c.OperationID || row.payload != p.MigratePayloadSHA256 || row.epoch != c.ControllerEpoch ||
		row.generation != mustDataInt(c.TargetGeneration) || row.state != "unknown" {
		return zero, "", fmt.Errorf("%w: discard target is not an unknown migration", ErrFenceReconciliation)
	}
	var marker migrateClaimMarker
	if !canonicalDataJSON(row.receipt, &marker) || !validMigrateMarker(marker) || marker.EffectID != p.MigrateEffectID ||
		marker.ResourceID != p.ResourceID || marker.NamespaceID != p.NamespaceID || marker.TargetReleaseSHA256 != p.TargetReleaseSHA256 {
		return zero, "", fmt.Errorf("%w: discard target claim", ErrFenceReconciliation)
	}
	var namespace, desired, observed, ownership string
	var revision int64
	var observedAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT data_namespace_id,desired_state,observed_state,ownership_sha256,revision,observed_ms FROM runner_resources WHERE id=? AND kind='container'`,
		p.ResourceID).Scan(&namespace, &desired, &observed, &ownership, &revision, &observedAt); err != nil ||
		namespace != p.NamespaceID || desired != "present" || observed != "unknown" || ownership != marker.OwnershipSHA256 ||
		revision != 1 || observedAt.Valid {
		return zero, "", fmt.Errorf("%w: discard target resource", ErrFenceReconciliation)
	}
	return marker, row.receipt, nil
}

// beginDiscardMigration verifies the signed command and, when the migrate-data effect it names is
// unknown, commits the discard as unknown. A discard is convergent, so an unknown discard is replayed
// rather than refused. An applied or reverted migration is never discarded.
func (s *Store) beginDiscardMigration(ctx context.Context, raw, signature []byte) (discardIntent, bool, error) {
	var zero discardIntent
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
	verified, err := control.VerifyDiscardMigrationSettlement(raw, signature, authority, now)
	if err != nil {
		return zero, false, err
	}
	c := verified.Command()
	existing, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil {
		return zero, false, err
	}
	if existing != nil {
		if !sameMigrateCommand(existing, c.CommandHeader) {
			return zero, false, ErrCommandConflict
		}
		if existing.state == "applied" {
			parsed, err := ParseDiscardMigrationReceipt([]byte(existing.receipt))
			if err != nil || !DiscardMigrationReceiptMatchesCommand(parsed, c) {
				return zero, false, ErrFenceReconciliation
			}
			return zero, true, nil
		}
		if existing.state != "unknown" {
			return zero, false, ErrFenceReconciliation
		}
		var marker discardClaimMarker
		if !canonicalDataJSON(existing.receipt, &marker) || marker.EffectID != c.EffectID || marker.MigrateEffectID != c.Payload.MigrateEffectID {
			return zero, false, ErrFenceReconciliation
		}
		migrate, migrateRaw, err := loadDiscardTarget(ctx, tx, c)
		if err != nil {
			return zero, false, err
		}
		return discardIntent{full: c, raw: bytes.Clone(raw), signature: bytes.Clone(signature), marker: marker, markerRaw: existing.receipt, migrate: migrate, migrateRaw: migrateRaw}, false, nil
	}
	// A new discard must be inside its signed window.
	if _, err := control.VerifyDiscardMigration(raw, signature, authority, now); err != nil {
		return zero, false, err
	}
	migrate, migrateRaw, err := loadDiscardTarget(ctx, tx, c)
	if err != nil {
		return zero, false, err
	}
	var siblings int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE operation_id=? AND (kind='discard-migration' OR (stage=? AND effect_number=?))`,
		c.OperationID, c.Stage, mustDataInt(c.EffectNumber)).Scan(&siblings); err != nil || siblings != 0 {
		return zero, false, fmt.Errorf("%w: one discard per operation", ErrCommandConflict)
	}
	var fenceFloor int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(fence),0) FROM (SELECT admitted_fence AS fence FROM runner_commands WHERE installation_id=? AND kind<>'load-image' UNION ALL SELECT control_fence AS fence FROM runner_resources WHERE installation_id=?)`, c.InstallationID, c.InstallationID).Scan(&fenceFloor); err != nil {
		return zero, false, err
	}
	fence := mustDataInt(c.InstallationFence)
	if fence < fenceFloor {
		return zero, false, ErrCommandConflict
	}
	claimID, err := dataClaimID()
	if err != nil {
		return zero, false, err
	}
	marker := discardClaimMarker{Format: discardClaimFormat, ClaimID: claimID, EffectID: c.EffectID, MigrateEffectID: c.Payload.MigrateEffectID,
		AdmittedFence: c.InstallationFence, ClaimedMS: strconv.FormatInt(now, 10)}
	markerRaw, err := json.Marshal(marker)
	if err != nil {
		return zero, false, ErrMigrateDataUnavailable
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,'unknown',?,NULL,?,?)`,
		c.EffectID, c.OperationID, c.InstallationID, c.Stage, mustDataInt(c.EffectNumber), c.Kind, c.PayloadSHA256,
		mustDataInt(c.TargetGeneration), c.ControllerEpoch, fence, string(markerRaw), now, now); err != nil {
		return zero, false, err
	}
	if s.closing.Load() {
		return zero, false, ErrMigrateDataUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, false, err
	}
	return discardIntent{full: c, raw: bytes.Clone(raw), signature: bytes.Clone(signature), marker: marker, markerRaw: string(markerRaw), migrate: migrate, migrateRaw: migrateRaw}, false, nil
}

// settleDiscardMigration reverts the migration and applies the discard in one transaction, after the
// job has been proven absent: the migrate-data row becomes reverted (outcome discarded), the job
// resource absent, and the discard row applied. Any lost race rolls all three back.
func (s *Store) settleDiscardMigration(ctx context.Context, intent discardIntent) (DiscardMigrationReceipt, error) {
	var zero DiscardMigrationReceipt
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
	verified, err := control.VerifyDiscardMigrationSettlement(intent.raw, intent.signature, migrateAuthority(deployment), now)
	if err != nil || verified.Command() != intent.full {
		return zero, ErrFenceReconciliation
	}
	c := intent.full
	row, err := loadImportRow(ctx, tx, c.EffectID)
	if err != nil || row == nil || !sameMigrateCommand(row, c.CommandHeader) || row.state != "unknown" || row.receipt != intent.markerRaw {
		return zero, fmt.Errorf("discard settlement state: %w", ErrFenceReconciliation)
	}
	migrate, migrateRaw, err := loadDiscardTarget(ctx, tx, c)
	if err != nil || migrateRaw != intent.migrateRaw || migrate != intent.migrate {
		return zero, fmt.Errorf("discard settlement target: %w", ErrFenceReconciliation)
	}
	migrateRow, err := loadImportRow(ctx, tx, c.Payload.MigrateEffectID)
	if err != nil || migrateRow == nil {
		return zero, ErrFenceReconciliation
	}
	discarded := MigrateDataReceipt{
		Format: migrateReceiptFormat, Claim: migrate, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: "migrate",
		EffectID: migrate.EffectID, PayloadSHA256: migrateRow.payload, ExitCode: "0", MarkerSchema: "0", UsedBytes: "0", UsedInodes: "0",
		State: "discarded", Outcome: "discarded", ObservedMS: migrate.ClaimedMS, SettledMS: strconv.FormatInt(now, 10),
	}
	if !validMigrateReceipt(discarded) {
		return zero, fmt.Errorf("discard migrate receipt: %w", ErrFenceReconciliation)
	}
	discardedRaw, err := json.Marshal(discarded)
	if err != nil || len(discardedRaw) > 16384 {
		return zero, ErrFenceReconciliation
	}
	receipt := DiscardMigrationReceipt{
		Format: discardReceiptFormat, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID,
		OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256,
		NamespaceID: c.Payload.NamespaceID, ResourceID: c.Payload.ResourceID, MigrateEffectID: c.Payload.MigrateEffectID,
		MigratePayloadSHA256: c.Payload.MigratePayloadSHA256, MigrateReceiptSHA256: candidateHash(discardedRaw),
		State: "discarded", ObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	if !validDiscardReceipt(receipt) || !DiscardMigrationReceiptMatchesCommand(receipt, c) {
		return zero, fmt.Errorf("discard receipt: %w", ErrFenceReconciliation)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, ErrFenceReconciliation
	}
	if err := settleMigrateRows(ctx, tx, migrate, migrateRaw, "reverted", string(discardedRaw), now); err != nil {
		return zero, err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`,
		string(encoded), now, c.EffectID, intent.markerRaw)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf("discard settlement command rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

func discardJobRequest(c control.DiscardMigrationCommand, migrate migrateClaimMarker, descriptor []byte, step appplatform.DataMigration, pin data.RootIdentity, discardClaim discardClaimMarker) driver.MigrationJobRequest {
	return driver.MigrationJobRequest{
		Binding: driver.MigrationBinding{EffectID: migrate.EffectID, ClaimID: migrate.ClaimID, PayloadSHA256: c.Payload.MigratePayloadSHA256,
			OwnershipSHA256: migrate.OwnershipSHA256, CheckpointReceiptSHA256: migrate.CheckpointReceiptSHA256},
		Identity: driver.CandidateIdentity{
			DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID,
			ResourceID: migrate.ResourceID, NamespaceID: migrate.NamespaceID, ReleaseSHA256: migrate.TargetReleaseSHA256,
			Generation: c.TargetGeneration, ControlFence: migrate.AdmittedFence, WriterFence: migrate.WriterFence,
		},
		Descriptor: bytes.Clone(descriptor), Platform: "linux/" + runtime.GOARCH,
		Data: driver.CandidateDataEvidence{
			Root: pin, ProjectID: uint32(mustDataUint(migrate.ProjectID)), Device: mustDataUint(migrate.Device),
			NamespaceInode: mustDataUint(migrate.NamespaceInode), QuotaBytes: mustDataInt(migrate.QuotaBytes), QuotaInodes: mustDataInt(migrate.QuotaInodes),
		},
		Step: step,
	}
}

func bindDiscardDescriptor(migrate migrateClaimMarker, descriptor []byte) (appplatform.DataMigration, error) {
	var zero appplatform.DataMigration
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != migrate.TargetReleaseSHA256 {
		return zero, ErrFenceReconciliation
	}
	step, err := appplatform.PlanDataUpgrade(int(mustDataInt(migrate.SourceSchema)), parsed.Manifest.Data)
	if err != nil || step == nil {
		return zero, ErrFenceReconciliation
	}
	return *step, nil
}

// DispatchDiscardMigration settles an unknown migration: under the data execution gate it finds the job
// by the name and labels derived for the effect (no stored native id is trusted), stops and removes it,
// proves absence, then reverts the migration in one transaction. Applied work replays its receipt.
func (s *Store) DispatchDiscardMigration(ctx context.Context, raw, signature, descriptor []byte, native MigrationNative, pin data.RootIdentity) (DiscardMigrationReceipt, error) {
	var zero DiscardMigrationReceipt
	if native == nil || s.closing.Load() || !validDataRoot(pin) || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 ||
		len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrMigrateDataUnavailable
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	intent, replay, err := s.beginDiscardMigration(ctx, raw, signature)
	if err != nil {
		return zero, fmt.Errorf("begin discard: %w", err)
	}
	if replay {
		return s.readDiscardReceiptHeld(ctx, raw, signature)
	}
	step, err := bindDiscardDescriptor(intent.migrate, descriptor)
	if err != nil {
		return zero, err
	}
	// A claimed discard runs to a definite end whatever happens to the connection.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), driver.MigrationGrace)
	defer cancel()
	if err := native.ProveMigrationAbsent(ctx, discardJobRequest(intent.full, intent.migrate, descriptor, step, pin, intent.marker)); err != nil {
		return zero, fmt.Errorf("prove migration absent: %w", err)
	}
	return s.settleDiscardMigration(ctx, intent)
}

type discardReadResult struct {
	row     any
	command control.DiscardMigrationCommand
	missing bool
	unknown bool
}

func (s *Store) readDiscardMigration(ctx context.Context, raw, signature []byte, verify func(raw, signature []byte, a control.Authority, nowMS int64) (control.VerifiedDiscardMigrationCommand, error)) (discardReadResult, error) {
	var zero discardReadResult
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
		return discardReadResult{command: c, missing: true}, nil
	}
	if !sameMigrateCommand(row, c.CommandHeader) {
		return zero, ErrCommandConflict
	}
	if row.state == "unknown" {
		return discardReadResult{command: c, unknown: true}, nil
	}
	if row.state != "applied" {
		return zero, ErrMigrateDataUnavailable
	}
	receipt, err := ParseDiscardMigrationReceipt([]byte(row.receipt))
	if err != nil || !DiscardMigrationReceiptMatchesCommand(receipt, c) {
		return zero, ErrFenceReconciliation
	}
	return discardReadResult{command: c, row: receipt}, nil
}

func (s *Store) readDiscardReceiptHeld(ctx context.Context, raw, signature []byte) (DiscardMigrationReceipt, error) {
	got, err := s.readDiscardMigration(ctx, raw, signature, control.VerifyDiscardMigrationSettlement)
	if err != nil {
		return DiscardMigrationReceipt{}, err
	}
	receipt, ok := got.row.(DiscardMigrationReceipt)
	if !ok {
		return DiscardMigrationReceipt{}, ErrMigrateDataUnavailable
	}
	return receipt, nil
}

// ReadDiscardMigrationOrStatus is the read-back answer for a discard: the receipt, or a status with the
// same meaning as for migrate-data. It verifies the command under the read purpose.
func (s *Store) ReadDiscardMigrationOrStatus(ctx context.Context, raw, signature []byte) (any, error) {
	got, err := s.readDiscardMigration(ctx, raw, signature, control.VerifyDiscardMigrationRead)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().UnixMilli()
	switch {
	case got.unknown:
		if afterFirstRead != nil {
			afterFirstRead()
		}
		if !s.tryAcquireDataExecution() {
			return migrateStatusFor(got.command.CommandHeader, "pending", now), nil
		}
		defer s.releaseDataExecution()
		again, err := s.readDiscardMigration(ctx, raw, signature, control.VerifyDiscardMigrationRead)
		if err != nil {
			return nil, err
		}
		switch {
		case again.unknown:
			return migrateStatusFor(again.command.CommandHeader, "orphaned", time.Now().UTC().UnixMilli()), nil
		case again.missing:
			return nil, ErrFenceReconciliation
		}
		return again.row, nil
	case got.missing:
		if !s.tryAcquireDataExecution() {
			return migrateStatusFor(got.command.CommandHeader, "pending", now), nil
		}
		defer s.releaseDataExecution()
		again, err := s.readDiscardMigration(ctx, raw, signature, control.VerifyDiscardMigrationRead)
		if err != nil {
			return nil, err
		}
		switch {
		case again.missing:
			return migrateStatusFor(again.command.CommandHeader, "absent", time.Now().UTC().UnixMilli()), nil
		case again.unknown:
			return migrateStatusFor(again.command.CommandHeader, "orphaned", time.Now().UTC().UnixMilli()), nil
		}
		return again.row, nil
	}
	return got.row, nil
}
