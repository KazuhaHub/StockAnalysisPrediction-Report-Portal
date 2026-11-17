//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// retryFixture is a retry of a failed update: the migrating operation (the fixture's) recovers `failed`, whose
// source removal it carries and whose migration was reverted and restored. Its own checkpoint is the fresh
// one taken after that restore.
type retryFixture struct {
	migrateFixture
	failed string
}

func retryOperation(n int) string { return fixtureID(1900 + n) }

// retryOrdinal is the n a retryOperation was made from.
func retryOrdinal(operation string) int {
	n, _ := strconv.Atoi(operation[24:])
	return n - 1900
}

// insertSettledMigration records an update whose migration the runner reverted and whose data it restored.
func (m migrateFixture) insertSettledMigration(t *testing.T, operation, recovers, state string, at int64) {
	t.Helper()
	p := m.command.Payload
	claim := migrateClaimMarker{
		Format: migrateClaimFormat, ClaimID: fixtureID(2000), EffectID: fixtureID(3000 + retryOrdinal(operation)), ResourceID: fixtureID(2200 + retryOrdinal(operation)),
		OwnershipSHA256: strings.Repeat("a", 64), AdmittedFence: "2", ClaimedMS: strconv.FormatInt(at, 10), NamespaceID: p.NamespaceID,
		CheckpointID: p.CheckpointID, CheckpointReceiptSHA256: strings.Repeat("b", 64), RemoveCodeReceiptSHA256: p.RemoveCodeReceiptSHA256,
		SourceReleaseSHA256: p.SourceReleaseSHA256, TargetReleaseSHA256: p.TargetReleaseSHA256, SourceSchema: p.SourceSchema, TargetSchema: p.TargetSchema,
		CheckpointWriterFence: p.CheckpointWriterFence, WriterFence: p.WriterFence, StartWriterFence: p.StartWriterFence, ProjectID: p.ProjectID,
		NamespaceInode: p.NamespaceInode, Device: p.Device, QuotaBytes: p.QuotaBytes, QuotaInodes: p.QuotaInodes, RecoversChain: recovers,
	}
	rowState, outcome := "failed", "exit_nonzero"
	if state == "discarded" {
		outcome = "discarded"
	}
	if state == "migrated" {
		rowState, outcome = "migrated", ""
	}
	receipt := MigrateDataReceipt{
		Format: migrateReceiptFormat, Claim: claim, DeploymentID: m.command.DeploymentID, ControllerEpoch: m.command.ControllerEpoch,
		InstallationID: m.command.InstallationID, OperationID: operation, TargetGeneration: m.command.TargetGeneration, Stage: "migrate",
		EffectID: claim.EffectID, PayloadSHA256: strings.Repeat("c", 64), ExitCode: "1", MarkerSchema: "0", UsedBytes: "0", UsedInodes: "0",
		ObservedMS: strconv.FormatInt(at, 10), SettledMS: strconv.FormatInt(at, 10),
	}
	if state == "discarded" {
		rowState = "discarded"
	}
	receipt.State, receipt.Outcome = rowState, outcome
	if state == "migrated" {
		receipt.ExitCode, receipt.MarkerSchema = "0", claim.TargetSchema
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	commandState := "reverted"
	switch state {
	case "unknown":
		commandState = state
	case "migrated":
		commandState = "applied"
	}
	mustJournalExec(t, m.f.store.db, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
		claim.EffectID, operation, m.command.InstallationID, "migrate", 2, "migrate-data", receipt.PayloadSHA256, 2, m.command.ControllerEpoch, 2, commandState, string(raw), at, at)
}

// insertRestore records the applied restore that settled the operation's migration.
func (m migrateFixture) insertRestore(t *testing.T, operation string, at int64) {
	t.Helper()
	p := m.command.Payload
	receipt := RestoreReceipt{
		Format: restoreReceiptFormat, DeploymentID: m.command.DeploymentID, ControllerEpoch: m.command.ControllerEpoch, InstallationID: m.command.InstallationID,
		OperationID: operation, TargetGeneration: m.command.TargetGeneration, Stage: "restore_data", EffectID: fixtureID(3500 + retryOrdinal(operation)),
		PayloadSHA256: strings.Repeat("d", 64), RecoverySetID: fixtureID(3600), CheckpointID: p.CheckpointID, NamespaceID: p.NamespaceID, ReleaseSHA256: p.SourceReleaseSHA256,
		ManifestSHA256: strings.Repeat("a", 64), ArchiveBytes: "4096", ProjectID: p.ProjectID, NamespaceInode: p.NamespaceInode, Device: p.Device,
		DataFormat: p.SourceSchema, WriterFence: p.CheckpointWriterFence, SourceCheckpointOperationID: operation, SourceCheckpointEffectID: p.CheckpointEffectID,
		SourceCheckpointReceiptSHA: strings.Repeat("e", 64), Entries: "3", UsedBytes: "4096", State: "restored",
		ObservedMS: strconv.FormatInt(at, 10), SettledMS: strconv.FormatInt(at, 10),
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	mustJournalExec(t, m.f.store.db, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,?,?,'applied',?,NULL,?,?)`,
		receipt.EffectID, operation, m.command.InstallationID, "restore_data", 1, "restore", receipt.PayloadSHA256, 2, m.command.ControllerEpoch, 2, string(raw), at, at)
}

// retryJournalFixture hands the removal evidence of the fixture to the failed update, settles that update's
// migration and restore, and re-signs the migration with the link a retry carries.
func retryJournalFixture(t *testing.T) retryFixture {
	t.Helper()
	m := migrateDataJournalFixture(t)
	failed := retryOperation(0)
	now := time.Now().UTC().UnixMilli()
	var raw string
	if err := m.f.store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, m.command.Payload.RemoveCodeEffectID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	removal, err := ParseManagedRemovalReceipt([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	removal.OperationID = failed
	rewritten, _ := json.Marshal(removal)
	mustJournalExec(t, m.f.store.db, `UPDATE runner_commands SET operation_id=?,receipt_json=? WHERE effect_id=?`, failed, string(rewritten), removal.EffectID)
	m.command.Payload.RemoveCodeReceiptSHA256 = candidateHash(rewritten)
	m.command.Payload.RecoversChain = failed
	m.insertSettledMigration(t, failed, "", "discarded", now-20)
	m.insertRestore(t, failed, now-10)
	// The fixture's own checkpoint is the fresh one, taken after the restore.
	mustJournalExec(t, m.f.store.db, `UPDATE runner_commands SET created_ms=?,updated_ms=? WHERE effect_id=?`, now-5, now-5, m.command.Payload.CheckpointEffectID)
	m.raw, m.sig = signMigrate(t, m.command)
	m.command.PayloadSHA256, _ = control.MigrateDataLogicalDigest(m.command)
	return retryFixture{m, failed}
}

func (r retryFixture) admit(t *testing.T, mutate func(*control.MigrateDataCommand)) error {
	t.Helper()
	raw, sig := r.resign(t, mutate)
	_, _, err := r.f.store.beginMigrateData(context.Background(), raw, sig)
	return err
}

func TestMigrateDataRetryAdmitsRecoveredOperationRemoval(t *testing.T) {
	r := retryJournalFixture(t)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err != nil {
		t.Fatalf("a retry carrying the removal of the update it recovers was refused: %v", err)
	}
	if rowState(t, r.f.store, r.command.EffectID) != "unknown" {
		t.Fatal("the retry's migration was not claimed")
	}
	var receipt string
	if err := r.f.store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, r.command.EffectID).Scan(&receipt); err != nil || !strings.Contains(receipt, `"recovers_chain":"`+r.failed+`"`) {
		t.Fatalf("the claim does not record the update it recovers: %v %s", err, receipt)
	}
}

func TestMigrateDataRetryRefusesRemovalOfAnotherOperation(t *testing.T) {
	r := retryJournalFixture(t)
	// A second settled update exists, but the removal evidence is not its own.
	other := retryOperation(1)
	now := time.Now().UTC().UnixMilli()
	r.insertSettledMigration(t, other, "", "discarded", now-20)
	r.insertRestore(t, other, now-10)
	if err := r.admit(t, func(c *control.MigrateDataCommand) { c.Payload.RecoversChain = other }); err == nil {
		t.Fatal("a retry was admitted on the removal of an update it does not recover")
	}
	// Without a recovery link, another operation's removal is never accepted either.
	if err := r.admit(t, func(c *control.MigrateDataCommand) { c.Payload.RecoversChain = "" }); err == nil {
		t.Fatal("an ordinary update was admitted on another operation's removal")
	}
}

func TestMigrateDataRetryRefusesUnsettledRecoveredMigration(t *testing.T) {
	for name, mutate := range map[string]func(r retryFixture){
		"migration still unknown": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET state='unknown' WHERE operation_id=? AND kind='migrate-data'`, r.failed)
		},
		"checkpoint never restored": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `DELETE FROM runner_commands WHERE operation_id=? AND kind='restore'`, r.failed)
		},
		"restore unknown": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET state='unknown' WHERE operation_id=? AND kind='restore'`, r.failed)
		},
		"restore of another namespace": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET receipt_json=replace(receipt_json,?,?) WHERE operation_id=? AND kind='restore'`,
				r.command.Payload.NamespaceID, fixtureID(3999), r.failed)
		},
		"checkpoint predates the restore": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET created_ms=1 WHERE effect_id=?`, r.command.Payload.CheckpointEffectID)
		},
		"recovered update is the migrating one": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET operation_id=? WHERE operation_id=? AND kind IN ('migrate-data','restore')`, r.command.OperationID, r.failed)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := retryJournalFixture(t)
			mutate(r)
			if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err == nil {
				t.Fatal("a retry was admitted over an unsettled migration")
			}
			if scalar(t, r.f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id = '`+r.command.EffectID+`'`) != 0 {
				t.Fatal("a refused retry wrote a row")
			}
		})
	}
}

func TestMigrateDataRetryRefusesLiveMigration(t *testing.T) {
	r := retryJournalFixture(t)
	now := time.Now().UTC().UnixMilli()
	// Another operation's migration is still unsettled in the installation.
	r.insertSettledMigration(t, retryOperation(2), "", "unknown", now-3)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err == nil {
		t.Fatal("a retry was admitted beside an unsettled migration")
	}
}

// retryOfRetryFixture adds a second failed update between the first and the migrating one: it recovered the first,
// carried the first's removal, and was settled in turn.
func retryOfRetryFixture(t *testing.T) retryFixture {
	t.Helper()
	r := retryJournalFixture(t)
	now := time.Now().UTC().UnixMilli()
	middle := retryOperation(3)
	// The middle update's restore comes after the first's, and the migrating one's checkpoint after both.
	r.insertSettledMigration(t, middle, r.failed, "discarded", now-4)
	r.insertRestore(t, middle, now-3)
	mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET created_ms=?,updated_ms=? WHERE effect_id=?`, now-2, now-2, r.command.Payload.CheckpointEffectID)
	r.command.Payload.RecoversChain = r.failed + "," + middle
	r.raw, r.sig = signMigrate(t, r.command)
	r.command.PayloadSHA256, _ = control.MigrateDataLogicalDigest(r.command)
	r.failed = middle
	return r
}

