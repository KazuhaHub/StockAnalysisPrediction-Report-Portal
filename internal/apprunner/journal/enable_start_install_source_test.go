//go:build linux || darwin

package journal

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// An enable or an import start of an installation whose latest lifecycle source is its install replaces the install's own
// stopped backend: the one that carries the candidate claim labels, generation 1 and writer fence 1. The native request
// names it, as it names an update's source, so the start can remove it before it provisions the new one.
func TestEnableStartOfAnInstallSourcedInstallationNamesTheInstallBackendAsItsSource(t *testing.T) {
	ctx := context.Background()
	f, _, start, _, _ := updateStartJournalFixture(t)
	start.Kind = "enable-start"
	start.Payload.TargetReleaseSHA256 = start.Payload.SourceReleaseSHA256
	raw, sig := resignUpdateStart(t, start)
	if _, err := f.store.ReserveUpdateStart(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimUpdateStart(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	tx, facts, err := f.store.beginUpdateStartExecution(ctx, raw, sig, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var marker UpdateStartClaimMarker
	var markerRaw string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands WHERE effect_id=?`, start.EffectID).Scan(&markerRaw); err != nil || !canonicalDataJSON(markerRaw, &marker) {
		t.Fatal("claim marker", err)
	}
	request, err := updateStartNativeRequest(facts, marker, nil)
	if err != nil {
		t.Fatalf("an enable-start over an install source has no native request: %v", err)
	}
	source := request.Source
	if !request.ReplaceSource || source == nil || source.ResourceID != f.source.Claim.ResourceID || source.NativeID != f.source.NativeID ||
		source.Generation != "1" || source.WriterFence != "1" || source.OperationID != start.Payload.SourceOperationID ||
		source.Origin != driver.UpdateStartSourceCandidate {
		t.Fatalf("the install backend is not the start's source: %+v", source)
	}
}
