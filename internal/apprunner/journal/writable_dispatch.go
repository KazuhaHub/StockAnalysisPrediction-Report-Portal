package journal

import (
	"bytes"
	"context"
	"errors"
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
		if err := requireBackendImage(ctx, native, descriptor, parsed.SHA256); err != nil {
			return zero, err
		}
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
//
// An unknown start whose command window ended is looked at, not resent: with the
// execution gate free and the container proven absent it is settled as never run
// (SettleWritableCandidateOrphan) and answered with *CandidateOrphanSettledError.
// The runner's writable candidate start action calls this (transport/writable_lifecycle).
func (s *Store) DispatchWritableCandidate(ctx context.Context, raw, signature, descriptor []byte, native *driver.CandidateRuntime) (WritableCandidateRunningReceipt, error) {
	if native == nil {
		return WritableCandidateRunningReceipt{}, ErrFenceReconciliation
	}
	return s.dispatchWritableCandidate(ctx, raw, signature, descriptor, native, candidateInspectorOf(native))
}

// dispatchWritableCandidate is DispatchWritableCandidate with the native look at an unknown start as its own seam.
func (s *Store) dispatchWritableCandidate(ctx context.Context, raw, signature, descriptor []byte, native *driver.CandidateRuntime, inspector CandidateTargetInspector) (WritableCandidateRunningReceipt, error) {
	var zero WritableCandidateRunningReceipt
	if (native == nil && inspector == nil) || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
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
	ctx = withBackendResources(ctx, parsed.Manifest.Backend.Resources)
	// Existing running/reserved history has its own strict current validators;
	// never weaken first admission to accept a later lifecycle resource shape.
	if receipt, err := s.ReadWritableCandidateRunningReceipt(ctx, raw, signature); err == nil {
		return receipt, nil
	}
	if _, err := s.ReadWritableCandidateReservation(ctx, raw, signature); err == nil {
		if native == nil {
			return zero, ErrFenceReconciliation
		}
		if err := requireBackendImage(ctx, native, descriptor, parsed.SHA256); err != nil {
			return zero, err
		}
		claim, err := s.ClaimWritableCandidate(ctx, raw, signature)
		if err != nil {
			return zero, err
		}
		return s.ExecuteWritableCandidate(ctx, claim, native, descriptor)
	}
	// A start settled as never run keeps its (absent) resource row, which first admission refuses, so its typed
	// answer is read before admission, under the signature verified above.
	if answer := s.lookupRevertedAnswer(ctx, CapacityIdentityOfCommand(candidateIndex(v.Index()))); answer != nil {
		return zero, answer
	}
	admitted, err := s.AdmitWritableCandidateCommand(ctx, raw, signature)
	if err != nil {
		// First admission refuses a candidate whose resource already exists, which is what an unknown start has.
		// That is where a start claimed and never finished is looked at.
		if errors.Is(err, ErrFenceReconciliation) && inspector != nil {
			return zero, s.settleUnknownWritableCandidate(ctx, raw, signature, inspector, err)
		}
		return zero, err
	}
	if admitted.AdmittedFence != admitted.CurrentFence {
		return zero, ErrFenceReconciliation
	}
	switch admitted.State {
	case "prepared":
		if native == nil {
			return zero, ErrFenceReconciliation
		}
		if err := requireBackendImage(ctx, native, descriptor, parsed.SHA256); err != nil {
			return zero, err
		}
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
	case "reverted":
		return zero, s.capacityRefusalFor(ctx, CapacityIdentityOfCommand(candidateIndex(v.Index())))
	default:
		// Unknown: never resent. After its window ended it may be proven never to have run.
		if admitted.State != "unknown" || inspector == nil {
			return zero, ErrFenceReconciliation
		}
		return zero, s.settleUnknownWritableCandidate(ctx, raw, signature, inspector, ErrFenceReconciliation)
	}
}

// settleUnknownWritableCandidate tries the orphan settlement and answers with its typed outcome. A start it cannot
// settle keeps the cause the caller already had, unless the settlement says why (a container exists, a start may be
// executing, the window is open).
func (s *Store) settleUnknownWritableCandidate(ctx context.Context, raw, signature []byte, inspector CandidateTargetInspector, cause error) error {
	receipt, err := s.SettleWritableCandidateOrphan(ctx, raw, signature, inspector)
	switch {
	case err == nil:
		return &CandidateOrphanSettledError{Receipt: receipt}
	case errors.Is(err, ErrCandidateOrphanPresent), errors.Is(err, ErrCandidateOrphanBusy), errors.Is(err, ErrCandidateOrphanWindow):
		return err
	}
	return cause
}
func (s *Store) writableDispatchAuthority(ctx context.Context) (control.Authority, error) {
	d, err := s.ControllerDeployment(ctx)
	return control.Authority{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, PublicKey: d.PublicKey, Fingerprint: d.Fingerprint}, err
}
