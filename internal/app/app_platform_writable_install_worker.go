package app

import (
	"context"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// runPlatformWritableInstall drives an already claimed first installation. Each
// stage retains its own current-authority and concrete observation checks. This
// driver neither claims/renews leases nor reconciles uncertain native effects.
func (s *Server) runPlatformWritableInstall(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformInstallRun, error) {
	return s.runPlatformWritableInstallMode(ctx, expected, endpoint, false)
}

func (s *Server) runPlatformWritableInstallMode(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint, waitReady bool) (platformInstallRun, error) {
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
			if waitReady {
				next, err = s.waitPlatformWritableCandidate(ctx, result.lease, endpoint)
			} else {
				next, err = s.probePlatformWritableCandidate(ctx, result.lease, endpoint)
			}
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

// Only readiness observation is polled. Writer/start/delivery are never retried
// here. Each probe repeats current source/authority checks before and after IO.
func (s *Server) waitPlatformWritableCandidate(parent context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return platformOperationLease{}, err
		}
		i, err := s.preparePlatformWritableProbe(ctx, lease)
		if err != nil {
			return platformOperationLease{}, err
		}
		if err := s.checkPlatformManagedRuntimeRoot(ctx, lease, i.writer.runtime, i.writer.authority, endpoint); err != nil {
			return platformOperationLease{}, err
		}
		proof, err := platformRunWritableStart(ctx, endpoint, i, transport.WritableCandidateProbe)
		if err == nil {
			// Commit exactly once. A SQL/source failure is not startup latency.
			return s.commitPlatformWritableReadiness(ctx, lease, i, endpoint, proof)
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return platformOperationLease{}, ctx.Err()
		case <-timer.C:
		}
	}
}
