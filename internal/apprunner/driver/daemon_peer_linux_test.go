//go:build linux

package driver

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeDaemonPeer replaces the four readers verify uses and counts the expensive one.
type fakeDaemonPeer struct {
	peerInode  uint64
	peerErr    error
	holders    []int
	holdersErr error
	holds      map[int]bool // pid -> still holds the inode
	exe        map[int]string
	sameNS     map[int]bool
	scans      int
}

func installFakeDaemonPeer(t *testing.T, f *fakeDaemonPeer) {
	t.Helper()
	oi, oh, oq, oe, on := daemonPeerInode, daemonSocketHolders, daemonHoldsSocket, daemonExecutableOf, daemonInRunnerMountNamespace
	daemonPeerInode = func(net.Conn) (uint64, error) { return f.peerInode, f.peerErr }
	daemonSocketHolders = func(uint64) ([]int, error) { f.scans++; return f.holders, f.holdersErr }
	daemonHoldsSocket = func(pid int, _ uint64) bool { return f.holds[pid] }
	daemonExecutableOf = func(pid int) (string, error) {
		if e, ok := f.exe[pid]; ok {
			return e, nil
		}
		return "", os.ErrNotExist
	}
	daemonInRunnerMountNamespace = func(pid int) bool { return f.sameNS[pid] }
	t.Cleanup(func() {
		daemonPeerInode, daemonSocketHolders, daemonHoldsSocket, daemonExecutableOf, daemonInRunnerMountNamespace = oi, oh, oq, oe, on
	})
}

func goodFake() *fakeDaemonPeer {
	return &fakeDaemonPeer{
		peerInode: 4242, holders: []int{900}, holds: map[int]bool{900: true},
		exe: map[int]string{900: "/usr/bin/dockerd", 901: "/usr/bin/socat", 902: "/usr/bin/dockerd"}, sameNS: map[int]bool{900: true, 901: true, 902: false},
	}
}

func TestPeerMustBeADockerdInTheRunnersMountNamespace(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*fakeDaemonPeer)
		ok     bool
	}{
		"a dockerd in the runner's mount namespace": {func(*fakeDaemonPeer) {}, true},
		"a forwarder (socat) holds the connection":  {func(f *fakeDaemonPeer) { f.holders = []int{901} }, false},
		"a dockerd in another mount namespace":      {func(f *fakeDaemonPeer) { f.holders = []int{902} }, false},
		"a dockerd and a forwarder both hold it":    {func(f *fakeDaemonPeer) { f.holders = []int{900, 901} }, false},
		"no process holds the peer socket":          {func(f *fakeDaemonPeer) { f.holders = nil }, false},
		"the scan fails":                            {func(f *fakeDaemonPeer) { f.holdersErr = errors.New("eacces") }, false},
		"sock_diag is unavailable":                  {func(f *fakeDaemonPeer) { f.peerErr = errors.New("netlink: protocol not supported") }, false},
		"the peer has no inode yet":                 {func(f *fakeDaemonPeer) { f.peerInode = 0 }, false},
		"the executable cannot be read":             {func(f *fakeDaemonPeer) { f.holders = []int{999} }, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := goodFake()
			tc.change(f)
			installFakeDaemonPeer(t, f)
			err := (&daemonPeer{}).verify(nil)
			if tc.ok != (err == nil) || (err != nil && !errors.Is(err, ErrUnavailable)) {
				t.Fatalf("ok=%v: %v", tc.ok, err)
			}
		})
	}
}

// The scan of /proc is done for a daemon the client has not vetted, not for every connection.
func TestVettedDaemonIsReverifiedWithoutAScan(t *testing.T) {
	f := goodFake()
	installFakeDaemonPeer(t, f)
	peer := &daemonPeer{}
	for i := 0; i < 5; i++ {
		f.peerInode = uint64(5000 + i) // each connection's peer socket is a new inode
		if err := peer.verify(nil); err != nil {
			t.Fatal(err)
		}
	}
	if f.scans != 1 {
		t.Fatalf("%d scans for five connections to one daemon", f.scans)
	}
	// The vetted process no longer holds the new peer socket: it is looked up again.
	f.holds[900] = false
	if err := peer.verify(nil); err != nil || f.scans != 2 {
		t.Fatalf("scans=%d err=%v", f.scans, err)
	}
	// The vetted pid now runs something else: it is refused, not trusted from the cache.
	f.holds[900] = true
	f.exe[900] = "/usr/bin/socat"
	if err := peer.verify(nil); err == nil {
		t.Fatal("a cached pid that became a forwarder was trusted")
	}
	// And one that left the runner's mount namespace.
	f.exe[900] = "/usr/bin/dockerd"
	f.sameNS[900] = false
	f.holders = []int{900}
	if err := peer.verify(nil); err == nil {
		t.Fatal("a cached pid that left the mount namespace was trusted")
	}
}

