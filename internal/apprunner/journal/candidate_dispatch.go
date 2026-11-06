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
// historical applied receipt. Unknown work never reenters create/start: its
// separately authorized native reconciliation path remains to be implemented.
// This infrastructure entry point is not connected to a production endpoint.
func (s *Store) DispatchCandidate(ctx context.Context, raw, signature []byte, native *driver.CandidateRuntime, descriptor []byte) (CandidateRunningReceipt, error) {
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
	default:
		return zero, ErrFenceReconciliation
	}
}
