package app

import (
	"context"
	"encoding/json"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (s *Server) observePlatformNetworkInstallRuntime(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint, policy string) (platformManagedRuntimeFacts, error) {
	if _, err := appplatform.ParseHTTPPolicy([]byte(policy)); err != nil {
		return platformManagedRuntimeFacts{}, errAppPlatformInstallUnavailable
	}
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformManagedRuntimeFacts{}, err
	}
	network, err := s.observePlatformNetworkRuntime(ctx, session, endpoint)
	if err != nil {
		return platformManagedRuntimeFacts{}, err
	}
	facts.network = &network
	facts.networkPolicy = policy
	if _, err := managedInstallTarget(facts, endpoint); err != nil {
		return platformManagedRuntimeFacts{}, err
	}
	return facts, nil
}

// Development entry point: a current administrator explicitly selects the
// canonical restriction and audience. No app or generic frontend route may
// supply a destination, runtime endpoint or observation to this repository.
func (s *Server) createPlatformNetworkWritableInstallReview(ctx context.Context, session platformActorSession, release string, revision int64, targets []platformAudienceTarget, endpoint platformRuntimeEndpoint, policy appplatform.HTTPPolicy) (platformRemovalReview, error) {
	raw, err := appplatform.CanonicalHTTPPolicy(policy)
	if err != nil {
		return platformRemovalReview{}, errAppPlatformInstallUnavailable
	}
	facts, err := s.observePlatformNetworkInstallRuntime(ctx, session, endpoint, string(raw))
	if err != nil {
		return platformRemovalReview{}, err
	}
	return s.createPlatformInstallReviewWithRuntime(ctx, session, release, revision, targets, platformWritableInstallFormat, &facts, endpoint)
}
func (s *Server) consumePlatformNetworkWritableInstallReview(ctx context.Context, session platformActorSession, plan, sha, key string, endpoint platformRuntimeEndpoint) (platformRemovalCommit, error) {
	if !platformValidUUID(plan) || !platformValidSHA(sha) || !platformValidUUID(key) {
		return platformRemovalCommit{}, errAppPlatformPlanChanged
	}
	// Routing hint only; the authoritative consumer repeats actor, full canonical
	// review/hash, signature/trust/runtime/audience and current profile checks.
	var raw string
	if err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT plan_json FROM app_platform_review_plans WHERE id=?`), plan).Scan(&raw); err != nil || len(raw) > 65536 {
		return platformRemovalCommit{}, errAppPlatformPlanChanged
	}
	var p platformInstallProjection
	if json.Unmarshal([]byte(raw), &p) != nil || p.Format != platformWritableInstallFormat || p.Runtime == nil || p.Runtime.NetworkPolicy == "" {
		return platformRemovalCommit{}, errAppPlatformPlanChanged
	}
	facts, err := s.observePlatformNetworkInstallRuntime(ctx, session, endpoint, p.Runtime.NetworkPolicy)
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return s.consumePlatformInstallReviewWithRuntime(ctx, session, plan, sha, key, platformWritableInstallFormat, &facts, endpoint)
}
