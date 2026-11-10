import errno
import importlib.util
from pathlib import Path
import tempfile
import stat
import struct
from types import SimpleNamespace
import unittest
from unittest.mock import mock_open, patch

SOURCE = Path(__file__).parent / "acceptance/quota_workload.py"
SPEC = importlib.util.spec_from_file_location("quota_workload", SOURCE)
workload = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(workload)


PROBE_SPEC = importlib.util.spec_from_file_location("project_quota_probe", SOURCE.with_name("project_quota_probe.py"))
probe = importlib.util.module_from_spec(PROBE_SPEC)
PROBE_SPEC.loader.exec_module(probe)


class ProjectQuotaProbeTests(unittest.TestCase):
    def test_readiness_image_requires_owned_loopback_digest(self):
        valid = "localhost:53001/portal-ready-fixture@sha256:" + "a" * 64
        with patch.dict(probe.os.environ, {"APP_READY_NATIVE_IMAGE": valid}, clear=True):
            self.assertEqual(probe.readiness_image_configuration(), valid)
        for image in ["registry.example/app@sha256:" + "a" * 64,
                      "localhost:0/portal-ready-fixture@sha256:" + "a" * 64,
                      "localhost:65536/portal-ready-fixture@sha256:" + "a" * 64,
                      "localhost:53001/other@sha256:" + "a" * 64,
                      "localhost:53001/portal-ready-fixture:latest"]:
            with patch.dict(probe.os.environ, {"APP_READY_NATIVE_IMAGE": image}, clear=True):
                with self.assertRaises(RuntimeError):
                    probe.readiness_image_configuration()

    def test_scheduled_digest_image_requires_owned_exact_digest(self):
        valid = "localhost:53001/scheduled-digest-exact@sha256:" + "a" * 64
        with patch.dict(
            probe.os.environ,
            {"APP_SCHEDULED_DIGEST_NATIVE_IMAGE": valid},
            clear=True,
        ):
            self.assertEqual(
                probe.scheduled_digest_image_configuration(), valid)
        for image in (
            "registry.example/scheduled-digest-exact@sha256:" + "a" * 64,
            "localhost:0/scheduled-digest-exact@sha256:" + "a" * 64,
            "localhost:65536/scheduled-digest-exact@sha256:" + "a" * 64,
            "localhost:53001/other@sha256:" + "a" * 64,
            "localhost:53001/scheduled-digest-exact:latest",
        ):
            with patch.dict(
                probe.os.environ,
                {"APP_SCHEDULED_DIGEST_NATIVE_IMAGE": image},
                clear=True,
            ):
                with self.assertRaises(RuntimeError):
                    probe.scheduled_digest_image_configuration()

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
            portal_args=["--disposable-github-worker", "--native-binary", "/tmp/native", "--abi-json", "/tmp/abi.json", "--journal-binary", "/tmp/journal", "--transport-binary", "/tmp/transport", "--portal-binary", "/tmp/portal"]
            with patch.object(probe.sys, "platform", "linux"), patch.object(probe.os, "geteuid", return_value=0), patch.dict(probe.os.environ, {}, clear=True):
                with self.assertRaisesRegex(RuntimeError, "outside a disposable"):
                    probe.main(portal_args)
            for position,value in [(9,"--wrong"),(10,"relative")]:
                malformed=list(portal_args);malformed[position]=value
                with self.assertRaises(RuntimeError):
                    probe.main(malformed)
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
                probe.replace_owned_uuid(
                    Path("/tmp/owned/mount"), "/dev/loop7",
                    Path("/tmp/owned/image"),
                    "11111111-1111-4111-8111-111111111111")
            command.assert_called_once_with("umount", "/tmp/owned/mount")
            self.assertEqual(owner.call_count, 1)
        with patch.object(probe, "require_loop_owner", side_effect=[None, RuntimeError("substituted loop")]), patch.object(probe, "require_mount_owner"), patch.object(probe, "owned_mount_present", return_value=False), patch.object(probe, "require_loop_unmounted"), patch.object(probe, "command") as command:
            with self.assertRaisesRegex(RuntimeError, "substituted loop"):
                probe.replace_owned_uuid(
                    Path("/tmp/owned/mount"), "/dev/loop7",
                    Path("/tmp/owned/image"),
                    "11111111-1111-4111-8111-111111111111")
            command.assert_called_once_with("umount", "/tmp/owned/mount")

    def test_uuid_replacement_uses_explicit_uuid_without_cached_blkid(self):
        old_uuid = "11111111-1111-4111-8111-111111111111"
        replacement = []
        commands = []

        def run(*args):
            commands.append(args)
            if args[:2] == ("xfs_admin", "-U"):
                replacement.append(args[2])
                return f"new UUID = {args[2]}"
            if args[:2] == ("findmnt", "--json"):
                return '{"filesystems":[]}'
            if args == ("losetup", "--find", "--show", "/tmp/owned/image"):
                return "/dev/loop8"
            return ""

        with patch.object(probe, "require_loop_owner"), patch.object(probe, "require_mount_owner"), patch.object(probe, "owned_mount_present", return_value=False), patch.object(probe, "require_loop_unmounted"), patch("builtins.open", mock_open()) as cache, patch.object(probe, "command", side_effect=run) as command, patch.object(probe, "command_text", side_effect=run):
            changed = probe.replace_owned_uuid(
                Path("/tmp/owned/mount"), "/dev/loop7",
                Path("/tmp/owned/image"), old_uuid)
        self.assertEqual(changed, replacement[0])
        cache.assert_called_once_with(
            "/proc/sys/vm/drop_caches", "w", encoding="ascii")
        self.assertIn(
            "xfs_admin", [entry[0] for entry in commands])
        replacements = [
            entry for entry in commands
            if entry[:2] == ("xfs_admin", "-U") and len(entry) == 4
        ]
        self.assertEqual(len(replacements), 1)
        self.assertRegex(
            replacements[0][2],
            r"^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$")
        self.assertNotEqual(replacements[0][2], old_uuid)
        self.assertFalse(any(command[0] == "blkid" for command in commands))

    def test_normal_filesystem_does_not_fake_inode_enforcement(self):
        with tempfile.TemporaryDirectory() as folder:
            with self.assertRaisesRegex(RuntimeError, "without quota denial"):
                workload.fill_inodes(Path(folder))

