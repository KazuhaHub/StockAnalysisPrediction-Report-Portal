package journal

import (
	"bytes"
	"context"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// DispatchDataWriter permits only the first prepared attempt or a historical
// applied read. An unknown attempt requires the separate observation-only
// ReconcileDataWriter API and renewed authority; it never repeats permissions IO.
func (s *Store) DispatchDataWriter(ctx context.Context, raw, signature, descriptor []byte, native *driver.CandidateRuntime) (DataWriterReceipt, error) {
	var zero DataWriterReceipt
	if native == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	a, err := s.writableDispatchAuthority(ctx)
	if err != nil {
		return zero, err
	}
	v, err := control.VerifyDataWriter(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != v.Data().Payload.ReleaseSHA256 {
		return zero, ErrFenceReconciliation
	}
	// First-admission source checks intentionally require sealed data. Completed
	// writer replay must use the separate strict revision-3 historical reader.
	if receipt, err := s.ReadDataWriterReceipt(ctx, raw, signature); err == nil {
		return receipt, nil
	}
	admitted, err := s.AdmitDataWriterCommand(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	if admitted.AdmittedFence != admitted.CurrentFence {
		return zero, ErrFenceReconciliation
	}
	switch admitted.State {
	case "prepared":
		claim, err := s.ClaimDataWriter(ctx, raw, signature)
		if err != nil {
			return zero, err
		}
		return s.ExecuteDataWriter(ctx, claim, descriptor, native)
	case "applied":
		return s.ReadDataWriterReceipt(ctx, raw, signature)
	default:
		return zero, ErrFenceReconciliation
	}
}

// DispatchWritableCandidate cannot use a historical or unknown claim as a new
// start permit. Lost replies retain the exact signed identity; recovery is a
// distinct observation-only action, never an implicit create/start retry.
func (s *Store) DispatchWritableCandidate(ctx context.Context, raw, signature, descriptor []byte, native *driver.CandidateRuntime) (WritableCandidateRunningReceipt, error) {
	var zero WritableCandidateRunningReceipt
	if native == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	a, err := s.writableDispatchAuthority(ctx)
	if err != nil {
		return zero, err
	}
	v, err := control.VerifyWritableCandidate(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != v.Index().Payload.ReleaseSHA256 {
		return zero, ErrFenceReconciliation
	}
	// Existing running/reserved history has its own strict current validators;
	// never weaken first admission to accept a later lifecycle resource shape.
	if receipt, err := s.ReadWritableCandidateRunningReceipt(ctx, raw, signature); err == nil {
		return receipt, nil
	}
	if _, err := s.ReadWritableCandidateReservation(ctx, raw, signature); err == nil {
		claim, err := s.ClaimWritableCandidate(ctx, raw, signature)
		if err != nil {
			return zero, err
		}
		return s.ExecuteWritableCandidate(ctx, claim, native, descriptor)
	}
	admitted, err := s.AdmitWritableCandidateCommand(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	if admitted.AdmittedFence != admitted.CurrentFence {
		return zero, ErrFenceReconciliation
	}
	switch admitted.State {
	case "prepared":
		if _, err := s.ReserveWritableCandidate(ctx, raw, signature); err != nil {
			return zero, err
		}
		claim, err := s.ClaimWritableCandidate(ctx, raw, signature)
		if err != nil {
			return zero, err
		}
		return s.ExecuteWritableCandidate(ctx, claim, native, descriptor)
	case "applied":
		return s.ReadWritableCandidateRunningReceipt(ctx, raw, signature)
	default:
		return zero, ErrFenceReconciliation
	}
}
func (s *Store) writableDispatchAuthority(ctx context.Context) (control.Authority, error) {
	d, err := s.ControllerDeployment(ctx)
	return control.Authority{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, PublicKey: d.PublicKey, Fingerprint: d.Fingerprint}, err
}
