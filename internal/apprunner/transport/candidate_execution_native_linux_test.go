//go:build linux && (amd64 || arm64)

package transport

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

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func executionFixtureDocker(t *testing.T, socket string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket}, args...)...).CombinedOutput()
	if err != nil || len(out) > 65536 {
		t.Fatal("owned fixture Docker command failed", err)
	}
	return out
}
func executionFixtureOwnedID(t *testing.T, socket string, c control.CandidateCommand) string {
	t.Helper()
	ids := bytes.Fields(executionFixtureDocker(t, socket, "container", "ls", "-a", "--no-trunc", "--filter", "label=org.portal.app.resource="+c.Payload.ResourceID, "--format", "{{.ID}}"))
	if len(ids) == 0 {
		return ""
	}
	if len(ids) != 1 || len(ids[0]) != 64 {
		t.Fatal("ambiguous owned candidate")
	}
	id := string(ids[0])
	var found []struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(executionFixtureDocker(t, socket, "container", "inspect", id), &found); err != nil || len(found) != 1 || found[0].ID != id {
		t.Fatal("owned native inspection mismatch", err)
	}
	for name, value := range map[string]string{"format": "candidate/1-dev.1", "deployment": c.DeploymentID, "epoch": c.ControllerEpoch, "installation": c.InstallationID, "operation": c.OperationID, "resource": c.Payload.ResourceID, "namespace": c.Payload.DataNamespaceID, "release": c.Payload.ReleaseSHA256, "generation": c.TargetGeneration, "control-fence": c.InstallationFence, "writer-fence": c.Payload.WriterFence} {
		if found[0].Config.Labels["org.portal.app."+name] != value {
			t.Fatal("refusing changed fixture ownership", name)
		}
	}
	return id
}
func TestNativeCandidateExecutionTransport(t *testing.T) {
	fixtureRoot := os.Getenv("APP_CANDIDATE_EXECUTION_FIXTURE")
	if fixtureRoot == "" {
		t.Skip("explicit disposable native execution channel fixture only")
	}
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || !filepath.IsAbs(fixtureRoot) {
		t.Fatal("requires explicit disposable root")
	}
	ctx := context.Background()
	rootPath, journalPath, socket := os.Getenv("APP_DATA_JOURNAL_ROOT"), os.Getenv("APP_DATA_JOURNAL_CONTROL"), os.Getenv("APP_CANDIDATE_DOCKER_SOCKET")
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
	store, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deployment, err := store.ControllerDeployment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := nativeCandidateDescriptor(t)
	raw, sig := candidateNativeReceiptCommand(t, deployment.ControllerEnrollment, 2)
	var c control.CandidateCommand
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	_, prepare, _, _ := signedIntentFixture(t)
	prepare.DeploymentID, prepare.ControllerEpoch, prepare.ControlRevision = c.DeploymentID, c.ControllerEpoch, c.ControlRevision
	prepare.InstallationID, prepare.OperationID, prepare.EffectID, prepare.TargetGeneration = c.InstallationID, c.OperationID, receiptNativeID(7122), c.TargetGeneration
	prepare.Payload.DataNamespaceID, prepare.Payload.ReleaseSHA256, prepare.Payload.QuotaBytes, prepare.Payload.QuotaInodes = c.Payload.DataNamespaceID, c.Payload.ReleaseSHA256, "8388608", "128"
	prepare.PayloadSHA256, err = control.LogicalDigest(prepare)
	if err != nil {
		t.Fatal(err)
	}
	authority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	prepareRaw, prepareSig, err := control.Sign(prepare, key, authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	var proof journal.DataPreparationReceipt
	if _, err := os.Lstat(filepath.Join(rootPath, prepare.Payload.DataNamespaceID)); !os.IsNotExist(err) {
		t.Fatal("fresh managed namespace already exists", err)
	}
	sockets, err := os.MkdirTemp("/run", "portal-cex-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	for _, name := range []string{"http", "broker"} {
		path := filepath.Join(sockets, c.InstallationID, c.TargetGeneration, name)
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
	native, err := driver.OpenCandidateRuntime(ctx, socket, rootPath, sockets, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	dir, err := os.MkdirTemp(fixtureRoot, "exec-")
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
	binaryPath := filepath.Join(dir, "client.test")
	target, err := os.OpenFile(binaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0555)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Chmod(0555); err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("fixture executable copy")
	}
	endpoint := filepath.Join(dir, "control")
	if err := os.Mkdir(endpoint, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(endpoint, 0, int(controllerTestGID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(endpoint, 0750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(endpoint, "control.sock")
	// These refuse before creating any endpoint, even with a real configured runtime.
	badPin := pin
	badPin.Inode++
	for _, configuration := range []struct {
		root *data.Root
		pin  data.RootIdentity
	}{{root, badPin}, {&data.Root{}, pin}} {
		rejected, err := ListenManagedController(ctx, path, controllerTestUID, controllerTestGID, store, configuration.root, configuration.pin, native)
		if err == nil {
			rejected.Close()
			t.Fatal("mismatched provider joined runtime")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("mismatched provider created endpoint", err)
		}
	}
	server, err := ListenManagedController(ctx, path, controllerTestUID, controllerTestGID, store, root, pin, native)
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Serve(serveCtx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	defer func() {
		if id := executionFixtureOwnedID(t, socket, c); id != "" {
			executionFixtureDocker(t, socket, "container", "stop", "--time", "10", id)
			executionFixtureDocker(t, socket, "container", "rm", id)
		}
	}()
	authorityJSON, _ := json.Marshal(authority)
	runData := func() {
		t.Helper()
		childCtx, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		cmd := exec.CommandContext(childCtx, binaryPath, "-test.run=^TestNativeReceiptChild$", "-test.timeout=15s")
		cmd.Env = []string{"GOMAXPROCS=2", "APP_RECEIPT_CHILD=accepted", "RECEIPT_OPERATION=prepare", "RECEIPT_SOCKET=" + path, "RECEIPT_COMMAND=" + base64.RawStdEncoding.EncodeToString(prepareRaw), "RECEIPT_SIGNATURE=" + base64.RawStdEncoding.EncodeToString(prepareSig), "RECEIPT_AUTHORITY=" + base64.RawStdEncoding.EncodeToString(authorityJSON), "RECEIPT_INODE=" + proof.NamespaceInode, "RECEIPT_FORMAT=portal.runner.prepare-data-receipt/1-dev.1", "RECEIPT_JOURNAL=" + filepath.Join(journalPath, "journal.sqlite3")}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: controllerTestUID, Gid: controllerTestGID, Groups: []uint32{controllerTestGID}}}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("unified data preparation: %v\n%s", err, out)
		}
	}
	runData()
	proof, err = store.ReadDataPreparationReceipt(ctx, prepareRaw, prepareSig)
	if err != nil {
		t.Fatal("cross-UID data receipt missing", err)
	}
	project, err := strconv.ParseUint(proof.ProjectID, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	before, err := root.Inspect(ctx, pin, prepare.Payload.DataNamespaceID, uint32(project))
	if err != nil || proof.NamespaceInode != strconv.FormatUint(before.Inode, 10) {
		t.Fatal("unified data receipt not native", err)
	}
	runData()
	after, err := root.Inspect(ctx, pin, prepare.Payload.DataNamespaceID, uint32(project))
	if err != nil || before != after {
		t.Fatal("unified data replay changed namespace", err)
	}
	run := func(uid, gid uint32, action, operation string, command, signature []byte) {
		t.Helper()
		childCtx, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		cmd := exec.CommandContext(childCtx, binaryPath, "-test.run=^TestNativeCandidateReceiptChild$", "-test.timeout=15s")
		cmd.Env = []string{"GOMAXPROCS=2", "APP_CANDIDATE_RECEIPT_CHILD=" + action, "RECEIPT_OPERATION=" + operation, "RECEIPT_DESCRIPTOR=" + base64.RawStdEncoding.EncodeToString(descriptor), "RECEIPT_SOCKET=" + path, "RECEIPT_COMMAND=" + base64.RawStdEncoding.EncodeToString(command), "RECEIPT_SIGNATURE=" + base64.RawStdEncoding.EncodeToString(signature), "RECEIPT_AUTHORITY=" + base64.RawStdEncoding.EncodeToString(authorityJSON), "RECEIPT_INODE=" + proof.NamespaceInode, "RECEIPT_FORMAT=portal.runner.candidate-running-receipt/1-dev.1", "RECEIPT_JOURNAL=" + filepath.Join(journalPath, "journal.sqlite3")}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{gid}}}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("execution child %s: %v\n%s", action, err, out)
		}
	}
	run(41002, controllerTestGID, "refused", "start", raw, sig)
	run(controllerTestUID, 41003, "refused", "start", raw, sig)
	run(controllerTestUID, controllerTestGID, "server-signature-refused", "start", raw, make([]byte, 64))
	if executionFixtureOwnedID(t, socket, c) != "" {
		t.Fatal("refused request created native effect")
	}
	run(controllerTestUID, controllerTestGID, "accepted", "start", raw, sig)
	receipt, err := store.ReadCandidateRunningReceipt(ctx, raw, sig)
	if err != nil || receipt.NativeID == "" || receipt.NativeID != executionFixtureOwnedID(t, socket, c) {
		t.Fatal("cross-UID execution lacked actual native receipt", err)
	}
	run(controllerTestUID, controllerTestGID, "accepted", "start", raw, sig)
	run(controllerTestUID, controllerTestGID, "accepted", "read", raw, sig)
	again, err := store.ReadCandidateRunningReceipt(ctx, raw, sig)
	if err != nil || !equalCandidateMetadata(again, receipt) || executionFixtureOwnedID(t, socket, c) != receipt.NativeID {
		t.Fatal("execution replay changed actual native observation", err)
	}
	unknown, unknownSig := candidateNativeReceiptCommand(t, deployment.ControllerEnrollment, 1)
	run(controllerTestUID, controllerTestGID, "refused", "start", unknown, unknownSig)
	var unknownCommand control.CandidateCommand
	if err := json.Unmarshal(unknown, &unknownCommand); err != nil {
		t.Fatal(err)
	}
	if id := executionFixtureOwnedID(t, socket, unknownCommand); id != "" {
		executionFixtureDocker(t, socket, "container", "stop", "--time", "10", id)
		executionFixtureDocker(t, socket, "container", "rm", id)
		t.Fatal("unknown candidate was physically recreated")
	}
}
