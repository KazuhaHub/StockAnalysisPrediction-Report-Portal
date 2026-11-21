//go:build linux

package transport

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// staleFixture is a control root as host-setup makes it (owner:gid 0750) holding the socket a killed runner
// left behind. The identity is the test user's, so the same checks run unprivileged.
type staleFixture struct {
	dir, socket string
	uid, gid    uint32
}

func newStaleFixture(t *testing.T) staleFixture {
	t.Helper()
	base, err := os.MkdirTemp("", "stale-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	if base, err = filepath.EvalSymlinks(base); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "control")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return staleFixture{dir: dir, socket: filepath.Join(dir, "runner.sock"), uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())}
}

// leave binds the socket and closes it without unlinking, as SIGKILL does, then sets the runner's mode.
func (f staleFixture) leave(t *testing.T, mode os.FileMode) {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: f.socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	if err := os.Chmod(f.socket, mode); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// Doc 187 section 11: a socket that is owner:gid, 0660, one link, is removed so the next listener can bind.
func TestStaleSocketOfTheRunnerIsRemoved(t *testing.T) {
	f := newStaleFixture(t)
	f.leave(t, 0o660)
	if _, err := os.Lstat(f.socket); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleControlSocket(f.socket, f.uid, f.gid); err != nil {
		t.Fatalf("a runner's own stale socket: %v", err)
	}
	if exists(f.socket) {
		t.Fatal("the stale socket is still there")
	}
	// Nothing there is not an error: the normal start.
	if err := removeStaleControlSocket(f.socket, f.uid, f.gid); err != nil {
		t.Fatalf("no socket: %v", err)
	}
}

// Fails closed: a regular file, another mode, another owner or group, a symbolic link, a directory and an
// extra hard link are each refused and left in place, so the listener then refuses them as it always has.
func TestStaleSocketRefusals(t *testing.T) {
	cases := map[string]func(t *testing.T, f staleFixture){
		"regular file": func(t *testing.T, f staleFixture) {
			if err := os.WriteFile(f.socket, []byte("x"), 0o660); err != nil {
				t.Fatal(err)
			}
		},
		"mode 0600": func(t *testing.T, f staleFixture) { f.leave(t, 0o600) },
		"mode 0666": func(t *testing.T, f staleFixture) { f.leave(t, 0o666) },
		"mode 0640": func(t *testing.T, f staleFixture) { f.leave(t, 0o640) },
		"symbolic link to a socket": func(t *testing.T, f staleFixture) {
			real := filepath.Join(f.dir, "real.sock")
			l, err := net.ListenUnix("unix", &net.UnixAddr{Name: real, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			l.SetUnlinkOnClose(false)
			l.Close()
			os.Chmod(real, 0o660)
			if err := os.Symlink(real, f.socket); err != nil {
				t.Fatal(err)
			}
		},
		"directory": func(t *testing.T, f staleFixture) {
			if err := os.Mkdir(f.socket, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"second hard link": func(t *testing.T, f staleFixture) {
			f.leave(t, 0o660)
			if err := os.Link(f.socket, filepath.Join(f.dir, "other.sock")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newStaleFixture(t)
			setup(t, f)
			if err := removeStaleControlSocket(f.socket, f.uid, f.gid); !errors.Is(err, ErrUnsafeEndpoint) {
				t.Fatalf("refusal: %v", err)
			}
			if !exists(f.socket) {
				t.Fatal("a refused path was removed")
			}
		})
	}
	t.Run("another owner", func(t *testing.T) {
		f := newStaleFixture(t)
		f.leave(t, 0o660)
		if err := removeStaleControlSocket(f.socket, f.uid+1, f.gid); !errors.Is(err, ErrUnsafeEndpoint) || !exists(f.socket) {
			t.Fatalf("another owner: %v", err)
		}
	})
	t.Run("another group", func(t *testing.T) {
		f := newStaleFixture(t)
		f.leave(t, 0o660)
		if err := removeStaleControlSocket(f.socket, f.uid, f.gid+1); !errors.Is(err, ErrUnsafeEndpoint) || !exists(f.socket) {
			t.Fatalf("another group: %v", err)
		}
	})
}

// A parent that is not the control root the runner requires is never touched either: nothing is removed
// beneath a directory the listener would refuse.
func TestStaleSocketUnderAnUnsafeParentIsLeftAlone(t *testing.T) {
	f := newStaleFixture(t)
	f.leave(t, 0o660)
	if err := os.Chmod(f.dir, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleControlSocket(f.socket, f.uid, f.gid); !errors.Is(err, ErrUnsafeEndpoint) || !exists(f.socket) {
		t.Fatalf("a group-writable parent: %v", err)
	}
}

// The entry point takes the journal store as the proof that the owner lock is held: without one it does nothing
// at all, so a second runner (which cannot open the journal) never reaches the removal. With the lock really held,
// the same socket is removed.
func TestRemoveStaleControlSocketNeedsTheJournalLock(t *testing.T) {
	f := newStaleFixture(t)
	f.leave(t, 0o660)
	if err := removeStaleUnderLock(f.socket, f.uid, f.gid, nil); !errors.Is(err, ErrUnsafeEndpoint) {
		t.Fatalf("without the lock: %v", err)
	}
	if !exists(f.socket) {
		t.Fatal("the socket was removed without the journal lock")
	}
	if err := RemoveStaleControlSocket(f.socket, f.gid, nil); !errors.Is(err, ErrUnsafeEndpoint) || !exists(f.socket) {
		t.Fatalf("the exported entry point without the lock: %v", err)
	}
	directory := filepath.Join(filepath.Dir(f.dir), "journal")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// A second owner of the same journal cannot get a store, so it can never reach the removal.
	if _, err := journal.Open(directory); !errors.Is(err, journal.ErrInUse) {
		t.Fatalf("a second journal owner: %v", err)
	}
	if err := removeStaleUnderLock(f.socket, f.uid, f.gid, store); err != nil || exists(f.socket) {
		t.Fatalf("with the lock held: %v", err)
	}
}
