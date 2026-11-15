//go:build linux

package driver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func peerClient(peer *daemonPeer) *http.Client {
	return &http.Client{Transport: allowListTransport{next: http.DefaultTransport, peer: peer}}
}

func TestInventoryRefusesADaemonOutsideTheMountNamespace(t *testing.T) {
	for name, tc := range map[string]struct {
		state int32
		want  bool
	}{
		"not yet seen":                   {daemonPeerUnknown, false},
		"outside the runner's namespace": {daemonPeerOtherMountNamespace, false},
		"shared":                         {daemonPeerSharesMountNamespace, true},
	} {
		t.Run(name, func(t *testing.T) {
			peer := &daemonPeer{}
			peer.state.Store(tc.state)
			err := requireDaemonSharesMountNamespace(peerClient(peer))
			if (err == nil) != tc.want || (err != nil && !errors.Is(err, ErrUnavailable)) {
				t.Fatalf("state %d: %v", tc.state, err)
			}
		})
	}
	// A client with no socket peer (a test client) is not asked.
	if err := requireDaemonSharesMountNamespace(&http.Client{Transport: allowListTransport{next: http.DefaultTransport}}); err != nil {
		t.Fatal(err)
	}
	// One connection outside the namespace taints the client; a later shared one does not clear it.
	peer := &daemonPeer{}
	peer.record(false)
	peer.record(true)
	if requireDaemonSharesMountNamespace(peerClient(peer)) == nil {
		t.Fatal("a later shared connection cleared an outside verdict")
	}
}

func TestSharesMountNamespace(t *testing.T) {
	if !sharesMountNamespace(os.Getpid()) {
		t.Fatal("a process is in its own mount namespace")
	}
	if sharesMountNamespace(1 << 30) {
		t.Fatal("a process that cannot be read was treated as shared")
	}
}

// The dialer records the verdict of the process it connects to. The listener is this process, so the verdict is
// shared; the dialer needs a root peer, so the test needs root.
func TestDockerDialerRecordsThePeerMountNamespace(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the dialer requires a root peer")
	}
	dir, err := os.MkdirTemp("/run", "portal-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	peer := &daemonPeer{}
	conn, err := dockerDialer(socket, peer)(context.Background(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := requireDaemonSharesMountNamespace(peerClient(peer)); err != nil {
		t.Fatal(err)
	}
}
