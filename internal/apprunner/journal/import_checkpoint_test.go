//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

var importDataPin = data.RootIdentity{FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", Inode: 64}

func importAuthority() control.Authority {
	enrollment := enrollmentFixture(30)
	return control.Authority{
		DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey,
		Fingerprint: enrollment.Fingerprint,
	}
}

// importCommandFixture returns an unsigned command whose ids start at base so
// several independent commands can share one journal.
func importCommandFixture(t *testing.T, base int) control.ImportCheckpointCommand {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	authority := importAuthority()
	command := control.ImportCheckpointCommand{
		Format: control.ImportCheckpointFormat, DeploymentID: authority.DeploymentID,
		ControllerEpoch: authority.ControllerEpoch, ControlRevision: "3",
		InstallationID: fixtureID(base), OperationID: fixtureID(base + 1),
		TargetGeneration: "2", Stage: "restore_data", EffectNumber: "1",
		EffectID: fixtureID(base + 2), Kind: "import-checkpoint", InstallationFence: "2",
		Payload: control.ImportCheckpointPayload{
			GrantID: fixtureID(base + 3), RecoverySetID: fixtureID(base + 4),
			CheckpointID: fixtureID(base + 5), NamespaceID: fixtureID(base + 6),
			ReleaseSHA256: strings.Repeat("a", 64), ManifestSHA256: strings.Repeat("b", 64),
			ArchiveBytes: "4096", DataFormat: "1", WriterFence: "1",
			QuotaBytes: "268435456", QuotaInodes: "65536",
			SourceProjectID: "4201", SourceNamespaceInode: "10", SourceDevice: "11",
			SourceFilesystemUUID: fixtureID(base + 7), SourceRootInode: "64",
			CheckpointFilesystemUUID: fixtureID(base + 8), CheckpointRootInode: "65",
		},
		IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
	}
	return command
}

func signImportCommand(t *testing.T, command control.ImportCheckpointCommand) ([]byte, []byte) {
	t.Helper()
	digest, err := control.ImportCheckpointLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	raw, signature, err := control.SignImportCheckpoint(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return raw, signature
}

func importStoreFixture(t *testing.T) (*Store, control.ImportCheckpointCommand, []byte, []byte) {
	t.Helper()
	store, _ := enrolledCommandStore(t)
	command := importCommandFixture(t, 1700)
	raw, signature := signImportCommand(t, command)
	return store, command, raw, signature
}

func TestImportClaimCommitsUnknownBeforeNative(t *testing.T) {
	store, command, raw, signature := importStoreFixture(t)
	intent, replay, err := store.beginImportCheckpoint(context.Background(), raw, signature, importDataPin)
	if err != nil || replay {
		t.Fatalf("import claim: replay=%v err=%v", replay, err)
	}
	var state, receipt, kind, stage string
	var fence int64
	var acked any
	if err := store.db.QueryRow(`SELECT state,receipt_json,kind,stage,admitted_fence,ack_attempted_ms FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&state, &receipt, &kind, &stage, &fence, &acked); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" || kind != "import-checkpoint" || stage != "restore_data" || fence != 2 || acked != nil {
		t.Fatalf("claim row: %s %s %s %d", state, kind, stage, fence)
	}
	var marker importClaimMarker
	if !canonicalDataJSON(receipt, &marker) || !validImportMarker(marker) {
		t.Fatalf("claim marker is not canonical and valid: %s", receipt)
	}
	if markerImportRoot(marker) != importDataPin {
		t.Fatal("claim was committed without the data root bound")
	}
	if marker.GrantID != command.Payload.GrantID || marker.CheckpointID != command.Payload.CheckpointID || marker.NamespaceID != command.Payload.NamespaceID || marker.RecoverySetID != command.Payload.RecoverySetID {
		t.Fatal("claim marker does not bind the grant, checkpoint and namespace")
	}
	if intent.command != command.Payload || intent.project == 0 || intent.pin != importDataPin || intent.quotaBytes != 268435456 {
		t.Fatalf("intent: %+v", intent)
	}
	var native, observed, desired, ownership string
	var revision int64
	if err := store.db.QueryRow(`SELECT native_id,observed_state,desired_state,revision,ownership_sha256 FROM runner_resources WHERE id=? AND kind='volume' AND data_namespace_id=?`,
		command.Payload.NamespaceID, command.Payload.NamespaceID).Scan(&native, &observed, &desired, &revision, &ownership); err != nil {
		t.Fatal(err)
	}
	if native != dataProjectPrefix+strconv.FormatUint(uint64(intent.project), 10) || observed != "unknown" || desired != "present" || revision != 1 || ownership != marker.OwnershipSHA256 {
		t.Fatalf("reserved resource: %s %s %s %d", native, observed, desired, revision)
	}
	// A restart or a second caller cannot reclaim unknown work.
	if _, _, err := store.beginImportCheckpoint(context.Background(), raw, signature, importDataPin); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatalf("unknown import was reclaimed: %v", err)
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands`) != 1 || scalar(t, store.db, `SELECT COUNT(*) FROM runner_resources`) != 1 {
		t.Fatal("second claim wrote rows")
	}
}

func TestImportCheckpointRefusesExistingNamespace(t *testing.T) {
	for _, mode := range []string{"namespace resource", "resource id", "installation volume"} {
		t.Run(mode, func(t *testing.T) {
			store, command, raw, signature := importStoreFixture(t)
			id, installation, namespace := command.Payload.NamespaceID, command.InstallationID, command.Payload.NamespaceID
			switch mode {
			case "namespace resource":
				id = fixtureID(1790)
			case "resource id":
				namespace = fixtureID(1791)
			case "installation volume":
				id, namespace = fixtureID(1790), fixtureID(1791)
			}
			if mode != "installation volume" {
				installation = fixtureID(1792)
			}
			mustJournalExec(t, store.db, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
				VALUES(?,?,?,'volume','xfs-project/v1/1',?,1,?,1,1,'present','present',?,2,1,1,1)`,
				id, installation, namespace, strings.Repeat("a", 64), command.ControllerEpoch, strings.Repeat("c", 64))
			if _, _, err := store.beginImportCheckpoint(context.Background(), raw, signature, importDataPin); err == nil {
				t.Fatal("import admitted over an existing namespace resource")
			}
			if scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands`) != 0 || scalar(t, store.db, `SELECT COUNT(*) FROM runner_resources`) != 1 {
				t.Fatal("refused import wrote rows")
			}
		})
	}
}

func TestImportCheckpointRefusesSecondEffectSameGrant(t *testing.T) {
	store, first, raw, signature := importStoreFixture(t)
	if _, _, err := store.beginImportCheckpoint(context.Background(), raw, signature, importDataPin); err != nil {
		t.Fatal(err)
	}
	for _, repeated := range []string{"grant", "checkpoint", "namespace"} {
		t.Run(repeated, func(t *testing.T) {
			// A different installation, operation and effect isolates the duplicate
			// rule from the per-installation pending-command rule.
			second := importCommandFixture(t, 1800)
			switch repeated {
			case "grant":
				second.Payload.GrantID = first.Payload.GrantID
			case "checkpoint":
				second.Payload.CheckpointID = first.Payload.CheckpointID
			case "namespace":
				second.Payload.NamespaceID = first.Payload.NamespaceID
			}
			secondRaw, secondSignature := signImportCommand(t, second)
			commands := scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands`)
			if _, _, err := store.beginImportCheckpoint(context.Background(), secondRaw, secondSignature, importDataPin); err == nil {
				t.Fatalf("second effect with a repeated %s admitted", repeated)
			}
			if scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands`) != commands || scalar(t, store.db, `SELECT COUNT(*) FROM runner_resources`) != 1 {
				t.Fatal("refused duplicate wrote rows")
			}
		})
	}
	independent := importCommandFixture(t, 1900)
	independentRaw, independentSignature := signImportCommand(t, independent)
	if _, _, err := store.beginImportCheckpoint(context.Background(), independentRaw, independentSignature, importDataPin); err != nil {
		t.Fatalf("independent import refused: %v", err)
	}
}

func TestImportCheckpointRefusesBadCommands(t *testing.T) {
	store, command, raw, signature := importStoreFixture(t)
	tampered := bytes.Clone(raw)
	tampered[len(tampered)/2] ^= 1
	if _, _, err := store.beginImportCheckpoint(context.Background(), tampered, signature, importDataPin); err == nil {
		t.Fatal("tampered import admitted")
	}
	expired := command
	past := time.Now().UTC().UnixMilli() - 60000
	expired.IssuedMS, expired.ExpiresMS = strconv.FormatInt(past, 10), strconv.FormatInt(past+25000, 10)
	digest, err := control.ImportCheckpointLogicalDigest(expired)
	if err != nil {
		t.Fatal(err)
	}
	expired.PayloadSHA256 = digest
	if _, _, err := control.SignImportCheckpoint(expired, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), past+1000); err != nil {
		t.Fatal(err)
	}
	expiredRaw, expiredSignature, _ := control.SignImportCheckpoint(expired, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), past+1000)
	if _, _, err := store.beginImportCheckpoint(context.Background(), expiredRaw, expiredSignature, importDataPin); err == nil {
		t.Fatal("expired import admitted")
	}
	for _, pin := range []data.RootIdentity{{}, {FilesystemUUID: importDataPin.FilesystemUUID}, {FilesystemUUID: "00000000-0000-0000-0000-000000000000", Inode: 1}} {
		if _, _, err := store.beginImportCheckpoint(context.Background(), raw, signature, pin); err == nil {
			t.Fatal("import admitted with an invalid root pin")
		}
	}
	unenrolled, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer unenrolled.Close()
	if _, _, err := unenrolled.beginImportCheckpoint(context.Background(), raw, signature, importDataPin); err == nil {
		t.Fatal("import admitted without an enrolled controller")
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands`)+scalar(t, store.db, `SELECT COUNT(*) FROM runner_resources`) != 0 {
		t.Fatal("refused commands wrote rows")
	}
}

func TestImportCheckpointRefusesFenceAndEpochRegression(t *testing.T) {
	store, command, raw, signature := importStoreFixture(t)
	mustJournalExec(t, store.db, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,'stage',1,'stage',?,3,?,5,'applied','{}',NULL,1,1)`,
		fixtureID(1750), fixtureID(1751), command.InstallationID, strings.Repeat("e", 64), command.ControllerEpoch)
	if _, _, err := store.beginImportCheckpoint(context.Background(), raw, signature, importDataPin); err == nil {
		t.Fatal("import below the installation fence and generation floor admitted")
	}
	mustJournalExec(t, store.db, `UPDATE runner_commands SET controller_epoch=? WHERE effect_id=?`, fixtureID(1752), fixtureID(1750))
	if _, _, err := store.beginImportCheckpoint(context.Background(), raw, signature, importDataPin); err == nil {
		t.Fatal("import admitted over rows from another controller epoch")
	}
}
