package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const candidatePreflightFormat = "portal.runner.candidate-preflight/1-dev.1"
const candidateReceiptFormat = "portal.runner.candidate-running-receipt/1-dev.1"

// candidatePreflightEvidence is path-free durable metadata, never executable
// authority. Decimal strings preserve native inode precision in journal JSON.
type candidatePreflightEvidence struct {
	Format             string `json:"format"`
	ImageID            string `json:"image_id"`
	Platform           string `json:"platform"`
	PolicySHA256       string `json:"policy_sha256"`
	MountsSHA256       string `json:"mounts_sha256"`
	RootFilesystemUUID string `json:"root_filesystem_uuid"`
	RootInode          string `json:"root_inode"`
	ProjectID          string `json:"project_id"`
	Device             string `json:"device"`
	NamespaceInode     string `json:"namespace_inode"`
	QuotaBytes         string `json:"quota_bytes"`
	QuotaInodes        string `json:"quota_inodes"`
}

// CandidateRunningReceipt records a historical native process observation. It
// is not app readiness, writer access, activation or controller acknowledgement.
type CandidateRunningReceipt struct {
	Format           string               `json:"format"`
	Claim            candidateClaimMarker `json:"claim"`
	DeploymentID     string               `json:"deployment_id"`
	ControllerEpoch  string               `json:"controller_epoch"`
	ControlRevision  string               `json:"control_revision"`
	InstallationID   string               `json:"installation_id"`
	OperationID      string               `json:"operation_id"`
	Generation       string               `json:"generation"`
	WriterFence      string               `json:"writer_fence"`
	PayloadSHA256    string               `json:"payload_sha256"`
	ReleaseSHA256    string               `json:"release_sha256"`
	DataNamespaceID  string               `json:"data_namespace_id"`
	DataFormat       string               `json:"data_format"`
	NativeID         string               `json:"native_id"`
	NativeObservedMS string               `json:"native_observed_ms"`
	SettledMS        string               `json:"settled_ms"`
}

