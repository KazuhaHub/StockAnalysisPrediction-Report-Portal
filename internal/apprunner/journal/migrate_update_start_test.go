//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func signUpdateStartCommand(t *testing.T, command control.UpdateStartCommand) ([]byte, []byte) {
	t.Helper()
	var err error
	command.PayloadSHA256, err = control.UpdateStartLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, sig, err := control.SignUpdateStart(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

// migratedStartCommand builds the update-start that follows the migration of m: the checkpoint fence S,
// the migration's S+1 as the source fence and S+2 as the target, in the target data format.
func migratedStartCommand(t *testing.T, m migrateFixture) control.UpdateStartCommand {
	t.Helper()
	checkpoint := m.checkpoint
	checkpointRaw, _ := json.Marshal(checkpoint)
	var migrateReceipt string
	if err := m.f.store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, m.command.EffectID).Scan(&migrateReceipt); err != nil {
		t.Fatal(err)
	}
	p := m.command.Payload
	return control.UpdateStartCommand{
		Format: control.UpdateStartFormat, DeploymentID: m.f.command.DeploymentID, ControllerEpoch: m.f.command.ControllerEpoch,
		ControlRevision: "3", InstallationID: m.f.command.InstallationID, OperationID: m.f.command.OperationID,
		TargetGeneration: m.f.command.TargetGeneration, Stage: "start", EffectNumber: "1", EffectID: fixtureID(1910),
		Kind: "update-start", InstallationFence: "2",
		Payload: control.UpdateStartPayload{
			ResourceID: fixtureID(1911), NamespaceID: checkpoint.NamespaceID,
			SourceOperationID: checkpoint.SourceOperationID, SourceStartEffectID: checkpoint.SourceStartEffectID,
			SourceStartReceiptSHA256: checkpoint.SourceReceiptSHA256, SourceDataEffectID: checkpoint.SourceDataEffectID,
			SourceDataReceiptSHA256:     checkpoint.SourceDataReceiptSHA256,
			SourceCheckpointOperationID: checkpoint.OperationID, SourceCheckpointEffectID: checkpoint.EffectID,
			SourceCheckpointReceiptSHA256: candidateHash(checkpointRaw), CheckpointID: checkpoint.CheckpointID,
			CheckpointManifestSHA256: checkpoint.ManifestSHA256, CheckpointArchiveBytes: checkpoint.ArchiveBytes,
			SourceReleaseSHA256: checkpoint.ReleaseSHA256, TargetReleaseSHA256: p.TargetReleaseSHA256,
			SourceGeneration: "1", TargetPolicyRevision: "2", ProjectID: checkpoint.ProjectID,
			NamespaceInode: checkpoint.NamespaceInode, Device: checkpoint.Device, QuotaBytes: m.f.data.QuotaBytes,
			QuotaInodes: m.f.data.QuotaInodes, RootFilesystemUUID: m.f.source.Claim.Preflight.RootFilesystemUUID,
			RootInode: m.f.source.Claim.Preflight.RootInode, DataFormat: p.TargetSchema,
			CheckpointWriterFence: p.CheckpointWriterFence, SourceWriterFence: p.WriterFence, TargetWriterFence: p.StartWriterFence,
			MigrateEffectID: m.command.EffectID, MigrateReceiptSHA256: candidateHash([]byte(migrateReceipt)),
			Platform: m.f.source.Claim.Preflight.Platform, SandboxProfile: control.CandidateSandboxProfile,
		},
	}
}

