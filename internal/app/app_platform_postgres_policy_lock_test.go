package app

import (
	"context"
	"testing"
	"time"
)

// Both calls of the review ask the runner before they open their transaction: while the runner
// answers (slowly here) no database connection, so no publisher-trust lock and no row lock, is held.
func testPlatformPostgresReviewDoesNotHoldLocksWhileAskingTheRunner(t *testing.T, pg bool) {
	s, admin, installation, state, target := platformPostgresFixture(t, pg, false)
	asked, held := 0, 0
	s.platformObservePostgres = func(ctx context.Context, e platformRuntimeEndpoint, need bool) (platformPostgresObservation, error) {
		asked++
		time.Sleep(50 * time.Millisecond)
		held += s.st.db.Stats().InUse
		return state.observe(ctx, e, need)
	}
	plan, err := s.createPlatformPostgresPolicyReview(context.Background(), admin, platformRuntimeEndpoint{}, installation, platformPostgresRequestFor(t, s, installation, target))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformPostgresPolicyReview(context.Background(), admin, platformRuntimeEndpoint{}, installation, plan.ID, plan.SHA256); err != nil {
		t.Fatal(err)
	}
	if asked != 2 || held != 0 {
		t.Fatalf("the runner was asked %d times with %d connections held", asked, held)
	}
}

func TestPlatformPostgresReviewDoesNotHoldLocksWhileAskingTheRunnerSQLite(t *testing.T) {
	t.Parallel()
	testPlatformPostgresReviewDoesNotHoldLocksWhileAskingTheRunner(t, false)
}
func TestPlatformPostgresReviewDoesNotHoldLocksWhileAskingTheRunnerPostgres(t *testing.T) {
	t.Parallel()
	testPlatformPostgresReviewDoesNotHoldLocksWhileAskingTheRunner(t, true)
}
