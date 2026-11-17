//go:build linux

package driver

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func openBrokerDir(t *testing.T) (string, int) {
	t.Helper()
	dir := t.TempDir()
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	return dir, fd
}

// The socket is created by name inside the pinned directory, world-writable so
// the application's user can connect, and removed with the listener.
func TestServiceBrokerSocketIsBoundInsideThePinnedDirectory(t *testing.T) {
	dir, fd := openBrokerDir(t)
	owner := uint32(os.Geteuid())
	l, err := bindServiceBrokerSocket(fd, owner)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ServiceBrokerSocketName)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0666 {
		t.Fatalf("socket %v %v", info, err)
	}
	// It accepts connections.
	accepted := make(chan struct{})
	go func() {
		c, err := l.Accept()
		if err == nil {
			c.Close()
		}
		close(accepted)
	}()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	<-accepted
	if l.intact() != nil {
		t.Fatal("a fresh socket is not intact")
	}
	// The directory is replaced by a different one under the same path: the
	// listener is bound to the pinned directory, so the identity check still
	// holds for the original and fails once the socket is replaced.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(l.intact(), ErrUnsafeSocket) {
		t.Fatal("a removed socket is still reported intact")
	}
	other, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(l.intact(), ErrUnsafeSocket) {
		t.Fatal("a replacement socket is reported intact")
	}
	// Closing the original must not remove the replacement.
	if err := l.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("close removed a socket it did not bind")
	}
	other.Close()
}

func TestServiceBrokerStaleSocketIsRemovedOnlyIfOurs(t *testing.T) {
	dir, fd := openBrokerDir(t)
	owner := uint32(os.Geteuid())
	path := filepath.Join(dir, ServiceBrokerSocketName)
	// A socket left by a previous run, owned by owner with one link, is replaced.
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	l, err := bindServiceBrokerSocket(fd, owner)
	if err != nil {
		t.Fatalf("stale socket of ours not replaced: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("close left the socket behind")
	}
	// A socket of another owner is refused, not removed.
	stale, err = net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	if _, err := bindServiceBrokerSocket(fd, owner+1); !errors.Is(err, ErrUnsafeSocket) {
		t.Fatalf("a socket of another owner was replaced: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("a refused bind removed the socket")
	}
	os.Remove(path)
	// A regular file, a symlink and a hard-linked socket are never replaced.
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := bindServiceBrokerSocket(fd, owner); !errors.Is(err, ErrUnsafeSocket) {
		t.Fatalf("a regular file was replaced: %v", err)
	}
	os.Remove(path)
	if err := os.Symlink("/etc/passwd", path); err != nil {
		t.Fatal(err)
	}
	if _, err := bindServiceBrokerSocket(fd, owner); !errors.Is(err, ErrUnsafeSocket) {
		t.Fatalf("a symlink was followed or replaced: %v", err)
	}
	os.Remove(path)
	stale, err = net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	link := filepath.Join(dir, "second-name")
	if err := os.Link(path, link); err != nil {
		t.Skip("hard links to sockets are not supported here:", err)
	}
	if _, err := bindServiceBrokerSocket(fd, owner); !errors.Is(err, ErrUnsafeSocket) {
		t.Fatalf("a multiply linked socket was replaced: %v", err)
	}
}

// The broker socket is created by root inside the root-owned directory, which
// the container sees through a read-only bind mount; the application's user
// (65532) must still be able to connect. This needs root and mount(2), so it
// runs only in the disposable acceptance environment (PORTAL_APP_RUNNER_
// CONTAINER_ACCEPTANCE=1 on a GitHub-hosted runner); everywhere else it skips.
func TestNativeServiceBrokerSocketInPinnedDir(t *testing.T) {
	if path := os.Getenv("PORTAL_SERVICE_BROKER_DIAL"); path != "" {
		// Child process, running as the application's user.
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		return
	}
	requireMountAcceptance(t)
	root, err := os.MkdirTemp("/run", "portal-broker-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	source, view := filepath.Join(root, "source"), filepath.Join(root, "view")
	for _, dir := range []string{source, view} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(source, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	listener, err := bindServiceBrokerSocket(fd, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(source, ServiceBrokerSocketName), &st); err != nil || st.Uid != 0 || st.Mode&07777 != 0666 || st.Nlink != 1 {
		t.Fatalf("socket is not root-owned 0666: %+v %v", st, err)
	}
	if err := unix.Mount(source, view, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer unix.Unmount(view, 0)
	if err := unix.Mount("", view, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
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
	child := exec.Command(os.Args[0], "-test.run=^TestNativeServiceBrokerSocketInPinnedDir$")
	child.Env = append(os.Environ(), "PORTAL_SERVICE_BROKER_DIAL="+filepath.Join(view, ServiceBrokerSocketName))
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("the application user could not connect through the read-only mount: %v\n%s", err, out)
	}
}
