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
    native_args = (
        len(args) in (5, 7, 9, 11)
        and args[:2] == ["--disposable-github-worker", "--native-binary"]
        and args[3] == "--abi-json"
        and Path(args[2]).is_absolute() and Path(args[4]).is_absolute()
        and (len(args) == 5 or (args[5] == "--journal-binary" and Path(args[6]).is_absolute()))
        and (len(args) < 9 or (args[7] == "--transport-binary" and Path(args[8]).is_absolute()))
        and (len(args) != 11 or (args[9] == "--portal-binary" and Path(args[10]).is_absolute()))
    )
    if not (args == ["--disposable-github-worker"] or native_args) or sys.platform != "linux" or os.geteuid() != 0:
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


def candidate_configuration():
    names = ("APP_CANDIDATE_NATIVE_BINARY", "APP_CANDIDATE_NATIVE_IMAGE", "APP_CANDIDATE_DOCKER_SOCKET")
    values = tuple(os.environ.get(name, "") for name in names)
    if not any(values):
        return None
    binary, image, socket = values
    if not all(values) or not Path(binary).is_absolute() or not Path(socket).is_absolute() or not re.fullmatch(r"registry\.k8s\.io/pause@sha256:[a-f0-9]{64}", image):
        raise RuntimeError("invalid explicit candidate fixture configuration")
    return values


def readiness_image_configuration():
    image = os.environ.get("APP_READY_NATIVE_IMAGE", "")
    if not image:
        return None
    match = re.fullmatch(r"localhost:([0-9]{1,5})/portal-ready-fixture@sha256:[a-f0-9]{64}", image)
    if not match or not 1 <= int(match.group(1)) <= 65535:
        raise RuntimeError("invalid owned readiness fixture image")
    return image


