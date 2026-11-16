//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// fakeDeleteNative is the native side of a deletion, with the seams a crash needs.
type fakeDeleteNative struct {
	mu                sync.Mutex
	files             map[string]int64
	inspects, removes int
	onRemove          func(id string)
	// removeErr fails the call before it touches anything (a crash after the claim, before the unlink).
	removeErr error
	// afterRemoveErr removes the file and then fails (a crash after the unlink, before the settlement).
	afterRemoveErr error
	inspectErr     error
	// trash holds the checkpoint ids whose archive was left under a trash name by an interrupted removal.
	trash map[string]bool
}

func (n *fakeDeleteNative) InspectCheckpointArchive(_ context.Context, id string) (data.CheckpointArchive, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.inspects++
	if n.inspectErr != nil {
		return data.CheckpointArchive{}, n.inspectErr
	}
	size, ok := n.files[id]
	return data.CheckpointArchive{Found: ok, Bytes: size}, nil
}

func (n *fakeDeleteNative) RemoveCheckpointArchive(_ context.Context, id string, archiveBytes int64, _ string) (data.CheckpointArchive, error) {
	n.mu.Lock()
	n.removes++
	hook := n.onRemove
	n.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.removeErr != nil {
		return data.CheckpointArchive{}, n.removeErr
	}
	if n.trash[id] {
		delete(n.trash, id)
	}
	size, ok := n.files[id]
	if !ok {
		return data.CheckpointArchive{}, nil
	}
	if size != archiveBytes {
		return data.CheckpointArchive{}, data.ErrArchiveMismatch
	}
	delete(n.files, id)
	if n.afterRemoveErr != nil {
		return data.CheckpointArchive{}, n.afterRemoveErr
	}
	return data.CheckpointArchive{Found: true, Bytes: size}, nil
}

func (n *fakeDeleteNative) has(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.files[id]
	return ok
}

func (n *fakeDeleteNative) counts() (int, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.inspects, n.removes
}

type deleteCheckpointFixtureT struct {
	checkpointFixture
	checkpoint CheckpointReceipt
	restoreCmd control.RestoreCommand
	restoreRaw []byte
	restoreSig []byte
	native     *fakeDeleteNative
	key        ed25519.PrivateKey
	command    control.DeleteCheckpointCommand
	raw, sig   []byte
}

// newDeleteCheckpointFixture builds a journal that holds one verified checkpoint (revision 2) with a retained
// archive, and a signed deletion of it. The namespace's volume is marked absent, so the checkpoint is deletable;
// tests that need a live namespace bring it back.
func newDeleteCheckpointFixture(t *testing.T) *deleteCheckpointFixtureT {
	t.Helper()
	f, checkpoint, restoreCmd, restoreRaw, restoreSig := restoreJournalFixture(t)
	d := &deleteCheckpointFixtureT{checkpointFixture: f, checkpoint: checkpoint, restoreCmd: restoreCmd, restoreRaw: restoreRaw, restoreSig: restoreSig,
		key: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), native: &fakeDeleteNative{files: map[string]int64{checkpoint.CheckpointID: 4096}}}
	d.setVolume(t, "absent")
	d.command, d.raw, d.sig = d.build(t, 2, 5100, nil)
	return d
}

func (d *deleteCheckpointFixtureT) setVolume(t *testing.T, observed string) {
	t.Helper()
	mustJournalExec(t, d.store.db, `UPDATE runner_resources SET observed_state=? WHERE data_namespace_id=? AND kind='volume'`, observed, d.checkpoint.NamespaceID)
	var n int
	if err := d.store.db.QueryRow(`SELECT COUNT(*) FROM runner_resources WHERE data_namespace_id=? AND kind='volume'`, d.checkpoint.NamespaceID).Scan(&n); err != nil || n == 0 {
		t.Fatalf("the fixture has no volume resource for the namespace: %d %v", n, err)
	}
}

// build signs a deletion of the fixture's checkpoint. Fence and generation sit above every floor the fixture holds.
func (d *deleteCheckpointFixtureT) build(t *testing.T, slot, effect int, mutate func(*control.DeleteCheckpointCommand)) (control.DeleteCheckpointCommand, []byte, []byte) {
	t.Helper()
	authority := importAuthority()
	c := control.DeleteCheckpointCommand{CommandHeader: control.CommandHeader{
		Format: control.DeleteCheckpointFormat, DeploymentID: authority.DeploymentID, ControllerEpoch: authority.ControllerEpoch, ControlRevision: "3",
		InstallationID: d.checkpoint.InstallationID, OperationID: fixtureID(effect + 1), TargetGeneration: "90", Stage: control.DeleteCheckpointStage,
		EffectNumber: strconv.Itoa(slot), EffectID: fixtureID(effect), Kind: control.DeleteCheckpointKind, InstallationFence: "90",
	}, Payload: control.DeleteCheckpointPayload{
		RecoverySetID: fixtureID(effect + 2), CheckpointID: d.checkpoint.CheckpointID, NamespaceID: d.checkpoint.NamespaceID,
		SourceOperationID: d.checkpoint.OperationID, ReleaseSHA256: d.checkpoint.ReleaseSHA256, ManifestSHA256: d.checkpoint.ManifestSHA256,
		ArchiveBytes: d.checkpoint.ArchiveBytes, DataFormat: d.checkpoint.DataFormat, WriterFence: d.checkpoint.WriterFence,
	}}
	if mutate != nil {
		mutate(&c)
	}
	digest, err := control.DeleteCheckpointLogicalDigest(c)
	if err != nil {
		t.Fatalf("digest: %v header=%+v payload=%+v", err, c.CommandHeader, c.Payload)
	}
	c.PayloadSHA256 = digest
	now := time.Now().UTC().UnixMilli()
	c.IssuedMS, c.ExpiresMS = strconv.FormatInt(now-1000, 10), strconv.FormatInt(now+25000, 10)
	raw, sig, err := control.SignDeleteCheckpoint(c, d.key, authority, now)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return c, raw, sig
}

