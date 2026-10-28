//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
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
	path := filepath.Join(endpoint, "data.sock")
	var server *transport.Server
	var native *driver.CandidateRuntime
	var managedSocketRoot string
	managed := os.Getenv("APP_CANDIDATE_DOCKER_SOCKET") != ""
	if managed {
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
		server, err = transport.ListenManagedController(context.Background(), path, 41001, 41001, store, root, pin, native)
	} else {
		server, err = transport.ListenDataController(context.Background(), path, 41001, 41001, store, root, pin)
	}
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
	child := exec.Command(binaryPath, "-test.run=^TestPlatformInstallDataEndpointChild$", "-test.v", "-test.timeout=30s")
	child.Dir = work
	child.Env = []string{"GOMAXPROCS=2", "PORTAL_DATA_CHILD=1", "PORTAL_DATA_SOCKET=" + path, "PORTAL_DATA_ENROLLMENT=" + base64.RawStdEncoding.EncodeToString(enrollment), "TMPDIR=" + childData}
	if managed {
		child.Env = append(child.Env, "PORTAL_MANAGED_PROFILE=1", "PORTAL_MANAGED_IMAGE="+os.Getenv("APP_CANDIDATE_NATIVE_IMAGE"))
		if os.Getenv("APP_READY_NATIVE_IMAGE") != "" {
			child.Env = append(child.Env, "PORTAL_MANAGED_READY=1")
		}
		fixtureAuthority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
		defer checkPlatformFixtureCandidate(t, filepath.Join(childData, "candidate-command.json"), fixtureAuthority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), true)
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
		checkRegistrationRPC = observeNativeRegistrationRPCFixture(t, ctx, registrationReader, readyWriter, managedSocketRoot, authority, store)
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
		t.Log("MANAGED_PROFILE_OBSERVED")
		if !bytes.Contains(output.Bytes(), []byte("MANAGED_INSTALL_DATA_READY")) {
			t.Fatal("Portal did not complete managed data stage", output.String())
		}
		t.Log("MANAGED_INSTALL_DATA_READY")
		if !bytes.Contains(output.Bytes(), []byte("MANAGED_INSTALL_CANDIDATE_PROBE")) {
			t.Fatal("Portal did not settle actual native candidate", output.String())
		}
		candidatePath := filepath.Join(childData, "candidate-command.json")
		nativeID := checkPlatformFixtureCandidate(t, candidatePath, authority, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), false)
		message := readPlatformFixtureCandidate(t, candidatePath, authority)
		proof, err := store.ReadCandidateRunningReceipt(ctx, message.Raw, message.Signature)
		if err != nil || nativeID == "" || proof.NativeID != nativeID {
			t.Fatal("Portal candidate lost actual native identity", err)
		}
		t.Log("MANAGED_INSTALL_CANDIDATE_PROBE")
		if os.Getenv("APP_READY_NATIVE_IMAGE") != "" {
			if !bytes.Contains(output.Bytes(), []byte("MANAGED_INSTALL_READINESS")) {
				t.Fatal("Portal did not observe actual backend readiness", output.String())
			}
			t.Log("MANAGED_INSTALL_READINESS")
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
		testNativePortalManagedInstallData(t, s, session, endpoint)
	}

}
