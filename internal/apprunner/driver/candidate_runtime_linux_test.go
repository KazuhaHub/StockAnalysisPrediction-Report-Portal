//go:build linux && (amd64 || arm64)

package driver

import (
	"context"
	"encoding/json"
	"os"
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

	native, err := OpenCandidateRuntime(context.Background(), os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), rootPath, socketRoot, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	request := CandidateLaunchRequest{Descriptor: raw, Platform: "linux/" + runtime.GOARCH, Identity: CandidateIdentity(owner), Data: CandidateDataEvidence{Root: pin, ProjectID: observed.ProjectID, Device: observed.Device, NamespaceInode: observed.Inode, QuotaBytes: observed.ByteLimit, QuotaInodes: observed.InodeLimit}}
	if err := native.ProvisionCandidateSockets(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := native.Close(); err != nil {
			t.Error(err)
		}
		if err := native.ReleaseCandidateHTTPStorage(context.Background(), request.Identity); err != nil {
			t.Error("owned IPC cleanup", err)
		}
	})

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
	if err := native.ReleaseCandidateHTTPStorage(context.Background(), request.Identity); err != ErrContainerConflict {
		t.Fatal("live native pins did not block IPC cleanup", err)
	}
	other, err := OpenCandidateRuntime(context.Background(), os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), rootPath, socketRoot, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.ReleaseCandidateHTTPStorage(context.Background(), request.Identity); err != ErrContainerConflict {
		other.Close()
		t.Fatal("Docker mount reference did not block IPC cleanup", err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	stopped, err := native.StopCandidate(context.Background(), launch, snapshot.NativeID)
	if err != nil {
		t.Fatal(err)
	}
	stoppedSnapshot, err := stopped.Snapshot()
	if err != nil || stoppedSnapshot.State != "exited" || stoppedSnapshot.NativeID != snapshot.NativeID {
		t.Fatal("stop did not return exact exited evidence", stoppedSnapshot, err)
	}
	removed, err := native.RemoveCandidate(context.Background(), launch, snapshot.NativeID)
	if err != nil {
		t.Fatal(err)
	}
	removedSnapshot, err := removed.Snapshot()
	if err != nil || removedSnapshot.State != "absent" || removedSnapshot.NativeID != snapshot.NativeID {
		t.Fatal("remove did not return exact absence evidence", removedSnapshot, err)
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
