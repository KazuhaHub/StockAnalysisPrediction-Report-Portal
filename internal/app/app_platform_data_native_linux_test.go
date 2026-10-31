//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/service"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func TestPlatformInstallDataEndpointAcceptance(t *testing.T) {
	fixture := os.Getenv("APP_DATA_PORTAL_FIXTURE")
	if fixture == "" {
		t.Skip("explicit disposable Portal data endpoint acceptance")
	}
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || !filepath.IsAbs(fixture) {
		t.Fatal("requires disposable Linux root")
	}
	if platformNetworkNamespaceFixture(t, fixture) {
		return
	}
	ctx := context.Background()
	useService := os.Getenv("APP_DATA_USE_SERVICE") == "1"
	input, err := os.Open(os.Getenv("APP_DATA_JOURNAL_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	root, err := data.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	inode, err := strconv.ParseUint(os.Getenv("APP_DATA_JOURNAL_INODE"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	pin := data.RootIdentity{FilesystemUUID: os.Getenv("APP_DATA_NATIVE_UUID"), Inode: inode}
	checkpointInput, err := os.Open(os.Getenv("APP_DATA_CHECKPOINT_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer checkpointInput.Close()
	checkpointRoot, err := data.Open(checkpointInput)
	if err != nil {
		t.Fatal(err)
	}
	defer checkpointRoot.Close()
	checkpointPin, err := checkpointRoot.Identity(ctx)
	if err != nil || checkpointPin == pin {
		t.Fatal("independent checkpoint root", checkpointPin, err)
	}
	store, err := journal.Open(os.Getenv("APP_DATA_JOURNAL_CONTROL"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deployment, err := store.ControllerDeployment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(fixture, "portal-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	binaryPath := filepath.Join(dir, "portal.test")
	target, err := os.OpenFile(binaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0555)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Chmod(0555); err != nil {
		target.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("copy fixture binary")
	}
	endpoint := filepath.Join(dir, "control")
	if err := os.Mkdir(endpoint, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(endpoint, 0, 41001); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(endpoint, 0750); err != nil {
		t.Fatal(err)
	}
	childData := filepath.Join(dir, "child")
	if err := os.Mkdir(childData, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(childData, 41001, 41001); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("APP_CANDIDATE_DOCKER_SOCKET") != "" {
		startPlatformNetworkHTTPSFixture(t, dir, childData)
	}
	// Copy the public descriptor into a traversable fixture tree. The synthetic
	// controller must not need access to the runner's checkout/home directory.
	descriptor, err := os.ReadFile(os.Getenv("APP_DATA_PORTAL_DESCRIPTOR"))
	if err != nil || len(descriptor) > 131072 {
		t.Fatal("public descriptor fixture unavailable", err)
	}
	work := filepath.Join(dir, "app")
	for _, folder := range []string{work, filepath.Join(dir, "appplatform"), filepath.Join(dir, "appplatform", "testdata"), filepath.Join(dir, "appplatform", "testdata", "descriptors")} {
		if err := os.Mkdir(folder, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(folder, 0755); err != nil {
			t.Fatal(err)
		}
	}
	descriptorPath := filepath.Join(dir, "appplatform", "testdata", "descriptors", "static.json")
	if err := os.WriteFile(descriptorPath, descriptor, 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(descriptorPath, 0444); err != nil {
		t.Fatal(err)
	}
	managed := os.Getenv("APP_CANDIDATE_DOCKER_SOCKET") != ""
	path := filepath.Join(endpoint, "data.sock")
	var server *transport.Server
	var native *driver.CandidateRuntime
	var managedSocketRoot string
	var serviceFixture *platformServiceFixture
	if useService {
		if !managed {
			t.Fatal("managed service acceptance requires the candidate runtime")
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		serviceFixture = startPlatformServiceFixture(t, binaryPath, dir, deployment)
		path = serviceFixture.socket
	} else if managed {
		sockets, openErr := os.MkdirTemp("/run", "portal-pf-")
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer os.RemoveAll(sockets)
		managedSocketRoot = sockets
		native, err = driver.OpenCandidateRuntime(context.Background(), os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), os.Getenv("APP_DATA_JOURNAL_ROOT"), sockets, root, pin)
		if err != nil {
			t.Fatal(err)
		}
		defer native.Close()
		badPath := filepath.Join(endpoint, "invalid-network.sock")
		if _, err := transport.ListenNetworkManagedController(context.Background(), badPath, 41001, 41001, store, root, pin, checkpointRoot, checkpointPin, native, 0, 4); err == nil {
			t.Fatal("invalid network pool silently started managed listener")
		}
		if _, err := os.Stat(badPath); !os.IsNotExist(err) {
			t.Fatal("invalid network bootstrap left an endpoint")
		}
		server, err = transport.ListenNetworkManagedController(context.Background(), path, 41001, 41001, store, root, pin, checkpointRoot, checkpointPin, native, 2, 4)
	} else {
		server, err = transport.ListenDataController(context.Background(), path, 41001, 41001, store, root, pin)
	}
	var stopServer func()
	if useService {
		stopServer = func() { serviceFixture.stop(t) }
	} else {
		if err != nil {
			t.Fatal(err)
		}
		serveCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- server.Serve(serveCtx) }()
		stopServer = sync.OnceFunc(func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
		defer stopServer()
	}
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
	registrationReader, registrationWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer registrationReader.Close()
	defer registrationWriter.Close()
	enrollment, _ := json.Marshal(deployment.ControllerEnrollment)
	childTimeout := "-test.timeout=60s"
	if os.Getenv("APP_SDK_BROWSER_DIR") != "" {
		childTimeout = "-test.timeout=95s"
	}
	child := exec.Command(binaryPath, "-test.run=^TestPlatformInstallDataEndpointChild$", "-test.v", childTimeout)
	child.Dir = work
	child.Env = []string{"GOMAXPROCS=2", "PORTAL_DATA_CHILD=1", "PORTAL_DATA_SOCKET=" + path, "PORTAL_DATA_ENROLLMENT=" + base64.RawStdEncoding.EncodeToString(enrollment), "TMPDIR=" + childData}
	if os.Getenv("PORTAL_NETWORK_NAMESPACE") == "1" {
		child.Env = append(child.Env, "PORTAL_NETWORK_NAMESPACE=1", "GODEBUG=netdns=go")
	}
	if os.Getenv("APP_SDK_BROWSER_DIR") != "" {
		for _, key := range []string{"APP_SDK_BROWSER_DIR", "APP_SDK_BROWSER_NODE", "APP_SDK_BROWSER_RUNNER", "SDK_BROWSER_PACKAGE", "PLAYWRIGHT_BROWSERS_PATH"} {
			value := os.Getenv(key)
			if !filepath.IsAbs(value) || filepath.Clean(value) != value {
				t.Fatal("invalid SDK browser environment", key)
			}
			child.Env = append(child.Env, key+"="+value)
		}
	}
	if managed {
		child.Env = append(child.Env, "PORTAL_MANAGED_PROFILE=1", "PORTAL_MANAGED_IMAGE="+os.Getenv("APP_CANDIDATE_NATIVE_IMAGE"))
		if os.Getenv("APP_READY_NATIVE_IMAGE") != "" {
			child.Env = append(child.Env, "PORTAL_MANAGED_READY=1")
		}
	}
	if managed && !useService {
		fixtureAuthority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
		defer func() {
			stopServer()
			commandPath := filepath.Join(childData, "candidate-command.json")
			checkPlatformFixtureCandidate(t, commandPath, fixtureAuthority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), true)
			checkPlatformWritableStartFixture(t, filepath.Join(childData, "writable-start.json"), fixtureAuthority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), native, true)
			if err := native.Close(); err != nil {
				t.Error(err)
			}
			if _, err := os.Stat(commandPath); err == nil {
				message := readPlatformFixtureCandidate(t, commandPath, fixtureAuthority)
				var c control.CandidateCommand
				if json.Unmarshal(message.Raw, &c) != nil {
					t.Error("invalid owned IPC fixture command")
					return
				}
				identity := driver.CandidateIdentity{DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID, ResourceID: c.Payload.ResourceID, NamespaceID: c.Payload.DataNamespaceID, ReleaseSHA256: c.Payload.ReleaseSHA256, Generation: c.TargetGeneration, ControlFence: c.InstallationFence, WriterFence: c.Payload.WriterFence}
				if err := native.ReleaseCandidateHTTPStorage(context.Background(), identity); err != nil {
					t.Error("owned IPC cleanup", err)
				}
			}
		}()
	}
	if managed && !useService {
		defer func() {
			stopServer()
			fixtureAuthority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
			checkPlatformWritableAutoFixture(t, childData, fixtureAuthority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), native, true)
			if !t.Failed() {
				// The first writable app remains alive while the second is removed.
				checkPlatformWritableStartFixture(t, filepath.Join(childData, "writable-start.json"), fixtureAuthority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), native, false)
				t.Log("PORTAL_WRITABLE_BACKGROUND_NEIGHBOR_PRESERVED")
			}
			t.Log("PORTAL_WRITABLE_BACKGROUND_OWNED_CLEANUP")
		}()
	}
	if useService {
		fixtureAuthority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
		defer func() {
			stopServer()
			input, err := os.Open(os.Getenv("APP_DATA_JOURNAL_ROOT"))
			if err != nil {
				t.Error("reopen service data root", err)
				return
			}
			defer input.Close()
			reopenedRoot, err := data.Open(input)
			if err != nil {
				t.Error("reopen service data root", err)
				return
			}
			defer reopenedRoot.Close()
			reopenedStore, err := journal.Open(os.Getenv("APP_DATA_JOURNAL_CONTROL"))
			if err != nil {
				t.Error("reopen service journal", err)
				return
			}
			defer reopenedStore.Close()
			native, err := driver.OpenCandidateRuntime(context.Background(), os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), os.Getenv("APP_DATA_JOURNAL_ROOT"), serviceFixture.ipcRoot, reopenedRoot, pin)
			if err != nil {
				t.Error("reopen service native runtime", err)
				return
			}
			defer native.Close()
			commandPath := filepath.Join(childData, "candidate-command.json")
			checkPlatformFixtureCandidate(t, commandPath, fixtureAuthority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), true)
			if _, err := os.Stat(commandPath); err == nil {
				message := readPlatformFixtureCandidate(t, commandPath, fixtureAuthority)
				var c control.CandidateCommand
				if json.Unmarshal(message.Raw, &c) != nil {
					t.Error("invalid managed service candidate command")
					return
				}
				identity := driver.CandidateIdentity{DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID, ResourceID: c.Payload.ResourceID, NamespaceID: c.Payload.DataNamespaceID, ReleaseSHA256: c.Payload.ReleaseSHA256, Generation: c.TargetGeneration, ControlFence: c.InstallationFence, WriterFence: c.Payload.WriterFence}
				if err := native.ReleaseCandidateHTTPStorage(context.Background(), identity); err != nil {
					t.Error("managed service candidate IPC cleanup", err)
				}
			}
			checkPlatformWritableAutoFixture(t, childData, fixtureAuthority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), native, false)
			checkPlatformWritableStartFixture(t, filepath.Join(childData, "writable-start.json"), fixtureAuthority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), native, false)
			if err := os.Rename(filepath.Join(childData, "writable-auto-start.json"), filepath.Join(childData, "writable-auto-start.verified.json")); err != nil {
				t.Error("preserve verified background witness", err)
				return
			}
			t.Log("PORTAL_WRITABLE_BACKGROUND_NATIVE_WITNESS")
			checkPlatformWritableAutoFixture(t, childData, fixtureAuthority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), native, true)
			if !t.Failed() {
				t.Log("PORTAL_WRITABLE_BACKGROUND_NEIGHBOR_PRESERVED")
			}
			checkPlatformWritableStartFixture(t, filepath.Join(childData, "writable-start.json"), fixtureAuthority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), native, true)
			t.Log("PORTAL_WRITABLE_BACKGROUND_OWNED_CLEANUP")
			if err := os.RemoveAll(serviceFixture.root); err != nil {
				t.Error("remove service fixture root", err)
			}
		}()
	}
	child.ExtraFiles = []*os.File{publicWriter, readyReader, registrationWriter}
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 41001, Gid: 41001, Groups: []uint32{41001}}}
	var output bytes.Buffer
	child.Stdout = &output
	child.Stderr = &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			child.Process.Kill()
			child.Wait()
		}
		// Wait/kill above joins output writes before printing a failed child's
		// diagnostics; no concurrent bytes.Buffer read or silent EOF masking.
		if t.Failed() {
			t.Logf("Portal child failure output:\n%s", output.String())
		}
	}()
	publicWriter.Close()
	readyReader.Close()
	registrationWriter.Close()
	var message struct{ Raw, Signature []byte }
	decoder := json.NewDecoder(io.LimitReader(publicReader, 131073))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil {
		t.Fatalf("child command unavailable: %v", err)
	}
	authority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
	verified, err := control.Verify(message.Raw, message.Signature, authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readyWriter.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	var checkRegistrationRPC func()
	if managed && os.Getenv("APP_READY_NATIVE_IMAGE") != "" {
		sockets := managedSocketRoot
		observerStore := store
		if useService {
			sockets = serviceFixture.ipcRoot
			observerStore = nil
		}
		checkRegistrationRPC = observeNativeRegistrationRPCFixture(t, ctx, registrationReader, readyWriter, sockets, authority, observerStore)
	}
	readyWriter.Close()
	if err := child.Wait(); err != nil {
		t.Fatalf("Portal data endpoint failed: %v\n%s", err, output.String())
	}
	if checkRegistrationRPC != nil {
		checkRegistrationRPC()
	}
	if managed {
		if !bytes.Contains(output.Bytes(), []byte("MANAGED_PROFILE_OBSERVED")) {
			t.Fatal("Portal did not validate managed profile", output.String())
		}
		for _, marker := range []string{"PORTAL_WRITABLE_REVIEW_CONFIRMED", "PORTAL_WRITABLE_NETWORK_REVIEW_CURRENT", "PORTAL_WRITABLE_NETWORK_GRANT_LAUNCHED", "PORTAL_NETWORK_CURRENT_VIEWER_SIGNED", "PORTAL_NETWORK_CURRENT_GRANT_RECHECKED", "PORTAL_NETWORK_HTTPS_CURRENT_VIEWER", "PORTAL_NETWORK_HTTPS_PRE_IO_REVOKED", "PORTAL_NETWORK_HTTPS_POST_IO_REVOKED", "PORTAL_NETWORK_SESSION_HTTP", "PORTAL_NETWORK_SESSION_HTTP_UNCERTAIN", "PORTAL_WRITER_SETTLEMENT_RECONCILED", "PORTAL_WRITER_RETAINED_IDENTITY", "PORTAL_WRITABLE_START_RECONCILED", "PORTAL_WRITABLE_START_RETAINED_IDENTITY", "PORTAL_WRITABLE_READINESS_ADVANCED", "PORTAL_WRITABLE_READINESS_REFRESHED", "PORTAL_WRITABLE_READINESS_STALE_SOURCE_REFUSED", "PORTAL_WRITABLE_READINESS_WAIT_CONFIRMED", "PORTAL_WRITABLE_REGISTRATION_COMMITTED", "PORTAL_WRITABLE_REGISTRATION_RETAINED_IDENTITY", "PORTAL_WRITABLE_REGISTRATION_STALE_SOURCE_REFUSED", "PORTAL_WRITABLE_BACKEND_BOOT_VERIFIED", "PORTAL_WRITABLE_BACKEND_BOOT_CURRENT_SOURCE_REFUSED", "WRITABLE_BACKEND_REQUEST_RELAY_VERIFIED", "WRITABLE_BACKEND_REQUEST_UNCERTAIN_NO_RETRY", "PORTAL_WRITABLE_INSTALL_ACTIVATED", "PORTAL_WRITABLE_CURRENT_OU_REQUEST_VERIFIED", "PORTAL_WRITABLE_POST_IO_OU_REVOKED"} {
			if !bytes.Contains(output.Bytes(), []byte(marker)) {
				t.Fatal("Portal writable acceptance marker missing", marker, output.String())
			}
			t.Log(marker)
		}
		if !bytes.Contains(output.Bytes(), []byte("PORTAL_WRITABLE_SCHEDULER_CLAIMED_PROGRESS")) {
			t.Fatal("Portal writable background scheduler acceptance missing", output.String())
		}
		t.Log("PORTAL_WRITABLE_SCHEDULER_CLAIMED_PROGRESS")
		for _, stage := range []string{"ARTIFACT_DATA", "WRITER_ROLLBACK", "START_ROLLBACK", "PROBE_ROLLBACK", "REGISTRATION_ROLLBACK", "REGISTRATION_TOKEN", "ACTIVATED"} {
			marker := "PORTAL_WRITABLE_WORKER_" + stage
			if !bytes.Contains(output.Bytes(), []byte(marker)) {
				t.Fatal("Portal writable worker acceptance marker missing", marker, output.String())
			}
			t.Log(marker)
		}
		for _, marker := range []string{"PORTAL_WRITABLE_BACKGROUND_COMPLETE", "PORTAL_WRITABLE_BACKGROUND_CURRENT_VIEWER", "PORTAL_WRITABLE_CURRENT_DOCUMENT_HTTP", "PORTAL_WRITABLE_CURRENT_DIRECTORY_HTTP", "PORTAL_WRITABLE_DOCUMENT_HTTP_REVOKED", "PORTAL_WRITABLE_PAGE_BINDING_NATIVE", "PORTAL_WRITABLE_BACKEND_SESSION_HTTP", "PORTAL_WRITABLE_BACKEND_HTTP_NO_RETRY"} {
			if !bytes.Contains(output.Bytes(), []byte(marker)) {
				t.Fatal("background completion acceptance missing", marker, output.String())
			}
			t.Log(marker)
		}
		if os.Getenv("APP_SDK_BROWSER_DIR") != "" {
			for _, marker := range []string{"SDK_NATIVE_BROWSER_PAGE_ACTIONS", "SDK_NATIVE_BROWSER_TYPED_QUERY", "SDK_NATIVE_BROWSER_NAVIGATION", "SDK_NATIVE_BROWSER_DRAFT_GUARD", "SDK_NATIVE_BROWSER_DOCUMENT_LAYOUT", "SDK_NATIVE_BROWSER_CURRENT_VIEWER", "SDK_NATIVE_BROWSER_UNCERTAIN_NO_RETRY", "SDK_NATIVE_BROWSER_REVOKED"} {
				if !bytes.Contains(output.Bytes(), []byte(marker)) {
					t.Fatal("SDK native browser assertion missing", marker, output.String())
				}
				t.Log(marker)
			}
		}
		if !useService {
			checkPlatformWritableAutoFixture(t, childData, authority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), native, false)
			// Preserve verified public evidence, then exercise cleanup when the normal
			// completed-controller snapshot is unavailable after an interruption.
			if err := os.Rename(filepath.Join(childData, "writable-auto-start.json"), filepath.Join(childData, "writable-auto-start.verified.json")); err != nil {
				t.Fatal(err)
			}
			t.Log("PORTAL_WRITABLE_BACKGROUND_NATIVE_WITNESS")
			checkPlatformWritableStartFixture(t, filepath.Join(childData, "writable-start.json"), authority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), native, false)
		}
		t.Log("MANAGED_PROFILE_OBSERVED")
		if !bytes.Contains(output.Bytes(), []byte("MANAGED_INSTALL_DATA_READY")) {
			t.Fatal("Portal did not complete managed data stage", output.String())
		}
		t.Log("MANAGED_INSTALL_DATA_READY")
		if !bytes.Contains(output.Bytes(), []byte("MANAGED_INSTALL_CANDIDATE_PROBE")) {
			t.Fatal("Portal did not settle actual native candidate", output.String())
		}
		if !useService {
			candidatePath := filepath.Join(childData, "candidate-command.json")
			nativeID := checkPlatformFixtureCandidate(t, candidatePath, authority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), false)
			message := readPlatformFixtureCandidate(t, candidatePath, authority)
			proof, err := store.ReadCandidateRunningReceipt(ctx, message.Raw, message.Signature)
			if err != nil || nativeID == "" || proof.NativeID != nativeID {
				t.Fatal("Portal candidate lost actual native identity", err)
			}
		}
		t.Log("MANAGED_INSTALL_CANDIDATE_PROBE")
		if os.Getenv("APP_READY_NATIVE_IMAGE") != "" {
			if !bytes.Contains(output.Bytes(), []byte("MANAGED_INSTALL_READINESS")) {
				t.Fatal("Portal did not observe actual backend readiness", output.String())
			}
			t.Log("MANAGED_INSTALL_READINESS")
			if !bytes.Contains(output.Bytes(), []byte("CANDIDATE_SECCOMP_ENTRYPOINT_VERIFIED")) {
				t.Fatal("native backend entrypoint seccomp verification missing")
			}
			t.Log("CANDIDATE_SECCOMP_ENTRYPOINT_VERIFIED")
			if !bytes.Contains(output.Bytes(), []byte("MANAGED_BACKEND_REGISTRATION_MATERIAL")) {
				t.Fatal("Portal did not prepare current managed registration material", output.String())
			}
			t.Log("MANAGED_BACKEND_REGISTRATION_MATERIAL")
			if !bytes.Contains(output.Bytes(), []byte("MANAGED_BACKEND_DELIVERY_COMMITTED")) {
				t.Fatal("Portal did not settle durable delivery")
			}
			t.Log("MANAGED_BACKEND_DELIVERY_COMMITTED")
			if !bytes.Contains(output.Bytes(), []byte("MANAGED_BACKEND_BOOT_VERIFIED")) {
				t.Fatal("Portal did not verify actual backend boot")
			}
			t.Log("MANAGED_BACKEND_BOOT_VERIFIED")
			if !bytes.Contains(output.Bytes(), []byte("MANAGED_BACKEND_REQUEST_RELAY_VERIFIED")) {
				t.Fatal("native backend request relay missing")
			}
			t.Log("MANAGED_BACKEND_REQUEST_RELAY_VERIFIED")
			if !bytes.Contains(output.Bytes(), []byte("MANAGED_CURRENT_VIEWER_REQUEST_VERIFIED")) {
				t.Fatalf("current viewer request verification missing from controller child: %s", output.String())
			}
			t.Log("MANAGED_CURRENT_VIEWER_REQUEST_VERIFIED")
			if !bytes.Contains(output.Bytes(), []byte("MANAGED_BACKEND_REGISTRATION_RPC")) {
				t.Fatal("Portal did not use protected registration RPC")
			}

		}
	}
	if !bytes.Contains(output.Bytes(), []byte("DATA_STAGE_DISPATCHED")) {
		t.Fatal("child did not dispatch data execution", output.String())
	}
	if !bytes.Contains(output.Bytes(), []byte("DATA_STAGE_ACTIVATE")) {
		t.Fatal("child did not prove stage commit", output.String())
	}
	if !bytes.Contains(output.Bytes(), []byte("STATIC_SCHEDULER_COMPLETE")) {
		t.Fatal("child did not automatically schedule queued installation", output.String())
	}
	if !bytes.Contains(output.Bytes(), []byte("STATIC_DRIVER_COMPLETE")) {
		t.Fatal("child did not drive queued installation to completion", output.String())
	}
	if !bytes.Contains(output.Bytes(), []byte("STATIC_ACTIVATION_READY")) {
		t.Fatal("child did not prove activation/launch", output.String())
	}
	if useService {
		t.Log("PORTAL_MANAGED_SERVICE_UI_INSTALL_VERIFIED")
		return
	}
	receipt, err := store.ReadDataPreparationReceipt(ctx, message.Raw, message.Signature)
	if err != nil || !receipt.MatchesCommand(verified.Command()) {
		t.Fatal("dispatched receipt does not match original logical intent", err)
	}
	project, err := strconv.ParseUint(receipt.ProjectID, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := root.Inspect(ctx, pin, verified.Command().Payload.DataNamespaceID, uint32(project))
	if err != nil || strconv.FormatUint(observed.Inode, 10) != receipt.NamespaceInode || observed.ByteLimit != 256<<20 || observed.InodeLimit != 65536 {
		t.Fatal("Portal stage lost native resource", err)
	}
}

