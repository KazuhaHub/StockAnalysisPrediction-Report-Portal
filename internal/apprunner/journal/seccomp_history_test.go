//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// The digest of the option the production runner (built from 4bcc3d5b) created containers with.
const seccompV1SHA256 = "981fc0c0c4ee290a969bed30c93e8ea443a4a0c42a0bdbadddd4f9f24eb142f5"

func TestSeccompModesAreDistinct(t *testing.T) {
	current := driver.CurrentSeccompSHA256()
	if current == seccompV1SHA256 || !driver.SeccompAccepted(seccompV1SHA256, driver.SeccompLive) || driver.SeccompAccepted(seccompV1SHA256, driver.SeccompCurrent) {
		t.Fatal("the v1 digest is not live/history-only")
	}
}

// A recorded v1 claim stays a valid record; a digest outside the accepted set never is.
func TestUpdateStartClaimMarkerAcceptsV1HistoryAndRefusesUnknown(t *testing.T) {
	_, _, command, _, _ := updateStartJournalFixture(t)
	marker := func(sha string) UpdateStartClaimMarker {
		preflight := UpdateStartPreflightEvidence{
			Format:  "portal.runner.update-start-preflight/1-dev.1",
			ImageID: "sha256:" + strings.Repeat("a", 64), Platform: command.Payload.Platform,
			PolicySHA256: strings.Repeat("b", 64), MountsSHA256: strings.Repeat("c", 64),
			RootFilesystemUUID: command.Payload.RootFilesystemUUID, RootInode: command.Payload.RootInode,
			ProjectID: command.Payload.ProjectID, Device: command.Payload.Device, NamespaceInode: command.Payload.NamespaceInode,
			QuotaBytes: command.Payload.QuotaBytes, QuotaInodes: command.Payload.QuotaInodes, SeccompSHA256: sha,
		}
		return UpdateStartClaimMarker{
			Format: "portal.runner.update-start-claim/1-dev.1", ClaimID: "70000000-0000-4000-8000-000000001501",
			EffectID: command.EffectID, ResourceID: command.Payload.ResourceID,
			PayloadSHA256: command.PayloadSHA256, OwnershipSHA256: strings.Repeat("d", 64),
			CheckpointReceiptSHA256: command.Payload.SourceCheckpointReceiptSHA256,
			PolicyRevision:          command.Payload.TargetPolicyRevision, ControlRevision: command.ControlRevision,
			AdmittedFence: command.InstallationFence, SandboxProfile: command.Payload.SandboxProfile,
			ClaimedMS: command.IssuedMS, ExpiresMS: command.ExpiresMS, Preflight: &preflight, PreflightBoundMS: command.IssuedMS,
		}
	}
	for name, tc := range map[string]struct {
		sha  string
		want bool
	}{"current": {driver.CurrentSeccompSHA256(), true}, "v1": {seccompV1SHA256, true}, "unknown": {strings.Repeat("e", 64), false}} {
		if validUpdateStartClaimMarker(marker(tc.sha)) != tc.want {
			t.Fatalf("%s: marker validity is not %v", name, tc.want)
		}
	}
}

