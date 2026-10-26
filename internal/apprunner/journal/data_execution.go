package journal

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

const dataReceiptFormat = "portal.runner.prepare-data-receipt/1-dev.1"

// DataPreparationReceipt is a historical native observation, not writer access,
// current readiness, controller acknowledgement or Portal stage completion.
// Numeric wire values are decimal strings to preserve integer precision.
type DataPreparationReceipt struct {
	Format          string          `json:"format"`
	Claim           dataClaimMarker `json:"claim"`
	DeploymentID    string          `json:"deployment_id"`
	ControllerEpoch string          `json:"controller_epoch"`
	ControlRevision string          `json:"control_revision"`
	InstallationID  string          `json:"installation_id"`
	PayloadSHA256   string          `json:"payload_sha256"`
	ReleaseSHA256   string          `json:"release_sha256"`
	DataFormat      string          `json:"data_format"`
	Generation      string          `json:"generation"`
	WriterFence     string          `json:"writer_fence"`
	ProjectID       string          `json:"project_id"`
	NamespaceInode  string          `json:"namespace_inode"`
	Device          string          `json:"device"`
	QuotaBytes      string          `json:"quota_bytes"`
	QuotaInodes     string          `json:"quota_inodes"`
	UsedBytes       string          `json:"used_bytes"`
	UsedInodes      string          `json:"used_inodes"`
	ObservedMS      string          `json:"observed_ms"`
}

func dataNumber(text string, positive bool) bool {
	n, err := strconv.ParseInt(text, 10, 64)
	return err == nil && n >= 0 && (!positive || n > 0) && strconv.FormatInt(n, 10) == text
}
func nativeNumber(text string, positive bool) bool {
	n, err := strconv.ParseUint(text, 10, 64)
	return err == nil && (!positive || n > 0) && strconv.FormatUint(n, 10) == text
}
func dataSHA(text string) bool {
	raw, err := hex.DecodeString(text)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == text
}
func validDataRoot(pin data.RootIdentity) bool {
	return enrollmentUUID.MatchString(pin.FilesystemUUID) && pin.FilesystemUUID != "00000000-0000-0000-0000-000000000000" && pin.Inode > 0
}
func canonicalDataJSON(raw string, target any) bool {
	if len(raw) > 16384 || json.Unmarshal([]byte(raw), target) != nil {
		return false
	}
	encoded, err := json.Marshal(target)
	return err == nil && string(encoded) == raw
}
func validDataMarker(m dataClaimMarker) bool {
	if m.Format != "portal.runner.prepare-data-claim/1-dev.1" || !enrollmentUUID.MatchString(m.ClaimID) || !enrollmentUUID.MatchString(m.EffectID) || !enrollmentUUID.MatchString(m.ResourceID) || !dataSHA(m.OwnershipSHA256) || !dataNumber(m.AdmittedFence, true) || !dataNumber(m.ClaimedMS, false) || !dataNumber(m.ExpiresMS, true) {
		return false
	}
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
	if expires <= claimed {
		return false
	}
	if m.RootFilesystemUUID == "" && m.RootInode == "" {
		return true
	}
	inode, err := strconv.ParseUint(m.RootInode, 10, 64)
	return err == nil && nativeNumber(m.RootInode, true) && validDataRoot(data.RootIdentity{FilesystemUUID: m.RootFilesystemUUID, Inode: inode})
}
func markerRoot(m dataClaimMarker) data.RootIdentity {
	inode, _ := strconv.ParseUint(m.RootInode, 10, 64)
	return data.RootIdentity{FilesystemUUID: m.RootFilesystemUUID, Inode: inode}
}
func validDataReceipt(r DataPreparationReceipt) bool {
	if r.Format != dataReceiptFormat || !validDataMarker(r.Claim) || !validDataRoot(markerRoot(r.Claim)) || !dataSHA(r.PayloadSHA256) || !dataSHA(r.ReleaseSHA256) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID} {
		if !enrollmentUUID.MatchString(id) {
			return false
		}
	}
	for _, n := range []string{r.ControlRevision, r.DataFormat, r.Generation, r.WriterFence, r.ProjectID, r.QuotaBytes, r.QuotaInodes} {
		if !dataNumber(n, true) {
			return false
		}
	}
	for _, n := range []string{r.Device, r.UsedBytes, r.UsedInodes, r.ObservedMS} {
		if !nativeNumber(n, false) {
			return false
		}
	}
	if !dataNumber(r.UsedBytes, false) || !dataNumber(r.UsedInodes, false) || !dataNumber(r.ObservedMS, false) || !nativeNumber(r.NamespaceInode, true) {
		return false
	}
	project, err := strconv.ParseUint(r.ProjectID, 10, 32)
	bytes, _ := strconv.ParseInt(r.QuotaBytes, 10, 64)
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	expires, _ := strconv.ParseInt(r.Claim.ExpiresMS, 10, 64)
	return err == nil && project <= uint64(maxDataProjectID) && bytes%512 == 0 && observed >= claimed && observed < expires
}

