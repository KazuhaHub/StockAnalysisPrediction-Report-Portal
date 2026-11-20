package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// The quota raise and the data import of doc 185. Each is one signed effect over an existing namespace, claimed in
// one transaction (row 'unknown') before the native call and settled to 'applied' with its receipt after it. Both
// natives converge when repeated, so an effect left unknown is finished under the same claim, never claimed again.

var ErrDataCommandUnavailable = errors.New("runner data command is unavailable")

const (
	setDataQuotaReceiptFormat = "portal.runner.set-data-quota-receipt/1-dev.1"
	importDataReceiptFormat   = "portal.runner.import-data-receipt/1-dev.1"
)

type SetDataQuotaReceipt struct {
	Format           string `json:"format"`
	DeploymentID     string `json:"deployment_id"`
	ControllerEpoch  string `json:"controller_epoch"`
	InstallationID   string `json:"installation_id"`
	OperationID      string `json:"operation_id"`
	TargetGeneration string `json:"target_generation"`
	Stage            string `json:"stage"`
	EffectID         string `json:"effect_id"`
	PayloadSHA256    string `json:"payload_sha256"`
	NamespaceID      string `json:"namespace_id"`
	ReleaseSHA256    string `json:"release_sha256"`
	DataFormat       string `json:"data_format"`
	WriterFence      string `json:"writer_fence"`
	ProjectID        string `json:"project_id"`
	NamespaceInode   string `json:"namespace_inode"`
	Device           string `json:"device"`
	OldBytes         string `json:"old_bytes"`
	OldInodes        string `json:"old_inodes"`
	NewBytes         string `json:"new_bytes"`
	NewInodes        string `json:"new_inodes"`
	UsedBytes        string `json:"used_bytes"`
	UsedInodes       string `json:"used_inodes"`
	State            string `json:"state"`
	ObservedMS       string `json:"observed_ms"`
	SettledMS        string `json:"settled_ms"`
}

func validSetDataQuotaReceipt(r SetDataQuotaReceipt) bool {
	if r.Format != setDataQuotaReceiptFormat || r.Stage != "prepare_data" || r.State != "raised" ||
		!dataSHA(r.PayloadSHA256) || !dataSHA(r.ReleaseSHA256) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID, r.NamespaceID} {
		if !enrollmentUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	for _, value := range []string{r.TargetGeneration, r.DataFormat, r.WriterFence, r.ProjectID, r.OldBytes, r.OldInodes, r.NewBytes, r.NewInodes} {
		if !dataNumber(value, true) {
			return false
		}
	}
	for _, value := range []string{r.UsedBytes, r.UsedInodes, r.ObservedMS, r.SettledMS} {
		if !dataNumber(value, false) {
			return false
		}
	}
	if !nativeNumber(r.NamespaceInode, true) || !nativeNumber(r.Device, true) {
		return false
	}
	oldB, oldI, newB, newI := mustDataInt(r.OldBytes), mustDataInt(r.OldInodes), mustDataInt(r.NewBytes), mustDataInt(r.NewInodes)
	observed, settled := mustDataInt(r.ObservedMS), mustDataInt(r.SettledMS)
	return newB >= oldB && newI >= oldI && (newB > oldB || newI > oldI) && mustDataInt(r.UsedBytes) <= newB &&
		mustDataInt(r.UsedInodes) <= newI && observed > 0 && settled >= observed && mustDataInt(r.ProjectID) <= math.MaxUint32
}

// ParseSetDataQuotaReceipt accepts only the canonical encoding of a valid receipt.
func ParseSetDataQuotaReceipt(raw []byte) (SetDataQuotaReceipt, error) {
	var r SetDataQuotaReceipt
	if len(raw) == 0 || len(raw) > 8192 || !canonicalDataJSON(string(raw), &r) || !validSetDataQuotaReceipt(r) {
		return SetDataQuotaReceipt{}, ErrFenceReconciliation
	}
	return r, nil
}

