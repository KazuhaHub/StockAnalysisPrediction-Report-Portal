import errno
import importlib.util
from pathlib import Path
import tempfile
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

    def test_normal_filesystem_does_not_fake_inode_enforcement(self):
        with tempfile.TemporaryDirectory() as folder:
            with self.assertRaisesRegex(RuntimeError, "without quota denial"):
                workload.fill_inodes(Path(folder))


if __name__ == "__main__":
    unittest.main()
