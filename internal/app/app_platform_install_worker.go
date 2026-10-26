package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type platformStaticInstallRun struct {
	lease      platformOperationLease
	activation platformActivationResult
}

// runPlatformStaticInstall advances one already-claimed initial static install.
// Each committed stage replaces the exact lease token. Failure returns the last
// confirmed token; a lost acknowledgement still requires durable reconciliation.
// This bounded driver does not enroll a runtime, poll, renew or reclaim leases.
func (s *Server) runPlatformStaticInstall(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformStaticInstallRun, error) {
	result := platformStaticInstallRun{lease: expected}
	if expected.snapshot.Action != appplatform.ActionInstall || expected.snapshot.Managed || expected.snapshot.Generation != 1 {
		return result, errAppPlatformStageEvidence
	}
	// Four artifact transitions, one data transition and one activation.
	for range 6 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var next platformOperationLease
		var err error
		switch result.lease.snapshot.Stage {
		case appplatform.StageQueued, appplatform.StageValidate, appplatform.StageDownload, appplatform.StageStage:
			next, err = s.st.advancePlatformInstallStagingContext(ctx, result.lease)
		case appplatform.StagePrepareData:
			next, err = s.dispatchPlatformInstallData(ctx, result.lease, endpoint)
		case appplatform.StageActivate:
			result.activation, err = s.activatePlatformStaticInstall(ctx, result.lease)
			if err == nil {
				result.lease = platformOperationLease{}
			}
			return result, err
		default:
			return result, errAppPlatformStageEvidence
		}
		if err != nil {
			return result, err
		}
		result.lease = next
	}
	return result, errAppPlatformStageEvidence
}