func TestMigrateDataRetryOfRetryAdmitted(t *testing.T) {
	r := retryOfRetryFixture(t)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err != nil {
		t.Fatalf("a retry of a retry was refused: %v", err)
	}
}

func TestMigrateDataRetryOfRetryRefusesBrokenChain(t *testing.T) {
	for name, mutate := range map[string]func(r retryFixture){
		"the middle update does not name the first": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET receipt_json=replace(receipt_json,?,'') WHERE operation_id=? AND kind='migrate-data'`,
				`,"recovers_chain":"`+retryOperation(0)+`"`, r.failed)
		},
		"the middle update carried other removal evidence": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET receipt_json=replace(receipt_json,?,?) WHERE operation_id=? AND kind='migrate-data'`,
				r.command.Payload.RemoveCodeReceiptSHA256, strings.Repeat("9", 64), r.failed)
		},
		"the middle update's migration was not settled": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET state='unknown' WHERE operation_id=? AND kind='migrate-data'`, r.failed)
		},
		"the middle update's data was not restored": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `DELETE FROM runner_commands WHERE operation_id=? AND kind='restore'`, r.failed)
		},
		"the first update's data was not restored": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `DELETE FROM runner_commands WHERE operation_id=? AND kind='restore'`, retryOperation(0))
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := retryOfRetryFixture(t)
			mutate(r)
			if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err == nil {
				t.Fatal("a retry of a retry was admitted over a broken chain")
			}
		})
	}
}

// C5: the migration was applied, readiness failed, and settlement removed the target and restored the checkpoint, so
// the runner's migrate-data stays applied. A retry may follow it only when the restore is newer than the migration
// and nothing of the failed update's target is left in the namespace.
func appliedRetryFixture(t *testing.T) retryFixture {
	t.Helper()
	r := retryJournalFixture(t)
	mustJournalExec(t, r.f.store.db, `DELETE FROM runner_commands WHERE operation_id=? AND kind IN ('migrate-data','restore')`, r.failed)
	now := time.Now().UTC().UnixMilli()
	r.insertSettledMigration(t, r.failed, "", "migrated", now-20)
	r.insertRestore(t, r.failed, now-10)
	mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET created_ms=?,updated_ms=? WHERE effect_id=?`, now-5, now-5, r.command.Payload.CheckpointEffectID)
	return r
}

