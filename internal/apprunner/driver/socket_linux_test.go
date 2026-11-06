//go:build linux

package driver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func fixtureSocket(t *testing.T) (string, net.Listener) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	return path, listener
}

func TestSocketPathProtection(t *testing.T) {
	for _, name := range []string{"safe", "relative", "symlink", "world_access", "unsafe_parent", "regular", "hard_link"} {
		t.Run(name, func(t *testing.T) {
			path, _ := fixtureSocket(t)
			candidate := path
			switch name {
			case "relative":
				candidate = "docker.sock"
			case "symlink":
				candidate = path + ".alias"
				if err := os.Symlink(path, candidate); err != nil {
					t.Fatal(err)
				}
			case "world_access":
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			case "unsafe_parent":
				if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
					t.Fatal(err)
				}
			case "regular":
				candidate = path + ".file"
				if err := os.WriteFile(candidate, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "hard_link":
				if err := os.Link(path, path+".alias"); err != nil {
					t.Fatal(err)
				}
			}
			_, err := socketInfo(candidate, uint32(os.Geteuid()))
			if name == "safe" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrUnsafeSocket) {
				t.Fatalf("unsafe path accepted: %v", err)
			}
		})
	}
}

func TestPeerIdentityUsesKernelCredentials(t *testing.T) {
	path, listener := fixtureSocket(t)
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		} else {
			accepted <- nil
		}
	}()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer := <-accepted
	if peer == nil {
		t.Fatal("fixture did not accept")
	}
	defer peer.Close()
	if got := rootPeer(conn); got != (os.Geteuid() == 0) {
		t.Fatalf("peer identity ignored: root=%v uid=%d", got, os.Geteuid())
	}
	if os.Geteuid() != 0 {
		if _, err := InspectDaemon(context.Background(), path); !errors.Is(err, ErrUnsafeSocket) {
			t.Fatalf("non-root fixture adopted as daemon: %v", err)
		}
	}
}

func TestDockerAcceptanceInspection(t *testing.T) {
	socket := os.Getenv("PORTAL_APP_RUNNER_ACCEPTANCE_SOCKET")
	if socket == "" {
		t.Skip("explicit disposable Linux Docker acceptance socket not set")
	}
	report, err := InspectDaemon(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	t.Log(string(raw))
	if !report.MetadataCompatible || report.ExecutionReady {
		t.Fatalf("unexpected daemon inspection: %+v", report)
	}
}