// SetDataQuotaReceiptMatchesCommand reports whether the receipt answers exactly this signed command.
func SetDataQuotaReceiptMatchesCommand(r SetDataQuotaReceipt, c control.SetDataQuotaCommand) bool {
	p := c.Payload
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch && r.InstallationID == c.InstallationID &&
		r.OperationID == c.OperationID && r.TargetGeneration == c.TargetGeneration && r.Stage == c.Stage &&
		r.EffectID == c.EffectID && r.PayloadSHA256 == c.PayloadSHA256 && r.NamespaceID == p.NamespaceID &&
		r.ReleaseSHA256 == p.ReleaseSHA256 && r.DataFormat == p.DataFormat && r.WriterFence == p.WriterFence &&
		r.ProjectID == p.ProjectID && r.NamespaceInode == p.NamespaceInode && r.Device == p.Device &&
		r.OldBytes == p.OldBytes && r.OldInodes == p.OldInodes && r.NewBytes == p.NewBytes && r.NewInodes == p.NewInodes
}

type ImportDataReceipt struct {
	Format                      string `json:"format"`
	DeploymentID                string `json:"deployment_id"`
	ControllerEpoch             string `json:"controller_epoch"`
	InstallationID              string `json:"installation_id"`
	OperationID                 string `json:"operation_id"`
	TargetGeneration            string `json:"target_generation"`
	Stage                       string `json:"stage"`
	EffectID                    string `json:"effect_id"`
	PayloadSHA256               string `json:"payload_sha256"`
	NamespaceID                 string `json:"namespace_id"`
	ReleaseSHA256               string `json:"release_sha256"`
	DataFormat                  string `json:"data_format"`
	WriterFence                 string `json:"writer_fence"`
	ProjectID                   string `json:"project_id"`
	NamespaceInode              string `json:"namespace_inode"`
	Device                      string `json:"device"`
	QuotaBytes                  string `json:"quota_bytes"`
	QuotaInodes                 string `json:"quota_inodes"`
	ArchiveSHA256               string `json:"archive_sha256"`
	ArchiveBytes                string `json:"archive_bytes"`
	ArchiveEntries              string `json:"archive_entries"`
	ContentBytes                string `json:"content_bytes"`
	SourceCheckpointOperationID string `json:"source_checkpoint_operation_id"`
	SourceCheckpointEffectID    string `json:"source_checkpoint_effect_id"`
	SourceCheckpointReceiptSHA  string `json:"source_checkpoint_receipt_sha256"`
	Entries                     string `json:"entries"`
	UsedBytes                   string `json:"used_bytes"`
	UsedInodes                  string `json:"used_inodes"`
	State                       string `json:"state"`
	ObservedMS                  string `json:"observed_ms"`
	SettledMS                   string `json:"settled_ms"`
}

func validImportDataReceipt(r ImportDataReceipt) bool {
	if r.Format != importDataReceiptFormat || r.Stage != "restore_data" || r.State != "imported" ||
		!dataSHA(r.PayloadSHA256) || !dataSHA(r.ReleaseSHA256) || !dataSHA(r.ArchiveSHA256) || !dataSHA(r.SourceCheckpointReceiptSHA) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID, r.NamespaceID,
		r.SourceCheckpointOperationID, r.SourceCheckpointEffectID} {
		if !enrollmentUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	for _, value := range []string{r.TargetGeneration, r.WriterFence, r.ProjectID, r.QuotaBytes, r.QuotaInodes, r.ArchiveBytes} {
		if !dataNumber(value, true) {
			return false
		}
	}
	for _, value := range []string{r.DataFormat, r.ArchiveEntries, r.ContentBytes, r.Entries, r.UsedBytes, r.UsedInodes, r.ObservedMS, r.SettledMS} {
		if !dataNumber(value, false) {
			return false
		}
	}
	if !nativeNumber(r.NamespaceInode, true) || !nativeNumber(r.Device, true) {
		return false
	}
	observed, settled := mustDataInt(r.ObservedMS), mustDataInt(r.SettledMS)
	return mustDataInt(r.UsedBytes) <= mustDataInt(r.QuotaBytes) && mustDataInt(r.UsedInodes) <= mustDataInt(r.QuotaInodes) &&
		r.Entries == r.ArchiveEntries && observed > 0 && settled >= observed && mustDataInt(r.ProjectID) <= math.MaxUint32
}

// ParseImportDataReceipt accepts only the canonical encoding of a valid receipt.
func ParseImportDataReceipt(raw []byte) (ImportDataReceipt, error) {
	var r ImportDataReceipt
	if len(raw) == 0 || len(raw) > 8192 || !canonicalDataJSON(string(raw), &r) || !validImportDataReceipt(r) {
		return ImportDataReceipt{}, ErrFenceReconciliation
	}
	return r, nil
}