func TestMigrateDataRetryAfterAppliedMigrationNeedsNewerRestore(t *testing.T) {
	r := appliedRetryFixture(t)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err != nil {
		t.Fatalf("a retry after an applied migration that was restored away was refused: %v", err)
	}
	for name, mutate := range map[string]func(r retryFixture){
		"no restore": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `DELETE FROM runner_commands WHERE operation_id=? AND kind='restore'`, r.failed)
		},
		"restore admitted before the migration": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET rowid=(SELECT MAX(rowid)+1 FROM runner_commands) WHERE operation_id=? AND kind='migrate-data'`, r.failed)
		},
		"the failed update's target is still there": func(r retryFixture) {
			now := time.Now().UTC().UnixMilli()
			mustJournalExec(t, r.f.store.db, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'container',?,?,2,?,2,3,'present','stopped',?,2,?,?,?)`,
				fixtureID(1990), r.command.InstallationID, r.command.Payload.NamespaceID, strings.Repeat("9", 64), r.command.Payload.TargetReleaseSHA256,
				r.command.ControllerEpoch, strings.Repeat("8", 64), now, now, now)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := appliedRetryFixture(t)
			mutate(r)
			if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err == nil {
				t.Fatal("a retry was admitted over an applied migration that was not restored away")
			}
		})
	}
}

