//go:build linux

package transport

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const controllerTestUID uint32 = 41001
const controllerTestGID uint32 = 41001

// This test is opt-in and root-only on a disposable hosted Linux worker. It never
// creates system accounts, changes a Docker socket, or touches existing journals.
func TestControllerIdentityAcceptance(t *testing.T) {
	if os.Getenv("PORTAL_APP_RUNNER_IDENTITY_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable Linux identity acceptance only")
	}
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Fatal("identity acceptance requires root on disposable hosted Linux")
	}
	root, err := os.MkdirTemp("/tmp", "runner-identity-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "identity.test")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(binary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0555)
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
		t.Fatal("copy fixture executable failed")
	}
	endpoint := filepath.Join(root, "control")
	if err := os.Mkdir(endpoint, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(endpoint, 0, int(controllerTestGID)); err != nil {
		t.Fatal(err)
	}
	// Reapply exact mode after chown; do not rely on the host process umask.
	if err := os.Chmod(endpoint, 0750); err != nil {
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
	enrollment, _, raw, sig := signedIntentFixture(t)
	if _, _, err := store.EnrollController(context.Background(), enrollment); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(endpoint, "control.sock")

	for _, mode := range []os.FileMode{0700, 0755, 0770} {
		if err := os.Chmod(endpoint, mode); err != nil {
			t.Fatal(err)
		}
		if unexpected, err := ListenController(path, controllerTestUID, controllerTestGID, store); err == nil {
			unexpected.Close()
			t.Fatal("unsafe controller directory mode accepted")
		}
	}
	if err := os.Chmod(endpoint, 0750); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][2]int{{int(controllerTestUID), int(controllerTestGID)}, {0, 41002}} {
		if err := os.Chown(endpoint, ids[0], ids[1]); err != nil {
			t.Fatal(err)
		}
		if unexpected, err := ListenController(path, controllerTestUID, controllerTestGID, store); err == nil {
			unexpected.Close()
			t.Fatal("foreign controller directory owner/group accepted")
		}
	}
	if err := os.Chown(endpoint, 0, int(controllerTestGID)); err != nil {
		t.Fatal(err)
	}
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
	child := func(uid, gid uint32, action, path string, signature []byte) *exec.Cmd {
		cmd := exec.Command(binary, "-test.run=^TestControllerIdentityChild$", "-test.timeout=15s")
		cmd.Env = []string{"GOMAXPROCS=2", "PORTAL_APP_RUNNER_IDENTITY_CHILD=" + action, "IDENTITY_SOCKET=" + path, "IDENTITY_COMMAND=" + base64.RawStdEncoding.EncodeToString(raw), "IDENTITY_SIGNATURE=" + base64.RawStdEncoding.EncodeToString(signature)}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{gid}}}
		return cmd
	}
	run := func(uid, gid uint32, action string, signature []byte) {
		t.Helper()
		cmd := child(uid, gid, action, path, signature)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("identity child %s (%d:%d): %v\n%s", action, uid, gid, err, out)
		}
	}
	// A group member holding a real signed request still cannot admit it as Portal.
	run(41002, controllerTestGID, "peer-refused", sig)
	// Both an unrelated user and the right UID without the endpoint group must be
	// denied by filesystem permissions before any stream can be opened.
	run(41003, 41003, "dac-refused", sig)
	run(controllerTestUID, 41003, "dac-refused", sig)
	run(controllerTestUID, controllerTestGID, "signature-refused", make([]byte, 64))
	run(controllerTestUID, controllerTestGID, "accepted", sig)
	result, err := store.AdmitControlCommand(context.Background(), raw, sig)
	if err != nil || !result.Replayed || result.State != "prepared" {
		t.Fatalf("cross-UID intent not persisted: %+v %v", result, err)
	}
	// Path metadata can be perfectly pinned while the listening process has the
	// wrong identity. Create an impostor elsewhere, then move only its socket inode
	// into the protected fixture directory as root to test the peer check itself.
	impostorDir := filepath.Join(root, "impostor")
	if err := os.Mkdir(impostorDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(impostorDir, 41002, 41002); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(impostorDir, "impostor.sock")
	impostor := child(41002, 41002, "impostor", oldPath, sig)
	ready, err := impostor.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	impostor.Stderr = &stderr
	if err := impostor.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if impostor.ProcessState == nil {
			impostor.Process.Kill()
			impostor.Wait()
		}
	}()
	signal := make([]byte, 6)
	if _, err := io.ReadFull(ready, signal); err != nil || string(signal) != "READY\n" {
		t.Fatalf("impostor not ready: %v %s", err, stderr.String())
	}
	fakePath := filepath.Join(endpoint, "impostor.sock")
	if err := os.Rename(oldPath, fakePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(fakePath, 0, int(controllerTestGID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fakePath, 0660); err != nil {
		t.Fatal(err)
	}
	cmd := child(controllerTestUID, controllerTestGID, "runner-refused", fakePath, sig)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("controller accepted impostor: %v\n%s", err, out)
	}
	rest, readErr := io.ReadAll(ready)
	waitErr := impostor.Wait()
	if readErr != nil || waitErr != nil || !strings.Contains(string(rest), "NO_REQUEST\n") {
		t.Fatalf("sent signed data to impostor: %v %v %s %s", readErr, waitErr, rest, stderr.String())
	}
	t.Log("non-root signed admission, same-group peer denial, filesystem isolation and impostor server denial verified")
}

func TestControllerIdentityChild(t *testing.T) {
	action := os.Getenv("PORTAL_APP_RUNNER_IDENTITY_CHILD")
	if action == "" {
		t.Skip("identity subprocess only")
	}
	path := os.Getenv("IDENTITY_SOCKET")
	if action == "impostor" {
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		listener.SetDeadline(time.Now().Add(10 * time.Second))
		// Raw signal precedes test output; the parent consumes exactly this marker.
		os.Stdout.WriteString("READY\n")
		conn, err := listener.AcceptUnix()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		var b [1]byte
		n, err := conn.Read(b[:])
		if n != 0 || err != io.EOF {
			t.Fatal("controller disclosed request to wrong runner")
		}
		os.Stdout.WriteString("NO_REQUEST\n")
		return
	}
	raw, err := base64.RawStdEncoding.DecodeString(os.Getenv("IDENTITY_COMMAND"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.RawStdEncoding.DecodeString(os.Getenv("IDENTITY_SIGNATURE"))
	if err != nil {
		t.Fatal(err)
	}
	if action == "dac-refused" {
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if err == nil {
			conn.Close()
			t.Fatal("filesystem allowed unrelated identity")
		}
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("expected filesystem permission refusal: %v", err)
		}
	}
	err = ExchangeController(context.Background(), path, uint32(os.Geteuid()), controllerTestGID, raw, sig)
	switch action {
	case "accepted":
		if err != nil {
			t.Fatal(err)
		}
	case "peer-refused", "signature-refused", "dac-refused", "runner-refused":
		if err == nil {
			t.Fatal("unauthorized request accepted")
		}
	default:
		t.Fatal("unknown subprocess action")
	}
}
