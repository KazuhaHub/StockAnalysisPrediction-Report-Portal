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
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
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
	server, err := transport.ListenController(path, 41001, 41001, store)
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
	enrollment, _ := json.Marshal(deployment.ControllerEnrollment)
	child := exec.Command(binaryPath, "-test.run=^TestPlatformInstallDataEndpointChild$", "-test.v", "-test.timeout=30s")
	child.Dir = work
	child.Env = []string{"GOMAXPROCS=2", "PORTAL_DATA_CHILD=1", "PORTAL_DATA_SOCKET=" + path, "PORTAL_DATA_ENROLLMENT=" + base64.RawStdEncoding.EncodeToString(enrollment), "TMPDIR=" + childData}
	child.ExtraFiles = []*os.File{publicWriter, readyReader}
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
	if _, err := store.AdmitControlCommand(ctx, message.Raw, message.Signature); err != nil {
		t.Fatal(err)
	}
	reservation, err := store.ReservePrepareData(ctx, message.Raw, message.Signature)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimPrepareData(ctx, message.Raw, message.Signature)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.ExecutePrepareData(ctx, claim, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.MatchesCommand(verified.Command()) {
		t.Fatal("Portal signed intent/native receipt differ")
	}
	if _, err := readyWriter.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	readyWriter.Close()
	if err := child.Wait(); err != nil {
		t.Fatalf("Portal data endpoint failed: %v\n%s", err, output.String())
	}
	if !bytes.Contains(output.Bytes(), []byte("DATA_STAGE_ACTIVATE")) {
		t.Fatal("child did not prove stage commit", output.String())
	}
	observed, err := root.Inspect(ctx, pin, reservation.NamespaceID, reservation.ProjectID)
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
	plan, err := s.createPlatformInstallReview(session, release.sha256, 1)
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
		t.Fatal("native preparation not ready")
	}
	endpoint := platformRuntimeEndpoint{path: os.Getenv("PORTAL_DATA_SOCKET"), controllerUID: 41001, controllerGID: 41001}
	next, err := s.completePlatformInstallData(context.Background(), lease, intent, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if next.snapshot.Stage != appplatform.StageActivate || scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "activate" || scalar[string](t, s.st, `SELECT state FROM app_platform_data_namespaces`) != "active" || scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects`) != "applied" {
		t.Fatal("native proof did not commit data/effect/stage")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations WHERE runtime_id IS NOT NULL OR phase<>'pending'`) != 0 {
		t.Fatal("data stage activated app or created backend")
	}
	t.Log("DATA_STAGE_ACTIVATE")
}