// requireDataRootHistory derives the deployment-wide root from retained command
// metadata, including old epochs/absent resources. Never recycle these tombstones.
// Unknown formats or truncated scans refuse instead of silently losing a binding.
func requireDataRootHistory(ctx context.Context, tx *sql.Tx, expected data.RootIdentity) error {
	if !validDataRoot(expected) {
		return ErrFenceReconciliation
	}
	rows, err := tx.QueryContext(ctx, `SELECT effect_id,state,receipt_json,admitted_fence,installation_id,payload_sha256,target_generation,controller_epoch FROM runner_commands WHERE kind='prepare-data' AND (receipt_json<>'{}' OR state<>'prepared') LIMIT 65537`)
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
		var effect, state, raw, installation, payload, epoch string
		var fence, generation int64
		if err := rows.Scan(&effect, &state, &raw, &fence, &installation, &payload, &generation, &epoch); err != nil {
			return err
		}
		var marker dataClaimMarker
		switch state {
		case "unknown":
			if !canonicalDataJSON(raw, &marker) || !validDataMarker(marker) {
				return ErrFenceReconciliation
			}
		case "applied":
			var receipt DataPreparationReceipt
			if !canonicalDataJSON(raw, &receipt) || !validDataReceipt(receipt) || receipt.InstallationID != installation || receipt.PayloadSHA256 != payload || receipt.Generation != strconv.FormatInt(generation, 10) || receipt.ControllerEpoch != epoch {
				return ErrFenceReconciliation
			}
			marker = receipt.Claim
		default:
			return ErrFenceReconciliation
		}
		if marker.EffectID != effect || marker.AdmittedFence != strconv.FormatInt(fence, 10) {
			return ErrFenceReconciliation
		}
		root := markerRoot(marker)
		if root != (data.RootIdentity{}) && root != expected {
			return ErrFenceReconciliation
		}
	}
	return rows.Err()
}

