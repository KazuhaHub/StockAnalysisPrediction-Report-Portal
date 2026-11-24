package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"log"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// checkpointSnapshotFor is what a walk that archived and verified the namespace of the command returns.
func checkpointSnapshotFor(f checkpointFixture, command control.CheckpointCommand) data.CheckpointSnapshot {
	project, _ := strconv.ParseUint(command.Payload.ProjectID, 10, 32)
	return data.CheckpointSnapshot{
		NamespaceID: command.Payload.NamespaceID, ReleaseSHA256: command.Payload.ReleaseSHA256, ProjectID: uint32(project),
		DataFormat: mustDataInt(command.Payload.DataFormat), ArchiveBytes: 4096, Entries: 3,
		ManifestSHA256: strings.Repeat("a", 64), ObservedMS: time.Now().UnixMilli(),
	}
}

func checkpointNativeSucceeding(f checkpointFixture, command control.CheckpointCommand, calls *int) checkpointNative {
	return func(context.Context, data.CheckpointRequest) (data.CheckpointSnapshot, error) {
		*calls++
		return checkpointSnapshotFor(f, command), nil
	}
}

// resignCheckpointAt signs the same command again as the Portal does on a later claim: a new window, and a fence moved
// on by every lease that was lost in between.
func resignCheckpointAt(t *testing.T, f checkpointFixture, command control.CheckpointCommand, fence int64) (control.CheckpointCommand, []byte, []byte) {
	t.Helper()
	command.InstallationFence = strconv.FormatInt(fence, 10)
	now := time.Now().UnixMilli()
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	enrollment := enrollmentFixture(30)
	authority := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	raw, sig, err := control.SignCheckpoint(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return command, raw, sig
}

// The wedged command, resumed on a later claim: its signature is new and its fence is higher. The partial archive is
// removed, the namespace is walked again under the same effect, and the result is applied.
func TestResumedCheckpointIsAppliedUnderAResignedCommandWithAHigherFence(t *testing.T) {
	f := newCheckpointFixture(t)
	id := f.command.Payload.CheckpointID
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
		t.Fatal(err)
	}
	fence := mustDataInt(f.command.InstallationFence)
	command, raw, sig := resignCheckpointAt(t, f, f.command, fence+4)
	calls, discards := 0, &checkpointDiscards{}
	receipt, err := f.store.dispatchCheckpointWith(context.Background(), raw, sig, checkpointNativeSucceeding(f, command, &calls), discards.discard)
	if err != nil || calls != 1 || len(discards.ids) != 1 || discards.ids[0] != id || receipt.CheckpointID != id {
		t.Fatal("the resumed walk did not settle", receipt, err, calls, discards.ids)
	}
	var state string
	var admitted int64
	if err := f.store.db.QueryRow(`SELECT state,admitted_fence FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&state, &admitted); err != nil || state != "applied" || admitted != fence+4 {
		t.Fatal("the resumed command is not applied at the new fence", state, admitted, err)
	}
	if again, err := f.store.ReadCheckpointReceipt(context.Background(), raw, sig); err != nil || again != receipt {
		t.Fatal("the receipt does not read back", err)
	}
}

// A resume is a fresh admission of the same command: a signed fence below the one the command was admitted under, or
// below the source's, is refused, and nothing is removed or walked.
func TestResumedCheckpointRefusesAFenceBelowTheAdmittedOne(t *testing.T) {
	f := newCheckpointFixture(t)
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
		t.Fatal(err)
	}
	fence := mustDataInt(f.command.InstallationFence)
	if _, err := f.store.db.Exec(`UPDATE runner_commands SET admitted_fence=? WHERE effect_id=?`, fence+10, f.command.EffectID); err != nil {
		t.Fatal(err)
	}
	command, raw, sig := resignCheckpointAt(t, f, f.command, fence+3)
	calls, discards := 0, &checkpointDiscards{}
	_, err := f.store.dispatchCheckpointWith(context.Background(), raw, sig, checkpointNativeSucceeding(f, command, &calls), discards.discard)
	if !errors.Is(err, ErrFenceReconciliation) || calls != 0 || len(discards.ids) != 0 {
		t.Fatal("a resume under a lower fence was accepted", err, calls, discards.ids)
	}
	if state, _, checkpoint, _ := checkpointRefusalRows(t, f.store, f.command.EffectID, f.command.Payload.CheckpointID); state != "unknown" || checkpoint != "preparing" {
		t.Fatal("a refused resume changed the rows", state, checkpoint)
	}
}

// A new checkpoint for an installation whose earlier snapshot was left unknown settles the earlier one as reverted and
// removes its archive, under the gate the walk uses, so repair's own checkpoint is not blocked by it for ever. A
// checkpoint only reads the data, so abandoning a walk that no longer runs loses nothing.
func TestANewCheckpointSettlesAnEarlierUnknownSnapshotOfTheInstallation(t *testing.T) {
	f := newCheckpointFixture(t)
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
		t.Fatal(err)
	}
	raw, sig := resignCheckpoint(t, f, func(c *control.CheckpointCommand) {
		c.OperationID, c.EffectID, c.Payload.CheckpointID = fixtureID(890), fixtureID(891), fixtureID(892)
	})
	var next control.CheckpointCommand
	enrollment := enrollmentFixture(30)
	authority := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	verified, err := control.VerifyCheckpoint(raw, sig, authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	next = verified.Command()
	calls, discards := 0, &checkpointDiscards{}
	receipt, err := f.store.dispatchCheckpointWith(context.Background(), raw, sig, checkpointNativeSucceeding(f, next, &calls), discards.discard)
	if err != nil || calls != 1 || receipt.CheckpointID != next.Payload.CheckpointID {
		t.Fatal("the new checkpoint was blocked by the earlier unknown one", receipt, err)
	}
	state, receiptJSON, checkpoint, unknown := checkpointRefusalRows(t, f.store, f.command.EffectID, f.command.Payload.CheckpointID)
	record, ok := parseDataEffectFailure(receiptJSON)
	if state != "reverted" || !ok || record.Code != dataFailureCheckpointIncomplete || record.Dirty || checkpoint != "deleted" || unknown != 0 {
		t.Fatal("the earlier snapshot was not settled", state, receiptJSON, checkpoint, unknown)
	}
	if len(discards.ids) != 1 || discards.ids[0] != f.command.Payload.CheckpointID {
		t.Fatal("the earlier snapshot's archive was not removed", discards.ids)
	}
}

// An earlier snapshot whose checkpoint row is anything but preparing is not abandoned: it stays for review and the new
// checkpoint is refused for it.
func TestANewCheckpointDoesNotSettleAnEarlierSnapshotWithAnotherRecord(t *testing.T) {
	f := newCheckpointFixture(t)
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`UPDATE runner_checkpoints SET state='review_required' WHERE id=?`, f.command.Payload.CheckpointID); err != nil {
		t.Fatal(err)
	}
	raw, sig := resignCheckpoint(t, f, func(c *control.CheckpointCommand) {
		c.OperationID, c.EffectID, c.Payload.CheckpointID = fixtureID(893), fixtureID(894), fixtureID(895)
	})
	calls, discards := 0, &checkpointDiscards{}
	_, err := f.store.dispatchCheckpointWith(context.Background(), raw, sig, checkpointNativeFailing(io.EOF, &calls), discards.discard)
	if !errors.Is(err, ErrFenceReconciliation) || calls != 0 || len(discards.ids) != 0 {
		t.Fatal("an earlier snapshot with another record was abandoned", err, calls, discards.ids)
	}
	if state, _, _, unknown := checkpointRefusalRows(t, f.store, f.command.EffectID, f.command.Payload.CheckpointID); state != "unknown" || unknown != 1 {
		t.Fatal("the earlier snapshot changed", state, unknown)
	}
}

// A failure to remove an archive is logged, not swallowed, and does not undo the settlement it follows.
func TestCheckpointArchiveRemovalFailureIsLogged(t *testing.T) {
	f := newCheckpointFixture(t)
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })
	calls := 0
	failing := func(context.Context, string) error { return errors.New("unlink: device busy") }
	_, err := f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(data.ErrCheckpointUnsafe, &calls), failing)
	var failed *DataEffectFailedError
	if !errors.As(err, &failed) || failed.Code != dataFailureCheckpointUnsafe {
		t.Fatal("the refusal was not settled", err)
	}
	if !strings.Contains(logged.String(), "device busy") || !strings.Contains(logged.String(), f.command.Payload.CheckpointID) {
		t.Fatalf("the removal failure was not logged: %q", logged.String())
	}
	// The removal runs on a context of its own: a dispatch whose context is already cancelled still removes.
	g := newCheckpointFixture(t)
	cancelled, cancel := context.WithCancel(context.Background())
	var seen error
	probe := func(ctx context.Context, id string) error { seen = ctx.Err(); return nil }
	native := func(context.Context, data.CheckpointRequest) (data.CheckpointSnapshot, error) {
		cancel()
		return data.CheckpointSnapshot{}, data.ErrCheckpointUnsafe
	}
	if _, err := g.store.dispatchCheckpointWith(cancelled, g.raw, g.sig, native, probe); !errors.As(err, &failed) || seen != nil {
		t.Fatal("the removal ran on the cancelled context of the dispatch", err, seen)
	}
}

// An earlier snapshot is bound to its checkpoint row by more than the operation: when the operation holds another
// checkpoint row too, which row the command claimed cannot be told, so neither is touched and the new checkpoint is
// refused for it.
func TestANewCheckpointDoesNotSettleAnEarlierSnapshotWithAnAmbiguousRecord(t *testing.T) {
	f := newCheckpointFixture(t)
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
		t.Fatal(err)
	}
	// A second row of the same operation, as the claim writes one.
	if _, err := f.store.db.Exec(`INSERT INTO runner_checkpoints(id,data_namespace_id,installation_id,operation_id,release_sha256,data_format,writer_fence,manifest_sha256,bytes,state,revision,verified_ms,created_ms,updated_ms)
		SELECT ?,data_namespace_id,installation_id,operation_id,release_sha256,data_format,writer_fence,NULL,0,'preparing',1,NULL,created_ms,updated_ms FROM runner_checkpoints WHERE id=?`,
		fixtureID(896), f.command.Payload.CheckpointID); err != nil {
		t.Fatal(err)
	}
	// The command's own row is not as its claim left it; only the other one is.
	if _, err := f.store.db.Exec(`UPDATE runner_checkpoints SET state='review_required' WHERE id=?`, f.command.Payload.CheckpointID); err != nil {
		t.Fatal(err)
	}
	raw, sig := resignCheckpoint(t, f, func(c *control.CheckpointCommand) {
		c.OperationID, c.EffectID, c.Payload.CheckpointID = fixtureID(897), fixtureID(898), fixtureID(899)
	})
	calls, discards := 0, &checkpointDiscards{}
	_, err := f.store.dispatchCheckpointWith(context.Background(), raw, sig, checkpointNativeFailing(io.EOF, &calls), discards.discard)
	if !errors.Is(err, ErrFenceReconciliation) || calls != 0 || len(discards.ids) != 0 {
		t.Fatal("an earlier snapshot with an ambiguous record was abandoned", err, calls, discards.ids)
	}
	if state, _, _, unknown := checkpointRefusalRows(t, f.store, f.command.EffectID, f.command.Payload.CheckpointID); state != "unknown" || unknown != 1 {
		t.Fatal("the earlier snapshot changed", state, unknown)
	}
	var other string
	if err := f.store.db.QueryRow(`SELECT state FROM runner_checkpoints WHERE id=?`, fixtureID(896)).Scan(&other); err != nil || other != "preparing" {
		t.Fatal("a checkpoint row the command may not own was ended", other, err)
	}
}
