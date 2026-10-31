//go:build linux && (amd64 || arm64)

package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
)

func TestNativeWritableHandshakeTransport(t *testing.T) {
	if os.Getenv("APP_WRITABLE_HANDSHAKE_TRANSPORT") != "1" {
		t.Skip("explicit disposable native writable transport only")
	}
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Fatal("requires disposable hosted root")
	}
	ctx := context.Background()
	rootPath, journalPath, socket := os.Getenv("APP_DATA_JOURNAL_ROOT"), os.Getenv("APP_DATA_JOURNAL_CONTROL"), os.Getenv("APP_CANDIDATE_DOCKER_SOCKET")
	if !filepath.IsAbs(rootPath) || !filepath.IsAbs(journalPath) || !filepath.IsAbs(socket) {
		t.Fatal("explicit owned native paths required")
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
	store, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enrollment, err := store.ControllerDeployment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	descriptor := nativeCandidateDescriptor(t)
	var manifest map[string]any
	json.Unmarshal(descriptor, &manifest)
	manifest["capabilities"] = map[string]any{"required": []string{}, "optional": []string{}}
	descriptor, _ = json.Marshal(manifest)
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/run", "portal-wht-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	sockets := filepath.Join(dir, "ipc")
	if err := os.Mkdir(sockets, 0700); err != nil {
		t.Fatal(err)
	}
	native, err := driver.OpenCandidateRuntime(ctx, socket, rootPath, sockets, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	_, prep, _, _ := signedIntentFixture(t)
	now := time.Now().UnixMilli()
	prep.DeploymentID, prep.ControllerEpoch, prep.ControlRevision = a.DeploymentID, a.ControllerEpoch, strconv.FormatInt(a.ControlRevision, 10)
	prep.InstallationID, prep.OperationID, prep.EffectID, prep.TargetGeneration = receiptNativeID(7300), receiptNativeID(7301), receiptNativeID(7302), "1"
	prep.Payload.DataNamespaceID, prep.Payload.ReleaseSHA256, prep.Payload.QuotaBytes, prep.Payload.QuotaInodes, prep.Payload.WriterFence = receiptNativeID(7303), parsed.SHA256, "8388608", "128", "1"
	prep.IssuedMS, prep.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+29000, 10)
	prep.PayloadSHA256, err = control.LogicalDigest(prep)
	if err != nil {
		t.Fatal(err)
	}
	pr, ps, err := control.Sign(prep, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.DispatchPrepareData(ctx, pr, ps, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(prepared)
	sum := sha256.Sum256(encoded)
	w := control.DataWriterCommand{Format: control.DataWriterFormat, EffectID: receiptNativeID(7304), DataRaw: pr, DataSignature: ps, DataReceiptSHA256: hex.EncodeToString(sum[:]), ResourceID: receiptNativeID(7305), Platform: "linux/" + runtime.GOARCH, SandboxProfile: control.DataWriterSandboxProfile}
	w.PayloadSHA256, err = control.DataWriterLogicalDigest(w)
	if err != nil {
		t.Fatal(err)
	}
	wr, ws, err := control.SignDataWriter(w, key, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitDataWriterCommand(ctx, wr, ws); err != nil {
		t.Fatal(err)
	}
	wc, err := store.ClaimDataWriter(ctx, wr, ws)
	if err != nil {
		t.Fatal(err)
	}
	written, err := store.ExecuteDataWriter(ctx, wc, descriptor, native)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(written)
	sum = sha256.Sum256(encoded)
	start := control.WritableCandidateCommand{Format: control.WritableCandidateFormat, EffectID: receiptNativeID(7306), WriterRaw: wr, WriterSignature: ws, WriterReceiptSHA256: hex.EncodeToString(sum[:]), SandboxProfile: control.WritableCandidateSandboxProfile}
	start.PayloadSHA256, err = control.WritableCandidateLogicalDigest(start)
	if err != nil {
		t.Fatal(err)
	}
	sr, ss, err := control.SignWritableCandidate(start, key, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitWritableCandidateCommand(ctx, sr, ss); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveWritableCandidate(ctx, sr, ss); err != nil {
		t.Fatal(err)
	}
	sc, err := store.ClaimWritableCandidate(ctx, sr, ss)
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.ExecuteWritableCandidate(ctx, sc, native, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	// Strictly inspect actual owned identity before the fixture can stop/remove.
	defer func() {
		raw, err := exec.Command("docker", "--host", "unix://"+socket, "inspect", running.NativeID).Output()
		if err != nil {
			t.Error(err)
			return
		}
		var inspected []struct {
			ID, Name, Image string
			Config          struct{ Labels map[string]string }
		}
		if json.Unmarshal(raw, &inspected) != nil || len(inspected) != 1 {
			t.Error("ambiguous owned native fixture")
			return
		}
		v := inspected[0]
		if v.ID != running.NativeID || v.Name != "/portal-app-"+w.ResourceID || v.Image != written.Native.ImageID || v.Config.Labels["org.portal.app.resource"] != w.ResourceID || v.Config.Labels["org.portal.app.installation"] != prep.InstallationID || v.Config.Labels["org.portal.app.start-effect"] != start.EffectID || v.Config.Labels["org.portal.app.start-payload"] != start.PayloadSHA256 {
			t.Error("refuse foreign fixture cleanup")
			return
		}
		for _, args := range [][]string{{"stop", "--time", "10", running.NativeID}, {"rm", running.NativeID}} {
			if out, err := exec.Command("docker", append([]string{"--host", "unix://" + socket}, args...)...).CombinedOutput(); err != nil {
				t.Errorf("owned cleanup %v %s", err, out)
				return
			}
		}
		identity := driver.CandidateIdentity{DeploymentID: prep.DeploymentID, ControllerEpoch: prep.ControllerEpoch, InstallationID: prep.InstallationID, OperationID: prep.OperationID, ResourceID: w.ResourceID, NamespaceID: prep.Payload.DataNamespaceID, ReleaseSHA256: parsed.SHA256, Generation: "1", ControlFence: prep.InstallationFence, WriterFence: "1"}
		if err := native.ReleaseCandidateHTTPStorage(ctx, identity); err != nil {
			t.Error(err)
		}
	}()
	registration, err := appplatform.NewBackendRegistration(appplatform.BackendIdentity{AppID: parsed.Manifest.ID, DeploymentID: prep.DeploymentID, ControllerEpoch: prep.ControllerEpoch, InstallationID: prep.InstallationID, Generation: "1", PolicyRevision: "1", ReleaseSHA256: parsed.SHA256}, bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	document, err := registration.Document()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(document)
	encoded, _ = json.Marshal(running)
	sourceSum := sha256.Sum256(encoded)
	documentSum := sha256.Sum256(document)
	reg := control.WritableBackendRegistrationCommand{Format: control.WritableBackendRegistrationFormat, EffectID: receiptNativeID(7307), CandidateRaw: sr, CandidateSignature: ss, RunningReceiptSHA256: hex.EncodeToString(sourceSum[:]), DocumentSHA256: hex.EncodeToString(documentSum[:])}
	reg.PayloadSHA256, err = control.WritableBackendRegistrationLogicalDigest(reg)
	if err != nil {
		t.Fatal(err)
	}
	rr, rs, err := control.SignWritableBackendRegistration(reg, key, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	// Native startup may still be doing its independent filtered quota probe.
	var delivered journal.WritableBackendRegistrationReceipt
	deadline := time.Now().Add(5 * time.Second)
	for {
		delivered, err = store.DispatchWritableBackendRegistration(ctx, rr, rs, descriptor, document, native)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual delivery", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	signer, err := registration.Signer("")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	challenge, err := signer.NewChallenge(time.Now())
	if err != nil {
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
	binaryPath := filepath.Join(dir, "child.test")
	target, err := os.OpenFile(binaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0555)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("copy child binary")
	}
	endpoint := filepath.Join(dir, "control")
	if err := os.Mkdir(endpoint, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(endpoint, 0, int(controllerTestGID)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(endpoint, "w.sock")
	server, err := ListenCandidateController(ctx, path, controllerTestUID, controllerTestGID, store, native)
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
	aj, _ := json.Marshal(a)
	dj, _ := json.Marshal(delivered)
	encode := base64.RawStdEncoding.EncodeToString
	run := func(uid, gid uint32, action string) string {
		t.Helper()
		childCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		cmd := exec.CommandContext(childCtx, binaryPath, "-test.run=^TestNativeWritableHandshakeChild$", "-test.timeout=8s", "-test.v")
		cmd.Env = []string{"GOMAXPROCS=2", "WRITABLE_HANDSHAKE_CHILD=" + action, "WH_SOCKET=" + path, "WH_COMMAND=" + encode(rr), "WH_SIGNATURE=" + encode(rs), "WH_AUTHORITY=" + encode(aj), "WH_DESCRIPTOR=" + encode(descriptor), "WH_DELIVERY=" + encode(dj), "WH_CHALLENGE=" + challenge}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{gid}}}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("writable controller child %s: %v %s", action, err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "WRITABLE_ANSWER=") {
				return strings.TrimPrefix(line, "WRITABLE_ANSWER=")
			}
		}
		return ""
	}
	answer := run(controllerTestUID, controllerTestGID, "accepted")
	boot, err := signer.VerifyAnswer(challenge, answer, time.Now())
	if err != nil || boot == "" {
		t.Fatal("cross-UID answer MAC/boot", err)
	}
	run(41002, controllerTestGID, "refused")
	run(controllerTestUID, 41003, "refused")
	t.Log("NATIVE_WRITABLE_HANDSHAKE_CROSS_UID_VERIFIED")
	t.Log("NATIVE_WRITABLE_HANDSHAKE_CONTROLLER_PEER_DENIAL_VERIFIED")
}
func TestNativeWritableHandshakeChild(t *testing.T) {
	action := os.Getenv("WRITABLE_HANDSHAKE_CHILD")
	if action == "" {
		t.Skip("explicit child only")
	}
	decode := func(name string) []byte {
		raw, err := base64.RawStdEncoding.DecodeString(os.Getenv(name))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var a control.Authority
	json.Unmarshal(decode("WH_AUTHORITY"), &a)
	var expected journal.WritableBackendRegistrationReceipt
	json.Unmarshal(decode("WH_DELIVERY"), &expected)
	proof, err := HandshakeWritableControllerBackend(context.Background(), os.Getenv("WH_SOCKET"), controllerTestUID, controllerTestGID, decode("WH_COMMAND"), decode("WH_SIGNATURE"), a, decode("WH_DESCRIPTOR"), expected, os.Getenv("WH_CHALLENGE"))
	if action == "refused" && uint32(os.Geteuid()) != controllerTestUID {
		// Bypass the public client's UID refusal only inside the test so the
		// real root listener must enforce SO_PEERCRED before reading a frame.
		proof, err = handshakeWritableBackendFrame(context.Background(), os.Getenv("WH_SOCKET"), controllerProfile(controllerTestGID), decode("WH_COMMAND"), decode("WH_SIGNATURE"), a, decode("WH_DESCRIPTOR"), expected, os.Getenv("WH_CHALLENGE"))
	}
	if action == "refused" {
		if err == nil || !proof.ReceivedAt().IsZero() {
			t.Fatal("unapproved controller peer admitted")
		}
		return
	}
	if err != nil || proof.ReceivedAt().IsZero() || !proof.Endpoint().RootController || proof.Endpoint().RunnerUID != 0 || proof.Endpoint().ControllerUID != controllerTestUID || proof.Evidence().NativeID != expected.Claim.NativeID {
		t.Fatal("actual root writable handshake", err)
	}
	// Answer is a short-lived signed proof, not the private key. Only the isolated
	// root parent consumes this test marker to independently verify its MAC/boot.
	println("WRITABLE_ANSWER=" + proof.Evidence().Answer)
}
