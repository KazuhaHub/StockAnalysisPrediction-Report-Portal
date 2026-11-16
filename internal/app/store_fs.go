package app

// Filesystem identification for the SQLite read pool (see openReadPool): WAL is only safe on a
// filesystem whose locking and shared-memory mapping behave as the local ones do.

// fsKind describes one filesystem type.
type fsKind struct {
	name string
	// unsafeForWAL is true for network filesystems and for the FUSE family (which is what Docker
	// Desktop, virtiofs, sshfs and most userspace mounts present as): file locking and memory-mapped
	// shared state are not reliable there, and SQLite's own documentation excludes them from WAL.
	unsafeForWAL bool
}

// linuxFilesystems maps statfs f_type magic numbers (linux/magic.h) to what the read pool needs to know.
// A type that is not listed is treated as LOCAL, so it gets WAL and the read pool: the table names what is
// known to fail and is not exhaustive. The only fail-safe is the error path (a statfs error, or a platform
// with no implementation), which disables WAL and the pool.
var linuxFilesystems = map[uint32]fsKind{
	0x6969:     {"nfs", true},
	0x517B:     {"smb", true},
	0xFF534D42: {"cifs", true},
	0xFE534D42: {"smb2", true},
	0x65735546: {"fuse", true}, // FUSE: virtiofs, gRPC FUSE, sshfs, s3fs, ...
	0x65735543: {"fuse-ctl", true},
	0x01021997: {"9p", true}, // Docker Desktop and WSL host mounts
	0x5346414F: {"afs", true},
	0x00C36400: {"ceph", true},
	// Cluster and parallel filesystems: shared memory and locking are not coherent across nodes the way WAL needs.
	0x0BD00BD0: {"lustre", true},
	0x47504653: {"gpfs", true},
	0x7461636F: {"ocfs2", true},
	0x01161970: {"gfs2", true},
	0x19830326: {"beegfs", true},
	0x20030528: {"orangefs", true},
	0x786F4256: {"vboxsf", true},
	0x6B414653: {"afs-kafs", true},
	0x73757245: {"coda", true},
	0x4D44:     {"msdos", false},
	0xEF53:     {"ext4", false},
	0x58465342: {"xfs", false},
	0x9123683E: {"btrfs", false},
	0x01021994: {"tmpfs", false},
	0x794C7630: {"overlayfs", false},
	0x2FC12FC1: {"zfs", false},
}

// classifyLinuxFilesystem names a statfs f_type and says whether WAL is unsafe on it.
func classifyLinuxFilesystem(magic uint32) (name string, unsafeForWAL bool) {
	if k, ok := linuxFilesystems[magic]; ok {
		return k.name, k.unsafeForWAL
	}
	return "unlisted", false
}

// sqliteFilesystemCheck identifies the filesystem a database file is on: its name, whether WAL is unsafe
// there, and an error when it cannot be identified (the caller then runs without WAL). It is a variable
// so a test can stand in for a mount it does not have.
var sqliteFilesystemCheck = detectSQLiteFilesystem
