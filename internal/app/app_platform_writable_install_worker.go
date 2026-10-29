package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// runPlatformWritableInstall drives an already claimed first installation. Each
// stage retains its own current-authority and concrete observation checks. This
// driver neither claims/renews leases nor reconciles uncertain native effects.
func (s *Server) runPlatformWritableInstall(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformInstallRun, error) {
	result := platformInstallRun{lease: expected}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if expected.snapshot.Action != appplatform.ActionInstall || !expected.snapshot.Managed || expected.snapshot.Generation != 1 || endpoint.path == "" {
		return result, errAppPlatformStageEvidence
	}
	profile, err := s.st.platformInstallProfile(ctx, expected.id)
	if err != nil {
		return result, err
	}
	if profile != platformWritableInstallFormat {
		return result, errAppPlatformStageEvidence
	}
	// Four artifact transitions, data, writer/start, readiness, registration/boot.
	for range 8 {
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
		case appplatform.StageStart:
			next, err = s.startPlatformWritableInstall(ctx, result.lease, endpoint)
		case appplatform.StageProbe:
			next, err = s.probePlatformWritableCandidate(ctx, result.lease, endpoint)
		case appplatform.StageActivate:
			next, err = s.dispatchPlatformWritableRegistration(ctx, result.lease, endpoint)
			if err != nil {
				return result, err
			}
			// Registration can commit a new row revision before boot/activation
			// fails. Return that token so the next attempt cannot reuse the old one.
			result.lease = next
			result.activation, err = s.activatePlatformWritableInstall(ctx, next, endpoint)
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

func (s *Server) startPlatformWritableInstall(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	// Routing hint only: a prepared start follows an already applied writer.
	// Re-entering the original two-effect writer reader would reject that third
	// effect. The start path independently verifies the complete current writer
	// and exact sole start source before any native IO; this count grants nothing.
	var starts int
	if err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`), lease.id).Scan(&starts); err != nil {
		return platformOperationLease{}, err
	}
	if starts == 0 {
		if _, err := s.dispatchPlatformInstallWriter(ctx, lease, endpoint); err != nil {
			return platformOperationLease{}, err
		}
	} else if starts != 1 {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	return s.dispatchPlatformWritableStart(ctx, lease, endpoint)
}