def main(args):
    require_disposable(args)
    candidate = candidate_configuration()
    ready_image = readiness_image_configuration()
    if ready_image and (not candidate or len(args) != 11):
        raise RuntimeError("readiness fixture requires managed Portal acceptance")
    if candidate and len(args) not in (5, 7, 9, 11):
        raise RuntimeError("candidate fixture requires native root acceptance")
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
        if len(args) in (5,7,9,11):
            native_root = mountpoint / "native-data"
            native_root.mkdir(mode=0o700)
            native_env = dict(ENV, APP_DATA_NATIVE_UUID=command("blkid", "-p", "-s", "UUID", "-o", "value", loop), APP_DATA_NATIVE_INODE=str(native_root.stat().st_ino), GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted", APP_DATA_NATIVE_ROOT=str(native_root), APP_DATA_NATIVE_ABI=args[4])
            native = subprocess.run([args[2], "-test.run=^TestNativeData(Primitive|WriteAccess)$", "-test.v", "-test.timeout=30s"], env=native_env, capture_output=True, text=True, timeout=45)
            if native.returncode or "--- PASS: TestNativeDataPrimitive" not in native.stdout:
                raise RuntimeError("native data preparation failed: " + (native.stdout+native.stderr)[-4096:])
            report["native_go_data_prepared"] = True
            report["native_go_root_identity_checked"] = True
            if "NATIVE_DATA_WRITE_ACCESS_TRANSITION_VERIFIED" not in native.stdout:
                raise RuntimeError("native data write-access transition missing")
            report["native_go_data_write_access_transition"] = True
            if candidate:
                candidate_root = mountpoint / "candidate-data"
                candidate_root.mkdir(mode=0o700)
                binary, candidate_image, candidate_socket = candidate
                candidate_env = dict(native_env, APP_CANDIDATE_NATIVE_ROOT=str(candidate_root), APP_CANDIDATE_NATIVE_INODE=str(candidate_root.stat().st_ino), APP_CANDIDATE_NATIVE_IMAGE=candidate_image, APP_CANDIDATE_DOCKER_SOCKET=candidate_socket)
                result = subprocess.run([binary, "-test.run=^TestNativeCandidateRuntime$", "-test.v", "-test.timeout=60s"], cwd=Path(__file__).resolve().parents[2] / "internal/apprunner/driver", env=candidate_env, capture_output=True, text=True, timeout=70)
                if result.returncode or "--- PASS: TestNativeCandidateRuntime" not in result.stdout:
                    raise RuntimeError("native candidate runtime failed: " + (result.stdout+result.stderr)[-4096:])
                report["native_candidate_quota_mount_start_once"] = True
                if ready_image:
                    writer_env = dict(candidate_env, APP_WRITER_NATIVE_IMAGE=ready_image)
                    writer = subprocess.run([binary, "-test.run=^TestNativeDataWriterContainer$", "-test.v", "-test.timeout=60s"], cwd=Path(__file__).resolve().parents[2] / "internal/apprunner/driver", env=writer_env, capture_output=True, text=True, timeout=70)
                    if writer.returncode or "NATIVE_FILTERED_DATA_WRITER_VERIFIED" not in writer.stdout:
                        raise RuntimeError("actual filtered data writer failed: " + (writer.stdout+writer.stderr)[-4096:])
                    report["native_filtered_data_writer_verified"] = True
                    if "NATIVE_DATA_WRITER_ADAPTER_VERIFIED" not in writer.stdout or "NATIVE_WRITER_RECOVERY_PARTIAL_PERMISSION_REFUSED" not in writer.stdout:
                        raise RuntimeError("actual writer adapter/reference exclusion missing")
                    report["native_writer_adapter_and_exclusion_verified"] = True
                    report["native_writer_recovery_partial_permission_refused"] = True
        journal_root = None
        if len(args) in (7,9,11):
            journal_root = mountpoint / "journal-data"
            journal_root.mkdir(mode=0o700)
            # One control journal outside app data; no per-app database.
            journal_control = root / "journal-control"
            journal_control.mkdir(mode=0o700)
            journal_inode = str(journal_root.stat().st_ino)

            def run_journal(phase):
                journal_env = dict(native_env, APP_DATA_NATIVE_PHASE=phase, APP_DATA_JOURNAL_ROOT=str(journal_root), APP_DATA_JOURNAL_CONTROL=str(journal_control), APP_DATA_JOURNAL_INODE=journal_inode)
                result = subprocess.run([args[6], "-test.run=^TestNativeJournalDataExecution$", "-test.v", "-test.timeout=45s"], env=journal_env, capture_output=True, text=True, timeout=55)
                if result.returncode or "--- PASS: TestNativeJournalDataExecution" not in result.stdout:
                    raise RuntimeError("native journal " + phase + " failed: " + (result.stdout+result.stderr)[-4096:])
                if phase == "inspect":
                    if "NATIVE_DATA_WRITER_INTENT_VERIFIED" not in result.stdout:
                        raise RuntimeError("actual prepared-data writer admission/claim verification missing")
                    report["native_writer_intent_and_claim_verified"] = True

            run_journal("prepare")
            if len(args) in (9,11):
                receipt_fixture = root / "receipt-channel"
                receipt_fixture.mkdir(mode=0o755)
                receipt_fixture.chmod(0o755)

                def run_transport(phase):
                    receipt_env = dict(native_env, APP_DATA_NATIVE_PHASE=phase, APP_DATA_JOURNAL_ROOT=str(journal_root), APP_DATA_JOURNAL_CONTROL=str(journal_control), APP_DATA_JOURNAL_INODE=journal_inode, APP_DATA_RECEIPT_FIXTURE=str(receipt_fixture))
                    result = subprocess.run([args[8], "-test.run=^TestNativeReceiptTransport$", "-test.v", "-test.timeout=60s"], env=receipt_env, capture_output=True, text=True, timeout=60)
                    if result.returncode or "--- PASS: TestNativeReceiptTransport" not in result.stdout:
                        raise RuntimeError("native receipt transport " + phase + " failed: " + (result.stdout+result.stderr)[-4096:])

                run_transport("prepared")
                report["native_receipt_cross_uid_transport"] = True
                report["native_receipt_peer_signature_and_journal_dac"] = True
            report["native_journal_physical_receipts"] = True
            report["native_journal_root_history_binding"] = True
            report["native_journal_settlement_failure_preserved_unknown"] = True
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
            if journal_root is not None:
                run_journal("inspect")
                report["native_journal_receipts_survive_remount"] = True
                report["native_journal_recovers_after_envelope_expiry"] = True
                report["native_journal_recovery_preserves_native_identity"] = True
                report["native_journal_partial_data_not_repaired"] = True
                report["native_journal_missing_data_not_created"] = True
                report["native_dispatch_recovers_without_reexecution"] = True
                if ready_image:
                    writer_journal_env = dict(native_env, APP_DATA_JOURNAL_ROOT=str(journal_root), APP_DATA_JOURNAL_CONTROL=str(journal_control), APP_DATA_JOURNAL_INODE=journal_inode, APP_WRITER_NATIVE_IMAGE=ready_image, APP_CANDIDATE_DOCKER_SOCKET=candidate[2])
                    executed = subprocess.run([args[6], "-test.run=^TestNativeJournalDataWriterExecution$", "-test.v", "-test.timeout=60s"], cwd=Path(__file__).resolve().parents[2] / "internal/apprunner/journal", env=writer_journal_env, capture_output=True, text=True, timeout=70)
                    if executed.returncode or "NATIVE_JOURNAL_DATA_WRITER_EXECUTION_VERIFIED" not in executed.stdout or "NATIVE_WRITER_SETTLEMENT_FAILURE_PRESERVED_UNKNOWN" not in executed.stdout or "NATIVE_DATA_WRITER_RECOVERY_AFTER_EXPIRY_VERIFIED" not in executed.stdout or "NATIVE_WRITER_RECOVERY_WITHOUT_PERMISSION_MUTATION" not in executed.stdout or "NATIVE_WRITER_RECOVERY_METADATA_FAILURE_RETRY_VERIFIED" not in executed.stdout or "NATIVE_WRITABLE_CANDIDATE_INTENT_ACTUAL_SOURCES_VERIFIED" not in executed.stdout or "NATIVE_WRITABLE_CANDIDATE_CLAIM_ACTUAL_SOURCES_VERIFIED" not in executed.stdout:
                        raise RuntimeError("actual journal writer execution failed: " + (executed.stdout+executed.stderr)[-4096:])
                    report["native_journal_data_writer_execution_verified"] = True
                    report["native_writer_settlement_failure_preserves_unknown"] = True
                    report["native_data_writer_recovery_after_expiry_verified"] = True
                    report["native_writer_recovery_without_permission_mutation"] = True
                    report["native_writer_recovery_metadata_failure_retry_verified"] = True
                    report["native_writable_candidate_intent_actual_sources_verified"] = True
                    report["native_writable_candidate_claim_actual_sources_verified"] = True
                if len(args) in (9,11):
                    run_transport("recovered")
                    report["native_recovered_receipt_cross_uid_transport"] = True
                if len(args) == 11:
                    portal_env = dict(native_env, APP_DATA_JOURNAL_ROOT=str(journal_root), APP_DATA_JOURNAL_CONTROL=str(journal_control), APP_DATA_JOURNAL_INODE=journal_inode, APP_DATA_PORTAL_FIXTURE=str(receipt_fixture), APP_DATA_PORTAL_DESCRIPTOR=str(Path(__file__).resolve().parents[2] / "internal/appplatform/testdata/descriptors/static.json"))
                    if candidate:
                        portal_env["APP_CANDIDATE_DOCKER_SOCKET"] = candidate[2]
                        portal_env["APP_CANDIDATE_NATIVE_IMAGE"] = candidate[1]
                    if ready_image:
                        portal_env["APP_CANDIDATE_NATIVE_IMAGE"] = ready_image
                        portal_env["APP_READY_NATIVE_IMAGE"] = ready_image
                    result = subprocess.run([args[10], "-test.run=^TestPlatformInstallDataEndpointAcceptance$", "-test.v", "-test.timeout=60s"], env=portal_env, capture_output=True, text=True, timeout=60)
                    if result.returncode or "--- PASS: TestPlatformInstallDataEndpointAcceptance" not in result.stdout:
                        raise RuntimeError("Portal data stage failed: " + (result.stdout+result.stderr)[-4096:])
                    report["native_portal_install_data_stage"] = True
                    report["native_portal_data_stage_not_activation"] = True
                    report["native_portal_static_activation_and_launch"] = True
                    report["native_portal_signed_data_dispatch"] = True
                    report["native_portal_static_driver_queued_to_complete"] = True
                    report["native_portal_background_install_scheduler"] = True
                    if candidate:
                        if "MANAGED_PROFILE_OBSERVED" not in result.stdout:
                            raise RuntimeError("Portal managed profile acceptance did not execute")
                        report["native_portal_authenticated_managed_profile"] = True
                        if "MANAGED_INSTALL_DATA_READY" not in result.stdout:
                            raise RuntimeError("Portal managed installation data stage did not execute")
                        report["native_portal_managed_install_data_to_start"] = True
                        if "MANAGED_INSTALL_CANDIDATE_PROBE" not in result.stdout:
                            raise RuntimeError("Portal managed candidate start/probe did not execute")
                        report["native_portal_managed_candidate_start_to_probe"] = True
                        if ready_image:
                            if "MANAGED_INSTALL_READINESS" not in result.stdout:
                                raise RuntimeError("Portal actual backend readiness did not execute")
                            report["native_portal_managed_backend_readiness"] = True
                            if "CANDIDATE_SECCOMP_ENTRYPOINT_VERIFIED" not in result.stdout:
                                raise RuntimeError("actual candidate entrypoint seccomp verification missing")
                            report["native_candidate_seccomp_entrypoint_verified"] = True
                            if "MANAGED_BACKEND_REGISTRATION_MATERIAL" not in result.stdout:
                                raise RuntimeError("Portal managed registration material was not prepared")
                            report["native_portal_managed_registration_material"] = True
                            if "BACKEND_REGISTRATION_INTENT_ADMITTED" not in result.stdout:
                                raise RuntimeError("actual candidate registration intent was not admitted")
                            report["native_backend_registration_intent"] = True
                            if "BACKEND_REGISTRATION_DELIVERY_RECONCILED" not in result.stdout:
                                raise RuntimeError("native backend registration delivery recovery missing")
                            report["native_backend_registration_delivery_reconciled"] = True
                            if "BACKEND_REGISTRATION_RPC_RECONCILED" not in result.stdout:
                                raise RuntimeError("protected registration RPC recovery missing")
                            report["native_backend_registration_rpc_reconciled"] = True
                            if "MANAGED_BACKEND_DELIVERY_COMMITTED" not in result.stdout:
                                raise RuntimeError("Portal delivery effect settlement recovery missing")
                            report["native_portal_backend_delivery_committed"] = True
                            if "MANAGED_BACKEND_BOOT_VERIFIED" not in result.stdout:
                                raise RuntimeError("actual backend boot handshake missing")
                            report["native_portal_backend_boot_verified"] = True
                            if "MANAGED_BACKEND_REQUEST_RELAY_VERIFIED" not in result.stdout:
                                raise RuntimeError("signed backend request relay missing")
                            report["native_backend_request_relay_verified"] = True
                            if "MANAGED_CURRENT_VIEWER_REQUEST_VERIFIED" not in result.stdout:
                                raise RuntimeError("current Portal viewer native request gating missing")
                            report["native_current_viewer_request_verified"] = True
                if candidate:
                    # Reuse the shared journal/provider after its restart tests.
                    # Its monotonic project allocator avoids earlier fixture IDs.
                    _, candidate_image, candidate_socket = candidate
                    candidate_journal_env = dict(native_env, APP_DATA_JOURNAL_ROOT=str(journal_root), APP_DATA_JOURNAL_CONTROL=str(journal_control), APP_DATA_JOURNAL_INODE=journal_inode, APP_CANDIDATE_NATIVE_IMAGE=candidate_image, APP_CANDIDATE_DOCKER_SOCKET=candidate_socket)
                    result = subprocess.run([args[6], "-test.run=^TestNativeJournalCandidateExecution$", "-test.v", "-test.timeout=60s"], cwd=Path(__file__).resolve().parents[2] / "internal/apprunner/journal", env=candidate_journal_env, capture_output=True, text=True, timeout=70)
                    if result.returncode or "--- PASS: TestNativeJournalCandidateExecution" not in result.stdout:
                        raise RuntimeError("native candidate journal failed: " + (result.stdout+result.stderr)[-4096:])
                    report["native_candidate_journal_running_receipt"] = True
                    report["native_candidate_journal_atomic_failure_unknown"] = True
                    report["native_candidate_receipt_restart_renewal_replay"] = True
                    report["native_candidate_unknown_dispatch_no_restart"] = True
                    if len(args) in (9, 11):
                        candidate_receipt_fixture = root / "candidate-receipt"
                        candidate_receipt_fixture.mkdir(mode=0o755)
                        candidate_transport_env = dict(candidate_journal_env, APP_CANDIDATE_RECEIPT_FIXTURE=str(candidate_receipt_fixture))
                        result = subprocess.run([args[8], "-test.run=^TestNativeCandidateReceiptTransport$", "-test.v", "-test.timeout=45s"], cwd=Path(__file__).resolve().parents[2] / "internal/apprunner/transport", env=candidate_transport_env, capture_output=True, text=True, timeout=55)
                        if result.returncode or "--- PASS: TestNativeCandidateReceiptTransport" not in result.stdout:
                            raise RuntimeError("native candidate receipt channel failed: " + (result.stdout+result.stderr)[-4096:])
                        report["native_candidate_receipt_cross_uid_and_journal_dac"] = True
                        candidate_execution_env = dict(candidate_journal_env, APP_CANDIDATE_EXECUTION_FIXTURE=str(candidate_receipt_fixture))
                        result = subprocess.run([args[8], "-test.run=^TestNativeCandidateExecutionTransport$", "-test.v", "-test.timeout=60s"], cwd=Path(__file__).resolve().parents[2] / "internal/apprunner/transport", env=candidate_execution_env, capture_output=True, text=True, timeout=70)
                        if result.returncode or "--- PASS: TestNativeCandidateExecutionTransport" not in result.stdout:
                            raise RuntimeError("native candidate execution channel failed: " + (result.stdout+result.stderr)[-4096:])
                        report["native_candidate_execution_cross_uid_actual_start"] = True
                        report["native_candidate_execution_replay_same_native_id"] = True
                        report["native_managed_endpoint_shared_provider_refusal"] = True
                        report["native_managed_endpoint_data_and_candidate_cross_uid"] = True
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
            if journal_root is not None:
                run_journal("replaced")
                report["native_journal_rejects_changed_filesystem_uuid"] = True
                report["native_journal_recovery_rejects_changed_filesystem_uuid"] = True
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
