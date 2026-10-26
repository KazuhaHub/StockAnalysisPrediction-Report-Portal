package journal

import (
	"bytes"
	"context"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// DispatchPrepareData is the trusted infrastructure orchestrator for one signed
// data request. The configured concrete root/pin never comes from the command.
// Prepared work can acquire a first claim; unknown work can only be inspected.
// An error may follow durable or physical effects and never authorizes a retry
// with another effect identity or a replacement namespace.
func (s *Store) DispatchPrepareData(ctx context.Context, raw, signature []byte, root *data.Root, pin data.RootIdentity) (DataPreparationReceipt, error) {
	var zero DataPreparationReceipt
	if root == nil || !validDataRoot(pin) || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return zero, ErrDataClaimUnavailable
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	actual, err := root.Identity(ctx)
	if err != nil {
		return zero, err
	}
	if actual != pin {
		return zero, data.ErrUnsafe
	}
	admission, err := s.AdmitControlCommand(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	if admission.AdmittedFence != admission.CurrentFence {
		return zero, ErrFenceReconciliation
	}
	switch admission.State {
	case "prepared":
		// A reservation is not execution authority. ClaimPrepareData durably changes
		// the command to unknown before its one-shot live handle can reach native IO.
		if _, err = s.ReservePrepareData(ctx, raw, signature); err != nil {
			return zero, err
		}
		claim, err := s.ClaimPrepareData(ctx, raw, signature)
		if err != nil {
			return zero, err
		}
		return s.ExecutePrepareData(ctx, claim, root, pin)
	case "unknown":
		return s.ReconcilePrepareData(ctx, raw, signature, root, pin)
	case "applied":
		receipt, err := s.ReadDataPreparationReceipt(ctx, raw, signature)
		if err != nil {
			return zero, err
		}
		if receipt.Claim.RootFilesystemUUID != pin.FilesystemUUID || receipt.Claim.RootInode != strconv.FormatUint(pin.Inode, 10) {
			return zero, ErrFenceReconciliation
		}
		return receipt, nil
	default:
		return zero, ErrFenceReconciliation
	}
}