func (d *deleteCheckpointFixtureT) dispatch() (any, error) {
	return d.store.DispatchDeleteCheckpoint(context.Background(), d.raw, d.sig, d.native)
}

func (d *deleteCheckpointFixtureT) dispatchCommand(raw, sig []byte) (any, error) {
	return d.store.DispatchDeleteCheckpoint(context.Background(), raw, sig, d.native)
}

func (d *deleteCheckpointFixtureT) readSig(t *testing.T, raw []byte) []byte {
	t.Helper()
	sig, err := control.SignDeleteCheckpointRead(raw, d.key, importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func (d *deleteCheckpointFixtureT) checkpointRow(t *testing.T) (string, int64) {
	t.Helper()
	var state string
	var revision int64
	if err := d.store.db.QueryRow(`SELECT state,revision FROM runner_checkpoints WHERE id=?`, d.checkpoint.CheckpointID).Scan(&state, &revision); err != nil {
		t.Fatal(err)
	}
	return state, revision
}

func (d *deleteCheckpointFixtureT) commandRow(t *testing.T, effect string) (string, string) {
	t.Helper()
	var state, receipt string
	if err := d.store.db.QueryRow(`SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`, effect).Scan(&state, &receipt); err != nil {
		return "", ""
	}
	return state, receipt
}

func (d *deleteCheckpointFixtureT) commandRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := d.store.db.QueryRow(`SELECT COUNT(*) FROM runner_commands WHERE kind='delete-checkpoint'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func mustDeleteReceipt(t *testing.T) func(any, error) DeleteCheckpointReceipt {
	return func(got any, err error) DeleteCheckpointReceipt {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		receipt, ok := got.(DeleteCheckpointReceipt)
		if !ok {
			t.Fatalf("not a receipt: %#v", got)
		}
		return receipt
	}
}

// 1. A verified checkpoint, at revision 2 and at revision 4 (the state after a completed restore), is deleted: the
// row ends deleted at revision+1, the archive is gone and the receipt says removed. Fails if the file remains or the
// row stays verified.
func TestDeleteCheckpointRetiresAVerifiedCheckpointAtRevisionTwoAndFour(t *testing.T) {
	for _, revision := range []int64{2, 4} {
		t.Run("revision "+strconv.FormatInt(revision, 10), func(t *testing.T) {
			d := newDeleteCheckpointFixture(t)
			mustJournalExec(t, d.store.db, `UPDATE runner_checkpoints SET revision=? WHERE id=?`, revision, d.checkpoint.CheckpointID)
			receipt := mustDeleteReceipt(t)(d.dispatch())
			if receipt.Outcome != DeleteOutcomeRemoved || !receipt.ArchiveFound || !DeleteCheckpointReceiptMatchesCommand(receipt, d.command) {
				t.Fatalf("receipt: %+v", receipt)
			}
			if state, got := d.checkpointRow(t); state != "deleted" || got != revision+1 {
				t.Fatalf("checkpoint row %s revision %d", state, got)
			}
			if d.native.has(d.checkpoint.CheckpointID) {
				t.Fatal("the archive remains")
			}
			if state, _ := d.commandRow(t, d.command.EffectID); state != "applied" {
				t.Fatalf("command row %q", state)
			}
		})
	}
}

// The claim marks the checkpoint deleted and records the command unknown before any native call, and the claim is
// the canonical, command-bound marker.
func TestDeleteCheckpointClaimsBeforeTheArchiveIsTouched(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	var stateAtCall, receiptAtCall, checkpointAtCall string
	d.native.onRemove = func(string) {
		stateAtCall, receiptAtCall = d.commandRow(t, d.command.EffectID)
		checkpointAtCall, _ = d.checkpointRow(t)
	}
	receipt := mustDeleteReceipt(t)(d.dispatch())
	if stateAtCall != "unknown" || checkpointAtCall != "deleted" {
		t.Fatalf("at the native call the effect was %q and the checkpoint %q", stateAtCall, checkpointAtCall)
	}
	var claim deleteCheckpointClaim
	if !canonicalDataJSON(receiptAtCall, &claim) || !validDeleteCheckpointClaim(claim) || !deleteCheckpointClaimMatchesCommand(claim, d.command) {
		t.Fatalf("claim marker is not canonical and bound to the command: %s", receiptAtCall)
	}
	if receipt.Claim.ClaimID != claim.ClaimID {
		t.Fatal("the receipt does not carry the claim")
	}
}

// 2. Replaying an applied effect id returns the identical receipt with no native call. Fails on a second removal.
func TestDeleteCheckpointReplayReturnsTheReceiptWithoutANativeCall(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	first := mustDeleteReceipt(t)(d.dispatch())
	_, removesBefore := d.native.counts()
	second := mustDeleteReceipt(t)(d.dispatch())
	if first != second {
		t.Fatalf("replay differs:\n%+v\n%+v", first, second)
	}
	if inspects, removes := d.native.counts(); removes != removesBefore || inspects != 0 {
		t.Fatalf("replay touched the native side: inspects=%d removes=%d (was %d)", inspects, removes, removesBefore)
	}
}

// 3. Each of restoring, preparing and review_required gives its typed refusal; the archive and the row are untouched
// and the command row is reverted. Fails closed: any deletion fails the test.
func TestDeleteCheckpointRefusesCheckpointsThatAreNotDeletable(t *testing.T) {
	for state, want := range map[string]string{
		"restoring":       DeleteOutcomeInUse,
		"preparing":       DeleteOutcomeNotVerified,
		"review_required": DeleteOutcomeNotVerified,
	} {
		t.Run(state, func(t *testing.T) {
			d := newDeleteCheckpointFixture(t)
			if state == "preparing" {
				mustJournalExec(t, d.store.db, `UPDATE runner_checkpoints SET state='preparing',revision=1,manifest_sha256=NULL,bytes=0,verified_ms=NULL WHERE id=?`, d.checkpoint.CheckpointID)
			} else {
				mustJournalExec(t, d.store.db, `UPDATE runner_checkpoints SET state=? WHERE id=?`, state, d.checkpoint.CheckpointID)
			}
			beforeState, beforeRevision := d.checkpointRow(t)
			receipt := mustDeleteReceipt(t)(d.dispatch())
			if receipt.Outcome != want || receipt.Applied() || receipt.ArchiveFound {
				t.Fatalf("receipt: %+v", receipt)
			}
			if afterState, afterRevision := d.checkpointRow(t); afterState != beforeState || afterRevision != beforeRevision {
				t.Fatalf("the row changed: %s/%d -> %s/%d", beforeState, beforeRevision, afterState, afterRevision)
			}
			if !d.native.has(d.checkpoint.CheckpointID) {
				t.Fatal("a refused deletion removed the archive")
			}
			if _, removes := d.native.counts(); removes != 0 {
				t.Fatal("a refusal made a native removal call")
			}
			if row, _ := d.commandRow(t, d.command.EffectID); row != "reverted" {
				t.Fatalf("command row %q", row)
			}
		})
	}
}

// A revision other than 2 or 4 is not a verified checkpoint the runner recognises.
func TestDeleteCheckpointRefusesAVerifiedCheckpointAtAnUnknownRevision(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	mustJournalExec(t, d.store.db, `UPDATE runner_checkpoints SET revision=3 WHERE id=?`, d.checkpoint.CheckpointID)
	if receipt := mustDeleteReceipt(t)(d.dispatch()); receipt.Outcome != DeleteOutcomeNotVerified {
		t.Fatalf("receipt: %+v", receipt)
	}
	if !d.native.has(d.checkpoint.CheckpointID) {
		t.Fatal("the archive was removed")
	}
}

// 4. Each payload field mismatched in turn gives checkpoint_identity_mismatch and nothing is deleted. The header's
// installation is one of the bound fields.
func TestDeleteCheckpointBindsEveryIdentityField(t *testing.T) {
	other := fixtureID(7777)
	other2 := strings.Repeat("e", 64)
	for name, mutate := range map[string]func(*control.DeleteCheckpointCommand){
		"namespace":    func(c *control.DeleteCheckpointCommand) { c.Payload.NamespaceID = other },
		"source":       func(c *control.DeleteCheckpointCommand) { c.Payload.SourceOperationID = other },
		"release":      func(c *control.DeleteCheckpointCommand) { c.Payload.ReleaseSHA256 = other2 },
		"manifest":     func(c *control.DeleteCheckpointCommand) { c.Payload.ManifestSHA256 = other2 },
		"bytes":        func(c *control.DeleteCheckpointCommand) { c.Payload.ArchiveBytes = "4097" },
		"data format":  func(c *control.DeleteCheckpointCommand) { c.Payload.DataFormat = "9" },
		"writer fence": func(c *control.DeleteCheckpointCommand) { c.Payload.WriterFence = "9" },
		"installation": func(c *control.DeleteCheckpointCommand) { c.InstallationID = other },
	} {
		t.Run(name, func(t *testing.T) {
			d := newDeleteCheckpointFixture(t)
			c, raw, sig := d.build(t, 2, 5200, mutate)
			receipt := mustDeleteReceipt(t)(d.dispatchCommand(raw, sig))
			if receipt.Outcome != DeleteOutcomeIdentityMismatch || receipt.Applied() || !DeleteCheckpointReceiptMatchesCommand(receipt, c) {
				t.Fatalf("receipt: %+v", receipt)
			}
			if state, revision := d.checkpointRow(t); state != "verified" || revision != 2 {
				t.Fatalf("the row changed: %s/%d", state, revision)
			}
			if !d.native.has(d.checkpoint.CheckpointID) {
				t.Fatal("the archive was removed")
			}
			if _, removes := d.native.counts(); removes != 0 {
				t.Fatal("a mismatch made a native removal call")
			}
		})
	}
}

// 5. The newest checkpoint of a namespace whose volume is present gives checkpoint_latest. After the volume is
// absent the same checkpoint is deleted. A checkpoint with a newer sibling is deletable while the volume lives.
func TestDeleteCheckpointRefusesTheNewestCheckpointOfALiveNamespace(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	d.setVolume(t, "present")
	receipt := mustDeleteReceipt(t)(d.dispatch())
	if receipt.Outcome != DeleteOutcomeLatest || receipt.Applied() {
		t.Fatalf("receipt: %+v", receipt)
	}
	if !d.native.has(d.checkpoint.CheckpointID) {
		t.Fatal("the latest checkpoint was removed")
	}
	// The same checkpoint, once the namespace is gone, under a new attempt's effect.
	d.setVolume(t, "absent")
	c, raw, sig := d.build(t, 3, 5300, nil)
	if got := mustDeleteReceipt(t)(d.dispatchCommand(raw, sig)); got.Outcome != DeleteOutcomeRemoved || !DeleteCheckpointReceiptMatchesCommand(got, c) {
		t.Fatalf("receipt: %+v", got)
	}
}

// Unusable newer rows are not a fallback: with a live volume the newest VERIFIED checkpoint stays protected whatever
// preparing, restoring or review_required rows sit beside it. Fails if any non-deleted row counts as newer.
func TestDeleteCheckpointLatestGuardCountsOnlyVerifiedSiblings(t *testing.T) {
	for _, state := range []string{"preparing", "restoring", "review_required"} {
		t.Run(state, func(t *testing.T) {
			d := newDeleteCheckpointFixture(t)
			d.setVolume(t, "present")
			insertSiblingCheckpoint(t, d, fixtureID(7101), state, 1000, "created_ms+1000")
			if receipt := mustDeleteReceipt(t)(d.dispatch()); receipt.Outcome != DeleteOutcomeLatest {
				t.Fatalf("a %s sibling made the newest verified checkpoint deletable: %+v", state, receipt)
			}
			if !d.native.has(d.checkpoint.CheckpointID) {
				t.Fatal("the archive was removed")
			}
		})
	}
}

// The tie-break matches the order the runner reads the newest checkpoint in: equal created_ms, smaller id is newer.
func TestDeleteCheckpointLatestGuardTieBreaksOnTheSmallerID(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	d.setVolume(t, "present")
	// A verified sibling with the same created_ms and a LARGER id is older: this checkpoint is the newest, refused.
	insertSiblingCheckpoint(t, d, "ffffffff-ffff-4fff-8fff-ffffffffffff", "verified", 0, "created_ms")
	if receipt := mustDeleteReceipt(t)(d.dispatch()); receipt.Outcome != DeleteOutcomeLatest {
		t.Fatalf("a tie with a larger id must leave this checkpoint the newest: %+v", receipt)
	}
	// A verified sibling with the same created_ms and a SMALLER id is newer: this one is deletable.
	d2 := newDeleteCheckpointFixture(t)
	d2.setVolume(t, "present")
	insertSiblingCheckpoint(t, d2, "00000000-0000-4000-8000-000000000001", "verified", 0, "created_ms")
	if receipt := mustDeleteReceipt(t)(d2.dispatch()); receipt.Outcome != DeleteOutcomeRemoved {
		t.Fatalf("a tie with a smaller id must make this checkpoint older: %+v", receipt)
	}
}

func insertSiblingCheckpoint(t *testing.T, d *deleteCheckpointFixtureT, id, state string, _ int, created string) {
	t.Helper()
	mustJournalExec(t, d.store.db, `INSERT INTO runner_checkpoints(id,data_namespace_id,installation_id,operation_id,release_sha256,data_format,writer_fence,manifest_sha256,bytes,state,revision,verified_ms,created_ms,updated_ms)
		SELECT ?,data_namespace_id,installation_id,?,release_sha256,data_format,writer_fence,CASE WHEN ?='preparing' THEN NULL ELSE manifest_sha256 END,CASE WHEN ?='preparing' THEN 0 ELSE bytes END,?,
		CASE WHEN ?='preparing' THEN 1 ELSE 2 END,CASE WHEN ?='preparing' THEN NULL ELSE verified_ms END,`+created+`,updated_ms FROM runner_checkpoints WHERE id=?`,
		id, fixtureID(7999), state, state, state, state, state, d.checkpoint.CheckpointID)
}

func TestDeleteCheckpointAdmitsAnOlderCheckpointOfALiveNamespace(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	d.setVolume(t, "present")
	mustJournalExec(t, d.store.db, `INSERT INTO runner_checkpoints(id,data_namespace_id,installation_id,operation_id,release_sha256,data_format,writer_fence,manifest_sha256,bytes,state,revision,verified_ms,created_ms,updated_ms)
		SELECT ?,data_namespace_id,installation_id,?,release_sha256,data_format,writer_fence,manifest_sha256,bytes,'verified',2,verified_ms,created_ms+1000,updated_ms+1000 FROM runner_checkpoints WHERE id=?`,
		fixtureID(7001), fixtureID(7002), d.checkpoint.CheckpointID)
	if receipt := mustDeleteReceipt(t)(d.dispatch()); receipt.Outcome != DeleteOutcomeRemoved {
		t.Fatalf("an older checkpoint was refused: %+v", receipt)
	}
	var newest string
	if err := d.store.db.QueryRow(`SELECT state FROM runner_checkpoints WHERE id=?`, fixtureID(7001)).Scan(&newest); err != nil || newest != "verified" {
		t.Fatal("the newer checkpoint changed", newest, err)
	}
}

// 6. A row absent with no file settles applied/absent in the claim transaction. A row absent with a file is refused
// checkpoint_unrecorded and the file is kept: the runner never deletes a file it has no record of.
func TestDeleteCheckpointWithNoRowSettlesAbsentOrRefusesAnUnrecordedFile(t *testing.T) {
	t.Run("no file", func(t *testing.T) {
		d := newDeleteCheckpointFixture(t)
		mustJournalExec(t, d.store.db, `DELETE FROM runner_checkpoints WHERE id=?`, d.checkpoint.CheckpointID)
		d.native.files = map[string]int64{}
		receipt := mustDeleteReceipt(t)(d.dispatch())
		if receipt.Outcome != DeleteOutcomeAbsent || !receipt.Applied() || receipt.ArchiveFound {
			t.Fatalf("receipt: %+v", receipt)
		}
		if state, _ := d.commandRow(t, d.command.EffectID); state != "applied" {
			t.Fatalf("command row %q", state)
		}
		if _, removes := d.native.counts(); removes != 0 {
			t.Fatal("nothing was there to remove")
		}
	})
	t.Run("a file with no record", func(t *testing.T) {
		d := newDeleteCheckpointFixture(t)
		mustJournalExec(t, d.store.db, `DELETE FROM runner_checkpoints WHERE id=?`, d.checkpoint.CheckpointID)
		receipt := mustDeleteReceipt(t)(d.dispatch())
		if receipt.Outcome != DeleteOutcomeUnrecorded || receipt.Applied() {
			t.Fatalf("receipt: %+v", receipt)
		}
		if !d.native.has(d.checkpoint.CheckpointID) {
			t.Fatal("an unrecorded file was deleted")
		}
		if _, removes := d.native.counts(); removes != 0 {
			t.Fatal("an unrecorded file was sent to removal")
		}
		if state, _ := d.commandRow(t, d.command.EffectID); state != "reverted" {
			t.Fatalf("command row %q", state)
		}
	})
	t.Run("an unanswerable probe claims nothing", func(t *testing.T) {
		d := newDeleteCheckpointFixture(t)
		mustJournalExec(t, d.store.db, `DELETE FROM runner_checkpoints WHERE id=?`, d.checkpoint.CheckpointID)
		d.native.inspectErr = errors.New("stat failed")
		if _, err := d.dispatch(); err == nil {
			t.Fatal("an unanswerable probe settled")
		}
		if d.commandRows(t) != 0 {
			t.Fatal("an unanswerable probe left a row")
		}
	})
}

// A checkpoint already deleted by another effect settles applied/absent under a new effect id, and the archive is not
// touched again.
func TestDeleteCheckpointOfADeletedRowSettlesAbsent(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	mustJournalExec(t, d.store.db, `UPDATE runner_checkpoints SET state='deleted',revision=3 WHERE id=?`, d.checkpoint.CheckpointID)
	receipt := mustDeleteReceipt(t)(d.dispatch())
	if receipt.Outcome != DeleteOutcomeAbsent || !receipt.Applied() {
		t.Fatalf("receipt: %+v", receipt)
	}
	if _, removes := d.native.counts(); removes != 0 {
		t.Fatal("a deleted row sent a removal")
	}
}

// 7. An unsettled (prepared or unknown) command that is installation work gives the pre-claim refusal with no row
// written, for every such kind. A load-image, remove-image or delete-checkpoint row does not block.
func TestDeleteCheckpointRefusesWhileTheInstallationHasUnsettledWork(t *testing.T) {
	kinds := []string{"restore", "migrate-data", "discard-migration", "update-start", "import-checkpoint", "snapshot", "start-candidate", "stop", "delete-data", "remove-code"}
	for _, kind := range kinds {
		for _, state := range []string{"prepared", "unknown"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				d := newDeleteCheckpointFixture(t)
				injectInstallationCommand(t, d, kind, state, 9100)
				_, err := d.dispatch()
				if !errors.Is(err, ErrDeleteCheckpointInstallationBusy) {
					t.Fatalf("a %s %s row did not block: %v", state, kind, err)
				}
				if d.commandRows(t) != 0 {
					t.Fatal("the refusal wrote a row")
				}
				if st, revision := d.checkpointRow(t); st != "verified" || revision != 2 || !d.native.has(d.checkpoint.CheckpointID) {
					t.Fatal("the refusal changed the checkpoint or its archive")
				}
			})
		}
	}
	for _, kind := range []string{"load-image", "remove-image", "delete-checkpoint"} {
		t.Run("neutral "+kind, func(t *testing.T) {
			d := newDeleteCheckpointFixture(t)
			injectInstallationCommand(t, d, kind, "unknown", 9200)
			if receipt := mustDeleteReceipt(t)(d.dispatch()); receipt.Outcome != DeleteOutcomeRemoved {
				t.Fatalf("a %s row blocked the deletion: %+v", kind, receipt)
			}
		})
	}
	t.Run("another installation", func(t *testing.T) {
		d := newDeleteCheckpointFixture(t)
		injectInstallationCommand(t, d, "restore", "unknown", 9300)
		mustJournalExec(t, d.store.db, `UPDATE runner_commands SET installation_id=? WHERE effect_id=?`, fixtureID(9999), fixtureID(9300))
		if receipt := mustDeleteReceipt(t)(d.dispatch()); receipt.Outcome != DeleteOutcomeRemoved {
			t.Fatalf("another installation's work blocked the deletion: %+v", receipt)
		}
	})
}

// injectInstallationCommand adds a row of the given kind and state for the fixture's installation, copied from an
// existing row so every constraint holds. It takes effect number 20 of its own operation.
func injectInstallationCommand(t *testing.T, d *deleteCheckpointFixtureT, kind, state string, effect int) {
	t.Helper()
	mustJournalExec(t, d.store.db, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		SELECT ?,?,installation_id,'stage',20,?,payload_sha256,target_generation,controller_epoch,admitted_fence,?,'{}',NULL,created_ms,updated_ms FROM runner_commands WHERE installation_id=? ORDER BY rowid LIMIT 1`,
		fixtureID(effect), fixtureID(effect+1), kind, state, d.checkpoint.InstallationID)
}

// 8. A fence or generation below the floor is a command conflict with no row written. Effect number 1 is the
// delete-data command's and the signed command refuses it.
func TestDeleteCheckpointRefusesBelowTheFloorsAndEffectNumberOne(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	// A neutral row still sets the floors: the host operation's own fence and generation are the installation's.
	injectInstallationCommand(t, d, "load-image", "applied", 9400)
	for name, floor := range map[string]struct {
		row, sql string
		mutate   func(*control.DeleteCheckpointCommand)
	}{
		"fence": {"fence", `UPDATE runner_commands SET admitted_fence=95,target_generation=1 WHERE effect_id=?`,
			func(c *control.DeleteCheckpointCommand) {}},
		"generation": {"generation", `UPDATE runner_commands SET admitted_fence=1,target_generation=95 WHERE effect_id=?`,
			func(c *control.DeleteCheckpointCommand) { c.InstallationFence = "100" }},
	} {
		mustJournalExec(t, d.store.db, floor.sql, fixtureID(9400))
		_, raw, sig := d.build(t, 2, 5400, floor.mutate)
		if _, err := d.dispatchCommand(raw, sig); !errors.Is(err, ErrCommandConflict) {
			t.Fatalf("%s below the floor: %v", name, err)
		}
		if d.commandRows(t) != 0 {
			t.Fatalf("%s: a refusal wrote a row", name)
		}
	}
	if state, revision := d.checkpointRow(t); state != "verified" || revision != 2 || !d.native.has(d.checkpoint.CheckpointID) {
		t.Fatal("a refusal changed the checkpoint or its archive")
	}
	c := d.command
	c.EffectNumber = "1"
	if _, _, err := control.SignDeleteCheckpoint(c, d.key, importAuthority(), time.Now().UTC().UnixMilli()); err == nil {
		t.Fatal("effect number 1 was signed")
	}
}

// 9. The crash windows, injected through the native seam.
func TestDeleteCheckpointResolvesEveryCrashWindow(t *testing.T) {
	t.Run("crash after the claim and before the unlink", func(t *testing.T) {
		d := newDeleteCheckpointFixture(t)
		d.native.removeErr = errors.New("crashed before the unlink")
		if _, err := d.dispatch(); err == nil {
			t.Fatal("a failed removal settled")
		}
		if state, _ := d.commandRow(t, d.command.EffectID); state != "unknown" {
			t.Fatalf("command row %q", state)
		}
		if state, _ := d.checkpointRow(t); state != "deleted" {
			t.Fatalf("checkpoint row %q: the claim must mark it deleted before the file is touched", state)
		}
		if !d.native.has(d.checkpoint.CheckpointID) {
			t.Fatal("the archive is gone")
		}
		// The read-back of an unknown effect with the gate free is orphaned.
		status, err := d.store.ReadDeleteCheckpointOrStatus(context.Background(), d.raw, d.readSig(t, d.raw))
		if got, ok := status.(DeleteCheckpointStatus); err != nil || !ok || got.State != "orphaned" {
			t.Fatalf("read-back of the unknown effect: %#v %v", status, err)
		}
		d.native.removeErr = nil
		receipt := mustDeleteReceipt(t)(d.dispatch())
		if receipt.Outcome != DeleteOutcomeRemoved || !receipt.ArchiveFound || d.native.has(d.checkpoint.CheckpointID) {
			t.Fatalf("resolution: %+v", receipt)
		}
		if state, _ := d.commandRow(t, d.command.EffectID); state != "applied" {
			t.Fatalf("command row %q", state)
		}
	})
	t.Run("crash after the unlink and before the settlement", func(t *testing.T) {
		d := newDeleteCheckpointFixture(t)
		d.native.afterRemoveErr = errors.New("crashed after the unlink")
		if _, err := d.dispatch(); err == nil {
			t.Fatal("a crash settled")
		}
		if d.native.has(d.checkpoint.CheckpointID) {
			t.Fatal("the unlink did not happen")
		}
		if state, _ := d.commandRow(t, d.command.EffectID); state != "unknown" {
			t.Fatalf("command row %q", state)
		}
		d.native.afterRemoveErr = nil
		receipt := mustDeleteReceipt(t)(d.dispatch())
		if receipt.Outcome != DeleteOutcomeRemoved || receipt.ArchiveFound {
			t.Fatalf("an already removed archive must resolve as removed with archive_found false: %+v", receipt)
		}
	})
	t.Run("an archive that is not the claimed file leaves the effect unknown", func(t *testing.T) {
		d := newDeleteCheckpointFixture(t)
		d.native.files[d.checkpoint.CheckpointID] = 999
		_, err := d.dispatch()
		if !errors.Is(err, ErrFenceReconciliation) {
			t.Fatalf("a mismatched archive: %v", err)
		}
		if state, _ := d.commandRow(t, d.command.EffectID); state != "unknown" {
			t.Fatalf("command row %q: a mismatch must never be settled blind", state)
		}
		if !d.native.has(d.checkpoint.CheckpointID) {
			t.Fatal("a mismatched file was removed")
		}
	})
	t.Run("an unanswerable removal leaves the effect unknown", func(t *testing.T) {
		d := newDeleteCheckpointFixture(t)
		d.native.removeErr = data.ErrArchiveRemovalIncomplete
		if _, err := d.dispatch(); err == nil {
			t.Fatal("an unconfirmed removal settled")
		}
		if state, _ := d.commandRow(t, d.command.EffectID); state != "unknown" {
			t.Fatalf("command row %q", state)
		}
	})
}

// An earlier claim marked the row deleted and its removal never finished; the fence then moved and the Portal signed
// a new attempt. The retry must finish the removal, trash leftover included, settle both rows and only then answer.
// Fails if the retry answers absent without touching the disk, which leaks the archive for ever.
func TestDeleteCheckpointRetryFinishesAnUnprovenEarlierClaim(t *testing.T) {
	for _, leftover := range []bool{false, true} {
		name := "archive"
		if leftover {
			name = "trash leftover"
		}
		t.Run(name, func(t *testing.T) {
			d := newDeleteCheckpointFixture(t)
			d.native.removeErr = errors.New("crashed before the unlink")
			if _, err := d.dispatch(); err == nil {
				t.Fatal("a failed removal settled")
			}
			if leftover {
				// The interrupted removal had already moved the archive to its trash name.
				d.native.trash = map[string]bool{d.checkpoint.CheckpointID: true}
			}
			d.native.removeErr = nil
			// The fence moves: a newer operation of the installation, and a retry under a new effect on a new host.
			c, raw, sig := d.build(t, 3, 5600, func(c *control.DeleteCheckpointCommand) {
				c.InstallationFence, c.TargetGeneration = "95", "95"
			})
			receipt := mustDeleteReceipt(t)(d.dispatchCommand(raw, sig))
			if receipt.Outcome != DeleteOutcomeRemoved || !receipt.ArchiveFound || !DeleteCheckpointReceiptMatchesCommand(receipt, c) {
				t.Fatalf("the retry did not finish the removal: %+v", receipt)
			}
			if d.native.has(d.checkpoint.CheckpointID) || d.native.trash[d.checkpoint.CheckpointID] {
				t.Fatal("the archive or its leftover was leaked")
			}
			if state, _ := d.commandRow(t, d.command.EffectID); state != "applied" {
				t.Fatalf("the earlier claim stayed %q", state)
			}
			if state, _ := d.commandRow(t, c.EffectID); state != "applied" {
				t.Fatalf("the retry row is %q", state)
			}
			var raw1 string
			if err := d.store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, d.command.EffectID).Scan(&raw1); err != nil {
				t.Fatal(err)
			}
			settled, err := ParseDeleteCheckpointReceipt([]byte(raw1))
			if err != nil || !DeleteCheckpointReceiptMatchesCommand(settled, d.command) || settled.Outcome != DeleteOutcomeRemoved {
				t.Fatalf("the earlier claim's receipt: %+v %v", settled, err)
			}
			if state, rev := d.checkpointRow(t); state != "deleted" || rev != 3 {
				t.Fatalf("checkpoint row %s/%d", state, rev)
			}
		})
	}
	// The retry names another size or digest than the row and the earlier claim: nothing is claimed, removed or settled.
	for name, mutate := range map[string]func(*control.DeleteCheckpointCommand){
		"size":   func(c *control.DeleteCheckpointCommand) { c.Payload.ArchiveBytes = "4097" },
		"digest": func(c *control.DeleteCheckpointCommand) { c.Payload.ManifestSHA256 = strings.Repeat("e", 64) },
	} {
		t.Run("a retry with another "+name+" is refused", func(t *testing.T) {
			d := newDeleteCheckpointFixture(t)
			d.native.removeErr = errors.New("crashed before the unlink")
			if _, err := d.dispatch(); err == nil {
				t.Fatal("a failed removal settled")
			}
			d.native.removeErr = nil
			_, raw, sig := d.build(t, 3, 5800, func(c *control.DeleteCheckpointCommand) {
				c.InstallationFence, c.TargetGeneration = "95", "95"
				mutate(c)
			})
			receipt := mustDeleteReceipt(t)(d.dispatchCommand(raw, sig))
			if receipt.Outcome != DeleteOutcomeIdentityMismatch || receipt.Applied() {
				t.Fatalf("receipt: %+v", receipt)
			}
			if !d.native.has(d.checkpoint.CheckpointID) {
				t.Fatal("a mismatching retry removed the archive")
			}
			if state, _ := d.commandRow(t, d.command.EffectID); state != "unknown" {
				t.Fatalf("the earlier claim was settled by a mismatching retry: %q", state)
			}
		})
	}
	t.Run("a settled earlier claim answers absent without touching the disk", func(t *testing.T) {
		d := newDeleteCheckpointFixture(t)
		mustDeleteReceipt(t)(d.dispatch())
		_, removes := d.native.counts()
		_, raw, sig := d.build(t, 3, 5700, func(c *control.DeleteCheckpointCommand) { c.InstallationFence, c.TargetGeneration = "95", "95" })
		if receipt := mustDeleteReceipt(t)(d.dispatchCommand(raw, sig)); receipt.Outcome != DeleteOutcomeAbsent {
			t.Fatalf("receipt: %+v", receipt)
		}
		if _, after := d.native.counts(); after != removes {
			t.Fatal("a proven deletion was removed again")
		}
	})
}

