package journal

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// DispatchCandidate routes a signed request to a first native attempt or a
// historical applied receipt. Unknown work never reenters create/start. A start
// that is unknown after its command window ended is looked at, not resent: with
// the execution gate free and the container proven absent it is settled as never
// run (SettleCandidateOrphan) and answered with *CandidateOrphanSettledError; in
// every other case it stays unknown and the answer is ErrFenceReconciliation.
// The runner's candidate execute action calls this (transport/candidate_receipt).
func (s *Store) DispatchCandidate(ctx context.Context, raw, signature []byte, native *driver.CandidateRuntime, descriptor []byte) (CandidateRunningReceipt, error) {
	return s.dispatchCandidate(ctx, raw, signature, native, candidateInspectorOf(native), descriptor)
}

// dispatchCandidate is DispatchCandidate with the native look at an unknown start as its own seam.
func (s *Store) dispatchCandidate(ctx context.Context, raw, signature []byte, native *driver.CandidateRuntime, inspector CandidateTargetInspector, descriptor []byte) (CandidateRunningReceipt, error) {
	var zero CandidateRunningReceipt
	if s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return zero, ErrCandidateClaimUnavailable
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil {
		return zero, ErrCandidateClaimUnavailable
	}
	// Check exact immutable bytes before admitting durable work. The fields below
	// are untrusted until AdmitCandidateCommand verifies the complete signature.
	var untrusted control.CandidateCommand
	if json.Unmarshal(raw, &untrusted) != nil || parsed.SHA256 != untrusted.Payload.ReleaseSHA256 {
		return zero, ErrCandidateClaimUnavailable
	}
	ctx = withBackendResources(ctx, parsed.Manifest.Backend.Resources)
	admission, err := s.AdmitCandidateCommand(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	if admission.AdmittedFence != admission.CurrentFence {
		return zero, ErrFenceReconciliation
	}
	switch admission.State {
	case "prepared":
		if native == nil {
			return zero, ErrCandidateClaimUnavailable
		}
		if err := requireBackendImage(ctx, native, descriptor, parsed.SHA256); err != nil {
			return zero, err
		}
		if _, err := s.ReserveCandidate(ctx, raw, signature); err != nil {
			return zero, err
		}
		claim, err := s.ClaimCandidate(ctx, raw, signature)
		if err != nil {
			return zero, err
		}
		return s.ExecuteCandidate(ctx, claim, native, descriptor)
	case "applied":
		return s.ReadCandidateRunningReceipt(ctx, raw, signature)
	case "reverted":
		return zero, s.capacityRefusalFor(ctx, CapacityIdentityOfCommand(candidateIndex(untrusted)))
	default:
		// Unknown: never resent. After its window ended it may be proven never to have run.
		if admission.State != "unknown" || inspector == nil {
			return zero, ErrFenceReconciliation
		}
		receipt, err := s.SettleCandidateOrphan(ctx, raw, signature, inspector)
		if err != nil {
			return zero, err
		}
		return zero, &CandidateOrphanSettledError{Receipt: receipt}
	}
}