// C4/C2: settlement proved the runner never claimed the failed update's migration, so there is no migrate-data row and
// no restore to follow. A retry may still recover it, and admitting it must close the identity: a late command of the
// original update can never be claimed afterwards.
func neverClaimedRetryFixture(t *testing.T) retryFixture {
	t.Helper()
	r := retryJournalFixture(t)
	mustJournalExec(t, r.f.store.db, `DELETE FROM runner_commands WHERE operation_id=? AND kind IN ('migrate-data','restore')`, r.failed)
	return r
}

func TestMigrateDataRetryAfterNeverClaimedMigrationAdmitted(t *testing.T) {
	r := neverClaimedRetryFixture(t)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err != nil {
		t.Fatalf("a retry of a migration the runner never claimed was refused: %v", err)
	}
	if scalar(t, r.f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE operation_id='`+r.failed+`' AND kind='migrate-data' AND state='reverted'`) != 1 {
		t.Fatal("admitting the retry left no tombstone for the recovered update's migration")
	}
}

func TestMigrateDataRetryOfNeverClaimedMigrationRefusals(t *testing.T) {
	for name, mutate := range map[string]func(r retryFixture){
		"no applied source removal": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET state='unknown' WHERE effect_id=?`, r.command.Payload.RemoveCodeEffectID)
		},
		"another migration is still unsettled": func(r retryFixture) {
			r.insertSettledMigration(t, retryOperation(2), "", "unknown", time.Now().UTC().UnixMilli()-3)
		},
		"a container of the namespace is live": func(r retryFixture) {
			now := time.Now().UTC().UnixMilli()
			mustJournalExec(t, r.f.store.db, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'container',?,?,2,?,2,1,'present','running',?,2,?,?,?)`,
				fixtureID(1995), r.command.InstallationID, r.command.Payload.NamespaceID, strings.Repeat("9", 64), r.command.Payload.TargetReleaseSHA256,
				r.command.ControllerEpoch, strings.Repeat("8", 64), now, now, now)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := neverClaimedRetryFixture(t)
			mutate(r)
			if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err == nil {
				t.Fatal("a retry was admitted without the evidence a never-claimed link needs")
			}
		})
	}
}

// lateOriginalCommand is the migration the failed update would have sent, arriving after the retry: the failed
// update's own checkpoint and removal, its own effect and job, and no recovery link.
func lateOriginalCommand(t *testing.T, r retryFixture) ([]byte, []byte) {
	t.Helper()
	// The failed update's checkpoint is the one the fixture's operation took; hand it over.
	mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET operation_id=? WHERE effect_id=?`, r.failed, r.command.Payload.CheckpointEffectID)
	mustJournalExec(t, r.f.store.db, `UPDATE runner_checkpoints SET operation_id=? WHERE id=?`, r.failed, r.command.Payload.CheckpointID)
	var checkpointRaw string
	if err := r.f.store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, r.command.Payload.CheckpointEffectID).Scan(&checkpointRaw); err != nil {
		t.Fatal(err)
	}
	var checkpoint CheckpointReceipt
	if err := json.Unmarshal([]byte(checkpointRaw), &checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint.OperationID = r.failed
	rewritten, _ := json.Marshal(checkpoint)
	mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET receipt_json=? WHERE effect_id=?`, string(rewritten), r.command.Payload.CheckpointEffectID)
	return r.resign(t, func(c *control.MigrateDataCommand) {
		c.OperationID, c.EffectID = r.failed, fixtureID(2300)
		c.Payload.ResourceID, c.Payload.RecoversChain = fixtureID(2301), ""
		c.Payload.CheckpointReceiptSHA256 = candidateHash(rewritten)
	})
}

func TestMigrateDataLateOriginalCommandRefusedAfterRetry(t *testing.T) {
	// Without a retry the original command is admissible, so the refusal below is the tombstone's doing.
	alone := neverClaimedRetryFixture(t)
	raw, sig := lateOriginalCommand(t, alone)
	if _, _, err := alone.f.store.beginMigrateData(context.Background(), raw, sig); err != nil {
		t.Fatalf("the original command is not admissible on its own, so the test proves nothing: %v", err)
	}

	r := neverClaimedRetryFixture(t)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err != nil {
		t.Fatal(err)
	}
	settleUnknownByHand(t, r.migrateFixture)
	raw, sig = lateOriginalCommand(t, r)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), raw, sig); err == nil {
		t.Fatal("the original migration was admitted after the retry that recovers it")
	}
}

func TestMigrateDataRetryOfRetryAfterNeverClaimedMigrationAdmitted(t *testing.T) {
	r := neverClaimedRetryFixture(t)
	// The second retry names the whole chain: the root, whose removal it carries, and the first retry. The runner
	// never saw either claim a migration, so both are closed.
	middle := retryOperation(7)
	r.command.Payload.RecoversChain = r.failed + "," + middle
	r.raw, r.sig = signMigrate(t, r.command)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err != nil {
		t.Fatalf("a retry of a never-claimed retry was refused: %v", err)
	}
	for _, operation := range []string{r.failed, middle} {
		if scalar(t, r.f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE operation_id='`+operation+`' AND kind='migrate-data' AND state='reverted'`) != 1 {
			t.Fatalf("no tombstone for %s", operation)
		}
	}
}

