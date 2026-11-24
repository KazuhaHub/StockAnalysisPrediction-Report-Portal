package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

func checkpointNativeFailing(err error, calls *int) checkpointNative {
	return func(context.Context, data.CheckpointRequest) (data.CheckpointSnapshot, error) {
		*calls++
		return data.CheckpointSnapshot{}, err
	}
}

// checkpointDiscards records the archives the dispatch asked to have removed.
type checkpointDiscards struct{ ids []string }

func (d *checkpointDiscards) discard(_ context.Context, id string) error {
	d.ids = append(d.ids, id)
	return nil
}

func checkpointRefusalRows(t *testing.T, s *Store, effect, checkpoint string) (commandState, receipt, checkpointState string, unknown int) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`, effect).Scan(&commandState, &receipt); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT state FROM runner_checkpoints WHERE id=?`, checkpoint).Scan(&checkpointState); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	return
}

// A walker refusal (a symlink in the data root, an unsupported file type, an archive over its cap) is definite: the
// checkpoint never writes to the data, so the command ends reverted, the checkpoint row ends, the partial archive is
// removed, and the answer is typed.
func TestCheckpointDefiniteRefusalIsSettledAndTyped(t *testing.T) {
	cases := map[string]struct {
		err  error
		code string
	}{
		"unsupported entry":    {data.ErrCheckpointUnsafe, dataFailureCheckpointUnsafe},
		"wrapped":              {fmt.Errorf("walk: %w", data.ErrCheckpointUnsafe), dataFailureCheckpointUnsafe},
		"over the archive cap": {errors.Join(data.ErrCheckpointUnsafe, data.ErrCheckpointTooLarge), dataFailureCheckpointTooLarge},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCheckpointFixture(t)
			calls, discards := 0, &checkpointDiscards{}
			id := f.command.Payload.CheckpointID
			_, err := f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(tc.err, &calls), discards.discard)
			var failed *DataEffectFailedError
			if !errors.As(err, &failed) || failed.Code != tc.code || !errors.Is(err, tc.err) || calls != 1 {
				t.Fatal("refusal was not a typed data effect failure", err, calls)
			}
			if len(discards.ids) != 1 || discards.ids[0] != id {
				t.Fatal("the refused archive was not removed", discards.ids)
			}
			state, receipt, checkpoint, unknown := checkpointRefusalRows(t, f.store, f.command.EffectID, id)
			record, ok := parseDataEffectFailure(receipt)
			if state != "reverted" || !ok || record.Code != tc.code || record.Dirty || checkpoint != "deleted" || unknown != 0 {
				t.Fatal("refusal was not settled", state, receipt, checkpoint, unknown)
			}
			// The settled refusal is replayed as the same typed answer, never reaches the native layer again, and
			// removes whatever archive is still there.
			_, err = f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(io.EOF, &calls), discards.discard)
			if !errors.As(err, &failed) || failed.Code != tc.code || calls != 1 || len(discards.ids) != 2 {
				t.Fatal("a settled refusal was not replayed as the same answer", err, calls, discards.ids)
			}
			// A read-back answers the same, and the status it carries is a valid failed status for this command.
			_, err = f.store.ReadCheckpointReceipt(context.Background(), f.raw, f.sig)
			if !errors.As(err, &failed) || failed.Code != tc.code {
				t.Fatal("read-back of a refusal", err)
			}
			status := failed.Status(1)
			if status.State != "failed" || status.Stage != "snapshot" || status.Kind != "snapshot" || !DataEffectStatusMatchesHeader(status, CheckpointCommandHeader(f.command)) {
				t.Fatal("refusal status", status)
			}
			encoded, _ := json.Marshal(status)
			if parsed, err := ParseMigrateDataStatus(encoded); err != nil || parsed != status {
				t.Fatal("refusal status does not round-trip", err)
			}
		})
	}
}

// An error that may have left the archive half written is not a refusal: it stays unknown and is not settled by the
// call that saw it.
func TestCheckpointUncertainFailureIsNotSettledByTheCallThatSawIt(t *testing.T) {
	for name, native := range map[string]error{
		"incomplete":        errors.Join(data.ErrCheckpointIncomplete, io.ErrUnexpectedEOF),
		"incomplete unsafe": errors.Join(data.ErrCheckpointIncomplete, data.ErrCheckpointUnsafe),
		"unclassified":      io.ErrUnexpectedEOF,
	} {
		t.Run(name, func(t *testing.T) {
			f := newCheckpointFixture(t)
			calls, discards := 0, &checkpointDiscards{}
			_, err := f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(native, &calls), discards.discard)
			var failed *DataEffectFailedError
			if err == nil || errors.As(err, &failed) || !errors.Is(err, native) {
				t.Fatal("an uncertain failure was typed as a refusal", err)
			}
			state, receipt, checkpoint, _ := checkpointRefusalRows(t, f.store, f.command.EffectID, f.command.Payload.CheckpointID)
			if state != "unknown" || receipt != "{}" || checkpoint != "preparing" || len(discards.ids) != 0 {
				t.Fatal("an uncertain failure was settled", state, receipt, checkpoint, discards.ids)
			}
		})
	}
}

