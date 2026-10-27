//go:build linux && (amd64 || arm64)

package journal

import (
	"context"
	"encoding/json"
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
	for slot := 0; slot < 2; slot++ {
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
			for _, name := range []string{"http", "broker"} {
				path := filepath.Join(socketRoot, c.InstallationID, c.TargetGeneration, name)
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				if name == "http" {
					if err := os.Chown(path, 65532, 65532); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
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
			})
			if slot == 1 {
				if _, err := s.db.Exec(`CREATE TRIGGER fixture_candidate_settlement_failure BEFORE UPDATE ON runner_commands WHEN NEW.kind='start-candidate' AND NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture settlement failure');END`); err != nil {
					t.Fatal(err)
				}
			}
			var receipt CandidateRunningReceipt
			if slot == 0 {
				receipt, err = s.DispatchCandidate(ctx, raw, sig, native, descriptor)
			} else {
				receipt, err = s.ExecuteCandidate(ctx, claim, native, descriptor)
			}
			if slot == 0 {
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
	t.Log("native journal candidate: actual running receipt; atomic SQL failure retained uncertainty; exact owned native IDs cleaned")
}