// The chain is anchored: its root must be the update that owns the removal the retry carries, however many links
// the runner has no record of.
func TestMigrateDataRetryRootMustOwnTheRemoval(t *testing.T) {
	unknown := retryOperation(9)
	for name, chain := range map[string]func(r retryFixture) string{
		"an identifier the runner has never seen in place of the root": func(r retryFixture) string { return unknown },
		"an unseen root before the real one":                           func(r retryFixture) string { return unknown + "," + r.failed },
		"another settled update as the root": func(r retryFixture) string {
			now := time.Now().UTC().UnixMilli()
			r.insertSettledMigration(t, retryOperation(1), "", "discarded", now-20)
			r.insertRestore(t, retryOperation(1), now-10)
			return retryOperation(1) + "," + r.failed
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := neverClaimedRetryFixture(t)
			if err := r.admit(t, func(c *control.MigrateDataCommand) { c.Payload.RecoversChain = chain(r) }); err == nil {
				t.Fatal("a retry was admitted over a chain whose root does not own the removal")
			}
			if scalar(t, r.f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='migrate-data' AND receipt_json LIKE '%tombstone%'`) != 0 {
				t.Fatal("a refused retry wrote a tombstone")
			}
		})
	}
}

// F (claimed, reverted) -> R1 (never claimed) -> the migrating R2: the missing link does not end the walk, so the
// root is still verified.
func TestMigrateDataChainVerifiesTheRootBehindAnUnclaimedLink(t *testing.T) {
	build := func(t *testing.T) retryFixture {
		r := retryJournalFixture(t)
		middle := retryOperation(4)
		r.command.Payload.RecoversChain = r.failed + "," + middle
		r.raw, r.sig = signMigrate(t, r.command)
		return r
	}
	r := build(t)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err != nil {
		t.Fatalf("a claimed root behind a never-claimed retry was refused: %v", err)
	}
	if scalar(t, r.f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE operation_id='`+retryOperation(4)+`' AND kind='migrate-data' AND state='reverted'`) != 1 {
		t.Fatal("the never-claimed link was not closed")
	}
	for name, mutate := range map[string]func(r retryFixture){
		"the root's migration is unsettled": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET state='unknown' WHERE operation_id=? AND kind='migrate-data'`, r.failed)
		},
		"the root's data was not restored": func(r retryFixture) {
			mustJournalExec(t, r.f.store.db, `DELETE FROM runner_commands WHERE operation_id=? AND kind='restore'`, r.failed)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := build(t)
			mutate(r)
			if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err == nil {
				t.Fatal("the walk stopped at the never-claimed link")
			}
			if scalar(t, r.f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE operation_id='`+retryOperation(4)+`' AND kind='migrate-data'`) != 0 {
				t.Fatal("a refused retry wrote a tombstone")
			}
		})
	}
}

