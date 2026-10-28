//go:build linux && (amd64 || arm64)

package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func receiptNativeID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
func receiptNativeCommand(t *testing.T, e journal.ControllerEnrollment, slot int) ([]byte, []byte) {
	t.Helper()
	_, c, _, _ := signedIntentFixture(t)
	c.DeploymentID, c.ControllerEpoch, c.ControlRevision = e.DeploymentID, e.ControllerEpoch, strconv.FormatInt(e.ControlRevision, 10)
	c.InstallationID, c.OperationID, c.EffectID, c.Payload.DataNamespaceID = receiptNativeID(6000+slot*10), receiptNativeID(6001+slot*10), receiptNativeID(6002+slot*10), receiptNativeID(6003+slot*10)
	c.Payload.QuotaBytes, c.Payload.QuotaInodes = "8388608", "128"
	var err error
	c.PayloadSHA256, err = control.LogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	framed := append([]byte{}, []byte("portal.app/runner-command/v1\x00")...)
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(raw)))
	framed = append(framed, length[:]...)
	framed = append(framed, raw...)
	return raw, ed25519.Sign(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), framed)
}

// Reads the same owned journal/data produced by the native execution fixture.
// It does not create a second journal, enroll keys or reserve overlapping projects.
func TestNativeReceiptTransport(t *testing.T) {
	fixtureRoot := os.Getenv("APP_DATA_RECEIPT_FIXTURE")
	if fixtureRoot == "" {
		t.Skip("explicit disposable receipt transport fixture only")
	}
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || !filepath.IsAbs(fixtureRoot) {
		t.Fatal("requires disposable Linux root")
	}
	rootPath, journalPath := os.Getenv("APP_DATA_JOURNAL_ROOT"), os.Getenv("APP_DATA_JOURNAL_CONTROL")
	if !filepath.IsAbs(rootPath) || !filepath.IsAbs(journalPath) {
		t.Fatal("missing owned native fixture")
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
	deployment, err := store.ControllerDeployment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slot := 1
	if os.Getenv("APP_DATA_NATIVE_PHASE") == "recovered" {
		slot = 4
	}
	raw, sig := receiptNativeCommand(t, deployment.ControllerEnrollment, slot)
	expected, err := store.ReadDataPreparationReceipt(context.Background(), raw, sig)
	if err != nil {
		t.Fatal("actual native receipt read", err)
	}
	observed, err := root.Inspect(context.Background(), pin, receiptNativeID(6003+slot*10), uint32(slot))
	if err != nil || expected.NamespaceInode != strconv.FormatUint(observed.Inode, 10) {
		t.Fatal("receipt/native inode differs", err)
	}
	dir, err := os.MkdirTemp(fixtureRoot, "channel-")
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
	binaryPath := filepath.Join(dir, "receipt.test")
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
	if err := os.Chown(endpoint, 0, int(controllerTestGID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(endpoint, 0750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(endpoint, "receipt.sock")
	server, err := ListenController(path, controllerTestUID, controllerTestGID, store)
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
	authority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
	authorityJSON, _ := json.Marshal(authority)
	run := func(uid, gid uint32, action string, command, signature []byte) {
		t.Helper()
		childCtx, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		cmd := exec.CommandContext(childCtx, binaryPath, "-test.run=^TestNativeReceiptChild$", "-test.timeout=15s")
		cmd.Env = []string{"GOMAXPROCS=2", "APP_RECEIPT_CHILD=" + action, "RECEIPT_SOCKET=" + path, "RECEIPT_COMMAND=" + base64.RawStdEncoding.EncodeToString(command), "RECEIPT_SIGNATURE=" + base64.RawStdEncoding.EncodeToString(signature), "RECEIPT_AUTHORITY=" + base64.RawStdEncoding.EncodeToString(authorityJSON), "RECEIPT_INODE=" + expected.NamespaceInode, "RECEIPT_FORMAT=" + expected.Format, "RECEIPT_JOURNAL=" + filepath.Join(journalPath, "journal.sqlite3")}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{gid}}}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("receipt child %s %d:%d: %v\n%s", action, uid, gid, err, out)
		}
	}
	run(controllerTestUID, controllerTestGID, "accepted", raw, sig)
	run(41002, controllerTestGID, "peer-refused", raw, sig)
	run(controllerTestUID, 41003, "refused", raw, sig)
	run(controllerTestUID, controllerTestGID, "server-signature-refused", raw, make([]byte, 64))
	unknownRaw, unknownSig := receiptNativeCommand(t, deployment.ControllerEnrollment, 2)
	run(controllerTestUID, controllerTestGID, "refused", unknownRaw, unknownSig)
}
func TestNativeReceiptChild(t *testing.T) {
	action := os.Getenv("APP_RECEIPT_CHILD")
	if action == "" {
		t.Skip("receipt subprocess only")
	}
	raw, err := base64.RawStdEncoding.DecodeString(os.Getenv("RECEIPT_COMMAND"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.RawStdEncoding.DecodeString(os.Getenv("RECEIPT_SIGNATURE"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := base64.RawStdEncoding.DecodeString(os.Getenv("RECEIPT_AUTHORITY"))
	if err != nil {
		t.Fatal(err)
	}
	var authority control.Authority
	if json.Unmarshal(encoded, &authority) != nil {
		t.Fatal("missing fixture authority")
	}
	path := os.Getenv("RECEIPT_SOCKET")
	if action == "server-signature-refused" {
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		frame := make([]byte, 44)
		copy(frame, dataReceiptQueryMagic)
		binary.BigEndian.PutUint32(frame[40:44], uint32(len(raw)))
		frame = append(frame, raw...)
		frame = append(frame, sig...)
		if n, err := io.Copy(conn, bytes.NewReader(frame)); err != nil || n != int64(len(frame)) {
			t.Fatal(err)
		}
		conn.CloseWrite()
		var b [1]byte
		if n, err := conn.Read(b[:]); n != 0 || err == nil {
			t.Fatal("server disclosed receipt for unsigned request")
		}
		return
	}
	var proof DataReceiptObservation
	if os.Getenv("RECEIPT_OPERATION") == "prepare" {
		proof, err = PrepareControllerData(context.Background(), path, uint32(os.Geteuid()), controllerTestGID, raw, sig, authority)
	} else {
		proof, err = ReadControllerDataReceipt(context.Background(), path, uint32(os.Geteuid()), controllerTestGID, raw, sig, authority)
	}
	if action != "accepted" {
		if err == nil || !proof.ReceivedAt().IsZero() {
			t.Fatal("unauthorized receipt received")
		}
		return
	}
	if err != nil || proof.Receipt().NamespaceInode == "" || (os.Getenv("RECEIPT_INODE") != "" && proof.Receipt().NamespaceInode != os.Getenv("RECEIPT_INODE")) || proof.Receipt().Format != os.Getenv("RECEIPT_FORMAT") {
		t.Fatal("cross-UID actual receipt unavailable", err)
	}
	if endpoint := proof.Endpoint(); !endpoint.RootController || endpoint.RunnerUID != 0 || endpoint.ControllerUID != controllerTestUID || endpoint.ControllerGID != controllerTestGID || endpoint.Path != path {
		t.Fatal("root/controller profile not bound to observation")
	}
	file, err := os.Open(os.Getenv("RECEIPT_JOURNAL"))
	if err == nil {
		file.Close()
		t.Fatal("controller could read runner journal directly")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatal("journal denial not DAC", err)
	}
}
