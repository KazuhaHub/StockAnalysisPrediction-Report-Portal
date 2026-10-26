import errno
import importlib.util
from pathlib import Path
import tempfile
import stat
import struct
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).parent / "acceptance/quota_workload.py"
SPEC = importlib.util.spec_from_file_location("quota_workload", SOURCE)
workload = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(workload)


PROBE_SPEC = importlib.util.spec_from_file_location("project_quota_probe", SOURCE.with_name("project_quota_probe.py"))
probe = importlib.util.module_from_spec(PROBE_SPEC)
PROBE_SPEC.loader.exec_module(probe)


class ProjectQuotaProbeTests(unittest.TestCase):
    def test_only_native_denials_with_real_filesystem_capacity_count(self):
        ample = SimpleNamespace(f_bavail=100000, f_frsize=4096, f_favail=10000)
        with patch.object(workload.os, "statvfs", return_value=ample):
            for number in (errno.EDQUOT, errno.ENOSPC):
                self.assertEqual(workload.require_project_quota_error(OSError(number, "quota"), Path("/owned/project"))["denial"], errno.errorcode[number])
            for error in [PermissionError(errno.EACCES, "permission"), RuntimeError("failure")]:
                with self.assertRaisesRegex(RuntimeError, "native project quota"):
                    workload.require_project_quota_error(error, Path("/owned/project"))
        for space in [SimpleNamespace(f_bavail=0, f_frsize=4096, f_favail=10000), SimpleNamespace(f_bavail=100000, f_frsize=4096, f_favail=0)]:
            with patch.object(workload.os, "statvfs", return_value=space):
                with self.assertRaisesRegex(RuntimeError, "filesystem exhaustion"):
                    workload.require_project_quota_error(OSError(errno.ENOSPC, "disk full"), Path("/owned/project"))

    def test_opt_in_and_host_refuse_before_any_mutation(self):
        with patch.object(probe, "command") as command, patch.object(probe.tempfile, "mkdtemp") as temporary:
            with self.assertRaises(RuntimeError):
                probe.main([])
            with patch.object(probe.sys, "platform", "linux"), patch.object(probe.os, "geteuid", return_value=0), patch.dict(probe.os.environ, {}, clear=True):
                with self.assertRaisesRegex(RuntimeError, "outside a disposable"):
                    probe.main(["--disposable-github-worker"])
            command.assert_not_called()
            temporary.assert_not_called()

    def test_private_workload_requires_distinct_owned_same_filesystem_descriptors(self):
        directory = SimpleNamespace(st_mode=stat.S_IFDIR, st_uid=10000, st_dev=7, st_ino=1)
        capacity = SimpleNamespace(st_mode=stat.S_IFDIR, st_uid=0, st_dev=7, st_ino=2)
        def project(fd, request, buffer, mutate):
            struct.pack_into("=I", buffer, 12, 4201 if fd == 3 else 0)
        with patch.object(workload.os, "fstat", side_effect=[directory, capacity]), patch.object(workload.fcntl, "ioctl", side_effect=project):
            folder, fd = workload.private_directory(["--private-fds", "3", "4"])
            self.assertEqual(str(folder), "/proc/self/fd/3")
            self.assertEqual(fd, 4)
        for args in [["--private-fds", "3", "3"], ["--private-fds", "03", "4"], ["--private-fds", "2", "4"]]:
            with self.assertRaises(RuntimeError):
                workload.private_directory(args)
        capacity.st_dev = 8
        with patch.object(workload.os, "fstat", side_effect=[directory, capacity]):
            with self.assertRaisesRegex(RuntimeError, "identity mismatch"):
                workload.private_directory(["--private-fds", "3", "4"])

    def test_private_capacity_uses_uncapped_fd_not_capped_namespace_parent(self):
        ample = SimpleNamespace(f_bavail=100000, f_frsize=4096, f_favail=10000)
        with patch.object(workload.os, "fstatvfs", return_value=ample) as capacity, patch.object(workload.os, "statvfs") as by_path:
            workload.require_project_quota_error(OSError(errno.ENOSPC, "quota"), Path("/proc/self/fd/3"), 4)
            capacity.assert_called_once_with(4)
            by_path.assert_not_called()

    def test_native_binary_arguments_still_require_exact_disposable_opt_in(self):
        with patch.object(probe, "command") as command, patch.object(probe.tempfile, "mkdtemp") as temporary:
            for args in [
                ["--native-binary", "/tmp/native"],
                ["--disposable-github-worker", "--native-binary", "relative", "--abi-json", "/tmp/abi.json"],
                ["--disposable-github-worker", "--native-binary", "/tmp/native", "--abi-json", "relative"],
                ["--disposable-github-worker", "--native-binary", "/tmp/native", "--abi-json", "/tmp/abi.json", "--journal-binary", "relative"],
                ["--disposable-github-worker", "--native-binary", "/tmp/native", "--abi-json", "/tmp/abi.json", "--unknown", "/tmp/journal"],
                ["--disposable-github-worker", "--native-binary", "/tmp/native", "--abi-json", "/tmp/abi.json", "--journal-binary", "/tmp/journal", "--transport-binary", "relative"],
                ["--disposable-github-worker", "--native-binary", "/tmp/native", "--abi-json", "/tmp/abi.json", "--journal-binary", "/tmp/journal", "--wrong", "/tmp/transport"],
            ]:
                with self.assertRaises(RuntimeError):
                    probe.main(args)
            with patch.object(probe.sys, "platform", "linux"), patch.object(probe.os, "geteuid", return_value=0), patch.dict(probe.os.environ, {}, clear=True):
                with self.assertRaisesRegex(RuntimeError, "outside a disposable"):
                    probe.main(["--disposable-github-worker", "--native-binary", "/tmp/native", "--abi-json", "/tmp/abi.json"])
            with patch.object(probe.sys, "platform", "linux"), patch.object(probe.os, "geteuid", return_value=0), patch.dict(probe.os.environ, {}, clear=True):
                with self.assertRaisesRegex(RuntimeError, "outside a disposable"):
                    probe.main(["--disposable-github-worker", "--native-binary", "/tmp/native", "--abi-json", "/tmp/abi.json", "--journal-binary", "/tmp/journal"])
            with patch.object(probe.sys, "platform", "linux"), patch.object(probe.os, "geteuid", return_value=0), patch.dict(probe.os.environ, {}, clear=True):
                with self.assertRaisesRegex(RuntimeError, "outside a disposable"):
                    probe.main(["--disposable-github-worker", "--native-binary", "/tmp/native", "--abi-json", "/tmp/abi.json", "--journal-binary", "/tmp/journal", "--transport-binary", "/tmp/transport"])
            command.assert_not_called()
            temporary.assert_not_called()

    def test_substituted_loop_is_not_an_owned_resource(self):
        with patch.object(probe, "command", return_value='{"loopdevices":[{"name":"/dev/loop7","back-file":"/tmp/another-image"}]}'):
            with self.assertRaisesRegex(RuntimeError, "ownership"):
                probe.require_loop_owner("/dev/loop7", Path("/tmp/owned-image"))
        with patch.object(probe, "command") as command:
            with self.assertRaises(RuntimeError):
                probe.require_loop_owner("/dev/sda", Path("/tmp/owned-image"))
            command.assert_not_called()

    def test_mount_substitution_or_missing_enforcement_options_refuses(self):
        for response in [
            '{"filesystems":[{"source":"/dev/sda","fstype":"xfs","options":"prjquota,nodev,nosuid,noexec"}]}',
            '{"filesystems":[{"source":"/dev/loop7","fstype":"xfs","options":"rw,nodev,nosuid,noexec"}]}',
        ]:
            with patch.object(probe, "command", return_value=response):
                with self.assertRaises(RuntimeError):
                    probe.require_mount_owner(Path("/tmp/owned-mount"), "/dev/loop7")

    def test_missing_mount_is_observed_but_errors_are_not_absence(self):
        with patch.object(probe, "command", side_effect=probe.ProbeCommandError(1, "no exact mount")):
            self.assertFalse(probe.owned_mount_present(Path("/tmp/owned"), "/dev/loop7"))
        with patch.object(probe, "command", side_effect=probe.ProbeCommandError(2, "inspection failed")):
            with self.assertRaises(RuntimeError):
                probe.owned_mount_present(Path("/tmp/owned"), "/dev/loop7")

    def test_unknown_attach_acknowledgement_can_find_only_exact_owned_image(self):
        response = '{"loopdevices":[{"name":"/dev/loop7","back-file":"/tmp/owned-image"}]}'
        with patch.object(probe, "command", return_value=response):
            self.assertEqual(probe.discover_owned_loop(Path("/tmp/owned-image")), "/dev/loop7")
        with patch.object(probe, "command", return_value='{"loopdevices":[]}'):
            self.assertIsNone(probe.discover_owned_loop(Path("/tmp/owned-image")))
        with patch.object(probe, "command", return_value='{"loopdevices":[{"name":"/dev/sda","back-file":"/tmp/foreign-image"}]}'):
            with self.assertRaises(RuntimeError):
                probe.discover_owned_loop(Path("/tmp/owned-image"))

    def test_uuid_replacement_refuses_foreign_loop_before_mutation(self):
        with patch.object(probe, "require_loop_owner", side_effect=RuntimeError("foreign loop")), patch.object(probe, "command") as command:
            with self.assertRaisesRegex(RuntimeError, "foreign loop"):
                probe.replace_owned_uuid(Path("/tmp/owned/mount"), "/dev/loop7", Path("/tmp/owned/image"))
            command.assert_not_called()

    def test_uuid_replacement_requires_confirmed_unmount_and_rechecks_loop(self):
        with patch.object(probe, "require_loop_owner") as owner, patch.object(probe, "require_mount_owner"), patch.object(probe, "owned_mount_present", return_value=True), patch.object(probe, "command") as command:
            with self.assertRaisesRegex(RuntimeError, "remained mounted"):
                probe.replace_owned_uuid(Path("/tmp/owned/mount"), "/dev/loop7", Path("/tmp/owned/image"))
            command.assert_called_once_with("umount", "/tmp/owned/mount")
            self.assertEqual(owner.call_count, 1)
        with patch.object(probe, "require_loop_owner", side_effect=[None, RuntimeError("substituted loop")]), patch.object(probe, "require_mount_owner"), patch.object(probe, "owned_mount_present", return_value=False), patch.object(probe, "command") as command:
            with self.assertRaisesRegex(RuntimeError, "substituted loop"):
                probe.replace_owned_uuid(Path("/tmp/owned/mount"), "/dev/loop7", Path("/tmp/owned/image"))
            command.assert_called_once_with("umount", "/tmp/owned/mount")

    def test_normal_filesystem_does_not_fake_inode_enforcement(self):
        with tempfile.TemporaryDirectory() as folder:
            with self.assertRaisesRegex(RuntimeError, "without quota denial"):
                workload.fill_inodes(Path(folder))


if __name__ == "__main__":
    unittest.main()