// Observing a container accepts a v1 preflight; binding, checking or settling a new start does not.
func TestUpdateStartEvidenceModesAndRecordedPolicy(t *testing.T) {
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
	unbound, err := updateStartNativeRequest(facts, marker, nil)
	if err != nil || unbound.RecordedPolicySHA256 != "" {
		t.Fatalf("a start that bound nothing names a recorded plan: %v %q", err, unbound.RecordedPolicySHA256)
	}
	snapshot := func(sha string) driver.UpdateStartLaunchSnapshot {
		return driver.UpdateStartLaunchSnapshot{
			Launch: driver.CandidateLaunchSnapshot{Identity: unbound.Identity, Data: unbound.Data, Platform: unbound.Platform,
				ImageID: "sha256:" + strings.Repeat("a", 64), PolicySHA256: strings.Repeat("b", 64), MountsSHA256: strings.Repeat("c", 64)},
			Binding: unbound.Binding, SandboxProfile: unbound.SandboxProfile, SeccompSHA256: sha,
		}
	}
	for name, tc := range map[string]struct {
		sha                 string
		current, live, hist bool
	}{
		"current": {driver.CurrentSeccompSHA256(), true, true, true},
		"v1":      {seccompV1SHA256, false, true, true},
		"unknown": {strings.Repeat("e", 64), false, false, false},
	} {
		snap := snapshot(tc.sha)
		marker.Preflight = nil
		preflight := updateStartPreflight(snap)
		bound := marker
		bound.Preflight, bound.PreflightBoundMS = &preflight, marker.ClaimedMS
		request, err := updateStartNativeRequest(facts, bound, nil)
		if !tc.hist {
			if err == nil {
				t.Fatalf("%s: a claim with an unknown seccomp digest was accepted", name)
			}
			continue
		}
		if err != nil || request.RecordedPolicySHA256 != preflight.PolicySHA256 {
			t.Fatalf("%s: a bound claim does not name its recorded plan: %v %q", name, err, request.RecordedPolicySHA256)
		}
		for mode, want := range map[driver.SeccompMode]bool{driver.SeccompCurrent: tc.current, driver.SeccompLive: tc.live} {
			if updateStartSnapshotMatches(snap, facts, bound, mode) != want || updateStartPreflightMatches(preflight, facts, bound, mode) != want {
				t.Fatalf("%s: mode %d evidence match is not %v", name, mode, want)
			}
		}
	}
}

// The stopped source of an enable or repair start is planned from the digest its own claim recorded.
func TestEnableStartSourceCarriesTheRecordedPolicyDigest(t *testing.T) {
	_, _, _, receipt := syntheticWritableRunningFixture(t)
	source := checkpointSource{writable: &receipt, sourceFence: 1}
	source.command.Payload.SourceOperationID = fixtureID(9101)
	source.command.Payload.ReleaseSHA256 = fixtureID(9102)
	source.data.WriterFence = "1"
	named, err := enableStartNativeSource(source)
	if err != nil || named.PolicySHA256 != receipt.Claim.Preflight.Native.PolicySHA256 {
		t.Fatalf("writable source policy digest: %v %q", err, named.PolicySHA256)
	}

	f, _, _, _, _ := updateStartJournalFixture(t)
	start := f.source
	candidate := checkpointSource{source: start, sourceFence: 1}
	candidate.command.Payload.SourceOperationID = fixtureID(9103)
	candidate.data.WriterFence = "1"
	named, err = enableStartNativeSource(candidate)
	if err != nil || named.PolicySHA256 != start.Claim.Preflight.PolicySHA256 {
		t.Fatalf("candidate source policy digest: %v %q", err, named.PolicySHA256)
	}

	updated := UpdateStartRunningReceipt{Claim: UpdateStartClaimMarker{Preflight: &UpdateStartPreflightEvidence{PolicySHA256: strings.Repeat("9", 64)}}}
	named, err = enableStartNativeSource(checkpointSource{update: &updated})
	if err != nil || named.PolicySHA256 != strings.Repeat("9", 64) {
		t.Fatalf("update source policy digest: %v %q", err, named.PolicySHA256)
	}
}

// Managed removal compares the recorded preflight with the snapshot the runner observed. For a v1 claim that
// snapshot carries the v1 digest; an unknown recorded digest never matches.
func TestManagedRemovalPreflightUsesTheRecordedSeccompDigest(t *testing.T) {
	for name, tc := range map[string]struct {
		sha  string
		want bool
	}{"v1": {seccompV1SHA256, true}, "current": {driver.CurrentSeccompSHA256(), true}, "unknown": {strings.Repeat("e", 64), false}} {
		launch := driver.CandidateLaunchSnapshot{ImageID: "sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64",
			PolicySHA256: strings.Repeat("b", 64), MountsSHA256: strings.Repeat("c", 64)}
		snapshot := driver.UpdateStartLaunchSnapshot{Launch: launch, SandboxProfile: control.WritableCandidateSandboxProfile, SeccompSHA256: tc.sha}
		preflight := updateStartPreflight(snapshot)
		receipt := &UpdateStartRunningReceipt{Claim: UpdateStartClaimMarker{Preflight: &preflight, SandboxProfile: control.WritableCandidateSandboxProfile}}
		if got := (managedRemovalSource{update: receipt}).preflightMatches(launch); got != tc.want {
			t.Fatalf("%s: removal preflight match = %v", name, got)
		}
	}
}

