//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// importSnapshotFor is the observation a correct native import would mint for
// the intent; tests edit single fields to prove each binding is checked.
func importSnapshotFor(intent importIntent) data.ImportSnapshot {
	return data.ImportSnapshot{
		NamespaceID: intent.command.NamespaceID, ReleaseSHA256: intent.command.ReleaseSHA256,
		ManifestSHA256: intent.command.ManifestSHA256, ProjectID: intent.project, DataFormat: 1,
		Entries: 3, UsedBytes: 8192, UsedInodes: 4, ByteLimit: intent.quotaBytes, InodeLimit: intent.quotaInodes,
		ObservedMS: time.Now().UTC().UnixMilli(), NamespaceDevice: 78, NamespaceInode: 77,
	}
}

func claimedImportFixture(t *testing.T) (*Store, control.ImportCheckpointCommand, []byte, []byte, importIntent) {
	t.Helper()
	store, command, raw, signature := importStoreFixture(t)
	intent, _, err := store.beginImportCheckpoint(context.Background(), raw, signature, importDataPin)
	if err != nil {
		t.Fatal(err)
	}
	return store, command, raw, signature, intent
}

func TestImportReplayReturnsSameReceipt(t *testing.T) {
	store, command, raw, signature, intent := claimedImportFixture(t)
	receipt, err := store.settleImportCheckpoint(context.Background(), intent, importSnapshotFor(intent))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "imported" || receipt.NamespaceInode != "77" || receipt.Device != "78" || receipt.ProjectID != strconv.FormatUint(uint64(intent.project), 10) ||
		markerImportRoot(receipt.Claim) != importDataPin || !importReceiptMatchesCommand(receipt, command) {
		t.Fatalf("receipt: %+v", receipt)
	}
	var state string
	var revision int64
	var observed string
	if err := store.db.QueryRow(`SELECT observed_state,revision,CAST(observed_ms AS TEXT) FROM runner_resources WHERE id=?`, command.Payload.NamespaceID).Scan(&state, &revision, &observed); err != nil ||
		state != "present" || revision != 2 || observed != receipt.SettledMS {
		t.Fatal("resource not settled with the receipt", state, revision, observed, err)
	}
	first, err := store.ReadImportCheckpointReceipt(context.Background(), raw, signature)
	if err != nil || first != receipt {
		t.Fatalf("read receipt: %+v %v", first, err)
	}
	// Replay through the dispatcher never touches a native root.
	for range 2 {
		replayed, err := store.DispatchImportCheckpoint(context.Background(), raw, signature, &data.Root{}, importDataPin, &data.Root{}, importOtherPin)
		if err != nil || replayed != receipt {
			t.Fatalf("replay changed the receipt: %+v %v", replayed, err)
		}
	}
	if _, err := store.DispatchImportCheckpoint(context.Background(), raw, signature, &data.Root{}, importOtherPin, &data.Root{}, importDataPin); err == nil {
		t.Fatal("receipt replayed for a different data root")
	}
	// A second settlement never rewrites an applied receipt.
	if _, err := store.settleImportCheckpoint(context.Background(), intent, importSnapshotFor(intent)); err == nil {
		t.Fatal("applied import settled twice")
	}
	if got, err := store.ReadImportCheckpointReceipt(context.Background(), raw, signature); err != nil || got != receipt {
		t.Fatal("second settlement changed the receipt", err)
	}
}

func TestImportReadRefusesChangedEvidence(t *testing.T) {
	for _, mode := range []string{"receipt field", "resource state", "resource revision", "not applied", "grant"} {
		t.Run(mode, func(t *testing.T) {
			store, command, raw, signature, intent := claimedImportFixture(t)
			if _, err := store.settleImportCheckpoint(context.Background(), intent, importSnapshotFor(intent)); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "receipt field":
				mustJournalExec(t, store.db, `UPDATE runner_commands SET receipt_json=replace(receipt_json,'"manifest_sha256":"`+strings.Repeat("b", 64)+`"','"manifest_sha256":"`+strings.Repeat("c", 64)+`"') WHERE effect_id=?`, command.EffectID)
			case "resource state":
				mustJournalExec(t, store.db, `UPDATE runner_resources SET observed_state='absent' WHERE id=?`, command.Payload.NamespaceID)
			case "resource revision":
				mustJournalExec(t, store.db, `UPDATE runner_resources SET revision=3 WHERE id=?`, command.Payload.NamespaceID)
			case "not applied":
				mustJournalExec(t, store.db, `UPDATE runner_commands SET state='unknown' WHERE effect_id=?`, command.EffectID)
			case "grant":
				mustJournalExec(t, store.db, `UPDATE runner_commands SET receipt_json=replace(receipt_json,'`+command.Payload.GrantID+`','`+fixtureID(1999)+`') WHERE effect_id=?`, command.EffectID)
			}
			if _, err := store.ReadImportCheckpointReceipt(context.Background(), raw, signature); err == nil {
				t.Fatal("changed evidence accepted")
			}
		})
	}
}

