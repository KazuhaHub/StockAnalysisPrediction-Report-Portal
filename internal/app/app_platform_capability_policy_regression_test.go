package app

import (
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A consumed capability review must not change what the other lifecycle readers
// of the installation decide: a removal review reads the grant rows and the
// installation, and is admitted exactly as it was before the review.
func testPlatformCapabilityRemovalReviewUnchanged(t *testing.T, pg bool) {
	s, actor, installation := platformCapabilityFixture(t, pg)
	platformCapabilityRunnerFor(t, s, installation, nil)
	_, beforeErr := s.createPlatformRemovalReview(actor, installation, appplatform.ActionDisable)
	plan, err := platformCapabilityCreate(t, s, actor, installation, []appplatform.HTTPTarget{platformV2ExtraTarget()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platformCapabilityConsume(t, s, actor, plan); err != nil {
		t.Fatal(err)
	}
	review, afterErr := s.createPlatformRemovalReview(actor, installation, appplatform.ActionDisable)
	if (beforeErr == nil) != (afterErr == nil) {
		t.Fatal("a consumed capability review changed removal admission", beforeErr, afterErr)
	}
	if afterErr == nil && review.ID == "" {
		t.Fatal("empty review")
	}
}

func TestPlatformCapabilityRemovalReviewUnchangedSQLite(t *testing.T) {
	testPlatformCapabilityRemovalReviewUnchanged(t, false)
}
func TestPlatformCapabilityRemovalReviewUnchangedPostgres(t *testing.T) {
	testPlatformCapabilityRemovalReviewUnchanged(t, true)
}
