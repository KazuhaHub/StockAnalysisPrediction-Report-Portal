#!/usr/bin/env python3
"""Run the exact Scheduled Digest image through its fixed Unix API."""
import argparse
import base64
import hashlib
import hmac
import http.client
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tempfile
import time

IMAGE = re.compile(
    r"^localhost:[0-9]+/[a-z0-9][a-z0-9._/-]*@sha256:[a-f0-9]{64}$"
)
DEPLOYMENT_ID = "10000000-0000-4000-8000-000000000005"
INSTALLATION_ID = "10000000-0000-4000-8000-000000000001"
CONTROLLER_EPOCH = "10000000-0000-4000-8000-000000000003"
INSTANCE_ID = "d" * 64
SIGNING_KEY = bytes(range(32))


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, path, timeout=5):
        super().__init__("localhost", timeout=timeout)
        self.path = path

    def connect(self):
        connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        connection.settimeout(self.timeout)
        connection.connect(str(self.path))
        self.sock = connection


def request(socket_path, method, path, body=None, headers=None, deadline=10):
    deadline = time.monotonic() + deadline
    while True:
        try:
            connection = UnixHTTPConnection(socket_path)
            connection.request(method, path, body=body, headers=headers or {})
            response = connection.getresponse()
            raw = response.read()
            connection.close()
            return response.status, raw
        except (FileNotFoundError, ConnectionRefusedError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.05)


def run(command, **kwargs):
    return subprocess.run(
        command, check=True, capture_output=True, text=True, **kwargs
    ).stdout.strip()


def image_identity(image):
    if not IMAGE.fullmatch(image):
        raise ValueError("image must be an immutable localhost registry digest")
    image_id = run(["docker", "image", "inspect", image, "--format", "{{.Id}}"])
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", image_id):
        raise ValueError("Docker did not return an immutable image ID")
    config = run(
        [
            "docker",
            "image",
            "inspect",
            image,
            "--format",
            "{{json .Config}}",
        ]
    )
    parsed = json.loads(config)
    if parsed.get("User") != "65532:65532" or parsed.get("Entrypoint") != [
        "/scheduled-digest"
    ]:
        raise ValueError("Scheduled Digest image identity changed")
    return image_id


def registration():
    key_id = hashlib.sha256(SIGNING_KEY).hexdigest()
    return {
        "format": "portal.managed-backend-registration/1-dev.1",
        "metadata": {
            "identity": {
                "app_id": "scheduled-digest",
                "deployment_id": DEPLOYMENT_ID,
                "controller_epoch": CONTROLLER_EPOCH,
                "installation_id": INSTALLATION_ID,
                "generation": "1",
                "policy_revision": "1",
                "release_sha256": "a" * 64,
            },
            "instance_id": INSTANCE_ID,
            "key_id": key_id,
        },
        "signing_key": base64.b64encode(SIGNING_KEY).decode("ascii"),
    }


def service_identity():
    now = int(time.time() * 1000)
    return {
        "format": "portal.app.service-identity/1-dev.1",
        "lease_id": "10000000-0000-4000-8000-000000000002",
        "app_id": "scheduled-digest",
        "installation_id": INSTALLATION_ID,
        "controller_epoch": CONTROLLER_EPOCH,
        "generation": "1",
        "policy_revision": "1",
        "release_sha256": "a" * 64,
        "resources": ["reports.read", "tasks.schedule"],
        "issued_ms": str(now),
        "expires_ms": str(now + 300000),
        "mac": "b" * 64,
    }


def handshake_challenge(now=None):
    issued = int(time.time()) if now is None else now
    return encode_handshake(
        {
            "kind": "challenge",
            "iss": "portal.app/" + DEPLOYMENT_ID,
            "aud": "portal.app/" + INSTALLATION_ID,
            "app": "scheduled-digest",
            "instance": INSTANCE_ID,
            "kid": hashlib.sha256(SIGNING_KEY).hexdigest(),
            "nonce": base64.urlsafe_b64encode(os.urandom(24))
            .rstrip(b"=")
            .decode("ascii"),
            "iat": issued,
            "exp": issued + 30,
        }
    )


def encode_handshake(value):
    raw = json.dumps(value, separators=(",", ":")).encode()
    encoded = base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")
    signature = hmac.new(
        SIGNING_KEY,
        b"rp-extension-v1\nhandshake:" + encoded.encode("ascii"),
        hashlib.sha256,
    ).digest()
    return encoded + "." + base64.urlsafe_b64encode(signature).rstrip(b"=").decode(
        "ascii"
    )


def _decode_token_part(value):
    return base64.b64decode(
        value + "=" * (-len(value) % 4),
        altchars=b"-_",
        validate=True,
    )


