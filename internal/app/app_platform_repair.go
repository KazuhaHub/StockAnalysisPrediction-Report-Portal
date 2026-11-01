package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const platformManagedRepairFormat = "portal.managed-repair-review/1-dev.1"

func platformSameReleaseLifecycle(action appplatform.Action) bool {
	return action == appplatform.ActionEnable ||
		action == appplatform.ActionRepair
}

func platformSameReleaseLifecycleFormat(action appplatform.Action) string {
	if action == appplatform.ActionRepair {
		return platformManagedRepairFormat
	}
	return platformManagedEnableFormat
}

func platformSameReleaseCheckpointStage(action appplatform.Action) appplatform.Stage {
	if action == appplatform.ActionRepair {
		return appplatform.StageQuiesce
	}
	return appplatform.StageSnapshot
}

func platformSameReleaseCheckpointEffectStage(action appplatform.Action) string {
	if action == appplatform.ActionRepair {
		return string(appplatform.StageQuiesce)
	}
	return string(appplatform.StageSnapshot)
}

func platformSameReleaseStartKind(action appplatform.Action) string {
	return "enable-start"
}

//lint:ignore U1000 explicit managed lifecycle development entry point
func (s *Server) createPlatformManagedRepairReviewContext(
	ctx context.Context,
	session platformActorSession,
	installation string,
	endpoint platformRuntimeEndpoint,
) (platformRemovalReview, error) {
	facts, err := s.observePlatformManagedRuntime(
		ctx, session, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	target, err := managedInstallTarget(facts, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	return s.createPlatformManagedSameReleaseReviewWithRuntimeContext(
		ctx, session, installation, target, facts.runtime,
		appplatform.ActionRepair)
}

func (s *Server) createPlatformManagedRepairReviewWithRuntimeContext(
	ctx context.Context,
	session platformActorSession,
	installation string,
	target *platformManagedInstallTarget,
	observedRuntime platformRuntimeRegistration,
) (platformRemovalReview, error) {
	return s.createPlatformManagedSameReleaseReviewWithRuntimeContext(
		ctx, session, installation, target, observedRuntime,
		appplatform.ActionRepair)
}

//lint:ignore U1000 explicit managed lifecycle development entry point
func (s *Server) consumePlatformManagedRepairReviewContext(
	ctx context.Context,
	session platformActorSession,
	planID, planSHA, key string,
	endpoint platformRuntimeEndpoint,
) (platformRemovalCommit, error) {
	facts, err := s.observePlatformManagedRuntime(
		ctx, session, endpoint)
	if err != nil {
		return platformRemovalCommit{}, err
	}
	target, err := managedInstallTarget(facts, endpoint)
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return s.consumePlatformManagedRepairReviewWithRuntimeContext(
		ctx, session, planID, planSHA, key,
		target, facts.runtime)
}

func (s *Server) consumePlatformManagedRepairReviewWithRuntimeContext(
	ctx context.Context,
	session platformActorSession,
	planID, planSHA, key string,
	target *platformManagedInstallTarget,
	observedRuntime platformRuntimeRegistration,
) (platformRemovalCommit, error) {
	return s.consumePlatformManagedSameReleaseReviewWithRuntimeContext(
		ctx, session, planID, planSHA, key, target, observedRuntime,
		appplatform.ActionRepair)
}

func (s *Server) claimNextPlatformManagedRepair(
	ctx context.Context, worker string,
) (platformInstallSelection, error) {
	return s.claimNextPlatformManagedSameRelease(
		ctx, worker, appplatform.ActionRepair)
}

func (s *Server) runPlatformManagedRepair(
	ctx context.Context,
	lease platformOperationLease,
	endpoint platformRuntimeEndpoint,
) (platformOperationLease, error) {
	return s.runPlatformManagedSameRelease(ctx, lease, endpoint)
}

func (s *Server) finishPlatformManagedRepairForReview(
	ctx context.Context, lease platformOperationLease,
) error {
	return s.finishPlatformManagedSameReleaseForReview(ctx, lease)
}
