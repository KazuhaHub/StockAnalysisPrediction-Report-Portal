//go:build linux

package service

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func me() ipcIdentity { return ipcIdentity{uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())} }

func modeOf(t *testing.T, path string) (uint32, os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Uid, info.Mode()
}

// Doc 187 section 7: a missing ipc_root is created as root:root 0700 and a missing parent as 0755, whatever
// the process umask (the container entry runs with 0077).
func TestEnsureIPCRootCreatesMissingRootAndParent(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	base := canonicalTempDir(t)
	path := filepath.Join(base, "portal-app-runner", "ipc")
	if err := ensureIPCRoot(path, me()); err != nil {
		t.Fatal(err)
	}
	if _, mode := modeOf(t, filepath.Dir(path)); mode.Perm() != 0o755 || !mode.IsDir() {
		t.Fatalf("parent mode %v", mode)
	}
	if uid, mode := modeOf(t, path); mode.Perm() != 0o700 || !mode.IsDir() || uid != me().uid {
		t.Fatalf("ipc root %v owner %d", mode, uid)
	}
	// Idempotent: a second start accepts what the first made.
	if err := ensureIPCRoot(path, me()); err != nil {
		t.Fatalf("second start: %v", err)
	}
}

// An existing ipc_root with another mode or owner refuses and is left alone. Fails closed.
func TestEnsureIPCRootRefusesAnotherModeOrOwner(t *testing.T) {
	base := canonicalTempDir(t)
	for _, mode := range []os.FileMode{0o750, 0o755, 0o770, 0o777, 0o500, os.ModeSetgid | 0o700} {
		path := filepath.Join(base, "ipc-"+mode.String())
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := ensureIPCRoot(path, me()); err == nil {
			t.Errorf("mode %v was accepted", mode)
		}
		if _, got := modeOf(t, path); got.Perm() != mode.Perm() {
			t.Errorf("a refused root was changed: %v", got)
		}
	}
	// Another owner (the identity names a uid this process is not).
	if os.Geteuid() == 0 {
		t.Skip("root owns every fixture directory")
	}
	path := filepath.Join(base, "foreign")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	other := me()
	other.uid++
	if err := ensureIPCRoot(path, other); err == nil {
		t.Fatal("a directory owned by another user was accepted")
	}
	// A symbolic link, even to a good directory.
	target := filepath.Join(base, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ensureIPCRoot(link, me()); err == nil {
		t.Fatal("a symbolic link was accepted as the ipc root")
	}
	// A file.
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureIPCRoot(file, me()); err == nil {
		t.Fatal("a regular file was accepted")
	}
}

// The parent rule is the ancestor rule every root uses: owned by root (or the runner's identity) and not group-
// or world-writable. Doc 146's layout, root:<gid> 0750, and a fresh 0755 are both accepted; a group-writable
// parent refuses.
func TestEnsureIPCRootParentRule(t *testing.T) {
	base := canonicalTempDir(t)
	for _, mode := range []os.FileMode{0o750, 0o755, 0o700, 0o555} {
		parent := filepath.Join(base, "parent-"+mode.String())
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, mode); err != nil {
			t.Fatal(err)
		}
		// The test user must be able to create below a 0555 parent: skip the creation, test acceptance below it.
		if mode == 0o555 {
			os.Chmod(parent, 0o755)
			ipc := filepath.Join(parent, "ipc")
			if err := os.Mkdir(ipc, 0o700); err != nil {
				t.Fatal(err)
			}
			os.Chmod(parent, mode)
			t.Cleanup(func() { os.Chmod(parent, 0o755) })
			if err := ensureIPCRoot(ipc, me()); err != nil {
				t.Errorf("a 0555 parent: %v", err)
			}
			continue
		}
		if err := ensureIPCRoot(filepath.Join(parent, "ipc"), me()); err != nil {
			t.Errorf("parent mode %v refused: %v", mode, err)
		}
	}
	for _, mode := range []os.FileMode{0o770, 0o775, 0o757, 0o777} {
		parent := filepath.Join(base, "bad-"+mode.String())
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, mode); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(parent, "ipc")
		if err := ensureIPCRoot(path, me()); err == nil {
			t.Errorf("a parent of mode %v was accepted", mode)
		}
		if _, err := os.Lstat(path); err == nil {
			t.Errorf("a root was created below a parent of mode %v", mode)
		}
	}
	// A sticky world-writable parent owned by root is the /tmp case and is accepted, as for every other root.
	if err := ensureIPCRoot("relative/ipc", me()); err == nil {
		t.Fatal("a relative path was accepted")
	}
}

// A parent owned by someone else is refused; a missing chain is created one directory at a time.
func TestEnsureIPCRootForeignAncestor(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root is always an accepted ancestor owner")
	}
	base := canonicalTempDir(t)
	other := me()
	other.uid += 7 // neither root nor the runner's identity
	if err := ensureIPCRoot(filepath.Join(base, "ipc"), other); err == nil {
		t.Fatal("an ancestor owned by another user was accepted")
	}
	deep := filepath.Join(base, "a", "b", "ipc")
	if err := ensureIPCRoot(deep, me()); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(base, "a"), filepath.Join(base, "a", "b")} {
		if _, mode := modeOf(t, dir); mode.Perm() != 0o755 {
			t.Errorf("%s mode %v", dir, mode)
		}
	}
}