// ImportDataReceiptMatchesCommand reports whether the receipt answers exactly this signed command.
func ImportDataReceiptMatchesCommand(r ImportDataReceipt, c control.ImportDataCommand) bool {
	p := c.Payload
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch && r.InstallationID == c.InstallationID &&
		r.OperationID == c.OperationID && r.TargetGeneration == c.TargetGeneration && r.Stage == c.Stage &&
		r.EffectID == c.EffectID && r.PayloadSHA256 == c.PayloadSHA256 && r.NamespaceID == p.NamespaceID &&
		r.ReleaseSHA256 == p.ReleaseSHA256 && r.DataFormat == p.DataFormat && r.WriterFence == p.WriterFence &&
		r.ProjectID == p.ProjectID && r.NamespaceInode == p.NamespaceInode && r.Device == p.Device &&
		r.QuotaBytes == p.QuotaBytes && r.QuotaInodes == p.QuotaInodes && r.ArchiveSHA256 == p.ArchiveSHA256 &&
		r.ArchiveBytes == p.ArchiveBytes && r.ArchiveEntries == p.ArchiveEntries && r.ContentBytes == p.ContentBytes &&
		r.SourceCheckpointOperationID == p.SourceCheckpointOperationID && r.SourceCheckpointEffectID == p.SourceCheckpointEffectID &&
		r.SourceCheckpointReceiptSHA == p.SourceCheckpointReceiptSHA
}

// dataEffectVolume is the namespace's volume resource row, the evidence that it exists, is the installation's and is
// not being removed.
type dataEffectVolume struct {
	controlFence, writerFence int64
}

func loadDataEffectVolume(ctx context.Context, tx *sql.Tx, installation, namespace string) (dataEffectVolume, error) {
	var v dataEffectVolume
	var owner, desired, observed string
	err := tx.QueryRowContext(ctx, `SELECT installation_id,desired_state,observed_state,control_fence,writer_fence FROM runner_resources
		WHERE id=? AND kind='volume' AND data_namespace_id=?`, namespace, namespace).Scan(&owner, &desired, &observed, &v.controlFence, &v.writerFence)
	if err != nil || owner != installation || desired != "present" || observed != "present" {
		return v, fmt.Errorf("data effect volume: %w", ErrFenceReconciliation)
	}
	return v, nil
}

// requireNoPendingInstallationWork refuses a claim while the installation has an unsettled command of its own
// besides this one.
func requireNoPendingInstallationWork(ctx context.Context, tx *sql.Tx, installation, effect string) error {
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND effect_id<>? AND kind NOT IN (`+installationNeutralCommandKinds+`) AND state IN ('prepared','unknown')`,
		installation, effect).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return fmt.Errorf("installation has unsettled work: %w", ErrFenceReconciliation)
	}
	return nil
}

// loadInstallData reads the install's prepare-data receipt that created the namespace, and proves it is the
// namespace's.
func loadInstallData(ctx context.Context, tx *sql.Tx, effect, receiptSHA, installation, namespace string) (DataPreparationReceipt, error) {
	var kind, state, raw string
	if err := tx.QueryRowContext(ctx, `SELECT kind,state,receipt_json FROM runner_commands WHERE effect_id=?`, effect).Scan(&kind, &state, &raw); err != nil ||
		kind != "prepare-data" || state != "applied" || candidateHash([]byte(raw)) != receiptSHA {
		return DataPreparationReceipt{}, fmt.Errorf("install data receipt: %w", ErrFenceReconciliation)
	}
	receipt, err := ParseDataPreparationReceipt([]byte(raw))
	if err != nil || receipt.InstallationID != installation || receipt.Claim.ResourceID != namespace {
		return DataPreparationReceipt{}, fmt.Errorf("install data receipt binding: %w", ErrFenceReconciliation)
	}
	return receipt, nil
}

func (s *Store) readAuthority(ctx context.Context, tx *sql.Tx) (control.Authority, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return control.Authority{}, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return control.Authority{}, err
	}
	return importAuthorityFrom(deployment), nil
}

// insertUnknownDataEffect claims the effect: one row, 'unknown', in the caller's transaction.
func insertUnknownDataEffect(ctx context.Context, tx *sql.Tx, h control.CommandHeader, now int64) error {
	slot, _ := strconv.ParseInt(h.EffectNumber, 10, 64)
	generation, _ := strconv.ParseInt(h.TargetGeneration, 10, 64)
	fence, _ := strconv.ParseInt(h.InstallationFence, 10, 64)
	_, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,'unknown','{}',NULL,?,?)`,
		h.EffectID, h.OperationID, h.InstallationID, h.Stage, slot, h.Kind, h.PayloadSHA256, generation, h.ControllerEpoch, fence, now, now)
	return err
}

