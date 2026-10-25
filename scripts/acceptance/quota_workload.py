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


def require_edquot(error):
    if not isinstance(error, OSError) or error.errno != errno.EDQUOT:
        raise RuntimeError("expected kernel EDQUOT, not another write failure")


def fill_blocks(folder, unlinked=False, name="charged.data"):
    try:
        fd = os.open(folder / name, os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o600)
    except OSError as error:
        if name != "after-remount.data":
            raise
        require_edquot(error)
        return {"denial": "EDQUOT", "written_bytes": 0, "allocated_bytes": 0,
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
                require_edquot(error)
                blocks = os.fstat(fd).st_blocks * 512
                if blocks > LIMIT or written > LIMIT or (name != "after-remount.data" and written == 0):
                    raise RuntimeError("allocated data exceeded project hard limit")
                return {"denial": "EDQUOT", "written_bytes": written,
                        "allocated_bytes": blocks, "unlinked_open": unlinked}
        raise RuntimeError("byte writes reached bounded ceiling without EDQUOT")
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
            require_edquot(error)
            if created > 128:
                raise RuntimeError("created files exceeded inode hard limit")
            return {"denial": "EDQUOT", "created_files": created,
                    "inherited_nested_project": True}
    raise RuntimeError("file creation reached bounded ceiling without EDQUOT")


def healthy_write(folder, name):
    with (folder / name).open("xb", buffering=0) as stream:
        if stream.write(b"h" * (1024 * 1024)) != 1024 * 1024:
            raise RuntimeError("short independent-project write")
        os.fsync(stream.fileno())
    return {"written_bytes": 1024 * 1024}


def main(args):
    if sys.platform != "linux" or os.geteuid() != 10000 or os.getegid() != 10000 or os.getgroups():
        raise RuntimeError("workload requires the isolated unprivileged identity")
    if len(args) != 2 or args[0] not in {"bytes", "inodes", "unlinked", "after-remount", "healthy-bytes", "healthy-inodes", "healthy-unlinked"}:
        raise RuntimeError("unknown bounded quota workload")
    signal.alarm(30)
    folder = Path(args[1])
    if not folder.is_absolute() or folder.is_symlink() or folder.stat().st_uid != 10000:
        raise RuntimeError("unexpected workload directory")
    mode = args[0]
    if mode == "inodes":
        result = fill_inodes(folder)
    elif mode.startswith("healthy-"):
        result = healthy_write(folder, mode)
    else:
        result = fill_blocks(folder, unlinked=mode == "unlinked",
                             name="after-remount.data" if mode == "after-remount" else "charged.data")
    print(json.dumps({"mode": mode, **result}, sort_keys=True))


if __name__ == "__main__":
    main(sys.argv[1:])
