//go:build linux

package service

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func controlFixture(t *testing.T) (path string, uid, gid uint32) {
	t.Helper()
	base, err := os.MkdirTemp("", "health-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	if base, err = filepath.EvalSymlinks(base); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "runner.sock"), uint32(os.Geteuid()), uint32(os.Getegid())
}

func listen(t *testing.T, path string, mode os.FileMode) *net.UnixListener {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := l.AcceptUnix()
			if err != nil {
				return
			}
			conn.Close() // the runner drops a peer that is not the controller
		}
	}()
	return l
}

// Doc 187 section 6: healthy means the socket exists as a root:gid 0660 socket and connect() succeeds.
func TestControlSocketHealthyWhenItAnswers(t *testing.T) {
	path, uid, gid := controlFixture(t)
	listen(t, path, 0o660)
	if err := checkControlSocket(context.Background(), path, uid, gid); err != nil {
		t.Fatalf("a serving socket: %v", err)
	}
}

// Fails closed on every other state: nothing there, a socket nobody listens on (stale), a file, a mode or an
// owner other than the runner's, and a symbolic link.
func TestControlSocketUnhealthyStates(t *testing.T) {
	ctx := context.Background()
	t.Run("missing", func(t *testing.T) {
		path, uid, gid := controlFixture(t)
		if err := checkControlSocket(ctx, path, uid, gid); err == nil {
			t.Fatal("a missing socket is healthy")
		}
	})
	t.Run("stale socket with no listener", func(t *testing.T) {
		path, uid, gid := controlFixture(t)
		l := listen(t, path, 0o660)
		l.SetUnlinkOnClose(false)
		l.Close()
		if err := checkControlSocket(ctx, path, uid, gid); err == nil {
			t.Fatal("a stale socket is healthy")
		}
	})
	t.Run("regular file", func(t *testing.T) {
		path, uid, gid := controlFixture(t)
		if err := os.WriteFile(path, nil, 0o660); err != nil {
			t.Fatal(err)
		}
		if err := checkControlSocket(ctx, path, uid, gid); err == nil {
			t.Fatal("a regular file is healthy")
		}
	})
	for _, mode := range []os.FileMode{0o600, 0o666, 0o640} {
		t.Run("mode "+mode.String(), func(t *testing.T) {
			path, uid, gid := controlFixture(t)
			listen(t, path, mode)
			if err := checkControlSocket(ctx, path, uid, gid); err == nil {
				t.Fatalf("mode %v is healthy", mode)
			}
		})
	}
	t.Run("another owner", func(t *testing.T) {
		path, uid, gid := controlFixture(t)
		listen(t, path, 0o660)
		if err := checkControlSocket(ctx, path, uid+1, gid); err == nil {
			t.Fatal("another owner is healthy")
		}
	})
	t.Run("another group", func(t *testing.T) {
		path, uid, gid := controlFixture(t)
		listen(t, path, 0o660)
		if err := checkControlSocket(ctx, path, uid, gid+1); err == nil {
			t.Fatal("another group is healthy")
		}
	})
	t.Run("symbolic link", func(t *testing.T) {
		path, uid, gid := controlFixture(t)
		real := path + ".real"
		listen(t, real, 0o660)
		if err := os.Symlink(real, path); err != nil {
			t.Fatal(err)
		}
		if err := checkControlSocket(ctx, path, uid, gid); err == nil {
			t.Fatal("a symbolic link is healthy")
		}
	})
}

// Health takes no lock: it answers while another process holds the journal, and it reads only the
// configuration. An invalid configuration is unhealthy, not a panic.
func TestHealthRefusesAnInvalidConfiguration(t *testing.T) {
	if err := Health(context.Background(), filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("a missing configuration is healthy")
	}
}
