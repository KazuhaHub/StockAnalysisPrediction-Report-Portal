package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// CandidateReadiness is transient root-observed evidence, not a persistent ready
// receipt, writer authorization, registration or application activation.
type CandidateReadiness struct {
	Format               string `json:"format"`
	PayloadSHA256        string `json:"payload_sha256"`
	RunningReceiptSHA256 string `json:"running_receipt_sha256"`
	NativeID             string `json:"native_id"`
	Protocol             string `json:"protocol"`
	DataFormat           string `json:"data_format"`
	ObservedMS           string `json:"observed_ms"`
}

func ParseCandidateReadiness(raw []byte) (CandidateReadiness, error) {
	var r CandidateReadiness
	if len(raw) == 0 || len(raw) > 4096 || json.Unmarshal(raw, &r) != nil {
		return r, driver.ErrCandidateNotReady
	}
	canonical, err := json.Marshal(r)
	if err != nil || !bytes.Equal(canonical, raw) || r.Format != "portal.runner.candidate-readiness/1-dev.1" || !dataSHA(r.PayloadSHA256) || !dataSHA(r.RunningReceiptSHA256) || !dataSHA(r.NativeID) || r.Protocol != "portal.backend/1" || !dataNumber(r.DataFormat, true) || !dataNumber(r.ObservedMS, false) {
		return CandidateReadiness{}, driver.ErrCandidateNotReady
	}
	return r, nil
}

func (s *Store) ObserveCandidateReadiness(ctx context.Context, raw, signature []byte, native *driver.CandidateRuntime, descriptor []byte) (CandidateReadiness, error) {
	var zero CandidateReadiness
	if native == nil {
		return zero, driver.ErrCandidateNotReady
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	r, err := s.ReadCandidateRunningReceipt(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	var c control.CandidateCommand
	if json.Unmarshal(raw, &c) != nil {
		return zero, driver.ErrCandidateNotReady
	}
	p := r.Claim.Preflight
	project, _ := strconv.ParseUint(p.ProjectID, 10, 32)
	device, _ := strconv.ParseUint(p.Device, 10, 64)
	inode, _ := strconv.ParseUint(p.NamespaceInode, 10, 64)
	quota, _ := strconv.ParseInt(p.QuotaBytes, 10, 64)
	inodes, _ := strconv.ParseInt(p.QuotaInodes, 10, 64)
	rootInode, _ := strconv.ParseUint(p.RootInode, 10, 64)
	request := driver.CandidateLaunchRequest{Identity: driver.CandidateIdentity{DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID, ResourceID: c.Payload.ResourceID, NamespaceID: c.Payload.DataNamespaceID, ReleaseSHA256: c.Payload.ReleaseSHA256, Generation: c.TargetGeneration, ControlFence: c.InstallationFence, WriterFence: c.Payload.WriterFence}, Descriptor: descriptor, Platform: c.Payload.Platform, Data: driver.CandidateDataEvidence{ProjectID: uint32(project), Device: device, NamespaceInode: inode, QuotaBytes: quota, QuotaInodes: inodes}}
	request.Data.Root.FilesystemUUID, request.Data.Root.Inode = p.RootFilesystemUUID, rootInode
	launch, err := native.PrepareCandidate(ctx, request)
	if err != nil {
		return zero, err
	}
	defer launch.Close()
	preflight, err := launch.Snapshot()
	if err != nil || candidatePreflight(preflight) != *p {
		return zero, driver.ErrCandidateNotReady
	}
	observation, err := native.CheckCandidateReadiness(ctx, launch, r.NativeID, c.Payload.DataFormat)
	if err != nil {
		return zero, err
	}
	observed, err := observation.Snapshot()
	if err != nil || observed.NativeID != r.NativeID || observed.Launch.Identity != request.Identity || candidatePreflight(observed.Launch) != *p || observed.DataFormat != c.Payload.DataFormat {
		return zero, driver.ErrCandidateNotReady
	}
	// IO happens outside SQL transactions. Recheck exact current enrolled authority,
	// original data/resource history and durable floors after the native probe.
	after, err := s.ReadCandidateRunningReceipt(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	beforeJSON, _ := json.Marshal(r)
	afterJSON, _ := json.Marshal(after)
	if !bytes.Equal(beforeJSON, afterJSON) || ctx.Err() != nil {
		return zero, driver.ErrCandidateNotReady
	}
	result := CandidateReadiness{Format: "portal.runner.candidate-readiness/1-dev.1", PayloadSHA256: c.PayloadSHA256, RunningReceiptSHA256: candidateHash(beforeJSON), NativeID: r.NativeID, Protocol: observed.Protocol, DataFormat: observed.DataFormat, ObservedMS: strconv.FormatInt(observed.ObservedMS, 10)}
	encoded, _ := json.Marshal(result)
	return ParseCandidateReadiness(encoded)
}