// A snapshot left unknown (the wedged production command) is not settled by its replay: a timeout, a full disk or a
// failed sync would become a permanent refusal. A checkpoint only reads the data, so the replay removes the partial
// archive and walks again under the same command. Whatever that walk ends with settles normally.
func TestUnknownCheckpointIsWalkedAgainOnReplay(t *testing.T) {
	f := newCheckpointFixture(t)
	id := f.command.Payload.CheckpointID
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
		t.Fatal(err)
	}
	// The walk is uncertain again: still unknown, the partial archive removed before the walk, nothing settled.
	calls, discards := 0, &checkpointDiscards{}
	uncertain := errors.Join(data.ErrCheckpointIncomplete, io.ErrUnexpectedEOF)
	_, err := f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(uncertain, &calls), discards.discard)
	var failed *DataEffectFailedError
	if err == nil || errors.As(err, &failed) || calls != 1 || len(discards.ids) != 1 || discards.ids[0] != id {
		t.Fatal("the unknown snapshot was not walked again", err, calls, discards.ids)
	}
	if state, _, checkpoint, unknown := checkpointRefusalRows(t, f.store, f.command.EffectID, id); state != "unknown" || checkpoint != "preparing" || unknown != 1 {
		t.Fatal("an uncertain replay settled the snapshot", state, checkpoint, unknown)
	}
	// The data now refuses definitively (the symlink is still there): the same command settles as refused.
	_, err = f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(data.ErrCheckpointUnsafe, &calls), discards.discard)
	if !errors.As(err, &failed) || failed.Code != dataFailureCheckpointUnsafe || calls != 2 {
		t.Fatal("the re-walk's refusal did not settle", err, calls)
	}
	if state, _, checkpoint, unknown := checkpointRefusalRows(t, f.store, f.command.EffectID, id); state != "reverted" || checkpoint != "deleted" || unknown != 0 {
		t.Fatal("the re-walk's refusal was not settled", state, checkpoint, unknown)
	}
}

// A replay only walks again a snapshot whose checkpoint row is still exactly what the claim wrote; anything else stays
// for review, and nothing is removed or walked.
func TestUnknownCheckpointWithAnotherRecordIsNotWalked(t *testing.T) {
	f := newCheckpointFixture(t)
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`UPDATE runner_checkpoints SET state='review_required' WHERE id=?`, f.command.Payload.CheckpointID); err != nil {
		t.Fatal(err)
	}
	calls, discards := 0, &checkpointDiscards{}
	_, err := f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(io.EOF, &calls), discards.discard)
	var failed *DataEffectFailedError
	if !errors.Is(err, ErrFenceReconciliation) || errors.As(err, &failed) || calls != 0 || len(discards.ids) != 0 {
		t.Fatal("a snapshot whose checkpoint is not preparing was walked", err, calls, discards.ids)
	}
	state, receipt, checkpoint, unknown := checkpointRefusalRows(t, f.store, f.command.EffectID, f.command.Payload.CheckpointID)
	if state != "unknown" || receipt != "{}" || checkpoint != "review_required" || unknown != 1 {
		t.Fatal("rows changed", state, receipt, checkpoint, unknown)
	}
}

// A refusal that the walk reaches after its signed window ran out is still recorded against the exact command: the
// window governs dispatch, not settlement.
func TestCheckpointRefusalAfterTheSignedWindowIsStillSettled(t *testing.T) {
	f := newCheckpointFixture(t)
	c := f.command
	now := time.Now().UnixMilli()
	c.IssuedMS, c.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+300, 10)
	enrollment := enrollmentFixture(30)
	authority := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	raw, sig, err := control.SignCheckpoint(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
	if err != nil {
		t.Fatal(err)
	}
	slow := func(context.Context, data.CheckpointRequest) (data.CheckpointSnapshot, error) {
		time.Sleep(400 * time.Millisecond)
		return data.CheckpointSnapshot{}, data.ErrCheckpointUnsafe
	}
	discards := &checkpointDiscards{}
	_, err = f.store.dispatchCheckpointWith(context.Background(), raw, sig, slow, discards.discard)
	var failed *DataEffectFailedError
	if !errors.As(err, &failed) || failed.Code != dataFailureCheckpointUnsafe {
		t.Fatal("a refusal after the window was not settled", err)
	}
	if state, _, checkpoint, _ := checkpointRefusalRows(t, f.store, c.EffectID, c.Payload.CheckpointID); state != "reverted" || checkpoint != "deleted" {
		t.Fatal("a refusal after the window was not recorded", state, checkpoint)
	}
}

func TestCheckpointRefusalClassification(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code string
		ok   bool
	}{
		"unsafe":           {data.ErrCheckpointUnsafe, dataFailureCheckpointUnsafe, true},
		"too large":        {errors.Join(data.ErrCheckpointUnsafe, data.ErrCheckpointTooLarge), dataFailureCheckpointTooLarge, true},
		"incomplete":       {errors.Join(data.ErrCheckpointIncomplete, data.ErrCheckpointUnsafe), "", false},
		"io":               {io.ErrUnexpectedEOF, "", false},
		"root unsafe":      {data.ErrUnsafe, "", false},
		"archive conflict": {data.ErrConflict, "", false},
	} {
		if code, ok := classifyCheckpointRefusal(tc.err); code != tc.code || ok != tc.ok {
			t.Fatal(name, code, ok)
		}
	}
}