func TestDataWriterReceiptModes(t *testing.T) {
	s, raw, sig, r := syntheticAppliedWriterFixture(t)
	defer s.Close()
	tx, f, err := s.beginDataWriter(context.Background(), raw, sig, "applied", nil)
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	for name, tc := range map[string]struct {
		sha           string
		current, hist bool
	}{"current": {driver.CurrentSeccompSHA256(), true, true}, "v1": {seccompV1SHA256, false, true}, "unknown": {strings.Repeat("e", 64), false, false}} {
		variant := r
		variant.Native.SeccompSHA256 = tc.sha
		variant.BindingSHA256 = dataWriterReceiptBinding(variant)
		if dataWriterReceiptMatches(variant, f, driver.SeccompCurrent) != tc.current || dataWriterReceiptMatches(variant, f, driver.SeccompHistory) != tc.hist {
			t.Fatalf("%s: writer receipt modes disagree", name)
		}
	}
}

// A claim and its writer receipt must name the same policy, in every mode.
func TestWritableEvidenceNeverMixesPolicies(t *testing.T) {
	s, raw, sig, claim := writableClaimFixture(t)
	defer s.Close()
	if _, err := s.RunWritableCandidateOnce(context.Background(), claim, func(context.Context, WritableCandidatePlan) error { return nil }); err != nil {
		t.Fatal(err)
	}
	tx, f, err := s.beginWritableCandidate(context.Background(), raw, sig, "unknown", &claim.state.marker)
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	var m writableCandidateClaimMarker
	json.Unmarshal([]byte(claim.state.marker), &m)
	request, err := writableNativeRequest(f, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func(sha string) driver.WritableCandidateLaunchSnapshot {
		return driver.WritableCandidateLaunchSnapshot{
			Launch: driver.CandidateLaunchSnapshot{Identity: request.Identity, Data: request.Data, Platform: request.Platform,
				ImageID: f.source.writer.Native.ImageID, PolicySHA256: strings.Repeat("a", 64), MountsSHA256: strings.Repeat("b", 64)},
			Binding: request.Binding, SandboxProfile: control.WritableCandidateSandboxProfile, SeccompSHA256: sha,
		}
	}
	type result struct{ current, live, hist bool }
	for name, tc := range map[string]struct {
		writer, claim string
		want          result
	}{
		"both current":      {driver.CurrentSeccompSHA256(), driver.CurrentSeccompSHA256(), result{true, true, true}},
		"both v1":           {seccompV1SHA256, seccompV1SHA256, result{false, true, true}},
		"v1 claim, current": {driver.CurrentSeccompSHA256(), seccompV1SHA256, result{false, false, false}},
		"current claim, v1": {seccompV1SHA256, driver.CurrentSeccompSHA256(), result{false, false, false}},
		"both unknown":      {strings.Repeat("e", 64), strings.Repeat("e", 64), result{false, false, false}},
	} {
		facts := f
		facts.source.writer.Native.SeccompSHA256 = tc.writer
		snap := snapshot(tc.claim)
		got := result{
			writableSnapshotMatches(snap, facts, m, driver.SeccompCurrent),
			writableSnapshotMatches(snap, facts, m, driver.SeccompLive),
			writableSnapshotMatches(snap, facts, m, driver.SeccompHistory),
		}
		if got != tc.want {
			t.Fatalf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
}