class CandidateProbeConfigurationTests(unittest.TestCase):
    def test_candidate_fixture_configuration_is_complete_and_closed(self):
        with patch.dict(probe.os.environ, {}, clear=True):
            self.assertIsNone(probe.candidate_configuration())
        good = {"APP_CANDIDATE_NATIVE_BINARY": "/tmp/native-driver", "APP_CANDIDATE_NATIVE_IMAGE": "registry.k8s.io/pause@sha256:" + "a" * 64, "APP_CANDIDATE_DOCKER_SOCKET": "/run/docker.sock"}
        with patch.dict(probe.os.environ, good, clear=True):
            self.assertEqual(probe.candidate_configuration(), tuple(good.values()))
        for changed in [{"APP_CANDIDATE_NATIVE_BINARY": "relative"}, {"APP_CANDIDATE_DOCKER_SOCKET": "relative"}, {"APP_CANDIDATE_NATIVE_IMAGE": "registry.k8s.io/pause:latest"}, {"APP_CANDIDATE_NATIVE_IMAGE": "other.invalid/pause@sha256:" + "a" * 64}, {"APP_CANDIDATE_NATIVE_BINARY": ""}]:
            with patch.dict(probe.os.environ, dict(good, **changed), clear=True):
                with self.assertRaisesRegex(RuntimeError, "explicit candidate"):
                    probe.candidate_configuration()