// bindDataRoot must commit before any native mutation. A lost commit ack leaves
// unknown work and never authorizes creation. The caller holds the execution gate.
func (s *Store) bindDataRoot(ctx context.Context, live *dataClaimState, pin data.RootIdentity) error {
	if live == nil || live.owner != s || !validDataRoot(pin) {
		return ErrDataClaimUnavailable
	}
	tx, facts, err := s.beginDataCommand(ctx, live.raw, live.signature, "unknown", live.marker)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	resource, err := existingDataResource(ctx, tx, facts.command)
	if err != nil {
		return err
	}
	if _, err := reservedDataResource(facts, resource); err != nil {
		return err
	}
	if err := requireDataRootHistory(ctx, tx, pin); err != nil {
		return err
	}
	var marker dataClaimMarker
	if !canonicalDataJSON(live.marker, &marker) || !validDataMarker(marker) {
		return ErrFenceReconciliation
	}
	marker.RootFilesystemUUID, marker.RootInode = pin.FilesystemUUID, strconv.FormatUint(pin.Inode, 10)
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`, string(encoded), facts.now, facts.command.EffectID, live.marker)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return ErrFenceReconciliation
	}
	if err := finalDataCommandCheck(ctx, facts); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	live.marker = string(encoded)
	return nil
}

// ExecutePrepareData is trusted runtime infrastructure, not an app capability.
// It accepts a concrete native provider, never caller-supplied success/receipt.
// The original signed envelope must remain current through durable settlement.
func (s *Store) ExecutePrepareData(ctx context.Context, claim DataClaim, root *data.Root, expected data.RootIdentity) (DataPreparationReceipt, error) {
	var receipt DataPreparationReceipt
	if root == nil || !validDataRoot(expected) {
		return receipt, ErrDataClaimUnavailable
	}
	_, err := s.RunPrepareDataOnce(ctx, claim, func(ctx context.Context, reservation DataReservation, payload control.PrepareDataPayload) error {
		var marker dataClaimMarker
		if !canonicalDataJSON(claim.state.marker, &marker) || !validDataMarker(marker) {
			return ErrFenceReconciliation
		}
		expires, _ := strconv.ParseInt(marker.ExpiresMS, 10, 64)
		ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
		defer cancel()
		actual, err := root.Identity(ctx)
		if err != nil {
			return err
		}
		if actual != expected {
			return data.ErrUnsafe
		}
		if err := s.bindDataRoot(ctx, claim.state, expected); err != nil {
			return err
		}
		bytes, _ := strconv.ParseInt(payload.QuotaBytes, 10, 64)
		inodes, _ := strconv.ParseInt(payload.QuotaInodes, 10, 64)
		prepared, err := root.Prepare(ctx, expected, data.Request{NamespaceID: reservation.NamespaceID, ProjectID: reservation.ProjectID, Bytes: bytes, Inodes: inodes})
		if err != nil {
			return err
		}
		observed, err := root.Inspect(ctx, expected, reservation.NamespaceID, reservation.ProjectID)
		if err != nil {
			return err
		}
		if observed.NamespaceID != prepared.NamespaceID || observed.ProjectID != prepared.ProjectID || observed.Device != prepared.Device || observed.Inode != prepared.Inode || observed.ByteLimit != prepared.ByteLimit || observed.InodeLimit != prepared.InodeLimit || !observed.Inherited {
			return data.ErrIncomplete
		}
		receipt, err = s.settlePreparedData(ctx, claim.state, expected, observed)
		return err
	})
	if err != nil {
		return DataPreparationReceipt{}, err
	}
	return receipt, nil
}

// Only the concrete native execution path calls this settlement helper. A native
// observation is historical: no SQL transaction spans filesystem work.
func (s *Store) settlePreparedData(ctx context.Context, live *dataClaimState, pin data.RootIdentity, observed data.Observation) (DataPreparationReceipt, error) {
	var zero DataPreparationReceipt
	if live == nil || live.owner != s || !live.used.Load() {
		return zero, ErrDataClaimUnavailable
	}
	tx, facts, err := s.beginDataCommand(ctx, live.raw, live.signature, "unknown", live.marker)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	resource, err := existingDataResource(ctx, tx, facts.command)
	if err != nil {
		return zero, err
	}
	reservation, err := reservedDataResource(facts, resource)
	if err != nil {
		return zero, err
	}
	if err := requireDataRootHistory(ctx, tx, pin); err != nil {
		return zero, err
	}
	var marker dataClaimMarker
	if !canonicalDataJSON(live.marker, &marker) || !validDataMarker(marker) || markerRoot(marker) != pin {
		return zero, ErrFenceReconciliation
	}
	c := facts.command
	if observed.NamespaceID != reservation.NamespaceID || observed.ProjectID != reservation.ProjectID || observed.ByteLimit <= 0 || observed.InodeLimit <= 0 || strconv.FormatInt(observed.ByteLimit, 10) != c.Payload.QuotaBytes || strconv.FormatInt(observed.InodeLimit, 10) != c.Payload.QuotaInodes || observed.Inode == 0 || observed.UsedBytes < 0 || observed.UsedInodes < 0 || !observed.Inherited {
		return zero, ErrFenceReconciliation
	}
	receipt := DataPreparationReceipt{Format: dataReceiptFormat, Claim: marker, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: c.ControlRevision, InstallationID: c.InstallationID, PayloadSHA256: c.PayloadSHA256, ReleaseSHA256: c.Payload.ReleaseSHA256, DataFormat: c.Payload.DataFormat, Generation: c.TargetGeneration, WriterFence: c.Payload.WriterFence, ProjectID: strconv.FormatUint(uint64(observed.ProjectID), 10), NamespaceInode: strconv.FormatUint(observed.Inode, 10), Device: strconv.FormatUint(observed.Device, 10), QuotaBytes: c.Payload.QuotaBytes, QuotaInodes: c.Payload.QuotaInodes, UsedBytes: strconv.FormatInt(observed.UsedBytes, 10), UsedInodes: strconv.FormatInt(observed.UsedInodes, 10), ObservedMS: strconv.FormatInt(facts.now, 10)}
	if !validDataReceipt(receipt) {
		return zero, ErrFenceReconciliation
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, ErrFenceReconciliation
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_resources SET observed_state='present',revision=2,observed_ms=?,updated_ms=? WHERE id=? AND observed_state='unknown' AND revision=1 AND observed_ms IS NULL AND ownership_sha256=?`, facts.now, facts.now, reservation.ResourceID, reservation.OwnershipSHA256)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	changed, err = tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`, string(encoded), facts.now, c.EffectID, live.marker)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if time.Now().UnixMilli() < facts.now {
		return zero, ErrFenceReconciliation
	}
	if err := finalDataCommandCheck(ctx, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}
