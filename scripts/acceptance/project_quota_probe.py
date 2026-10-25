#!/usr/bin/env python3
"""Real XFS quota enforcement on a newly owned disposable CI loopback image only."""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

PATH = "/usr/sbin:/usr/bin:/sbin:/bin"
ENV = {"PATH": PATH, "LC_ALL": "C"}


def command(*args):
    result = subprocess.run(args, env=ENV, check=True, capture_output=True, text=True, timeout=60)
    if len(result.stdout) + len(result.stderr) > 65536:
        raise RuntimeError("unexpected probe command output size")
    return result.stdout.strip()


def require_disposable(args):
    if args != ["--disposable-github-worker"] or sys.platform != "linux" or os.geteuid() != 0:
        raise RuntimeError("probe requires explicit disposable Linux root acceptance")
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise RuntimeError("probe refuses hosts outside a disposable GitHub-hosted worker")
    for tool in ("losetup", "mkfs.xfs", "mount", "umount", "findmnt", "xfs_quota", "setpriv", "python3"):
        if not shutil.which(tool, path=PATH):
            raise RuntimeError("missing explicit acceptance tool")


def require_loop_owner(loop, image):
    if not re.fullmatch(r"/dev/loop[0-9]+", loop):
        raise RuntimeError("unexpected loop device")
    data = json.loads(command("losetup", "--json", "--list", "--output", "NAME,BACK-FILE", loop))
    devices = data.get("loopdevices", [])
    if len(devices) != 1 or devices[0].get("name") != loop or devices[0].get("back-file") != str(image):
        raise RuntimeError("loop ownership no longer matches this newly created image")


def require_mount_owner(mountpoint, loop):
    data = json.loads(command("findmnt", "--json", "--mountpoint", str(mountpoint), "--output", "SOURCE,FSTYPE,OPTIONS"))
    mounts = data.get("filesystems", [])
    if len(mounts) != 1 or mounts[0].get("source") != loop or mounts[0].get("fstype") != "xfs":
        raise RuntimeError("mount does not identify the owned XFS loop device")
    options = set(mounts[0].get("options", "").split(","))
    if not options.intersection({"prjquota", "pquota"}) or not {"nodev", "nosuid", "noexec"}.issubset(options):
        raise RuntimeError("owned mount lacks required explicit options")


def main(args):
    require_disposable(args)
    root = Path(tempfile.mkdtemp(prefix="portal-quota-acceptance-", dir="/tmp"))
    # Only synthetic fixture paths live here; permit the test UID to traverse.
    root.chmod(0o755)
    image = root / "owned.xfs"
    mountpoint = root / "mount"
    mountpoint.mkdir()
    loop = None
    mounted = False
    report = {"profile": "xfs-project-quota/disposable-v1-dev", "kernel": os.uname().release,
              "architecture": os.uname().machine, "execution_ready": False, "workloads": []}
    try:
        with image.open("xb") as stream:
            stream.truncate(1024 * 1024 * 1024)
        image.chmod(0o600)
        loop = command("losetup", "--find", "--show", str(image))
        require_loop_owner(loop, image)
        report["mkfs_version"] = command("mkfs.xfs", "-V")
        command("mkfs.xfs", "-f", loop)
        command("mount", "-t", "xfs", "-o", "prjquota,nodev,nosuid,noexec", loop, str(mountpoint))
        mounted = True
        require_mount_owner(mountpoint, loop)
        quota_state = command("xfs_quota", "-x", "-c", "state -p", str(mountpoint))
        if "Accounting: ON" not in quota_state or "Enforcement: ON" not in quota_state:
            raise RuntimeError("project quota accounting/enforcement not both enabled")
        report["project_quota_accounting"] = True
        report["project_quota_enforcement"] = True
        report["limits"] = {"bytes": 8 * 1024 * 1024, "inodes": 128}
        folders = {}
        for index, name in enumerate(("bytes", "inodes", "unlinked", "healthy"), 4101):
            folder = mountpoint / name
            folder.mkdir()
            command("xfs_quota", "-x", "-c", f"project -s -p {folder} {index}", str(mountpoint))
            command("xfs_quota", "-x", "-c", f"limit -p bhard=8m ihard=128 {index}", str(mountpoint))
            os.chown(folder, 10000, 10000)
            folder.chmod(0o700)
            folders[name] = folder
        workload = Path(__file__).with_name("quota_workload.py").resolve()

        def run_workload(mode, folder):
            output = command("setpriv", "--reuid=10000", "--regid=10000", "--clear-groups",
                             "--no-new-privs", "/usr/bin/python3", str(workload), mode, str(folder))
            result = json.loads(output)
            if result.get("mode") != mode:
                raise RuntimeError("unexpected workload result identity")
            report["workloads"].append(result)

        for mode in ("bytes", "inodes", "unlinked"):
            run_workload(mode, folders[mode])
            run_workload("healthy-" + mode, folders["healthy"])
        require_loop_owner(loop, image)
        require_mount_owner(mountpoint, loop)
        command("umount", str(mountpoint))
        mounted = False
        command("mount", "-t", "xfs", "-o", "prjquota,nodev,nosuid,noexec", loop, str(mountpoint))
        mounted = True
        require_mount_owner(mountpoint, loop)
        run_workload("after-remount", folders["bytes"])
        report["remount_enforcement"] = True
    finally:
        # Never unmount, detach or delete a resource whose ownership changed.
        # On cleanup failure preserve it for this ephemeral worker's teardown.
        if mounted:
            require_loop_owner(loop, image)
            require_mount_owner(mountpoint, loop)
            command("umount", str(mountpoint))
        if loop is not None:
            require_loop_owner(loop, image)
            command("losetup", "--detach", loop)
        shutil.rmtree(root)
    report["owned_resources_cleaned"] = True
    print(json.dumps(report, sort_keys=True, indent=2))


if __name__ == "__main__":
    main(sys.argv[1:])
