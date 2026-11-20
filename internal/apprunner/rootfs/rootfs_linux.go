//go:build linux

package rootfs

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const loopMajor = 7

// Inspect reads the filesystem kind of path and, for a loop-mounted filesystem, the kind of the filesystem that
// holds the backing file (from /sys/dev/block/7:N/loop/backing_file).
func Inspect(path string) (Info, error) {
	return inspect("/sys", path)
}

func inspect(sysRoot, path string) (Info, error) {
	var fs unix.Statfs_t
	var st unix.Stat_t
	if err := unix.Statfs(path, &fs); err != nil {
		return Info{}, err
	}
	if err := unix.Stat(path, &st); err != nil {
		return Info{}, err
	}
	info := Info{Kind: KindName(int64(fs.Type))}
	major, minor := unix.Major(uint64(st.Dev)), unix.Minor(uint64(st.Dev))
	if major != loopMajor {
		return info, nil
	}
	info.LoopBacked = true
	raw, err := os.ReadFile(loopBackingPath(sysRoot, major, minor))
	if err != nil {
		return info, nil
	}
	backing := strings.TrimSpace(string(raw))
	var backingFS unix.Statfs_t
	if backing != "" && unix.Statfs(backing, &backingFS) == nil {
		info.BackingKind = KindName(int64(backingFS.Type))
	}
	return info, nil
}