def verify_handshake_answer(challenge, raw, now=None):
    if not isinstance(raw, dict) or set(raw) != {"answer"}:
        raise ValueError("scheduled digest handshake response changed")
    answer = raw["answer"]
    if not isinstance(answer, str) or answer.count(".") != 1:
        raise ValueError("scheduled digest handshake answer is invalid")
    encoded, supplied = answer.split(".", 1)
    expected = hmac.new(
        SIGNING_KEY,
        b"rp-extension-v1\nhandshake:" + encoded.encode("ascii"),
        hashlib.sha256,
    ).digest()
    try:
        supplied_raw = _decode_token_part(supplied)
        challenge_raw = json.loads(_decode_token_part(challenge.split(".", 1)[0]))
        value = json.loads(_decode_token_part(encoded))
    except (ValueError, TypeError, json.JSONDecodeError, UnicodeDecodeError) as error:
        raise ValueError("scheduled digest handshake answer is invalid") from error
    instant = int(time.time()) if now is None else now
    boot = value.get("boot", "")
    if (
        not hmac.compare_digest(supplied_raw, expected)
        or set(value)
        != {
            "kind",
            "iss",
            "aud",
            "app",
            "instance",
            "kid",
            "nonce",
            "iat",
            "exp",
            "boot",
            "challenge_sha256",
        }
        or value.get("kind") != "answer"
        or value.get("challenge_sha256")
        != hashlib.sha256(challenge.encode("ascii")).hexdigest()
        or value.get("nonce") != challenge_raw.get("nonce")
        or value.get("iss") != challenge_raw.get("iss")
        or value.get("aud") != challenge_raw.get("aud")
        or value.get("app") != challenge_raw.get("app")
        or value.get("instance") != challenge_raw.get("instance")
        or value.get("kid") != challenge_raw.get("kid")
        or value.get("iat") != challenge_raw.get("iat")
        or value.get("exp") != challenge_raw.get("exp")
        or not re.fullmatch(r"[a-f0-9]{32}", boot)
    ):
        raise ValueError("scheduled digest handshake answer is invalid")
    if value.get("exp", 0) <= instant or value.get("exp", 0) <= value.get("iat", 0):
        raise ValueError("scheduled digest handshake answer expired")
    return boot


def task_body(task_id, occurrence, report_ids):
    return json.dumps(
        {
            "format": "portal.app-task.request/1-dev.1",
            "task_id": task_id,
            "occurrence_id": occurrence,
            "payload": {
                "scheduleId": "daily",
                "reportIds": report_ids,
            },
        },
        separators=(",", ":"),
        sort_keys=True,
    ).encode()