func candidateNativeRequest(c control.CandidateCommand, proof DataPreparationReceipt, descriptor []byte) (driver.CandidateLaunchRequest, error) {
	if !validDataReceipt(proof) || proof.Claim.ResourceID != c.Payload.DataNamespaceID || proof.ReleaseSHA256 != c.Payload.ReleaseSHA256 || proof.InstallationID != c.InstallationID || proof.DeploymentID != c.DeploymentID || proof.ControllerEpoch != c.ControllerEpoch || proof.Generation != c.TargetGeneration || proof.DataFormat != c.Payload.DataFormat || proof.WriterFence != c.Payload.WriterFence {
		return driver.CandidateLaunchRequest{}, ErrFenceReconciliation
	}
	project, _ := strconv.ParseUint(proof.ProjectID, 10, 32)
	device, _ := strconv.ParseUint(proof.Device, 10, 64)
	inode, _ := strconv.ParseUint(proof.NamespaceInode, 10, 64)
	quotaBytes, _ := strconv.ParseInt(proof.QuotaBytes, 10, 64)
	quotaInodes, _ := strconv.ParseInt(proof.QuotaInodes, 10, 64)
	return driver.CandidateLaunchRequest{Identity: driver.CandidateIdentity{DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID, ResourceID: c.Payload.ResourceID, NamespaceID: c.Payload.DataNamespaceID, ReleaseSHA256: c.Payload.ReleaseSHA256, Generation: c.TargetGeneration, ControlFence: c.InstallationFence, WriterFence: c.Payload.WriterFence}, Descriptor: bytes.Clone(descriptor), Platform: c.Payload.Platform, Data: driver.CandidateDataEvidence{Root: markerRoot(proof.Claim), ProjectID: uint32(project), Device: device, NamespaceInode: inode, QuotaBytes: quotaBytes, QuotaInodes: quotaInodes}}, nil
}
func candidatePreflight(snapshot driver.CandidateLaunchSnapshot) candidatePreflightEvidence {
	d := snapshot.Data
	return candidatePreflightEvidence{Format: candidatePreflightFormat, ImageID: snapshot.ImageID, Platform: snapshot.Platform, PolicySHA256: snapshot.PolicySHA256, MountsSHA256: snapshot.MountsSHA256, RootFilesystemUUID: d.Root.FilesystemUUID, RootInode: strconv.FormatUint(d.Root.Inode, 10), ProjectID: strconv.FormatUint(uint64(d.ProjectID), 10), Device: strconv.FormatUint(d.Device, 10), NamespaceInode: strconv.FormatUint(d.NamespaceInode, 10), QuotaBytes: strconv.FormatInt(d.QuotaBytes, 10), QuotaInodes: strconv.FormatInt(d.QuotaInodes, 10)}
}
func validCandidatePreflight(p candidatePreflightEvidence) bool {
	if p.Format != candidatePreflightFormat || len(p.ImageID) != 71 || p.ImageID[:7] != "sha256:" || !dataSHA(p.ImageID[7:]) || !dataSHA(p.PolicySHA256) || !dataSHA(p.MountsSHA256) || (p.Platform != "linux/amd64" && p.Platform != "linux/arm64") || !enrollmentUUID.MatchString(p.RootFilesystemUUID) || p.RootFilesystemUUID == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for _, n := range []string{p.RootInode, p.NamespaceInode} {
		if !nativeNumber(n, true) {
			return false
		}
	}
	if !nativeNumber(p.Device, false) || !dataNumber(p.QuotaBytes, true) || !dataNumber(p.QuotaInodes, true) || !nativeNumber(p.ProjectID, true) {
		return false
	}
	project, err := strconv.ParseUint(p.ProjectID, 10, 32)
	quota, _ := strconv.ParseInt(p.QuotaBytes, 10, 64)
	return err == nil && project <= uint64(maxDataProjectID) && quota%512 == 0
}
func validCandidateMarker(m candidateClaimMarker) bool {
	if m.Format != "portal.runner.candidate-claim/1-dev.1" || !enrollmentUUID.MatchString(m.ClaimID) || !enrollmentUUID.MatchString(m.EffectID) || !enrollmentUUID.MatchString(m.ResourceID) || !dataSHA(m.OwnershipSHA256) || !dataSHA(m.DataReceiptSHA256) || !dataNumber(m.AdmittedFence, true) || !dataNumber(m.ClaimedMS, false) || !dataNumber(m.ExpiresMS, true) {
		return false
	}
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
	if expires <= claimed {
		return false
	}
	if m.Preflight == nil {
		return m.PreflightBoundMS == ""
	}
	if !validCandidatePreflight(*m.Preflight) || !dataNumber(m.PreflightBoundMS, false) {
		return false
	}
	bound, _ := strconv.ParseInt(m.PreflightBoundMS, 10, 64)
	return bound >= claimed && bound < expires
}
func matchCandidateMarker(m candidateClaimMarker, f candidateFacts, r CandidateReservation) bool {
	return validCandidateMarker(m) && m.EffectID == f.command.EffectID && m.ResourceID == r.ResourceID && m.OwnershipSHA256 == r.OwnershipSHA256 && m.DataReceiptSHA256 == f.dataSHA && m.AdmittedFence == strconv.FormatInt(f.stored.fence, 10) && m.ExpiresMS == f.command.ExpiresMS
}

