//go:build linux

package driver

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// How the daemon on the other end of a connection is identified, per connection and without any path:
//
//  1. After the first response (dockerd has accepted the connection by then), ask the kernel through NETLINK
//     sock_diag (UNIX_DIAG, UDIAG_SHOW_PEER) for the peer of the runner's own socket. Before accept the peer is
//     the server end of the pending connection, which exists in the connecting process's network namespace with
//     the listener's path copied onto it and has no inode (a "socket with inode 0" in /proc/net/unix); once the
//     daemon accepts, it gets one.
//  2. Find the processes that hold that inode through /proc/*/fd.
//  3. Require every holder to be a dockerd (the executable's base name) in the runner's mount namespace.
//
// That refuses a forwarder (socat, a proxy to another host's daemon: its holder is the forwarder), another
// mount namespace (a snap-packaged Docker, a containerised daemon) and a Docker Desktop VM, because none of
// them leaves a local dockerd holding the connection. It is not a defence against host root, which can
// impersonate anything, and a dockerd is recognised by its base name only. A pid that is reused between the
// scan and a later connection is re-checked by what it holds and runs on that connection, not trusted.

// daemonExecutableAccepted decides whether an executable is the container daemon. A variable so a test can
// stand in for it.
var daemonExecutableAccepted = dockerdExecutable

// dockerdExecutable reports whether an executable path is a dockerd binary, by its base name.
func dockerdExecutable(exe string) bool {
	return filepath.Base(strings.TrimSuffix(exe, " (deleted)")) == "dockerd"
}