class ImageDeliveryProbeTests(unittest.TestCase):
    ENV = {
        "APP_IMAGE_DELIVERY_CANDIDATE": "/tmp/image-delivery/candidate",
        "APP_IMAGE_DELIVERY_BLOBS": "/tmp/image-delivery/export/blobs",
        "APP_IMAGE_DELIVERY_BUILT_IMAGE_ID": "sha256:" + "a" * 64,
    }

    def evidence(self, **changed):
        good = {name: True for name in probe.IMAGE_DELIVERY_EVIDENCE_TRUE}
        good["format"] = probe.IMAGE_DELIVERY_EVIDENCE_FORMAT
        good["allow_local_reference_images"] = False
        good["runner"] = {"profile": "p", "platform": "linux/amd64", "images": "production"}
        good.update(changed)
        return good

    def stdout(self, drop=None):
        lines = ["    x_test.go:1: " + marker for marker in probe.IMAGE_DELIVERY_MARKERS if marker != drop]
        lines.append("--- PASS: TestPlatformManagedImageDeliveryAcceptance (12.00s)")
        return "\n".join(lines)

    def test_configuration_is_absent_or_complete_and_absolute(self):
        with patch.dict(probe.os.environ, {}, clear=True):
            self.assertIsNone(probe.image_delivery_configuration())
        with patch.dict(probe.os.environ, self.ENV, clear=True):
            self.assertEqual(probe.image_delivery_configuration(), tuple(self.ENV.values()))
        for changed in ({"APP_IMAGE_DELIVERY_CANDIDATE": "relative"}, {"APP_IMAGE_DELIVERY_BLOBS": "relative"},
                        {"APP_IMAGE_DELIVERY_BLOBS": ""}, {"APP_IMAGE_DELIVERY_CANDIDATE": ""},
                        {"APP_IMAGE_DELIVERY_BUILT_IMAGE_ID": ""}, {"APP_IMAGE_DELIVERY_BUILT_IMAGE_ID": "a" * 64},
                        {"APP_IMAGE_DELIVERY_BUILT_IMAGE_ID": "sha256:short"}):
            with patch.dict(probe.os.environ, dict(self.ENV, **changed), clear=True):
                with self.assertRaisesRegex(RuntimeError, "image delivery"):
                    probe.image_delivery_configuration()

    def test_every_marker_and_the_pass_line_are_required(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "evidence.json"
            path.write_text(__import__("json").dumps(self.evidence()))
            self.assertTrue(probe.verify_image_delivery(self.stdout(), path)["production_profile"])
            for marker in probe.IMAGE_DELIVERY_MARKERS:
                with self.assertRaisesRegex(RuntimeError, "image delivery"):
                    probe.verify_image_delivery(self.stdout(drop=marker), path)
            with self.assertRaisesRegex(RuntimeError, "image delivery"):
                probe.verify_image_delivery(self.stdout().replace("--- PASS", "--- FAIL"), path)

    def test_the_evidence_must_prove_each_claim_and_the_production_profile(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "evidence.json"
            for name in probe.IMAGE_DELIVERY_EVIDENCE_TRUE:
                path.write_text(json.dumps(self.evidence(**{name: False})))
                with self.assertRaisesRegex(RuntimeError, "image delivery"):
                    probe.verify_image_delivery(self.stdout(), path)
            for changed in ({"allow_local_reference_images": True}, {"format": "other"},
                            {"runner": {"images": "local"}}, {"runner": {}}):
                path.write_text(json.dumps(self.evidence(**changed)))
                with self.assertRaisesRegex(RuntimeError, "image delivery"):
                    probe.verify_image_delivery(self.stdout(), path)
            missing = self.evidence()
            del missing["no_pull_events"]
            path.write_text(json.dumps(missing))
            with self.assertRaisesRegex(RuntimeError, "image delivery"):
                probe.verify_image_delivery(self.stdout(), path)
            with self.assertRaisesRegex(RuntimeError, "image delivery"):
                probe.verify_image_delivery(self.stdout(), Path(directory) / "absent.json")

    def test_the_scenario_is_required_when_the_job_expects_it(self):
        report = {"managed_image_delivery_real_runner": {}}
        with patch.dict(probe.os.environ, {}, clear=True):
            self.assertFalse(probe.image_delivery_required())
            probe.require_image_delivery({}, None)
        with patch.dict(probe.os.environ, {"APP_IMAGE_DELIVERY_REQUIRED": "1"}, clear=True):
            self.assertTrue(probe.image_delivery_required())
            probe.require_image_delivery(report, tuple(self.ENV.values()))
            with self.assertRaisesRegex(RuntimeError, "image delivery"):
                probe.require_image_delivery({}, tuple(self.ENV.values()))
            with self.assertRaisesRegex(RuntimeError, "image delivery"):
                probe.require_image_delivery(report, None)
        with patch.dict(probe.os.environ, {"APP_IMAGE_DELIVERY_REQUIRED": "yes"}, clear=True):
            with self.assertRaisesRegex(RuntimeError, "image delivery"):
                probe.image_delivery_required()

    def test_a_failed_run_still_keeps_its_evidence_file(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            source, target = Path(directory) / "evidence.json", Path(directory) / "kept.json"
            probe.keep_image_delivery_evidence(source, target)
            self.assertFalse(target.exists())
            source.write_text(json.dumps({"x": 1}))
            probe.keep_image_delivery_evidence(source, target)
            self.assertEqual(json.loads(target.read_text()), {"x": 1})


if __name__ == "__main__":
    unittest.main()