func TestUpdateStartRequiresAppliedMigration(t *testing.T) {
	m := migrateDataJournalFixture(t)
	if r, err := m.dispatch(t); err != nil || r.State != "migrated" {
		t.Fatalf("migration: %+v %v", r, err)
	}
	command := migratedStartCommand(t, m)
	raw, sig := signUpdateStartCommand(t, command)
	reservation, err := m.f.store.ReserveUpdateStart(context.Background(), raw, sig)
	if err != nil || reservation.SourceWriterFence != mustDataInt(m.command.Payload.WriterFence) || reservation.TargetWriterFence != mustDataInt(m.command.Payload.StartWriterFence) {
		t.Fatalf("bound update-start refused: %+v %v", reservation, err)
	}
	for name, mutate := range map[string]func(*control.UpdateStartCommand){
		"migration ignored": func(c *control.UpdateStartCommand) {
			c.Payload.MigrateEffectID, c.Payload.MigrateReceiptSHA256 = "", ""
			c.Payload.CheckpointWriterFence = ""
			c.Payload.DataFormat = m.checkpoint.DataFormat
			c.Payload.SourceWriterFence, c.Payload.TargetWriterFence = m.checkpoint.WriterFence, strconv.Itoa(int(mustDataInt(m.checkpoint.WriterFence))+1)
		},
		"other receipt":   func(c *control.UpdateStartCommand) { c.Payload.MigrateReceiptSHA256 = candidateHash([]byte("other")) },
		"other effect":    func(c *control.UpdateStartCommand) { c.Payload.MigrateEffectID = fixtureID(1999) },
		"wrong format":    func(c *control.UpdateStartCommand) { c.Payload.DataFormat = "7" },
		"other target":    func(c *control.UpdateStartCommand) { c.Payload.TargetReleaseSHA256 = candidateHash([]byte("t")) },
		"other namespace": func(c *control.UpdateStartCommand) { c.Payload.NamespaceID = fixtureID(1998) },
	} {
		t.Run(name, func(t *testing.T) {
			m := migrateDataJournalFixture(t)
			if _, err := m.dispatch(t); err != nil {
				t.Fatal(err)
			}
			c := migratedStartCommand(t, m)
			mutate(&c)
			raw, sig := signUpdateStartCommand(t, c)
			if _, err := m.f.store.ReserveUpdateStart(context.Background(), raw, sig); err == nil {
				t.Fatal("update-start admitted without its applied migration")
			}
		})
	}
	// A reverted known failure is not an applied migration.
	failed := migrateDataJournalFixture(t)
	failed.native.exit = 1
	if _, err := failed.dispatch(t); err != nil {
		t.Fatal(err)
	}
	c := migratedStartCommand(t, failed)
	raw, sig = signUpdateStartCommand(t, c)
	if _, err := failed.f.store.ReserveUpdateStart(context.Background(), raw, sig); err == nil {
		t.Fatal("update-start admitted over a failed migration")
	}
}

func TestUpdateStartRefusedAfterDiscardedMigration(t *testing.T) {
	d := unknownMigrationFixture(t)
	if _, err := d.dispatch(); err != nil {
		t.Fatal(err)
	}
	c := migratedStartCommand(t, d.migrateFixture)
	raw, sig := signUpdateStartCommand(t, c)
	if _, err := d.f.store.ReserveUpdateStart(context.Background(), raw, sig); err == nil {
		t.Fatal("update-start admitted over a discarded migration")
	}
}

func TestUpdateStartRefusesMigrationFenceNotBetween(t *testing.T) {
	for name, mutate := range map[string]func(*control.UpdateStartCommand, migrateFixture){
		"source fence is not the migration's": func(c *control.UpdateStartCommand, m migrateFixture) {
			c.Payload.SourceWriterFence = m.checkpoint.WriterFence
			c.Payload.CheckpointWriterFence = strconv.Itoa(int(mustDataInt(m.checkpoint.WriterFence)) - 1 + 1)
		},
		"target fence is not the reserved one": func(c *control.UpdateStartCommand, m migrateFixture) {
			c.Payload.TargetWriterFence = strconv.Itoa(int(mustDataInt(m.command.Payload.StartWriterFence)) + 1)
		},
		"target equals the migration fence": func(c *control.UpdateStartCommand, m migrateFixture) {
			c.Payload.TargetWriterFence = m.command.Payload.WriterFence
		},
		"checkpoint fence differs": func(c *control.UpdateStartCommand, m migrateFixture) {
			c.Payload.CheckpointWriterFence = "9"
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := migrateDataJournalFixture(t)
			if _, err := m.dispatch(t); err != nil {
				t.Fatal(err)
			}
			c := migratedStartCommand(t, m)
			mutate(&c, m)
			digest, err := control.UpdateStartLogicalDigest(c)
			if err != nil {
				return // refused by the control contract before the journal sees it
			}
			_ = digest
			raw, sig := signUpdateStartCommand(t, c)
			if _, err := m.f.store.ReserveUpdateStart(context.Background(), raw, sig); err == nil {
				t.Fatal("fences outside the migration's window admitted")
			}
		})
	}
}