// Stopped is not absent: whatever the links are, a container left in the namespace refuses the retry.
func TestMigrateDataRetryRefusesAStoppedLeftoverContainer(t *testing.T) {
	leftover := func(r retryFixture) {
		now := time.Now().UTC().UnixMilli()
		mustJournalExec(t, r.f.store.db, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'container',?,?,2,?,2,3,'present','stopped',?,2,?,?,?)`,
			fixtureID(1996), r.command.InstallationID, r.command.Payload.NamespaceID, strings.Repeat("9", 64), r.command.Payload.TargetReleaseSHA256,
			r.command.ControllerEpoch, strings.Repeat("8", 64), now, now, now)
	}
	for name, build := range map[string]func(t *testing.T) retryFixture{
		"a reverted link":     retryJournalFixture,
		"an applied link":     appliedRetryFixture,
		"a never-claimed one": neverClaimedRetryFixture,
		"a chain":             retryOfRetryFixture,
	} {
		t.Run(name, func(t *testing.T) {
			r := build(t)
			leftover(r)
			if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err == nil {
				t.Fatal("a retry was admitted beside a stopped container")
			}
		})
	}
}

// Supersession follows the order the runner admitted the commands in. Two commands admitted in the same millisecond
// are ordered all the same, and a later clock reading on an earlier command orders nothing.
func TestMigrateDataSupersessionOrdersBySequenceNotClock(t *testing.T) {
	same := func(r retryFixture) {
		mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET updated_ms=(SELECT updated_ms FROM runner_commands WHERE operation_id=? AND kind='migrate-data') WHERE operation_id=? AND kind='restore'`, r.failed, r.failed)
	}
	r := appliedRetryFixture(t)
	same(r)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err != nil {
		t.Fatalf("a restore admitted after the migration in the same millisecond did not supersede it: %v", err)
	}
	r = appliedRetryFixture(t)
	same(r)
	mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET rowid=(SELECT MAX(rowid)+1 FROM runner_commands) WHERE operation_id=? AND kind='migrate-data'`, r.failed)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err == nil {
		t.Fatal("a restore admitted before the migration in the same millisecond superseded it")
	}
	r = appliedRetryFixture(t)
	mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET rowid=(SELECT MAX(rowid)+1 FROM runner_commands) WHERE operation_id=? AND kind='migrate-data'`, r.failed)
	if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err == nil {
		t.Fatal("a later clock reading ordered a restore before the migration it came first of")
	}
}