func rowMatchesHeader(r *importRow, h control.CommandHeader) bool {
	slot, _ := strconv.ParseInt(h.EffectNumber, 10, 64)
	generation, _ := strconv.ParseInt(h.TargetGeneration, 10, 64)
	return r.operation == h.OperationID && r.installation == h.InstallationID && r.stage == h.Stage && r.kind == h.Kind &&
		r.payload == h.PayloadSHA256 && r.epoch == h.ControllerEpoch && r.slot == slot && r.generation == generation
}

// settleDataEffect turns the claimed row into the applied receipt, if the row is still exactly the claim.
func (s *Store) settleDataEffect(ctx context.Context, h control.CommandHeader, receipt any, check func(ctx context.Context, tx *sql.Tx, a control.Authority, now int64) error) error {
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 8192 {
		return ErrFenceReconciliation
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	authority, err := s.readAuthority(ctx, tx)
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	if err := check(ctx, tx, authority, now); err != nil {
		return err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json='{}' AND payload_sha256=?`,
		string(encoded), now, h.EffectID, h.PayloadSHA256)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("settle data effect rows=%d err=%v: %w", n, err, ErrFenceReconciliation)
	}
	return tx.Commit()
}

// ---- set-data-quota ----

// SetDataQuotaNative performs the native raise.
type SetDataQuotaNative func(ctx context.Context, request data.QuotaRaiseRequest) (data.QuotaRaiseSnapshot, error)

func quotaRaiseRequestFor(c control.SetDataQuotaCommand) data.QuotaRaiseRequest {
	p := c.Payload
	return data.QuotaRaiseRequest{
		NamespaceID: p.NamespaceID, ProjectID: uint32(mustDataInt(p.ProjectID)),
		OldBytes: mustDataInt(p.OldBytes), OldInodes: mustDataInt(p.OldInodes),
		NewBytes: mustDataInt(p.NewBytes), NewInodes: mustDataInt(p.NewInodes),
		ExpectedDevice: mustDataUint(p.Device), ExpectedNamespaceInode: mustDataUint(p.NamespaceInode),
	}
}

// claimSetDataQuota verifies the command against the journal's evidence and claims it, with its larger capacity
// reservation, in one transaction. It returns replay=true when the effect is already applied.
func (s *Store) claimSetDataQuota(ctx context.Context, raw, signature []byte) (command control.SetDataQuotaCommand, receipt *SetDataQuotaReceipt, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return command, nil, err
	}
	defer tx.Rollback()
	authority, err := s.readAuthority(ctx, tx)
	if err != nil {
		return command, nil, err
	}
	now := time.Now().UTC().UnixMilli()
	// An effect already in the journal is verified under the settlement purpose: its signed window may be past.
	verified, err := control.VerifySetDataQuotaSettlement(raw, signature, authority, now)
	if err != nil {
		return command, nil, err
	}
	command = verified.Command()
	row, err := loadImportRow(ctx, tx, command.EffectID)
	if err != nil {
		return command, nil, err
	}
	if row != nil {
		if !rowMatchesHeader(row, command.CommandHeader) {
			return command, nil, ErrCommandConflict
		}
		switch row.state {
		case "applied":
			parsed, err := ParseSetDataQuotaReceipt([]byte(row.receipt))
			if err != nil || !SetDataQuotaReceiptMatchesCommand(parsed, command) {
				return command, nil, ErrFenceReconciliation
			}
			return command, &parsed, nil
		case "unknown":
			return command, nil, tx.Commit()
		}
		return command, nil, ErrFenceReconciliation
	}
	// A new claim must be inside its signed window.
	if _, err := control.VerifySetDataQuota(raw, signature, authority, now); err != nil {
		return command, nil, err
	}
	p := command.Payload
	if err := requireNoPendingInstallationWork(ctx, tx, command.InstallationID, command.EffectID); err != nil {
		return command, nil, err
	}
	volume, err := loadDataEffectVolume(ctx, tx, command.InstallationID, p.NamespaceID)
	if err != nil {
		return command, nil, err
	}
	fence := mustDataInt(command.InstallationFence)
	if fence < volume.controlFence || mustDataInt(p.WriterFence) > volume.writerFence {
		return command, nil, fmt.Errorf("set data quota fence: %w", ErrFenceReconciliation)
	}
	install, err := loadInstallData(ctx, tx, p.DataEffectID, p.DataReceiptSHA256, command.InstallationID, p.NamespaceID)
	if err != nil || install.ProjectID != p.ProjectID || install.NamespaceInode != p.NamespaceInode || install.Device != p.Device {
		return command, nil, fmt.Errorf("set data quota install evidence: %w", ErrFenceReconciliation)
	}
	currentBytes, currentInodes, err := currentQuota(ctx, tx, p.NamespaceID, install.QuotaBytes, install.QuotaInodes)
	if err != nil {
		return command, nil, err
	}
	if currentBytes != p.OldBytes || currentInodes != p.OldInodes {
		return command, nil, fmt.Errorf("set data quota is not over the recorded limits: %w", ErrFenceReconciliation)
	}
	if plan, admit := s.namespacePlan(p.NamespaceID, command.InstallationID, mustDataInt(p.NewBytes), mustDataInt(p.NewInodes)); admit {
		refusal, err := s.admitCapacity(ctx, tx, plan, now)
		if err != nil {
			return command, nil, err
		}
		if refusal != nil {
			generation := mustDataInt(command.TargetGeneration)
			receipt, err := refuseCapacityInsert(ctx, tx, CapacityIdentityOfHeader(command.CommandHeader), fence, generation, *refusal, now)
			if err != nil {
				return command, nil, err
			}
			if err := tx.Commit(); err != nil {
				return command, nil, err
			}
			return command, nil, &CapacityRefusalError{Receipt: receipt}
		}
	}
	if err := insertUnknownDataEffect(ctx, tx, command.CommandHeader, now); err != nil {
		return command, nil, err
	}
	return command, nil, tx.Commit()
}

// DispatchSetDataQuota claims a raise once and applies it. A raise left unknown by an earlier attempt is finished
// under that same claim: the native raise converges.
func (s *Store) DispatchSetDataQuota(ctx context.Context, raw, signature []byte, native SetDataQuotaNative) (SetDataQuotaReceipt, error) {
	var zero SetDataQuotaReceipt
	if s.closing.Load() || native == nil {
		return zero, ErrDataCommandUnavailable
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	command, replay, err := s.claimSetDataQuota(ctx, raw, signature)
	if err != nil {
		return zero, fmt.Errorf("claim set data quota: %w", err)
	}
	if replay != nil {
		return *replay, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	snapshot, err := native(ctx, quotaRaiseRequestFor(command))
	if err != nil {
		return zero, fmt.Errorf("raise data quota: %w", err)
	}
	p := command.Payload
	if snapshot.NamespaceID != p.NamespaceID || snapshot.ByteLimit != mustDataInt(p.NewBytes) || snapshot.InodeLimit != mustDataInt(p.NewInodes) ||
		snapshot.NamespaceDevice != mustDataUint(p.Device) || snapshot.NamespaceInode != mustDataUint(p.NamespaceInode) {
		return zero, fmt.Errorf("raise observation binding: %w", ErrFenceReconciliation)
	}
	now := time.Now().UTC().UnixMilli()
	result := SetDataQuotaReceipt{
		Format: setDataQuotaReceiptFormat, DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
		InstallationID: command.InstallationID, OperationID: command.OperationID, TargetGeneration: command.TargetGeneration,
		Stage: command.Stage, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
		NamespaceID: p.NamespaceID, ReleaseSHA256: p.ReleaseSHA256, DataFormat: p.DataFormat, WriterFence: p.WriterFence,
		ProjectID: p.ProjectID, NamespaceInode: p.NamespaceInode, Device: p.Device,
		OldBytes: p.OldBytes, OldInodes: p.OldInodes, NewBytes: p.NewBytes, NewInodes: p.NewInodes,
		UsedBytes: strconv.FormatInt(snapshot.UsedBytes, 10), UsedInodes: strconv.FormatInt(snapshot.UsedInodes, 10),
		State: "raised", ObservedMS: strconv.FormatInt(min(snapshot.ObservedMS, now), 10), SettledMS: strconv.FormatInt(now, 10),
	}
	if !validSetDataQuotaReceipt(result) {
		return zero, fmt.Errorf("raise receipt: %w", ErrFenceReconciliation)
	}
	err = s.settleDataEffect(ctx, command.CommandHeader, result, func(ctx context.Context, tx *sql.Tx, a control.Authority, now int64) error {
		_, err := control.VerifySetDataQuotaSettlement(raw, signature, a, now)
		return err
	})
	if err != nil {
		return zero, fmt.Errorf("settle set data quota: %w", err)
	}
	return result, nil
}

// ReadSetDataQuotaReceipt answers a read-back: the receipt, or ErrNoSuchEffect when the effect has no applied row.
func (s *Store) ReadSetDataQuotaReceipt(ctx context.Context, raw, signature []byte) (SetDataQuotaReceipt, error) {
	var zero SetDataQuotaReceipt
	if s.closing.Load() {
		return zero, ErrDataCommandUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	authority, err := s.readAuthority(ctx, tx)
	if err != nil {
		return zero, err
	}
	verified, err := control.VerifySetDataQuotaRead(raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return zero, err
	}
	command := verified.Command()
	row, err := loadImportRow(ctx, tx, command.EffectID)
	if err != nil {
		return zero, err
	}
	if row == nil {
		return zero, ErrNoSuchEffect
	}
	if !rowMatchesHeader(row, command.CommandHeader) {
		return zero, ErrCommandConflict
	}
	if row.state != "applied" {
		return zero, ErrDataCommandUnavailable
	}
	parsed, err := ParseSetDataQuotaReceipt([]byte(row.receipt))
	if err != nil || !SetDataQuotaReceiptMatchesCommand(parsed, command) {
		return zero, ErrFenceReconciliation
	}
	return parsed, nil
}

// ---- import-data ----

// ImportDataNative performs the native import.
type ImportDataNative func(ctx context.Context, request data.ImportDataRequest) (data.ImportDataSnapshot, error)

func importDataRequestFor(c control.ImportDataCommand) data.ImportDataRequest {
	p := c.Payload
	return data.ImportDataRequest{
		NamespaceID: p.NamespaceID, ProjectID: uint32(mustDataInt(p.ProjectID)), ArchiveSHA256: p.ArchiveSHA256,
		ArchiveBytes: mustDataInt(p.ArchiveBytes), Entries: mustDataInt(p.ArchiveEntries), Bytes: mustDataInt(p.ContentBytes),
		QuotaBytes: mustDataInt(p.QuotaBytes), QuotaInodes: mustDataInt(p.QuotaInodes), DataFormat: mustDataInt(p.DataFormat),
		MaxArchiveBytes: mustDataInt(p.ArchiveBytes), ExpectedDevice: mustDataUint(p.Device),
		ExpectedNamespaceInode: mustDataUint(p.NamespaceInode), WriterFence: mustDataInt(p.WriterFence),
	}
}

func (s *Store) claimImportData(ctx context.Context, raw, signature []byte) (command control.ImportDataCommand, receipt *ImportDataReceipt, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return command, nil, err
	}
	defer tx.Rollback()
	authority, err := s.readAuthority(ctx, tx)
	if err != nil {
		return command, nil, err
	}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyImportDataSettlement(raw, signature, authority, now)
	if err != nil {
		return command, nil, err
	}
	command = verified.Command()
	row, err := loadImportRow(ctx, tx, command.EffectID)
	if err != nil {
		return command, nil, err
	}
	if row != nil {
		if !rowMatchesHeader(row, command.CommandHeader) {
			return command, nil, ErrCommandConflict
		}
		switch row.state {
		case "applied":
			parsed, err := ParseImportDataReceipt([]byte(row.receipt))
			if err != nil || !ImportDataReceiptMatchesCommand(parsed, command) {
				return command, nil, ErrFenceReconciliation
			}
			return command, &parsed, nil
		case "unknown":
			return command, nil, tx.Commit()
		}
		return command, nil, ErrFenceReconciliation
	}
	if _, err := control.VerifyImportData(raw, signature, authority, now); err != nil {
		return command, nil, err
	}
	p := command.Payload
	if err := requireNoPendingInstallationWork(ctx, tx, command.InstallationID, command.EffectID); err != nil {
		return command, nil, err
	}
	volume, err := loadDataEffectVolume(ctx, tx, command.InstallationID, p.NamespaceID)
	if err != nil {
		return command, nil, err
	}
	fence := mustDataInt(command.InstallationFence)
	if fence < volume.controlFence || mustDataInt(p.WriterFence) > volume.writerFence {
		return command, nil, fmt.Errorf("import data fence: %w", ErrFenceReconciliation)
	}
	var running int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources WHERE data_namespace_id=? AND kind='container' AND observed_state NOT IN ('stopped','absent')`,
		p.NamespaceID).Scan(&running); err != nil || running != 0 {
		return command, nil, fmt.Errorf("import data running containers=%d err=%v: %w", running, err, ErrFenceReconciliation)
	}
	// The pre-import checkpoint of this very operation must be applied, verified and still retained.
	var snapOperation, snapInstallation, snapStage, snapKind, snapState, snapReceipt string
	var snapGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT operation_id,installation_id,stage,kind,state,receipt_json,target_generation FROM runner_commands WHERE effect_id=?`,
		p.SourceCheckpointEffectID).Scan(&snapOperation, &snapInstallation, &snapStage, &snapKind, &snapState, &snapReceipt, &snapGeneration); err != nil ||
		snapOperation != p.SourceCheckpointOperationID || snapOperation != command.OperationID || snapInstallation != command.InstallationID ||
		snapStage != "snapshot" || snapKind != "snapshot" || snapState != "applied" || snapGeneration != mustDataInt(command.TargetGeneration) ||
		candidateHash([]byte(snapReceipt)) != p.SourceCheckpointReceiptSHA {
		return command, nil, fmt.Errorf("import data checkpoint evidence: %w", ErrFenceReconciliation)
	}
	checkpoint, err := ParseCheckpointReceipt([]byte(snapReceipt))
	if err != nil || checkpoint.NamespaceID != p.NamespaceID || checkpoint.ProjectID != p.ProjectID ||
		checkpoint.NamespaceInode != p.NamespaceInode || checkpoint.Device != p.Device {
		return command, nil, fmt.Errorf("import data checkpoint receipt: %w", ErrFenceReconciliation)
	}
	var state string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT state,revision FROM runner_checkpoints WHERE id=? AND data_namespace_id=? AND installation_id=? AND operation_id=? AND manifest_sha256=?`,
		checkpoint.CheckpointID, p.NamespaceID, command.InstallationID, command.OperationID, checkpoint.ManifestSHA256).Scan(&state, &revision); err != nil ||
		state != "verified" || revision != 2 {
		return command, nil, fmt.Errorf("import data checkpoint state=%s revision=%d: %w", state, revision, ErrFenceReconciliation)
	}
	install, err := loadInstallDataByNamespace(ctx, tx, command.InstallationID, p.NamespaceID)
	if err != nil {
		return command, nil, err
	}
	currentBytes, currentInodes, err := currentQuota(ctx, tx, p.NamespaceID, install.QuotaBytes, install.QuotaInodes)
	if err != nil {
		return command, nil, err
	}
	if currentBytes != p.QuotaBytes || currentInodes != p.QuotaInodes {
		return command, nil, fmt.Errorf("import data quota differs from the namespace's: %w", ErrFenceReconciliation)
	}
	if err := insertUnknownDataEffect(ctx, tx, command.CommandHeader, now); err != nil {
		return command, nil, err
	}
	return command, nil, tx.Commit()
}

