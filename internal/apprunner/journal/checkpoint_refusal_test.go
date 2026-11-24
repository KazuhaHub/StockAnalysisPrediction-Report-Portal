package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

func checkpointNativeFailing(err error, calls *int) checkpointNative {
	return func(context.Context, data.CheckpointRequest) (data.CheckpointObservation, error) {
		*calls++
		return data.CheckpointObservation{}, err
	}
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
// checkpoint never writes to the data, so the command ends reverted, the checkpoint row ends, and the answer is typed.
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
			calls := 0
			_, err := f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(tc.err, &calls))
			var failed *DataEffectFailedError
			if !errors.As(err, &failed) || failed.Code != tc.code || !errors.Is(err, tc.err) || calls != 1 {
				t.Fatal("refusal was not a typed data effect failure", err, calls)
			}
			state, receipt, checkpoint, unknown := checkpointRefusalRows(t, f.store, f.command.EffectID, f.command.Payload.CheckpointID)
			record, ok := parseDataEffectFailure(receipt)
			if state != "reverted" || !ok || record.Code != tc.code || record.Dirty || checkpoint != "deleted" || unknown != 0 {
				t.Fatal("refusal was not settled", state, receipt, checkpoint, unknown)
			}
			// The settled refusal is replayed as the same typed answer and never reaches the native layer again.
			_, err = f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(io.EOF, &calls))
			if !errors.As(err, &failed) || failed.Code != tc.code || calls != 1 {
				t.Fatal("a settled refusal was not replayed as the same answer", err, calls)
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
			calls := 0
			_, err := f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(native, &calls))
			var failed *DataEffectFailedError
			if err == nil || errors.As(err, &failed) || !errors.Is(err, native) {
				t.Fatal("an uncertain failure was typed as a refusal", err)
			}
			state, receipt, checkpoint, _ := checkpointRefusalRows(t, f.store, f.command.EffectID, f.command.Payload.CheckpointID)
			if state != "unknown" || receipt != "{}" || checkpoint != "preparing" {
				t.Fatal("an uncertain failure was settled", state, receipt, checkpoint)
			}
		})
	}
}

// A snapshot left unknown (the wedged production command) settles as refused on its next replay when its checkpoint
// row is still preparing: a checkpoint never writes to the data, so the effect cannot have applied.
func TestUnknownCheckpointSettlesAsRefusedOnReplay(t *testing.T) {
	f := newCheckpointFixture(t)
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err := f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(io.EOF, &calls))
	var failed *DataEffectFailedError
	if !errors.As(err, &failed) || failed.Code != dataFailureCheckpointUnsettled || calls != 0 {
		t.Fatal("unknown snapshot did not settle as refused", err, calls)
	}
	state, receipt, checkpoint, unknown := checkpointRefusalRows(t, f.store, f.command.EffectID, f.command.Payload.CheckpointID)
	if record, ok := parseDataEffectFailure(receipt); state != "reverted" || !ok || record.Code != dataFailureCheckpointUnsettled || record.Dirty || checkpoint != "deleted" || unknown != 0 {
		t.Fatal("unknown snapshot was not settled", state, receipt, checkpoint, unknown)
	}
}

// The replay only settles a snapshot whose checkpoint row is still preparing; anything else stays for review.
func TestUnknownCheckpointWithAnotherRecordIsNotSettled(t *testing.T) {
	f := newCheckpointFixture(t)
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`UPDATE runner_checkpoints SET state='review_required' WHERE id=?`, f.command.Payload.CheckpointID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err := f.store.dispatchCheckpointWith(context.Background(), f.raw, f.sig, checkpointNativeFailing(io.EOF, &calls))
	var failed *DataEffectFailedError
	if !errors.Is(err, ErrFenceReconciliation) || errors.As(err, &failed) || calls != 0 {
		t.Fatal("a snapshot whose checkpoint is not preparing was settled", err, calls)
	}
	state, receipt, checkpoint, unknown := checkpointRefusalRows(t, f.store, f.command.EffectID, f.command.Payload.CheckpointID)
	if state != "unknown" || receipt != "{}" || checkpoint != "review_required" || unknown != 1 {
		t.Fatal("rows changed", state, receipt, checkpoint, unknown)
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
