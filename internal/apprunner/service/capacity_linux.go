//go:build linux

package service

import "golang.org/x/sys/unix"

// volumeBytes is the size of the filesystem holding path, or zero when it cannot be read. Zero is never a size:
// DeriveBudgets reports it as an unreadable host fact and the runner is not ready.
func volumeBytes(path string) int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil || st.Bsize <= 0 {
		return 0
	}
	size := int64(st.Blocks) * int64(st.Bsize)
	if size < 0 {
		return 0
	}
	return size
}