// loadInstallDataByNamespace finds the install's prepare-data receipt of a namespace: the applied prepare-data
// row whose receipt names it.
func loadInstallDataByNamespace(ctx context.Context, tx *sql.Tx, installation, namespace string) (DataPreparationReceipt, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands WHERE installation_id=? AND kind='prepare-data' AND state='applied' AND json_extract(receipt_json,'$.claim.resource_id')=?`,
		installation, namespace).Scan(&raw); err != nil {
		return DataPreparationReceipt{}, fmt.Errorf("install data receipt of the namespace: %w", ErrFenceReconciliation)
	}
	receipt, err := ParseDataPreparationReceipt([]byte(raw))
	if err != nil {
		return DataPreparationReceipt{}, fmt.Errorf("install data receipt of the namespace: %w", ErrFenceReconciliation)
	}
	return receipt, nil
}

// DispatchImportData claims an import once and runs it. An import left unknown is finished under the same claim.
func (s *Store) DispatchImportData(ctx context.Context, raw, signature []byte, native ImportDataNative) (ImportDataReceipt, error) {
	var zero ImportDataReceipt
	if s.closing.Load() || native == nil {
		return zero, ErrDataCommandUnavailable
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	command, replay, err := s.claimImportData(ctx, raw, signature)
	if err != nil {
		return zero, fmt.Errorf("claim import data: %w", err)
	}
	if replay != nil {
		return *replay, nil
	}
	ctx, cancel := detachedImportContext(ctx, mustDataInt(command.Payload.ArchiveBytes))
	defer cancel()
	snapshot, err := native(ctx, importDataRequestFor(command))
	if err != nil {
		return zero, fmt.Errorf("import data: %w", err)
	}
	p := command.Payload
	if snapshot.NamespaceID != p.NamespaceID || snapshot.ArchiveSHA256 != p.ArchiveSHA256 || snapshot.Entries != mustDataInt(p.ArchiveEntries) ||
		snapshot.ByteLimit != mustDataInt(p.QuotaBytes) || snapshot.InodeLimit != mustDataInt(p.QuotaInodes) ||
		snapshot.NamespaceDevice != mustDataUint(p.Device) || snapshot.NamespaceInode != mustDataUint(p.NamespaceInode) {
		return zero, fmt.Errorf("import observation binding: %w", ErrFenceReconciliation)
	}
	now := time.Now().UTC().UnixMilli()
	result := ImportDataReceipt{
		Format: importDataReceiptFormat, DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
		InstallationID: command.InstallationID, OperationID: command.OperationID, TargetGeneration: command.TargetGeneration,
		Stage: command.Stage, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
		NamespaceID: p.NamespaceID, ReleaseSHA256: p.ReleaseSHA256, DataFormat: p.DataFormat, WriterFence: p.WriterFence,
		ProjectID: p.ProjectID, NamespaceInode: p.NamespaceInode, Device: p.Device,
		QuotaBytes: p.QuotaBytes, QuotaInodes: p.QuotaInodes, ArchiveSHA256: p.ArchiveSHA256, ArchiveBytes: p.ArchiveBytes,
		ArchiveEntries: p.ArchiveEntries, ContentBytes: p.ContentBytes,
		SourceCheckpointOperationID: p.SourceCheckpointOperationID, SourceCheckpointEffectID: p.SourceCheckpointEffectID,
		SourceCheckpointReceiptSHA: p.SourceCheckpointReceiptSHA,
		Entries:                    strconv.FormatInt(snapshot.Entries, 10), UsedBytes: strconv.FormatInt(snapshot.UsedBytes, 10),
		UsedInodes: strconv.FormatInt(snapshot.UsedInodes, 10), State: "imported",
		ObservedMS: strconv.FormatInt(min(snapshot.ObservedMS, now), 10), SettledMS: strconv.FormatInt(now, 10),
	}
	if !validImportDataReceipt(result) {
		return zero, fmt.Errorf("import receipt: %w", ErrFenceReconciliation)
	}
	err = s.settleDataEffect(ctx, command.CommandHeader, result, func(ctx context.Context, tx *sql.Tx, a control.Authority, now int64) error {
		_, err := control.VerifyImportDataSettlement(raw, signature, a, now)
		return err
	})
	if err != nil {
		return zero, fmt.Errorf("settle import data: %w", err)
	}
	return result, nil
}

// ReadImportDataReceipt answers a read-back: the receipt, or ErrNoSuchEffect when the effect has no row.
func (s *Store) ReadImportDataReceipt(ctx context.Context, raw, signature []byte) (ImportDataReceipt, error) {
	var zero ImportDataReceipt
	if s.closing.Load() {
		return zero, ErrDataCommandUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	authority, err := s.readAuthority(ctx, tx)
	if err != nil {
		return zero, err
	}
	verified, err := control.VerifyImportDataRead(raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return zero, err
	}
	command := verified.Command()
	row, err := loadImportRow(ctx, tx, command.EffectID)
	if err != nil {
		return zero, err
	}
	if row == nil {
		return zero, ErrNoSuchEffect
	}
	if !rowMatchesHeader(row, command.CommandHeader) {
		return zero, ErrCommandConflict
	}
	if row.state != "applied" {
		return zero, ErrDataCommandUnavailable
	}
	parsed, err := ParseImportDataReceipt([]byte(row.receipt))
	if err != nil || !ImportDataReceiptMatchesCommand(parsed, command) {
		return zero, ErrFenceReconciliation
	}
	return parsed, nil
}
