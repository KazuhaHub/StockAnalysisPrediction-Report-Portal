//go:build linux && (amd64 || arm64)

package driver

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"golang.org/x/sys/unix"
)

func TestNativeDataWriterContainer(t *testing.T) {
	imageReference := os.Getenv("APP_WRITER_NATIVE_IMAGE")
	if imageReference == "" {
		t.Skip("explicit disposable XFS writer image only")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("writer container requires disposable Linux root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rootPath := os.Getenv("APP_CANDIDATE_NATIVE_ROOT")
	input, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close() })
	root, err := data.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	inode, err := strconv.ParseUint(os.Getenv("APP_CANDIDATE_NATIVE_INODE"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	pin := data.RootIdentity{FilesystemUUID: os.Getenv("APP_DATA_NATIVE_UUID"), Inode: inode}
	owner := containerFixture(t).owner
	owner.NamespaceID = "00000000-0000-4000-8000-000000600002"
	owner.InstallationID = "00000000-0000-4000-8000-000000600004"
	owner.ResourceID = "00000000-0000-4000-8000-000000600005"
	owner.WriterFence = "1"
	allocation, err := root.Prepare(ctx, pin, data.Request{NamespaceID: owner.NamespaceID, ProjectID: 600002, Bytes: 8 << 20, Inodes: 128})
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _ := managedImageFixture(t)
	var manifest map[string]any
	if json.Unmarshal(raw, &manifest) != nil {
		t.Fatal("fixture manifest")
	}
	backend := manifest["backend"].(map[string]any)
	backend["image"], backend["platforms"] = imageReference, []string{"linux/" + runtime.GOARCH}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	owner.ReleaseSHA256 = containerHash(raw)
	sockets, err := os.MkdirTemp("/run", "portal-writer-")
	if err != nil {
		t.Fatal(err)
	}
	storageReleased := false
	t.Cleanup(func() {
		if !storageReleased {
			t.Error("preserving writer socket paths because owned storage release was not verified")
			return
		}
		if err := os.RemoveAll(sockets); err != nil {
			t.Error(err)
		}
	})
	native, err := OpenCandidateRuntime(ctx, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), rootPath, sockets, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	request := CandidateLaunchRequest{Descriptor: raw, Platform: "linux/" + runtime.GOARCH, Identity: CandidateIdentity(owner), Data: CandidateDataEvidence{Root: pin, ProjectID: allocation.ProjectID, Device: allocation.Device, NamespaceInode: allocation.Inode, QuotaBytes: allocation.ByteLimit, QuotaInodes: allocation.InodeLimit}}
	if err := native.ProvisionCandidateSockets(ctx, request); err != nil {
		native.Close()
		t.Fatal(err)
	}
	client, closeClient := native.client()
	var nativeID string
	var plan containerPlan
	t.Cleanup(func() {
		defer closeClient()
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if nativeID != "" {
			if _, err := stopCandidateContainerHTTP(cleanup, client, nativeID, plan); err != nil {
				t.Error("owned writer fixture stop", err)
				return
			}
			if err := removeCandidateContainerHTTP(cleanup, client, nativeID, plan); err != nil {
				t.Error("owned writer fixture removal", err)
				return
			}
		}
		if err := native.Close(); err != nil {
			t.Error(err)
		}
		if err := native.ReleaseCandidateHTTPStorage(cleanup, request.Identity); err != nil {
			t.Error("owned writer IPC cleanup", err)
			return
		}
		storageReleased = true
	})
	image, err := InspectBackendImage(ctx, native.socket, raw, owner.ReleaseSHA256)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = planCandidateContainer(raw, image, owner, native.roots)
	if err != nil {
		t.Fatal(err)
	}
	writerRequest := DataWriterRequest{Identity: request.Identity, Descriptor: raw, Platform: request.Platform, Data: request.Data, Binding: DataWriterBinding{EffectID: "00000000-0000-4000-8000-000000600006", ClaimID: "00000000-0000-4000-8000-000000600007", PayloadSHA256: containerHash([]byte("writer trial")), DataReceiptSHA256: containerHash([]byte("allocation trial"))}}
	launch, err := native.PrepareCandidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := native.EnableDataWriter(ctx, writerRequest); err != ErrContainerConflict {
		launch.Close()
		t.Fatal("live namespace pin did not exclude writer", err)
	}
	launch.Close()
	nativeID, err = createCandidateContainerHTTP(ctx, client, plan)
	if err != nil {
		t.Fatal(err)
	}
	other, err := OpenCandidateRuntime(ctx, native.socket, rootPath, sockets, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	_, conflict := other.EnableDataWriter(ctx, writerRequest)
	other.Close()
	if conflict != ErrContainerConflict {
		t.Fatal("actual stopped container reference did not exclude writer", conflict)
	}
	sealed, err := root.Inspect(ctx, pin, allocation.NamespaceID, allocation.ProjectID)
	if err != nil || sealed != allocation {
		t.Fatal("refused writer changed sealed namespace", err)
	}
	if err := removeCandidateContainerHTTP(ctx, client, nativeID, plan); err != nil {
		t.Fatal(err)
	}
	nativeID = ""
	observation, err := native.EnableDataWriter(ctx, writerRequest)
	if err != nil {
		t.Fatal("concrete writer transition", err)
	}
	snapshot, err := observation.Snapshot()
	if err != nil || snapshot.Binding != writerRequest.Binding || snapshot.Identity != request.Identity || snapshot.Data != request.Data || snapshot.Profile != DataWriterSandboxProfile {
		t.Fatal("opaque native writer evidence", err)
	}
	if _, err := json.Marshal(observation); err == nil {
		t.Fatal("native writer evidence serialized into authority")
	}
	if _, err := native.EnableDataWriter(ctx, writerRequest); err == nil {
		t.Fatal("native writer transition replayed/repaired")
	}
	t.Log("NATIVE_DATA_WRITER_ADAPTER_VERIFIED")
	checkGlobalSpace := func() {
		t.Helper()
		var fs unix.Statfs_t
		if err := unix.Fstatfs(int(input.Fd()), &fs); err != nil || fs.Bsize <= 0 || fs.Bavail < uint64(64<<20)/uint64(fs.Bsize) {
			t.Fatal("disposable filesystem lacks independent global capacity", err)
		}
	}
	checkGlobalSpace()
	// The production candidate route must remain sealed/read-only. This test
	// does not smuggle a writable launch into an admitted candidate command.
	if launch, err := native.PrepareCandidate(ctx, request); err == nil {
		launch.Close()
		t.Fatal("production candidate accepted write-access namespace")
	}
	var policy containerCreate
	if json.Unmarshal(plan.body, &policy) != nil || !policy.HostConfig.Mounts[0].ReadOnly {
		t.Fatal("production builder opened data writes")
	}
	// Explicit test-only physical trial: alter just data RW and the fixture
	// startup probe flag. All syscall/identity/network/resource constraints stay
	// fixed and native inspection must match the exact derived test policy.
	policy.HostConfig.Mounts[0].ReadOnly = false
	policy.Env = append(policy.Env, "PORTAL_APP_WRITER_PROBE=1")
	delete(policy.Labels, containerLabelPrefix+"policy-sha256")
	encoded, _ := json.Marshal(policy)
	policy.Labels[containerLabelPrefix+"policy-sha256"] = containerHash(encoded)
	plan.body, _ = json.Marshal(policy)
	plan.bodySHA = containerHash(plan.body)
	nativeID, err = createCandidateContainerHTTP(ctx, client, plan)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := startCandidateContainerHTTP(ctx, client, nativeID, plan); err != nil || state != "running" {
		logDisposableCandidateFailure(t, client, nativeID)
		t.Fatal("physical writer fixture start", state, err)
	}
	socket := filepath.Join(sockets, owner.InstallationID, owner.Generation, "http", "app.sock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		ready, err := probeCandidateReadiness(ctx, socket, 65532, 65532)
		if err == nil && ready.Ready && ready.DataFormat == "1" {
			break
		}
		if !time.Now().Before(deadline) {
			logDisposableCandidateFailure(t, client, nativeID)
			t.Fatal("actual write/attribute/quota probe did not reach readiness", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if state, err := stopCandidateContainerHTTP(ctx, client, nativeID, plan); err != nil || state != "exited" {
		t.Fatal("stop before host data inspection", state, err)
	}
	after, err := root.InspectWriteAccess(ctx, pin, allocation.NamespaceID, allocation.ProjectID)
	if err != nil || after.Namespace.Inode != allocation.Inode || after.Namespace.ByteLimit != 8<<20 || after.Namespace.InodeLimit != 128 || after.Namespace.UsedBytes > 8<<20 || after.Namespace.UsedInodes != 128 {
		t.Fatal("app changed root identity, inheritance or quota", err, after.Namespace)
	}
	dataPath := filepath.Join(rootPath, owner.NamespaceID)
	checkWriterProbeProject(t, dataPath, allocation.ProjectID, true)
	entries, err := os.ReadDir(dataPath)
	if err != nil || len(entries) != 3 {
		t.Fatal("unexpected writer probe root contents", err)
	}
	for _, entry := range entries {
		checkWriterProbeProject(t, filepath.Join(dataPath, entry.Name()), allocation.ProjectID, entry.IsDir())
	}
	children, err := os.ReadDir(filepath.Join(dataPath, "children"))
	if err != nil || len(children) < 110 || len(children) >= 128 {
		t.Fatal("inode exhaustion fixture missing", err)
	}
	for _, entry := range children {
		checkWriterProbeProject(t, filepath.Join(dataPath, "children", entry.Name()), allocation.ProjectID, false)
	}
	fd, err := unix.Open(filepath.Join(dataPath, "writer-proof.json"), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	proofFile := os.NewFile(uintptr(fd), "owned-writer-proof")
	proof, err := io.ReadAll(io.LimitReader(proofFile, 4097))
	proofFile.Close()
	var result struct {
		Format        string `json:"format"`
		Allocated     int64  `json:"allocated_bytes"`
		Inodes        int    `json:"created_inodes"`
		ByteDenied    bool   `json:"byte_denied"`
		InodeDenied   bool   `json:"inode_denied"`
		AttrDenied    bool   `json:"attribute_changes_denied"`
		RootProtected bool   `json:"root_owner_protected"`
	}
	if err != nil || len(proof) > 4096 || json.Unmarshal(proof, &result) != nil || result.Format != "portal.fixture.write-probe/1-dev.1" || result.Allocated < 7<<20 || result.Allocated > 8<<20 || result.Inodes != len(children) || !result.ByteDenied || !result.InodeDenied || !result.AttrDenied || !result.RootProtected {
		t.Fatal("bounded actual writer proof missing", err)
	}
	// Another project remains writable after this one exhausts its inodes. This
	// excludes a global filesystem-full result as the explanation for refusal.
	healthyID := "00000000-0000-4000-8000-000000600003"
	if _, err := root.Prepare(ctx, pin, data.Request{NamespaceID: healthyID, ProjectID: 600003, Bytes: 8 << 20, Inodes: 128}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, healthyID, "healthy"), []byte("independent quota remains writable"), 0600); err != nil {
		t.Fatal("sibling project affected", err)
	}
	checkGlobalSpace()
	t.Log("NATIVE_FILTERED_DATA_WRITER_VERIFIED")
}

func checkWriterProbeProject(t *testing.T, path string, project uint32, directory bool) {
	t.Helper()
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	var attr [28]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0x801c581f, uintptr(unsafe.Pointer(&attr[0])))
	if errno != 0 || binary.NativeEndian.Uint32(attr[12:16]) != project || (directory && binary.NativeEndian.Uint32(attr[:4])&0x200 == 0) {
		t.Fatal("actual child escaped project identity/inheritance", fmt.Sprintf("%s: %v", filepath.Base(path), errno))
	}
}
