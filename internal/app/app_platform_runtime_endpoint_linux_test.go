//go:build linux

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func TestPlatformRuntimeEndpointAcceptance(t *testing.T) {
	if os.Getenv("PORTAL_RUNTIME_CONFIRM_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable root Linux endpoint acceptance")
	}
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Fatal("requires root on disposable hosted Linux")
	}
	root, err := os.MkdirTemp("/tmp", "portal-runtime-confirm-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "portal.test")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(binary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0555)
	if err != nil {
		t.Fatal(err)
	}
	if err := output.Chmod(0555); err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("fixture binary copy failed")
	}
	control := filepath.Join(root, "control")
	if err := os.Mkdir(control, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(control, 0, 41001); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(control, 0750); err != nil {
		t.Fatal(err)
	}
	childData := filepath.Join(root, "child")
	if err := os.Mkdir(childData, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(childData, 41001, 41001); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "journal")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	publicReader, publicWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer publicReader.Close()
	defer publicWriter.Close()
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyReader.Close()
	defer readyWriter.Close()
	path := filepath.Join(control, "control.sock")
	child := exec.Command(binary, "-test.v", "-test.run=^TestPlatformRuntimeEndpointChild$", "-test.timeout=30s")
	child.Env = []string{"GOMAXPROCS=2", "PORTAL_RUNTIME_CONFIRM_CHILD=1", "RUNTIME_CONFIRM_SOCKET=" + path, "TMPDIR=" + childData}
	child.ExtraFiles = []*os.File{publicWriter, readyReader}
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 41001, Gid: 41001, Groups: []uint32{41001}}}
	var result bytes.Buffer
	child.Stdout = &result
	child.Stderr = &result
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			child.Process.Kill()
			child.Wait()
		}
	}()
	publicWriter.Close()
	readyReader.Close()
	raw, err := io.ReadAll(io.LimitReader(publicReader, 4097))
	if err != nil || len(raw) > 4096 {
		t.Fatal("invalid child enrollment frame")
	}
	var enrollment journal.ControllerEnrollment
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&enrollment) != nil {
		t.Fatal("child did not produce public enrollment")
	}
	if _, created, err := store.EnrollController(context.Background(), enrollment); err != nil || !created {
		t.Fatalf("explicit isolated enrollment: %v", err)
	}
	server, err := transport.ListenNetworkController(context.Background(), path, 41001, 41001, store, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	if _, err := readyWriter.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	readyWriter.Close()
	if err := child.Wait(); err != nil {
		t.Fatalf("Portal endpoint confirmation failed: %v\n%s", err, result.String())
	}
	if !bytes.Contains(result.Bytes(), []byte("CURRENT_SQL_NETWORK_PROFILE_CROSS_UID_VERIFIED")) {
		t.Fatal("child did not verify current SQL network observation")
	}
	t.Log("CURRENT_SQL_NETWORK_PROFILE_CROSS_UID_VERIFIED")
	t.Log("non-root Portal registration -> explicit root journal enrollment -> authenticated nonce probe -> transactional binding confirmation passed")
}
func TestPlatformRuntimeEndpointChild(t *testing.T) {
	if os.Getenv("PORTAL_RUNTIME_CONFIRM_CHILD") != "1" {
		t.Skip("isolated Portal subprocess only")
	}
	if os.Geteuid() != 41001 {
		t.Fatal("Portal did not drop root identity")
	}
	public := os.NewFile(3, "public-enrollment")
	ready := os.NewFile(4, "ready")
	if public == nil || ready == nil {
		t.Fatal("missing fixture pipes")
	}
	defer public.Close()
	defer ready.Close()
	s, session := runtimeRegistrationFixture(t, false)
	expected, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	enrollment := journal.ControllerEnrollment{DeploymentID: expected.deploymentID, ControllerEpoch: expected.controllerEpoch, ControlRevision: expected.controlRevision, PublicKey: expected.publicKey, Fingerprint: expected.fingerprint}
	if err := json.NewEncoder(public).Encode(enrollment); err != nil {
		t.Fatal(err)
	}
	public.Close()
	var signal [1]byte
	if _, err := io.ReadFull(ready, signal[:]); err != nil || signal[0] != 1 {
		t.Fatal("root endpoint not ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	endpoint := platformRuntimeEndpoint{path: os.Getenv("RUNTIME_CONFIRM_SOCKET"), controllerUID: 41001, controllerGID: 41001}
	confirmed, err := s.confirmPlatformRuntimeBinding(ctx, session, expected, endpoint)
	if err != nil || confirmed.revision != 2 || scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings`) != 1 {
		t.Fatalf("live endpoint did not confirm: %v", err)
	}

	before := dumpOf(t, s.st)
	network, err := s.observePlatformNetworkRuntime(ctx, session, endpoint)
	if err != nil || !samePlatformManagedRuntime(network.runtime, confirmed) || !validPlatformNetworkRuntimeObservation(network.observation, confirmed, endpoint) || network.observation.Profile().ActiveRequests != 2 || network.observation.Profile().CacheEntries != 3 || network.observation.Profile().WritableBackend {
		t.Fatalf("current SQL network observation: %v", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("network observation changed authority")
	}
	if validPlatformNetworkRuntimeObservation(network.observation, confirmed, platformRuntimeEndpoint{path: endpoint.path, controllerUID: 41002, controllerGID: 41001}) {
		t.Fatal("observation moved to different controller")
	}
	changed := confirmed
	changed.controlRevision++
	if validPlatformNetworkRuntimeObservation(network.observation, changed, endpoint) {
		t.Fatal("observation survived control revision change")
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0 WHERE id=?`, confirmed.id)
	before = dumpOf(t, s.st)
	if _, err := s.observePlatformNetworkRuntime(ctx, session, endpoint); err == nil {
		t.Fatal("disabled SQL runtime reused observed network provider")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("disabled observation refusal wrote")
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=1 WHERE id=?`, confirmed.id)
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, session.username)
	if _, err := s.observePlatformNetworkRuntime(ctx, session, endpoint); err == nil {
		t.Fatal("demoted administrator sampled live network runtime")
	}
	t.Log("CURRENT_SQL_NETWORK_PROFILE_CROSS_UID_VERIFIED")
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != 0 {
		t.Fatal("control confirmation admitted an app")
	}
}
