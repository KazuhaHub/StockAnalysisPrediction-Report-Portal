//go:build linux && (amd64 || arm64)

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// Fixture-only CLI use independently observes/cleans exact owned native IDs.
// No production lifecycle API or name-only adoption is introduced.
func candidateFixtureDocker(t *testing.T, socket string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket}, args...)...).CombinedOutput()
	if err != nil || len(result) > 65536 {
		t.Fatalf("disposable candidate Docker command failed: %v", err)
	}
	return result
}
func candidateFixtureOwnedNative(t *testing.T, socket string, c control.CandidateCommand) string {
	t.Helper()
	raw := candidateFixtureDocker(t, socket, "container", "ls", "-a", "--no-trunc", "--filter", "label=org.portal.app.resource="+c.Payload.ResourceID, "--format", "{{.ID}}")
	ids := strings.Fields(string(raw))
	if len(ids) == 0 {
		return ""
	}
	if len(ids) != 1 || !dataSHA(ids[0]) {
		t.Fatal("ambiguous fixture native identity")
	}
	var inspected []struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	if err := json.Unmarshal(candidateFixtureDocker(t, socket, "container", "inspect", ids[0]), &inspected); err != nil || len(inspected) != 1 || inspected[0].ID != ids[0] {
		t.Fatal("native fixture inspect mismatch", err)
	}
	expected := map[string]string{"format": "candidate/1-dev.1", "deployment": c.DeploymentID, "epoch": c.ControllerEpoch, "installation": c.InstallationID, "operation": c.OperationID, "resource": c.Payload.ResourceID, "namespace": c.Payload.DataNamespaceID, "release": c.Payload.ReleaseSHA256, "generation": c.TargetGeneration, "control-fence": c.InstallationFence, "writer-fence": c.Payload.WriterFence}
	labels := inspected[0].Config.Labels
	for name, value := range expected {
		if labels["org.portal.app."+name] != value {
			t.Fatal("refusing fixture cleanup: changed native ownership", name)
		}
	}
	if !dataSHA(labels["org.portal.app.policy-sha256"]) {
		t.Fatal("missing fixture native policy identity")
	}
	return ids[0]
}
func TestNativeJournalCandidateExecution(t *testing.T) {
	rootPath, journalPath := os.Getenv("APP_DATA_JOURNAL_ROOT"), os.Getenv("APP_DATA_JOURNAL_CONTROL")
	if os.Getenv("APP_CANDIDATE_NATIVE_IMAGE") == "" {
		t.Skip("explicit disposable journal/XFS/Docker fixture only")
	}
	socket := os.Getenv("APP_CANDIDATE_DOCKER_SOCKET")
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || !filepath.IsAbs(rootPath) || !filepath.IsAbs(journalPath) || !filepath.IsAbs(socket) {
		t.Fatal("requires disposable root and explicit paths")
	}
	inode, err := strconv.ParseUint(os.Getenv("APP_DATA_JOURNAL_INODE"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	pin := data.RootIdentity{FilesystemUUID: os.Getenv("APP_DATA_NATIVE_UUID"), Inode: inode}
	input, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	root, err := data.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	checkpointPath := os.Getenv("APP_DATA_CHECKPOINT_ROOT")
	checkpointInput, err := os.Open(checkpointPath)
	if err != nil || !filepath.IsAbs(checkpointPath) {
		t.Fatal("open checkpoint root", err)
	}
	defer checkpointInput.Close()
	checkpointRoot, err := data.Open(checkpointInput)
	if err != nil {
		t.Fatal("open checkpoint runtime", err)
	}
	defer checkpointRoot.Close()
	checkpointPin, err := checkpointRoot.Identity(context.Background())
	if err != nil {
		t.Fatal("checkpoint identity", err)
	}
	s, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	socketRoot, err := os.MkdirTemp("/run", "portal-jrc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketRoot)
	native, err := driver.OpenCandidateRuntime(context.Background(), socket, rootPath, socketRoot, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	rawDescriptor, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(rawDescriptor, &manifest); err != nil {
		t.Fatal(err)
	}
	backend := manifest["backend"].(map[string]any)
	backend["image"], backend["platforms"] = os.Getenv("APP_CANDIDATE_NATIVE_IMAGE"), []string{"linux/" + runtime.GOARCH}
	descriptor, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	checkpointVerified := false
	restoreVerified := false
	for slot := 0; slot < 4; slot++ {
		if slot == 2 {
			continue
		}
		t.Run(strconv.Itoa(slot), func(t *testing.T) {
			ctx := context.Background()
			prepare := runnerCommandFixture(t)
			prepare.InstallationID, prepare.OperationID, prepare.EffectID, prepare.Payload.DataNamespaceID = fixtureID(7100+slot*10), fixtureID(7101+slot*10), fixtureID(7102+slot*10), fixtureID(7103+slot*10)
			prepare.Payload.ReleaseSHA256 = candidateHash(descriptor)
			prepare.Payload.QuotaBytes, prepare.Payload.QuotaInodes = "8388608", "128"
			raw, sig := signRunnerCommand(t, prepare)
			proof, err := s.DispatchPrepareData(ctx, raw, sig, root, pin)
			if err != nil {
				t.Fatal("real candidate data preparation", err)
			}
			c := journalCandidateFixture(t)
			c.InstallationID, c.OperationID, c.EffectID, c.TargetGeneration = prepare.InstallationID, prepare.OperationID, fixtureID(7104+slot*10), prepare.TargetGeneration
			c.Payload.ResourceID, c.Payload.DataNamespaceID, c.Payload.ReleaseSHA256, c.Payload.Platform = fixtureID(7105+slot*10), prepare.Payload.DataNamespaceID, prepare.Payload.ReleaseSHA256, "linux/"+runtime.GOARCH
			raw, sig = signJournalCandidate(t, c)
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			var claim CandidateClaim
			if slot == 1 {
				if _, err := s.AdmitCandidateCommand(ctx, raw, sig); err != nil {
					t.Fatal(err)
				}
				if _, err := s.ReserveCandidate(ctx, raw, sig); err != nil {
					t.Fatal(err)
				}
				claim, err = s.ClaimCandidate(ctx, raw, sig)
				if err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				id := candidateFixtureOwnedNative(t, socket, c)
				if id != "" {
					candidateFixtureDocker(t, socket, "container", "stop", "--time", "10", id)
					candidateFixtureDocker(t, socket, "container", "rm", id)
				}
				identity := driver.CandidateIdentity{DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID, ResourceID: c.Payload.ResourceID, NamespaceID: c.Payload.DataNamespaceID, ReleaseSHA256: c.Payload.ReleaseSHA256, Generation: c.TargetGeneration, ControlFence: c.InstallationFence, WriterFence: c.Payload.WriterFence}
				if err := native.ReleaseCandidateHTTPStorage(context.Background(), identity); err != nil {
					t.Error("owned IPC cleanup", err)
				}
			})
			if slot == 1 {
				if _, err := s.db.Exec(`CREATE TRIGGER fixture_candidate_settlement_failure BEFORE UPDATE ON runner_commands WHEN NEW.kind='start-candidate' AND NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture settlement failure');END`); err != nil {
					t.Fatal(err)
				}
			}
			var receipt CandidateRunningReceipt
			if slot == 0 || slot == 3 {
				receipt, err = s.DispatchCandidate(ctx, raw, sig, native, descriptor)
			} else {
				receipt, err = s.ExecuteCandidate(ctx, claim, native, descriptor)
			}
			if slot == 0 || slot == 3 {
				if err != nil || !validCandidateReceipt(receipt) || receipt.Claim.Preflight == nil || receipt.Claim.Preflight.NamespaceInode != proof.NamespaceInode {
					t.Fatal("real native candidate receipt", err)
				}
				id := candidateFixtureOwnedNative(t, socket, c)
				if id == "" || id != receipt.NativeID {
					t.Fatal("receipt does not identify actual native process")
				}
				var stored string
				if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=? AND state='applied'`, c.EffectID).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				parsed, err := ParseCandidateRunningReceipt([]byte(stored))
				if err != nil || parsed.NativeID != id {
					t.Fatal("native receipt not persisted", err)
				}
				var nativeID, observed string
				var revision int
				if err := s.db.QueryRow(`SELECT native_id,observed_state,revision FROM runner_resources WHERE id=?`, c.Payload.ResourceID).Scan(&nativeID, &observed, &revision); err != nil || nativeID != id || observed != "running" || revision != 2 {
					t.Fatal("native resource/receipt not atomic", err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				renewed := c
				now := time.Now().UnixMilli()
				renewed.IssuedMS, renewed.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
				renewedRaw, renewedSig := signJournalCandidate(t, renewed)
				historical, err := s.ReadCandidateRunningReceipt(ctx, renewedRaw, renewedSig)
				if err != nil || !sameCandidateReceipt(historical, receipt) {
					t.Fatal("native receipt lost after restart/renewal", err)
				}
				replay, err := s.DispatchCandidate(ctx, renewedRaw, renewedSig, nil, descriptor)
				if err != nil || !sameCandidateReceipt(replay, receipt) || candidateFixtureOwnedNative(t, socket, c) != id {
					t.Fatal("applied dispatch reexecuted/changed native identity", err)
				}
				if slot == 3 {
					enrollment := enrollmentFixture(30)
					authority := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
					key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
					dispatchRemoval := func(stage, kind, operation, effect string) {
						t.Helper()
						now := time.Now().UnixMilli()
						command := control.ManagedRemovalCommand{
							Format: control.ManagedRemovalFormat, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
							ControlRevision: c.ControlRevision, InstallationID: c.InstallationID, OperationID: operation,
							TargetGeneration: "2", Stage: stage, EffectNumber: "1", EffectID: effect, Kind: kind,
							InstallationFence: "2", IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
							Payload: control.ManagedRemovalPayload{ResourceID: c.Payload.ResourceID, DataNamespaceID: c.Payload.DataNamespaceID,
								ReleaseSHA256: c.Payload.ReleaseSHA256, Platform: c.Payload.Platform, SourceOperationID: c.OperationID,
								SourceEffectID: c.EffectID, SourceReceiptSHA256: candidateHash([]byte(stored))},
						}
						digest, err := control.ManagedRemovalLogicalDigest(command)
						if err != nil {
							t.Fatal(err)
						}
						command.PayloadSHA256 = digest
						raw, sig, err := control.SignManagedRemoval(command, key, authority, now)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := s.DispatchManagedRemoval(ctx, raw, sig, descriptor, native); err != nil {
							t.Fatal("managed removal", stage, err)
						}
					}
					dispatchRemoval("stop", "stop", fixtureID(7106+slot*10), fixtureID(7107+slot*10))
					var stoppedState string
					var stoppedRevision int
					if err := s.db.QueryRow(`SELECT observed_state,revision FROM runner_resources WHERE id=?`, c.Payload.ResourceID).Scan(&stoppedState, &stoppedRevision); err != nil || stoppedState != "stopped" || stoppedRevision != 3 {
						t.Fatal("managed stop did not settle", stoppedState, stoppedRevision, err)
					}
					var sourceDataEffect, sourceDataReceipt string
					if err := s.db.QueryRow(`SELECT effect_id,receipt_json FROM runner_commands WHERE operation_id=? AND stage='prepare_data' AND effect_number=1 AND kind='prepare-data' AND state='applied'`, c.OperationID).Scan(&sourceDataEffect, &sourceDataReceipt); err != nil {
						t.Fatal(err)
					}
					now := time.Now().UnixMilli()
					checkpointCommand := control.CheckpointCommand{
						Format: control.CheckpointFormat, DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch,
						ControlRevision: strconv.FormatInt(enrollment.ControlRevision, 10), InstallationID: c.InstallationID,
						OperationID: fixtureID(7112 + slot*10), TargetGeneration: "2", Stage: "snapshot", EffectNumber: "1",
						EffectID: fixtureID(7113 + slot*10), Kind: "snapshot", InstallationFence: "2",
						IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
						Payload: control.CheckpointPayload{
							CheckpointID: fixtureID(7114 + slot*10), NamespaceID: c.Payload.DataNamespaceID, ResourceID: c.Payload.ResourceID,
							ReleaseSHA256: c.Payload.ReleaseSHA256, SourceOperationID: c.OperationID,
							SourceStartEffectID: c.EffectID, SourceReceiptSHA256: candidateHash([]byte(stored)),
							SourceDataEffectID: sourceDataEffect, SourceDataReceiptSHA256: candidateHash([]byte(sourceDataReceipt)),
							ProjectID: proof.ProjectID, NamespaceInode: proof.NamespaceInode, Device: proof.Device,
							DataFormat: proof.DataFormat, WriterFence: proof.WriterFence,
						},
					}
					checkpointCommand.PayloadSHA256, err = control.CheckpointLogicalDigest(checkpointCommand)
					if err != nil {
						t.Fatal(err)
					}
					checkpointRaw, checkpointSig, err := control.SignCheckpoint(checkpointCommand, key, authority, now)
					if err != nil {
						t.Fatal(err)
					}
					checkpoint, err := s.DispatchCheckpoint(ctx, checkpointRaw, checkpointSig, root, pin, checkpointRoot, checkpointPin)
					if err != nil || checkpoint.Format != checkpointReceiptFormat || checkpoint.ManifestSHA256 == "" || checkpoint.ArchiveBytes == "0" {
						t.Fatal("managed update checkpoint", checkpoint, err)
					}
					checkpointAgain, err := s.ReadCheckpointReceipt(ctx, checkpointRaw, checkpointSig)
					if err != nil || checkpointAgain != checkpoint {
						t.Fatal("managed update checkpoint read", checkpointAgain, err)
					}
					checkpointVerified = true
					checkpointReceiptRaw, err := json.Marshal(checkpoint)
					if err != nil {
						t.Fatal(err)
					}
					restoreCommand := control.RestoreCommand{
						Format: control.RestoreFormat, DeploymentID: enrollment.DeploymentID,
						ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: strconv.FormatInt(enrollment.ControlRevision, 10),
						InstallationID: c.InstallationID, OperationID: fixtureID(7115 + slot*10),
						TargetGeneration: "2", Stage: "restore_data", EffectNumber: "1",
						EffectID: fixtureID(7116 + slot*10), Kind: "restore", InstallationFence: "2",
						IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
						Payload: control.RestorePayload{
							RecoverySetID: fixtureID(7117 + slot*10), CheckpointID: checkpoint.CheckpointID,
							NamespaceID: checkpoint.NamespaceID, ReleaseSHA256: checkpoint.ReleaseSHA256,
							ManifestSHA256: checkpoint.ManifestSHA256, ArchiveBytes: checkpoint.ArchiveBytes,
							ProjectID: checkpoint.ProjectID, NamespaceInode: checkpoint.NamespaceInode,
							Device: checkpoint.Device, DataFormat: checkpoint.DataFormat,
							WriterFence:                 checkpoint.WriterFence,
							SourceCheckpointOperationID: checkpointCommand.OperationID,
							SourceCheckpointEffectID:    checkpointCommand.EffectID,
							SourceCheckpointReceiptSHA:  candidateHash(checkpointReceiptRaw),
						},
					}
					restoreCommand.PayloadSHA256, err = control.RestoreLogicalDigest(restoreCommand)
					if err != nil {
						t.Fatal(err)
					}
					restoreRaw, restoreSig, err := control.SignRestore(restoreCommand, key, authority, now)
					if err != nil {
						t.Fatal(err)
					}
					restored, err := s.DispatchRestore(ctx, restoreRaw, restoreSig, root, pin, checkpointRoot, checkpointPin)
					if err != nil || restored.Format != restoreReceiptFormat || restored.ManifestSHA256 != checkpoint.ManifestSHA256 ||
						restored.NamespaceInode != checkpoint.NamespaceInode || restored.NamespaceID != checkpoint.NamespaceID {
						t.Fatal("managed update restore", restored, err)
					}
					restoredAgain, err := s.ReadRestoreReceipt(ctx, restoreRaw, restoreSig)
					if err != nil || restoredAgain != restored {
						t.Fatal("managed update restore read", restoredAgain, err)
					}
					restoreVerified = true
					dispatchRemoval("remove_code", "remove-code", fixtureID(7108+slot*10), fixtureID(7109+slot*10))
					var absentState, absentDesired string
					var absentRevision int
					if err := s.db.QueryRow(`SELECT observed_state,desired_state,revision FROM runner_resources WHERE id=?`, c.Payload.ResourceID).Scan(&absentState, &absentDesired, &absentRevision); err != nil || absentState != "absent" || absentDesired != "absent" || absentRevision != 4 || candidateFixtureOwnedNative(t, socket, c) != "" {
						t.Fatal("managed remove-code did not settle", absentState, absentDesired, absentRevision, err)
					}
					project, err := strconv.ParseUint(proof.ProjectID, 10, 32)
					if err != nil {
						t.Fatal(err)
					}
					retained, err := root.Inspect(ctx, pin, c.Payload.DataNamespaceID, uint32(project))
					if err != nil || retained.NamespaceID != c.Payload.DataNamespaceID || retained.ProjectID != uint32(project) {
						t.Fatal("managed remove-code deleted or changed the retained data namespace", retained, err)
					}
				}
			} else {
				if err == nil || receipt.Format != "" {
					t.Fatal("failed native settlement returned success")
				}
				if candidateFixtureOwnedNative(t, socket, c) == "" {
					t.Fatal("failure did not occur after actual native effect")
				}
				var markerRaw, state, nativeID, observed string
				var revision int
				if err := s.db.QueryRow(`SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&state, &markerRaw); err != nil {
					t.Fatal(err)
				}
				var marker candidateClaimMarker
				if state != "unknown" || !canonicalDataJSON(markerRaw, &marker) || !validCandidateMarker(marker) || marker.Preflight == nil {
					t.Fatal("failed settlement lost durable preflight")
				}
				if err := s.db.QueryRow(`SELECT native_id,observed_state,revision FROM runner_resources WHERE id=?`, c.Payload.ResourceID).Scan(&nativeID, &observed, &revision); err != nil || nativeID != candidatePendingPrefix+c.Payload.ResourceID || observed != "unknown" || revision != 1 {
					t.Fatal("failed settlement partially updated resource", err)
				}
				if _, err := s.db.Exec(`DROP TRIGGER fixture_candidate_settlement_failure`); err != nil {
					t.Fatal(err)
				}
				id := candidateFixtureOwnedNative(t, socket, c)
				if replay, err := s.DispatchCandidate(ctx, raw, sig, native, descriptor); err == nil || replay.Format != "" || candidateFixtureOwnedNative(t, socket, c) != id {
					t.Fatal("unknown dispatch repeated native effect", err)
				}
			}
			if repeated, err := s.ExecuteCandidate(ctx, claim, native, descriptor); err == nil || repeated.Format != "" {
				t.Fatal("consumed/uncertain native effect repeated")
			}
		})
	}
	if !checkpointVerified {
		t.Fatal("signed journal checkpoint was not verified")
	}
	if !restoreVerified {
		t.Fatal("signed journal restore was not verified")
	}
	fmt.Println("NATIVE_JOURNAL_CHECKPOINT_VERIFIED")
	fmt.Println("NATIVE_JOURNAL_RESTORE_VERIFIED")
	t.Log("native journal candidate: actual running receipt; atomic SQL failure retained uncertainty; exact owned native IDs cleaned")
}