def decode_response(raw):
    def no_duplicates(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError("duplicate response field")
            value[key] = item
        return value

    value = json.loads(raw, object_pairs_hook=no_duplicates)
    if (
        not isinstance(value, dict)
        or value.get("format") != "portal.app-task.response/1-dev.1"
        or json.dumps(value, separators=(",", ":"), sort_keys=False).encode()
        != raw
    ):
        raise ValueError("invalid scheduled digest response")
    outcome = value.get("outcome")
    expected = {
        "succeeded": ["format", "outcome", "result", "evidence_sha256"],
        "failed": ["format", "outcome", "error_code"],
    }.get(outcome)
    if expected is None or list(value) != expected:
        raise ValueError("scheduled digest response outcome changed")
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--docker-socket", required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    if not args.evidence.is_absolute():
        parser.error("--evidence must be absolute")
    if not Path(args.docker_socket).is_socket():
        parser.error("--docker-socket must name a Unix socket")
    image_id = image_identity(args.image)
    args.evidence.parent.mkdir(parents=True, exist_ok=True)
    root = Path(tempfile.mkdtemp(prefix="scheduled-digest-image-"))
    container = "scheduled-digest-exact-" + hashlib.sha256(
        os.urandom(32)
    ).hexdigest()[:16]
    report = {
        "format": "portal.scheduled-digest-exact-image/1-dev.1",
        "passed": False,
        "image": args.image,
        "image_id": image_id,
    }
    try:
        http_dir = root / "http"
        broker_dir = root / "broker"
        data_dir = root / "data"
        for folder in (http_dir, broker_dir, data_dir):
            folder.mkdir(mode=0o700)
            os.chown(folder, 65532, 65532)
        (broker_dir / "registration.json").write_text(
            json.dumps(registration(), separators=(",", ":"))
        )
        os.chown(broker_dir / "registration.json", 65532, 65532)
        os.chmod(broker_dir / "registration.json", 0o400)
        environment = {
            **os.environ,
            "DOCKER_HOST": "unix://" + args.docker_socket,
        }
        run(
            [
                "docker",
                "run",
                "--detach",
                "--name",
                container,
                "--label",
                "org.portal.fixture=scheduled-digest-exact",
                "--network",
                "none",
                "--read-only",
                "--user",
                "65532:65532",
                "--cap-drop",
                "ALL",
                "--security-opt",
                "no-new-privileges",
                "--memory",
                "128m",
                "--pids-limit",
                "32",
                "--cpus",
                "0.25",
                "--tmpfs",
                "/tmp:rw,noexec,nosuid,nodev,size=1m",
                "--mount",
                f"type=bind,src={http_dir},dst=/run/portal-app/http",
                "--mount",
                f"type=bind,src={broker_dir},dst=/run/portal-app/broker,readonly",
                "--mount",
                f"type=bind,src={data_dir},dst=/data",
                args.image,
            ],
            env=environment,
        )
        socket_path = http_dir / "app.sock"
        status, raw = request(socket_path, "GET", "/health/ready")
        health = json.loads(raw)
        if status != 200 or health != {
            "protocol": "portal.backend/1",
            "ready": True,
            "data_format": "1",
        }:
            raise ValueError("exact image readiness failed")
        challenge = handshake_challenge()
        handshake_status, handshake_raw = request(
            socket_path,
            "POST",
            "/v1/handshake",
            json.dumps({"challenge": challenge}, separators=(",", ":")).encode(),
            {"Content-Type": "application/json"},
        )
        if handshake_status != 200:
            raise ValueError("exact image registration handshake failed")
        boot_id = verify_handshake_answer(
            challenge, json.loads(handshake_raw), now=int(time.time())
        )
        identity = json.dumps(service_identity(), separators=(",", ":"))
        headers = {
            "Content-Type": "application/json",
            "X-Portal-Service-Identity": identity,
        }
        task_id = "10000000-0000-4000-8000-000000000004"
        occurrence = "digest:exact-image:1"
        first_status, first_raw = request(
            socket_path,
            "POST",
            "/v1/tasks/digest",
            task_body(task_id, occurrence, [41, 42]),
            headers,
        )
        first = decode_response(first_raw)
        replay_status, replay_raw = request(
            socket_path,
            "POST",
            "/v1/tasks/digest",
            task_body(task_id, occurrence, [41, 42]),
            headers,
        )
        replay = decode_response(replay_raw)
        changed_status, changed_raw = request(
            socket_path,
            "POST",
            "/v1/tasks/digest",
            task_body(task_id, occurrence, [41]),
            headers,
        )
        changed = decode_response(changed_raw)
        if (
            first_status != 200
            or replay_status != 200
            or changed_status != 200
            or first.get("outcome") != "succeeded"
            or replay.get("outcome") != "succeeded"
            or first.get("result", {}).get("duplicate") is not False
            or replay.get("result", {}).get("duplicate") is not True
            or changed.get("outcome") != "failed"
            or changed.get("error_code") != "digest_identity_conflict"
        ):
            raise ValueError("exact image fixed task handler failed")
        digest_status, digest_raw = request(socket_path, "GET", "/v1/digest")
        state = json.loads(digest_raw)
        if (
            digest_status != 200
            or state.get("format") != "scheduled-digest.state/1-dev.1"
            or state.get("scheduleId") != "daily"
            or state.get("lastRun", {}).get("occurrenceId") != occurrence
        ):
            raise ValueError("exact image persisted digest state failed")
        if (socket_path.stat().st_mode & 0o777) != 0o600:
            raise ValueError("exact image socket mode changed")
        report.update(
            {
                "passed": True,
                "health": health,
                "handshake_boot_sha256": hashlib.sha256(
                    boot_id.encode("ascii")
                ).hexdigest(),
                "task_occurrence": occurrence,
                "first_outcome": first.get("outcome"),
                "replay_duplicate": replay.get("result", {}).get("duplicate"),
                "changed_identity_refused": changed.get("error_code")
                == "digest_identity_conflict",
                "persisted_state": True,
                "socket_mode": "0600",
                "network": "none",
                "user": "65532:65532",
                "read_only_rootfs": True,
                "cap_drop": "ALL",
                "memory_mib": 128,
                "pids": 32,
                "cpu": "0.25",
            }
        )
        print("SCHEDULED_DIGEST_EXACT_IMAGE_VERIFIED")
    except Exception as error:
        report["error"] = str(error)
        try:
            report["logs"] = run(["docker", "logs", container], env={
                **os.environ,
                "DOCKER_HOST": "unix://" + args.docker_socket,
            })[-16384:]
        except Exception:
            pass
        raise
    finally:
        try:
            run(
                ["docker", "container", "rm", "--force", container],
                env={**os.environ, "DOCKER_HOST": "unix://" + args.docker_socket},
            )
        except Exception:
            pass
        shutil.rmtree(root, ignore_errors=True)
        args.evidence.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
