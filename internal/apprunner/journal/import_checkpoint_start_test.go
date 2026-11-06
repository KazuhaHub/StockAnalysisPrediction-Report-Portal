//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// importedStartFixture settles an import and returns a restore-start command
// whose only checkpoint evidence is that applied import, as on a fresh host.
func importedStartFixture(t *testing.T) (*Store, ImportCheckpointReceipt, control.UpdateStartCommand) {
	t.Helper()
	store, _, _, _, intent := claimedImportFixture(t)
	receipt, err := store.settleImportCheckpoint(context.Background(), intent, importSnapshotFor(intent))
	if err != nil {
		t.Fatal(err)
	}
	var receiptRaw string
	if err := store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, receipt.EffectID).Scan(&receiptRaw); err != nil {
		t.Fatal(err)
	}
	command := control.UpdateStartCommand{
		Format: control.UpdateStartFormat, DeploymentID: receipt.DeploymentID,
		ControllerEpoch: receipt.ControllerEpoch, ControlRevision: "3",
		InstallationID: receipt.InstallationID, OperationID: fixtureID(2000),
		TargetGeneration: "3", Stage: "start", EffectNumber: "1", EffectID: fixtureID(2001),
		Kind: "restore-start", InstallationFence: "2",
		Payload: control.UpdateStartPayload{
			ResourceID: fixtureID(2002), NamespaceID: receipt.NamespaceID,
			SourceOperationID: fixtureID(2003), SourceStartEffectID: fixtureID(2004),
			SourceStartReceiptSHA256: strings.Repeat("1", 64),
			SourceDataEffectID:       fixtureID(2005), SourceDataReceiptSHA256: strings.Repeat("2", 64),
			SourceCheckpointOperationID:   receipt.OperationID,
			SourceCheckpointEffectID:      receipt.EffectID,
			SourceCheckpointReceiptSHA256: candidateHash([]byte(receiptRaw)),
			CheckpointID:                  receipt.CheckpointID, CheckpointManifestSHA256: receipt.ManifestSHA256,
			CheckpointArchiveBytes: receipt.ArchiveBytes,
			SourceReleaseSHA256:    receipt.ReleaseSHA256, TargetReleaseSHA256: receipt.ReleaseSHA256,
			SourceGeneration: receipt.TargetGeneration, TargetPolicyRevision: "2",
			ProjectID: receipt.ProjectID, NamespaceInode: receipt.NamespaceInode, Device: receipt.Device,
			QuotaBytes: receipt.QuotaBytes, QuotaInodes: receipt.QuotaInodes,
			RootFilesystemUUID: receipt.Claim.RootFilesystemUUID, RootInode: receipt.Claim.RootInode,
			DataFormat: receipt.DataFormat, SourceWriterFence: receipt.WriterFence, TargetWriterFence: "2",
			Platform: "linux/amd64", SandboxProfile: control.CandidateSandboxProfile,
		},
	}
	return store, receipt, command
}