// 10. Once the claim has committed, the checkpoint read-back, restore and every consumer that needs a verified row
// refuse the checkpoint, with no consumer changed.
func TestDeleteCheckpointClaimMakesEveryConsumerRefuseTheCheckpoint(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	d.native.removeErr = errors.New("crashed before the unlink")
	if _, err := d.dispatch(); err == nil {
		t.Fatal("a failed removal settled")
	}
	if _, err := d.store.ReadCheckpointReceipt(context.Background(), d.raw0(t), d.sig0(t)); err == nil {
		t.Fatal("the checkpoint receipt is still readable after the claim")
	}
	ran := false
	_, err := d.store.dispatchRestore(context.Background(), d.restoreRaw, d.restoreSig, func(context.Context, data.RestoreRequest) (data.RestoreSnapshot, error) {
		ran = true
		return data.RestoreSnapshot{}, nil
	})
	if err == nil || ran {
		t.Fatalf("a restore of a claimed deletion ran: %v ran=%v", err, ran)
	}
}

// raw0 and sig0 are the signed checkpoint command the fixture started from.
func (d *deleteCheckpointFixtureT) raw0(t *testing.T) []byte { return d.checkpointFixture.raw }
func (d *deleteCheckpointFixtureT) sig0(t *testing.T) []byte { return d.checkpointFixture.sig }

