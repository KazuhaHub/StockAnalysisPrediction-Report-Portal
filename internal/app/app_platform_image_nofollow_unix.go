//go:build !windows

package app

import (
	"errors"
	"os"
	"syscall"
)

// platformNoFollow makes an open refuse a symbolic link in the final path element.
const platformNoFollow = syscall.O_NOFOLLOW

// platformCheckStoreDir requires an image store directory to be a real directory owned by the service user, and
// tightens a mode that lets anyone else in.
func platformCheckStoreDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("image store directory is not a real directory owned by the service user")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return os.Chmod(path, 0o700)
	}
	return nil
}