func TestImportSettleRefusesMismatchedObservation(t *testing.T) {
	for name, mutate := range map[string]func(*data.ImportSnapshot){
		"namespace":   func(s *data.ImportSnapshot) { s.NamespaceID = fixtureID(1998) },
		"release":     func(s *data.ImportSnapshot) { s.ReleaseSHA256 = strings.Repeat("f", 64) },
		"manifest":    func(s *data.ImportSnapshot) { s.ManifestSHA256 = strings.Repeat("f", 64) },
		"project":     func(s *data.ImportSnapshot) { s.ProjectID++ },
		"data format": func(s *data.ImportSnapshot) { s.DataFormat = 2 },
		"byte quota":  func(s *data.ImportSnapshot) { s.ByteLimit -= 512 },
		"inode quota": func(s *data.ImportSnapshot) { s.InodeLimit++ },
		"no inode":    func(s *data.ImportSnapshot) { s.NamespaceInode = 0 },
		"over quota":  func(s *data.ImportSnapshot) { s.UsedBytes = s.ByteLimit + 1 },
		"clock":       func(s *data.ImportSnapshot) { s.ObservedMS = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			store, command, _, _, intent := claimedImportFixture(t)
			snapshot := importSnapshotFor(intent)
			mutate(&snapshot)
			if _, err := store.settleImportCheckpoint(context.Background(), intent, snapshot); err == nil {
				t.Fatal("mismatched native observation settled")
			}
			var state string
			var observed string
			if err := store.db.QueryRow(`SELECT c.state,r.observed_state FROM runner_commands c,runner_resources r WHERE c.effect_id=? AND r.id=?`, command.EffectID, command.Payload.NamespaceID).Scan(&state, &observed); err != nil ||
				state != "unknown" || observed != "unknown" {
				t.Fatal("refused settlement changed state", state, observed, err)
			}
		})
	}
}

func TestImportUnknownReconcilesNotRetries(t *testing.T) {
	directory := privateDirectory(t)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnrollController(context.Background(), enrollmentFixture(30)); err != nil {
		t.Fatal(err)
	}
	command := importCommandFixture(t, 1700)
	digest, _ := control.ImportCheckpointLogicalDigest(command)
	command.PayloadSHA256 = digest
	raw, signature := signImportCommand(t, command)
	if _, _, err := store.beginImportCheckpoint(context.Background(), raw, signature, importDataPin); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// The dispatcher never claims again: with an unusable native root it fails
	// and leaves the unknown claim byte-for-byte as it was.
	if _, err := store.DispatchImportCheckpoint(context.Background(), raw, signature, &data.Root{}, importDataPin, &data.Root{}, importOtherPin); err == nil {
		t.Fatal("dispatch reported success without native evidence")
	}
	var after, state string
	var rows int
	if err := store.db.QueryRow(`SELECT receipt_json,state FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&after, &state); err != nil || after != before || state != "unknown" {
		t.Fatal("dispatch of unknown work rewrote the claim", state, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runner_commands`).Scan(&rows); err != nil || rows != 1 {
		t.Fatal("dispatch of unknown work wrote a second command")
	}
	// After a restart nothing in memory remains: reconciliation reloads the claim
	// from the journal and settles only against matching native evidence.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.loadUnknownImport(context.Background(), raw, signature, importOtherPin); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatalf("unknown import reloaded under a foreign root: %v", err)
	}
	intent, err := store.loadUnknownImport(context.Background(), raw, signature, importDataPin)
	if err != nil {
		t.Fatal(err)
	}
	wrong := importSnapshotFor(intent)
	wrong.ManifestSHA256 = strings.Repeat("9", 64)
	if _, err := store.settleImportCheckpoint(context.Background(), intent, wrong); err == nil {
		t.Fatal("reconciliation settled mismatched evidence")
	}
	receipt, err := store.settleImportCheckpoint(context.Background(), intent, importSnapshotFor(intent))
	if err != nil || receipt.EffectID != command.EffectID || receipt.Claim.ClaimID != intent.marker.ClaimID {
		t.Fatalf("reconciliation: %+v %v", receipt, err)
	}
	if _, err := store.loadUnknownImport(context.Background(), raw, signature, importDataPin); err == nil {
		t.Fatal("applied import reloaded as unknown")
	}
	// Reconciling after the signed window still works, because settlement only
	// needs the command to have been issued by the current controller.
	expired := time.Now().UTC().UnixMilli() + 3600000
	if _, err := control.VerifyImportCheckpointSettlement(raw, signature, importAuthority(), expired); err != nil {
		t.Fatal(err)
	}
}