// insertTombstone records that an update's migration was closed by another retry.
func (m migrateFixture) insertTombstone(t *testing.T, operation, recoveredBy, chain string) {
	t.Helper()
	p := m.command.Payload
	raw, err := json.Marshal(migrateTombstone{
		Format: migrateTombstoneFormat, OperationID: operation, InstallationID: m.command.InstallationID, NamespaceID: p.NamespaceID,
		RemoveCodeEffectID: p.RemoveCodeEffectID, RemoveCodeReceiptSHA256: p.RemoveCodeReceiptSHA256, RecoversChain: chain, RecoveredBy: recoveredBy,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	mustJournalExec(t, m.f.store.db, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,?,?,'reverted',?,NULL,?,?)`,
		migrateTombstoneEffectID(m.command.InstallationID, operation), operation, m.command.InstallationID, "migrate", 2, "migrate-data", strings.Repeat("7", 64), 2,
		m.command.ControllerEpoch, 2, string(raw), now, now)
}

// A failed update is recovered by one retry. A chain that runs through a link another chain already recovered is a
// second retry of the same update, and the runner refuses it whatever the Portal sent.
func TestMigrateDataRefusesALinkAnotherChainRecovered(t *testing.T) {
	rival := retryOperation(6)
	t.Run("a claimed retry of the same update", func(t *testing.T) {
		r := retryJournalFixture(t)
		now := time.Now().UTC().UnixMilli()
		r.insertSettledMigration(t, rival, r.failed, "discarded", now-9)
		r.insertRestore(t, rival, now-8)
		mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET created_ms=?,updated_ms=? WHERE effect_id=?`, now-2, now-2, r.command.Payload.CheckpointEffectID)
		if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err == nil {
			t.Fatal("a second retry of an update another retry already recovered was admitted")
		}
	})
	t.Run("a tombstone another retry wrote", func(t *testing.T) {
		r := neverClaimedRetryFixture(t)
		r.insertTombstone(t, r.failed, rival, "")
		if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err == nil {
			t.Fatal("a retry was admitted over a link another retry closed")
		}
	})
	t.Run("the tombstone of its own chain is not a rival", func(t *testing.T) {
		r := neverClaimedRetryFixture(t)
		r.insertTombstone(t, r.failed, r.command.OperationID, "")
		if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err != nil {
			t.Fatalf("a link the retry itself closed was taken for a rival: %v", err)
		}
	})
	t.Run("the retry before it in the same chain is not a rival", func(t *testing.T) {
		r := retryOfRetryFixture(t)
		if _, _, err := r.f.store.beginMigrateData(context.Background(), r.raw, r.sig); err != nil {
			t.Fatalf("a link recovered by an earlier retry of the same chain was taken for a rival: %v", err)
		}
	})
}