type platformServiceFixture struct {
	cmd     *exec.Cmd
	done    chan error
	root    string
	ipcRoot string
	socket  string
	output  bytes.Buffer
	once    sync.Once
}

func startPlatformServiceFixture(t *testing.T, binaryPath, fixtureDir string, deployment journal.Deployment) *platformServiceFixture {
	t.Helper()
	root, err := os.MkdirTemp("/run", "portal-service-")
	if err != nil {
		t.Fatal(err)
	}
	controlRoot := filepath.Join(root, "control")
	ipcRoot := filepath.Join(root, "ipc")
	if err := os.Chmod(root, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(root, 0, 41001); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(controlRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(controlRoot, 0, 41001); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ipcRoot, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(controlRoot, "runner.sock")
	checkpointRoot, err := os.MkdirTemp(filepath.Dir(os.Getenv("APP_DATA_JOURNAL_ROOT")), "service-checkpoints-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(checkpointRoot, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := service.Config{
		Format:         service.ConfigFormat,
		DockerSocket:   os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"),
		JournalDir:     os.Getenv("APP_DATA_JOURNAL_CONTROL"),
		DataRoot:       os.Getenv("APP_DATA_JOURNAL_ROOT"),
		CheckpointRoot: checkpointRoot,
		IPCRoot:        ipcRoot,
		ControlRoot:    controlRoot,
		ControlSocket:  socket,
		ControllerUID:  41001,
		ControllerGID:  41001,
		NetworkActive:  2,
		NetworkEntries: 16,
		MinimumCPUs:    1,
		MinimumMemory:  512 << 20,
		Controller: service.Controller{
			DeploymentID:    deployment.DeploymentID,
			ControllerEpoch: deployment.ControllerEpoch,
			ControlRevision: deployment.ControlRevision,
			Fingerprint:     deployment.Fingerprint,
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(fixtureDir, "service.json")
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	fixture := &platformServiceFixture{cmd: exec.Command(binaryPath, "-test.run=^TestPlatformRunnerServiceChild$", "-test.v", "-test.timeout=10m"), done: make(chan error, 1), root: root, ipcRoot: ipcRoot, socket: socket}
	fixture.cmd.Env = append(os.Environ(), "PORTAL_RUNNER_SERVICE_CHILD=1", "PORTAL_RUNNER_SERVICE_CONFIG="+configPath)
	fixture.cmd.Stdout = &fixture.output
	fixture.cmd.Stderr = &fixture.output
	if err := fixture.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { fixture.done <- fixture.cmd.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Lstat(socket); err == nil {
			return fixture
		}
		select {
		case err := <-fixture.done:
			t.Fatalf("managed service exited before listening: %v\n%s", err, fixture.output.String())
		default:
		}
		if time.Now().After(deadline) {
			_ = fixture.cmd.Process.Kill()
			<-fixture.done
			t.Fatalf("managed service startup deadline\n%s", fixture.output.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (f *platformServiceFixture) stop(t *testing.T) {
	t.Helper()
	f.once.Do(func() {
		if err := f.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Error(err)
		}
		select {
		case err := <-f.done:
			if err != nil {
				t.Errorf("managed service stopped with an error: %v\n%s", err, f.output.String())
			}
		case <-time.After(10 * time.Second):
			_ = f.cmd.Process.Kill()
			<-f.done
			t.Errorf("managed service did not stop")
		}
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
			if _, err := os.Lstat(f.socket); os.IsNotExist(err) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Errorf("managed service socket remained after shutdown")
	})
}

func TestPlatformRunnerServiceChild(t *testing.T) {
	if os.Getenv("PORTAL_RUNNER_SERVICE_CHILD") != "1" {
		t.Skip("isolated managed service child only")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if err := service.Serve(ctx, os.Getenv("PORTAL_RUNNER_SERVICE_CONFIG"), logger); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformInstallDataEndpointChild(t *testing.T) {
	if os.Getenv("PORTAL_DATA_CHILD") != "1" {
		t.Skip("isolated Portal data child only")
	}
	if os.Geteuid() != 41001 {
		t.Fatal("Portal child did not drop root")
	}
	public, ready := os.NewFile(3, "public-command"), os.NewFile(4, "native-ready")
	if public == nil || ready == nil {
		t.Fatal("missing fixture pipes")
	}
	defer public.Close()
	defer ready.Close()
	encoded, err := base64.RawStdEncoding.DecodeString(os.Getenv("PORTAL_DATA_ENROLLMENT"))
	if err != nil {
		t.Fatal(err)
	}
	var enrollment journal.ControllerEnrollment
	if json.Unmarshal(encoded, &enrollment) != nil {
		t.Fatal("missing public enrollment")
	}
	s, session, release := platformInstallFixture(t, false)
	// Existing runtime confirmation has separate cross-UID acceptance. Seed a
	// confirmed binding for this shared public fixture deployment, using real DEK
	// encryption; never enroll this public seed in a production deployment.
	mustExec(t, s.st, `UPDATE meta SET v=? WHERE k='app_platform_controller_epoch'`, enrollment.ControllerEpoch)
	runtime, err := s.preparePlatformRuntimeBinding(session, enrollment.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	seed := bytes.Repeat([]byte{30}, ed25519.SeedSize)
	key := ed25519.NewKeyFromSeed(seed)
	if !bytes.Equal(key[ed25519.SeedSize:], enrollment.PublicKey) {
		t.Fatal("shared public fixture key changed")
	}
	clear(key)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := aesSeal(dek, seed, aad("app-platform", "runtime-control:"+runtime.id))
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=1,revision=2,control_revision=?,verification_key_fingerprint=?,sealed_control_key=? WHERE id=?`, enrollment.ControlRevision, enrollment.Fingerprint, sealPrefix+ciphertext, runtime.id)
	plan, err := s.createPlatformNativeInstallAudienceReview(session, release.sha256, 1, []platformAudienceTarget{{kind: "user", username: session.username}})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := s.consumePlatformInstallReview(session, plan.ID, plan.SHA256, platformFixtureID(870))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.st.claimPlatformOperation(admitted.OperationID, platformFixtureID(871))
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []appplatform.Stage{appplatform.StageValidate, appplatform.StageDownload, appplatform.StageStage, appplatform.StagePrepareData} {
		lease, err = s.st.advancePlatformInstallStaging(lease)
		if err != nil || lease.snapshot.Stage != stage {
			t.Fatal(err)
		}
	}
	intent, err := s.preparePlatformInstallData(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(public).Encode(struct{ Raw, Signature []byte }{intent.raw, intent.signature}); err != nil {
		t.Fatal(err)
	}
	public.Close()
	var signal [1]byte
	if _, err := io.ReadFull(ready, signal[:]); err != nil || signal[0] != 1 {
		t.Fatal("runner endpoint not ready")
	}
	endpoint := platformRuntimeEndpoint{path: os.Getenv("PORTAL_DATA_SOCKET"), controllerUID: 41001, controllerGID: 41001}
	if os.Getenv("PORTAL_MANAGED_PROFILE") == "1" {
		facts, err := s.observePlatformManagedRuntime(context.Background(), session, endpoint)
		if err != nil {
			t.Fatal("Portal authenticated runtime profile", err)
		}
		p := facts.observation.Profile()
		if p.ExecutionReady || p.Platform != "linux/"+goruntime.GOARCH {
			t.Fatal("configuration became readiness or wrong platform")
		}
		var manifest appplatform.Manifest
		raw := scalar[[]byte](t, s.st, `SELECT descriptor FROM app_platform_releases WHERE sha256=?`, release.sha256)
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.Compatibility.RunnerProtocol = ">=1.0.0-0 <2.0.0"
		manifest.Backend = &appplatform.Backend{Kind: "oci", Image: "registry.k8s.io/pause@sha256:" + strings.Repeat("a", 64), Platforms: []string{p.Platform}, Protocol: transport.ManagedBackendProtocol, Resources: appplatform.Resources{MemoryMiB: 256, CPUMillis: 500, PIDs: 64}}
		compatible, err := platformManagedRuntimeCompatibility(manifest, facts, endpoint)
		if err != nil || !compatible.Compatible {
			t.Fatal("managed compatibility lacked authenticated target", err)
		}
		if err := platformInstallRequirements(manifest); err == nil {
			t.Fatal("profile bypassed existing install gate")
		}
		t.Log("MANAGED_PROFILE_OBSERVED")
	}
	next, err := s.dispatchPlatformInstallData(context.Background(), lease, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := transport.PrepareControllerData(context.Background(), endpoint.path, endpoint.controllerUID, endpoint.controllerGID, intent.raw, intent.signature, intent.authority)
	if err != nil || !replayed.Receipt().MatchesCommand(intent.command) {
		t.Fatal("applied dispatch replay missing", err)
	}
	readBack, err := transport.ReadControllerDataReceipt(context.Background(), endpoint.path, endpoint.controllerUID, endpoint.controllerGID, intent.raw, intent.signature, intent.authority)
	if err != nil || replayed.Receipt() != readBack.Receipt() {
		t.Fatal("dispatch replay changed physical receipt", err)
	}
	t.Log("DATA_STAGE_DISPATCHED")
	if next.snapshot.Stage != appplatform.StageActivate || scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "activate" || scalar[string](t, s.st, `SELECT state FROM app_platform_data_namespaces`) != "active" || scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects`) != "applied" {
		t.Fatal("native proof did not commit data/effect/stage")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations WHERE runtime_id IS NOT NULL OR phase<>'pending'`) != 0 {
		t.Fatal("data stage activated app or created backend")
	}
	t.Log("DATA_STAGE_ACTIVATE")
	run, err := s.runPlatformStaticInstall(context.Background(), next, endpoint)
	activated := run.activation
	if err != nil {
		t.Fatal(err)
	}
	if activated.snapshot.Stage != appplatform.StageComplete || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations`) != "ready" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE completed_ms IS NOT NULL AND lease_owner IS NULL AND lease_until_ms IS NULL`) != 1 {
		t.Fatal("static activation did not finish operation")
	}
	access, err := s.inspectPlatformLaunchAccess(session, lease.installationID)
	if err != nil {
		t.Fatal("reviewed audience cannot launch", err)
	}
	document, err := s.readPlatformLaunchDocumentContext(context.Background(), session, access)
	if err != nil || len(document.Content) == 0 || document.DocumentSHA256 == "" {
		t.Fatal("activated document cannot be served", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE principal_type='user' AND username=?`, session.username) != 1 {
		t.Fatal("activation did not apply exact audience")
	}
	t.Log("STATIC_ACTIVATION_READY")
	// A second signed package exercises the complete queued-to-complete driver
	// through the actual root provider, with no manual intermediate advancement.
	raw, _, archive := platformSignedReleaseFixture(t, "1.0.0-beta.1")
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ID = "notes-driver"
	manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	manifest.Capabilities.Required = []string{}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	fullRelease, err := s.stagePlatformRelease(session, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	fullPlan, err := s.createPlatformNativeInstallAudienceReview(session, fullRelease.sha256, 1, []platformAudienceTarget{{kind: "user", username: session.username}})
	if err != nil {
		t.Fatal(err)
	}
	fullAdmitted, err := s.consumePlatformInstallReview(session, fullPlan.ID, fullPlan.SHA256, platformFixtureID(872))
	if err != nil {
		t.Fatal(err)
	}

	scheduler, err := newPlatformInstallScheduler(s, endpoint, platformFixtureID(873), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stopWorker := context.WithTimeout(context.Background(), 10*time.Second)
	workerDone := make(chan error, 1)
	go func() { workerDone <- scheduler.serve(workerCtx) }()
	workerStopped := false
	defer func() {
		stopWorker()
		if !workerStopped {
			if err := <-workerDone; err != nil {
				t.Error(err)
			}
		}
	}()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		var stage string
		if err := s.st.queryRow(`SELECT stage FROM app_platform_operations WHERE id=?`, fullAdmitted.OperationID).Scan(&stage); err != nil {
			t.Fatal(err)
		}
		if stage == "complete" {
			break
		}
		select {
		case err := <-workerDone:
			workerStopped = true
			t.Fatal("background installer stopped before completion", err, scheduler.currentStatus())
		case <-workerCtx.Done():
			t.Fatal("background install timed out", scheduler.currentStatus())
		case <-poll.C:
		}
	}
	stopWorker()
	if err := <-workerDone; err != nil {
		workerStopped = true
		t.Fatal(err)
	}
	workerStopped = true
	t.Log("STATIC_SCHEDULER_COMPLETE")
	fullAccess, err := s.inspectPlatformLaunchAccess(session, fullAdmitted.InstallationID)
	if err != nil {
		t.Fatal("full driver result not launchable", err)
	}
	fullDocument, err := s.readPlatformLaunchDocumentContext(context.Background(), session, fullAccess)
	if err != nil || len(fullDocument.Content) == 0 {
		t.Fatal("full driver document unavailable", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND stage='complete' AND completed_ms IS NOT NULL AND lease_owner IS NULL AND lease_until_ms IS NULL`, fullAdmitted.OperationID) != 1 {
		t.Fatal("full driver retained lease or incomplete state")
	}
	t.Log("STATIC_DRIVER_COMPLETE")
	if os.Getenv("PORTAL_MANAGED_PROFILE") == "1" {
		testNativePortalWritableWriter(t, s, session, endpoint)
		testNativeWritableBackgroundCompletion(t, s, session, endpoint)
		testNativePortalManagedInstallData(t, s, session, endpoint)
	}

}
