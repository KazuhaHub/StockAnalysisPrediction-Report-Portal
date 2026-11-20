// Package rootfs reports what kind of filesystem a runner root lives on. It is diagnostic only: nothing here
// grants or widens access, and the data layer keeps its own XFS and project-quota checks.
package rootfs

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported is returned on a platform without the Linux statfs and sysfs facts.
var ErrUnsupported = errors.New("filesystem inspection requires Linux")

// KindXFS is the only kind the data layer accepts as a root.
const KindXFS = "xfs"

// Info describes the filesystem holding a path.
type Info struct {
	// Kind is the short filesystem name ("xfs", "ext4", ...) or "other(0x...)" for a magic this package does
	// not name.
	Kind string
	// LoopBacked is set when the filesystem sits on a loop device. BackingKind is then the kind of the
	// filesystem holding the loop's backing file, empty when that could not be read.
	LoopBacked  bool
	BackingKind string
}

// Describe is a path-free one-line summary, such as "xfs loop-backed on ext4".
func (i Info) Describe() string {
	if !i.LoopBacked {
		return i.Kind
	}
	backing := i.BackingKind
	if backing == "" {
		backing = "unknown"
	}
	return fmt.Sprintf("%s loop-backed on %s", i.Kind, backing)
}

var kindNames = map[int64]string{
	0x58465342: "xfs",
	0xef53:     "ext4", // also ext2/ext3: the magic is shared
	0x9123683e: "btrfs",
	0x01021994: "tmpfs",
	0x794c7630: "overlay",
	0x2fc12fc1: "zfs",
	0x6969:     "nfs",
	0x65735546: "fuse",
	0x4d44:     "vfat",
	0x2011bab0: "exfat",
	0xf2f52010: "f2fs",
	0x73717368: "squashfs",
	0x52654973: "reiserfs",
	0x3153464a: "jfs",
	0xff534d42: "cifs",
	0xc36400:   "ceph",
	0xca451a4e: "bcachefs",
}

// KindName names a statfs filesystem magic.
func KindName(magic int64) string {
	if name, ok := kindNames[magic&0xffffffff]; ok {
		return name
	}
	return fmt.Sprintf("other(%#x)", magic&0xffffffff)
}

// loopBackingPath is the sysfs file that names a loop device's backing file.
func loopBackingPath(sysRoot string, major, minor uint32) string {
	return fmt.Sprintf("%s/dev/block/%d:%d/loop/backing_file", strings.TrimRight(sysRoot, "/"), major, minor)
}