func signImportedStart(t *testing.T, command control.UpdateStartCommand) ([]byte, []byte) {
	t.Helper()
	digest, err := control.UpdateStartLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	now := time.Now().UTC().UnixMilli()
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, signature, err := control.SignUpdateStart(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, signature
}

func TestRestoreStartAdmitsImportedSource(t *testing.T) {
	store, receipt, command := importedStartFixture(t)
	raw, signature := signImportedStart(t, command)
	reservation, err := store.ReserveUpdateStart(context.Background(), raw, signature)
	if err != nil || reservation.ResourceID != command.Payload.ResourceID || reservation.TargetGeneration != 3 ||
		reservation.TargetReleaseSHA256 != receipt.ReleaseSHA256 || reservation.Replayed {
		t.Fatalf("imported restore-start reservation: %+v %v", reservation, err)
	}
	if replay, err := store.ReserveUpdateStart(context.Background(), raw, signature); err != nil || !replay.Replayed {
		t.Fatalf("imported restore-start replay: %+v %v", replay, err)
	}
	// The claim and the native request read the imported namespace, never a
	// source container or a prepare-data receipt that this journal does not have.
	if _, err := store.ClaimUpdateStart(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	var markerRaw string
	if err := store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&markerRaw); err != nil {
		t.Fatal(err)
	}
	var marker UpdateStartClaimMarker
	if !canonicalDataJSON(markerRaw, &marker) {
		t.Fatal("update-start claim marker")
	}
	tx, facts, err := store.beginUpdateStartExecution(context.Background(), raw, signature, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	request, err := updateStartNativeRequest(facts, marker, nil)
	if err != nil {
		t.Fatal(err)
	}
	project, _ := strconv.ParseUint(receipt.ProjectID, 10, 32)
	inode, _ := strconv.ParseUint(receipt.NamespaceInode, 10, 64)
	device, _ := strconv.ParseUint(receipt.Device, 10, 64)
	if request.Data.Root != importDataPin || uint64(request.Data.ProjectID) != project || request.Data.NamespaceInode != inode ||
		request.Data.Device != device || request.Data.QuotaBytes != 268435456 || request.Data.QuotaInodes != 65536 ||
		request.Identity.NamespaceID != receipt.NamespaceID || request.Binding.CheckpointReceiptSHA256 != command.Payload.SourceCheckpointReceiptSHA256 {
		t.Fatalf("native request does not read the import receipt: %+v", request)
	}
}

func TestRestoreStartImportedRefusesRootMismatch(t *testing.T) {
	for name, mutate := range map[string]func(*control.UpdateStartPayload){
		"filesystem": func(p *control.UpdateStartPayload) { p.RootFilesystemUUID = importOtherPin.FilesystemUUID },
		"inode":      func(p *control.UpdateStartPayload) { p.RootInode = strconv.FormatUint(importOtherPin.Inode, 10) },
	} {
		t.Run(name, func(t *testing.T) {
			store, _, command := importedStartFixture(t)
			mutate(&command.Payload)
			raw, signature := signImportedStart(t, command)
			commands := scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands`)
			resources := scalar(t, store.db, `SELECT COUNT(*) FROM runner_resources`)
			if _, err := store.ReserveUpdateStart(context.Background(), raw, signature); err == nil {
				t.Fatal("restore-start admitted a root the import never used")
			}
			if scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands`) != commands || scalar(t, store.db, `SELECT COUNT(*) FROM runner_resources`) != resources {
				t.Fatal("refused restore-start wrote rows")
			}
		})
	}
}

func TestRestoreStartImportedRefusesProvenanceMismatch(t *testing.T) {
	other := strings.Repeat("9", 64)
	for name, mutate := range map[string]func(*control.UpdateStartPayload){
		"checkpoint":       func(p *control.UpdateStartPayload) { p.CheckpointID = fixtureID(2100) },
		"namespace":        func(p *control.UpdateStartPayload) { p.NamespaceID = fixtureID(2101) },
		"manifest":         func(p *control.UpdateStartPayload) { p.CheckpointManifestSHA256 = other },
		"archive":          func(p *control.UpdateStartPayload) { p.CheckpointArchiveBytes = "8192" },
		"release":          func(p *control.UpdateStartPayload) { p.SourceReleaseSHA256, p.TargetReleaseSHA256 = other, other },
		"data format":      func(p *control.UpdateStartPayload) { p.DataFormat = "2" },
		"writer fence":     func(p *control.UpdateStartPayload) { p.SourceWriterFence, p.TargetWriterFence = "2", "3" },
		"project":          func(p *control.UpdateStartPayload) { p.ProjectID = "4999" },
		"inode":            func(p *control.UpdateStartPayload) { p.NamespaceInode = "1" },
		"device":           func(p *control.UpdateStartPayload) { p.Device = "1" },
		"quota bytes":      func(p *control.UpdateStartPayload) { p.QuotaBytes = "512" },
		"quota inodes":     func(p *control.UpdateStartPayload) { p.QuotaInodes = "1" },
		"receipt digest":   func(p *control.UpdateStartPayload) { p.SourceCheckpointReceiptSHA256 = other },
		"import effect":    func(p *control.UpdateStartPayload) { p.SourceCheckpointEffectID = fixtureID(2102) },
		"import operation": func(p *control.UpdateStartPayload) { p.SourceCheckpointOperationID = fixtureID(2103) },
	} {
		t.Run(name, func(t *testing.T) {
			store, _, command := importedStartFixture(t)
			mutate(&command.Payload)
			raw, signature := signImportedStart(t, command)
			resources := scalar(t, store.db, `SELECT COUNT(*) FROM runner_resources`)
			if _, err := store.ReserveUpdateStart(context.Background(), raw, signature); err == nil {
				t.Fatal("restore-start admitted evidence that disagrees with the import receipt")
			}
			if scalar(t, store.db, `SELECT COUNT(*) FROM runner_resources`) != resources || scalarJournalInt(t, store.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=?`, command.EffectID) != 0 {
				t.Fatal("refused restore-start wrote rows")
			}
		})
	}
	for name, statement := range map[string]string{
		"import not applied":   `UPDATE runner_commands SET state='unknown' WHERE kind='import-checkpoint'`,
		"resource not present": `UPDATE runner_resources SET observed_state='unknown',revision=1 WHERE kind='volume'`,
		"receipt changed":      `UPDATE runner_commands SET receipt_json=replace(receipt_json,'"entries":"3"','"entries":"4"') WHERE kind='import-checkpoint'`,
		"running container": `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
			SELECT '` + fixtureID(2110) + `',installation_id,data_namespace_id,'container','fixture-running',release_sha256,generation,controller_epoch,control_fence,writer_fence,'running','running','` + strings.Repeat("c", 64) + `',2,1,1,1 FROM runner_resources WHERE kind='volume'`,
		"pending command": `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
			SELECT '` + fixtureID(2111) + `','` + fixtureID(2112) + `',installation_id,'stop',1,'stop','` + strings.Repeat("d", 64) + `',target_generation,controller_epoch,admitted_fence,'prepared','{}',NULL,1,1 FROM runner_commands WHERE kind='import-checkpoint'`,
	} {
		t.Run(name, func(t *testing.T) {
			store, _, command := importedStartFixture(t)
			raw, signature := signImportedStart(t, command)
			mustJournalExec(t, store.db, statement)
			if _, err := store.ReserveUpdateStart(context.Background(), raw, signature); err == nil {
				t.Fatal("restore-start admitted an import that is no longer clean evidence")
			}
		})
	}
}

func TestSnapshotSourceStillRefusedForUpdateStartOverImport(t *testing.T) {
	// Only restore-start may rest on import evidence; an update-start naming an
	// import effect as its checkpoint is refused.
	store, _, command := importedStartFixture(t)
	command.Kind = "update-start"
	command.OperationID = command.Payload.SourceCheckpointOperationID
	command.Payload.TargetReleaseSHA256 = strings.Repeat("c", 64)
	raw, signature := signImportedStart(t, command)
	if _, err := store.ReserveUpdateStart(context.Background(), raw, signature); err == nil {
		t.Fatal("update-start accepted import evidence")
	}
}
