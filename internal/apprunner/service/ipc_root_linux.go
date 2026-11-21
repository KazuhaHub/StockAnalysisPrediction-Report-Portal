//go:build linux

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ipcIdentity is who owns the runner's directories: root:root in production. Tests substitute their own
// identity so the same code runs unprivileged.
type ipcIdentity struct{ uid, gid uint32 }

var rootIdentity = ipcIdentity{}

// ensureIPCRoot makes the application IPC root exist before anything uses it (doc 187 section 7), because it
// lives on /run and does not survive a reboot.
//
//   - A missing ipc_root is created as root:root 0700, whatever the process umask.
//   - A missing parent is created as root:root 0755, one directory at a time.
//   - An existing parent is accepted when it is a real directory owned by root (or the runner's identity) that
//     is not group- or world-writable, the ancestor rule every other root uses. Doc 146's layout, root:<gid>
//     0750, and a fresh 0755 therefore both pass.
//   - An existing ipc_root with any other owner, group or mode, or that is not a real directory, is refused
//     and left as it is.
//
// Nothing is removed or repaired.
func ensureIPCRoot(path string, who ipcIdentity) error {
	if !validAbsolute(path) || path == "/" {
		return errors.New("ipc_root is not a clean absolute path")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := "/"
	for i, part := range parts {
		next := filepath.Join(current, part)
		leaf := i == len(parts)-1
		info, err := os.Lstat(next)
		switch {
		case err == nil:
			if err := checkIPCDirectory(next, info, leaf, who); err != nil {
				return err
			}
		case errors.Is(err, os.ErrNotExist):
			if err := makeIPCDirectory(next, leaf, who); err != nil {
				return err
			}
		default:
			return fmt.Errorf("cannot read %s: %w", next, err)
		}
		current = next
	}
	return nil
}

func checkIPCDirectory(path string, info os.FileInfo, leaf bool, who ipcIdentity) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return fmt.Errorf("%s is not a real directory", path)
	}
	if leaf {
		if st.Uid != who.uid || st.Gid != who.gid || info.Mode().Perm() != 0o700 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return fmt.Errorf("ipc_root exists but is not owned root:root with mode 0700")
		}
		return nil
	}
	if st.Uid != 0 && st.Uid != who.uid {
		return fmt.Errorf("the directory %s above ipc_root must be owned by root", path)
	}
	if info.Mode().Perm()&0o022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
		return fmt.Errorf("the directory %s above ipc_root must not be group- or world-writable", path)
	}
	return nil
}

func makeIPCDirectory(path string, leaf bool, who ipcIdentity) error {
	mode := os.FileMode(0o755)
	if leaf {
		mode = 0o700
	}
	if err := os.Mkdir(path, mode); err != nil {
		return fmt.Errorf("cannot create %s: %w", path, err)
	}
	// The process umask (0077 under the entry) must not decide the mode, and the directory must be ours.
	if err := os.Chown(path, int(who.uid), int(who.gid)); err != nil {
		return fmt.Errorf("cannot own %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("cannot set the mode of %s: %w", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return checkIPCDirectory(path, info, leaf, who)
}
