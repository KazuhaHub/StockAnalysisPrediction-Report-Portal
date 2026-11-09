//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

type discardFixture struct {
	migrateFixture
	discard  control.DiscardMigrationCommand
	dRaw     []byte
	dSig     []byte
	dReadSig []byte
}

func signDiscard(t *testing.T, c control.DiscardMigrationCommand) ([]byte, []byte, []byte) {
	t.Helper()
	digest, err := control.DiscardMigrationLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	c.PayloadSHA256 = digest
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	now := time.Now().UTC().UnixMilli()
	raw, sig, err := control.SignDiscardMigration(c, key, importAuthority(), now)
	if err != nil {
		t.Fatal(err)
	}
	read, err := control.SignDiscardMigrationRead(raw, key, importAuthority(), now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig, read
}

// unknownMigrationFixture leaves the migration unknown, as a crash after the claim would.
func unknownMigrationFixture(t *testing.T) discardFixture {
	t.Helper()
	m := migrateDataJournalFixture(t)
	m.native.runErr = driver.ErrContainerUncertain
	if _, err := m.dispatch(t); err == nil {
		t.Fatal("uncertain run succeeded")
	}
	m.native.runErr = nil
	now := time.Now().UTC().UnixMilli()
	c := control.DiscardMigrationCommand{CommandHeader: m.command.CommandHeader, Payload: control.DiscardMigrationPayload{
		NamespaceID: m.command.Payload.NamespaceID, ResourceID: m.command.Payload.ResourceID,
		MigrateEffectID: m.command.EffectID, MigratePayloadSHA256: m.command.PayloadSHA256,
		TargetReleaseSHA256: m.command.Payload.TargetReleaseSHA256,
	}}
	c.Format, c.EffectNumber, c.Kind, c.EffectID = control.DiscardMigrationFormat, "3", "discard-migration", fixtureID(1900)
	c.IssuedMS, c.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, sig, read := signDiscard(t, c)
	c.PayloadSHA256, _ = control.DiscardMigrationLogicalDigest(c)
	return discardFixture{migrateFixture: m, discard: c, dRaw: raw, dSig: sig, dReadSig: read}
}

func (d discardFixture) dispatch() (DiscardMigrationReceipt, error) {
	return d.f.store.DispatchDiscardMigration(context.Background(), d.dRaw, d.dSig, d.descriptor, d.native, importDataPin)
}

func TestDiscardWithoutNativeIDFindsByName(t *testing.T) {
	d := unknownMigrationFixture(t)
	var native string
	if err := d.f.store.db.QueryRow(`SELECT native_id FROM runner_resources WHERE id=?`, d.command.Payload.ResourceID).Scan(&native); err != nil || native != migrateJobNativeID(d.command.Payload.ResourceID) {
		t.Fatalf("job resource holds a real native id: %q %v", native, err)
	}
	receipt, err := d.dispatch()
	if err != nil {
		t.Fatal(err)
	}
	if d.native.absents != 1 || receipt.State != "discarded" {
		t.Fatalf("discard did not prove absence by identity: absents=%d %+v", d.native.absents, receipt)
	}
	request := d.native.absentIDs[0]
	if request.Identity.ResourceID != d.command.Payload.ResourceID || request.Binding.EffectID != d.command.EffectID ||
		request.Identity.WriterFence != d.command.Payload.WriterFence || request.Step.From != 1 {
		t.Fatalf("absence proof was not bound to the migration's identity: %+v", request)
	}
}

func TestDiscardRevertsMigrateAtomically(t *testing.T) {
	d := unknownMigrationFixture(t)
	// An absence proof that fails changes nothing: the migration stays unknown.
	d.native.absentErr = driver.ErrContainerUncertain
	if _, err := d.dispatch(); err == nil {
		t.Fatal("discard succeeded without an absence proof")
	}
	if rowState(t, d.f.store, d.command.EffectID) != "unknown" {
		t.Fatal("migration settled although the job was not proven absent")
	}
	// A settlement that loses a race rolls every row back together.
	d.native.absentErr = nil
	d.native.absentHook = func() {
		mustJournalExec(t, d.f.store.db, `UPDATE runner_resources SET revision=revision+1 WHERE id=?`, d.command.Payload.ResourceID)
	}
	if _, err := d.dispatch(); err == nil {
		t.Fatal("settlement over a changed resource succeeded")
	}
	if rowState(t, d.f.store, d.command.EffectID) != "unknown" || rowState(t, d.f.store, d.discard.EffectID) != "unknown" {
		t.Fatal("settlement was not atomic")
	}
	d.native.absentHook = nil
	mustJournalExec(t, d.f.store.db, `UPDATE runner_resources SET revision=1 WHERE id=?`, d.command.Payload.ResourceID)
	receipt, err := d.dispatch()
	if err != nil {
		t.Fatal(err)
	}
	var observed, desired string
	if err := d.f.store.db.QueryRow(`SELECT observed_state,desired_state FROM runner_resources WHERE id=?`, d.command.Payload.ResourceID).Scan(&observed, &desired); err != nil || observed != "absent" || desired != "absent" {
		t.Fatalf("job resource after discard: %s %s %v", observed, desired, err)
	}
	if rowState(t, d.f.store, d.command.EffectID) != "reverted" || rowState(t, d.f.store, d.discard.EffectID) != "applied" || receipt.State != "discarded" {
		t.Fatal("discard did not revert the migration with the discard applied")
	}
	// The migrate command now reads back as a discarded, reverted effect.
	got, err := d.f.store.ReadMigrateDataOrStatus(context.Background(), d.raw, migrateRead(t, d.raw))
	migrated, ok := got.(MigrateDataReceipt)
	if err != nil || !ok || migrated.State != "discarded" || migrated.Outcome != "discarded" {
		t.Fatalf("discarded migration read: %#v %v", got, err)
	}
	// And is never run again.
	if again, err := d.f.store.DispatchMigrateData(context.Background(), d.raw, d.sig, d.descriptor, d.native, importDataPin); err != nil || again.State != "discarded" || d.native.runs != 1 {
		t.Fatalf("discarded migration reran: %+v %v runs=%d", again, err, d.native.runs)
	}
}

func TestDiscardReplaySameReceipt(t *testing.T) {
	d := unknownMigrationFixture(t)
	first, err := d.dispatch()
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.dispatch()
	if err != nil || second != first {
		t.Fatalf("replay changed the receipt: %+v %v", second, err)
	}
	if d.native.absents != 1 {
		t.Fatal("replay touched the native side")
	}
	got, err := d.f.store.ReadDiscardMigrationOrStatus(context.Background(), d.dRaw, d.dReadSig)
	if read, ok := got.(DiscardMigrationReceipt); err != nil || !ok || read != first {
		t.Fatalf("read-back differs: %#v %v", got, err)
	}
}

func TestDiscardRefusedForAppliedMigrate(t *testing.T) {
	m := migrateDataJournalFixture(t)
	if receipt, err := m.dispatch(t); err != nil || receipt.State != "migrated" {
		t.Fatalf("migration: %+v %v", receipt, err)
	}
	now := time.Now().UTC().UnixMilli()
	c := control.DiscardMigrationCommand{CommandHeader: m.command.CommandHeader, Payload: control.DiscardMigrationPayload{
		NamespaceID: m.command.Payload.NamespaceID, ResourceID: m.command.Payload.ResourceID,
		MigrateEffectID: m.command.EffectID, MigratePayloadSHA256: m.command.PayloadSHA256, TargetReleaseSHA256: m.command.Payload.TargetReleaseSHA256,
	}}
	c.Format, c.EffectNumber, c.Kind, c.EffectID = control.DiscardMigrationFormat, "3", "discard-migration", fixtureID(1901)
	c.IssuedMS, c.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, sig, _ := signDiscard(t, c)
	if _, err := m.f.store.DispatchDiscardMigration(context.Background(), raw, sig, m.descriptor, m.native, importDataPin); err == nil {
		t.Fatal("an applied migration was discarded")
	}
	if rowState(t, m.f.store, m.command.EffectID) != "applied" || m.native.absents != 0 {
		t.Fatal("refused discard changed state")
	}
	if scalar(t, m.f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='discard-migration'`) != 0 {
		t.Fatal("refused discard wrote a row")
	}
}

func TestDiscardReadStatuses(t *testing.T) {
	d := unknownMigrationFixture(t)
	got, err := d.f.store.ReadDiscardMigrationOrStatus(context.Background(), d.dRaw, d.dReadSig)
	status, ok := got.(MigrateDataStatus)
	if err != nil || !ok || status.State != "absent" || status.Kind != "discard-migration" || !DiscardMigrationStatusMatchesCommand(status, d.discard) {
		t.Fatalf("unclaimed discard read: %#v %v", got, err)
	}
	if err := d.f.store.acquireDataExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ = d.f.store.ReadDiscardMigrationOrStatus(context.Background(), d.dRaw, d.dReadSig)
	if status, ok := got.(MigrateDataStatus); !ok || status.State != "pending" {
		t.Fatalf("held gate must read pending: %#v", got)
	}
	d.f.store.releaseDataExecution()
	_ = errors.Is
}
