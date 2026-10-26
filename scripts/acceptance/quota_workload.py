#!/usr/bin/env python3
"""Unprivileged bounded workloads for the disposable project-quota probe."""
import errno
import fcntl
import json
import os
from pathlib import Path
import signal
import stat
import struct
import sys

LIMIT = 8 * 1024 * 1024
CHUNK = 65536


def require_project_quota_error(error, folder, capacity_fd=None):
    # XFS intentionally maps project hard-limit failures to ENOSPC. The owned
    # mount root has no project limit; never use the capped directory's statfs.
    if not isinstance(error, OSError) or error.errno not in {errno.EDQUOT, errno.ENOSPC}:
        raise RuntimeError("expected a native project quota denial")
    free = os.fstatvfs(capacity_fd) if capacity_fd is not None else os.statvfs(folder.parent)
    free_bytes = free.f_bavail * free.f_frsize
    if free_bytes < LIMIT * 8 or free.f_favail < 1024:
        raise RuntimeError("filesystem exhaustion cannot count as quota enforcement")
    return {"denial": errno.errorcode[error.errno], "filesystem_free_bytes": free_bytes,
            "filesystem_free_inodes": free.f_favail}


def fill_blocks(folder, unlinked=False, name="charged.data", capacity_fd=None):
    try:
        fd = os.open(folder / name, os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o600)
    except OSError as error:
        if name != "after-remount.data":
            raise
        denial = require_project_quota_error(error, folder, capacity_fd)
        occupied = (folder / "charged.data").stat().st_blocks * 512
        if not LIMIT - 1024 * 1024 <= occupied <= LIMIT:
            raise RuntimeError("remount denial did not retain the previously charged file")
        return {**denial, "written_bytes": 0, "allocated_bytes": occupied,
                "creation_denied_after_remount": True}
    written = 0
    try:
        if unlinked:
            os.unlink(folder / name)
        for offset in range(0, LIMIT * 4, CHUNK):
            try:
                os.posix_fallocate(fd, offset, CHUNK)
                if os.write(fd, b"q" * CHUNK) != CHUNK:
                    raise RuntimeError("short quota-probe write")
                os.fsync(fd)
                written += CHUNK
            except OSError as error:
                denial = require_project_quota_error(error, folder, capacity_fd)
                blocks = os.fstat(fd).st_blocks * 512
                occupied = (folder / "charged.data").stat().st_blocks * 512 if name == "after-remount.data" else blocks
                if blocks > LIMIT or written > LIMIT or occupied < LIMIT - 1024 * 1024:
                    raise RuntimeError("quota denial did not occur near the configured hard byte limit")
                return {**denial, "written_bytes": written,
                        "allocated_bytes": blocks, "unlinked_open": unlinked}
        raise RuntimeError("byte writes reached bounded ceiling without quota denial")
    finally:
        os.close(fd)


def fill_inodes(folder, capacity_fd=None):
    nested = folder / "nested"
    nested.mkdir()
    created = 0
    for index in range(512):
        try:
            fd = os.open(nested / f"inode-{index}", os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            os.close(fd)
            created += 1
        except OSError as error:
            denial = require_project_quota_error(error, folder, capacity_fd)
            if not 120 <= created <= 128:
                raise RuntimeError("inode denial did not occur near the configured hard limit")
            return {**denial, "created_files": created,
                    "inherited_nested_project": True}
    raise RuntimeError("file creation reached bounded ceiling without quota denial")


def healthy_write(folder, name):
    with (folder / name).open("xb", buffering=0) as stream:
        if stream.write(b"h" * (1024 * 1024)) != 1024 * 1024:
            raise RuntimeError("short independent-project write")
        os.fsync(stream.fileno())
    return {"written_bytes": 1024 * 1024}


def private_directory(arguments):
    if len(arguments) != 3 or arguments[0] != "--private-fds" or any(not item.isdecimal() or str(int(item)) != item or not 3 <= int(item) <= 1024 for item in arguments[1:]):
        raise RuntimeError("invalid private fixture descriptors")
    directory_fd, capacity_fd = map(int, arguments[1:])
    if directory_fd == capacity_fd:
        raise RuntimeError("private capacity must be independent")
    directory, capacity = os.fstat(directory_fd), os.fstat(capacity_fd)
    if not stat.S_ISDIR(directory.st_mode) or not stat.S_ISDIR(capacity.st_mode) or directory.st_uid != 10000 or capacity.st_uid != 0 or directory.st_dev != capacity.st_dev or directory.st_ino == capacity.st_ino:
        raise RuntimeError("private fixture descriptor identity mismatch")
    def project(fd):
        attributes = bytearray(28)
        fcntl.ioctl(fd, 0x801c581f, attributes, True)
        return struct.unpack_from("=I", attributes, 12)[0]
    if project(directory_fd) == 0 or project(capacity_fd) != 0:
        raise RuntimeError("capacity descriptor must observe the uncapped project")
    return Path(f"/proc/self/fd/{directory_fd}"), capacity_fd


def main(args):
    if sys.platform != "linux" or os.geteuid() != 10000 or os.getegid() != 10000 or os.getgroups():
        raise RuntimeError("workload requires the isolated unprivileged identity")
    if len(args) not in {2, 4} or args[0] not in {"bytes", "inodes", "unlinked", "after-remount", "healthy-bytes", "healthy-inodes", "healthy-unlinked", "relaxed-bytes", "relaxed-inodes", "released-unlinked"}:
        raise RuntimeError("unknown bounded quota workload")
    signal.alarm(30)
    capacity_fd = None
    if len(args) == 4:
        folder, capacity_fd = private_directory(args[1:])
    else:
        folder = Path(args[1])
        if not folder.is_absolute() or folder.is_symlink() or folder.stat().st_uid != 10000:
            raise RuntimeError("unexpected workload directory")
    mode = args[0]
    if mode == "inodes":
        result = fill_inodes(folder, capacity_fd)
    elif mode == "relaxed-inodes":
        fd = os.open(folder / "nested" / "relaxed-inode", os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        os.close(fd)
        result = {"created_files": 1, "prior_denial_reversed_by_limit_change": True}
    elif mode.startswith("healthy-") or mode in {"relaxed-bytes", "released-unlinked"}:
        result = healthy_write(folder, mode)
    else:
        result = fill_blocks(folder, unlinked=mode == "unlinked",
                             name="after-remount.data" if mode == "after-remount" else "charged.data", capacity_fd=capacity_fd)
    print(json.dumps({"mode": mode, **result}, sort_keys=True))


if __name__ == "__main__":
    main(sys.argv[1:])
