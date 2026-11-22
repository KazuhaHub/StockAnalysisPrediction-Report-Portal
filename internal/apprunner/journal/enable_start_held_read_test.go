//go:build linux || darwin

package journal

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// The Portal settles a held enable, repair or import start by reading the start back from the runner, which answers a
// start it holds without a running receipt with a typed state. The read is signed for the command's own kind, which is
// enable-start there, and it was answered for no other kind than update-start: an unclaimed or claimed enable-start
// fell through to the ordinary running-receipt error and the Portal could never tell it from an unreachable runner.
func TestEnableStartReadAnswersEveryStateTheJournalHoldsItIn(t *testing.T) {
	ctx := context.Background()
	f, _, start, _, _ := updateStartJournalFixture(t)
	start.Kind = "enable-start"
	start.Payload.TargetReleaseSHA256 = start.Payload.SourceReleaseSHA256
	var err error
	if start.PayloadSHA256, err = control.UpdateStartLogicalDigest(start); err != nil {
		t.Fatal(err)
	}
	raw, sig := resignUpdateStart(t, start)
	read := func() UpdateStartAbsence {
		t.Helper()
		result, err := f.store.ReadUpdateStartRunningOrAbsence(ctx, raw, updateStartReadSignature(t, raw))
		if err != nil {
			t.Fatalf("the read of an enable-start has no typed answer: %v", err)
		}
		absence, ok := result.(UpdateStartAbsence)
		if !ok {
			t.Fatalf("the read answered %#v", result)
		}
		return absence
	}
	// Never admitted: absent, bound to the effect identity.
	if absence := read(); absence.State != "absent" || absence.EffectID != start.EffectID || !UpdateStartAbsenceMatchesCommand(absence, start) {
		t.Fatalf("an enable-start the runner never admitted was not reported absent: %+v", absence)
	}
	if _, err := f.store.ReserveUpdateStart(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	if absence := read(); absence.State != "reserved" || !UpdateStartAbsenceMatchesCommand(absence, start) {
		t.Fatalf("a reserved enable-start was not reported reserved: %+v", absence)
	}
	// Claimed and never executed, as prod's start is: the runner holds it with nothing executing it.
	if _, err := f.store.ClaimUpdateStart(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	if absence := read(); absence.State != "orphaned" || !UpdateStartAbsenceMatchesCommand(absence, start) {
		t.Fatalf("a claimed enable-start was not reported orphaned: %+v", absence)
	}
}

// A held enable-start of an install-sourced installation, as prod holds one: claimed by the previous binary with
// nothing executing it and no container made. The Portal reads it as orphaned and asks the runner to settle it. The
// settlement finds no target and the install's stopped backend still there, so the start never ran, the source is left as
// it was, and the next read says so.
func TestSettleOrphanedEnableStartOfAnInstallSourceIsNotRunAndKeepsTheSource(t *testing.T) {
	ctx := context.Background()
	f, _, start, _, _ := updateStartJournalFixture(t)
	start.Kind = "enable-start"
	start.Payload.TargetReleaseSHA256 = start.Payload.SourceReleaseSHA256
	var err error
	if start.PayloadSHA256, err = control.UpdateStartLogicalDigest(start); err != nil {
		t.Fatal(err)
	}
	raw, sig := resignUpdateStart(t, start)
	if _, err := f.store.ReserveUpdateStart(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimUpdateStart(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	r := reservedStartFixture{f: f, command: start, raw: raw, sig: sig}
	sourceNative := f.source.NativeID
	inspector := &fakeInspector{byID: map[string]driver.TargetInspection{
		f.source.Claim.ResourceID: {Found: true, NativeID: sourceNative, State: "exited"},
	}}
	settleRaw, settleSig := r.settleCommand(t, 1)
	receipt, err := f.store.SettleUpdateStart(ctx, settleRaw, settleSig, inspector)
	if err != nil || receipt.Outcome != "never_ran" || receipt.StartEffectID != start.EffectID {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	if r.startState(t) != "reverted" {
		t.Fatalf("the start is %s", r.startState(t))
	}
	var desired, observed string
	var revision int64
	if err := f.store.db.QueryRow(`SELECT desired_state,observed_state,revision FROM runner_resources WHERE id=?`, f.source.Claim.ResourceID).Scan(&desired, &observed, &revision); err != nil ||
		observed != "stopped" || revision != 3 {
		t.Fatalf("the install's backend was changed by a start that never ran: %s %s %d %v", desired, observed, revision, err)
	}
	held, err := f.store.ReadUpdateStartRunningOrAbsence(ctx, raw, updateStartReadSignature(t, raw))
	if status, ok := held.(UpdateStartAbsence); err != nil || !ok || status.State != "never_ran" {
		t.Fatalf("the settled enable-start reads as %#v %v", held, err)
	}
}