func TestDaemonExecutableNameIsDockerd(t *testing.T) {
	for exe, want := range map[string]bool{
		"/usr/bin/dockerd": true, "/usr/local/bin/dockerd": true, "/usr/bin/dockerd (deleted)": true,
		"/usr/lib/systemd/systemd": false, "/usr/bin/docker-proxy": false, "/usr/bin/dockerd-evil": false, "/usr/bin/socat": false, "": false,
	} {
		if got := dockerdExecutable(exe); got != want {
			t.Fatalf("%q: %v", exe, got)
		}
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

func TestParseUnixDiagPeer(t *testing.T) {
	// nlmsghdr(16) + unix_diag_msg(16) + UNIX_DIAG_PEER(8)
	msg := make([]byte, 40)
	put32 := func(off int, v uint32) {
		msg[off], msg[off+1], msg[off+2], msg[off+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	put16 := func(off int, v uint16) { msg[off], msg[off+1] = byte(v), byte(v>>8) }
	put32(0, 40)
	put16(4, 20) // SOCK_DIAG_BY_FAMILY
	put32(16+4, 77)
	put16(32, 8)
	put16(34, 2) // UNIX_DIAG_PEER
	put32(36, 31337)
	if ino, err := parseUnixDiagPeer(msg); err != nil || ino != 31337 {
		t.Fatal(ino, err)
	}
	put16(34, 1) // another attribute: no peer
	if _, err := parseUnixDiagPeer(msg); err == nil {
		t.Fatal("a reply without a peer attribute was accepted")
	}
	put16(4, 2) // NLMSG_ERROR
	if _, err := parseUnixDiagPeer(msg); err == nil {
		t.Fatal("a netlink error was accepted")
	}
	if _, err := parseUnixDiagPeer(msg[:10]); err == nil {
		t.Fatal("a truncated reply was accepted")
	}
}

// unixDaemon serves HTTP on a unix socket in this process, so the accepted connection is held by this process.
func unixDaemon(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "portal-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "[]") })}
	go server.Serve(l)
	t.Cleanup(func() { server.Close() })
	return socket
}

func realPeerClient(socket string) *http.Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &http.Client{Transport: allowListTransport{next: transport, peer: &daemonPeer{}}, Timeout: 5 * time.Second}
}

func inventoryRequest(t *testing.T, client *http.Client) error {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, "http://"+dockerRequestHost+"/v"+APIVersion+"/containers/json", nil)
	response, err := client.Do(request)
	if err == nil {
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	return err
}

// The real sock_diag and /proc readers: the peer of the runner's connection is the accepted socket this process
// holds, found without any path.
func TestRealPeerIdentityThroughSockDiagAndProc(t *testing.T) {
	socket := unixDaemon(t)
	acceptExecutable(t, func(string) bool { return true })
	client := realPeerClient(socket)
	for i := 0; i < 3; i++ {
		if err := inventoryRequest(t, client); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	// The same connection answered by a process that is not a dockerd is refused on the first response.
	acceptExecutable(t, func(string) bool { return false })
	err := inventoryRequest(t, realPeerClient(socket))
	if err == nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a daemon that is not a dockerd was accepted: %v", err)
	}
}

// The cost of identifying the daemon behind a new connection, once the daemon has been vetted: one sock_diag
// query and a look at the vetted process's descriptors.
func TestPeerVerificationCostPerConnection(t *testing.T) {
	socket := unixDaemon(t)
	acceptExecutable(t, func(string) bool { return true })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	client := &http.Client{Transport: allowListTransport{next: transport, peer: &daemonPeer{}}, Timeout: 5 * time.Second}
	if err := inventoryRequest(t, client); err != nil { // vets the daemon: one scan
		t.Fatal(err)
	}
	const n = 200
	var withCheck time.Duration
	for i := 0; i < n; i++ {
		transport.CloseIdleConnections() // the next request is a new connection
		start := time.Now()
		if err := inventoryRequest(t, client); err != nil {
			t.Fatal(err)
		}
		withCheck += time.Since(start)
	}
	plain := &http.Client{Transport: &http.Transport{DialContext: transport.DialContext}, Timeout: 5 * time.Second}
	var without time.Duration
	for i := 0; i < n; i++ {
		plain.Transport.(*http.Transport).CloseIdleConnections()
		start := time.Now()
		inventoryRequest(t, plain)
		without += time.Since(start)
	}
	t.Logf("a new connection: %v per request with the peer check, %v without", withCheck/n, without/n)
}

func acceptExecutable(t *testing.T, accept func(string) bool) {
	t.Helper()
	old := daemonExecutableAccepted
	daemonExecutableAccepted = accept
	t.Cleanup(func() { daemonExecutableAccepted = old })
}
