//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

type fakeTargetProver struct {
	calls     []string
	err       error
	onProving func()
}

func (f *fakeTargetProver) ProveTargetAbsent(_ context.Context, resourceID string) error {
	f.calls = append(f.calls, resourceID)
	if f.onProving != nil {
		f.onProving()
	}
	return f.err
}

type reservedStartFixture struct {
	f       checkpointFixture
	command control.UpdateStartCommand
	raw     []byte
	sig     []byte
}

// reservedStart is an update-start the runner reserved: its pending target resource exists, nothing created a
// container, and nothing has claimed it.
func reservedStart(t *testing.T) reservedStartFixture {
	t.Helper()
	f, _, command, raw, sig := updateStartJournalFixture(t)
	if _, err := f.store.ReserveUpdateStart(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	return reservedStartFixture{f: f, command: command, raw: raw, sig: sig}
}

func (r reservedStartFixture) removal(t *testing.T, stage string, effect int) ([]byte, []byte) {
	t.Helper()
	command := control.UpdateStartRemovalCommand{
		Format: control.UpdateStartRemovalFormat, EffectID: fixtureID(2700 + effect), OperationID: fixtureID(2790),
		Stage: stage, InstallationFence: r.command.InstallationFence, StartRaw: r.raw, StartSignature: r.sig,
		RunningReceiptSHA256: control.ReservedStartReceiptSHA256,
	}
	var err error
	if command.PayloadSHA256, err = control.UpdateStartRemovalLogicalDigest(command); err != nil {
		t.Fatal(err)
	}
	raw, sig, err := control.SignUpdateStartRemoval(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

func (r reservedStartFixture) startState(t *testing.T) string {
	t.Helper()
	var state string
	if err := r.f.store.db.QueryRow(`SELECT state FROM runner_commands WHERE effect_id=?`, r.command.EffectID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func (r reservedStartFixture) resource(t *testing.T) (string, string, int64) {
	t.Helper()
	var desired, observed string
	var revision int64
	if err := r.f.store.db.QueryRow(`SELECT desired_state,observed_state,revision FROM runner_resources WHERE id=?`, r.command.Payload.ResourceID).Scan(&desired, &observed, &revision); err != nil {
		t.Fatal(err)
	}
	return desired, observed, revision
}

func TestReservedStartIsRemovedByProvingItAbsent(t *testing.T) {
	r := reservedStart(t)
	prover := &fakeTargetProver{}
	raw, sig := r.removal(t, "stop", 1)
	stopped, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover)
	if err != nil || stopped.State != "stopped" || stopped.Stage != "stop" || stopped.ResourceID != r.command.Payload.ResourceID ||
		stopped.NativeID != control.ReservedStartNativeID(r.command.Payload.ResourceID) || stopped.SourceReceiptSHA256 != control.ReservedStartReceiptSHA256 {
		t.Fatalf("stop: %+v %v", stopped, err)
	}
	if len(prover.calls) != 1 || prover.calls[0] != r.command.Payload.ResourceID {
		t.Fatalf("absence was not proven for the resource: %v", prover.calls)
	}
	// The start never ran: it is closed as not run in the same step that stops its resource.
	if r.startState(t) != "reverted" {
		t.Fatalf("the start is %s", r.startState(t))
	}
	var notRun string
	if err := r.f.store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, r.command.EffectID).Scan(&notRun); err != nil || !bytes.Contains([]byte(notRun), []byte(`"outcome":"never_ran"`)) {
		t.Fatalf("no typed outcome on the start: %s %v", notRun, err)
	}
	if desired, observed, revision := r.resource(t); desired != "stopped" || observed != "stopped" || revision != 3 {
		t.Fatalf("resource after stop: %s %s %d", desired, observed, revision)
	}
	raw, sig = r.removal(t, "remove_code", 2)
	removed, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover)
	if err != nil || removed.State != "absent" || removed.Stage != "remove_code" {
		t.Fatalf("remove: %+v %v", removed, err)
	}
	if desired, observed, revision := r.resource(t); desired != "absent" || observed != "absent" || revision != 4 {
		t.Fatalf("resource after remove: %s %s %d", desired, observed, revision)
	}
	// A replay returns the receipt and proves nothing again.
	before := len(prover.calls)
	again, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover)
	if err != nil || again != removed || len(prover.calls) != before {
		t.Fatalf("replay: %+v %v calls=%d", again, err, len(prover.calls)-before)
	}
	if read, err := r.f.store.ReadReservedUpdateStartRemovalReceipt(context.Background(), raw, sig); err != nil || read != removed {
		t.Fatalf("read: %+v %v", read, err)
	}
}

func TestReservedStartRemovalRefusedWhileAClaimIsExecuting(t *testing.T) {
	r := reservedStart(t)
	if err := r.f.store.acquireDataExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer r.f.store.releaseDataExecution()
	prover := &fakeTargetProver{}
	raw, sig := r.removal(t, "stop", 1)
	if _, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover); !errors.Is(err, ErrManagedRemovalUnavailable) {
		t.Fatalf("a removal beside an executing start was not refused: %v", err)
	}
	if len(prover.calls) != 0 || r.startState(t) != "prepared" {
		t.Fatal("a refused removal proved or changed something")
	}
	if desired, observed, revision := r.resource(t); desired != "present" || observed != "unknown" || revision != 1 {
		t.Fatalf("the resource moved: %s %s %d", desired, observed, revision)
	}
	if scalar(t, r.f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='stop'`) != 0 {
		t.Fatal("a refused removal left a command row")
	}
}

// A claim nothing is executing is an orphan. It is read back and never sent again; the absence of a container, proven
// under the gate, settles it as not run, together with its resource.
func TestOrphanedStartIsSettledByTheAbsenceProof(t *testing.T) {
	r := reservedStart(t)
	if _, err := r.f.store.ClaimUpdateStart(context.Background(), r.raw, r.sig); err != nil {
		t.Fatal(err)
	}
	if r.startState(t) != "unknown" {
		t.Fatalf("the claim left the start %s", r.startState(t))
	}
	held, err := r.f.store.ReadUpdateStartRunningOrAbsence(context.Background(), r.raw, updateStartReadSignature(t, r.raw))
	if status, ok := held.(UpdateStartAbsence); err != nil || !ok || status.State != "orphaned" {
		t.Fatalf("an orphaned start read as %#v %v", held, err)
	}
	prover := &fakeTargetProver{}
	raw, sig := r.removal(t, "stop", 1)
	if _, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover); err != nil {
		t.Fatalf("an orphaned start was not settled: %v", err)
	}
	if r.startState(t) != "reverted" || len(prover.calls) != 1 {
		t.Fatalf("start %s, proofs %d", r.startState(t), len(prover.calls))
	}
}

func TestReservedStartRemovalRefusedWhenAContainerExists(t *testing.T) {
	r := reservedStart(t)
	prover := &fakeTargetProver{err: errTargetExists}
	raw, sig := r.removal(t, "stop", 1)
	if _, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover); err == nil {
		t.Fatal("a removal was settled although a container exists for the resource")
	}
	if r.startState(t) != "prepared" {
		t.Fatal("the start was closed without an absence proof")
	}
	if desired, observed, revision := r.resource(t); desired != "present" || observed != "unknown" || revision != 1 {
		t.Fatalf("the resource moved without a proof: %s %s %d", desired, observed, revision)
	}
	// The unknown removal row stays, so the same effect is retried and converges.
	prover.err = nil
	if _, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover); err != nil {
		t.Fatalf("the removal did not converge once the container was gone: %v", err)
	}
}

func TestReservedStartRemovalRefusesAStartThatRan(t *testing.T) {
	f, _, _, raw, sig := updateStartJournalFixture(t)
	r := reservedStartFixture{f: f, raw: raw, sig: sig}
	cmd, err := control.VerifyUpdateStart(raw, sig, importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	r.command = cmd.Command()
	// No reservation at all: the runner holds no resource to remove.
	rawRemoval, sigRemoval := r.removal(t, "stop", 1)
	if _, err := f.store.RemoveReservedUpdateStart(context.Background(), rawRemoval, sigRemoval, &fakeTargetProver{}); err == nil {
		t.Fatal("a start the runner holds nothing for was removed")
	}
	if _, err := f.store.ReserveUpdateStart(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	mustJournalExec(t, f.store.db, `UPDATE runner_commands SET state='applied' WHERE effect_id=?`, r.command.EffectID)
	if _, err := f.store.RemoveReservedUpdateStart(context.Background(), rawRemoval, sigRemoval, &fakeTargetProver{}); err == nil {
		t.Fatal("a start that ran was removed as if it never had")
	}
}

func TestUpdateStartReadReportsWhatTheRunnerHolds(t *testing.T) {
	r := reservedStart(t)
	read := func() string {
		got, err := r.f.store.ReadUpdateStartRunningOrAbsence(context.Background(), r.raw, updateStartReadSignature(t, r.raw))
		status, ok := got.(UpdateStartAbsence)
		if err != nil || !ok {
			t.Fatalf("read: %#v %v", got, err)
		}
		return status.State
	}
	if read() != "reserved" {
		t.Fatal("a prepared start with its pending resource is not reserved")
	}
	if err := r.f.store.acquireDataExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	if read() != "pending" {
		t.Fatal("a start read while the gate is held may be executing")
	}
	r.f.store.releaseDataExecution()
	if _, err := r.f.store.ClaimUpdateStart(context.Background(), r.raw, r.sig); err != nil {
		t.Fatal(err)
	}
	if read() != "orphaned" {
		t.Fatal("a claim nothing executes is not orphaned")
	}
}

var errTargetExists = errors.New("a container exists for the resource")