// The readers verify uses, as variables so a test can fake the kernel.
var (
	daemonPeerInode              = realDaemonPeerInode
	daemonSocketHolders          = realDaemonSocketHolders
	daemonHoldsSocket            = realDaemonHoldsSocket
	daemonExecutableOf           = func(pid int) (string, error) { return os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe") }
	daemonInRunnerMountNamespace = sharesMountNamespace
)

// sharesMountNamespace reports whether a process is in the runner's mount namespace. A namespace file that
// cannot be read is not shared.
func sharesMountNamespace(pid int) bool {
	theirs, err := os.Stat("/proc/" + strconv.Itoa(pid) + "/ns/mnt")
	if err != nil {
		return false
	}
	ours, err := os.Stat("/proc/self/ns/mnt")
	return err == nil && os.SameFile(ours, theirs)
}

// daemonVouched reports whether a pid is a dockerd in the runner's mount namespace right now.
func daemonVouched(pid int) bool {
	exe, err := daemonExecutableOf(pid)
	return err == nil && daemonExecutableAccepted(exe) && daemonInRunnerMountNamespace(pid)
}

// verify identifies the process that holds the daemon end of an established connection. It runs after the
// daemon has answered, on every connection, and refuses unless every holder is a dockerd in the runner's mount
// namespace.
func (p *daemonPeer) verify(conn net.Conn) error {
	ino, err := daemonPeerInode(conn)
	if err != nil {
		return p.refuse("sock_diag: %v", err)
	}
	if ino == 0 {
		return p.refuse("the peer socket has no inode, so the connection was not accepted")
	}
	p.mu.Lock()
	var cached []int
	for pid := range p.vetted {
		cached = append(cached, pid)
	}
	p.mu.Unlock()
	for _, pid := range cached {
		if daemonHoldsSocket(pid, ino) && daemonVouched(pid) {
			return nil
		}
	}
	holders, err := daemonSocketHolders(ino)
	if err != nil {
		return p.refuse("process scan: %v", err)
	}
	if len(holders) == 0 {
		return p.refuse("no process holds the peer socket %d", ino)
	}
	for _, pid := range holders {
		if !daemonVouched(pid) {
			exe, _ := daemonExecutableOf(pid)
			return p.refuse("the connection is held by pid %d (%q, mount namespace shared: %v)", pid, exe, daemonInRunnerMountNamespace(pid))
		}
	}
	p.mu.Lock()
	p.vetted = map[int]struct{}{}
	for _, pid := range holders {
		p.vetted[pid] = struct{}{}
	}
	p.mu.Unlock()
	return nil
}

// realDaemonHoldsSocket reports whether a process has a descriptor on the socket inode.
func realDaemonHoldsSocket(pid int, ino uint64) bool {
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	fds, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	want := "socket:[" + strconv.FormatUint(ino, 10) + "]"
	for _, fd := range fds {
		if target, err := os.Readlink(dir + "/" + fd.Name()); err == nil && target == want {
			return true
		}
	}
	return false
}

// realDaemonSocketHolders finds every process with a descriptor on the socket inode.
func realDaemonSocketHolders(ino uint64) ([]int, error) {
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var holders []int
	for _, entry := range processes {
		if pid, err := strconv.Atoi(entry.Name()); err == nil && realDaemonHoldsSocket(pid, ino) {
			holders = append(holders, pid)
		}
	}
	return holders, nil
}

const (
	sockDiagByFamily = 20 // SOCK_DIAG_BY_FAMILY
	udiagShowPeer    = 4  // UDIAG_SHOW_PEER
	unixDiagPeer     = 2  // UNIX_DIAG_PEER
	nlmsgError       = 2
)

// realDaemonPeerInode returns the inode of the peer of a connected unix socket, from the kernel's sock_diag.
func realDaemonPeerInode(conn net.Conn) (uint64, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix connection")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var own uint64
	var statErr error
	if err := raw.Control(func(fd uintptr) {
		var st unix.Stat_t
		if statErr = unix.Fstat(int(fd), &st); statErr == nil {
			own = uint64(st.Ino)
		}
	}); err != nil {
		return 0, err
	}
	if statErr != nil {
		return 0, statErr
	}
	nl, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_SOCK_DIAG)
	if err != nil {
		return 0, fmt.Errorf("netlink: %w", err)
	}
	defer unix.Close(nl)
	timeout := unix.Timeval{Sec: 2}
	if err := unix.SetsockoptTimeval(nl, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &timeout); err != nil {
		return 0, err
	}
	// nlmsghdr, then unix_diag_req: family, protocol, pad, states, ino, show, cookie[2].
	request := make([]byte, 16+24)
	order := binary.NativeEndian
	order.PutUint32(request[0:], uint32(len(request)))
	order.PutUint16(request[4:], sockDiagByFamily)
	order.PutUint16(request[6:], unix.NLM_F_REQUEST)
	request[16] = unix.AF_UNIX
	order.PutUint32(request[20:], ^uint32(0)) // every state
	order.PutUint32(request[24:], uint32(own))
	order.PutUint32(request[28:], udiagShowPeer)
	order.PutUint32(request[32:], ^uint32(0)) // no cookie
	order.PutUint32(request[36:], ^uint32(0))
	if err := unix.Sendto(nl, request, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return 0, fmt.Errorf("netlink send: %w", err)
	}
	reply := make([]byte, 4096)
	n, _, err := unix.Recvfrom(nl, reply, 0)
	if err != nil {
		return 0, fmt.Errorf("netlink receive: %w", err)
	}
	return parseUnixDiagPeer(reply[:n])
}

// parseUnixDiagPeer reads the UNIX_DIAG_PEER attribute of a sock_diag reply: nlmsghdr, unix_diag_msg, then
// attributes of (length, type, value). A netlink error, a short reply or a reply without a peer is an error.
func parseUnixDiagPeer(reply []byte) (uint64, error) {
	order := binary.NativeEndian
	if len(reply) < 16 {
		return 0, errors.New("short netlink reply")
	}
	total := int(order.Uint32(reply[0:]))
	if total < 16 || total > len(reply) {
		return 0, errors.New("malformed netlink reply")
	}
	if order.Uint16(reply[4:]) == nlmsgError {
		code := int32(0)
		if total >= 20 {
			code = int32(order.Uint32(reply[16:]))
		}
		return 0, fmt.Errorf("netlink error %v", syscall.Errno(-code))
	}
	if total < 32 {
		return 0, errors.New("short unix_diag message")
	}
	attrs := reply[16+16 : total]
	for len(attrs) >= 4 {
		length := int(order.Uint16(attrs[0:]))
		kind := order.Uint16(attrs[2:])
		if length < 4 || length > len(attrs) {
			return 0, errors.New("malformed attribute")
		}
		if kind == unixDiagPeer && length >= 8 {
			return uint64(order.Uint32(attrs[4:])), nil
		}
		next := (length + 3) &^ 3
		if next > len(attrs) {
			break
		}
		attrs = attrs[next:]
	}
	return 0, errors.New("the reply has no peer attribute")
}
