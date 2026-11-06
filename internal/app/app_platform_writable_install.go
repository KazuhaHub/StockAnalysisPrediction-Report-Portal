package app

import "context"

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
