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


class ProbeCommandError(RuntimeError):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


def command(*args):
    result = subprocess.run(args, env=ENV, check=False, capture_output=True, text=True, timeout=60)
    if len(result.stdout) + len(result.stderr) > 65536:
        raise RuntimeError("unexpected probe command output size")
    if result.returncode:
        raise ProbeCommandError(result.returncode, f"{Path(args[0]).name} failed ({result.returncode}): {result.stderr[:2048]}")
    return result.stdout.strip()


def require_disposable(args):
    if not (args == ["--disposable-github-worker"] or (len(args)==5 and args[:2]==["--disposable-github-worker", "--native-binary"] and args[3]=="--abi-json" and Path(args[2]).is_absolute() and Path(args[4]).is_absolute())) or sys.platform != "linux" or os.geteuid() != 0:
        raise RuntimeError("probe requires explicit disposable Linux root acceptance")
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise RuntimeError("probe refuses hosts outside a disposable GitHub-hosted worker")
    for tool in ("losetup", "mkfs.xfs", "mount", "umount", "findmnt", "xfs_quota", "xfs_admin", "blkid", "setpriv", "python3"):
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


def owned_mount_present(mountpoint, loop):
    try:
        require_mount_owner(mountpoint, loop)
    except ProbeCommandError as error:
        if error.code == 1:  # findmnt reports no exact mount point.
            return False
        raise
    return True


def discover_owned_loop(image):
    data = json.loads(command("losetup", "--json", "--list", "--associated", str(image), "--output", "NAME,BACK-FILE"))
    devices = data.get("loopdevices", [])
    if not devices:
        return None
    if len(devices) != 1 or devices[0].get("back-file") != str(image):
        raise RuntimeError("uncertain loop allocation requires worker teardown")
    loop = devices[0].get("name", "")
    require_loop_owner(loop, image)
    return loop


def replace_owned_uuid(mountpoint, loop, image):
    # Only the newly created image can be mutated, and only while unmounted.
    require_loop_owner(loop, image)
    require_mount_owner(mountpoint, loop)
    command("umount", str(mountpoint))
    if owned_mount_present(mountpoint, loop):
        raise RuntimeError("owned filesystem remained mounted before UUID replacement")
    require_loop_owner(loop, image)
    command("xfs_admin", "-U", "generate", loop)
    require_loop_owner(loop, image)
    command("mount", "-t", "xfs", "-o", "prjquota,nodev,nosuid,noexec", loop, str(mountpoint))
    require_mount_owner(mountpoint, loop)