// 11. A deletion and a restore of the same checkpoint share one gate: exactly one runs at a time, and whichever comes
// second sees the other's verdict. Fails if the deletion does not take the gate restore takes.
func TestDeleteCheckpointAndRestoreShareTheDataExecutionGate(t *testing.T) {
	t.Run("a restore in flight holds the deletion off", func(t *testing.T) {
		d := newDeleteCheckpointFixture(t)
		entered, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := d.store.dispatchRestore(context.Background(), d.restoreRaw, d.restoreSig, func(context.Context, data.RestoreRequest) (data.RestoreSnapshot, error) {
				close(entered)
				<-release
				return data.RestoreSnapshot{}, errors.New("stop after the gate was shown to be held")
			})
			done <- err
		}()
		<-entered
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, err := d.store.DispatchDeleteCheckpoint(ctx, d.raw, d.sig, d.native); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the deletion ran beside a restore: %v", err)
		}
		if d.commandRows(t) != 0 {
			t.Fatal("a deletion that never got the gate wrote a row")
		}
		close(release)
		<-done
		// The restore left its command unknown: the checkpoint is claimed by an unsettled restore, so the deletion is
		// refused before any claim of its own.
		if _, err := d.dispatch(); !errors.Is(err, ErrDeleteCheckpointInstallationBusy) {
			t.Fatalf("a checkpoint with an unsettled restore was not refused: %v", err)
		}
		if d.commandRows(t) != 0 {
			t.Fatal("the refusal wrote a row")
		}
	})
	t.Run("a deletion in flight holds the restore off, and the restore is then refused", func(t *testing.T) {
		d := newDeleteCheckpointFixture(t)
		entered, release := make(chan struct{}), make(chan struct{})
		d.native.onRemove = func(string) {
			close(entered)
			<-release
		}
		done := make(chan error, 1)
		go func() {
			_, err := d.dispatch()
			done <- err
		}()
		<-entered
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, err := d.store.dispatchRestore(ctx, d.restoreRaw, d.restoreSig, func(context.Context, data.RestoreRequest) (data.RestoreSnapshot, error) {
			t.Error("a restore ran beside a deletion")
			return data.RestoreSnapshot{}, nil
		}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the restore ran beside a deletion: %v", err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		ran := false
		if _, err := d.store.dispatchRestore(context.Background(), d.restoreRaw, d.restoreSig, func(context.Context, data.RestoreRequest) (data.RestoreSnapshot, error) {
			ran = true
			return data.RestoreSnapshot{}, nil
		}); err == nil || ran {
			t.Fatalf("a restore of a deleted checkpoint ran: %v", err)
		}
	})
}

