//go:build linux && (amd64 || arm64)

package driver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

func TestNativeCandidateRuntime(t *testing.T) {
	rootPath := os.Getenv("APP_CANDIDATE_NATIVE_ROOT")
	if rootPath == "" {
		t.Skip("explicit disposable XFS plus Docker fixture not enabled")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("native candidate requires disposable Linux root")
	}
	if !t.Run("registration_atomic_publication", TestBackendRegistrationAtomicPublish) {
		t.Fatal("protected file publication contract failed")
	}
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
	inode, err := strconv.ParseUint(os.Getenv("APP_CANDIDATE_NATIVE_INODE"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	pin := data.RootIdentity{FilesystemUUID: os.Getenv("APP_DATA_NATIVE_UUID"), Inode: inode}
	actual, err := root.Identity(context.Background())
	if err != nil || actual != pin {
		t.Fatal("independent root pin mismatch", err)
	}
	owner := containerFixture(t).owner
	raw, _, _ := managedImageFixture(t)
	var manifest map[string]any
	if json.Unmarshal(raw, &manifest) != nil {
		t.Fatal("fixture manifest")
	}
	backend := manifest["backend"].(map[string]any)
	backend["image"], backend["platforms"] = os.Getenv("APP_CANDIDATE_NATIVE_IMAGE"), []string{"linux/" + runtime.GOARCH}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	owner.ReleaseSHA256 = containerHash(raw)
	observed, err := root.Prepare(context.Background(), pin, data.Request{NamespaceID: owner.NamespaceID, ProjectID: 600001, Bytes: 8 << 20, Inodes: 128})
	if err != nil {
		t.Fatal(err)
	}
	socketRoot, err := os.MkdirTemp("/run", "portal-launch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(socketRoot); err != nil {
			t.Error(err)
		}
	})
	for _, name := range []string{"http", "broker"} {
		path := filepath.Join(socketRoot, owner.InstallationID, owner.Generation, name)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if name == "http" {
			if err := os.Chown(path, 65532, 65532); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.Chmod(path, 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	native, err := OpenCandidateRuntime(context.Background(), os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), rootPath, socketRoot, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	request := CandidateLaunchRequest{Descriptor: raw, Platform: "linux/" + runtime.GOARCH, Identity: CandidateIdentity(owner), Data: CandidateDataEvidence{Root: pin, ProjectID: observed.ProjectID, Device: observed.Device, NamespaceInode: observed.Inode, QuotaBytes: observed.ByteLimit, QuotaInodes: observed.InodeLimit}}
	wrong := request
	wrong.Data.NamespaceInode++
	if launch, err := native.PrepareCandidate(context.Background(), wrong); err == nil {
		launch.Close()
		t.Fatal("changed data pin accepted")
	}
	launch, err := native.PrepareCandidate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Close()
	prepared, err := launch.Snapshot()
	if err != nil || prepared.Identity != request.Identity || prepared.Data != request.Data {
		t.Fatal("preflight snapshot mismatch", err)
	}
	running, err := native.StartCandidate(context.Background(), launch)
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	defer func() {
		if cleaned {
			return
		}
		if err := native.cleanupCandidate(context.Background(), launch, running); err != nil {
			t.Error("owned candidate cleanup requires reconciliation", err)
		}
	}()
	snapshot, err := running.Snapshot()
	if err != nil || snapshot.Launch != prepared || snapshot.NativeID == "" {
		t.Fatal("running snapshot mismatch", err)
	}
	if _, err := native.StartCandidate(context.Background(), launch); err == nil {
		t.Fatal("native launch handle replayed")
	}
	if err := native.cleanupCandidate(context.Background(), launch, running); err != nil {
		t.Fatal(err)
	}
	cleaned = true
	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := launch.Snapshot(); err == nil {
		t.Fatal("runtime close left launch handle live")
	}
	if actual, err := root.Identity(context.Background()); err != nil || actual != pin {
		t.Fatal("runtime closed the shared quota provider", err)
	}
	t.Log("native candidate verified concrete XFS quota/root, image and mount pins, ran once and returned opaque running evidence")
}
