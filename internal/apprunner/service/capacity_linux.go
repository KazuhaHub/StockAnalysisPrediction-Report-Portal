//go:build linux

package service

import "golang.org/x/sys/unix"

// volumeBytes is the size of the filesystem holding path, or zero when it cannot be read. Zero is never a size:
// DeriveBudgets reports it as an unreadable host fact and the runner is not ready. The size is blocks times the
// fragment size (Frsize), the unit the block counts are in; Bsize is only the preferred transfer size.
func volumeBytes(path string) int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil || st.Frsize <= 0 {
		return 0
	}
	size := int64(st.Blocks) * int64(st.Frsize)
	if size < 0 {
		return 0
	}
	return size
}

// sameFilesystem reports whether two paths are on one filesystem. Unreadable paths are reported as separate, so
// the stricter check applies.
func sameFilesystem(a, b string) bool {
	var x, y unix.Stat_t
	if unix.Stat(a, &x) != nil || unix.Stat(b, &y) != nil {
		return false
	}
	return x.Dev == y.Dev
}