// The read-back answers: the receipt, pending while the gate is held, orphaned for an unknown effect, absent for no
// row. A read never writes a row.
func TestReadDeleteCheckpointAnswersReceiptPendingOrphanedAndAbsent(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	readSig := d.readSig(t, d.raw)
	asStatus := func(got any, err error) string {
		t.Helper()
		status, ok := got.(DeleteCheckpointStatus)
		if err != nil || !ok || !DeleteCheckpointStatusMatchesCommand(status, d.command) {
			t.Fatalf("not a matching status: %#v %v", got, err)
		}
		return status.State
	}
	if state := asStatus(d.store.ReadDeleteCheckpointOrStatus(context.Background(), d.raw, readSig)); state != "absent" {
		t.Fatalf("a never claimed effect read %q", state)
	}
	if d.commandRows(t) != 0 {
		t.Fatal("a read wrote a row")
	}
	if err := d.store.acquireDataExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := asStatus(d.store.ReadDeleteCheckpointOrStatus(context.Background(), d.raw, readSig)); state != "pending" {
		t.Fatalf("a read with the gate held answered %q", state)
	}
	d.store.releaseDataExecution()
	want := mustDeleteReceipt(t)(d.dispatch())
	got, err := d.store.ReadDeleteCheckpointOrStatus(context.Background(), d.raw, readSig)
	if err != nil || got != any(want) {
		t.Fatalf("read-back of an applied effect: %#v %v", got, err)
	}
	if _, err := d.store.ReadDeleteCheckpointOrStatus(context.Background(), d.raw, d.sig); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
}

