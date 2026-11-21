package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformRunnerInstallFormat is the install format the console plans on a runner. The operation engine runs
// only the writable format, so a runner without the writable sandbox gets no runner-backed install at all.
func platformRunnerInstallFormat(profile transport.ManagedRuntimeProfile) (string, error) {
	if profile.WritableSandboxProfile != control.WritableCandidateSandboxProfile {
		return "", errAppPlatformInstallUnavailable
	}
	return platformWritableInstallFormat, nil
}

// createPlatformRunnerInstallReview is the console's runner-backed install review.
func (s *Server) createPlatformRunnerInstallReview(ctx context.Context, session platformActorSession, release string, revision int64, targets []platformAudienceTarget, endpoint platformRuntimeEndpoint) (platformRemovalReview, error) {
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	format, err := platformRunnerInstallFormat(facts.observation.Profile())
	if err != nil {
		return platformRemovalReview{}, err
	}
	return s.createPlatformInstallReviewWithRuntime(ctx, session, release, revision, targets, format, &facts, endpoint)
}

// This separately confirmed development profile permits the writable lifecycle.
// Generic HTTP admission and existing readonly review entry points stay separate.
func (s *Server) createPlatformWritableInstallReview(ctx context.Context, session platformActorSession, release string, revision int64, targets []platformAudienceTarget, endpoint platformRuntimeEndpoint) (platformRemovalReview, error) {
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	return s.createPlatformInstallReviewWithRuntime(ctx, session, release, revision, targets, platformWritableInstallFormat, &facts, endpoint)
}
func (s *Server) consumePlatformWritableInstallReview(ctx context.Context, session platformActorSession, plan, sha, key string, endpoint platformRuntimeEndpoint) (platformRemovalCommit, error) {
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return s.consumePlatformInstallReviewWithRuntime(ctx, session, plan, sha, key, platformWritableInstallFormat, &facts, endpoint)
}
