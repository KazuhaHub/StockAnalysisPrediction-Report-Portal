//go:build linux

package driver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The runner's own connection to the real dockerd is identified (dockerd, in the runner's mount namespace), and
// a socat forwarder on another socket path that points at the same daemon is refused, because the connection is
// held by socat. Runs against a real daemon as root on a disposable runner (the foreign container job).
func TestDockerAcceptancePeerIdentity(t *testing.T) {
	if os.Getenv("PORTAL_APP_RUNNER_FOREIGN_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable foreign container acceptance not enabled")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("peer identity acceptance requires root on a disposable GitHub-hosted Linux runner")
	}
	socket := os.Getenv("PORTAL_APP_RUNNER_ACCEPTANCE_SOCKET")
	if socket == "" {
		t.Fatal("explicit daemon socket required")
	}
	client, closeClient := newDockerClient(socket, 10*time.Second, 30*time.Second)
	defer closeClient()
	if _, err := readCandidateIPCInventory(context.Background(), client); err != nil {
		t.Fatalf("the runner's own connection to the real dockerd was refused: %v", err)
	}
	// The cost of identifying the real daemon behind a new connection, with its real descriptor table.
	const n = 50
	var total time.Duration
	for i := 0; i < n; i++ {
		closeClient()
		start := time.Now()
		if _, err := readCandidateIPCInventory(context.Background(), client); err != nil {
			t.Fatal(err)
		}
		total += time.Since(start)
	}
	t.Logf("a new connection to the real dockerd: %v per inventory request including the peer check", total/n)

	socat, err := exec.LookPath("socat")
	if err != nil {
		t.Fatal("socat is required for the forwarder case:", err)
	}
	dir, err := os.MkdirTemp("/run", "portal-fwd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	forwarder := filepath.Join(dir, "fwd.sock")
	process := exec.Command(socat, "UNIX-LISTEN:"+forwarder+",fork,mode=600", "UNIX-CONNECT:"+socket)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { process.Process.Kill(); process.Wait() })
	for i := 0; ; i++ {
		if _, err := os.Stat(forwarder); err == nil {
			break
		}
		if i > 100 {
			t.Fatal("the forwarder did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	fwd, closeFwd := newDockerClient(forwarder, 10*time.Second, 30*time.Second)
	defer closeFwd()
	_, err = readCandidateIPCInventory(context.Background(), fwd)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a socat forwarder to the daemon was not refused: %v", err)
	}
	t.Logf("the forwarder was refused: %v", err)
}