// bindCandidatePreflight commits exact native evidence before the first possible
// Docker mutation. Caller holds the execution gate; no SQL lock spans native IO.
func (s *Store) bindCandidatePreflight(ctx context.Context, live *candidateClaimState, snapshot driver.CandidateLaunchSnapshot) error {
	if live == nil || live.owner != s || !live.used.Load() {
		return ErrCandidateClaimUnavailable
	}
	tx, f, err := s.beginCandidateCommand(ctx, live.raw, live.signature, "unknown", live.marker)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	resource, err := existingCandidateResource(ctx, tx, f.command)
	if err != nil {
		return err
	}
	reservation, err := reservedCandidate(f, resource)
	if err != nil {
		return err
	}
	var marker candidateClaimMarker
	if !canonicalDataJSON(live.marker, &marker) || !matchCandidateMarker(marker, f, reservation) || marker.Preflight != nil {
		return ErrFenceReconciliation
	}
	request, err := candidateNativeRequest(f.command, f.data, nil)
	if err != nil {
		return err
	}
	if snapshot.Identity != request.Identity || snapshot.Data != request.Data || snapshot.Platform != request.Platform {
		return ErrFenceReconciliation
	}
	preflight := candidatePreflight(snapshot)
	if !validCandidatePreflight(preflight) {
		return ErrFenceReconciliation
	}
	marker.Preflight = &preflight
	marker.PreflightBoundMS = strconv.FormatInt(f.now, 10)
	if !validCandidateMarker(marker) {
		return ErrFenceReconciliation
	}
	encoded, err := json.Marshal(marker)
	if err != nil || len(encoded) > 16384 {
		return ErrFenceReconciliation
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`, string(encoded), f.now, f.command.EffectID, live.marker)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return ErrFenceReconciliation
	}
	if err := finalCandidateCheck(ctx, f); err != nil {
		return err
	}
	if s.closing.Load() {
		return ErrCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	live.marker = string(encoded)
	return nil
}

// ExecuteCandidate uses only a concrete native runtime and its opaque result.
// Descriptor bytes must match the signed release; applications supply neither
// callbacks nor a native ID. Failures retain uncertainty and never auto-retry.
func (s *Store) ExecuteCandidate(ctx context.Context, claim CandidateClaim, native *driver.CandidateRuntime, descriptor []byte) (CandidateRunningReceipt, error) {
	var receipt CandidateRunningReceipt
	if native == nil {
		return receipt, ErrCandidateClaimUnavailable
	}
	descriptor = bytes.Clone(descriptor)
	_, err := s.RunCandidateOnce(ctx, claim, func(ctx context.Context, _ CandidateReservation, _ control.CandidatePayload, proof DataPreparationReceipt) error {
		var command control.CandidateCommand
		if json.Unmarshal(claim.state.raw, &command) != nil {
			return ErrCandidateClaimUnavailable
		}
		request, err := candidateNativeRequest(command, proof, descriptor)
		if err != nil {
			return err
		}
		launch, err := native.PrepareCandidate(ctx, request)
		if err != nil {
			return err
		}
		defer launch.Close()
		snapshot, err := launch.Snapshot()
		if err != nil {
			return err
		}
		if err := s.bindCandidatePreflight(ctx, claim.state, snapshot); err != nil {
			return err
		}
		// Recheck signed authority and the durable binding immediately before native
		// invocation. A later loss of authority also prevents receipt settlement.
		if err := s.checkBoundCandidate(ctx, claim.state, snapshot); err != nil {
			return err
		}
		observed, err := native.StartCandidate(ctx, launch)
		if err != nil {
			return err
		}
		receipt, err = s.settleCandidateRunning(ctx, claim.state, observed)
		return err
	})
	if err != nil {
		return CandidateRunningReceipt{}, err
	}
	return receipt, nil
}
func (s *Store) checkBoundCandidate(ctx context.Context, live *candidateClaimState, snapshot driver.CandidateLaunchSnapshot) error {
	if live == nil || live.owner != s || !live.used.Load() || !time.Now().Before(live.deadline) {
		return ErrCandidateClaimUnavailable
	}
	tx, f, err := s.beginCandidateCommand(ctx, live.raw, live.signature, "unknown", live.marker)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	resource, err := existingCandidateResource(ctx, tx, f.command)
	if err != nil {
		return err
	}
	r, err := reservedCandidate(f, resource)
	if err != nil {
		return err
	}
	var m candidateClaimMarker
	if !canonicalDataJSON(live.marker, &m) || !matchCandidateMarker(m, f, r) || m.Preflight == nil || *m.Preflight != candidatePreflight(snapshot) {
		return ErrFenceReconciliation
	}
	request, err := candidateNativeRequest(f.command, f.data, nil)
	if err != nil || request.Identity != snapshot.Identity || request.Data != snapshot.Data || request.Platform != snapshot.Platform {
		return ErrFenceReconciliation
	}
	if err := finalCandidateCheck(ctx, f); err != nil {
		return err
	}
	if s.closing.Load() {
		return ErrCandidateClaimUnavailable
	}
	return tx.Commit()
}

// Only opaque native observations enter this settlement path. A plain snapshot
// or parsed receipt cannot assert a real process effect.
func (s *Store) settleCandidateRunning(ctx context.Context, live *candidateClaimState, observation driver.CandidateRunningObservation) (CandidateRunningReceipt, error) {
	var zero CandidateRunningReceipt
	if live == nil || live.owner != s || !live.used.Load() {
		return zero, ErrCandidateClaimUnavailable
	}
	observed, err := observation.Snapshot()
	if err != nil {
		return zero, err
	}
	tx, f, err := s.beginCandidateCommand(ctx, live.raw, live.signature, "unknown", live.marker)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	resource, err := existingCandidateResource(ctx, tx, f.command)
	if err != nil {
		return zero, err
	}
	r, err := reservedCandidate(f, resource)
	if err != nil {
		return zero, err
	}
	var marker candidateClaimMarker
	if !canonicalDataJSON(live.marker, &marker) || !matchCandidateMarker(marker, f, r) || marker.Preflight == nil || *marker.Preflight != candidatePreflight(observed.Launch) {
		return zero, ErrFenceReconciliation
	}
	request, err := candidateNativeRequest(f.command, f.data, nil)
	if err != nil || request.Identity != observed.Launch.Identity || request.Data != observed.Launch.Data || request.Platform != observed.Launch.Platform {
		return zero, ErrFenceReconciliation
	}
	c := f.command
	receipt := CandidateRunningReceipt{Format: candidateReceiptFormat, Claim: marker, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: c.ControlRevision, InstallationID: c.InstallationID, OperationID: c.OperationID, Generation: c.TargetGeneration, WriterFence: c.Payload.WriterFence, PayloadSHA256: c.PayloadSHA256, ReleaseSHA256: c.Payload.ReleaseSHA256, DataNamespaceID: c.Payload.DataNamespaceID, DataFormat: c.Payload.DataFormat, NativeID: observed.NativeID, NativeObservedMS: strconv.FormatInt(observed.ObservedMS, 10), SettledMS: strconv.FormatInt(f.now, 10)}
	if !validCandidateReceipt(receipt) {
		return zero, ErrFenceReconciliation
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, ErrFenceReconciliation
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_resources SET native_id=?,observed_state='running',revision=2,observed_ms=?,updated_ms=? WHERE id=? AND kind='container' AND native_id=? AND observed_state='unknown' AND revision=1 AND observed_ms IS NULL AND ownership_sha256=?`, receipt.NativeID, observed.ObservedMS, f.now, r.ResourceID, candidatePendingPrefix+r.ResourceID, r.OwnershipSHA256)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	changed, err = tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`, string(encoded), f.now, c.EffectID, live.marker)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if err := finalCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() || !time.Now().Before(live.deadline) {
		return zero, ErrCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}
func validCandidateReceipt(r CandidateRunningReceipt) bool {
	if r.Format != candidateReceiptFormat || !validCandidateMarker(r.Claim) || r.Claim.Preflight == nil || !dataSHA(r.NativeID) || !dataSHA(r.PayloadSHA256) || !dataSHA(r.ReleaseSHA256) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.DataNamespaceID} {
		if !enrollmentUUID.MatchString(id) {
			return false
		}
	}
	for _, n := range []string{r.ControlRevision, r.Generation, r.WriterFence, r.DataFormat} {
		if !dataNumber(n, true) {
			return false
		}
	}
	if !dataNumber(r.NativeObservedMS, false) || !dataNumber(r.SettledMS, false) {
		return false
	}
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	observed, _ := strconv.ParseInt(r.NativeObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	expires, _ := strconv.ParseInt(r.Claim.ExpiresMS, 10, 64)
	bound, _ := strconv.ParseInt(r.Claim.PreflightBoundMS, 10, 64)
	return observed >= claimed && observed >= bound && settled >= observed && settled < expires
}

// ParseCandidateRunningReceipt validates canonical metadata only. It never
// authenticates a process or authorizes settlement, recovery or application IO.
func ParseCandidateRunningReceipt(raw []byte) (CandidateRunningReceipt, error) {
	var r CandidateRunningReceipt
	if len(raw) == 0 || len(raw) > 16384 || !canonicalDataJSON(string(raw), &r) || !validCandidateReceipt(r) {
		return CandidateRunningReceipt{}, ErrFenceReconciliation
	}
	return r, nil
}
