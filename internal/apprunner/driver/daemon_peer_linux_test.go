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
	acceptExecutable(t, func(string) bool { return true })
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

func listenAt(t *testing.T) (string, net.Listener) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "portal-holder-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return path, l
}

func acceptExecutable(t *testing.T, accept func(string) bool) {
	t.Helper()
	old := daemonExecutableAccepted
	daemonExecutableAccepted = accept
	t.Cleanup(func() { daemonExecutableAccepted = old })
}

func TestParseProcNetUnixFindsTheListeningInode(t *testing.T) {
	raw := "Num       RefCount Protocol Flags    Type St Inode Path\n" +
		"0000000000000000: 00000002 00000000 00010000 0001 01 12345 /run/docker.sock\n" +
		"0000000000000000: 00000003 00000000 00000000 0001 03 12346\n" +
		"0000000000000000: 00000002 00000000 00010000 0001 01 777 /run/other.sock\n" +
		"0000000000000000: 00000002 00000000 00010000 0001 01 888 /run/docker.sock.bak\n"
	got := parseProcNetUnixInodes([]byte(raw), "/run/docker.sock")
	if len(got) != 1 || got[0] != "12345" {
		t.Fatalf("%v", got)
	}
	if len(parseProcNetUnixInodes([]byte(raw), "/run/none.sock")) != 0 {
		t.Fatal("an unbound path matched")
	}
}

// The listener's process holds the socket, and the holder is accepted only when its executable is a dockerd.
func TestSocketHoldersMustIncludeADaemonInTheRunnersMountNamespace(t *testing.T) {
	path, _ := listenAt(t)
	acceptExecutable(t, func(string) bool { return true })
	holders, err := vetDaemonSocketHolders(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := holders[os.Getpid()]; !ok {
		t.Fatalf("this process holds the socket but is not among %v", holders)
	}
	// A forwarding proxy to a remote daemon: the holders are not a dockerd.
	acceptExecutable(t, func(string) bool { return false })
	if _, err := vetDaemonSocketHolders(path); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a socket no dockerd holds was vetted", err)
	}
	// A socket nobody holds under that path.
	acceptExecutable(t, func(string) bool { return true })
	if _, err := vetDaemonSocketHolders(filepath.Join(filepath.Dir(path), "absent.sock")); !errors.Is(err, ErrUnavailable) {
		t.Fatal("an unbound socket path was vetted", err)
	}
}

func TestDaemonExecutableNameIsDockerd(t *testing.T) {
	for exe, want := range map[string]bool{
		"/usr/bin/dockerd": true, "/usr/local/bin/dockerd": true, "/usr/bin/dockerd (deleted)": true,
		"/usr/lib/systemd/systemd": false, "/usr/bin/docker-proxy": false, "/usr/bin/dockerd-evil": false, "": false,
	} {
		if got := dockerdExecutable(exe); got != want {
			t.Fatalf("%q: %v", exe, got)
		}
	}
}

// Every dial is checked: the peer must be one of the vetted holders and in the runner's mount namespace.
func TestPeerObservationChecksEveryDial(t *testing.T) {
	path, _ := listenAt(t)
	acceptExecutable(t, func(string) bool { return true })
	peer := &daemonPeer{}
	peer.observe(path, os.Getpid())
	if err := requireDaemonSharesMountNamespace(peerClient(peer)); err != nil {
		t.Fatal(err)
	}
	// A later dial answered by a process that is not a vetted holder taints the client.
	peer.observe(path, 1<<30)
	if requireDaemonSharesMountNamespace(peerClient(peer)) == nil {
		t.Fatal("a dial answered by an unvetted process passed")
	}
	// No dockerd among the holders: refused on the first dial.
	acceptExecutable(t, func(string) bool { return false })
	other := &daemonPeer{}
	other.observe(path, os.Getpid())
	if requireDaemonSharesMountNamespace(peerClient(other)) == nil {
		t.Fatal("a socket without a dockerd holder passed")
	}
}