def main(args):
    require_disposable(args)
    root = Path(tempfile.mkdtemp(prefix="portal-quota-acceptance-", dir="/tmp"))
    # Only synthetic fixture paths live here; permit the test UID to traverse.
    root.chmod(0o755)
    image = root / "owned.xfs"
    mountpoint = root / "mount"
    mountpoint.mkdir()
    loop = None
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
        native_root = None
        if len(args) == 5:
            native_root = mountpoint / "native-data"
            native_root.mkdir(mode=0o700)
            native_env = dict(ENV, APP_DATA_NATIVE_UUID=command("blkid", "-p", "-s", "UUID", "-o", "value", loop), APP_DATA_NATIVE_INODE=str(native_root.stat().st_ino), GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted", APP_DATA_NATIVE_ROOT=str(native_root), APP_DATA_NATIVE_ABI=args[4])
            native = subprocess.run([args[2], "-test.run=^TestNativeDataPrimitive$", "-test.v", "-test.timeout=30s"], env=native_env, capture_output=True, text=True, timeout=45)
            if native.returncode or "--- PASS: TestNativeDataPrimitive" not in native.stdout:
                raise RuntimeError("native data preparation failed: " + (native.stdout+native.stderr)[-4096:])
            report["native_go_data_prepared"] = True
            report["native_go_root_identity_checked"] = True
        # The synthetic UID must not depend on traversing the runner's private
        # checkout/home. Keep the reviewed workload in the root-owned fixture.
        workload = root / "workload.py"
        workload.write_bytes(Path(__file__).with_name("quota_workload.py").read_bytes())
        workload.chmod(0o444)

        def run_workload(mode, folder, private=False):
            if private:
                directory_fd = os.open(folder, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
                capacity_fd = os.open(mountpoint, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
                try:
                    result = subprocess.run(["setpriv", "--reuid=10000", "--regid=10000", "--clear-groups", "--no-new-privs", "/usr/bin/python3", str(workload), mode, "--private-fds", str(directory_fd), str(capacity_fd)], pass_fds=(directory_fd, capacity_fd), env=ENV, capture_output=True, text=True, timeout=45)
                finally:
                    os.close(directory_fd)
                    os.close(capacity_fd)
                if result.returncode:
                    raise RuntimeError("native quota workload failed: " + result.stderr[-2048:])
                output = result.stdout
            else:
                output = command("setpriv", "--reuid=10000", "--regid=10000", "--clear-groups",
                                 "--no-new-privs", "/usr/bin/python3", str(workload), mode, str(folder))
            result = json.loads(output)
            if result.get("mode") != mode:
                raise RuntimeError("unexpected workload result identity")
            report["workloads"].append(result)

        for mode in ("bytes", "inodes", "unlinked"):
            run_workload(mode, folders[mode])
            run_workload("healthy-" + mode, folders["healthy"])
        if native_root is not None:
            native_folders = [native_root / f"00000000-0000-0000-0000-{i:012d}" for i in range(1,5)]
            # Writer access is synthetic test setup, not part of Prepare. The
            # provider leaves roots sealed until future journal/writer admission.
            for folder in native_folders:
                os.chown(folder, 10000, 10000)
            for i, mode in enumerate(("bytes", "inodes", "unlinked")):
                run_workload(mode, native_folders[i], private=True)
                run_workload("healthy-"+mode, native_folders[3], private=True)
            for folder in native_folders:
                os.chown(folder, 0, 0)
            report["native_go_unprivileged_enforcement"] = True
        require_loop_owner(loop, image)
        require_mount_owner(mountpoint, loop)
        command("umount", str(mountpoint))
        command("mount", "-t", "xfs", "-o", "prjquota,nodev,nosuid,noexec", loop, str(mountpoint))
        require_mount_owner(mountpoint, loop)
        run_workload("after-remount", folders["bytes"])
        report["remount_enforcement"] = True
        if native_root is not None:
            native_env["APP_DATA_NATIVE_PHASE"] = "inspect"
            native = subprocess.run([args[2], "-test.run=^TestNativeDataPrimitive$", "-test.v", "-test.timeout=30s"], env=native_env, capture_output=True, text=True, timeout=45)
            if native.returncode or "--- PASS: TestNativeDataPrimitive" not in native.stdout:
                raise RuntimeError("native data reopen inspection failed: " + (native.stdout+native.stderr)[-4096:])
            report["native_go_reopen_after_remount"] = True
            command("xfs_quota", "-x", "-c", "disable -p", str(mountpoint))
            native_env["APP_DATA_NATIVE_PHASE"] = "disabled"
            native = subprocess.run([args[2], "-test.run=^TestNativeDataPrimitive$", "-test.v", "-test.timeout=30s"], env=native_env, capture_output=True, text=True, timeout=45)
            if native.returncode or "--- PASS: TestNativeDataPrimitive" not in native.stdout:
                raise RuntimeError("native data accepted disabled enforcement: " + (native.stdout+native.stderr)[-4096:])
            command("xfs_quota", "-x", "-c", "enable -p", str(mountpoint))
            report["native_go_detects_disabled_enforcement"] = True
        # Keep the original exhausted files. A raised project limit, rather than
        # cleanup or a newly selected filesystem, must reverse each denial.
        command("xfs_quota", "-x", "-c", "limit -p bhard=16m ihard=128 4101", str(mountpoint))
        run_workload("relaxed-bytes", folders["bytes"])
        command("xfs_quota", "-x", "-c", "limit -p bhard=8m ihard=256 4102", str(mountpoint))
        run_workload("relaxed-inodes", folders["inodes"])
        run_workload("released-unlinked", folders["unlinked"])
        report["same_project_limit_controls"] = True
        if native_root is not None:
            replace_owned_uuid(mountpoint, loop, image)
            changed_uuid = command("blkid", "-p", "-s", "UUID", "-o", "value", loop)
            if changed_uuid == native_env["APP_DATA_NATIVE_UUID"]:
                raise RuntimeError("owned UUID replacement did not take effect")
            native_env["APP_DATA_NATIVE_PHASE"] = "replaced"
            native = subprocess.run([args[2], "-test.run=^TestNativeDataPrimitive$", "-test.v", "-test.timeout=30s"], env=native_env, capture_output=True, text=True, timeout=45)
            if native.returncode or "--- PASS: TestNativeDataPrimitive" not in native.stdout:
                raise RuntimeError("native data accepted replaced filesystem identity: " + (native.stdout+native.stderr)[-4096:])
            report["native_go_rejects_changed_filesystem_uuid"] = True
    finally:
        # Never unmount, detach or delete a resource whose ownership changed.
        # On cleanup failure preserve it for this ephemeral worker's teardown.
        # A failed/timed-out command can still have attached or mounted. Observe
        # the owned resources instead of trusting an acknowledgement boolean.
        if loop is None and image.exists():
            loop = discover_owned_loop(image)
        if loop is not None:
            require_loop_owner(loop, image)
            if owned_mount_present(mountpoint, loop):
                command("umount", str(mountpoint))
            if owned_mount_present(mountpoint, loop):
                raise RuntimeError("owned filesystem remained mounted after unmount")
            require_loop_owner(loop, image)
            command("losetup", "--detach", loop)
            if discover_owned_loop(image) is not None:
                raise RuntimeError("owned loop attachment remains after detach")
        shutil.rmtree(root)
    report["owned_resources_cleaned"] = True
    print(json.dumps(report, sort_keys=True, indent=2))


if __name__ == "__main__":
    main(sys.argv[1:])