// A different command under a used effect id, or a used effect number, is a conflict, never a replay.
func TestDeleteCheckpointRefusesAConflictingCommand(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	mustDeleteReceipt(t)(d.dispatch())
	_, raw, sig := d.build(t, 2, 5100, func(c *control.DeleteCheckpointCommand) { c.Payload.RecoverySetID = fixtureID(8888) })
	if _, err := d.dispatchCommand(raw, sig); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("same effect id, different payload: %v", err)
	}
	_, raw, sig = d.build(t, 2, 5500, func(c *control.DeleteCheckpointCommand) { c.OperationID = d.command.OperationID })
	if _, err := d.dispatchCommand(raw, sig); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("same effect number: %v", err)
	}
}

// The receipt parser is closed: it refuses what the runner would never write.
func TestParseDeleteCheckpointReceiptIsClosed(t *testing.T) {
	d := newDeleteCheckpointFixture(t)
	good := mustDeleteReceipt(t)(d.dispatch())
	for name, mutate := range map[string]func(*DeleteCheckpointReceipt){
		"unknown outcome":        func(r *DeleteCheckpointReceipt) { r.Outcome = "deleted" },
		"refusal that found":     func(r *DeleteCheckpointReceipt) { r.Outcome, r.ArchiveFound = DeleteOutcomeLatest, true },
		"absent that found":      func(r *DeleteCheckpointReceipt) { r.Outcome, r.ArchiveFound = DeleteOutcomeAbsent, true },
		"set differs from claim": func(r *DeleteCheckpointReceipt) { r.RecoverySetID = fixtureID(1234) },
		"settled before claimed": func(r *DeleteCheckpointReceipt) { r.SettledMS, r.ObservedMS = "1", "1" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := good
			mutate(&bad)
			if validDeleteCheckpointReceipt(bad) {
				t.Fatal("a receipt the runner never writes was accepted")
			}
		})
	}
	if !validDeleteCheckpointReceipt(good) {
		t.Fatal("the runner's own receipt was refused")
	}
}

var _ = sql.ErrNoRows