func TestUpdateStartTaintedNamespaceRefused(t *testing.T) {
	// A migration left unknown in the namespace blocks every start, bound or not.
	d := unknownMigrationFixture(t)
	c := migratedStartCommand(t, d.migrateFixture)
	raw, sig := signUpdateStartCommand(t, c)
	if _, err := d.f.store.ReserveUpdateStart(context.Background(), raw, sig); err == nil {
		t.Fatal("update-start admitted in a tainted namespace")
	}
	// An applied migration whose job resource is somehow not absent also taints it.
	m := migrateDataJournalFixture(t)
	if _, err := m.dispatch(t); err != nil {
		t.Fatal(err)
	}
	mustJournalExec(t, m.f.store.db, `UPDATE runner_resources SET desired_state='present',observed_state='stopped' WHERE id=?`, m.command.Payload.ResourceID)
	c = migratedStartCommand(t, m)
	raw, sig = signUpdateStartCommand(t, c)
	if _, err := m.f.store.ReserveUpdateStart(context.Background(), raw, sig); err == nil {
		t.Fatal("update-start admitted beside a surviving migration job")
	}
}

func TestRestoreStartAfterSettledMigration(t *testing.T) {
	// Rollback start after a discarded migration: the checkpoint was restored, the migration is
	// reverted and its job proven absent, so the restore-start reuses fence S+1 of the migration.
	build := func(t *testing.T, d discardFixture) ([]byte, []byte) {
		c := migratedStartCommand(t, d.migrateFixture)
		c.Kind = "restore-start"
		c.OperationID = fixtureID(1920)
		c.EffectID, c.Payload.ResourceID = fixtureID(1921), fixtureID(1922)
		c.Payload.TargetReleaseSHA256 = c.Payload.SourceReleaseSHA256
		c.Payload.MigrateEffectID, c.Payload.MigrateReceiptSHA256 = "", ""
		c.Payload.CheckpointWriterFence = ""
		c.Payload.DataFormat = d.checkpoint.DataFormat
		c.Payload.SourceWriterFence = d.checkpoint.WriterFence
		c.Payload.TargetWriterFence = d.command.Payload.WriterFence
		return signUpdateStartCommand(t, c)
	}
	tainted := unknownMigrationFixture(t)
	raw, sig := build(t, tainted)
	if _, err := tainted.f.store.ReserveUpdateStart(context.Background(), raw, sig); err == nil {
		t.Fatal("rollback start admitted over an unsettled migration")
	}
	settled := unknownMigrationFixture(t)
	if _, err := settled.dispatch(); err != nil {
		t.Fatal(err)
	}
	raw, sig = build(t, settled)
	reservation, err := settled.f.store.ReserveUpdateStart(context.Background(), raw, sig)
	if err != nil || reservation.TargetWriterFence != mustDataInt(settled.command.Payload.WriterFence) {
		t.Fatalf("rollback start refused after settlement: %+v %v", reservation, err)
	}
}

// A migration whose namespace was restored afterwards is superseded: the data it produced is gone, so no start may be
// bound to its receipt.
func TestUpdateStartRefusesMigrationSupersededByRestore(t *testing.T) {
	m := migrateDataJournalFixture(t)
	if r, err := m.dispatch(t); err != nil || r.State != "migrated" {
		t.Fatalf("migration: %+v %v", r, err)
	}
	var applied int64
	if err := m.f.store.db.QueryRow(`SELECT updated_ms FROM runner_commands WHERE effect_id=?`, m.command.EffectID).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	// Admitted after the migration in the very same millisecond: the clock cannot tell, the order does.
	m.insertRestore(t, m.command.OperationID, applied)
	raw, sig := signUpdateStartCommand(t, migratedStartCommand(t, m))
	if _, err := m.f.store.ReserveUpdateStart(context.Background(), raw, sig); err == nil {
		t.Fatal("a start was bound to a migration whose namespace was restored after it")
	}
}

// A restore admitted before the migration does not supersede it, whatever the clock says.
func TestUpdateStartIgnoresARestoreAdmittedBeforeTheMigration(t *testing.T) {
	m := migrateDataJournalFixture(t)
	m.insertRestore(t, m.command.OperationID, time.Now().UTC().UnixMilli()+60000)
	if r, err := m.dispatch(t); err != nil || r.State != "migrated" {
		t.Fatalf("migration: %+v %v", r, err)
	}
	raw, sig := signUpdateStartCommand(t, migratedStartCommand(t, m))
	if _, err := m.f.store.ReserveUpdateStart(context.Background(), raw, sig); err != nil {
		t.Fatalf("an earlier restore with a later clock reading superseded the migration: %v", err)
	}
}
