//go:build linux || darwin

package journal

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// enableStartRequestFor reserves and claims an enable-start over the install-sourced fixture, lets the caller change the
// journal first, and returns the native request the start builds.
func enableStartRequestFor(t *testing.T, change func(f checkpointFixture, start control.UpdateStartCommand)) (checkpointFixture, control.UpdateStartCommand, driver.UpdateStartLaunchRequest) {
	t.Helper()
	ctx := context.Background()
	f, _, start, _, _ := updateStartJournalFixture(t)
	start.Kind = "enable-start"
	start.Payload.TargetReleaseSHA256 = start.Payload.SourceReleaseSHA256
	raw, sig := resignUpdateStart(t, start)
	if change != nil {
		change(f, start)
	}
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
	return f, start, request
}

// An enable or an import start of an installation whose latest lifecycle source is its install replaces the install's own
// stopped backend: the one that carries the candidate claim labels, generation 1 and the writer fence of its start receipt.
// The native request names it, as it names an update's source, so the start can remove it before it provisions the new one.
func TestEnableStartOfAnInstallSourcedInstallationNamesTheInstallBackendAsItsSource(t *testing.T) {
	f, start, request := enableStartRequestFor(t, nil)
	source := request.Source
	if !request.ReplaceSource || source == nil || source.ResourceID != f.source.Claim.ResourceID || source.NativeID != f.source.NativeID ||
		source.Generation != "1" || source.WriterFence != f.source.WriterFence || source.OperationID != start.Payload.SourceOperationID ||
		source.ReleaseSHA256 != f.source.ReleaseSHA256 || source.Origin != driver.UpdateStartSourceCandidate ||
		source.ControlFence != "1" || source.SandboxProfile != driver.UpdateStartReadOnlySandboxProfile {
		t.Fatalf("the install backend is not the start's source: %+v", source)
	}
}

// A source the journal records as absent (revision 4) is not replaced: removal would find no container and leave the start
// unknown for good. The start proceeds with nothing to remove.
func TestEnableStartDoesNotReplaceASourceTheJournalRecordsAsAbsent(t *testing.T) {
	_, _, request := enableStartRequestFor(t, func(f checkpointFixture, _ control.UpdateStartCommand) {
		mustJournalExec(t, f.store.db, `UPDATE runner_resources SET desired_state='absent',observed_state='absent',revision=4 WHERE id=?`, f.source.Claim.ResourceID)
	})
	if request.ReplaceSource || request.Source != nil {
		t.Fatalf("an absent source was named for removal: %+v", request.Source)
	}
}

// The decision is the journal's own record of the source container, whichever start produced it.
func TestEnableStartSourceRecordedAbsentReadsTheJournalRecordOnly(t *testing.T) {
	for name, source := range map[string]checkpointSource{
		"update":  {update: &UpdateStartRunningReceipt{}},
		"install": {},
	} {
		source.resource = managedRemovalResource{revision: 3, observed: "stopped", desired: "stopped"}
		if enableStartSourceRecordedAbsent(source) {
			t.Fatalf("%s: a stopped source was read as absent", name)
		}
		source.resource = managedRemovalResource{revision: 4, observed: "absent", desired: "absent"}
		if !enableStartSourceRecordedAbsent(source) {
			t.Fatalf("%s: an absent source was not read as absent", name)
		}
		source.resource = managedRemovalResource{revision: 4, observed: "stopped", desired: "absent"}
		if enableStartSourceRecordedAbsent(source) {
			t.Fatalf("%s: a half-recorded removal was read as absent", name)
		}
	}
}

// The writable install backend is named with the claim it was started under, generation 1 and fence 1.
func TestEnableStartNamesTheWritableInstallBackendAsItsSource(t *testing.T) {
	_, _, _, receipt := syntheticWritableRunningFixture(t)
	source := checkpointSource{writable: &receipt, sourceFence: 1}
	source.command.Payload.SourceOperationID = fixtureID(9101)
	source.command.Payload.ReleaseSHA256 = fixtureID(9102)
	source.data.WriterFence = "1"
	named, err := enableStartNativeSource(source)
	if err != nil {
		t.Fatal(err)
	}
	binding := named.Writable
	if named.Origin != driver.UpdateStartSourceWritable || named.Generation != "1" || named.WriterFence != "1" ||
		named.ResourceID != receipt.Claim.ResourceID || named.NativeID != receipt.NativeID ||
		named.SandboxProfile != driver.UpdateStartWritableSandboxProfile || binding == nil ||
		binding.EffectID != receipt.Claim.EffectID || binding.ClaimID != receipt.Claim.ClaimID ||
		binding.PayloadSHA256 != receipt.Claim.PayloadSHA256 || binding.OwnershipSHA256 != receipt.Claim.OwnershipSHA256 ||
		binding.DataReceiptSHA256 != receipt.Claim.DataReceiptSHA256 || binding.WriterReceiptSHA256 != receipt.Claim.WriterReceiptSHA256 {
		t.Fatalf("the writable install backend is not named by its own claim: %+v", named)
	}
}

// The read-only install backend's labels carry the writer fence of its own start receipt, not the one of the data
// receipt. The two differ here, so the source can only be right if it reads the start receipt.
func TestEnableStartReadOnlyInstallSourceCarriesTheStartReceiptWriterFence(t *testing.T) {
	f, _, _, _, _ := updateStartJournalFixture(t)
	start := f.source
	start.WriterFence = "3"
	source := checkpointSource{source: start, sourceFence: 1}
	source.command.Payload.SourceOperationID = fixtureID(9103)
	source.data.WriterFence = "1"
	named, err := enableStartNativeSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if named.Origin != driver.UpdateStartSourceCandidate || named.WriterFence != "3" {
		t.Fatalf("the source carries fence %s, not its start receipt's 3: %+v", named.WriterFence, named)
	}
}
