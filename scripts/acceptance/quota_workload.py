#!/usr/bin/env python3
"""Unprivileged bounded workloads for the disposable project-quota probe."""
import errno
import json
import os
from pathlib import Path
import signal
import sys

LIMIT = 8 * 1024 * 1024
CHUNK = 65536


def require_project_quota_error(error, folder):
    # XFS intentionally maps project hard-limit failures to ENOSPC. The owned
    # mount root has no project limit; never use the capped directory's statfs.
    if not isinstance(error, OSError) or error.errno not in {errno.EDQUOT, errno.ENOSPC}:
        raise RuntimeError("expected a native project quota denial")
    free = os.statvfs(folder.parent)
    free_bytes = free.f_bavail * free.f_frsize
    if free_bytes < LIMIT * 8 or free.f_favail < 1024:
        raise RuntimeError("filesystem exhaustion cannot count as quota enforcement")
    return {"denial": errno.errorcode[error.errno], "filesystem_free_bytes": free_bytes,
            "filesystem_free_inodes": free.f_favail}


def fill_blocks(folder, unlinked=False, name="charged.data"):
    try:
        fd = os.open(folder / name, os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o600)
    except OSError as error:
        if name != "after-remount.data":
            raise
        denial = require_project_quota_error(error, folder)
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
                denial = require_project_quota_error(error, folder)
                blocks = os.fstat(fd).st_blocks * 512
                occupied = (folder / "charged.data").stat().st_blocks * 512 if name == "after-remount.data" else blocks
                if blocks > LIMIT or written > LIMIT or occupied < LIMIT - 1024 * 1024:
                    raise RuntimeError("quota denial did not occur near the configured hard byte limit")
                return {**denial, "written_bytes": written,
                        "allocated_bytes": blocks, "unlinked_open": unlinked}
        raise RuntimeError("byte writes reached bounded ceiling without quota denial")
    finally:
        os.close(fd)


def fill_inodes(folder):
    nested = folder / "nested"
    nested.mkdir()
    created = 0
    for index in range(512):
        try:
            fd = os.open(nested / f"inode-{index}", os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            os.close(fd)
            created += 1
        except OSError as error:
            denial = require_project_quota_error(error, folder)
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


def main(args):
    if sys.platform != "linux" or os.geteuid() != 10000 or os.getegid() != 10000 or os.getgroups():
        raise RuntimeError("workload requires the isolated unprivileged identity")
    if len(args) != 2 or args[0] not in {"bytes", "inodes", "unlinked", "after-remount", "healthy-bytes", "healthy-inodes", "healthy-unlinked", "relaxed-bytes", "relaxed-inodes", "released-unlinked"}:
        raise RuntimeError("unknown bounded quota workload")
    signal.alarm(30)
    folder = Path(args[1])
    if not folder.is_absolute() or folder.is_symlink() or folder.stat().st_uid != 10000:
        raise RuntimeError("unexpected workload directory")
    mode = args[0]
    if mode == "inodes":
        result = fill_inodes(folder)
    elif mode == "relaxed-inodes":
        fd = os.open(folder / "nested" / "relaxed-inode", os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        os.close(fd)
        result = {"created_files": 1, "prior_denial_reversed_by_limit_change": True}
    elif mode.startswith("healthy-") or mode in {"relaxed-bytes", "released-unlinked"}:
        result = healthy_write(folder, mode)
    else:
        result = fill_blocks(folder, unlinked=mode == "unlinked",
                             name="after-remount.data" if mode == "after-remount" else "charged.data")
    print(json.dumps({"mode": mode, **result}, sort_keys=True))


if __name__ == "__main__":
    main(sys.argv[1:])
