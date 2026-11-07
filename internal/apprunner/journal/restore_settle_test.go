//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

func restoreSnapshotFor(c control.RestoreCommand) data.RestoreSnapshot {
	return data.RestoreSnapshot{
		NamespaceID: c.Payload.NamespaceID, ReleaseSHA256: c.Payload.ReleaseSHA256, ManifestSHA256: c.Payload.ManifestSHA256,
		ProjectID: uint32(mustDataUint(c.Payload.ProjectID)), DataFormat: mustDataInt(c.Payload.DataFormat), Entries: 3, UsedBytes: 4096,
		ObservedMS: time.Now().UTC().UnixMilli(), NamespaceDevice: mustDataUint(c.Payload.Device), NamespaceInode: mustDataUint(c.Payload.NamespaceInode),
	}
}

func restoreReadSignature(t *testing.T, raw []byte) []byte {
	t.Helper()
	sig, err := control.SignRestoreRead(raw, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestRestoreUnknownCompletesUnderSameClaim(t *testing.T) {
	f, _, command, raw, signature := restoreJournalFixture(t)
	// A first attempt claims the restore and is lost before the native restore settled.
	if _, _, err := f.store.beginRestore(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	runs := 0
	run := func(ctx context.Context, request data.RestoreRequest) (data.RestoreSnapshot, error) {
		runs++
		if request.CheckpointID != command.Payload.CheckpointID || request.WriterFence != mustDataInt(command.Payload.WriterFence) {
			t.Error("completion is not bound to the claimed checkpoint")
		}
		return restoreSnapshotFor(command), nil
	}
	receipt, err := f.store.dispatchRestore(context.Background(), raw, signature, run)
	if err != nil || receipt.State != "restored" || runs != 1 {
		t.Fatalf("unknown restore was not completed under its claim: %+v %v runs=%d", receipt, err, runs)
	}
	if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='restore'`) != 1 {
		t.Fatal("completion claimed a second effect")
	}
	if rowState(t, f.store, command.EffectID) != "applied" {
		t.Fatal("restore not applied")
	}
	var state string
	var revision int64
	if err := f.store.db.QueryRow(`SELECT state,revision FROM runner_checkpoints WHERE id=?`, command.Payload.CheckpointID).Scan(&state, &revision); err != nil || state != "verified" || revision != 4 {
		t.Fatalf("checkpoint after completion: %s %d %v", state, revision, err)
	}
	again, err := f.store.dispatchRestore(context.Background(), raw, signature, run)
	if err != nil || again != receipt || runs != 1 {
		t.Fatalf("replay reran or changed: %+v %v runs=%d", again, err, runs)
	}
	// A different effect on the same checkpoint is still refused: a checkpoint restores only once.
}

func TestRestoreClaimedRunsDetached(t *testing.T) {
	f, _, command, raw, signature := restoreJournalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	var observed error
	run := func(inner context.Context, request data.RestoreRequest) (data.RestoreSnapshot, error) {
		cancel() // the connection that carried the command goes away
		observed = inner.Err()
		if deadline, ok := inner.Deadline(); !ok || time.Until(deadline) < time.Minute {
			t.Error("claimed restore has no generous own bound")
		}
		return restoreSnapshotFor(command), nil
	}
	receipt, err := f.store.dispatchRestore(ctx, raw, signature, run)
	if err != nil || receipt.State != "restored" || observed != nil {
		t.Fatalf("claimed restore followed its connection: %+v err=%v ctx=%v", receipt, err, observed)
	}
	// Settlement used a context that survived the cancellation, so the row is final.
	if rowState(t, f.store, command.EffectID) != "applied" {
		t.Fatal("restore left unsettled by a cancelled connection")
	}
}

func TestRestoreReadAbsenceUnderGate(t *testing.T) {
	f, _, command, raw, _ := restoreJournalFixture(t)
	read := restoreReadSignature(t, raw)
	got, err := f.store.ReadRestoreOrStatus(context.Background(), raw, read)
	status, ok := got.(MigrateDataStatus)
	if err != nil || !ok || status.State != "absent" || status.Kind != "restore" || !RestoreStatusMatchesCommand(status, command) {
		t.Fatalf("unclaimed restore read: %#v %v", got, err)
	}
	if err := f.store.acquireDataExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ = f.store.ReadRestoreOrStatus(context.Background(), raw, read)
	if status, ok := got.(MigrateDataStatus); !ok || status.State != "pending" {
		t.Fatalf("a held gate must not read absent: %#v", got)
	}
	f.store.releaseDataExecution()
	// A claimed restore is orphaned with the gate free, never absent.
	if _, _, err := f.store.beginRestore(context.Background(), raw, restoreSignatureFor(t, command)); err != nil {
		t.Fatal(err)
	}
	got, err = f.store.ReadRestoreOrStatus(context.Background(), raw, read)
	if status, ok := got.(MigrateDataStatus); err != nil || !ok || status.State != "orphaned" {
		t.Fatalf("unknown restore read: %#v %v", got, err)
	}
	// The execute signature is not a read.
	_, executeSig, _ := signedRestore(t, command)
	if _, err := f.store.ReadRestoreOrStatus(context.Background(), raw, executeSig); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
	_ = strconv.Itoa
}

func signedRestore(t *testing.T, c control.RestoreCommand) ([]byte, []byte, error) {
	t.Helper()
	raw, sig, err := control.SignRestore(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig, nil
}

func restoreSignatureFor(t *testing.T, c control.RestoreCommand) []byte {
	_, sig, _ := signedRestore(t, c)
	return sig
}
